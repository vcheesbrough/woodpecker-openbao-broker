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
	scenario Scenario
	seeds    map[string]map[string]string // path → key → realised value
	expect   map[string]string            // secret name → realised value
	branch   string
	yamlPath string
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
	expect := map[string]string{}
	for k, v := range s.Expect {
		expect[k] = mapValue(v)
	}

	branch := "scenario/" + s.ID + "-" + randomToken(4)
	return scenarioRun{
		scenario: s,
		seeds:    seeds,
		expect:   expect,
		branch:   branch,
		yamlPath: ".woodpecker.yaml",
	}
}

// runScenario drives a single scenario end-to-end: clears KV, seeds it,
// commits a generated pipeline yaml on a fresh branch (which fires the
// Gitea webhook into Woodpecker), polls the resulting pipeline, then
// asserts the receiver collected the expected secrets.
func (h *Harness) runScenario(ctx context.Context, t *testing.T, repoID int64, run scenarioRun) {
	t.Helper()

	if err := h.ClearKVTree(ctx, "woodpecker"); err != nil {
		t.Fatalf("clear kv: %v", err)
	}
	for path, kv := range run.seeds {
		if err := h.WriteKV(ctx, path, kv); err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}

	yaml := generatePipelineYAML(run)
	if err := h.CreateBranch("main", run.branch); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	if _, err := h.CommitFile(run.branch, run.yamlPath, yaml, "scenario "+run.scenario.ID); err != nil {
		t.Fatalf("commit yaml: %v", err)
	}

	number, err := h.TriggerPipeline(ctx, repoID, run.branch)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
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
