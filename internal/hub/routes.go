package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HUB
// registerRoutes sets up the HTTP handlers on the mux.
func (d *Daemon) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ping", d.handlePing)
	mux.HandleFunc("POST /sessions", d.handleCreateSession)
	mux.HandleFunc("GET /sessions", d.handleListSessions)
	mux.HandleFunc("GET /sessions/search", d.handleSearchSessions)
	mux.HandleFunc("GET /sessions/{id}", d.handleGetSession)
	mux.HandleFunc("GET /sessions/{id}/messages", d.handleGetSessionMessages)
	mux.HandleFunc("POST /sessions/{id}/message", d.handleSendMessage)
	mux.HandleFunc("POST /sessions/{id}/revert", d.handleRevertSession)
	mux.HandleFunc("POST /sessions/{id}/fork", d.handleForkSession)
	mux.HandleFunc("POST /sessions/{id}/abort", d.handleAbortSession)
	mux.HandleFunc("POST /sessions/{id}/read", d.handleMarkSessionRead)
	mux.HandleFunc("POST /sessions/{id}/followup", d.handleToggleFollowUp)
	mux.HandleFunc("POST /sessions/{id}/visibility", d.handleSetVisibility)
	mux.HandleFunc("POST /sessions/{id}/draft", d.handleSetDraft)
	mux.HandleFunc("DELETE /sessions/{id}", d.handleDeleteSession)
	mux.HandleFunc("GET /events", d.handleEvents)
	mux.HandleFunc("POST /sessions/{id}/permissions/{permID}/reply", d.handlePermissionReply)
	mux.HandleFunc("GET /sessions/{id}/pending-permission", d.handleGetPendingPermission)
	mux.HandleFunc("GET /status", d.handleStatus)
	mux.HandleFunc("GET /agents", d.handleListAgents)
	mux.HandleFunc("GET /models", d.handleListModels)
	mux.HandleFunc("POST /sessions/discover", d.handleDiscoverSessions)
	// Worktree / branch endpoints.
	mux.HandleFunc("GET /branches", d.handleListBranches)
	mux.HandleFunc("POST /worktrees", d.handleCreateWorktree)
	mux.HandleFunc("DELETE /worktrees", d.handleRemoveWorktree)
	mux.HandleFunc("POST /worktrees/merge", d.handleMergeWorktree)

	// Voice endpoints.
	mux.HandleFunc("GET /voice/audio", d.handleVoiceAudio)
	mux.HandleFunc("GET /voice/status", d.handleVoiceStatus)
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeSSE(w io.Writer, event string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
}
