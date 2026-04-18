package hubmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
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
// query string. The three discrete GitRef fields are passed verbatim
// (kind/url/path) so the client can reconstruct the wire shape from a
// cached struct without canonical-form round-trip parsing — same shape
// as the host's /agents endpoint (§7.3).
func parseCatalogQuery(r *http.Request) (agent.BackendType, host.Hostname, agent.GitRef, error) {
	q := r.URL.Query()
	bt := agent.BackendType(q.Get("backend"))
	hostname := host.Hostname(q.Get("hostname"))
	ref := agent.GitRef{
		Kind: agent.GitRefKind(q.Get("git_ref_kind")),
		URL:  q.Get("git_ref_url"),
		Path: q.Get("git_ref_path"),
	}
	if bt == "" {
		return "", "", agent.GitRef{}, errBadCatalogQuery("backend is required")
	}
	if hostname == "" {
		return "", "", agent.GitRef{}, errBadCatalogQuery("hostname is required")
	}
	if err := ref.Validate(); err != nil {
		return "", "", agent.GitRef{}, err
	}
	return bt, hostname, ref, nil
}

type errBadCatalogQuery string

func (e errBadCatalogQuery) Error() string { return string(e) }
