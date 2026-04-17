package hub

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/git"
	"github.com/oklog/ulid/v2"
)

// HUB
func (s *Service) handleDiscoverSessions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectDir string `json:"project_dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.ProjectDir == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_dir is required"})
		return
	}
	// Fan out to every backend the host knows about and accumulate
	// snapshots. Discovery is best-effort per backend — failures are
	// logged but do not abort the whole call.
	var snapshots []agent.SessionSnapshot
	backends, err := s.hostClient.ListBackends(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, bi := range backends {
		found, err := s.hostClient.DiscoverSessions(r.Context(), bi.Name, body.ProjectDir)
		if err != nil {
			s.log.Printf("discover sessions (%s): %v", bi.Name, err)
			continue
		}
		snapshots = append(snapshots, found...)
	}
	if snapshots == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"discovered": 0, "total": 0})
		return
	}

	// Build a worktree path → branch name map so we can attribute discovered
	// sessions to the correct worktree. This lets the TUI filter sessions by
	// worktree directory (ProjectDir).
	wtPathToBranch := make(map[string]string)
	worktrees, err := git.ListWorktrees(body.ProjectDir)
	if err == nil {
		for _, wt := range worktrees {
			if !wt.Bare && wt.Branch != "" {
				wtPathToBranch[wt.Path] = wt.Branch
			}
		}
	}

	// Register discovered sessions, skipping any whose ExternalID already exists
	// (i.e., sessions the daemon is already managing from a previous create or discover).
	// Also check backend.SessionID() to catch sessions whose Start() is still
	// in progress (ExternalID not yet written back to info).
	//
	// For duplicates, refresh backend-owned fields (title, timestamps) from the
	// snapshot while preserving user-owned fields (visibility, follow_up, draft,
	// last_read_at) from the in-memory/DB copy.
	added := 0
	s.mu.Lock()
	for _, snap := range snapshots {
		var existingMS *managedSession
		for _, existing := range s.sessions {
			if existing.info.ExternalID == snap.ID {
				existingMS = existing
				break
			}
			if existing.backend != nil && existing.backend.SessionID() == snap.ID {
				existingMS = existing
				break
			}
		}
		if existingMS != nil {
			// Refresh backend-owned fields from the snapshot.
			existingMS.info.Title = snap.Title
			existingMS.info.CreatedAt = snap.CreatedAt
			existingMS.info.UpdatedAt = snap.UpdatedAt
			existingMS.info.ProjectDir = snap.Directory
			existingMS.info.ProjectName = filepath.Base(snap.Directory)
			existingMS.info.RevertMessageID = snap.RevertMessageID
			// Backfill worktree attribution if not already set.
			if existingMS.info.WorktreeBranch == "" {
				if branch, ok := wtPathToBranch[snap.Directory]; ok {
					existingMS.info.WorktreeBranch = branch
					existingMS.info.WorktreeDir = snap.Directory
				}
			}
			// Normalize stale statuses for backend-less sessions —
			// same rationale as the startup normalization.
			if existingMS.backend == nil && (existingMS.info.Status == agent.StatusBusy || existingMS.info.Status == agent.StatusStarting || existingMS.info.Status == agent.StatusDead) {
				existingMS.info.Status = agent.StatusIdle
			}
			s.persistSession(existingMS)
			continue
		}

		// Attribute the session to a worktree by matching its directory
		// against known worktree paths.
		var wtBranch, wtDir string
		if branch, ok := wtPathToBranch[snap.Directory]; ok {
			wtBranch = branch
			wtDir = snap.Directory
		}

		id := ulid.Make().String()
		info := agent.SessionInfo{
			ID:              id,
			ExternalID:      snap.ID,
			Backend:         agent.BackendOpenCode,
			Status:          agent.StatusIdle,
			ProjectDir:      snap.Directory,
			ProjectName:     filepath.Base(snap.Directory),
			WorktreeBranch:  wtBranch,
			WorktreeDir:     wtDir,
			Title:           snap.Title,
			RevertMessageID: snap.RevertMessageID,
			CreatedAt:       snap.CreatedAt,
			UpdatedAt:       snap.UpdatedAt,
			LastReadAt:      snap.UpdatedAt, // Mark as read — they're not new activity
		}
		s.sessions[id] = &managedSession{info: info, backend: nil}
		s.persistSession(s.sessions[id])
		added++
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"discovered": added,
		"total":      len(snapshots),
	})
}

// HUB
func (s *Service) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req agent.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	info, err := s.createSession(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

// HUB
func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.snapshotSessions())
}

// HUB
func (s *Service) handleSearchSessions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	sinceRaw := r.URL.Query().Get("since")
	untilRaw := r.URL.Query().Get("until")
	visibility := agent.SessionVisibility(r.URL.Query().Get("visibility"))

	if q == "" && sinceRaw == "" && untilRaw == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one of q, since, or until is required"})
		return
	}

	var p agent.SearchParams
	p.Query = q
	p.Visibility = visibility

	if sinceRaw != "" {
		t, err := parseTimeParam(sinceRaw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid since param: " + err.Error()})
			return
		}
		p.Since = t
	}
	if untilRaw != "" {
		t, err := parseTimeParam(untilRaw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid until param: " + err.Error()})
			return
		}
		p.Until = t
	}

	writeJSON(w, http.StatusOK, s.searchSessions(p))
}

// HUB
func (s *Service) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	ms, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	info := ms.info
	if ms.backend != nil {
		info.Status = ms.backend.Status()
	}
	if info.Backend == agent.BackendOpenCode {
		if urls := s.openCodeServerURLs(); urls != nil {
			info.ServerURL = urls[info.ProjectDir]
		}
	}
	writeJSON(w, http.StatusOK, info)
}

// HOST
// activateBackend creates and attaches a backend to a historical session
// (one loaded via discover that has backend == nil). The backend is started
// via Watch() to enable SSE streaming without sending a prompt. An event
// relay goroutine is started so that events from the backend flow through
// the daemon's broadcast system.
func (s *Service) activateBackend(id string, ms *managedSession) error {
	backend, err := s.hostClient.CreateSession(s.ctx, id, agent.StartRequest{
		Backend:    ms.info.Backend,
		ProjectDir: ms.info.ProjectDir,
		SessionID:  ms.info.ExternalID,
	})
	if err != nil {
		return fmt.Errorf("activate backend: %w", err)
	}

	// Start watching for events (SSE) without sending a prompt.
	if err := backend.Watch(s.ctx); err != nil {
		return fmt.Errorf("watch backend: %w", err)
	}

	s.mu.Lock()
	ms.backend = backend
	ms.watchOnly = true
	s.mu.Unlock()

	// Start event relay goroutine so backend events flow through broadcast.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for evt := range backend.Events() {
			evt.SessionID = id
			s.broadcast(evt)

			if evt.Type == agent.EventStatusChange {
				if data, ok := evt.Data.(agent.StatusChangeData); ok {
					s.updateSessionStatus(id, data.NewStatus)
				}
			}
			if evt.Type == agent.EventTitleChange {
				if data, ok := evt.Data.(agent.TitleChangeData); ok {
					s.updateSessionTitle(id, data.Title)
				}
			}
			if evt.Type == agent.EventRevertChange {
				if data, ok := evt.Data.(agent.RevertChangeData); ok {
					s.updateSessionRevert(id, data.MessageID)
				}
			}
			if evt.Type == agent.EventPermission {
				if data, ok := evt.Data.(agent.PermissionData); ok {
					s.mu.Lock()
					ms.pendingPerms = append(ms.pendingPerms, data)
					s.mu.Unlock()
				}
			}
		}
	}()

	return nil
}

// HUB
func (s *Service) handleGetSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	ms, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if ms.backend == nil {
		// Historical session — activate a read-only backend to fetch messages.
		if err := s.activateBackend(id, ms); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	messages, err := ms.backend.Messages(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if messages == nil {
		messages = []agent.MessageData{}
	}
	writeJSON(w, http.StatusOK, messages)
}

// HUB
func (s *Service) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text  string               `json:"text"`
		Agent string               `json:"agent"`
		Model *agent.ModelOverride `json:"model,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}

	s.mu.RLock()
	ms, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	// Update the session's current agent if one was specified, clear any draft,
	// and reset visibility if the session was hidden (user re-engaging means
	// it's no longer done/archived).
	s.mu.Lock()
	if body.Agent != "" {
		ms.info.Agent = body.Agent
	}
	ms.info.Draft = ""
	if ms.info.Visibility == agent.VisibilityDone || ms.info.Visibility == agent.VisibilityArchived {
		ms.info.Visibility = agent.VisibilityVisible
	}
	s.persistSession(ms)
	s.mu.Unlock()

	if ms.backend == nil {
		// Historical session with no backend — create one and start it with
		// the follow-up prompt. Start() handles resume: it skips Session.New()
		// because sessionID is already set, starts SSE, then sends the prompt.
		req := agent.StartRequest{
			Backend:    ms.info.Backend,
			ProjectDir: ms.info.ProjectDir,
			SessionID:  ms.info.ExternalID,
			Prompt:     body.Text,
			Agent:      body.Agent,
			Model:      body.Model,
		}
		backend, err := s.hostClient.CreateSession(s.ctx, id, req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		s.mu.Lock()
		ms.backend = backend
		ms.watchOnly = false
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runBackend(id, ms, req)
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
		return
	}

	if ms.watchOnly {
		// Backend was started via Watch() (read-only observation). Try
		// SendMessage first — OpenCode supports this. If it fails (e.g.,
		// Claude), fall back to stopping the watch-only backend and starting
		// a fresh one via Start().
		s.mu.Lock()
		ms.watchOnly = false
		s.mu.Unlock()
	}

	opts := agent.SendMessageOpts{
		Text:  body.Text,
		Agent: body.Agent,
		Model: body.Model,
	}

	// Dispatch asynchronously — SendMessage blocks until the LLM responds.
	// The TUI tracks progress via the SSE event stream instead.
	go func() {
		if err := ms.backend.SendMessage(s.ctx, opts); err != nil {
			s.log.Printf("session %s: send message error: %v", id, err)
			s.broadcast(agent.Event{
				Type:      agent.EventError,
				SessionID: id,
				Timestamp: time.Now(),
				Data:      agent.ErrorData{Message: err.Error()},
			})
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
}

// HOST
func (s *Service) handleAbortSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	ms, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if ms.backend == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no active backend"})
		return
	}

	if err := ms.backend.Abort(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

// HOST
func (s *Service) handleRevertSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if body.MessageID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message_id is required"})
		return
	}

	s.mu.RLock()
	ms, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if ms.backend == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no active backend"})
		return
	}

	if err := ms.backend.Revert(r.Context(), body.MessageID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	ms.info.RevertMessageID = body.MessageID
	s.persistSession(ms)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reverted"})
}

// HUB
func (s *Service) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	// message_id is optional: empty means "fork the entire session".

	s.mu.RLock()
	ms, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if ms.backend == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "session has no active backend"})
		return
	}

	// Ask the backend to fork — returns the new session's external ID and title.
	forkResult, err := ms.backend.Fork(r.Context(), body.MessageID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Create a new managed session for the fork.
	newID := ulid.Make().String()
	now := time.Now()
	newInfo := agent.SessionInfo{
		ID:          newID,
		ExternalID:  forkResult.ID,
		Backend:     ms.info.Backend,
		Status:      agent.StatusIdle,
		ProjectDir:  ms.info.ProjectDir,
		ProjectName: ms.info.ProjectName,
		Title:       forkResult.Title,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	newMS := &managedSession{info: newInfo}

	s.mu.Lock()
	s.sessions[newID] = newMS
	s.persistSession(newMS)
	s.mu.Unlock()

	// Activate the backend so the forked session can stream events and accept prompts.
	if err := s.activateBackend(newID, newMS); err != nil {
		log.Printf("[daemon] fork: failed to activate backend for %s: %v", newID, err)
		// Session is persisted but backend is inactive; user can still navigate to it.
	}

	s.broadcast(agent.Event{
		Type:      agent.EventSessionCreate,
		SessionID: newID,
		Timestamp: now,
		Data:      newInfo,
	})

	writeJSON(w, http.StatusOK, newInfo)
}

// HUB
func (s *Service) handleMarkSessionRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	ms, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.LastReadAt = time.Now()
	s.persistSession(ms)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HUB
func (s *Service) handleToggleFollowUp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	ms, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.FollowUp = !ms.info.FollowUp
	followUp := ms.info.FollowUp
	s.persistSession(ms)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"follow_up": followUp})
}

// HUB
func (s *Service) handleSetVisibility(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Visibility agent.SessionVisibility `json:"visibility"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	switch body.Visibility {
	case agent.VisibilityVisible, agent.VisibilityDone, agent.VisibilityArchived:
		// valid
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid visibility: %q", body.Visibility)})
		return
	}

	s.mu.Lock()
	ms, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.Visibility = body.Visibility
	s.persistSession(ms)
	s.mu.Unlock()
}

// HUB
func (s *Service) handleSetDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Draft string `json:"draft"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	s.mu.Lock()
	ms, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.Draft = body.Draft
	s.persistSession(ms)
	s.mu.Unlock()
}

// HUB
func (s *Service) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.mu.Lock()
	ms, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	delete(s.sessions, id)
	s.deletePersistedSession(id)
	s.mu.Unlock()

	// Free the host registry slot. StopSession is a no-op if the session
	// was never registered (historical session that was never activated).
	if ms.backend != nil {
		if err := s.hostClient.StopSession(s.ctx, id); err != nil {
			s.log.Printf("error stopping session %s on host: %v", id, err)
		}
	}

	s.broadcast(agent.Event{
		Type:      agent.EventSessionDelete,
		SessionID: id,
		Timestamp: time.Now(),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// HUB
// snapshotSessions returns a point-in-time copy of all session infos
// with live status from backends and populated ServerURL for OpenCode sessions.
func (s *Service) snapshotSessions() []agent.SessionInfo {
	serverURLs := s.openCodeServerURLs()

	s.mu.RLock()
	defer s.mu.RUnlock()
	sessions := make([]agent.SessionInfo, 0, len(s.sessions))
	for _, ms := range s.sessions {
		info := ms.info
		if ms.backend != nil {
			info.Status = ms.backend.Status()
		}
		if info.Backend == agent.BackendOpenCode && serverURLs != nil {
			info.ServerURL = serverURLs[info.ProjectDir]
		}
		sessions = append(sessions, info)
	}
	return sessions
}

// HUB
// searchSessions returns sessions matching the given search parameters.
//
// Query supports pipe-separated OR groups: "auth bug|dark mode" matches
// sessions containing ("auth" AND "bug") OR ("dark" AND "mode"). All
// matching is case-insensitive substring matching against the concatenation
// of title, prompt, draft, and project_name.
//
// Since/Until filter on UpdatedAt. Results are sorted by updated_at descending.
func (s *Service) searchSessions(p agent.SearchParams) []agent.SessionInfo {
	// Parse OR groups from the query. Each group is a slice of AND terms.
	var orGroups [][]string
	if p.Query != "" {
		for _, group := range strings.Split(p.Query, "|") {
			terms := strings.Fields(strings.ToLower(strings.TrimSpace(group)))
			if len(terms) > 0 {
				orGroups = append(orGroups, terms)
			}
		}
	}

	hasQuery := len(orGroups) > 0
	hasSince := !p.Since.IsZero()
	hasUntil := !p.Until.IsZero()

	all := s.snapshotSessions()
	results := make([]agent.SessionInfo, 0)
	for _, si := range all {
		// Visibility filter.
		switch p.Visibility {
		case agent.VisibilityAll:
			// No filter — include everything.
		case agent.VisibilityDone, agent.VisibilityArchived:
			if si.Visibility != p.Visibility {
				continue
			}
		default:
			// Default ("") — active sessions only.
			if si.Hidden() {
				continue
			}
		}

		// Time filter.
		if hasSince && si.UpdatedAt.Before(p.Since) {
			continue
		}
		if hasUntil && !si.UpdatedAt.Before(p.Until) {
			continue
		}

		// Text filter: match if ANY OR group matches (all terms in the group present).
		if hasQuery {
			hay := strings.ToLower(si.Title + " " + si.Prompt + " " + si.Draft + " " + si.ProjectName)
			matched := false
			for _, terms := range orGroups {
				allMatch := true
				for _, term := range terms {
					if !strings.Contains(hay, term) {
						allMatch = false
						break
					}
				}
				if allMatch {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}

		results = append(results, si)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	return results
}

// HUB
// parseTimeParam parses a time parameter that is either an RFC 3339 timestamp
// or a relative duration suffix (e.g. "7d", "24h") interpreted as "ago from now".
// Supported units: h (hours), d (days).
func parseTimeParam(s string) (time.Time, error) {
	return agent.ParseTimeParam(s)
}

// HUB
// createSession creates a new managed session and starts the backend.
// When req.WorktreeBranch is set, a git worktree is created (or reused) for
// that branch, and the backend is started in the worktree directory instead of
// the original ProjectDir.
func (s *Service) createSession(req agent.StartRequest) (*agent.SessionInfo, error) {
	// Resolve worktree if a branch is requested.
	var wtBranch, worktreeDir string
	if req.WorktreeBranch != "" {
		wt, err := s.resolveWorktree(req.ProjectDir, req.WorktreeBranch)
		if err != nil {
			return nil, fmt.Errorf("resolve worktree for branch %q: %w", req.WorktreeBranch, err)
		}
		wtBranch = req.WorktreeBranch
		worktreeDir = wt
		// Point the backend at the worktree directory so the agent
		// operates on the correct branch.
		req.ProjectDir = wt
	}

	// Hub assigns the session ID up front, then asks the Host to create
	// and register a backend under it. After Phase 2 this is an HTTP
	// round-trip; today it's an in-process call but the call shape is
	// already wire-correct.
	id := ulid.Make().String()
	backend, err := s.hostClient.CreateSession(s.ctx, id, req)
	if err != nil {
		return nil, fmt.Errorf("create session backend: %w", err)
	}

	now := time.Now()

	info := agent.SessionInfo{
		ID:             id,
		Backend:        req.Backend,
		Status:         agent.StatusStarting,
		ProjectDir:     req.ProjectDir,
		ProjectName:    filepath.Base(req.ProjectDir),
		WorktreeBranch: wtBranch,
		WorktreeDir:    worktreeDir,
		Prompt:         req.Prompt,
		TicketID:       req.TicketID,
		Agent:          req.Agent,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	ms := &managedSession{
		info:    info,
		backend: backend,
	}

	s.mu.Lock()
	s.sessions[id] = ms
	s.persistSession(ms)
	s.mu.Unlock()

	// Broadcast session creation.
	s.broadcast(agent.Event{
		Type:      agent.EventSessionCreate,
		SessionID: id,
		Timestamp: now,
		Data:      info,
	})

	// Start the backend in a goroutine.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runBackend(id, ms, req)
	}()

	return &info, nil
}

// HOST
// runBackend starts the backend and relays its events.
func (s *Service) runBackend(id string, ms *managedSession, req agent.StartRequest) {
	// Start relaying events BEFORE calling Start(), because Start() blocks
	// for the entire LLM response (Prompt() is synchronous). Events emitted
	// by the backend's SSE goroutine during Start() must be relayed in real time.
	events := ms.backend.Events()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range events {
			evt.SessionID = id
			s.broadcast(evt)

			if evt.Type == agent.EventStatusChange {
				if data, ok := evt.Data.(agent.StatusChangeData); ok {
					s.updateSessionStatus(id, data.NewStatus)
				}
			}
			if evt.Type == agent.EventTitleChange {
				if data, ok := evt.Data.(agent.TitleChangeData); ok {
					s.updateSessionTitle(id, data.Title)
				}
			}
			if evt.Type == agent.EventRevertChange {
				if data, ok := evt.Data.(agent.RevertChangeData); ok {
					s.updateSessionRevert(id, data.MessageID)
				}
			}
			if evt.Type == agent.EventPermission {
				if data, ok := evt.Data.(agent.PermissionData); ok {
					s.mu.Lock()
					ms.pendingPerms = append(ms.pendingPerms, data)
					s.mu.Unlock()
				}
			}
		}
	}()
	defer func() { <-done }() // wait for relay goroutine to finish

	if err := ms.backend.Start(s.ctx, req); err != nil {
		s.log.Printf("session %s: backend start error: %v", id, err)
		s.updateSessionStatus(id, agent.StatusError)
		s.broadcast(agent.Event{
			Type:      agent.EventError,
			SessionID: id,
			Timestamp: time.Now(),
			Data:      agent.ErrorData{Message: err.Error()},
		})
		return
	}

	// After Start() returns, capture the backend's native session ID so
	// future discover calls can deduplicate against it.
	if extID := ms.backend.SessionID(); extID != "" {
		s.mu.Lock()
		if ms2, ok := s.sessions[id]; ok {
			ms2.info.ExternalID = extID
			s.persistSession(ms2)
		}
		s.mu.Unlock()
	}

	// Backend event channel closed — mark as dead if still busy.
	s.mu.RLock()
	ms2, ok := s.sessions[id]
	s.mu.RUnlock()
	if ok && ms2.backend != nil {
		status := ms2.backend.Status()
		if status == agent.StatusBusy || status == agent.StatusStarting {
			s.updateSessionStatus(id, agent.StatusDead)
		}
	}
}

// HUB
// updateSessionStatus updates the cached status and UpdatedAt.
func (s *Service) updateSessionStatus(id string, status agent.SessionStatus) {
	s.mu.Lock()
	if ms, ok := s.sessions[id]; ok {
		ms.info.Status = status
		ms.info.UpdatedAt = time.Now()
		s.persistSession(ms)
	}
	s.mu.Unlock()
}

// HUB
// updateSessionTitle updates the cached title and UpdatedAt.
func (s *Service) updateSessionTitle(id string, title string) {
	s.mu.Lock()
	if ms, ok := s.sessions[id]; ok {
		ms.info.Title = title
		ms.info.UpdatedAt = time.Now()
		s.persistSession(ms)
	}
	s.mu.Unlock()
}

// HUB
// updateSessionRevert updates the cached revert message ID.
func (s *Service) updateSessionRevert(id string, messageID string) {
	s.mu.Lock()
	if ms, ok := s.sessions[id]; ok {
		ms.info.RevertMessageID = messageID
		s.persistSession(ms)
	}
	s.mu.Unlock()
}
