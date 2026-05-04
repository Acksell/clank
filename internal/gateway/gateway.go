// Package gateway is the daemon's single ingress: a reverse proxy
// that authenticates incoming requests, resolves the calling user to
// a persistent host (via the provisioner), and forwards HTTP/WebSocket
// traffic to that host. After PR 3 it replaces clank-hub as the
// public listener; cmd/clankd mounts gateway.Handler() on the
// configured port.
//
// PR 3 ships with PermissiveAuth (any bearer accepted, "local" user
// id assumed). PR 4 swaps in real JWT verification + per-device
// auth without changing the gateway's shape.
//
// Routing is intentionally minimal: a tiny set of self-served
// endpoints (/ping, /gateway/health) and a catch-all that proxies
// everything else to the user's host. The previous hub-vs-host
// distinction is gone — with one host per user, every non-gateway
// route lives on the host.
package gateway

import (
	"fmt"
	"log"
	"net/http"

	"github.com/acksell/clank/internal/provisioner"
)

// Config wires the gateway's dependencies. All fields except
// Provisioner have sensible defaults.
type Config struct {
	// Provisioner resolves a userID into the user's HostRef
	// (URL + auth-wired Transport). EnsureHost is called per-
	// request; the provisioner's in-process cache makes repeated
	// calls cheap.
	Provisioner provisioner.Provisioner

	// Auth verifies the bearer on incoming requests. PR 3 ships
	// PermissiveAuth which accepts everything; PR 4 swaps in JWT.
	Auth Authenticator

	// ResolveUserID maps the verified request to a userID for the
	// provisioner lookup. PR 3 hardcodes "local"; PR 4 reads
	// claims from the verified JWT.
	ResolveUserID func(*http.Request) string
}

// Gateway is the public ingress.
type Gateway struct {
	cfg Config
	log *log.Logger
}

// NewGateway constructs a Gateway. Returns an error if Provisioner
// is missing.
func NewGateway(cfg Config, lg *log.Logger) (*Gateway, error) {
	if cfg.Provisioner == nil {
		return nil, fmt.Errorf("gateway: Provisioner is required")
	}
	if cfg.Auth == nil {
		cfg.Auth = PermissiveAuth{}
	}
	if cfg.ResolveUserID == nil {
		cfg.ResolveUserID = func(*http.Request) string { return "local" }
	}
	if lg == nil {
		lg = log.Default()
	}
	return &Gateway{cfg: cfg, log: lg}, nil
}

// Handler returns the http.Handler that the daemon mounts on its
// public listener. Routes:
//
//	GET /ping              → gateway responds immediately (heartbeat)
//	GET /gateway/health    → gateway responds (separate from /status
//	                         because /status proxies to the host —
//	                         /gateway/health answers "is the gateway
//	                         itself alive" without waking a sleeping
//	                         host)
//	*                      → proxied to the user's host with the
//	                         provisioner-supplied Transport injecting
//	                         per-request auth (Daytona preview-token,
//	                         Sprites bearer, etc.)
func (g *Gateway) Handler() http.Handler {
	mx := http.NewServeMux()
	mx.HandleFunc("GET /ping", g.handlePing)
	mx.HandleFunc("GET /gateway/health", g.handleGatewayHealth)
	mx.HandleFunc("/", g.proxyToHost)
	return mx
}

func (g *Gateway) handlePing(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("pong\n"))
}

func (g *Gateway) handleGatewayHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
