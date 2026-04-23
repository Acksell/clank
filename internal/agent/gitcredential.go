package agent

// GitCredential is the opaque "how to authenticate" half of a git
// repository access. Hub-resolved per (target host, endpoint) and
// passed alongside the GitEndpoint to the host. The host switches on
// Kind to construct the correct git invocation; it never invents
// credentials of its own.
//
// See docs/git_credentials_refactor.md for the resolver policy.

import "fmt"

// GitCredentialKind enumerates the credential mechanisms the host
// knows how to consume. New mechanisms (mTLS, GitHub App JWT,
// SaaS-minted tokens) extend this enum + add a host-side case.
type GitCredentialKind string

const (
	// GitCredAnonymous: clone without auth. Valid for public HTTPS
	// repos and for file:// endpoints.
	GitCredAnonymous GitCredentialKind = "anonymous"

	// GitCredHTTPSBasic: username + password (or PAT used as password)
	// over HTTPS. Transported via GIT_ASKPASS — never argv. Reserved
	// for the token-discovery follow-up PR; resolver does not emit it
	// in v1.
	GitCredHTTPSBasic GitCredentialKind = "https_basic"

	// GitCredHTTPSToken: bearer token over HTTPS. Reserved.
	GitCredHTTPSToken GitCredentialKind = "https_token"

	// GitCredSSHAgent: use the local ssh-agent. Carries no transmissible
	// secret material — the agent socket is process-local. Therefore
	// only valid when the target host IS local. Hub validates this
	// invariant before forwarding; host validates again on receipt.
	GitCredSSHAgent GitCredentialKind = "ssh_agent"
)

// GitCredential carries the secret material for a single auth attempt.
// Username/Password/Token are kind-specific; fields not used by Kind
// must be empty so logging/diffing doesn't accidentally surface unused
// values that look meaningful.
//
// Marshals to JSON for the wire. Loggers MUST NOT format Password or
// Token; use Redacted() for any human-facing rendering.
type GitCredential struct {
	Kind     GitCredentialKind `json:"kind"`
	Username string            `json:"username,omitempty"`
	Password string            `json:"password,omitempty"`
	Token    string            `json:"token,omitempty"`
}

// Validate enforces the per-kind field invariants. Defense-in-depth on
// the host side: a malformed credential should fail loudly before any
// git command is constructed.
func (c GitCredential) Validate() error {
	switch c.Kind {
	case GitCredAnonymous, GitCredSSHAgent:
		if c.Username != "" || c.Password != "" || c.Token != "" {
			return fmt.Errorf("credential kind %q must not carry secrets", c.Kind)
		}
	case GitCredHTTPSBasic:
		if c.Username == "" || c.Password == "" {
			return fmt.Errorf("credential kind %q requires username and password", c.Kind)
		}
		if c.Token != "" {
			return fmt.Errorf("credential kind %q must not set token", c.Kind)
		}
	case GitCredHTTPSToken:
		if c.Token == "" {
			return fmt.Errorf("credential kind %q requires token", c.Kind)
		}
		if c.Username != "" || c.Password != "" {
			return fmt.Errorf("credential kind %q must not set username/password", c.Kind)
		}
	default:
		return fmt.Errorf("unknown credential kind %q", c.Kind)
	}
	return nil
}

// Redacted returns a log-safe rendering of the credential. Never
// includes Password or Token.
func (c GitCredential) Redacted() string {
	switch c.Kind {
	case GitCredHTTPSBasic:
		return fmt.Sprintf("https_basic(user=%q,password=<redacted>)", c.Username)
	case GitCredHTTPSToken:
		return "https_token(token=<redacted>)"
	default:
		return string(c.Kind)
	}
}
