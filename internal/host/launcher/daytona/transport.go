// Package daytona implements a HostLauncher backed by Daytona-managed
// cloud sandboxes. The cloud hub uses it to provision a fresh
// clank-host inside a sandbox when a session request asks for
// LaunchHost{Provider:"daytona"}.
//
// The sandbox runs the published clank-host image (see
// cmd/clank-host/Dockerfile) listening on a fixed port; Daytona's
// preview proxy exposes that port at a per-sandbox URL guarded by
// `x-daytona-preview-token`. The launcher constructs an HTTP host
// client whose RoundTripper injects that token on every outbound
// request.
package daytona

import (
	"net/http"
)

// previewTokenInjector wraps an http.RoundTripper to add the
// `x-daytona-preview-token` header on every outbound request — that's
// how Daytona's preview proxy authorizes inbound traffic to the
// sandbox.
type previewTokenInjector struct {
	wrapped http.RoundTripper
	token   string
}

// RoundTrip implements http.RoundTripper.
func (p *previewTokenInjector) RoundTrip(r *http.Request) (*http.Response, error) {
	// Clone the request before mutating headers — RoundTrip is forbidden
	// from modifying its input. http.Request.Clone is shallow except for
	// the URL and Header maps, which is what we need.
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	r2.Header.Set("x-daytona-preview-token", p.token)
	rt := p.wrapped
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(r2)
}
