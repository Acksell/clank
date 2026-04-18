package host

import "testing"

func TestHostLocalConstant(t *testing.T) {
	t.Parallel()
	// Sanity check: the canonical local host ID should not change without
	// updating callers (TUI defaults, doc references).
	if HostLocal != "local" {
		t.Fatalf("HostLocal changed: got %q, want \"local\"", HostLocal)
	}
}

func TestGitRef_Validate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		ref     GitRef
		wantErr bool
	}{
		{name: "empty_kind", ref: GitRef{}, wantErr: true},
		{name: "unknown_kind", ref: GitRef{Kind: "weird", URL: "x"}, wantErr: true},

		{name: "remote_ok_https", ref: GitRef{Kind: GitRefRemote, URL: "https://github.com/acksell/clank.git"}},
		{name: "remote_ok_scp", ref: GitRef{Kind: GitRefRemote, URL: "git@github.com:acksell/clank.git"}},
		{name: "remote_ok_with_sha", ref: GitRef{Kind: GitRefRemote, URL: "https://github.com/x/y", CommitSHA: "deadbeef"}},
		{name: "remote_missing_url", ref: GitRef{Kind: GitRefRemote}, wantErr: true},
		{name: "remote_blank_url", ref: GitRef{Kind: GitRefRemote, URL: "   "}, wantErr: true},
		{name: "remote_with_path_rejected", ref: GitRef{Kind: GitRefRemote, URL: "https://x/y", Path: "/tmp"}, wantErr: true},
		{name: "remote_invalid_url", ref: GitRef{Kind: GitRefRemote, URL: "not a url"}, wantErr: true},

		{name: "local_ok_abs", ref: GitRef{Kind: GitRefLocal, Path: "/tmp/repo"}},
		{name: "local_ok_with_sha", ref: GitRef{Kind: GitRefLocal, Path: "/tmp/repo", CommitSHA: "abc"}},
		{name: "local_missing_path", ref: GitRef{Kind: GitRefLocal}, wantErr: true},
		{name: "local_relative_path", ref: GitRef{Kind: GitRefLocal, Path: "relative/path"}, wantErr: true},
		{name: "local_with_url_rejected", ref: GitRef{Kind: GitRefLocal, Path: "/tmp", URL: "https://x/y"}, wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.ref.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGitRef_Canonical(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ref  GitRef
		want string
	}{
		{name: "remote_https_dotgit", ref: GitRef{Kind: GitRefRemote, URL: "https://github.com/acksell/clank.git"}, want: "github.com/acksell/clank"},
		{name: "remote_https_no_dotgit", ref: GitRef{Kind: GitRefRemote, URL: "https://github.com/acksell/clank"}, want: "github.com/acksell/clank"},
		{name: "remote_scp", ref: GitRef{Kind: GitRefRemote, URL: "git@github.com:acksell/clank.git"}, want: "github.com/acksell/clank"},
		{name: "remote_ssh_scheme", ref: GitRef{Kind: GitRefRemote, URL: "ssh://git@github.com/acksell/clank.git"}, want: "github.com/acksell/clank"},
		{name: "remote_uppercase_lowercased", ref: GitRef{Kind: GitRefRemote, URL: "https://GitHub.com/Acksell/Clank.git"}, want: "github.com/acksell/clank"},

		{name: "local_abs_path", ref: GitRef{Kind: GitRefLocal, Path: "/Users/me/proj"}, want: "/Users/me/proj"},

		{name: "invalid_empty", ref: GitRef{}, want: ""},
		{name: "remote_bad_url", ref: GitRef{Kind: GitRefRemote, URL: "not a url"}, want: ""},
		{name: "local_relative", ref: GitRef{Kind: GitRefLocal, Path: "rel"}, want: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.ref.Canonical()
			if got != tc.want {
				t.Fatalf("Canonical()=%q want %q", got, tc.want)
			}
			// Idempotence: re-canonicalizing a synthesized remote ref by feeding
			// the canonical back in as a URL must yield the same value.
			if tc.want != "" && tc.ref.Kind == GitRefRemote {
				again := GitRef{Kind: GitRefRemote, URL: got}.Canonical()
				if again != got {
					t.Fatalf("not idempotent: %q -> %q", got, again)
				}
			}
		})
	}
}

func TestGitRef_Equal(t *testing.T) {
	t.Parallel()

	a := GitRef{Kind: GitRefRemote, URL: "https://github.com/acksell/clank.git"}
	b := GitRef{Kind: GitRefRemote, URL: "git@github.com:Acksell/Clank.git"}
	c := GitRef{Kind: GitRefRemote, URL: "https://github.com/other/repo.git"}
	la := GitRef{Kind: GitRefLocal, Path: "/tmp/repo"}
	lb := GitRef{Kind: GitRefLocal, Path: "/tmp/repo"}
	lc := GitRef{Kind: GitRefLocal, Path: "/tmp/other"}
	bad := GitRef{}

	if !a.Equal(a) {
		t.Fatal("reflexivity failed for remote")
	}
	if !a.Equal(b) || !b.Equal(a) {
		t.Fatal("https/scp same repo not equal (or asymmetric)")
	}
	if a.Equal(c) || c.Equal(a) {
		t.Fatal("different remotes equal")
	}
	if !la.Equal(lb) || !lb.Equal(la) {
		t.Fatal("identical local paths not equal")
	}
	if la.Equal(lc) {
		t.Fatal("different local paths equal")
	}
	if a.Equal(la) {
		t.Fatal("remote and local equal across kinds")
	}
	// CommitSHA is advisory: two refs that differ only in SHA still equal.
	a2 := a
	a2.CommitSHA = "deadbeef"
	if !a.Equal(a2) {
		t.Fatal("CommitSHA difference broke equality")
	}
	// Invalid refs are never equal to anything (including themselves).
	if bad.Equal(bad) {
		t.Fatal("invalid ref equal to itself")
	}
	if a.Equal(bad) || bad.Equal(a) {
		t.Fatal("valid ref equal to invalid ref")
	}
}
