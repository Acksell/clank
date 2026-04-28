package agent

import (
	"errors"
	"fmt"
)

// PermissionMode is the clank-level enum for Claude Code's runtime
// permission modes. The values intentionally match the Claude SDK's
// PermissionMode string constants so they can cross the wire and be
// passed straight to the SDK without a translation table.
type PermissionMode string

const (
	PermissionModeDefault           PermissionMode = "default"
	PermissionModeAcceptEdits       PermissionMode = "acceptEdits"
	PermissionModePlan              PermissionMode = "plan"
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// PermissionModeCycle is the canonical cycle order surfaced in the TUI.
// Tab/Shift+Tab walks this slice. bypassPermissions is included unconditionally;
// the warning prompt is gated at send-time per workspace, not at cycle-time.
var PermissionModeCycle = []PermissionMode{
	PermissionModeDefault,
	PermissionModeAcceptEdits,
	PermissionModePlan,
	PermissionModeBypassPermissions,
}

// ErrPermissionModeUnsupported is returned by SessionBackend.SetPermissionMode
// for backends that have no concept of permission modes (e.g. OpenCode).
var ErrPermissionModeUnsupported = errors.New("permission mode not supported by this backend")

// Validate reports an error if the mode is not one of the known constants.
// The empty string is treated as PermissionModeDefault by callers; callers
// must normalize before validating if they want that behaviour.
func (m PermissionMode) Validate() error {
	switch m {
	case PermissionModeDefault, PermissionModeAcceptEdits, PermissionModePlan, PermissionModeBypassPermissions:
		return nil
	}
	return fmt.Errorf("unknown permission mode: %q", string(m))
}

// DisplayName returns a short human-readable label for the mode, suitable
// for a status badge. We don't surface the raw SDK strings — "default" is
// not informative.
func (m PermissionMode) DisplayName() string {
	switch m {
	case PermissionModeDefault:
		return "ask"
	case PermissionModeAcceptEdits:
		return "auto-edit"
	case PermissionModePlan:
		return "plan"
	case PermissionModeBypassPermissions:
		return "yolo"
	}
	return string(m)
}

// Next returns the mode that follows m in PermissionModeCycle, wrapping
// at the end. An unknown mode returns the first cycle entry.
func (m PermissionMode) Next() PermissionMode {
	for i, c := range PermissionModeCycle {
		if c == m {
			return PermissionModeCycle[(i+1)%len(PermissionModeCycle)]
		}
	}
	return PermissionModeCycle[0]
}
