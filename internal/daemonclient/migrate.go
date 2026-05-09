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
// Only "to_sprite" is implemented today; "to_laptop" is a P5 stub.
type MigrateDirection string

const (
	MigrateToSprite MigrateDirection = "to_sprite"
	MigrateToLaptop MigrateDirection = "to_laptop"
)

// MigrateResponse mirrors the gateway's reply to POST
// /v1/migrate/worktrees/{id}.
type MigrateResponse struct {
	WorktreeID   string `json:"worktree_id"`
	NewOwnerKind string `json:"new_owner_kind"`
	NewOwnerID   string `json:"new_owner_id"`
	CheckpointID string `json:"checkpoint_id"`
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
		direction = MigrateToSprite
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
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("migrate %s: %d: %s", worktreeID, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out MigrateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode migrate response: %w", err)
	}
	return &out, nil
}
