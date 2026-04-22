// Package uistate persists TUI preferences (active host, sidebar
// collapsed state, etc.) to ~/.clank/tui-state.json.
//
// Forward-compatibility: unknown keys are preserved on round-trip so
// an older clank build doesn't silently strip state written by a newer
// build. State is stored as a raw JSON map internally; typed
// accessors read and write specific keys, leaving everything else
// untouched.
//
// Concurrency: State is not safe for concurrent use. The TUI is
// single-goroutine for state mutation; saves are debounced from the
// same goroutine.
package uistate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/acksell/clank/internal/config"
)

// stateFileName is the basename inside ~/.clank.
const stateFileName = "tui-state.json"

// Known keys. Adding a new pref:
//  1. Add a `key*` constant here.
//  2. Add typed accessors below.
//  3. Update the godoc on State.
const (
	keyActiveHost       = "active_host"
	keySidebarCollapsed = "sidebar_collapsed"
)

// State is the in-memory representation of tui-state.json. Mutate via
// the typed setters; serialise with Save. The zero value is a valid
// empty state — Load returns this when the file doesn't exist.
type State struct {
	raw map[string]json.RawMessage
}

// New returns an empty State. Equivalent to a State backed by an
// empty file.
func New() *State {
	return &State{raw: map[string]json.RawMessage{}}
}

// Path returns the absolute path of the state file. Exposed mainly
// for tests and diagnostic logging.
func Path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, stateFileName), nil
}

// Load reads the state file. Returns an empty State (not an error)
// when the file is absent — first-run on a fresh install is the norm,
// not a failure.
//
// A malformed file is an error: silently zeroing the user's prefs
// because of a parse mistake would be a debuggability nightmare.
// Callers should surface the error and either prompt the user to
// delete the file or fall back to a fresh state explicitly.
func Load() (*State, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("uistate: read %s: %w", path, err)
	}
	if len(data) == 0 {
		return New(), nil
	}
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("uistate: parse %s: %w", path, err)
	}
	return &State{raw: raw}, nil
}

// Save writes the state to disk atomically: write-to-tmp + rename so
// a crash mid-write never leaves a half-written file.
func (s *State) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("uistate: mkdir %s: %w", filepath.Dir(path), err)
	}
	if s.raw == nil {
		s.raw = map[string]json.RawMessage{}
	}
	data, err := json.MarshalIndent(s.raw, "", "  ")
	if err != nil {
		return fmt.Errorf("uistate: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("uistate: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("uistate: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// --- Typed accessors ---

// ActiveHost returns the saved active host name, or "" if unset. The
// hostname is opaque to this package; the TUI is responsible for
// validating it against the live host catalog before use.
func (s *State) ActiveHost() string {
	return s.getString(keyActiveHost)
}

// SetActiveHost records the active host. Empty string clears the key
// from the file (so an older build doesn't see "active_host": "").
func (s *State) SetActiveHost(name string) {
	s.setString(keyActiveHost, name)
}

// SidebarCollapsed returns the saved sidebar-collapsed flag. Defaults
// to false when unset.
func (s *State) SidebarCollapsed() bool {
	return s.getBool(keySidebarCollapsed)
}

// SetSidebarCollapsed records the sidebar-collapsed flag. False
// clears the key (matching the default).
func (s *State) SetSidebarCollapsed(v bool) {
	if !v {
		s.clear(keySidebarCollapsed)
		return
	}
	s.setBool(keySidebarCollapsed, v)
}

// --- Internal helpers ---

func (s *State) ensure() {
	if s.raw == nil {
		s.raw = map[string]json.RawMessage{}
	}
}

func (s *State) clear(key string) {
	if s.raw == nil {
		return
	}
	delete(s.raw, key)
}

func (s *State) getString(key string) string {
	v, ok := s.raw[key]
	if !ok {
		return ""
	}
	var out string
	if err := json.Unmarshal(v, &out); err != nil {
		// Type mismatch (e.g. someone hand-edited the file): treat
		// as unset rather than crashing. The next Save round-trips
		// the bad value through unless setString overwrites it.
		return ""
	}
	return out
}

func (s *State) setString(key, val string) {
	if val == "" {
		s.clear(key)
		return
	}
	s.ensure()
	b, _ := json.Marshal(val)
	s.raw[key] = b
}

func (s *State) getBool(key string) bool {
	v, ok := s.raw[key]
	if !ok {
		return false
	}
	var out bool
	if err := json.Unmarshal(v, &out); err != nil {
		return false
	}
	return out
}

func (s *State) setBool(key string, val bool) {
	s.ensure()
	b, _ := json.Marshal(val)
	s.raw[key] = b
}
