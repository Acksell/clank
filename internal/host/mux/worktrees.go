package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// Worktree/branch endpoints. The /repos surface was removed in §7.8 of
// hub_host_refactor_code_review.md alongside the host repo registry —
// callers now identify the repo by its GitRef in the request body.

type worktreeBranchRequest struct {
	GitRef agent.GitRef        `json:"git_ref"`
	Auth   agent.GitCredential `json:"auth"`
	Branch string              `json:"branch"`
	Force  bool                `json:"force,omitempty"`
}

type mergeBranchRequest struct {
	GitRef        agent.GitRef        `json:"git_ref"`
	Auth          agent.GitCredential `json:"auth"`
	Branch        string              `json:"branch"`
	CommitMessage string              `json:"commit_message,omitempty"`
}

// validateAuth defers to GitCredential.Validate, but skips zero-value
// credentials so legacy callers (pre-Phase-6 hub paths and tests that
// build StartRequest by hand) keep working. Phase 6 promotes this to
// "credential is required for clone paths" once the host actually uses
// it; until then a zero-kind cred is silently ignored.
func validateAuth(c agent.GitCredential) error {
	if c.Kind == "" {
		return nil
	}
	return c.Validate()
}

// HOST
func (m *Mux) handleListBranches(w http.ResponseWriter, r *http.Request) {
	var req worktreeBranchRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.GitRef.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := validateAuth(req.Auth); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	out, err := m.svc.ListBranches(r.Context(), req.GitRef)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HOST
func (m *Mux) handleResolveWorktree(w http.ResponseWriter, r *http.Request) {
	var req worktreeBranchRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.GitRef.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "branch is required"})
		return
	}
	if err := validateAuth(req.Auth); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	out, err := m.svc.ResolveWorktree(r.Context(), req.GitRef, req.Branch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HOST
func (m *Mux) handleRemoveWorktree(w http.ResponseWriter, r *http.Request) {
	var req worktreeBranchRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.GitRef.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "branch is required"})
		return
	}
	if err := validateAuth(req.Auth); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := m.svc.RemoveWorktree(r.Context(), req.GitRef, req.Branch, req.Force); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HOST
func (m *Mux) handleMergeBranch(w http.ResponseWriter, r *http.Request) {
	var req mergeBranchRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.GitRef.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "branch is required"})
		return
	}
	if err := validateAuth(req.Auth); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	out, err := m.svc.MergeBranch(r.Context(), req.GitRef, req.Branch, req.CommitMessage)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
