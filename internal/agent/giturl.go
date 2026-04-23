package agent

// Clone-dir naming. CloneDirName is consumed by host.Service.workDirFor
// to pick a stable subdir under ClonesDir for each remote.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// CloneDirName returns a single filesystem-safe directory name for the
// endpoint. Used by host.Service to pick a stable subdir under
// ClonesDir for each remote.
//
// Format: lowercased "<host>-<path-with-slashes-as-dashes>" with all
// chars outside [a-z0-9._-] replaced by "-", then a short hash of the
// canonical (host, path) pair appended. The hash makes the result
// collision-resistant: two distinct remotes that share the same lossy
// sanitized name (e.g. case differences, escaped chars) get distinct
// clone dirs and cannot accidentally reuse one checkout.
//
// Examples:
//
//	git@github.com:acksell/clank.git    → github.com-acksell-clank-<hash>
//	https://github.com/acksell/clank    → github.com-acksell-clank-<hash>
//	file:///srv/git/foo.git             → file--srv-git-foo-<hash>
func CloneDirName(ep *GitEndpoint) (string, error) {
	if ep == nil {
		return "", fmt.Errorf("clone dir name: nil endpoint")
	}
	if err := ep.Validate(); err != nil {
		return "", fmt.Errorf("clone dir name: %w", err)
	}
	host := ep.Host
	if ep.Protocol == GitProtoFile && host == "" {
		// file:///abs/path — synthesise a literal so the result is
		// stable and distinguishable from network protocols.
		host = "file"
	}
	raw := strings.ToLower(host + "-" + strings.ReplaceAll(ep.Path, "/", "-"))

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
		return "", fmt.Errorf("clone dir name for %q resolves to %q", ep.String(), name)
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("clone dir name for %q starts with dot: %q", ep.String(), name)
	}
	// Append a short hash of the canonical identity so distinct remotes
	// that sanitize to the same name don't share a checkout.
	sum := sha256.Sum256([]byte(host + "\x00" + ep.Path))
	return name + "-" + hex.EncodeToString(sum[:6]), nil
}
