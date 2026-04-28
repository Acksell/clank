package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
)

// modelResultMsg is dispatched after a SetModel RPC completes. The TUI
// uses it to surface errors and to roll back the optimistic local
// update when the call fails.
type modelResultMsg struct {
	previous string
	target   string
	err      error
}

// applyClaudeModelSelection dispatches the SetModel RPC for Claude
// sessions and applies an optimistic update so the badge reflects the
// new model immediately. modelResultMsg reverts the optimistic write if
// the call fails.
//
// During compose mode (no session yet) the RPC is skipped — the model
// is recorded locally on m.info so launchSession can fold it into the
// StartRequest as the initial backend model. Non-Claude backends and
// "(default)" selections (selectedModel < 0) are no-ops here; the
// caller's existing persistence path handles them.
func (m *SessionViewModel) applyClaudeModelSelection() tea.Cmd {
	if m.backend != agent.BackendClaudeCode {
		return nil
	}
	if m.selectedModel < 0 || m.selectedModel >= len(m.models) {
		return nil
	}
	target := m.models[m.selectedModel].ID

	previous := ""
	if m.info != nil {
		previous = m.info.Model
	}
	if m.info == nil {
		m.info = &agent.SessionInfo{Backend: m.backend}
	}
	m.info.Model = target

	if m.composing || m.sessionID == "" {
		return nil
	}
	client := m.client
	sessionID := m.sessionID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := client.Session(sessionID).SetModel(ctx, target)
		return modelResultMsg{previous: previous, target: target, err: err}
	}
}
