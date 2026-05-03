package daytona

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
)

// previewTokenInjector adds Daytona's `x-daytona-preview-token`
// header to every outbound request before delegating to wrapped.
type previewTokenInjector struct {
	wrapped http.RoundTripper
	token   string
}

// RoundTrip implements http.RoundTripper.
func (p *previewTokenInjector) RoundTrip(r *http.Request) (*http.Response, error) {
	// RoundTrip must not modify its input — clone before setting headers.
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	r2.Header.Set("x-daytona-preview-token", p.token)
	rt := p.wrapped
	if rt == nil {
		rt = http.DefaultTransport
	}
	return rt.RoundTrip(r2)
}

// CloseIdleConnections delegates so wrapping us doesn't hide the pool.
func (p *previewTokenInjector) CloseIdleConnections() {
	type idler interface{ CloseIdleConnections() }
	rt := p.wrapped
	if rt == nil {
		rt = http.DefaultTransport
	}
	if i, ok := rt.(idler); ok {
		i.CloseIdleConnections()
	}
}

// bearerInjector adds `Authorization: Bearer <token>` to every
// outbound request. Used as the universal capability-token layer
// stacked on top of provider-specific edge auth.
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

// CloseIdleConnections delegates.
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

// chainTransport returns the full RoundTripper stack for talking to
// a Daytona-hosted clank-host: bearer (auth-token, app layer) wrapping
// preview-token (Daytona edge layer) wrapping http.DefaultTransport.
//
// An empty authToken means "no app-layer auth" (pre-PR-2 sandboxes
// adopted via label-recovery); the bearer header is omitted so
// clank-host's no-token-set path is exercised.
func chainTransport(authToken, previewToken string) http.RoundTripper {
	rt := http.RoundTripper(&previewTokenInjector{token: previewToken})
	if authToken != "" {
		rt = &bearerInjector{wrapped: rt, token: authToken}
	}
	return rt
}

// generateAuthToken returns a cryptographically-random URL-safe token
// suitable for the require-bearer middleware. ~256 bits of entropy.
func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
