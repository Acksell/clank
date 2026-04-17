package hub

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HUB
// registerRoutes sets up the HTTP handlers on the mux.
func (s *Service) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ping", s.handlePing)
	mux.HandleFunc("POST /sessions", s.handleCreateSession)
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /sessions/search", s.handleSearchSessions)
	mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
	mux.HandleFunc("GET /sessions/{id}/messages", s.handleGetSessionMessages)
	mux.HandleFunc("POST /sessions/{id}/message", s.handleSendMessage)
	mux.HandleFunc("POST /sessions/{id}/revert", s.handleRevertSession)
	mux.HandleFunc("POST /sessions/{id}/fork", s.handleForkSession)
	mux.HandleFunc("POST /sessions/{id}/abort", s.handleAbortSession)
	mux.HandleFunc("POST /sessions/{id}/read", s.handleMarkSessionRead)
	mux.HandleFunc("POST /sessions/{id}/followup", s.handleToggleFollowUp)
	mux.HandleFunc("POST /sessions/{id}/visibility", s.handleSetVisibility)
	mux.HandleFunc("POST /sessions/{id}/draft", s.handleSetDraft)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("POST /sessions/{id}/permissions/{permID}/reply", s.handlePermissionReply)
	mux.HandleFunc("GET /sessions/{id}/pending-permission", s.handleGetPendingPermission)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /agents", s.handleListAgents)
	mux.HandleFunc("GET /models", s.handleListModels)
	mux.HandleFunc("POST /sessions/discover", s.handleDiscoverSessions)
	// Worktree / branch endpoints.
	mux.HandleFunc("GET /branches", s.handleListBranches)
	mux.HandleFunc("POST /worktrees", s.handleCreateWorktree)
	mux.HandleFunc("DELETE /worktrees", s.handleRemoveWorktree)
	mux.HandleFunc("POST /worktrees/merge", s.handleMergeWorktree)

	// Phase 3B: host- and repo-scoped routes. Path parameters carry
	// the identity (no filesystem paths on the wire). Branch arrives
	// in body or query because branch names contain "/".
	mux.HandleFunc("GET /hosts/{hostID}/repos", s.handleListReposOnHost)
	mux.HandleFunc("GET /hosts/{hostID}/repos/{repoID}/branches", s.handleListBranchesOnRepo)
	mux.HandleFunc("POST /hosts/{hostID}/repos/{repoID}/worktrees", s.handleCreateWorktreeOnRepo)
	mux.HandleFunc("DELETE /hosts/{hostID}/repos/{repoID}/worktrees", s.handleRemoveWorktreeOnRepo)
	mux.HandleFunc("POST /hosts/{hostID}/repos/{repoID}/worktrees/merge", s.handleMergeBranchOnRepo)

	// Voice endpoints.
	mux.HandleFunc("GET /voice/audio", s.handleVoiceAudio)
	mux.HandleFunc("GET /voice/status", s.handleVoiceStatus)
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
