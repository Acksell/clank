package tui

// Tests for sidebar host-section logic and section-aware cursor
// traversal. These tests construct SidebarModel directly and inject
// branches/host rows without going through the daemon — the daemon
// I/O paths (loadBranches, loadHosts) are exercised by integration
// tests in the hub package.

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// newTestSidebar returns a sidebar with no client (safe as long as no
// daemon I/O is triggered) and an in-memory ActiveHost.
func newTestSidebar(t *testing.T) *SidebarModel {
	t.Helper()
	a := &ActiveHost{state: nil, name: host.HostLocal}
	s := NewSidebarModel(nil, agent.GitRef{}, "/tmp", a)
	s.SetFocused(true)
	s.SetSize(40, 30)
	return &s
}

func keyDown() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyDown} }
func keyUp() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyUp} }
func keyN() tea.KeyPressMsg    { return tea.KeyPressMsg{Text: "n", Code: 'n'} }

// TestHostsSection_AppliesLoadedMergesKnownKinds: when /hosts returns
// only "local", KnownHostKinds should fill in "daytona" as a
// disconnected row. Regression: forgetting this merge would leave
// users no way to provision daytona from the TUI.
func TestHostsSection_AppliesLoadedMergesKnownKinds(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)

	s.hosts.applyLoaded([]host.Hostname{host.HostLocal}, nil)

	if got := s.hosts.count(); got != 2 {
		t.Fatalf("count() = %d, want 2 (local + daytona)", got)
	}
	r0, _ := s.hosts.rowAt(0)
	if r0.name != host.HostLocal || !r0.connected {
		t.Errorf("row 0 = %+v, want connected local", r0)
	}
	r1, _ := s.hosts.rowAt(1)
	if r1.name != "daytona" || r1.connected {
		t.Errorf("row 1 = %+v, want disconnected daytona", r1)
	}
	if r1.kind != "daytona" {
		t.Errorf("row 1 kind = %q, want daytona", r1.kind)
	}
}

// TestHostsSection_KnownKindAlreadyConnectedNotDuplicated: once daytona
// has been provisioned, /hosts will include it; KnownHostKinds must
// not add a second row.
func TestHostsSection_KnownKindAlreadyConnectedNotDuplicated(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal, "daytona"}, nil)

	if got := s.hosts.count(); got != 2 {
		t.Fatalf("count() = %d, want 2 (no duplicate daytona)", got)
	}
	r1, _ := s.hosts.rowAt(1)
	if r1.name != "daytona" || !r1.connected {
		t.Errorf("row 1 = %+v, want connected daytona", r1)
	}
}

// TestSidebar_CursorTraversalAcrossSections walks j down through the
// hosts section and into worktrees, then back up with k. The
// worktrees section starts at index hosts.count() — Phase F's most
// likely off-by-one.
func TestSidebar_CursorTraversalAcrossSections(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal}, nil) // 2 host rows
	s.branches = []host.BranchInfo{{Name: "main"}, {Name: "feature"}}
	// Reset cursor to top after seeding.
	s.cursor = 0

	// Section 0: row 0 (local), row 1 (daytona).
	for i, want := range []sidebarSection{sectionHosts, sectionHosts, sectionWorktrees, sectionWorktrees, sectionWorktrees} {
		sec, _ := s.cursorSection()
		if sec != want {
			t.Errorf("step %d cursor=%d section=%v, want %v", i, s.cursor, sec, want)
		}
		_ = s.handleKey(keyDown())
	}

	// Cursor should now be at maxIdx (2 hosts + 1 All + 2 branches - 1 = 4).
	if s.cursor != s.totalRows()-1 {
		t.Errorf("after 5 downs cursor = %d, want %d", s.cursor, s.totalRows()-1)
	}

	// Walk back up. After totalRows-1 ups, should be at 0.
	for range s.totalRows() - 1 {
		_ = s.handleKey(keyUp())
	}
	if s.cursor != 0 {
		t.Errorf("after walk-up cursor = %d, want 0", s.cursor)
	}
}

// TestSidebar_BranchIndexCorrectAcrossHostsOffset: SelectedBranch must
// account for the hosts section taking up the first N cursor slots.
// Off-by-one here would silently apply the wrong branch filter.
func TestSidebar_BranchIndexCorrectAcrossHostsOffset(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal}, nil) // 2 host rows
	s.branches = []host.BranchInfo{{Name: "main"}, {Name: "feature"}}

	// Cursor on "All" (linear index = hosts.count() = 2).
	s.cursor = 2
	if got := s.SelectedBranch(); got != "" {
		t.Errorf("SelectedBranch on All = %q, want empty", got)
	}

	// Cursor on first branch (linear index = 3).
	s.cursor = 3
	if got := s.SelectedBranch(); got != "main" {
		t.Errorf("SelectedBranch index 3 = %q, want main", got)
	}

	// Cursor on second branch.
	s.cursor = 4
	if got := s.SelectedBranch(); got != "feature" {
		t.Errorf("SelectedBranch index 4 = %q, want feature", got)
	}
}

// TestSidebar_CursorOnHostDetectsSection covers the inbox's Enter
// dispatch: Enter on a host row activates the host; Enter on a branch
// row selects it and switches panes. cursorOnHost is the predicate.
func TestSidebar_CursorOnHostDetectsSection(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal, "daytona"}, nil)
	s.branches = []host.BranchInfo{{Name: "main"}}

	cases := []struct {
		cursor int
		want   bool
	}{
		{0, true},  // local
		{1, true},  // daytona
		{2, false}, // All
		{3, false}, // main
	}
	for _, tc := range cases {
		s.cursor = tc.cursor
		if got := s.cursorOnHost(); got != tc.want {
			t.Errorf("cursor=%d cursorOnHost() = %v, want %v", tc.cursor, got, tc.want)
		}
	}
}

// TestSidebar_ActivateSelectedHostUpdatesActiveHost is the integration
// of activateSelectedHost + ActiveHost.Set: pressing Enter on the
// daytona row should flip the active host pointer (and persist when
// the state is non-nil — covered separately in active_host_test.go).
func TestSidebar_ActivateSelectedHostUpdatesActiveHost(t *testing.T) {
	// Cannot use t.Parallel(): t.Setenv below is incompatible with parallel
	// execution per the testing package contract.
	t.Setenv("HOME", t.TempDir())
	a, err := LoadActiveHost()
	if err != nil {
		t.Fatalf("LoadActiveHost: %v", err)
	}
	s := NewSidebarModel(nil, agent.GitRef{}, "/tmp", a)
	s.SetFocused(true)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal, "daytona"}, nil)

	// Cursor on daytona (index 1 — connected).
	s.cursor = 1
	cmd, err := s.activateSelectedHost()
	if err != nil {
		t.Fatalf("activateSelectedHost: %v", err)
	}
	if cmd == nil {
		t.Error("activateSelectedHost on different host returned nil cmd; want loadBranches cmd")
	}
	if a.Name() != "daytona" {
		t.Errorf("active host = %q, want daytona", a.Name())
	}
}

// TestSidebar_ActivateOnDisconnectedKindNoOp: a disconnected kind
// row is not a real host, so activating it would set the active host
// to a name the hub can't route. activateSelectedHost should refuse.
func TestSidebar_ActivateOnDisconnectedKindNoOp(t *testing.T) {
	t.Parallel()
	a := &ActiveHost{state: nil, name: host.HostLocal}
	s := NewSidebarModel(nil, agent.GitRef{}, "/tmp", a)
	s.SetFocused(true)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal}, nil) // daytona will be added as disconnected

	// Cursor on the disconnected daytona row (index 1).
	s.cursor = 1
	if _, err := s.activateSelectedHost(); err != nil {
		t.Fatalf("activateSelectedHost: %v", err)
	}
	if a.Name() != host.HostLocal {
		t.Errorf("active host = %q, want unchanged local", a.Name())
	}
}

// TestSidebar_HostsLoadedPreservesCursorIdentity is a regression test
// for Bug #2: provisioning daytona changed the host row order, but
// the cursor tracked position (index 0) instead of identity ("local"),
// so it silently jumped to whichever host now occupied index 0.
//
// We simulate by placing the cursor on "local" (index 0), then
// dispatching a hostsLoadedMsg with a reordered list where local is
// no longer first. The cursor should follow local, not stay at 0.
func TestSidebar_HostsLoadedPreservesCursorIdentity(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal}, nil)
	s.cursor = 0 // on local

	// Reload with a list where local has been pushed to index 1
	// (mimics what would happen if Hosts() didn't pin local first).
	_ = s.Update(hostsLoadedMsg{hosts: []host.Hostname{"daytona", host.HostLocal}, err: nil})

	row, ok := s.hosts.rowAt(s.cursor)
	if !ok {
		t.Fatalf("cursor=%d out of range after reload (count=%d)", s.cursor, s.hosts.count())
	}
	if row.name != host.HostLocal {
		t.Errorf("cursor moved to %q after reload; want still on %q", row.name, host.HostLocal)
	}
}

// TestSidebar_NewBranchKeyOnlyInWorktreesSection: pressing 'n' while
// the cursor is on a host row must not enter create-branch mode —
// otherwise the user would type a branch name into nowhere.
func TestSidebar_NewBranchKeyOnlyInWorktreesSection(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.hosts.applyLoaded([]host.Hostname{host.HostLocal}, nil)
	s.branches = []host.BranchInfo{{Name: "main"}}

	s.cursor = 0 // on the local host row
	_ = s.handleKey(keyN())
	if s.creating {
		t.Error("creating=true after 'n' on host row; want false")
	}

	s.cursor = s.hosts.count() // on "All" — worktrees section
	_ = s.handleKey(keyN())
	if !s.creating {
		t.Error("creating=false after 'n' on worktrees row; want true")
	}
}

// TestSidebar_GitRefForActiveHostStripsLocalPathOnRemote is the
// regression for Bug #1 ("worktrees created on remote host don't
// appear in sidebar"). Branch ops must not pass the laptop's
// LocalPath when the active host is remote — the host either errors
// ("local_path not usable") or routes to the wrong repo and silently
// returns an empty branch list. Endpoint is the sole remote
// identity and must be preserved untouched.
func TestSidebar_GitRefForActiveHostStripsLocalPathOnRemote(t *testing.T) {
	t.Parallel()
	a := &ActiveHost{state: nil, name: host.HostLocal}
	ref := agent.GitRef{LocalPath: "/Users/axe/proj"}
	s := NewSidebarModel(nil, ref, "/Users/axe/proj", a)

	// Active host = local: LocalPath preserved (host can read fs).
	if got := s.GitRefForActiveHost(); got.LocalPath != "/Users/axe/proj" {
		t.Errorf("local active: LocalPath = %q, want preserved", got.LocalPath)
	}

	// Active host = daytona: LocalPath stripped.
	a.name = "daytona"
	if got := s.GitRefForActiveHost(); got.LocalPath != "" {
		t.Errorf("remote active: LocalPath = %q, want empty", got.LocalPath)
	}
}
