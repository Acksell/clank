package host

// Service is the in-process domain object for the Host plane. It owns the
// BackendManagers that run agent sessions and the git/worktree logic tied
// to repos on this host's filesystem.
//
// A Service exists in two places in the end-state architecture:
//   - Inside the `clank-host` process, this is the real thing — it owns
//     BackendManagers that spawn processes and touches the filesystem.
//   - Inside tests, a Service can be constructed in-process with mock
//     BackendManagers and driven directly without spinning up HTTP.
//
// The Hub (`clankd`) never holds a *Service directly; it always goes
// through the `internal/host/client` package. That client has two
// implementations: an in-process adapter that calls Service methods
// directly (used in tests) and an HTTP adapter that dials a Unix socket
// or TCP+TLS endpoint (used in production).

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/git"
)

// Service is the Host plane's domain object. Construct with New; call Run
// to block until ctx cancels, and Shutdown to release resources.
//
// Service owns a registry of live SessionBackends keyed by the
// Hub-assigned session ID (a ULID). The Hub is the source of truth for
// session IDs because it owns the durable registry; the Host stores
// backends under those IDs so HTTP handlers can look them up by URL
// path. The in-process hostclient adapter uses the same ID-keyed API for
// parity with the HTTP adapter.
type Service struct {
	id              HostID
	startedAt       time.Time
	backendManagers map[agent.BackendType]agent.BackendManager
	log             *log.Logger

	mu       sync.RWMutex
	sessions map[string]agent.SessionBackend

	reposMu sync.RWMutex
	repos   map[RepoID]Repo
}

// Options configures a Service at construction time.
type Options struct {
	// ID is the host identifier. Defaults to HostLocal when empty.
	ID HostID
	// BackendManagers maps each backend type to its manager. Required.
	BackendManagers map[agent.BackendType]agent.BackendManager
	// Log is the logger. Defaults to a logger writing to stderr with the
	// "[clank-host]" prefix.
	Log *log.Logger
}

// New creates a Service. Panics if opts.BackendManagers is nil — the Host
// is not useful without at least one backend manager, and a fast failure
// at construction time is clearer than a nil deref later.
func New(opts Options) *Service {
	if opts.BackendManagers == nil {
		panic("host.New: BackendManagers is required")
	}
	id := opts.ID
	if id == "" {
		id = HostLocal
	}
	lg := opts.Log
	if lg == nil {
		lg = log.New(os.Stderr, "[clank-host] ", log.LstdFlags|log.Lmsgprefix)
	}
	return &Service{
		id:              id,
		startedAt:       time.Now(),
		backendManagers: opts.BackendManagers,
		log:             lg,
		sessions:        make(map[string]agent.SessionBackend),
		repos:           make(map[RepoID]Repo),
	}
}

// ID returns the host's ID.
func (s *Service) ID() HostID { return s.id }

// Run initializes all BackendManagers. knownDirs is a per-backend lookup
// that returns previously-seen project directories (used to warm
// long-lived servers like OpenCode). Pass a func returning nil, nil to
// skip warm-up.
//
// Run does NOT block — initialization kicks off reconciler goroutines that
// live for the duration of ctx. The caller is expected to manage the
// overall process lifecycle; clank-host blocks on a signal, tests block on
// t.Cleanup → Shutdown.
func (s *Service) Run(ctx context.Context, knownDirs func(agent.BackendType) ([]string, error)) error {
	for bt, mgr := range s.backendManagers {
		bt := bt
		fn := func() ([]string, error) {
			if knownDirs == nil {
				return nil, nil
			}
			return knownDirs(bt)
		}
		if err := mgr.Init(ctx, fn); err != nil {
			s.log.Printf("warning: init %s backend: %v", bt, err)
		}
	}
	return nil
}

// Shutdown stops all live SessionBackends and then shuts down all
// BackendManagers. Safe to call multiple times.
func (s *Service) Shutdown() {
	s.mu.Lock()
	live := s.sessions
	s.sessions = make(map[string]agent.SessionBackend)
	s.mu.Unlock()
	for id, b := range live {
		if err := b.Stop(); err != nil {
			s.log.Printf("warning: stop session %s: %v", id, err)
		}
	}
	for bt, mgr := range s.backendManagers {
		s.log.Printf("shutting down %s backend manager", bt)
		mgr.Shutdown()
	}
}

// Status returns the current host status.
func (s *Service) Status(_ context.Context) (HostStatus, error) {
	s.mu.RLock()
	live := len(s.sessions)
	s.mu.RUnlock()
	return HostStatus{
		HostID:    s.id,
		Version:   "", // Populated once we have a version string wired up
		StartedAt: s.startedAt,
		Sessions:  live,
	}, nil
}

// ListBackends returns the set of backends known to this host.
func (s *Service) ListBackends(_ context.Context) ([]BackendInfo, error) {
	out := make([]BackendInfo, 0, len(s.backendManagers))
	for bt := range s.backendManagers {
		out = append(out, BackendInfo{
			Name:        bt,
			DisplayName: string(bt),
			Available:   true,
		})
	}
	return out, nil
}

// ListAgents returns the agents supported by the given backend for a
// project directory. Not every backend manager implements agent listing;
// callers get an empty slice and no error when a manager lacks the
// capability.
// Returns (nil, nil) when the backend is unknown to this host or its
// manager does not implement listing — both are normal "this host does
// not surface that capability" answers, not errors.
func (s *Service) ListAgents(ctx context.Context, bt agent.BackendType, projectDir string) ([]AgentInfo, error) {
	mgr, ok := s.backendManagers[bt]
	if !ok {
		return nil, nil
	}
	lister, ok := mgr.(agent.AgentLister)
	if !ok {
		return nil, nil
	}
	return lister.ListAgents(ctx, projectDir)
}

// ListModels mirrors ListAgents for model catalogs. Same nil/nil
// semantics for unknown backends and non-listing managers.
func (s *Service) ListModels(ctx context.Context, bt agent.BackendType, projectDir string) ([]ModelInfo, error) {
	mgr, ok := s.backendManagers[bt]
	if !ok {
		return nil, nil
	}
	lister, ok := mgr.(agent.ModelLister)
	if !ok {
		return nil, nil
	}
	return lister.ListModels(ctx, projectDir)
}

// DiscoverSessions asks the given backend manager for historical sessions
// it already knows about. Returns nil, nil for managers that do not
// implement discovery (e.g. Claude Code, which is stateless across runs).
// Returns (nil, nil) for unknown backend or backend without discovery
// capability — best-effort semantics so the Hub can fan out across all
// known backends without dealing with per-backend feature errors.
func (s *Service) DiscoverSessions(ctx context.Context, bt agent.BackendType, seedDir string) ([]agent.SessionSnapshot, error) {
	mgr, ok := s.backendManagers[bt]
	if !ok {
		return nil, nil
	}
	disc, ok := mgr.(agent.SessionDiscoverer)
	if !ok {
		return nil, nil
	}
	return disc.DiscoverSessions(ctx, seedDir)
}

// CreateInfo is the host-resolved runtime metadata for a session
// backend. The hub uses it to populate SessionInfo.{ProjectDir,
// WorktreeDir} without having to know about host filesystem layout.
type CreateInfo struct {
	ProjectDir  string // Repo root directory on the host's filesystem.
	WorktreeDir string // Worktree path; equals ProjectDir when no branch was requested.
}

// CreateSession creates a fresh SessionBackend for req and registers it
// under the given Hub-assigned session ID. The backend is NOT started —
// callers call Start() or Watch() on the returned backend.
//
// Identity (HostID, RepoRemoteURL, Branch) is path-free; the host
// resolves a working directory by:
//  1. deriving RepoID from req.RepoRemoteURL,
//  2. looking up the previously-registered rootDir,
//  3. resolving (rootDir, Branch) → workDir via resolveWorktree when
//     Branch is non-empty (otherwise workDir == rootDir).
//
// The repo MUST have been registered via RegisterRepo (either directly
// or through the host mux's POST /repos route) before CreateSession can
// resolve the workDir. This is the explicit replacement for the legacy
// auto-registration that piggy-backed on the path-bearing wire format.
//
// Note: this does not call req.Validate(). The watch-only activation
// path (re-attaching to a historical session) creates a backend without
// a prompt, which Validate() would reject. Callers that send prompts
// validate at their own boundary.
func (s *Service) CreateSession(ctx context.Context, sessionID string, req agent.StartRequest) (agent.SessionBackend, CreateInfo, error) {
	if sessionID == "" {
		return nil, CreateInfo{}, fmt.Errorf("session id is required")
	}
	if req.Backend == "" {
		return nil, CreateInfo{}, fmt.Errorf("backend is required")
	}
	if req.RepoRemoteURL == "" {
		return nil, CreateInfo{}, fmt.Errorf("repo_remote_url is required")
	}
	mgr, ok := s.backendManagers[req.Backend]
	if !ok {
		return nil, CreateInfo{}, fmt.Errorf("no backend manager for %s", req.Backend)
	}
	s.mu.Lock()
	if _, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		return nil, CreateInfo{}, fmt.Errorf("session %s already registered", sessionID)
	}
	s.mu.Unlock()

	repoID, err := RepoRef{RemoteURL: req.RepoRemoteURL}.ID()
	if err != nil {
		return nil, CreateInfo{}, fmt.Errorf("derive repo id: %w", err)
	}
	rootDir, err := s.repoRoot(repoID)
	if err != nil {
		return nil, CreateInfo{}, err
	}

	workDir := rootDir
	if req.Branch != "" {
		wt, err := s.resolveWorktree(ctx, rootDir, req.Branch)
		if err != nil {
			return nil, CreateInfo{}, fmt.Errorf("resolve worktree for branch %q: %w", req.Branch, err)
		}
		workDir = wt.WorktreeDir
	}

	b, err := mgr.CreateBackend(req, workDir)
	if err != nil {
		return nil, CreateInfo{}, err
	}
	s.mu.Lock()
	s.sessions[sessionID] = b
	s.mu.Unlock()

	return b, CreateInfo{ProjectDir: rootDir, WorktreeDir: workDir}, nil
}

// Session returns the SessionBackend registered under id, or nil and
// false if there is no such session.
func (s *Service) Session(id string) (agent.SessionBackend, bool) {
	s.mu.RLock()
	b, ok := s.sessions[id]
	s.mu.RUnlock()
	return b, ok
}

// StopSession stops the SessionBackend registered under id and removes
// it from the registry. Returns ErrNotFound if there is no such session.
// Safe to call concurrently with reads.
func (s *Service) StopSession(id string) error {
	s.mu.Lock()
	b, ok := s.sessions[id]
	if !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.sessions, id)
	s.mu.Unlock()
	return b.Stop()
}

// --- Worktree / branch ops ----------------------------------------------

// listBranches returns the branches (and their checked-out worktrees)
// for the repository at projectDir. Skips bare and detached entries.
func (s *Service) listBranches(_ context.Context, projectDir string) ([]BranchInfo, error) {
	worktrees, err := git.ListWorktrees(projectDir)
	if err != nil {
		return nil, err
	}

	defaultBranch, _ := git.DefaultBranch(projectDir)
	currentBranch, _ := git.CurrentBranch(projectDir)

	result := make([]BranchInfo, 0, len(worktrees))
	for _, wt := range worktrees {
		if wt.Bare || wt.Branch == "" {
			continue
		}
		info := BranchInfo{
			Name:        wt.Branch,
			WorktreeDir: wt.Path,
			IsDefault:   wt.Branch == defaultBranch,
			IsCurrent:   wt.Branch == currentBranch,
		}
		// Diff stats + ahead count only make sense for non-default branches.
		if wt.Branch != defaultBranch {
			if added, removed, err := git.DiffStat(wt.Path, defaultBranch); err == nil {
				info.LinesAdded = added
				info.LinesRemoved = removed
			}
			if ahead, err := git.CommitsAhead(projectDir, defaultBranch, wt.Branch); err == nil {
				info.CommitsAhead = ahead
			}
		}
		result = append(result, info)
	}
	return result, nil
}

// resolveWorktree ensures a git worktree exists for (projectDir, branch).
// If the branch has a worktree, returns its path; otherwise creates one.
// If the branch does not exist locally, creates a new branch from the
// repo's default branch.
func (s *Service) resolveWorktree(_ context.Context, projectDir, branch string) (WorktreeInfo, error) {
	wt, err := git.FindWorktreeForBranch(projectDir, branch)
	if err != nil {
		return WorktreeInfo{}, err
	}
	if wt != nil {
		return WorktreeInfo{Branch: branch, WorktreeDir: wt.Path}, nil
	}

	projectName := filepath.Base(projectDir)
	wtDir, err := git.WorktreeDir(projectName, branch)
	if err != nil {
		return WorktreeInfo{}, err
	}

	exists, err := git.BranchExists(projectDir, branch)
	if err != nil {
		return WorktreeInfo{}, err
	}

	if exists {
		if err := git.AddWorktree(projectDir, wtDir, branch); err != nil {
			return WorktreeInfo{}, err
		}
	} else {
		base, err := git.DefaultBranch(projectDir)
		if err != nil {
			return WorktreeInfo{}, fmt.Errorf("determine default branch: %w", err)
		}
		if err := git.AddWorktreeNewBranch(projectDir, wtDir, branch, base); err != nil {
			return WorktreeInfo{}, err
		}
	}
	s.log.Printf("created worktree for branch %q at %s", branch, wtDir)
	return WorktreeInfo{Branch: branch, WorktreeDir: wtDir}, nil
}

// removeWorktree removes the worktree for (projectDir, branch). Returns
// an error if there is no such worktree.
func (s *Service) removeWorktree(_ context.Context, projectDir, branch string, force bool) error {
	wt, err := git.FindWorktreeForBranch(projectDir, branch)
	if err != nil {
		return err
	}
	if wt == nil {
		return fmt.Errorf("no worktree found for branch %q", branch)
	}
	if err := git.RemoveWorktree(projectDir, wt.Path, force); err != nil {
		return err
	}
	s.log.Printf("removed worktree for branch %q at %s", branch, wt.Path)
	return nil
}

// MergeResult describes the outcome of MergeBranch.
type MergeResult struct {
	MergedBranch    string
	BranchWorktree  string // Path of the feature-branch worktree (empty if it was cleaned up)
	WorktreeRemoved bool
	BranchDeleted   bool
}

// mergeBranch merges `branch` into the repo's default branch. Before
// merging, it `git add -A`s the feature worktree; if there are staged
// changes, commitMessage is used to commit them first (required in that
// case).
//
// Returns an error with a stable sentinel (via errors.Is) for each
// user-facing failure mode so handlers can translate to appropriate HTTP
// status codes.
func (s *Service) mergeBranch(_ context.Context, projectDir, branch, commitMessage string) (MergeResult, error) {
	defaultBranch, err := git.DefaultBranch(projectDir)
	if err != nil {
		return MergeResult{}, fmt.Errorf("determine default branch: %w", err)
	}
	if branch == defaultBranch {
		return MergeResult{}, ErrCannotMergeDefault
	}

	branchWt, err := git.FindWorktreeForBranch(projectDir, branch)
	if err != nil {
		return MergeResult{}, fmt.Errorf("find branch worktree: %w", err)
	}
	if branchWt == nil {
		return MergeResult{}, fmt.Errorf("%w: no worktree found for branch %q", ErrNotFound, branch)
	}

	if err := git.AddAll(branchWt.Path); err != nil {
		return MergeResult{}, fmt.Errorf("git add -A in worktree: %w", err)
	}
	hasStagedWork, err := git.HasStagedChanges(branchWt.Path)
	if err != nil {
		return MergeResult{}, fmt.Errorf("check staged changes: %w", err)
	}
	commitsAhead, err := git.CommitsAhead(projectDir, defaultBranch, branch)
	if err != nil {
		return MergeResult{}, fmt.Errorf("count commits ahead: %w", err)
	}
	if !hasStagedWork && commitsAhead == 0 {
		return MergeResult{}, ErrNothingToMerge
	}
	if hasStagedWork {
		if commitMessage == "" {
			return MergeResult{}, ErrCommitMessageRequired
		}
		if err := git.Commit(branchWt.Path, commitMessage); err != nil {
			return MergeResult{}, fmt.Errorf("commit worktree changes: %w", err)
		}
		s.log.Printf("committed worktree changes in %s on branch %q", branchWt.Path, branch)
	}

	mainWt, err := git.FindWorktreeForBranch(projectDir, defaultBranch)
	if err != nil {
		return MergeResult{}, fmt.Errorf("find main worktree: %w", err)
	}
	if mainWt == nil {
		return MergeResult{}, fmt.Errorf("%w: no worktree for default branch %q", ErrNotFound, defaultBranch)
	}
	clean, err := git.IsClean(mainWt.Path)
	if err != nil {
		return MergeResult{}, fmt.Errorf("check worktree clean: %w", err)
	}
	if !clean {
		return MergeResult{}, ErrMainDirty
	}

	mergeMsg := fmt.Sprintf("Merge branch '%s'", branch)
	if err := git.MergeNoFF(mainWt.Path, branch, mergeMsg); err != nil {
		if git.IsMerging(mainWt.Path) {
			_ = git.AbortMerge(mainWt.Path)
			return MergeResult{}, ErrMergeConflict
		}
		return MergeResult{}, fmt.Errorf("merge failed: %w", err)
	}
	s.log.Printf("merged branch %q into %q in %s", branch, defaultBranch, mainWt.Path)

	res := MergeResult{
		MergedBranch:   branch,
		BranchWorktree: branchWt.Path,
	}
	if err := git.RemoveWorktree(projectDir, branchWt.Path, true); err != nil {
		s.log.Printf("warning: could not remove worktree after merge: %v", err)
	} else {
		res.WorktreeRemoved = true
	}
	if err := git.DeleteBranch(projectDir, branch, false); err != nil {
		s.log.Printf("warning: could not delete branch after merge: %v", err)
	} else {
		res.BranchDeleted = true
	}
	return res, nil
}
