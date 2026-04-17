package hub

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/acksell/clank/internal/host"
)

// Phase 3B: Hub-side handlers for `/hosts/{hostID}/repos/...` routes.
// These are pure pass-throughs to the host plane via hostclient — the
// Hub no longer touches git directly on these paths. The legacy
// `/branches`, `/worktrees` handlers in worktrees.go still bypass
// hostclient and call git.X; they will be removed in Phase 3D.

func (s *Service) lookupHost(w http.ResponseWriter, r *http.Request) (host.HostID, bool) {
	id := host.HostID(r.PathValue("hostID"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host id is required"})
		return "", false
	}
	if _, ok := s.Host(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "host not registered: " + string(id)})
		return "", false
	}
	return id, true
}

func writeRepoErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, host.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, host.ErrCannotMergeDefault),
		errors.Is(err, host.ErrNothingToMerge),
		errors.Is(err, host.ErrCommitMessageRequired),
		errors.Is(err, host.ErrMainDirty),
		errors.Is(err, host.ErrMergeConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func (s *Service) handleListReposOnHost(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.lookupHost(w, r)
	if !ok {
		return
	}
	hc, _ := s.Host(hostID)
	repos, err := hc.ListRepos(r.Context())
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Service) handleListBranchesOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.lookupHost(w, r)
	if !ok {
		return
	}
	repoID := host.RepoID(r.PathValue("repoID"))
	if repoID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo id is required"})
		return
	}
	hc, _ := s.Host(hostID)
	branches, err := hc.ListBranchesByRepo(r.Context(), repoID)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

type createWorktreeOnRepoRequest struct {
	Branch string `json:"branch"`
}

func (s *Service) handleCreateWorktreeOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.lookupHost(w, r)
	if !ok {
		return
	}
	repoID := host.RepoID(r.PathValue("repoID"))
	if repoID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo id is required"})
		return
	}
	var req createWorktreeOnRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "branch is required"})
		return
	}
	hc, _ := s.Host(hostID)
	wt, err := hc.ResolveWorktreeByRepo(r.Context(), repoID, req.Branch)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

func (s *Service) handleRemoveWorktreeOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.lookupHost(w, r)
	if !ok {
		return
	}
	repoID := host.RepoID(r.PathValue("repoID"))
	branch := r.URL.Query().Get("branch")
	force := r.URL.Query().Get("force") == "true"
	if repoID == "" || branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo id and branch are required"})
		return
	}
	hc, _ := s.Host(hostID)
	if err := hc.RemoveWorktreeByRepo(r.Context(), repoID, branch, force); err != nil {
		writeRepoErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type mergeBranchOnRepoRequest struct {
	Branch        string `json:"branch"`
	CommitMessage string `json:"commit_message,omitempty"`
}

func (s *Service) handleMergeBranchOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, ok := s.lookupHost(w, r)
	if !ok {
		return
	}
	repoID := host.RepoID(r.PathValue("repoID"))
	if repoID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo id is required"})
		return
	}
	var req mergeBranchOnRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Branch == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "branch is required"})
		return
	}
	hc, _ := s.Host(hostID)
	res, err := hc.MergeBranchByRepo(r.Context(), repoID, req.Branch, req.CommitMessage)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
