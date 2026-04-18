package repostore_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/host/repostore"
)

// openTmp opens a fresh JSON-backed store in t.TempDir() and registers
// cleanup. Real filesystem — no mocks — per AGENTS.md.
func openTmp(t *testing.T) *repostore.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host.json")
	s, err := repostore.Open(path)
	if err != nil {
		t.Fatalf("repostore.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_SaveAndList_Remote(t *testing.T) {
	t.Parallel()
	s := openTmp(t)

	repo := host.Repo{
		Ref:     host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"},
		RootDir: "/tmp/clank",
	}
	if err := s.SaveRepo(repo); err != nil {
		t.Fatalf("SaveRepo: %v", err)
	}

	got, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].RootDir != repo.RootDir {
		t.Errorf("RootDir = %q, want %q", got[0].RootDir, repo.RootDir)
	}
	// The reconstructed GitRef must canonicalize to the same value the
	// caller used as the primary key — round-trip safety relied on by
	// the host.Service preload path.
	if got[0].Ref.Canonical() != repo.Ref.Canonical() {
		t.Errorf("Canonical = %q, want %q", got[0].Ref.Canonical(), repo.Ref.Canonical())
	}
	if got[0].Ref.Kind != host.GitRefRemote {
		t.Errorf("Kind = %q, want %q", got[0].Ref.Kind, host.GitRefRemote)
	}
}

func TestStore_SaveAndList_Local(t *testing.T) {
	t.Parallel()
	s := openTmp(t)

	repo := host.Repo{
		Ref:     host.GitRef{Kind: host.GitRefLocal, Path: "/Users/me/work/proj"},
		RootDir: "/Users/me/work/proj",
	}
	if err := s.SaveRepo(repo); err != nil {
		t.Fatalf("SaveRepo: %v", err)
	}
	got, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(got) != 1 || got[0].Ref.Kind != host.GitRefLocal {
		t.Fatalf("got = %+v", got)
	}
	if got[0].Ref.Path != repo.RootDir {
		t.Errorf("Path = %q, want %q", got[0].Ref.Path, repo.RootDir)
	}
}

func TestStore_Upsert_LastWriterWins(t *testing.T) {
	t.Parallel()
	s := openTmp(t)

	ref := host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}
	if err := s.SaveRepo(host.Repo{Ref: ref, RootDir: "/old/path"}); err != nil {
		t.Fatalf("first SaveRepo: %v", err)
	}
	if err := s.SaveRepo(host.Repo{Ref: ref, RootDir: "/new/path"}); err != nil {
		t.Fatalf("second SaveRepo: %v", err)
	}
	got, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (upsert should not duplicate)", len(got))
	}
	if got[0].RootDir != "/new/path" {
		t.Errorf("RootDir = %q, want /new/path (last writer wins)", got[0].RootDir)
	}
}

func TestStore_Forget(t *testing.T) {
	t.Parallel()
	s := openTmp(t)

	ref := host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"}
	if err := s.SaveRepo(host.Repo{Ref: ref, RootDir: "/tmp"}); err != nil {
		t.Fatalf("SaveRepo: %v", err)
	}
	if err := s.ForgetRepo(ref.Canonical()); err != nil {
		t.Fatalf("ForgetRepo: %v", err)
	}
	got, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestStore_PersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host.json")
	s1, err := repostore.Open(path)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	if err := s1.SaveRepo(host.Repo{
		Ref:     host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"},
		RootDir: "/tmp/x",
	}); err != nil {
		t.Fatalf("SaveRepo: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	s2, err := repostore.Open(path)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer s2.Close()
	got, err := s2.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(got) != 1 || got[0].RootDir != "/tmp/x" {
		t.Fatalf("got = %+v, want one row with RootDir=/tmp/x", got)
	}
}

func TestStore_ListSortedByCanonical(t *testing.T) {
	t.Parallel()
	s := openTmp(t)

	// Insert in reverse-sorted order to verify ListRepos sorts.
	for _, url := range []string{
		"git@github.com:zeta/repo.git",
		"git@github.com:alpha/repo.git",
		"git@github.com:mu/repo.git",
	} {
		if err := s.SaveRepo(host.Repo{
			Ref:     host.GitRef{Kind: host.GitRefRemote, URL: url},
			RootDir: "/tmp/" + url,
		}); err != nil {
			t.Fatalf("SaveRepo %q: %v", url, err)
		}
	}
	got, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Ref.Canonical() > got[i].Ref.Canonical() {
			t.Errorf("not sorted: %q > %q", got[i-1].Ref.Canonical(), got[i].Ref.Canonical())
		}
	}
}

func TestStore_SaveRepo_RejectsEmptyCanonical(t *testing.T) {
	t.Parallel()
	s := openTmp(t)

	// A zero-value GitRef has Canonical == "" and would otherwise insert
	// an unindexed empty-string row that breaks ListRepos reconstruction.
	if err := s.SaveRepo(host.Repo{}); err == nil {
		t.Error("expected error for empty canonical, got nil")
	}
}

// TestStore_OpenMissingFileIsEmpty verifies that pointing Open at a
// non-existent path is not an error: the file is created lazily on the
// first SaveRepo. This is the cold-start path on a fresh host.
func TestStore_OpenMissingFileIsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	s, err := repostore.Open(path)
	if err != nil {
		t.Fatalf("Open missing file: %v", err)
	}
	defer s.Close()
	got, err := s.ListRepos()
	if err != nil || len(got) != 0 {
		t.Fatalf("ListRepos = %+v err=%v, want empty no-error", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Open should not create the file before first write; stat err=%v", err)
	}
}

// TestStore_OpenMalformedFileIsError verifies we refuse to start on a
// corrupted file rather than silently reset to empty (which would lose
// every persisted registration). Operators get a loud failure.
func TestStore_OpenMalformedFileIsError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repostore.Open(path); err == nil {
		t.Error("Open on malformed file should error, got nil")
	}
}

// TestStore_FileShape pins the on-disk layout: a versioned envelope
// with sorted entries. Future format changes must update this test
// (and bump fileFormatVersion). Documenting the shape in a test makes
// the file format part of the contract.
func TestStore_FileShape(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host.json")
	s, err := repostore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveRepo(host.Repo{
		Ref:     host.GitRef{Kind: host.GitRefRemote, URL: "git@github.com:acksell/clank.git"},
		RootDir: "/tmp/clank",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Version int `json:"version"`
		Repos   []struct {
			GitRef    string `json:"git_ref"`
			Kind      string `json:"kind"`
			RootDir   string `json:"root_dir"`
			CreatedAt int64  `json:"created_at"`
		} `json:"repos"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("file is not valid JSON: %v\n%s", err, data)
	}
	if env.Version != 1 {
		t.Errorf("Version = %d, want 1", env.Version)
	}
	if len(env.Repos) != 1 {
		t.Fatalf("len(Repos) = %d, want 1", len(env.Repos))
	}
	r := env.Repos[0]
	if r.GitRef != "github.com/acksell/clank" || r.Kind != "remote" || r.RootDir != "/tmp/clank" || r.CreatedAt == 0 {
		t.Errorf("entry = %+v", r)
	}
}

// TestStore_ForgetIdempotent verifies that forgetting a missing canonical
// is a no-op, not an error. Callers can issue safe retries.
func TestStore_ForgetIdempotent(t *testing.T) {
	t.Parallel()
	s := openTmp(t)
	if err := s.ForgetRepo("github.com/never/registered"); err != nil {
		t.Errorf("ForgetRepo on missing canonical should be no-op, got %v", err)
	}
}
