package agent

import "time"

// AuthCredential is the on-disk credential shape OpenCode reads from
// `~/.local/share/opencode/auth.json`. Three discriminated variants
// keyed on Type ("oauth" | "api" | "wellknown"); only the fields for
// that variant are populated. Mirrors `Oauth` / `Api` / `WellKnown` in
// packages/opencode/src/auth/index.ts upstream.
//
// For github-copilot the upstream plugin writes `type: "oauth"` with
// both Refresh and Access set to the same GitHub access_token and
// Expires=0 (Copilot tokens do not have a tracked TTL in OpenCode).
// See packages/opencode/src/plugin/github-copilot/copilot.ts.
type AuthCredential struct {
	Type    string `json:"type"`
	Refresh string `json:"refresh,omitempty"`
	Access  string `json:"access,omitempty"`
	Expires int64  `json:"expires,omitempty"`
	Key     string `json:"key,omitempty"`

	// EnterpriseURL carries through extra fields the github-copilot
	// plugin populates when the deployment type is "enterprise". The
	// loader uses it to compute the API base URL.
	EnterpriseURL string `json:"enterpriseUrl,omitempty"`
}

// ProviderAuthInfo is the snapshot a client gets from
// GET /auth/providers. AuthType selects which begin-flow the client
// dispatches to: "device" (kicks off device flow) or "api" (prompts
// for a key).
type ProviderAuthInfo struct {
	ProviderID  string `json:"provider_id"`
	DisplayName string `json:"display_name"`
	AuthType    string `json:"auth_type"`
	Connected   bool   `json:"connected"`
}

// DeviceFlowStart is the response body for POST /auth/{provider}/device/start.
// FlowID identifies the in-memory flow on subsequent status polls and
// cancellation. UserCode is what the user types into VerificationURL
// in their browser.
type DeviceFlowStart struct {
	FlowID          string    `json:"flow_id"`
	DeviceCode      string    `json:"-"` // not exposed to clients; sandbox-internal
	UserCode        string    `json:"user_code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
	Interval        int       `json:"interval"`
}

// DeviceFlowState enumerates the states of a device-flow lifecycle.
// pending → authorized → success is the happy path; the auth.json
// write happens at the pending→authorized boundary, the OpenCode
// server restart happens during authorized, and the transition to
// success only fires once the new server is healthy.
type DeviceFlowState string

const (
	DeviceFlowPending    DeviceFlowState = "pending"
	DeviceFlowAuthorized DeviceFlowState = "authorized"
	DeviceFlowSuccess    DeviceFlowState = "success"
	DeviceFlowExpired    DeviceFlowState = "expired"
	DeviceFlowDenied     DeviceFlowState = "denied"
	DeviceFlowError      DeviceFlowState = "error"
	DeviceFlowCanceled   DeviceFlowState = "canceled"
)

// DeviceFlowStatus is the response body for GET /auth/{provider}/device/status.
// Pure read: no side effects. The TUI polls this every couple seconds
// to drive its phase transitions and labels.
type DeviceFlowStatus struct {
	State DeviceFlowState `json:"state"`
	Error string          `json:"error,omitempty"`
}
