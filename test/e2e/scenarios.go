//go:build e2e

// Package e2e holds the end-to-end release-verification harness for
// woodpecker-openbao-broker (bored card #118).
//
// State of the harness as of this PR (scaffolding only):
//
//   - Network, receiver image, OpenBao bringup, AppRole + KV helpers, the
//     resource ledger, and the report writer are wired up and exercised by
//     TestSmoke.
//   - Gitea, Woodpecker server/agent, and the broker-under-test are NOT yet
//     wired up. TestE2E enumerates the full 20-row scenario matrix from the
//     card but is `t.Skip`'d. Each subsequent PR adds one of those layers.
//
// All files in this package compile only under `-tags=e2e` so the default
// `go test ./...` run is unaffected.
package e2e

// Trigger describes how a scenario causes a Woodpecker pipeline to run.
type Trigger string

const (
	TriggerPush        Trigger = "push"
	TriggerBranchPush  Trigger = "branch_push"
	TriggerPullRequest Trigger = "pull_request"
	TriggerTag         Trigger = "tag"
	TriggerManual      Trigger = "manual"
)

// Scenario captures one row from the test matrix in bored card #118.
//
// Templates is the broker's `SECRET_PATH_TEMPLATES` for this scenario.
// Seeds names KV paths whose values must be written before the trigger.
// Expect describes the secret names and values the pipeline should report
// back through the receiver.
type Scenario struct {
	ID          string
	Title       string
	Templates   []string
	// JoinWith is the separator used to join Templates into SECRET_PATH_TEMPLATES.
	// Defaults to "\n". Set to "," to test the comma-separated form.
	JoinWith      string
	Seeds         map[string]map[string]string // path -> {key: value}
	NativeSecrets map[string]string            // Woodpecker-native secret name -> value
	Trigger       Trigger
	BranchName    string            // for branch_push / pull_request / tag scenarios
	Expect        map[string]string // expected secret name -> value
	Description   string

	// Disable marks scenarios that cannot run until later layers
	// (Gitea, Woodpecker, broker) are added. Every scenario starts true here
	// in the scaffolding PR.
	Disabled bool
}

// AllScenarios returns the static 20-row matrix from card #118.
//
// Per-scenario seed values are placeholder template strings — the actual
// values will be replaced with random per-run nonces by the test driver
// before being written to OpenBao, so that no per-run secret ever leaks.
func AllScenarios() []Scenario {
	return []Scenario{
		{
			ID: "s01", Title: "Empty templates",
			Description: "broker returns 0 secrets, pipeline still succeeds",
			Templates:   nil,
			Trigger:     TriggerPush,
			Expect:      map[string]string{},
		},
		{
			ID: "s02", Title: "Single global",
			Templates: []string{"woodpecker/global"},
			Seeds: map[string]map[string]string{
				"woodpecker/global": {"global_only": "<<global_only>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"global_only": "<<global_only>>"},
			Description: "global keys delivered",
		},
		{
			ID: "s03", Title: "Per-repo (FullName)",
			Templates: []string{"woodpecker/repos/{{.Repo.FullName}}"},
			Seeds: map[string]map[string]string{
				"woodpecker/repos/e2e/broker-target": {"repo_only": "<<repo_only>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"repo_only": "<<repo_only>>"},
			Description: "repo keys delivered",
		},
		{
			ID: "s04", Title: "Owner-keyed",
			Templates: []string{"owner/{{.Repo.Owner}}"},
			Seeds: map[string]map[string]string{
				"owner/e2e": {"owner_only": "<<owner_only>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"owner_only": "<<owner_only>>"},
			Description: "owner keys delivered",
		},
		{
			ID: "s05", Title: "Name-keyed",
			Templates: []string{"name/{{.Repo.Name}}"},
			Seeds: map[string]map[string]string{
				"name/broker-target": {"name_only": "<<name_only>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"name_only": "<<name_only>>"},
			Description: "name keys delivered",
		},
		{
			ID: "s06", Title: "ForgeID-keyed",
			Templates: []string{"forge/{{.Repo.ForgeID}}"},
			// Seed path is filled in by the harness once Gitea is registered as
			// a forge in Woodpecker (forge ID is assigned then).
			Trigger:     TriggerPush,
			Expect:      map[string]string{"forge_only": "<<forge_only>>"},
			Description: "forge keys delivered",
			Disabled:    true,
		},
		{
			ID: "s07", Title: "Branch-keyed",
			Templates: []string{"woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}"},
			Seeds: map[string]map[string]string{
				"woodpecker/repos/e2e/broker-target/branches/feature/x": {"branch_only": "<<branch_only>>"},
			},
			Trigger:     TriggerBranchPush,
			BranchName:  "feature/x",
			Expect:      map[string]string{"branch_only": "<<branch_only>>"},
			Description: "branch-specific keys delivered, base branch keys absent",
			Disabled:    true,
		},
		{
			ID: "s08", Title: "Event-keyed (push)",
			Templates: []string{"event/{{.Pipeline.Event}}"},
			Seeds: map[string]map[string]string{
				"event/push": {"event_only": "<<event_push>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"event_only": "<<event_push>>"},
			Description: "push bucket delivered",
			Disabled:    true, // TriggerPipeline API gives event=manual; needs real Gitea webhook push
		},
		{
			ID: "s09", Title: "Event-keyed (pull_request)",
			Templates: []string{"event/{{.Pipeline.Event}}"},
			Seeds: map[string]map[string]string{
				"event/pull_request": {"event_only": "<<event_pr>>"},
			},
			Trigger:     TriggerPullRequest,
			BranchName:  "pr-branch",
			Expect:      map[string]string{"event_only": "<<event_pr>>"},
			Description: "pull_request bucket delivered",
			Disabled:    true,
		},
		{
			ID: "s10", Title: "Event-keyed (tag)",
			Templates: []string{"event/{{.Pipeline.Event}}"},
			Seeds: map[string]map[string]string{
				"event/tag": {"event_only": "<<event_tag>>"},
			},
			Trigger:     TriggerTag,
			BranchName:  "v0.0.1-e2e",
			Expect:      map[string]string{"event_only": "<<event_tag>>"},
			Description: "tag bucket delivered",
			Disabled:    true,
		},
		{
			ID: "s11", Title: "Event-keyed (manual)",
			Templates: []string{"event/{{.Pipeline.Event}}"},
			Seeds: map[string]map[string]string{
				"event/manual": {"event_only": "<<event_manual>>"},
			},
			Trigger:     TriggerManual,
			Expect:      map[string]string{"event_only": "<<event_manual>>"},
			Description: "manual bucket delivered",
		},
		{
			ID: "s12", Title: "Layered, no conflicts",
			Templates: []string{
				"woodpecker/global",
				"woodpecker/repos/{{.Repo.FullName}}",
			},
			Seeds: map[string]map[string]string{
				"woodpecker/global":                  {"shared_a": "<<global_a>>"},
				"woodpecker/repos/e2e/broker-target": {"shared_b": "<<repo_b>>"},
			},
			Trigger: TriggerPush,
			Expect: map[string]string{
				"shared_a": "<<global_a>>",
				"shared_b": "<<repo_b>>",
			},
			Description: "union of both",
		},
		{
			ID: "s13", Title: "Layered, conflicts",
			Templates: []string{
				"woodpecker/global",
				"woodpecker/repos/{{.Repo.FullName}}",
			},
			Seeds: map[string]map[string]string{
				"woodpecker/global":                  {"shared": "<<global_wins_unless>>"},
				"woodpecker/repos/e2e/broker-target": {"shared": "<<repo_wins>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"shared": "<<repo_wins>>"},
			Description: "per-repo wins (later paths win)",
		},
		{
			ID: "s14", Title: "Layered three-deep",
			Templates: []string{
				"woodpecker/global",
				"woodpecker/repos/{{.Repo.FullName}}",
				"woodpecker/repos/{{.Repo.FullName}}/branches/{{.Pipeline.Branch}}",
			},
			Seeds: map[string]map[string]string{
				"woodpecker/global":                                       {"shared": "<<global>>"},
				"woodpecker/repos/e2e/broker-target":                      {"shared": "<<repo>>"},
				"woodpecker/repos/e2e/broker-target/branches/feature/y":   {"shared": "<<branch>>"},
			},
			Trigger:     TriggerBranchPush,
			BranchName:  "feature/y",
			Expect:      map[string]string{"shared": "<<branch>>"},
			Description: "branch > repo > global",
			Disabled:    true,
		},
		{
			ID: "s15", Title: "Missing path tolerated",
			Templates:   []string{"woodpecker/repos/{{.Repo.FullName}}/this-path-does-not-exist"},
			Seeds:       nil,
			Trigger:     TriggerPush,
			Expect:      map[string]string{},
			Description: "pipeline succeeds, no error",
		},
		{
			ID: "s16", Title: "403 path tolerated",
			Templates:   []string{"forbidden/{{.Repo.FullName}}"},
			Seeds: map[string]map[string]string{
				"forbidden/e2e/broker-target": {"never_visible": "<<unreachable>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{},
			Description: "pipeline succeeds, no error (broker uses narrower policy that omits forbidden/*)",
		},
		{
			ID: "s17", Title: "Bad field name",
			Templates:   []string{"woodpecker/{{.Repo.Nope}}"},
			Trigger:     TriggerPush,
			Expect:      map[string]string{},
			Description: "broker returns 400; combined.go fail-soft means pipeline still runs with zero extension secrets — assertion is broker-log + zero received",
		},
		{
			ID: "s18", Title: "Multi-line vs comma form",
			Templates: []string{
				"woodpecker/global",
				"woodpecker/repos/{{.Repo.FullName}}",
			},
			JoinWith: ",",
			Seeds: map[string]map[string]string{
				"woodpecker/global":                  {"shared_a": "<<a>>"},
				"woodpecker/repos/e2e/broker-target": {"shared_b": "<<b>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"shared_a": "<<a>>", "shared_b": "<<b>>"},
			Description: "comma-joined templates; newline form already covered by s12",
		},
		{
			ID: "s19", Title: "from_secret in pipeline",
			Templates: []string{"woodpecker/repos/{{.Repo.FullName}}"},
			Seeds: map[string]map[string]string{
				"woodpecker/repos/e2e/broker-target": {"registry_user": "<<reg_user>>"},
			},
			Trigger:     TriggerPush,
			Expect:      map[string]string{"registry_user": "<<reg_user>>"},
			Description: "step env var via from_secret echoes correct value through receiver",
		},
		{
			ID: "s20", Title: "Native + extension coexist",
			Templates: []string{"woodpecker/repos/{{.Repo.FullName}}"},
			Seeds: map[string]map[string]string{
				"woodpecker/repos/e2e/broker-target": {"clash": "<<extension_wins>>"},
			},
			NativeSecrets: map[string]string{"clash": "<<native_clash>>"},
			Trigger:       TriggerPush,
			Expect:        map[string]string{"clash": "<<extension_wins>>"},
			Description:   "native Woodpecker secret with same name is overridden by extension per combined.go",
		},
	}
}
