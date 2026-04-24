# Git credential discovery

How clank finds the token to authenticate `git push` on remote hosts
(Daytona sandboxes, etc.) without making the user type their PAT every
time.

Companion doc to `docs/git_credentials_refactor.md` (which establishes
the type system) and `docs/publish_and_branch_defaults.md` (which
defines the `[p]ush` flow).

## Why this exists

The original credential resolver (`internal/hub/credentials.go` before
the push-credentials PR) returned `GitCredAnonymous` for every HTTPS
endpoint. That's fine for **clone of a public repo** but breaks **push
to anything**: GitHub/GitLab/Bitbucket all require auth on push, even
when the repo itself is public. Symptom on a Daytona host:

```
push_auth_required: fatal: could not read Username for 'https://github.com'
```

(The Daytona docs claim public repos work without auth — true for
clone, false for push. Don't trust them on this point.)

## Discovery order

The hub assembles a `gitcred.Stack` (see `internal/gitcred/stack.go`)
that runs each discoverer in turn and returns the first hit. A
discoverer can return one of three things:

1. **Hit** — a valid `agent.GitCredential`. Stack stops, returns it.
2. **Soft miss** — `gitcred.ErrNoCredential`. Stack continues to the
   next discoverer.
3. **Hard error** — anything else. Stack short-circuits and surfaces
   the error to the user. We treat parse failures, unreadable files,
   and timeouts as hard errors so the user can debug them rather
   than silently falling through to "no credential found".

The production stack (wired in `internal/cli/daemoncli/daemoncli.go`):

| # | Source | Honours | Notes |
|---|---|---|---|
| 1 | `gitcred.FromEnv` | `CLANK_GIT_TOKEN_<HOST>` (e.g. `CLANK_GIT_TOKEN_GITHUB_COM`); per-provider env vars (`GH_TOKEN`/`GITHUB_TOKEN`, `GITLAB_TOKEN`, `BITBUCKET_TOKEN`) | Per-host override beats per-provider. Tokens never leak across providers. |
| 2 | `gitcred.FromGH` | `gh auth token` | github.com only. Distinguishes "not logged in" (soft miss) from "command timed out" (hard error). |
| 3 | `gitcred.FromSettings` | `~/.clank/credentials.json` | Mode 0600 plaintext JSON. Keyed by endpoint host. Written via the TUI credential modal. |

We **deliberately skip** `git credential fill` — it can hang waiting
for an interactive helper (osxkeychain on a stale entry, etc.) and
there's no clean async API. We also skip the OS keychain in v1 to
keep the dep surface minimal; the modal-paste flow is good enough
until we hit a user who needs hardware-backed storage.

## Wire shape

All discovered tokens are encoded as
`GitCredHTTPSBasic{Username:"x-access-token", Password:<token>}`.
The `x-access-token` username is the universal-PAT convention that
github / gitlab / bitbucket all accept; it lets us route everything
through the existing `GIT_ASKPASS` plumbing in
`internal/git/git.go:165` without per-provider branching.

The TUI never sees the token. Discovery runs hub-side; the hub only
ships the credential to the host inside the standard `GitCredential`
DTO, where the host's `git.Push` consumes it via `GIT_ASKPASS` (no
argv exposure).

## Caching

`internal/hub/credcache.go` wraps the stack with a process-lifetime
cache keyed on `(target host.Hostname, endpointHost)`. Two reasons
for the cache:

1. Discovery hits the disk and shells out to `gh`. Doing that on
   every git operation would be silly.
2. The (target, endpointHost) key prevents one user / one repo's
   credential from leaking into another's — even if the same
   endpoint host is reused across hosts, we re-discover per
   target so per-host policy can change later.

**Errors and soft misses are NOT cached.** A transient `gh` failure
should heal on the next attempt, not poison the slot for the
process lifetime.

**Invalidation:** `PushBranchOnHost` invalidates the cache entry on
`ErrPushAuthRequired` and re-runs discovery once. If the second
attempt also fails, the error is wrapped as
`host.PushAuthRequiredError{Hostname, EndpointHost, Underlying}`
and propagates up to the TUI. The single auto-retry exists so a
stale-token-in-cache state is self-healing the moment the user
fixes it (rotated PAT, fresh `gh auth login`, etc.) without them
needing to know that a cache exists.

## TUI flow

When `*host.PushAuthRequiredError` reaches the inbox via
`pushResultMsg`, the credential modal opens
(`internal/tui/credentialmodal.go`). Two paths:

- **`[r]etry`** — close the modal, re-issue `pushBranchCmd`. The
  hub-side cache was already invalidated so the next push re-runs
  the full discovery stack from scratch. Useful when the user has
  fixed `gh auth login` in another terminal since the previous
  attempt.
- **`[t]` then paste** — collect a PAT in a masked textinput,
  persist it to `~/.clank/credentials.json` via
  `gitcred.FromSettings().SaveToken(host, token)`, then close the
  modal and re-issue the push. The fresh token wins on the next
  discovery pass because `SettingsDiscoverer` is in the stack.
- **`[esc]`** — close the modal without retrying. The user can
  still see the original error in the inbox status line.

The modal owns the input lifetime; tokens never persist in TUI
state outside `m.input.Value()` between paste and Enter.

## Storage layout

`~/.clank/credentials.json`:

```json
{
  "credentials": {
    "github.com": {
      "kind": "https_basic",
      "username": "x-access-token",
      "password": "ghp_…"
    }
  }
}
```

- File mode is **0600** — owner read/write only. The file holds
  plaintext tokens; widening the mode would let any process running
  as the same user (or anyone with `sudo -u`) exfiltrate them.
- Writes are **atomic** (write-tmp + rename) so a crash mid-save
  never corrupts existing entries.
- An empty token passed to `SaveToken` **deletes** the entry — this
  is the API surface for "log out" once we expose it in the TUI.

## Security posture

- Tokens are passed to git via `GIT_ASKPASS` only — never argv,
  never env vars that survive past the git invocation, never log
  lines (see `agent.GitCredential.Redacted`).
- Tokens never cross the wire from hub to TUI. The TUI sees them
  exactly once, at paste time, and only if the user typed them.
- `~/.clank/credentials.json` is not encrypted at rest. If you
  need that, your threat model wants OS-keychain storage (see
  Future work).

## Future work

- **OS keychain support.** Promote `gitcred.FromKeychain` ahead of
  `FromSettings` in the stack. Means a real macOS / libsecret /
  wincred dep. Worth doing for users on shared / unencrypted
  disks.
- **GitHub device-flow OAuth.** Skip the PAT step entirely for the
  common github case — open a browser, complete device flow,
  store the resulting token in settings (or keychain). Tracked as
  Phase 5 in the original push-credentials plan; deferred because
  the [t]oken-paste flow covers the same ground without a browser
  dep.
- **SSH-agent forwarding to remote hosts.** Today, `ssh://`
  endpoints on remote hosts get rewritten to `https://` and
  resolved through this discovery flow. Forwarding the local
  agent socket would let users keep their existing SSH-only
  workflows.
- **Per-target credential overrides.** The cache is already keyed
  on `(target, endpointHost)`; the discovery layer isn't. If a
  user wants different tokens for the same endpoint on different
  Daytona pools, we'd need a target-aware
  `gitcred.SaveToken(target, host, token)` API.
