package hostclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/acksell/clank/internal/host"
)

// WorktreeClient is a per-(repo,branch) handle. Obtained via
// RepoClient.Worktree(branch).
type WorktreeClient struct {
	c      *HTTP
	ref    string
	branch string
}

// Resolve creates the worktree if absent and returns its info.
func (w *WorktreeClient) Resolve(ctx context.Context) (host.WorktreeInfo, error) {
	body := struct {
		Branch string `json:"branch"`
	}{w.branch}
	var out host.WorktreeInfo
	err := w.c.do(ctx, http.MethodPost, "/repos/"+url.PathEscape(w.ref)+"/worktrees", body, &out)
	return out, err
}

// Remove deletes the worktree. force=true removes even with local changes.
func (w *WorktreeClient) Remove(ctx context.Context, force bool) error {
	q := url.Values{
		"branch": {w.branch},
		"force":  {strconv.FormatBool(force)},
	}
	return w.c.do(ctx, http.MethodDelete, "/repos/"+url.PathEscape(w.ref)+"/worktrees?"+q.Encode(), nil, nil)
}

// Merge merges this branch into the repo's default branch.
func (w *WorktreeClient) Merge(ctx context.Context, commitMessage string) (host.MergeResult, error) {
	body := struct {
		Branch        string `json:"branch"`
		CommitMessage string `json:"commit_message,omitempty"`
	}{w.branch, commitMessage}
	var out host.MergeResult
	err := w.c.do(ctx, http.MethodPost, "/repos/"+url.PathEscape(w.ref)+"/worktrees/merge", body, &out)
	return out, err
}
