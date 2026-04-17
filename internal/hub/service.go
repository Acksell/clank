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
	// HostID. Phase 2 only uses a single "local" host whose client is
	// also mirrored into hostClient below for the legacy single-host
	// fast path; multi-host dispatch arrives with the TCP+TLS transport
	// in Phase 4.
	hostsMu sync.RWMutex
	hosts   map[host.HostID]hostclient.Client

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

	// host is the in-process Host plane. Built in Run() from
	// BackendManagers ONLY when no HostClient was injected (i.e., tests).
	// In production clankd, host lives in the clank-host subprocess and
	// this field stays nil; the daemon talks to it via hostClient.
	host *host.Service
	// hostClient is the Hub-side abstraction over host. May be injected
	// by the caller before Run() (production clankd path: HTTP client
	// over a Unix socket to the clank-host subprocess). If nil at Run()
	// entry, the daemon constructs an in-process host from
	// BackendManagers — used by tests and by the legacy single-process
	// path.
	//
	// All HUB-tagged code paths go through this so the call shape matches
	// the wire path.
	hostClient hostclient.Client

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
		hosts:                        make(map[host.HostID]hostclient.Client),
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
func (s *Service) Run(listener net.Listener) error {
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

	// Construct the in-process Host plane from the configured
	// BackendManagers, UNLESS the caller injected a HostClient (the
	// production clankd path, where clank-host runs as a subprocess and
	// daemon talks to it over a Unix socket).
	//
	// In InProcess mode the daemon owns the host's lifetime: it must
	// call host.Run / host.Shutdown. In HTTP mode the subprocess owns
	// the host; the daemon only owns the client connection.
	if s.hostClient == nil {
		s.host = host.New(host.Options{
			BackendManagers: s.BackendManagers,
			Log:             s.log,
		})
		// Register as the canonical "local" host so the catalog stays
		// in sync; RegisterHost also sets s.hostClient under the hood.
		if err := s.RegisterHost("local", hostclient.NewInProcess(s.host)); err != nil {
			s.log.Printf("warning: register local host: %v", err)
		}

		// Initialize backend managers via host.Service. The knownDirs callback
		// is closed over s.Store so warm-up uses the persisted project list.
		knownDirs := func(bt agent.BackendType) ([]string, error) {
			if s.Store == nil {
				return nil, nil
			}
			return s.Store.KnownProjectDirs(bt)
		}
		if err := s.host.Run(s.ctx, knownDirs); err != nil {
			s.log.Printf("warning: host.Run: %v", err)
		}
	}

	// Warm primary agent caches AFTER the reconciler has started. The
	// cache warmer uses GetOrStartServer which will wait for the reconciler
	// to finish starting servers rather than starting them itself.
	s.warmPrimaryAgentCaches()

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	server := &http.Server{Handler: mux}

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

	// Shut down the host plane. host.Shutdown() stops every backend in
	// the in-process host's registry; closeHosts then disconnects every
	// client in the catalog (including the legacy s.hostClient shortcut,
	// which is the same object as catalog["local"]). Service must not
	// double-stop the host service itself.
	if s.host != nil {
		s.host.Shutdown()
	}
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

// SetHostClient injects a host client. Call before Run(). When set, the
// Service does NOT construct its own in-process host.Service and the
// caller is responsible for the host plane's lifetime (e.g. the clankd
// subprocess supervisor for clank-host). When unset, Service falls back
// to building an in-process host from BackendManagers.
//
// Equivalent to RegisterHost("local", c); kept as a convenience for the
// existing call sites that predate the host catalog.
func (s *Service) SetHostClient(c hostclient.Client) {
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
