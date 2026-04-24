package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// SeedIdentityIfMissing sets `user.name` / `user.email` in the local
// repo config at repoDir for any of the two keys that is currently
// unset. Existing values are preserved — this is meant for blank-state
// remote sandboxes (e.g. a fresh Daytona clone) where neither key is
// configured and the agent's first `git commit` would otherwise fail
// with "Please tell me who you are".
//
// Operates on --local config so worktrees of the same repo share the
// identity (worktrees see the same .git/config) without polluting any
// global config the host might have.
//
// Idempotent: safe to call on every workDirFor invocation. The cost is
// 1-2 `git config --get` reads per call, plus 0-2 writes the first
// time.
func SeedIdentityIfMissing(repoDir, name, email string) error {
	if name == "" || email == "" {
		return fmt.Errorf("seed identity: name and email are required")
	}
	if err := setIfUnset(repoDir, "user.name", name); err != nil {
		return err
	}
	if err := setIfUnset(repoDir, "user.email", email); err != nil {
		return err
	}
	return nil
}

// setIfUnset reads `git config --local --get key` and only writes when
// the key is missing or empty. We can't use gitCmd's error directly
// because `git config --get` exits 1 both when the key is unset AND on
// other failures (malformed config etc.). Distinguishing isn't worth
// the complexity here — on any read failure we attempt the write,
// which will surface the real error.
func setIfUnset(repoDir, key, value string) error {
	cmd := exec.Command("git", "config", "--local", "--get", key)
	cmd.Dir = repoDir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // exit 1 on unset is expected; ignore
	if strings.TrimSpace(stdout.String()) != "" {
		return nil
	}
	if _, err := gitCmd(repoDir, "config", "--local", key, value); err != nil {
		return fmt.Errorf("set %s=%q: %w", key, value, err)
	}
	return nil
}

// LocalGlobalIdentity returns the laptop user's `git config --global
// user.name` and `user.email`. The hub calls this once per remote host
// provision to propagate the identity to the sandbox.
//
// Returns a hard error when either key is unset. Per AGENTS.md "no
// fallbacks": we don't synthesize a placeholder like "clank@local" —
// the user has to set their git identity once, locally, before
// provisioning a remote host.
func LocalGlobalIdentity() (name, email string, err error) {
	name, err = readGlobal("user.name")
	if err != nil {
		return "", "", err
	}
	email, err = readGlobal("user.email")
	if err != nil {
		return "", "", err
	}
	return name, email, nil
}

func readGlobal(key string) (string, error) {
	cmd := exec.Command("git", "config", "--global", "--get", key)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	// `git config --get` exits 1 when the key is unset (which is
	// what we want to surface as "not set" with a friendly hint).
	// Any other exit code — or a non-ExitError like "git not on
	// PATH" or a permissions failure — is a real environmental
	// problem the user needs to see verbatim.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("git config --global --get %s exited %d: %s",
				key, exitErr.ExitCode(), strings.TrimSpace(stderr.String()))
		}
	} else if err != nil {
		return "", fmt.Errorf("git config --global --get %s: %w", key, err)
	}
	v := strings.TrimSpace(stdout.String())
	if v == "" {
		return "", fmt.Errorf("git global %s is not set; run `git config --global %s <value>`", key, key)
	}
	return v, nil
}
