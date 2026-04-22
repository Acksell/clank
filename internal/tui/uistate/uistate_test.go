package uistate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acksell/clank/internal/tui/uistate"
)

// withTempHome redirects ~/.clank to a per-test directory. Returns the
// state file's expected path. Uses t.Setenv so the override is reverted
// automatically — important because parallel tests can't safely share
// HOME, hence the explicit lack of t.Parallel() in the tests below.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".clank", "tui-state.json")
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	withTempHome(t)
	s, err := uistate.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if got := s.ActiveHost(); got != "" {
		t.Errorf("ActiveHost on empty = %q, want \"\"", got)
	}
	if s.SidebarCollapsed() {
		t.Error("SidebarCollapsed on empty = true, want false")
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	withTempHome(t)
	s := uistate.New()
	s.SetActiveHost("daytona")
	s.SetSidebarCollapsed(true)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	s2, err := uistate.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := s2.ActiveHost(); got != "daytona" {
		t.Errorf("ActiveHost = %q, want daytona", got)
	}
	if !s2.SidebarCollapsed() {
		t.Error("SidebarCollapsed = false, want true")
	}
}

func TestSetActiveHost_EmptyClearsKey(t *testing.T) {
	path := withTempHome(t)

	s := uistate.New()
	s.SetActiveHost("daytona")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Clear it and re-save.
	s.SetActiveHost("")
	if err := s.Save(); err != nil {
		t.Fatalf("Save (clear): %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	if _, present := raw["active_host"]; present {
		t.Errorf("active_host key still present after clear; raw=%s", data)
	}
}

func TestUnknownKeysPreservedOnRoundtrip(t *testing.T) {
	path := withTempHome(t)

	// Hand-write a state file with both known and unknown keys, as
	// would happen when a newer build of clank wrote a key the
	// current build doesn't know about. The current build must NOT
	// strip it on the next Save.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "active_host": "daytona",
  "future_pref_from_newer_build": {"shape": "anything", "n": 42}
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := uistate.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Mutate a known key to force a Save.
	s.SetActiveHost("local")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["future_pref_from_newer_build"]; !ok {
		t.Errorf("unknown key dropped on round-trip; got %s", data)
	}
	if got := s.ActiveHost(); got != "local" {
		t.Errorf("ActiveHost = %q, want local", got)
	}
}

func TestLoad_MalformedFileIsError(t *testing.T) {
	path := withTempHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := uistate.Load(); err == nil {
		t.Fatal("Load on malformed file: want error, got nil")
	}
}

func TestSave_AtomicViaTempFile(t *testing.T) {
	path := withTempHome(t)

	s := uistate.New()
	s.SetActiveHost("daytona")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The .tmp file should not linger after a successful rename.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file %s still present (or stat err %v)", path+".tmp", err)
	}
}

func TestSidebarCollapsed_FalseClearsKey(t *testing.T) {
	path := withTempHome(t)
	s := uistate.New()
	s.SetSidebarCollapsed(true)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.SetSidebarCollapsed(false)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["sidebar_collapsed"]; present {
		t.Errorf("sidebar_collapsed still present after clearing to false; raw=%s", data)
	}
}
