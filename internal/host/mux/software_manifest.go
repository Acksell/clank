package hostmux

import (
	"context"
	"net/http"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// handleSoftwareManifest returns the cached agent.SoftwareManifest
// for this clank-host. Used by the laptop CLI to compare versions
// across hosts before a migration (currently only opencode, but the
// manifest is structured for future tools — claude, clank-host's
// own version, anything else clank cares about pinning).
//
// First request pays opencode's JS startup cost (~300-500ms) for
// the probe; subsequent requests are served from in-memory cache
// in nanoseconds. See agent.GetSoftwareManifest's docstring for
// the staleness contract.
//
// The 5s timeout bounds the FIRST request's probe — if opencode is
// hung at startup, fail fast rather than wedge the laptop's
// migration check. Once cached, the timeout doesn't matter because
// the call returns synchronously without I/O.
func (m *Mux) handleSoftwareManifest(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, agent.GetSoftwareManifest(ctx))
}
