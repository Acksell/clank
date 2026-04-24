package tui

// Inbox-side glue for the push confirm modal. Lives in its own file
// because pushconfirm.go owns the modal and inbox.go owns the screen
// stack; this file is the seam between the two and the only place
// that knows about both showPushConfirm and pushResultMsg routing.

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/host"
)

// updatePushConfirm routes messages while the push confirm modal is
// open. Three message types matter:
//
//   - pushConfirmCancelledMsg: user dismissed before confirming.
//   - pushResultMsg: the in-flight push completed. Outcomes:
//     success     -> close modal, fire success toast + refresh.
//     auth-error  -> close modal, hand off to the credential modal
//     (single entry point preserved at
//     openCredentialModalForPushAuth).
//     other error -> stay in modal, surface err for [r]etry.
//   - everything else: forwarded to the modal.
func (m *InboxModel) updatePushConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case pushConfirmCancelledMsg:
		m.showPushConfirm = false
		return m, nil

	case pushResultMsg:
		if msg.err != nil {
			var authErr *host.PushAuthRequiredError
			if errors.As(msg.err, &authErr) {
				m.showPushConfirm = false
				m.openCredentialModalForPushAuth(authErr, msg.branch, msg.err)
				return m, nil
			}
			// Non-auth error: keep the modal open so the user can
			// hit [r] without re-pressing [p].
			m.pushConfirm.pushing = false
			m.pushConfirm.err = msg.err
			return m, nil
		}
		m.showPushConfirm = false
		return m, m.handlePushSuccess(msg.branch)

	default:
		cmd := m.pushConfirm.Update(msg)
		return m, cmd
	}
}

// openCredentialModalForPushAuth swaps the screen stack from "push
// confirm or no modal" into the credential-modal state. Centralised
// so the [p]-confirm flow and the legacy "no-confirm" code path share
// exactly the same handoff — including the captured branch/host/ref
// that the credential modal uses to retry.
func (m *InboxModel) openCredentialModalForPushAuth(
	authErr *host.PushAuthRequiredError,
	branch string,
	pushErr error,
) {
	m.credModal = newCredentialModal(
		m.client,
		authErr.Hostname,
		m.sidebar.GitRefForActiveHost(),
		branch,
		authErr.EndpointHost,
		pushErr,
	)
	m.credModal.SetSize(m.width, m.height)
	m.showCredModal = true
}

// handlePushSuccess fires the success toast and schedules a branch
// refresh. Returns a single batched command so callers can return
// it directly. Clears m.err because a successful push supersedes
// any prior surfaced error from the same row.
func (m *InboxModel) handlePushSuccess(branch string) tea.Cmd {
	m.err = nil
	toastCmd := m.toast.Show("Pushed "+branch, toastSuccess)
	return tea.Batch(m.sidebar.loadBranches(), toastCmd)
}
