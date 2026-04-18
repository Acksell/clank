package store_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/store"

	_ "modernc.org/sqlite"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.db")
}

func mustOpen(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesDB(t *testing.T) {
	t.Parallel()
	path := tempDBPath(t)

	// File should not exist yet.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected DB file to not exist before Open")
	}

	s := mustOpen(t, path)

	// File should now exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected DB file to exist after Open: %v", err)
	}

	// Should be able to load sessions (empty).
	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions from fresh DB, got %d", len(sessions))
	}
}

func TestMigrationIdempotent(t *testing.T) {
	t.Parallel()
	path := tempDBPath(t)

	// Open twice — migration should be idempotent.
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	s2.Close()
}

func TestUpsertAndLoad(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:             "ses-001",
		ExternalID:     "oc-ext-001",
		Backend:        agent.BackendOpenCode,
		Status:         agent.StatusBusy,
		Visibility:     agent.VisibilityDone,
		FollowUp:       true,
		ProjectDir:     "/tmp/project-a",
		ProjectName:    "project-a",
		WorktreeDir:    "/home/user/.clank/worktrees/project-a/feat-login",
		WorktreeBranch: "feat/login",
		Prompt:         "Fix the login bug",
		Title:          "Fix authentication",
		TicketID:       "TICKET-42",
		Agent:          "plan",
		Draft:          "work in progress",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-1 * time.Hour),
		LastReadAt:     now,
	}

	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	got := sessions[0]
	if got.ID != info.ID {
		t.Errorf("ID = %q, want %q", got.ID, info.ID)
	}
	if got.ExternalID != info.ExternalID {
		t.Errorf("ExternalID = %q, want %q", got.ExternalID, info.ExternalID)
	}
	if got.Backend != info.Backend {
		t.Errorf("Backend = %q, want %q", got.Backend, info.Backend)
	}
	if got.Status != info.Status {
		t.Errorf("Status = %q, want %q", got.Status, info.Status)
	}
	if got.Visibility != info.Visibility {
		t.Errorf("Visibility = %q, want %q", got.Visibility, info.Visibility)
	}
	if got.FollowUp != info.FollowUp {
		t.Errorf("FollowUp = %v, want %v", got.FollowUp, info.FollowUp)
	}
	if got.ProjectDir != info.ProjectDir {
		t.Errorf("ProjectDir = %q, want %q", got.ProjectDir, info.ProjectDir)
	}
	if got.ProjectName != info.ProjectName {
		t.Errorf("ProjectName = %q, want %q", got.ProjectName, info.ProjectName)
	}
	if got.WorktreeBranch != info.WorktreeBranch {
		t.Errorf("Branch = %q, want %q", got.WorktreeBranch, info.WorktreeBranch)
	}
	if got.WorktreeDir != info.WorktreeDir {
		t.Errorf("WorktreeDir = %q, want %q", got.WorktreeDir, info.WorktreeDir)
	}
	if got.Prompt != info.Prompt {
		t.Errorf("Prompt = %q, want %q", got.Prompt, info.Prompt)
	}
	if got.Title != info.Title {
		t.Errorf("Title = %q, want %q", got.Title, info.Title)
	}
	if got.TicketID != info.TicketID {
		t.Errorf("TicketID = %q, want %q", got.TicketID, info.TicketID)
	}
	if got.Agent != info.Agent {
		t.Errorf("Agent = %q, want %q", got.Agent, info.Agent)
	}
	if got.Draft != info.Draft {
		t.Errorf("Draft = %q, want %q", got.Draft, info.Draft)
	}
	if !got.CreatedAt.Equal(info.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, info.CreatedAt)
	}
	if !got.UpdatedAt.Equal(info.UpdatedAt) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, info.UpdatedAt)
	}
	if !got.LastReadAt.Equal(info.LastReadAt) {
		t.Errorf("LastReadAt = %v, want %v", got.LastReadAt, info.LastReadAt)
	}
}

func TestUpsertUpdatesExisting(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:          "ses-002",
		Backend:     agent.BackendOpenCode,
		Status:      agent.StatusIdle,
		ProjectDir:  "/tmp/project-b",
		ProjectName: "project-b",
		Prompt:      "original prompt",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession (initial): %v", err)
	}

	// Update fields.
	info.Visibility = agent.VisibilityArchived
	info.FollowUp = true
	info.Title = "Updated title"
	info.Draft = "new draft"
	info.UpdatedAt = now.Add(1 * time.Hour)
	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession (update): %v", err)
	}

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after upsert, got %d", len(sessions))
	}

	got := sessions[0]
	if got.Visibility != agent.VisibilityArchived {
		t.Errorf("Visibility = %q, want %q", got.Visibility, agent.VisibilityArchived)
	}
	if !got.FollowUp {
		t.Error("expected FollowUp = true")
	}
	if got.Title != "Updated title" {
		t.Errorf("Title = %q, want %q", got.Title, "Updated title")
	}
	if got.Draft != "new draft" {
		t.Errorf("Draft = %q, want %q", got.Draft, "new draft")
	}
}

func TestDeleteSession(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:          "ses-003",
		Backend:     agent.BackendOpenCode,
		Status:      agent.StatusIdle,
		ProjectDir:  "/tmp/project-c",
		ProjectName: "project-c",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	if err := s.DeleteSession("ses-003"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after delete, got %d", len(sessions))
	}
}

func TestDeleteNonexistentSession(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	// Deleting a nonexistent session should not error.
	if err := s.DeleteSession("nonexistent"); err != nil {
		t.Fatalf("DeleteSession(nonexistent): %v", err)
	}
}

func TestLoadSessionsEmpty(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if sessions == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestLastReadAtZeroValue(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:          "ses-004",
		Backend:     agent.BackendOpenCode,
		Status:      agent.StatusIdle,
		ProjectDir:  "/tmp/project-d",
		ProjectName: "project-d",
		CreatedAt:   now,
		UpdatedAt:   now,
		// LastReadAt intentionally left as zero value.
	}
	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if !sessions[0].LastReadAt.IsZero() {
		t.Errorf("expected zero LastReadAt, got %v", sessions[0].LastReadAt)
	}
}

func TestFindByExternalID(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:          "ses-005",
		ExternalID:  "oc-ext-005",
		Backend:     agent.BackendOpenCode,
		Status:      agent.StatusIdle,
		Visibility:  agent.VisibilityDone,
		FollowUp:    true,
		ProjectDir:  "/tmp/project-e",
		ProjectName: "project-e",
		Draft:       "saved draft",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	// Found case.
	got, err := s.FindByExternalID("oc-ext-005")
	if err != nil {
		t.Fatalf("FindByExternalID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.ID != "ses-005" {
		t.Errorf("ID = %q, want %q", got.ID, "ses-005")
	}
	if got.Visibility != agent.VisibilityDone {
		t.Errorf("Visibility = %q, want %q", got.Visibility, agent.VisibilityDone)
	}
	if !got.FollowUp {
		t.Error("expected FollowUp = true")
	}
	if got.Draft != "saved draft" {
		t.Errorf("Draft = %q, want %q", got.Draft, "saved draft")
	}

	// Not found case.
	got, err = s.FindByExternalID("nonexistent")
	if err != nil {
		t.Fatalf("FindByExternalID(nonexistent): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent external_id, got %+v", got)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	t.Parallel()
	path := tempDBPath(t)

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:          "ses-006",
		ExternalID:  "oc-ext-006",
		Backend:     agent.BackendOpenCode,
		Status:      agent.StatusBusy,
		Visibility:  agent.VisibilityDone,
		FollowUp:    true,
		ProjectDir:  "/tmp/project-f",
		ProjectName: "project-f",
		Prompt:      "do stuff",
		Title:       "Doing stuff",
		Draft:       "my draft",
		CreatedAt:   now.Add(-1 * time.Hour),
		UpdatedAt:   now,
		LastReadAt:  now,
	}

	// Write with first store instance.
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open (1): %v", err)
	}
	if err := s1.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	s1.Close()

	// Read with second store instance.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open (2): %v", err)
	}
	defer s2.Close()

	sessions, err := s2.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	got := sessions[0]
	if got.ID != "ses-006" {
		t.Errorf("ID = %q, want %q", got.ID, "ses-006")
	}
	if got.Visibility != agent.VisibilityDone {
		t.Errorf("Visibility = %q, want %q", got.Visibility, agent.VisibilityDone)
	}
	if !got.FollowUp {
		t.Error("expected FollowUp = true")
	}
	if got.Draft != "my draft" {
		t.Errorf("Draft = %q, want %q", got.Draft, "my draft")
	}
	if got.Status != agent.StatusBusy {
		t.Errorf("Status = %q, want %q", got.Status, agent.StatusBusy)
	}
}

func TestMultipleSessions(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	for i, id := range []string{"ses-a", "ses-b", "ses-c"} {
		info := agent.SessionInfo{
			ID:          id,
			Backend:     agent.BackendOpenCode,
			Status:      agent.StatusIdle,
			ProjectDir:  "/tmp/project",
			ProjectName: "project",
			CreatedAt:   now.Add(time.Duration(i) * time.Minute),
			UpdatedAt:   now.Add(time.Duration(i) * time.Minute),
		}
		if err := s.UpsertSession(info); err != nil {
			t.Fatalf("UpsertSession(%s): %v", id, err)
		}
	}

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// Delete the middle one.
	if err := s.DeleteSession("ses-b"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	sessions, err = s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions after delete: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	ids := map[string]bool{}
	for _, s := range sessions {
		ids[s.ID] = true
	}
	if ids["ses-b"] {
		t.Error("expected ses-b to be deleted")
	}
	if !ids["ses-a"] || !ids["ses-c"] {
		t.Errorf("expected ses-a and ses-c to remain, got %v", ids)
	}
}

func TestLoadPrimaryAgentsEmpty(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	agents, err := s.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/project")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents: %v", err)
	}
	if agents != nil {
		t.Errorf("expected nil for uncached project, got %v", agents)
	}
}

func TestUpsertAndLoadPrimaryAgents(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	agents := []agent.AgentInfo{
		{Name: "build", Description: "Build agent", Mode: "primary", Hidden: false},
		{Name: "plan", Description: "Plan agent", Mode: "primary", Hidden: false},
	}

	if err := s.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/project", agents); err != nil {
		t.Fatalf("UpsertPrimaryAgents: %v", err)
	}

	got, err := s.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/project")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(got))
	}
	if got[0].Name != "build" {
		t.Errorf("agents[0].Name = %q, want %q", got[0].Name, "build")
	}
	if got[1].Name != "plan" {
		t.Errorf("agents[1].Name = %q, want %q", got[1].Name, "plan")
	}
}

func TestUpsertPrimaryAgentsOverwrites(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	initial := []agent.AgentInfo{
		{Name: "build", Description: "Build agent", Mode: "primary"},
	}
	if err := s.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/project", initial); err != nil {
		t.Fatalf("UpsertPrimaryAgents (initial): %v", err)
	}

	updated := []agent.AgentInfo{
		{Name: "build", Description: "Build agent v2", Mode: "primary"},
		{Name: "plan", Description: "Plan agent", Mode: "primary"},
	}
	if err := s.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/project", updated); err != nil {
		t.Fatalf("UpsertPrimaryAgents (update): %v", err)
	}

	got, err := s.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/project")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 agents after update, got %d", len(got))
	}
	if got[0].Description != "Build agent v2" {
		t.Errorf("agents[0].Description = %q, want %q", got[0].Description, "Build agent v2")
	}
}

func TestLoadPrimaryAgentsIsolatedByBackendAndProject(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	agentsA := []agent.AgentInfo{{Name: "build", Mode: "primary"}}
	agentsB := []agent.AgentInfo{{Name: "plan", Mode: "primary"}}

	if err := s.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/project-a", agentsA); err != nil {
		t.Fatalf("UpsertPrimaryAgents A: %v", err)
	}
	if err := s.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/project-b", agentsB); err != nil {
		t.Fatalf("UpsertPrimaryAgents B: %v", err)
	}

	gotA, err := s.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/project-a")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents A: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Name != "build" {
		t.Errorf("project-a agents = %v, want [{build}]", gotA)
	}

	gotB, err := s.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/project-b")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents B: %v", err)
	}
	if len(gotB) != 1 || gotB[0].Name != "plan" {
		t.Errorf("project-b agents = %v, want [{plan}]", gotB)
	}

	// Different backend, same project — should be empty.
	gotC, err := s.LoadPrimaryAgents(agent.BackendClaudeCode, "/tmp/project-a")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents claude: %v", err)
	}
	if gotC != nil {
		t.Errorf("expected nil for claude-code backend, got %v", gotC)
	}
}

func TestKnownProjectDirs(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	for _, dir := range []string{"/tmp/project-a", "/tmp/project-b", "/tmp/project-a"} {
		info := agent.SessionInfo{
			ID:          "ses-" + dir,
			Backend:     agent.BackendOpenCode,
			Status:      agent.StatusIdle,
			ProjectDir:  dir,
			ProjectName: "proj",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.UpsertSession(info); err != nil {
			t.Fatalf("UpsertSession: %v", err)
		}
	}

	dirs, err := s.KnownProjectDirs(agent.BackendOpenCode)
	if err != nil {
		t.Fatalf("KnownProjectDirs: %v", err)
	}
	dirSet := map[string]bool{}
	for _, d := range dirs {
		dirSet[d] = true
	}
	if len(dirSet) != 2 {
		t.Errorf("expected 2 distinct dirs, got %d: %v", len(dirSet), dirs)
	}
	if !dirSet["/tmp/project-a"] || !dirSet["/tmp/project-b"] {
		t.Errorf("expected project-a and project-b, got %v", dirs)
	}

	// Different backend should return empty.
	dirs, err = s.KnownProjectDirs(agent.BackendClaudeCode)
	if err != nil {
		t.Fatalf("KnownProjectDirs claude: %v", err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected 0 dirs for claude-code, got %d", len(dirs))
	}
}

func TestMigrationV2Idempotent(t *testing.T) {
	t.Parallel()
	path := tempDBPath(t)

	// Open, write primary agents, close.
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	agents := []agent.AgentInfo{{Name: "build", Mode: "primary"}}
	if err := s1.UpsertPrimaryAgents(agent.BackendOpenCode, "/tmp/proj", agents); err != nil {
		t.Fatalf("UpsertPrimaryAgents: %v", err)
	}
	s1.Close()

	// Re-open — migration should be idempotent and data should survive.
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	got, err := s2.LoadPrimaryAgents(agent.BackendOpenCode, "/tmp/proj")
	if err != nil {
		t.Fatalf("LoadPrimaryAgents: %v", err)
	}
	if len(got) != 1 || got[0].Name != "build" {
		t.Errorf("expected [{build}] after reopen, got %v", got)
	}
}

// TestConcurrentWrites verifies that many goroutines can write to the store
// simultaneously without SQLITE_BUSY errors. Before the SetMaxOpenConns(1)
// fix, pooled connections lacked the busy_timeout PRAGMA and this test
// would fail with "database is locked (5) (SQLITE_BUSY)".
func TestConcurrentWrites(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	const numWriters = 20
	now := time.Now().Truncate(time.Millisecond)

	var wg sync.WaitGroup
	errs := make(chan error, numWriters*2) // sessions + primary agents

	// Launch concurrent session upserts.
	for i := range numWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := agent.SessionInfo{
				ID:          fmt.Sprintf("ses-concurrent-%d", i),
				Backend:     agent.BackendOpenCode,
				Status:      agent.StatusIdle,
				ProjectDir:  fmt.Sprintf("/tmp/project-%d", i),
				ProjectName: fmt.Sprintf("project-%d", i),
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := s.UpsertSession(info); err != nil {
				errs <- fmt.Errorf("UpsertSession(%d): %w", i, err)
			}
		}()
	}

	// Launch concurrent primary agent upserts.
	for i := range numWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			agents := []agent.AgentInfo{
				{Name: fmt.Sprintf("agent-%d", i), Mode: "primary"},
			}
			if err := s.UpsertPrimaryAgents(agent.BackendOpenCode, fmt.Sprintf("/tmp/project-%d", i), agents); err != nil {
				errs <- fmt.Errorf("UpsertPrimaryAgents(%d): %w", i, err)
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	// Verify all sessions were written.
	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != numWriters {
		t.Errorf("expected %d sessions, got %d", numWriters, len(sessions))
	}
}

// TestUpsertAndLoadHostScopedIdentity verifies the path-free identity fields
// (Hostname, GitRef, WorktreeBranch) round-trip through the JSON-encoded
// git_ref column alongside the legacy path-style fields.
func TestUpsertAndLoadHostScopedIdentity(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:             "ses-host-1",
		Backend:        agent.BackendOpenCode,
		Status:         agent.StatusIdle,
		Hostname:       "local",
		GitRef:         agent.GitRef{Kind: agent.GitRefRemote, URL: "git@github.com:acksell/clank.git"},
		WorktreeBranch: "feat/x",
		ProjectDir:     "/tmp/clank",
		ProjectName:    "clank",
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	got := sessions[0]
	if got.Hostname != "local" {
		t.Errorf("Hostname = %q, want local", got.Hostname)
	}
	if got.GitRef != info.GitRef {
		t.Errorf("GitRef = %+v, want %+v", got.GitRef, info.GitRef)
	}
	// Branch round-trips (DB column worktree_branch is bound to info.WorktreeBranch).
	if got.WorktreeBranch != "feat/x" {
		t.Errorf("Branch = %q, want feat/x", got.WorktreeBranch)
	}
}

// TestUpsertDefaultsHostnameToLocal verifies that legacy callers that never
// set Hostname still produce rows with host_id='local' (the migration default
// also covers this for pre-v11 rows).
func TestUpsertDefaultsHostnameToLocal(t *testing.T) {
	t.Parallel()
	s := mustOpen(t, tempDBPath(t))

	now := time.Now().Truncate(time.Millisecond)
	info := agent.SessionInfo{
		ID:          "ses-legacy-1",
		Backend:     agent.BackendOpenCode,
		Status:      agent.StatusIdle,
		ProjectDir:  "/tmp/legacy",
		ProjectName: "legacy",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.UpsertSession(info); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Hostname != "local" {
		t.Fatalf("expected Hostname=local, got %+v", sessions)
	}
}

// TestMigrationV12_SplitsRepoRemoteURLIntoGitRefColumns verifies that
// the v11→v12 migration renames repo_remote_url to git_ref_url, adds
// git_ref_kind / git_ref_path, and backfills kind='remote' for rows
// that had a populated URL. Empty rows stay empty (kind=”) so they get
// re-resolved on next session start instead of being stamped with a
// bogus remote.
func TestMigrationV12_SplitsRepoRemoteURLIntoGitRefColumns(t *testing.T) {
	t.Parallel()
	path := tempDBPath(t)

	// Bootstrap a v11-shaped database directly via the sqlite driver:
	// run the same DDL the historical migrations did, insert two rows
	// (one populated, one empty repo_remote_url), leave user_version=11.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE sessions (
			id              TEXT PRIMARY KEY,
			external_id     TEXT NOT NULL DEFAULT '',
			backend         TEXT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'idle',
			visibility      TEXT NOT NULL DEFAULT '',
			follow_up       INTEGER NOT NULL DEFAULT 0,
			project_dir     TEXT NOT NULL,
			project_name    TEXT NOT NULL,
			prompt          TEXT NOT NULL DEFAULT '',
			title           TEXT NOT NULL DEFAULT '',
			ticket_id       TEXT NOT NULL DEFAULT '',
			agent           TEXT NOT NULL DEFAULT '',
			draft           TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL,
			updated_at      DATETIME NOT NULL,
			last_read_at    DATETIME,
			worktree_branch TEXT NOT NULL DEFAULT '',
			worktree_dir    TEXT NOT NULL DEFAULT '',
			host_id         TEXT NOT NULL DEFAULT 'local',
			repo_remote_url TEXT NOT NULL DEFAULT ''
		);
		PRAGMA user_version = 11;
	`); err != nil {
		t.Fatalf("seed v11 schema: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.Exec(`
		INSERT INTO sessions
			(id, backend, status, project_dir, project_name,
			 created_at, updated_at, host_id, repo_remote_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "ses-old-1", string(agent.BackendOpenCode), string(agent.StatusIdle),
		"/tmp/old", "old", now, now, "local",
		"git@github.com:acksell/clank.git"); err != nil {
		t.Fatalf("seed populated row: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO sessions
			(id, backend, status, project_dir, project_name,
			 created_at, updated_at, host_id, repo_remote_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "ses-old-2", string(agent.BackendOpenCode), string(agent.StatusIdle),
		"/tmp/old2", "old2", now, now, "local", ""); err != nil {
		t.Fatalf("seed empty row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-open via store.Open to apply v12.
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	sessions, err := s.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	byID := map[string]agent.SessionInfo{}
	for _, info := range sessions {
		byID[info.ID] = info
	}
	got1, ok := byID["ses-old-1"]
	if !ok {
		t.Fatal("missing ses-old-1 after migration")
	}
	want1 := agent.GitRef{Kind: agent.GitRefRemote, URL: "git@github.com:acksell/clank.git"}
	if got1.GitRef != want1 {
		t.Errorf("ses-old-1 GitRef = %+v, want %+v", got1.GitRef, want1)
	}
	got2 := byID["ses-old-2"]
	if got2.GitRef != (agent.GitRef{}) {
		t.Errorf("ses-old-2 GitRef = %+v, want zero (empty repo_remote_url stays empty)", got2.GitRef)
	}

	// Verify the underlying schema actually has the discrete columns
	// (catches a future regression where the migration silently no-ops).
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	defer raw.Close()
	cols, err := raw.Query(`SELECT name FROM pragma_table_info('sessions') WHERE name LIKE 'git_ref%' ORDER BY name`)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer cols.Close()
	var names []string
	for cols.Next() {
		var n string
		if err := cols.Scan(&n); err != nil {
			t.Fatalf("scan col name: %v", err)
		}
		names = append(names, n)
	}
	wantCols := []string{"git_ref_kind", "git_ref_path", "git_ref_url"}
	if len(names) != len(wantCols) {
		t.Fatalf("git_ref* columns = %v, want %v", names, wantCols)
	}
	for i, want := range wantCols {
		if names[i] != want {
			t.Errorf("col[%d] = %q, want %q", i, names[i], want)
		}
	}
}
