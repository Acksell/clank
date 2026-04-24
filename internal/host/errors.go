package host

import (
	"errors"
	"fmt"

	"github.com/acksell/clank/internal/git"
)

// Push sentinels are re-exported from internal/git so callers above the
// host layer (hub, TUI) can use errors.Is without importing internal/git
// directly. Value identity is preserved — these are the same sentinels
// returned by git.Push — so PushBranch can pass them through unwrapped.
var (
	ErrPushRejected     = git.ErrPushRejected
	ErrPushAuthRequired = git.ErrPushAuthRequired
	ErrNothingToPush    = git.ErrNothingToPush
)

// PushAuthRequiredError is the structured form of ErrPushAuthRequired
// surfaced by the hub after a push retry has already been attempted
// and still failed. It carries the (target host, endpoint host) pair
// the TUI needs to render a useful prompt — "save a token for
// github.com" rather than a bare "push needs auth".
//
// Wraps ErrPushAuthRequired so existing errors.Is(err,
// host.ErrPushAuthRequired) checks at intermediate layers still match.
// TUI code that wants the structured detail should errors.As() into
// *PushAuthRequiredError.
type PushAuthRequiredError struct {
	// Hostname is the clank host the push ran on (e.g. "local",
	// "daytona-1"). Empty for local-only refs that somehow got here.
	Hostname Hostname
	// EndpointHost is the git remote host (e.g. "github.com"). This
	// is the key the TUI should write to credentials.json when the
	// user supplies a token.
	EndpointHost string
	// Underlying is the original error from the host (typically
	// ErrPushAuthRequired wrapping git's stderr).
	Underlying error
}

func (e *PushAuthRequiredError) Error() string {
	return fmt.Sprintf("push to %s on host %q requires authentication: %v",
		e.EndpointHost, e.Hostname, e.Underlying)
}

// Unwrap exposes the underlying error so errors.Is(err,
// ErrPushAuthRequired) keeps working through the wrap.
func (e *PushAuthRequiredError) Unwrap() error { return e.Underlying }

// Sentinel errors for Service methods. Callers (e.g. HTTP handlers in
// host/mux) use errors.Is to translate these into appropriate status
// codes without coupling to string matching.
var (
	// ErrNotFound is returned when a requested repo, branch, or worktree
	// does not exist on the host.
	ErrNotFound = errors.New("host: not found")

	// ErrCannotMergeDefault is returned when MergeBranch is called with
	// the default branch as its target (you cannot merge a branch into
	// itself).
	ErrCannotMergeDefault = errors.New("host: cannot merge the default branch into itself")

	// ErrNothingToMerge is returned when MergeBranch finds the feature
	// branch has no commits ahead and a clean worktree.
	ErrNothingToMerge = errors.New("host: nothing to merge")

	// ErrCommitMessageRequired is returned when MergeBranch finds
	// uncommitted work in the feature worktree but no commit message was
	// supplied for the auto-commit.
	ErrCommitMessageRequired = errors.New("host: commit_message is required when worktree has uncommitted changes")

	// ErrTargetDirty is returned when MergeBranch finds the merge
	// target's worktree has uncommitted changes. Named branch-agnostic
	// because the target may be any branch (default branch, a release
	// branch, etc.) — not always "main".
	ErrTargetDirty = errors.New("host: merge target worktree has uncommitted changes; commit or stash them first")

	// ErrMergeConflict is returned when the merge produces a conflict
	// that MergeBranch has already rolled back.
	ErrMergeConflict = errors.New("host: merge conflict: resolve manually or choose a different approach")

	// ErrReservedBranch is returned when ResolveWorktree is asked to
	// create a worktree for the repository's default branch (e.g.
	// "main"/"master"). The default branch is reserved for the primary
	// checkout — putting it in a separate worktree would prevent
	// `git checkout <default>` from working in the original repo
	// directory and breaks the user's mental model that worktrees are
	// for *other* branches.
	ErrReservedBranch = errors.New("host: cannot create a worktree for the default branch; it is reserved for the primary checkout")

	// ErrInvalidBranchName is returned when ResolveWorktree is given an
	// empty or whitespace-only branch name.
	ErrInvalidBranchName = errors.New("host: branch name must be non-empty")

	// ErrCannotPushDefault is returned when PushBranch is called with
	// the repository's default branch. Publishing the default branch
	// is intentionally unsupported — the feature-branch model is the
	// only supported publish flow (see
	// docs/publish_and_branch_defaults.md).
	ErrCannotPushDefault = errors.New("host: cannot push the default branch")
)
