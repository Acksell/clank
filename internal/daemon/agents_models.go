package daemon

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// HUB
func (d *Daemon) handleListAgents(w http.ResponseWriter, r *http.Request) {
	backendStr := r.URL.Query().Get("backend")
	projectDir := r.URL.Query().Get("project_dir")

	if backendStr == "" || projectDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backend and project_dir query params are required"})
		return
	}

	bt := agent.BackendType(backendStr)
	mgr, ok := d.BackendManagers[bt]
	if !ok {
		writeJSON(w, http.StatusOK, []agent.AgentInfo{})
		return
	}
	lister, ok := mgr.(agent.AgentLister)
	if !ok {
		writeJSON(w, http.StatusOK, []agent.AgentInfo{})
		return
	}

	// Try to serve from the persistent cache (SQLite) first. This avoids
	// blocking on the OpenCode server starting up — the response is instant.
	if d.Store != nil {
		cached, err := d.Store.LoadPrimaryAgents(bt, projectDir)
		if err != nil {
			d.log.Printf("warning: load cached primary agents: %v", err)
		}
		if cached != nil {
			// Return cached data immediately and refresh in the background.
			d.refreshPrimaryAgentsInBackground(bt, projectDir, lister)
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	// No cache — must fetch synchronously (first time for this project).
	agents, err := lister.ListAgents(r.Context(), projectDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if agents == nil {
		agents = []agent.AgentInfo{}
	}
	d.persistPrimaryAgents(bt, projectDir, agents)
	writeJSON(w, http.StatusOK, agents)
}

// HUB
// handleListModels returns available models for a given backend and project.
func (d *Daemon) handleListModels(w http.ResponseWriter, r *http.Request) {
	backendStr := r.URL.Query().Get("backend")
	projectDir := r.URL.Query().Get("project_dir")

	if backendStr == "" || projectDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "backend and project_dir query params are required"})
		return
	}

	bt := agent.BackendType(backendStr)
	mgr, ok := d.BackendManagers[bt]
	if !ok {
		writeJSON(w, http.StatusOK, []agent.ModelInfo{})
		return
	}
	lister, ok := mgr.(agent.ModelLister)
	if !ok {
		writeJSON(w, http.StatusOK, []agent.ModelInfo{})
		return
	}

	models, err := lister.ListModels(r.Context(), projectDir)
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
// handleDebugOpenCodeServers returns running OpenCode server processes.
// This is a debug endpoint specific to the OpenCode backend — it type-asserts
// directly to *OpenCodeBackendManager rather than going through an interface.
func (d *Daemon) handleDebugOpenCodeServers(w http.ResponseWriter, r *http.Request) {
	type serverWithSessions struct {
		agent.ServerInfo
		SessionCount int `json:"session_count"`
	}

	ocMgr, ok := d.BackendManagers[agent.BackendOpenCode].(*OpenCodeBackendManager)
	if !ok {
		writeJSON(w, http.StatusOK, []serverWithSessions{})
		return
	}

	// Count sessions per project dir.
	d.mu.RLock()
	projectSessions := make(map[string]int)
	for _, ms := range d.sessions {
		projectSessions[ms.info.ProjectDir]++
	}
	d.mu.RUnlock()

	var result []serverWithSessions
	for _, srv := range ocMgr.ListServers() {
		result = append(result, serverWithSessions{
			ServerInfo:   srv,
			SessionCount: projectSessions[srv.ProjectDir],
		})
	}
	if result == nil {
		result = []serverWithSessions{}
	}
	writeJSON(w, http.StatusOK, result)
}

// HOST
// openCodeServerURLs returns a map from project directory to server URL
// for all running OpenCode servers. Returns nil if no OpenCode backend
// manager is registered.
func (d *Daemon) openCodeServerURLs() map[string]string {
	ocMgr, ok := d.BackendManagers[agent.BackendOpenCode].(*OpenCodeBackendManager)
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
func (d *Daemon) persistPrimaryAgents(bt agent.BackendType, projectDir string, agents []agent.AgentInfo) {
	if d.Store == nil {
		return
	}
	if err := d.Store.UpsertPrimaryAgents(bt, projectDir, agents); err != nil {
		d.log.Printf("warning: persist primary agents for %s/%s: %v", bt, projectDir, err)
	}
}

// HUB
// refreshPrimaryAgentsInBackground kicks off an async refresh of the primary
// agent list for the given backend/project. The result is persisted to SQLite
// so that subsequent requests get the updated list. Safe to call multiple
// times — concurrent refreshes for the same key are deduplicated.
func (d *Daemon) refreshPrimaryAgentsInBackground(bt agent.BackendType, projectDir string, lister agent.AgentLister) {
	d.primaryAgentsRefreshMu.Lock()
	key := string(bt) + "\x00" + projectDir
	if d.primaryAgentsRefreshInFlight[key] {
		d.primaryAgentsRefreshMu.Unlock()
		return
	}
	d.primaryAgentsRefreshInFlight[key] = true
	d.primaryAgentsRefreshMu.Unlock()

	go func() {
		defer func() {
			d.primaryAgentsRefreshMu.Lock()
			delete(d.primaryAgentsRefreshInFlight, key)
			d.primaryAgentsRefreshMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
		defer cancel()

		agents, err := lister.ListAgents(ctx, projectDir)
		if err != nil {
			d.log.Printf("background primary agent refresh for %s/%s: %v", bt, projectDir, err)
			return
		}
		if agents == nil {
			agents = []agent.AgentInfo{}
		}
		d.persistPrimaryAgents(bt, projectDir, agents)
	}()
}

// HUB
// warmPrimaryAgentCaches fetches and persists primary agent lists for all
// known project directories. Called once on daemon startup after the
// reconciler has been started. The refreshPrimaryAgentsInBackground calls
// use GetOrStartServer which will wait for the reconciler to provide a
// running server — this method does NOT start servers itself.
func (d *Daemon) warmPrimaryAgentCaches() {
	if d.Store == nil {
		return
	}
	for bt, mgr := range d.BackendManagers {
		lister, ok := mgr.(agent.AgentLister)
		if !ok {
			continue
		}
		dirs, err := d.Store.KnownProjectDirs(bt)
		if err != nil {
			d.log.Printf("warning: load project dirs for %s: %v", bt, err)
			continue
		}
		for _, dir := range dirs {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				continue
			}
			d.refreshPrimaryAgentsInBackground(bt, dir, lister)
		}
	}
}
