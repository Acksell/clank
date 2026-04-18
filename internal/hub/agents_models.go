package hub

import (
	"context"
	"os"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

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

		agents, err := s.hostClient.Backend(bt).Agents(ctx, projectDir)
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
	backends, err := s.hostClient.Backends(s.ctx)
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
