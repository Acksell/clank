package hostclient

import (
	"context"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
)

// InProcess implements Client by calling *host.Service directly. The
// returned SessionBackend from CreateSession is the real backend
// instance, not a wrapper. Suitable for tests and the local-only
// deployment.
type InProcess struct {
	svc *host.Service
}

// NewInProcess constructs an in-process client around svc. svc must be
// non-nil.
func NewInProcess(svc *host.Service) *InProcess {
	if svc == nil {
		panic("hostclient.NewInProcess: svc is required")
	}
	return &InProcess{svc: svc}
}

// Close is a no-op; the caller manages the Service lifecycle.
func (c *InProcess) Close() error { return nil }

func (c *InProcess) Status(ctx context.Context) (host.HostStatus, error) {
	return c.svc.Status(ctx)
}

func (c *InProcess) ListBackends(ctx context.Context) ([]host.BackendInfo, error) {
	return c.svc.ListBackends(ctx)
}

func (c *InProcess) ListAgents(ctx context.Context, bt agent.BackendType, projectDir string) ([]host.AgentInfo, error) {
	return c.svc.ListAgents(ctx, bt, projectDir)
}

func (c *InProcess) ListModels(ctx context.Context, bt agent.BackendType, projectDir string) ([]host.ModelInfo, error) {
	return c.svc.ListModels(ctx, bt, projectDir)
}

func (c *InProcess) DiscoverSessions(ctx context.Context, bt agent.BackendType, seedDir string) ([]agent.SessionSnapshot, error) {
	return c.svc.DiscoverSessions(ctx, bt, seedDir)
}

func (c *InProcess) ListRepos(ctx context.Context) ([]host.Repo, error) {
	return c.svc.ListRepos(ctx)
}

func (c *InProcess) RegisterRepo(_ context.Context, ref host.RepoRef, rootDir string) (host.Repo, error) {
	return c.svc.RegisterRepo(ref, rootDir)
}

func (c *InProcess) ListBranchesByRepo(ctx context.Context, id host.RepoID) ([]host.BranchInfo, error) {
	return c.svc.ListBranchesByRepo(ctx, id)
}

func (c *InProcess) ResolveWorktreeByRepo(ctx context.Context, id host.RepoID, branch string) (host.WorktreeInfo, error) {
	return c.svc.ResolveWorktreeByRepo(ctx, id, branch)
}

func (c *InProcess) RemoveWorktreeByRepo(ctx context.Context, id host.RepoID, branch string, force bool) error {
	return c.svc.RemoveWorktreeByRepo(ctx, id, branch, force)
}

func (c *InProcess) MergeBranchByRepo(ctx context.Context, id host.RepoID, branch, commitMessage string) (host.MergeResult, error) {
	return c.svc.MergeBranchByRepo(ctx, id, branch, commitMessage)
}

func (c *InProcess) CreateSession(ctx context.Context, sessionID string, req agent.StartRequest) (agent.SessionBackend, host.CreateInfo, error) {
	return c.svc.CreateSession(ctx, sessionID, req)
}

func (c *InProcess) StopSession(_ context.Context, sessionID string) error {
	return c.svc.StopSession(sessionID)
}
