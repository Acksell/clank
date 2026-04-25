package tui

// inbox_settings.go — integration code that wires the settings page and the
// color-scheme picker overlay into the inbox. Kept separate from inbox.go
// so we don't bloat that file further as more settings are added.

import (
	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/config"
)

// showSettings renders the Settings page in the right pane without
// shifting focus away from the sidebar. Used for "hover preview" when
// the sidebar cursor lands on the ⚙ Settings footer row, so the right
// pane reflects the cursor position the same way it does for branches.
func (m *InboxModel) showSettings() {
	prefs, _ := config.LoadPreferences()
	m.settings = newSettingsView(prefs.ColorScheme)
	m.settings.SetSize(m.sessionPaneWidth(), m.height)
	m.screen = screenSettings
}

// openSettings switches the inbox into the Settings screen and moves
// focus to the settings page so the user can start navigating entries
// immediately; pressing left returns focus to the sidebar.
func (m *InboxModel) openSettings() {
	m.showSettings()
	m.settings.SetFocused(true)
	m.pane = paneSessions
	m.sidebar.SetFocused(false)
}

// closeSettings returns from the Settings screen back to the inbox list.
// Focus goes back to the sidebar so the user lands where they came from.
func (m *InboxModel) closeSettings() {
	m.screen = screenInbox
	m.pane = paneSidebar
	m.sidebar.SetFocused(true)
	m.settings.SetFocused(false)
}

// updateSettings handles messages while the settings page is active.
// Overlay messages (theme picker) are routed here too when the overlay
// is showing, so the picker can intercept keys before the page sees them.
func (m *InboxModel) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Theme picker overlay takes precedence: eat all keys while it's open.
	if m.showThemePicker {
		return m.updateThemePicker(msg)
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.sidebar.SetSize(m.sidebarRenderWidth(), m.height)
		m.settings.SetSize(m.sessionPaneWidth(), m.height)
		return m, nil

	case branchLoadedMsg, branchWorktreeCreatedMsg:
		cmd := m.sidebar.Update(msg)
		return m, cmd

	case settingsActivatedMsg:
		switch msg.kind {
		case settingsEntryColorScheme:
			prefs, _ := config.LoadPreferences()
			m.themePicker = newThemePicker(prefs.ColorScheme)
			m.showThemePicker = true
			return m, m.themePicker.Init()
		}
		return m, nil

	case settingsFocusSidebarMsg:
		// Move focus to the sidebar without leaving the settings screen,
		// so the user can scroll branches and come back to settings.
		m.pane = paneSidebar
		m.sidebar.SetFocused(true)
		m.settings.SetFocused(false)
		return m, nil

	case settingsCloseMsg:
		m.closeSettings()
		return m, nil
	}

	// If a key press arrives while the sidebar is focused, route it to
	// the sidebar handler. This is how the user navigates back from the
	// settings page (they pressed left to focus the sidebar, then can
	// move the cursor or press right to re-enter the settings page).
	//
	// If the sidebar cursor moves off the "⚙ Settings" row while the
	// settings screen is still showing, drop back to the inbox list —
	// otherwise the page lingers behind a hovered branch and the user
	// has to jump into the right pane and press esc to dismiss it.
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok && m.pane == paneSidebar {
		tm, cmd := m.handleSidebarKey(keyMsg)
		if m.screen == screenSettings && !m.sidebar.CursorOnSettings() {
			m.screen = screenInbox
			m.settings.SetFocused(false)
		}
		return tm, cmd
	}

	// Otherwise delegate to the settings view itself.
	var cmd tea.Cmd
	m.settings, cmd = m.settings.Update(msg)
	return m, cmd
}

// updateThemePicker forwards messages to the picker overlay and handles
// its result / cancel messages.
func (m *InboxModel) updateThemePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case themePickerResultMsg:
		m.showThemePicker = false
		// The scheme is already applied (live preview); persist the name.
		go persistColorScheme(msg.scheme)
		m.settings.SetColorSchemeValue(msg.scheme)
		return m, nil

	case themePickerCancelMsg:
		// Picker has already reverted the palette; just hide.
		m.showThemePicker = false
		return m, nil
	}

	var cmd tea.Cmd
	m.themePicker, cmd = m.themePicker.Update(msg)
	return m, cmd
}

// persistColorScheme writes the user's chosen scheme to preferences.json.
// Runs in a goroutine — scheme selection should feel instant even if
// disk I/O is slow. We drop errors silently: the palette is already
// applied in-memory, and a failed write only means the scheme won't
// persist across restarts (surfaced to the user indirectly on next launch).
func persistColorScheme(name string) {
	prefs, _ := config.LoadPreferences()
	prefs.ColorScheme = name
	_ = config.SavePreferences(prefs)
}
