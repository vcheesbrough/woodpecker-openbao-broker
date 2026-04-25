package bao_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	vault "github.com/hashicorp/vault/api"

	"github.com/vcheesbrough/woodpecker-openbao-broker/internal/bao"
)

// These tests run against a real OpenBao (or Vault) instance. CI provides one
// as a service container at $BAO_ADDR with $BAO_TOKEN. Locally, run e.g.
// `bao server -dev -dev-root-token-id=root` and export those vars.
func setupClient(t *testing.T) (*bao.Client, *vault.Client) {
	t.Helper()
	addr := os.Getenv("BAO_ADDR")
	rootToken := os.Getenv("BAO_TOKEN")
	if addr == "" || rootToken == "" {
		t.Skip("set BAO_ADDR and BAO_TOKEN to run integration tests")
	}
	mount := os.Getenv("BAO_KV_MOUNT")
	if mount == "" {
		mount = "secret"
	}

	c, err := bao.New(addr, "", mount)
	if err != nil {
		t.Fatalf("bao.New: %v", err)
	}
	c.SetToken(rootToken)

	cfg := vault.DefaultConfig()
	cfg.Address = addr
	cfg.Timeout = 5 * time.Second
	api, err := vault.NewClient(cfg)
	if err != nil {
		t.Fatalf("vault.NewClient: %v", err)
	}
	api.SetToken(rootToken)

	// Ping — fail fast with a clearer message than later test errors.
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(addr + "/v1/sys/health")
	if err != nil {
		t.Skipf("OpenBao not reachable at %s: %v", addr, err)
	}
	_ = resp.Body.Close()

	return c, api
}

func writeKVv2(t *testing.T, api *vault.Client, mount, path string, kv map[string]any) {
	t.Helper()
	if mount == "" {
		mount = "secret"
	}
	if _, err := api.KVv2(mount).Put(context.Background(), path, kv); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func TestClient_ReadKVv2_HitAndMiss(t *testing.T) {
	c, api := setupClient(t)
	mount := os.Getenv("BAO_KV_MOUNT")
	writeKVv2(t, api, mount, "woodpecker-broker-tests/hit", map[string]any{
		"alpha": "one",
		"beta":  "two",
	})

	got, err := c.ReadKVv2(context.Background(), "woodpecker-broker-tests/hit")
	if err != nil {
		t.Fatalf("read hit: %v", err)
	}
	if got["alpha"] != "one" || got["beta"] != "two" {
		t.Errorf("read hit: got %v", got)
	}

	miss, err := c.ReadKVv2(context.Background(), "woodpecker-broker-tests/definitely-not-here")
	if err != nil {
		t.Fatalf("read miss: %v", err)
	}
	if miss != nil {
		t.Errorf("read miss: want nil map, got %v", miss)
	}
}
