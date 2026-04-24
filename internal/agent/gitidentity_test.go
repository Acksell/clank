package agent

import "testing"

func TestGitIdentityValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		id      GitIdentity
		wantErr bool
	}{
		{"ok", GitIdentity{Name: "Alice", Email: "a@example.com"}, false},
		{"missing name", GitIdentity{Email: "a@example.com"}, true},
		{"missing email", GitIdentity{Name: "Alice"}, true},
		{"empty", GitIdentity{}, true},
		// Whitespace-only must fail — git would accept these and
		// produce commits with blank authors, which is the same
		// silent mis-attribution Validate is meant to prevent
		// (CodeRabbit PR #3 inline 3137099771).
		{"whitespace name", GitIdentity{Name: "  \t", Email: "a@example.com"}, true},
		{"whitespace email", GitIdentity{Name: "Alice", Email: "\n "}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.id.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
