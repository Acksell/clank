// Package gitcred discovers git push/fetch credentials on the hub
// machine. It is the missing piece between [hub.ResolveCredential]
// (which decides whether a credential is needed) and
// [agent.GitCredential] (the wire-shape carried to the host).
//
// Discovery is layered: each [Discoverer] looks in exactly one place
// (env vars, `gh` CLI, settings file). A [Stack] runs them in order
// and returns the first non-empty result. A discoverer that finds
// nothing returns [ErrNoCredential]; any other error is fatal and
// bubbles up immediately so misconfigured environments fail loudly
// instead of falling through to anonymous and producing a confusing
// 401 at push time.
//
// The package never logs secret material. All discoverers emit
// credentials whose Token/Password fields are populated; rendering
// goes through [agent.GitCredential.Redacted].
//
// See docs/credential_discovery.md for the discovery order and
// security posture.
package gitcred
