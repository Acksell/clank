package hostclient

import (
	"fmt"
	"maps"
	"net/http"
)

// NewRemoteHTTP constructs an HTTP client for a clank-host reachable
// over the public internet (or any non-Unix transport). Every outbound
// request — including the long-lived SSE stream — gets the supplied
// headers injected by a RoundTripper wrapper, so callers don't need to
// thread auth through every request site.
//
// This is the entry point used by the Daytona launcher: baseURL is the
// preview-URL origin (e.g. "https://8080-{sandboxId}.proxy.daytona.work")
// and headers carries {"x-daytona-preview-token": <token>}.
//
// baseURL must not end with a trailing slash; the path-building code in
// http.go concatenates "/foo" directly. headers must be non-empty —
// constructing a "remote" client with no auth headers is almost
// certainly a bug, so we fail fast rather than silently producing an
// unauthenticated client.
func NewRemoteHTTP(baseURL string, headers map[string]string) (*HTTP, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("hostclient.NewRemoteHTTP: baseURL is required")
	}
	if len(headers) == 0 {
		return nil, fmt.Errorf("hostclient.NewRemoteHTTP: headers is required (use NewHTTP for unauthenticated clients)")
	}
	tr := &headerTransport{
		base:    http.DefaultTransport,
		headers: maps.Clone(headers),
	}
	return NewHTTP(baseURL, tr), nil
}

// headerTransport is a thin RoundTripper that copies a static header
// bag onto every request before delegating to base. Single chokepoint
// for both `do` (one-shot JSON calls) and the SSE stream — both go
// through the same *http.Client.
//
// The header bag is cloned at construction so callers can't mutate it
// out from under in-flight requests, and we never mutate the inbound
// request's existing headers (defensive copy via Clone).
type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Per net/http docs: RoundTripper must not modify the request.
	// Clone is the documented way to obtain a mutable copy.
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		clone.Header.Set(k, v)
	}
	return t.base.RoundTrip(clone)
}
