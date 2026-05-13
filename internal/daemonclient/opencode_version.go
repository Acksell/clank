package daemonclient

import (
	"context"
	"fmt"
)

// OpenCodeVersion calls clank-host's GET /opencode-version (proxied
// through whichever daemon this client targets) and returns the
// bare opencode version string the host is running. Used by the
// laptop CLI to enforce the version-skew policy before any
// migration kicks off.
//
// Empty response is treated as an error so callers don't have to
// double-check.
func (c *Client) OpenCodeVersion(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.get(ctx, "/opencode-version", &out); err != nil {
		return "", fmt.Errorf("opencode-version: %w", err)
	}
	if out.Version == "" {
		return "", fmt.Errorf("opencode-version: daemon returned empty version")
	}
	return out.Version, nil
}
