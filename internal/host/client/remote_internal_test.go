package hostclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHeaderTransport_DoesNotMutateInboundRequest verifies the
// RoundTripper contract: net/http.RoundTripper.RoundTrip must not
// modify the *http.Request it receives. headerTransport must Clone
// before injecting headers; this test fails if a future refactor
// drops the Clone and mutates req.Header in place.
//
// Lives in an internal test file because headerTransport is
// unexported — the prior _test.go variant could only call Status()
// (which builds requests internally) and so could never observe
// inbound mutation.
func TestHeaderTransport_DoesNotMutateInboundRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	tr := &headerTransport{
		base:    http.DefaultTransport,
		headers: map[string]string{"x-injected": "yes"},
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("x-original", "preserved")

	// Snapshot header values before RoundTrip.
	beforeOriginal := req.Header.Get("x-original")
	beforeInjected := req.Header.Get("x-injected")
	beforeLen := len(req.Header)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	// Original header preserved, no new keys added in place.
	if got := req.Header.Get("x-original"); got != beforeOriginal {
		t.Errorf("x-original mutated: before=%q after=%q", beforeOriginal, got)
	}
	if got := req.Header.Get("x-injected"); got != beforeInjected {
		t.Errorf("x-injected leaked onto inbound request: before=%q after=%q (RoundTripper must Clone before mutating)", beforeInjected, got)
	}
	if got := len(req.Header); got != beforeLen {
		t.Errorf("req.Header length changed: before=%d after=%d", beforeLen, got)
	}
}
