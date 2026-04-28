package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/voice"
)

func TestClankViewAppendAndRender(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 20)
	c.AppendTranscript("hello world", true, agent.VoiceRoleAssistant)
	c.AppendToolCall("list_sessions", `{"limit":5}`)
	c.SetStatus(agent.VoiceStatusListening)

	out := c.View()
	if !strings.Contains(out, "Clank") {
		t.Errorf("missing Clank header:\n%s", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("missing transcript:\n%s", out)
	}
	if !strings.Contains(out, "list_sessions") {
		t.Errorf("missing tool call:\n%s", out)
	}
	if !strings.Contains(out, string(agent.VoiceStatusListening)) {
		t.Errorf("missing status indicator:\n%s", out)
	}
}

func TestClankViewSetEntriesSeedsStatus(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 20)
	c.SetEntries([]voice.Entry{
		{Kind: voice.EntryKindTranscript, Text: "first", Timestamp: time.Now(), Done: true},
		{Kind: voice.EntryKindStatus, Status: agent.VoiceStatusSpeaking, Timestamp: time.Now()},
		{Kind: voice.EntryKindStatus, Status: agent.VoiceStatusIdle, Timestamp: time.Now()},
	})
	if c.status != agent.VoiceStatusIdle {
		t.Errorf("status = %q, want idle (latest)", c.status)
	}
	if got := len(c.entries); got != 3 {
		t.Errorf("entries len = %d, want 3", got)
	}
}

func TestClankViewSetStatusDedup(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetStatus(agent.VoiceStatusListening)
	c.SetStatus(agent.VoiceStatusListening) // no-op
	c.SetStatus(agent.VoiceStatusThinking)

	if got := len(c.entries); got != 2 {
		t.Errorf("entries len = %d, want 2 (dedup expected)", got)
	}
}

func TestClankViewToolArgsTruncated(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", clankToolArgsMaxWidth*3)
	c := newClankView()
	c.SetSize(80, 20)
	c.AppendToolCall("noisy_tool", long)

	out := c.View()
	if !strings.Contains(out, "…") {
		t.Errorf("expected ellipsis in truncated tool args:\n%s", out)
	}
	if strings.Count(out, "x") >= len(long) {
		t.Errorf("expected truncated args, got full length")
	}
}

func TestClankViewEscEmitsCloseMsg(t *testing.T) {
	t.Parallel()

	c := newClankView()
	_, cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected close cmd on esc")
	}
	if _, ok := cmd().(clankCloseMsg); !ok {
		t.Errorf("expected clankCloseMsg, got %T", cmd())
	}
}

func TestClankViewLeftEmitsFocusSidebarMsg(t *testing.T) {
	t.Parallel()

	c := newClankView()
	_, cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd == nil {
		t.Fatal("expected focus-sidebar cmd on left")
	}
	if _, ok := cmd().(clankFocusSidebarMsg); !ok {
		t.Errorf("expected clankFocusSidebarMsg, got %T", cmd())
	}
}

func TestClankViewEmptyTimelinePlaceholder(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 20)
	out := c.View()
	if !strings.Contains(out, "Nothing here yet") {
		t.Errorf("expected empty-state title in output:\n%s", out)
	}
	if !strings.Contains(out, "Hold space to talk to Clank.") {
		t.Errorf("expected empty-state subtitle in output:\n%s", out)
	}
}

// TestClankView_EmptyStateIsCenteredAndBordered ensures the empty
// state isn't crammed into the top-left corner: it should be inside a
// rounded-bordered box, padded down from the header, and not appear on
// the very first content row of the timeline area.
func TestClankView_EmptyStateIsCenteredAndBordered(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 20)
	out := c.View()

	// Rounded border glyph (top-left corner of lipgloss.RoundedBorder).
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╮") {
		t.Errorf("expected rounded border around empty-state card:\n%s", out)
	}

	lines := strings.Split(out, "\n")
	// Find the line containing the empty-state title.
	titleRow := -1
	for i, ln := range lines {
		if strings.Contains(ln, "Nothing here yet") {
			titleRow = i
			break
		}
	}
	if titleRow < 0 {
		t.Fatalf("empty-state title not found in output:\n%s", out)
	}
	// Header occupies row 0 and hint row 2; the card title should be
	// well below them rather than glued to the top of the pane.
	if titleRow < 5 {
		t.Errorf("empty-state title at row %d — expected vertically centered (>=5):\n%s", titleRow, out)
	}
	// Title line should have leading whitespace from horizontal centering.
	titleLine := lines[titleRow]
	if !strings.HasPrefix(titleLine, "  ") {
		t.Errorf("empty-state title line not horizontally indented: %q", titleLine)
	}
}

// TestClankView_HorizontalMarginScalesWithWidth: header should be
// indented from the left edge by ~14 % of pane width, never less than
// the floor and never more than the ceiling.
func TestClankView_HorizontalMarginScalesWithWidth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		width   int
		wantMin int // minimum leading-space count on header line
	}{
		{width: 40, wantMin: 2},
		{width: 100, wantMin: 10},
	}
	for _, tc := range cases {
		c := newClankView()
		c.SetSize(tc.width, 20)
		out := c.View()
		first := strings.Split(out, "\n")[0]
		got := len(first) - len(strings.TrimLeft(first, " "))
		if got < tc.wantMin {
			t.Errorf("width=%d: header indent=%d, want >= %d (line=%q)",
				tc.width, got, tc.wantMin, first)
		}
	}
}

func TestClankView_CoalescesStreamingTranscript(t *testing.T) {
	t.Parallel()

	c := newClankView()
	chunks := []string{"Hi", "!", " Great", " to", " have"}
	for _, ch := range chunks {
		c.AppendTranscript(ch, false, agent.VoiceRoleAssistant)
	}
	c.AppendTranscript(" you.", true, agent.VoiceRoleAssistant)

	if got := len(c.entries); got != 1 {
		t.Fatalf("entries len = %d, want 1 (coalesced); entries=%+v", got, c.entries)
	}
	want := "Hi! Great to have you."
	if c.entries[0].text != want {
		t.Errorf("text = %q, want %q", c.entries[0].text, want)
	}
	if !c.entries[0].done {
		t.Errorf("expected done=true after final chunk")
	}
}

func TestClankView_BoundaryBetweenSpeakers(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.AppendTranscript("hello", false, agent.VoiceRoleAssistant)
	// User chunk must not coalesce into the still-open assistant entry.
	c.AppendTranscript("hi back", false, agent.VoiceRoleUser)

	if got := len(c.entries); got != 2 {
		t.Fatalf("entries len = %d, want 2 (no cross-speaker coalesce)", got)
	}
	if c.entries[0].role != agent.VoiceRoleAssistant || c.entries[1].role != agent.VoiceRoleUser {
		t.Errorf("roles = %v / %v, want assistant/user", c.entries[0].role, c.entries[1].role)
	}
}

func TestClankView_NewUtteranceAfterDone(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.AppendTranscript("first.", true, agent.VoiceRoleAssistant)
	c.AppendTranscript("second.", true, agent.VoiceRoleAssistant)
	if got := len(c.entries); got != 2 {
		t.Errorf("entries len = %d, want 2 (done closes utterance)", got)
	}
}

func TestClankView_SetEntriesCoalescesSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Now()
	c := newClankView()
	c.SetEntries([]voice.Entry{
		{Kind: voice.EntryKindTranscript, Text: "Hi", Role: agent.VoiceRoleAssistant, Timestamp: now},
		{Kind: voice.EntryKindTranscript, Text: "!", Role: agent.VoiceRoleAssistant, Timestamp: now},
		{Kind: voice.EntryKindTranscript, Text: " Great", Done: true, Role: agent.VoiceRoleAssistant, Timestamp: now},
		{Kind: voice.EntryKindTranscript, Text: "ok", Role: agent.VoiceRoleUser, Timestamp: now, Done: true},
	})
	if got := len(c.entries); got != 2 {
		t.Fatalf("entries len = %d, want 2 (snapshot coalesce); entries=%+v", got, c.entries)
	}
	if c.entries[0].text != "Hi! Great" {
		t.Errorf("first entry text = %q, want %q", c.entries[0].text, "Hi! Great")
	}
	if c.entries[1].role != agent.VoiceRoleUser || c.entries[1].text != "ok" {
		t.Errorf("second entry = %+v, want user/ok", c.entries[1])
	}
}

func TestClankView_RendersSpeakerPrefix(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 20)
	c.AppendTranscript("hello", true, agent.VoiceRoleUser)
	c.AppendTranscript("hi back", true, agent.VoiceRoleAssistant)
	out := c.View()
	if !strings.Contains(out, "you") {
		t.Errorf("missing 'you' prefix:\n%s", out)
	}
	if !strings.Contains(out, "clank") {
		t.Errorf("missing 'clank' prefix:\n%s", out)
	}
}

func TestClankViewScrollUpDisengagesFollow(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 12) // small viewport so scroll is meaningful
	for i := 0; i < 30; i++ {
		// done=true so each chunk is a new entry rather than coalescing.
		c.AppendTranscript("line", true, agent.VoiceRoleAssistant)
	}
	// Force a render so follow clamps scroll to bottom.
	_ = c.View()
	if !c.follow {
		t.Fatal("expected follow=true after initial render")
	}
	// Update has a value receiver; capture the returned model.
	c, _ = c.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if c.follow {
		t.Errorf("expected follow=false after scrolling up")
	}
}

// TestClankView_LongTranscriptWrapsWithinPaneWidth: a long utterance
// must wrap so no rendered line exceeds the pane width (no clipping
// at the right edge), and the wrapped continuation lines must be
// indented to the body column (no margin violations on the left).
func TestClankView_LongTranscriptWrapsWithinPaneWidth(t *testing.T) {
	t.Parallel()

	const paneWidth = 80
	c := newClankView()
	c.SetSize(paneWidth, 30)
	long := strings.Repeat("the quick brown fox jumps over the lazy dog ", 6)
	c.AppendTranscript(long, true, agent.VoiceRoleAssistant)

	out := c.View()
	margin := c.horizontalMargin()
	maxLineWidth := paneWidth // padded output should never exceed pane width

	transcriptLines := 0
	for _, ln := range strings.Split(out, "\n") {
		w := ansi.StringWidth(ln)
		if w > maxLineWidth {
			t.Errorf("line wider than pane (%d > %d): %q", w, maxLineWidth, ln)
		}
		if strings.Contains(ln, "fox") || strings.Contains(ln, "lazy") || strings.Contains(ln, "quick") {
			transcriptLines++
			// Every transcript line (initial + continuations) should
			// start with at least the configured horizontal margin.
			leading := len(ln) - len(strings.TrimLeft(ln, " "))
			if leading < margin {
				t.Errorf("transcript line under-indented: leading=%d < margin=%d  line=%q", leading, margin, ln)
			}
		}
	}
	if transcriptLines < 2 {
		t.Errorf("expected long transcript to wrap onto multiple lines, got %d:\n%s", transcriptLines, out)
	}
}
