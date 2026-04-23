package hostclient

import (
	"context"
	"net/http"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// Worktree/branch operations are GitRef-scoped (the host repo registry
// was removed in §7.8). All requests carry the GitRef + the hub-resolved
// GitCredential in the JSON body. The credential is opaque to the
// client: the hub decides what kind, the host decides how to use it.

// ListBranches returns branches in the repo identified by ref.
func (c *HTTP) ListBranches(ctx context.Context, ref agent.GitRef, auth agent.GitCredential) ([]host.BranchInfo, error) {
	body := struct {
		GitRef agent.GitRef        `json:"git_ref"`
		Auth   agent.GitCredential `json:"auth"`
	}{ref, auth}
	var out []host.BranchInfo
	err := c.do(ctx, http.MethodPost, "/worktrees/list-branches", body, &out)
	return out, err
}

// ResolveWorktree creates (or reuses) the worktree for branch and
// returns its info.
func (c *HTTP) ResolveWorktree(ctx context.Context, ref agent.GitRef, auth agent.GitCredential, branch string) (host.WorktreeInfo, error) {
	body := struct {
		GitRef agent.GitRef        `json:"git_ref"`
		Auth   agent.GitCredential `json:"auth"`
		Branch string              `json:"branch"`
	}{ref, auth, branch}
	var out host.WorktreeInfo
	err := c.do(ctx, http.MethodPost, "/worktrees/resolve", body, &out)
	return out, err
}

// RemoveWorktree removes the worktree for branch. force forwards to git.
func (c *HTTP) RemoveWorktree(ctx context.Context, ref agent.GitRef, auth agent.GitCredential, branch string, force bool) error {
	body := struct {
		GitRef agent.GitRef        `json:"git_ref"`
		Auth   agent.GitCredential `json:"auth"`
		Branch string              `json:"branch"`
		Force  bool                `json:"force,omitempty"`
	}{ref, auth, branch, force}
	return c.do(ctx, http.MethodPost, "/worktrees/remove", body, nil)
}

// MergeBranch merges branch into the repo's default branch.
func (c *HTTP) MergeBranch(ctx context.Context, ref agent.GitRef, auth agent.GitCredential, branch, commitMessage string) (host.MergeResult, error) {
	body := struct {
		GitRef        agent.GitRef        `json:"git_ref"`
		Auth          agent.GitCredential `json:"auth"`
		Branch        string              `json:"branch"`
		CommitMessage string              `json:"commit_message,omitempty"`
	}{ref, auth, branch, commitMessage}
	var out host.MergeResult
	err := c.do(ctx, http.MethodPost, "/worktrees/merge", body, &out)
	return out, err
}

// PushBranch pushes branch to origin on the host. The auth credential
// is used for the push; anonymous is permitted and will succeed if the
// remote allows it. Returns host.ErrPushRejected / ErrPushAuthRequired
// / ErrNothingToPush (via errors.Is) for the three outcomes callers
// care to distinguish.
func (c *HTTP) PushBranch(ctx context.Context, ref agent.GitRef, auth agent.GitCredential, branch string) (host.PushResult, error) {
	body := struct {
		GitRef agent.GitRef        `json:"git_ref"`
		Auth   agent.GitCredential `json:"auth"`
		Branch string              `json:"branch"`
	}{ref, auth, branch}
	var out host.PushResult
	err := c.do(ctx, http.MethodPost, "/worktrees/push", body, &out)
	return out, err
}
