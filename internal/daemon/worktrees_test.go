package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/daemon"
)

func TestMergeWorktree_HappyPath(t *testing.T) {
	t.Parallel()

	// Set up a real git repo with a feature branch and worktree.
	repoDir := initGitRepo(t)

	// Create a feature branch with a worktree.
	// Resolve symlinks so paths match what git worktree list reports
	// (on macOS /var → /private/var).
	wtDir := filepath.Join(t.TempDir(), "wt-merge")
	gitRun(t, repoDir, "worktree", "add", "-b", "feat/merge", wtDir, "main")
	wtDir, _ = filepath.EvalSymlinks(wtDir)

	// Make a commit on the feature branch (pre-existing committed work).
	gitWriteFile(t, filepath.Join(wtDir, "feature.txt"), "feature work\n")
	gitRun(t, wtDir, "add", ".")
	gitRun(t, wtDir, "commit", "-m", "add feature work")

	// Also leave an uncommitted file — the daemon should auto-commit it.
	gitWriteFile(t, filepath.Join(wtDir, "uncommitted.txt"), "agent-created file\n")

	// Start the daemon with sessions on the worktree.
	d, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()

	// Inject a session whose ProjectDir is the worktree path, simulating
	// a session that was running in the worktree.
	d.InjectSession(agent.SessionInfo{
		ID:         "ses-on-worktree",
		Status:     agent.StatusIdle,
		ProjectDir: wtDir,
		UpdatedAt:  time.Now(),
	})

	// Verify the session exists and is visible.
	sessions, err := client.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.ID == "ses-on-worktree" {
			if s.Visibility != "" && s.Visibility != agent.VisibilityVisible {
				t.Errorf("session visibility = %q, want visible", s.Visibility)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("injected session not found in ListSessions")
	}

	// Merge the feature branch. CommitMessage is the worktree commit message
	// (used to commit the uncommitted.txt file before merging).
	resp, err := client.MergeWorktree(ctx, daemon.MergeWorktreeRequest{
		ProjectDir:    repoDir,
		Branch:        "feat/merge",
		CommitMessage: "commit remaining agent work",
	})
	if err != nil {
		t.Fatalf("MergeWorktree: %v", err)
	}

	if resp.Status != "merged" {
		t.Errorf("status = %q, want merged", resp.Status)
	}
	if resp.MergedBranch != "feat/merge" {
		t.Errorf("merged_branch = %q, want feat/merge", resp.MergedBranch)
	}
	if resp.SessionsDone != 1 {
		t.Errorf("sessions_done = %d, want 1", resp.SessionsDone)
	}
	if !resp.WorktreeRemoved {
		t.Error("expected worktree_removed = true")
	}
	if !resp.BranchDeleted {
		t.Error("expected branch_deleted = true")
	}

	// Verify both the pre-committed and auto-committed files exist on main.
	if _, err := os.Stat(filepath.Join(repoDir, "feature.txt")); os.IsNotExist(err) {
		t.Error("feature.txt not present on main after merge")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "uncommitted.txt")); os.IsNotExist(err) {
		t.Error("uncommitted.txt not present on main after merge (auto-commit failed)")
	}

	// Verify the merge commit message was auto-generated.
	mergeLog := gitRun(t, repoDir, "log", "-1", "--format=%s")
	if !strings.Contains(mergeLog, "Merge branch 'feat/merge'") {
		t.Errorf("merge commit message = %q, expected auto-generated message", strings.TrimSpace(mergeLog))
	}

	// Verify the session was marked as done.
	sessions, err = client.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions after merge: %v", err)
	}
	for _, s := range sessions {
		if s.ID == "ses-on-worktree" {
			if s.Visibility != agent.VisibilityDone {
				t.Errorf("session visibility after merge = %q, want done", s.Visibility)
			}
		}
	}
}

func TestMergeWorktree_DirtyMainFails(t *testing.T) {
	t.Parallel()

	repoDir := initGitRepo(t)

	// Create a feature branch with worktree.
	wtDir := filepath.Join(t.TempDir(), "wt-dirty")
	gitRun(t, repoDir, "worktree", "add", "-b", "feat/dirty-test", wtDir, "main")
	gitWriteFile(t, filepath.Join(wtDir, "change.txt"), "change\n")
	gitRun(t, wtDir, "add", ".")
	gitRun(t, wtDir, "commit", "-m", "add change")

	// Make main dirty by modifying a tracked file (untracked files are
	// ignored by IsClean so they wouldn't block the merge).
	gitWriteFile(t, filepath.Join(repoDir, "README.md"), "dirty modification\n")

	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	_, err := client.MergeWorktree(ctx, daemon.MergeWorktreeRequest{
		ProjectDir: repoDir,
		Branch:     "feat/dirty-test",
	})
	if err == nil {
		t.Fatal("expected error for dirty main worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("error = %q, expected to mention uncommitted changes", err.Error())
	}
}

func TestMergeWorktree_ConflictAborts(t *testing.T) {
	t.Parallel()

	repoDir := initGitRepo(t)

	// Create conflicting changes.
	wtDir := filepath.Join(t.TempDir(), "wt-conflict")
	gitRun(t, repoDir, "worktree", "add", "-b", "feat/conflict", wtDir, "main")
	gitWriteFile(t, filepath.Join(wtDir, "README.md"), "branch version\n")
	gitRun(t, wtDir, "add", ".")
	gitRun(t, wtDir, "commit", "-m", "branch change to readme")

	// Conflicting change on main.
	gitWriteFile(t, filepath.Join(repoDir, "README.md"), "main version\n")
	gitRun(t, repoDir, "add", ".")
	gitRun(t, repoDir, "commit", "-m", "main change to readme")

	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	_, err := client.MergeWorktree(ctx, daemon.MergeWorktreeRequest{
		ProjectDir: repoDir,
		Branch:     "feat/conflict",
	})
	if err == nil {
		t.Fatal("expected error for merge conflict")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("error = %q, expected to mention conflict", err.Error())
	}

	// Main worktree should be clean (merge was aborted).
	out := gitRun(t, repoDir, "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		t.Errorf("main worktree not clean after aborted merge: %s", out)
	}
}

func TestMergeWorktree_NothingToMerge(t *testing.T) {
	t.Parallel()

	repoDir := initGitRepo(t)

	// Create a feature branch worktree with 0 commits ahead and no changes.
	wtDir := filepath.Join(t.TempDir(), "wt-nothing")
	gitRun(t, repoDir, "worktree", "add", "-b", "feat/nothing", wtDir, "main")

	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	_, err := client.MergeWorktree(ctx, daemon.MergeWorktreeRequest{
		ProjectDir: repoDir,
		Branch:     "feat/nothing",
	})
	if err == nil {
		t.Fatal("expected error for nothing to merge")
	}
	if !strings.Contains(err.Error(), "nothing to merge") {
		t.Errorf("error = %q, expected to mention 'nothing to merge'", err.Error())
	}
}

func TestMergeWorktree_AutoCommitsThenMerges(t *testing.T) {
	t.Parallel()

	repoDir := initGitRepo(t)

	// Create a feature branch worktree with 0 pre-existing commits ahead,
	// but with uncommitted files (simulating agent work that was never committed).
	wtDir := filepath.Join(t.TempDir(), "wt-autocommit")
	gitRun(t, repoDir, "worktree", "add", "-b", "feat/autocommit", wtDir, "main")
	wtDir, _ = filepath.EvalSymlinks(wtDir)

	// Write files in the worktree but do NOT commit them.
	gitWriteFile(t, filepath.Join(wtDir, "agent-output.txt"), "agent generated code\n")
	gitWriteFile(t, filepath.Join(wtDir, "new-module.go"), "package newmodule\n")

	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	resp, err := client.MergeWorktree(ctx, daemon.MergeWorktreeRequest{
		ProjectDir:    repoDir,
		Branch:        "feat/autocommit",
		CommitMessage: "auto-commit agent work",
	})
	if err != nil {
		t.Fatalf("MergeWorktree: %v", err)
	}

	if resp.Status != "merged" {
		t.Errorf("status = %q, want merged", resp.Status)
	}

	// Verify agent files ended up on main.
	if _, err := os.Stat(filepath.Join(repoDir, "agent-output.txt")); os.IsNotExist(err) {
		t.Error("agent-output.txt not present on main after merge")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "new-module.go")); os.IsNotExist(err) {
		t.Error("new-module.go not present on main after merge")
	}

	if !resp.WorktreeRemoved {
		t.Error("expected worktree_removed = true")
	}
	if !resp.BranchDeleted {
		t.Error("expected branch_deleted = true")
	}
}
