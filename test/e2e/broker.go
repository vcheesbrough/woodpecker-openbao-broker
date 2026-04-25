//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	brokerHTTPPort    = "8080"
	brokerPubkeyMount = "/etc/woodpecker/pubkey.pem"
)

type brokerState struct {
	container        testcontainers.Container
	internalHTTPURL  string
	hostHTTPURL      string
	pubkeyPEM        []byte
	imageTag         string // resolved at first bringup; reused on restart
	templates        string
	ledgerRegistered bool
}

// startBroker brings up the broker-under-test for the first time, wired
// to OpenBao with the AppRole minted in startOpenBao and to Woodpecker
// via the public key fetched after OAuth bootstrap. Subsequent calls
// during a run should use RestartBroker to swap SECRET_PATH_TEMPLATES.
func (h *Harness) startBroker(ctx context.Context) error {
	if h.bao == nil {
		return errors.New("openbao must be running before broker")
	}
	if h.woodpecker == nil || h.woodpecker.sessionClient == nil {
		return errors.New("woodpecker oauth bootstrap must run before broker")
	}

	pubkeyPEM, err := fetchWoodpeckerPubkey(ctx, h.woodpecker.sessionClient, h.woodpecker.internalHTTPURL)
	if err != nil {
		return fmt.Errorf("fetch woodpecker pubkey: %w", err)
	}

	imageTag := h.cfg.BrokerImage
	if imageTag == "" {
		// The repo's Dockerfile uses BuildKit-only directives — see
		// reference_e2e_buildx_required memory. Preflight-build via
		// `docker buildx build` on the host so testcontainers just pulls
		// the resulting tag.
		imageTag, err = buildBrokerImageOnHost(ctx, h.runID)
		if err != nil {
			return fmt.Errorf("preflight build broker image: %w", err)
		}
	}

	h.broker = &brokerState{
		pubkeyPEM: pubkeyPEM,
		imageTag:  imageTag,
	}
	return h.spawnBroker(ctx, "")
}

// RestartBroker terminates the running broker container and starts a new
// one with the given SECRET_PATH_TEMPLATES. Used by the scenario driver
// to switch broker config between scenario groups.
func (h *Harness) RestartBroker(ctx context.Context, templates string) error {
	if h.broker == nil {
		return errors.New("broker has not been started yet")
	}
	if h.broker.templates == templates && h.broker.container != nil {
		return nil
	}
	if h.broker.container != nil {
		if err := h.broker.container.Terminate(ctx); err != nil {
			return fmt.Errorf("terminate previous broker: %w", err)
		}
		h.broker.container = nil
	}
	return h.spawnBroker(ctx, templates)
}

func (h *Harness) spawnBroker(ctx context.Context, templates string) error {
	req := testcontainers.ContainerRequest{
		Image:          h.broker.imageTag,
		Name:           fmt.Sprintf("e2e-broker-%s-%d", h.runID, time.Now().UnixNano()),
		Networks:       []string{h.networkName},
		NetworkAliases: map[string][]string{h.networkName: {brokerNetAlias}},
		ExposedPorts:   []string{brokerHTTPPort + "/tcp"},
		Env: map[string]string{
			"WOODPECKER_PUBLIC_KEY_FILE": brokerPubkeyMount,
			"OPENBAO_ADDR":               h.bao.internalAddr,
			"OPENBAO_ROLE_ID":            h.bao.roleID,
			"OPENBAO_SECRET_ID":          h.bao.secretID,
			"OPENBAO_KV_MOUNT":           openBaoKVMount,
			"SECRET_PATH_TEMPLATES":      templates,
			"LISTEN_ADDR":                ":" + brokerHTTPPort,
		},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            bytes.NewReader(h.broker.pubkeyPEM),
				ContainerFilePath: brokerPubkeyMount,
				FileMode:          0o444,
			},
		},
		WaitingFor: wait.ForHTTP("/health").
			WithPort(brokerHTTPPort + "/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		// On wait-timeout testcontainers leaves the container running; if
		// we can still get a handle, kill it so the network teardown
		// doesn't trip on a dangling endpoint.
		if c != nil {
			_ = c.Terminate(ctx)
		}
		return err
	}

	hostHTTPURL, err := containerHTTPEndpoint(ctx, c, brokerHTTPPort)
	if err != nil {
		return err
	}
	h.broker.container = c
	h.broker.internalHTTPURL = fmt.Sprintf("http://%s:%s", brokerNetAlias, brokerHTTPPort)
	h.broker.hostHTTPURL = hostHTTPURL
	h.broker.templates = templates
	if !h.broker.ledgerRegistered {
		// One ledger entry that always points at the *current* broker
		// container — restarts swap the pointer, so cleanup terminates
		// only what's actually still running.
		h.ledger.Add(NewFuncResource("broker container", func(ctx context.Context) error {
			if h.broker.container == nil {
				return nil
			}
			return h.broker.container.Terminate(ctx)
		}))
		h.broker.ledgerRegistered = true
	}
	return nil
}

func buildBrokerImageOnHost(ctx context.Context, runID string) (string, error) {
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	tag := "e2e-broker:" + runID

	cmd := exec.CommandContext(ctx, "docker", "buildx", "build", "--platform=linux/amd64", "--load", "--pull=false", "-t", tag, repoRoot)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return tag, nil
}

// fetchWoodpeckerPubkey retrieves the ed25519 public key Woodpecker uses
// to sign secret-extension requests. The endpoint is unauthenticated in
// v3.14 but we hit it via the bootstrapped session client anyway so the
// path stays consistent if the auth requirements ever tighten.
func fetchWoodpeckerPubkey(ctx context.Context, client *http.Client, wpBase string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wpBase+"/api/signature/public-key", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, snippet(body))
	}
	if !strings.Contains(string(body), "BEGIN PUBLIC KEY") {
		return nil, fmt.Errorf("response does not contain a PEM public key: %s", snippet(body))
	}
	return body, nil
}

// BrokerHostURL is the host-reachable broker URL — useful for harness-side
// /health checks.
func (h *Harness) BrokerHostURL() string { return h.broker.hostHTTPURL }

func (h *Harness) dumpBrokerLog(ctx context.Context) string {
	if h.broker == nil || h.broker.container == nil {
		return "(no broker)"
	}
	rc, err := h.broker.container.Logs(ctx)
	if err != nil {
		return fmt.Sprintf("(logs: %v)", err)
	}
	defer func() { _ = rc.Close() }()
	body, _ := io.ReadAll(io.LimitReader(rc, 32<<10))
	return string(body)
}
