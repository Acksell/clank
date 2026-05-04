package daemoncli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/daemonclient"
	"github.com/acksell/clank/internal/gateway"
	"github.com/acksell/clank/internal/host"
	hostmux "github.com/acksell/clank/internal/host/mux"
	hoststore "github.com/acksell/clank/internal/host/store"
	"github.com/acksell/clank/internal/provisioner"
)

// TestLocalE2E_TUICreatesSession_AndFetches drives the full local-mode
// wire end to end: a daemonclient (TUI's HTTP client) talks to a gateway,
// which talks to an in-process host service backed by a stub backend
// manager. POST /sessions must return a SessionInfo with a non-empty ID
// (the host generates the ID; pre-PR-3 the hub did) and GET /sessions/{id}
// must return that same SessionInfo augmented with the live backend's
// runtime status.
//
// This test exists because the PR 3 hub deletion silently broke the
// "session_id is required" contract on POST /sessions — the host's
// create handler still expected a hub-assigned ID after the hub was
// removed. The fix moves ID generation onto the host. This regression
// test pins that contract.
func TestLocalE2E_TUICreatesSession_AndFetches(t *testing.T) {
	t.Parallel()

	// Stub backend so the test never touches opencode/claude.
	stub := &stubBackendManager{}

	repo := initTestGitRepo(t)

	// Real host store at a temp DB so handleGetSession's
	// GetSessionMetadata path is exercised.
	dbPath := filepath.Join(t.TempDir(), "host.db")
	hs, err := hoststore.Open(dbPath)
	if err != nil {
		t.Fatalf("hoststore.Open: %v", err)
	}
	t.Cleanup(func() { hs.Close() })

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: stub,
		},
		ClonesDir:     t.TempDir(),
		SessionsStore: hs,
	})
	t.Cleanup(svc.Shutdown)

	hostHTTP := httptest.NewServer(hostmux.New(svc, nil).Handler())
	t.Cleanup(hostHTTP.Close)

	// Gateway in front of the host. ResolveUserID returns the laptop
	// "local" sentinel; PermissiveAuth lets every request through.
	gw, err := gateway.NewGateway(gateway.Config{
		Provisioner: &fixedHostProvisioner{
			url:       hostHTTP.URL,
			transport: http.DefaultTransport,
		},
		Auth:          gateway.PermissiveAuth{},
		ResolveUserID: func(*http.Request) string { return "local" },
	}, nil)
	if err != nil {
		t.Fatalf("gateway.NewGateway: %v", err)
	}
	gwSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(gwSrv.Close)

	// daemonclient is the TUI's HTTP client; same one production wires.
	cli := daemonclient.NewTCPClient(gwSrv.URL, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	created, err := cli.Sessions().Create(ctx, agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{LocalPath: repo, RemoteURL: "git@example.com:acme/repo.git"},
		Prompt:  "hello",
	})
	if err != nil {
		t.Fatalf("Sessions().Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Sessions().Create returned SessionInfo with empty ID")
	}
	if created.Backend != agent.BackendOpenCode {
		t.Errorf("Backend: got %q, want %q", created.Backend, agent.BackendOpenCode)
	}
	if created.Prompt != "hello" {
		t.Errorf("Prompt: got %q, want %q", created.Prompt, "hello")
	}

	// GET /sessions/{id} must return SessionInfo (not the lightweight
	// SessionSnapshot) so the TUI's Session(id).Get() works.
	got, err := cli.Session(created.ID).Get(ctx)
	if err != nil {
		t.Fatalf("Session(%s).Get: %v", created.ID, err)
	}
	if got.ID != created.ID {
		t.Errorf("ID round-trip: got %q, want %q", got.ID, created.ID)
	}
	if got.Backend != agent.BackendOpenCode {
		t.Errorf("Backend round-trip: got %q, want %q", got.Backend, agent.BackendOpenCode)
	}
}

// fixedHostProvisioner returns the same HostRef on every EnsureHost. The
// test wires it to the in-process host's HTTP test server.
type fixedHostProvisioner struct {
	url       string
	transport http.RoundTripper
}

func (f *fixedHostProvisioner) EnsureHost(context.Context, string) (provisioner.HostRef, error) {
	return provisioner.HostRef{URL: f.url, Transport: f.transport, Hostname: "local"}, nil
}
func (*fixedHostProvisioner) SuspendHost(context.Context, string) error { return nil }
func (*fixedHostProvisioner) DestroyHost(context.Context, string) error { return nil }

// stubBackendManager spawns a stubBackend on every CreateBackend.
type stubBackendManager struct{}

func (*stubBackendManager) Init(_ context.Context, _ func() ([]string, error)) error { return nil }
func (*stubBackendManager) CreateBackend(_ context.Context, _ agent.BackendInvocation) (agent.SessionBackend, error) {
	return &stubBackend{}, nil
}
func (*stubBackendManager) Shutdown() {}

type stubBackend struct{}

func (*stubBackend) Open(context.Context) error                                { return nil }
func (*stubBackend) OpenAndSend(context.Context, agent.SendMessageOpts) error  { return nil }
func (*stubBackend) Send(context.Context, agent.SendMessageOpts) error         { return nil }
func (*stubBackend) Abort(context.Context) error                               { return nil }
func (*stubBackend) Stop() error                                               { return nil }
func (*stubBackend) Status() agent.SessionStatus                               { return agent.StatusIdle }
func (*stubBackend) SessionID() string                                         { return "stub-ext-id" }
func (*stubBackend) Messages(context.Context) ([]agent.MessageData, error)     { return nil, nil }
func (*stubBackend) Revert(context.Context, string) error                      { return nil }
func (*stubBackend) Fork(context.Context, string) (agent.ForkResult, error)    { return agent.ForkResult{}, nil }
func (*stubBackend) RespondPermission(context.Context, string, bool) error     { return nil }
func (*stubBackend) Events() <-chan agent.Event {
	ch := make(chan agent.Event)
	close(ch)
	return ch
}

// initTestGitRepo creates a git repo with an "origin" remote so
// host.workDirFor accepts the LocalPath as a usable repo root.
func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("git", "init", "-b", "main")
	run("git", "config", "user.email", "t@t")
	run("git", "config", "user.name", "T")
	run("git", "remote", "add", "origin", "git@example.com:acme/repo.git")
	run("git", "commit", "--allow-empty", "-m", "initial")
	return dir
}
