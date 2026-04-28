package tui

// clankview.go — the "Clank" page rendered in the right pane of the
// inbox layout. Clank is the orchestrator agent (also reachable via
// voice); this view is a chronological transcript of voice activity:
// user/agent transcripts and tool calls, interleaved.
//
// Navigation contract (mirrors settingsview.go):
//
//	up/down / j/k   → scroll
//	pgup/pgdown     → scroll a page
//	home/g          → top
//	end/G           → bottom (re-engages auto-follow)
//	left            → return focus to the sidebar
//	esc             → close the Clank page (handled by inbox)
//
// Recording is push-to-talk via SPACE and lives in the existing voice
// pipeline (see voice.go); this view never originates audio.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/voice"
)

// clankToolArgsMaxWidth caps how many runes of a tool-call's args
// string we render inline. Past this we ellipsize. Voice tools often
// emit long JSON blobs that would otherwise dominate the timeline.
const clankToolArgsMaxWidth = 60

// clankCloseMsg asks the inbox to swap the right pane back to the
// inbox screen (parallel to settingsCloseMsg).
type clankCloseMsg struct{}

// clankFocusSidebarMsg asks the inbox to move focus back to the
// sidebar while keeping the Clank page visible (parallel to
// settingsFocusSidebarMsg).
type clankFocusSidebarMsg struct{}

// clankEntry is one row in the timeline. We keep the same shape as
// voice.Entry but drop the wire-only json tags and add a coalesced
// transcript text (multiple streamed chunks → one displayable line).
type clankEntry struct {
	kind      voice.EntryKind
	timestamp time.Time
	// transcript:
	text string
	done bool
	role agent.VoiceRole
	// tool_call:
	toolName string
	toolArgs string
	// status:
	status agent.VoiceStatus
}

// clankView is the right-pane Clank page model. It owns scroll/focus
// state and an append-only list of entries. The inbox is responsible
// for feeding entries via Append* (live events) and SetEntries
// (cold-start snapshot from the daemon).
type clankView struct {
	entries []clankEntry

	// status is the latest status reported by the voice agent. Shown
	// at the bottom of the page so users can see whether Clank is
	// listening / thinking / speaking even when the timeline is idle.
	status agent.VoiceStatus

	scroll int
	// follow stays true while the cursor is at the bottom; new entries
	// scroll automatically. Switches to false on manual scroll-up so
	// the user can read history without being yanked away.
	follow bool

	width   int
	height  int
	focused bool
}

// newClankView constructs an empty Clank page in idle state with
// auto-follow enabled.
func newClankView() clankView {
	return clankView{
		status: agent.VoiceStatusIdle,
		follow: true,
	}
}

func (c clankView) Init() tea.Cmd { return nil }

func (c *clankView) SetSize(w, h int) {
	c.width = w
	c.height = h
}

func (c *clankView) SetFocused(f bool) {
	c.focused = f
}

// SetEntries replaces the timeline (used on cold-start snapshot from
// the daemon). Streaming chunks (consecutive transcript entries from
// the same role with done=false) are coalesced into a single line so
// the TUI never displays the daemon's raw token stream. Status is also
// seeded from the most recent status entry, if any.
func (c *clankView) SetEntries(entries []voice.Entry) {
	c.entries = c.entries[:0]
	for _, e := range entries {
		if e.Kind == voice.EntryKindTranscript {
			c.appendTranscriptEntry(e.Text, e.Done, e.Role, e.Timestamp)
		} else {
			c.entries = append(c.entries, fromVoiceEntry(e))
		}
		if e.Kind == voice.EntryKindStatus {
			c.status = e.Status
		}
	}
	c.follow = true
	c.scroll = 0
}

// AppendTranscript adds a transcript chunk. Streaming partials from
// the same speaker are coalesced into the previous entry until a
// done=true chunk closes the utterance. A new chunk after done=true
// (or from a different speaker) starts a fresh entry.
func (c *clankView) AppendTranscript(text string, done bool, role agent.VoiceRole) {
	c.appendTranscriptEntry(text, done, role, time.Now())
}

// appendTranscriptEntry is the shared coalescing path used by both
// live appends and snapshot ingestion.
func (c *clankView) appendTranscriptEntry(text string, done bool, role agent.VoiceRole, ts time.Time) {
	if n := len(c.entries); n > 0 {
		last := &c.entries[n-1]
		if last.kind == voice.EntryKindTranscript && !last.done && last.role == role {
			last.text += text
			last.done = done
			return
		}
	}
	c.entries = append(c.entries, clankEntry{
		kind:      voice.EntryKindTranscript,
		timestamp: ts,
		text:      text,
		done:      done,
		role:      role,
	})
}

// AppendToolCall records a voice-agent tool invocation.
func (c *clankView) AppendToolCall(name, args string) {
	c.entries = append(c.entries, clankEntry{
		kind:      voice.EntryKindToolCall,
		timestamp: time.Now(),
		toolName:  name,
		toolArgs:  args,
	})
}

// SetStatus updates the live status indicator. Status entries are also
// appended to the timeline so the chronological view shows transitions.
func (c *clankView) SetStatus(s agent.VoiceStatus) {
	if c.status == s {
		return
	}
	c.status = s
	c.entries = append(c.entries, clankEntry{
		kind:      voice.EntryKindStatus,
		timestamp: time.Now(),
		status:    s,
	})
}

func (c clankView) Update(msg tea.Msg) (clankView, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return c, nil
	}
	keyMsg = normalizeKeyCase(keyMsg)

	switch {
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("up", "k"))):
		if c.scroll > 0 {
			c.scroll--
		}
		c.follow = false
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("down", "j"))):
		c.scroll++
		// follow re-engages once scroll is clamped to bottom in View.
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("pgup"))):
		page := c.viewportHeight()
		if c.scroll -= page; c.scroll < 0 {
			c.scroll = 0
		}
		c.follow = false
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("pgdown"))):
		c.scroll += c.viewportHeight()
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("home", "g"))):
		c.scroll = 0
		c.follow = false
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("end", "G"))):
		c.follow = true
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("esc"))):
		return c, func() tea.Msg { return clankCloseMsg{} }
	case key.Matches(keyMsg, key.NewBinding(key.WithKeys("left", "h"))):
		return c, func() tea.Msg { return clankFocusSidebarMsg{} }
	}
	return c, nil
}

// viewportHeight is the number of timeline rows visible at a time.
// Reserves rows for the header, hint, blank lines, and the status
// indicator footer. Keep in sync with View() so scrolling math doesn't
// drift past the visible window.
func (c clankView) viewportHeight() int {
	// header(1) + blank(1) + hint(1) + blank(1) + body... + blank(1) + status(1) + blank(1)
	const reserved = 6
	h := c.height - reserved
	if h < 1 {
		h = 1
	}
	return h
}

// horizontalMargin returns the per-side padding (in columns) applied
// to all rendered content. Aim ~14 % of pane width with a sensible
// floor/ceiling so narrow panes still get some breathing room and
// very wide panes don't waste the screen.
func (c clankView) horizontalMargin() int {
	const (
		minMargin = 2
		maxMargin = 14
	)
	m := c.width * 14 / 100
	if m < minMargin {
		m = minMargin
	}
	if m > maxMargin {
		m = maxMargin
	}
	if m*2 >= c.width {
		// Defensive: never eat the whole pane.
		m = minMargin
	}
	return m
}

// View renders the Clank page content. Composed by the inbox into the
// right pane (parallel to settingsView.View).
func (c clankView) View() string {
	margin := c.horizontalMargin()
	// Only left-pad. Right padding would make a single-line element
	// (header / hint / status) wider than the pane when its content
	// is itself near pane width, since lipgloss pads literally rather
	// than truncating. Right margin for the timeline is enforced by
	// wrapping body text to innerWidth in renderClankEntry.
	padStyle := lipgloss.NewStyle().PaddingLeft(margin)

	var sb strings.Builder

	header := lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		Render("Clank")
	sb.WriteString(padStyle.Render(header))
	sb.WriteString("\n")

	hint := lipgloss.NewStyle().
		Foreground(dimColor).
		Render("hold space to talk · ↑↓ scroll · G follow · ← sidebar · esc close")
	sb.WriteString(padStyle.Render(hint))
	sb.WriteString("\n\n")

	innerWidth := c.width - 2*margin
	if innerWidth < 20 {
		innerWidth = 20
	}

	rendered := c.renderTimeline(innerWidth)
	sb.WriteString(padStyle.Render(rendered))
	sb.WriteString("\n")

	sb.WriteString(padStyle.Render(c.renderStatus()))
	sb.WriteString("\n")

	return sb.String()
}

// renderTimeline returns the chronological body, scrolled per c.scroll
// (with auto-follow clamping the scroll position to the tail when
// c.follow is true). Empty timelines show a centered placeholder card
// so the right pane never looks broken on cold-start.
//
// Pagination operates on display lines rather than entries because a
// single transcript can wrap across many lines once horizontal margins
// are applied — counting entries would let long utterances scroll past
// the viewport.
func (c *clankView) renderTimeline(innerWidth int) string {
	if len(c.entries) == 0 {
		return c.renderEmptyState(innerWidth)
	}

	rendered := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		rendered = append(rendered, renderClankEntry(e, innerWidth))
	}
	// Flatten to display lines for accurate scroll math.
	allLines := strings.Split(strings.Join(rendered, "\n"), "\n")

	vh := c.viewportHeight()
	if c.follow || c.scroll > len(allLines)-vh {
		c.follow = true
		c.scroll = len(allLines) - vh
	}
	if c.scroll < 0 {
		c.scroll = 0
	}
	end := c.scroll + vh
	if end > len(allLines) {
		end = len(allLines)
	}
	return strings.Join(allLines[c.scroll:end], "\n")
}

// renderEmptyState draws a faint, rounded-bordered card centered in
// the timeline area. Replaces the old top-left italic line that made
// the pane look unstyled on first launch.
func (c clankView) renderEmptyState(innerWidth int) string {
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(mutedColor).
		Foreground(dimColor).
		Padding(1, 3).
		Align(lipgloss.Center).
		Render("Nothing here yet\n\nHold space to talk to Clank.")

	vh := c.viewportHeight()
	if vh < 3 {
		vh = 3
	}
	return lipgloss.Place(
		innerWidth, vh,
		lipgloss.Center, lipgloss.Center,
		card,
	)
}

// renderClankEntry styles a single timeline entry. Tool calls render
// dim-italic with truncated args; status entries render as a quiet
// inline marker. The body is word-wrapped to maxWidth so it respects
// the pane's horizontal margins instead of bleeding past the right
// edge (or wrapping with no left indent on continuation lines).
func renderClankEntry(e clankEntry, maxWidth int) string {
	ts := e.timestamp.Format("15:04:05")
	tsStyled := lipgloss.NewStyle().Foreground(dimColor).Render(ts + "  ")
	tsWidth := ansi.StringWidth(ts + "  ")

	switch e.kind {
	case voice.EntryKindTranscript:
		text := strings.TrimRight(e.text, "\n")
		var prefix string
		prefixColor := dimColor
		switch e.role {
		case agent.VoiceRoleUser:
			prefix = "you  › "
			prefixColor = secondaryColor
		case agent.VoiceRoleAssistant:
			prefix = "clank › "
			prefixColor = primaryColor
		default:
			prefix = "· "
		}
		prefixWidth := ansi.StringWidth(prefix)
		prefixStyled := lipgloss.NewStyle().Foreground(prefixColor).Bold(true).Render(prefix)

		bodyStyle := lipgloss.NewStyle().Foreground(textColor)
		return wrapTimelineRow(tsStyled, tsWidth, prefixStyled, prefixWidth, text, bodyStyle, maxWidth)

	case voice.EntryKindToolCall:
		args := truncateRunes(e.toolArgs, clankToolArgsMaxWidth)
		body := fmt.Sprintf("→ %s(%s)", e.toolName, args)
		styled := lipgloss.NewStyle().Foreground(mutedColor).Italic(true)
		return wrapTimelineRow(tsStyled, tsWidth, "", 0, body, styled, maxWidth)

	case voice.EntryKindStatus:
		body := fmt.Sprintf("· %s", e.status)
		styled := lipgloss.NewStyle().Foreground(dimColor)
		return wrapTimelineRow(tsStyled, tsWidth, "", 0, body, styled, maxWidth)
	}
	return tsStyled
}

// wrapTimelineRow word-wraps body to fit maxWidth, prefixing the first
// line with [tsStyled][prefixStyled] and indenting continuation lines
// with whitespace of equal width so the body column stays aligned.
// Returns the multi-line string ready to embed in the timeline.
func wrapTimelineRow(tsStyled string, tsWidth int, prefixStyled string, prefixWidth int, body string, bodyStyle lipgloss.Style, maxWidth int) string {
	indentWidth := tsWidth + prefixWidth
	bodyWidth := maxWidth - indentWidth
	if bodyWidth < 8 {
		// Defensive floor: extremely narrow panes should still produce
		// readable output rather than an infinite-loop wrap.
		bodyWidth = 8
	}
	wrapped := ansi.Wrap(body, bodyWidth, " \t-")
	lines := strings.Split(wrapped, "\n")
	indent := strings.Repeat(" ", indentWidth)
	var sb strings.Builder
	for i, ln := range lines {
		if i == 0 {
			sb.WriteString(tsStyled)
			sb.WriteString(prefixStyled)
		} else {
			sb.WriteString("\n")
			sb.WriteString(indent)
		}
		sb.WriteString(bodyStyle.Render(ln))
	}
	return sb.String()
}

// renderStatus draws the bottom status indicator. The dot color tracks
// the agent's state so users can glance at it during a conversation.
func (c clankView) renderStatus() string {
	color := dimColor
	switch c.status {
	case agent.VoiceStatusListening:
		color = successColor
	case agent.VoiceStatusThinking:
		color = primaryColor
	case agent.VoiceStatusSpeaking:
		color = primaryColor
	}
	dot := lipgloss.NewStyle().Foreground(color).Bold(true).Render("●")
	label := lipgloss.NewStyle().Foreground(dimColor).Render(string(c.status))
	return dot + " " + label
}

// fromVoiceEntry converts a wire-level voice.Entry (from the daemon
// snapshot) into a clankEntry. Kept as a free function so tests can
// exercise the conversion without a clankView.
func fromVoiceEntry(e voice.Entry) clankEntry {
	return clankEntry{
		kind:      e.Kind,
		timestamp: e.Timestamp,
		text:      e.Text,
		done:      e.Done,
		role:      e.Role,
		toolName:  e.ToolName,
		toolArgs:  e.ToolArgs,
		status:    e.Status,
	}
}

// truncateRunes shortens s to at most max runes, appending "…" when
// truncation occurred. Rune-aware so multibyte tool arg strings
// (emoji, non-ASCII) don't get sliced mid-character.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}
