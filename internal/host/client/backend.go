package hostclient

import (
	"context"
	"net/http"
	"net/url"

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

// Agents lists agents available for this backend in the given project.
func (b *BackendClient) Agents(ctx context.Context, projectDir string) ([]host.AgentInfo, error) {
	q := url.Values{"backend": {string(b.bt)}, "project_dir": {projectDir}}
	var out []host.AgentInfo
	err := b.c.do(ctx, http.MethodGet, "/agents?"+q.Encode(), nil, &out)
	return out, err
}

// Models lists models available for this backend in the given project.
func (b *BackendClient) Models(ctx context.Context, projectDir string) ([]host.ModelInfo, error) {
	q := url.Values{"backend": {string(b.bt)}, "project_dir": {projectDir}}
	var out []host.ModelInfo
	err := b.c.do(ctx, http.MethodGet, "/models?"+q.Encode(), nil, &out)
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
