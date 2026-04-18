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
	"github.com/acksell/clank/internal/host/repostore"
)

// noopBackendManager is a fixture (not a mock) used to construct a
// host.Service in tests that exercise non-backend code paths
// (CreateSession registration, repo registry).
type noopBackendManager struct {
	created agent.StartRequest
}

func (m *noopBackendManager) Init(_ context.Context, _ func() ([]string, error)) error { return nil }
func (m *noopBackendManager) CreateBackend(req agent.StartRequest, workDir string) (agent.SessionBackend, error) {
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
// host can canonicalize a GitRef and call git ops.
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

func TestService_AddRepoAndLookup(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	ref := host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}

	repo, err := svc.AddRepo(ref, dir)
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if got := repo.Ref.Canonical(); got != "github.com/acksell/clank" {
		t.Errorf("Ref.Canonical = %q", got)
	}
	if repo.RootDir != dir {
		t.Errorf("RootDir = %q, want %q", repo.RootDir, dir)
	}

	got, ok := svc.Repo(repo.Ref)
	if !ok || got.RootDir != dir {
		t.Errorf("Repo lookup mismatch: ok=%v got=%+v", ok, got)
	}

	all, err := svc.ListRepos(context.Background())
	if err != nil || len(all) != 1 || all[0].Ref.Canonical() != repo.Ref.Canonical() {
		t.Errorf("ListRepos = %+v err=%v", all, err)
	}
}

func TestService_AddRepoValidation(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	if _, err := svc.AddRepo(host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:a/b.git"}, ""); err == nil {
		t.Error("empty rootDir should error")
	}
	if _, err := svc.AddRepo(host.GitRef{}, "/tmp"); err == nil {
		t.Error("empty GitRef should error")
	}
	if _, err := svc.AddRepo(host.GitRef{Kind: host.GitRefRemote, URL: "://invalid"}, "/tmp"); err == nil {
		t.Error("invalid URL should error")
	}
}

// TestService_CreateSessionRequiresRegisteredRepo verifies that CreateSession
// errors when the repo hasn't been pre-registered, and succeeds once it is.
// This replaces the legacy lazy auto-registration path (Phase 3D-2).
func TestService_CreateSessionRequiresRegisteredRepo(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	req := agent.StartRequest{
		Backend:       agent.BackendOpenCode,
		RepoRemoteURL: "git@github.com:acksell/clank.git",
		Prompt:        "hi",
	}

	if _, _, err := svc.CreateSession(context.Background(), "sid-1", req); err == nil {
		t.Fatal("CreateSession should error when repo not registered")
	}

	if _, err := svc.AddRepo(host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}, dir); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	_, info, err := svc.CreateSession(context.Background(), "sid-1", req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.ProjectDir != dir {
		t.Errorf("CreateInfo.ProjectDir = %q, want %q", info.ProjectDir, dir)
	}
}

// TestService_ByRepoMethods_NotFound verifies that the *ByRepo helpers
// return host.ErrNotFound when the gitRef is unknown.
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
	repo, err := svc.AddRepo(host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}, dir)
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	branches, err := svc.ListBranchesByRepo(context.Background(), repo.Ref.Canonical())
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

// TestService_RepoStore_PersistsAcrossRestart verifies the end-to-end
// write-through path through host.Service: register a repo on a fresh
// service backed by a real SQLite store, shut it down, re-open a new
// service against the same DB, and confirm the in-memory registry was
// preloaded so subsequent lookups (and CreateSession's repoRoot
// resolution) succeed without re-registration. This is the core
// behavioural contract for §7.6 / step 5.
func TestService_RepoStore_PersistsAcrossRestart(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "host.json")
	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	ref := host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}

	// First service: register, persist, shut down.
	store1, err := repostore.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc1 := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		RepoStore: store1,
	})
	if _, err := svc1.AddRepo(ref, dir); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	svc1.Shutdown()
	if err := store1.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Second service: same DB, no explicit AddRepo call. The repo
	// should be hot in the in-memory registry by virtue of the preload.
	store2, err := repostore.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })
	svc2 := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		RepoStore: store2,
	})
	t.Cleanup(svc2.Shutdown)

	got, ok := svc2.Repo(ref)
	if !ok {
		t.Fatal("repo should be preloaded after restart")
	}
	if got.RootDir != dir {
		t.Errorf("preloaded RootDir = %q, want %q", got.RootDir, dir)
	}
	if got.Ref.Canonical() != ref.Canonical() {
		t.Errorf("preloaded canonical = %q, want %q", got.Ref.Canonical(), ref.Canonical())
	}

	// And the higher-level path that depends on the registry — CreateSession
	// — succeeds without an intervening AddRepo.
	req := agent.StartRequest{
		Backend:       agent.BackendOpenCode,
		RepoRemoteURL: "git@github.com:acksell/clank.git",
		Prompt:        "hi",
	}
	if _, info, err := svc2.CreateSession(context.Background(), "sid-after-restart", req); err != nil {
		t.Fatalf("CreateSession after restart: %v", err)
	} else if info.ProjectDir != dir {
		t.Errorf("CreateSession.ProjectDir = %q, want %q", info.ProjectDir, dir)
	}
}
