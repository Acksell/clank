package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// HOST
func (m *Mux) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := m.svc.Status(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// HOST
func (m *Mux) handleListBackends(w http.ResponseWriter, r *http.Request) {
	bs, err := m.svc.ListBackends(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bs)
}

// HOST
// handleListAgents serves GET /agents?backend=&git_ref=. The git_ref is
// the URL-encoded canonical form (see agent.GitRef.Canonical). The host
// resolves it to a workdir internally — no paths cross the wire (§7.3).
func (m *Mux) handleListAgents(w http.ResponseWriter, r *http.Request) {
	bt := agent.BackendType(r.URL.Query().Get("backend"))
	ref, err := refFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if bt == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "backend is required"})
		return
	}
	out, err := m.svc.ListAgents(r.Context(), bt, ref)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// HOST
func (m *Mux) handleListModels(w http.ResponseWriter, r *http.Request) {
	bt := agent.BackendType(r.URL.Query().Get("backend"))
	ref, err := refFromQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if bt == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "backend is required"})
		return
	}
	out, err := m.svc.ListModels(r.Context(), bt, ref)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// refFromQuery extracts the GitRef from the query string. Callers send
// the three discrete fields (kind/url/path) rather than the canonical
// form so the host doesn't have to round-trip parse: a Hub that cached
// the GitRef as a struct can reconstruct the wire shape verbatim.
func refFromQuery(r *http.Request) (agent.GitRef, error) {
	q := r.URL.Query()
	ref := agent.GitRef{
		Kind: agent.GitRefKind(q.Get("git_ref_kind")),
		URL:  q.Get("git_ref_url"),
		Path: q.Get("git_ref_path"),
	}
	if err := ref.Validate(); err != nil {
		return agent.GitRef{}, err
	}
	return ref, nil
}

// HOST
type DiscoverRequest struct {
	Backend agent.BackendType `json:"backend"`
	SeedDir string            `json:"seed_dir"`
}

// HOST
func (m *Mux) handleDiscoverSessions(w http.ResponseWriter, r *http.Request) {
	var req DiscoverRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if req.Backend == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Error: "backend is required"})
		return
	}
	out, err := m.svc.DiscoverSessions(r.Context(), req.Backend, req.SeedDir)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
