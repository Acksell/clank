package hubmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

func (m *Mux) handleListAgents(w http.ResponseWriter, r *http.Request) {
	backendStr := r.URL.Query().Get("backend")
	projectDir := r.URL.Query().Get("project_dir")
	if backendStr == "" || projectDir == "" {
		writeBadRequest(w, "backend and project_dir query params are required")
		return
	}
	agents, err := m.svc.ListAgents(r.Context(), agent.BackendType(backendStr), projectDir)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (m *Mux) handleListModels(w http.ResponseWriter, r *http.Request) {
	backendStr := r.URL.Query().Get("backend")
	projectDir := r.URL.Query().Get("project_dir")
	if backendStr == "" || projectDir == "" {
		writeBadRequest(w, "backend and project_dir query params are required")
		return
	}
	models, err := m.svc.ListModels(r.Context(), agent.BackendType(backendStr), projectDir)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}
