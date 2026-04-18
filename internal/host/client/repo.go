package hostclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/acksell/clank/internal/host"
)

// RepoClient is a per-repo handle bound to a canonical GitRef string
// (URL key form). Obtained via HTTP.Repo(ref).
type RepoClient struct {
	c   *HTTP
	ref string
}

// Repo returns a handle for the repo identified by ref. The ref is the
// canonical GitRef string typically produced by GitRef.Canonical().
func (c *HTTP) Repo(ref string) *RepoClient {
	return &RepoClient{c: c, ref: ref}
}

// Branches lists branches for this repo.
func (r *RepoClient) Branches(ctx context.Context) ([]host.BranchInfo, error) {
	var out []host.BranchInfo
	err := r.c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(r.ref)+"/branches", nil, &out)
	return out, err
}

// Worktree returns a handle for the (repo, branch) worktree pair.
func (r *RepoClient) Worktree(branch string) *WorktreeClient {
	return &WorktreeClient{c: r.c, ref: r.ref, branch: branch}
}
