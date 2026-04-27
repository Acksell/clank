package daytona

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hostclient "github.com/acksell/clank/internal/host/client"
)

// TestNew_FailsFastOnMissingOptions pins the construction guards.
// Catching missing fields here matters because Launch is called from
// session-create on a hot path; failing earlier (boot) is much
// friendlier than failing per-session.
func TestNew_FailsFastOnMissingOptions(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		// SDKClient is nil + APIKey empty → SDK construction fails.
		{"missing-api-key", Options{HubBaseURL: "http://h", HubAuthToken: "t"}},
		{"missing-hub-url", Options{APIKey: "k", HubAuthToken: "t"}},
		{"missing-hub-token", Options{APIKey: "k", HubBaseURL: "http://h"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.opts, nil); err == nil {
				t.Errorf("New(%+v) returned nil error", c.opts)
			}
		})
	}
}

// TestSafeHostnameSuffix locks the hostname-suffix shape so any future
// schema drift in Daytona's sandbox IDs (length, separator) shows up
// here rather than in mysterious "host not registered" errors.
func TestSafeHostnameSuffix(t *testing.T) {
	cases := map[string]string{
		"sb-abc-123def456789":   "123def456789",
		"sandbox-aaa":           "aaa",
		"plain":                 "plain",
		"long-tail-abcdefghijklmnop": "abcdefghijkl", // truncated to 12
	}
	for in, want := range cases {
		if got := safeHostnameSuffix(in); got != want {
			t.Errorf("safeHostnameSuffix(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestPreviewTokenInjector verifies the RoundTripper attaches the
// `x-daytona-preview-token` header on every outbound request — that
// header is what the Daytona preview proxy uses to authenticate the
// hub-to-sandbox calls.
func TestPreviewTokenInjector(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-daytona-preview-token")
		w.WriteHeader(204)
	}))
	t.Cleanup(srv.Close)

	cli := &http.Client{Transport: &previewTokenInjector{token: "tkn"}}
	resp, err := cli.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if got != "tkn" {
		t.Errorf("preview token header: got %q, want %q", got, "tkn")
	}
}

// TestWaitForHostReady_RetriesUntilStatusOK verifies the readiness
// probe keeps polling /status until the sandbox starts answering, and
// that it surfaces the underlying error (not just "deadline exceeded")
// when it gives up — that's what makes a misconfigured sandbox
// debuggable rather than mysterious.
func TestWaitForHostReady_RetriesUntilStatusOK(t *testing.T) {
	var hits atomic.Int32
	var ready atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// First two attempts: 502 (proxy can't reach upstream).
		// Third onwards: 200 with a minimal /status payload.
		if !ready.Load() && hits.Load() < 3 {
			http.Error(w, "Bad Gateway", 502)
			return
		}
		ready.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hostname":   "fake",
			"version":    "test",
			"started_at": time.Now(),
			"sessions":   0,
		})
	}))
	t.Cleanup(srv.Close)

	l := &Launcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := hostclient.NewHTTP(srv.URL, http.DefaultTransport)
	if err := l.waitForHostReady(ctx, c, "sb-test"); err != nil {
		t.Fatalf("waitForHostReady: %v", err)
	}
	if hits.Load() < 3 {
		t.Errorf("expected at least 3 attempts before ready, got %d", hits.Load())
	}
}

// TestWaitForHostReady_TimesOutWithUnderlyingError makes sure a
// permanently-broken sandbox surfaces a useful message rather than
// just "context deadline exceeded".
func TestWaitForHostReady_TimesOutWithUnderlyingError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Bad Gateway", 502)
	}))
	t.Cleanup(srv.Close)

	l := &Launcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	c := hostclient.NewHTTP(srv.URL, http.DefaultTransport)
	err := l.waitForHostReady(ctx, c, "sb-broken")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "sb-broken") {
		t.Errorf("error should name the sandbox, got %q", err.Error())
	}
	// We don't pin the exact substring (could be "502" or "Bad Gateway"
	// depending on hostclient's error wrapping), but it must NOT be
	// just a bare deadline error.
	if errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "Bad Gateway") && !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention the underlying 502, got %q", err.Error())
	}
}
