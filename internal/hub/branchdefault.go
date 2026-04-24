package hub

// branchdefault.go implements the remote-host default-branch policy: a
// session targeting a remote host with an unset WorktreeBranch gets
// "clank/<sessionID>" filled in before the ref reaches the host. This
// keeps work on remote sandboxes off the repo's default branch so
// push/PR flows have a branch to target and shutdown-delete of a
// Daytona sandbox cannot clobber default.
//
// Local hosts are deliberately excluded: the laptop is the user's
// interactive environment and may legitimately operate on the default
// branch (e.g. a casual poke at main). Only remote hosts — where every
// sandbox is ephemeral and clones are throwaway — get the auto-branch.
//
// Caller contract: apply once at the hub seam, before hostForRef. An
// explicit WorktreeBranch from the caller (CLI flag, TUI selection) is
// ALWAYS honored verbatim; this helper only fills the empty case.

import (
	"fmt"

	"github.com/acksell/clank/internal/host"
)

// branchPrefix is the namespace for auto-generated session branches.
// Matches the convention used by other agentic tools (claude/, cursor/).
const branchPrefix = "clank/"

// defaultWorktreeBranch returns the branch name to use when a caller
// did not set one. Remote hosts get a session-derived branch to keep
// work off default; local hosts and already-set branches are returned
// unchanged. sessionID must be non-empty for the remote branch path.
func defaultWorktreeBranch(hostname host.Hostname, sessionID, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if hostname == "" || hostname == host.HostLocal {
		return "", nil
	}
	if sessionID == "" {
		// A remote session without an ID is a programming error
		// upstream (CreateSession assigns the ID before this seam).
		// Surface it loudly instead of silently returning empty and
		// hoping the host-side branch-reserved check catches us.
		return "", fmt.Errorf("defaultWorktreeBranch: remote host %q requires a non-empty sessionID", hostname)
	}
	return branchPrefix + sessionID, nil
}
