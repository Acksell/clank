package daemon

// HOST: worktree and branch management is part of the Host plane in the
// target architecture (see hub_host_refactor.md). These handlers and helpers
// will move into host.Service in Phase 1.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/git"
)

// BranchInfo describes a worktree entry (including the main working tree).
type BranchInfo struct {
	Name         string `json:"name"`
	WorktreeDir  string `json:"worktree_dir,omitempty"`  // Non-empty if a worktree is checked out for this branch
	IsDefault    bool   `json:"is_default,omitempty"`    // True if this is the repo's default branch (main/master)
	IsCurrent    bool   `json:"is_current,omitempty"`    // True if this branch is checked out in the main working tree
	LinesAdded   int    `json:"lines_added,omitempty"`   // Lines added vs default branch
	LinesRemoved int    `json:"lines_removed,omitempty"` // Lines removed vs default branch
	CommitsAhead int    `json:"commits_ahead,omitempty"` // Commits ahead of default branch
}

// CreateWorktreeRequest is the request body for POST /worktrees.
type CreateWorktreeRequest struct {
	ProjectDir string `json:"project_dir"`
	Branch     string `json:"branch"`
	NewBranch  bool   `json:"new_branch,omitempty"` // If true, create a new branch
	Base       string `json:"base,omitempty"`       // Base ref for new branches (default: repo default branch)
}

// WorktreeInfo is the response for POST /worktrees.
type WorktreeInfo struct {
	Branch      string `json:"branch"`
	WorktreeDir string `json:"worktree_dir"`
}

// RemoveWorktreeRequest is the request body for DELETE /worktrees.
type RemoveWorktreeRequest struct {
	ProjectDir string `json:"project_dir"`
	Branch     string `json:"branch"`
	Force      bool   `json:"force,omitempty"`
}

// MergeWorktreeRequest is the request body for POST /worktrees/merge.
type MergeWorktreeRequest struct {
	ProjectDir    string `json:"project_dir"`
	Branch        string `json:"branch"`                   // Branch to merge into the default branch
	CommitMessage string `json:"commit_message,omitempty"` // Worktree commit message (used to commit uncommitted work before merging)
}

// MergeWorktreeResponse is the response from POST /worktrees/merge.
type MergeWorktreeResponse struct {
	Status          string `json:"status"`           // "merged"
	MergedBranch    string `json:"merged_branch"`    // Branch that was merged
	SessionsDone    int    `json:"sessions_done"`    // Number of sessions marked done
	WorktreeRemoved bool   `json:"worktree_removed"` // Whether the worktree was cleaned up
	BranchDeleted   bool   `json:"branch_deleted"`   // Whether the branch was deleted
}

// resolveWorktree ensures a git worktree exists for the given branch in the
// repository at projectDir. Returns the worktree's filesystem path.
//
// If the branch already has a worktree checked out, returns the existing path.
// If the branch exists locally but has no worktree, creates one.
// If the branch doesn't exist, creates a new branch based on the default branch.
func (d *Daemon) resolveWorktree(projectDir, branch string) (string, error) {
	// Check if a worktree already exists for this branch.
	wt, err := git.FindWorktreeForBranch(projectDir, branch)
	if err != nil {
		return "", err
	}
	if wt != nil {
		return wt.Path, nil
	}

	// Determine the worktree directory path.
	projectName := filepath.Base(projectDir)
	wtDir, err := git.WorktreeDir(projectName, branch)
	if err != nil {
		return "", err
	}

	// Check if the branch already exists locally.
	exists, err := git.BranchExists(projectDir, branch)
	if err != nil {
		return "", err
	}

	if exists {
		if err := git.AddWorktree(projectDir, wtDir, branch); err != nil {
			return "", err
		}
	} else {
		// New branch: base off the default branch.
		base, err := git.DefaultBranch(projectDir)
		if err != nil {
			return "", fmt.Errorf("determine default branch: %w", err)
		}
		if err := git.AddWorktreeNewBranch(projectDir, wtDir, branch, base); err != nil {
			return "", err
		}
	}

	d.log.Printf("created worktree for branch %q at %s", branch, wtDir)
	return wtDir, nil
}

func (d *Daemon) handleListBranches(w http.ResponseWriter, r *http.Request) {
	projectDir := r.URL.Query().Get("project_dir")
	if projectDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_dir is required"})
		return
	}

	worktrees, err := git.ListWorktrees(projectDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	defaultBranch, _ := git.DefaultBranch(projectDir)
	currentBranch, _ := git.CurrentBranch(projectDir)

	result := make([]BranchInfo, 0, len(worktrees))
	for _, wt := range worktrees {
		if wt.Bare || wt.Branch == "" {
			continue
		}

		info := BranchInfo{
			Name:        wt.Branch,
			WorktreeDir: wt.Path,
			IsDefault:   wt.Branch == defaultBranch,
			IsCurrent:   wt.Branch == currentBranch,
		}

		// Compute diff stats and commit count against the default branch.
		// Skip for the default branch itself (diff would be empty).
		if wt.Branch != defaultBranch {
			added, removed, err := git.DiffStat(wt.Path, defaultBranch)
			if err == nil {
				info.LinesAdded = added
				info.LinesRemoved = removed
			}
			ahead, err := git.CommitsAhead(projectDir, defaultBranch, wt.Branch)
			if err == nil {
				info.CommitsAhead = ahead
			}
		}

		result = append(result, info)
	}

	writeJSON(w, http.StatusOK, result)
}

func (d *Daemon) handleCreateWorktree(w http.ResponseWriter, r *http.Request) {
	var req CreateWorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.ProjectDir == "" || req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_dir and branch are required"})
		return
	}

	wtDir, err := d.resolveWorktree(req.ProjectDir, req.Branch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, WorktreeInfo{
		Branch:      req.Branch,
		WorktreeDir: wtDir,
	})
}

func (d *Daemon) handleRemoveWorktree(w http.ResponseWriter, r *http.Request) {
	var req RemoveWorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.ProjectDir == "" || req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_dir and branch are required"})
		return
	}

	wt, err := git.FindWorktreeForBranch(req.ProjectDir, req.Branch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if wt == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no worktree found for branch %q", req.Branch)})
		return
	}

	if err := git.RemoveWorktree(req.ProjectDir, wt.Path, req.Force); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	d.log.Printf("removed worktree for branch %q at %s", req.Branch, wt.Path)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (d *Daemon) handleMergeWorktree(w http.ResponseWriter, r *http.Request) {
	var req MergeWorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.ProjectDir == "" || req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_dir and branch are required"})
		return
	}

	// Determine the default (target) branch.
	defaultBranch, err := git.DefaultBranch(req.ProjectDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "determine default branch: " + err.Error()})
		return
	}
	if req.Branch == defaultBranch {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot merge the default branch into itself"})
		return
	}

	// Find the feature branch worktree.
	branchWt, err := git.FindWorktreeForBranch(req.ProjectDir, req.Branch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "find branch worktree: " + err.Error()})
		return
	}
	if branchWt == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("no worktree found for branch %q", req.Branch)})
		return
	}

	// Stage all work in the worktree (including untracked agent-created files).
	if err := git.AddAll(branchWt.Path); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "git add -A in worktree: " + err.Error()})
		return
	}

	// Check if there's anything to merge: staged changes after add-all,
	// or commits already ahead of the default branch.
	hasStagedWork, err := git.HasStagedChanges(branchWt.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "check staged changes: " + err.Error()})
		return
	}
	commitsAhead, err := git.CommitsAhead(req.ProjectDir, defaultBranch, req.Branch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "count commits ahead: " + err.Error()})
		return
	}
	if !hasStagedWork && commitsAhead == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing to merge: branch has no commits ahead and worktree is clean"})
		return
	}

	// Commit staged work in the worktree (skip if nothing was staged).
	if hasStagedWork {
		commitMsg := req.CommitMessage
		if commitMsg == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "commit_message is required when worktree has uncommitted changes"})
			return
		}
		if err := git.Commit(branchWt.Path, commitMsg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "commit worktree changes: " + err.Error()})
			return
		}
		d.log.Printf("committed worktree changes in %s on branch %q", branchWt.Path, req.Branch)
	}

	// Find the main worktree (the one with the default branch checked out).
	mainWt, err := git.FindWorktreeForBranch(req.ProjectDir, defaultBranch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "find main worktree: " + err.Error()})
		return
	}
	if mainWt == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("no worktree found for default branch %q", defaultBranch)})
		return
	}

	// Verify the main worktree is clean (tracked files only).
	clean, err := git.IsClean(mainWt.Path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "check worktree clean: " + err.Error()})
		return
	}
	if !clean {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "main worktree has uncommitted changes; commit or stash them first"})
		return
	}

	// Perform the merge (--no-ff) with auto-generated merge commit message.
	mergeMsg := fmt.Sprintf("Merge branch '%s'", req.Branch)
	if err := git.MergeNoFF(mainWt.Path, req.Branch, mergeMsg); err != nil {
		// If merge failed, check for conflicts and abort.
		if git.IsMerging(mainWt.Path) {
			_ = git.AbortMerge(mainWt.Path)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "merge conflict: resolve manually or choose a different approach"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "merge failed: " + err.Error()})
		return
	}

	d.log.Printf("merged branch %q into %q in %s", req.Branch, defaultBranch, mainWt.Path)

	resp := MergeWorktreeResponse{
		Status:       "merged",
		MergedBranch: req.Branch,
	}

	// Post-merge: mark sessions on this worktree as done.
	resp.SessionsDone = d.markWorktreeSessionsDone(branchWt.Path)

	// Post-merge: remove worktree (force — worktrees often have untracked files).
	if err := git.RemoveWorktree(req.ProjectDir, branchWt.Path, true); err != nil {
		d.log.Printf("warning: could not remove worktree after merge: %v", err)
	} else {
		resp.WorktreeRemoved = true
	}

	// Post-merge: delete the branch (safe delete, only if fully merged).
	if err := git.DeleteBranch(req.ProjectDir, req.Branch, false); err != nil {
		d.log.Printf("warning: could not delete branch after merge: %v", err)
	} else {
		resp.BranchDeleted = true
	}

	writeJSON(w, http.StatusOK, resp)
}

// markWorktreeSessionsDone marks all non-archived sessions whose ProjectDir
// matches the given worktree path as "done". Returns the count of sessions updated.
func (d *Daemon) markWorktreeSessionsDone(worktreePath string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	count := 0
	for _, ms := range d.sessions {
		if ms.info.ProjectDir != worktreePath {
			continue
		}
		if ms.info.Visibility == agent.VisibilityArchived || ms.info.Visibility == agent.VisibilityDone {
			continue
		}
		ms.info.Visibility = agent.VisibilityDone
		d.persistSession(ms)
		count++
	}
	return count
}
