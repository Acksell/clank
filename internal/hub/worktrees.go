package hub

// Hub-side helpers for worktree state. The legacy `/branches`,
// `/worktrees`, and `/worktrees/merge` HTTP handlers were removed in
// Phase 3D-1. The remaining helper marks merged-branch sessions done
// after a successful host-side merge; it matches on (RepoID, Branch)
// so it does not depend on path identity.

import (
	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// markBranchSessionsDone flips every non-archived session attached to
// (gitRef, branch) to "done". Returns the count of sessions updated.
//
// gitRef is the canonical GitRef string (URL key form). The session's
// RepoRemoteURL is canonicalized the same way for comparison so the
// helper does not depend on URL spelling differences.
//
// Used by handleMergeBranchOnRepo after a successful merge so that the
// inbox surfaces the work as completed without the user having to
// archive each session by hand.
func (s *Service) markBranchSessionsDone(gitRef, branch string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, ms := range s.sessions {
		if ms.info.WorktreeBranch != branch {
			continue
		}
		// Guard against accidentally completing same-named branches across
		// repos by re-canonicalizing the session's RemoteURL.
		sessionRef := host.GitRef{Kind: host.GitRefRemote, URL: ms.info.RepoRemoteURL}.Canonical()
		if sessionRef == "" || sessionRef != gitRef {
			continue
		}
		if ms.info.Visibility == agent.VisibilityArchived || ms.info.Visibility == agent.VisibilityDone {
			continue
		}
		ms.info.Visibility = agent.VisibilityDone
		s.persistSession(ms)
		count++
	}
	return count
}
