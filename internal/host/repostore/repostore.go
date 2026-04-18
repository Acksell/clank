// Package repostore provides JSON-file persistence for the Host plane's
// repo registry.
//
// The host has historically held its (canonical → rootDir) registry only
// in memory, populated explicitly by the Hub via RegisterRepoOnHost on
// every restart. Step 5 of §7.8 (hub_host_refactor_code_review.md) moves
// this state to a tiny JSON file so a `clank-host` restart no longer
// loses the mapping.
//
// JSON, not SQLite: this is a (canonical → rootDir) map of <50 entries
// read once at startup and rewritten on register/delete. SQLite would be
// dead weight. Atomic durability is delegated to renameio (temp file +
// fsync + rename, with parent-dir fsync on Linux).
//
// Worktrees are intentionally NOT persisted: git itself is the source of
// truth (see git.ListWorktrees / git.FindWorktreeForBranch in
// internal/git). A persisted cache would only let us lie when the user
// mutates worktrees outside clank.
package repostore

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/acksell/clank/internal/host"
	"github.com/google/renameio/v2"
)

// fileFormatVersion is the on-disk schema version. Bump when the file
// layout changes incompatibly; load() switches on it to migrate forward.
const fileFormatVersion = 1

// Store persists the host's repo registry to a single JSON file.
// One Store per `clank-host` process. Tests use a per-test tmp file.
//
// The store keeps the full set in memory (`repos` map) and rewrites the
// whole file on every mutation. With ≤O(100) entries this is trivially
// fast and the simplest correct design.
type Store struct {
	path string

	mu    sync.Mutex
	repos map[string]storedRepo // key: GitRef.Canonical()
}

// storedRepo is the on-disk representation of a single registered repo.
// We persist only the canonical key + kind + root_dir; the original
// remote URL is not stored because GitRef.Canonical is idempotent
// (canonicalize(canonical) == canonical), so reconstruction reuses the
// canonical string.
type storedRepo struct {
	GitRef    string `json:"git_ref"`
	Kind      string `json:"kind"`
	RootDir   string `json:"root_dir"`
	CreatedAt int64  `json:"created_at"`
}

// fileEnvelope is the top-level on-disk layout. The Version field gives
// us a forward-compat migration hook without re-encoding the file.
type fileEnvelope struct {
	Version int          `json:"version"`
	Repos   []storedRepo `json:"repos"`
}

// Open opens (or creates) the store at path. A missing file is treated
// as an empty store; any other read/parse error is fatal so corrupted
// state surfaces loudly instead of being silently overwritten.
func Open(path string) (*Store, error) {
	s := &Store{
		path:  path,
		repos: make(map[string]storedRepo),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close is a no-op; provided for parity with stores that hold resources.
func (s *Store) Close() error { return nil }

// load reads the file into the in-memory map. A missing file is empty;
// a present but malformed file is an error (we refuse to start rather
// than silently zero out persisted state).
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return nil
	}
	var env fileEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	switch env.Version {
	case 0, 1:
		// v0 (no version field) and v1 share the same layout today.
	default:
		return fmt.Errorf("unsupported repostore file version %d in %s", env.Version, s.path)
	}
	for _, r := range env.Repos {
		if r.GitRef == "" {
			return fmt.Errorf("malformed entry in %s: empty git_ref", s.path)
		}
		s.repos[r.GitRef] = r
	}
	return nil
}

// flushLocked writes the in-memory map to disk atomically. Caller holds s.mu.
func (s *Store) flushLocked() error {
	out := make([]storedRepo, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, r)
	}
	// Stable order makes diffs reviewable when the file is hand-inspected.
	sort.Slice(out, func(i, j int) bool { return out[i].GitRef < out[j].GitRef })

	data, err := json.MarshalIndent(fileEnvelope{Version: fileFormatVersion, Repos: out}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := renameio.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}

// SaveRepo upserts a repo registration. Idempotent; created_at is
// preserved across re-saves of the same canonical so we don't reset the
// timestamp every time the host restarts and re-registers.
func (s *Store) SaveRepo(repo host.Repo) error {
	canonical := repo.Ref.Canonical()
	if canonical == "" {
		return fmt.Errorf("repostore: cannot save repo with empty canonical")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	createdAt := time.Now().Unix()
	if existing, ok := s.repos[canonical]; ok {
		createdAt = existing.CreatedAt
	}
	s.repos[canonical] = storedRepo{
		GitRef:    canonical,
		Kind:      string(repo.Ref.Kind),
		RootDir:   repo.RootDir,
		CreatedAt: createdAt,
	}
	if err := s.flushLocked(); err != nil {
		// Roll back the in-memory change so caller-visible state matches
		// disk. The Service layer rolls its own map on top of us, but
		// keeping our own state truthful too means tests that hit the
		// store directly (or future callers) don't observe a phantom save.
		delete(s.repos, canonical)
		return err
	}
	return nil
}

// ListRepos returns every persisted repo, sorted by canonical for
// deterministic output (matches host.Service.ListRepos behaviour).
func (s *Store) ListRepos() ([]host.Repo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]host.Repo, 0, len(s.repos))
	for _, r := range s.repos {
		ref, err := reconstructRef(r)
		if err != nil {
			return nil, err
		}
		out = append(out, host.Repo{Ref: ref, RootDir: r.RootDir})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Ref.Canonical() < out[j].Ref.Canonical()
	})
	return out, nil
}

// ForgetRepo removes a repo by canonical from the persisted registry.
// "Forget" rather than "Delete" because nothing is deleted on disk —
// we're just dropping our knowledge of the mapping. Missing entries are
// a no-op (not an error) so callers can issue idempotent forgets.
func (s *Store) ForgetRepo(canonical string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.repos[canonical]; !ok {
		return nil
	}
	prev := s.repos[canonical]
	delete(s.repos, canonical)
	if err := s.flushLocked(); err != nil {
		s.repos[canonical] = prev
		return err
	}
	return nil
}

// reconstructRef rebuilds a GitRef from a stored row. For remote-kind
// repos we stored only the canonical (no original URL); reusing
// canonical as URL is safe because GitRef.Canonical is idempotent (see
// TestGitRef_Canonical's idempotence subtest). For local-kind repos,
// root_dir is the URL by construction.
func reconstructRef(r storedRepo) (host.GitRef, error) {
	switch host.GitRefKind(r.Kind) {
	case host.GitRefRemote:
		return host.GitRef{Kind: host.GitRefRemote, URL: r.GitRef}, nil
	case host.GitRefLocal:
		return host.GitRef{Kind: host.GitRefLocal, Path: r.RootDir}, nil
	default:
		return host.GitRef{}, fmt.Errorf("unknown git ref kind %q for canonical %q", r.Kind, r.GitRef)
	}
}
