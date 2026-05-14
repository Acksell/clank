// Package cloud is the laptop's HTTP client for the user's clank
// gateway deployment. Provider-agnostic: the gateway exposes an
// /auth-config discovery endpoint that returns the IdP details
// (Supabase URL + anon key today), and clank's OAuth client (oauth.go)
// runs standard OAuth 2.0 authorization code + PKCE against that IdP.
//
// Designed for the TUI's Cloud panel (internal/tui/cloudview.go) and
// the `clank login` subcommand. Every call is one-shot, Context-bounded.
// No background goroutines, no caching: callers own lifecycle.
package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized is returned when the bearer is rejected by the
// gateway (e.g. token expired). Caller should re-prompt sign-in.
var ErrUnauthorized = errors.New("cloud: unauthorized")

// Client wraps an *http.Client targeting the gateway base URL. It
// covers the small bootstrap surface the laptop needs *before* it
// has a session — currently just /auth-config. Authenticated calls
// (sync, sessions, etc.) go through dedicated clients elsewhere
// (e.g. pkg/sync/client) wrapping the same gateway URL.
type Client struct {
	gatewayURL string
	http       *http.Client
}

// New constructs a Client targeting the gateway base URL (e.g.
// "https://clankgw.fly.dev"). httpClient may be nil for the default.
func New(gatewayURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{gatewayURL: strings.TrimRight(gatewayURL, "/"), http: httpClient}
}

// AuthConfig is the gateway's reply to GET /auth-config. It tells
// clank which IdP to run OAuth against, plus the publishable details
// needed to do so. The endpoint is public (no auth) because clank
// has no token yet at the point it calls it.
//
// Fields are intentionally narrow — additions go in a separate
// optional struct so older clients don't break on new fields.
type AuthConfig struct {
	// SupabaseURL is the Supabase project root, e.g.
	// "https://abc123.supabase.co". OAuth endpoints sit under
	// /auth/v1/*.
	SupabaseURL string `json:"supabase_url"`

	// AnonKey is the Supabase publishable anon key. Sent in the
	// `apikey` header on every call to /auth/v1/*. Safe to ship
	// to clients by design (RLS is the security boundary).
	AnonKey string `json:"anon_key"`

	// DefaultProvider is the OAuth provider name the gateway
	// suggests as the primary sign-in option (e.g. "github").
	// Optional — clank's UI can override.
	DefaultProvider string `json:"default_provider,omitempty"`
}

// FetchAuthConfig calls GET <gateway>/auth-config to discover the
// IdP details. Public endpoint — no Authorization header.
func (c *Client) FetchAuthConfig(ctx context.Context) (*AuthConfig, error) {
	url := c.gatewayURL + "/auth-config"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch auth-config: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth-config: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var cfg AuthConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("parse auth-config: %w", err)
	}
	if cfg.SupabaseURL == "" || cfg.AnonKey == "" {
		return nil, fmt.Errorf("auth-config: response missing supabase_url or anon_key")
	}
	return &cfg, nil
}

// Session is the credential set returned after a successful OAuth
// grant. ExpiresAt is unix-seconds; treat AccessToken as invalid
// once time.Now().Unix() > ExpiresAt.
type Session struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	UserEmail    string
	ExpiresAt    int64
}

// GatewayURL returns the configured gateway base URL. Useful for
// callers that need to construct sub-paths (e.g. sync clients).
func (c *Client) GatewayURL() string { return c.gatewayURL }
