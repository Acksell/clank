package agent

import "testing"

func TestGitEndpointValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ep      *GitEndpoint
		wantErr bool
	}{
		{"nil", nil, true},
		{"https ok", &GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"}, false},
		{"ssh ok", &GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"}, false},
		{"file no host ok", &GitEndpoint{Protocol: GitProtoFile, Path: "srv/git/foo"}, false},
		{"file with authority ok", &GitEndpoint{Protocol: GitProtoFile, Host: "server", Path: "share/repo"}, false},
		{"unknown proto", &GitEndpoint{Protocol: "ftp", Host: "x", Path: "y"}, true},
		{"empty path", &GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com"}, true},
		{"leading slash", &GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "/acksell/clank"}, true},
		{"trailing .git", &GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "acksell/clank.git"}, true},
		{"https no host", &GitEndpoint{Protocol: GitProtoHTTPS, Path: "acksell/clank"}, true},
		{"port out of range", &GitEndpoint{Protocol: GitProtoHTTPS, Host: "x", Path: "y", Port: 70000}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.ep.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGitEndpointString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ep   *GitEndpoint
		want string
	}{
		{
			"https",
			&GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
			"https://github.com/acksell/clank.git",
		},
		{
			"ssh url-form (not scp)",
			&GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"},
			"ssh://git@github.com/acksell/clank.git",
		},
		{
			"ssh with port",
			&GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "example.com", Port: 2222, Path: "x/y"},
			"ssh://git@example.com:2222/x/y.git",
		},
		{
			"file no host",
			&GitEndpoint{Protocol: GitProtoFile, Path: "srv/git/foo"},
			"file:///srv/git/foo.git",
		},
		{
			"file with host",
			&GitEndpoint{Protocol: GitProtoFile, Host: "server", Path: "share/repo"},
			"file://server/share/repo.git",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ep.String(); got != tc.want {
				t.Fatalf("String()=%q want %q", got, tc.want)
			}
		})
	}
}

// TestGitEndpointCloneURL pins the file:// behavior: CloneURL must
// NOT append the trailing ".git" that String() does, because the
// on-disk path is the original directory and "<dir>.git" doesn't
// exist. Network protocols round-trip identically to String().
func TestGitEndpointCloneURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ep   *GitEndpoint
		want string
	}{
		{
			"file no host: no trailing .git",
			&GitEndpoint{Protocol: GitProtoFile, Path: "srv/git/foo"},
			"file:///srv/git/foo",
		},
		{
			"file with host: no trailing .git",
			&GitEndpoint{Protocol: GitProtoFile, Host: "server", Path: "share/repo"},
			"file://server/share/repo",
		},
		{
			"https: identical to String()",
			&GitEndpoint{Protocol: GitProtoHTTPS, Host: "github.com", Path: "acksell/clank"},
			"https://github.com/acksell/clank.git",
		},
		{
			"ssh: identical to String()",
			&GitEndpoint{Protocol: GitProtoSSH, User: "git", Host: "github.com", Path: "acksell/clank"},
			"ssh://git@github.com/acksell/clank.git",
		},
		{
			"nil: empty string",
			nil,
			"",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ep.CloneURL(); got != tc.want {
				t.Fatalf("CloneURL()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestGitEndpointIsLocal(t *testing.T) {
	t.Parallel()
	if (&GitEndpoint{Protocol: GitProtoFile, Path: "x"}).IsLocal() != true {
		t.Fatal("file:// should be local")
	}
	if (&GitEndpoint{Protocol: GitProtoHTTPS, Host: "x", Path: "y"}).IsLocal() {
		t.Fatal("https should not be local")
	}
	var nilEp *GitEndpoint
	if nilEp.IsLocal() {
		t.Fatal("nil should not be local")
	}
}
