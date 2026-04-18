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

func parseHostname(r *http.Request) (host.Hostname, error) {
	id := host.Hostname(r.PathValue("hostname"))
	if id == "" {
		return "", errors.New("host id is required")
	}
	return id, nil
}

func parseGitRef(r *http.Request) (string, error) {
	ref := r.PathValue("gitRef")
	if ref == "" {
		return "", errors.New("git ref is required")
	}
	return ref, nil
}

func (m *Mux) handleListReposOnHost(w http.ResponseWriter, r *http.Request) {
	hostname, err := parseHostname(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	repos, err := m.svc.ListReposOnHost(r.Context(), hostname)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

func (m *Mux) handleListBranchesOnRepo(w http.ResponseWriter, r *http.Request) {
	hostname, err := parseHostname(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	gitRef, err := parseGitRef(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	branches, err := m.svc.ListBranchesOnRepo(r.Context(), hostname, gitRef)
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
	hostname, err := parseHostname(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	gitRef, err := parseGitRef(r)
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
	wt, err := m.svc.CreateWorktreeOnRepo(r.Context(), hostname, gitRef, body.Branch)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, wt)
}

func (m *Mux) handleRemoveWorktreeOnRepo(w http.ResponseWriter, r *http.Request) {
	hostname, err := parseHostname(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	gitRef, err := parseGitRef(r)
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
	if err := m.svc.RemoveWorktreeOnRepo(r.Context(), hostname, gitRef, branch, force); err != nil {
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
	hostname, err := parseHostname(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	gitRef, err := parseGitRef(r)
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
	res, err := m.svc.MergeBranchOnRepo(r.Context(), hostname, gitRef, body.Branch, body.CommitMessage)
	if err != nil {
		writeRepoErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
