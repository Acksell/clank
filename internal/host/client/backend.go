package hostclient

import (
	"context"
	"net/http"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// BackendClient is a per-backend-type handle. Obtained via
// HTTP.Backend(bt).
type BackendClient struct {
	c  *HTTP
	bt agent.BackendType
}

// Backend returns a handle scoped to the given backend type. Use it for
// per-backend catalog and discovery operations.
func (c *HTTP) Backend(bt agent.BackendType) *BackendClient {
	return &BackendClient{c: c, bt: bt}
}

// catalogRequest is the wire shape for /agents and /models. Migrated
// from GET-with-query to POST-with-body in Phase 5 because (a) we now
// carry an opaque GitCredential alongside the GitRef and credential
// material has no business in URL query strings, and (b) the host's
// credential consumption (Phase 6) keeps the same shape regardless of
// kind.
type catalogRequest struct {
	Backend agent.BackendType   `json:"backend"`
	GitRef  agent.GitRef        `json:"git_ref"`
	Auth    agent.GitCredential `json:"auth"`
}

// Agents lists agents available for this backend in the given repo.
// The host resolves ref to a workdir internally — paths never cross the
// wire (§7.3).
func (b *BackendClient) Agents(ctx context.Context, ref agent.GitRef, auth agent.GitCredential) ([]host.AgentInfo, error) {
	var out []host.AgentInfo
	err := b.c.do(ctx, http.MethodPost, "/agents", catalogRequest{Backend: b.bt, GitRef: ref, Auth: auth}, &out)
	return out, err
}

// Models lists models available for this backend in the given repo.
func (b *BackendClient) Models(ctx context.Context, ref agent.GitRef, auth agent.GitCredential) ([]host.ModelInfo, error) {
	var out []host.ModelInfo
	err := b.c.do(ctx, http.MethodPost, "/models", catalogRequest{Backend: b.bt, GitRef: ref, Auth: auth}, &out)
	return out, err
}

// Discover lists existing on-disk session snapshots for this backend
// rooted at seedDir.
func (b *BackendClient) Discover(ctx context.Context, seedDir string) ([]agent.SessionSnapshot, error) {
	body := struct {
		Backend agent.BackendType `json:"backend"`
		SeedDir string            `json:"seed_dir"`
	}{Backend: b.bt, SeedDir: seedDir}
	var out []agent.SessionSnapshot
	err := b.c.do(ctx, http.MethodPost, "/discover", body, &out)
	return out, err
}
