package hubmux

import (
	"net/http"
	"os"
	"time"

	"github.com/acksell/clank/internal/agent"
)

func (m *Mux) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"pid":     os.Getpid(),
		"uptime":  time.Since(m.svc.StartTime()).String(),
		"version": "0.1.0",
	})
}

func (m *Mux) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"pid":      os.Getpid(),
		"uptime":   time.Since(m.svc.StartTime()).String(),
		"sessions": m.svc.Sessions(),
	})
}

// handleEvents serves the SSE event stream to subscribers.
func (m *Mux) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming not supported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	subID, ch, unsub := m.svc.Subscribe()
	defer unsub()

	writeSSE(w, "connected", map[string]string{"subscriber_id": subID})
	flusher.Flush()

	shutdownCtx := m.svc.ShutdownContext()
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, string(evt.Type), evt)
			flusher.Flush()
		case <-r.Context().Done():
			return
		case <-shutdownCtx.Done():
			return
		}
	}
}

// keep agent import needed for future use; currently the SSE writer
// only stringifies evt.Type, but other handlers read agent types.
var _ agent.EventType
