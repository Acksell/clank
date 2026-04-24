package gitendpoint

import "testing"

// TestRedactURL is a regression test for the voice tool error path
// that previously echoed the raw remote URL — including any userinfo
// — into the parse-failure error string. The function must strip
// userinfo from every shape we accept (https, ssh URL form, scp
// shorthand) and never return the secret unchanged.
func TestRedactURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain https", "https://github.com/a/b.git", "https://github.com/a/b.git"},
		{
			"https with token in userinfo",
			"https://x-access-token:ghp_secret@github.com/a/b.git",
			"https://github.com/a/b.git",
		},
		{
			"https with user:pass and query",
			"https://user:pass@example.com/a/b?ref=main",
			"https://example.com/a/b",
		},
		{
			"ssh url form keeps user removed",
			"ssh://git:hunter2@github.com/a/b.git",
			"ssh://github.com/a/b.git",
		},
		{
			"scp-style with user",
			"git@github.com:a/b.git",
			"<redacted>@github.com:a/b.git",
		},
		{
			"scp-style without user is unchanged",
			"github.com:a/b.git",
			"github.com:a/b.git",
		},
		{
			"unparseable falls back to scheme-only",
			"https://%%bad@host/path",
			"https://<redacted>",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RedactURL(tc.in)
			if got != tc.want {
				t.Fatalf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Cross-check: the secret token from the table inputs
			// must not survive in the output.
			for _, secret := range []string{"ghp_secret", "hunter2", "pass"} {
				if got != "" && containsSubstr(got, secret) {
					t.Fatalf("RedactURL(%q) leaked %q in %q", tc.in, secret, got)
				}
			}
		})
	}
}

func containsSubstr(s, sub string) bool {
	if sub == "" {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
