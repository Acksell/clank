package tui

// inbox_clank.go — integration glue for the Clank (orchestrator) page
// in the right pane of the inbox. Mirrors inbox_settings.go: the page
// is a small bespoke view (clankview.go) and this file owns the
// transitions and message routing that wire it into InboxModel.

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/voice"
)

// clankSnapshotMsg carries the cold-start transcript snapshot fetched
// from the daemon when the Clank page is opened. An error or empty
// snapshot just leaves the page blank — live events keep flowing.
type clankSnapshotMsg struct {
	entries []voice.Entry
	err     error
}

// showClank renders the Clank page in the right pane without shifting
// focus away from the sidebar. Used both for "hover preview" when the
// sidebar cursor lands on the Clank header row and as the default
// landing screen on launch.
func (m *InboxModel) showClank() {
	m.clank.SetSize(m.sessionPaneWidth(), m.height)
	m.screen = screenClank
}

// openClank switches the inbox into the Clank screen and moves focus to
// the page so the user can scroll history immediately. Pressing left
// returns focus to the sidebar.
func (m *InboxModel) openClank() tea.Cmd {
	m.showClank()
	m.clank.SetFocused(true)
	m.setPane(paneSessions)
	return m.fetchClankSnapshotCmd()
}

// closeClank returns from the Clank screen back to the inbox list with
// focus on the sidebar, mirroring closeSettings.
func (m *InboxModel) closeClank() {
	m.screen = screenInbox
	m.setPane(paneSidebar)
	m.clank.SetFocused(false)
}

// fetchClankSnapshotCmd pulls the daemon's in-memory transcript buffer
// so the timeline survives navigation away/back. Daemon restart still
// loses history (in-memory only — see internal/voice/transcript_buffer.go).
func (m *InboxModel) fetchClankSnapshotCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resp, err := m.client.VoiceTranscripts(ctx)
		if err != nil {
			return clankSnapshotMsg{err: err}
		}
		return clankSnapshotMsg{entries: resp.Entries}
	}
}

// updateClank handles messages while the Clank page is active. Mirrors
// updateSettings.
func (m *InboxModel) updateClank(msg tea.Msg) (tea.Model, tea.Cmd) {
	if wMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = wMsg.Width
		m.height = wMsg.Height
		m.sidebar.SetSize(m.sidebarRenderWidth(), m.height)
		m.clank.SetSize(m.sessionPaneWidth(), m.height)
		return m, nil
	}

	switch msg := msg.(type) {
	case branchLoadedMsg, branchWorktreeCreatedMsg:
		cmd := m.sidebar.Update(msg)
		return m, cmd

	case clankSnapshotMsg:
		if msg.err != nil || len(msg.entries) == 0 {
			return m, nil
		}
		m.clank.SetEntries(msg.entries)
		return m, nil

	case clankFocusSidebarMsg:
		m.setPane(paneSidebar)
		m.clank.SetFocused(false)
		return m, nil

	case clankCloseMsg:
		m.closeClank()
		return m, nil
	}

	// Sidebar key routing — same pattern as updateSettings: when the
	// sidebar is focused we forward keys, then drop the Clank screen
	// if the cursor moves off the Clank header (so hovering branches
	// reverts to the inbox view).
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok && m.pane == paneSidebar {
		tm, cmd := m.handleSidebarKey(keyMsg)
		if m.screen == screenClank && !m.sidebar.CursorOnClank() {
			m.screen = screenInbox
			m.clank.SetFocused(false)
		}
		return tm, cmd
	}

	var cmd tea.Cmd
	m.clank, cmd = m.clank.Update(msg)
	return m, cmd
}
