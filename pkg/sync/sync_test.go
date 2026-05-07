package sync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/acksell/clank/pkg/provisioner"
)

// fakeProvisioner returns a constant HostRef and counts EnsureHost calls.
type fakeProvisioner struct {
	target  string
	calls   int
	authTok string
}

func (f *fakeProvisioner) EnsureHost(ctx context.Context, userID string) (provisioner.HostRef, error) {
	f.calls++
	return provisioner.HostRef{
		HostID:    "h-" + userID,
		URL:       f.target,
		AuthToken: f.authTok,
	}, nil
}
func (f *fakeProvisioner) SuspendHost(context.Context, string) error { return nil }
func (f *fakeProvisioner) DestroyHost(context.Context, string) error { return nil }

// TestPushAndFlush_RoundTripsBundle verifies that a bundle pushed via
// /v1/bundles ends up POSTed to the sandbox's /sync/apply during flush,
// with the correct repo query param and bearer token.
func TestPushAndFlush_RoundTripsBundle(t *testing.T) {
	t.Parallel()
	var (
		gotPath  string
		gotQuery url.Values
		gotAuth  string
		gotBody  []byte
	)
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sandbox.Close)

	prov := &fakeProvisioner{target: sandbox.URL, authTok: "host-token"}
	srv, err := NewServer(Config{
		Provisioner: prov,
		// Tight debounce so the test doesn't sit idle for 30s.
		FlushDebounce: 50 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Stop)

	gw := httptest.NewServer(srv.Handler())
	t.Cleanup(gw.Close)

	body := strings.NewReader("FAKE-BUNDLE-BYTES")
	req, _ := http.NewRequest("POST", gw.URL+"/v1/bundles?repo=myrepo", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST bundle: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("push status: got %d, want 202", resp.StatusCode)
	}

	// Wait for the debounce timer + the flush goroutine to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if prov.calls > 0 && string(gotBody) == "FAKE-BUNDLE-BYTES" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if prov.calls != 1 {
		t.Fatalf("expected 1 EnsureHost call, got %d", prov.calls)
	}
	if gotPath != "/sync/apply" {
		t.Errorf("path: got %q, want /sync/apply", gotPath)
	}
	if gotQuery.Get("repo") != "myrepo" {
		t.Errorf("query repo: got %q, want myrepo", gotQuery.Get("repo"))
	}
	if gotAuth != "Bearer host-token" {
		t.Errorf("auth: got %q, want %q", gotAuth, "Bearer host-token")
	}
	if string(gotBody) != "FAKE-BUNDLE-BYTES" {
		t.Errorf("body: got %q, want FAKE-BUNDLE-BYTES", string(gotBody))
	}
}

// TestPush_RejectsBadRepoSlug ensures path-traversal-shaped slugs are
// rejected before they reach the sandbox.
func TestPush_RejectsBadRepoSlug(t *testing.T) {
	t.Parallel()
	srv, err := NewServer(Config{Provisioner: &fakeProvisioner{}}, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Stop)
	gw := httptest.NewServer(srv.Handler())
	t.Cleanup(gw.Close)

	for _, slug := range []string{"", "..", "foo/bar", "foo/../bar", `with\backslash`} {
		req, _ := http.NewRequest("POST", gw.URL+"/v1/bundles?repo="+url.QueryEscape(slug), strings.NewReader("x"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %q: %v", slug, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("slug %q: got status %d, want 400", slug, resp.StatusCode)
		}
	}
}

// TestExplicitFlush_BypassesDebounce ensures the cloud's wake-time
// flush call (Flush(ctx, userID)) doesn't wait for the debounce timer.
func TestExplicitFlush_BypassesDebounce(t *testing.T) {
	t.Parallel()
	calls := 0
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sandbox.Close)

	prov := &fakeProvisioner{target: sandbox.URL}
	// Long debounce — explicit Flush must work without waiting.
	srv, err := NewServer(Config{Provisioner: prov, FlushDebounce: time.Hour}, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(srv.Stop)
	gw := httptest.NewServer(srv.Handler())
	t.Cleanup(gw.Close)

	req, _ := http.NewRequest("POST", gw.URL+"/v1/bundles?repo=r", strings.NewReader("data"))
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	if err := srv.Flush(context.Background(), "local"); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected sandbox to be hit once, got %d", calls)
	}
}
