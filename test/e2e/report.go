//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Report aggregates per-scenario results plus environment metadata for
// rendering as both a human-readable markdown file and a machine-readable
// JSON file under the run's artifact directory.
type Report struct {
	mu         sync.Mutex
	RunID      string                 `json:"run_id"`
	StartedAt  time.Time              `json:"started_at"`
	EndedAt    time.Time              `json:"ended_at,omitempty"`
	Env        map[string]string      `json:"env"`
	Scenarios  []ScenarioResult       `json:"scenarios"`
	Cleanup    []string               `json:"cleanup_errors,omitempty"`
}

type ScenarioResult struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Status        string            `json:"status"` // "pass", "fail", "skip"
	Reason        string            `json:"reason,omitempty"`
	Templates     []string          `json:"templates,omitempty"`
	SeededDigests map[string]string `json:"seeded_digests,omitempty"` // path -> sha256-of-keys
	ReceivedHashes map[string]string `json:"received_hashes,omitempty"` // name -> sha256(value)
	BrokerLog     string            `json:"broker_log_excerpt,omitempty"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	DurationMS    int64             `json:"duration_ms,omitempty"`
}

func NewReport(runID string) *Report {
	return &Report{
		RunID:     runID,
		StartedAt: time.Now().UTC(),
		Env:       map[string]string{},
	}
}

func (r *Report) SetEnv(k, v string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Env[k] = v
}

func (r *Report) Append(res ScenarioResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Scenarios = append(r.Scenarios, res)
}

func (r *Report) AppendCleanupError(err string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Cleanup = append(r.Cleanup, err)
}

func (r *Report) WriteFiles(dir string) error {
	r.mu.Lock()
	r.EndedAt = time.Now().UTC()
	r.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	jsonBytes, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), jsonBytes, 0o644); err != nil {
		return err
	}

	md := r.renderMarkdown()
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	return nil
}

func (r *Report) renderMarkdown() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var sb strings.Builder
	fmt.Fprintf(&sb, "# E2E Release Verification — Run %s\n\n", r.RunID)
	fmt.Fprintf(&sb, "Started: %s  \nEnded: %s  \nDuration: %s\n\n",
		r.StartedAt.Format(time.RFC3339),
		r.EndedAt.Format(time.RFC3339),
		r.EndedAt.Sub(r.StartedAt).Round(time.Millisecond))

	if len(r.Env) > 0 {
		sb.WriteString("## Environment\n\n")
		envKeys := make([]string, 0, len(r.Env))
		for k := range r.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		sb.WriteString("| Key | Value |\n|---|---|\n")
		for _, k := range envKeys {
			fmt.Fprintf(&sb, "| `%s` | `%s` |\n", k, r.Env[k])
		}
		sb.WriteString("\n")
	}

	pass, fail, skip := 0, 0, 0
	for _, s := range r.Scenarios {
		switch s.Status {
		case "pass":
			pass++
		case "fail":
			fail++
		case "skip":
			skip++
		}
	}
	fmt.Fprintf(&sb, "## Summary\n\n%d pass, %d fail, %d skip (total %d)\n\n",
		pass, fail, skip, len(r.Scenarios))

	if len(r.Scenarios) > 0 {
		sb.WriteString("## Scenarios\n\n")
		sb.WriteString("| ID | Title | Status | Reason / Duration |\n|---|---|---|---|\n")
		for _, s := range r.Scenarios {
			detail := s.Reason
			if detail == "" && s.DurationMS > 0 {
				detail = fmt.Sprintf("%d ms", s.DurationMS)
			}
			fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n", s.ID, s.Title, s.Status, detail)
		}
		sb.WriteString("\n")
	}

	if len(r.Cleanup) > 0 {
		sb.WriteString("## Cleanup errors\n\n")
		for _, e := range r.Cleanup {
			fmt.Fprintf(&sb, "- %s\n", e)
		}
		sb.WriteString("\n")
	} else {
		sb.WriteString("## Cleanup\n\nAll resources removed; verifiers passed.\n")
	}

	return sb.String()
}

// Sha256Hex returns a short fingerprint suitable for the report. Used for
// receiver-collected secret values so plaintext never lands in artifacts.
func Sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
