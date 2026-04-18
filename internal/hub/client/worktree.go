package hubclient

import (
	"context"
	"net/url"
	"strconv"

	"github.com/acksell/clank/internal/host"
)

// WorktreeClient is bound to (hostname, gitRef, branch).
type WorktreeClient struct {
	r      *RepoClient
	branch string
}

// Resolve creates (or reuses) the worktree for this branch and returns its info.
func (w *WorktreeClient) Resolve(ctx context.Context) (*host.WorktreeInfo, error) {
	body := struct {
		Branch string `json:"branch"`
	}{w.branch}
	var out host.WorktreeInfo
	if err := w.r.h.c.post(ctx, w.r.base()+"/worktrees", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Remove deletes the worktree for this branch. force forwards to git.
func (w *WorktreeClient) Remove(ctx context.Context, force bool) error {
	q := url.Values{
		"branch": {w.branch},
		"force":  {strconv.FormatBool(force)},
	}
	return w.r.h.c.do(ctx, "DELETE", w.r.base()+"/worktrees?"+q.Encode(), nil, nil)
}

// Merge merges this branch into the repo's default branch using commitMessage.
func (w *WorktreeClient) Merge(ctx context.Context, commitMessage string) (*host.MergeResult, error) {
	body := struct {
		Branch        string `json:"branch"`
		CommitMessage string `json:"commit_message,omitempty"`
	}{w.branch, commitMessage}
	var out host.MergeResult
	if err := w.r.h.c.post(ctx, w.r.base()+"/worktrees/merge", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
