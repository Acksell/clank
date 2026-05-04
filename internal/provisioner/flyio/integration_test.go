package flyio_test

// Real-sprite integration tests. Skipped unless SPRITES_TOKEN is in
// the env so CI / local `go test ./...` stays fast and sandbox-free.
// Run explicitly with:
//
//   SPRITES_TOKEN=$YOUR_TOKEN go test -count=1 -run TestIntegration_FlyIO ./internal/provisioner/flyio/...
//
// These were added to iterate on a production bug where the gateway
// proxied /events to a sprite and the sprite returned
// "404 | Sprites" (the Sprites edge "no service bound" page) — even
// though /status on the same sprite returned 200. The unit tests in
// install_test.go and ready_test.go pass, but the bug only reproduces
// against a real sprite; the in-sprite probe added in
// diagnoseInSprite logs each route's status when running, so we can
// tell whether the running clank-host actually registers /events or
// the Sprites edge is intercepting it.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sprites "github.com/superfly/sprites-go"

	"github.com/acksell/clank/internal/gateway"
	"github.com/acksell/clank/internal/provisioner"
	"github.com/acksell/clank/internal/provisioner/flyio"
	"github.com/acksell/clank/internal/store"
)

// newGatewayHandler builds a real gateway.Gateway in front of prov
// so the test can exercise the same proxy path the cloud-hub
// daemoncli mounts. PermissiveAuth lets every request through;
// ResolveUserID hardcodes "local" because that's the user the cloud-
// hub flow uses (see internal/cli/daemoncli/server.go).
func newGatewayHandler(prov provisioner.Provisioner) (http.Handler, error) {
	gw, err := gateway.NewGateway(gateway.Config{
		Provisioner:   prov,
		Auth:          gateway.PermissiveAuth{},
		ResolveUserID: func(*http.Request) string { return "local" },
	}, nil)
	if err != nil {
		return nil, err
	}
	return gw.Handler(), nil
}

// loadFlyIOCreds resolves Sprites credentials with this precedence:
//
//  1. SPRITES_TOKEN env (canonical CI/dev override)
//  2. CLANK_DIR/preferences.json (the cloud-hub setup the user
//     already wired for `make cloud-hub`)
//  3. ~/.clank-cloud/preferences.json (the dev default for cloud-hub)
//
// Returns "" + skip if none yield a token. Lets the integration
// tests run from a plain `go test` without exposing the token on
// the command line.
func loadFlyIOCreds(t *testing.T) (token, org string) {
	t.Helper()
	if v := os.Getenv("SPRITES_TOKEN"); v != "" {
		return v, os.Getenv("SPRITES_ORG")
	}
	candidates := []string{}
	if dir := os.Getenv("CLANK_DIR"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "preferences.json"))
	}
	if home, _ := os.UserHomeDir(); home != "" {
		candidates = append(candidates, filepath.Join(home, ".clank-cloud", "preferences.json"))
	}
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var prefs struct {
			FlyIO struct {
				APIToken         string `json:"api_token"`
				OrganizationSlug string `json:"organization_slug"`
			} `json:"flyio"`
		}
		if err := json.Unmarshal(raw, &prefs); err != nil {
			continue
		}
		if prefs.FlyIO.APIToken != "" {
			return prefs.FlyIO.APIToken, prefs.FlyIO.OrganizationSlug
		}
	}
	t.Skip("integration test: SPRITES_TOKEN not set and no flyio.api_token in preferences.json (CLANK_DIR/~/.clank-cloud)")
	return "", ""
}

// integrationSpriteName names the sprite all integration tests
// share. We adopt-or-recreate it per test run rather than minting a
// new one each time — leaks would pile up on the user's account.
const integrationUserID = "test"
const integrationSpriteName = "clank-host-test"

// newIntegrationProvisioner constructs a flyio.Provisioner against a
// real Sprites account using credentials from env or
// preferences.json. Cleans up any orphan sprite from a previous run
// (the test DB is fresh each time, so resolveOrCreate would
// otherwise fail with "name already exists").
func newIntegrationProvisioner(t *testing.T) *flyio.Provisioner {
	t.Helper()
	token, org := loadFlyIOCreds(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Best-effort upfront cleanup. A leftover sprite from a previous
	// test run would block resolveOrCreate.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rawClient := sprites.New(token)
	if err := rawClient.DeleteSprite(cleanupCtx, integrationSpriteName); err != nil && !strings.Contains(err.Error(), "404") {
		t.Logf("integration test: pre-cleanup of %s: %v (continuing)", integrationSpriteName, err)
	}

	prov, err := flyio.New(flyio.Options{
		APIToken:         token,
		HubBaseURL:       "https://test.example.invalid",
		HubAuthToken:     "test-bearer-token",
		OrganizationSlug: org,
	}, st, nil)
	if err != nil {
		t.Fatalf("flyio.New: %v", err)
	}
	return prov
}

// TestIntegration_FlyIO_EventsRouteIsReachable is the headline
// regression: provisions (or reuses) a sprite, then GETs /events on
// the public URL with the auth token. The expected response is HTTP
// 200 with a `Content-Type: text/event-stream` header — clank-host
// always emits an `event: connected` frame as the first thing on
// /events, so a quick read confirms the route is alive.
//
// If the public URL returns "404 | Sprites" but /status returns 200
// (the production symptom), this test FAILS. We then look at the
// daemon logs (diagnoseInSprite output) to learn whether the running
// binary is missing the route or the edge is intercepting it.
func TestIntegration_FlyIO_EventsRouteIsReachable(t *testing.T) {
	prov := newIntegrationProvisioner(t)
	t.Cleanup(prov.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ref, err := prov.EnsureHost(ctx, integrationUserID)
	if err != nil {
		t.Fatalf("EnsureHost: %v", err)
	}
	if ref.URL == "" {
		t.Fatal("EnsureHost returned empty URL")
	}

	// Probe /status first to confirm we're hitting the host's mux.
	statusOK, statusBody := getRoute(t, ref, "/status")
	if statusOK != http.StatusOK {
		t.Fatalf("/status: HTTP %d, body=%q (host mux not responding)", statusOK, statusBody)
	}

	// Now probe /events. Read the first chunk to verify it's a real
	// SSE stream, not an HTML 404 page from the Sprites edge.
	body, status, ct := getRouteStream(t, ref, "/events", 3*time.Second)
	if status != http.StatusOK {
		t.Errorf("/events: HTTP %d (expected 200), Content-Type=%q, body=%q", status, ct, snippet(string(body), 240))
	}
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("/events Content-Type=%q (expected text/event-stream)", ct)
	}
	if !strings.Contains(string(body), "event: connected") {
		t.Errorf("/events did not emit `event: connected` frame; body=%q", snippet(string(body), 240))
	}
}

// TestIntegration_FlyIO_StatusRouteIsReachable is a sanity check for
// the integration setup itself: if even /status fails, every other
// test will fail and the failure mode is uninformative. This isolates
// "can the test reach the sprite at all" from "does /events work".
func TestIntegration_FlyIO_StatusRouteIsReachable(t *testing.T) {
	prov := newIntegrationProvisioner(t)
	t.Cleanup(prov.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ref, err := prov.EnsureHost(ctx, integrationUserID)
	if err != nil {
		t.Fatalf("EnsureHost: %v", err)
	}
	status, body := getRoute(t, ref, "/status")
	if status != http.StatusOK {
		t.Fatalf("/status: HTTP %d, body=%q", status, snippet(body, 240))
	}
}

// TestIntegration_FlyIO_PingRouteIsReachable confirms /ping works
// too. /ping returns the running clank-host's version + PID so the
// test output identifies which version is responding (helpful when
// chasing stale-binary bugs).
func TestIntegration_FlyIO_PingRouteIsReachable(t *testing.T) {
	prov := newIntegrationProvisioner(t)
	t.Cleanup(prov.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ref, err := prov.EnsureHost(ctx, integrationUserID)
	if err != nil {
		t.Fatalf("EnsureHost: %v", err)
	}
	status, body := getRoute(t, ref, "/ping")
	if status != http.StatusOK {
		t.Fatalf("/ping: HTTP %d, body=%q", status, snippet(body, 240))
	}
	t.Logf("/ping body: %s", body)
}

// TestIntegration_FlyIO_RealSpriteEventsRoute exercises the user's
// actual cloud-hub sprite (clank-host-local) without recreating it.
// This is the exact thing the TUI hits — including any stale URL
// settings or routing rules that may have accumulated on it across
// previous daemon versions.
//
// Skipped automatically when the sprite doesn't exist or when there's
// no host row for "local" in the user's clank.db. Run with the same
// SPRITES_TOKEN credential the other integration tests use.
//
// Useful runs:
//
//	CLANK_DIR=$HOME/.clank-cloud go test -count=1 -run TestIntegration_FlyIO_RealSpriteEventsRoute -v ./internal/provisioner/flyio/...
func TestIntegration_FlyIO_RealSpriteEventsRoute(t *testing.T) {
	token, org := loadFlyIOCreds(t)
	clankDir := os.Getenv("CLANK_DIR")
	if clankDir == "" {
		home, _ := os.UserHomeDir()
		clankDir = filepath.Join(home, ".clank-cloud")
	}
	dbPath := filepath.Join(clankDir, "clank.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("real-sprite test: %s missing — run `make cloud-hub` once first to provision", dbPath)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	prov, err := flyio.New(flyio.Options{
		APIToken:         token,
		HubBaseURL:       "https://test.example.invalid",
		HubAuthToken:     "test-bearer-token",
		OrganizationSlug: org,
	}, st, nil)
	if err != nil {
		t.Fatalf("flyio.New: %v", err)
	}
	t.Cleanup(prov.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ref, err := prov.EnsureHost(ctx, "local")
	if err != nil {
		t.Fatalf("EnsureHost(local): %v", err)
	}
	t.Logf("real sprite URL: %s", ref.URL)

	body, status, ct := getRouteStream(t, ref, "/events", 3*time.Second)
	t.Logf("real sprite /events: HTTP %d, Content-Type=%q", status, ct)
	t.Logf("real sprite /events body snippet: %s", snippet(string(body), 240))
	if status != http.StatusOK {
		t.Errorf("real sprite /events returned HTTP %d (TUI bug repro)", status)
	}
}

// getRoute does a short-timeout GET against ref.URL+path using the
// HostRef's transport (so auth is applied). Returns status and body.
func getRoute(t *testing.T, ref provisioner.HostRef, path string) (int, string) {
	t.Helper()
	cli := &http.Client{Transport: ref.Transport, Timeout: 10 * time.Second}
	url := strings.TrimRight(ref.URL, "/") + path
	resp, err := cli.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	return resp.StatusCode, string(body)
}

// getRouteStream is like getRoute but caps the body read at a short
// duration — for SSE endpoints that hold the connection open
// indefinitely, we just want enough to verify the first frame.
func getRouteStream(t *testing.T, ref provisioner.HostRef, path string, dur time.Duration) (body []byte, status int, contentType string) {
	t.Helper()
	cli := &http.Client{Transport: ref.Transport}
	url := strings.TrimRight(ref.URL, "/") + path
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := cli.Do(req)
	if err != nil {
		// Timeout reading the body is OK — we got the response start.
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, ""
		}
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	contentType = resp.Header.Get("Content-Type")
	status = resp.StatusCode
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	return body, status, contentType
}

// snippet trims body strings for log readability.
func snippet(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// TestIntegration_FlyIO_GatewayProxyToRealSpriteEvents reproduces the
// TUI bug as faithfully as possible without standing up cloudflared:
// it mounts our actual gateway in front of the user's real flyio
// provisioner, then SSE-subscribes through the gateway and reads
// the first frame.
//
// Failure modes the gateway path could introduce that the direct
// sprite test wouldn't see:
//   - httputil.ReverseProxy buffering the SSE response
//   - The gateway's stripHostsPrefix mangling /events
//   - The bearerInjector chain breaking when called via Rewrite
//   - WithoutCancel context not propagating something needed
//
// Run with:
//
//	CLANK_DIR=$HOME/.clank-cloud go test -count=1 -run TestIntegration_FlyIO_GatewayProxyToRealSpriteEvents -v ./internal/provisioner/flyio/...
func TestIntegration_FlyIO_GatewayProxyToRealSpriteEvents(t *testing.T) {
	token, org := loadFlyIOCreds(t)
	clankDir := os.Getenv("CLANK_DIR")
	if clankDir == "" {
		home, _ := os.UserHomeDir()
		clankDir = filepath.Join(home, ".clank-cloud")
	}
	dbPath := filepath.Join(clankDir, "clank.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skipf("gateway-proxy test: %s missing — run `make cloud-hub` once first", dbPath)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open user store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	prov, err := flyio.New(flyio.Options{
		APIToken:         token,
		HubBaseURL:       "https://test.example.invalid",
		HubAuthToken:     "test-bearer-token",
		OrganizationSlug: org,
	}, st, nil)
	if err != nil {
		t.Fatalf("flyio.New: %v", err)
	}
	t.Cleanup(prov.Stop)

	gw, err := newGatewayHandler(prov)
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	t.Logf("gateway listening on %s; proxying to real flyio sprite for user 'local'", srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events through gateway: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	t.Logf("gateway → /events: HTTP %d, Content-Type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	t.Logf("gateway → /events body snippet: %s", snippet(string(body), 240))
	if resp.StatusCode != http.StatusOK {
		t.Errorf("gateway → /events returned HTTP %d (matches TUI bug)", resp.StatusCode)
	}
}

// _ guards against the linter complaining about unused sprites
// import — kept for future tests that need direct SDK access.
var _ = sprites.Sprite{}
