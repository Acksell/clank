// Package hub is the control plane of Clank's two-process architecture.
//
// hub.Service owns:
//   - the host catalog (one or more clank-host plane endpoints)
//   - the session registry (Hub-side view; the authoritative routing target)
//   - event fanout to connected clients (TUI, CLI)
//   - the permission broker
//   - persistence (SQLite store)
//
// In production, clankd constructs a hub.Service, mounts internal/hub/mux on
// it, and supervises a clank-host child whose Unix socket address gets
// registered as the "local" host. See hub_host_refactor.md.
//
// Phase 2B (this commit) introduces the package and the host-catalog
// primitives. Phase 2C migrates the session registry off internal/daemon;
// 2D moves HTTP handlers to internal/hub/mux; 2E switches cmd/clankd to
// construct hub.Service directly.
package hub

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	"github.com/acksell/clank/internal/store"
)

// Options configures a Service. All fields are optional.
type Options struct {
	// Store is the optional SQLite persistence layer. Nil = pure in-memory
	// (used by some integration tests).
	Store *store.Store

	// Log overrides the default stderr logger.
	Log *log.Logger
}

// Service is the Hub control plane. Construct with New; start with Run.
type Service struct {
	log *log.Logger

	// hosts is the catalog of registered Host endpoints, keyed by HostID.
	// Phase 2B only supports a single host (typically "local"); multi-host
	// dispatch arrives with the TCP+TLS transport in Phase 4.
	hostsMu sync.RWMutex
	hosts   map[host.HostID]hostclient.Client

	// store is the optional persistence layer. May be nil.
	store *store.Store

	startTime time.Time
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup

	// Future migration targets (filled in by Phase 2C+ — intentionally
	// commented out here so the build fails loudly when a sub-step lands):
	//   sessions    map[string]*Session         // 2C
	//   subscribers map[string]chan agent.Event // 2C
	//   permissions *permissionBroker           // 2C
	//   voice       *voice.Session              // deferred to Phase 3
}

// New constructs a Service. It does not start any goroutines or open any
// network endpoints — call Run for that.
func New(opts Options) *Service {
	logger := opts.Log
	if logger == nil {
		logger = log.New(os.Stderr, "[clank-hub] ", log.LstdFlags|log.Lmsgprefix)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		log:       logger,
		hosts:     make(map[host.HostID]hostclient.Client),
		store:     opts.Store,
		startTime: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Run starts the background goroutines owned by the Service (e.g. the
// per-host event-fanout subscriptions added in Phase 2C). It blocks only
// for synchronous setup; long-running work is detached on s.wg.
//
// Today Run is a no-op beyond marking the start time, because no
// background work has been migrated yet.
func (s *Service) Run(ctx context.Context) error {
	// Honour the caller's context for any future setup.
	_ = ctx
	return nil
}

// Shutdown stops every background goroutine, closes the persistence store,
// and disconnects from registered hosts. Safe to call multiple times.
func (s *Service) Shutdown() error {
	s.cancel()

	// Close every registered host client. Hosts() returns a snapshot so we
	// don't hold the lock while calling external Close.
	for id, c := range s.snapshotHosts() {
		if err := c.Close(); err != nil {
			s.log.Printf("close host %s: %v", id, err)
		}
	}

	// Close the persistence store.
	if s.store != nil {
		if err := s.store.Close(); err != nil {
			s.log.Printf("close store: %v", err)
		}
	}

	s.wg.Wait()
	return nil
}

// SetLogOutput redirects the Service logger to w. Call before Run.
func (s *Service) SetLogOutput(w io.Writer) { s.log.SetOutput(w) }

// --- Host catalog ---

// RegisterHost adds a host to the catalog. The Service does not take
// ownership of the lifetime of c; the caller (e.g. clankd's host
// supervisor) decides when to spawn/kill the underlying process.
//
// Re-registering the same HostID replaces the previous entry without
// closing it (the caller is responsible).
func (s *Service) RegisterHost(id host.HostID, c hostclient.Client) error {
	if id == "" {
		return fmt.Errorf("host id is required")
	}
	if c == nil {
		return fmt.Errorf("host client is required")
	}
	s.hostsMu.Lock()
	s.hosts[id] = c
	s.hostsMu.Unlock()
	return nil
}

// UnregisterHost removes a host from the catalog. Returns the client so the
// caller can decide whether to Close it. Returns nil if not registered.
func (s *Service) UnregisterHost(id host.HostID) hostclient.Client {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	c, ok := s.hosts[id]
	if !ok {
		return nil
	}
	delete(s.hosts, id)
	return c
}

// Host returns the client for the given HostID. The boolean is false if
// the host is not registered.
func (s *Service) Host(id host.HostID) (hostclient.Client, bool) {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	c, ok := s.hosts[id]
	return c, ok
}

// Hosts returns a snapshot of all registered host IDs.
func (s *Service) Hosts() []host.HostID {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	ids := make([]host.HostID, 0, len(s.hosts))
	for id := range s.hosts {
		ids = append(ids, id)
	}
	return ids
}

// snapshotHosts returns a copy of the host map, taken under the read lock,
// so callers can iterate without holding the lock during external calls.
func (s *Service) snapshotHosts() map[host.HostID]hostclient.Client {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	out := make(map[host.HostID]hostclient.Client, len(s.hosts))
	for id, c := range s.hosts {
		out[id] = c
	}
	return out
}
