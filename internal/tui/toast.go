package tui

// Toast is a transient bottom-anchored notification overlay. It is
// rendered as a lipgloss compositor layer ON TOP of the inbox view —
// it does NOT consume a layout row, so the underlying content stays
// untouched and the toast disappears cleanly after its TTL.
//
// Lifecycle:
//
//	cmd := toast.Show("Pushed feat/login", toastSuccess)
//	// returned cmd is a tea.Tick that emits toastClearMsg{gen: N}
//	// after the TTL; the inbox Update routes the message back to
//	// toast.HandleClear which compares N against the current
//	// generation and clears only when it still matches.
//
// The generation counter prevents a stale TTL from clearing a fresher
// toast — e.g. user pushes A, then immediately pushes B; B's Show
// bumps gen to 2, and A's TTL message arrives carrying gen=1, so it
// is silently dropped.

import (
	"image/color"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// toastTTL is how long a toast stays on screen. Long enough to read a
// short sentence, short enough not to feel sticky.
const toastTTL = 3 * time.Second

// toastKind selects the toast's color theme. Keep this enum small —
// each kind is a distinct visual signal and adding too many dilutes
// the meaning.
type toastKind int

const (
	toastInfo toastKind = iota
	toastSuccess
	toastError
)

// toastClearMsg asks the inbox to drop the current toast IF gen still
// matches m.toast.gen. See package doc for the staleness rationale.
type toastClearMsg struct {
	gen int
}

// toastModel holds the currently-displayed toast (if any). The zero
// value is "no toast"; the visible field is the discriminator.
type toastModel struct {
	visible bool
	text    string
	kind    toastKind
	gen     int
}

// Show updates the model to display text and returns a tea.Cmd that
// emits a toastClearMsg after toastTTL. Replacing an existing toast is
// safe — the old TTL becomes stale via the bumped gen.
func (t *toastModel) Show(text string, kind toastKind) tea.Cmd {
	t.gen++
	t.visible = true
	t.text = text
	t.kind = kind
	gen := t.gen
	return tea.Tick(toastTTL, func(time.Time) tea.Msg {
		return toastClearMsg{gen: gen}
	})
}

// HandleClear consumes a toastClearMsg and clears the toast iff its
// gen matches the current one. Returns true when a clear actually
// happened so the caller can decide whether to trigger a redraw.
func (t *toastModel) HandleClear(msg toastClearMsg) bool {
	if !t.visible || msg.gen != t.gen {
		return false
	}
	t.visible = false
	t.text = ""
	return true
}

// Render returns the styled toast string, or "" when invisible. Width
// is the available terminal width; the toast self-truncates if needed
// so it never wraps past the right edge.
func (t *toastModel) Render(width int) string {
	if !t.visible {
		return ""
	}
	var border, fg color.Color
	switch t.kind {
	case toastSuccess:
		border, fg = successColor, successColor
	case toastError:
		border, fg = dangerColor, dangerColor
	default:
		border, fg = secondaryColor, secondaryColor
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Foreground(fg).
		Padding(0, 1).
		MaxWidth(width)
	return style.Render(t.text)
}

// overlayBottomRight composites toast over base anchored to the
// bottom-right corner of the (width, height) area, leaving a 1-cell
// margin from each edge. Returns base unchanged when toast is empty.
func overlayBottomRight(base, toast string, width, height int) string {
	if toast == "" {
		return base
	}
	tw := lipgloss.Width(toast)
	th := lipgloss.Height(toast)
	x := width - tw - 1
	if x < 0 {
		x = 0
	}
	y := height - th - 1
	if y < 0 {
		y = 0
	}
	bg := lipgloss.NewLayer(
		lipgloss.NewStyle().MaxWidth(width).Height(height).Render(base),
	).Z(0)
	fg := lipgloss.NewLayer(toast).X(x).Y(y).Z(2)
	return lipgloss.NewCompositor(bg, fg).Render()
}
