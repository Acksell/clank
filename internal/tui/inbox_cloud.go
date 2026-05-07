package tui

// inbox_cloud.go — integration code wiring the cloud panel into the
// inbox. Mirrors inbox_settings.go: hover-preview via showCloud,
// focused-page via openCloud, exit via closeCloud, and an updateCloud
// dispatcher that delegates to the cloudView model while routing
// sidebar keys to the sidebar.

import (
	tea "charm.land/bubbletea/v2"
)

// showCloud renders the Cloud panel in the right pane without shifting
// focus from the sidebar. Used for hover-preview when the cursor lands
// on the ☁ row.
func (m *InboxModel) showCloud() {
	m.cloud = newCloudView()
	m.cloud.SetSize(m.sessionPaneWidth(), m.height)
	m.screen = screenCloud
}

// openCloud switches the inbox into the Cloud screen and moves focus
// to the panel so the user can start typing immediately. Returns the
// init Cmd so the panel can kick off async work (loading prefs,
// attempting an /me with a saved session).
func (m *InboxModel) openCloud() tea.Cmd {
	m.showCloud()
	m.cloud.SetFocused(true)
	m.setPane(paneSessions)
	return m.cloud.Init()
}

// closeCloud returns from the Cloud screen back to the inbox list.
// Focus goes back to the sidebar so the user lands where they came from.
func (m *InboxModel) closeCloud() {
	m.screen = screenInbox
	m.setPane(paneSidebar)
	m.cloud.SetFocused(false)
}

// updateCloud handles messages while the cloud panel is active.
// Sidebar-focused keys are forwarded to the sidebar so the user can
// navigate up/down between footer rows without first leaving the panel.
func (m *InboxModel) updateCloud(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Resize must always update layout.
	if wMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = wMsg.Width
		m.height = wMsg.Height
		m.sidebar.SetSize(m.sidebarRenderWidth(), m.height)
		m.cloud.SetSize(m.sessionPaneWidth(), m.height)
		return m, nil
	}

	// Sidebar-focused keys: route to sidebar handler. Cursor leaving
	// the ☁ row while the cloud is showing drops back to the inbox
	// (mirrors the settings transition).
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok && m.pane == paneSidebar {
		tm, cmd := m.handleSidebarKey(keyMsg)
		if m.screen == screenCloud && !m.sidebar.CursorOnCloud() {
			m.screen = screenInbox
			m.cloud.SetFocused(false)
		}
		return tm, cmd
	}

	// Otherwise delegate to the cloud view itself.
	var cmd tea.Cmd
	m.cloud, cmd = m.cloud.Update(msg)
	return m, cmd
}
