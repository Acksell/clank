package hub

import (
	"errors"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
)

func newInProcessHostClient(t *testing.T) hostclient.Client {
	t.Helper()
	hostSvc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{},
	})
	return hostclient.NewInProcess(hostSvc)
}

func TestService_RegisterAndLookupHost(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Stop()

	hc := newInProcessHostClient(t)
	if err := s.RegisterHost("local", hc); err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}

	got, ok := s.Host("local")
	if !ok {
		t.Fatal("Host(local) returned ok=false after RegisterHost")
	}
	if got != hc {
		t.Errorf("Host(local) returned a different client than registered")
	}

	ids := s.Hosts()
	if len(ids) != 1 || ids[0] != "local" {
		t.Errorf("Hosts() = %v, want [local]", ids)
	}
}

func TestService_RegisterHost_Validation(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Stop()

	hc := newInProcessHostClient(t)
	if err := s.RegisterHost("", hc); err == nil {
		t.Error("RegisterHost with empty id should error")
	}
	if err := s.RegisterHost("local", nil); err == nil {
		t.Error("RegisterHost with nil client should error")
	}
}

func TestService_UnregisterHost(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Stop()

	hc := newInProcessHostClient(t)
	_ = s.RegisterHost("local", hc)

	got := s.UnregisterHost("local")
	if got != hc {
		t.Errorf("UnregisterHost returned %v, want the registered client", got)
	}
	if _, ok := s.Host("local"); ok {
		t.Error("Host(local) still present after UnregisterHost")
	}

	if got := s.UnregisterHost("missing"); got != nil {
		t.Errorf("UnregisterHost(missing) = %v, want nil", got)
	}
}

// TestService_closeHosts_ClosesEveryRegisteredHost guards the
// catalog-iterating cleanup in shutdown(server). The pre-refactor
// shutdown path called Close on s.hostClient only (the legacy
// single-host shortcut); migrating to a multi-host catalog without
// updating shutdown would silently leak every non-"local" host. This
// test fails if someone reverts closeHosts to the single-client path.
func TestService_closeHosts_ClosesEveryRegisteredHost(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Stop()

	closedLocal := false
	closedRemote := false
	_ = s.RegisterHost("local", &closeRecorder{onClose: func() error { closedLocal = true; return nil }})
	_ = s.RegisterHost("remote-1", &closeRecorder{onClose: func() error { closedRemote = true; return nil }})

	s.closeHosts()

	if !closedLocal {
		t.Error("closeHosts did not close the local host")
	}
	if !closedRemote {
		t.Error("closeHosts did not close the non-local host (catalog leak)")
	}
}

// TestService_closeHosts_SwallowsCloseErrors asserts that one host
// returning an error from Close does not abort the iteration; the
// remote host's Close must still run.
func TestService_closeHosts_SwallowsCloseErrors(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Stop()

	remoteClosed := false
	_ = s.RegisterHost("local", &closeRecorder{onClose: func() error { return errors.New("boom") }})
	_ = s.RegisterHost("remote", &closeRecorder{onClose: func() error { remoteClosed = true; return nil }})

	s.closeHosts() // must not panic

	if !remoteClosed {
		t.Error("closeHosts stopped iterating after the first host's Close errored")
	}
}

// closeRecorder is the minimum satisfier of hostclient.Client we need to
// observe shutdown semantics. It panics on every other method to make
// accidental expansion of the surface obvious during the migration.
type closeRecorder struct {
	hostclient.Client // embedded interface; all unimplemented methods panic
	onClose           func() error
}

func (c *closeRecorder) Close() error {
	if c.onClose == nil {
		return nil
	}
	return c.onClose()
}
