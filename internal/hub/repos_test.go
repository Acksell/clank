package hub_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	"github.com/acksell/clank/internal/hub"
)

// noopHostBackendManager is a tiny BackendManager fixture local to this
// test file (not a mock of the SUT) so we can construct a real
// host.Service. The agents_models_test fixtures live in package hub
// internals so we can't reuse them directly.
type noopHostBackendManager struct{}

func (m *noopHostBackendManager) Init(_ context.Context, _ func() ([]string, error)) error {
	return nil
}
func (m *noopHostBackendManager) CreateBackend(_ agent.StartRequest, _ string) (agent.SessionBackend, error) {
	return nil, nil
}
func (m *noopHostBackendManager) Shutdown() {}

func initRepoForHub(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("git", "init", "-b", "main")
	run("git", "config", "user.email", "t@t")
	run("git", "config", "user.name", "T")
	run("git", "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")
	return dir
}

// TestHubReposEndToEnd exercises Phase 3B's full path:
// hubclient → hub HTTP handler → hostclient.InProcess → host.Service →
// real git. We register a real host.Service via SetHostClient, then
// drive it through the public hubclient API.
func TestHubReposEndToEnd(t *testing.T) {
	t.Parallel()

	const remote = "git@github.com:acksell/clank.git"
	dir := initRepoForHub(t, remote)

	hostSvc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopHostBackendManager{},
		},
	})
	t.Cleanup(hostSvc.Shutdown)
	if _, err := hostSvc.RegisterRepo(host.RepoRef{RemoteURL: remote}, dir); err != nil {
		t.Fatalf("RegisterRepo: %v", err)
	}

	s := hub.New()
	s.SetHostClient(hostclient.NewInProcess(hostSvc))

	client, _, cleanup := startHubOnSocket(t, s)
	defer cleanup()

	ctx := context.Background()

	// ListReposOnHost surfaces the registered repo.
	repos, err := client.ListReposOnHost(ctx, host.HostLocal)
	if err != nil {
		t.Fatalf("ListReposOnHost: %v", err)
	}
	if len(repos) != 1 || string(repos[0].ID) != "github.com/acksell/clank" {
		t.Fatalf("repos = %+v", repos)
	}

	// ListBranchesOnRepo runs against real git.
	branches, err := client.ListBranchesOnRepo(ctx, host.HostLocal, repos[0].ID)
	if err != nil {
		t.Fatalf("ListBranchesOnRepo: %v", err)
	}
	if len(branches) == 0 || branches[0].Name != "main" {
		t.Fatalf("branches = %+v", branches)
	}

	// CreateWorktreeOnRepo creates a new branch + worktree.
	wt, err := client.CreateWorktreeOnRepo(ctx, host.HostLocal, repos[0].ID, "feat/x")
	if err != nil {
		t.Fatalf("CreateWorktreeOnRepo: %v", err)
	}
	if wt.WorktreeDir == "" || wt.Branch != "feat/x" {
		t.Fatalf("wt = %+v", wt)
	}
	t.Cleanup(func() { _ = os.RemoveAll(wt.WorktreeDir) })

	// RemoveWorktreeOnRepo cleans up.
	if err := client.RemoveWorktreeOnRepo(ctx, host.HostLocal, repos[0].ID, "feat/x", true); err != nil {
		t.Fatalf("RemoveWorktreeOnRepo: %v", err)
	}
}

// TestHubReposUnknownHost verifies the 404 path when hostID is not
// registered in the catalog.
func TestHubReposUnknownHost(t *testing.T) {
	t.Parallel()

	s := hub.New()
	// no host registered

	client, _, cleanup := startHubOnSocket(t, s)
	defer cleanup()

	if _, err := client.ListReposOnHost(context.Background(), "ghost"); err == nil {
		t.Fatal("expected error for unknown host")
	}
}
