package git

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/acksell/clank/internal/agent"
)

// initBareRepo creates a bare git repo at a fresh tempdir and returns
// its path. The bare repo acts as the "remote" in push tests; no
// network involved.
func initBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "--bare")
	return dir
}

// cloneWithRemote clones bare into a fresh tempdir, sets a starter
// commit (inherited from initTestRepo-style setup on the working
// copy), and returns the working-copy path. The "remote" name is
// always "origin".
func cloneWithRemote(t *testing.T, bare string) string {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "work")
	run(t, parent, "git", "clone", bare, "work")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	return dir
}

// commitFile writes a file with content and commits it. Returns the
// new branch's name (assumes current branch).
func commitFile(t *testing.T, dir, name, content, message string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	run(t, dir, "git", "add", ".")
	run(t, dir, "git", "commit", "-m", message)
}

func TestPush_NewBranch_Success(t *testing.T) {
	t.Parallel()
	bare := initBareRepo(t)
	work := cloneWithRemote(t, bare)

	// Bare has no commits yet — seed the default branch so future
	// force-check paths have something to compare against.
	commitFile(t, work, "README.md", "# hello\n", "initial")
	run(t, work, "git", "checkout", "-b", "feature/first")
	commitFile(t, work, "a.txt", "a\n", "add a")

	ctx := context.Background()
	err := Push(ctx, work, "origin", "feature/first", agent.GitCredential{Kind: agent.GitCredAnonymous})
	if err != nil {
		t.Fatalf("Push new branch: %v", err)
	}
}

func TestPush_NothingToPush(t *testing.T) {
	t.Parallel()
	bare := initBareRepo(t)
	work := cloneWithRemote(t, bare)
	commitFile(t, work, "README.md", "# hello\n", "initial")
	run(t, work, "git", "checkout", "-b", "feature/stable")
	commitFile(t, work, "a.txt", "a\n", "add a")

	ctx := context.Background()
	cred := agent.GitCredential{Kind: agent.GitCredAnonymous}
	if err := Push(ctx, work, "origin", "feature/stable", cred); err != nil {
		t.Fatalf("initial Push: %v", err)
	}
	err := Push(ctx, work, "origin", "feature/stable", cred)
	if !errors.Is(err, ErrNothingToPush) {
		t.Fatalf("second Push: expected ErrNothingToPush, got %v", err)
	}
}

func TestPush_NonFastForward_Rejected(t *testing.T) {
	t.Parallel()
	bare := initBareRepo(t)

	// Two independent working copies sharing the same bare remote.
	workA := cloneWithRemote(t, bare)
	commitFile(t, workA, "README.md", "# hello\n", "initial")
	run(t, workA, "git", "checkout", "-b", "feature/race")
	commitFile(t, workA, "a.txt", "a\n", "A adds a")

	ctx := context.Background()
	cred := agent.GitCredential{Kind: agent.GitCredAnonymous}
	if err := Push(ctx, workA, "origin", "feature/race", cred); err != nil {
		t.Fatalf("A push: %v", err)
	}

	// B clones the remote (so it sees A's work), then rewrites
	// history locally to force a divergence.
	workB := cloneWithRemote(t, bare)
	run(t, workB, "git", "checkout", "feature/race")
	run(t, workB, "git", "reset", "--hard", "HEAD~1") // drop A's commit
	commitFile(t, workB, "b.txt", "b\n", "B rewrites")

	err := Push(ctx, workB, "origin", "feature/race", cred)
	if !errors.Is(err, ErrPushRejected) {
		t.Fatalf("B push: expected ErrPushRejected, got %v", err)
	}
}

func TestPush_Validation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cred := agent.GitCredential{Kind: agent.GitCredAnonymous}

	if err := Push(ctx, ".", "", "main", cred); err == nil {
		t.Error("expected error for empty remote")
	}
	if err := Push(ctx, ".", "origin", "", cred); err == nil {
		t.Error("expected error for empty branch")
	}
	if err := Push(ctx, ".", "origin", "main", agent.GitCredential{Kind: "bogus"}); err == nil {
		t.Error("expected error for invalid credential kind")
	}
}
