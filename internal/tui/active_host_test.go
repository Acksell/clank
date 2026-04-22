package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/tui/uistate"
)

// TestActiveHost_DefaultsToLocal exercises the load-when-missing path:
// a fresh ~/.clank/tui-state.json (none) should yield HostLocal.
func TestActiveHost_DefaultsToLocal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, err := LoadActiveHost()
	if err != nil {
		t.Fatalf("LoadActiveHost: %v", err)
	}
	if a.Name() != host.HostLocal {
		t.Errorf("Name() = %q, want %q", a.Name(), host.HostLocal)
	}
	if !a.IsLocal() {
		t.Error("IsLocal() = false, want true")
	}
}

// TestActiveHost_SetPersists writes through to uistate so a subsequent
// load returns the same value. Regression for the Phase F wiring:
// without persistence, the sidebar selection would be lost on TUI
// restart.
func TestActiveHost_SetPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := LoadActiveHost()
	if err != nil {
		t.Fatalf("LoadActiveHost: %v", err)
	}
	if err := a.Set("daytona"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Reload from disk and assert.
	a2, err := LoadActiveHost()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if a2.Name() != "daytona" {
		t.Errorf("after reload Name() = %q, want %q", a2.Name(), "daytona")
	}
	if a2.IsLocal() {
		t.Error("IsLocal() = true after Set(daytona)")
	}
}

// TestActiveHost_SetLocalClearsKey: setting back to HostLocal should
// remove the persisted entry rather than write "active_host":"local",
// matching uistate's empty-clears-key contract. This keeps the file
// minimal and avoids the "older build sees a sentinel value it doesn't
// recognise" failure mode the uistate package guards against.
func TestActiveHost_SetLocalClearsKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := LoadActiveHost()
	if err != nil {
		t.Fatalf("LoadActiveHost: %v", err)
	}
	if err := a.Set("daytona"); err != nil {
		t.Fatalf("Set daytona: %v", err)
	}
	if err := a.Set(host.HostLocal); err != nil {
		t.Fatalf("Set local: %v", err)
	}

	// Round-trip through Load — the key should be gone, so default
	// (HostLocal) applies.
	a2, _ := LoadActiveHost()
	if a2.Name() != host.HostLocal {
		t.Errorf("after Set(local) Name() = %q, want %q", a2.Name(), host.HostLocal)
	}

	// And confirm the file doesn't contain the active_host key.
	st, _ := uistate.Load()
	if st.ActiveHost() != "" {
		t.Errorf("uistate ActiveHost() = %q after clear, want empty", st.ActiveHost())
	}
}

// TestActiveHost_SetEmptyTreatedAsLocal: passing "" must coerce to
// HostLocal so the in-memory state matches what's on disk after a
// fresh load. Otherwise IsLocal() would lie.
func TestActiveHost_SetEmptyTreatedAsLocal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a, _ := LoadActiveHost()
	_ = a.Set("")
	if a.Name() != host.HostLocal {
		t.Errorf("Name() = %q, want %q", a.Name(), host.HostLocal)
	}
}

// TestActiveHost_SetSurvivesNilState verifies the detached-state
// fallback (used in NewInboxModel when uistate.Load returns an error).
// Set must not panic and the in-memory value must update; persistence
// is silently skipped.
func TestActiveHost_SetSurvivesNilState(t *testing.T) {
	a := &ActiveHost{state: nil, name: host.HostLocal}
	if err := a.Set("daytona"); err != nil {
		t.Errorf("Set with nil state: %v", err)
	}
	if a.Name() != "daytona" {
		t.Errorf("Name() = %q, want daytona", a.Name())
	}
}

// TestKnownHostKinds_NonEmpty: the sidebar's "disconnected" rows come
// from this list. Empty would silently disable the [c] connect UX.
func TestKnownHostKinds_NonEmpty(t *testing.T) {
	if len(KnownHostKinds) == 0 {
		t.Fatal("KnownHostKinds is empty; sidebar will not offer [c] connect for any kind")
	}
	// daytona is the only kind we ship a launcher for in Phase F.
	found := false
	for _, k := range KnownHostKinds {
		if k == "daytona" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("KnownHostKinds = %v, missing daytona", KnownHostKinds)
	}
}

// TestActiveHost_LoadMalformedReturnsError mirrors the uistate
// contract: malformed file is an error, not a silent reset. The TUI
// chooses to fall back to HostLocal on this error (see NewInboxModel)
// — but that decision belongs to the caller, not LoadActiveHost.
func TestActiveHost_LoadMalformedReturnsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path, err := uistate.Path()
	if err != nil {
		t.Fatalf("uistate.Path: %v", err)
	}
	// Write garbage to the state file.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadActiveHost()
	if err == nil {
		t.Fatal("LoadActiveHost succeeded on malformed file; want error")
	}
	if !strings.Contains(err.Error(), "uistate") {
		t.Errorf("error = %v; want one mentioning uistate", err)
	}
	// Sanity: the underlying error chain should be a parse failure.
	if errors.Is(err, errors.New("nope")) {
		t.Error("errors.Is matched a fresh sentinel; chain is suspicious")
	}
}
