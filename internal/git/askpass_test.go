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

// TestAskpassScriptForCreds_DispatchesByPrompt is a regression test
// for the bug where AskpassScript answered every git prompt with the
// password. Git invokes askpass once per missing credential field
// (Username, then Password) with the prompt text on argv[1]. The
// helper must return the username for "Username for ..." prompts and
// the secret otherwise — otherwise HTTPS Basic auth silently
// authenticates as user=<password> and fails on hosts that validate
// the username separately.
func TestAskpassScriptForCreds_DispatchesByPrompt(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("askpass not supported on windows")
	}

	const username = "alice"
	const secret = "s3cret with spaces"

	env, cleanup, err := AskpassScriptForCreds(username, secret)
	if err != nil {
		t.Fatalf("AskpassScriptForCreds: %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	var path string
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			path = strings.TrimPrefix(kv, "GIT_ASKPASS=")
		}
	}
	if path == "" {
		t.Fatalf("no GIT_ASKPASS in env: %v", env)
	}

	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{"username prompt", "Username for 'https://github.com': ", username},
		{"password prompt", "Password for 'https://alice@github.com': ", secret},
		{"unknown prompt falls back to secret", "Some other prompt: ", secret},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := exec.Command(path, tc.prompt).Output()
			if err != nil {
				t.Fatalf("exec script: %v", err)
			}
			if string(out) != tc.want {
				t.Fatalf("got %q want %q", out, tc.want)
			}
		})
	}
}

// TestAskpassScriptForCreds_EmptyUsernameMatchesLegacy verifies the
// no-username path still returns the secret for any prompt. This is
// what AskpassScript-as-wrapper relies on; if it broke we'd start
// returning empty username strings to git's Username prompt and break
// every HTTPS Token clone.
func TestAskpassScriptForCreds_EmptyUsernameMatchesLegacy(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("askpass not supported on windows")
	}
	const secret = "tok"
	env, cleanup, err := AskpassScriptForCreds("", secret)
	if err != nil {
		t.Fatalf("AskpassScriptForCreds: %v", err)
	}
	defer func() { _ = cleanup() }()
	var path string
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_ASKPASS=") {
			path = strings.TrimPrefix(kv, "GIT_ASKPASS=")
		}
	}
	out, err := exec.Command(path, "Username for 'https://x': ").Output()
	if err != nil {
		t.Fatalf("exec script: %v", err)
	}
	if string(out) != secret {
		t.Fatalf("got %q want %q", out, secret)
	}
}
