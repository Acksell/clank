package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitcred"
	"github.com/acksell/clank/internal/host"
)

// stubDisc is a minimal credDiscoverer for resolver tests. We don't
// reach for [CachingDiscoverer] here because the tested behaviour
// (consult + propagate) is independent of caching.
type stubDisc struct {
	cred  agent.GitCredential
	err   error
	calls int
}

func (s *stubDisc) DiscoverFor(_ context.Context, _ host.Hostname, _ *agent.GitEndpoint) (agent.GitCredential, error) {
	s.calls++
	return s.cred, s.err
}

func TestResolveCredential_HTTPSWithDiscovererHit(t *testing.T) {
	t.Parallel()
	disc := &stubDisc{cred: agent.GitCredential{
		Kind: agent.GitCredHTTPSBasic, Username: "x-access-token", Password: "tok",
	}}
	cred, ep, err := ResolveCredential(context.Background(), "daytona-1",
		&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}, disc)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Kind != agent.GitCredHTTPSBasic || cred.Password != "tok" {
		t.Fatalf("cred = %+v, want https_basic with tok", cred.Redacted())
	}
	if ep.Protocol != agent.GitProtoHTTPS {
		t.Fatalf("proto = %q", ep.Protocol)
	}
	if disc.calls != 1 {
		t.Fatalf("disc.calls = %d, want 1", disc.calls)
	}
}

func TestResolveCredential_HTTPSDiscovererMissFallsBackAnonymous(t *testing.T) {
	t.Parallel()
	disc := &stubDisc{err: gitcred.ErrNoCredential}
	cred, _, err := ResolveCredential(context.Background(), "daytona-1",
		&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}, disc)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Kind != agent.GitCredAnonymous {
		t.Fatalf("kind = %q, want anonymous on soft miss", cred.Kind)
	}
}

func TestResolveCredential_HTTPSDiscovererHardErrorPropagates(t *testing.T) {
	t.Parallel()
	disc := &stubDisc{err: fmt.Errorf("settings file is corrupt")}
	_, _, err := ResolveCredential(context.Background(), "daytona-1",
		&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}, disc)
	if err == nil {
		t.Fatal("want hard error to propagate")
	}
}

func TestResolveCredential_SSHRewriteAlsoConsultsDiscoverer(t *testing.T) {
	t.Parallel()
	// Regression: the daytona use case. SSH remote URL must rewrite to
	// HTTPS AND get the discovered token attached, otherwise push 401s.
	disc := &stubDisc{cred: agent.GitCredential{
		Kind: agent.GitCredHTTPSBasic, Username: "x-access-token", Password: "ghp_tok",
	}}
	cred, ep, err := ResolveCredential(context.Background(), "daytona-1",
		&agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "a/b"}, disc)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if ep.Protocol != agent.GitProtoHTTPS {
		t.Fatalf("proto = %q, want https rewrite", ep.Protocol)
	}
	if cred.Kind != agent.GitCredHTTPSBasic || cred.Password != "ghp_tok" {
		t.Fatalf("cred = %s, want token attached", cred.Redacted())
	}
}

func TestResolveCredential_LocalSSHSkipsDiscoverer(t *testing.T) {
	t.Parallel()
	// Local target keeps using ssh-agent; the discoverer is a no-op
	// for SSH-on-local. (HTTPS-on-local still consults it, since a
	// user might want to clone over HTTPS+token from the local host.)
	disc := &stubDisc{}
	cred, _, err := ResolveCredential(context.Background(), host.HostLocal,
		&agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "a/b"}, disc)
	if err != nil {
		t.Fatalf("ResolveCredential: %v", err)
	}
	if cred.Kind != agent.GitCredSSHAgent {
		t.Fatalf("kind = %q, want ssh_agent", cred.Kind)
	}
	if disc.calls != 0 {
		t.Fatalf("disc.calls = %d, want 0 (ssh-on-local must not consult discovery)", disc.calls)
	}
}

func TestCachingDiscoverer_HitSkipsInner(t *testing.T) {
	t.Parallel()
	inner := &countingInner{cred: agent.GitCredential{
		Kind: agent.GitCredHTTPSBasic, Username: "x-access-token", Password: "tok",
	}}
	c := NewCachingDiscoverer(inner)
	ep := &agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}

	if _, err := c.DiscoverFor(context.Background(), "daytona-1", ep); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := c.DiscoverFor(context.Background(), "daytona-1", ep); err != nil {
		t.Fatalf("second: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner.calls = %d, want 1 (second call should be cache hit)", inner.calls)
	}
}

func TestCachingDiscoverer_InvalidateForcesRediscover(t *testing.T) {
	t.Parallel()
	inner := &countingInner{cred: agent.GitCredential{
		Kind: agent.GitCredHTTPSBasic, Username: "x-access-token", Password: "tok",
	}}
	c := NewCachingDiscoverer(inner)
	ep := &agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}

	_, _ = c.DiscoverFor(context.Background(), "daytona-1", ep)
	c.Invalidate("daytona-1", "github.com")
	_, _ = c.DiscoverFor(context.Background(), "daytona-1", ep)

	if inner.calls != 2 {
		t.Fatalf("inner.calls = %d, want 2 after invalidate", inner.calls)
	}
}

func TestCachingDiscoverer_TargetIsPartOfKey(t *testing.T) {
	t.Parallel()
	// Same endpoint host, different target hostnames → independent
	// cache entries. Today they're populated identically; future
	// per-target token routing depends on this isolation.
	inner := &countingInner{cred: agent.GitCredential{
		Kind: agent.GitCredHTTPSBasic, Username: "x-access-token", Password: "tok",
	}}
	c := NewCachingDiscoverer(inner)
	ep := &agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}

	_, _ = c.DiscoverFor(context.Background(), "daytona-1", ep)
	_, _ = c.DiscoverFor(context.Background(), "daytona-2", ep)
	if inner.calls != 2 {
		t.Fatalf("inner.calls = %d, want 2 (per-target keying)", inner.calls)
	}
}

func TestCachingDiscoverer_ErrorsNotCached(t *testing.T) {
	t.Parallel()
	inner := &countingInner{err: errors.New("transient")}
	c := NewCachingDiscoverer(inner)
	ep := &agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"}
	_, _ = c.DiscoverFor(context.Background(), "daytona-1", ep)
	_, _ = c.DiscoverFor(context.Background(), "daytona-1", ep)
	if inner.calls != 2 {
		t.Fatalf("inner.calls = %d, want 2 (errors must not be cached)", inner.calls)
	}
}

type countingInner struct {
	cred  agent.GitCredential
	err   error
	calls int
}

func (c *countingInner) Discover(_ context.Context, _ *agent.GitEndpoint) (agent.GitCredential, error) {
	c.calls++
	return c.cred, c.err
}
