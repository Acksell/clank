package agent

// GitRef and friends live in the agent package because they are wire-level
// identity types used inside StartRequest. The host package re-exports them
// as type aliases (host.GitRef, host.GitRefRemote, ...) so the existing
// host-internal call sites keep working without an agent → host import
// cycle.

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
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

// Validate enforces the kind-specific field rules from §7.2 of
// hub_host_refactor_code_review.md.
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
	// file://<absolute-path> URLs are git's documented form for cloning
	// from a local source (mirrors, backups, air-gapped setups). They
	// have no host; the path itself is the identity. Canonicalize as
	// "file/<cleaned-path>" — the "file" scheme prefix can't collide
	// with any real remote (real hosts canonicalize as "<host>/<repo>"
	// with no scheme prefix).
	if u.Scheme == "file" {
		if u.Path == "" {
			return "", "", fmt.Errorf("file URL %q has no path", raw)
		}
		path = strings.TrimSuffix(u.Path, ".git")
		path = strings.TrimRight(path, "/")
		if path == "" {
			return "", "", fmt.Errorf("file URL %q has empty path after trim", raw)
		}
		return "file", path, nil
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
