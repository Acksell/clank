package daemon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/daemon"
	"github.com/acksell/clank/internal/store"
)

func TestDaemonListAgents(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	d := daemon.NewWithPaths(sockPath, pidPath)

	// OpenCode manager with agent listing support.
	ocMgr := &mockAgentListerManager{
		agents: func(ctx context.Context, projectDir string) ([]agent.AgentInfo, error) {
			return []agent.AgentInfo{
				{Name: "build", Description: "Build agent", Mode: "primary"},
				{Name: "plan", Description: "Plan agent", Mode: "primary"},
			}, nil
		},
	}
	d.BackendManagers[agent.BackendOpenCode] = ocMgr

	// Claude manager — no agent lister support.
	d.BackendManagers[agent.BackendClaudeCode] = newMockBackendManager()

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)
	defer d.Stop()

	ctx := context.Background()

	// List agents for OpenCode backend.
	agents, err := client.ListAgents(ctx, agent.BackendOpenCode, "/tmp/test")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].Name != "build" || agents[1].Name != "plan" {
		t.Errorf("unexpected agents: %+v", agents)
	}

	// List agents for Claude Code (no agent lister support).
	agents, err = client.ListAgents(ctx, agent.BackendClaudeCode, "/tmp/test")
	if err != nil {
		t.Fatalf("ListAgents for Claude Code: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents for Claude Code, got %d", len(agents))
	}
}

func TestDaemonListAgentsMissingParams(t *testing.T) {
	t.Parallel()
	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	// Missing backend param — should return an error.
	_, err := client.ListAgents(ctx, "", "/tmp/test")
	if err == nil {
		t.Error("expected error for missing backend param")
	}
}

func TestDaemonListAgentsReturnsCachedFromStore(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")
	dbPath := filepath.Join(dir, "test.db")

	// Pre-seed the store with cached primary agents.
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	cachedAgents := []agent.AgentInfo{
		{Name: "build", Description: "Cached build", Mode: "primary"},
		{Name: "plan", Description: "Cached plan", Mode: "primary"},
	}
	if err := st.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/test-proj", cachedAgents); err != nil {
		t.Fatalf("UpsertPrimaryAgents: %v", err)
	}

	d := daemon.NewWithPaths(sockPath, pidPath)
	d.Store = st

	// Use an agent lister that tracks whether it was called synchronously.
	// The lister blocks until explicitly unblocked, so if the handler
	// returns before unblocking, we know it served from cache.
	listerCalled := make(chan struct{}, 1)
	ocMgr := &mockAgentListerManager{
		agents: func(ctx context.Context, projectDir string) ([]agent.AgentInfo, error) {
			listerCalled <- struct{}{}
			return []agent.AgentInfo{
				{Name: "build", Description: "Fresh build", Mode: "primary"},
				{Name: "plan", Description: "Fresh plan", Mode: "primary"},
				{Name: "debug", Description: "Fresh debug", Mode: "primary"},
			}, nil
		},
	}
	d.BackendManagers[agent.BackendOpenCode] = ocMgr
	d.BackendManagers[agent.BackendClaudeCode] = newMockBackendManager()

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)
	defer func() {
		d.Stop()
		<-errCh
	}()

	ctx := context.Background()

	// Drain any background refresh triggered by warmAgentCaches (from
	// KnownProjectDirs finding sessions in the store — but we didn't
	// create any sessions, so this shouldn't fire).

	// Request agents — should return cached data immediately.
	agents, err := client.ListAgents(ctx, agent.BackendOpenCode, "/tmp/test-proj")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}

	// Should get the CACHED agents (2 agents, not 3).
	if len(agents) != 2 {
		t.Fatalf("expected 2 cached agents, got %d: %+v", len(agents), agents)
	}
	if agents[0].Description != "Cached build" {
		t.Errorf("expected cached description, got %q", agents[0].Description)
	}

	// The background refresh should have been triggered.
	select {
	case <-listerCalled:
		// Good — background refresh happened.
	case <-time.After(5 * time.Second):
		t.Error("expected background refresh to be triggered")
	}

	// After the refresh completes, subsequent requests should get the fresh data.
	time.Sleep(200 * time.Millisecond)

	agents, err = client.ListAgents(ctx, agent.BackendOpenCode, "/tmp/test-proj")
	if err != nil {
		t.Fatalf("ListAgents (2nd call): %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 fresh agents after refresh, got %d: %+v", len(agents), agents)
	}
	if agents[0].Description != "Fresh build" {
		t.Errorf("expected fresh description, got %q", agents[0].Description)
	}
}

func TestDaemonListAgentsFallsBackToListerOnCacheMiss(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")
	dbPath := filepath.Join(dir, "test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// No pre-seeded agents — cache miss.

	d := daemon.NewWithPaths(sockPath, pidPath)
	d.Store = st

	ocMgr := &mockAgentListerManager{
		agents: func(ctx context.Context, projectDir string) ([]agent.AgentInfo, error) {
			return []agent.AgentInfo{
				{Name: "build", Description: "Build agent", Mode: "primary"},
			}, nil
		},
	}
	d.BackendManagers[agent.BackendOpenCode] = ocMgr
	d.BackendManagers[agent.BackendClaudeCode] = newMockBackendManager()

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)
	defer func() {
		d.Stop()
		<-errCh
	}()

	ctx := context.Background()

	// No cache — should fall back to synchronous lister call.
	agents, err := client.ListAgents(ctx, agent.BackendOpenCode, "/tmp/uncached-proj")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 || agents[0].Name != "build" {
		t.Errorf("unexpected agents: %+v", agents)
	}

	// After the synchronous call, the result should be persisted.
	cached, err := st.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/uncached-proj")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents: %v", err)
	}
	if len(cached) != 1 || cached[0].Name != "build" {
		t.Errorf("expected persisted agents, got %+v", cached)
	}
}

func TestDaemonDebugOpenCodeServers(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	d := daemon.NewWithPaths(sockPath, pidPath)

	// Use a real OpenCodeBackendManager with a fake startServerFn so the
	// reconciler populates the servers map without spawning real processes.
	ocMgr := daemon.NewOpenCodeBackendManager()
	ocMgr.ServerManager().SetStartServerFn(func(ctx context.Context, projectDir string) (*agent.OpenCodeServer, error) {
		return &agent.OpenCodeServer{
			URL:        "http://127.0.0.1:54321",
			ProjectDir: projectDir,
			StartedAt:  time.Now(),
		}, nil
	})

	d.BackendManagers[agent.BackendOpenCode] = ocMgr
	d.BackendManagers[agent.BackendClaudeCode] = newMockBackendManager()

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)
	defer d.Stop()

	ctx := context.Background()

	// Create a session so the server gets started via GetOrStartServer.
	_, err := client.CreateSession(ctx, agent.StartRequest{
		Backend:    agent.BackendOpenCode,
		ProjectDir: "/tmp/project-a",
		Prompt:     "test",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Wait for the server to appear (reconciler may need a tick).
	var servers []daemon.ServerStatus
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		servers, err = client.ListOpenCodeServers(ctx)
		if err != nil {
			t.Fatalf("ListOpenCodeServers: %v", err)
		}
		if len(servers) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	srv := servers[0]
	if srv.ProjectDir != "/tmp/project-a" {
		t.Errorf("project dir = %q, want /tmp/project-a", srv.ProjectDir)
	}
	if srv.URL != "http://127.0.0.1:54321" {
		t.Errorf("URL = %q, want http://127.0.0.1:54321", srv.URL)
	}
	if srv.SessionCount != 1 {
		t.Errorf("session count = %d, want 1", srv.SessionCount)
	}
}

func TestDaemonDebugOpenCodeServersEmpty(t *testing.T) {
	t.Parallel()
	// When no OpenCode backend manager is registered, the endpoint
	// should return an empty list, not an error.
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	d := daemon.NewWithPaths(sockPath, pidPath)
	d.BackendManagers[agent.BackendOpenCode] = newMockBackendManager()

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)
	defer d.Stop()

	servers, err := client.ListOpenCodeServers(context.Background())
	if err != nil {
		t.Fatalf("ListOpenCodeServers: %v", err)
	}
	if len(servers) != 0 {
		t.Errorf("expected 0 servers, got %d", len(servers))
	}
}
