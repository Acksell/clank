package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// pushBranchRequest is the wire shape for POST /worktrees/push. Kept
// separate from worktreeBranchRequest so adding push-specific fields
// later (e.g. explicit remote, message) does not disturb unrelated
// endpoints.
type pushBranchRequest struct {
	GitRef agent.GitRef        `json:"git_ref"`
	Auth   agent.GitCredential `json:"auth"`
	Branch string              `json:"branch"`
}

// HOST POST /worktrees/push
func (m *Mux) handlePushBranch(w http.ResponseWriter, r *http.Request) {
	var req pushBranchRequest
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
	out, err := m.svc.PushBranch(r.Context(), req.GitRef, req.Auth, req.Branch)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
