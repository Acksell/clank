package daemon

import (
	"encoding/json"
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// HOST
func (d *Daemon) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	permID := r.PathValue("permID")

	var body struct {
		Allow bool `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	d.mu.RLock()
	ms, ok := d.sessions[sessionID]
	d.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if ms.backend == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no active backend"})
		return
	}

	if err := ms.backend.RespondPermission(r.Context(), permID, body.Allow); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	d.mu.Lock()
	// OpenCode may cancel the remaining permission batch after a rejection.
	// Keep the daemon queue aligned with that behavior so reconnect/resync
	// never re-surface stale prompts the backend will not honor.
	if body.Allow {
		filtered := ms.pendingPerms[:0]
		for _, p := range ms.pendingPerms {
			if p.RequestID != permID {
				filtered = append(filtered, p)
			}
		}
		ms.pendingPerms = filtered
	} else {
		ms.pendingPerms = nil
	}
	d.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HUB
func (d *Daemon) handleGetPendingPermission(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	d.mu.RLock()
	ms, ok := d.sessions[sessionID]
	d.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	d.mu.RLock()
	perms := make([]agent.PermissionData, len(ms.pendingPerms))
	copy(perms, ms.pendingPerms)
	d.mu.RUnlock()

	writeJSON(w, http.StatusOK, perms)
}
