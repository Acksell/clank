package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/pkg/sync/checkpoint"
)

// RegisterImportedSession installs an exported opencode session into
// this host: invokes `opencode import <blobPath>`, then upserts a
// SessionInfo row into host.db keyed by entry.SessionID (the
// cross-machine stable host ULID) with worktreeID stamped onto its
// GitRef. Idempotent — re-running with the same input is safe
// (UpsertSession is upsert, `opencode import` additive-merges by
// message ID, see plan §E and TestOpenCodeImportSemantics).
//
// If opencode allocates a different external_id than the manifest's
// (would contradict TestOpenCodeImportSemantics path (a)), the
// imported external_id wins and a warning is logged.
func (s *Service) RegisterImportedSession(ctx context.Context, worktreeID string, entry checkpoint.SessionEntry, blobPath string) (agent.SessionInfo, error) {
	if s.sessionsStore == nil {
		return agent.SessionInfo{}, fmt.Errorf("register imported session: sessions store not configured")
	}
	if worktreeID == "" {
		return agent.SessionInfo{}, fmt.Errorf("register imported session: worktreeID is required")
	}
	if entry.SessionID == "" {
		return agent.SessionInfo{}, fmt.Errorf("register imported session: entry.SessionID is required")
	}
	if entry.Backend != agent.BackendOpenCode {
		return agent.SessionInfo{}, fmt.Errorf("register imported session: backend %q not supported in v1", entry.Backend)
	}

	// Rewrite info.directory in the blob from the SOURCE's local path
	// (e.g. /Users/foo/repo on a laptop) to the DESTINATION's local
	// path (workRoot/<worktreeID> on a sprite) before import. opencode
	// keys its "project" grouping on info.directory; without this
	// rewrite, imported sessions land under a synthetic "global"
	// project on the destination because the source path doesn't
	// resolve. This is a workaround for upstream behavior — once
	// opencode's import supports a target-directory or path-rebase
	// flag, this hack can be deleted in favor of passing the flag
	// through. See:
	//   - https://github.com/anomalyco/opencode/issues/15797
	//   - https://github.com/anomalyco/opencode/pull/15826
	rewrittenPath, err := s.rewriteImportBlobDir(blobPath, worktreeID)
	if err != nil {
		return agent.SessionInfo{}, fmt.Errorf("register imported session %s: rewrite blob: %w", entry.SessionID, err)
	}
	defer os.Remove(rewrittenPath)

	// projectDir on the subprocess is intentionally empty: `opencode
	// import` reads/writes its own storage (HOME-relative) and ignores
	// cwd, and entry.ProjectDir is the SOURCE host's local path —
	// which doesn't exist on a destination host (chdir would fail
	// before opencode even runs). Verified by
	// TestOpenCodeImportSemantics.
	importedExternalID, err := agent.OpenCodeImportSession(ctx, "", rewrittenPath)
	if err != nil {
		return agent.SessionInfo{}, fmt.Errorf("register imported session %s: %w", entry.SessionID, err)
	}
	if entry.ExternalID != "" && importedExternalID != entry.ExternalID {
		s.log.Printf("register imported session %s: opencode allocated fresh external_id %q (manifest had %q); using imported", entry.SessionID, importedExternalID, entry.ExternalID)
	}

	now := time.Now()
	// Status on a freshly-imported session is always idle — the abort
	// happened on the source, and opencode import doesn't preserve
	// in-flight tool calls. The manifest's WasBusy flag stays as a
	// hint for the (future) auto-resume feature.
	info := agent.SessionInfo{
		ID:         entry.SessionID,
		ExternalID: importedExternalID,
		Backend:    entry.Backend,
		Status:     agent.StatusIdle,
		Hostname:   s.id,
		GitRef: agent.GitRef{
			WorktreeID:     worktreeID,
			WorktreeBranch: entry.WorktreeBranch,
		},
		Prompt:    entry.Prompt,
		Title:     entry.Title,
		TicketID:  entry.TicketID,
		Agent:     entry.Agent,
		CreatedAt: nonZeroOr(entry.CreatedAt, now),
		UpdatedAt: now,
	}

	if err := s.sessionsStore.UpsertSession(ctx, info); err != nil {
		return agent.SessionInfo{}, fmt.Errorf("register imported session %s: upsert: %w", entry.SessionID, err)
	}
	return info, nil
}

func nonZeroOr(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

// rewriteImportBlobDir reads an opencode export blob, replaces
// info.directory with the destination's local worktree path, clears
// info.projectID so opencode rederives it on import, and writes the
// result to a sibling temp file. Returns the new path. Caller owns
// cleanup (os.Remove).
//
// Workaround for opencode not rebasing paths on import — see
// https://github.com/anomalyco/opencode/issues/15797 and
// https://github.com/anomalyco/opencode/pull/15826. Once that lands
// upstream and we bump the opencode pin, this helper can be removed
// and OpenCodeImportSession called on the original blobPath again.
//
// The function modifies only top-level info.{directory,projectID};
// every other field (id, slug, title, messages, parts, ...) is
// preserved verbatim. JSON parsing uses map[string]any so we don't
// have to track opencode's full schema.
func (s *Service) rewriteImportBlobDir(srcPath, worktreeID string) (string, error) {
	destDir, err := workRootDir()
	if err != nil {
		return "", fmt.Errorf("resolve work root: %w", err)
	}
	destDir = filepath.Join(destDir, worktreeID)

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read blob: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("parse blob: %w", err)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("blob missing top-level info object")
	}
	info["directory"] = destDir
	// Force opencode to rederive projectID from the new directory.
	// Leaving the source's value would peg the imported session to a
	// project hash that doesn't exist on this host, defeating the
	// rewrite. Empty string is honored as "rederive" — verified via
	// TestRegisterImportedSession_RewritesDirectoryToDestination.
	info["projectID"] = ""
	doc["info"] = info

	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal rewritten blob: %w", err)
	}
	dstPath := srcPath + ".rewritten.json"
	if err := os.WriteFile(dstPath, out, 0o600); err != nil {
		return "", fmt.Errorf("write rewritten blob: %w", err)
	}
	return dstPath, nil
}
