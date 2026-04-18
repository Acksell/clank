package agent

import "testing"

func TestGitRefDisplayName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ref  GitRef
		want string
	}{
		{"https remote", GitRef{Kind: GitRefRemote, URL: "https://github.com/acksell/clank.git"}, "clank"},
		{"scp remote", GitRef{Kind: GitRefRemote, URL: "git@github.com:acksell/clank.git"}, "clank"},
		{"file URL", GitRef{Kind: GitRefRemote, URL: "file:///srv/git/foo.git"}, "foo"},
		{"local abs", GitRef{Kind: GitRefLocal, Path: "/Users/x/code/clank"}, "clank"},
		{"local trailing", GitRef{Kind: GitRefLocal, Path: "/Users/x/code/clank/"}, "clank"},
		{"invalid kind", GitRef{Kind: "weird"}, ""},
		{"local relative", GitRef{Kind: GitRefLocal, Path: "rel/path"}, ""},
		{"remote bad URL", GitRef{Kind: GitRefRemote, URL: "::::"}, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.ref.DisplayName(); got != tc.want {
				t.Fatalf("DisplayName()=%q want %q", got, tc.want)
			}
		})
	}
}
