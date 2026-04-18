package hubclient

import (
	"fmt"

	"github.com/acksell/clank/internal/git"
	"github.com/acksell/clank/internal/host"
)

// ResolveRepo inspects a working directory and returns the canonical repo
// identity (GitRef), the host that owns it, the absolute repo root on
// disk, and the currently checked-out branch. It is the client-side bridge
// from "the directory the user is in" to the path-free identity the hub
// expects in StartRequest.
//
// The repo root is resolved via `git rev-parse --show-toplevel`; the
// canonical remote URL comes from `git config --get remote.origin.url`.
// Both must succeed — a directory without a remote can't be addressed
// host-scopedly today, so we fail loudly rather than silently degrading.
// (Local-kind GitRef adoption arrives with §7.5 in step 6.)
//
// Hostname is currently always "local"; remote hosts will be plumbed
// through when they exist. The returned root is what callers feed into
// hubclient.RegisterRepoOnHost so the host can map (canonical → workDir)
// without paths on the StartRequest wire.
func ResolveRepo(cwd string) (hostname host.Hostname, ref host.GitRef, root, branch string, err error) {
	root, err = git.RepoRoot(cwd)
	if err != nil {
		return "", host.GitRef{}, "", "", fmt.Errorf("resolve repo root for %s: %w", cwd, err)
	}
	remoteURL, err := git.RemoteURL(root, "origin")
	if err != nil {
		return "", host.GitRef{}, "", "", fmt.Errorf("resolve remote url for %s: %w", root, err)
	}
	branch, err = git.CurrentBranch(root)
	if err != nil {
		return "", host.GitRef{}, "", "", fmt.Errorf("resolve current branch for %s: %w", root, err)
	}
	ref = host.GitRef{Kind: host.GitRefRemote, URL: remoteURL}
	if err := ref.Validate(); err != nil {
		return "", host.GitRef{}, "", "", fmt.Errorf("invalid git ref for %s: %w", root, err)
	}
	return host.HostLocal, ref, root, branch, nil
}
