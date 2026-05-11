package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/acksell/clank/pkg/sync/storage"
)

// ErrForbidden is returned by direct-call API methods when the
// supplied userID doesn't own the requested resource.
var ErrForbidden = errors.New("sync: forbidden")

// CheckpointDownloadURLs holds presigned GET URLs for the three
// bundle objects of a committed checkpoint.
type CheckpointDownloadURLs struct {
	CheckpointID     string
	HeadCommitGetURL string
	IncrementalURL   string
	ManifestGetURL   string
}

// GetWorktree looks up a worktree by ID and verifies it belongs to
// userID. Returns ErrWorktreeNotFound or ErrForbidden on auth failure.
// Used by the gateway's MigrateWorktree flow instead of HTTP.
func (s *Server) GetWorktree(ctx context.Context, userID, worktreeID string) (Worktree, error) {
	wt, err := s.cfg.Store.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		return Worktree{}, err
	}
	if wt.UserID != userID {
		return Worktree{}, fmt.Errorf("%w: worktree %s", ErrForbidden, worktreeID)
	}
	return wt, nil
}

// DownloadCheckpointURLs returns presigned GET URLs for the given
// committed checkpoint. userID is checked against the checkpoint's
// owning worktree. Returns ErrForbidden on tenant mismatch and
// ErrCheckpointNotFound when the checkpoint doesn't exist.
func (s *Server) DownloadCheckpointURLs(ctx context.Context, userID, checkpointID string) (CheckpointDownloadURLs, error) {
	ck, wt, err := s.lookupCheckpointForUser(ctx, checkpointID, userID)
	if err != nil {
		return CheckpointDownloadURLs{}, err
	}
	if ck.UploadedAt.IsZero() {
		return CheckpointDownloadURLs{}, fmt.Errorf("sync: checkpoint %s not yet uploaded", checkpointID)
	}

	urls := make(map[storage.Blob]string, 3)
	for _, blob := range []storage.Blob{storage.BlobHeadCommit, storage.BlobIncremental, storage.BlobManifest} {
		key, err := storage.KeyFor(wt.UserID, wt.ID, ck.ID, blob)
		if err != nil {
			return CheckpointDownloadURLs{}, fmt.Errorf("sync: build key: %w", err)
		}
		u, err := s.cfg.Storage.PresignGet(ctx, key, s.cfg.PresignTTL)
		if err != nil {
			return CheckpointDownloadURLs{}, fmt.Errorf("sync: presign get: %w", err)
		}
		urls[blob] = u
	}

	return CheckpointDownloadURLs{
		CheckpointID:     ck.ID,
		HeadCommitGetURL: urls[storage.BlobHeadCommit],
		IncrementalURL:   urls[storage.BlobIncremental],
		ManifestGetURL:   urls[storage.BlobManifest],
	}, nil
}

// TransferOwnership atomically transfers worktree ownership.
// userID is the tenancy gate; toKind/toID/expectedOwnerID are
// forwarded to the store's optimistic-concurrency guard.
// Returns ErrOwnerMismatch on a lost-update race.
func (s *Server) TransferOwnership(ctx context.Context, userID, worktreeID string, toKind OwnerKind, toID, expectedOwnerID string) (Worktree, error) {
	wt, err := s.cfg.Store.GetWorktreeByID(ctx, worktreeID)
	if errors.Is(err, ErrWorktreeNotFound) {
		return Worktree{}, err
	}
	if err != nil {
		return Worktree{}, fmt.Errorf("sync: get worktree: %w", err)
	}
	if wt.UserID != userID {
		return Worktree{}, fmt.Errorf("%w: worktree %s", ErrForbidden, worktreeID)
	}

	expected := expectedOwnerID
	if expected == "" {
		expected = wt.OwnerID
	}

	if err := s.cfg.Store.UpdateWorktreeOwner(ctx, worktreeID, wt.OwnerKind, expected, toKind, toID); err != nil {
		return Worktree{}, err
	}

	updated, err := s.cfg.Store.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		return Worktree{}, fmt.Errorf("sync: re-read after transfer: %w", err)
	}
	return updated, nil
}
