package hubmux

import (
	"net/http"

	"github.com/acksell/clank/internal/host"
)

// listHostsResponse is the JSON shape of GET /hosts. Kept as a named
// struct (not an inline anon) so the OpenAPI / hubclient unmarshal
// stays decoupled from the wire ordering.
type listHostsResponse struct {
	Hosts []host.Hostname `json:"hosts"`
}

func (m *Mux) handleListHosts(w http.ResponseWriter, r *http.Request) {
	hosts := m.svc.Hosts()
	m.writeJSON(w, http.StatusOK, listHostsResponse{Hosts: hosts})
}

// provisionHostRequest carries the kind discriminator. Future fields
// (region, snapshot id) go on this struct directly; per AGENTS.md
// "no fallbacks", any unknown kind fails fast at ProvisionHost.
type provisionHostRequest struct {
	Kind string `json:"kind"`
}

type provisionHostResponse struct {
	HostID host.Hostname `json:"host_id"`
	Status string        `json:"status"`
}

func (m *Mux) handleProvisionHost(w http.ResponseWriter, r *http.Request) {
	var req provisionHostRequest
	if err := decodeJSON(r.Body, &req); err != nil {
		writeBadRequest(w, "invalid JSON: "+err.Error())
		return
	}
	if req.Kind == "" {
		writeBadRequest(w, "kind is required")
		return
	}
	hn, err := m.svc.ProvisionHost(r.Context(), req.Kind)
	if err != nil {
		writeInternal(w, err)
		return
	}
	m.writeJSON(w, http.StatusOK, provisionHostResponse{
		HostID: hn,
		// "ready" is the only state ProvisionHost ever returns
		// because it's synchronous (Decision #4). Async future:
		// "provisioning" → poll.
		Status: "ready",
	})
}
