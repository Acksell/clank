// Package host defines the value types for Clank's Host plane and (in
// later phases) the host.Service that runs agent backends, owns the repo
// cache, and exposes an HTTP API to the Hub.
//
// Phase 0 only introduces the value types so the rest of the codebase can
// start referring to host+repo+branch identity ahead of the daemon split.
package host

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// Hostname identifies a Host within the Hub's catalog. It is a short,
// human-readable slug ("local", "daytona-abc123") chosen by whoever
// registers the Host. The Hub treats it as an opaque key.
type Hostname string

const (
	// HostLocal is the canonical ID for the laptop's supervised clank-host
	// child. The TUI defaults to this until the host-selection UX lands.
	HostLocal Hostname = "local"
)

// GitRefKind tags whether a GitRef points at a remote URL (cloneable) or
// a pre-existing local checkout on the Host.
type GitRefKind string

const (
	GitRefRemote GitRefKind = "remote"
	GitRefLocal  GitRefKind = "local"
)

// GitRef is the canonical reference to a git repository. It supersedes
// RepoRef/RepoID by carrying enough information to either clone a remote
// or use a local checkout, plus an optional commit pin.
//
// Canonical() yields a stable string used both as the storage primary key
// (repos.git_ref) and as the URL-encoded path component on Hub/Host APIs.
type GitRef struct {
	Kind      GitRefKind `json:"kind"`
	URL       string     `json:"url,omitempty"`        // required when Kind=remote; rejected otherwise
	Path      string     `json:"path,omitempty"`       // required when Kind=local; must be absolute
	CommitSHA string     `json:"commit_sha,omitempty"` // optional pinning hint, advisory only
}

// Validate enforces the kind-specific field rules from §7.2.
func (g GitRef) Validate() error {
	switch g.Kind {
	case "":
		return fmt.Errorf("kind is required")
	case GitRefRemote:
		if strings.TrimSpace(g.URL) == "" {
			return fmt.Errorf("kind=remote requires url")
		}
		if g.Path != "" {
			return fmt.Errorf("kind=remote must not set path")
		}
		if _, _, err := parseGitURL(g.URL); err != nil {
			return fmt.Errorf("url is not a recognized git URL: %w", err)
		}
		return nil
	case GitRefLocal:
		if strings.TrimSpace(g.Path) == "" {
			return fmt.Errorf("kind=local requires path")
		}
		if g.URL != "" {
			return fmt.Errorf("kind=local must not set url")
		}
		if !filepath.IsAbs(g.Path) {
			return fmt.Errorf("kind=local path must be absolute, got %q", g.Path)
		}
		return nil
	default:
		return fmt.Errorf("unknown kind %q", g.Kind)
	}
}

// Canonical returns the stable string identity of the ref.
//
// remote: "<host>/<path>" lowercased, scheme-normalized, ".git"-stripped
// local : the absolute path as-is (Unix; Windows handling TBD)
//
// Returns "" if the ref is invalid; callers that need to surface errors
// should call Validate first.
func (g GitRef) Canonical() string {
	switch g.Kind {
	case GitRefRemote:
		host, path, err := parseGitURL(g.URL)
		if err != nil {
			return ""
		}
		return strings.ToLower(host + "/" + path)
	case GitRefLocal:
		if !filepath.IsAbs(g.Path) {
			return ""
		}
		return g.Path
	default:
		return ""
	}
}

// Equal reports whether two refs identify the same repository per their
// canonical form. CommitSHA is intentionally ignored — it pins a revision,
// not a repo identity.
func (g GitRef) Equal(other GitRef) bool {
	a, b := g.Canonical(), other.Canonical()
	if a == "" || b == "" {
		return false
	}
	return a == b
}

// parseGitURL extracts the host and repo path (without the trailing ".git")
// from any standard git URL form.
func parseGitURL(raw string) (host, path string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty URL")
	}

	// scp-like form: git@host:owner/repo(.git)
	// Detected by the presence of "@" before any "/" and a ":" before the path.
	if !strings.Contains(raw, "://") {
		if at := strings.Index(raw, "@"); at >= 0 {
			rest := raw[at+1:]
			if colon := strings.Index(rest, ":"); colon >= 0 && !strings.Contains(rest[:colon], "/") {
				host = rest[:colon]
				path = strings.TrimSuffix(rest[colon+1:], ".git")
				path = strings.Trim(path, "/")
				if host == "" || path == "" {
					return "", "", fmt.Errorf("scp-like URL missing host or path: %q", raw)
				}
				return host, path, nil
			}
		}
		// Fall through: try to parse as URL by prepending a scheme.
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse URL: %w", err)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("URL %q has no host", raw)
	}
	host = u.Host
	path = strings.TrimSuffix(u.Path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", fmt.Errorf("URL %q has no repo path", raw)
	}
	return host, path, nil
}

// Repo describes a repository known to a Host.
type Repo struct {
	Ref     GitRef `json:"ref"`
	RootDir string `json:"root_dir"` // Local filesystem path on the Host (host-internal info, exposed for diagnostics)
}

// BackendInfo describes one backend installed on a Host (e.g. "opencode",
// "claude-code"). Catalog endpoints return slices of these.
type BackendInfo struct {
	Name        agent.BackendType `json:"name"`
	DisplayName string            `json:"display_name"`
	Available   bool              `json:"available"`         // false when the backend's binary/server is missing
	Reason      string            `json:"reason,omitempty"`  // Why it is unavailable (when Available is false)
	Version     string            `json:"version,omitempty"` // Reported by the backend, when available
}

// AgentInfo is re-exported from the agent package so callers in the host
// plane import a single source. The underlying type is unchanged in Phase 0
// to keep the diff small; Phase 3+ may move the canonical definition here.
type AgentInfo = agent.AgentInfo

// ModelInfo is re-exported from the agent package for the same reason as
// AgentInfo.
type ModelInfo = agent.ModelInfo

// BranchInfo describes a branch on a Host's repo.
type BranchInfo struct {
	Name         string `json:"name"`
	WorktreeDir  string `json:"worktree_dir,omitempty"` // Filesystem path on the Host if a worktree is checked out
	IsDefault    bool   `json:"is_default,omitempty"`
	IsCurrent    bool   `json:"is_current,omitempty"`
	LinesAdded   int    `json:"lines_added,omitempty"`
	LinesRemoved int    `json:"lines_removed,omitempty"`
	CommitsAhead int    `json:"commits_ahead,omitempty"`
}

// WorktreeInfo describes a single worktree managed by the Host.
type WorktreeInfo struct {
	Branch      string `json:"branch"`
	WorktreeDir string `json:"worktree_dir"`
}

// HostStatus is the response body of `GET /status` on the Host API. Hub
// surfaces a derived view (online/offline + last seen) to clients.
type HostStatus struct {
	Hostname  Hostname  `json:"hostname"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Sessions  int       `json:"sessions"` // Number of live (backend-attached) sessions
}
