package gitcred

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// Unit tests for [GHDiscoverer]. We never actually exec `gh`; we
// inject lookPath and run so the tests are hermetic and fast.

func TestGHDiscoverer_NonGithubHostIsSoftMiss(t *testing.T) {
	t.Parallel()
	g := &GHDiscoverer{
		lookPath: func(string) (string, error) {
			t.Fatal("lookPath unexpectedly called for non-github host")
			return "", nil
		},
	}
	_, err := g.Discover(context.Background(), validEp(t, "gitlab.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

func TestGHDiscoverer_GhMissingIsSoftMiss(t *testing.T) {
	t.Parallel()
	g := &GHDiscoverer{
		lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}
	_, err := g.Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

func TestGHDiscoverer_NotLoggedInIsSoftMiss(t *testing.T) {
	t.Parallel()
	// Run a real subprocess that exits non-zero so we get a genuine
	// *exec.ExitError — matches what `gh auth token` does when not
	// logged in. /usr/bin/false exists on macOS and Linux; if not we
	// fall back to `sh -c 'exit 1'`.
	exitErrCmd := func(ctx context.Context) error {
		if _, err := exec.LookPath("false"); err == nil {
			return exec.CommandContext(ctx, "false").Run()
		}
		return exec.CommandContext(ctx, "sh", "-c", "exit 1").Run()
	}
	g := &GHDiscoverer{
		lookPath: func(string) (string, error) { return "/usr/bin/gh", nil },
		run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, exitErrCmd(ctx)
		},
	}
	_, err := g.Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

func TestGHDiscoverer_SuccessReturnsBasicCred(t *testing.T) {
	t.Parallel()
	g := &GHDiscoverer{
		lookPath: func(string) (string, error) { return "/bin/echo", nil },
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("ghp_realtoken\n"), nil
		},
	}
	cred, err := g.Discover(context.Background(), validEp(t, "github.com"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Kind != agent.GitCredHTTPSBasic {
		t.Fatalf("kind = %q, want https_basic", cred.Kind)
	}
	if cred.Password != "ghp_realtoken" {
		t.Fatalf("password = %q, want ghp_realtoken (trimmed)", cred.Password)
	}
}

func TestGHDiscoverer_TimeoutIsHardError(t *testing.T) {
	t.Parallel()
	// Force a deadline-exceeded by sleeping past the parent ctx.
	g := &GHDiscoverer{
		lookPath: func(string) (string, error) { return "/bin/sleep", nil },
		run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := g.Discover(parent, validEp(t, "github.com"))
	if err == nil || errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want hard error (not ErrNoCredential)", err)
	}
}

func TestGHDiscoverer_EmptyOutputIsSoftMiss(t *testing.T) {
	t.Parallel()
	// `gh auth token` exiting 0 with empty stdout shouldn't happen,
	// but if it does we treat it as "no credential" rather than
	// constructing an invalid empty-password basic cred.
	g := &GHDiscoverer{
		lookPath: func(string) (string, error) { return "/bin/echo", nil },
		run:      func(context.Context, string, ...string) ([]byte, error) { return []byte("\n"), nil },
	}
	_, err := g.Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}
