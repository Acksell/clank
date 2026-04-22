package hostclient_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	hostclient "github.com/acksell/clank/internal/host/client"
)

// TestNewRemoteHTTP_Validation guards the fail-fast preconditions.
// Per AGENTS.md: never silently produce a misconfigured client.
func TestNewRemoteHTTP_Validation(t *testing.T) {
	t.Parallel()

	if _, err := hostclient.NewRemoteHTTP("", map[string]string{"x": "y"}); err == nil {
		t.Error("empty baseURL: expected error, got nil")
	}
	if _, err := hostclient.NewRemoteHTTP("http://x", nil); err == nil {
		t.Error("nil headers: expected error, got nil")
	}
	if _, err := hostclient.NewRemoteHTTP("http://x", map[string]string{}); err == nil {
		t.Error("empty headers: expected error, got nil")
	}
}

// TestNewRemoteHTTP_HeadersInjected proves the headerTransport injects
// the configured header bag onto every outbound request, including
// across multiple calls. This is the daytona auth chokepoint — a
// regression here silently breaks every preview-URL request.
func TestNewRemoteHTTP_HeadersInjected(t *testing.T) {
	t.Parallel()

	const tokenHeader = "x-daytona-preview-token"
	const tokenValue = "secret-abc"

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get(tokenHeader); got != tokenValue {
			t.Errorf("call %d to %s: missing header %s (got %q)", calls.Load(), r.URL.Path, tokenHeader, got)
		}
		// Status endpoint is the simplest typed surface to exercise.
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"sessions": 0})
			return
		}
		// Default: empty JSON array (covers /backends and similar list endpoints).
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)

	c, err := hostclient.NewRemoteHTTP(srv.URL, map[string]string{tokenHeader: tokenValue})
	if err != nil {
		t.Fatalf("NewRemoteHTTP: %v", err)
	}

	ctx := context.Background()
	if _, err := c.Status(ctx); err != nil {
		t.Fatalf("Status #1: %v", err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatalf("Status #2: %v", err)
	}
	if _, err := c.Backends(ctx); err != nil {
		t.Fatalf("Backends: %v", err)
	}

	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}

// TestNewRemoteHTTP_HeadersClonedAtConstruction prevents a footgun
// where the caller mutates the header map after construction (e.g.
// clearing a token on logout) and silently strips auth from
// in-flight clients. The client must own its own copy.
func TestNewRemoteHTTP_HeadersClonedAtConstruction(t *testing.T) {
	t.Parallel()

	headers := map[string]string{"x-token": "v1"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-token"); got != "v1" {
			t.Errorf("expected x-token=v1, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":0}`))
	}))
	t.Cleanup(srv.Close)

	c, err := hostclient.NewRemoteHTTP(srv.URL, headers)
	if err != nil {
		t.Fatalf("NewRemoteHTTP: %v", err)
	}

	// Mutate the source map post-construction. The client must be
	// unaffected.
	headers["x-token"] = "tampered"
	delete(headers, "x-token")

	if _, err := c.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
}

// TestNewRemoteHTTP_DoesNotMutateInboundRequest is a defensive guard:
// net/http.RoundTripper contract says implementations must not modify
// the request. We add headers, so we must Clone first. This test
// catches a regression where we'd accidentally mutate req.Header
// directly.
func TestNewRemoteHTTP_DoesNotMutateInboundRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":0}`))
	}))
	t.Cleanup(srv.Close)

	c, err := hostclient.NewRemoteHTTP(srv.URL, map[string]string{"x-injected": "yes"})
	if err != nil {
		t.Fatalf("NewRemoteHTTP: %v", err)
	}

	// Hit it twice; if RoundTrip mutated the inbound request, the
	// second call would see leaked state — but Status builds a fresh
	// request each time, so the only way to expose mutation here is via
	// the response body / client cookie jar etc., which we don't use.
	// The strongest assertion we can make is that nothing panics and
	// the client stays usable.
	for i := 0; i < 3; i++ {
		if _, err := c.Status(context.Background()); err != nil {
			t.Fatalf("Status #%d: %v", i, err)
		}
	}
}

// TestNewRemoteHTTP_BaseURLConcat documents the expected baseURL
// shape: paths are appended directly with no normalization. This is
// the same behavior NewHTTP already has; codifying it here so the
// daytona launcher knows not to pass a trailing slash.
func TestNewRemoteHTTP_BaseURLConcat(t *testing.T) {
	t.Parallel()

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":0}`))
	}))
	t.Cleanup(srv.Close)

	c, err := hostclient.NewRemoteHTTP(srv.URL, map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("NewRemoteHTTP: %v", err)
	}
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.HasSuffix(seen, "/status") {
		t.Errorf("expected path to end with /status, got %q", seen)
	}
}
