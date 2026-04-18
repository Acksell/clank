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
	"regexp"
	"strings"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// HostID identifies a Host within the Hub's catalog. It is a short,
// human-readable slug ("local", "daytona-abc123") chosen by whoever
// registers the Host. The Hub treats it as an opaque key.
type HostID string

const (
	// HostLocal is the canonical ID for the laptop's supervised clank-host
	// child. The TUI defaults to this until the host-selection UX lands.
	HostLocal HostID = "local"
)

// RepoID is a URL-safe slug derived deterministically from a RepoRef's
// RemoteURL — for example "github.com/acksell/clank". Stable across host
// reboots and used as a path component in Hub HTTP routes
// (`/hosts/{hostID}/repos/{repoID}/...`).
type RepoID string

// RepoRef is the canonical handle for a repository on a Host. It carries
// the remote URL and is the only repo identity that crosses the wire — the
// Host translates it to a local filesystem path internally.
//
// In Phase 0 this is purely a value type; it gets wired into StartRequest
// and the Hub API in Phase 3.
type RepoRef struct {
	// RemoteURL is the repository's canonical remote URL. Any standard git
	// URL form is accepted (https, ssh, scp-like git@host:owner/repo).
	RemoteURL string `json:"remote_url"`
}

// Validate ensures the RepoRef carries enough info to compute an ID.
func (r RepoRef) Validate() error {
	if strings.TrimSpace(r.RemoteURL) == "" {
		return fmt.Errorf("remote_url is required")
	}
	if _, err := r.ID(); err != nil {
		return fmt.Errorf("remote_url is not a recognized git URL: %w", err)
	}
	return nil
}

// ID derives the deterministic RepoID slug from the RemoteURL.
//
// Examples:
//
//	git@github.com:acksell/clank.git         -> github.com/acksell/clank
//	https://github.com/acksell/clank.git     -> github.com/acksell/clank
//	https://github.com/acksell/clank         -> github.com/acksell/clank
//	ssh://git@github.com/acksell/clank.git   -> github.com/acksell/clank
//	git://gitlab.example.com/team/proj.git   -> gitlab.example.com/team/proj
//
// The slug is lowercased so capitalization differences in user-supplied
// URLs do not produce divergent IDs for the same repo.
func (r RepoRef) ID() (RepoID, error) {
	host, path, err := parseGitURL(r.RemoteURL)
	if err != nil {
		return "", err
	}
	slug := strings.ToLower(host + "/" + path)
	if !repoIDRe.MatchString(slug) {
		return "", fmt.Errorf("derived slug %q has unsafe characters", slug)
	}
	return RepoID(slug), nil
}

// repoIDRe enforces the URL-safe character set for RepoID. We allow
// lowercase letters, digits, dots, dashes, underscores, and forward slashes
// as path separators.
var repoIDRe = regexp.MustCompile(`^[a-z0-9._\-]+(/[a-z0-9._\-]+)+$`)

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
	ID      RepoID  `json:"id"`
	Ref     RepoRef `json:"ref"`
	RootDir string  `json:"root_dir"` // Local filesystem path on the Host (host-internal info, exposed for diagnostics)
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
	HostID    HostID    `json:"host_id"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	Sessions  int       `json:"sessions"` // Number of live (backend-attached) sessions
}
