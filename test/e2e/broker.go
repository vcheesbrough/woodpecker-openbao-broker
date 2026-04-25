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
	brokerHTTPPort     = "8080"
	brokerPubkeyMount  = "/etc/woodpecker/pubkey.pem"
)

type brokerState struct {
	container       testcontainers.Container
	internalHTTPURL string
	hostHTTPURL     string
}

// startBroker brings up the broker-under-test, wired to OpenBao with the
// AppRole minted in startOpenBao and to Woodpecker via the public key
// fetched after OAuth bootstrap. The Woodpecker server has already been
// started with WOODPECKER_SECRET_EXTENSION_ENDPOINT pointing at the
// broker DNS alias, so once the broker is up secret resolution flows
// end-to-end.
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

	req := testcontainers.ContainerRequest{
		Name:           "e2e-broker-" + h.runID,
		Networks:       []string{h.networkName},
		NetworkAliases: map[string][]string{h.networkName: {brokerNetAlias}},
		ExposedPorts:   []string{brokerHTTPPort + "/tcp"},
		Env: map[string]string{
			"WOODPECKER_PUBLIC_KEY_FILE": brokerPubkeyMount,
			"OPENBAO_ADDR":               h.bao.internalAddr,
			"OPENBAO_ROLE_ID":            h.bao.roleID,
			"OPENBAO_SECRET_ID":          h.bao.secretID,
			"OPENBAO_KV_MOUNT":           openBaoKVMount,
			"SECRET_PATH_TEMPLATES":      "",
			"LISTEN_ADDR":                ":" + brokerHTTPPort,
		},
		Files: []testcontainers.ContainerFile{
			{
				Reader:            bytes.NewReader(pubkeyPEM),
				ContainerFilePath: brokerPubkeyMount,
				FileMode:          0o444,
			},
		},
		WaitingFor: wait.ForHTTP("/health").
			WithPort(brokerHTTPPort + "/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	if h.cfg.BrokerImage != "" {
		req.Image = h.cfg.BrokerImage
	} else {
		// The repo's Dockerfile uses BuildKit-only directives
		// (`# syntax=docker/dockerfile:1`, `--platform=$BUILDPLATFORM`)
		// that the docker engine's `/build` endpoint can't parse cleanly
		// when invoked via the moby client (BUILDPLATFORM resolves to
		// empty and BuildKit's frontend rejects it). Preflight-build the
		// image with the host's `docker build` command (which behaves
		// the same way the project's CI does) and point testcontainers
		// at the resulting tag.
		tag, err := buildBrokerImageOnHost(ctx, h.runID)
		if err != nil {
			return fmt.Errorf("preflight build broker image: %w", err)
		}
		req.Image = tag
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if c != nil {
		h.ledger.Add(NewFuncResource("broker container", func(ctx context.Context) error {
			return c.Terminate(ctx)
		}))
	}
	if err != nil {
		return err
	}

	hostHTTPURL, err := containerHTTPEndpoint(ctx, c, brokerHTTPPort)
	if err != nil {
		return err
	}
	h.broker = &brokerState{
		container:       c,
		internalHTTPURL: fmt.Sprintf("http://%s:%s", brokerNetAlias, brokerHTTPPort),
		hostHTTPURL:     hostHTTPURL,
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
