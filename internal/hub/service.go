// This file contains the core Service struct, constructor, and HTTP
// serve loop for the Hub control plane. The host-catalog primitives
// are in hub.go; topical method sets are in sessions.go, events.go,
// permissions.go, voice.go, etc.
//
// Service does NOT touch the filesystem for its own listener or PID
// file — those are caller responsibilities (daemoncli in production,
// startHubOnSocket in tests). Run takes a pre-bound net.Listener and
// returns when Stop() is called or s.ctx is otherwise cancelled.
package hub

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	"github.com/acksell/clank/internal/store"
	"github.com/acksell/clank/internal/voice"
	"github.com/coder/websocket"
)

// Service is the Hub control plane.
//
// It manages coding agent sessions (OpenCode, Claude Code) — currently
// in-process or via a single clank-host subprocess — aggregates their
// events, and exposes an HTTP API to its caller-supplied net.Listener
// (a Unix domain socket in production).
type Service struct {
	// hosts is the catalog of registered Host endpoints, keyed by
	// Hostname. Phase 2 only uses a single "local" host whose client is
	// also mirrored into hostClient below for the legacy single-host
	// fast path; multi-host dispatch arrives with the TCP+TLS transport
	// in Phase 4.
	hostsMu sync.RWMutex
	hosts   map[host.Hostname]*hostclient.HTTP

	mu       sync.RWMutex
	sessions map[string]*managedSession // keyed by hub session ID
	// subscribers receive all events broadcast by the hub.
	subMu       sync.RWMutex
	subscribers map[string]chan agent.Event // keyed by subscriber ID

	startTime time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// BackendManagers maps each backend type to its manager. The manager
	// creates SessionBackend instances and owns shared resources (e.g.,
	// OpenCode servers). Managers that also implement agent.AgentLister or
	// agent.SessionDiscoverer gain agent listing and session discovery
	// capabilities automatically.
	//
	// As of Phase 1, this field is **only used by tests**. Production
	// clankd injects a HostClient via SetHostClient (the clank-host
	// subprocess owns the real BackendManagers). Tests still use the
	// `s.BackendManagers[X] = mgr` pattern; when Run() finds hostClient
	// nil it builds an in-process host from this map.
	//
	// Removed in Phase 2 once tests get a `WithHost` constructor option.
	BackendManagers map[agent.BackendType]agent.BackendManager

	// hostClient is the Hub-side abstraction over host. Per Decision #3
	// it is the concrete *hostclient.HTTP — no Go interface, no
	// in-process shortcut. The caller (production clankd, or the test
	// helper) injects it before Run() either via SetHostClient or by
	// registering a host into the catalog.
	//
	// All HUB-tagged code paths go through this so the call shape matches
	// the wire path.
	hostClient *hostclient.HTTP

	// Store is the optional SQLite persistence layer. When non-nil, session
	// metadata is written through on every mutation and loaded on startup.
	// When nil (e.g. in tests), the daemon operates purely in-memory.
	Store *store.Store

	// primaryAgentsRefreshMu guards primaryAgentsRefreshInFlight.
	primaryAgentsRefreshMu       sync.Mutex
	primaryAgentsRefreshInFlight map[string]bool // keyed by "backend\x00projectDir"

	// voice is the singleton voice session (nil when inactive).
	voice          *voice.Session
	voiceAudioConn *websocket.Conn

	log *log.Logger
}

// managedSession tracks a running agent session.
type managedSession struct {
	info         agent.SessionInfo
	backend      agent.SessionBackend   // nil until started
	watchOnly    bool                   // true when backend was started via Watch() (no prompt sent yet)
	pendingPerms []agent.PermissionData // queue of permission prompts awaiting responses
}

// New constructs an in-memory Hub Service. It does not touch the
// filesystem or open any sockets — wire up BackendManagers / Store /
// HostClient as needed, then hand the result to a Run-driver
// (daemoncli.startServer in production, startHubOnSocket in tests).
func New() *Service {
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		hosts:                        make(map[host.Hostname]*hostclient.HTTP),
		sessions:                     make(map[string]*managedSession),
		subscribers:                  make(map[string]chan agent.Event),
		startTime:                    time.Now(),
		ctx:                          ctx,
		cancel:                       cancel,
		log:                          log.New(os.Stderr, "[clank-hub] ", log.LstdFlags|log.Lmsgprefix),
		BackendManagers:              make(map[agent.BackendType]agent.BackendManager),
		primaryAgentsRefreshInFlight: make(map[string]bool),
	}
}

// Run serves the Hub HTTP API on the provided listener and blocks
// until Stop() is called (or s.ctx is cancelled some other way). The
// caller owns the listener's lifetime AND any on-disk artifacts (PID
// file, socket file, etc.); Run never touches them.
//
// handler is the HTTP handler the listener should serve. Production
// uses internal/hub/mux (hubmux.New(s, log).Handler()); tests use the
// same. Run takes the handler from the caller (rather than wiring it
// internally) to keep the hub package free of an import cycle on
// internal/hub/mux, which itself imports internal/hub.
func (s *Service) Run(listener net.Listener, handler http.Handler) error {
	s.log.Printf("hub started (pid=%d, addr=%s)", os.Getpid(), listener.Addr())

	// Load persisted sessions from the store (if available).
	if s.Store != nil {
		sessions, err := s.Store.LoadSessions()
		if err != nil {
			s.log.Printf("warning: failed to load sessions from store: %v", err)
		} else {
			s.mu.Lock()
			for _, info := range sessions {
				// Sessions loaded from DB have no backend — they can't
				// be actively busy. Normalize stale statuses to idle so
				// the inbox doesn't show spinners or dead markers for
				// sessions interrupted by a daemon restart.
				if info.Status == agent.StatusBusy || info.Status == agent.StatusStarting || info.Status == agent.StatusDead {
					info.Status = agent.StatusIdle
				}
				s.sessions[info.ID] = &managedSession{info: info, backend: nil}
			}
			s.mu.Unlock()
			if len(sessions) > 0 {
				s.log.Printf("loaded %d sessions from store", len(sessions))
			}
		}
	}

	// Per Decision #3 (no in-process Host shortcut), the caller MUST
	// inject a host client before Run(). Production clankd does this
	// via SetHostClient(*hostclient.HTTP) pointed at the clank-host
	// subprocess; tests do it through the test helper which spins a
	// real host.Service behind an httptest server.
	if s.hostClient == nil {
		return fmt.Errorf("hub.Service.Run: no host client registered (call SetHostClient before Run)")
	}

	// Warm primary agent caches AFTER the reconciler has started. The
	// cache warmer uses GetOrStartServer which will wait for the reconciler
	// to finish starting servers rather than starting them itself.
	s.warmPrimaryAgentCaches()

	server := &http.Server{Handler: handler}

	// Start HTTP server in background.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.log.Printf("http serve error: %v", err)
		}
	}()

	// Wait for shutdown (Stop() or external ctx cancellation).
	<-s.ctx.Done()
	s.log.Printf("context cancelled, shutting down")

	return s.shutdown(server)
}

// shutdown gracefully tears down internal subsystems and the HTTP
// server. The on-disk listener artifacts (socket file, PID file) are
// the caller's responsibility — Run never created them.
func (s *Service) shutdown(server *http.Server) error {
	s.cancel()

	// Stop the voice session if active.
	s.mu.Lock()
	if s.voice != nil {
		s.log.Println("stopping voice session")
		s.voice.Close()
		s.voice = nil
	}
	if s.voiceAudioConn != nil {
		s.voiceAudioConn.CloseNow()
		s.voiceAudioConn = nil
	}
	s.mu.Unlock()

	// Release every active session backend. Stop() unblocks the
	// per-session SSE relay goroutines (started by runBackend) by
	// instructing the host to close their event streams; without this
	// step s.wg.Wait below would deadlock, since we no longer own the
	// host plane and thus can't shut it down to cascade-close streams.
	//
	// In production, this is also what TestDaemonGracefulShutdownStopsBackends
	// pins down: shutting hub down must release the agent processes it
	// asked the host to spawn, even when the host process outlives hub.
	s.stopActiveBackends()

	// Disconnect every host client in the catalog. The host plane's
	// lifetime (process or test fixture) is owned by the caller; we
	// only release our HTTP transport handles here.
	s.closeHosts()

	// Close the persistence store.
	if s.Store != nil {
		if err := s.Store.Close(); err != nil {
			s.log.Printf("error closing store: %v", err)
		}
	}

	// Close all subscriber channels.
	s.subMu.Lock()
	for id, ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, id)
	}
	s.subMu.Unlock()

	// Shutdown HTTP server.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		s.log.Printf("http shutdown error: %v", err)
	}

	s.wg.Wait()
	s.log.Printf("hub stopped")
	return nil
}

// SetLogOutput redirects the daemon's logger to the given writer.
// Call before Run() to capture all daemon output.
func (s *Service) SetLogOutput(w io.Writer) {
	s.log.SetOutput(w)
}

// SetHostClient injects the host client. Call before Run(). The caller
// owns the host plane's lifetime (e.g. clankd's host supervisor for the
// production clank-host subprocess, or the test helper for in-test
// httptest fixtures).
//
// Equivalent to RegisterHost("local", c); kept as a convenience for the
// existing call sites that predate the host catalog.
func (s *Service) SetHostClient(c *hostclient.HTTP) {
	_ = s.RegisterHost("local", c)
}

// Stop requests the daemon to shut down. Safe to call from any
// goroutine; idempotent.
func (s *Service) Stop() {
	s.cancel()
}

// closeHosts disconnects every host client in the catalog. Errors are
// logged but not returned: shutdown is best-effort, and the caller
// (typically the Run-cleanup path) has nothing useful to do with a
// per-host close failure.
//
// Iterates a snapshot so we don't hold hostsMu across an external Close
// call. Safe to call multiple times — Close on an already-closed client
// is a no-op for both InProcess and HTTP transports.
func (s *Service) closeHosts() {
	for id, c := range s.snapshotHosts() {
		if err := c.Close(); err != nil {
			s.log.Printf("close host %s: %v", id, err)
		}
	}
}

// stopActiveBackends instructs every live SessionBackend to stop. This
// is the hub-side complement to host.Service.Shutdown: it releases the
// session resources hub asked the host to allocate, in the order it
// allocated them, without requiring host-process teardown to do it.
//
// Iterates a snapshot of *managedSession pointers so backend.Stop()
// runs without holding s.mu (Stop performs an HTTP round-trip for
// remote backends).
func (s *Service) stopActiveBackends() {
	s.mu.RLock()
	live := make([]*managedSession, 0, len(s.sessions))
	for _, ms := range s.sessions {
		if ms.backend != nil {
			live = append(live, ms)
		}
	}
	s.mu.RUnlock()

	for _, ms := range live {
		if err := ms.backend.Stop(); err != nil {
			s.log.Printf("stop session %s: %v", ms.info.ID, err)
		}
	}
}
