package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// LocalGlobalIdentity must surface real `git config --global --get`
// failures (broken config, missing git, permissions) instead of
// reporting them as the misleading "is not set; run `git config ...`"
// hint. We stub `git` with a shell script that exits 128, the code
// git uses for fatal config errors, and assert the error wraps the
// non-1 exit code with stderr context rather than the "not set"
// message.
//
// Cannot t.Parallel: PATH is process-wide.
func TestLocalGlobalIdentity_PropagatesNonExit1Failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script git stub is POSIX-only")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "git")
	script := "#!/bin/sh\necho 'fatal: bad config line' >&2\nexit 128\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("PATH", dir)

	_, _, err := git.LocalGlobalIdentity()
	if err == nil {
		t.Fatal("expected error from broken git, got nil")
	}
	if strings.Contains(err.Error(), "is not set") {
		t.Fatalf("error masks real failure as 'not set': %v", err)
	}
	if !strings.Contains(err.Error(), "exited 128") {
		t.Fatalf("error should mention real exit code 128, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bad config line") {
		t.Fatalf("error should include stderr context, got: %v", err)
	}
}

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

	// And the resulting commit must actually carry the seeded
	// identity. Without this assertion the test passes even if the
	// seed silently regressed to e.g. an empty/fallback name —
	// `git commit --allow-empty` would still succeed because the
	// pre-existing throwaway identity (or any other side-channel)
	// could satisfy git. (CodeRabbit PR #3 inline 3137099802.)
	logCmd := exec.Command("git", "log", "-1", "--format=%an <%ae>")
	logCmd.Dir = wt
	out, err := logCmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "Alice <a@example.com>"
	if got != want {
		t.Fatalf("worktree commit author = %q, want %q", got, want)
	}
}
