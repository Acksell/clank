package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	clanksync "github.com/acksell/clank/pkg/sync"
)

// migrationTokenTTL bounds how long a materialize → commit pair can
// straddle. 10 minutes is generous enough for a slow apply on a big
// worktree and short enough that a stale token doesn't grant indefinite
// commit authority.
const migrationTokenTTL = 10 * time.Minute

// materializeResponse is the body returned by
// POST /v1/migrate/worktrees/{id}/materialize. The CLI feeds these
// fields back into the commit call after downloading + applying the
// checkpoint locally.
type materializeResponse struct {
	CheckpointID    string `json:"checkpoint_id"`
	HeadCommit      string `json:"head_commit"`
	ManifestURL     string `json:"manifest_url"`
	HeadCommitURL   string `json:"head_commit_url"`
	IncrementalURL  string `json:"incremental_url"`
	MigrationToken  string `json:"migration_token"`
	MigrationExpiry int64  `json:"migration_expiry"` // unix seconds
}

// commitRequest is the body for /commit. The migration token gates this
// call: it proves the laptop just successfully materialized the named
// checkpoint and is calling commit on the same migration attempt.
type commitRequest struct {
	CheckpointID   string `json:"checkpoint_id"`
	MigrationToken string `json:"migration_token"`
}

// handleMigrateMaterialize asks the sprite to checkpoint its current
// state and returns presigned GET URLs the laptop can download from.
// No ownership change yet — that happens on the matching commit call.
//
// The migration token returned here proves identity continuity between
// the two phases: only the laptop that called materialize can commit
// (mismatched device_id rejects), and only for the same checkpoint
// (a newer checkpoint landing in between fails commit).
func (g *Gateway) handleMigrateMaterialize(w http.ResponseWriter, r *http.Request) {
	if _, err := g.cfg.Auth.Verify(r); err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clank"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if g.cfg.Sync == nil {
		http.Error(w, "migration not configured (Sync unset)", http.StatusServiceUnavailable)
		return
	}
	if g.cfg.SyncPublicURL == "" {
		http.Error(w, "migration not configured (SyncPublicURL unset)", http.StatusServiceUnavailable)
		return
	}
	userID := g.cfg.ResolveUserID(r)
	if userID == "" {
		http.Error(w, "no user identity", http.StatusUnauthorized)
		return
	}
	deviceID := r.Header.Get(HeaderDeviceID)
	if deviceID == "" {
		http.Error(w, "missing "+HeaderDeviceID, http.StatusBadRequest)
		return
	}
	worktreeID := r.PathValue("id")
	if worktreeID == "" {
		http.Error(w, "worktree id missing", http.StatusBadRequest)
		return
	}

	wt, err := g.cfg.Sync.GetWorktree(r.Context(), userID, worktreeID)
	if err != nil {
		syncErrToHTTP(w, "read worktree", err)
		return
	}
	if wt.OwnerKind != clanksync.OwnerKindRemote {
		http.Error(w, "worktree is not currently sprite-owned (nothing to materialize)", http.StatusConflict)
		return
	}

	hostRef, err := g.cfg.Provisioner.EnsureHost(r.Context(), userID)
	if err != nil {
		g.log.Printf("gateway materialize: EnsureHost(%s): %v", userID, err)
		http.Error(w, "ensure sprite: "+err.Error(), http.StatusBadGateway)
		return
	}

	cli := &http.Client{Timeout: 5 * time.Minute}
	checkpointID, headCommit, err := triggerSpriteCheckpoint(r.Context(), cli, hostRef.URL, hostRef.Transport, hostRef.AuthToken, spriteCheckpointParams{
		Repo:          wt.ID,
		WorktreeID:    wt.ID,
		HostID:        hostRef.HostID,
		SyncBaseURL:   g.cfg.SyncPublicURL,
		SyncAuthToken: g.cfg.SyncAuthToken,
	})
	if err != nil {
		g.log.Printf("gateway materialize: trigger sprite checkpoint: %v", err)
		http.Error(w, "sprite checkpoint: "+err.Error(), http.StatusBadGateway)
		return
	}

	urls, err := g.cfg.Sync.DownloadCheckpointURLs(r.Context(), userID, checkpointID)
	if err != nil {
		syncErrToHTTP(w, "download checkpoint URLs", err)
		return
	}

	expiry := time.Now().Add(migrationTokenTTL).Unix()
	token := g.signMigrationToken(wt.ID, checkpointID, deviceID, expiry)

	writeJSON(w, http.StatusOK, materializeResponse{
		CheckpointID:    checkpointID,
		HeadCommit:      headCommit,
		ManifestURL:     urls.ManifestGetURL,
		HeadCommitURL:   urls.HeadCommitGetURL,
		IncrementalURL:  urls.IncrementalURL,
		MigrationToken:  token,
		MigrationExpiry: expiry,
	})
}

// handleMigrateCommit verifies the migration token, double-checks that
// the sync server's latest_synced_checkpoint still points at the one
// the laptop just applied (i.e. no race where the sprite checkpointed
// again in between), and atomically transfers ownership to the laptop.
func (g *Gateway) handleMigrateCommit(w http.ResponseWriter, r *http.Request) {
	if _, err := g.cfg.Auth.Verify(r); err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clank"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if g.cfg.Sync == nil {
		http.Error(w, "migration not configured (Sync unset)", http.StatusServiceUnavailable)
		return
	}
	userID := g.cfg.ResolveUserID(r)
	if userID == "" {
		http.Error(w, "no user identity", http.StatusUnauthorized)
		return
	}
	deviceID := r.Header.Get(HeaderDeviceID)
	if deviceID == "" {
		http.Error(w, "missing "+HeaderDeviceID, http.StatusBadRequest)
		return
	}
	worktreeID := r.PathValue("id")
	if worktreeID == "" {
		http.Error(w, "worktree id missing", http.StatusBadRequest)
		return
	}

	var req commitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.CheckpointID == "" || req.MigrationToken == "" {
		http.Error(w, "checkpoint_id and migration_token are required", http.StatusBadRequest)
		return
	}
	if !g.verifyMigrationToken(req.MigrationToken, worktreeID, req.CheckpointID, deviceID) {
		http.Error(w, "invalid or expired migration_token", http.StatusForbidden)
		return
	}

	wt, err := g.cfg.Sync.GetWorktree(r.Context(), userID, worktreeID)
	if err != nil {
		syncErrToHTTP(w, "read worktree", err)
		return
	}
	if wt.LatestSyncedCheckpoint != req.CheckpointID {
		http.Error(w, "newer checkpoint exists; re-run materialize", http.StatusConflict)
		return
	}
	if wt.OwnerKind != clanksync.OwnerKindRemote {
		http.Error(w, "worktree is no longer sprite-owned", http.StatusConflict)
		return
	}

	updated, err := g.cfg.Sync.TransferOwnership(r.Context(), userID, wt.ID, clanksync.OwnerKindLocal, deviceID, wt.OwnerID)
	if err != nil {
		syncErrToHTTP(w, "transfer ownership", err)
		return
	}

	writeJSON(w, http.StatusOK, migrateResponse{
		WorktreeID:   updated.ID,
		NewOwnerKind: string(updated.OwnerKind),
		NewOwnerID:   updated.OwnerID,
		CheckpointID: req.CheckpointID,
	})
}

// spriteCheckpointParams mirrors the JSON body of the sprite's
// /sync/checkpoint endpoint (internal/host/mux/sync.go's
// createCheckpointRequest). Kept here as a separate struct to avoid an
// import cycle between gateway and host packages.
type spriteCheckpointParams struct {
	Repo          string `json:"repo"`
	WorktreeID    string `json:"worktree_id"`
	HostID        string `json:"host_id"`
	SyncBaseURL   string `json:"sync_base_url"`
	SyncAuthToken string `json:"sync_auth_token"`
}

// triggerSpriteCheckpoint POSTs to the sprite's /sync/checkpoint and
// returns the canonical checkpoint ID + head commit SHA. Uses the
// HostRef's transport for the sprite-side bearer.
func triggerSpriteCheckpoint(ctx context.Context, baseClient *http.Client, spriteURL string, transport http.RoundTripper, authToken string, params spriteCheckpointParams) (checkpointID, headCommit string, err error) {
	body, err := json.Marshal(params)
	if err != nil {
		return "", "", fmt.Errorf("marshal: %w", err)
	}
	target := strings.TrimRight(spriteURL, "/") + "/sync/checkpoint"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	cli := baseClient
	if transport != nil {
		cli = &http.Client{Transport: transport, Timeout: baseClient.Timeout}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("sprite checkpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		CheckpointID string `json:"checkpoint_id"`
		HeadCommit   string `json:"head_commit"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", "", fmt.Errorf("decode: %w", err)
	}
	if parsed.CheckpointID == "" {
		return "", "", fmt.Errorf("sprite returned empty checkpoint_id")
	}
	return parsed.CheckpointID, parsed.HeadCommit, nil
}

// signMigrationToken issues an HMAC-SHA256 over
// "<worktreeID>:<checkpointID>:<deviceID>:<expiryUnix>" using the
// gateway's migrationKey. The expiry is encoded in the token itself
// so verification doesn't need extra state.
func (g *Gateway) signMigrationToken(worktreeID, checkpointID, deviceID string, expiry int64) string {
	payload := fmt.Sprintf("%s:%s:%s:%d", worktreeID, checkpointID, deviceID, expiry)
	mac := hmac.New(sha256.New, g.migrationKey)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return strconv.FormatInt(expiry, 10) + "." + sig
}

// verifyMigrationToken returns true iff sig matches the recomputed HMAC
// for the given fields and the embedded expiry is in the future.
func (g *Gateway) verifyMigrationToken(token, worktreeID, checkpointID, deviceID string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().Unix() > expiry {
		return false
	}
	payload := fmt.Sprintf("%s:%s:%s:%d", worktreeID, checkpointID, deviceID, expiry)
	mac := hmac.New(sha256.New, g.migrationKey)
	mac.Write([]byte(payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(parts[1]))
}
