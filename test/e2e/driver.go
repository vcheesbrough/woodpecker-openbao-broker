//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// scenarioRun is what the driver materializes from a Scenario at run
// time: random per-run nonces in place of `<<placeholder>>` strings.
type scenarioRun struct {
	scenario      Scenario
	seeds         map[string]map[string]string // path → key → realised value
	nativeSecrets map[string]string            // Woodpecker-native secret name → realised value
	expect        map[string]string            // secret name → realised value
	branch        string
	yamlPath      string
}

var placeholderRE = regexp.MustCompile(`^<<([^>]+)>>$`)

// realiseScenario substitutes every <<placeholder>> with a per-run
// random hex nonce so leaks are detectable and across-scenario state
// can't accidentally pass an assertion.
func realiseScenario(s Scenario, runID string) scenarioRun {
	subs := map[string]string{}
	mapValue := func(v string) string {
		m := placeholderRE.FindStringSubmatch(v)
		if len(m) != 2 {
			return v
		}
		key := m[1]
		if existing, ok := subs[key]; ok {
			return existing
		}
		nonce := fmt.Sprintf("%s-%s-%s", runID, s.ID, randomToken(12))
		subs[key] = nonce
		return nonce
	}

	seeds := map[string]map[string]string{}
	for path, kv := range s.Seeds {
		out := map[string]string{}
		for k, v := range kv {
			out[k] = mapValue(v)
		}
		seeds[path] = out
	}
	nativeSecrets := map[string]string{}
	for k, v := range s.NativeSecrets {
		nativeSecrets[k] = mapValue(v)
	}
	expect := map[string]string{}
	for k, v := range s.Expect {
		expect[k] = mapValue(v)
	}

	branch := "scenario/" + s.ID + "-" + randomToken(4)
	return scenarioRun{
		scenario:      s,
		seeds:         seeds,
		nativeSecrets: nativeSecrets,
		expect:        expect,
		branch:        branch,
		yamlPath:      ".woodpecker.yaml",
	}
}

// runScenario drives a single scenario end-to-end: clears KV, seeds it,
// triggers a pipeline (method depends on Trigger type), polls until
// terminal, and asserts the receiver collected the expected secrets.
func (h *Harness) runScenario(ctx context.Context, t *testing.T, repoID int64, run scenarioRun) {
	t.Helper()

	if err := h.ClearKVTree(ctx, "woodpecker"); err != nil {
		t.Fatalf("clear kv: %v", err)
	}

	// Resolve {{forge_id}} in seed paths to the Gitea repo's numeric ID.
	forgeID := int64(0)
	for path := range run.seeds {
		if strings.Contains(path, "{{forge_id}}") {
			var err error
			forgeID, err = h.giteaTestRepoID()
			if err != nil {
				t.Fatalf("forge id: %v", err)
			}
			break
		}
	}
	for path, kv := range run.seeds {
		realPath := strings.ReplaceAll(path, "{{forge_id}}", fmt.Sprintf("%d", forgeID))
		if err := h.WriteKV(ctx, realPath, kv); err != nil {
			t.Fatalf("seed %s: %v", realPath, err)
		}
	}

	for name, value := range run.nativeSecrets {
		name, value := name, value
		if err := h.CreateNativeSecret(ctx, repoID, name, value); err != nil {
			t.Fatalf("create native secret %s: %v", name, err)
		}
		t.Cleanup(func() {
			_ = h.DeleteNativeSecret(context.Background(), repoID, name)
		})
	}

	yaml := generatePipelineYAML(run)
	number := h.triggerScenarioPipeline(ctx, t, repoID, run, yaml)

	status, err := h.PollPipeline(ctx, repoID, number, 90*time.Second)
	if err != nil {
		t.Fatalf("poll: %v (last status %q)", err, status)
	}
	if status != pipelineStatusSuccess {
		dump := h.dumpPipelineLog(ctx, repoID, number)
		brokerLog := h.dumpBrokerLog(ctx)
		wpLog := h.dumpWoodpeckerServerLog(ctx)
		t.Fatalf("pipeline status %q, want success\npipeline dump:\n%s\nbroker log:\n%s\nwoodpecker tail:\n%s", status, dump, brokerLog, wpLog)
	}

	got, err := h.ReceiverPoll(ctx, run.scenario.ID)
	if err != nil {
		t.Fatalf("receiver poll: %v", err)
	}
	if !sameMap(got, run.expect) {
		t.Fatalf("received %v, want %v", redactValues(got), redactValues(run.expect))
	}
}

// triggerScenarioPipeline creates the pipeline for run and returns its number.
// The mechanism depends on the scenario's Trigger type.
func (h *Harness) triggerScenarioPipeline(ctx context.Context, t *testing.T, repoID int64, run scenarioRun, yaml string) int64 {
	t.Helper()
	commit := func(branch string) {
		if _, err := h.CommitFile(branch, run.yamlPath, yaml, "scenario "+run.scenario.ID); err != nil {
			t.Fatalf("commit yaml to %s: %v", branch, err)
		}
	}
	createBranch := func(branch string) {
		if err := h.CreateBranch("main", branch); err != nil {
			t.Fatalf("create branch %s: %v", branch, err)
		}
	}

	switch run.scenario.Trigger {
	case TriggerPush, TriggerManual:
		createBranch(run.branch)
		commit(run.branch)
		n, err := h.TriggerPipeline(ctx, repoID, run.branch)
		if err != nil {
			t.Fatalf("trigger pipeline: %v", err)
		}
		return n

	case TriggerBranchPush:
		// Use the fixed BranchName so {{.Pipeline.Branch}} resolves correctly;
		// trigger manually (event=manual is fine — the template doesn't use event).
		branch := run.scenario.BranchName
		_ = h.DeleteBranch(branch) // no-op if absent
		createBranch(branch)
		commit(branch)
		t.Cleanup(func() { _ = h.DeleteBranch(branch) })
		n, err := h.TriggerPipeline(ctx, repoID, branch)
		if err != nil {
			t.Fatalf("trigger pipeline on %s: %v", branch, err)
		}
		return n

	case TriggerPushWebhook:
		// Rely on the Gitea push webhook that fires when CommitFile is called.
		createBranch(run.branch)
		lastNum, err := h.latestPipelineNumber(ctx, repoID)
		if err != nil {
			t.Fatalf("latest pipeline number: %v", err)
		}
		commit(run.branch)
		n, err := h.waitForPipelineAfter(ctx, repoID, lastNum, "push", 30*time.Second)
		if err != nil {
			t.Fatalf("wait for push pipeline: %v", err)
		}
		return n

	case TriggerPullRequest:
		// Commit the YAML (fires a push webhook), then open a PR (fires a
		// pull_request webhook). Capture the push pipeline number first so
		// waitForPipelineAfter skips it.
		createBranch(run.branch)
		beforeCommit, err := h.latestPipelineNumber(ctx, repoID)
		if err != nil {
			t.Fatalf("latest pipeline number: %v", err)
		}
		commit(run.branch)
		// Wait for the push pipeline so we have a clean baseline.
		afterPush, _ := h.waitForPipelineAfter(ctx, repoID, beforeCommit, "push", 15*time.Second)
		if afterPush == 0 {
			afterPush = beforeCommit
		}
		if _, err := h.OpenPullRequest(run.branch, "main", "scenario "+run.scenario.ID); err != nil {
			t.Fatalf("open PR: %v", err)
		}
		n, err := h.waitForPipelineAfter(ctx, repoID, afterPush, "pull_request", 30*time.Second)
		if err != nil {
			t.Fatalf("wait for PR pipeline: %v", err)
		}
		return n

	case TriggerTag:
		// Commit the YAML (fires push webhook), create a tag (fires tag
		// webhook). BranchName holds the tag name; falls back to a run-scoped name.
		tagName := run.scenario.BranchName
		if tagName == "" {
			tagName = "e2e-tag-" + run.branch
		}
		createBranch(run.branch)
		beforeCommit, err := h.latestPipelineNumber(ctx, repoID)
		if err != nil {
			t.Fatalf("latest pipeline number: %v", err)
		}
		commit(run.branch)
		afterPush, _ := h.waitForPipelineAfter(ctx, repoID, beforeCommit, "push", 15*time.Second)
		if afterPush == 0 {
			afterPush = beforeCommit
		}
		_ = h.DeleteTag(tagName)
		if err := h.CreateTag(tagName, run.branch); err != nil {
			t.Fatalf("create tag %s: %v", tagName, err)
		}
		t.Cleanup(func() { _ = h.DeleteTag(tagName) })
		n, err := h.waitForPipelineAfter(ctx, repoID, afterPush, "tag", 30*time.Second)
		if err != nil {
			t.Fatalf("wait for tag pipeline: %v", err)
		}
		return n

	default:
		t.Fatalf("unhandled trigger type %q", run.scenario.Trigger)
		return 0
	}
}

// generatePipelineYAML produces the per-scenario .woodpecker.yaml. For
// each expected secret name the step writes the value to a file then
// curls it (via --data-binary @-) to the receiver, never echoing
// plaintext to stdout.
func generatePipelineYAML(run scenarioRun) string {
	names := make([]string, 0, len(run.expect))
	for k := range run.expect {
		names = append(names, k)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("when:\n  - event: [push, pull_request, tag, manual]\n\n")
	sb.WriteString("steps:\n  - name: receive\n    image: alpine:3\n    commands:\n")
	sb.WriteString("      - apk add --quiet curl\n")
	sb.WriteString(`      - mkdir -p "$CI_WORKSPACE/.secrets"` + "\n")
	for _, n := range names {
		envVar := "SECRET_" + n
		path := "$CI_WORKSPACE/.secrets/" + n
		url := "http://" + receiverNetAlias + ":" + receiverPort + "/scenarios/" + run.scenario.ID + "/" + n
		sb.WriteString(fmt.Sprintf(`      - printf '%%s' "$%s" > "%s" && curl -sS --data-binary @"%s" "%s"`, envVar, path, path, url) + "\n")
	}
	if len(names) == 0 {
		sb.WriteString("      - echo no-secrets-expected\n")
	}
	if len(names) > 0 {
		sb.WriteString("    environment:\n")
		for _, n := range names {
			sb.WriteString(fmt.Sprintf("      SECRET_%s:\n        from_secret: %s\n", n, n))
		}
	}
	return sb.String()
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// redactValues replaces secret values with their length so test failure
// messages don't bake plaintext into log artifacts.
func redactValues(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = fmt.Sprintf("<%d bytes>", len(v))
	}
	return out
}
