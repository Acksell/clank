package host_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// noopBackendManager is a fixture (not a mock) used to construct a
// host.Service in tests that exercise non-backend code paths
// (CreateSession registration, repo registry).
type noopBackendManager struct {
	created agent.StartRequest
}

func (m *noopBackendManager) Init(_ context.Context, _ func() ([]string, error)) error { return nil }
func (m *noopBackendManager) CreateBackend(req agent.StartRequest) (agent.SessionBackend, error) {
	m.created = req
	return &noopBackend{}, nil
}
func (m *noopBackendManager) Shutdown() {}

type noopBackend struct{}

func (b *noopBackend) Start(_ context.Context, _ agent.StartRequest) error { return nil }
func (b *noopBackend) Watch(_ context.Context) error                       { return nil }
func (b *noopBackend) SendMessage(_ context.Context, _ agent.SendMessageOpts) error {
	return nil
}
func (b *noopBackend) Abort(_ context.Context) error { return nil }
func (b *noopBackend) Stop() error                   { return nil }
func (b *noopBackend) Events() <-chan agent.Event    { return nil }
func (b *noopBackend) Status() agent.SessionStatus   { return agent.StatusIdle }
func (b *noopBackend) SessionID() string             { return "stub" }
func (b *noopBackend) Messages(_ context.Context) ([]agent.MessageData, error) {
	return nil, nil
}
func (b *noopBackend) Revert(_ context.Context, _ string) error { return nil }
func (b *noopBackend) Fork(_ context.Context, _ string) (agent.ForkResult, error) {
	return agent.ForkResult{}, nil
}
func (b *noopBackend) RespondPermission(_ context.Context, _ string, _ bool) error { return nil }

func newTestService(t *testing.T) *host.Service {
	t.Helper()
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
	})
	t.Cleanup(svc.Shutdown)
	return svc
}

// initGitRepo creates a real git repo with an "origin" remote so the
// host can resolve a RepoRef → RepoID and call git ops.
func initGitRepo(t *testing.T, remote string) string {
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
	run("git", "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "initial")
	return dir
}

func TestService_RegisterRepoAndLookup(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	ref := host.RepoRef{RemoteURL: "git@github.com:acksell/clank.git"}

	repo, err := svc.RegisterRepo(ref, dir)
	if err != nil {
		t.Fatalf("RegisterRepo: %v", err)
	}
	if string(repo.ID) != "github.com/acksell/clank" {
		t.Errorf("ID = %q", repo.ID)
	}
	if repo.RootDir != dir {
		t.Errorf("RootDir = %q, want %q", repo.RootDir, dir)
	}

	got, ok := svc.Repo(repo.ID)
	if !ok || got.RootDir != dir {
		t.Errorf("Repo lookup mismatch: ok=%v got=%+v", ok, got)
	}

	all, err := svc.ListRepos(context.Background())
	if err != nil || len(all) != 1 || all[0].ID != repo.ID {
		t.Errorf("ListRepos = %+v err=%v", all, err)
	}
}

func TestService_RegisterRepoValidation(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	if _, err := svc.RegisterRepo(host.RepoRef{RemoteURL: "git@github.com:a/b.git"}, ""); err == nil {
		t.Error("empty rootDir should error")
	}
	if _, err := svc.RegisterRepo(host.RepoRef{}, "/tmp"); err == nil {
		t.Error("empty RemoteURL should error")
	}
	if _, err := svc.RegisterRepo(host.RepoRef{RemoteURL: "://invalid"}, "/tmp"); err == nil {
		t.Error("invalid RemoteURL should error")
	}
}

// TestService_CreateSessionAutoRegistersRepo verifies the lazy
// auto-registration path: when CreateSession sees both RepoRemoteURL
// and ProjectDir, it records the repo so subsequent /repos/{id}/...
// routes can resolve the host's filesystem path.
func TestService_CreateSessionAutoRegistersRepo(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	_, err := svc.CreateSession(context.Background(), "sid-1", agent.StartRequest{
		Backend:       agent.BackendOpenCode,
		ProjectDir:    dir,
		RepoRemoteURL: "git@github.com:acksell/clank.git",
		Prompt:        "hi",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	repo, ok := svc.Repo("github.com/acksell/clank")
	if !ok {
		t.Fatal("repo not auto-registered")
	}
	if repo.RootDir != dir {
		t.Errorf("RootDir = %q, want %q", repo.RootDir, dir)
	}
}

// TestService_ByRepoMethods_NotFound verifies that the *ByRepo helpers
// return host.ErrNotFound when the RepoID is unknown.
func TestService_ByRepoMethods_NotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if _, err := svc.ListBranchesByRepo(ctx, "ghost/repo"); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("ListBranchesByRepo: err=%v, want ErrNotFound", err)
	}
	if _, err := svc.ResolveWorktreeByRepo(ctx, "ghost/repo", "feat"); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("ResolveWorktreeByRepo: err=%v, want ErrNotFound", err)
	}
	if err := svc.RemoveWorktreeByRepo(ctx, "ghost/repo", "feat", false); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("RemoveWorktreeByRepo: err=%v, want ErrNotFound", err)
	}
	if _, err := svc.MergeBranchByRepo(ctx, "ghost/repo", "feat", ""); !errors.Is(err, host.ErrNotFound) {
		t.Errorf("MergeBranchByRepo: err=%v, want ErrNotFound", err)
	}
}

// TestService_ListBranchesByRepo runs against a real git repo to
// verify the full pipeline (register → repoRoot → ListBranches → git).
func TestService_ListBranchesByRepo(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	repo, err := svc.RegisterRepo(host.RepoRef{RemoteURL: "git@github.com:acksell/clank.git"}, dir)
	if err != nil {
		t.Fatalf("RegisterRepo: %v", err)
	}

	branches, err := svc.ListBranchesByRepo(context.Background(), repo.ID)
	if err != nil {
		t.Fatalf("ListBranchesByRepo: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("expected at least one branch (main)")
	}
	if branches[0].Name != "main" {
		t.Errorf("branches[0].Name = %q, want main", branches[0].Name)
	}
}
