package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSidebar_SectionBreakpoints verifies the breakpoint list adapts to the
// number of entries:
//
//   - 0 entries: [0 (All), 1 (Import), 2 (Settings)]
//   - 1 entry:   [0, 1, 2, 3]
//   - 3 entries: [0, 3, 4, 5]
func TestSidebar_SectionBreakpoints(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		entries int
		want    []int
	}{
		{"no entries", 0, []int{0, 1, 2}},
		{"one entry", 1, []int{0, 1, 2, 3}},
		{"three entries", 3, []int{0, 1, 3, 4, 5}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := SidebarModel{entries: makeEntries(tc.entries)}
			got := m.sectionBreakpoints()
			if !equalInts(got, tc.want) {
				t.Errorf("sectionBreakpoints(%d entries) = %v, want %v", tc.entries, got, tc.want)
			}
		})
	}
}

// TestSidebar_ShiftArrowNavigation exercises shift+up / shift+down via
// handleKey so we cover binding wiring as well as the breakpoint math.
func TestSidebar_ShiftArrowNavigation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		entries    int
		startCur   int
		key        tea.KeyPressMsg
		wantCursor int
	}{
		// 3 entries → breakpoints [0, 1, 3, 4, 5]
		{"3e: shift+down from All -> first entry", 3, 0, shiftDownKey(), 1},
		{"3e: shift+down from first entry -> end of worktrees", 3, 1, shiftDownKey(), 3},
		{"3e: shift+down from middle -> end of worktrees", 3, 2, shiftDownKey(), 3},
		{"3e: shift+down from end of worktrees -> Import", 3, 3, shiftDownKey(), 4},
		{"3e: shift+down from Import -> Settings", 3, 4, shiftDownKey(), 5},
		{"3e: shift+down from Settings clamps", 3, 5, shiftDownKey(), 5},
		{"3e: shift+up from Settings -> Import", 3, 5, shiftUpKey(), 4},
		{"3e: shift+up from Import -> end of worktrees", 3, 4, shiftUpKey(), 3},
		{"3e: shift+up from end of worktrees -> first entry", 3, 3, shiftUpKey(), 1},
		{"3e: shift+up from middle -> first entry", 3, 2, shiftUpKey(), 1},
		{"3e: shift+up from first entry -> All", 3, 1, shiftUpKey(), 0},
		{"3e: shift+up from All clamps", 3, 0, shiftUpKey(), 0},

		// 0 entries → breakpoints [0, 1, 2]
		{"0e: shift+down from All -> Import", 0, 0, shiftDownKey(), 1},
		{"0e: shift+down from Import -> Settings", 0, 1, shiftDownKey(), 2},
		{"0e: shift+up from Settings -> Import", 0, 2, shiftUpKey(), 1},
		{"0e: shift+up from Import -> All", 0, 1, shiftUpKey(), 0},

		// 1 entry → breakpoints [0, 1, 2, 3]
		{"1e: shift+down from All -> entry", 1, 0, shiftDownKey(), 1},
		{"1e: shift+down from entry -> Import", 1, 1, shiftDownKey(), 2},
		{"1e: shift+down from Import -> Settings", 1, 2, shiftDownKey(), 3},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := SidebarModel{
				entries: makeEntries(tc.entries),
				cursor:  tc.startCur,
				focused: true,
				height:  20,
			}
			m.handleKey(tc.key)
			if m.cursor != tc.wantCursor {
				t.Errorf("cursor after key: got %d, want %d", m.cursor, tc.wantCursor)
			}
		})
	}
}

// TestSidebar_ShiftDown_EnsuresVisible checks ensureVisible runs after a
// shift+down jump, by giving the sidebar a tiny height so Settings is
// off-screen from cursor=0 and verifying scroll moves.
func TestSidebar_ShiftDown_EnsuresVisible(t *testing.T) {
	t.Parallel()

	m := SidebarModel{
		entries: makeEntries(20),
		cursor:  0,
		focused: true,
		height:  8, // listHeight clamps small but viewport stays smaller than list
	}
	m.handleKey(shiftDownKey()) // All → first entry (cursor=1)
	m.handleKey(shiftDownKey()) // first entry → last entry (cursor=20)
	if m.cursor != 20 {
		t.Fatalf("cursor: got %d, want 20", m.cursor)
	}
	if m.scroll == 0 {
		t.Errorf("expected scroll to advance from 0 after shift+down jump, got 0")
	}
}

// --- helpers ---

func upKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyUp}
}

func downKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyDown}
}

// TestSidebar_ArrowNavigationWraps verifies that pressing up at the top
// wraps to the bottom (Settings row) and pressing down at the bottom wraps
// to the top (All row).
func TestSidebar_ArrowNavigationWraps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		entries    int
		startCur   int
		key        tea.KeyPressMsg
		wantCursor int
	}{
		// 3 entries → maxIdx = 5 (Settings row)
		{"3e: up from All wraps to Settings", 3, 0, upKey(), 5},
		{"3e: down from Settings wraps to All", 3, 5, downKey(), 0},
		{"3e: up from middle moves up", 3, 2, upKey(), 1},
		{"3e: down from middle moves down", 3, 2, downKey(), 3},

		// 0 entries → maxIdx = 2
		{"0e: up from All wraps to Settings", 0, 0, upKey(), 2},
		{"0e: down from Settings wraps to All", 0, 2, downKey(), 0},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := SidebarModel{
				entries: makeEntries(tc.entries),
				cursor:  tc.startCur,
				focused: true,
				height:  20,
			}
			m.handleKey(tc.key)
			if m.cursor != tc.wantCursor {
				t.Errorf("cursor after key: got %d, want %d", m.cursor, tc.wantCursor)
			}
		})
	}
}

func makeEntries(n int) []worktreeEntry {
	out := make([]worktreeEntry, n)
	for i := 0; i < n; i++ {
		out[i] = worktreeEntry{
			LocalPath: "/repo/" + entryName(i),
			Label:     entryName(i),
		}
	}
	return out
}

func entryName(i int) string {
	return "b" + string(rune('a'+i))
}

func shiftUpKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
}

func shiftDownKey() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
