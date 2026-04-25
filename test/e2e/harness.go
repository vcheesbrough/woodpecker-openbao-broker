//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
)

const (
	receiverNetAlias  = "e2e-receiver"
	openbaoNetAlias   = "openbao"
	giteaNetAlias     = "gitea"
	wpServerNetAlias  = "woodpecker-server"
	wpAgentNetAlias   = "woodpecker-agent"
	brokerNetAlias    = "broker"
	receiverPort      = "9000"

	defaultWoodpeckerImage = "woodpeckerci/woodpecker-server:v3.14.0-rc.1"
	defaultAgentImage      = "woodpeckerci/woodpecker-agent:v3.14.0-rc.1"
	defaultOpenBaoImage    = "openbao/openbao:2.5.3"
	defaultGiteaImage      = "gitea/gitea:1.22"
)

type Config struct {
	BrokerImage     string
	WoodpeckerImage string
	AgentImage      string
	OpenBaoImage    string
	GiteaImage      string
}

func ConfigFromEnv() Config {
	return Config{
		BrokerImage:     os.Getenv("BROKER_IMAGE"),
		WoodpeckerImage: envOr("WOODPECKER_IMAGE", defaultWoodpeckerImage),
		AgentImage:      envOr("WOODPECKER_AGENT_IMAGE", defaultAgentImage),
		OpenBaoImage:    envOr("OPENBAO_IMAGE", defaultOpenBaoImage),
		GiteaImage:      envOr("GITEA_IMAGE", defaultGiteaImage),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type Harness struct {
	cfg Config

	runID       string
	artifactDir string
	ledger      *Ledger
	report      *Report

	network     *testcontainers.DockerNetwork
	networkName string

	bao        *baoState
	gitea      *giteaState
	woodpecker *woodpeckerState
	receiver   *receiverState
}

func New(t *testing.T, cfg Config) *Harness {
	t.Helper()
	runID := newRunID()
	artifactDir, err := filepath.Abs(filepath.Join("_artifacts", runID))
	if err != nil {
		t.Fatalf("artifact dir: %v", err)
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	return &Harness{
		cfg:         cfg,
		runID:       runID,
		artifactDir: artifactDir,
		ledger:      NewLedger(),
		report:      NewReport(runID),
	}
}

func (h *Harness) ArtifactDir() string { return h.artifactDir }

func (h *Harness) Setup(ctx context.Context, t *testing.T) {
	t.Helper()

	t.Logf("e2e: run %s, artifact dir %s", h.runID, h.artifactDir)

	net, err := network.New(ctx)
	if err != nil {
		t.Fatalf("create docker network: %v", err)
	}
	h.network = net
	h.networkName = net.Name
	h.ledger.Add(NewFuncResource(fmt.Sprintf("network %s", net.Name), func(ctx context.Context) error {
		return net.Remove(ctx)
	}))
	t.Logf("e2e: network %s", net.Name)

	if err := h.startReceiver(ctx); err != nil {
		t.Fatalf("receiver: %v", err)
	}
	if err := h.startOpenBao(ctx); err != nil {
		t.Fatalf("openbao: %v", err)
	}
	if err := h.startGitea(ctx); err != nil {
		t.Fatalf("gitea: %v", err)
	}
	if err := h.startWoodpecker(ctx); err != nil {
		t.Fatalf("woodpecker: %v", err)
	}
	if err := h.bootstrapWoodpeckerOAuth(ctx); err != nil {
		t.Fatalf("woodpecker oauth bootstrap: %v", err)
	}
	// Broker bringup, repo registration, and the scenario driver remain
	// in follow-up PRs — see the package doc comment in scenarios.go.
}

func (h *Harness) Teardown(ctx context.Context, t *testing.T) {
	t.Helper()

	if errs := h.ledger.Cleanup(ctx); len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("cleanup error: %v", e)
		}
	}
	if err := h.ledger.Verify(ctx); err != nil {
		t.Errorf("cleanup verification: %v", err)
	}

	if err := h.report.WriteFiles(h.artifactDir); err != nil {
		t.Errorf("write report: %v", err)
	}
	t.Logf("e2e: report written to %s", h.artifactDir)
}

func newRunID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
