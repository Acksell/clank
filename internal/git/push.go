package git

// push.go implements the authenticated `git push` primitive used by
// host.Service.PushBranch. Mirrors the Clone shape: ctx + dir + cred,
// GIT_ASKPASS for token transit, structured sentinel errors for the
// three outcomes callers care to distinguish (rejected, auth missing,
// nothing to push).
//
// Deliberately thin: no retry, no force variants, no PR creation.
// Those belong in upper layers.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/acksell/clank/internal/agent"
)

// ErrPushRejected is returned when the remote refused the push as a
// non-fast-forward. Force-push is intentionally not supported — the
// publish plan calls for agentic rebase + conflict resolution later
// (see docs/publish_and_branch_defaults.md §Out of scope).
var ErrPushRejected = errors.New("push rejected: non-fast-forward")

// ErrPushAuthRequired is returned when the remote demanded auth and
// the supplied credential was anonymous, missing, or rejected. The
// caller (TUI) maps this to a "configure a credential" affordance
// once the token-discovery follow-up lands.
var ErrPushAuthRequired = errors.New("push requires authentication")

// ErrNothingToPush is returned when the local branch is already at or
// behind the remote; git's native exit code is 0 ("Everything
// up-to-date") but surfacing it as a distinct error lets the TUI
// avoid showing a misleading "pushed" confirmation.
var ErrNothingToPush = errors.New("nothing to push")

// Push pushes branch from dir to remote using cred. Sets upstream
// tracking on first push (-u); subsequent pushes are idempotent with
// the same flag.
//
// Maps git's three interesting failure modes to typed sentinels:
// ErrPushRejected (non-fast-forward), ErrPushAuthRequired (auth), and
// ErrNothingToPush (already up-to-date). Other failures are wrapped
// verbatim for diagnostics.
//
// Never force-pushes. If the remote has diverged, the caller must
// rebase locally before calling Push again.
func Push(ctx context.Context, dir, remote, branch string, cred agent.GitCredential) error {
	if err := cred.Validate(); err != nil {
		return fmt.Errorf("push: invalid credential: %w", err)
	}
	if strings.TrimSpace(remote) == "" {
		return fmt.Errorf("push: remote is required")
	}
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("push: branch is required")
	}

	envExtra, cleanup, err := gitAuthEnv(cred)
	if err != nil {
		return fmt.Errorf("push: prepare auth env: %w", err)
	}
	if cleanup != nil {
		defer func() { _ = cleanup() }()
	}

	cmd := exec.CommandContext(ctx, "git", "push", "-u", remote, branch)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new",
	)
	cmd.Env = append(cmd.Env, envExtra...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Git emits "Everything up-to-date" on stderr with exit 0 when
	// there is nothing to push. Surface as ErrNothingToPush so the
	// caller doesn't show a misleading success.
	combined := stderr.String() + stdout.String()
	if runErr == nil {
		if strings.Contains(combined, "Everything up-to-date") {
			return ErrNothingToPush
		}
		return nil
	}

	// Classify failure. Order matters: auth detection first because
	// an auth failure can also print "rejected" noise in some git
	// versions.
	if isAuthFailure(combined) {
		return fmt.Errorf("%w: %s", ErrPushAuthRequired, strings.TrimSpace(combined))
	}
	if isNonFastForward(combined) {
		return fmt.Errorf("%w: %s", ErrPushRejected, strings.TrimSpace(combined))
	}
	return fmt.Errorf("git push %s %s: %s (%w)", remote, branch, strings.TrimSpace(combined), runErr)
}

// isAuthFailure detects auth-related stderr patterns across git
// versions and remote types (github, gitlab, generic http server).
// The strings are stable enough across git 2.30+ that pattern-matching
// is acceptable here; a structured alternative would require parsing
// exit code + a credential-helper protocol which does not exist.
func isAuthFailure(s string) bool {
	needles := []string{
		"Authentication failed",
		"could not read Username",
		"could not read Password",
		"terminal prompts disabled",
		"403 Forbidden",
		"Permission denied (publickey)",
		"Host key verification failed",
	}
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// isNonFastForward detects the "rejected because non-fast-forward"
// pattern. Both the short "! [rejected]" marker and the explanatory
// "non-fast-forward" text must be present to avoid false positives
// (a rejection can also come from pre-receive hooks, stale info, etc.).
func isNonFastForward(s string) bool {
	return strings.Contains(s, "! [rejected]") && strings.Contains(s, "non-fast-forward")
}
