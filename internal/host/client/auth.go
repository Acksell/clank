package hostclient

import (
	"context"
	"net/http"
	"net/url"

	"github.com/acksell/clank/internal/agent"
)

// ListAuthProviders returns the providers this host can authenticate
// plus their current connection state. Wraps GET /auth/providers.
func (c *HTTP) ListAuthProviders(ctx context.Context) ([]agent.ProviderAuthInfo, error) {
	var out []agent.ProviderAuthInfo
	err := c.do(ctx, http.MethodGet, "/auth/providers", nil, &out)
	return out, err
}

// StartDeviceFlow kicks off device-flow auth for providerID and
// returns the user-facing fields (URL, user_code) plus a flow_id
// for status polls.
func (c *HTTP) StartDeviceFlow(ctx context.Context, providerID string) (agent.DeviceFlowStart, error) {
	var out agent.DeviceFlowStart
	path := "/auth/" + url.PathEscape(providerID) + "/device/start"
	err := c.do(ctx, http.MethodPost, path, nil, &out)
	return out, err
}

// DeviceFlowStatus reads the current state of an in-progress flow.
// Pure read — safe to call as fast as the TUI wants.
func (c *HTTP) DeviceFlowStatus(ctx context.Context, providerID, flowID string) (agent.DeviceFlowStatus, error) {
	var out agent.DeviceFlowStatus
	path := "/auth/" + url.PathEscape(providerID) + "/device/status?flow_id=" + url.QueryEscape(flowID)
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// CancelDeviceFlow signals the host to abort an in-progress flow.
// Idempotent for already-finished flows.
func (c *HTTP) CancelDeviceFlow(ctx context.Context, providerID, flowID string) error {
	path := "/auth/" + url.PathEscape(providerID) + "/device?flow_id=" + url.QueryEscape(flowID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// DeleteAuthCredential removes the stored credential for providerID
// (logging the user out) and triggers an OpenCode server restart.
func (c *HTTP) DeleteAuthCredential(ctx context.Context, providerID string) error {
	path := "/auth/" + url.PathEscape(providerID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
