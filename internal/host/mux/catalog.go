package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// HOST
func (m *Mux) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := m.svc.Status(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// HOST
func (m *Mux) handleListBackends(w http.ResponseWriter, r *http.Request) {
	bs, err := m.svc.ListBackends(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bs)
}

// catalogRequest is the wire shape for /agents and /models. Switched
// from query-string GET to JSON POST in Phase 5 because we now carry
// an opaque GitCredential alongside the GitRef. Credential material
// belongs in a request body, not a URL.
type catalogRequest struct {
	Backend agent.BackendType   `json:"backend"`
	GitRef  agent.GitRef        `json:"git_ref"`
	Auth    agent.GitCredential `json:"auth"`
}

func (req catalogRequest) validate() error {
	if req.Backend == "" {
		return errMsg("backend is required")
	}
	if err := req.GitRef.Validate(); err != nil {
		return err
	}
	return validateAuth(req.Auth)
}

type errMsg string

func (e errMsg) Error() string { return string(e) }

// HOST
// handleListAgents serves POST /agents with a catalogRequest body.
// The host resolves the GitRef to a workdir internally — no paths cross
// the wire beyond the caller-supplied LocalPath (§7.3).
func (m *Mux) handleListAgents(w http.ResponseWriter, r *http.Request) {
	var req catalogRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	out, err := m.svc.ListAgents(r.Context(), req.Backend, req.GitRef, req.Auth)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HOST
func (m *Mux) handleListModels(w http.ResponseWriter, r *http.Request) {
	var req catalogRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	out, err := m.svc.ListModels(r.Context(), req.Backend, req.GitRef, req.Auth)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HOST
type DiscoverRequest struct {
	Backend agent.BackendType `json:"backend"`
	SeedDir string            `json:"seed_dir"`
}

// HOST
func (m *Mux) handleDiscoverSessions(w http.ResponseWriter, r *http.Request) {
	var req DiscoverRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Backend == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "backend is required"})
		return
	}
	out, err := m.svc.DiscoverSessions(r.Context(), req.Backend, req.SeedDir)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
