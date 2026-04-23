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

// Push tests use a bare repo as the "origin" remote (no network) and a
// clone as the work dir. The feature branch is checked out in the
// main worktree of the clone — pushBranch uses FindWorktreeForBranch
// to locate it. This mirrors the happy-path shape on real hosts where
// the session bootstrap has created a secondary worktree for the
// feature branch and PushBranch targets that worktree.

// initPushFixture returns (workDir, bareRemote). The work dir is on
// branch `featureBranch` with one commit beyond main.
func initPushFixture(t *testing.T, featureBranch string) (string, string) {
	t.Helper()
	bareParent := t.TempDir()
	bare := filepath.Join(bareParent, "remote.git")
	mustRun(t, bareParent, "git", "init", "--bare", "-b", "main", "remote.git")

	workParent := t.TempDir()
	work := filepath.Join(workParent, "work")
	mustRun(t, workParent, "git", "clone", bare, "work")
	mustRun(t, work, "git", "config", "user.email", "t@t")
	mustRun(t, work, "git", "config", "user.name", "T")
	// Seed main with an initial commit and publish it — so `main`
	// exists on the remote as the default branch ref.
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "checkout", "-b", "main")
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "commit", "-m", "initial")
	mustRun(t, work, "git", "push", "-u", "origin", "main")

	// Branch the feature from main and add a commit.
	mustRun(t, work, "git", "checkout", "-b", featureBranch)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", ".")
	mustRun(t, work, "git", "commit", "-m", "add a")
	return work, bare
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestPushBranch_NewBranch(t *testing.T) {
	t.Parallel()
	work, _ := initPushFixture(t, "feature/a")

	svc := newTestService(t)
	res, err := svc.PushBranch(context.Background(),
		agent.GitRef{LocalPath: work},
		agent.GitCredential{Kind: agent.GitCredAnonymous},
		"feature/a")
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if res.Branch != "feature/a" || res.Remote != "origin" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if res.CommitsAhead != 1 {
		t.Errorf("CommitsAhead = %d, want 1", res.CommitsAhead)
	}
}

func TestPushBranch_RejectsDefaultBranch(t *testing.T) {
	t.Parallel()
	work, _ := initPushFixture(t, "feature/a")

	svc := newTestService(t)
	_, err := svc.PushBranch(context.Background(),
		agent.GitRef{LocalPath: work},
		agent.GitCredential{Kind: agent.GitCredAnonymous},
		"main")
	if !errors.Is(err, host.ErrCannotPushDefault) {
		t.Fatalf("expected ErrCannotPushDefault, got %v", err)
	}
}

func TestPushBranch_NothingToPush(t *testing.T) {
	t.Parallel()
	work, _ := initPushFixture(t, "feature/b")

	svc := newTestService(t)
	ctx := context.Background()
	ref := agent.GitRef{LocalPath: work}
	cred := agent.GitCredential{Kind: agent.GitCredAnonymous}

	if _, err := svc.PushBranch(ctx, ref, cred, "feature/b"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	_, err := svc.PushBranch(ctx, ref, cred, "feature/b")
	if !errors.Is(err, host.ErrNothingToPush) {
		t.Fatalf("second push: expected ErrNothingToPush, got %v", err)
	}
}

func TestPushBranch_UnknownBranch(t *testing.T) {
	t.Parallel()
	work, _ := initPushFixture(t, "feature/a")

	svc := newTestService(t)
	_, err := svc.PushBranch(context.Background(),
		agent.GitRef{LocalPath: work},
		agent.GitCredential{Kind: agent.GitCredAnonymous},
		"feature/does-not-exist")
	if !errors.Is(err, host.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
