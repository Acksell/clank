package hub

import (
	"context"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// alwaysTokenDiscoverer hands out a github PAT for any endpoint. Used
// to prove that ResolveCredential refuses to forward it for
// non-HTTPS protocols regardless of what the discoverer offers.
type alwaysTokenDiscoverer struct{}

func (alwaysTokenDiscoverer) DiscoverFor(_ context.Context, _ host.Hostname, _ *agent.GitEndpoint) (agent.GitCredential, error) {
	return agent.GitCredential{
		Kind:     agent.GitCredHTTPSBasic,
		Username: "x-access-token",
		Password: "ghp_must_not_leak",
	}, nil
}

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
		{
			// Cleartext HTTP must stay anonymous even with a
			// discoverer present — attaching a PAT would leak it
			// over the wire.
			name:      "http never gets a token",
			target:    host.HostLocal,
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoHTTP, Host: "github.com", Path: "a/b"},
			wantKind:  agent.GitCredAnonymous,
			wantProto: agent.GitProtoHTTP,
		},
		{
			// git:// has no Basic auth channel; trying to attach a
			// token is meaningless and the push-retry/save flow can
			// never recover.
			name:      "git:// never gets a token",
			target:    host.HostLocal,
			ep:        &agent.GitEndpoint{Protocol: agent.GitProtoGit, Host: "github.com", Path: "a/b"},
			wantKind:  agent.GitCredAnonymous,
			wantProto: agent.GitProtoGit,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Inject a discoverer that ALWAYS returns a token so we
			// can prove that http/git refuse to attach it. HTTPS
			// cases above don't pass this discoverer (nil) so they
			// keep their anonymous expectations.
			var disc credDiscoverer
			if tc.ep != nil && (tc.ep.Protocol == agent.GitProtoHTTP || tc.ep.Protocol == agent.GitProtoGit) {
				disc = alwaysTokenDiscoverer{}
			}
			cred, gotEp, err := ResolveCredential(context.Background(), tc.target, tc.ep, disc)
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
