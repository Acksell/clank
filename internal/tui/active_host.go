package tui

// ActiveHost owns the TUI's notion of "where the next session will run".
//
// Persistence is delegated to internal/tui/uistate; this type is the
// in-process source of truth and the only mutator of the active_host
// key. Any Set() also writes through to disk so a restart preserves the
// user's selection — saves are synchronous because the TUI mutates this
// at most a few times per session (cursor selection on a host row).
//
// Validation lives at use-time, not at load: the catalog of hosts is
// fetched async, and we don't want to block startup or silently reset
// the user's pref just because the daytona host hasn't been provisioned
// yet. Callers should sanity-check Name() against the live catalog
// before issuing host-bound RPCs.

import (
	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/tui/uistate"
)

// KnownHostKinds enumerates host kinds the user can provision via the
// sidebar's [c] connect keybinding. Hardcoded for now; a future
// `GET /host-kinds` endpoint can replace this once we have more than
// one launcher.
var KnownHostKinds = []string{"daytona"}

// ActiveHost is the TUI's currently selected host. Construct with
// LoadActiveHost so the saved preference is honored on startup.
type ActiveHost struct {
	state *uistate.State
	name  host.Hostname
}

// LoadActiveHost reads the persisted active host from uistate, falling
// back to HostLocal when unset. Errors from uistate (malformed file,
// IO error) are returned so the caller can surface them — silently
// resetting the user's prefs would be a debuggability nightmare.
func LoadActiveHost() (*ActiveHost, error) {
	st, err := uistate.Load()
	if err != nil {
		return nil, err
	}
	name := host.Hostname(st.ActiveHost())
	if name == "" {
		name = host.HostLocal
	}
	return &ActiveHost{state: st, name: name}, nil
}

// Name returns the active host's name. Always non-empty (defaults to
// HostLocal).
func (a *ActiveHost) Name() host.Hostname { return a.name }

// IsLocal returns true when the active host is the laptop's local host.
// Convenience for the session-creation path that needs to decide whether
// to send LocalPath in the GitRef.
func (a *ActiveHost) IsLocal() bool { return a.name == host.HostLocal }

// Set updates the active host and persists it. Setting to HostLocal
// clears the persisted key (matches uistate.SetActiveHost("") semantics
// — see the package godoc for why empty strings clear).
//
// Returns the save error so the caller can surface it; the in-memory
// value is updated regardless so the UI reflects the user's intent
// even when persistence fails. When state is nil (caller constructed
// a detached ActiveHost after a uistate load failure) Set updates the
// in-memory value only — silently losing writes is preferable to
// crashing the TUI.
func (a *ActiveHost) Set(name host.Hostname) error {
	if name == "" {
		name = host.HostLocal
	}
	a.name = name
	if a.state == nil {
		return nil
	}
	if name == host.HostLocal {
		a.state.SetActiveHost("")
	} else {
		a.state.SetActiveHost(string(name))
	}
	return a.state.Save()
}
