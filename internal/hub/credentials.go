package hub

// Hub-side credential resolution. The hub is the policy owner: given a
// target host and a parsed GitEndpoint, it picks the credential the
// host should use AND, when necessary, rewrites the endpoint to a form
// the credential can actually drive (e.g. ssh→https for a remote host
// with no SSH agent).
//
// See docs/git_credentials_refactor.md for the full policy table.

import (
	"fmt"

	"github.com/acksell/clank/internal/agent"
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

// ResolveCredential picks the credential for sending (target, ep) to a
// host. May return a rewritten endpoint (currently only ssh→https for
// remote-host forwards on allowlisted public providers).
//
// The output endpoint is always non-nil on success and is what callers
// should send to the host. The credential's Kind tells the host how to
// drive it.
//
// This function never reads the filesystem, environment, or network.
// All policy is derived from (target hostname, parsed endpoint). Token
// discovery is a separate, deferred step.
func ResolveCredential(target host.Hostname, ep *agent.GitEndpoint) (agent.GitCredential, *agent.GitEndpoint, error) {
	if ep == nil {
		return agent.GitCredential{}, nil, fmt.Errorf("resolve credential: nil endpoint")
	}
	if err := ep.Validate(); err != nil {
		return agent.GitCredential{}, nil, fmt.Errorf("resolve credential: invalid endpoint: %w", err)
	}
	isLocalTarget := target == "" || target == host.HostLocal

	switch ep.Protocol {
	case agent.GitProtoHTTPS, agent.GitProtoHTTP, agent.GitProtoGit:
		// Public/anonymous-friendly transports: no rewrite, no secret.
		// Token-discovery PR will add an HTTPS-with-token branch here.
		return agent.GitCredential{Kind: agent.GitCredAnonymous}, ep, nil

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
		// sandbox doesn't hang on an SSH host-key prompt.
		rewritten := &agent.GitEndpoint{
			Protocol: agent.GitProtoHTTPS,
			Host:     ep.Host,
			Path:     ep.Path,
		}
		if err := rewritten.Validate(); err != nil {
			return agent.GitCredential{}, nil, fmt.Errorf("rewritten endpoint invalid: %w", err)
		}
		return agent.GitCredential{Kind: agent.GitCredAnonymous}, rewritten, nil

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
