package host

import (
	"context"
	"fmt"
	"sort"
)

// Repo registry. Phase 3B introduces a RepoID → RootDir map so the host
// can serve `/repos/{id}/...` routes without callers having to send
// filesystem paths over the wire. Auto-populated by CreateSession (when
// the request carries both RepoRemoteURL and ProjectDir) and by the
// explicit RegisterRepo entry point used from tests / future Hub
// startup-time backfill.

// RegisterRepo records a (RepoID → rootDir) mapping derived from ref and
// returns the resulting Repo. Idempotent: re-registering the same RepoID
// updates the rootDir (last writer wins) so a moved checkout is picked
// up automatically.
func (s *Service) RegisterRepo(ref RepoRef, rootDir string) (Repo, error) {
	if rootDir == "" {
		return Repo{}, fmt.Errorf("root_dir is required")
	}
	if err := ref.Validate(); err != nil {
		return Repo{}, fmt.Errorf("invalid repo ref: %w", err)
	}
	id, err := ref.ID()
	if err != nil {
		return Repo{}, err
	}
	repo := Repo{ID: id, Ref: ref, RootDir: rootDir}
	s.reposMu.Lock()
	s.repos[id] = repo
	s.reposMu.Unlock()
	return repo, nil
}

// Repo returns the Repo registered under id, or false if there is no
// such repo. Callers that need a path use Repo.RootDir.
func (s *Service) Repo(id RepoID) (Repo, bool) {
	s.reposMu.RLock()
	r, ok := s.repos[id]
	s.reposMu.RUnlock()
	return r, ok
}

// ListRepos returns all repos this host has been told about, sorted by
// ID for stable output. An empty slice (not nil) is returned when no
// repos have been registered.
func (s *Service) ListRepos(_ context.Context) ([]Repo, error) {
	s.reposMu.RLock()
	out := make([]Repo, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, r)
	}
	s.reposMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// repoRoot looks up the root directory for id. Returns ErrNotFound if
// the repo is not registered.
func (s *Service) repoRoot(id RepoID) (string, error) {
	r, ok := s.Repo(id)
	if !ok {
		return "", fmt.Errorf("%w: repo %q not registered on host", ErrNotFound, id)
	}
	return r.RootDir, nil
}

// ListBranchesByRepo is the RepoID-scoped variant of ListBranches.
func (s *Service) ListBranchesByRepo(ctx context.Context, id RepoID) ([]BranchInfo, error) {
	root, err := s.repoRoot(id)
	if err != nil {
		return nil, err
	}
	return s.ListBranches(ctx, root)
}

// ResolveWorktreeByRepo is the RepoID-scoped variant of ResolveWorktree.
func (s *Service) ResolveWorktreeByRepo(ctx context.Context, id RepoID, branch string) (WorktreeInfo, error) {
	root, err := s.repoRoot(id)
	if err != nil {
		return WorktreeInfo{}, err
	}
	return s.ResolveWorktree(ctx, root, branch)
}

// RemoveWorktreeByRepo is the RepoID-scoped variant of RemoveWorktree.
func (s *Service) RemoveWorktreeByRepo(ctx context.Context, id RepoID, branch string, force bool) error {
	root, err := s.repoRoot(id)
	if err != nil {
		return err
	}
	return s.RemoveWorktree(ctx, root, branch, force)
}

// MergeBranchByRepo is the RepoID-scoped variant of MergeBranch.
func (s *Service) MergeBranchByRepo(ctx context.Context, id RepoID, branch, commitMessage string) (MergeResult, error) {
	root, err := s.repoRoot(id)
	if err != nil {
		return MergeResult{}, err
	}
	return s.MergeBranch(ctx, root, branch, commitMessage)
}
