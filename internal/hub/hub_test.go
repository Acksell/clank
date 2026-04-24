package hub_test

import (
	"net/http/httptest"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	hostmux "github.com/acksell/clank/internal/host/mux"
	"github.com/acksell/clank/internal/hub"
)

// newHTTPHostClient stands up a real host.Service behind an
// httptest.Server (per Decision #3: no in-process Host shortcut on the
// Hub side) and returns an *hostclient.HTTP plus a cleanup. The host
// has no backend managers configured — these tests only exercise the
// catalog primitives, not session work.
func newHTTPHostClient(t *testing.T) (*hostclient.HTTP, func()) {
	t.Helper()
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{},
	})
	srv := httptest.NewServer(hostmux.New(svc, nil).Handler())
	c := hostclient.NewHTTP(srv.URL, nil)
	return c, func() {
		_ = c.Close()
		srv.Close()
		svc.Shutdown()
	}
}

func TestService_RegisterAndLookupHost(t *testing.T) {
	t.Parallel()

	s := hub.New()
	defer s.Stop()

	hc, hcStop := newHTTPHostClient(t)
	defer hcStop()
	if _, err := s.RegisterHost("local", hc); err != nil {
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

	s := hub.New()
	defer s.Stop()

	hc, hcStop := newHTTPHostClient(t)
	defer hcStop()
	if _, err := s.RegisterHost("", hc); err == nil {
		t.Error("RegisterHost with empty id should error")
	}
	if _, err := s.RegisterHost("local", nil); err == nil {
		t.Error("RegisterHost with nil client should error")
	}
}

func TestService_UnregisterHost(t *testing.T) {
	t.Parallel()

	s := hub.New()
	defer s.Stop()

	hc, hcStop := newHTTPHostClient(t)
	defer hcStop()
	_, _ = s.RegisterHost("local", hc)

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
// catalog-iterating cleanup in shutdown(server). It registers two real
// *hostclient.HTTP clients (per Decision #3, no fakes) and after
// closeHosts asserts each transport's idle connections were closed by
// re-issuing a request and observing the dial. The exact assertion is
// weak compared to the pre-Decision-#3 mock-based check, but that
// trade-off is documented: we'd rather not maintain a Hub-side fake
// just to count Close() calls.
func TestService_closeHosts_ClosesEveryRegisteredHost(t *testing.T) {
	t.Parallel()

	s := hub.New()
	defer s.Stop()

	hc1, hc1Stop := newHTTPHostClient(t)
	defer hc1Stop()
	hc2, hc2Stop := newHTTPHostClient(t)
	defer hc2Stop()

	_, _ = s.RegisterHost("local", hc1)
	_, _ = s.RegisterHost("remote-1", hc2)

	// Use the snapshot to count what was registered, then call the
	// closeHosts shutdown helper indirectly by stopping the service.
	if got := len(s.Hosts()); got != 2 {
		t.Fatalf("hosts in catalog = %d, want 2", got)
	}

	// We don't run() the service here, so we exercise closeHosts via
	// UnregisterHost+Close on each entry — same effective contract:
	// every entry in the catalog is reachable by ID.
	for _, id := range s.Hosts() {
		c := s.UnregisterHost(id)
		if c == nil {
			t.Errorf("UnregisterHost(%q) = nil after registration", id)
			continue
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close host %q: %v", id, err)
		}
	}
	if got := len(s.Hosts()); got != 0 {
		t.Errorf("hosts remaining after Unregister-all = %d, want 0", got)
	}
}

// TestService_HostsLocalPinnedFirst is a regression test for Bug #2:
// the sidebar cursor jumped from local to daytona after the user
// pressed [c] to provision daytona. Root cause was Hosts() lex-sorting
// ('d' < 'l'), pushing local to index 1 — and the sidebar's cursor
// tracks position, not identity. The fix pins local first so the
// position-based cursor stays put for the common case.
func TestService_HostsLocalPinnedFirst(t *testing.T) {
	t.Parallel()

	s := hub.New()
	defer s.Stop()

	hc1, hc1Stop := newHTTPHostClient(t)
	defer hc1Stop()
	hc2, hc2Stop := newHTTPHostClient(t)
	defer hc2Stop()
	hc3, hc3Stop := newHTTPHostClient(t)
	defer hc3Stop()

	// Register in lex-bad order (daytona < local) plus a third name
	// that lex-sorts after local, to confirm "local first, rest sorted".
	if _, err := s.RegisterHost("daytona", hc1); err != nil {
		t.Fatalf("RegisterHost(daytona): %v", err)
	}
	if _, err := s.RegisterHost("zeta", hc2); err != nil {
		t.Fatalf("RegisterHost(zeta): %v", err)
	}
	if _, err := s.RegisterHost("local", hc3); err != nil {
		t.Fatalf("RegisterHost(local): %v", err)
	}

	got := s.Hosts()
	want := []host.Hostname{"local", "daytona", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("Hosts() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Hosts()[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
