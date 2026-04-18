package host_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// newTestServiceWithCloneRoot builds a Service with a temp clone_root so
// AllowClone tests don't pollute the real ~/.clank/repos.
func newTestServiceWithCloneRoot(t *testing.T) (*host.Service, string) {
	t.Helper()
	cloneRoot := t.TempDir()
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		CloneRoot: cloneRoot,
	})
	t.Cleanup(svc.Shutdown)
	return svc, cloneRoot
}

// TestCreateSession_AddByDir_Success exercises §7.5 step 4: when the repo
// is unknown but the caller passes a Dir whose remote canonicalizes to
// the requested URL, the host adds it implicitly and uses that directory.
func TestCreateSession_AddByDir_Success(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	const remote = "git@github.com:acksell/clank.git"
	dir := initGitRepo(t, remote)

	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefRemote, URL: remote},
		Dir:     dir,
		Prompt:  "hi",
	}
	_, info, err := svc.CreateSession(context.Background(), "sid-add", req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.ProjectDir != dir {
		t.Errorf("ProjectDir = %q, want %q", info.ProjectDir, dir)
	}

	// Repo should now be in the registry so a subsequent CreateSession
	// without Dir succeeds.
	got, ok := svc.Repo(host.GitRef{Kind: host.GitRefRemote, URL: remote})
	if !ok || got.RootDir != dir {
		t.Errorf("repo not added: ok=%v got=%+v", ok, got)
	}
}

// TestCreateSession_AddByDir_Mismatch exercises the §7.5 step 4 mismatch
// branch: Dir is a real git repo but its remotes don't include the
// requested URL → error, no add.
func TestCreateSession_AddByDir_Mismatch(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	dir := initGitRepo(t, "git@github.com:other/repo.git")

	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefRemote, URL: "git@github.com:acksell/clank.git"},
		Dir:     dir,
		Prompt:  "hi",
	}
	_, _, err := svc.CreateSession(context.Background(), "sid-mismatch", req)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "do not match") {
		t.Errorf("error = %v, want 'do not match' message", err)
	}
	// Repo must NOT have been added on a mismatch.
	if _, ok := svc.Repo(host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}); ok {
		t.Error("repo should not be added on mismatch")
	}
}

// TestCreateSession_AddByDir_NotRoot rejects a Dir that points inside a
// repo but isn't the root — the registry stores roots, and accepting a
// subdirectory would silently change semantics.
func TestCreateSession_AddByDir_NotRoot(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	const remote = "git@github.com:acksell/clank.git"
	root := initGitRepo(t, remote)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefRemote, URL: remote},
		Dir:     sub,
		Prompt:  "hi",
	}
	_, _, err := svc.CreateSession(context.Background(), "sid-sub", req)
	if err == nil {
		t.Fatal("expected error for non-root dir")
	}
	if !strings.Contains(err.Error(), "not the repo root") {
		t.Errorf("error = %v, want 'not the repo root' message", err)
	}
}

// TestCreateSession_NoHint exercises §7.5 step 6: unknown repo, no Dir,
// no AllowClone → ErrNotFound with a clear message.
func TestCreateSession_NoHint(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefRemote, URL: "git@github.com:acksell/clank.git"},
		Prompt:  "hi",
	}
	_, _, err := svc.CreateSession(context.Background(), "sid-nope", req)
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if !errors.Is(err, host.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestCreateSession_DirDisagreesWithStored exercises §7.5 step 2's
// auto-rebind guard: repo already known at dirA, but caller passes a
// different dirB → error (operator must edit host.json).
func TestCreateSession_DirDisagreesWithStored(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	const remote = "git@github.com:acksell/clank.git"
	dirA := initGitRepo(t, remote)
	dirB := initGitRepo(t, remote)

	if _, err := svc.AddRepo(host.GitRef{Kind: host.GitRefRemote, URL: remote}, dirA); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefRemote, URL: remote},
		Dir:     dirB,
		Prompt:  "hi",
	}
	_, _, err := svc.CreateSession(context.Background(), "sid-rebind", req)
	if err == nil {
		t.Fatal("expected rebind error, got nil")
	}
	if !strings.Contains(err.Error(), "auto-rebind is not supported") {
		t.Errorf("error = %v, want 'auto-rebind is not supported' message", err)
	}
}

// TestCreateSession_CloneByAllowClone exercises §7.5 step 5: unknown
// repo, AllowClone=true → host clones into cloneRoot, adds, and uses
// the clone.
//
// Uses a `file://` source URL so canonicalization succeeds without
// needing a git daemon. (file:// support landed in step 8c.)
func TestCreateSession_CloneByAllowClone(t *testing.T) {
	t.Parallel()
	svc, cloneRoot := newTestServiceWithCloneRoot(t)

	source := initGitRepo(t, "git@github.com:acksell/clank.git")
	sourceURL := "file://" + source
	req := agent.StartRequest{
		Backend:    agent.BackendOpenCode,
		GitRef:     agent.GitRef{Kind: agent.GitRefRemote, URL: sourceURL},
		AllowClone: true,
		Prompt:     "hi",
	}
	_, info, err := svc.CreateSession(context.Background(), "sid-clone", req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !strings.HasPrefix(info.ProjectDir, cloneRoot) {
		t.Errorf("ProjectDir %q is not under cloneRoot %q", info.ProjectDir, cloneRoot)
	}
	if _, err := os.Stat(filepath.Join(info.ProjectDir, ".git")); err != nil {
		t.Errorf("clone destination missing .git: %v", err)
	}
}

// TestCreateSession_LocalAdd exercises §7.5 step 3: unknown repo with
// GitRef.Kind=local → host adds RootDir = GitRef.Path directly, no Dir
// or AllowClone hint required.
func TestCreateSession_LocalAdd(t *testing.T) {
	t.Parallel()
	svc, _ := newTestServiceWithCloneRoot(t)

	dir := initGitRepo(t, "git@github.com:acksell/clank.git")
	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefLocal, Path: dir},
		Prompt:  "hi",
	}
	_, info, err := svc.CreateSession(context.Background(), "sid-local", req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if info.ProjectDir != dir {
		t.Errorf("ProjectDir = %q, want %q", info.ProjectDir, dir)
	}
	// Repo should now be registered under the local path canonical.
	if _, ok := svc.Repo(host.GitRef{Kind: host.GitRefLocal, Path: dir}); !ok {
		t.Errorf("expected local repo to be added to registry")
	}
}

// TestCreateSession_LocalAdd_RejectsNonGit verifies that step 3 fails
// fast when GitRef.Path is not a git repository instead of silently
// adding a bogus entry.
func TestCreateSession_LocalAdd_RejectsNonGit(t *testing.T) {
	t.Parallel()
	svc, _ := newTestServiceWithCloneRoot(t)

	dir := t.TempDir() // not a git repo
	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{Kind: agent.GitRefLocal, Path: dir},
		Prompt:  "hi",
	}
	_, _, err := svc.CreateSession(context.Background(), "sid-local-bad", req)
	if err == nil {
		t.Fatal("expected error when local path is not a git repo, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error = %v, want 'not a git repository' message", err)
	}
}
