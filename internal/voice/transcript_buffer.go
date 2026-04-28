package voice

import (
	"sync"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// EntryKind enumerates the kinds of entries stored in the transcript
// buffer. Entries form a single chronological timeline of voice
// activity that the TUI can replay.
type EntryKind string

const (
	EntryKindTranscript EntryKind = "transcript"
	EntryKindToolCall   EntryKind = "tool_call"
	EntryKindStatus     EntryKind = "status"
)

// Entry is a single item in the voice transcript buffer.
//
// For transcript entries, multiple chunks with Done=false followed by
// a Done=true chunk represent a single utterance streamed
// incrementally. The TUI is responsible for collapsing them.
type Entry struct {
	Kind      EntryKind         `json:"kind"`
	Timestamp time.Time         `json:"timestamp"`
	Text      string            `json:"text,omitempty"` // transcript chunk
	Done      bool              `json:"done,omitempty"` // transcript: end-of-utterance marker
	Role      agent.VoiceRole   `json:"role,omitempty"` // transcript speaker (user/assistant)
	ToolName  string            `json:"tool_name,omitempty"`
	ToolArgs  string            `json:"tool_args,omitempty"`
	Status    agent.VoiceStatus `json:"status,omitempty"`
}

// transcriptBufferCapacity is the maximum number of entries retained
// in memory. Older entries are dropped when the buffer overflows.
const transcriptBufferCapacity = 1000

// TranscriptBuffer is a bounded, thread-safe ring buffer of voice
// activity entries. It lives on the daemon and is exposed via the hub
// API so cold-starting TUIs can replay the conversation.
type TranscriptBuffer struct {
	mu      sync.Mutex
	entries []Entry
	cap     int
}

// NewTranscriptBuffer constructs a buffer with the default capacity.
func NewTranscriptBuffer() *TranscriptBuffer {
	return &TranscriptBuffer{cap: transcriptBufferCapacity}
}

// Append adds an entry, evicting the oldest if at capacity.
func (b *TranscriptBuffer) Append(e Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	if len(b.entries) >= b.cap {
		// Drop oldest. Copy to avoid unbounded slice growth.
		copy(b.entries, b.entries[1:])
		b.entries = b.entries[:len(b.entries)-1]
	}
	b.entries = append(b.entries, e)
}

// Snapshot returns a copy of all current entries in chronological order.
func (b *TranscriptBuffer) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, len(b.entries))
	copy(out, b.entries)
	return out
}

// Len returns the current number of entries (test helper).
func (b *TranscriptBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}
