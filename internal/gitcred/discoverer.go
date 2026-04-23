package gitcred

import (
	"context"

	"github.com/acksell/clank/internal/agent"
)

// Discoverer locates a credential for a single git endpoint from one
// source (env, gh CLI, settings file, ...). Implementations MUST:
//
//   - Return [ErrNoCredential] when this source has nothing for ep —
//     this is the "try the next source" signal.
//   - Return any other error verbatim when the source IS configured
//     but is broken (gh crashed, settings file unparseable, etc.).
//     The [Stack] will not swallow these.
//   - Return a credential whose [agent.GitCredential.Validate] passes
//     so downstream wire serialization can't fail on malformed input.
//
// Implementations must not read the network. ctx is provided for
// short-lived subprocess calls (e.g. `gh auth token`).
type Discoverer interface {
	Discover(ctx context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error)
}

// DiscovererFunc adapts a plain function to [Discoverer] for tests
// and small inline discoverers.
type DiscovererFunc func(ctx context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error)

// Discover implements [Discoverer].
func (f DiscovererFunc) Discover(ctx context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error) {
	return f(ctx, ep)
}
