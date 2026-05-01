package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// newTestAuthManager constructs an AuthManager pinned to a temp dir
// (so writes don't touch the real $HOME) and a no-op restart hook.
// homeDir is exposed for assertions on the on-disk auth.json.
func newTestAuthManager(t *testing.T) (*AuthManager, string) {
	t.Helper()
	dir := t.TempDir()
	a := &AuthManager{
		homeDir: dir,
		restart: func(context.Context) error { return nil },
		flows:   make(map[string]*flowState),
		httpc:   &http.Client{Timeout: 5 * time.Second},
	}
	return a, dir
}

func TestAuthManager_ListProviders_EmptyFile(t *testing.T) {
	t.Parallel()
	a, _ := newTestAuthManager(t)

	infos, err := a.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(infos))
	}
	if infos[0].ProviderID != ProviderGitHubCopilot {
		t.Fatalf("expected %s, got %s", ProviderGitHubCopilot, infos[0].ProviderID)
	}
	if infos[0].Connected {
		t.Errorf("expected Connected=false on a fresh sandbox")
	}
}

func TestAuthManager_WriteAndReadAuthJSON(t *testing.T) {
	t.Parallel()
	a, home := newTestAuthManager(t)

	cred := agent.AuthCredential{
		Type:    "oauth",
		Refresh: "tok",
		Access:  "tok",
		Expires: 0,
	}
	if err := a.writeAuthJSON("github-copilot", cred); err != nil {
		t.Fatalf("writeAuthJSON: %v", err)
	}

	// File should exist with the expected layout.
	path := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var got map[string]agent.AuthCredential
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode auth.json: %v", err)
	}
	if got["github-copilot"].Refresh != "tok" {
		t.Errorf("expected refresh=tok, got %+v", got["github-copilot"])
	}

	// ListProviders should now report connected.
	infos, _ := a.ListProviders(context.Background())
	if !infos[0].Connected {
		t.Errorf("expected connected after write")
	}

	// File mode should be 0o600 (perm-restricted credentials).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("expected 0o600, got %o", perm)
	}
}

func TestAuthManager_DeleteCredentialRoundTrip(t *testing.T) {
	t.Parallel()
	a, _ := newTestAuthManager(t)

	cred := agent.AuthCredential{Type: "oauth", Refresh: "tok", Access: "tok"}
	if err := a.writeAuthJSON("github-copilot", cred); err != nil {
		t.Fatalf("write: %v", err)
	}

	var restartCalls int32
	a.restart = func(context.Context) error { atomic.AddInt32(&restartCalls, 1); return nil }

	if err := a.DeleteCredential(context.Background(), "github-copilot"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	if got := atomic.LoadInt32(&restartCalls); got != 1 {
		t.Errorf("expected 1 restart call, got %d", got)
	}

	infos, _ := a.ListProviders(context.Background())
	if infos[0].Connected {
		t.Errorf("expected disconnected after delete")
	}
}

func TestAuthManager_StartDeviceFlow_RejectsUnknownProvider(t *testing.T) {
	t.Parallel()
	a, _ := newTestAuthManager(t)
	if _, err := a.StartDeviceFlow(context.Background(), "unknown-provider"); err == nil {
		t.Fatalf("expected ErrUnknownProvider, got nil")
	}
}

// TestAuthManager_FullDeviceFlow_Success drives the end-to-end happy
// path with a stub GitHub server. Verifies the goroutine walks
// pending → authorized → success, writes auth.json, and triggers the
// restart hook.
func TestAuthManager_FullDeviceFlow_Success(t *testing.T) {
	t.Parallel()
	a, home := newTestAuthManager(t)

	// First poll returns authorization_pending; second returns the
	// access token. This exercises both code paths.
	var pollCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-abc",
				"user_code":        "USER-CODE",
				"verification_uri": "https://github.com/login/device",
				"expires_in":       900,
				"interval":         1, // tight to keep the test fast
			})
		case "/login/oauth/access_token":
			n := atomic.AddInt32(&pollCount, 1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "the-token"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Redirect outbound calls to our stub server. The HTTP client's
	// transport rewrites github.com → srv.URL.
	a.httpc = &http.Client{Transport: rewriteTransport(srv.URL)}

	var restartCalls int32
	a.restart = func(context.Context) error { atomic.AddInt32(&restartCalls, 1); return nil }

	start, err := a.StartDeviceFlow(context.Background(), ProviderGitHubCopilot)
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	if start.UserCode != "USER-CODE" {
		t.Errorf("expected USER-CODE, got %s", start.UserCode)
	}

	// Poll status until terminal. Use a generous deadline since the
	// flow goroutine sleeps `interval+safetyMargin` between polls.
	deadline := time.Now().Add(15 * time.Second)
	var finalState agent.DeviceFlowState
	for time.Now().Before(deadline) {
		status, err := a.GetFlowStatus(context.Background(), start.FlowID)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		finalState = status.State
		if status.State == agent.DeviceFlowSuccess ||
			status.State == agent.DeviceFlowError ||
			status.State == agent.DeviceFlowDenied {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if finalState != agent.DeviceFlowSuccess {
		t.Fatalf("expected success, got %s", finalState)
	}
	if got := atomic.LoadInt32(&restartCalls); got != 1 {
		t.Errorf("expected 1 restart call, got %d", got)
	}

	// auth.json should contain the token under github-copilot.
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	var stored map[string]agent.AuthCredential
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode auth.json: %v", err)
	}
	if stored[ProviderGitHubCopilot].Access != "the-token" {
		t.Errorf("expected access=the-token, got %+v", stored[ProviderGitHubCopilot])
	}
	if stored[ProviderGitHubCopilot].Type != "oauth" {
		t.Errorf("expected type=oauth, got %s", stored[ProviderGitHubCopilot].Type)
	}
}

// TestAuthManager_FullDeviceFlow_AccessDenied verifies the goroutine
// surfaces denial back through the flow state.
func TestAuthManager_FullDeviceFlow_AccessDenied(t *testing.T) {
	t.Parallel()
	a, _ := newTestAuthManager(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-abc",
				"user_code":        "USER-CODE",
				"verification_uri": "https://github.com/login/device",
				"expires_in":       900,
				"interval":         1,
			})
		case "/login/oauth/access_token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "access_denied"})
		}
	}))
	defer srv.Close()
	a.httpc = &http.Client{Transport: rewriteTransport(srv.URL)}

	start, err := a.StartDeviceFlow(context.Background(), ProviderGitHubCopilot)
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := a.GetFlowStatus(context.Background(), start.FlowID)
		if status.State == agent.DeviceFlowDenied {
			return
		}
		if status.State == agent.DeviceFlowError ||
			status.State == agent.DeviceFlowSuccess {
			t.Fatalf("expected denied, got %s", status.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("flow did not reach denied state in time")
}

// rewriteTransport redirects any request to https://github.com/...
// to the test server URL, so the AuthManager's hardcoded GitHub
// endpoints can be intercepted without exposing a base-URL config knob.
func rewriteTransport(target string) http.RoundTripper {
	u, _ := url.Parse(target)
	return &rewriteRT{target: u}
}

type rewriteRT struct{ target *url.URL }

func (rt *rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "github.com" || strings.HasSuffix(req.URL.Host, ".github.com") {
		req = req.Clone(req.Context())
		req.URL.Scheme = rt.target.Scheme
		req.URL.Host = rt.target.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}
