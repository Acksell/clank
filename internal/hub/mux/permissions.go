package hubmux

import "net/http"

func (m *Mux) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Allow bool `json:"allow"`
	}
	if err := decodeJSON(r.Body, &body); err != nil {
		writeBadRequest(w, "invalid JSON")
		return
	}
	if err := m.svc.RespondPermission(r.Context(), r.PathValue("id"), r.PathValue("permID"), body.Allow); err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (m *Mux) handleGetPendingPermission(w http.ResponseWriter, r *http.Request) {
	perms, err := m.svc.PendingPermissions(r.PathValue("id"))
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, perms)
}
