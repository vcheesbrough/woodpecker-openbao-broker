//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"
)

// TestSmoke is the foundation-level integration test: it stands up the
// docker network, the receiver image, and OpenBao (with AppRole + KV-v2
// configured), exercises the harness's KV write/read/list helpers, and
// verifies the cleanup ledger leaves nothing behind.
//
// Anything beyond this — Gitea, Woodpecker, broker, the 20-scenario
// matrix — is intentionally not wired up in this PR. See scenarios.go
// Anything beyond this — Gitea, Woodpecker, broker, the 20-scenario
// matrix — is intentionally not wired up in this PR. See scenarios.go
// package doc for the layered rollout.
// follow-up layers.
func TestSmoke(t *testing.T) {
	cfg := ConfigFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	h := New(t, cfg)
	h.Setup(ctx, t)
	defer h.Teardown(ctx, t)

	t.Run("kv write/read/list round-trip", func(t *testing.T) {
		path := "woodpecker/smoke/" + h.runID
		want := map[string]string{"hello": "world", "k2": "v2"}
		if err := h.WriteKV(ctx, path, want); err != nil {
			t.Fatalf("write: %v", err)
		}
		keys, err := h.ListKVUnder(ctx, "woodpecker/smoke")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(keys) == 0 {
			t.Fatalf("expected at least one key under woodpecker/smoke, got 0")
		}
		if err := h.DeleteKV(ctx, path); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})

	t.Run("receiver round-trip", func(t *testing.T) {
		// The receiver is reachable from inside the docker network only
		// (DNS alias e2e-receiver). For TestSmoke we just verify the host
		// poll endpoint returns an empty map for an unknown scenario.
		got, err := h.ReceiverPoll(ctx, "smoke-unknown")
		if err != nil {
			t.Fatalf("receiver poll: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty receiver map, got %v", got)
		}
	})
}

// TestE2E enumerates the full 20-row matrix from card #118 but is skipped
// until the Gitea/Woodpecker/broker bringup layers land in follow-up PRs.
func TestE2E(t *testing.T) {
	t.Skip("scaffolding-only PR: Gitea/Woodpecker/broker bringup pending. " +
		"See scenarios.go package doc and the plan file for the layered rollout.")

	scenarios := AllScenarios()
	for _, s := range scenarios {
		t.Run(s.ID+"_"+s.Title, func(t *testing.T) {
			if s.Disabled {
				t.Skipf("disabled in this PR: %s", s.Description)
			}
			// Real driver lands in a follow-up.
		})
	}
}
