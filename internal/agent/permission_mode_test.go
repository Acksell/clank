package agent_test

import (
	"testing"

	"github.com/acksell/clank/internal/agent"
)

func TestPermissionModeNextCyclesAndWraps(t *testing.T) {
	t.Parallel()

	want := []agent.PermissionMode{
		agent.PermissionModeAcceptEdits,
		agent.PermissionModePlan,
		agent.PermissionModeBypassPermissions,
		agent.PermissionModeDefault, // wrap
	}

	got := agent.PermissionModeDefault
	for i, expected := range want {
		got = got.Next()
		if got != expected {
			t.Errorf("step %d: expected %q, got %q", i, expected, got)
		}
	}
}

func TestPermissionModeNextUnknownReturnsFirst(t *testing.T) {
	t.Parallel()

	got := agent.PermissionMode("garbage").Next()
	if got != agent.PermissionModeCycle[0] {
		t.Errorf("expected first cycle entry %q for unknown mode, got %q", agent.PermissionModeCycle[0], got)
	}
}

func TestPermissionModeValidate(t *testing.T) {
	t.Parallel()

	cases := map[agent.PermissionMode]bool{
		agent.PermissionModeDefault:           true,
		agent.PermissionModeAcceptEdits:       true,
		agent.PermissionModePlan:              true,
		agent.PermissionModeBypassPermissions: true,
		"":                                    false,
		"weird":                               false,
	}
	for mode, ok := range cases {
		err := mode.Validate()
		if (err == nil) != ok {
			t.Errorf("Validate(%q): ok=%v, err=%v", mode, ok, err)
		}
	}
}

func TestPermissionModeDisplayNameDistinct(t *testing.T) {
	t.Parallel()

	seen := map[string]agent.PermissionMode{}
	for _, m := range agent.PermissionModeCycle {
		name := m.DisplayName()
		if name == "" {
			t.Errorf("mode %q has empty display name", m)
		}
		if other, ok := seen[name]; ok {
			t.Errorf("display name %q used by both %q and %q", name, other, m)
		}
		seen[name] = m
	}
}
