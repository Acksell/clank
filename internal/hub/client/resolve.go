package hubclient

import (
	"fmt"

	"github.com/acksell/clank/internal/git"
	"github.com/acksell/clank/internal/host"
)

// ResolveRepo inspects a working directory and returns the canonical repo
// identity (RepoRef), the host that owns it, the absolute repo root on
// disk, and the currently checked-out branch. It is the client-side
// bridge from "the directory the user is in" to the path-free identity
// the hub expects in StartRequest (Phase 3 of hub_host_refactor.md).
//
// The repo root is resolved via `git rev-parse --show-toplevel`; the canonical
// remote URL comes from `git config --get remote.origin.url`. Both must
// succeed -- a directory without a remote can't be addressed host-scopedly,
// so we fail loudly rather than silently degrading.
//
// HostID is currently always "local"; remote hosts will be plumbed through
// when they exist. The returned root is what callers feed into
// hubclient.RegisterRepoOnHost so the host can map RepoID → workDir
// without paths on the StartRequest wire.
func ResolveRepo(cwd string) (hostID host.HostID, ref host.RepoRef, root, branch string, err error) {
	root, err = git.RepoRoot(cwd)
	if err != nil {
		return "", host.RepoRef{}, "", "", fmt.Errorf("resolve repo root for %s: %w", cwd, err)
	}
	remoteURL, err := git.RemoteURL(root, "origin")
	if err != nil {
		return "", host.RepoRef{}, "", "", fmt.Errorf("resolve remote url for %s: %w", root, err)
	}
	branch, err = git.CurrentBranch(root)
	if err != nil {
		return "", host.RepoRef{}, "", "", fmt.Errorf("resolve current branch for %s: %w", root, err)
	}
	ref = host.RepoRef{RemoteURL: remoteURL}
	if err := ref.Validate(); err != nil {
		return "", host.RepoRef{}, "", "", fmt.Errorf("invalid repo ref for %s: %w", root, err)
	}
	return host.HostLocal, ref, root, branch, nil
}
