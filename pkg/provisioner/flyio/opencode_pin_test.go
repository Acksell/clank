package flyio

import (
	"strings"
	"testing"

	"github.com/acksell/clank/internal/agent"
)

// TestValidatePinForShell_Allows pins the contract that
// digit-dot-hyphen versions pass — covers semver and pre-release
// tags. Anything else is treated as a bug in the caller (the pin
// is a clank-source constant, not user input).
func TestValidatePinForShell_Allows(t *testing.T) {
	t.Parallel()
	for _, v := range []string{
		"1.14.49",
		"2.0.0",
		"1.14.49-rc.1", // hypothetical future pre-release tag
		"1.14",
	} {
		if err := validatePinForShell(v); err != nil {
			t.Errorf("validatePinForShell(%q) = %v, want nil", v, err)
		}
	}
}

// TestValidatePinForShell_Rejects covers the threats: shell
// metacharacters and command-injection vectors. validatePinForShell
// is the only thing keeping a misconfigured pin from running
// arbitrary shell on a sprite.
func TestValidatePinForShell_Rejects(t *testing.T) {
	t.Parallel()
	for _, v := range []string{
		"",                  // empty
		"1.14.49; rm -rf /", // command chaining
		"1.14.49`echo p`",   // command substitution
		"1.14.49$(id)",      // command substitution
		"1.14.49 && id",     // command chaining (whitespace)
		"1.14.49|id",        // pipe
		"1.14.49\nid",       // newline injection
		"../../etc/passwd",  // path-traversal-shaped (slash)
		"1.14.49/foo",       // slash
	} {
		if err := validatePinForShell(v); err == nil {
			t.Errorf("validatePinForShell(%q) = nil, want error", v)
		}
	}
}

// TestPinnedOpencodeVersion_PassesShellSafetyCheck makes sure the
// constant clank actually ships with isn't shaped in a way the
// shell-safety check would reject — that would be a self-inflicted
// runtime error every EnsureHost.
func TestPinnedOpencodeVersion_PassesShellSafetyCheck(t *testing.T) {
	t.Parallel()
	if err := validatePinForShell(agent.PinnedOpencodeVersion); err != nil {
		t.Fatalf("agent.PinnedOpencodeVersion = %q is not shell-safe: %v", agent.PinnedOpencodeVersion, err)
	}
}

// TestOpencodeInstallScript_HasPinnedSubstitution confirms the
// script template still contains the __PINNED_VERSION__ placeholder
// that ensureOpenCodeInstalled relies on. A future refactor that
// removes the placeholder without removing the substitution call
// would silently install opencode-ai@__PINNED_VERSION__ — a
// nonexistent package — and break every sprite. Catch it here.
func TestOpencodeInstallScript_HasPinnedSubstitution(t *testing.T) {
	t.Parallel()
	if !strings.Contains(opencodeInstallScript, "__PINNED_VERSION__") {
		t.Errorf("opencodeInstallScript no longer contains __PINNED_VERSION__ placeholder; the substitution in ensureOpenCodeInstalled would no-op")
	}
}
