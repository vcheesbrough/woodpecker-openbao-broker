//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSmoke is the foundation-level integration test: it stands up the
// docker network, the receiver image, OpenBao (with AppRole + KV-v2
// configured), and Gitea (with admin user + token + e2e/broker-target
// repo provisioned), exercises the harness's KV and Gitea helpers, and
// verifies the cleanup ledger leaves nothing behind.
//
// Anything beyond this — Woodpecker, broker, the 20-scenario matrix —
// is intentionally not wired up in this PR. See scenarios.go package
// doc and the plan file for the layered rollout.
func TestSmoke(t *testing.T) {
	cfg := ConfigFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	h := New(t, cfg)
	defer h.Teardown(ctx, t) // registered before Setup so partial bringup still cleans up
	h.Setup(ctx, t)

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
		got, err := h.ReceiverPoll(ctx, "smoke-unknown")
		if err != nil {
			t.Fatalf("receiver poll: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("expected empty receiver map, got %v", got)
		}
	})

	t.Run("gitea commit + read on main", func(t *testing.T) {
		const path = ".woodpecker.yaml"
		const want = "# placeholder pipeline\n"
		if _, err := h.CommitFile("main", path, want, "smoke: seed pipeline"); err != nil {
			t.Fatalf("commit: %v", err)
		}
		got, err := h.ReadFile("main", path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != want {
			t.Fatalf("file contents mismatch: got %q, want %q", got, want)
		}
	})

	t.Run("gitea branch + tag + PR", func(t *testing.T) {
		const branch = "smoke-feature"
		if err := h.CreateBranch("main", branch); err != nil {
			t.Fatalf("create branch: %v", err)
		}
		if _, err := h.CommitFile(branch, "feature.txt", "hello\n", "smoke: feature commit"); err != nil {
			t.Fatalf("commit on branch: %v", err)
		}
		if err := h.CreateTag("v0.0.0-smoke", "main"); err != nil {
			t.Fatalf("create tag: %v", err)
		}
		idx, err := h.OpenPullRequest(branch, "main", "smoke PR")
		if err != nil {
			t.Fatalf("open PR: %v", err)
		}
		if idx == 0 {
			t.Fatalf("expected non-zero PR index")
		}
	})

	t.Run("woodpecker server healthz", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.WoodpeckerHostURL()+"/healthz", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("woodpecker healthz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("woodpecker healthz status %d", resp.StatusCode)
		}
	})

	t.Run("woodpecker session authenticates as admin", func(t *testing.T) {
		client := h.WoodpeckerSession()
		if client == nil {
			t.Fatal("expected non-nil woodpecker session client")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.WoodpeckerInternalURL()+"/api/user", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("get /api/user: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("/api/user status %d body=%s", resp.StatusCode, string(body))
		}
		var u struct {
			Login string `json:"login"`
			Admin bool   `json:"admin"`
		}
		if err := json.Unmarshal(body, &u); err != nil {
			t.Fatalf("decode /api/user: %v body=%s", err, string(body))
		}
		if u.Login != h.GiteaAdminUser() {
			t.Fatalf("expected login %q, got %q", h.GiteaAdminUser(), u.Login)
		}
		if !u.Admin {
			t.Fatal("expected admin=true on bootstrapped user")
		}
	})

	t.Run("broker health", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.BrokerHostURL()+"/health", nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("broker health: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("broker health status %d", resp.StatusCode)
		}
	})

	t.Run("register repo and run a manual pipeline", func(t *testing.T) {
		const yaml = "steps:\n  - name: hello\n    image: alpine:3\n    commands:\n      - echo hello-from-e2e\n"
		if _, err := h.CommitFile("main", ".woodpecker.yaml", yaml, "smoke: trivial pipeline"); err != nil {
			t.Fatalf("commit pipeline yaml: %v", err)
		}

		repoID, err := h.RegisterRepoWithWoodpecker(ctx)
		if err != nil {
			t.Fatalf("register repo: %v", err)
		}
		t.Logf("woodpecker repo id: %d", repoID)

		number, err := h.TriggerPipeline(ctx, repoID, "main")
		if err != nil {
			t.Fatalf("trigger pipeline: %v", err)
		}
		t.Logf("triggered pipeline number: %d", number)

		status, err := h.PollPipeline(ctx, repoID, number, 3*time.Minute)
		if err != nil {
			t.Fatalf("poll pipeline: %v (last status %s)", err, status)
		}
		if status != pipelineStatusSuccess {
			t.Fatalf("pipeline ended with status %q, want success", status)
		}
	})
}

// TestE2E walks the 20-row scenario matrix from card #118. Scenarios
// flagged Disabled are skipped — see scenarios.go for the rollout plan
// that gradually un-flags rows as later layers wire up the trigger
// types they need.
func TestE2E(t *testing.T) {
	cfg := ConfigFromEnv()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	h := New(t, cfg)
	defer h.Teardown(ctx, t)
	h.Setup(ctx, t)

	repoID, err := h.RegisterRepoWithWoodpecker(ctx)
	if err != nil {
		t.Fatalf("register repo: %v", err)
	}

	for _, s := range AllScenarios() {
		t.Run(s.ID+"_"+s.Title, func(t *testing.T) {
			if s.Disabled {
				t.Skipf("disabled (rollout pending): %s", s.Description)
			}
			// TriggerPush and TriggerManual both use TriggerPipeline (POST
			// /api/repos/{id}/pipelines). That API creates a manual-event
			// pipeline regardless — event-type-specific scenarios (s08, s09,
			// s10) need real webhook triggering and stay disabled until then.
			if s.Trigger != TriggerPush && s.Trigger != TriggerManual {
				t.Skipf("trigger %q not yet implemented in driver", s.Trigger)
			}
			sep := "\n"
			if s.JoinWith != "" {
				sep = s.JoinWith
			}
			templates := strings.Join(s.Templates, sep)
			if err := h.RestartBroker(ctx, templates); err != nil {
				t.Fatalf("restart broker with templates %q: %v", templates, err)
			}
			run := realiseScenario(s, h.runID)
			h.runScenario(ctx, t, repoID, run)
		})
	}
}
