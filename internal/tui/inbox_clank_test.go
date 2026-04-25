package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// TestInbox_DefaultScreenIsClank verifies the user's chosen landing
// behaviour: opening the inbox drops them on the Clank page, with the
// sidebar focused and its cursor parked on the Clank header row.
func TestInbox_DefaultScreenIsClank(t *testing.T) {
	t.Parallel()

	m := NewInboxModel(nil)
	if m.screen != screenClank {
		t.Errorf("expected default screen=screenClank, got %v", m.screen)
	}
	if m.pane != paneSidebar {
		t.Errorf("expected default pane=paneSidebar, got %v", m.pane)
	}
	if !m.sidebar.CursorOnClank() {
		t.Errorf("expected sidebar cursor on Clank row by default")
	}
}

// TestInbox_EnterOnClankRow_OpensClankScreen mirrors the settings footer
// test: pressing Enter on the Clank header in the sidebar opens the
// Clank page in the right pane and moves focus there.
func TestInbox_EnterOnClankRow_OpensClankScreen(t *testing.T) {
	t.Parallel()

	m := &InboxModel{
		width:  120,
		height: 40,
		screen: screenInbox,
		pane:   paneSidebar,
		clank:  newClankView(),
		sidebar: SidebarModel{
			projectDir: "/tmp/test",
			focused:    true,
			branches:   []host.BranchInfo{{Name: "main"}},
		},
	}
	m.sidebar.cursor = m.sidebar.clankCursorIndex()

	m.handleSidebarKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.screen != screenClank {
		t.Errorf("expected screenClank after Enter on Clank row, got %v", m.screen)
	}
	if m.sidebar.Focused() {
		t.Error("expected sidebar to lose focus when Clank screen opens")
	}
}

// TestInbox_RightArrowOnClankRow_OpensClankScreen confirms the alternative
// activation gesture mirrors Enter.
func TestInbox_RightArrowOnClankRow_OpensClankScreen(t *testing.T) {
	t.Parallel()

	m := &InboxModel{
		width:  120,
		height: 40,
		screen: screenInbox,
		pane:   paneSidebar,
		clank:  newClankView(),
		sidebar: SidebarModel{
			projectDir: "/tmp/test",
			focused:    true,
		},
	}
	m.sidebar.cursor = m.sidebar.clankCursorIndex()

	m.handleSidebarKey(tea.KeyPressMsg{Code: tea.KeyRight})

	if m.screen != screenClank {
		t.Errorf("expected screenClank after right-arrow on Clank row, got %v", m.screen)
	}
}

// TestInbox_NavigatingOffClankRow_ClosesClankScreen mirrors the settings
// hover behaviour: while Clank is showing under sidebar focus, scrolling
// off the Clank row drops the preview back to the inbox list.
func TestInbox_NavigatingOffClankRow_ClosesClankScreen(t *testing.T) {
	t.Parallel()

	m := &InboxModel{
		width:  120,
		height: 40,
		screen: screenClank,
		pane:   paneSidebar,
		clank:  newClankView(),
		sidebar: SidebarModel{
			projectDir: "/tmp/test",
			focused:    true,
			branches:   []host.BranchInfo{{Name: "main"}},
		},
	}
	m.sidebar.cursor = m.sidebar.clankCursorIndex()

	// Press down — cursor moves off the Clank row.
	m.updateClank(tea.KeyPressMsg{Code: tea.KeyDown})

	if m.sidebar.CursorOnClank() {
		t.Fatal("precondition: cursor still on Clank row after Down")
	}
	if m.screen == screenClank {
		t.Errorf("expected Clank screen to close when cursor leaves Clank row")
	}
}

// TestInbox_VoiceEventFeedsClankTimeline verifies the integration that
// makes Clank useful: voice events arriving from the daemon flow into
// the Clank timeline and status, regardless of which screen is active.
func TestInbox_VoiceEventFeedsClankTimeline(t *testing.T) {
	t.Parallel()

	m := &InboxModel{clank: newClankView()}

	m.handleVoiceEvent(agent.Event{
		Type: agent.EventVoiceTranscript,
		Data: agent.VoiceTranscriptData{Text: "hello clank", Done: true},
	})
	m.handleVoiceEvent(agent.Event{
		Type: agent.EventVoiceToolCall,
		Data: agent.VoiceToolCallData{Name: "list_sessions", Args: `{"limit":5}`},
	})
	m.handleVoiceEvent(agent.Event{
		Type: agent.EventVoiceStatus,
		Data: agent.VoiceStatusData{Status: agent.VoiceStatusListening},
	})

	if got := len(m.clank.entries); got != 3 {
		t.Errorf("expected 3 clank entries (transcript+tool+status), got %d", got)
	}
	if m.clank.status != agent.VoiceStatusListening {
		t.Errorf("expected status=listening, got %q", m.clank.status)
	}
}
