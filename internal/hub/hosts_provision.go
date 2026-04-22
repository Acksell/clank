// Host provisioning lets the hub bring up a new clank-host on demand,
// e.g. by spinning up a Daytona sandbox. The hub itself is plane-
// agnostic — it doesn't import any specific provider — so each
// provider registers a HostLauncher under a kind string and the hub
// dispatches by name at request time.
//
// This split keeps daytona-specific code out of the hub package and
// lets tests register a real httptest-backed launcher (no mocks per
// AGENTS.md "NEVER mock dependencies").

package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
)

// HostLauncher provisions a new host plane on demand. Implementations
// are registered with Service.RegisterHostLauncher under a kind key
// (e.g. "daytona") and invoked by ProvisionHost.
type HostLauncher interface {
	// Launch creates the host plane and returns a hostclient pointed
	// at it plus a handle the hub uses to tear it down later. Both
	// must be non-nil on success.
	Launch(ctx context.Context) (*hostclient.HTTP, RemoteHostHandle, error)
}

// RemoteHostHandle is the hub's handle to a launcher-managed host. The
// hub calls Stop during shutdown; lifetime is otherwise opaque.
type RemoteHostHandle interface {
	Stop(ctx context.Context) error
}

// RegisterHostLauncher associates a launcher with a kind key. Calling
// twice with the same kind replaces the prior launcher and returns it,
// so callers can stack/decorate. Returns nil for the prior launcher
// when the registration is new. Empty kind is rejected — that would
// silently shadow a future registration.
func (s *Service) RegisterHostLauncher(kind string, l HostLauncher) (HostLauncher, error) {
	if kind == "" {
		return nil, fmt.Errorf("hub.RegisterHostLauncher: kind is required")
	}
	if l == nil {
		return nil, fmt.Errorf("hub.RegisterHostLauncher: launcher is required")
	}
	s.launchersMu.Lock()
	defer s.launchersMu.Unlock()
	if s.launchers == nil {
		s.launchers = make(map[string]HostLauncher)
	}
	prev := s.launchers[kind]
	s.launchers[kind] = l
	return prev, nil
}

// ProvisionHost brings up a host plane of the given kind and registers
// it in the catalog under the same name. Idempotent: if a host with
// that name is already registered, returns its hostname without
// re-launching. The launcher itself is not consulted in the idempotent
// path, so a partial-failure cleanup that re-uses the same kind name
// must Unregister first.
//
// Synchronous on purpose (Decision #4 in docs/daytona_plan.md): the
// caller's HTTP request blocks until the host is reachable. Daytona is
// sub-second; if other launchers grow long enough to feel laggy, an
// async + SSE-progress version can be added later without breaking
// this signature.
func (s *Service) ProvisionHost(ctx context.Context, kind string) (host.Hostname, error) {
	if kind == "" {
		return "", fmt.Errorf("hub.ProvisionHost: kind is required")
	}
	hostname := host.Hostname(kind)

	// Idempotent fast path. Using the catalog (not remoteHandles) as
	// the source of truth so a host registered through any path
	// (RegisterHost, SetHostClient, ProvisionHost) is honored.
	if _, ok := s.Host(hostname); ok {
		return hostname, nil
	}

	s.launchersMu.RLock()
	launcher, ok := s.launchers[kind]
	s.launchersMu.RUnlock()
	if !ok {
		return "", fmt.Errorf("hub.ProvisionHost: no launcher registered for kind %q", kind)
	}

	client, handle, err := launcher.Launch(ctx)
	if err != nil {
		return "", fmt.Errorf("hub.ProvisionHost(%s): %w", kind, err)
	}
	if client == nil || handle == nil {
		// Defensive: a launcher returning nil with no error is a
		// programming bug we want to surface loudly.
		return "", fmt.Errorf("hub.ProvisionHost(%s): launcher returned nil client/handle without error", kind)
	}

	if _, err := s.RegisterHost(hostname, client); err != nil {
		// Best-effort teardown so a failed registration doesn't
		// leak the just-provisioned remote resource.
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = handle.Stop(stopCtx)
		return "", fmt.Errorf("hub.ProvisionHost(%s): register: %w", kind, err)
	}

	s.remoteHandlesMu.Lock()
	if s.remoteHandles == nil {
		s.remoteHandles = make(map[host.Hostname]RemoteHostHandle)
	}
	s.remoteHandles[hostname] = handle
	s.remoteHandlesMu.Unlock()
	return hostname, nil
}

// stopRemoteHandles tears down every launcher-provisioned host. Called
// from shutdown(); errors are logged, not returned, because shutdown
// is best-effort and there's no useful caller-side action.
//
// Iterates a snapshot taken under the lock so a long-running Stop
// (Daytona Delete is ~1s) doesn't block other shutdown work.
func (s *Service) stopRemoteHandles() {
	s.remoteHandlesMu.Lock()
	snap := make(map[host.Hostname]RemoteHostHandle, len(s.remoteHandles))
	for k, v := range s.remoteHandles {
		snap[k] = v
	}
	s.remoteHandles = nil
	s.remoteHandlesMu.Unlock()

	if len(snap) == 0 {
		return
	}
	// Bound the per-host stop so a single hung Daytona Delete doesn't
	// keep clankd from exiting. 30s is generous; production Delete is
	// sub-second.
	var wg sync.WaitGroup
	for hn, h := range snap {
		wg.Add(1)
		go func(hn host.Hostname, h RemoteHostHandle) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := h.Stop(ctx); err != nil {
				s.log.Printf("stop remote host %s: %v", hn, err)
			}
		}(hn, h)
	}
	wg.Wait()
}
