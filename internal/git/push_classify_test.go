package git

import "testing"

// TestIsAuthFailure is the regression battery for the stderr-pattern
// classifier. Each entry locks in one git/server variant so a future
// edit to the needle list can't silently regress recognition.
func TestIsAuthFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"github 401", "remote: HTTP Basic: Access denied\nfatal: Authentication failed for 'https://github.com/x/y'", true},
		{"https 401 unauth", "fatal: unable to access 'https://example.com/x/y': The requested URL returned error: 401 Unauthorized", true},
		{"https 403 forbidden", "fatal: unable to access 'https://example.com/x/y': The requested URL returned error: 403 Forbidden", true},
		{"prompts disabled", "fatal: could not read Username for 'https://github.com': terminal prompts disabled", true},
		{"ssh publickey", "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.", true},
		{"ssh host key", "Host key verification failed.\nfatal: Could not read from remote repository.", true},
		{"non-fast-forward (not auth)", "! [rejected]        main -> main (non-fast-forward)", false},
		{"empty", "", false},
		{"unrelated stderr", "error: failed to push some refs", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isAuthFailure(tc.in); got != tc.want {
				t.Fatalf("isAuthFailure(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
