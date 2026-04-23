package hub

// Hub-side credential resolution. The hub is the policy owner: given a
// target host and a parsed GitEndpoint, it picks the credential the
// host should use AND, when necessary, rewrites the endpoint to a form
// the credential can actually drive (e.g. ssh→https for a remote host
// with no SSH agent).
//
// See docs/git_credentials_refactor.md for the full policy table.

import (
	"context"
	"errors"
	"fmt"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitcred"
	"github.com/acksell/clank/internal/host"
)

// publicHTTPSAllowlist enumerates providers known to expose the same
// repo over anonymous HTTPS as over SSH, with the standard
// "/owner/repo.git" path layout. Self-hosted Git servers don't
// generally satisfy this and require explicit credentials.
//
// Lifted (unchanged) from the legacy internal/agent/giturl.go list so
// the v1 behaviour matches what shipped before this refactor.
var publicHTTPSAllowlist = map[string]bool{
	"github.com":    true,
	"gitlab.com":    true,
	"bitbucket.org": true,
}

// credDiscoverer is the subset of [CachingDiscoverer] (or any other
// discovery façade) the resolver needs. Local interface so callers
// can pass a bare [gitcred.Discoverer] adapter in tests without
// wrapping in a cache.
type credDiscoverer interface {
	DiscoverFor(ctx context.Context, target host.Hostname, ep *agent.GitEndpoint) (agent.GitCredential, error)
}

// ResolveCredential picks the credential for sending (target, ep) to a
// host. May return a rewritten endpoint (currently only ssh→https for
// remote-host forwards on allowlisted public providers).
//
// disc may be nil — when so, the resolver behaves as in the v1
// "policy only" world and returns anonymous for HTTPS endpoints. With
// a non-nil disc, the resolver consults it for HTTPS (including
// rewritten ssh→https) endpoints and uses the discovered credential
// when present, falling back to anonymous on [gitcred.ErrNoCredential].
//
// Any other discovery error is fatal — broken local config (e.g.
// unparseable settings file) must surface to the user instead of
// silently degrading.
//
// The output endpoint is always non-nil on success and is what callers
// should send to the host. The credential's Kind tells the host how to
// drive it.
func ResolveCredential(ctx context.Context, target host.Hostname, ep *agent.GitEndpoint, disc credDiscoverer) (agent.GitCredential, *agent.GitEndpoint, error) {
	if ep == nil {
		return agent.GitCredential{}, nil, fmt.Errorf("resolve credential: nil endpoint")
	}
	if err := ep.Validate(); err != nil {
		return agent.GitCredential{}, nil, fmt.Errorf("resolve credential: invalid endpoint: %w", err)
	}
	isLocalTarget := target == "" || target == host.HostLocal

	switch ep.Protocol {
	case agent.GitProtoHTTPS, agent.GitProtoHTTP, agent.GitProtoGit:
		// Public/anonymous-friendly transports. With a discoverer
		// configured we try to upgrade to a token (required for
		// push); without one (or on soft miss) we return anonymous
		// — clone of public repos still works.
		return discoverOrAnonymous(ctx, target, ep, disc)

	case agent.GitProtoSSH:
		if isLocalTarget {
			// ssh-agent is process-local; only valid when we're talking
			// to the local host. Endpoint stays as-is.
			return agent.GitCredential{Kind: agent.GitCredSSHAgent}, ep, nil
		}
		if !publicHTTPSAllowlist[ep.Host] {
			return agent.GitCredential{}, nil, fmt.Errorf(
				"remote host %q has no credentials for %q and provider %q is not on the public-HTTPS allowlist",
				target, ep.String(), ep.Host)
		}
		// Public provider: rewrite to anonymous HTTPS so the remote
		// sandbox doesn't hang on an SSH host-key prompt. Then try
		// discovery against the rewritten endpoint so a saved token
		// upgrades us from anonymous-clone to authenticated-push.
		rewritten := &agent.GitEndpoint{
			Protocol: agent.GitProtoHTTPS,
			Host:     ep.Host,
			Path:     ep.Path,
		}
		if err := rewritten.Validate(); err != nil {
			return agent.GitCredential{}, nil, fmt.Errorf("rewritten endpoint invalid: %w", err)
		}
		return discoverOrAnonymous(ctx, target, rewritten, disc)

	case agent.GitProtoFile:
		if !isLocalTarget {
			return agent.GitCredential{}, nil, fmt.Errorf(
				"file:// endpoints are not valid for remote host %q (endpoint=%q)",
				target, ep.String())
		}
		return agent.GitCredential{Kind: agent.GitCredAnonymous}, ep, nil

	default:
		return agent.GitCredential{}, nil, fmt.Errorf("resolve credential: unsupported protocol %q", ep.Protocol)
	}
}

// discoverOrAnonymous runs the discoverer against ep and returns the
// discovered credential on hit, or anonymous on miss. A hard
// discovery error is propagated.
func discoverOrAnonymous(ctx context.Context, target host.Hostname, ep *agent.GitEndpoint, disc credDiscoverer) (agent.GitCredential, *agent.GitEndpoint, error) {
	if disc == nil {
		return agent.GitCredential{Kind: agent.GitCredAnonymous}, ep, nil
	}
	cred, err := disc.DiscoverFor(ctx, target, ep)
	if err == nil {
		return cred, ep, nil
	}
	if errors.Is(err, gitcred.ErrNoCredential) {
		return agent.GitCredential{Kind: agent.GitCredAnonymous}, ep, nil
	}
	return agent.GitCredential{}, nil, fmt.Errorf("resolve credential: discovery failed: %w", err)
}
