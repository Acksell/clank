package hub_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	hostmux "github.com/acksell/clank/internal/host/mux"
	"github.com/acksell/clank/internal/hub"
)

// remoteHostLauncher is like httptestLauncher but lets the test inject
// BackendManagers so CreateSession can actually succeed against the
// remote sandbox. Used to verify that the hub rewrites scp-form SSH
// URLs to HTTPS when forwarding to a non-local host.
type remoteHostLauncher struct {
	srv *httptest.Server
	svc *host.Service
}

func newRemoteHostLauncher(t *testing.T, mgrs map[agent.BackendType]agent.BackendManager) *remoteHostLauncher {
	t.Helper()
	svc := host.New(host.Options{
		BackendManagers: mgrs,
		ClonesDir:       t.TempDir(),
	})
	if err := svc.Init(context.Background(), func(agent.BackendType) ([]string, error) { return nil, nil }); err != nil {
		t.Fatalf("host.Init: %v", err)
	}
	srv := httptest.NewServer(hostmux.New(svc, nil).Handler())
	t.Cleanup(func() {
		srv.Close()
		svc.Shutdown()
	})
	return &remoteHostLauncher{srv: srv, svc: svc}
}

func (l *remoteHostLauncher) Launch(ctx context.Context) (*hostclient.HTTP, hub.RemoteHostHandle, error) {
	return hostclient.NewHTTP(l.srv.URL, nil), l, nil
}

func (l *remoteHostLauncher) Stop(ctx context.Context) error { return nil }

// TestCreateSession_RewritesSSHToHTTPSForRemoteHost exercises the hub-side
// rewrite added to fix the Daytona "stuck in empty clone dir" bug.
//
// When forwarding a CreateSession to a non-local host, an scp-form SSH
// URL like "git@github.com:acksell/clank.git" must be rewritten to its
// HTTPS equivalent. Remote sandboxes start without SSH credentials so
// the SSH form would hang on the host-key prompt (or fail auth) and
// leave a partial clone behind.
//
// Setup: provision a fake remote host whose backend can succeed without
// touching the network by giving the request a LocalPath that already
// exists (the host treats LocalPath as authoritative when present).
// Even though no real clone happens, the rewrite must still be applied
// to the GitRef that the hub persists and returns to the client.
func TestCreateSession_RewritesSSHToHTTPSForRemoteHost(t *testing.T) {
	t.Parallel()

	mgr := newMockBackendManager()
	s := hub.New()
	s.BackendManagers[agent.BackendOpenCode] = mgr
	s.BackendManagers[agent.BackendClaudeCode] = mgr

	// Register the remote-sandbox launcher under a non-local kind.
	launcher := newRemoteHostLauncher(t, s.BackendManagers)
	if _, err := s.RegisterHostLauncher("daytona", launcher); err != nil {
		t.Fatalf("RegisterHostLauncher: %v", err)
	}

	client, _, cleanup := startHubOnSocket(t, s)
	defer cleanup()
	registerTestRepo(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Provision the remote host so subsequent CreateSession can target it.
	hn, err := s.ProvisionHost(ctx, "daytona")
	if err != nil {
		t.Fatalf("ProvisionHost: %v", err)
	}

	// Use a real local repo for LocalPath so the remote (in-process)
	// host treats it as authoritative and skips cloning entirely. The
	// rewrite must still apply to the persisted RemoteURL.
	localRepo := initGitRepo(t)

	const sshURL = "git@github.com:acksell/clank.git"
	const wantHTTPS = "https://github.com/acksell/clank.git"

	info, err := client.Sessions().Create(ctx, agent.StartRequest{
		Backend:  agent.BackendOpenCode,
		Hostname: string(hn),
		GitRef:   agent.GitRef{LocalPath: localRepo, RemoteURL: sshURL},
		Prompt:   "test rewrite",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if info.GitRef.RemoteURL != wantHTTPS {
		t.Errorf("RemoteURL on returned info = %q, want %q", info.GitRef.RemoteURL, wantHTTPS)
	}

	got, err := client.Session(info.ID).Get(ctx)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.GitRef.RemoteURL != wantHTTPS {
		t.Errorf("RemoteURL on persisted session = %q, want %q", got.GitRef.RemoteURL, wantHTTPS)
	}
}

// TestCreateSession_PreservesSSHURLForLocalHost is the negative companion:
// the local host has the user's SSH agent and credentials, so the hub
// must NOT rewrite SSH URLs there. (TestDaemonCreateSession covers this
// implicitly via the default hostname; this test is explicit so a
// future refactor that drops the gating can't silently regress.)
func TestCreateSession_PreservesSSHURLForLocalHost(t *testing.T) {
	t.Parallel()
	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	info, err := client.Sessions().Create(ctx, agent.StartRequest{
		Backend:  agent.BackendOpenCode,
		Hostname: "local",
		GitRef:   agent.GitRef{RemoteURL: testRemoteURL}, // scp-form
		Prompt:   "no rewrite",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.GitRef.RemoteURL != testRemoteURL {
		t.Errorf("RemoteURL = %q, want unchanged %q (local host must not rewrite)", info.GitRef.RemoteURL, testRemoteURL)
	}
}
