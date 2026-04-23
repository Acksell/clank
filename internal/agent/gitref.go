package agent

// GitRef is the canonical wire-level identity for a git repository plus
// an optional worktree branch. Lives in the agent package because it is
// embedded in StartRequest; the host package consumes it directly.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GitRef tells a host where to find a project's files.
//
// Two optional locator fields:
//
//   - LocalPath: an absolute filesystem path on the *target host*. When
//     present and usable, the host opens this path directly — no clone.
//   - Endpoint: a parsed git remote identity. When LocalPath is empty
//     or not usable on the target host, the host clones the endpoint
//     into its clones dir and works from there.
//
// At least one MUST be set. Both is the common case for clients that
// are running co-located with the target host (laptop TUI in $repo): the
// host uses LocalPath and ignores Endpoint. A client targeting a
// *different* host (e.g. mobile) sends only Endpoint; the host clones.
//
// Resolution precedence on the host (see host.Service.workDirFor):
//  1. If LocalPath is set, exists, and is the repo root → use it.
//  2. Else if Endpoint is set → clone into <clonesDir>/<CloneDirName>.
//  3. Else → error.
//
// This is precedence, not a fallback in the AGENTS.md "no fallbacks"
// sense: it's the documented contract every caller relies on. Clients
// that have a LocalPath are expected to send it; mobile clients that
// don't are expected to send only Endpoint.
type GitRef struct {
	// Endpoint is the parsed, structured remote identity. The hub is
	// responsible for parsing raw URLs at ingress (TUI, clankcli,
	// voice, discovery) before forwarding to a host. The host package
	// has no URL parser of its own.
	Endpoint *GitEndpoint `json:"endpoint,omitempty"`

	LocalPath      string `json:"local_path,omitempty"`
	WorktreeBranch string `json:"worktree_branch,omitempty"`
}

// Validate enforces: at least one of LocalPath / Endpoint is set, and
// each set field is well-formed. Multiple set is allowed (and normal
// for laptop clients).
func (g GitRef) Validate() error {
	hasLocal := strings.TrimSpace(g.LocalPath) != ""
	hasEndpoint := g.Endpoint != nil
	if !hasLocal && !hasEndpoint {
		return fmt.Errorf("git ref must set at least one of local_path or endpoint")
	}
	if hasLocal && !filepath.IsAbs(g.LocalPath) {
		return fmt.Errorf("local_path must be absolute, got %q", g.LocalPath)
	}
	if hasEndpoint {
		if err := g.Endpoint.Validate(); err != nil {
			return fmt.Errorf("endpoint invalid: %w", err)
		}
	}
	return nil
}

// RepoKey returns a stable map key for a GitRef. Used by in-memory dedup
// tables (e.g. the primary-agents background-refresh set) where the
// identity is (project, branch).
//
// Prefers Endpoint when present: that key is protocol-independent so
// `ssh://github.com/foo` and `https://github.com/foo` collapse to one
// entry — important once the credential resolver may rewrite ssh→https
// for remote hosts. Falls back to LocalPath for repos with no configured
// origin. Returns "" for invalid refs.
func RepoKey(g GitRef) string {
	switch {
	case g.Endpoint != nil:
		return "E\x00" + g.Endpoint.Host + "\x00" + g.Endpoint.Path + "\x00" + g.WorktreeBranch
	case g.LocalPath != "":
		return "L\x00" + g.LocalPath + "\x00" + g.WorktreeBranch
	default:
		return ""
	}
}

// RepoDisplayName returns a short human-readable label for UIs and logs.
//
// Prefers Endpoint.Path (last segment) so the display matches across
// hosts and across protocols. Falls back to filepath.Base(LocalPath).
// Returns "" for invalid refs.
func RepoDisplayName(g GitRef) string {
	if g.Endpoint != nil && g.Endpoint.Path != "" {
		p := g.Endpoint.Path
		if i := strings.LastIndex(p, "/"); i >= 0 {
			return p[i+1:]
		}
		return p
	}
	if g.LocalPath != "" && filepath.IsAbs(g.LocalPath) {
		return filepath.Base(filepath.Clean(g.LocalPath))
	}
	return ""
}
