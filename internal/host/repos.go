package host

import (
	"context"
	"fmt"
	"sort"
)

// Repo registry. The Host keeps a (canonical GitRef → RootDir) map so it
// can serve `/repos/{gitref}/...` routes without callers having to send
// filesystem paths over the wire. Populated exclusively via the explicit
// RegisterRepo entry point — clients (TUI/CLI/tests) call
// hubclient.RegisterRepoOnHost after resolving cwd → (GitRef, root).
// CreateSession no longer auto-registers; if the host doesn't know the
// repo, it returns ErrNotFound so the caller fails loudly instead of
// silently inferring identity from a filesystem path.

// RegisterRepo records a (canonical → rootDir) mapping derived from ref
// and returns the resulting Repo. Idempotent: re-registering the same
// canonical updates the rootDir (last writer wins) so a moved checkout
// is picked up automatically.
func (s *Service) RegisterRepo(ref GitRef, rootDir string) (Repo, error) {
	if rootDir == "" {
		return Repo{}, fmt.Errorf("root_dir is required")
	}
	if err := ref.Validate(); err != nil {
		return Repo{}, fmt.Errorf("invalid git ref: %w", err)
	}
	canonical := ref.Canonical()
	repo := Repo{Ref: ref, RootDir: rootDir}
	s.reposMu.Lock()
	s.repos[canonical] = repo
	s.reposMu.Unlock()
	return repo, nil
}

// Repo returns the Repo registered under ref's canonical, or false if
// there is no such repo. Callers that need a path use Repo.RootDir.
func (s *Service) Repo(ref GitRef) (Repo, bool) {
	return s.repoByCanonical(ref.Canonical())
}

// repoByCanonical is the lookup used by HTTP handlers that only have the
// URL-encoded canonical string (no Kind/URL info).
func (s *Service) repoByCanonical(canonical string) (Repo, bool) {
	if canonical == "" {
		return Repo{}, false
	}
	s.reposMu.RLock()
	r, ok := s.repos[canonical]
	s.reposMu.RUnlock()
	return r, ok
}

// ListRepos returns all repos this host has been told about, sorted by
// canonical for stable output. An empty slice (not nil) is returned when
// no repos have been registered.
func (s *Service) ListRepos(_ context.Context) ([]Repo, error) {
	s.reposMu.RLock()
	out := make([]Repo, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, r)
	}
	s.reposMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Canonical() < out[j].Ref.Canonical() })
	return out, nil
}

// repoRoot looks up the root directory for the canonical key. Returns
// ErrNotFound if the repo is not registered.
func (s *Service) repoRoot(canonical string) (string, error) {
	r, ok := s.repoByCanonical(canonical)
	if !ok {
		return "", fmt.Errorf("%w: repo %q not registered on host", ErrNotFound, canonical)
	}
	return r.RootDir, nil
}

// ListBranchesByRepo is the canonical-scoped variant of ListBranches.
func (s *Service) ListBranchesByRepo(ctx context.Context, canonical string) ([]BranchInfo, error) {
	root, err := s.repoRoot(canonical)
	if err != nil {
		return nil, err
	}
	return s.listBranches(ctx, root)
}

// ResolveWorktreeByRepo is the canonical-scoped variant of ResolveWorktree.
func (s *Service) ResolveWorktreeByRepo(ctx context.Context, canonical, branch string) (WorktreeInfo, error) {
	root, err := s.repoRoot(canonical)
	if err != nil {
		return WorktreeInfo{}, err
	}
	return s.resolveWorktree(ctx, root, branch)
}

// RemoveWorktreeByRepo is the canonical-scoped variant of RemoveWorktree.
func (s *Service) RemoveWorktreeByRepo(ctx context.Context, canonical, branch string, force bool) error {
	root, err := s.repoRoot(canonical)
	if err != nil {
		return err
	}
	return s.removeWorktree(ctx, root, branch, force)
}

// MergeBranchByRepo is the canonical-scoped variant of MergeBranch.
func (s *Service) MergeBranchByRepo(ctx context.Context, canonical, branch, commitMessage string) (MergeResult, error) {
	root, err := s.repoRoot(canonical)
	if err != nil {
		return MergeResult{}, err
	}
	return s.mergeBranch(ctx, root, branch, commitMessage)
}
