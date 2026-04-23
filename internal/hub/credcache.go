package hub

// Process-lifetime cache wrapping a [gitcred.Discoverer]. The hub
// resolver consults this on every push/clone; without a cache, every
// resolve would re-shell out to `gh auth token` (cheap, but adds
// ~30ms latency × every push). The cache also lets the push-retry
// path invalidate a single (host, endpointHost) pair on 401 without
// disturbing other entries.
//
// Process-lifetime is the right scope:
//   - Tokens rotate rarely (PAT expiry is months, OAuth refresh is
//     handled inside the discoverer).
//   - A clankd restart is the user's escape hatch when state is
//     stale, and starts often enough in dev that we don't need
//     time-based eviction.
//   - On auth failure the resolver invalidates explicitly, so a
//     newly-saved token takes effect on the next push.

import (
	"context"
	"sync"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitcred"
	"github.com/acksell/clank/internal/host"
)

// credCacheKey identifies one cached credential. We key on TARGET host
// AND endpoint host because the same endpoint may be accessed from
// multiple hosts (local + each remote sandbox) and a future feature
// could vary credentials per-target. Today they don't, but the wider
// key costs nothing and keeps the door open.
type credCacheKey struct {
	target       host.Hostname
	endpointHost string
}

// CachingDiscoverer wraps an inner [gitcred.Discoverer] with an
// in-memory map. Hits skip the inner discoverer entirely. Misses and
// errors are NOT cached — a transient subprocess failure shouldn't
// poison future lookups. ErrNoCredential is a soft miss and is also
// not cached, so a user who saves a token mid-session sees it on the
// next attempt without restarting.
//
// Safe for concurrent use.
type CachingDiscoverer struct {
	inner gitcred.Discoverer
	mu    sync.Mutex
	cache map[credCacheKey]agent.GitCredential
}

// NewCachingDiscoverer wraps inner. Panics if inner is nil — a cache
// with nothing to cache is always a bug.
func NewCachingDiscoverer(inner gitcred.Discoverer) *CachingDiscoverer {
	if inner == nil {
		panic("hub: NewCachingDiscoverer: nil inner")
	}
	return &CachingDiscoverer{
		inner: inner,
		cache: map[credCacheKey]agent.GitCredential{},
	}
}

// DiscoverFor is the cache's primary entry point. The (target,
// endpointHost) tuple is the cache key; the inner discoverer is
// called only on miss.
func (c *CachingDiscoverer) DiscoverFor(ctx context.Context, target host.Hostname, ep *agent.GitEndpoint) (agent.GitCredential, error) {
	if ep == nil {
		// Defer to inner so it can produce its own canonical error;
		// caching nil-ep is meaningless.
		return c.inner.Discover(ctx, ep)
	}
	key := credCacheKey{target: target, endpointHost: ep.Host}

	c.mu.Lock()
	cred, ok := c.cache[key]
	c.mu.Unlock()
	if ok {
		return cred, nil
	}

	cred, err := c.inner.Discover(ctx, ep)
	if err != nil {
		return agent.GitCredential{}, err
	}
	c.mu.Lock()
	c.cache[key] = cred
	c.mu.Unlock()
	return cred, nil
}

// Invalidate drops the cached entry for (target, endpointHost). The
// push-retry path calls this on a 401 so the next attempt re-runs
// discovery and picks up a freshly-saved token. No-op when the entry
// is absent.
func (c *CachingDiscoverer) Invalidate(target host.Hostname, endpointHost string) {
	c.mu.Lock()
	delete(c.cache, credCacheKey{target: target, endpointHost: endpointHost})
	c.mu.Unlock()
}
