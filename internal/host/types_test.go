package host

import "testing"

func TestRepoRef_ID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		url     string
		want    RepoID
		wantErr bool
	}{
		{name: "scp_like_with_git_suffix", url: "git@github.com:acksell/clank.git", want: "github.com/acksell/clank"},
		{name: "scp_like_no_suffix", url: "git@github.com:acksell/clank", want: "github.com/acksell/clank"},
		{name: "https_with_suffix", url: "https://github.com/acksell/clank.git", want: "github.com/acksell/clank"},
		{name: "https_no_suffix", url: "https://github.com/acksell/clank", want: "github.com/acksell/clank"},
		{name: "ssh_scheme", url: "ssh://git@github.com/acksell/clank.git", want: "github.com/acksell/clank"},
		{name: "git_scheme", url: "git://gitlab.example.com/team/proj.git", want: "gitlab.example.com/team/proj"},
		{name: "lowercased", url: "https://GitHub.com/Acksell/Clank.git", want: "github.com/acksell/clank"},
		{name: "nested_path", url: "https://gitlab.com/group/sub/proj.git", want: "gitlab.com/group/sub/proj"},
		{name: "schemeless_host_path", url: "github.com/acksell/clank", want: "github.com/acksell/clank"},

		{name: "empty", url: "", wantErr: true},
		{name: "no_path_https", url: "https://github.com/", wantErr: true},
		{name: "no_path_scp", url: "git@github.com:", wantErr: true},
		{name: "spaces_unsafe", url: "https://github.com/with space/repo", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ref := RepoRef{RemoteURL: tc.url}
			got, err := ref.ID()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got id=%q", tc.url, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.url, err)
			}
			if got != tc.want {
				t.Fatalf("RepoRef{%q}.ID() = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestRepoRef_IDIsDeterministic(t *testing.T) {
	t.Parallel()
	ref := RepoRef{RemoteURL: "git@github.com:acksell/clank.git"}
	first, err := ref.ID()
	if err != nil {
		t.Fatalf("first ID: %v", err)
	}
	for i := 0; i < 5; i++ {
		got, err := ref.ID()
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("repeat %d: got %q, want %q", i, got, first)
		}
	}
}

func TestRepoRef_Validate(t *testing.T) {
	t.Parallel()

	if err := (RepoRef{}).Validate(); err == nil {
		t.Fatal("expected error for empty RepoRef")
	}
	if err := (RepoRef{RemoteURL: "   "}).Validate(); err == nil {
		t.Fatal("expected error for whitespace-only RemoteURL")
	}
	if err := (RepoRef{RemoteURL: "https://example.com/"}).Validate(); err == nil {
		t.Fatal("expected error for URL with no repo path")
	}
	if err := (RepoRef{RemoteURL: "git@github.com:acksell/clank.git"}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHostLocalConstant(t *testing.T) {
	t.Parallel()
	// Sanity check: the canonical local host ID should not change without
	// updating callers (TUI defaults, doc references).
	if HostLocal != "local" {
		t.Fatalf("HostLocal changed: got %q, want \"local\"", HostLocal)
	}
}
