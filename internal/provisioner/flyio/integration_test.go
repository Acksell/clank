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
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sprites "github.com/superfly/sprites-go"

	"github.com/acksell/clank/internal/provisioner"
	"github.com/acksell/clank/internal/provisioner/flyio"
	"github.com/acksell/clank/internal/store"
)

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("integration test: %s not set", key)
	}
	return v
}

// newIntegrationProvisioner constructs a flyio.Provisioner against a
// real Sprites account using SPRITES_TOKEN. Each test gets its own
// fresh store DB so persisted state from a previous run can't bleed
// in. SPRITES_USER is optional — the test sprite name is
// "clank-host-test-<user>".
func newIntegrationProvisioner(t *testing.T) *flyio.Provisioner {
	t.Helper()
	token := mustEnv(t, "SPRITES_TOKEN")
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	prov, err := flyio.New(flyio.Options{
		APIToken:         token,
		HubBaseURL:       "https://test.example.invalid",
		HubAuthToken:     "test-bearer-token",
		OrganizationSlug: os.Getenv("SPRITES_ORG"),
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

	const userID = "test"
	ref, err := prov.EnsureHost(ctx, userID)
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

	ref, err := prov.EnsureHost(ctx, "test")
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

	ref, err := prov.EnsureHost(ctx, "test")
	if err != nil {
		t.Fatalf("EnsureHost: %v", err)
	}
	status, body := getRoute(t, ref, "/ping")
	if status != http.StatusOK {
		t.Fatalf("/ping: HTTP %d, body=%q", status, snippet(body, 240))
	}
	t.Logf("/ping body: %s", body)
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

// _ guards against the linter complaining about unused sprites
// import — kept for future tests that need direct SDK access.
var _ = sprites.Sprite{}
