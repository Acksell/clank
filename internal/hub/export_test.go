package hub

import (
	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// InjectSession exposes injectSession for external tests (package hub_test).
func (s *Service) InjectSession(info agent.SessionInfo) { s.injectSession(info) }

// ExportStopRemoteHandles exposes stopRemoteHandles for external tests so
// shutdown teardown can be exercised without spinning a full Run loop.
func ExportStopRemoteHandles(s *Service) { s.stopRemoteHandles() }

// ExportHostIdentity reads the identity the hub propagated to a host's
// service via SetIdentity. Used by hosts_provision_test.go.
func ExportHostIdentity(svc *host.Service) (name, email string) {
	id := svc.Identity()
	return id.Name, id.Email
}

// injectSession adds a session to the hub's in-memory map without starting
// a backend. Used by tests via the export_test.go bridge.
func (s *Service) injectSession(info agent.SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[info.ID] = &managedSession{info: info}
}
