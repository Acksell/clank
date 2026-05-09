// Package sync is the sandbox-as-storage middleware. Laptop watchers
// stream git bundles to its HTTP endpoint; the server buffers them
// in-memory and flushes (debounced + on-demand) to each user's sandbox
// via clank's Provisioner.
//
// The sandbox volume is the source of truth — buffered bundles in this
// process are short-lived (TTL minutes-to-hours, evicted post-flush).
// No persistent mirror storage. Closing your laptop is safe: anything
// we already received reaches the sandbox before the next session.
//
// Auth is pluggable — pass an Authenticator. The library provides
// PermissiveAuth (any bearer accepted) for MVP/dev; production wraps
// the cloud's JWT verifier.
package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/acksell/clank/pkg/provisioner"
	"github.com/acksell/clank/pkg/sync/storage"
)

// Authenticator verifies the inbound bearer token and returns claims.
// The userID extractor (Config.UserIDFromClaims) maps those claims to
// the userID the provisioner expects.
type Authenticator interface {
	Verify(r *http.Request) (claims map[string]any, err error)
}

// PermissiveAuth accepts every request. Useful for laptop-local dev;
// do not deploy publicly without replacing it.
type PermissiveAuth struct{}

func (PermissiveAuth) Verify(*http.Request) (map[string]any, error) {
	return map[string]any{"sub": "local"}, nil
}

// Config configures the sync Server.
type Config struct {
	// Provisioner resolves a userID into the user's sandbox HostRef.
	// EnsureHost is called during flush — the same provisioner the
	// gateway uses, so a flush wakes the same sprite a normal request
	// would.
	Provisioner provisioner.Provisioner

	// Auth verifies the inbound bearer. Defaults to PermissiveAuth.
	Auth Authenticator

	// UserIDFromClaims maps the Authenticator's claim map to the
	// provisioner-userID. Required when Auth is non-permissive.
	// For Supabase: claims["sub"] (after lookup) → users.id string.
	UserIDFromClaims func(claims map[string]any) (string, error)

	// FlushDebounce caps how long a bundle waits before flushing,
	// counted from the most recent push for that user. Default 30s.
	FlushDebounce time.Duration

	// MaxBundleBytes per (userID, repoSlug). Larger uploads return 413.
	// Default 100MB — generous for typical edit cadence; protects against
	// runaway full-history bundles.
	MaxBundleBytes int64

	// Store backs the new checkpoint substrate (worktrees + checkpoints
	// tables). When nil, the /v1/worktrees and /v1/checkpoints endpoints
	// return 503 — the legacy /v1/bundles path stays available either way.
	// Required for the gateway's MigrateWorktree flow (P3).
	Store SyncStore

	// Storage is the object-storage backend (S3-compatible) where
	// checkpoint bundles live. When nil, the new endpoints return 503.
	// Bundle bytes never traverse the sync server in either direction —
	// laptop and gateway upload/download via presigned URLs minted here.
	Storage storage.Storage

	// PresignTTL is how long presigned PUT/GET URLs stay valid. Default
	// 5 minutes — long enough for slow uploads, short enough to bound a
	// leaked URL. Affects only the new /v1/checkpoints flow.
	PresignTTL time.Duration
}

// Server is the sync middleware.
type Server struct {
	cfg Config
	log *log.Logger

	mu      sync.Mutex
	buffers map[bufKey]*bundleSlot // (userID, repoSlug) → buffered bundle
	timers  map[string]*time.Timer // userID → debounce timer

	// httpClient pushes bundles to sandbox /sync/apply during flush.
	// Long timeout to allow large bundles to upload over slow links.
	httpClient *http.Client
}

type bufKey struct {
	userID, repo string
}

type bundleSlot struct {
	data       []byte
	receivedAt time.Time
}

// NewServer constructs a sync Server.
func NewServer(cfg Config, lg *log.Logger) (*Server, error) {
	if cfg.Provisioner == nil {
		return nil, fmt.Errorf("sync: Provisioner is required")
	}
	if cfg.Auth == nil {
		cfg.Auth = PermissiveAuth{}
	}
	if cfg.UserIDFromClaims == nil {
		cfg.UserIDFromClaims = func(claims map[string]any) (string, error) {
			if s, ok := claims["sub"].(string); ok && s != "" {
				return s, nil
			}
			return "", fmt.Errorf("no sub claim")
		}
	}
	if cfg.FlushDebounce == 0 {
		cfg.FlushDebounce = 30 * time.Second
	}
	if cfg.MaxBundleBytes == 0 {
		cfg.MaxBundleBytes = 100 << 20
	}
	if cfg.PresignTTL == 0 {
		cfg.PresignTTL = 5 * time.Minute
	}
	if (cfg.Store == nil) != (cfg.Storage == nil) {
		return nil, fmt.Errorf("sync: Store and Storage must be set together (or both unset)")
	}
	if lg == nil {
		lg = log.Default()
	}
	return &Server{
		cfg:        cfg,
		log:        lg,
		buffers:    make(map[bufKey]*bundleSlot),
		timers:     make(map[string]*time.Timer),
		httpClient: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

// Handler returns the public HTTP handler. Routes:
//
//	GET  /v1/health                       — liveness (no auth)
//	POST /v1/bundles?repo=<slug>          — legacy: buffer + autonomous flush (auth required)
//	POST /v1/worktrees                    — register a worktree, returns ID
//	POST /v1/checkpoints                  — create checkpoint metadata, returns presigned PUT URLs
//	POST /v1/checkpoints/{id}/commit      — confirm upload, advance latest_synced_checkpoint
//	GET  /v1/checkpoints/{id}/download    — return presigned GET URLs (gateway uses on migration)
//
// The new /v1/worktrees + /v1/checkpoints routes only register when
// Config.Store and Config.Storage are both set.
func (s *Server) Handler() http.Handler {
	mx := http.NewServeMux()
	mx.HandleFunc("GET /v1/health", s.handleHealth)
	mx.HandleFunc("POST /v1/bundles", s.handlePushBundle)
	if s.cfg.Store != nil && s.cfg.Storage != nil {
		mx.HandleFunc("POST /v1/worktrees", s.handleRegisterWorktree)
		mx.HandleFunc("POST /v1/checkpoints", s.handleCreateCheckpoint)
		mx.HandleFunc("POST /v1/checkpoints/{id}/commit", s.handleCommitCheckpoint)
		mx.HandleFunc("GET /v1/checkpoints/{id}/download", s.handleDownloadCheckpoint)
	}
	return mx
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handlePushBundle(w http.ResponseWriter, r *http.Request) {
	claims, err := s.cfg.Auth.Verify(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clank-sync"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	userID, err := s.cfg.UserIDFromClaims(claims)
	if err != nil || userID == "" {
		http.Error(w, "no user identity", http.StatusUnauthorized)
		return
	}

	repo := r.URL.Query().Get("repo")
	if !validRepoSlug(repo) {
		http.Error(w, "bad repo slug", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.MaxBundleBytes+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if int64(len(body)) > s.cfg.MaxBundleBytes {
		http.Error(w, "bundle too large", http.StatusRequestEntityTooLarge)
		return
	}

	s.storeBundle(userID, repo, body)
	s.scheduleFlush(userID)

	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) storeBundle(userID, repo string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffers[bufKey{userID, repo}] = &bundleSlot{
		data:       data,
		receivedAt: time.Now(),
	}
}

// scheduleFlush starts (or resets) a debounced flush timer for userID.
// The timer fires Config.FlushDebounce after the most recent push;
// each new push during the window pushes the deadline out.
func (s *Server) scheduleFlush(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.timers[userID]; ok {
		t.Reset(s.cfg.FlushDebounce)
		return
	}
	s.timers[userID] = time.AfterFunc(s.cfg.FlushDebounce, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := s.Flush(ctx, userID); err != nil {
			s.log.Printf("sync: flush %s: %v", userID, err)
		}
		s.mu.Lock()
		delete(s.timers, userID)
		s.mu.Unlock()
	})
}

// Flush is the explicit on-demand entry point — the cloud gateway
// calls this synchronously when a user wakes a session from another
// device, to ensure the sandbox reflects laptop state-as-of-last-bundle
// before the proxy starts.
//
// Idempotent. Safe to call concurrently — buffers are taken atomically.
func (s *Server) Flush(ctx context.Context, userID string) error {
	pending := s.takePending(userID)
	if len(pending) == 0 {
		return nil
	}

	ref, err := s.cfg.Provisioner.EnsureHost(ctx, userID)
	if err != nil {
		// Re-buffer so a retry can succeed. Lossy on multi-flush races —
		// a later push for the same (user, repo) would overwrite, which
		// is fine since the laptop's bundle is the source of truth.
		s.restorePending(userID, pending)
		return fmt.Errorf("ensure host: %w", err)
	}

	for _, p := range pending {
		if err := s.applyOne(ctx, ref, p); err != nil {
			s.log.Printf("sync: apply %s/%s: %v", userID, p.repo, err)
			// Re-buffer just this entry so partial failures don't lose data.
			s.storeBundle(userID, p.repo, p.data)
		}
	}
	return nil
}

type pendingItem struct {
	repo string
	data []byte
}

func (s *Server) takePending(userID string) []pendingItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pendingItem
	for k, v := range s.buffers {
		if k.userID == userID {
			out = append(out, pendingItem{repo: k.repo, data: v.data})
			delete(s.buffers, k)
		}
	}
	return out
}

func (s *Server) restorePending(userID string, items []pendingItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		// Don't clobber a newer push that arrived during the failed flush.
		if _, exists := s.buffers[bufKey{userID, it.repo}]; exists {
			continue
		}
		s.buffers[bufKey{userID, it.repo}] = &bundleSlot{data: it.data, receivedAt: time.Now()}
	}
}

func (s *Server) applyOne(ctx context.Context, ref provisioner.HostRef, p pendingItem) error {
	url := strings.TrimRight(ref.URL, "/") + "/sync/apply?repo=" + p.repo
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(p.data))
	if err != nil {
		return err
	}
	if ref.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+ref.AuthToken)
	}
	req.Header.Set("Content-Type", "application/x-git-bundle")

	client := s.httpClient
	if ref.Transport != nil {
		client = &http.Client{Transport: ref.Transport, Timeout: s.httpClient.Timeout}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post bundle: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("apply returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func validRepoSlug(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "/\\") || strings.Contains(s, "..") {
		return false
	}
	return true
}

// Stop cancels all pending flush timers. Buffers in memory are dropped —
// callers that need durability should checkpoint to disk before Stop
// (not implemented in MVP; clank-sync is best-effort and laptops can
// always re-push).
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.timers {
		t.Stop()
	}
	s.timers = make(map[string]*time.Timer)
}

// errBufferFull is returned when MaxBundleBytes is exceeded. Exported
// so callers can match on it; not currently surfaced — the handler
// returns 413 directly.
var errBufferFull = errors.New("sync: bundle exceeds MaxBundleBytes")
