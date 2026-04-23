package hubmux

import (
	"fmt"
	"net/http"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitendpoint"
	"github.com/acksell/clank/internal/host"
)

func (m *Mux) handleListAgents(w http.ResponseWriter, r *http.Request) {
	bt, hostname, ref, err := parseCatalogQuery(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	agents, err := m.svc.ListAgents(r.Context(), bt, hostname, ref)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (m *Mux) handleListModels(w http.ResponseWriter, r *http.Request) {
	bt, hostname, ref, err := parseCatalogQuery(r)
	if err != nil {
		writeBadRequest(w, err.Error())
		return
	}
	models, err := m.svc.ListModels(r.Context(), bt, hostname, ref)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

// parseCatalogQuery extracts (backend, hostname, gitRef) from the
// query string. The discrete GitRef fields are passed verbatim
// (git_local_path | git_remote_url + worktree_branch). The remote URL
// is parsed into a GitEndpoint at this ingress — the host has no
// parser of its own.
func parseCatalogQuery(r *http.Request) (agent.BackendType, host.Hostname, agent.GitRef, error) {
	q := r.URL.Query()
	bt := agent.BackendType(q.Get("backend"))
	hostname := host.Hostname(q.Get("hostname"))
	var ref agent.GitRef
	if p := q.Get("git_local_path"); p != "" {
		ref.LocalPath = p
	}
	if u := q.Get("git_remote_url"); u != "" {
		ep, err := gitendpoint.Parse(u)
		if err != nil {
			return "", "", agent.GitRef{}, fmt.Errorf("parse git_remote_url %q: %w", u, err)
		}
		ref.Endpoint = ep
	}
	ref.WorktreeBranch = q.Get("worktree_branch")
	if bt == "" {
		return "", "", agent.GitRef{}, errBadCatalogQuery("backend is required")
	}
	if hostname == "" {
		return "", "", agent.GitRef{}, errBadCatalogQuery("hostname is required")
	}
	if err := ref.Validate(); err != nil {
		return "", "", agent.GitRef{}, fmt.Errorf("invalid git_ref: %w", err)
	}
	return bt, hostname, ref, nil
}

type errBadCatalogQuery string

func (e errBadCatalogQuery) Error() string { return string(e) }
