package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// proxyToHost is the catch-all handler: every route not served by
// the gateway itself proxies to the user's persistent host.
//
// For each request we:
//  1. Run the Authenticator. A non-nil error returns 401.
//  2. Resolve the userID via the configured callback.
//  3. Call Provisioner.EnsureHost(userID). The provisioner caches
//     the HostRef in-process so repeat calls are cheap; the first
//     call after a daemon start may incur a ~100ms-2s wake or
//     create-fresh latency.
//  4. Construct an httputil.ReverseProxy targeting the host's URL
//     and using the HostRef.Transport so per-request auth headers
//     (Daytona's x-daytona-preview-token, Sprites' Authorization
//     bearer, etc.) are injected by the same RoundTripper that
//     PR 1+2 already wired up.
//
// httputil.ReverseProxy supports HTTP/1.1 protocol upgrades since
// Go 1.20, including WebSocket — /events flows through this same
// path with no separate code.
func (g *Gateway) proxyToHost(w http.ResponseWriter, r *http.Request) {
	if _, err := g.cfg.Auth.Verify(r); err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clank"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	userID := g.cfg.ResolveUserID(r)
	if userID == "" {
		http.Error(w, "no user identity", http.StatusUnauthorized)
		return
	}

	ref, err := g.cfg.Provisioner.EnsureHost(r.Context(), userID)
	if err != nil {
		g.log.Printf("gateway: ensure host for user %s: %v", userID, err)
		http.Error(w, "host unavailable", http.StatusBadGateway)
		return
	}

	target, err := url.Parse(ref.URL)
	if err != nil {
		g.log.Printf("gateway: invalid host URL %q for user %s: %v", ref.URL, userID, err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Use the upstream's Host (from target). Some upstream
			// edges — Sprites in particular — route requests by
			// Host header, and forwarding the client's Host (a
			// cloudflare quick-tunnel hostname or a localhost
			// listener) made them serve their own 404 page instead
			// of routing to the sprite's service. The host plane
			// itself doesn't care about Host; only the edge does.
			// pr.SetURL already sets pr.Out.URL.Host to target.Host;
			// pr.Out.Host gets cleared by the rewrite to match.
			pr.Out.Host = target.Host
			// Strip the /hosts/{hostname} prefix the TUI's
			// HostClient prepends — the host plane is single-user
			// and serves bare paths (/auth/..., /worktrees/...).
			// The hostname segment was a routing hint for the old
			// hub; the gateway already resolved (userID → host) by
			// the time we get here, so the segment is informational
			// only.
			pr.Out.URL.Path = stripHostsPrefix(pr.Out.URL.Path)
			pr.Out.URL.RawPath = stripHostsPrefix(pr.Out.URL.RawPath)
		},
		Transport: ref.Transport,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			g.log.Printf("gateway: proxy %s %s: %v", req.Method, req.URL.Path, err)
			http.Error(rw, "upstream error", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// stripHostsPrefix turns "/hosts/{name}/foo/bar" into "/foo/bar". A path
// not under /hosts/ is returned unchanged. The empty input is preserved
// so we don't have to special-case RawPath (which is "" when the URL
// has no encoded segments).
func stripHostsPrefix(p string) string {
	if p == "" || !strings.HasPrefix(p, "/hosts/") {
		return p
	}
	rest := p[len("/hosts/"):]
	// rest is "{name}/..." — drop the first segment.
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[i:]
	}
	// "/hosts/{name}" with no trailing path → "/".
	return "/"
}
