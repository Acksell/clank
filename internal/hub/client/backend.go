package hubclient

import (
	"context"
	"net/url"

	"github.com/acksell/clank/internal/agent"
)

// BackendClient is bound to a backend type. Backend selection on the wire
// is flat at the hub level — the hub picks the host internally.
type BackendClient struct {
	c       *Client
	backend agent.BackendType
}

// Backend returns a handle for the named backend.
func (c *Client) Backend(backend agent.BackendType) *BackendClient {
	return &BackendClient{c: c, backend: backend}
}

// Agents returns available agents for this backend, scoped to projectDir.
func (b *BackendClient) Agents(ctx context.Context, projectDir string) ([]agent.AgentInfo, error) {
	path := "/agents?backend=" + url.QueryEscape(string(b.backend)) + "&project_dir=" + url.QueryEscape(projectDir)
	var out []agent.AgentInfo
	if err := b.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Models returns available models for this backend, scoped to projectDir.
func (b *BackendClient) Models(ctx context.Context, projectDir string) ([]agent.ModelInfo, error) {
	path := "/models?backend=" + url.QueryEscape(string(b.backend)) + "&project_dir=" + url.QueryEscape(projectDir)
	var out []agent.ModelInfo
	if err := b.c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}
