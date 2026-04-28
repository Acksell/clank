package tui

import (
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/config"
)

// TestCyclePermissionMode_BypassNoLongerOpensDialog asserts that Tab
// cycling through to bypassPermissions does NOT pop the confirm
// dialog any more — the warning has moved to send-time.
func TestCyclePermissionMode_BypassNoLongerOpensDialog(t *testing.T) {
	t.Parallel()

	m := newTestSessionModel(nil)
	m.backend = agent.BackendClaudeCode
	m.info = &agent.SessionInfo{PermissionMode: agent.PermissionModePlan} // Next() = bypass

	cmd := m.cyclePermissionMode()

	if m.showConfirm {
		t.Error("cyclePermissionMode to bypass must not open a confirm dialog")
	}
	if m.info.PermissionMode != agent.PermissionModeBypassPermissions {
		t.Errorf("PermissionMode = %q, want %q", m.info.PermissionMode, agent.PermissionModeBypassPermissions)
	}
	// Compose-mode model has no live session; cmd is nil because there's
	// no RPC to dispatch. We only care that no dialog opened.
	_ = cmd
}

// TestCommitSend_BypassUsesGitRefLocalPath asserts the live-session
// path: NewSessionViewModel does not set m.projectDir (only the
// compose constructor does), so the dialog must key off
// SessionInfo.GitRef.LocalPath instead. Without this fallback, every
// session opened from the inbox silently skipped the bypass warning.
func TestCommitSend_BypassUsesGitRefLocalPath(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	m := newTestSessionModel(nil)
	m.backend = agent.BackendClaudeCode
	m.projectDir = "" // simulate inbox-opened live session
	m.info = &agent.SessionInfo{
		PermissionMode: agent.PermissionModeBypassPermissions,
		GitRef:         agent.GitRef{LocalPath: "/tmp/live-session-workspace"},
	}

	cmd := m.commitSend("hello")

	if cmd != nil {
		t.Error("commitSend must defer when bypass needs confirmation, even with empty projectDir")
	}
	if !m.showConfirm {
		t.Fatal("commitSend must open the confirm dialog using GitRef.LocalPath as the workspace key")
	}
	if m.pendingSendText != "hello" {
		t.Errorf("pendingSendText = %q, want %q", m.pendingSendText, "hello")
	}
}

// TestCommitSend_BypassShowsConfirmFirstTime verifies the one-time
// warning fires when sending a prompt with bypass active in a workspace
// the user hasn't acknowledged yet.
func TestCommitSend_BypassShowsConfirmFirstTime(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	m := newTestSessionModel(nil)
	m.backend = agent.BackendClaudeCode
	m.projectDir = "/tmp/some-project"
	m.info = &agent.SessionInfo{PermissionMode: agent.PermissionModeBypassPermissions}

	cmd := m.commitSend("hello")

	if cmd != nil {
		t.Error("commitSend must defer the send (return nil cmd) when bypass needs confirmation")
	}
	if !m.showConfirm {
		t.Fatal("commitSend must open the confirm dialog on first bypass send")
	}
	if m.confirm.action != "send-bypass" {
		t.Errorf("confirm action = %q, want %q", m.confirm.action, "send-bypass")
	}
	if m.pendingSendText != "hello" {
		t.Errorf("pendingSendText = %q, want %q", m.pendingSendText, "hello")
	}
	if m.submitting {
		t.Error("submitting must remain false until the dialog resolves")
	}
}

// TestCommitSend_BypassSkipsConfirmAfterAck verifies the dialog does
// not re-open for the same workspace once the user has acknowledged.
func TestCommitSend_BypassSkipsConfirmAfterAck(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	const dir = "/tmp/already-acked"
	if err := config.MarkBypassPermissionsConfirmed(dir); err != nil {
		t.Fatalf("MarkBypassPermissionsConfirmed: %v", err)
	}

	m := newTestSessionModel(nil)
	m.backend = agent.BackendClaudeCode
	m.projectDir = dir
	m.info = &agent.SessionInfo{PermissionMode: agent.PermissionModeBypassPermissions}
	m.inputActive = true
	m.input.Focus()

	cmd := m.commitSend("hello")

	if m.showConfirm {
		t.Error("commitSend must not open the confirm dialog for an acknowledged workspace")
	}
	if cmd == nil {
		t.Error("commitSend must dispatch immediately for an acknowledged workspace")
	}
	if !m.submitting {
		t.Error("submitting must be set when the send dispatches inline")
	}
}

// TestHandleConfirmAction_SendBypassPersistsAck verifies that confirming
// the bypass dialog records the workspace as acknowledged so subsequent
// sends in the same workspace skip the prompt.
func TestHandleConfirmAction_SendBypassPersistsAck(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	const dir = "/tmp/confirm-once"
	m := newTestSessionModel(nil)
	m.backend = agent.BackendClaudeCode
	m.projectDir = dir
	m.info = &agent.SessionInfo{PermissionMode: agent.PermissionModeBypassPermissions}
	m.pendingSendText = "queued"

	cmd := m.handleConfirmAction("send-bypass")
	if cmd == nil {
		t.Fatal("handleConfirmAction(send-bypass) must dispatch the queued send")
	}
	if m.pendingSendText != "" {
		t.Errorf("pendingSendText = %q, want cleared", m.pendingSendText)
	}

	prefs, err := config.LoadPreferences()
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if !prefs.IsBypassPermissionsConfirmed(dir) {
		t.Errorf("workspace %q not recorded as confirmed", dir)
	}
}

// TestConfirmCancel_ClearsPendingSend asserts that cancelling the
// dialog drops the deferred prompt instead of leaking it into a later
// confirm flow (e.g. if the user dismisses then triggers another
// unrelated dialog).
func TestConfirmCancel_ClearsPendingSend(t *testing.T) {
	t.Parallel()

	m := newTestSessionModel(nil)
	m.showConfirm = true
	m.pendingSendText = "should be dropped"

	model, _ := m.Update(confirmResultMsg{confirmed: false, action: "send-bypass"})
	m = model.(*SessionViewModel)

	if m.pendingSendText != "" {
		t.Errorf("pendingSendText = %q, want cleared after cancel", m.pendingSendText)
	}
	if m.showConfirm {
		t.Error("showConfirm must be cleared after cancel")
	}
}

// TestLaunchSession_BypassDefersBehindConfirm verifies that launching a
// new compose-mode session with bypass-permissions selected stashes the
// StartRequest behind the warning dialog instead of dispatching it.
// Regression: the original gate only covered live-session sends, so a
// brand-new session opened with bypass selected skipped the warning.
func TestLaunchSession_BypassDefersBehindConfirm(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	m := newTestSessionModel(nil)
	m.composing = true
	m.backend = agent.BackendClaudeCode
	m.projectDir = "/tmp/launch-bypass-workspace"
	m.info = &agent.SessionInfo{PermissionMode: agent.PermissionModeBypassPermissions}
	m.input.SetValue("first prompt")

	_, cmd := m.launchSession()

	if cmd != nil {
		t.Error("launchSession must defer when bypass needs confirmation")
	}
	if !m.showConfirm {
		t.Fatal("launchSession must open the confirm dialog on first bypass launch")
	}
	if m.confirm.action != "launch-bypass" {
		t.Errorf("confirm action = %q, want %q", m.confirm.action, "launch-bypass")
	}
	if m.pendingLaunchReq == nil {
		t.Fatal("pendingLaunchReq must hold the deferred StartRequest")
	}
	if m.pendingLaunchReq.Prompt != "first prompt" {
		t.Errorf("pendingLaunchReq.Prompt = %q, want %q", m.pendingLaunchReq.Prompt, "first prompt")
	}
	if m.pendingLaunchReq.PermissionMode != agent.PermissionModeBypassPermissions {
		t.Errorf("pendingLaunchReq.PermissionMode = %q, want bypass", m.pendingLaunchReq.PermissionMode)
	}
	if m.submitting {
		t.Error("submitting must remain false until the dialog resolves")
	}
}

// TestLaunchSession_BypassSkipsConfirmAfterAck verifies the dialog is
// not re-shown when launching into a workspace the user already
// acknowledged for bypass.
func TestLaunchSession_BypassSkipsConfirmAfterAck(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	const dir = "/tmp/launch-already-acked"
	if err := config.MarkBypassPermissionsConfirmed(dir); err != nil {
		t.Fatalf("MarkBypassPermissionsConfirmed: %v", err)
	}

	m := newTestSessionModel(nil)
	m.composing = true
	m.backend = agent.BackendClaudeCode
	m.projectDir = dir
	m.info = &agent.SessionInfo{PermissionMode: agent.PermissionModeBypassPermissions}
	m.input.SetValue("first prompt")

	_, cmd := m.launchSession()

	if m.showConfirm {
		t.Error("launchSession must not open the dialog for an acknowledged workspace")
	}
	if m.pendingLaunchReq != nil {
		t.Error("pendingLaunchReq must stay nil when the launch dispatches inline")
	}
	if cmd == nil {
		t.Error("launchSession must dispatch immediately for an acknowledged workspace")
	}
	if !m.submitting {
		t.Error("submitting must be set when the launch dispatches inline")
	}
}

// TestHandleConfirmAction_LaunchBypassPersistsAck verifies that
// confirming the launch-time bypass dialog records the workspace as
// acknowledged, clears the pending request, and dispatches the launch.
func TestHandleConfirmAction_LaunchBypassPersistsAck(t *testing.T) {
	t.Setenv("CLANK_DIR", t.TempDir())

	const dir = "/tmp/launch-confirm-once"
	m := newTestSessionModel(nil)
	m.composing = true
	m.backend = agent.BackendClaudeCode
	m.projectDir = dir
	m.info = &agent.SessionInfo{
		PermissionMode: agent.PermissionModeBypassPermissions,
		GitRef:         agent.GitRef{LocalPath: dir},
	}
	m.pendingLaunchReq = &agent.StartRequest{
		Backend:        agent.BackendClaudeCode,
		Prompt:         "queued",
		PermissionMode: agent.PermissionModeBypassPermissions,
		GitRef:         agent.GitRef{LocalPath: dir},
	}

	cmd := m.handleConfirmAction("launch-bypass")
	if cmd == nil {
		t.Fatal("handleConfirmAction(launch-bypass) must dispatch the queued launch")
	}
	if m.pendingLaunchReq != nil {
		t.Error("pendingLaunchReq must be cleared after dispatch")
	}
	if !m.submitting {
		t.Error("submitting must be set when the launch dispatches")
	}

	prefs, err := config.LoadPreferences()
	if err != nil {
		t.Fatalf("LoadPreferences: %v", err)
	}
	if !prefs.IsBypassPermissionsConfirmed(dir) {
		t.Errorf("workspace %q not recorded as confirmed", dir)
	}
}

// TestConfirmCancel_ClearsPendingLaunch asserts that cancelling the
// launch-time bypass dialog drops the deferred StartRequest.
func TestConfirmCancel_ClearsPendingLaunch(t *testing.T) {
	t.Parallel()

	m := newTestSessionModel(nil)
	m.composing = true
	m.showConfirm = true
	m.pendingLaunchReq = &agent.StartRequest{Prompt: "should be dropped"}

	model, _ := m.Update(confirmResultMsg{confirmed: false, action: "launch-bypass"})
	m = model.(*SessionViewModel)

	if m.pendingLaunchReq != nil {
		t.Error("pendingLaunchReq must be cleared after cancel")
	}
	if m.showConfirm {
		t.Error("showConfirm must be cleared after cancel")
	}
}
