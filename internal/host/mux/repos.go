package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/host"
)

// Phase 3B: RepoID-scoped routes parallel to the legacy path-style
// `/branches` and `/worktrees` endpoints. The wire shape moves the repo
// identity into the URL (`/repos/{id}/...`) so callers no longer need to
// send filesystem paths over the wire.

func (m *Mux) handleListRepos(w http.ResponseWriter, r *http.Request) {
	out, err := m.svc.ListRepos(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// RegisterRepoRequest is the body for POST /repos. Used by the Hub to
// seed the host's (RepoID → rootDir) map so subsequent CreateSession
// calls can resolve a workDir from the path-free StartRequest.
type RegisterRepoRequest struct {
	Ref     host.RepoRef `json:"ref"`
	RootDir string       `json:"root_dir"`
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

func (m *Mux) handleListBranchesByRepo(w http.ResponseWriter, r *http.Request) {
	id := host.RepoID(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "repo id is required"})
		return
	}
	out, err := m.svc.ListBranchesByRepo(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateWorktreeByRepoRequest is the body for POST /repos/{id}/worktrees.
type CreateWorktreeByRepoRequest struct {
	Branch string `json:"branch"`
}

func (m *Mux) handleCreateWorktreeByRepo(w http.ResponseWriter, r *http.Request) {
	id := host.RepoID(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "repo id is required"})
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
	wt, err := m.svc.ResolveWorktreeByRepo(r.Context(), id, req.Branch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

// Branch arrives via query string because branch names contain "/" which
// would conflict with path-segment routing.
func (m *Mux) handleRemoveWorktreeByRepo(w http.ResponseWriter, r *http.Request) {
	id := host.RepoID(r.PathValue("id"))
	branch := r.URL.Query().Get("branch")
	force := r.URL.Query().Get("force") == "true"
	if id == "" || branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "repo id and branch are required"})
		return
	}
	if err := m.svc.RemoveWorktreeByRepo(r.Context(), id, branch, force); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MergeBranchByRepoRequest is the body for POST /repos/{id}/worktrees/merge.
type MergeBranchByRepoRequest struct {
	Branch        string `json:"branch"`
	CommitMessage string `json:"commit_message,omitempty"`
}

func (m *Mux) handleMergeBranchByRepo(w http.ResponseWriter, r *http.Request) {
	id := host.RepoID(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "repo id is required"})
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
	res, err := m.svc.MergeBranchByRepo(r.Context(), id, req.Branch, req.CommitMessage)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
