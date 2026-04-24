package hostclient

import (
	"context"
	"net/http"

	"github.com/acksell/clank/internal/agent"
)

// SetIdentity tells the host to use id as committer/author for any
// commits it makes (e.g. the agent committing inside a sandbox). The
// hub calls this once per remote host immediately after RegisterHost
// in ProvisionHost; the host stores the identity and seeds it into
// each repo's --local config on first use.
func (c *HTTP) SetIdentity(ctx context.Context, id agent.GitIdentity) error {
	// Fail-fast on the client so callers get a clear local error rather
	// than a 400 round-trip when name/email are empty or whitespace.
	if err := id.Validate(); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/identity", id, nil)
}
