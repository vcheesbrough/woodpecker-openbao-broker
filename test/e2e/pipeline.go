//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// PipelineState mirrors the subset of Woodpecker's pipeline status field
// that the harness asserts on.
type PipelineState string

const (
	pipelineStatusSuccess PipelineState = "success"
	pipelineStatusFailure PipelineState = "failure"
	pipelineStatusError   PipelineState = "error"
	pipelineStatusKilled  PipelineState = "killed"
	pipelineStatusBlocked PipelineState = "blocked"
	pipelineStatusDeclined PipelineState = "declined"
)

// RegisterRepoWithWoodpecker registers e2e/broker-target with Woodpecker
// using the Gitea numeric repo id as forge_remote_id. Returns the
// resulting Woodpecker repo id, which the manual-trigger and poll
// helpers below use as their path component.
func (h *Harness) RegisterRepoWithWoodpecker(ctx context.Context) (int64, error) {
	giteaRepo, _, err := h.gitea.client.GetRepo(giteaTestOrg, giteaTestRepo)
	if err != nil {
		return 0, fmt.Errorf("get gitea repo: %w", err)
	}
	forgeRemoteID := strconv.FormatInt(giteaRepo.ID, 10)

	url := fmt.Sprintf("%s/api/repos?forge_remote_id=%s", h.woodpecker.internalHTTPURL, forgeRemoteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-CSRF-TOKEN", h.woodpecker.csrfToken)

	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("register repo status %d: %s", resp.StatusCode, snippet(body))
	}

	var registered struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &registered); err != nil {
		return 0, fmt.Errorf("decode registered repo: %w body=%s", err, snippet(body))
	}
	if registered.ID == 0 {
		return 0, fmt.Errorf("registered repo response missing id: %s", snippet(body))
	}

	h.ledger.Add(NewFuncResource(fmt.Sprintf("woodpecker repo %d", registered.ID), func(ctx context.Context) error {
		return h.deregisterRepoWithWoodpecker(ctx, registered.ID)
	}))
	return registered.ID, nil
}

func (h *Harness) deregisterRepoWithWoodpecker(ctx context.Context, repoID int64) error {
	url := fmt.Sprintf("%s/api/repos/%d", h.woodpecker.internalHTTPURL, repoID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-CSRF-TOKEN", h.woodpecker.csrfToken)
	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("deregister status %d: %s", resp.StatusCode, snippet(body))
	}
	return nil
}

// TriggerPipeline manually fires a pipeline against the given branch.
// Returns the pipeline number that the poll helper takes.
func (h *Harness) TriggerPipeline(ctx context.Context, repoID int64, branch string) (int64, error) {
	url := fmt.Sprintf("%s/api/repos/%d/pipelines", h.woodpecker.internalHTTPURL, repoID)
	payload := map[string]any{
		"branch": branch,
	}
	bodyBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-TOKEN", h.woodpecker.csrfToken)

	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("trigger pipeline status %d: %s", resp.StatusCode, snippet(body))
	}

	var triggered struct {
		Number int64 `json:"number"`
	}
	if err := json.Unmarshal(body, &triggered); err != nil {
		return 0, fmt.Errorf("decode trigger: %w body=%s", err, snippet(body))
	}
	if triggered.Number == 0 {
		return 0, fmt.Errorf("trigger response missing number: %s", snippet(body))
	}
	return triggered.Number, nil
}

// PollPipeline blocks until the pipeline reaches a terminal status or
// the context expires. Returns the final status string.
func (h *Harness) PollPipeline(ctx context.Context, repoID, number int64, timeout time.Duration) (PipelineState, error) {
	deadline := time.Now().Add(timeout)
	for {
		status, err := h.fetchPipelineStatus(ctx, repoID, number)
		if err != nil {
			return "", err
		}
		if isTerminalPipeline(status) {
			return status, nil
		}
		if time.Now().After(deadline) {
			return status, fmt.Errorf("pipeline %d/%d still %s after %s", repoID, number, status, timeout)
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (h *Harness) fetchPipelineStatus(ctx context.Context, repoID, number int64) (PipelineState, error) {
	url := fmt.Sprintf("%s/api/repos/%d/pipelines/%d", h.woodpecker.internalHTTPURL, repoID, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("get pipeline status %d: %s", resp.StatusCode, snippet(body))
	}
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", fmt.Errorf("decode pipeline: %w body=%s", err, snippet(body))
	}
	return PipelineState(p.Status), nil
}

// dumpPipelineLog returns a best-effort string with the pipeline's
// workflow status and step logs. Used for failure diagnostics.
func (h *Harness) dumpPipelineLog(ctx context.Context, repoID, number int64) string {
	url := fmt.Sprintf("%s/api/repos/%d/pipelines/%d", h.woodpecker.internalHTTPURL, repoID, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Sprintf("(build req: %v)", err)
	}
	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return fmt.Sprintf("(get pipeline: %v)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return string(body)
}

type pipelineSummary struct {
	Number int64  `json:"number"`
	Event  string `json:"event"`
}

// listRecentPipelines returns up to limit pipelines newest-first.
func (h *Harness) listRecentPipelines(ctx context.Context, repoID int64, limit int) ([]pipelineSummary, error) {
	url := fmt.Sprintf("%s/api/repos/%d/pipelines?page=1&limit=%d", h.woodpecker.internalHTTPURL, repoID, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("list pipelines status %d: %s", resp.StatusCode, snippet(body))
	}
	var out []pipelineSummary
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode pipeline list: %w body=%s", err, snippet(body))
	}
	return out, nil
}

// latestPipelineNumber returns the number of the most recently created
// pipeline, or 0 if none exist yet.
func (h *Harness) latestPipelineNumber(ctx context.Context, repoID int64) (int64, error) {
	ps, err := h.listRecentPipelines(ctx, repoID, 1)
	if err != nil {
		return 0, err
	}
	if len(ps) == 0 {
		return 0, nil
	}
	return ps[0].Number, nil
}

// waitForPipelineAfter polls until a pipeline appears with number > afterNum
// and (if wantEvent is non-empty) the matching event type. Pipelines with
// number > afterNum but wrong event advance the baseline so they are skipped.
func (h *Harness) waitForPipelineAfter(ctx context.Context, repoID, afterNum int64, wantEvent string, timeout time.Duration) (int64, error) {
	deadline := time.Now().Add(timeout)
	for {
		ps, err := h.listRecentPipelines(ctx, repoID, 20)
		if err != nil {
			return 0, err
		}
		for _, p := range ps {
			if p.Number <= afterNum {
				break
			}
			if wantEvent == "" || p.Event == wantEvent {
				return p.Number, nil
			}
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("timeout waiting for pipeline event=%q after #%d", wantEvent, afterNum)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// CreateNativeSecret creates a repo-level secret in Woodpecker that applies
// to all event types. Used by s20 to verify extension secrets take precedence.
func (h *Harness) CreateNativeSecret(ctx context.Context, repoID int64, name, value string) error {
	url := fmt.Sprintf("%s/api/repos/%d/secrets", h.woodpecker.internalHTTPURL, repoID)
	payload := map[string]any{
		"name":   name,
		"value":  value,
		"events": []string{"push", "pull_request", "tag", "manual", "deployment", "cron", "release"},
	}
	bodyBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-TOKEN", h.woodpecker.csrfToken)
	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("create native secret status %d: %s", resp.StatusCode, snippet(b))
	}
	return nil
}

// DeleteNativeSecret removes a repo-level Woodpecker secret by name.
func (h *Harness) DeleteNativeSecret(ctx context.Context, repoID int64, name string) error {
	url := fmt.Sprintf("%s/api/repos/%d/secrets/%s", h.woodpecker.internalHTTPURL, repoID, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-CSRF-TOKEN", h.woodpecker.csrfToken)
	resp, err := h.woodpecker.sessionClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete native secret status %d: %s", resp.StatusCode, snippet(b))
	}
	return nil
}

func isTerminalPipeline(s PipelineState) bool {
	switch s {
	case pipelineStatusSuccess, pipelineStatusFailure, pipelineStatusError,
		pipelineStatusKilled, pipelineStatusBlocked, pipelineStatusDeclined:
		return true
	}
	return false
}
