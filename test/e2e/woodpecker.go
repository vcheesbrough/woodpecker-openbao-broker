//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	gitea "code.gitea.io/sdk/gitea"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	woodpeckerHTTPPort = "8000"
	woodpeckerGRPCPort = "9000"
	woodpeckerOAuthApp = "e2e-harness-woodpecker"
)

type woodpeckerState struct {
	server          testcontainers.Container
	agent           testcontainers.Container
	internalHTTPURL string
	hostHTTPURL     string

	oauthClientID     string
	oauthClientSecret string
	agentSecret       string

	// sessionClient carries the user_sess cookie minted during OAuth
	// bootstrap. Use this for any authenticated Woodpecker API call from
	// the harness.
	sessionClient *http.Client
	// csrfToken is the JWT Woodpecker expects in X-CSRF-TOKEN on every
	// non-GET API request when authenticated by session cookie. Fetched
	// from /web-config.js after OAuth bootstrap.
	csrfToken string
}

func (h *Harness) startWoodpecker(ctx context.Context) error {
	if h.gitea == nil {
		return errors.New("gitea must be running before woodpecker")
	}

	clientID, clientSecret, err := registerWoodpeckerOAuthApp(h)
	if err != nil {
		return fmt.Errorf("register oauth app: %w", err)
	}
	h.ledger.Add(NewFuncResource("gitea oauth2 app "+woodpeckerOAuthApp, func(ctx context.Context) error {
		return deleteOAuthAppByName(h.gitea.client, woodpeckerOAuthApp)
	}))

	agentSecret := randomToken(24)

	server, internalHTTPURL, hostHTTPURL, err := startWoodpeckerServer(ctx, h, clientID, clientSecret, agentSecret)
	if server != nil {
		h.ledger.Add(NewFuncResource("woodpecker-server container", func(ctx context.Context) error {
			return server.Terminate(ctx)
		}))
	}
	if err != nil {
		return fmt.Errorf("start woodpecker server: %w", err)
	}

	agent, err := startWoodpeckerAgent(ctx, h, agentSecret)
	if agent != nil {
		h.ledger.Add(NewFuncResource("woodpecker-agent container", func(ctx context.Context) error {
			return agent.Terminate(ctx)
		}))
	}
	if err != nil {
		return fmt.Errorf("start woodpecker agent: %w", err)
	}

	h.woodpecker = &woodpeckerState{
		server:            server,
		agent:             agent,
		internalHTTPURL:   internalHTTPURL,
		hostHTTPURL:       hostHTTPURL,
		oauthClientID:     clientID,
		oauthClientSecret: clientSecret,
		agentSecret:       agentSecret,
	}
	return nil
}

func registerWoodpeckerOAuthApp(h *Harness) (string, string, error) {
	redirect := fmt.Sprintf("http://%s:%s/authorize", wpServerNetAlias, woodpeckerHTTPPort)
	app, _, err := h.gitea.client.CreateOauth2(gitea.CreateOauth2Option{
		Name:               woodpeckerOAuthApp,
		ConfidentialClient: true,
		RedirectURIs:       []string{redirect},
	})
	if err != nil {
		return "", "", err
	}
	if app == nil || app.ClientID == "" || app.ClientSecret == "" {
		return "", "", errors.New("empty oauth credentials returned")
	}
	return app.ClientID, app.ClientSecret, nil
}

func deleteOAuthAppByName(client *gitea.Client, name string) error {
	apps, _, err := client.ListOauth2(gitea.ListOauth2Option{})
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Name == name {
			if _, err := client.DeleteOauth2(a.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func startWoodpeckerServer(ctx context.Context, h *Harness, clientID, clientSecret, agentSecret string) (testcontainers.Container, string, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        h.cfg.WoodpeckerImage,
		Name:         "e2e-woodpecker-server-" + h.runID,
		Networks:     []string{h.networkName},
		NetworkAliases: map[string][]string{h.networkName: {wpServerNetAlias}},
		ExposedPorts: []string{woodpeckerHTTPPort + "/tcp", woodpeckerGRPCPort + "/tcp"},
		Env: map[string]string{
			"WOODPECKER_OPEN":                     "true",
			"WOODPECKER_HOST":                     fmt.Sprintf("http://%s:%s", wpServerNetAlias, woodpeckerHTTPPort),
			"WOODPECKER_GITEA":                    "true",
			"WOODPECKER_GITEA_URL":                h.gitea.internalURL,
			"WOODPECKER_GITEA_CLIENT":             clientID,
			"WOODPECKER_GITEA_SECRET":             clientSecret,
			"WOODPECKER_AGENT_SECRET":             agentSecret,
			"WOODPECKER_ADMIN":                    giteaAdminUser,
			"WOODPECKER_LOG_LEVEL":                "info",
			"WOODPECKER_DATABASE_DRIVER":          "sqlite3",
			"WOODPECKER_SECRET_EXTENSION_ENDPOINT": fmt.Sprintf("http://%s:%s/secrets", brokerNetAlias, brokerHTTPPort),
			"WOODPECKER_EXTENSIONS_ALLOWED_HOSTS":  brokerNetAlias,
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(woodpeckerHTTPPort+"/tcp"),
			wait.ForLog("starting http server"),
		).WithStartupTimeoutDefault(120 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return c, "", "", err
	}

	hostHTTPURL, err := containerHTTPEndpoint(ctx, c, woodpeckerHTTPPort)
	if err != nil {
		return c, "", "", err
	}
	internalHTTPURL := fmt.Sprintf("http://%s:%s", wpServerNetAlias, woodpeckerHTTPPort)
	return c, internalHTTPURL, hostHTTPURL, nil
}

func startWoodpeckerAgent(ctx context.Context, h *Harness, agentSecret string) (testcontainers.Container, error) {
	req := testcontainers.ContainerRequest{
		Image:        h.cfg.AgentImage,
		Name:         "e2e-woodpecker-agent-" + h.runID,
		Networks:     []string{h.networkName},
		NetworkAliases: map[string][]string{h.networkName: {wpAgentNetAlias}},
		Env: map[string]string{
			"WOODPECKER_SERVER":                fmt.Sprintf("%s:%s", wpServerNetAlias, woodpeckerGRPCPort),
			"WOODPECKER_AGENT_SECRET":          agentSecret,
			"WOODPECKER_BACKEND":               "docker",
			"WOODPECKER_BACKEND_DOCKER_NETWORK": h.networkName,
			"WOODPECKER_LOG_LEVEL":             "info",
		},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, "/var/run/docker.sock:/var/run/docker.sock")
		},
		WaitingFor: wait.ForLog("starting").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	return c, err
}

// WoodpeckerHostURL is the host-reachable Woodpecker URL (for harness-side
// HTTP calls like /healthz).
func (h *Harness) WoodpeckerHostURL() string { return h.woodpecker.hostHTTPURL }

// WoodpeckerInternalURL is the docker-network URL — use it as the base
// for any HTTP request made via WoodpeckerSession. Aliases there are
// resolved by the rewriting client's DialContext.
func (h *Harness) WoodpeckerInternalURL() string { return h.woodpecker.internalHTTPURL }

// WoodpeckerSession is the OAuth-bootstrapped HTTP client for making
// authenticated calls against the Woodpecker API. nil until
// bootstrapWoodpeckerOAuth has run.
func (h *Harness) WoodpeckerSession() *http.Client { return h.woodpecker.sessionClient }

// WoodpeckerCSRFToken returns the JWT to set as X-CSRF-TOKEN on
// non-GET Woodpecker API requests. Empty until OAuth bootstrap.
func (h *Harness) WoodpeckerCSRFToken() string { return h.woodpecker.csrfToken }

func (h *Harness) dumpWoodpeckerServerLog(ctx context.Context) string {
	if h.woodpecker == nil || h.woodpecker.server == nil {
		return "(no woodpecker)"
	}
	rc, err := h.woodpecker.server.Logs(ctx)
	if err != nil {
		return fmt.Sprintf("(logs: %v)", err)
	}
	defer func() { _ = rc.Close() }()
	body, _ := io.ReadAll(io.LimitReader(rc, 64<<10))
	// Tail the last 4K — earliest startup chatter is rarely useful.
	if len(body) > 4096 {
		body = body[len(body)-4096:]
	}
	return string(body)
}

func randomToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	mod := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, _ := rand.Int(rand.Reader, mod)
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}
