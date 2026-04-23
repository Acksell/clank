package gitcred

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// ghHost is the only host the `gh` CLI knows about by default.
// gh enterprise users need GH_HOST set, which we don't probe — they
// can use [EnvDiscoverer] or settings instead.
const ghHost = "github.com"

// ghTimeout caps the `gh auth token` subprocess. The command is local
// (just reads ~/.config/gh/hosts.yml) so it normally returns in <50ms;
// a 5s ceiling protects against a wedged or hung gh binary without
// being so tight it flakes on a busy CI.
const ghTimeout = 5 * time.Second

// GHDiscoverer shells out to `gh auth token` to obtain a github.com
// PAT from the user's existing gh login. Returns [ErrNoCredential]
// when:
//
//   - The endpoint host is not github.com.
//   - The `gh` binary is not on PATH.
//   - `gh auth token` exits non-zero (typically: not logged in).
//
// Any OTHER failure (timeout, exec error that isn't NotFound) is a
// hard error — a misconfigured gh shouldn't silently downgrade us to
// anonymous and produce a confusing 401 at push time.
type GHDiscoverer struct {
	// lookPath is the exec.LookPath equivalent, swappable for tests.
	// Nil = real exec.LookPath.
	lookPath func(string) (string, error)
	// run executes the resolved binary and returns stdout. Nil = real
	// exec.CommandContext + CombinedOutput. Tests inject a stub.
	run func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// FromGH returns the production [GHDiscoverer].
func FromGH() *GHDiscoverer { return &GHDiscoverer{} }

// Discover implements [Discoverer].
func (g *GHDiscoverer) Discover(ctx context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error) {
	if ep.Host != ghHost {
		return agent.GitCredential{}, ErrNoCredential
	}
	lookPath := g.lookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	bin, err := lookPath("gh")
	if err != nil {
		// LookPath failure means gh isn't installed; that's a soft miss.
		return agent.GitCredential{}, ErrNoCredential
	}

	subCtx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	run := g.run
	if run == nil {
		run = defaultRun
	}
	out, err := run(subCtx, bin, "auth", "token")
	if err != nil {
		// Distinguish "gh installed but not logged in" (exit code
		// non-zero, soft) from "context timed out" (hard).
		if errors.Is(subCtx.Err(), context.DeadlineExceeded) {
			return agent.GitCredential{}, fmt.Errorf("gh auth token: timed out after %s", ghTimeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return agent.GitCredential{}, ErrNoCredential
		}
		return agent.GitCredential{}, fmt.Errorf("gh auth token: %w", err)
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return agent.GitCredential{}, ErrNoCredential
	}
	return tokenAsBasic(tok), nil
}

// defaultRun is the production exec wrapper. Kept package-private so
// tests don't accidentally bypass [GHDiscoverer.run] injection.
func defaultRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{} // discard; gh prints "not logged in" hints there
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}
