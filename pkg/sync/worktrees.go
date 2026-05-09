package sync

import (
	"context"
	"errors"
	"time"
)

// ErrWorktreeNotFound is returned by SyncStore lookups when the
// requested worktree row doesn't exist.
var ErrWorktreeNotFound = errors.New("sync: worktree not found")

// ErrCheckpointNotFound is returned by SyncStore lookups when the
// requested checkpoint row doesn't exist.
var ErrCheckpointNotFound = errors.New("sync: checkpoint not found")

// ErrOwnerMismatch is returned by SyncStore.UpdateWorktreeOwner when
// the optimistic concurrency check fails — the expected current owner
// did not match what's in the row, indicating a concurrent migration
// or a stale read. Callers should retry from a fresh read.
var ErrOwnerMismatch = errors.New("sync: worktree owner mismatch (concurrent migration?)")

// OwnerKind enumerates which actor type owns a worktree's write
// authority. New values require schema-level coordination — never use
// raw string literals at call sites.
type OwnerKind string

const (
	OwnerKindLaptop OwnerKind = "laptop"
	OwnerKindSprite OwnerKind = "sprite"
)

// Worktree is a per-user persistent unit of sync ownership. One row
// per logical working tree. Multiple worktrees can exist for the same
// user (and even the same repo, on different branches or worktrees).
type Worktree struct {
	ID                     string
	UserID                 string
	DisplayName            string
	OwnerKind              OwnerKind
	OwnerID                string // device_id or host_id; "" if no owner has claimed yet
	LatestSyncedCheckpoint string // checkpoint ID; "" if no checkpoint pushed yet
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Checkpoint is the per-push manifest pointer. Bundle bytes live in
// object storage; this row is metadata only. UploadedAt is zero until
// /v1/checkpoints/<id>/commit confirms both bundles landed.
type Checkpoint struct {
	ID                string
	WorktreeID        string
	HeadCommit        string
	HeadRef           string
	IndexTree         string
	WorktreeTree      string
	IncrementalCommit string
	CreatedAt         time.Time
	CreatedBy         string
	UploadedAt        time.Time // zero until uploaded
}

// SyncStore is the persistence contract for worktrees + checkpoints.
// Implementations MUST be safe for concurrent use.
type SyncStore interface {
	GetWorktreeByID(ctx context.Context, id string) (Worktree, error)
	ListWorktreesByUser(ctx context.Context, userID string) ([]Worktree, error)
	ListWorktreesByOwner(ctx context.Context, kind OwnerKind, ownerID string) ([]Worktree, error)
	InsertWorktree(ctx context.Context, w Worktree) error
	UpdateWorktreePointer(ctx context.Context, id, checkpointID string) error

	// UpdateWorktreeOwner performs an atomic optimistic-concurrency
	// transfer: the row's owner is updated only if the current
	// owner_id equals expectedOwnerID. Returns ErrOwnerMismatch if the
	// guard fails (the caller's view of "current owner" was stale).
	UpdateWorktreeOwner(ctx context.Context, id, expectedOwnerID string, newKind OwnerKind, newOwnerID string) error

	GetCheckpointByID(ctx context.Context, id string) (Checkpoint, error)
	ListCheckpointsByWorktree(ctx context.Context, worktreeID string, limit int) ([]Checkpoint, error)
	InsertCheckpoint(ctx context.Context, c Checkpoint) error
	MarkCheckpointUploaded(ctx context.Context, id string, when time.Time) error
}
