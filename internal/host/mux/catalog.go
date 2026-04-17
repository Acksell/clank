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

// HOST
func (m *Mux) handleListAgents(w http.ResponseWriter, r *http.Request) {
	bt := agent.BackendType(r.URL.Query().Get("backend"))
	projectDir := r.URL.Query().Get("project_dir")
	if bt == "" || projectDir == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "backend and project_dir are required"})
		return
	}
	out, err := m.svc.ListAgents(r.Context(), bt, projectDir)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HOST
func (m *Mux) handleListModels(w http.ResponseWriter, r *http.Request) {
	bt := agent.BackendType(r.URL.Query().Get("backend"))
	projectDir := r.URL.Query().Get("project_dir")
	if bt == "" || projectDir == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "backend and project_dir are required"})
		return
	}
	out, err := m.svc.ListModels(r.Context(), bt, projectDir)
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
