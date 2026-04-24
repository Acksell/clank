package gitcred

import (
	"context"
	"errors"
	"fmt"

	"github.com/acksell/clank/internal/agent"
)

// Stack runs a slice of [Discoverer]s in order and returns the first
// credential one of them produces. The first discoverer to return a
// non-error result wins; later discoverers are not consulted.
//
// A discoverer returning [ErrNoCredential] means "I have nothing,
// keep going." Any other error short-circuits the stack and bubbles
// up — broken local config should fail loudly, not silently fall
// through to a less-secure source.
//
// Stack itself returns [ErrNoCredential] only when EVERY discoverer
// returned [ErrNoCredential].
type Stack struct {
	Discoverers []Discoverer
}

// Discover implements [Discoverer]. ep must be non-nil and validate.
func (s Stack) Discover(ctx context.Context, ep *agent.GitEndpoint) (agent.GitCredential, error) {
	if ep == nil {
		return agent.GitCredential{}, fmt.Errorf("gitcred: nil endpoint")
	}
	if err := ep.Validate(); err != nil {
		return agent.GitCredential{}, fmt.Errorf("gitcred: invalid endpoint: %w", err)
	}
	for i, d := range s.Discoverers {
		// Reject nil entries up front rather than letting them
		// panic on d.Discover(...). A nil slot is a programming
		// bug at registration time and we want a clear error.
		// (CodeRabbit PR #3 inline 3137099827.)
		if d == nil {
			return agent.GitCredential{}, fmt.Errorf("gitcred: discoverer %d is nil", i)
		}
		cred, err := d.Discover(ctx, ep)
		if err == nil {
			if vErr := cred.Validate(); vErr != nil {
				return agent.GitCredential{}, fmt.Errorf("gitcred: discoverer %d returned invalid credential: %w", i, vErr)
			}
			return cred, nil
		}
		if errors.Is(err, ErrNoCredential) {
			continue
		}
		return agent.GitCredential{}, fmt.Errorf("gitcred: discoverer %d: %w", i, err)
	}
	return agent.GitCredential{}, ErrNoCredential
}
