package agent

import "testing"

// epHTTPS is a small helper for the test cases below.
func epHTTPS(t *testing.T, host, path string) *GitEndpoint {
	t.Helper()
	return &GitEndpoint{Protocol: GitProtoHTTPS, Host: host, Path: path}
}

func TestRepoDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  GitRef
		want string
	}{
		{"endpoint https", GitRef{Endpoint: epHTTPS(t, "github.com", "acksell/clank")}, "clank"},
		{"endpoint ssh", GitRef{Endpoint: &GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"}}, "clank"},
		{"endpoint file", GitRef{Endpoint: &GitEndpoint{Protocol: GitProtoFile, Path: "srv/git/foo"}}, "foo"},
		{"local abs", GitRef{LocalPath: "/Users/x/code/clank"}, "clank"},
		{"local trailing slash", GitRef{LocalPath: "/Users/x/code/clank/"}, "clank"},
		{"both prefers endpoint", GitRef{LocalPath: "/Users/x/code/wrong", Endpoint: epHTTPS(t, "github.com", "acksell/clank")}, "clank"},
		{"empty ref", GitRef{}, ""},
		{"local relative", GitRef{LocalPath: "rel/path"}, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RepoDisplayName(tc.ref); got != tc.want {
				t.Fatalf("RepoDisplayName()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestGitRefValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ref     GitRef
		wantErr bool
	}{
		{"empty", GitRef{}, true},
		{"both set ok", GitRef{LocalPath: "/x", Endpoint: epHTTPS(t, "github.com", "acksell/clank")}, false},
		{"local ok", GitRef{LocalPath: "/tmp/repo"}, false},
		{"local relative", GitRef{LocalPath: "rel"}, true},
		{"endpoint ok", GitRef{Endpoint: epHTTPS(t, "github.com", "acksell/clank")}, false},
		{"endpoint missing host", GitRef{Endpoint: &GitEndpoint{Protocol: GitProtoHTTPS, Path: "acksell/clank"}}, true},
		{"endpoint trailing .git", GitRef{Endpoint: &GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "acksell/clank.git"}}, true},
		{"both but local relative", GitRef{LocalPath: "rel", Endpoint: epHTTPS(t, "github.com", "x/y")}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.ref.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRepoKey(t *testing.T) {
	t.Parallel()
	// Endpoint-keyed refs match across machines regardless of LocalPath.
	a := GitRef{LocalPath: "/Users/alice/code/clank", Endpoint: epHTTPS(t, "github.com", "acksell/clank"), WorktreeBranch: "main"}
	b := GitRef{LocalPath: "/home/bob/src/clank", Endpoint: epHTTPS(t, "github.com", "acksell/clank"), WorktreeBranch: "main"}
	if RepoKey(a) != RepoKey(b) {
		t.Fatalf("expected matching keys for same Endpoint+branch, got %q vs %q", RepoKey(a), RepoKey(b))
	}
	// Local-only refs key by LocalPath.
	c := GitRef{LocalPath: "/x", WorktreeBranch: "feat"}
	if RepoKey(c) == "" {
		t.Fatal("expected non-empty key for local-only ref")
	}
	if RepoKey(GitRef{}) != "" {
		t.Fatal("expected empty key for empty ref")
	}
}

// TestRepoKeyEndpointProtocolIndependent: ssh and https endpoints
// pointing at the same repo must share a key, so dedup tables don't
// double-count the same project once the credential resolver may
// rewrite ssh→https for remote-host forwards.
func TestRepoKeyEndpointProtocolIndependent(t *testing.T) {
	t.Parallel()
	sshRef := GitRef{
		Endpoint:       &GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"},
		WorktreeBranch: "main",
	}
	httpsRef := GitRef{
		Endpoint:       &GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
		WorktreeBranch: "main",
	}
	if RepoKey(sshRef) != RepoKey(httpsRef) {
		t.Fatalf("ssh and https endpoint keys differ: %q vs %q", RepoKey(sshRef), RepoKey(httpsRef))
	}
}

func TestRepoDisplayNameFromEndpoint(t *testing.T) {
	t.Parallel()
	ref := GitRef{Endpoint: &GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"}}
	if got := RepoDisplayName(ref); got != "clank" {
		t.Fatalf("RepoDisplayName()=%q want %q", got, "clank")
	}
}
