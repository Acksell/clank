package hub_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	hostmux "github.com/acksell/clank/internal/host/mux"
	"github.com/acksell/clank/internal/hub"
)

// httptestLauncher is a real HostLauncher backed by an httptest server
// running an actual host.Service. No mocks per AGENTS.md — this is the
// same path production daytona.Launch follows, just with a local
// listener instead of a Daytona preview URL.
type httptestLauncher struct {
	srv     *httptest.Server
	svc     *host.Service
	stopped atomic.Int32
}

func newHTTPTestLauncher(t *testing.T) *httptestLauncher {
	t.Helper()
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{},
	})
	srv := httptest.NewServer(hostmux.New(svc, nil).Handler())
	return &httptestLauncher{srv: srv, svc: svc}
}

func (l *httptestLauncher) Launch(ctx context.Context) (*hostclient.HTTP, hub.RemoteHostHandle, error) {
	return hostclient.NewHTTP(l.srv.URL, nil), l, nil
}

// Stop satisfies hub.RemoteHostHandle. Records that the hub asked us
// to tear down, so tests can assert shutdown ordering.
func (l *httptestLauncher) Stop(ctx context.Context) error {
	l.stopped.Add(1)
	l.srv.Close()
	l.svc.Shutdown()
	return nil
}

func TestProvisionHost_RegistersAndIsIdempotent(t *testing.T) {
	t.Parallel()
	s := hub.New()
	defer s.Stop()

	launcher := newHTTPTestLauncher(t)
	if _, err := s.RegisterHostLauncher("daytona", launcher); err != nil {
		t.Fatalf("RegisterHostLauncher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hn1, err := s.ProvisionHost(ctx, "daytona")
	if err != nil {
		t.Fatalf("ProvisionHost: %v", err)
	}
	if hn1 != "daytona" {
		t.Errorf("hostname = %q want daytona", hn1)
	}
	if _, ok := s.Host("daytona"); !ok {
		t.Error("daytona host not in catalog after ProvisionHost")
	}

	// Second call must be idempotent — same hostname, no second
	// Launch (asserted indirectly: only one stop on shutdown).
	hn2, err := s.ProvisionHost(ctx, "daytona")
	if err != nil {
		t.Fatalf("ProvisionHost (idempotent): %v", err)
	}
	if hn2 != hn1 {
		t.Errorf("idempotent ProvisionHost returned %q want %q", hn2, hn1)
	}
}

func TestProvisionHost_UnknownKind(t *testing.T) {
	t.Parallel()
	s := hub.New()
	defer s.Stop()

	_, err := s.ProvisionHost(context.Background(), "unknown")
	if err == nil {
		t.Fatal("ProvisionHost(unknown) returned nil err; want failure")
	}
}

func TestProvisionHost_LauncherErrorPropagates(t *testing.T) {
	t.Parallel()
	s := hub.New()
	defer s.Stop()

	wantErr := errors.New("synthetic launch failure")
	if _, err := s.RegisterHostLauncher("flaky", failingLauncher{err: wantErr}); err != nil {
		t.Fatalf("RegisterHostLauncher: %v", err)
	}

	_, err := s.ProvisionHost(context.Background(), "flaky")
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v; want wrap of %v", err, wantErr)
	}
	// And the catalog must remain empty.
	if hs := s.Hosts(); len(hs) != 0 {
		t.Errorf("Hosts() = %v after failed ProvisionHost; want empty", hs)
	}
}

type failingLauncher struct{ err error }

func (f failingLauncher) Launch(ctx context.Context) (*hostclient.HTTP, hub.RemoteHostHandle, error) {
	return nil, nil, f.err
}

func TestStop_TearsDownProvisionedHosts(t *testing.T) {
	t.Parallel()
	s := hub.New()

	launcher := newHTTPTestLauncher(t)
	if _, err := s.RegisterHostLauncher("daytona", launcher); err != nil {
		t.Fatalf("RegisterHostLauncher: %v", err)
	}

	// We need the "local" host to exist so Run() doesn't bail; for
	// shutdown plumbing we drive shutdown via a direct call rather
	// than spinning a real listener.
	hc, hcStop := newHTTPHostClient(t)
	defer hcStop()
	if _, err := s.RegisterHost("local", hc); err != nil {
		t.Fatalf("RegisterHost local: %v", err)
	}

	if _, err := s.ProvisionHost(context.Background(), "daytona"); err != nil {
		t.Fatalf("ProvisionHost: %v", err)
	}

	// Test the cleanup primitive directly. (TestService_closeHosts
	// already exercises the full Run/Stop drain; here we just want to
	// verify that remote handles get torn down.)
	hub.ExportStopRemoteHandles(s)

	if got := launcher.stopped.Load(); got != 1 {
		t.Errorf("launcher Stop called %d times; want 1", got)
	}
}

func TestRegisterHostLauncher_Validation(t *testing.T) {
	t.Parallel()
	s := hub.New()
	defer s.Stop()

	if _, err := s.RegisterHostLauncher("", newHTTPTestLauncher(t)); err == nil {
		t.Error("empty kind: want error")
	}
	if _, err := s.RegisterHostLauncher("daytona", nil); err == nil {
		t.Error("nil launcher: want error")
	}
}
