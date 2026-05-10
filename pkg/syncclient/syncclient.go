// Package syncclient is the laptop side of clank-sync. It registers
// worktrees and pushes checkpoint bundles via the gateway-orchestrated
// migration substrate. See checkpoints.go for the public methods.
package syncclient

import (
	"fmt"
	"net/http"
)

// Config configures a Client.
type Config struct {
	// BaseURL is the clank-sync endpoint, e.g. "https://sync.example.com".
	BaseURL string

	// AuthToken is sent as `Authorization: Bearer <token>` on every
	// upload. Required for non-permissive deployments.
	AuthToken string

	// DeviceID identifies this laptop in worktree ownership records.
	// Sent as X-Clank-Device-Id on every request.
	DeviceID string

	// HTTPClient overrides the default http.Client. Optional.
	HTTPClient *http.Client
}

// Client uploads bundles to a clank-sync server.
type Client struct {
	cfg    Config
	client *http.Client
}

// New constructs a Client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("syncclient: BaseURL is required")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Client{cfg: cfg, client: cfg.HTTPClient}, nil
}
