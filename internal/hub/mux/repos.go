package hubmux

import (
	"errors"
	"net/http"

	"github.com/acksell/clank/internal/host"
)

// writeRepoErr maps host-package sentinels (NotFound, merge errors)
// to HTTP statuses, then falls through to writeServiceErr for the
// hub-package sentinels.
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
		writeServiceErr(w, err)
	}
}

func parseHostID(r *http.Request) (host.HostID, error) {
	id := host.HostID(r.PathValue("hostID"))
	if id == "" {
		return "", errors.New("host id is required")
	}
	return id, nil
}

func parseRepoID(r *http.Request) (host.RepoID, error) {
	id := host.RepoID(r.PathValue("repoID"))
	if id == "" {
		return "", errors.New("repo id is required")
	}
	return id, nil
}

func (m *Mux) handleListReposOnHost(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseHostID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	repos, err := m.svc.ListReposOnHost(r.Context(), hostID)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

type registerRepoOnHostRequest struct {
	RemoteURL string `json:"remote_url"`
	RootDir   string `json:"root_dir"`
}

func (m *Mux) handleRegisterRepoOnHost(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseHostID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	var body registerRepoOnHostRequest
	if err := decodeJSON(r.Body, &body); err != nil {
		writeBadRequest(w, "invalid JSON: "+err.Error())
		return
	}
	if body.RemoteURL == "" || body.RootDir == "" {
		writeBadRequest(w, "remote_url and root_dir are required")
		return
	}
	repo, err := m.svc.RegisterRepoOnHost(r.Context(), hostID, host.RepoRef{RemoteURL: body.RemoteURL}, body.RootDir)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

func (m *Mux) handleListBranchesOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseHostID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	branches, err := m.svc.ListBranchesOnRepo(r.Context(), hostID, repoID)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

type createWorktreeOnRepoRequest struct {
	Branch string `json:"branch"`
}

func (m *Mux) handleCreateWorktreeOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseHostID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	var body createWorktreeOnRepoRequest
	if err := decodeJSON(r.Body, &body); err != nil {
		writeBadRequest(w, "invalid JSON: "+err.Error())
		return
	}
	if body.Branch == "" {
		writeBadRequest(w, "branch is required")
		return
	}
	wt, err := m.svc.CreateWorktreeOnRepo(r.Context(), hostID, repoID, body.Branch)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

func (m *Mux) handleRemoveWorktreeOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseHostID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		writeBadRequest(w, "branch is required")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := m.svc.RemoveWorktreeOnRepo(r.Context(), hostID, repoID, branch, force); err != nil {
		writeRepoErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type mergeBranchOnRepoRequest struct {
	Branch        string `json:"branch"`
	CommitMessage string `json:"commit_message,omitempty"`
}

func (m *Mux) handleMergeBranchOnRepo(w http.ResponseWriter, r *http.Request) {
	hostID, err := parseHostID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	repoID, err := parseRepoID(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	var body mergeBranchOnRepoRequest
	if err := decodeJSON(r.Body, &body); err != nil {
		writeBadRequest(w, "invalid JSON: "+err.Error())
		return
	}
	if body.Branch == "" {
		writeBadRequest(w, "branch is required")
		return
	}
	res, err := m.svc.MergeBranchOnRepo(r.Context(), hostID, repoID, body.Branch, body.CommitMessage)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
