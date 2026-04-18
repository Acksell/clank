package hub_test

import (
	"context"
	"testing"

	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/hub"
)

// testRemoteURL is the canonical remote URL used by hub tests.
// It is paired with a per-test git repo registered through
// registerTestRepo / testDaemon so that the host plane can resolve
// (RepoRemoteURL, Branch) → workDir without callers passing paths.
const testRemoteURL = "git@github.com:acksell/clank.git"

// registerTestRepo creates a real git repo (via initGitRepo from
// service_test.go), wires up an `origin` remote pointing at
// testRemoteURL, and registers it on the hub's local host plane.
// Returns the repo root directory so callers can assert on
// info.ProjectDir or build worktree paths if needed.
//
// The hub must already be running (i.e. startHubOnSocket has returned)
// so that the "local" host is in the catalog.
func registerTestRepo(t *testing.T, s *hub.Service) string {
	t.Helper()
	dir := initGitRepo(t)
	// Set origin remote so git.RemoteURL(dir, "origin") in the discover
	// path can recover RepoRemoteURL — needed for lazy backend
	// activation of historical sessions.
	gitRun(t, dir, "remote", "add", "origin", testRemoteURL)
	registerTestRepoAt(t, s, dir)
	return dir
}

// registerTestRepoAt registers an existing git repo dir on the hub's
// local host. Useful for two-phase persistence tests that need to
// register the same repo on the post-restart daemon.
func registerTestRepoAt(t *testing.T, s *hub.Service, dir string) {
	t.Helper()
	c, ok := s.Host(host.HostLocal)
	if !ok {
		t.Fatal("local host not registered on hub.Service")
	}
	if _, err := c.RegisterRepo(context.Background(), host.GitRef{Kind: host.GitRefRemote, URL: testRemoteURL}, dir); err != nil {
		t.Fatalf("RegisterRepo: %v", err)
	}
}
