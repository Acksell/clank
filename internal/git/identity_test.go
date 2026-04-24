package git_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/git"
)

// initBareRepo creates a fresh repo with no user.name/user.email set.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func readConfig(t *testing.T, dir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--local", "--get", key)
	cmd.Dir = dir
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func TestSeedIdentityIfMissing_SeedsBlankRepo(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := git.SeedIdentityIfMissing(dir, "Alice", "a@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := readConfig(t, dir, "user.name"); got != "Alice" {
		t.Fatalf("user.name = %q, want Alice", got)
	}
	if got := readConfig(t, dir, "user.email"); got != "a@example.com" {
		t.Fatalf("user.email = %q, want a@example.com", got)
	}
}

func TestSeedIdentityIfMissing_PreservesExisting(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	mustGit(t, dir, "config", "--local", "user.name", "Bob")
	mustGit(t, dir, "config", "--local", "user.email", "b@existing.com")
	if err := git.SeedIdentityIfMissing(dir, "Alice", "a@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := readConfig(t, dir, "user.name"); got != "Bob" {
		t.Fatalf("user.name overwritten to %q, want Bob", got)
	}
	if got := readConfig(t, dir, "user.email"); got != "b@existing.com" {
		t.Fatalf("user.email overwritten to %q, want b@existing.com", got)
	}
}

func TestSeedIdentityIfMissing_FillsOnlyMissingKey(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	mustGit(t, dir, "config", "--local", "user.name", "Bob")
	if err := git.SeedIdentityIfMissing(dir, "Alice", "a@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := readConfig(t, dir, "user.name"); got != "Bob" {
		t.Fatalf("user.name overwritten to %q, want Bob", got)
	}
	if got := readConfig(t, dir, "user.email"); got != "a@example.com" {
		t.Fatalf("user.email = %q, want a@example.com", got)
	}
}

func TestSeedIdentityIfMissing_RequiresBothInputs(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	if err := git.SeedIdentityIfMissing(dir, "", "a@example.com"); err == nil {
		t.Fatal("want error for empty name, got nil")
	}
	if err := git.SeedIdentityIfMissing(dir, "Alice", ""); err == nil {
		t.Fatal("want error for empty email, got nil")
	}
}

// TestSeedIdentityIfMissing_VisibleInWorktree verifies the seeding
// holds for `git commit` inside a linked worktree of the same repo —
// this is the actual production shape (the agent commits inside a
// per-branch worktree, not the clone root).
func TestSeedIdentityIfMissing_VisibleInWorktree(t *testing.T) {
	t.Parallel()
	dir := initRepo(t)
	// Need an initial commit before adding a worktree, so set a
	// throwaway identity, commit, then unset and re-seed.
	mustGit(t, dir, "config", "--local", "user.name", "tmp")
	mustGit(t, dir, "config", "--local", "user.email", "tmp@tmp")
	mustGit(t, dir, "commit", "--allow-empty", "-m", "init")
	mustGit(t, dir, "config", "--local", "--unset", "user.name")
	mustGit(t, dir, "config", "--local", "--unset", "user.email")

	if err := git.SeedIdentityIfMissing(dir, "Alice", "a@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	wtParent := t.TempDir()
	wt := filepath.Join(wtParent, "wt")
	mustGit(t, dir, "worktree", "add", "-b", "feat", wt)

	// Commit in the worktree without setting any identity locally —
	// it must succeed because worktrees share the seed config.
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "feat work")
	cmd.Dir = wt
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit in worktree: %v\n%s", err, out)
	}
}
