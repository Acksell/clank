// Package cloud is the laptop's HTTP client for the the cloud cloud:
// Supabase auth (sign-in / sign-up against the project's REST API) and
// the the cloud control plane (/me, /provision).
//
// Designed for the TUI's Cloud panel (internal/tui/cloudview.go) — every
// call is a one-shot Context-bounded request returning a typed result
// the TUI can render. No background goroutines, no caching: the TUI
// owns lifecycle.
package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized is returned when Supabase or the cloud rejects the
// bearer token (401). Callers should re-prompt for sign-in.
var ErrUnauthorized = errors.New("cloud: unauthorized")

// Endpoints are the URLs the client talks to. All three are required.
type Endpoints struct {
	// SupabaseProjectURL is the project root (no trailing slash), e.g.
	// "https://abc123.supabase.co". Auth lives at /auth/v1/* under it.
	SupabaseProjectURL string

	// SupabaseAnonKey is the project's publishable anon key. Sent in
	// the `apikey` header on every Supabase auth request.
	SupabaseAnonKey string

	// ClankctlURL is the upstream cloud control plane root, e.g.
	// "https://your-cloud.example.com".
	ClankctlURL string
}

// Client wraps an *http.Client with Supabase + the cloud helpers.
type Client struct {
	endpoints Endpoints
	http      *http.Client
}

// New constructs a Client. httpClient may be nil to use the default.
func New(endpoints Endpoints, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{endpoints: endpoints, http: httpClient}
}

// Session is the credential set returned after a successful
// SignIn/SignUp. ExpiresAt is unix-seconds; the caller should treat
// AccessToken as invalid once time.Now().Unix() crosses it.
type Session struct {
	AccessToken  string
	RefreshToken string
	UserID       string
	UserEmail    string
	ExpiresAt    int64
}

type supabaseTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	User         struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

type supabaseError struct {
	Code    string `json:"code"`
	Message string `json:"msg"`
	// Some Supabase errors use error_description (older shape).
	ErrorDescription string `json:"error_description"`
}

// SignIn exchanges email+password for a Session via the Supabase
// password grant. Returns ErrUnauthorized on 400/401 with a wrapped
// reason from Supabase's error body.
func (c *Client) SignIn(ctx context.Context, email, password string) (*Session, error) {
	url := strings.TrimRight(c.endpoints.SupabaseProjectURL, "/") + "/auth/v1/token?grant_type=password"
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.endpoints.SupabaseAnonKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase signin: %w", err)
	}
	defer resp.Body.Close()
	return parseTokenResponse(resp)
}

// SignUp creates a new Supabase user and returns a Session iff the
// project is configured to auto-confirm users (the default for new
// projects without email-confirmation enabled). When email confirmation
// is required, Supabase returns 200 with a user object but no tokens —
// in that case ExpiresAt is 0 and the caller should ask the user to
// check their inbox.
func (c *Client) SignUp(ctx context.Context, email, password string) (*Session, error) {
	url := strings.TrimRight(c.endpoints.SupabaseProjectURL, "/") + "/auth/v1/signup"
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.endpoints.SupabaseAnonKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase signup: %w", err)
	}
	defer resp.Body.Close()
	return parseTokenResponse(resp)
}

func parseTokenResponse(resp *http.Response) (*Session, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest {
		var sbe supabaseError
		_ = json.Unmarshal(body, &sbe)
		msg := strings.TrimSpace(sbe.Message)
		if msg == "" {
			msg = strings.TrimSpace(sbe.ErrorDescription)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("%w: %s", ErrUnauthorized, msg)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("supabase: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok supabaseTokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tok.AccessToken == "" {
		// Email-confirmation flow: no tokens issued yet.
		return &Session{UserID: tok.User.ID, UserEmail: tok.User.Email}, nil
	}
	return &Session{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		UserID:       tok.User.ID,
		UserEmail:    tok.User.Email,
		ExpiresAt:    tok.ExpiresAt,
	}, nil
}

// SignOut revokes the access token at Supabase. Best-effort: a network
// failure or a 4xx is not surfaced as an error — the caller has already
// erased the local session.
func (c *Client) SignOut(ctx context.Context, accessToken string) {
	url := strings.TrimRight(c.endpoints.SupabaseProjectURL, "/") + "/auth/v1/logout"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("apikey", c.endpoints.SupabaseAnonKey)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// MeResponse mirrors the cloud's GET /me payload. Only the fields the
// TUI actually renders are typed; ignore unknown fields gracefully.
type MeResponse struct {
	UserID        string             `json:"user_id"`
	Email         string             `json:"email"`
	Organisations []OrganisationView `json:"organisations"`
	Hubs          []HubView          `json:"hubs"`
	Hosts         []HostView         `json:"hosts"`
}

type OrganisationView struct {
	ID         string `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Region     string `json:"region"`
	FlyAppName string `json:"fly_app_name"`
	PlanID     string `json:"plan_id"`
	Role       string `json:"role"`
}

type HubView struct {
	ID        string `json:"id"`
	OrgID     string `json:"org_id"`
	Subdomain string `json:"subdomain"`
	Region    string `json:"region"`
	Status    string `json:"status"`
	PublicURL string `json:"public_url"`
}

type HostView struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Hostname   string `json:"hostname"`
	Status     string `json:"status"`
	ExternalID string `json:"external_id,omitempty"`
}

// Me fetches the authenticated user's cloud-side state. Returns
// ErrUnauthorized on 401 (token expired) and a 404-flavored error
// when the user hasn't called /signup yet.
func (c *Client) Me(ctx context.Context, accessToken string) (*MeResponse, error) {
	url := strings.TrimRight(c.endpoints.ClankctlURL, "/") + "/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud /me: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		// User authenticated but has no signup row; surface as a typed
		// error so the TUI can offer a "sign up" flow without parsing
		// strings.
		return nil, ErrNotSignedUp
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("cloud /me: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var me MeResponse
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, fmt.Errorf("parse /me: %w", err)
	}
	return &me, nil
}

// ErrNotSignedUp is returned by Me when the Supabase JWT verifies but
// no cloud-side users row exists yet. Caller should run Signup.
var ErrNotSignedUp = errors.New("cloud: user not signed up")

// SignupRequest is the body of the cloud's POST /signup.
type SignupRequest struct {
	Handle string `json:"handle"`
}

// Signup creates the the cloud-side users + personal-org rows for an
// authenticated user. Idempotency: 409 means "already signed up" and
// is mapped to ErrAlreadySignedUp so the TUI can ignore it.
func (c *Client) Signup(ctx context.Context, accessToken, handle string) error {
	url := strings.TrimRight(c.endpoints.ClankctlURL, "/") + "/signup"
	body, _ := json.Marshal(SignupRequest{Handle: handle})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("the cloud /signup: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrAlreadySignedUp
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return fmt.Errorf("the cloud /signup: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

// ErrAlreadySignedUp is returned by Signup on a 409. Idempotent re-runs
// can ignore it.
var ErrAlreadySignedUp = errors.New("cloud: user already signed up")
