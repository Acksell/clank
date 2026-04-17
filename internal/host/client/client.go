// Package hostclient is the Hub-side abstraction for talking to a Host.
//
// It exists so the Hub never imports `internal/host` directly. Two
// implementations satisfy the Client interface:
//
//   - InProcess (constructed via NewInProcess) wraps a *host.Service
//     directly. Used in tests and in the local-only deployment where Hub
//     and Host live in the same process.
//
//   - HTTP (constructed via NewHTTP) talks to a remote Host over HTTP
//     (Unix socket locally, HTTPS for managed remote hosts). Used in
//     the default split-process deployment.
//
// Note: this Client interface deviates from the original refactor doc's
// Decision #3 ("no Host Go interface"). We keep it because:
//
//   - The Hub's callers need a single type to depend on, otherwise every
//     call site would have to be templated/duplicated.
//   - Tests need a way to inject a fake or in-process Host without
//     spinning subprocess + HTTP. The interface gives us that without
//     mocking — the InProcess impl IS the production code path for the
//     local case.
//   - The interface is narrow: it mirrors host.Service's surface 1:1.
//     There is no "third implementation" risk because every Host backend
//     must implement the same wire protocol.
package hostclient

import (
	"context"
	"io"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// Client is what the Hub uses to talk to a single Host.
type Client interface {
	io.Closer

	Status(ctx context.Context) (host.HostStatus, error)
	ListBackends(ctx context.Context) ([]host.BackendInfo, error)
	ListAgents(ctx context.Context, bt agent.BackendType, projectDir string) ([]host.AgentInfo, error)
	ListModels(ctx context.Context, bt agent.BackendType, projectDir string) ([]host.ModelInfo, error)
	DiscoverSessions(ctx context.Context, bt agent.BackendType, seedDir string) ([]agent.SessionSnapshot, error)

	ListBranches(ctx context.Context, projectDir string) ([]host.BranchInfo, error)
	ResolveWorktree(ctx context.Context, projectDir, branch string) (host.WorktreeInfo, error)
	RemoveWorktree(ctx context.Context, projectDir, branch string, force bool) error
	MergeBranch(ctx context.Context, projectDir, branch, commitMessage string) (host.MergeResult, error)

	// Phase 3B: RepoID-scoped variants. The Hub uses these once the TUI
	// is on the host-scoped API. The legacy path-style methods above
	// stick around until Phase 3D removes them.
	ListRepos(ctx context.Context) ([]host.Repo, error)
	ListBranchesByRepo(ctx context.Context, id host.RepoID) ([]host.BranchInfo, error)
	ResolveWorktreeByRepo(ctx context.Context, id host.RepoID, branch string) (host.WorktreeInfo, error)
	RemoveWorktreeByRepo(ctx context.Context, id host.RepoID, branch string, force bool) error
	MergeBranchByRepo(ctx context.Context, id host.RepoID, branch, commitMessage string) (host.MergeResult, error)

	// CreateSession registers a SessionBackend on the Host under
	// sessionID and returns a SessionBackend the Hub can use as if it
	// were the real backend. With InProcess, the returned object IS the
	// real backend; with HTTP, it is a thin adapter that translates
	// each method call to HTTP/SSE.
	CreateSession(ctx context.Context, sessionID string, req agent.StartRequest) (agent.SessionBackend, error)

	// StopSession stops the SessionBackend registered under sessionID
	// and releases it from the Host's registry.
	StopSession(ctx context.Context, sessionID string) error
}

// Compile-time guarantees that the two implementations satisfy Client.
var (
	_ Client = (*InProcess)(nil)
	_ Client = (*HTTP)(nil)
)

// Compile-time guarantee that the HTTP-side session adapter satisfies
// the SessionBackend interface.
var _ agent.SessionBackend = (*httpSessionBackend)(nil)
