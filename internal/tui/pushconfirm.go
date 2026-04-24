package tui

// Push confirmation modal — owns the in-flight push so the UI has a
// single, obvious place to show "we're pushing", "we hit an error,
// retry?", or to absorb [esc] without losing the user's intent.
//
// Lifecycle:
//
//   [p] in inbox  -> opens this modal (push has NOT started yet)
//   [enter]       -> dispatches pushBranchCmd, sets pushing=true
//   pushResultMsg -> intercepted by inbox.updatePushConfirm:
//                      - success      -> close modal, fire toast
//                      - auth-error   -> close modal, open cred modal
//                                        (existing single entry point)
//                      - other error  -> stays in modal as m.err so the
//                                        user can [r]etry without
//                                        re-pressing [p]
//   [esc]         -> emits pushConfirmCancelledMsg (only when not
//                    pushing — cancelling an in-flight push is not
//                    safe; the hub call is uninterruptible from here)
//
// The modal does NOT touch credentials. Auth handling stays in the
// hub-owned credential modal so a future self-hosted hub can swap its
// credential UI without touching the push flow.

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hubclient "github.com/acksell/clank/internal/hub/client"
)

// pushConfirmCancelledMsg signals the user dismissed the modal without
// initiating a push. Pushing-already-in-flight is NOT cancellable so
// this never fires while the request is outstanding.
type pushConfirmCancelledMsg struct{}

type pushConfirmModel struct {
	client   *hubclient.Client
	hostname host.Hostname
	gitRef   agent.GitRef
	branch   string

	// pushing is true between [enter] and the corresponding
	// pushResultMsg arrival. While true the modal swallows all key
	// input — there is no abort path for an in-flight hub call.
	pushing bool
	// err is the last non-auth push error. Auth errors close the
	// modal and hand off to the credential modal, so they never end
	// up here. When set the modal renders an [r]etry hint.
	err error

	width  int
	height int
}

func newPushConfirm(
	client *hubclient.Client,
	hostname host.Hostname,
	gitRef agent.GitRef,
	branch string,
) pushConfirmModel {
	return pushConfirmModel{
		client:   client,
		hostname: hostname,
		gitRef:   gitRef,
		branch:   branch,
	}
}

func (m *pushConfirmModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

func (m *pushConfirmModel) overlayInnerWidth() int {
	w := 56
	if m.width > 0 && m.width < w+6 {
		w = m.width - 6
	}
	if w < 20 {
		w = 20
	}
	return w
}

// startPush dispatches the push and flips into the in-flight state.
// Centralised so [enter] and [r]etry stay in lock-step.
func (m *pushConfirmModel) startPush() tea.Cmd {
	m.pushing = true
	m.err = nil
	return pushBranchCmd(m.client, m.hostname, m.gitRef, m.branch)
}

func (m *pushConfirmModel) Update(msg tea.Msg) tea.Cmd {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	if m.pushing {
		// In-flight hub call — swallow input rather than queueing
		// commands that race against the result.
		return nil
	}
	keyMsg = normalizeKeyCase(keyMsg)
	switch {
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("esc"))):
		return func() tea.Msg { return pushConfirmCancelledMsg{} }
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("enter"))):
		return m.startPush()
	case m.err != nil && key.Matches(keyMsg, key.NewBinding(key.WithKeys("r"))):
		return m.startPush()
	}
	return nil
}

func (m *pushConfirmModel) View() string {
	innerW := m.overlayInnerWidth()
	var sb strings.Builder

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(primaryColor).
		Width(innerW).
		Render("Push branch?")
	sb.WriteString(title)
	sb.WriteString("\n")

	sep := lipgloss.NewStyle().Foreground(mutedColor).
		Render(strings.Repeat("─", innerW))
	sb.WriteString(sep)
	sb.WriteString("\n\n")

	branchLabel := lipgloss.NewStyle().Foreground(dimColor).Render("Branch: ")
	branchName := lipgloss.NewStyle().Foreground(secondaryColor).Bold(true).Render(m.branch)
	sb.WriteString(branchLabel + branchName)
	sb.WriteString("\n")

	hostLabel := lipgloss.NewStyle().Foreground(dimColor).Render("Host:   ")
	hostName := lipgloss.NewStyle().Foreground(textColor).Render(string(m.hostname))
	sb.WriteString(hostLabel + hostName)
	sb.WriteString("\n\n")

	switch {
	case m.pushing:
		status := lipgloss.NewStyle().Foreground(warningColor).Bold(true).
			Render("Pushing...")
		sb.WriteString(status)
	case m.err != nil:
		errLine := lipgloss.NewStyle().Foreground(dangerColor).
			Render(truncateForModal(m.err.Error(), innerW))
		sb.WriteString(errLine)
		sb.WriteString("\n\n")
		hint := lipgloss.NewStyle().Foreground(dimColor).
			Render("r: retry   esc: close")
		sb.WriteString(hint)
	default:
		hint := lipgloss.NewStyle().Foreground(dimColor).
			Render("enter: push   esc: cancel")
		sb.WriteString(hint)
	}

	popup := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(primaryColor).
		Padding(1, 2).
		Render(sb.String())
	return popup
}
