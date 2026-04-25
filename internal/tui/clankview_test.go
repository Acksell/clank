package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/voice"
)

func TestClankViewAppendAndRender(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 20)
	c.AppendTranscript("hello world", true)
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
	if !strings.Contains(out, "No voice activity yet") {
		t.Errorf("expected empty placeholder:\n%s", out)
	}
}

func TestClankViewScrollUpDisengagesFollow(t *testing.T) {
	t.Parallel()

	c := newClankView()
	c.SetSize(80, 12) // small viewport so scroll is meaningful
	for i := 0; i < 30; i++ {
		c.AppendTranscript("line", true)
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
