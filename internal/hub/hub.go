// Package hub is the control plane of Clank's two-process architecture.
//
// hub.Service owns:
//   - the host catalog (one or more clank-host plane endpoints)
//   - the session registry (Hub-side view; the authoritative routing target)
//   - event fanout to connected clients (TUI, CLI)
//   - the permission broker
//   - persistence (SQLite store)
//
// In production, clankd constructs a hub.Service, wires HTTP routes via
// internal/hub/mux, opens the listening Unix socket itself (see
// internal/cli/daemoncli), and supervises a clank-host child whose Unix
// socket gets registered as the "local" host through a *hostclient.HTTP.
// See hub_host_refactor.md.
//
// This file holds only the multi-host catalog primitives. The Service
// struct itself, its constructor, and the bulk of its methods live in
// service.go and the topical files (sessions.go, events.go, ...).
package hub

import (
	"fmt"

	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
)

// RegisterHost adds a host to the catalog. The Service does not take
// ownership of the lifetime of c; the caller (e.g. clankd's host
// supervisor) decides when to spawn/kill the underlying process.
//
// Per Decision #3 of the refactor, the catalog is typed concretely on
// *hostclient.HTTP — there is no Host Go interface and no in-process
// shortcut. Tests stand up a real host.Service behind an httptest
// server and register the resulting HTTP client; production registers
// the supervisor's HTTP client over a Unix socket.
//
// As a Phase 2 transitional convenience, registering the "local" host
// also wires it into the legacy single-host code path (s.hostClient),
// which most session/permission/event code still routes through.
// Phase 3+ removes that field in favour of per-request catalog lookup.
//
// Re-registering the same HostID replaces the previous entry without
// closing it (the caller is responsible).
func (s *Service) RegisterHost(id host.HostID, c *hostclient.HTTP) error {
	if id == "" {
		return fmt.Errorf("host id is required")
	}
	if c == nil {
		return fmt.Errorf("host client is required")
	}
	s.hostsMu.Lock()
	s.hosts[id] = c
	s.hostsMu.Unlock()

	if id == "local" {
		s.hostClient = c
	}
	return nil
}

// UnregisterHost removes a host from the catalog. Returns the client so the
// caller can decide whether to Close it. Returns nil if not registered.
func (s *Service) UnregisterHost(id host.HostID) *hostclient.HTTP {
	s.hostsMu.Lock()
	defer s.hostsMu.Unlock()
	c, ok := s.hosts[id]
	if !ok {
		return nil
	}
	delete(s.hosts, id)
	if id == "local" && s.hostClient == c {
		s.hostClient = nil
	}
	return c
}

// Host returns the client for the given HostID. The boolean is false if
// the host is not registered.
func (s *Service) Host(id host.HostID) (*hostclient.HTTP, bool) {
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
func (s *Service) snapshotHosts() map[host.HostID]*hostclient.HTTP {
	s.hostsMu.RLock()
	defer s.hostsMu.RUnlock()
	out := make(map[host.HostID]*hostclient.HTTP, len(s.hosts))
	for id, c := range s.hosts {
		out[id] = c
	}
	return out
}
