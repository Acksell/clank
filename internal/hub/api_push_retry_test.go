package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitcred"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
)

// flakyDisc returns a different token each call so the test can verify
// that the second push attempt got a re-discovered credential, not the
// stale one served from cache.
type flakyDisc struct {
	calls atomic.Int32
}

func (f *flakyDisc) Discover(_ context.Context, _ *agent.GitEndpoint) (agent.GitCredential, error) {
	n := f.calls.Add(1)
	pwd := "tok-stale"
	if n > 1 {
		pwd = "tok-fresh"
	}
	return agent.GitCredential{
		Kind: agent.GitCredHTTPSBasic, Username: "x-access-token", Password: pwd,
	}, nil
}

// TestPushBranchOnHost_RetriesAndInvalidatesOnAuthFailure is the
// regression test for the daytona-push bug: a stale cached token must
// be evicted on 401 and a second discovery attempt made before
// surfacing the error.
func TestPushBranchOnHost_RetriesAndInvalidatesOnAuthFailure(t *testing.T) {
	t.Parallel()

	var pushCalls atomic.Int32
	var seenPasswords []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/worktrees/push") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Auth agent.GitCredential `json:"auth"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenPasswords = append(seenPasswords, body.Auth.Password)

		n := pushCalls.Add(1)
		if n == 1 {
			// First attempt: simulate stale-token rejection.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code":  "push_auth_required",
				"error": "fatal: could not read Username for 'https://github.com'",
			})
			return
		}
		// Second attempt: success.
		_ = json.NewEncoder(w).Encode(host.PushResult{
			Branch: "feature/x", Remote: "origin", CommitsAhead: 1,
		})
	}))
	t.Cleanup(srv.Close)

	s := New()
	s.SetCredentialDiscoverer(NewCachingDiscoverer(gitcred.Stack{
		Discoverers: []gitcred.Discoverer{&flakyDisc{}},
	}))
	if _, err := s.RegisterHost("daytona-1", hostclient.NewHTTP(srv.URL, nil)); err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}

	ref := agent.GitRef{Endpoint: &agent.GitEndpoint{
		Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "owner/repo",
	}}
	res, err := s.PushBranchOnHost(context.Background(), "daytona-1", ref, "feature/x")
	if err != nil {
		t.Fatalf("PushBranchOnHost: %v", err)
	}
	if res.Branch != "feature/x" {
		t.Fatalf("res.Branch = %q", res.Branch)
	}
	if pushCalls.Load() != 2 {
		t.Fatalf("pushCalls = %d, want 2 (one fail, one retry)", pushCalls.Load())
	}
	// The retry must have re-run discovery — first call sees stale,
	// second sees fresh. If invalidation failed, both would be stale.
	if len(seenPasswords) != 2 || seenPasswords[0] != "tok-stale" || seenPasswords[1] != "tok-fresh" {
		t.Fatalf("seenPasswords = %v, want [tok-stale tok-fresh]", seenPasswords)
	}
}

// TestPushBranchOnHost_PersistentAuthFailureWrapsAsStructuredError
// asserts that when retry ALSO fails, the error carries the
// (hostname, endpointHost) the TUI needs to render a credential
// modal — and still satisfies errors.Is(host.ErrPushAuthRequired) so
// intermediate layers don't have to know about the wrapper.
func TestPushBranchOnHost_PersistentAuthFailureWrapsAsStructuredError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code":  "push_auth_required",
			"error": "fatal: auth still missing",
		})
	}))
	t.Cleanup(srv.Close)

	s := New()
	if _, err := s.RegisterHost("daytona-1", hostclient.NewHTTP(srv.URL, nil)); err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}

	ref := agent.GitRef{Endpoint: &agent.GitEndpoint{
		Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "owner/repo",
	}}
	_, err := s.PushBranchOnHost(context.Background(), "daytona-1", ref, "feature/x")
	if err == nil {
		t.Fatal("expected error after persistent auth failure")
	}
	if !errors.Is(err, host.ErrPushAuthRequired) {
		t.Fatalf("err = %v, want errors.Is(ErrPushAuthRequired)", err)
	}
	var structured *host.PushAuthRequiredError
	if !errors.As(err, &structured) {
		t.Fatalf("err = %v, want errors.As(*PushAuthRequiredError)", err)
	}
	if structured.Hostname != "daytona-1" {
		t.Errorf("Hostname = %q, want daytona-1", structured.Hostname)
	}
	if structured.EndpointHost != "github.com" {
		t.Errorf("EndpointHost = %q, want github.com", structured.EndpointHost)
	}
}
