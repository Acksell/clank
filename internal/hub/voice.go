package hub

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/voice"
	"github.com/coder/websocket"
)

// handleVoiceAudio upgrades to a WebSocket for bidirectional audio
// streaming, then creates the voice session immediately.
//
// Protocol:
//   - Client → Server text:   {"type":"end"} (end of audio sequence)
//   - Client → Server binary: PCM audio chunks (24kHz 16-bit signed LE mono)
//   - Server → Client binary: PCM audio chunks (speaker)
//   - Server → Client binary (len=0): flush signal (barge-in)
//
// There is no explicit start signal — the agent infers the start of a
// new audio sequence from the first binary data frame after an end.
//
// Only one voice session can be active at a time (singleton). The
// session is torn down when the WebSocket disconnects.
func (s *Service) handleVoiceAudio(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.voice != nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "voice session already active"})
		return
	}
	s.mu.Unlock()

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "OPENAI_API_KEY environment variable is not set"})
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// No origin check — Unix socket is already authenticated.
	})
	if err != nil {
		s.log.Printf("voice audio websocket accept: %v", err)
		return
	}

	// Increase read limit for audio frames.
	conn.SetReadLimit(256 * 1024)

	source, sink := voice.NewWSAudioAdapters(conn)
	tp := &hubToolProvider{s: s}

	// Use the daemon's long-lived context, not the HTTP request context.
	sess, err := voice.NewSession(s.ctx, voice.Config{
		APIKey:       apiKey,
		Source:       source,
		Sink:         sink,
		ToolProvider: tp,
		Broadcast:    s.broadcast,
		Logger:       s.log,
	})
	if err != nil {
		s.log.Printf("voice session create error: %v", err)
		conn.Close(websocket.StatusInternalError, err.Error())
		return
	}

	s.mu.Lock()
	// Re-check under lock to avoid races with concurrent connects.
	if s.voice != nil {
		s.mu.Unlock()
		sess.Close()
		conn.Close(websocket.StatusPolicyViolation, "voice session already active")
		return
	}
	s.voice = sess
	s.voiceAudioConn = conn
	s.mu.Unlock()

	s.log.Println("voice audio WebSocket connected, session created")

	// Monitor voice session in background — clean up when it ends.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := sess.Wait(); err != nil {
			s.log.Printf("voice session ended: %v", err)
		}
		s.mu.Lock()
		if s.voice == sess {
			s.voice = nil
		}
		s.mu.Unlock()
	}()

	// Keep the handler alive until the connection closes — the HTTP
	// handler goroutine must outlive the WebSocket.
	<-r.Context().Done()

	s.mu.Lock()
	if s.voiceAudioConn == conn {
		s.voiceAudioConn = nil
	}
	// Tear down the voice session when the audio WebSocket disconnects.
	activeSess := s.voice
	if activeSess == sess {
		s.voice = nil
	} else {
		activeSess = nil // another session replaced ours
	}
	s.mu.Unlock()

	if activeSess != nil {
		activeSess.Close()
	}
}

// handleVoiceStatus returns the current voice session state.
func (s *Service) handleVoiceStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sess := s.voice
	s.mu.RUnlock()

	if sess == nil {
		writeJSON(w, http.StatusOK, map[string]string{"active": "false", "status": string(agent.VoiceStatusIdle)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"active": "true",
		"status": string(sess.Status()),
	})
}

// hubToolProvider implements voice.ToolProvider using direct
// access to the Service's internal state and methods.
type hubToolProvider struct {
	s *Service
}

func (tp *hubToolProvider) SearchSessions(ctx context.Context, p agent.SearchParams) ([]agent.SessionInfo, error) {
	return tp.s.searchSessions(p), nil
}

func (tp *hubToolProvider) GetSession(ctx context.Context, id string) (*agent.SessionInfo, error) {
	tp.s.mu.RLock()
	ms, ok := tp.s.sessions[id]
	tp.s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	info := ms.info
	if ms.backend != nil {
		info.Status = ms.backend.Status()
	}
	return &info, nil
}

func (tp *hubToolProvider) GetSessionMessages(ctx context.Context, sessionID string) ([]agent.MessageData, error) {
	tp.s.mu.RLock()
	ms, ok := tp.s.sessions[sessionID]
	tp.s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if ms.backend == nil {
		// Try to activate a read-only backend.
		if err := tp.s.activateBackend(sessionID, ms); err != nil {
			return nil, fmt.Errorf("activate backend: %w", err)
		}
	}
	return ms.backend.Messages(ctx)
}

func (tp *hubToolProvider) SendMessage(ctx context.Context, sessionID string, text string) error {
	tp.s.mu.RLock()
	ms, ok := tp.s.sessions[sessionID]
	tp.s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if ms.backend == nil {
		return fmt.Errorf("session %s has no active backend", sessionID)
	}
	go func() {
		if err := ms.backend.SendMessage(tp.s.ctx, agent.SendMessageOpts{Text: text}); err != nil {
			tp.s.log.Printf("voice send_message to %s: %v", sessionID, err)
			tp.s.broadcast(agent.Event{
				Type:      agent.EventError,
				SessionID: sessionID,
				Timestamp: time.Now(),
				Data:      agent.ErrorData{Message: err.Error()},
			})
		}
	}()
	return nil
}

func (tp *hubToolProvider) CreateSession(ctx context.Context, req agent.StartRequest) (*agent.SessionInfo, error) {
	return tp.s.createSession(req)
}

func (tp *hubToolProvider) AbortSession(ctx context.Context, sessionID string) error {
	tp.s.mu.RLock()
	ms, ok := tp.s.sessions[sessionID]
	tp.s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}
	if ms.backend == nil {
		return fmt.Errorf("session %s has no active backend", sessionID)
	}
	return ms.backend.Abort(ctx)
}

func (tp *hubToolProvider) KnownProjectDirs(ctx context.Context) ([]string, error) {
	if tp.s.Store == nil {
		return nil, nil
	}
	seen := make(map[string]struct{})
	backends, err := tp.s.hostClient.ListBackends(ctx)
	if err != nil {
		return nil, fmt.Errorf("list backends: %w", err)
	}
	for _, bi := range backends {
		dirs, err := tp.s.Store.KnownProjectDirs(bi.Name)
		if err != nil {
			return nil, fmt.Errorf("known dirs for %s: %w", bi.Name, err)
		}
		for _, dir := range dirs {
			seen[dir] = struct{}{}
		}
	}
	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs, nil
}
