package hub_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	hubclient "github.com/acksell/clank/internal/hub/client"
	"github.com/acksell/clank/internal/hub"
	"github.com/acksell/clank/internal/store"
)

func TestPersistence_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)

	// --- Phase 1: create sessions and mutate user-owned fields ---

	d1, client1, sockPath, pidPath, dbPath, cleanup1 := testDaemonWithStore(t, dir)
	_ = d1
	ctx := context.Background()

	// Create a session.
	info, err := client1.CreateSession(ctx, agent.StartRequest{
		Backend:    agent.BackendOpenCode,
		ProjectDir: "/tmp/myproject",
		Prompt:     "fix the bug",
		TicketID:   "TICKET-42",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Wait for backend to start.
	time.Sleep(150 * time.Millisecond)

	// Mutate user-owned fields.
	if _, err := client1.ToggleFollowUp(ctx, info.ID); err != nil {
		t.Fatalf("ToggleFollowUp: %v", err)
	}
	if err := client1.SetVisibility(ctx, info.ID, agent.VisibilityDone); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}
	if err := client1.SetDraft(ctx, info.ID, "my draft text"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}
	if err := client1.MarkSessionRead(ctx, info.ID); err != nil {
		t.Fatalf("MarkSessionRead: %v", err)
	}

	// Snapshot the session before stopping.
	before, err := client1.GetSession(ctx, info.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}

	// Stop daemon 1.
	cleanup1()

	// --- Phase 2: restart daemon with same DB, verify persistence ---

	// Need to remove stale socket before new daemon can listen.
	os.Remove(sockPath)
	os.Remove(pidPath)

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open (phase 2): %v", err)
	}
	d2 := hub.NewWithPaths(sockPath, pidPath)
	d2.Store = st2
	mgr2 := newMockBackendManager()
	d2.BackendManagers[agent.BackendOpenCode] = mgr2
	d2.BackendManagers[agent.BackendClaudeCode] = mgr2

	errCh2 := make(chan error, 1)
	go func() { errCh2 <- d2.Run() }()

	client2 := hubclient.NewClient(sockPath)
	waitForDaemon(t, client2)

	defer func() {
		d2.Stop()
		select {
		case <-errCh2:
		case <-time.After(5 * time.Second):
			t.Error("daemon 2 did not stop in time")
		}
	}()

	// The session should survive the restart.
	sessions, err := client2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after restart, got %d", len(sessions))
	}

	after := sessions[0]

	// Verify identity.
	if after.ID != before.ID {
		t.Errorf("ID mismatch: %s vs %s", after.ID, before.ID)
	}

	// Verify user-owned fields survived.
	if !after.FollowUp {
		t.Error("FollowUp should be true after restart")
	}
	if after.Visibility != agent.VisibilityDone {
		t.Errorf("Visibility = %q, want %q", after.Visibility, agent.VisibilityDone)
	}
	if after.Draft != "my draft text" {
		t.Errorf("Draft = %q, want %q", after.Draft, "my draft text")
	}
	if after.LastReadAt.IsZero() {
		t.Error("LastReadAt should not be zero")
	}

	// Verify backend-owned fields survived.
	if after.ProjectDir != "/tmp/myproject" {
		t.Errorf("ProjectDir = %q, want %q", after.ProjectDir, "/tmp/myproject")
	}
	if after.TicketID != "TICKET-42" {
		t.Errorf("TicketID = %q, want %q", after.TicketID, "TICKET-42")
	}
}

func TestPersistence_DeleteSurvivesRestart(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)

	// Phase 1: create and delete a session.
	_, client1, sockPath, pidPath, dbPath, cleanup1 := testDaemonWithStore(t, dir)
	ctx := context.Background()

	info, err := client1.CreateSession(ctx, agent.StartRequest{
		Backend:    agent.BackendOpenCode,
		ProjectDir: "/tmp/proj",
		Prompt:     "hello",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	if err := client1.DeleteSession(ctx, info.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	cleanup1()

	// Phase 2: restart, verify session is gone.
	os.Remove(sockPath)
	os.Remove(pidPath)

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	d2 := hub.NewWithPaths(sockPath, pidPath)
	d2.Store = st2

	errCh := make(chan error, 1)
	go func() { errCh <- d2.Run() }()

	client2 := hubclient.NewClient(sockPath)
	waitForDaemon(t, client2)

	defer func() {
		d2.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
		st2.Close()
	}()

	sessions, err := client2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after delete+restart, got %d", len(sessions))
	}
}

// TestPersistence_StaleBusyStatusNormalizedOnRestart verifies that sessions
// persisted with a busy or starting status are normalized to idle when the
// daemon restarts. Without this fix, the inbox shows an infinite spinner for
// sessions that were interrupted by a daemon restart.
func TestPersistence_StaleBusyStatusNormalizedOnRestart(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)

	// Phase 1: create a session and leave it in a busy state.
	d1, client1, sockPath, pidPath, dbPath, cleanup1 := testDaemonWithStore(t, dir)
	_ = d1
	ctx := context.Background()

	info, err := client1.CreateSession(ctx, agent.StartRequest{
		Backend:    agent.BackendOpenCode,
		ProjectDir: "/tmp/stale-proj",
		Prompt:     "do something",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Verify the session is busy (mockBackend transitions to busy on Start).
	session, err := client1.GetSession(ctx, info.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if session.Status != agent.StatusBusy {
		t.Fatalf("expected status=busy before shutdown, got %s", session.Status)
	}

	// Kill daemon without letting the backend transition to idle.
	cleanup1()

	// Phase 2: restart daemon — the session should be normalized to idle.
	os.Remove(sockPath)
	os.Remove(pidPath)

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	d2 := hub.NewWithPaths(sockPath, pidPath)
	d2.Store = st2
	mgr2 := newMockBackendManager()
	d2.BackendManagers[agent.BackendOpenCode] = mgr2
	d2.BackendManagers[agent.BackendClaudeCode] = mgr2

	errCh := make(chan error, 1)
	go func() { errCh <- d2.Run() }()

	client2 := hubclient.NewClient(sockPath)
	waitForDaemon(t, client2)

	defer func() {
		d2.Stop()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
		st2.Close()
	}()

	sessions, err := client2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after restart, got %d", len(sessions))
	}
	if sessions[0].Status != agent.StatusIdle {
		t.Errorf("expected status=idle after restart, got %s (stale busy status was not normalized)", sessions[0].Status)
	}
}

// TestDiscoverSessions_NormalizesStaleStatusOnRediscover verifies that
// rediscovery normalizes stale busy/starting statuses for backend-less
// sessions.
func TestDiscoverSessions_NormalizesStaleStatusOnRediscover(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	now := time.Now()

	snapshots := []agent.SessionSnapshot{
		{
			ID:        "ext-stale-1",
			Title:     "Stale session",
			Directory: "/tmp/stale-project",
			CreatedAt: now.Add(-1 * time.Hour),
			UpdatedAt: now,
		},
	}

	// Phase 1: create daemon with store, discover session, then
	// manually corrupt the status by writing busy to the DB.
	sockPath := filepath.Join(dir, "test.sock")
	pidPath := filepath.Join(dir, "test.pid")
	dbPath := filepath.Join(dir, "test.db")

	st1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	d1 := hub.NewWithPaths(sockPath, pidPath)
	d1.Store = st1
	discMgr1 := &mockDiscovererManager{snapshots: snapshots}
	d1.BackendManagers[agent.BackendOpenCode] = discMgr1
	d1.BackendManagers[agent.BackendClaudeCode] = &discMgr1.mockBackendManager

	errCh1 := make(chan error, 1)
	go func() { errCh1 <- d1.Run() }()
	client1 := hubclient.NewClient(sockPath)
	waitForDaemon(t, client1)

	ctx := context.Background()
	if err := client1.DiscoverSessions(ctx, "/tmp/stale-project"); err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}

	sessions1, err := client1.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions1) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions1))
	}
	if sessions1[0].Status != agent.StatusIdle {
		t.Fatalf("expected idle after discover, got %s", sessions1[0].Status)
	}

	// Corrupt the persisted status to simulate a stale busy state
	// (as if the daemon had been killed while the session was active).
	corruptedInfo := sessions1[0]
	corruptedInfo.Status = agent.StatusBusy
	if err := st1.UpsertSession(corruptedInfo); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}

	d1.Stop()
	<-errCh1
	st1.Close()

	// Phase 2: restart and re-discover. The stale busy status
	// should be normalized to idle.
	os.Remove(sockPath)
	os.Remove(pidPath)

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	d2 := hub.NewWithPaths(sockPath, pidPath)
	d2.Store = st2
	discMgr2 := &mockDiscovererManager{snapshots: snapshots}
	d2.BackendManagers[agent.BackendOpenCode] = discMgr2
	d2.BackendManagers[agent.BackendClaudeCode] = &discMgr2.mockBackendManager

	errCh2 := make(chan error, 1)
	go func() { errCh2 <- d2.Run() }()
	client2 := hubclient.NewClient(sockPath)
	waitForDaemon(t, client2)

	defer func() {
		d2.Stop()
		select {
		case <-errCh2:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
		st2.Close()
	}()

	// After restart, the session loaded from DB should already be idle
	// (normalized on load).
	sessions2, err := client2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions (after restart): %v", err)
	}
	if len(sessions2) != 1 {
		t.Fatalf("expected 1 session after restart, got %d", len(sessions2))
	}
	if sessions2[0].Status != agent.StatusIdle {
		t.Errorf("expected status=idle after restart, got %s", sessions2[0].Status)
	}

	// Re-discover — should also not revert to stale status.
	if err := client2.DiscoverSessions(ctx, "/tmp/stale-project"); err != nil {
		t.Fatalf("DiscoverSessions (phase 2): %v", err)
	}

	sessions3, err := client2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions (after re-discover): %v", err)
	}
	if len(sessions3) != 1 {
		t.Fatalf("expected 1 session after re-discover, got %d", len(sessions3))
	}
	if sessions3[0].Status != agent.StatusIdle {
		t.Errorf("expected status=idle after re-discover, got %s (rediscovery did not normalize stale status)", sessions3[0].Status)
	}
}

func TestPersistence_NilStoreDoesNotPanic(t *testing.T) {
	t.Parallel()

	// The standard testDaemon helper does NOT set a store.
	// This test verifies the nil-safe path doesn't panic.
	_, client, cleanup := testDaemon(t)
	defer cleanup()

	ctx := context.Background()
	info, err := client.CreateSession(ctx, agent.StartRequest{
		Backend:    agent.BackendOpenCode,
		ProjectDir: "/tmp/proj",
		Prompt:     "hello",
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Mutate through all persist paths — none should panic.
	_, _ = client.ToggleFollowUp(ctx, info.ID)
	_ = client.SetVisibility(ctx, info.ID, agent.VisibilityArchived)
	_ = client.SetDraft(ctx, info.ID, "draft")
	_ = client.MarkSessionRead(ctx, info.ID)
	_ = client.SendMessage(ctx, info.ID, agent.SendMessageOpts{Text: "msg"})
	time.Sleep(100 * time.Millisecond)
	_ = client.DeleteSession(ctx, info.ID)
}

func TestPersistence_DiscoverMergePreservesUserFields(t *testing.T) {
	t.Parallel()

	dir := shortTempDir(t)
	now := time.Now()

	// Phase 1: discover a session and set user-owned fields.
	snapshots := []agent.SessionSnapshot{
		{
			ID:        "ext-merge-1",
			Title:     "Original title",
			Directory: "/tmp/merge-project",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now.Add(-1 * time.Hour),
		},
	}

	d1, client1, sockPath, pidPath, dbPath, cleanup1 := testDaemonWithStore(t, dir)
	discMgr1 := &mockDiscovererManager{snapshots: snapshots}
	d1.BackendManagers[agent.BackendOpenCode] = discMgr1
	ctx := context.Background()

	if err := client1.DiscoverSessions(ctx, "/tmp/merge-project"); err != nil {
		t.Fatalf("DiscoverSessions: %v", err)
	}

	sessions, err := client1.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	sessionID := sessions[0].ID

	// Set user-owned fields on the discovered session.
	if _, err := client1.ToggleFollowUp(ctx, sessionID); err != nil {
		t.Fatalf("ToggleFollowUp: %v", err)
	}
	if err := client1.SetVisibility(ctx, sessionID, agent.VisibilityDone); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}
	if err := client1.SetDraft(ctx, sessionID, "my followup draft"); err != nil {
		t.Fatalf("SetDraft: %v", err)
	}

	cleanup1()

	// Phase 2: restart daemon, re-discover with updated backend fields.
	os.Remove(sockPath)
	os.Remove(pidPath)

	updatedSnapshots := []agent.SessionSnapshot{
		{
			ID:        "ext-merge-1",                // same external ID
			Title:     "Updated title from backend", // backend changed the title
			Directory: "/tmp/merge-project",
			CreatedAt: now.Add(-2 * time.Hour),
			UpdatedAt: now, // backend has a newer UpdatedAt
		},
	}

	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	d2 := hub.NewWithPaths(sockPath, pidPath)
	d2.Store = st2
	discMgr2 := &mockDiscovererManager{snapshots: updatedSnapshots}
	d2.BackendManagers[agent.BackendOpenCode] = discMgr2
	d2.BackendManagers[agent.BackendClaudeCode] = &discMgr2.mockBackendManager

	errCh2 := make(chan error, 1)
	go func() { errCh2 <- d2.Run() }()

	client2 := hubclient.NewClient(sockPath)
	waitForDaemon(t, client2)
	defer func() {
		d2.Stop()
		select {
		case <-errCh2:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop in time")
		}
	}()

	// Re-discover — the session should be a duplicate (already loaded from DB).
	if err := client2.DiscoverSessions(ctx, "/tmp/merge-project"); err != nil {
		t.Fatalf("DiscoverSessions (phase 2): %v", err)
	}

	sessions2, err := client2.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions2) != 1 {
		t.Fatalf("expected 1 session after re-discover, got %d", len(sessions2))
	}

	merged := sessions2[0]

	// Backend-owned fields should be updated from the new snapshot.
	if merged.Title != "Updated title from backend" {
		t.Errorf("Title = %q, want %q", merged.Title, "Updated title from backend")
	}

	// User-owned fields should be preserved from the DB.
	if !merged.FollowUp {
		t.Error("FollowUp should be true (preserved from DB)")
	}
	if merged.Visibility != agent.VisibilityDone {
		t.Errorf("Visibility = %q, want %q", merged.Visibility, agent.VisibilityDone)
	}
	if merged.Draft != "my followup draft" {
		t.Errorf("Draft = %q, want %q", merged.Draft, "my followup draft")
	}
}
