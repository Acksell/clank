package sync

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/acksell/clank/pkg/sync/storage"
	"github.com/oklog/ulid/v2"
)

// registerWorktreeRequest is the body of POST /v1/worktrees.
type registerWorktreeRequest struct {
	DisplayName string `json:"display_name"`
	// DeviceID identifies the laptop registering this worktree. P2 will
	// move this into JWT claims; for MVP with PermissiveAuth the client
	// supplies it directly.
	DeviceID string `json:"device_id"`
}

type worktreeResponse struct {
	ID                     string    `json:"id"`
	UserID                 string    `json:"user_id"`
	DisplayName            string    `json:"display_name"`
	OwnerKind              OwnerKind `json:"owner_kind"`
	OwnerID                string    `json:"owner_id"`
	LatestSyncedCheckpoint string    `json:"latest_synced_checkpoint,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func (s *Server) handleRegisterWorktree(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.callerOrUnauthorized(w, r)
	if !ok {
		return
	}

	var req registerWorktreeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.DisplayName == "" {
		http.Error(w, "display_name is required", http.StatusBadRequest)
		return
	}
	if req.DeviceID == "" {
		http.Error(w, "device_id is required", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	wt := Worktree{
		ID:          newULID(),
		UserID:      caller.userID,
		DisplayName: req.DisplayName,
		OwnerKind:   OwnerKindLaptop,
		OwnerID:     req.DeviceID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.cfg.Store.InsertWorktree(r.Context(), wt); err != nil {
		s.log.Printf("sync: insert worktree: %v", err)
		http.Error(w, "insert worktree", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, worktreeToResponse(wt))
}

// createCheckpointRequest is the body of POST /v1/checkpoints. Field
// shapes match checkpoint.Manifest minus the server-assigned ID and
// CreatedAt/By.
type createCheckpointRequest struct {
	WorktreeID        string `json:"worktree_id"`
	HeadCommit        string `json:"head_commit"`
	HeadRef           string `json:"head_ref"`
	IndexTree         string `json:"index_tree"`
	WorktreeTree      string `json:"worktree_tree"`
	IncrementalCommit string `json:"incremental_commit"`
	DeviceID          string `json:"device_id"`
}

type createCheckpointResponse struct {
	CheckpointID     string    `json:"checkpoint_id"`
	HeadCommitPutURL string    `json:"head_commit_put_url"`
	IncrementalURL   string    `json:"incremental_put_url"`
	ManifestPutURL   string    `json:"manifest_put_url"`
	TTLSeconds       int       `json:"ttl_seconds"`
	CreatedAt        time.Time `json:"created_at"`
}

func (s *Server) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.callerOrUnauthorized(w, r)
	if !ok {
		return
	}

	var req createCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.WorktreeID == "" || req.HeadCommit == "" || req.IndexTree == "" || req.WorktreeTree == "" || req.IncrementalCommit == "" || req.DeviceID == "" {
		http.Error(w, "worktree_id, head_commit, index_tree, worktree_tree, incremental_commit, device_id are required", http.StatusBadRequest)
		return
	}

	wt, err := s.cfg.Store.GetWorktreeByID(r.Context(), req.WorktreeID)
	if errors.Is(err, ErrWorktreeNotFound) {
		http.Error(w, "worktree not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.log.Printf("sync: get worktree: %v", err)
		http.Error(w, "lookup worktree", http.StatusInternalServerError)
		return
	}
	if wt.UserID != caller.userID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if wt.OwnerKind != OwnerKindLaptop || wt.OwnerID != req.DeviceID {
		http.Error(w, "not the current owner", http.StatusForbidden)
		return
	}

	now := time.Now().UTC()
	checkpointID := newULID()
	ck := Checkpoint{
		ID:                checkpointID,
		WorktreeID:        wt.ID,
		HeadCommit:        req.HeadCommit,
		HeadRef:           req.HeadRef,
		IndexTree:         req.IndexTree,
		WorktreeTree:      req.WorktreeTree,
		IncrementalCommit: req.IncrementalCommit,
		CreatedAt:         now,
		CreatedBy:         "laptop:" + req.DeviceID,
	}
	if err := s.cfg.Store.InsertCheckpoint(r.Context(), ck); err != nil {
		s.log.Printf("sync: insert checkpoint: %v", err)
		http.Error(w, "insert checkpoint", http.StatusInternalServerError)
		return
	}

	urls, err := s.presignCheckpointPuts(r.Context(), wt.UserID, wt.ID, checkpointID)
	if err != nil {
		s.log.Printf("sync: presign puts: %v", err)
		http.Error(w, "presign", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, createCheckpointResponse{
		CheckpointID:     checkpointID,
		HeadCommitPutURL: urls[storage.BlobHeadCommit],
		IncrementalURL:   urls[storage.BlobIncremental],
		ManifestPutURL:   urls[storage.BlobManifest],
		TTLSeconds:       int(s.cfg.PresignTTL.Seconds()),
		CreatedAt:        now,
	})
}

type commitCheckpointResponse struct {
	CheckpointID string    `json:"checkpoint_id"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

func (s *Server) handleCommitCheckpoint(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.callerOrUnauthorized(w, r)
	if !ok {
		return
	}
	checkpointID := r.PathValue("id")
	if checkpointID == "" {
		http.Error(w, "checkpoint id missing", http.StatusBadRequest)
		return
	}

	ck, wt, err := s.lookupCheckpointForUser(r.Context(), checkpointID, caller.userID)
	if err != nil {
		http.Error(w, err.Error(), httpStatusForLookupErr(err))
		return
	}

	ctx := r.Context()
	for _, blob := range []storage.Blob{storage.BlobHeadCommit, storage.BlobIncremental, storage.BlobManifest} {
		key, err := storage.KeyFor(wt.UserID, wt.ID, ck.ID, blob)
		if err != nil {
			http.Error(w, "build key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		exists, err := s.cfg.Storage.Exists(ctx, key)
		if err != nil {
			s.log.Printf("sync: storage exists: %v", err)
			http.Error(w, "storage check", http.StatusInternalServerError)
			return
		}
		if !exists {
			http.Error(w, "blob "+string(blob)+" not yet uploaded", http.StatusConflict)
			return
		}
	}

	now := time.Now().UTC()
	if err := s.cfg.Store.MarkCheckpointUploaded(r.Context(), ck.ID, now); err != nil {
		s.log.Printf("sync: mark uploaded: %v", err)
		http.Error(w, "mark uploaded", http.StatusInternalServerError)
		return
	}
	if err := s.cfg.Store.UpdateWorktreePointer(r.Context(), wt.ID, ck.ID); err != nil {
		s.log.Printf("sync: advance pointer: %v", err)
		http.Error(w, "advance pointer", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, commitCheckpointResponse{
		CheckpointID: ck.ID,
		UploadedAt:   now,
	})
}

type downloadCheckpointResponse struct {
	CheckpointID     string `json:"checkpoint_id"`
	HeadCommitGetURL string `json:"head_commit_get_url"`
	IncrementalURL   string `json:"incremental_get_url"`
	ManifestGetURL   string `json:"manifest_get_url"`
	TTLSeconds       int    `json:"ttl_seconds"`
}

// handleDownloadCheckpoint returns presigned GET URLs for the gateway
// to fetch bundle bytes during MigrateWorktree (P3+). Authorized to
// the checkpoint's owning user.
func (s *Server) handleDownloadCheckpoint(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.callerOrUnauthorized(w, r)
	if !ok {
		return
	}
	checkpointID := r.PathValue("id")
	if checkpointID == "" {
		http.Error(w, "checkpoint id missing", http.StatusBadRequest)
		return
	}

	ck, wt, err := s.lookupCheckpointForUser(r.Context(), checkpointID, caller.userID)
	if err != nil {
		http.Error(w, err.Error(), httpStatusForLookupErr(err))
		return
	}
	if ck.UploadedAt.IsZero() {
		http.Error(w, "checkpoint not yet uploaded", http.StatusConflict)
		return
	}

	urls := make(map[storage.Blob]string, 3)
	for _, blob := range []storage.Blob{storage.BlobHeadCommit, storage.BlobIncremental, storage.BlobManifest} {
		key, err := storage.KeyFor(wt.UserID, wt.ID, ck.ID, blob)
		if err != nil {
			http.Error(w, "build key: "+err.Error(), http.StatusInternalServerError)
			return
		}
		u, err := s.cfg.Storage.PresignGet(r.Context(), key, s.cfg.PresignTTL)
		if err != nil {
			s.log.Printf("sync: presign get: %v", err)
			http.Error(w, "presign", http.StatusInternalServerError)
			return
		}
		urls[blob] = u
	}

	writeJSON(w, http.StatusOK, downloadCheckpointResponse{
		CheckpointID:     ck.ID,
		HeadCommitGetURL: urls[storage.BlobHeadCommit],
		IncrementalURL:   urls[storage.BlobIncremental],
		ManifestGetURL:   urls[storage.BlobManifest],
		TTLSeconds:       int(s.cfg.PresignTTL.Seconds()),
	})
}

// caller bundles the few pieces of identity we extract from claims for
// the new endpoints. P2 will replace this with a richer struct
// (DeviceID, HostID, Kind) once JWT claims carry those fields.
type caller struct {
	userID string
}

func (s *Server) callerOrUnauthorized(w http.ResponseWriter, r *http.Request) (caller, bool) {
	claims, err := s.cfg.Auth.Verify(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="clank-sync"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return caller{}, false
	}
	userID, err := s.cfg.UserIDFromClaims(claims)
	if err != nil || userID == "" {
		http.Error(w, "no user identity", http.StatusUnauthorized)
		return caller{}, false
	}
	return caller{userID: userID}, true
}

// lookupCheckpointForUser fetches a checkpoint and its worktree,
// asserting the worktree belongs to userID. Distinct error returns
// drive the right HTTP status via httpStatusForLookupErr.
func (s *Server) lookupCheckpointForUser(ctx context.Context, checkpointID, userID string) (Checkpoint, Worktree, error) {
	ck, err := s.cfg.Store.GetCheckpointByID(ctx, checkpointID)
	if errors.Is(err, ErrCheckpointNotFound) {
		return Checkpoint{}, Worktree{}, errCheckpointNotFound
	}
	if err != nil {
		s.log.Printf("sync: get checkpoint: %v", err)
		return Checkpoint{}, Worktree{}, errLookupInternal
	}
	wt, err := s.cfg.Store.GetWorktreeByID(ctx, ck.WorktreeID)
	if errors.Is(err, ErrWorktreeNotFound) {
		return Checkpoint{}, Worktree{}, errWorktreeNotFound
	}
	if err != nil {
		s.log.Printf("sync: get worktree: %v", err)
		return Checkpoint{}, Worktree{}, errLookupInternal
	}
	if wt.UserID != userID {
		return Checkpoint{}, Worktree{}, errForbidden
	}
	return ck, wt, nil
}

var (
	errCheckpointNotFound = errors.New("checkpoint not found")
	errWorktreeNotFound   = errors.New("worktree not found")
	errForbidden          = errors.New("forbidden")
	errLookupInternal     = errors.New("internal")
)

func httpStatusForLookupErr(err error) int {
	switch {
	case errors.Is(err, errCheckpointNotFound), errors.Is(err, errWorktreeNotFound):
		return http.StatusNotFound
	case errors.Is(err, errForbidden):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) presignCheckpointPuts(ctx context.Context, userID, worktreeID, checkpointID string) (map[storage.Blob]string, error) {
	out := make(map[storage.Blob]string, 3)
	for _, blob := range []storage.Blob{storage.BlobHeadCommit, storage.BlobIncremental, storage.BlobManifest} {
		key, err := storage.KeyFor(userID, worktreeID, checkpointID, blob)
		if err != nil {
			return nil, fmt.Errorf("key for %s: %w", blob, err)
		}
		u, err := s.cfg.Storage.PresignPut(ctx, key, s.cfg.PresignTTL)
		if err != nil {
			return nil, fmt.Errorf("presign %s: %w", blob, err)
		}
		out[blob] = u
	}
	return out, nil
}

func worktreeToResponse(w Worktree) worktreeResponse {
	return worktreeResponse{
		ID:                     w.ID,
		UserID:                 w.UserID,
		DisplayName:            w.DisplayName,
		OwnerKind:              w.OwnerKind,
		OwnerID:                w.OwnerID,
		LatestSyncedCheckpoint: w.LatestSyncedCheckpoint,
		CreatedAt:              w.CreatedAt,
		UpdatedAt:              w.UpdatedAt,
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newULID() string {
	return ulid.MustNew(ulid.Now(), cryptorand.Reader).String()
}
