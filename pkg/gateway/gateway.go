// Package gateway is the daemon's single ingress: it authenticates,
// resolves the user to a persistent host via the provisioner, and
// reverse-proxies everything else through.
//
// Routing: /ping and /gateway/health are served locally; every other
// path proxies to the user's host with the provisioner-supplied
// transport injecting per-request auth.
package gateway

import (
	"crypto/rand"
	"fmt"
	"log"
	"net/http"

	"github.com/acksell/clank/pkg/provisioner"
	clanksync "github.com/acksell/clank/pkg/sync"
)

// Config wires the gateway's dependencies. All fields except
// Provisioner have sensible defaults.
type Config struct {
	// Provisioner resolves a userID into the user's HostRef. EnsureHost
	// is called per-request; the provisioner caches in-process.
	Provisioner provisioner.Provisioner

	// Auth verifies the bearer on incoming requests. Defaults to
	// PermissiveAuth (any bearer accepted).
	Auth Authenticator

	// ResolveUserID maps a verified request to a userID. Defaults to
	// returning "local".
	ResolveUserID func(*http.Request) string

	// Sync is the embedded sync server. When non-nil, the gateway mounts
	// the sync API routes under /v1/ (behind Auth) and the migration
	// route calls sync methods directly rather than via HTTP.
	// When nil, the migration route returns 503.
	Sync *clanksync.Server

	// SyncPublicURL is the externally-reachable URL the gateway passes
	// to the sprite during a migrate-back's /sync/checkpoint call, so
	// the sprite knows where to upload its checkpoint. Typically the
	// gateway's own public URL — the sprite hits the same clankd that
	// orchestrates the migration.
	SyncPublicURL string

	// SyncAuthToken is the bearer the sprite presents to SyncPublicURL.
	// Same gating as Auth — for PermissiveAuth dev deployments any
	// value works.
	SyncAuthToken string
}

// Gateway is the public ingress.
type Gateway struct {
	cfg Config
	log *log.Logger

	// migrationKey signs two-phase migration tokens. Random-on-startup
	// so a daemon restart invalidates any pending materialize → commit
	// in flight; the laptop re-runs `clank pull --migrate`.
	migrationKey []byte
}

// NewGateway constructs a Gateway.
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
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("gateway: generate migration signing key: %w", err)
	}
	return &Gateway{cfg: cfg, log: lg, migrationKey: key}, nil
}

// Handler returns the public-listener http.Handler.
//
// /ping and /gateway/health answer locally without waking a host;
// /v1/migrate/worktrees/{id} runs the gateway-orchestrated migration
// flow when Sync is configured; /v1/ (other paths) forwards to the
// embedded sync server when Sync is configured; every other path
// proxies to the user's host.
func (g *Gateway) Handler() http.Handler {
	mx := http.NewServeMux()
	mx.HandleFunc("GET /ping", g.handlePing)
	mx.HandleFunc("GET /gateway/health", g.handleGatewayHealth)
	mx.HandleFunc("POST /v1/migrate/worktrees/{id}", g.handleMigrateWorktree)
	mx.HandleFunc("POST /v1/migrate/worktrees/{id}/materialize", g.handleMigrateMaterialize)
	mx.HandleFunc("POST /v1/migrate/worktrees/{id}/commit", g.handleMigrateCommit)
	if g.cfg.Sync != nil {
		// Mount sync API routes under /v1/ behind gateway auth.
		// POST /v1/migrate/worktrees/{id} is more specific and wins
		// over the /v1/ prefix registered here.
		syncH := g.authWrap(g.cfg.Sync.Handler())
		mx.Handle("/v1/", syncH)
	}
	mx.HandleFunc("/", g.proxyToHost)
	return mx
}

// authWrap returns an http.Handler that runs g.cfg.Auth.Verify before
// delegating to next. A failed verify returns 401 without calling next.
func (g *Gateway) authWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := g.cfg.Auth.Verify(r); err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="clank"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) handlePing(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("pong\n"))
}

func (g *Gateway) handleGatewayHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
