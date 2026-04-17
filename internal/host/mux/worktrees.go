package hostmux

import (
	"net/http"
)

// HOST
func (m *Mux) handleListBranches(w http.ResponseWriter, r *http.Request) {
	projectDir := r.URL.Query().Get("project_dir")
	if projectDir == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "project_dir is required"})
		return
	}
	out, err := m.svc.ListBranches(r.Context(), projectDir)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateWorktreeRequest is the body for POST /worktrees.
type CreateWorktreeRequest struct {
	ProjectDir string `json:"project_dir"`
	Branch     string `json:"branch"`
}

// HOST
func (m *Mux) handleCreateWorktree(w http.ResponseWriter, r *http.Request) {
	var req CreateWorktreeRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.ProjectDir == "" || req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "project_dir and branch are required"})
		return
	}
	wt, err := m.svc.ResolveWorktree(r.Context(), req.ProjectDir, req.Branch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

// HOST
func (m *Mux) handleRemoveWorktree(w http.ResponseWriter, r *http.Request) {
	projectDir := r.URL.Query().Get("project_dir")
	branch := r.URL.Query().Get("branch")
	force := r.URL.Query().Get("force") == "true"
	if projectDir == "" || branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "project_dir and branch are required"})
		return
	}
	if err := m.svc.RemoveWorktree(r.Context(), projectDir, branch, force); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// MergeWorktreeRequest is the body for POST /worktrees/merge.
type MergeWorktreeRequest struct {
	ProjectDir    string `json:"project_dir"`
	Branch        string `json:"branch"`
	CommitMessage string `json:"commit_message,omitempty"`
}

// HOST
func (m *Mux) handleMergeWorktree(w http.ResponseWriter, r *http.Request) {
	var req MergeWorktreeRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.ProjectDir == "" || req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "project_dir and branch are required"})
		return
	}
	res, err := m.svc.MergeBranch(r.Context(), req.ProjectDir, req.Branch, req.CommitMessage)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
