package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/host"
)

// Repo-scoped routes. The wire shape carries the repo identity in the URL
// (`/repos/{ref}/...`) using the canonical GitRef form (URL-encoded) so
// callers no longer need to send filesystem paths over the wire.

func (m *Mux) handleListRepos(w http.ResponseWriter, r *http.Request) {
	out, err := m.svc.ListRepos(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// RegisterRepoRequest is the body for POST /repos. Used by the Hub to seed
// the host's (canonical → rootDir) map so subsequent CreateSession calls
// can resolve a workDir from the path-free StartRequest.
type RegisterRepoRequest struct {
	Ref     host.GitRef `json:"ref"`
	RootDir string      `json:"root_dir"`
}

func (m *Mux) handleRegisterRepo(w http.ResponseWriter, r *http.Request) {
	var req RegisterRepoRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	repo, err := m.svc.RegisterRepo(req.Ref, req.RootDir)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

// pathRef extracts the canonical git-ref from the {ref} path parameter.
// The handler chain rejects empty values uniformly so individual handlers
// don't have to repeat the check.
func pathRef(r *http.Request) string {
	return r.PathValue("ref")
}

func (m *Mux) handleListBranchesByRepo(w http.ResponseWriter, r *http.Request) {
	ref := pathRef(r)
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "git ref is required"})
		return
	}
	out, err := m.svc.ListBranchesByRepo(r.Context(), ref)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateWorktreeByRepoRequest is the body for POST /repos/{ref}/worktrees.
type CreateWorktreeByRepoRequest struct {
	Branch string `json:"branch"`
}

func (m *Mux) handleCreateWorktreeByRepo(w http.ResponseWriter, r *http.Request) {
	ref := pathRef(r)
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "git ref is required"})
		return
	}
	var req CreateWorktreeByRepoRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "branch is required"})
		return
	}
	wt, err := m.svc.ResolveWorktreeByRepo(r.Context(), ref, req.Branch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

// Branch arrives via query string because branch names contain "/" which
// would conflict with path-segment routing.
func (m *Mux) handleRemoveWorktreeByRepo(w http.ResponseWriter, r *http.Request) {
	ref := pathRef(r)
	branch := r.URL.Query().Get("branch")
	force := r.URL.Query().Get("force") == "true"
	if ref == "" || branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "git ref and branch are required"})
		return
	}
	if err := m.svc.RemoveWorktreeByRepo(r.Context(), ref, branch, force); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MergeBranchByRepoRequest is the body for POST /repos/{ref}/worktrees/merge.
type MergeBranchByRepoRequest struct {
	Branch        string `json:"branch"`
	CommitMessage string `json:"commit_message,omitempty"`
}

func (m *Mux) handleMergeBranchByRepo(w http.ResponseWriter, r *http.Request) {
	ref := pathRef(r)
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "git ref is required"})
		return
	}
	var req MergeBranchByRepoRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "branch is required"})
		return
	}
	res, err := m.svc.MergeBranchByRepo(r.Context(), ref, req.Branch, req.CommitMessage)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
