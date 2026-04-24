package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/config"
	"github.com/acksell/clank/internal/gitcred"
	"github.com/acksell/clank/internal/host"
)

// pressCred is a tiny helper: build the modal, dispatch a keypress,
// drain the returned cmd into a tea.Msg, and return both the modal
// and the resulting message (nil if no msg was emitted). Keeps the
// individual test bodies focused on assertions, not bubbletea
// plumbing.
func pressCred(t *testing.T, m credentialModalModel, key tea.KeyPressMsg) (credentialModalModel, tea.Msg) {
	t.Helper()
	cmd := m.Update(key)
	if cmd == nil {
		return m, nil
	}
	return m, cmd()
}

func newTestCredModal(pushErr error) credentialModalModel {
	return newCredentialModal(
		nil,                    // client – unused until [r]etry which we observe via pushBranchCmd from inbox
		host.Hostname("local"), // hostname
		agent.GitRef{},
		"feature/x",
		"github.com",
		pushErr,
	)
}

func TestCredentialModal_EscFromPromptCancels(t *testing.T) {
	t.Parallel()
	m := newTestCredModal(errors.New("auth required"))

	_, msg := pressCred(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	res, ok := msg.(credentialModalResultMsg)
	if !ok {
		t.Fatalf("expected credentialModalResultMsg, got %T", msg)
	}
	if res.retry {
		t.Errorf("esc must not request retry")
	}
	if res.saveErr != nil {
		t.Errorf("esc must not surface a save error: %v", res.saveErr)
	}
}

func TestCredentialModal_RKeyTriggersRetryWithoutSaving(t *testing.T) {
	t.Parallel()
	m := newTestCredModal(errors.New("auth required"))

	_, msg := pressCred(t, m, tea.KeyPressMsg{Code: 'r'})

	res, ok := msg.(credentialModalResultMsg)
	if !ok {
		t.Fatalf("expected credentialModalResultMsg, got %T", msg)
	}
	if !res.retry {
		t.Errorf("[r] must request retry")
	}
}

func TestCredentialModal_TKeyEntersInputMode(t *testing.T) {
	t.Parallel()
	m := newTestCredModal(errors.New("auth required"))

	m, msg := pressCred(t, m, tea.KeyPressMsg{Code: 't'})

	if m.mode != credModalInput {
		t.Errorf("[t] should switch to input mode, got %v", m.mode)
	}
	// [t] returns input.Focus() which is a non-nil cmd; the
	// resulting msg is a focus-related event we don't care about
	// here, just that the mode advanced.
	_ = msg
}

func TestCredentialModal_EscFromInputReturnsToPrompt(t *testing.T) {
	t.Parallel()
	m := newTestCredModal(errors.New("auth required"))
	m, _ = pressCred(t, m, tea.KeyPressMsg{Code: 't'})
	if m.mode != credModalInput {
		t.Fatalf("setup: expected input mode")
	}

	m, msg := pressCred(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.mode != credModalPrompt {
		t.Errorf("esc from input should return to prompt, got %v", m.mode)
	}
	if msg != nil {
		t.Errorf("esc from input must not close modal, got msg %T", msg)
	}
}

func TestCredentialModal_EmptyTokenIsNoOp(t *testing.T) {
	t.Parallel()
	m := newTestCredModal(errors.New("auth required"))
	m, _ = pressCred(t, m, tea.KeyPressMsg{Code: 't'})

	// No paste, just Enter.
	_, msg := pressCred(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if msg != nil {
		t.Errorf("empty-token enter must not close the modal; got %T %+v", msg, msg)
	}
}

// TestCredentialModal_SaveAndRetry exercises the happy path: paste a
// token, hit Enter, see (a) the file get written and (b) a retry
// result emitted. Uses HOME override so we don't pollute the real
// ~/.clank/credentials.json. This is an integration test in the
// sense forbidden by AGENTS.md only if we'd mocked the storage —
// instead we redirect the storage to a tempdir.
func TestCredentialModal_SaveAndRetry(t *testing.T) {
	// NOT t.Parallel — mutates HOME and the gitcred test path.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config")) // belt-and-braces

	m := newTestCredModal(errors.New("auth required"))
	m, _ = pressCred(t, m, tea.KeyPressMsg{Code: 't'})

	// Type a token character-by-character. textinput accepts
	// individual KeyPressMsgs with a Code rune; paste is the same
	// from its perspective.
	for _, r := range "ghp_testtoken123" {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		_ = cmd
	}
	if got := strings.TrimSpace(m.input.Value()); got != "ghp_testtoken123" {
		t.Fatalf("setup: expected token in input, got %q", got)
	}

	_, msg := pressCred(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	res, ok := msg.(credentialModalResultMsg)
	if !ok {
		t.Fatalf("expected credentialModalResultMsg, got %T (%v)", msg, msg)
	}
	if res.saveErr != nil {
		t.Fatalf("save unexpectedly failed: %v", res.saveErr)
	}
	if !res.retry {
		t.Errorf("save+enter must request retry")
	}

	// Verify the file actually contains the token. Reading via
	// the public Discoverer is cleaner than parsing the JSON
	// blob ourselves.
	dir, err := config.Dir()
	if err != nil {
		t.Fatalf("config.Dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("expected credentials.json to exist: %v", err)
	}
	if !strings.Contains(string(data), "ghp_testtoken123") {
		t.Errorf("expected token to be persisted, got: %s", string(data))
	}

	// And via the discoverer.
	cred, err := gitcred.FromSettings().Discover(t.Context(), &agent.GitEndpoint{Host: "github.com"})
	if err != nil {
		t.Fatalf("discover after save: %v", err)
	}
	if cred.Kind != agent.GitCredHTTPSBasic || cred.Password != "ghp_testtoken123" {
		t.Errorf("expected discovered token to match, got %+v", cred)
	}
}
