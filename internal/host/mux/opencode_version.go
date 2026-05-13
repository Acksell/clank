package hostmux

import (
	"context"
	"net/http"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// opencodeVersionResponse is the body of GET /opencode-version. The
// laptop's clankcli (push --migrate / pull --migrate) hits this on
// both ends to enforce the version-skew policy in
// agent.AssertOpencodeVersionsCompatible.
type opencodeVersionResponse struct {
	Version string `json:"version"`
}

// handleOpencodeVersion shells out `opencode --version` and returns
// the bare version string. Bounded by a short context — this should
// take milliseconds; if opencode is hung, fail fast rather than
// wedge the migration.
func (m *Mux) handleOpencodeVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	v, err := agent.OpenCodeVersion(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "opencode_version", Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opencodeVersionResponse{Version: v})
}
