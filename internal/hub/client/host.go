package hubclient

import (
	"context"
	"net/url"

	"github.com/acksell/clank/internal/host"
)

// HostClient is the hub-side handle for one host. Bound to a hostname.
type HostClient struct {
	c        *Client
	hostname host.Hostname
}

// Host returns a handle for the named host.
func (c *Client) Host(hostname host.Hostname) *HostClient {
	return &HostClient{c: c, hostname: hostname}
}

// Hostname returns the hostname this handle is bound to.
func (h *HostClient) Hostname() host.Hostname { return h.hostname }

// Repos lists repos registered on this host.
func (h *HostClient) Repos(ctx context.Context) ([]host.Repo, error) {
	var out []host.Repo
	if err := h.c.get(ctx, "/hosts/"+url.PathEscape(string(h.hostname))+"/repos", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Repo returns a handle for one repo on this host. gitRef is the canonical
// GitRef string (URL key form).
func (h *HostClient) Repo(gitRef string) *RepoClient {
	return &RepoClient{h: h, gitRef: gitRef}
}
