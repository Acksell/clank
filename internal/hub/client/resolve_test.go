package hubclient

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acksell/clank/internal/host"
)

// initTestRepo creates a git repo with a configured `origin` remote and
// returns the repo path. We mirror internal/git's helper rather than export
// it to keep that package's surface small.
func initTestRepoWithRemote(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")
	mustRun(t, dir, "git", "remote", "add", "origin", remoteURL)
	// Need an initial commit so HEAD/branch resolve.
	if err := writeFile(filepath.Join(dir, "README.md"), "# test\n"); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "initial")
	return dir
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestResolveRepo(t *testing.T) {
	t.Parallel()
	const remote = "git@github.com:acksell/clank.git"
	dir := initTestRepoWithRemote(t, remote)

	hostID, ref, branch, err := ResolveRepo(dir)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	if hostID != host.HostLocal {
		t.Errorf("hostID = %q, want %q", hostID, host.HostLocal)
	}
	if ref.RemoteURL != remote {
		t.Errorf("RemoteURL = %q, want %q", ref.RemoteURL, remote)
	}
	id, err := ref.ID()
	if err != nil {
		t.Fatalf("ref.ID: %v", err)
	}
	if string(id) != "github.com/acksell/clank" {
		t.Errorf("ref.ID = %q, want github.com/acksell/clank", id)
	}
	if branch == "" {
		t.Error("branch is empty")
	}
}

func TestResolveRepoNoRemote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")
	if err := writeFile(filepath.Join(dir, "README.md"), "# test\n"); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "initial")

	if _, _, _, err := ResolveRepo(dir); err == nil {
		t.Fatal("ResolveRepo without origin: expected error, got nil")
	}
}
