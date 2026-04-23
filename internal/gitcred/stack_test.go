package gitcred

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/acksell/clank/internal/agent"
)

func TestStack_FirstHitWins(t *testing.T) {
	t.Parallel()
	ep := validEp(t, "github.com")
	calls := 0
	first := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		calls++
		return tokenAsBasic("from-first"), nil
	})
	second := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		t.Fatal("second discoverer should not be consulted after first hit")
		return agent.GitCredential{}, nil
	})
	cred, err := (Stack{Discoverers: []Discoverer{first, second}}).Discover(context.Background(), ep)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Password != "from-first" {
		t.Fatalf("password = %q, want from-first", cred.Password)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestStack_SoftMissContinues(t *testing.T) {
	t.Parallel()
	miss := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		return agent.GitCredential{}, ErrNoCredential
	})
	hit := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		return tokenAsBasic("after-miss"), nil
	})
	cred, err := (Stack{Discoverers: []Discoverer{miss, hit}}).Discover(context.Background(), validEp(t, "github.com"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if cred.Password != "after-miss" {
		t.Fatalf("password = %q, want after-miss", cred.Password)
	}
}

func TestStack_HardErrorShortCircuits(t *testing.T) {
	t.Parallel()
	hard := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		return agent.GitCredential{}, fmt.Errorf("boom")
	})
	never := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		t.Fatal("should not be consulted after hard error")
		return agent.GitCredential{}, nil
	})
	_, err := (Stack{Discoverers: []Discoverer{hard, never}}).Discover(context.Background(), validEp(t, "github.com"))
	if err == nil || errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want hard error", err)
	}
}

func TestStack_AllMissReturnsErrNoCredential(t *testing.T) {
	t.Parallel()
	miss := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		return agent.GitCredential{}, ErrNoCredential
	})
	_, err := (Stack{Discoverers: []Discoverer{miss, miss}}).Discover(context.Background(), validEp(t, "github.com"))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential", err)
	}
}

func TestStack_RejectsInvalidCredFromDiscoverer(t *testing.T) {
	t.Parallel()
	// Defence-in-depth: a buggy discoverer returning a malformed cred
	// must not propagate to the wire.
	bad := DiscovererFunc(func(context.Context, *agent.GitEndpoint) (agent.GitCredential, error) {
		return agent.GitCredential{Kind: agent.GitCredHTTPSBasic, Username: "x"}, nil // missing password
	})
	_, err := (Stack{Discoverers: []Discoverer{bad}}).Discover(context.Background(), validEp(t, "github.com"))
	if err == nil || errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want validation hard error", err)
	}
}

func TestStack_NilEndpointRejected(t *testing.T) {
	t.Parallel()
	_, err := Stack{}.Discover(context.Background(), nil)
	if err == nil {
		t.Fatal("nil endpoint must error")
	}
}
