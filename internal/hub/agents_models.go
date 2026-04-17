package hub

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// HUB
func (s *Service) handleListAgents(w http.ResponseWriter, r *http.Request) {
	backendStr := r.URL.Query().Get("backend")
	projectDir := r.URL.Query().Get("project_dir")

	if backendStr == "" || projectDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backend and project_dir query params are required"})
		return
	}

	bt := agent.BackendType(backendStr)

	// Try to serve from the persistent cache (SQLite) first. This avoids
	// blocking on the OpenCode server starting up — the response is instant.
	if s.Store != nil {
		cached, err := s.Store.LoadPrimaryAgents(bt, projectDir)
		if err != nil {
			s.log.Printf("warning: load cached primary agents: %v", err)
		}
		if cached != nil {
			// Return cached data immediately and refresh in the background.
			s.refreshPrimaryAgentsInBackground(bt, projectDir)
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	// No cache — fetch synchronously through the host plane.
	agents, err := s.hostClient.ListAgents(r.Context(), bt, projectDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if agents == nil {
		agents = []agent.AgentInfo{}
	}
	s.persistPrimaryAgents(bt, projectDir, agents)
	writeJSON(w, http.StatusOK, agents)
}

// HUB
// handleListModels returns available models for a given backend and project.
func (s *Service) handleListModels(w http.ResponseWriter, r *http.Request) {
	backendStr := r.URL.Query().Get("backend")
	projectDir := r.URL.Query().Get("project_dir")

	if backendStr == "" || projectDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backend and project_dir query params are required"})
		return
	}

	bt := agent.BackendType(backendStr)
	models, err := s.hostClient.ListModels(r.Context(), bt, projectDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if models == nil {
		models = []agent.ModelInfo{}
	}
	writeJSON(w, http.StatusOK, models)
}

// HOST
// openCodeServerURLs returns a map from project directory to server URL
// for all running OpenCode servers. Returns nil if no OpenCode backend
// manager is registered.
//
// TODO(hub-host-refactor): this still type-asserts to the concrete
// OpenCodeBackendManager. Once the daemon's session info no longer needs
// the URL inline (or once it's exposed via SessionBackend / Client),
// this helper goes away.
func (s *Service) openCodeServerURLs() map[string]string {
	ocMgr, ok := s.BackendManagers[agent.BackendOpenCode].(*host.OpenCodeBackendManager)
	if !ok {
		return nil
	}
	urls := make(map[string]string)
	for _, srv := range ocMgr.ListServers() {
		urls[srv.ProjectDir] = srv.URL
	}
	return urls
}

// HUB
// persistPrimaryAgents writes the primary agent list to the store for future cache hits.
func (s *Service) persistPrimaryAgents(bt agent.BackendType, projectDir string, agents []agent.AgentInfo) {
	if s.Store == nil {
		return
	}
	if err := s.Store.UpsertPrimaryAgents(bt, projectDir, agents); err != nil {
		s.log.Printf("warning: persist primary agents for %s/%s: %v", bt, projectDir, err)
	}
}

// HUB
// refreshPrimaryAgentsInBackground kicks off an async refresh of the primary
// agent list for the given backend/project. The result is persisted to SQLite
// so that subsequent requests get the updated list. Safe to call multiple
// times — concurrent refreshes for the same key are deduplicated.
func (s *Service) refreshPrimaryAgentsInBackground(bt agent.BackendType, projectDir string) {
	s.primaryAgentsRefreshMu.Lock()
	key := string(bt) + "\x00" + projectDir
	if s.primaryAgentsRefreshInFlight[key] {
		s.primaryAgentsRefreshMu.Unlock()
		return
	}
	s.primaryAgentsRefreshInFlight[key] = true
	s.primaryAgentsRefreshMu.Unlock()

	go func() {
		defer func() {
			s.primaryAgentsRefreshMu.Lock()
			delete(s.primaryAgentsRefreshInFlight, key)
			s.primaryAgentsRefreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
		defer cancel()

		agents, err := s.hostClient.ListAgents(ctx, bt, projectDir)
		if err != nil {
			s.log.Printf("background primary agent refresh for %s/%s: %v", bt, projectDir, err)
			return
		}
		if agents == nil {
			agents = []agent.AgentInfo{}
		}
		s.persistPrimaryAgents(bt, projectDir, agents)
	}()
}

// HUB
// warmPrimaryAgentCaches fetches and persists primary agent lists for all
// known project directories. Called once on daemon startup after the
// reconciler has been started. The refreshPrimaryAgentsInBackground calls
// use GetOrStartServer which will wait for the reconciler to provide a
// running server — this method does NOT start servers itself.
func (s *Service) warmPrimaryAgentCaches() {
	if s.Store == nil {
		return
	}
	backends, err := s.hostClient.ListBackends(s.ctx)
	if err != nil {
		s.log.Printf("warning: list backends: %v", err)
		return
	}
	for _, bi := range backends {
		bt := bi.Name
		dirs, err := s.Store.KnownProjectDirs(bt)
		if err != nil {
			s.log.Printf("warning: load project dirs for %s: %v", bt, err)
			continue
		}
		for _, dir := range dirs {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				continue
			}
			s.refreshPrimaryAgentsInBackground(bt, dir)
		}
	}
}
