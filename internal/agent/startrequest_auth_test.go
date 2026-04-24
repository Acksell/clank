package agent_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/agent"
)

// Regression for CodeRabbit comment on agent.go:335-343 — Auth was a
// non-pointer GitCredential, so json:"omitempty" never actually omitted
// it: the empty struct still marshals to `"auth":{"kind":""}`. Switching
// to *GitCredential makes the wire shape honest about absence.
func TestStartRequest_AuthOmitemptyOmitsNil(t *testing.T) {
	t.Parallel()

	req := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{LocalPath: "/tmp/x"},
		Prompt:  "hi",
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"auth"`) {
		t.Fatalf("expected no auth key when Auth is nil, got: %s", b)
	}
}

func TestStartRequest_AuthRoundTripsExplicitCredential(t *testing.T) {
	t.Parallel()

	in := agent.StartRequest{
		Backend: agent.BackendOpenCode,
		GitRef:  agent.GitRef{LocalPath: "/tmp/x"},
		Prompt:  "hi",
		Auth: &agent.GitCredential{
			Kind:  agent.GitCredHTTPSToken,
			Token: "ghp_secret",
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"token":"ghp_secret"`) {
		t.Fatalf("expected token to round-trip, got: %s", b)
	}
	var out agent.StartRequest
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Auth == nil || out.Auth.Kind != agent.GitCredHTTPSToken || out.Auth.Token != "ghp_secret" {
		t.Fatalf("auth round-trip mismatch: %+v", out.Auth)
	}
}

// A wire payload with no `auth` key at all must decode to a nil pointer
// (so callers / mux handlers can detect absence and decide whether to
// reject or default). Previously this decoded to a zero-valued struct
// indistinguishable from an explicit `{"kind":""}`.
func TestStartRequest_AuthDecodesNilWhenAbsent(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"backend":"opencode","git_ref":{"local_path":"/tmp/x"},"prompt":"hi"}`)
	var req agent.StartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Auth != nil {
		t.Fatalf("expected Auth nil when absent on the wire, got %+v", req.Auth)
	}
}
