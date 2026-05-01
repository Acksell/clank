package hostmux

// HTTP routes for the AuthManager surface. The wire shape is the
// contract clank-host exposes to the hub for provider authentication.
// See internal/host/auth.go for the actual flow logic; this file is
// pure decode/dispatch/encode.

import (
	"errors"
	"net/http"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// registerAuth wires the /auth/* routes onto mx. Called from
// register() in mux.go.
func (m *Mux) registerAuth(mx *http.ServeMux) {
	mx.HandleFunc("GET /auth/providers", m.handleListAuthProviders)
	mx.HandleFunc("POST /auth/{provider}/device/start", m.handleStartDeviceFlow)
	mx.HandleFunc("GET /auth/{provider}/device/status", m.handleDeviceFlowStatus)
	mx.HandleFunc("DELETE /auth/{provider}/device", m.handleCancelDeviceFlow)
	mx.HandleFunc("DELETE /auth/{provider}", m.handleDeleteCredential)
}

func (m *Mux) requireAuth(w http.ResponseWriter) (*host.AuthManager, bool) {
	a := m.svc.Auth()
	if a == nil {
		writeJSON(w, http.StatusServiceUnavailable, errResp{
			Code:  "auth_unavailable",
			Error: "auth manager is not configured on this host",
		})
		return nil, false
	}
	return a, true
}

func (m *Mux) handleListAuthProviders(w http.ResponseWriter, r *http.Request) {
	a, ok := m.requireAuth(w)
	if !ok {
		return
	}
	infos, err := a.ListProviders(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if infos == nil {
		infos = []agent.ProviderAuthInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (m *Mux) handleStartDeviceFlow(w http.ResponseWriter, r *http.Request) {
	a, ok := m.requireAuth(w)
	if !ok {
		return
	}
	provider := r.PathValue("provider")
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_request", Error: "provider is required"})
		return
	}
	start, err := a.StartDeviceFlow(r.Context(), provider)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, start)
}

func (m *Mux) handleDeviceFlowStatus(w http.ResponseWriter, r *http.Request) {
	a, ok := m.requireAuth(w)
	if !ok {
		return
	}
	flowID := r.URL.Query().Get("flow_id")
	if flowID == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_request", Error: "flow_id is required"})
		return
	}
	status, err := a.GetFlowStatus(r.Context(), flowID)
	if err != nil {
		writeAuthErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (m *Mux) handleCancelDeviceFlow(w http.ResponseWriter, r *http.Request) {
	a, ok := m.requireAuth(w)
	if !ok {
		return
	}
	flowID := r.URL.Query().Get("flow_id")
	if flowID == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_request", Error: "flow_id is required"})
		return
	}
	if err := a.CancelFlow(r.Context(), flowID); err != nil {
		writeAuthErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Mux) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	a, ok := m.requireAuth(w)
	if !ok {
		return
	}
	provider := r.PathValue("provider")
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_request", Error: "provider is required"})
		return
	}
	if err := a.DeleteCredential(r.Context(), provider); err != nil {
		writeAuthErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAuthErr maps auth-package sentinels to HTTP statuses; falls
// through to the general writeError for anything else.
func writeAuthErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, host.ErrUnknownProvider):
		writeJSON(w, http.StatusBadRequest, errResp{Code: "unknown_provider", Error: err.Error()})
	case errors.Is(err, host.ErrUnknownFlow):
		writeJSON(w, http.StatusNotFound, errResp{Code: "unknown_flow", Error: err.Error()})
	default:
		writeError(w, err)
	}
}
