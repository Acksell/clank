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
// (repoID, branch) to "done". Returns the count of sessions updated.
//
// Used by handleMergeBranchOnRepo after a successful merge so that the
// inbox surfaces the work as completed without the user having to
// archive each session by hand.
func (s *Service) markBranchSessionsDone(repoID host.RepoID, branch string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, ms := range s.sessions {
		if ms.info.Branch != branch {
			continue
		}
		// Skip if the session's RepoRemoteURL doesn't reduce to the
		// same RepoID — guards against accidentally completing
		// same-named branches across repos.
		sessionRepoID, err := host.RepoRef{RemoteURL: ms.info.RepoRemoteURL}.ID()
		if err != nil || sessionRepoID != repoID {
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
