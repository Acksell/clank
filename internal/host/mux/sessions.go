package hostmux

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/acksell/clank/internal/agent"
)

// SessionSnapshot summarizes a registered session for HTTP responses. It
// is the lighter shape returned by GET /sessions/{id} and POST
// /sessions/{id}/open — endpoints where the caller already has the full
// agent.SessionInfo and only needs to refresh runtime fields.
//
// ServerURL is populated on POST /sessions/{id}/open responses for
// backends that expose an HTTP server (currently only OpenCode); empty
// otherwise.
type SessionSnapshot struct {
	SessionID  string              `json:"session_id"`
	ExternalID string              `json:"external_id"`
	Status     agent.SessionStatus `json:"status"`
	ServerURL  string              `json:"server_url,omitempty"`
}

// HOST
//
// POST /sessions accepts agent.StartRequest directly, generates a fresh
// session ID, and returns the persisted agent.SessionInfo. The host owns
// session ID generation post-PR-3 (the hub used to do this).
func (m *Mux) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req agent.StartRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	sessionID := ulid.Make().String()
	b, serverURL, err := m.svc.CreateSession(r.Context(), sessionID, req)
	if err != nil {
		writeError(w, err)
		return
	}

	// Dispatch the initial prompt. OpenAndSend is the create-and-go
	// contract the hub used to invoke; with the hub gone the host owns
	// it. Open is synchronous (creates the opencode session and stamps
	// b.SessionID); Send is fire-and-forget — the prompt runs on the
	// backend's long-lived context, not the HTTP request's. Errors
	// here mean the LLM-side session never opened: we tear down the
	// host-side registration so the user can retry instead of leaving
	// a "starting" zombie.
	if err := b.OpenAndSend(r.Context(), agent.SendMessageOpts{
		Text:  req.Prompt,
		Agent: req.Agent,
		Model: req.Model,
	}); err != nil {
		_ = m.svc.StopSession(sessionID)
		writeError(w, fmt.Errorf("open session: %w", err))
		return
	}

	now := time.Now()
	info := agent.SessionInfo{
		ID:         sessionID,
		ExternalID: b.SessionID(),
		Backend:    req.Backend,
		Status:     b.Status(),
		Hostname:   req.Hostname,
		GitRef:     req.GitRef,
		Prompt:     req.Prompt,
		TicketID:   req.TicketID,
		Agent:      req.Agent,
		ServerURL:  serverURL,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	writeJSON(w, http.StatusCreated, info)
}

// HOST
//
// GET /sessions/{id} returns the persisted agent.SessionInfo, augmented
// with runtime fields (status, external_id) from the live backend if one
// is registered. Returns 404 when the session is not in the store and
// not in the live registry.
func (m *Mux) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, err := m.svc.GetSessionMetadata(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if b, ok := m.svc.Session(id); ok {
		info.Status = b.Status()
		if extID := b.SessionID(); extID != "" {
			info.ExternalID = extID
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// HOST
//
// handleSessionSnapshot is the lightweight response shape used by
// internal/host/client (the in-process / host-supervisor path). The
// gateway-facing GET /sessions/{id} routes to handleGetSession instead.
func (m *Mux) handleSessionSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionSnapshot{
		SessionID:  id,
		ExternalID: b.SessionID(),
		Status:     b.Status(),
	})
}

// HOST
func (m *Mux) handleOpenSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := b.Open(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	// Return the post-Open snapshot so the client can refresh its
	// cached ExternalID/Status. The ID may not be known yet for
	// async-init backends (e.g. Claude); the caller will pick it up
	// later via Event.ExternalID on the SSE stream.
	writeJSON(w, http.StatusOK, SessionSnapshot{
		SessionID:  id,
		ExternalID: b.SessionID(),
		Status:     b.Status(),
	})
}

// HOST
func (m *Mux) handleSendSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var opts agent.SendMessageOpts
	if err := decodeJSON(r.Body, &opts); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := b.Send(r.Context(), opts); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HOST
func (m *Mux) handleOpenAndSendSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var opts agent.SendMessageOpts
	if err := decodeJSON(r.Body, &opts); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := b.OpenAndSend(r.Context(), opts); err != nil {
		writeError(w, err)
		return
	}
	// Return the post-OpenAndSend snapshot so the client can refresh
	// its cached ExternalID/Status. Some backends (opencode) learn
	// their sessionID inside Open synchronously; without this the
	// client keeps the empty ExternalID it received from POST
	// /sessions and persists external_id="" on the Hub. Async-init
	// backends (claude) still rely on Event.ExternalID.
	writeJSON(w, http.StatusOK, SessionSnapshot{
		SessionID:  id,
		ExternalID: b.SessionID(),
		Status:     b.Status(),
	})
}

// HOST
func (m *Mux) handleAbortSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := b.Abort(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevertRequest is the body for POST /sessions/{id}/revert.
type RevertRequest struct {
	MessageID string `json:"message_id"`
}

// HOST
func (m *Mux) handleRevertSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var req RevertRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.MessageID == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "message_id is required"})
		return
	}
	if err := b.Revert(r.Context(), req.MessageID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ForkRequest is the body for POST /sessions/{id}/fork.
type ForkRequest struct {
	MessageID string `json:"message_id"`
}

// HOST
func (m *Mux) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var req ForkRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	// Empty MessageID is valid: it forks the entire session from
	// scratch. Backends interpret it as "no truncation point".
	res, err := b.Fork(r.Context(), req.MessageID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// HOST
func (m *Mux) handleGetMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	msgs, err := b.Messages(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// PermissionReplyRequest is the body for POST
// /sessions/{id}/permissions/{permID}/reply.
type PermissionReplyRequest struct {
	Allow bool `json:"allow"`
}

// HOST
//
// GET /sessions/{id}/pending-permission returns permission requests
// the agent is currently blocked on. Pre-PR-3 the hub kept these in
// memory; the TUI used the endpoint to recover after a reconnect.
//
// The host doesn't currently snapshot pending perms — the SSE stream
// is the canonical source. We return an empty list so the TUI's recovery
// path doesn't 404; live permission prompts arrive via /events as
// EventPermission. A persistent snapshot lives behind a future PR.
func (m *Mux) handlePendingPermissions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Use GetSessionMetadata (store-only, no rehydrate) — listing
	// pending perms shouldn't spawn an opencode subprocess just to
	// return an empty array.
	if _, err := m.svc.GetSessionMetadata(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

// HOST
func (m *Mux) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	permID := r.PathValue("permID")
	b, err := m.svc.ResumeSession(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var req PermissionReplyRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := b.RespondPermission(r.Context(), permID, req.Allow); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HOST
func (m *Mux) handleStopSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := m.svc.StopSession(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HOST
//
// handleSessionEvents bridges agent events to Server-Sent Events,
// filtered to a single session id. PR 3 changed the underlying
// source: previously this read directly from SessionBackend.Events(),
// but now the host's per-session relay goroutine is the sole consumer
// of that channel. We subscribe to the global broadcast and filter
// client-side instead, which lets multiple consumers (this SSE, the
// global /events WebSocket, future watchers) all receive the stream.
//
// Each event is encoded as `event: <type>\ndata: <json>\n\n`.
func (m *Mux) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Subscribe-only: just confirm the session exists (in store or
	// live registry) — don't rehydrate the backend on the SSE path.
	// Events for resumed sessions only flow once another caller
	// invokes Send/Messages/etc, which triggers the rehydrate.
	if _, err := m.svc.GetSessionMetadata(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	subID, ch := m.svc.Subscribe()
	defer m.svc.Unsubscribe(subID)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				// Subscriber channel closed (host shutting down).
				fmt.Fprintf(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			if ev.SessionID != id {
				continue
			}
			data, err := json.Marshal(ev)
			if err != nil {
				m.log.Printf("hostmux: marshal event for %s: %v", id, err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}
