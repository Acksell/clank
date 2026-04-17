package hub

import (
	"net/http"
	"os"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/oklog/ulid/v2"
)

// HUB
func (s *Service) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"pid":     os.Getpid(),
		"uptime":  time.Since(s.startTime).String(),
		"version": "0.1.0",
	})
}

// HUB
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pid":      os.Getpid(),
		"uptime":   time.Since(s.startTime).String(),
		"sessions": s.snapshotSessions(),
	})
}

// HUB
// handleEvents serves the SSE event stream to subscribers.
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	subID := ulid.Make().String()
	ch := make(chan agent.Event, 64)

	s.subMu.Lock()
	s.subscribers[subID] = ch
	s.subMu.Unlock()

	defer func() {
		s.subMu.Lock()
		delete(s.subscribers, subID)
		s.subMu.Unlock()
	}()

	// Send initial connected event.
	writeSSE(w, "connected", map[string]string{"subscriber_id": subID})
	flusher.Flush()

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return // channel closed, daemon shutting down
			}
			writeSSE(w, string(evt.Type), evt)
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// HUB
// broadcast sends an event to all connected subscribers.
func (s *Service) broadcast(evt agent.Event) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for _, ch := range s.subscribers {
		select {
		case ch <- evt:
		default:
			// Subscriber too slow, drop event to avoid blocking.
		}
	}
}
