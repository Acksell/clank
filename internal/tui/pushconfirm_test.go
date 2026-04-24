package tui

// Regression tests for the push confirm modal lifecycle.
//
// What this guards:
//   - [esc] before confirming exits without pushing.
//   - [enter] flips into pushing state and emits a push command.
//   - In-flight state swallows further input (no double-push, no
//     accidental cancel mid-flight).
//   - Non-auth errors keep the modal open and re-pressable via [r].
//   - Auth errors close the modal and hand off to the credential
//     modal — preserving the single credential-modal entry point.
//   - Success closes the modal and emits the toast + branch refresh.

import (
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

func newTestPushConfirm() pushConfirmModel {
	return newPushConfirm(nil, host.HostLocal, agent.GitRef{}, "feat/login")
}

func keyEsc() tea.KeyPressMsg   { return tea.KeyPressMsg{Code: tea.KeyEscape} }
func keyEnter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func keyR() tea.KeyPressMsg     { return tea.KeyPressMsg{Text: "r", Code: 'r'} }

// TestPushConfirm_EscEmitsCancelled — [esc] before confirming must
// surface a pushConfirmCancelledMsg so the inbox can dismiss the modal
// without ever reaching the hub.
func TestPushConfirm_EscEmitsCancelled(t *testing.T) {
	t.Parallel()
	m := newTestPushConfirm()
	cmd := m.Update(keyEsc())
	if cmd == nil {
		t.Fatal("[esc] must emit a command")
	}
	if _, ok := cmd().(pushConfirmCancelledMsg); !ok {
		t.Fatalf("expected pushConfirmCancelledMsg, got %T", cmd())
	}
	if m.pushing {
		t.Fatal("cancel must NOT enter pushing state")
	}
}

// TestPushConfirm_EnterStartsPush — [enter] flips pushing=true. We can't
// run the real push (no client), but we verify the state transition
// and that a non-nil command is returned.
func TestPushConfirm_EnterStartsPush(t *testing.T) {
	t.Parallel()
	m := newTestPushConfirm()
	cmd := m.Update(keyEnter())
	if cmd == nil {
		t.Fatal("[enter] must emit a push command")
	}
	if !m.pushing {
		t.Fatal("[enter] must set pushing=true")
	}
	if m.err != nil {
		t.Fatal("[enter] must clear any prior error")
	}
}

// TestPushConfirm_InFlightSwallowsInput — once a push is in flight no
// key must be able to cancel or restart it. Otherwise [esc] or a
// stray [enter] would race against the hub call.
func TestPushConfirm_InFlightSwallowsInput(t *testing.T) {
	t.Parallel()
	m := newTestPushConfirm()
	m.pushing = true
	if cmd := m.Update(keyEsc()); cmd != nil {
		t.Fatal("[esc] must be a no-op while pushing")
	}
	if cmd := m.Update(keyEnter()); cmd != nil {
		t.Fatal("[enter] must be a no-op while pushing")
	}
	if cmd := m.Update(keyR()); cmd != nil {
		t.Fatal("[r] must be a no-op while pushing")
	}
}

// TestPushConfirm_RetryAfterError — after a non-auth error sets m.err,
// [r] must restart the push. Before the error [r] is unbound (so a
// stray r doesn't fire a push).
func TestPushConfirm_RetryAfterError(t *testing.T) {
	t.Parallel()
	m := newTestPushConfirm()
	if cmd := m.Update(keyR()); cmd != nil {
		t.Fatal("[r] must be unbound when no error is showing")
	}
	m.err = errors.New("rejected: non-fast-forward")
	cmd := m.Update(keyR())
	if cmd == nil {
		t.Fatal("[r] after error must restart the push")
	}
	if !m.pushing {
		t.Fatal("retry must set pushing=true")
	}
	if m.err != nil {
		t.Fatal("retry must clear the prior error")
	}
}

// TestInbox_PushConfirmSuccessClosesAndToasts — feeds a successful
// pushResultMsg through updatePushConfirm and asserts modal closes,
// toast becomes visible, and a branch-refresh batch comes back.
func TestInbox_PushConfirmSuccessClosesAndToasts(t *testing.T) {
	t.Parallel()
	a := &ActiveHost{state: nil, name: host.HostLocal}
	sb := NewSidebarModel(nil, agent.GitRef{}, "/tmp", a)
	m := &InboxModel{showPushConfirm: true, sidebar: sb, activeHost: a}
	model, cmd := m.updatePushConfirm(pushResultMsg{branch: "feat/login"})
	im := model.(*InboxModel)
	if im.showPushConfirm {
		t.Fatal("success must close the push confirm modal")
	}
	if !im.toast.visible {
		t.Fatal("success must surface a toast")
	}
	if cmd == nil {
		t.Fatal("success must return a refresh+toast batch")
	}
}

// TestInbox_PushConfirmAuthErrorOpensCredModal — when the in-flight
// push returns *host.PushAuthRequiredError, the confirm modal must
// close AND the credential modal must take over. This guards the
// single credential-modal entry point.
func TestInbox_PushConfirmAuthErrorOpensCredModal(t *testing.T) {
	t.Parallel()
	a := &ActiveHost{state: nil, name: host.HostLocal}
	sb := NewSidebarModel(nil, agent.GitRef{}, "/tmp", a)
	m := &InboxModel{showPushConfirm: true, sidebar: sb, activeHost: a}
	authErr := &host.PushAuthRequiredError{
		Hostname:     host.HostLocal,
		EndpointHost: "github.com",
		Underlying:   errors.New("auth required"),
	}
	model, _ := m.updatePushConfirm(pushResultMsg{branch: "feat/login", err: authErr})
	im := model.(*InboxModel)
	if im.showPushConfirm {
		t.Fatal("auth error must close push confirm modal")
	}
	if !im.showCredModal {
		t.Fatal("auth error must open credential modal")
	}
}

// TestInbox_PushConfirmNonAuthErrorStaysInModal — generic push errors
// must NOT close the modal; they surface inside it so [r]etry stays
// usable without re-pressing [p].
func TestInbox_PushConfirmNonAuthErrorStaysInModal(t *testing.T) {
	t.Parallel()
	pc := newTestPushConfirm()
	pc.pushing = true
	m := &InboxModel{showPushConfirm: true, pushConfirm: pc}
	bareErr := fmt.Errorf("rejected: non-fast-forward")
	model, _ := m.updatePushConfirm(pushResultMsg{branch: "feat/login", err: bareErr})
	im := model.(*InboxModel)
	if !im.showPushConfirm {
		t.Fatal("non-auth error must keep the modal open")
	}
	if im.pushConfirm.pushing {
		t.Fatal("non-auth error must clear pushing state for [r]etry")
	}
	if im.pushConfirm.err == nil {
		t.Fatal("non-auth error must surface inside the modal")
	}
}

// TestInbox_PushConfirmCancelClosesModal — pushConfirmCancelledMsg
// from the modal must just close it, no side effects.
func TestInbox_PushConfirmCancelClosesModal(t *testing.T) {
	t.Parallel()
	m := &InboxModel{showPushConfirm: true}
	model, cmd := m.updatePushConfirm(pushConfirmCancelledMsg{})
	im := model.(*InboxModel)
	if im.showPushConfirm {
		t.Fatal("cancel must close the modal")
	}
	if cmd != nil {
		t.Fatal("cancel must not emit follow-up commands")
	}
	if im.toast.visible {
		t.Fatal("cancel must NOT toast")
	}
}
