package daemon_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/daemon"
	"github.com/acksell/clank/internal/store"
)

// mockBackend implements agent.SessionBackend for testing.
type mockBackend struct {
	mu                sync.Mutex
	events            chan agent.Event
	status            agent.SessionStatus
	sessionID         string
	started           bool
	stopped           bool
	messages          []string
	aborted           bool
	reverted          bool
	revertedMessageID string
	forked            bool
	forkedMessageID   string
	permissionReplied bool
	permissionID      string
	permissionAllow   bool

	// history is returned by Messages(). Tests can set it to control
	// the message history returned by the backend.
	history []agent.MessageData

	// onStart is called during Start, allowing tests to control behavior.
	onStart func(ctx context.Context, req agent.StartRequest) error
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		events:    make(chan agent.Event, 64),
		status:    agent.StatusStarting,
		sessionID: "mock-session-1",
	}
}

func (m *mockBackend) Start(ctx context.Context, req agent.StartRequest) error {
	m.mu.Lock()
	m.started = true
	m.status = agent.StatusBusy
	m.mu.Unlock()

	// Emit a status change event.
	m.events <- agent.Event{
		Type:      agent.EventStatusChange,
		Timestamp: time.Now(),
		Data: agent.StatusChangeData{
			OldStatus: agent.StatusStarting,
			NewStatus: agent.StatusBusy,
		},
	}

	if m.onStart != nil {
		return m.onStart(ctx, req)
	}
	return nil
}

func (m *mockBackend) Watch(ctx context.Context) error {
	return nil
}

func (m *mockBackend) SendMessage(ctx context.Context, opts agent.SendMessageOpts) error {
	m.mu.Lock()
	m.messages = append(m.messages, opts.Text)
	m.mu.Unlock()

	// Emit a message event.
	m.events <- agent.Event{
		Type:      agent.EventMessage,
		Timestamp: time.Now(),
		Data: agent.MessageData{
			Role:    "user",
			Content: opts.Text,
		},
	}
	return nil
}

func (m *mockBackend) Abort(ctx context.Context) error {
	m.mu.Lock()
	m.aborted = true
	m.status = agent.StatusIdle
	m.mu.Unlock()

	m.events <- agent.Event{
		Type:      agent.EventStatusChange,
		Timestamp: time.Now(),
		Data: agent.StatusChangeData{
			OldStatus: agent.StatusBusy,
			NewStatus: agent.StatusIdle,
		},
	}
	return nil
}

func (m *mockBackend) Stop() error {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	close(m.events)
	return nil
}

func (m *mockBackend) Events() <-chan agent.Event {
	return m.events
}

func (m *mockBackend) Status() agent.SessionStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockBackend) SessionID() string {
	return m.sessionID
}

func (m *mockBackend) Messages(ctx context.Context) ([]agent.MessageData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.history, nil
}

func (m *mockBackend) Revert(ctx context.Context, messageID string) error {
	m.mu.Lock()
	m.reverted = true
	m.revertedMessageID = messageID
	m.mu.Unlock()
	return nil
}

func (m *mockBackend) Fork(ctx context.Context, messageID string) (agent.ForkResult, error) {
	m.mu.Lock()
	m.forked = true
	m.forkedMessageID = messageID
	m.mu.Unlock()
	return agent.ForkResult{ID: "forked-external-id", Title: "Forked session"}, nil
}

func (m *mockBackend) RespondPermission(ctx context.Context, permissionID string, allow bool) error {
	m.mu.Lock()
	m.permissionReplied = true
	m.permissionID = permissionID
	m.permissionAllow = allow
	m.mu.Unlock()
	return nil
}

// mockBackendManager implements agent.BackendManager for testing. It wraps
// a creation function so tests can intercept and track created backends.
type mockBackendManager struct {
	mu      sync.Mutex
	create  func(req agent.StartRequest) *mockBackend // custom creation logic; nil = newMockBackend()
	latest  *mockBackend                              // last created backend
	all     []*mockBackend                            // all created backends
	stopped bool
}

func newMockBackendManager() *mockBackendManager {
	return &mockBackendManager{}
}

func (m *mockBackendManager) CreateBackend(req agent.StartRequest) (agent.SessionBackend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b *mockBackend
	if m.create != nil {
		b = m.create(req)
	} else {
		b = newMockBackend()
	}
	m.latest = b
	m.all = append(m.all, b)
	return b, nil
}

func (m *mockBackendManager) Init(ctx context.Context, knownDirs func() ([]string, error)) error {
	return nil
}

func (m *mockBackendManager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
}

// getLatest returns the most recently created mockBackend.
func (m *mockBackendManager) getLatest() *mockBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.latest
}

// getAll returns all created mockBackends.
func (m *mockBackendManager) getAll() []*mockBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]*mockBackend, len(m.all))
	copy(cp, m.all)
	return cp
}

// mockDiscovererManager implements BackendManager + SessionDiscoverer.
type mockDiscovererManager struct {
	mockBackendManager
	snapshots []agent.SessionSnapshot
}

func (m *mockDiscovererManager) DiscoverSessions(ctx context.Context, seedDir string) ([]agent.SessionSnapshot, error) {
	return m.snapshots, nil
}

// mockAgentListerManager implements BackendManager + AgentLister.
type mockAgentListerManager struct {
	mockBackendManager
	agents func(ctx context.Context, projectDir string) ([]agent.AgentInfo, error)
}

func (m *mockAgentListerManager) ListAgents(ctx context.Context, projectDir string) ([]agent.AgentInfo, error) {
	return m.agents(ctx, projectDir)
}

// shortTempDir creates a temp directory with a short path suitable for Unix sockets.
// macOS has a 104-char limit on socket paths, and t.TempDir() can produce
// paths that exceed this when combined with long test names.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "clank-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// testDaemon creates a daemon in a temp directory, starts it, and returns
// the daemon, a connected client, and a cleanup function.
func testDaemon(t *testing.T) (*daemon.Daemon, *daemon.Client, func()) {
	t.Helper()

	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	d := daemon.NewWithPaths(sockPath, pidPath)

	// Wire up mock backend manager for all backend types.
	mgr := newMockBackendManager()
	d.BackendManagers[agent.BackendOpenCode] = mgr
	d.BackendManagers[agent.BackendClaudeCode] = mgr

	// Start daemon in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Run()
	}()

	// Wait for socket to exist.
	client := daemon.NewClient(sockPath)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if err := client.Ping(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("daemon did not start in time")
		case err := <-errCh:
			t.Fatalf("daemon exited unexpectedly: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}

	cleanup := func() {
		d.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
	}

	// Store lastBackend accessor on test via a helper.
	// We'll access it through the test closure.
	_ = mgr

	return d, client, cleanup
}

// --- Tests ---

func testDaemonWithBackendAccess(t *testing.T) (*daemon.Daemon, *daemon.Client, func() *mockBackend, func()) {
	t.Helper()

	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	d := daemon.NewWithPaths(sockPath, pidPath)

	mgr := newMockBackendManager()
	d.BackendManagers[agent.BackendOpenCode] = mgr
	d.BackendManagers[agent.BackendClaudeCode] = mgr

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)

	getBackend := func() *mockBackend {
		return mgr.getLatest()
	}

	cleanup := func() {
		d.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
	}

	return d, client, getBackend, cleanup
}

// receiveEvents collects up to n events from the channel, timing out after d.
func receiveEvents(ch <-chan agent.Event, n int, timeout time.Duration) []agent.Event {
	var result []agent.Event
	timer := time.After(timeout)
	for len(result) < n {
		select {
		case evt, ok := <-ch:
			if !ok {
				return result
			}
			result = append(result, evt)
		case <-timer:
			return result
		}
	}
	return result
}

// TestEventRoundTrip_StatusChange verifies that StatusChangeData survives
// the backend -> daemon SSE -> client JSON round-trip as a concrete type.
// This test is separate because the event originates from Start(), not injection.
func eventTypes(events []agent.Event) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = string(e.Type)
	}
	return types
}

func testDaemonWithDiscover(t *testing.T, snapshots []agent.SessionSnapshot) (*daemon.Daemon, *daemon.Client, func() *mockBackend, func()) {
	t.Helper()

	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")

	d := daemon.NewWithPaths(sockPath, pidPath)

	discMgr := &mockDiscovererManager{
		snapshots: snapshots,
	}
	// Custom create func to propagate sessionID from the request.
	discMgr.create = func(req agent.StartRequest) *mockBackend {
		b := newMockBackend()
		b.sessionID = req.SessionID
		return b
	}
	d.BackendManagers[agent.BackendOpenCode] = discMgr
	d.BackendManagers[agent.BackendClaudeCode] = &discMgr.mockBackendManager

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client := daemon.NewClient(sockPath)
	waitForDaemon(t, client)

	getBackend := func() *mockBackend {
		return discMgr.getLatest()
	}

	cleanup := func() {
		d.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
	}

	return d, client, getBackend, cleanup
}

func testDaemonWithStore(t *testing.T, dir string) (d *daemon.Daemon, client *daemon.Client, sockPath, pidPath, dbPath string, cleanup func()) {
	t.Helper()

	if dir == "" {
		dir = shortTempDir(t)
	}
	sockPath = filepath.Join(dir, "test.sock")
	pidPath = filepath.Join(dir, "test.pid")
	dbPath = filepath.Join(dir, "test.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	d = daemon.NewWithPaths(sockPath, pidPath)
	d.Store = st

	// Wire up mock backend manager.
	mgr := newMockBackendManager()
	d.BackendManagers[agent.BackendOpenCode] = mgr
	d.BackendManagers[agent.BackendClaudeCode] = mgr

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run() }()

	client = daemon.NewClient(sockPath)
	waitForDaemon(t, client)

	cleanup = func() {
		d.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
	}
	return
}

func waitForDaemon(t *testing.T, client *daemon.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if err := client.Ping(ctx); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("daemon did not start in time")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// --- Git helpers for merge tests ---

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

// initGitRepo creates a fresh git repo with an initial commit.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(dir, "README.md"), "# test\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "initial commit")
	return dir
}
