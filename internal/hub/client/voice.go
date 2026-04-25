package hubclient

import (
	"context"
	"fmt"

	"github.com/acksell/clank/internal/voice"
	"github.com/coder/websocket"
)

// VoiceAudioStream opens a WebSocket connection for bidirectional PCM audio
// streaming. Caller sends mic PCM as binary messages and receives speaker
// PCM back. A zero-length binary message from the server signals a flush
// (barge-in / discard playback buffer). The returned *websocket.Conn must
// be closed by the caller.
func (c *Client) VoiceAudioStream(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, "ws://daemon/voice/audio", &websocket.DialOptions{
		HTTPClient: c.httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("voice audio websocket: %w", err)
	}
	conn.SetReadLimit(256 * 1024)
	return conn, nil
}

// VoiceTranscriptsResponse mirrors the daemon's /voice/transcripts
// JSON shape.
type VoiceTranscriptsResponse struct {
	Active  bool          `json:"active"`
	Entries []voice.Entry `json:"entries"`
}

// VoiceTranscripts fetches a snapshot of the daemon's voice activity
// log so a cold-starting TUI can replay the conversation.
func (c *Client) VoiceTranscripts(ctx context.Context) (*VoiceTranscriptsResponse, error) {
	var resp VoiceTranscriptsResponse
	if err := c.get(ctx, "/voice/transcripts", &resp); err != nil {
		return nil, fmt.Errorf("voice transcripts: %w", err)
	}
	return &resp, nil
}
