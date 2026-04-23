# Publish UX & Remote-Host Branch Defaults

> Living plan document. Update as phases complete. Each phase ends in a single commit.

## Background

Two coupled UX gaps after the git-credentials refactor (`docs/git_credentials_refactor.md`, Phase 9 landed at `7106fd0`) made Daytona sandboxes usable but not safe:

1. **Remote-host sessions land on `main`.** `host.Service.workDirFor`
   (`internal/host/service.go:352`) returns the clone root when `WorktreeBranch`
   is empty, and none of the 4 ingress sites (clankcli, two TUI spots, voice
   tools) default-fill `WorktreeBranch` for remote hosts. Result: an agent run
   on a Daytona sandbox commits straight to `main` in the clone, and the user
   has no branch to push or PR from.

2. **No publish verb beyond `MergeBranch`.** `host.Service.MergeBranch`
   (`internal/host/service.go:555`) rebase-merges a feature branch into
   default and cleans up the worktree. That's the only "publish" primitive.
   Daytona sandboxes are ephemeral — `daytona.Handle.Stop` deletes the
   sandbox (`internal/host/daytona/handle.go:29`), and there is no
   suspend/persist primitive. Without a push path, work is lost on shutdown.

This plan adds a push primitive and a default-branch policy so remote-host
work is (a) always on a named branch and (b) recoverable via a remote.

## Decisions locked in

| Topic | Decision |
|---|---|
| Branch-naming policy | An explicit `WorktreeBranch` from the caller is always used verbatim. Only when it is empty does the hub auto-fill `clank/<sessionID>` (remote hosts only — see next row). Deterministic, ugly, good enough for v0. Claude uses `claude/<slug>-<id>` — a nicer slug can come later. |
| Default-branch protection scope | **Remote hosts only.** Local hosts still allow `main` sessions (user controls their own worktrees). |
| Policy seam | Fill `WorktreeBranch` at the hub seam (one place), not at each of the 4 ingress sites. |
| Publish verbs | **Two distinct verbs**: `merge` and `push`. Not a single magic `publish`. TUI surfaces `[m]erge` and `[p]ush` hints on worktrees with a diff. |
| Force-push | **Never.** Non-fast-forward push is a hard error. Smooth rebases with agentic conflict resolution come later (see §Out of scope). |
| Credential consent | First `push` per credential triggers a modal asking the user to (a) confirm use of the discovered credential and (b) opt into auto-push / auto-commit. Preferences persist in settings JSON. |
| Credential discovery | **Deferred.** Push v0 only works when the "Token-discovery PR" follow-up from `git_credentials_refactor.md` §Deferred has landed. Until then `clank push` on a private remote fails fast with `ErrPushAuthRequired`. |
| Auto-push | **Deferred** (Phase E, out of scope for this plan). Shipped later once settings infra exists. |
| `git.Push` auth transit | Same `GIT_ASKPASS` pattern as `git.Clone`. No tokens in argv. |
| Wire transport | New `POST /worktrees/push`, symmetrical to `/worktrees/merge`. |

## Out of scope (explicit follow-ups)

- **Auto-push / auto-commit on activity.** Debounced push after successful
  tool-use turns that produced commits. Settings-gated. Motivation: the
  "don't lose work on Daytona shutdown" guarantee. Depends on settings
  storage + settings sidebar page.
- **Settings sidebar page.** Configure auto-push, auto-commit, credential
  consent, branch-naming prefix, etc. Stored in a settings JSON file.
- **Onboarding modal for git credentials.** First push triggers a
  modal: "we found a credential in `gh auth token` — use it? also enable
  auto-push?" Writes to settings. Depends on the token-discovery follow-up
  + settings infra.
- **`OpenPR` verb.** Shell out to `gh` / `glab` after a successful push.
  Separate phase; needs host-OS detection of PR CLIs.
- **Agentic rebase + merge-conflict resolution.** Replaces the need for
  force-push. Agent rebases feature branch onto updated default, resolves
  conflicts, re-pushes. Non-trivial; depends on a stable push primitive.
- **Branch rename on title-arrival.** Start as `clank/<sessionID>`, rename
  to `clank/<slug-of-title>` once the backend sets a session title. Nice
  UX polish; awkward if the branch has already been pushed.
- **`jj` (jj-vcs) migration.** Gitbutler-style nested/parallel worktrees
  per agent. The push primitive here must not hard-code assumptions that
  preclude a future `jj` backend — keep `host.Service.PushBranch` VCS-
  agnostic in signature (takes `WorktreeRef`, not git refspecs).
- **Daytona persistent volumes / live snapshots.** Daytona's "snapshot"
  concept is a boot image, not a live-state snapshot. Alternative to
  auto-push for work preservation; larger scope.

## Phases

### Phase A — Default-branch protection for remote hosts

**Goal:** A remote-host session with `WorktreeBranch == ""` resolves to a
non-default branch `clank/<sessionID>`. Local-host sessions unchanged.

**Changes:**
- New helper `internal/hub/branchdefault.go`:
  ```go
  // defaultWorktreeBranch returns the branch to use when a caller left
  // WorktreeBranch empty. Remote hosts get clank/<sessionID> to keep work
  // off the repo's default branch; local hosts return "" unchanged.
  func defaultWorktreeBranch(hostKind HostKind, sessionID string) string
  ```
- Apply at every `hub.Service` method that forwards a `WorktreeRef` to a
  host and originates from a session (session-create, session-resume,
  first message on a new session). Single call site in each method,
  immediately before `hostForRef`.
- **Do not** patch the 4 ingress sites (clankcli flags, TUI sidebar,
  voice). They already pass whatever the user asked for; the hub is the
  policy layer.

**Tests** (`internal/hub/branchdefault_test.go`):
- Remote host + empty branch + sessionID `abc123` → `clank/abc123`.
- Remote host + explicit branch `feature/x` → `feature/x` unchanged.
- Local host + empty branch → `""` unchanged.
- Local host + explicit branch → unchanged.

**Regression test** (`internal/host/service_test.go` or hub integration):
Create a session on a fake remote host, assert `workDirFor` resolved a
worktree under `clank/<sessionID>` and that the clone root still points
at default.

**Commit:** `hub: default remote-host sessions to clank/<sessionID> branch`

### Phase B — `git.Push` primitive

**Goal:** A thin `git push` wrapper in `internal/git/` that uses the same
`GIT_ASKPASS` plumbing as `git.Clone`.

**Changes:**
- Rename `cloneAuthEnv` → `gitAuthEnv` in `internal/git/git.go:179`
  (already suitable; just removes misleading name).
- New `internal/git/push.go`:
  ```go
  // Push pushes branch from dir to remote using cred. Never force-pushes;
  // non-fast-forward is a hard error (ErrPushRejected).
  func Push(ctx context.Context, dir, remote, branch string, cred agent.GitCredential) error
  ```
- Non-fast-forward exit code from `git push` maps to a sentinel
  `ErrPushRejected`. Auth failure maps to `ErrPushAuthRequired`.
  "Nothing to push" (up-to-date) maps to `ErrNothingToPush`.
- Sentinels live in `internal/git/errors.go` (create if missing).

**Tests** (`internal/git/push_test.go`, integration, no mocks):
- Set up a local bare repo as "remote" + a working clone.
- Push a new branch with one commit → success.
- Push same branch again with no new commits → `ErrNothingToPush`.
- Advance the bare repo, push a diverged branch → `ErrPushRejected`.
- (Auth test deferred until Phase 11 credential discovery lands; for now
  a `GitCredAnonymous` push to a local bare repo is the only path.)

**Commit:** `git: add Push primitive with GIT_ASKPASS auth`

### Phase C — `host.Service.PushBranch` + wire transport

**Goal:** Expose push over the host mux so the hub can drive it.

**Changes:**
- `internal/host/service.go`:
  ```go
  type PushResult struct {
      RemoteURL    string
      Branch       string
      CommitsAhead int  // before push; 0 after success
  }

  func (s *Service) PushBranch(
      ctx context.Context,
      ref WorktreeRef,
      cred agent.GitCredential,
  ) (PushResult, error)
  ```
  Resolves worktree via `workDirFor`, refuses default branch
  (`ErrCannotPushDefault`), calls `git.Push` against `origin`.
- Error sentinels in `internal/host/errors.go`: `ErrCannotPushDefault`,
  `ErrNothingToPush`, `ErrPushRejected`, `ErrPushAuthRequired` (re-export
  or wrap `git.*`).
- Transport:
  - `internal/host/mux/worktrees_push.go` — `POST /worktrees/push`
    handler (one-file-per-endpoint per AGENTS.md §1).
  - Route registered in `internal/host/mux/mux.go` alongside
    `/worktrees/merge`.
  - Client method `PushBranch` in `internal/host/client/worktree.go`.

**Tests:**
- `internal/host/service_push_test.go`: integration against a local bare
  "remote", asserts each sentinel fires on the right input.
- `internal/host/mux/worktrees_push_test.go`: roundtrip through the mux.

**Commit:** `host: add PushBranch with /worktrees/push transport`

### Phase D — Hub plumbing + TUI surface

**Goal:** User-facing TUI `[p]ush` affordance. No new `clankcli` commands —
push is a TUI-only action for v0 (keeps `clankcli` surface minimal per
project convention).

**Changes:**
- `internal/hub/api.go`: new `PushBranchOnHost` mirroring
  `MergeBranchOnHost` (line 555). Goes through `hostForRef` so endpoint +
  credential resolution is identical to clone/merge.
- `internal/hub/client/host.go`: add `PushBranch` client call.
- **TUI:**
  - `internal/tui/sessionview_compose.go`: add `[p]ush` keybind next to
    existing `[m]erge`. Only visible when `CommitsAhead > 0` on the
    current worktree (data already computed in
    `host.Service.listBranches`, `service.go:568`).
  - On `p`, dispatch `PushBranchOnHost` via hub client; show spinner;
    surface result inline.
  - **Credential-consent modal:** deferred until the token-discovery
    follow-up + settings infra land. For v0, a missing credential
    surfaces `ErrPushAuthRequired` inline with text "no credential
    configured; see `docs/git_credentials_refactor.md` §Deferred:
    Token-discovery PR".

**Tests:**
- `internal/hub/api_push_test.go`: hub-level integration, asserts
  `hostForRef` is used (same cred-resolution path as merge).
- TUI: unit test for the `[p]ush` visibility predicate (diff > 0 and
  branch != default).

**Commit:** `hub,tui: wire push end-to-end via TUI [p]ush`

## Sequencing notes

- Phase A is independent and can land first; it unblocks every remote-host
  session from the `main`-pollution bug immediately, even before push
  exists.
- Phases B → C → D are strictly sequential.
- The token-discovery follow-up (from `git_credentials_refactor.md`
  §Deferred) is **not** a blocker for landing Phases A-D — push to public
  forks / anonymous remotes works without it, and the failure mode for
  private remotes is a clean `ErrPushAuthRequired`. Token discovery should
  land before the credential-consent modal work in the out-of-scope list.

## Open questions (none blocking — noted for later)

- **Remote name.** Hard-coded `origin` in Phase C. Multi-remote support
  (e.g. a user's fork vs upstream) is a later phase.
- **Branch-prefix configurability.** `clank/` is hard-coded in Phase A.
  Goes into the settings-page work when that lands.
- **Push target for a branch whose remote tracking isn't set.** Phase B's
  `git.Push` will `git push -u origin <branch>` on first push; subsequent
  pushes omit `-u`. Sentinel behaviour should be unchanged.
