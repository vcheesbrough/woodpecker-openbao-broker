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

func isTerminalPipeline(s PipelineState) bool {
	switch s {
	case pipelineStatusSuccess, pipelineStatusFailure, pipelineStatusError,
		pipelineStatusKilled, pipelineStatusBlocked, pipelineStatusDeclined:
		return true
	}
	return false
}
