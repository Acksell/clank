package hubmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/voice"
)

// handleVoiceStatus returns the current voice session state. The
// websocket audio handler stays on *hub.Service (HandleVoiceAudio)
// because it owns Service-internal singleton state plus a long-lived
// connection; mux.register() delegates the route directly.
func (m *Mux) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	active, status := m.svc.VoiceStatus()
	if !active {
		writeJSON(w, http.StatusOK, map[string]string{"active": "false", "status": string(agent.VoiceStatusIdle)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"active": "true",
		"status": string(status),
	})
}

// voiceTranscriptsResponse is the wire format for GET /voice/transcripts.
// Always returns a non-nil entries slice so clients can JSON-decode
// without nil-checking.
type voiceTranscriptsResponse struct {
	Active  bool          `json:"active"`
	Entries []voice.Entry `json:"entries"`
}

// handleVoiceTranscripts returns a snapshot of the in-memory voice
// activity log so cold-starting TUIs can replay the conversation.
func (m *Mux) handleVoiceTranscripts(w http.ResponseWriter, r *http.Request) {
	entries := m.svc.VoiceTranscripts()
	resp := voiceTranscriptsResponse{
		Active:  entries != nil,
		Entries: entries,
	}
	if resp.Entries == nil {
		resp.Entries = []voice.Entry{}
	}
	writeJSON(w, http.StatusOK, resp)
}
