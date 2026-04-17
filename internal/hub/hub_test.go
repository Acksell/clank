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
	if err != nil { t.Fatalf("New: %v", err) }
	defer s.Shutdown()

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
	if err != nil { t.Fatalf("New: %v", err) }
	defer s.Shutdown()

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
	if err != nil { t.Fatalf("New: %v", err) }
	defer s.Shutdown()

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

func TestService_Shutdown_ClosesRegisteredHosts(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil { t.Fatalf("New: %v", err) }

	closed := false
	closer := &closeRecorder{onClose: func() error { closed = true; return nil }}
	if err := s.RegisterHost("local", closer); err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}

	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !closed {
		t.Error("Shutdown did not call Close on the registered host")
	}
}

func TestService_Shutdown_SwallowsHostCloseErrors(t *testing.T) {
	t.Parallel()

	s, err := New()
	if err != nil { t.Fatalf("New: %v", err) }
	closer := &closeRecorder{onClose: func() error { return errors.New("boom") }}
	_ = s.RegisterHost("local", closer)

	if err := s.Shutdown(); err != nil {
		t.Errorf("Shutdown propagated host close error: %v", err)
	}
}

// closeRecorder is the minimum satisfier of hostclient.Client we need to
// observe Shutdown semantics. It panics on every other method to make
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
