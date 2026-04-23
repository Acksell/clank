package gitcred

import (
	"context"
	"os"
	"strings"

	"github.com/acksell/clank/internal/agent"
)

// envProviderTokens maps a git endpoint host to the env-var names that
// conventionally hold a personal-access-token for that provider, in
// preference order. The first non-empty value wins.
//
// We intentionally do NOT consult provider-agnostic envs like
// `GIT_TOKEN` — they would leak a github PAT to a gitlab endpoint.
var envProviderTokens = map[string][]string{
	"github.com":    {"GH_TOKEN", "GITHUB_TOKEN"},
	"gitlab.com":    {"GITLAB_TOKEN", "GL_TOKEN"},
	"bitbucket.org": {"BITBUCKET_TOKEN"},
}

// clankEnvPrefix is the universal override. CLANK_GIT_TOKEN_<HOST>
// where <HOST> is the endpoint host uppercased with dots → underscores
// (e.g. github.com → GITHUB_COM, git.internal.corp → GIT_INTERNAL_CORP).
// Always checked first, so a user can force a specific token for any
// host — including ones not in [envProviderTokens].
const clankEnvPrefix = "CLANK_GIT_TOKEN_"

// gitTokenUsername is the conventional username for HTTPS-basic auth
// when the password is actually a token. github, gitlab, and bitbucket
// all accept "x-access-token" (or any non-empty username) as the basic
// pair, so we standardise on it across providers.
const gitTokenUsername = "x-access-token"

// EnvDiscoverer reads tokens from environment variables. Stateless;
// safe for concurrent use.
type EnvDiscoverer struct{}

// FromEnv returns an [EnvDiscoverer]. Pure for symmetry with other
// constructors; the type itself has no fields.
func FromEnv() EnvDiscoverer { return EnvDiscoverer{} }

// Discover implements [Discoverer]. Lookup order:
//  1. CLANK_GIT_TOKEN_<HOST> (universal override).
//  2. Per-provider conventional vars from [envProviderTokens].
//
// Returns [ErrNoCredential] if no env var is set. Never errors
// otherwise — env reads can't fail at runtime in any way we can
// distinguish from "unset".
func (EnvDiscoverer) Discover(_ context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error) {
	if tok := os.Getenv(clankEnvVarFor(ep.Host)); tok != "" {
		return tokenAsBasic(tok), nil
	}
	for _, name := range envProviderTokens[ep.Host] {
		if tok := os.Getenv(name); tok != "" {
			return tokenAsBasic(tok), nil
		}
	}
	return agent.GitCredential{}, ErrNoCredential
}

// clankEnvVarFor returns the CLANK_GIT_TOKEN_* var name for a given
// endpoint host. Exported via tests only (lowercase).
func clankEnvVarFor(host string) string {
	upper := strings.ToUpper(host)
	upper = strings.ReplaceAll(upper, ".", "_")
	upper = strings.ReplaceAll(upper, "-", "_")
	return clankEnvPrefix + upper
}

// tokenAsBasic wraps a bearer token as HTTPS-basic with the
// conventional username, the form git's askpass plumbing expects.
func tokenAsBasic(token string) agent.GitCredential {
	return agent.GitCredential{
		Kind:     agent.GitCredHTTPSBasic,
		Username: gitTokenUsername,
		Password: token,
	}
}
