package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	"github.com/acksell/clank/internal/host/store"
)

// TestExportSessions_SkipsClaude: a claude-code session is enumerated
// but never exported (no opencode binary call). It shows up in the
// result.Skipped slice with a clear reason.
func TestExportSessions_SkipsClaude(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "host.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now()
	const worktreeID = "wt-skip-claude"
	claudeSession := agent.SessionInfo{
		ID:        "01HCLAUDE0000000000000000000",
		Backend:   agent.BackendClaudeCode,
		Status:    agent.StatusIdle,
		GitRef:    agent.GitRef{WorktreeID: worktreeID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.UpsertSession(context.Background(), claudeSession); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var logBuf bytes.Buffer
	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		SessionsStore: st,
		Log:           log.New(&logBuf, "", 0),
	})
	t.Cleanup(svc.Shutdown)

	res, err := svc.ExportSessions(context.Background(), worktreeID, "ck-1")
	if err != nil {
		t.Fatalf("ExportSessions: %v", err)
	}
	t.Cleanup(res.Cleanup)

	if len(res.Entries) != 0 {
		t.Errorf("expected 0 manifest entries (claude skipped), got %d", len(res.Entries))
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("expected 1 skipped, got %d", len(res.Skipped))
	}
	if res.Skipped[0].SessionID != claudeSession.ID {
		t.Errorf("skipped.SessionID = %q, want %q", res.Skipped[0].SessionID, claudeSession.ID)
	}
	if res.Skipped[0].Backend != agent.BackendClaudeCode {
		t.Errorf("skipped.Backend = %q, want claude-code", res.Skipped[0].Backend)
	}
}

// TestExportSessions_EmptyWorktree: no sessions in the worktree
// returns an empty result, not an error.
func TestExportSessions_EmptyWorktree(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "host.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		SessionsStore: st,
	})
	t.Cleanup(svc.Shutdown)

	res, err := svc.ExportSessions(context.Background(), "wt-nothing", "ck-empty")
	if err != nil {
		t.Fatalf("ExportSessions: %v", err)
	}
	t.Cleanup(res.Cleanup)

	if len(res.Entries) != 0 {
		t.Errorf("want 0 entries, got %d", len(res.Entries))
	}
	if len(res.Skipped) != 0 {
		t.Errorf("want 0 skipped, got %d", len(res.Skipped))
	}
}

// TestExportSessions_RejectsEmptyArgs: input-validation guards.
func TestExportSessions_RejectsEmptyArgs(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "host.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		SessionsStore: st,
	})
	t.Cleanup(svc.Shutdown)

	if _, err := svc.ExportSessions(context.Background(), "", "ck"); err == nil {
		t.Errorf("expected error for empty worktreeID")
	}
	if _, err := svc.ExportSessions(context.Background(), "wt", ""); err == nil {
		t.Errorf("expected error for empty checkpointID")
	}
}

// TestExportSessions_OpenCodeHappyPath integration-tests the full
// quiesce-and-export flow against a real opencode binary. Skips if
// opencode is not on $PATH.
//
// Per CLAUDE.md "NEVER mock dependencies", this exercises the real
// CLI in an isolated HOME with a synthetic seed blob — no LLM round
// trip required.
func TestExportSessions_OpenCodeHappyPath(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode binary not on $PATH")
	}

	// Isolated opencode HOME so we don't touch the user's storage.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local/share/opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config/opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local/share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Seed an opencode session via synthetic blob import.
	const externalID = "ses_exporttesthappypath00000000"
	seed := buildSyntheticOCBlob(externalID, "msg_exporttestseed000000000000", "build", "hello")
	seedPath := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(seedPath, seed, 0o644); err != nil {
		t.Fatal(err)
	}
	importedID, err := agent.OpenCodeImportSession(context.Background(), "", seedPath)
	if err != nil {
		t.Fatalf("seed import: %v", err)
	}
	if importedID != externalID {
		t.Fatalf("seed import returned %q, want %q", importedID, externalID)
	}

	// Register the session in host.db so ListSessionsByWorktree picks it up.
	dbPath := filepath.Join(t.TempDir(), "host.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	const sessULID = "01HEXPORTHAPPY0000000000000000"
	const worktreeID = "wt-export-happy"
	now := time.Now()
	if err := st.UpsertSession(context.Background(), agent.SessionInfo{
		ID:         sessULID,
		ExternalID: externalID,
		Backend:    agent.BackendOpenCode,
		Status:     agent.StatusIdle,
		GitRef:     agent.GitRef{WorktreeID: worktreeID},
		Title:      "export-happy",
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		SessionsStore: st,
	})
	t.Cleanup(svc.Shutdown)

	res, err := svc.ExportSessions(context.Background(), worktreeID, "ck-happy")
	if err != nil {
		t.Fatalf("ExportSessions: %v", err)
	}
	t.Cleanup(res.Cleanup)

	if len(res.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(res.Entries))
	}
	if len(res.Skipped) != 0 {
		t.Errorf("want 0 skipped, got %d", len(res.Skipped))
	}
	e := res.Entries[0]
	if e.SessionID != sessULID {
		t.Errorf("entry.SessionID = %q, want %q", e.SessionID, sessULID)
	}
	if e.ExternalID != externalID {
		t.Errorf("entry.ExternalID = %q, want %q", e.ExternalID, externalID)
	}
	if e.Backend != agent.BackendOpenCode {
		t.Errorf("entry.Backend = %q, want opencode", e.Backend)
	}
	if e.BlobKey != "sessions/"+sessULID+".json" {
		t.Errorf("entry.BlobKey = %q, want sessions/%s.json", e.BlobKey, sessULID)
	}
	if e.Bytes <= 0 {
		t.Errorf("entry.Bytes = %d, want positive", e.Bytes)
	}
	if e.WasBusy {
		t.Errorf("entry.WasBusy = true for an idle session")
	}

	blobPath, ok := res.BlobPaths[sessULID]
	if !ok {
		t.Fatalf("BlobPaths missing %s", sessULID)
	}
	blobData, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	var parsed struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
	}
	if err := json.Unmarshal(blobData, &parsed); err != nil {
		t.Fatalf("parse blob: %v", err)
	}
	if parsed.Info.ID != externalID {
		t.Errorf("blob info.id = %q, want %q", parsed.Info.ID, externalID)
	}
}

// TestExportSessions_IgnoresStaleLocalPath is the regression test
// for the chdir-on-export bug observed during pull --migrate: a
// session row whose host.db.project_dir is the SOURCE host's local
// path (because the row was imported earlier from that source) must
// still export cleanly on the DESTINATION. Without the fix, the
// sprite's Service.ExportSessions would chdir into the laptop's
// /Users/... path and fail before opencode could run.
//
// Mirrors TestRegisterImportedSession_IgnoresSourceProjectDir on
// the import side. Both fixes together close the same chdir-on-
// cross-machine-path footgun in both directions.
func TestExportSessions_IgnoresStaleLocalPath(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode binary not on $PATH")
	}

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".local/share/opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".config/opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local/share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// Seed an opencode session.
	const externalID = "ses_staleLocalPathRegress0000"
	blob := buildSyntheticOCBlob(externalID, "msg_staleseed00000000000000000", "build", "hello")
	seedPath := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(seedPath, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OpenCodeImportSession(context.Background(), "", seedPath); err != nil {
		t.Fatalf("seed import: %v", err)
	}

	// Stamp the host.db row with a SOURCE-style LocalPath that
	// definitely doesn't exist on this destination.
	const sessULID = "01HSTALELOCALPATHREGRESS0000"
	const worktreeID = "wt-stale-localpath"
	dbPath := filepath.Join(t.TempDir(), "host.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	if err := st.UpsertSession(context.Background(), agent.SessionInfo{
		ID:         sessULID,
		ExternalID: externalID,
		Backend:    agent.BackendOpenCode,
		Status:     agent.StatusIdle,
		GitRef: agent.GitRef{
			WorktreeID: worktreeID,
			LocalPath:  "/path/that/exists/only/on/another/machine",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
		SessionsStore: st,
	})
	t.Cleanup(svc.Shutdown)

	res, err := svc.ExportSessions(context.Background(), worktreeID, "ck-stale")
	if err != nil {
		t.Fatalf("ExportSessions with stale LocalPath: %v", err)
	}
	t.Cleanup(res.Cleanup)
	if len(res.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(res.Entries))
	}
	if res.Entries[0].SessionID != sessULID {
		t.Errorf("entry.SessionID = %q, want %q", res.Entries[0].SessionID, sessULID)
	}
}

// buildSyntheticOCBlob returns a minimal valid opencode session
// export JSON with one user message. Mirrors the schema seen on
// real exports of opencode 1.3.15.
func buildSyntheticOCBlob(sessID, msgID, agentSlug, text string) []byte {
	return buildOCBlobWithDir(sessID, msgID, agentSlug, text, "/tmp/clank-diag-test", "0000000000000000000000000000000000000000")
}

// buildOCBlobWithDir is the parameterized form of buildSyntheticOCBlob
// used by tests that need to assert specific directory / projectID
// values in the export (e.g. the directory-rewrite shim regression).
func buildOCBlobWithDir(sessID, msgID, agentSlug, text, directory, projectID string) []byte {
	body := map[string]any{
		"info": map[string]any{
			"id":        sessID,
			"slug":      "diag-slug",
			"projectID": projectID,
			"directory": directory,
			"title":     "diag",
			"version":   "1.3.15",
			"summary":   map[string]any{"additions": 0, "deletions": 0, "files": 0},
			"time":      map[string]any{"created": 1000, "updated": 1000},
		},
		"messages": []map[string]any{
			{
				"info": map[string]any{
					"id":        msgID,
					"sessionID": sessID,
					"role":      "user",
					"agent":     agentSlug,
					"model":     map[string]any{"providerID": "diag", "modelID": "diag"},
					"summary":   map[string]any{"diffs": []any{}},
					"time":      map[string]any{"created": 1000},
				},
				"parts": []map[string]any{
					{
						"type":      "text",
						"text":      text,
						"id":        "prt_" + msgID,
						"sessionID": sessID,
						"messageID": msgID,
					},
				},
			},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return b
}
