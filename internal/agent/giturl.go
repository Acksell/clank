package agent

// Git URL parsing + clone-dir naming. Lives in the agent package because
// GitRef construction and host.Service.workDirFor both need it; pulling
// it into a separate file keeps gitref.go focused on the type itself.

import (
	"fmt"
	"net/url"
	"strings"
)

// parseGitURL extracts the host and repo path (without the trailing
// ".git") from any standard git URL form: https://, ssh://, scp-like
// (git@host:owner/repo), or file://.
//
// Returns ("file", <cleaned-path>, nil) for file:// URLs since they have
// no host; the path itself is the identity. The "file" prefix can't
// collide with any real remote because real hosts canonicalize as
// "<host>/<repo>" with no scheme prefix.
func parseGitURL(raw string) (host, path string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty URL")
	}

	// scp-like form: git@host:owner/repo(.git)
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

// CloneDirName returns a single filesystem-safe directory name for the
// remote URL. Used by host.Service to pick a stable subdir under
// ClonesDir for each remote.
//
// Format: lowercased "<host>-<path-with-slashes-as-dashes>", restricted
// to [a-z0-9._-]. Dots, leading dots and ".."/"." are rejected to keep
// the result safe to join into a path without escaping ClonesDir.
//
// Examples:
//
//	git@github.com:acksell/clank.git    → github.com-acksell-clank
//	https://github.com/acksell/clank    → github.com-acksell-clank
//	file:///srv/git/foo.git             → file--srv-git-foo
func CloneDirName(remoteURL string) (string, error) {
	host, path, err := parseGitURL(remoteURL)
	if err != nil {
		return "", err
	}
	raw := strings.ToLower(host + "-" + strings.ReplaceAll(path, "/", "-"))

	// Allowlist sanitization: anything outside [a-z0-9._-] becomes "-".
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("clone dir name for %q resolves to %q", remoteURL, name)
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("clone dir name for %q starts with dot: %q", remoteURL, name)
	}
	return name, nil
}
