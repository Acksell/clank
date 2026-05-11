package daemonclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// MigrateDirection enumerates the supported MigrateWorktree directions.
type MigrateDirection string

const (
	MigrateToRemote MigrateDirection = "to_remote"
)

// MigrateResponse mirrors the gateway's reply to POST
// /v1/migrate/worktrees/{id}.
type MigrateResponse struct {
	WorktreeID   string `json:"worktree_id"`
	NewOwnerKind string `json:"new_owner_kind"`
	NewOwnerID   string `json:"new_owner_id"`
	CheckpointID string `json:"checkpoint_id"`
}

// MaterializeResponse mirrors the gateway's reply to POST
// /v1/migrate/worktrees/{id}/materialize.
type MaterializeResponse struct {
	CheckpointID    string `json:"checkpoint_id"`
	HeadCommit      string `json:"head_commit"`
	ManifestURL     string `json:"manifest_url"`
	HeadCommitURL   string `json:"head_commit_url"`
	IncrementalURL  string `json:"incremental_url"`
	MigrationToken  string `json:"migration_token"`
	MigrationExpiry int64  `json:"migration_expiry"`
}

// MaterializeMigration is phase 1 of the migrate-back flow: the gateway
// asks the sprite to checkpoint its current state and returns presigned
// GET URLs for the bundles plus a signed migration token. The laptop
// downloads + applies before calling CommitMigration.
func (c *Client) MaterializeMigration(ctx context.Context, worktreeID, deviceID string) (*MaterializeResponse, error) {
	if worktreeID == "" {
		return nil, fmt.Errorf("MaterializeMigration: worktreeID is required")
	}
	body, err := json.Marshal(map[string]any{"confirm": true})
	if err != nil {
		return nil, err
	}
	target := c.baseURL + "/v1/migrate/worktrees/" + url.PathEscape(worktreeID) + "/materialize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	if deviceID != "" {
		req.Header.Set("X-Clank-Device-Id", deviceID)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("materialize: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return nil, fmt.Errorf("read materialize response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("materialize %s: %d: %s", worktreeID, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out MaterializeResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode materialize response: %w", err)
	}
	return &out, nil
}

// CommitMigration is phase 2: the laptop has successfully applied the
// checkpoint locally; ownership atomically transfers back. Caller must
// pass the migration_token and checkpoint_id from the matching
// materialize response.
func (c *Client) CommitMigration(ctx context.Context, worktreeID, deviceID, checkpointID, migrationToken string) (*MigrateResponse, error) {
	if worktreeID == "" || checkpointID == "" || migrationToken == "" {
		return nil, fmt.Errorf("CommitMigration: worktreeID, checkpointID, and migrationToken are required")
	}
	body, err := json.Marshal(map[string]any{
		"checkpoint_id":   checkpointID,
		"migration_token": migrationToken,
	})
	if err != nil {
		return nil, err
	}
	target := c.baseURL + "/v1/migrate/worktrees/" + url.PathEscape(worktreeID) + "/commit"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	if deviceID != "" {
		req.Header.Set("X-Clank-Device-Id", deviceID)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return nil, fmt.Errorf("read commit response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("commit %s: %d: %s", worktreeID, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out MigrateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode commit response: %w", err)
	}
	return &out, nil
}

// MigrateWorktree calls the gateway's MigrateWorktree endpoint.
// deviceID identifies the laptop owning the worktree (forwarded as
// X-Clank-Device-Id; ignored when the active hub is a Unix socket
// since the laptop-local clankd's local provisioner doesn't gate on
// device ownership).
func (c *Client) MigrateWorktree(ctx context.Context, worktreeID, deviceID string, direction MigrateDirection) (*MigrateResponse, error) {
	if worktreeID == "" {
		return nil, fmt.Errorf("MigrateWorktree: worktreeID is required")
	}
	if direction == "" {
		direction = MigrateToRemote
	}

	body, err := json.Marshal(map[string]any{
		"direction": direction,
		"confirm":   true,
	})
	if err != nil {
		return nil, err
	}
	target := c.baseURL + "/v1/migrate/worktrees/" + url.PathEscape(worktreeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	if deviceID != "" {
		req.Header.Set("X-Clank-Device-Id", deviceID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("migrate request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return nil, fmt.Errorf("read migrate response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("migrate %s: %d: %s", worktreeID, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out MigrateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode migrate response: %w", err)
	}
	return &out, nil
}
