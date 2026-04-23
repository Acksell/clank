package hub

import (
	"testing"

	"github.com/acksell/clank/internal/agent"
)

func TestParseGitEndpoint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want *agent.GitEndpoint
	}{
		{
			"https with .git",
			"https://github.com/acksell/clank.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
		},
		{
			"https without .git",
			"https://github.com/acksell/clank",
			&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
		},
		{
			"https mixed-case host normalised",
			"https://GitHub.com/acksell/clank.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
		},
		{
			"https default port dropped",
			"https://github.com:443/acksell/clank.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
		},
		{
			"https custom port preserved",
			"https://example.com:8443/x/y.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoHTTPS, Host: "example.com", Port: 8443, Path: "x/y"},
		},
		{
			"scp form",
			"git@github.com:acksell/clank.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"},
		},
		{
			"ssh url form",
			"ssh://git@github.com/acksell/clank.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"},
		},
		{
			"ssh with custom port",
			"ssh://git@example.com:2222/x/y.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoSSH, User: "git", Host: "example.com", Port: 2222, Path: "x/y"},
		},
		{
			"file with absolute path",
			"file:///srv/git/foo.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoFile, Path: "srv/git/foo"},
		},
		{
			"git protocol",
			"git://github.com/acksell/clank.git",
			&agent.GitEndpoint{Protocol: agent.GitProtoGit, Host: "github.com", Path: "acksell/clank"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseGitEndpoint(tc.raw)
			if err != nil {
				t.Fatalf("ParseGitEndpoint(%q): %v", tc.raw, err)
			}
			if !endpointsEqual(got, tc.want) {
				t.Fatalf("ParseGitEndpoint(%q)=%+v want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseGitEndpointErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace", "   "},
		{"unsupported scheme", "ftp://x.example.com/y.git"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseGitEndpoint(tc.raw)
			if err == nil {
				t.Fatalf("expected error for %q", tc.raw)
			}
		})
	}
}

// TestParseGitEndpointSshHttpsShareKey: the whole point of the
// refactor — ssh and https URLs for the same repo must produce
// endpoints whose RepoKey collapses to one entry.
func TestParseGitEndpointSshHttpsShareKey(t *testing.T) {
	t.Parallel()
	ssh, err := ParseGitEndpoint("git@github.com:acksell/clank.git")
	if err != nil {
		t.Fatal(err)
	}
	https, err := ParseGitEndpoint("https://github.com/acksell/clank.git")
	if err != nil {
		t.Fatal(err)
	}
	a := agent.GitRef{Endpoint: ssh, WorktreeBranch: "main"}
	b := agent.GitRef{Endpoint: https, WorktreeBranch: "main"}
	if agent.RepoKey(a) != agent.RepoKey(b) {
		t.Fatalf("RepoKey diverged: %q vs %q", agent.RepoKey(a), agent.RepoKey(b))
	}
}

// TestParseGitEndpointRoundTrip: parser → String() → parser must be
// idempotent. Guards against drift between the parser's normalisation
// and the type's String() rendering.
func TestParseGitEndpointRoundTrip(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"https://github.com/acksell/clank.git",
		"ssh://git@github.com/acksell/clank.git",
		"ssh://git@example.com:2222/x/y.git",
		"file:///srv/git/foo.git",
		"git://github.com/acksell/clank.git",
	}
	for _, in := range inputs {
		in := in
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			ep1, err := ParseGitEndpoint(in)
			if err != nil {
				t.Fatal(err)
			}
			ep2, err := ParseGitEndpoint(ep1.String())
			if err != nil {
				t.Fatalf("re-parse of %q: %v", ep1.String(), err)
			}
			if !endpointsEqual(ep1, ep2) {
				t.Fatalf("round-trip drift: %+v -> %q -> %+v", ep1, ep1.String(), ep2)
			}
		})
	}
}

func endpointsEqual(a, b *agent.GitEndpoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
