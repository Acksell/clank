package daemon

// Backwards-compat re-exports for the daemon-facing client. The canonical
// implementation now lives in internal/hub/client (Phase 2A of
// hub_host_refactor.md). New code should import that package directly.
//
// Every name exposed here is a type alias or a thin wrapper around its
// hubclient counterpart so existing daemon-internal tests, the TUI, and
// clankcli can keep compiling during the migration.

import (
	hubclient "github.com/acksell/clank/internal/hub/client"
)

type (
	Client         = hubclient.Client
	PingResponse   = hubclient.PingResponse
	StatusResponse = hubclient.StatusResponse
)

// NewClient connects to clankd at the given socket path.
func NewClient(sockPath string) *Client { return hubclient.NewClient(sockPath) }

// NewDefaultClient connects using the default socket path.
func NewDefaultClient() (*Client, error) { return hubclient.NewDefaultClient() }
