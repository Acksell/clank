# Git Credentials Refactor

> Living plan document. Update as phases complete. Each phase ends in a single commit.

## Background

The original bug: a remote Daytona host received an scp-form SSH URL
(`git@github.com:owner/repo.git`) and tried to clone it. The sandbox has no
SSH agent, so it hung on the host-key prompt and timed out on port 22.

The first fix attempted to pattern-match SSH URLs and rewrite them to HTTPS at
each hub→host call site (`internal/agent/giturl.go`, see baseline commit
`51a9773`). That solution scattered URL-rewriting logic across the codebase
and conflated two concepts: where a repo lives (identity) and how to
authenticate to it (credential).

This refactor adopts the data model used by
[`go-git/v5`](https://pkg.go.dev/github.com/go-git/go-git/v5/plumbing/transport):
parsed `Endpoint` (the "where") + opaque `AuthMethod` (the "how"), passed
together as separate arguments. The hub becomes the credential-policy owner;
the host becomes a dumb credential-consumer. Adding new auth methods (mTLS,
GitHub App JWT, SaaS-minted tokens) becomes a typed extension instead of a
new branch in a URL string-matcher.

## Decisions locked in

| Topic | Decision |
|---|---|
| Naming | `Git*` prefix everywhere (`GitEndpoint`, `GitCredential`). No `Repo*`. |
| Library use | Depend on `go-git/v5` for URL parsing only. Single importer (`internal/hub/endpoint.go`). `agent` package stays dep-free. |
| PR shape | Single full-refactor PR. Commit per phase. |
| Token transit | `GIT_ASKPASS` script — no tokens in argv. |
| SQLite | New flat columns `git_endpoint_*` + `git_remote_url` (derived, diagnostic). Drop legacy `remote_url`. |
| Token discovery | Out of scope. v1 supports public repos only. `GitCredKindHTTPSBasic` exists in the type system, resolver never emits it yet. |
| Credential names | `anonymous`, `https_basic` (TODO), `https_token` (TODO), `ssh_agent` (was `ssh_local` — renamed; locality is a separate invariant from the mechanism). |
| Migration failures | Hard-fail loudly. Ask user before dropping conflicting rows. |
| TUI parse failures | Refuse the action, surface inline error. No silent fallback. |
| `LocalPath` | Keep as host-local hint, distinct from `Endpoint`. Not a `file://` endpoint. |
| Resolver heuristic | Derive from parsed endpoint protocol only. No `~/.ssh/known_hosts` snooping. |

## Deferred (explicit follow-ups)

- **Token-discovery PR.** Read `gh auth token` / env vars / config file.
  Wire up `GitCredKindHTTPSBasic` emission in resolver. Enables private repos.
- **`LocalPath` removal + deviceID-based locality.** Drop `LocalPath` from
  the wire DTO. Local hosts maintain an `Endpoint → localPath` registry.
  Replace the `"local"` Hostname sentinel with deviceID intersection
  (multi-device per user, mobile, etc.).
- **Folders without `.git`.** Allow refs that point at directories not yet
  initialized as git repos (init-on-demand).
- **Switch `git.Clone` to use `go-git` natively** instead of shelling out.
  Orthogonal; current `exec.Command` works.

## Type model (target state)

```go
// internal/agent/gitendpoint.go
type GitEndpoint struct {
    Protocol string  // "https" | "http" | "ssh" | "git" | "file"
    User     string  // ssh: "git" typically; https: rare
    Host     string  // "github.com"
    Port     int     // 0 = default for protocol
    Path     string  // "owner/repo.git" (no leading "/")
}

// internal/agent/gitcredential.go
type GitCredentialKind string
const (
    GitCredAnonymous  GitCredentialKind = "anonymous"
    GitCredHTTPSBasic GitCredentialKind = "https_basic" // TODO PR2
    GitCredHTTPSToken GitCredentialKind = "https_token" // TODO PR2
    GitCredSSHAgent   GitCredentialKind = "ssh_agent"   // local-only invariant
)
type GitCredential struct {
    Kind     GitCredentialKind
    Username string `json:",omitempty"`
    Password string `json:",omitempty"` // never logged
    Token    string `json:",omitempty"` // never logged
}

// internal/agent/gitref.go
type GitRef struct {
    Endpoint       *GitEndpoint  // canonical identity (nil = local-only)
    LocalPath      string        // local-host clone hint; will be removed in deviceID PR
    WorktreeBranch string
}
```

Hub→host call carries both `(GitRef, GitCredential)`. The host's clone code
switches on `credential.Kind` to construct the correct `git clone` invocation.

## Hub credential-resolver policy (v1)

Input: `(target host.Hostname, ep *GitEndpoint)`.
Output: `(GitCredential, possibly-rewritten *GitEndpoint, error)`.

| Endpoint protocol | Target host kind | Resolution |
|---|---|---|
| `https` / `http` | any | `anonymous`, endpoint unchanged |
| `ssh` | local | `ssh_agent`, endpoint unchanged |
| `ssh` | remote, host on public allowlist | `anonymous`, endpoint rewritten to `https` |
| `ssh` | remote, host not on allowlist | hard error: "remote host has no credentials and provider not on public-HTTPS allowlist" |
| `file` | local | `anonymous`, endpoint unchanged |
| `file` | remote | hard error: "file:// endpoints not valid for remote host" |
| anything else | any | hard error: clear message |

Public-HTTPS allowlist (lifted from existing `internal/agent/giturl.go`):
`github.com`, `gitlab.com`, `bitbucket.org`. Extensible.

## Phase plan

Each phase = one commit. Each phase must compile and tests must pass on its
HEAD.

### Phase 0 — Module deps
- ~~Separate phase~~ folded into Phase 2: `go mod tidy` removes deps with no
  importer, so `go-git/v5` will be added as part of the parser commit.
- **Status:** [x] merged into Phase 2

### Phase 1 — Agent types (additive)
- New: `internal/agent/gitendpoint.go` (type only; parser lives in Phase 2 hub).
- New: `internal/agent/gitcredential.go` (type, kind constants, Validate).
- Edit: `internal/agent/gitref.go` — add `Endpoint *GitEndpoint` field
  alongside existing `RemoteURL` (kept for compat through Phase 7).
  Update `RepoKey` to key on `Endpoint.Host + Path + Branch` when
  `Endpoint != nil`, else fall back to `RemoteURL`. Update
  `RepoDisplayName` similarly.
- New tests: `gitendpoint_test.go`, `gitcredential_test.go`. Extend
  `gitref_test.go` with a "ssh and https endpoints share RepoKey" test.
- **Deferred to Phase 9:** delete `internal/agent/giturl.go` +
  `giturl_test.go`. They host `parseGitURL` / `CloneDirName` /
  `HTTPSRemoteURL`; the first two are still used by
  `internal/host/service.go` clone path until Phase 6, and the third
  by `internal/hub/sessions.go` rewrite block until Phase 4. Removing
  any of them earlier breaks compile.
- **Status:** [x] complete

### Phase 2 — Endpoint parser
- New: `internal/hub/endpoint.go` — `ParseGitEndpoint(raw string) (*agent.GitEndpoint, error)` via `transport.NewEndpoint`.
- New: `internal/hub/endpoint_test.go` — table tests (scp form, https, ssh://, file://, malformed).
- **Status:** [x] complete

### Phase 3 — Credential resolver
- New: `internal/hub/credentials.go` — `resolveCredential(target, ep)` per the policy table above.
- New: `internal/hub/credentials_test.go`.
- Edit: `internal/hub/hub.go` — add `hostForRef(hostname, ref) (*hostclient.HTTP, agent.GitRef, agent.GitCredential, error)`.
- **Status:** [x] complete

### Phase 4 — Hub call-site rewiring
- `internal/hub/sessions.go` — `createSession` etc. swap to `hostForRef`. Delete inline rewrite block.
- `internal/hub/api.go` — `ListAgents`, `ListModels`, `ListBranchesOnHost`, `ResolveWorktreeOnHost`, `RemoveWorktreeOnHost`, `MergeBranchOnHost`.
- `internal/hub/agents_models.go` — `refreshPrimaryAgentsInBackground` (the actual failing path from the bug).
- **Status:** [x] complete

### Phase 5 — Host-client signatures
- `internal/hostclient/*.go` — every method that takes a `GitRef` adds an `auth GitCredential` parameter; JSON-encodes both into the wire request.
- **Status:** [x] complete

### Phase 6 — Host-side consumption
- `internal/host/dto.go` (or wherever) — request DTOs gain `Auth GitCredential`.
- `internal/host/service.go` — clone path takes `(endpoint, credential)`. Switches on `credential.Kind`.
- `internal/git/git.go` — `Clone` signature takes `endpoint + credential`. Routes:
  - `anonymous` → plain https URL
  - `https_basic` → askpass script
  - `ssh_agent` → scp-form URL; refuse if `CLANK_HOST_KIND` indicates remote
- New: `internal/git/askpass.go` — temp-file helper, mode 0700, cleanup closure.
- Update `internal/git/git_test.go`. Add askpass test using local file:// remote.
- **Status:** [x] complete

### Phase 7 — TUI / clankcli ingress
- `internal/tui/inbox.go:172`, `internal/tui/sessionview_compose.go:63,237`, `clankcli/clankcli.go:141` — call `gitendpoint.Parse` alongside the raw string and populate `Endpoint`.
- TUI policy: parse failure → drop both `RemoteURL` and `Endpoint` (don't propagate half-formed refs across the wire). LocalPath alone still works on the local host.
- clankcli policy: hard-fail (returns parse error to caller) since CLI is non-interactive.
- Discovery (hub/sessions.go:118) silently drops unparseable origins to avoid breaking the entire scan.
- **Voice intentionally NOT touched** — voice package has structural issues that need separate planning (per user direction); it will continue passing string-only RemoteURL.
- `RemoteURL` field NOT removed yet — that is Phase 9.
- **Status:** [x] complete (merged with Phase 8)

### Phase 8 — SQLite migration
- New migration `migrate_v16.go`: adds `git_endpoint_protocol`, `git_endpoint_user`, `git_endpoint_host`, `git_endpoint_port`, `git_endpoint_path` to `sessions` table; backfills via `gitendpoint.Parse`; hard-fails (with row-id list) on parse errors; drops legacy `git_remote_url` column. `primary_agents` table dropped+recreated (pure derivation cache, no backfill needed).
- `gitRefColumns` helper struct in `store.go`. `gitRefToColumns` parses `RemoteURL` if `Endpoint` is nil (transitional bridge — to be deleted in Phase 9 with `RemoteURL` field). `gitRefFromColumns` reconstructs `Endpoint` from columns and derives `RemoteURL = ep.String()` for back-compat.
- All store queries (`LoadSessions`, `UpsertSession`, `LoadPrimaryAgents`, `UpsertPrimaryAgents`, `KnownAgentTargets`, `FindByExternalID`) use new endpoint columns.
- **Status:** [x] complete (merged with Phase 7)

### Phase 9 — Cleanup & verification
- Delete `internal/hub/sessions_remote_rewrite_test.go` (salvage helpers if needed).
- Audit: `git grep -i 'RemoteURL'` returns zero hits in non-test code.
- `go test ./...` green.
- Manual smoke: register Daytona, public-repo session works; private repo errors clearly.
- **Status:** [x] code complete (manual smoke pending). The
  `agent.GitRef.RemoteURL` field is gone; `Endpoint` is the sole
  remote identity. `internal/agent/giturl.go` no longer parses URLs
  (`CloneDirName` now takes `*GitEndpoint`). The store bridge in
  `gitRefToColumns` is deleted — Endpoint must be set by the caller.
  All ingress sites (clankcli, TUI inbox/sessionview\_compose, hub
  discovery, voice tools, hub mux catalog query) parse the URL
  themselves; voice received the minimal mechanical edit needed to
  keep compiling. Wire format on the hub catalog endpoints
  (`/agents`, `/models`) still uses the `git_remote_url` query param
  string, parsed at the mux ingress. `internal/agent/giturl_test.go`
  and `internal/hub/sessions_remote_rewrite_test.go` were deleted as
  obsolete. Hub test fixtures gained `mustParseEndpoint` /
  `mustRef`; store tests use `mustParseRemoteRef`. Two store tests
  that previously asserted a derived `RemoteURL` string now assert
  on `Endpoint.String()` directly. `go test ./...` green.

### Phase 10 — Docs
- Update `docs/daytona_plan.md` Phase G — link to this doc; describe what's left.
- Update this doc's status table.
- **Status:** [ ] not started

## Throwaway audit

What this PR deletes:
- `internal/agent/giturl.go` + test
- The SSH→HTTPS rewrite block in `internal/hub/sessions.go:341-357`
- `internal/hub/sessions_remote_rewrite_test.go`

The first fix attempt (commit `51a9773`) is fully superseded.

## Open invariants to enforce

- `GitCredSSHAgent` is only valid when target host is local. Hub validates
  before constructing the request; host validates again on receipt
  (defense-in-depth). Surfaces clearly if violated.
- Tokens must never appear in `argv` or `os.Environ()` values. Only inside
  the askpass script body, which is mode 0700 and unlinked on close.
- `RepoKey` must be protocol-independent — `ssh://github.com/foo` and
  `https://github.com/foo` share a key.

## Progress log

(append entries as phases complete)

- 2026-04-23 — Plan document created. Baseline commit `51a9773`.
- 2026-04-23 — Phase 1 complete: added `GitEndpoint`, `GitCredential`
  types and `Endpoint *GitEndpoint` field on `GitRef`. `RepoKey` is
  now protocol-independent when `Endpoint` is populated. Existing
  `RemoteURL` plumbing untouched; downstream behaviour unchanged.
- 2026-04-23 — Phase 2 complete: `internal/hub/endpoint.go` adds
  `ParseGitEndpoint` (sole importer of `go-git/v5`). Round-trip,
  scp↔https key-equivalence, and host-case/default-port normalisation
  all covered by tests.
- 2026-04-23 — Phase 3 complete: `ResolveCredential` (policy in
  `internal/hub/credentials.go`) plus `Service.hostForRef` glue.
  Still no call sites switched over — that is Phase 4. Existing
  integration suite stays green.
- 2026-04-23 — Phase 4 complete: every hub→host GitRef-forwarding
  call site (createSession, activateBackend, reactivation in api.go,
  ListAgents/Models, all worktree pass-throughs, background primary
  agent refresh) goes through `hostForRef`. Inline ssh→https rewrite
  block in `createSession` deleted. Daytona bug now fixed across all
  paths, not just session create. Credential return value is `_` for
  one phase — Phase 5 plumbs it through hostclient.
- 2026-04-23 — Phase 5 complete: `internal/host/client/{worktree,backend}.go`
  every method gains an `auth agent.GitCredential` parameter, encoded
  into the JSON request body alongside the GitRef. `agent.StartRequest`
  gains an `Auth` field so session-create carries the credential too.
  `/agents` and `/models` migrated from GET-with-query to POST-with-JSON
  body — credential material has no business in URL strings (and the
  shape stays uniform regardless of credential kind). Host-side mux
  handlers validate the credential when present (zero-Kind tolerated
  for one more phase; Phase 6 makes it required for clone paths). All
  hub call sites that previously discarded `cred` with `_` now pass it
  through. `go test ./...` green.
- 2026-04-23 — Phase 6 complete: `internal/git/git.go` `Clone` rewritten
  to take `(*GitEndpoint, GitCredential)` and dispatch on `cred.Kind`.
  New `internal/git/askpass.go` writes mode-0700 temp scripts so HTTPS
  basic/token secrets never appear in argv or `os.Environ()`. Defense-
  in-depth invariant `authMatchesEndpoint` rejects mismatched kind/
  protocol pairs (e.g. `ssh_agent` for HTTPS, anonymous for SSH) before
  invoking git. Host-side `Service.workDirFor` and all five public
  GitRef-taking methods (`CreateSession`, `ListAgents`, `ListModels`,
  `ListBranches`, `ResolveWorktree`, `RemoveWorktree`, `MergeBranch`)
  now thread `GitCredential` through. The clone branch refuses if hub
  forgot to populate `ref.Endpoint` (no host-side go-git import) and
  refuses `ssh_agent` when `s.id != HostLocal`. New
  `GitEndpoint.CloneURL()` differs from `String()` only for `file://`
  where the trailing `.git` would refer to a nonexistent on-disk path.
  Mux handlers stop discarding `req.Auth`. Tests updated to construct
  endpoints/credentials directly; new `askpass_test.go` round-trips
  the script with awkward secrets (spaces, single quotes, $/\`/").
  `go test ./...` green.
- 2026-04-23 — Phase 8 prep (commit `8f44f57`): extracted parser to
  neutral `internal/gitendpoint` package so `internal/store` (which
  cannot import `internal/hub`) can use it during migration backfill.
  `ParseGitEndpoint` → `gitendpoint.Parse`. Sole import of `go-git/v5`
  stays here; `agent` package remains dep-free.
- 2026-04-23 — Phases 7+8 merged and complete: SQLite migration v16
  explodes `git_remote_url` into `git_endpoint_{protocol,user,host,port,path}`
  on `sessions`; `primary_agents` recreated from scratch (derivation
  cache, no backfill). Migration hard-fails with row-id list on parse
  errors. `gitRefColumns` helper + `gitRefToColumns`/`gitRefFromColumns`
  bridge in `store.go`; all queries (`LoadSessions`, `UpsertSession`,
  `LoadPrimaryAgents`, `UpsertPrimaryAgents`, `KnownAgentTargets`,
  `FindByExternalID`) on the new columns. RemoteURL is now derived
  read-time from `ep.String()` which canonicalises scp form to
  `ssh://` URL form — tests that compared literal strings switched to
  `agent.RepoKey` equivalence (with both sides parsed) instead.
  Production ingress sites (`clankcli`, `tui/inbox.go`,
  `tui/sessionview_compose.go`, `hub/sessions.go` discovery) populate
  `Endpoint` alongside `RemoteURL` via `gitendpoint.Parse`. TUI drops
  unparseable refs; clankcli hard-fails; discovery silently skips.
  **Voice intentionally untouched** per user direction (structural
  issues to address separately). The `RemoteURL` field on `GitRef` is
  retained for one more phase as a transitional bridge — Phase 9
  removes it. `go test ./...` green across all packages.
