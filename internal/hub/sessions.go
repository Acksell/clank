package daemon

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
func (d *Daemon) handleDiscoverSessions(w http.ResponseWriter, r *http.Request) {
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
	backends, err := d.hostClient.ListBackends(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, bi := range backends {
		found, err := d.hostClient.DiscoverSessions(r.Context(), bi.Name, body.ProjectDir)
		if err != nil {
			d.log.Printf("discover sessions (%s): %v", bi.Name, err)
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
	d.mu.Lock()
	for _, snap := range snapshots {
		var existingMS *managedSession
		for _, existing := range d.sessions {
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
			d.persistSession(existingMS)
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
		d.sessions[id] = &managedSession{info: info, backend: nil}
		d.persistSession(d.sessions[id])
		added++
	}
	d.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"discovered": added,
		"total":      len(snapshots),
	})
}

// HUB
func (d *Daemon) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req agent.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	info, err := d.createSession(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, info)
}

// HUB
func (d *Daemon) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.snapshotSessions())
}

// HUB
func (d *Daemon) handleSearchSessions(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, d.searchSessions(p))
}

// HUB
func (d *Daemon) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d.mu.RLock()
	ms, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	info := ms.info
	if ms.backend != nil {
		info.Status = ms.backend.Status()
	}
	if info.Backend == agent.BackendOpenCode {
		if urls := d.openCodeServerURLs(); urls != nil {
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
func (d *Daemon) activateBackend(id string, ms *managedSession) error {
	backend, err := d.hostClient.CreateSession(d.ctx, id, agent.StartRequest{
		Backend:    ms.info.Backend,
		ProjectDir: ms.info.ProjectDir,
		SessionID:  ms.info.ExternalID,
	})
	if err != nil {
		return fmt.Errorf("activate backend: %w", err)
	}

	// Start watching for events (SSE) without sending a prompt.
	if err := backend.Watch(d.ctx); err != nil {
		return fmt.Errorf("watch backend: %w", err)
	}

	d.mu.Lock()
	ms.backend = backend
	ms.watchOnly = true
	d.mu.Unlock()

	// Start event relay goroutine so backend events flow through broadcast.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for evt := range backend.Events() {
			evt.SessionID = id
			d.broadcast(evt)

			if evt.Type == agent.EventStatusChange {
				if data, ok := evt.Data.(agent.StatusChangeData); ok {
					d.updateSessionStatus(id, data.NewStatus)
				}
			}
			if evt.Type == agent.EventTitleChange {
				if data, ok := evt.Data.(agent.TitleChangeData); ok {
					d.updateSessionTitle(id, data.Title)
				}
			}
			if evt.Type == agent.EventRevertChange {
				if data, ok := evt.Data.(agent.RevertChangeData); ok {
					d.updateSessionRevert(id, data.MessageID)
				}
			}
			if evt.Type == agent.EventPermission {
				if data, ok := evt.Data.(agent.PermissionData); ok {
					d.mu.Lock()
					ms.pendingPerms = append(ms.pendingPerms, data)
					d.mu.Unlock()
				}
			}
		}
	}()

	return nil
}

// HUB
func (d *Daemon) handleGetSessionMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d.mu.RLock()
	ms, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	if ms.backend == nil {
		// Historical session — activate a read-only backend to fetch messages.
		if err := d.activateBackend(id, ms); err != nil {
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
func (d *Daemon) handleSendMessage(w http.ResponseWriter, r *http.Request) {
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

	d.mu.RLock()
	ms, ok := d.sessions[id]
	d.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	// Update the session's current agent if one was specified, clear any draft,
	// and reset visibility if the session was hidden (user re-engaging means
	// it's no longer done/archived).
	d.mu.Lock()
	if body.Agent != "" {
		ms.info.Agent = body.Agent
	}
	ms.info.Draft = ""
	if ms.info.Visibility == agent.VisibilityDone || ms.info.Visibility == agent.VisibilityArchived {
		ms.info.Visibility = agent.VisibilityVisible
	}
	d.persistSession(ms)
	d.mu.Unlock()

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
		backend, err := d.hostClient.CreateSession(d.ctx, id, req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		d.mu.Lock()
		ms.backend = backend
		ms.watchOnly = false
		d.mu.Unlock()

		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			d.runBackend(id, ms, req)
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "sent"})
		return
	}

	if ms.watchOnly {
		// Backend was started via Watch() (read-only observation). Try
		// SendMessage first — OpenCode supports this. If it fails (e.g.,
		// Claude), fall back to stopping the watch-only backend and starting
		// a fresh one via Start().
		d.mu.Lock()
		ms.watchOnly = false
		d.mu.Unlock()
	}

	opts := agent.SendMessageOpts{
		Text:  body.Text,
		Agent: body.Agent,
		Model: body.Model,
	}

	// Dispatch asynchronously — SendMessage blocks until the LLM responds.
	// The TUI tracks progress via the SSE event stream instead.
	go func() {
		if err := ms.backend.SendMessage(d.ctx, opts); err != nil {
			d.log.Printf("session %s: send message error: %v", id, err)
			d.broadcast(agent.Event{
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
func (d *Daemon) handleAbortSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d.mu.RLock()
	ms, ok := d.sessions[id]
	d.mu.RUnlock()
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
func (d *Daemon) handleRevertSession(w http.ResponseWriter, r *http.Request) {
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

	d.mu.RLock()
	ms, ok := d.sessions[id]
	d.mu.RUnlock()
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
	d.mu.Lock()
	ms.info.RevertMessageID = body.MessageID
	d.persistSession(ms)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "reverted"})
}

// HUB
func (d *Daemon) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		MessageID string `json:"message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	// message_id is optional: empty means "fork the entire session".

	d.mu.RLock()
	ms, ok := d.sessions[id]
	d.mu.RUnlock()
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

	d.mu.Lock()
	d.sessions[newID] = newMS
	d.persistSession(newMS)
	d.mu.Unlock()

	// Activate the backend so the forked session can stream events and accept prompts.
	if err := d.activateBackend(newID, newMS); err != nil {
		log.Printf("[daemon] fork: failed to activate backend for %s: %v", newID, err)
		// Session is persisted but backend is inactive; user can still navigate to it.
	}

	d.broadcast(agent.Event{
		Type:      agent.EventSessionCreate,
		SessionID: newID,
		Timestamp: now,
		Data:      newInfo,
	})

	writeJSON(w, http.StatusOK, newInfo)
}

// HUB
func (d *Daemon) handleMarkSessionRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d.mu.Lock()
	ms, ok := d.sessions[id]
	if !ok {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.LastReadAt = time.Now()
	d.persistSession(ms)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// HUB
func (d *Daemon) handleToggleFollowUp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d.mu.Lock()
	ms, ok := d.sessions[id]
	if !ok {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.FollowUp = !ms.info.FollowUp
	followUp := ms.info.FollowUp
	d.persistSession(ms)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"follow_up": followUp})
}

// HUB
func (d *Daemon) handleSetVisibility(w http.ResponseWriter, r *http.Request) {
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

	d.mu.Lock()
	ms, ok := d.sessions[id]
	if !ok {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.Visibility = body.Visibility
	d.persistSession(ms)
	d.mu.Unlock()
}

// HUB
func (d *Daemon) handleSetDraft(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Draft string `json:"draft"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	d.mu.Lock()
	ms, ok := d.sessions[id]
	if !ok {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	ms.info.Draft = body.Draft
	d.persistSession(ms)
	d.mu.Unlock()
}

// HUB
func (d *Daemon) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	d.mu.Lock()
	ms, ok := d.sessions[id]
	if !ok {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	delete(d.sessions, id)
	d.deletePersistedSession(id)
	d.mu.Unlock()

	// Free the host registry slot. StopSession is a no-op if the session
	// was never registered (historical session that was never activated).
	if ms.backend != nil {
		if err := d.hostClient.StopSession(d.ctx, id); err != nil {
			d.log.Printf("error stopping session %s on host: %v", id, err)
		}
	}

	d.broadcast(agent.Event{
		Type:      agent.EventSessionDelete,
		SessionID: id,
		Timestamp: time.Now(),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// HUB
// snapshotSessions returns a point-in-time copy of all session infos
// with live status from backends and populated ServerURL for OpenCode sessions.
func (d *Daemon) snapshotSessions() []agent.SessionInfo {
	serverURLs := d.openCodeServerURLs()

	d.mu.RLock()
	defer d.mu.RUnlock()
	sessions := make([]agent.SessionInfo, 0, len(d.sessions))
	for _, ms := range d.sessions {
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
func (d *Daemon) searchSessions(p agent.SearchParams) []agent.SessionInfo {
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

	all := d.snapshotSessions()
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
func (d *Daemon) createSession(req agent.StartRequest) (*agent.SessionInfo, error) {
	// Resolve worktree if a branch is requested.
	var wtBranch, worktreeDir string
	if req.WorktreeBranch != "" {
		wt, err := d.resolveWorktree(req.ProjectDir, req.WorktreeBranch)
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
	backend, err := d.hostClient.CreateSession(d.ctx, id, req)
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

	d.mu.Lock()
	d.sessions[id] = ms
	d.persistSession(ms)
	d.mu.Unlock()

	// Broadcast session creation.
	d.broadcast(agent.Event{
		Type:      agent.EventSessionCreate,
		SessionID: id,
		Timestamp: now,
		Data:      info,
	})

	// Start the backend in a goroutine.
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runBackend(id, ms, req)
	}()

	return &info, nil
}

// HOST
// runBackend starts the backend and relays its events.
func (d *Daemon) runBackend(id string, ms *managedSession, req agent.StartRequest) {
	// Start relaying events BEFORE calling Start(), because Start() blocks
	// for the entire LLM response (Prompt() is synchronous). Events emitted
	// by the backend's SSE goroutine during Start() must be relayed in real time.
	events := ms.backend.Events()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range events {
			evt.SessionID = id
			d.broadcast(evt)

			if evt.Type == agent.EventStatusChange {
				if data, ok := evt.Data.(agent.StatusChangeData); ok {
					d.updateSessionStatus(id, data.NewStatus)
				}
			}
			if evt.Type == agent.EventTitleChange {
				if data, ok := evt.Data.(agent.TitleChangeData); ok {
					d.updateSessionTitle(id, data.Title)
				}
			}
			if evt.Type == agent.EventRevertChange {
				if data, ok := evt.Data.(agent.RevertChangeData); ok {
					d.updateSessionRevert(id, data.MessageID)
				}
			}
			if evt.Type == agent.EventPermission {
				if data, ok := evt.Data.(agent.PermissionData); ok {
					d.mu.Lock()
					ms.pendingPerms = append(ms.pendingPerms, data)
					d.mu.Unlock()
				}
			}
		}
	}()
	defer func() { <-done }() // wait for relay goroutine to finish

	if err := ms.backend.Start(d.ctx, req); err != nil {
		d.log.Printf("session %s: backend start error: %v", id, err)
		d.updateSessionStatus(id, agent.StatusError)
		d.broadcast(agent.Event{
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
		d.mu.Lock()
		if ms2, ok := d.sessions[id]; ok {
			ms2.info.ExternalID = extID
			d.persistSession(ms2)
		}
		d.mu.Unlock()
	}

	// Backend event channel closed — mark as dead if still busy.
	d.mu.RLock()
	ms2, ok := d.sessions[id]
	d.mu.RUnlock()
	if ok && ms2.backend != nil {
		status := ms2.backend.Status()
		if status == agent.StatusBusy || status == agent.StatusStarting {
			d.updateSessionStatus(id, agent.StatusDead)
		}
	}
}

// HUB
// updateSessionStatus updates the cached status and UpdatedAt.
func (d *Daemon) updateSessionStatus(id string, status agent.SessionStatus) {
	d.mu.Lock()
	if ms, ok := d.sessions[id]; ok {
		ms.info.Status = status
		ms.info.UpdatedAt = time.Now()
		d.persistSession(ms)
	}
	d.mu.Unlock()
}

// HUB
// updateSessionTitle updates the cached title and UpdatedAt.
func (d *Daemon) updateSessionTitle(id string, title string) {
	d.mu.Lock()
	if ms, ok := d.sessions[id]; ok {
		ms.info.Title = title
		ms.info.UpdatedAt = time.Now()
		d.persistSession(ms)
	}
	d.mu.Unlock()
}

// HUB
// updateSessionRevert updates the cached revert message ID.
func (d *Daemon) updateSessionRevert(id string, messageID string) {
	d.mu.Lock()
	if ms, ok := d.sessions[id]; ok {
		ms.info.RevertMessageID = messageID
		d.persistSession(ms)
	}
	d.mu.Unlock()
}
