package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/acksell/clank/internal/host"
)

// renderBranchHint emits a single dim line under the selected branch
// row exposing the row-scoped actions ([m]erge / [p]ush). Returns ""
// for unselected rows or rows where no action applies (the default
// branch can't be merged or pushed via these keybinds).
//
// [m]erge is gated to local hosts because git merge applies to the
// local working copy — running it against a remote sandbox would
// merge on the wrong tree. The [m] keybind in inbox.go enforces the
// same rule; this hint must mirror it so the affordance never shows
// a key that's secretly a no-op.
func (m *SidebarModel) renderBranchHint(b host.BranchInfo, linearIdx int) string {
	selected := m.cursor == linearIdx && m.focused
	if !selected || b.IsDefault {
		return ""
	}

	var actions []string
	if m.activeHost != nil && m.activeHost.IsLocal() {
		actions = append(actions, "[m]erge")
	}
	actions = append(actions, "[p]ush")
	if len(actions) == 0 {
		return ""
	}

	style := lipgloss.NewStyle().Foreground(dimColor)
	hint := "    "
	for i, a := range actions {
		if i > 0 {
			hint += "  "
		}
		hint += a
	}
	return style.Render(hint)
}
