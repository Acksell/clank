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
	"strings"
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
	id              Hostname
	startedAt       time.Time
	backendManagers map[agent.BackendType]agent.BackendManager
	log             *log.Logger

	mu       sync.RWMutex
	sessions map[string]agent.SessionBackend
	// closed is set by Shutdown under s.mu and gates new session
	// registrations. CreateSession checks it both at entry and after the
	// (potentially slow) mgr.CreateBackend call so a backend created
	// concurrently with Shutdown cannot leak into a torn-down registry.
	closed bool

	reposMu sync.RWMutex
	repos   map[string]Repo // key: GitRef.Canonical()
	// repoStore is the optional persistence layer for the repo registry.
	// When non-nil, AddRepo writes through (in-memory map first,
	// then store) and New() loads any pre-existing rows into the map at
	// construction time. Tests that don't care about persistence leave
	// it nil and get pure in-memory behaviour. See §7.6 / step 5.
	repoStore RepoStore

	// cloneRoot is the parent directory under which CreateSession clones
	// repos when StartRequest.AllowClone is true. Each clone lands in
	// `<cloneRoot>/<sanitized-canonical>/`. Defaults to ~/.clank/repos at
	// construction time; tests override via Options.CloneRoot.
	cloneRoot string
}

// RepoStore is the persistence contract for the host's repo registry.
// internal/host/repostore.Store satisfies it; tests can supply their own
// in-memory implementation if they need to assert on persisted state
// without the SQLite cost. The interface is intentionally narrow — the
// host owns the canonical-derivation rules; the store is dumb storage.
type RepoStore interface {
	SaveRepo(repo Repo) error
	ListRepos() ([]Repo, error)
	ForgetRepo(canonical string) error
}

// Options configures a Service at construction time.
type Options struct {
	// ID is the host identifier. Defaults to HostLocal when empty.
	ID Hostname
	// BackendManagers maps each backend type to its manager. Required.
	BackendManagers map[agent.BackendType]agent.BackendManager
	// Log is the logger. Defaults to a logger writing to stderr with the
	// "[clank-host]" prefix.
	Log *log.Logger
	// RepoStore is the optional repo persistence layer. When set, New
	// preloads existing rows into the in-memory registry and AddRepo
	// write-throughs every change. Leave nil for pure in-memory operation
	// (e.g. unit tests).
	RepoStore RepoStore
	// CloneRoot is the parent directory under which CreateSession clones
	// repos when StartRequest.AllowClone is true. Defaults to
	// ~/.clank/repos when empty. Tests should set this to a t.TempDir().
	CloneRoot string
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
	s := &Service{
		id:              id,
		startedAt:       time.Now(),
		backendManagers: opts.BackendManagers,
		log:             lg,
		sessions:        make(map[string]agent.SessionBackend),
		repos:           make(map[string]Repo),
		repoStore:       opts.RepoStore,
		cloneRoot:       opts.CloneRoot,
	}
	if s.cloneRoot == "" {
		// Best-effort default; if the home dir lookup fails the field
		// stays empty and CreateSession will reject AllowClone with a
		// loud error rather than guessing a path.
		if home, err := os.UserHomeDir(); err == nil {
			s.cloneRoot = filepath.Join(home, ".clank", "repos")
		}
	}
	if opts.RepoStore != nil {
		// Preload at construction time so the registry is hot before any
		// HTTP handler dispatches. A failure here is logged but
		// non-fatal: the host can still serve registrations and accept
		// new repos; the operator gets one loud line in the log.
		existing, err := opts.RepoStore.ListRepos()
		if err != nil {
			lg.Printf("warning: load persisted repos: %v", err)
		} else {
			for _, r := range existing {
				s.repos[r.Ref.Canonical()] = r
			}
			if len(existing) > 0 {
				lg.Printf("loaded %d persisted repos", len(existing))
			}
		}
	}
	return s
}

// ID returns the host's ID.
func (s *Service) ID() Hostname { return s.id }

// Init initializes all BackendManagers. knownDirs is a per-backend lookup
// that returns previously-seen project directories (used to warm
// long-lived servers like OpenCode). Pass a func returning nil, nil to
// skip warm-up.
//
// Init does NOT block — initialization kicks off reconciler goroutines that
// live for the duration of ctx. The caller is expected to manage the
// overall process lifecycle; clank-host blocks on a signal, tests block on
// t.Cleanup → Shutdown.
//
// (Renamed from Run in the §2 cleanup pass: Go convention is that Run
// blocks for the lifetime of the work, e.g. http.Server.ListenAndServe
// or errgroup.Wait. This function returns immediately after kicking off
// goroutines, so Init reflects the actual semantics.)
func (s *Service) Init(ctx context.Context, knownDirs func(agent.BackendType) ([]string, error)) error {
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
// BackendManagers. Safe to call multiple times — subsequent calls are
// no-ops. After Shutdown returns, CreateSession will reject new
// registrations rather than leaking backends into a torn-down service.
func (s *Service) Shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
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
		Hostname:  s.id,
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

// ListAgents returns the agents supported by the given backend for the
// repo identified by ref. Per §7.3 of hub_host_refactor_code_review.md
// the wire is path-free: callers send a GitRef and the host resolves it
// to a working directory via its repo registry.
//
// Returns (nil, nil) when the backend is unknown to this host or its
// manager does not implement listing — both are normal "this host does
// not surface that capability" answers, not errors.
func (s *Service) ListAgents(ctx context.Context, bt agent.BackendType, ref agent.GitRef) ([]AgentInfo, error) {
	mgr, ok := s.backendManagers[bt]
	if !ok {
		return nil, nil
	}
	lister, ok := mgr.(agent.AgentLister)
	if !ok {
		return nil, nil
	}
	workDir, err := s.repoRoot(ref.Canonical())
	if err != nil {
		return nil, err
	}
	return lister.ListAgents(ctx, workDir)
}

// ListModels mirrors ListAgents for model catalogs. Same nil/nil
// semantics for unknown backends and non-listing managers; same
// path-free wire contract (caller sends GitRef, host resolves).
func (s *Service) ListModels(ctx context.Context, bt agent.BackendType, ref agent.GitRef) ([]ModelInfo, error) {
	mgr, ok := s.backendManagers[bt]
	if !ok {
		return nil, nil
	}
	lister, ok := mgr.(agent.ModelLister)
	if !ok {
		return nil, nil
	}
	workDir, err := s.repoRoot(ref.Canonical())
	if err != nil {
		return nil, err
	}
	return lister.ListModels(ctx, workDir)
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

// CreateSession creates a fresh SessionBackend for req and registers it
// under the given Hub-assigned session ID. The backend is NOT started —
// callers call Start() or Watch() on the returned backend.
//
// Returns the resolved serverURL (empty string for backends without an
// HTTP server, e.g. Claude Code). The hub uses serverURL on a per-session
// basis (e.g. for `opencode attach <url>` shell-out).
//
// Identity (Hostname, GitRef, WorktreeBranch) is path-free; the
// host implements the §7.5 resolution algorithm to map identity → workDir:
//
//  1. Use req.GitRef as the canonical key (kind + url|path).
//  2. If the key is already in the repo registry: use its rootDir.
//     If the caller also passed req.Dir and it disagrees with the stored
//     rootDir, error loudly — auto-rebind is YAGNI; operator must edit
//     the host's repo store.
//  3. Else if req.Dir is set: verify that req.Dir is a git repo whose
//     remotes (any of them) canonicalize to the key, then add it to the
//     registry. Mismatch → error.
//  4. Else if req.AllowClone is true: clone the remote URL into
//     <cloneRoot>/<sanitized-key>/ and add it.
//  5. Else: error — the caller must say how to obtain the repo.
//
// Then resolve (rootDir, WorktreeBranch) → workDir via resolveWorktree
// when WorktreeBranch is non-empty (otherwise workDir == rootDir).
//
// Note: this does not call req.Validate(). The watch-only activation
// path (re-attaching to a historical session) creates a backend without
// a prompt, which Validate() would reject. Callers that send prompts
// validate at their own boundary.
func (s *Service) CreateSession(ctx context.Context, sessionID string, req agent.StartRequest) (agent.SessionBackend, string, error) {
	if sessionID == "" {
		return nil, "", fmt.Errorf("session id is required")
	}
	if req.Backend == "" {
		return nil, "", fmt.Errorf("backend is required")
	}
	if req.Dir != "" && req.AllowClone {
		return nil, "", fmt.Errorf("dir and allow_clone are mutually exclusive")
	}
	mgr, ok := s.backendManagers[req.Backend]
	if !ok {
		return nil, "", fmt.Errorf("no backend manager for %s", req.Backend)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("host service is shut down")
	}
	if _, exists := s.sessions[sessionID]; exists {
		s.mu.Unlock()
		return nil, "", fmt.Errorf("session %s already registered", sessionID)
	}
	s.mu.Unlock()

	ref := req.GitRef
	if ref.Kind == "" {
		return nil, "", fmt.Errorf("git_ref is required")
	}
	if err := ref.Validate(); err != nil {
		return nil, "", fmt.Errorf("git_ref: %w", err)
	}
	if req.AllowClone && ref.Kind == GitRefLocal {
		return nil, "", fmt.Errorf("allow_clone is incompatible with git_ref.kind=local")
	}
	canonical := ref.Canonical()
	if canonical == "" {
		return nil, "", fmt.Errorf("git_ref %+v has empty canonical form", ref)
	}
	rootDir, err := s.resolveRepoRoot(ref, canonical, req)
	if err != nil {
		return nil, "", err
	}

	workDir := rootDir
	if req.WorktreeBranch != "" {
		wt, err := s.resolveWorktree(ctx, rootDir, req.WorktreeBranch)
		if err != nil {
			return nil, "", fmt.Errorf("resolve worktree for branch %q: %w", req.WorktreeBranch, err)
		}
		workDir = wt.WorktreeDir
	}

	b, err := mgr.CreateBackend(ctx, agent.BackendInvocation{
		WorkDir:          workDir,
		ResumeExternalID: req.SessionID,
	})
	if err != nil {
		return nil, "", err
	}
	// Re-check closed under the lock before publishing. mgr.CreateBackend
	// can take seconds (process spawn, HTTP probe) and Shutdown may have
	// run in the interim. Without this check the backend would be
	// inserted into a wiped registry and outlive the manager that owns
	// it.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if stopErr := b.Stop(); stopErr != nil {
			s.log.Printf("warning: stop backend created during shutdown: %v", stopErr)
		}
		return nil, "", fmt.Errorf("host service is shut down")
	}
	s.sessions[sessionID] = b
	s.mu.Unlock()

	// Resolve per-session serverURL for backends that expose one. Only
	// OpenCode currently does; iterate ListServers (no fresh start —
	// CreateBackend already ensured a server exists for workDir).
	serverURL := ""
	if oc, ok := mgr.(*OpenCodeBackendManager); ok {
		for _, srv := range oc.ListServers() {
			if srv.ProjectDir == workDir {
				serverURL = srv.URL
				break
			}
		}
	}

	return b, serverURL, nil
}

// resolveRepoRoot implements the §7.5 add/clone resolution. Returns the
// rootDir for canonical, adding it to the registry if necessary.
func (s *Service) resolveRepoRoot(ref GitRef, canonical string, req agent.StartRequest) (string, error) {
	// Step 2: lookup.
	if stored, ok := s.repoByCanonical(canonical); ok {
		if req.Dir != "" && req.Dir != stored.RootDir {
			return "", fmt.Errorf("host already knows repo %q at %q; remove the entry from the host's registry file if you moved the repo (auto-rebind is not supported)", canonical, stored.RootDir)
		}
		return stored.RootDir, nil
	}
	// Step 3: local-kind GitRef — RootDir is the path itself. No verify-via-Dir
	// step needed; the path IS the identity.
	if ref.Kind == GitRefLocal {
		if _, err := git.RepoRoot(ref.Path); err != nil {
			return "", fmt.Errorf("git_ref.path %q is not a git repository: %w", ref.Path, err)
		}
		if _, err := s.AddRepo(ref, ref.Path); err != nil {
			return "", err
		}
		return ref.Path, nil
	}
	// Step 4: caller pinned a directory — verify-and-add.
	if req.Dir != "" {
		if err := s.verifyDirMatchesRef(req.Dir, canonical); err != nil {
			return "", err
		}
		if _, err := s.AddRepo(ref, req.Dir); err != nil {
			return "", err
		}
		return req.Dir, nil
	}
	// Step 5: clone if explicitly allowed.
	if req.AllowClone {
		dest, err := s.cloneRepo(ref, canonical)
		if err != nil {
			return "", err
		}
		if _, err := s.AddRepo(ref, dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	// Step 6: nothing to go on.
	return "", fmt.Errorf("%w: repo %q unknown to host; pass `dir` to add an existing checkout or set `allow_clone=true` to clone", ErrNotFound, canonical)
}

// verifyDirMatchesRef confirms dir is a git repo whose `git remote` set
// contains at least one URL that canonicalizes to want. People fork and
// add upstream remotes all the time — accepting any matching remote
// keeps the UX sane while still preventing the wrong-repo footgun.
//
// Path comparison evaluates symlinks on both sides because `git
// rev-parse --show-toplevel` returns the realpath while callers
// commonly pass the symlinked form (e.g. macOS /tmp → /private/tmp).
func (s *Service) verifyDirMatchesRef(dir, want string) error {
	root, err := git.RepoRoot(dir)
	if err != nil {
		return fmt.Errorf("verify dir %q: %w", dir, err)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve repo root %q: %w", root, err)
	}
	dirResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("resolve dir %q: %w", dir, err)
	}
	if rootResolved != dirResolved {
		return fmt.Errorf("dir %q is inside repo %q but is not the repo root", dir, root)
	}
	remotes, err := git.RemoteURLs(dir)
	if err != nil {
		return fmt.Errorf("verify dir %q: %w", dir, err)
	}
	for _, url := range remotes {
		if (GitRef{Kind: GitRefRemote, URL: url}).Canonical() == want {
			return nil
		}
	}
	return fmt.Errorf("dir %q remotes (%v) do not match requested repo %q", dir, remotes, want)
}

// cloneRepo clones ref.URL into <cloneRoot>/<sanitized-canonical>/ and
// returns the destination path. Errors when cloneRoot is unset or the
// destination already exists (which would mean the registry lost track
// of an existing clone — a louder failure than silently using it).
func (s *Service) cloneRepo(ref GitRef, canonical string) (string, error) {
	if s.cloneRoot == "" {
		return "", fmt.Errorf("cannot clone repo %q: host has no clone_root configured", canonical)
	}
	dest := filepath.Join(s.cloneRoot, sanitizeCanonical(canonical))
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("clone destination %q already exists but repo is not in registry; remove the directory or add it manually", dest)
	}
	if err := git.Clone(ref.URL, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// sanitizeCanonical maps a canonical GitRef string to a single
// filesystem-safe path component. The canonical form is
// "github.com/owner/repo" for remote refs; replace separators with
// dashes so we get one directory per repo under cloneRoot.
func sanitizeCanonical(canonical string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-")
	return r.Replace(canonical)
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
