package host

// BackendManager implementations live on the Host plane. Each manages a
// specific backend type (OpenCode, Claude Code), owning any long-lived
// resources such as OpenCode server processes.

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/acksell/clank/internal/agent"
)

// OpenCodeBackendManager implements agent.BackendManager, agent.AgentLister,
// agent.ModelLister, and agent.SessionDiscoverer for OpenCode sessions. It
// manages one OpenCode server per project directory via OpenCodeServerManager.
type OpenCodeBackendManager struct {
	serverMgr *agent.OpenCodeServerManager
}

// NewOpenCodeBackendManager creates a manager with a new server manager.
func NewOpenCodeBackendManager() *OpenCodeBackendManager {
	return &OpenCodeBackendManager{
		serverMgr: agent.NewOpenCodeServerManager(),
	}
}

// Init populates the desired server set from known project directories and
// starts the reconciler loop. The reconciler is the single owner of server
// lifecycle — it is the only code path that starts or stops servers.
// The first reconcile tick runs immediately, starting all known servers
// in parallel for fast startup.
func (m *OpenCodeBackendManager) Init(ctx context.Context, knownDirs func() ([]string, error)) error {
	dirs, err := knownDirs()
	if err != nil {
		return fmt.Errorf("load known dirs: %w", err)
	}
	var validDirs []string
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			log.Printf("[opencode] skipping desired dir %s: directory does not exist", dir)
			continue
		}
		validDirs = append(validDirs, dir)
	}
	if len(validDirs) > 0 {
		m.serverMgr.AddDesired(validDirs...)
		log.Printf("[opencode] added %d project dirs to desired set", len(validDirs))
	}

	go m.serverMgr.Run(ctx)
	return nil
}

// CreateBackend creates an OpenCode SessionBackend. It ensures an OpenCode
// server is running for the project directory before creating the backend.
// The backend receives a resolver closure that re-resolves the server URL
// on reconnect (handles server restarts on new ports).
func (m *OpenCodeBackendManager) CreateBackend(req agent.StartRequest) (agent.SessionBackend, error) {
	serverURL, err := m.serverMgr.GetOrStartServer(context.Background(), req.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("start opencode server for %s: %w", req.ProjectDir, err)
	}
	resolver := func(ctx context.Context) (string, error) {
		return m.serverMgr.GetOrStartServer(ctx, req.ProjectDir)
	}
	return agent.NewOpenCodeBackend(serverURL, req.SessionID, resolver), nil
}

// Shutdown stops all managed OpenCode servers.
func (m *OpenCodeBackendManager) Shutdown() {
	m.serverMgr.StopAll()
}

// ListAgents returns available agents for the given project directory.
func (m *OpenCodeBackendManager) ListAgents(ctx context.Context, projectDir string) ([]agent.AgentInfo, error) {
	return m.serverMgr.ListAgents(ctx, projectDir)
}

// ListModels returns available models from connected providers for the given project directory.
func (m *OpenCodeBackendManager) ListModels(ctx context.Context, projectDir string) ([]agent.ModelInfo, error) {
	return m.serverMgr.ListModels(ctx, projectDir)
}

// ServerManager returns the underlying server manager. Exported for tests
// that need to configure the manager (e.g. injecting a fake startServerFn).
func (m *OpenCodeBackendManager) ServerManager() *agent.OpenCodeServerManager {
	return m.serverMgr
}

// ListServers returns running OpenCode server info from the server manager.
func (m *OpenCodeBackendManager) ListServers() []agent.ServerInfo {
	return m.serverMgr.ListServers()
}

// DiscoverSessions lists all projects from the OpenCode server, then lists
// all sessions using the same seed server. This avoids starting a new server
// per worktree (the old bug that caused triple-starts on startup).
//
// Worktrees that are clearly invalid (e.g. "/") are filtered out. Valid
// worktrees are added to the desired set so the reconciler starts servers
// for them (needed for future backend connections), but session listing
// uses the existing seed server.
func (m *OpenCodeBackendManager) DiscoverSessions(ctx context.Context, seedDir string) ([]agent.SessionSnapshot, error) {
	// Get the seed server URL — this will wait for the reconciler if needed.
	seedURL, err := m.serverMgr.GetOrStartServer(ctx, seedDir)
	if err != nil {
		return nil, fmt.Errorf("get seed server: %w", err)
	}

	projects, err := m.serverMgr.ListProjects(ctx, seedDir)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	// Collect valid worktrees for the reconciler's desired set.
	var validDirs []string
	for _, proj := range projects {
		// Filter bogus dirs: root dir, empty, or non-existent.
		if proj.Worktree == "" || proj.Worktree == "/" {
			continue
		}
		if _, err := os.Stat(proj.Worktree); os.IsNotExist(err) {
			continue
		}
		validDirs = append(validDirs, proj.Worktree)
	}

	// Add discovered worktrees to desired set so they get servers for
	// future backend operations. The reconciler will start them.
	if len(validDirs) > 0 {
		m.serverMgr.AddDesired(validDirs...)
	}

	// List sessions from the SEED server — all projects share the same
	// OpenCode database, so one server can return all sessions.
	sessions, err := m.serverMgr.ListSessionsFromServer(ctx, seedURL)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

// ClaudeBackendManager implements agent.BackendManager for Claude Code sessions.
type ClaudeBackendManager struct{}

// NewClaudeBackendManager creates a new Claude backend manager.
func NewClaudeBackendManager() *ClaudeBackendManager {
	return &ClaudeBackendManager{}
}

// CreateBackend creates a Claude Code SessionBackend.
func (m *ClaudeBackendManager) CreateBackend(req agent.StartRequest) (agent.SessionBackend, error) {
	return agent.NewClaudeCodeBackend(), nil
}

// Init is a no-op for Claude — there are no long-lived servers to manage.
func (m *ClaudeBackendManager) Init(ctx context.Context, knownDirs func() ([]string, error)) error {
	return nil
}

// Shutdown is a no-op for Claude — each session manages its own SDK client connection.
func (m *ClaudeBackendManager) Shutdown() {}
