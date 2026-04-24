package tui

// Regression tests for the row-level discoverability hint and the
// host-aware action gating it advertises.
//
// The hint must NEVER show a key whose handler is a no-op — the user
// would press it, see nothing happen, and (rightly) lose trust in the
// affordance system. So both `[m]` and `[p]` visibility here are
// in lock-step with the keybind handlers in inbox.go.

import (
	"strings"
	"testing"

	"github.com/acksell/clank/internal/host"
)

func TestRenderBranchHint_HiddenWhenUnselected(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	bi := host.BranchInfo{Name: "feat/login", IsDefault: false}
	got := s.renderBranchHint(bi, 999) // cursor is on a different row
	if got != "" {
		t.Fatalf("expected empty hint when row not selected, got %q", got)
	}
}

func TestRenderBranchHint_HiddenForDefaultBranch(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.cursor = 5
	bi := host.BranchInfo{Name: "main", IsDefault: true}
	got := s.renderBranchHint(bi, 5)
	if got != "" {
		t.Fatalf("default branch must not advertise [m]/[p], got %q", got)
	}
}

func TestRenderBranchHint_LocalShowsMergeAndPush(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	s.cursor = 7
	bi := host.BranchInfo{Name: "feat/login"}
	got := s.renderBranchHint(bi, 7)
	if !strings.Contains(got, "[m]erge") {
		t.Fatalf("local active host must show [m]erge hint, got %q", got)
	}
	if !strings.Contains(got, "[p]ush") {
		t.Fatalf("hint must always show [p]ush for non-default branches, got %q", got)
	}
}

func TestRenderBranchHint_RemoteHidesMerge(t *testing.T) {
	t.Parallel()
	s := newTestSidebar(t)
	// Switch active host to a remote name. Set on a detached state is
	// safe — see ActiveHost.Set godoc.
	if err := s.activeHost.Set(host.Hostname("daytona-1")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.cursor = 3
	bi := host.BranchInfo{Name: "feat/login"}
	got := s.renderBranchHint(bi, 3)
	if strings.Contains(got, "[m]erge") {
		t.Fatalf("remote host must NOT advertise [m]erge (it's a no-op), got %q", got)
	}
	if !strings.Contains(got, "[p]ush") {
		t.Fatalf("remote host must still advertise [p]ush, got %q", got)
	}
}
