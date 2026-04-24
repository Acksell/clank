package hostmux

import (
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// handleSetIdentity records the (name, email) the hub wants this host
// to use as committer/author. The hub calls this once per remote host
// provision (see hub.ProvisionHost). Local hosts never receive this
// call — they share the laptop's ~/.gitconfig directly.
//
// Returns 204 on success, 400 on missing/empty fields.
func (m *Mux) handleSetIdentity(w http.ResponseWriter, r *http.Request) {
	var req agent.GitIdentity
	if err := decodeJSON(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Error: err.Error()})
		return
	}
	m.svc.SetIdentity(req)
	w.WriteHeader(http.StatusNoContent)
}
