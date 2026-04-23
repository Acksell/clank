package hub

// Git endpoint parsing. Lives here (not in internal/agent) so the
// agent package — which is imported by every component — stays free
// of go-git/v5 and its dependency tree. The hub is the policy owner
// for git access; consolidating the parser here also concentrates
// the surface that any future "use go-git natively" port has to touch.

import (
	"fmt"
	"strings"

	gitTransport "github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/acksell/clank/internal/agent"
)

// ParseGitEndpoint turns any standard git URL form (https://, http://,
// ssh://, scp-style git@host:owner/repo.git, file://, git://) into a
// canonical, protocol-independent agent.GitEndpoint.
//
// Normalisation rules (must round-trip stably so RepoKey stays consistent):
//   - Path has no leading "/" and no trailing ".git".
//   - Host is lower-cased so https://GitHub.com and https://github.com
//     produce equal endpoints.
//   - Default ports are dropped (Port=0) so explicit and implicit
//     defaults collapse.
//   - SSH "git@host:owner/repo" parses identically to "ssh://git@host/owner/repo".
//
// Returns a typed error for unrecognised protocols so callers can
// surface a clear "not a git URL" message instead of a parser internal.
func ParseGitEndpoint(raw string) (*agent.GitEndpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty git URL")
	}
	ep, err := gitTransport.NewEndpoint(raw)
	if err != nil {
		return nil, fmt.Errorf("parse git URL %q: %w", raw, err)
	}

	proto, err := normaliseProtocol(ep.Protocol)
	if err != nil {
		return nil, fmt.Errorf("git URL %q: %w", raw, err)
	}

	host := strings.ToLower(ep.Host)
	port := ep.Port
	if port == defaultPortFor(proto) {
		port = 0
	}

	path := normalisePath(ep.Path, proto)
	if path == "" {
		return nil, fmt.Errorf("git URL %q has no repo path", raw)
	}

	out := &agent.GitEndpoint{
		Protocol: proto,
		User:     ep.User,
		Host:     host,
		Port:     port,
		Path:     path,
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("parsed endpoint invalid for %q: %w", raw, err)
	}
	return out, nil
}

// normaliseProtocol maps go-git's protocol strings into our typed enum.
// "git+ssh", "ssh+git" etc. are not in our model — surface them
// explicitly rather than silently coercing.
func normaliseProtocol(p string) (agent.GitEndpointProtocol, error) {
	switch p {
	case "https":
		return agent.GitProtoHTTPS, nil
	case "http":
		return agent.GitProtoHTTP, nil
	case "ssh":
		return agent.GitProtoSSH, nil
	case "git":
		return agent.GitProtoGit, nil
	case "file":
		return agent.GitProtoFile, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", p)
	}
}

// defaultPortFor returns the wire default for a protocol so we can
// drop a redundant explicit port. Returning 0 means "no default
// applies" (e.g. file://).
func defaultPortFor(p agent.GitEndpointProtocol) int {
	switch p {
	case agent.GitProtoHTTPS:
		return 443
	case agent.GitProtoHTTP:
		return 80
	case agent.GitProtoSSH:
		return 22
	case agent.GitProtoGit:
		return 9418
	default:
		return 0
	}
}

// normalisePath strips leading slashes and the trailing ".git" so two
// URLs that differ only in those decorations produce equal endpoints.
// For file:// the leading slash is significant to absolute-path
// resolution but we keep the no-slash invariant on GitEndpoint.Path
// and reattach during String() rendering.
func normalisePath(p string, _ agent.GitEndpointProtocol) string {
	p = strings.TrimLeft(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimRight(p, "/")
	return p
}
