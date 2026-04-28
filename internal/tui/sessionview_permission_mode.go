package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/config"
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
// Tab is intentionally allowed to land on bypassPermissions without a
// confirmation: the warning is gated at send-time instead (see
// SessionViewModel.commitSend), since merely selecting bypass has no
// observable effect until the next prompt is sent.
func (m *SessionViewModel) cyclePermissionMode() tea.Cmd {
	return m.setPermissionMode(m.currentPermissionMode().Next())
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

// workspacePath returns the canonical workspace directory for this
// session — used as the key for per-workspace bypass acknowledgement.
// SessionInfo.GitRef.LocalPath is preferred because it reflects the
// session's actual working directory; m.projectDir (the inbox's launch
// cwd) is a fallback for compose mode and other code paths that
// haven't loaded SessionInfo yet.
func (m *SessionViewModel) workspacePath() string {
	if m.info != nil && m.info.GitRef.LocalPath != "" {
		return m.info.GitRef.LocalPath
	}
	return m.projectDir
}

// commitSend dispatches a user prompt, appending it to the visible
// transcript and clearing the input. When bypass-permissions is the
// active Claude mode and the user has not yet acknowledged the warning
// for this workspace, the prompt is stashed and a one-time confirmation
// dialog is shown instead. handleConfirmAction("send-bypass") resumes
// the send after the user accepts.
func (m *SessionViewModel) commitSend(text string) tea.Cmd {
	workspace := m.workspacePath()
	if m.backend == agent.BackendClaudeCode &&
		m.currentPermissionMode() == agent.PermissionModeBypassPermissions &&
		workspace != "" {
		prefs, _ := config.LoadPreferences()
		if !prefs.IsBypassPermissionsConfirmed(workspace) {
			m.pendingSendText = text
			m.showConfirm = true
			m.confirm = newConfirmDialog(
				"Bypass all permissions?",
				fmt.Sprintf(
					"Claude will read, edit, and run commands without asking — including potentially destructive ones.\nUse only in disposable or isolated environments.\n\n%s\n\nYou won't be asked again for this workspace.",
					workspace,
				),
				"send-bypass",
			)
			return nil
		}
	}
	return m.dispatchSend(text)
}

// dispatchSend performs the actual transcript append + RPC send. Split
// from commitSend so the bypass confirm dialog can resume the send via
// handleConfirmAction without duplicating the bookkeeping.
func (m *SessionViewModel) dispatchSend(text string) tea.Cmd {
	agentName := ""
	if len(m.agents) > 0 {
		agentName = m.agents[m.selectedAgent].Name
	}
	if m.info != nil {
		m.info.RevertMessageID = ""
	}
	m.entries = append(m.entries, displayEntry{
		kind:    entryUser,
		content: text,
		agent:   agentName,
	})
	m.input.Reset()
	m.inputActive = false
	m.input.Blur()
	m.follow = true
	m.submitting = true
	return m.sendMessage(text)
}
