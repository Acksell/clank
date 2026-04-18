package hubclient

import (
	"context"
	"net/url"

	"github.com/acksell/clank/internal/host"
)

// RepoClient is bound to (hostname, gitRef).
type RepoClient struct {
	h      *HostClient
	gitRef string
}

func (r *RepoClient) base() string {
	return "/hosts/" + url.PathEscape(string(r.h.hostname)) + "/repos/" + url.PathEscape(r.gitRef)
}

// Branches lists branches/worktrees for this repo.
func (r *RepoClient) Branches(ctx context.Context) ([]host.BranchInfo, error) {
	var out []host.BranchInfo
	if err := r.h.c.get(ctx, r.base()+"/branches", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Worktree returns a handle for one branch/worktree on this repo.
func (r *RepoClient) Worktree(branch string) *WorktreeClient {
	return &WorktreeClient{r: r, branch: branch}
}
