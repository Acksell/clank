package git

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestAskpassScript_RoundTrip verifies the temp script git would invoke
// actually echoes the secret we baked into it. This is the contract
// git relies on: GIT_ASKPASS=<path> means "exec <path> and read the
// secret from stdout". If we ever broke the script format (escaping,
// shebang, exec semantics) git would silently degrade to a TTY prompt
// and clones would hang under -batch.
func TestAskpassScript_RoundTrip(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("askpass not supported on windows")
	}

	cases := []string{
		"plain-token",
		"with spaces",
		"with'single'quotes",
		"weird$chars`and\"more",
	}
	for _, secret := range cases {
		secret := secret
		t.Run(secret, func(t *testing.T) {
			t.Parallel()
			env, cleanup, err := AskpassScript(secret)
			if err != nil {
				t.Fatalf("AskpassScript: %v", err)
			}
			defer func() { _ = cleanup() }()

			var path string
			for _, kv := range env {
				if strings.HasPrefix(kv, "GIT_ASKPASS=") {
					path = strings.TrimPrefix(kv, "GIT_ASKPASS=")
				}
			}
			if path == "" {
				t.Fatalf("no GIT_ASKPASS in env: %v", env)
			}
			out, err := exec.Command(path).Output()
			if err != nil {
				t.Fatalf("exec script: %v", err)
			}
			if string(out) != secret {
				t.Fatalf("got %q want %q", out, secret)
			}
		})
	}
}

// TestAskpassScript_RejectsEmpty guards against a silent-failure mode
// where an empty secret would produce a script that prints nothing
// and git would fall through to its next credential helper, possibly
// surfacing the user's saved github creds when we wanted exactly the
// (empty) cred we were handed. Fail loudly instead.
func TestAskpassScript_RejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, _, err := AskpassScript(""); err == nil {
		t.Fatalf("expected error for empty secret")
	}
}
