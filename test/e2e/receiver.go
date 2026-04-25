//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type receiverState struct {
	container testcontainers.Container
	hostURL   string
}

func (h *Harness) startReceiver(ctx context.Context) error {
	_, file, _, _ := runtime.Caller(0)
	receiverDir := filepath.Join(filepath.Dir(file), "receiver")

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    receiverDir,
			Dockerfile: "Dockerfile",
		},
		Name:         "e2e-receiver-" + h.runID,
		ExposedPorts: []string{receiverPort + "/tcp"},
		Networks:     []string{h.networkName},
		NetworkAliases: map[string][]string{
			h.networkName: {receiverNetAlias},
		},
		WaitingFor: wait.ForHTTP("/healthz").
			WithPort(receiverPort + "/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return fmt.Errorf("start receiver: %w", err)
	}

	hostURL, err := containerHTTPEndpoint(ctx, c, receiverPort)
	if err != nil {
		_ = c.Terminate(ctx)
		return err
	}

	h.receiver = &receiverState{container: c, hostURL: hostURL}
	h.ledger.Add(NewFuncResource("receiver container", func(ctx context.Context) error {
		return c.Terminate(ctx)
	}))
	return nil
}

func (h *Harness) ReceiverPoll(ctx context.Context, scenarioID string) (map[string]string, error) {
	url := h.receiver.hostURL + "/scenarios/" + scenarioID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("receiver poll status %d: %s", resp.StatusCode, string(body))
	}
	var out map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func containerHTTPEndpoint(ctx context.Context, c testcontainers.Container, port string) (string, error) {
	host, err := c.Host(ctx)
	if err != nil {
		return "", err
	}
	mapped, err := c.MappedPort(ctx, port+"/tcp")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("http://%s:%s", host, mapped.Port()), nil
}
