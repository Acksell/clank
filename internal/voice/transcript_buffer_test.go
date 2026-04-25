package voice

import (
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
)

func TestTranscriptBufferAppendAndSnapshot(t *testing.T) {
	t.Parallel()

	b := NewTranscriptBuffer()
	b.Append(Entry{Kind: EntryKindTranscript, Text: "hello", Done: true})
	b.Append(Entry{Kind: EntryKindToolCall, ToolName: "list_sessions", ToolArgs: "{}"})
	b.Append(Entry{Kind: EntryKindStatus, Status: agent.VoiceStatusListening})

	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(snap))
	}
	if snap[0].Text != "hello" {
		t.Errorf("entry 0 text = %q, want hello", snap[0].Text)
	}
	if snap[1].ToolName != "list_sessions" {
		t.Errorf("entry 1 tool name = %q", snap[1].ToolName)
	}
	if snap[2].Status != agent.VoiceStatusListening {
		t.Errorf("entry 2 status = %q", snap[2].Status)
	}
	for i, e := range snap {
		if e.Timestamp.IsZero() {
			t.Errorf("entry %d has zero timestamp", i)
		}
	}
}

func TestTranscriptBufferAutoTimestamp(t *testing.T) {
	t.Parallel()

	b := NewTranscriptBuffer()
	before := time.Now()
	b.Append(Entry{Kind: EntryKindTranscript, Text: "x"})
	after := time.Now()

	got := b.Snapshot()[0].Timestamp
	if got.Before(before) || got.After(after) {
		t.Errorf("auto timestamp %v not in [%v, %v]", got, before, after)
	}
}

func TestTranscriptBufferPreservesProvidedTimestamp(t *testing.T) {
	t.Parallel()

	b := NewTranscriptBuffer()
	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	b.Append(Entry{Kind: EntryKindTranscript, Text: "x", Timestamp: ts})

	if got := b.Snapshot()[0].Timestamp; !got.Equal(ts) {
		t.Errorf("timestamp = %v, want %v", got, ts)
	}
}

func TestTranscriptBufferRingEviction(t *testing.T) {
	t.Parallel()

	b := &TranscriptBuffer{cap: 3}
	for i := 0; i < 5; i++ {
		b.Append(Entry{Kind: EntryKindTranscript, Text: string(rune('a' + i))})
	}

	snap := b.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("len = %d, want 3", len(snap))
	}
	wantTexts := []string{"c", "d", "e"}
	for i, e := range snap {
		if e.Text != wantTexts[i] {
			t.Errorf("entry %d text = %q, want %q", i, e.Text, wantTexts[i])
		}
	}
}

func TestTranscriptBufferSnapshotIsCopy(t *testing.T) {
	t.Parallel()

	b := NewTranscriptBuffer()
	b.Append(Entry{Kind: EntryKindTranscript, Text: "original"})

	snap := b.Snapshot()
	snap[0].Text = "mutated"

	if got := b.Snapshot()[0].Text; got != "original" {
		t.Errorf("buffer mutated through snapshot: %q", got)
	}
}
