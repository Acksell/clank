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
// the daemon). Status is also seeded from the most recent status entry,
// if any.
func (c *clankView) SetEntries(entries []voice.Entry) {
	c.entries = c.entries[:0]
	for _, e := range entries {
		c.entries = append(c.entries, fromVoiceEntry(e))
		if e.Kind == voice.EntryKindStatus {
			c.status = e.Status
		}
	}
	c.follow = true
	c.scroll = 0
}

// AppendTranscript adds a transcript chunk. Streaming partials and the
// final done-marker are kept as separate entries; the renderer joins
// them visually until done=true closes the line.
func (c *clankView) AppendTranscript(text string, done bool) {
	c.entries = append(c.entries, clankEntry{
		kind:      voice.EntryKindTranscript,
		timestamp: time.Now(),
		text:      text,
		done:      done,
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
// indicator footer.
func (c clankView) viewportHeight() int {
	const reserved = 6 // header + hint + blank + status + blank + 1
	h := c.height - reserved
	if h < 1 {
		h = 1
	}
	return h
}

// View renders the Clank page content. Composed by the inbox into the
// right pane (parallel to settingsView.View).
func (c clankView) View() string {
	var sb strings.Builder

	header := lipgloss.NewStyle().
		Foreground(primaryColor).
		Bold(true).
		Render("Clank")
	sb.WriteString(header)
	sb.WriteString("\n")

	hint := lipgloss.NewStyle().
		Foreground(dimColor).
		Render("hold space to talk · ↑↓ scroll · G follow · ← sidebar · esc close")
	sb.WriteString(hint)
	sb.WriteString("\n\n")

	innerWidth := c.width - 2
	if innerWidth < 30 {
		innerWidth = 30
	}

	rendered := c.renderTimeline(innerWidth)
	sb.WriteString(rendered)
	sb.WriteString("\n")

	sb.WriteString(c.renderStatus())
	sb.WriteString("\n")

	return sb.String()
}

// renderTimeline returns the chronological body, scrolled per c.scroll
// (with auto-follow clamping the scroll position to the tail when
// c.follow is true). Empty timelines show a friendly placeholder.
func (c *clankView) renderTimeline(innerWidth int) string {
	if len(c.entries) == 0 {
		return lipgloss.NewStyle().
			Foreground(mutedColor).
			Italic(true).
			Render("  No voice activity yet. Hold space to start talking to Clank.")
	}

	// Render every entry to a string; we paginate post-render so
	// per-entry styling stays simple.
	rows := make([]string, 0, len(c.entries))
	for _, e := range c.entries {
		rows = append(rows, renderClankEntry(e, innerWidth))
	}

	vh := c.viewportHeight()
	if c.follow || c.scroll > len(rows)-vh {
		// Re-engage auto-follow when scroll lands at/past the tail.
		c.follow = true
		c.scroll = len(rows) - vh
	}
	if c.scroll < 0 {
		c.scroll = 0
	}
	end := c.scroll + vh
	if end > len(rows) {
		end = len(rows)
	}
	return strings.Join(rows[c.scroll:end], "\n")
}

// renderClankEntry styles a single timeline entry. Tool calls render
// dim-italic with truncated args; status entries render as a quiet
// inline marker.
func renderClankEntry(e clankEntry, maxWidth int) string {
	ts := e.timestamp.Format("15:04:05")
	tsStyled := lipgloss.NewStyle().Foreground(dimColor).Render(ts + "  ")

	switch e.kind {
	case voice.EntryKindTranscript:
		text := strings.TrimRight(e.text, "\n")
		body := lipgloss.NewStyle().Foreground(textColor).Render(text)
		return tsStyled + body
	case voice.EntryKindToolCall:
		args := truncateRunes(e.toolArgs, clankToolArgsMaxWidth)
		body := fmt.Sprintf("→ %s(%s)", e.toolName, args)
		styled := lipgloss.NewStyle().
			Foreground(mutedColor).
			Italic(true).
			Render(body)
		return tsStyled + styled
	case voice.EntryKindStatus:
		body := fmt.Sprintf("· %s", e.status)
		styled := lipgloss.NewStyle().Foreground(dimColor).Render(body)
		return tsStyled + styled
	}
	return tsStyled
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
