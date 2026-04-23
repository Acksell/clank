package agent

import (
	"strings"
	"testing"
)

func TestGitCredentialValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		c       GitCredential
		wantErr bool
	}{
		{"anonymous ok", GitCredential{Kind: GitCredAnonymous}, false},
		{"anonymous with token rejected", GitCredential{Kind: GitCredAnonymous, Token: "x"}, true},
		{"ssh agent ok", GitCredential{Kind: GitCredSSHAgent}, false},
		{"ssh agent with password rejected", GitCredential{Kind: GitCredSSHAgent, Password: "x"}, true},
		{"https basic ok", GitCredential{Kind: GitCredHTTPSBasic, Username: "u", Password: "p"}, false},
		{"https basic missing pw", GitCredential{Kind: GitCredHTTPSBasic, Username: "u"}, true},
		{"https basic with token rejected", GitCredential{Kind: GitCredHTTPSBasic, Username: "u", Password: "p", Token: "t"}, true},
		{"https token ok", GitCredential{Kind: GitCredHTTPSToken, Token: "t"}, false},
		{"https token with username rejected", GitCredential{Kind: GitCredHTTPSToken, Token: "t", Username: "u"}, true},
		{"unknown kind", GitCredential{Kind: "what"}, true},
		{"empty kind", GitCredential{}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.c.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestGitCredentialRedactedNeverLeaks: secrets must never appear in
// the log-safe rendering. Regression guard against accidental %v.
func TestGitCredentialRedactedNeverLeaks(t *testing.T) {
	t.Parallel()
	secret := "s3cr3t-token-value"
	cases := []GitCredential{
		{Kind: GitCredHTTPSBasic, Username: "alice", Password: secret},
		{Kind: GitCredHTTPSToken, Token: secret},
	}
	for _, c := range cases {
		got := c.Redacted()
		if strings.Contains(got, secret) {
			t.Fatalf("Redacted() leaked secret: %q", got)
		}
	}
}
