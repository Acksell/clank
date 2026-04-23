package hub

import (
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

func TestResolveCredential(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		target      host.Hostname
		ep          *agent.GitEndpoint
		wantKind    agent.GitCredentialKind
		wantProto   agent.GitEndpointProtocol // expected protocol of the returned endpoint
		wantErr     bool
		wantRewrite bool // true if returned endpoint must differ from input
	}{
		{
			name:      "https local",
			target:    host.HostLocal,
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"},
			wantKind:  agent.GitCredAnonymous,
			wantProto: agent.GitProtoHTTPS,
		},
		{
			name:      "https remote (still anonymous)",
			target:    "daytona-1",
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "a/b"},
			wantKind:  agent.GitCredAnonymous,
			wantProto: agent.GitProtoHTTPS,
		},
		{
			name:      "ssh local uses agent",
			target:    host.HostLocal,
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "a/b"},
			wantKind:  agent.GitCredSSHAgent,
			wantProto: agent.GitProtoSSH,
		},
		{
			name:        "ssh remote allowlisted rewrites to https",
			target:      "daytona-1",
			ep:          &agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "a/b"},
			wantKind:    agent.GitCredAnonymous,
			wantProto:   agent.GitProtoHTTPS,
			wantRewrite: true,
		},
		{
			name:    "ssh remote unknown provider errors",
			target:  "daytona-1",
			ep:      &agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "git.internal.example", Path: "a/b"},
			wantErr: true,
		},
		{
			name:      "file local ok",
			target:    host.HostLocal,
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoFile, Path: "srv/git/foo"},
			wantKind:  agent.GitCredAnonymous,
			wantProto: agent.GitProtoFile,
		},
		{
			name:    "file remote rejected",
			target:  "daytona-1",
			ep:      &agent.GitEndpoint{Protocol: agent.GitProtoFile, Path: "srv/git/foo"},
			wantErr: true,
		},
		{
			name:    "nil endpoint",
			target:  host.HostLocal,
			ep:      nil,
			wantErr: true,
		},
		{
			name:      "empty hostname treated as local",
			target:    "",
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "a/b"},
			wantKind:  agent.GitCredSSHAgent,
			wantProto: agent.GitProtoSSH,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cred, gotEp, err := ResolveCredential(tc.target, tc.ep)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got cred=%+v ep=%+v", cred, gotEp)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cred.Kind != tc.wantKind {
				t.Fatalf("kind=%q want %q", cred.Kind, tc.wantKind)
			}
			if err := cred.Validate(); err != nil {
				t.Fatalf("returned credential fails Validate: %v", err)
			}
			if gotEp == nil {
				t.Fatal("returned nil endpoint without error")
			}
			if gotEp.Protocol != tc.wantProto {
				t.Fatalf("returned proto=%q want %q", gotEp.Protocol, tc.wantProto)
			}
			rewrote := gotEp != tc.ep || gotEp.Protocol != tc.ep.Protocol
			if tc.wantRewrite && !rewrote {
				t.Fatal("expected endpoint rewrite, got identity")
			}
			if !tc.wantRewrite && rewrote && gotEp.Protocol != tc.ep.Protocol {
				t.Fatalf("unexpected rewrite: %+v -> %+v", tc.ep, gotEp)
			}
		})
	}
}
