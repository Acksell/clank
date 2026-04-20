package agent

// GitRef and friends live in the agent package because they are wire-level
// identity types used inside StartRequest. The host package consumes them
// directly; there is no host-side alias.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GitRef is the canonical reference to a git repository plus an optional
// worktree branch. Exactly one of Local or Remote MUST be set; Validate
// enforces this. The pointer-vs-pointer shape encodes the
// mutually-exclusive choice in the type itself so callers can switch on
// which field is non-nil rather than dispatching on a string Kind.
type GitRef struct {
	Local          *LocalRef  `json:"local,omitempty"`
	Remote         *RemoteRef `json:"remote,omitempty"`
	WorktreeBranch string     `json:"worktree_branch,omitempty"`
}

// LocalRef points at an existing checkout on the host's filesystem.
// Path must be absolute and must be a git RepoRoot.
type LocalRef struct {
	Path string `json:"path"`
}

// RemoteRef points at a remote git URL the host may clone on demand.
// URL accepts any standard git URL form (https, ssh, scp-like, file).
type RemoteRef struct {
	URL string `json:"url"`
}

// Validate enforces the field rules from §7.2 of
// hub_host_refactor_code_review.md: exactly one of Local/Remote, plus
// per-variant well-formedness.
func (g GitRef) Validate() error {
	switch {
	case g.Local != nil && g.Remote != nil:
		return fmt.Errorf("git ref must set exactly one of local or remote, not both")
	case g.Local != nil:
		if strings.TrimSpace(g.Local.Path) == "" {
			return fmt.Errorf("local ref requires path")
		}
		if !filepath.IsAbs(g.Local.Path) {
			return fmt.Errorf("local ref path must be absolute, got %q", g.Local.Path)
		}
		return nil
	case g.Remote != nil:
		if strings.TrimSpace(g.Remote.URL) == "" {
			return fmt.Errorf("remote ref requires url")
		}
		if _, _, err := parseGitURL(g.Remote.URL); err != nil {
			return fmt.Errorf("remote ref url is not a recognized git URL: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("git ref must set exactly one of local or remote")
	}
}

// RepoKey returns a stable map key for a GitRef. Used by in-memory
// dedup tables (e.g. the primary-agents background-refresh set) where
// the identity is (Local|Remote, branch). Empty for invalid refs.
func RepoKey(g GitRef) string {
	switch {
	case g.Local != nil:
		return "L\x00" + g.Local.Path + "\x00" + g.WorktreeBranch
	case g.Remote != nil:
		return "R\x00" + g.Remote.URL + "\x00" + g.WorktreeBranch
	default:
		return ""
	}
}

// RepoDisplayName returns a short human-readable label for UIs and logs.
//
// remote: the last path segment of the parsed URL (e.g. "clank" for
//
//	"https://github.com/acksell/clank").
//
// local : filepath.Base of the absolute path.
//
// Returns "" for invalid refs; callers that need errors should call
// Validate first.
func RepoDisplayName(g GitRef) string {
	switch {
	case g.Local != nil:
		if !filepath.IsAbs(g.Local.Path) {
			return ""
		}
		return filepath.Base(g.Local.Path)
	case g.Remote != nil:
		_, path, err := parseGitURL(g.Remote.URL)
		if err != nil {
			return ""
		}
		if i := strings.LastIndex(path, "/"); i >= 0 {
			return path[i+1:]
		}
		return path
	default:
		return ""
	}
}
