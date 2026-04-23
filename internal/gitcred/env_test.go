package gitcred

import (
	"context"
	"errors"
	"testing"

	"github.com/acksell/clank/internal/agent"
)

// validEp is the canonical github endpoint used across tests. Kept as
// a helper rather than a top-level var so tests are independent of
// init order.
func validEp(t *testing.T, host string) *agent.GitEndpoint {
	t.Helper()
	ep := &agent.GitEndpoint{
		Protocol: agent.GitProtoHTTPS,
		Host:     host,
		Path:     "owner/repo",
	}
	if err := ep.Validate(); err != nil {
		t.Fatalf("invalid endpoint setup: %v", err)
	}
	return ep
}

func TestEnvDiscoverer_ClankOverrideWinsOverProviderVar(t *testing.T) {
	// Universal override must shadow the per-provider var so users can
	// force a specific token without unsetting their global GH_TOKEN.
	t.Setenv("GH_TOKEN", "provider-token")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("CLANK_GIT_TOKEN_GITHUB_COM", "override-token")

	cred, err := FromEnv().Discover(context.Background(), validEp(t, "github.com"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Password != "override-token" {
		t.Fatalf("password = %q, want override-token", cred.Password)
	}
	if cred.Username != gitTokenUsername {
		t.Fatalf("username = %q, want %q", cred.Username, gitTokenUsername)
	}
	if cred.Kind != agent.GitCredHTTPSBasic {
		t.Fatalf("kind = %q, want https_basic", cred.Kind)
	}
}

func TestEnvDiscoverer_PerProviderFallback(t *testing.T) {
	// With no override, the first provider env var wins.
	t.Setenv("CLANK_GIT_TOKEN_GITHUB_COM", "")
	t.Setenv("GH_TOKEN", "gh-tok")
	t.Setenv("GITHUB_TOKEN", "github-tok")

	cred, err := FromEnv().Discover(context.Background(), validEp(t, "github.com"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Password != "gh-tok" {
		t.Fatalf("password = %q, want gh-tok (first-in-list)", cred.Password)
	}
}

func TestEnvDiscoverer_NoEnvReturnsErrNoCredential(t *testing.T) {
	t.Setenv("CLANK_GIT_TOKEN_GITHUB_COM", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	_, err := FromEnv().Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

func TestEnvDiscoverer_DoesNotLeakAcrossProviders(t *testing.T) {
	// Regression: a github PAT in GH_TOKEN must NOT be returned for a
	// gitlab.com endpoint. There is no provider-agnostic env var.
	t.Setenv("GH_TOKEN", "github-pat-do-not-leak")
	t.Setenv("GITHUB_TOKEN", "github-pat-do-not-leak")
	t.Setenv("CLANK_GIT_TOKEN_GITLAB_COM", "")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GL_TOKEN", "")

	_, err := FromEnv().Discover(context.Background(), validEp(t, "gitlab.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential (no leak)", err)
	}
}

func TestEnvDiscoverer_ClankOverrideForUnknownHost(t *testing.T) {
	// Self-hosted git server: not in the provider table, but the
	// universal override must still work.
	t.Setenv("CLANK_GIT_TOKEN_GIT_INTERNAL_CORP", "internal-tok")

	cred, err := FromEnv().Discover(context.Background(), validEp(t, "git.internal.corp"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Password != "internal-tok" {
		t.Fatalf("password = %q, want internal-tok", cred.Password)
	}
}

func TestClankEnvVarFor_NormalisesPunctuation(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"github.com":        "CLANK_GIT_TOKEN_GITHUB_COM",
		"git.internal.corp": "CLANK_GIT_TOKEN_GIT_INTERNAL_CORP",
		"my-host.example":   "CLANK_GIT_TOKEN_MY_HOST_EXAMPLE",
	}
	for in, want := range cases {
		if got := clankEnvVarFor(in); got != want {
			t.Errorf("clankEnvVarFor(%q) = %q, want %q", in, got, want)
		}
	}
}
