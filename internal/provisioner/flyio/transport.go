package flyio

import "net/http"

// bearerInjector adds `Authorization: Bearer <token>` to every
// outbound request. Identical to the Daytona variant in
// internal/provisioner/daytona/transport.go but kept package-private
// here so each provisioner owns its own auth chain. If a third
// provider duplicates this, extract to a shared package.
type bearerInjector struct {
	wrapped http.RoundTripper
	token   string
}

// RoundTrip implements http.RoundTripper.
func (b *bearerInjector) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	r2.Header.Set("Authorization", "Bearer "+b.token)
	rt := b.wrapped
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(r2)
}

// CloseIdleConnections delegates so wrapping us doesn't hide the pool.
func (b *bearerInjector) CloseIdleConnections() {
	type idler interface{ CloseIdleConnections() }
	rt := b.wrapped
	if rt == nil {
		rt = http.DefaultTransport
	}
	if i, ok := rt.(idler); ok {
		i.CloseIdleConnections()
	}
}
