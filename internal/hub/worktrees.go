package hub

// Hub-side helpers for worktree state. Identity is `(GitRef, branch)`
// post-§7 — repos are no longer keyed by canonical string.

import (
	"github.com/acksell/clank/internal/agent"
)

// markBranchSessionsDone flips every non-archived session attached to
// (ref, branch) to "done". Match is by (Local.Path|Remote.URL,
// WorktreeBranch). Returns the count of sessions updated.
//
// Used by MergeBranchOnHost after a successful merge so that the
// inbox surfaces the work as completed without the user having to
// archive each session by hand.
func (s *Service) markBranchSessionsDone(ref agent.GitRef, branch string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, ms := range s.sessions {
		if ms.info.GitRef.WorktreeBranch != branch {
			continue
		}
		if !sameRepo(ms.info.GitRef, ref) {
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

// sameRepo returns true when a and b name the same repo (Local.Path or
// Remote.URL match). Branch is ignored — the caller checks separately.
func sameRepo(a, b agent.GitRef) bool {
	switch {
	case a.Local != nil && b.Local != nil:
		return a.Local.Path == b.Local.Path
	case a.Remote != nil && b.Remote != nil:
		return a.Remote.URL == b.Remote.URL
	default:
		return false
	}
}
