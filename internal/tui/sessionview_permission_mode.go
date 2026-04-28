package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/agent"
)

// permissionModeResultMsg is dispatched after a SetPermissionMode RPC
// completes. The TUI uses it to surface errors and to fold the
// optimistic local update back to the authoritative server value when
// the call fails.
type permissionModeResultMsg struct {
	previous agent.PermissionMode
	target   agent.PermissionMode
	err      error
}

// currentPermissionMode returns the cached mode, defaulting to the
// backend's seed value (acceptEdits) when the session info hasn't been
// hydrated yet. Keeping the default in one place avoids surfacing a
// blank badge on a freshly-opened session before the first refresh.
func (m *SessionViewModel) currentPermissionMode() agent.PermissionMode {
	if m.info != nil && m.info.PermissionMode != "" {
		return m.info.PermissionMode
	}
	return agent.PermissionModeAcceptEdits
}

// cyclePermissionMode advances the active mode through PermissionModeCycle.
// bypassPermissions is gated behind a confirm dialog because it disables
// every safety check; the dialog reuses the standard confirmAction path.
func (m *SessionViewModel) cyclePermissionMode() tea.Cmd {
	next := m.currentPermissionMode().Next()
	if next == agent.PermissionModeBypassPermissions {
		m.showConfirm = true
		m.confirm = newConfirmDialog(
			"Enable bypass permissions?",
			"This disables ALL permission checks for this session.\nThe agent can edit, run, and delete anything without asking.",
			"permission-mode-bypass",
		)
		return nil
	}
	return m.setPermissionMode(next)
}

// setPermissionMode dispatches the RPC and applies an optimistic update
// so the badge reflects the new mode immediately. permissionModeResultMsg
// reverts the optimistic write if the call fails.
//
// During compose mode (no session yet) the RPC is skipped — the mode is
// recorded locally on m.info so launchSession can fold it into the
// StartRequest as the initial backend mode.
func (m *SessionViewModel) setPermissionMode(mode agent.PermissionMode) tea.Cmd {
	previous := m.currentPermissionMode()
	if m.info == nil {
		m.info = &agent.SessionInfo{Backend: m.backend}
	}
	m.info.PermissionMode = mode
	if m.composing || m.sessionID == "" {
		return nil
	}
	client := m.client
	sessionID := m.sessionID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := client.Session(sessionID).SetPermissionMode(ctx, mode)
		return permissionModeResultMsg{previous: previous, target: mode, err: err}
	}
}

// renderPermissionModeBadge returns the styled badge text for the
// current Claude permission mode, or empty for non-Claude backends.
// Bypass is rendered in warningColor to keep the danger visible.
func (m *SessionViewModel) renderPermissionModeBadge() string {
	if m.backend != agent.BackendClaudeCode {
		return ""
	}
	mode := m.currentPermissionMode()
	fg := secondaryColor
	if mode == agent.PermissionModeBypassPermissions {
		fg = warningColor
	}
	return lipgloss.NewStyle().Foreground(fg).Render(mode.DisplayName())
}
