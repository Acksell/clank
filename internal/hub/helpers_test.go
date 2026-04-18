package hub_test

import (
	"testing"

	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/hub"
)

// testRemoteURL is the canonical remote URL used by hub tests.
// It is paired with a per-test git repo registered through
// registerTestRepo / testDaemon so that the host plane can resolve
// (GitRef, WorktreeBranch) → workDir without callers passing paths.
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
	// path can recover the GitRef — needed for lazy backend activation
	// of historical sessions.
	gitRun(t, dir, "remote", "add", "origin", testRemoteURL)
	registerTestRepoAt(t, s, dir)
	return dir
}

// registerTestRepoAt registers an existing git repo dir on the hub's
// local host. Useful for two-phase persistence tests that need to
// register the same repo on the post-restart daemon.
//
// Bypasses the (now-deleted) hub→host wire path and calls
// host.Service.AddRepo directly via the package-level fixture map. The
// production call site (host.Service.CreateSession §7.5 implicit add)
// is exercised by the dedicated integration tests in
// internal/host/repos_test.go.
func registerTestRepoAt(t *testing.T, s *hub.Service, dir string) {
	t.Helper()
	v, ok := hostFixturesByHub.Load(s)
	if !ok {
		t.Fatal("registerTestRepoAt: no host fixture found for this hub.Service; ensure the test goes through testDaemon / ensureHostFixture")
	}
	f := v.(*hostTestFixture)
	if _, err := f.svc.AddRepo(host.GitRef{Kind: host.GitRefRemote, URL: testRemoteURL}, dir); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
}
