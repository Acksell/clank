package agent

import "testing"

func TestRepoDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  GitRef
		want string
	}{
		{"https remote", GitRef{Remote: &RemoteRef{URL: "https://github.com/acksell/clank.git"}}, "clank"},
		{"scp remote", GitRef{Remote: &RemoteRef{URL: "git@github.com:acksell/clank.git"}}, "clank"},
		{"file URL", GitRef{Remote: &RemoteRef{URL: "file:///srv/git/foo.git"}}, "foo"},
		{"local abs", GitRef{Local: &LocalRef{Path: "/Users/x/code/clank"}}, "clank"},
		{"local trailing slash", GitRef{Local: &LocalRef{Path: "/Users/x/code/clank/"}}, "clank"},
		{"empty ref", GitRef{}, ""},
		{"local relative", GitRef{Local: &LocalRef{Path: "rel/path"}}, ""},
		{"remote bad URL", GitRef{Remote: &RemoteRef{URL: "::::"}}, ""},
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
		{"both set", GitRef{Local: &LocalRef{Path: "/x"}, Remote: &RemoteRef{URL: "https://x/y"}}, true},
		{"local ok", GitRef{Local: &LocalRef{Path: "/tmp/repo"}}, false},
		{"local empty path", GitRef{Local: &LocalRef{Path: ""}}, true},
		{"local relative", GitRef{Local: &LocalRef{Path: "rel"}}, true},
		{"remote https", GitRef{Remote: &RemoteRef{URL: "https://github.com/acksell/clank.git"}}, false},
		{"remote scp", GitRef{Remote: &RemoteRef{URL: "git@github.com:acksell/clank.git"}}, false},
		{"remote empty", GitRef{Remote: &RemoteRef{URL: ""}}, true},
		{"remote bad", GitRef{Remote: &RemoteRef{URL: "not a url"}}, true},
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
