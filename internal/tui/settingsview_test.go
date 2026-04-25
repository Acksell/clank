package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSettingsView_NewShowsCurrentScheme(t *testing.T) {
	t.Parallel()

	s := newSettingsView("dracula")
	if s.entries[0].kind != settingsEntryColorScheme {
		t.Fatalf("expected first entry to be color scheme, got %v", s.entries[0].kind)
	}
	if s.entries[0].value != "dracula" {
		t.Errorf("expected value 'dracula', got %q", s.entries[0].value)
	}
}

func TestSettingsView_NewEmptySchemeDisplaysDefault(t *testing.T) {
	t.Parallel()

	s := newSettingsView("")
	if got, want := s.entries[0].value, builtInSchemes[0].Name; got != want {
		t.Errorf("empty scheme name should resolve to %q, got %q", want, got)
	}
}

// TestSettingsView_EnterEmitsActivatedMsg verifies the page emits a message
// for the inbox to open the scheme picker, rather than opening it itself
// (keeping overlay ownership in the inbox).
func TestSettingsView_EnterEmitsActivatedMsg(t *testing.T) {
	t.Parallel()

	s := newSettingsView("")
	s, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected Update to return a cmd on Enter")
	}
	msg := cmd()
	act, ok := msg.(settingsActivatedMsg)
	if !ok {
		t.Fatalf("expected settingsActivatedMsg, got %T", msg)
	}
	if act.kind != settingsEntryColorScheme {
		t.Errorf("kind: got %v, want settingsEntryColorScheme", act.kind)
	}
	_ = s
}

func TestSettingsView_EscEmitsCloseMsg(t *testing.T) {
	t.Parallel()

	s := newSettingsView("")
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("expected cmd on esc")
	}
	if _, ok := cmd().(settingsCloseMsg); !ok {
		t.Errorf("expected settingsCloseMsg, got %T", cmd())
	}
}

func TestSettingsView_LeftEmitsFocusSidebarMsg(t *testing.T) {
	t.Parallel()

	s := newSettingsView("")
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd == nil {
		t.Fatal("expected cmd on left")
	}
	if _, ok := cmd().(settingsFocusSidebarMsg); !ok {
		t.Errorf("expected settingsFocusSidebarMsg, got %T", cmd())
	}
}

func TestSettingsView_SetColorSchemeValueUpdatesEntry(t *testing.T) {
	t.Parallel()

	s := newSettingsView("default")
	s.SetColorSchemeValue("tokyo-night")
	if got := s.entries[0].value; got != "tokyo-night" {
		t.Errorf("entry value: got %q, want tokyo-night", got)
	}

	// Empty name resolves to the default built-in scheme.
	s.SetColorSchemeValue("")
	if got, want := s.entries[0].value, builtInSchemes[0].Name; got != want {
		t.Errorf("empty name should resolve to %q, got %q", want, got)
	}
}

func TestSettingsView_ViewContainsLabelAndValue(t *testing.T) {
	t.Parallel()

	s := newSettingsView("gruvbox-dark")
	s.SetSize(80, 30)
	s.SetFocused(true)
	out := s.View()
	if !strings.Contains(out, "Change color scheme") {
		t.Errorf("expected entry label in view, got:\n%s", out)
	}
	if !strings.Contains(out, "gruvbox-dark") {
		t.Errorf("expected current value in view, got:\n%s", out)
	}
}
