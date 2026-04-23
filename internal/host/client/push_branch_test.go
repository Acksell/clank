package hostclient_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostclient "github.com/acksell/clank/internal/host/client"
	hostmux "github.com/acksell/clank/internal/host/mux"
)

// initPushRepo is a test-local fixture (not reused from the host
// package's push_branch_test.go because that file is in a different
// _test package). It mirrors the shape: bare "origin" remote plus a
// clone with main pushed and a feature branch ahead by one commit.
func initPushRepo(t *testing.T, feature string) string {
	t.Helper()
	bareParent := t.TempDir()
	bare := filepath.Join(bareParent, "remote.git")
	runT(t, bareParent, "git", "init", "--bare", "-b", "main", "remote.git")

	workParent := t.TempDir()
	work := filepath.Join(workParent, "work")
	runT(t, workParent, "git", "clone", bare, "work")
	runT(t, work, "git", "config", "user.email", "t@t")
	runT(t, work, "git", "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, work, "git", "checkout", "-b", "main")
	runT(t, work, "git", "add", ".")
	runT(t, work, "git", "commit", "-m", "initial")
	runT(t, work, "git", "push", "-u", "origin", "main")
	runT(t, work, "git", "checkout", "-b", feature)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runT(t, work, "git", "add", ".")
	runT(t, work, "git", "commit", "-m", "add a")
	return work
}

func runT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestHTTPRoundTrip_PushBranch exercises the full wire path:
// host.Service.PushBranch -> hostmux /worktrees/push -> httptest
// server -> hostclient.HTTP.PushBranch. It validates the happy path
// and the two sentinel-mapping cases the TUI relies on: "nothing to
// push" (second push) and "cannot push default" (sent as 409 with
// code cannot_push_default, client remaps to host.ErrCannotPushDefault
// via errors.Is).
func TestHTTPRoundTrip_PushBranch(t *testing.T) {
	t.Parallel()

	mgr := &stubManager{}
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: mgr,
		},
	})
	t.Cleanup(svc.Shutdown)

	work := initPushRepo(t, "feature/x")
	srv := httptest.NewServer(hostmux.New(svc, nil).Handler())
	t.Cleanup(srv.Close)

	c := hostclient.NewHTTP(srv.URL, nil)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ref := agent.GitRef{LocalPath: work}
	cred := agent.GitCredential{Kind: agent.GitCredAnonymous}

	res, err := c.PushBranch(ctx, ref, cred, "feature/x")
	if err != nil {
		t.Fatalf("PushBranch new: %v", err)
	}
	if res.Branch != "feature/x" || res.Remote != "origin" || res.CommitsAhead != 1 {
		t.Errorf("unexpected result over wire: %+v", res)
	}

	_, err = c.PushBranch(ctx, ref, cred, "feature/x")
	if !errors.Is(err, host.ErrNothingToPush) {
		t.Errorf("second push: expected ErrNothingToPush through wire, got %v", err)
	}

	_, err = c.PushBranch(ctx, ref, cred, "main")
	if !errors.Is(err, host.ErrCannotPushDefault) {
		t.Errorf("default-branch push: expected ErrCannotPushDefault through wire, got %v", err)
	}
}
