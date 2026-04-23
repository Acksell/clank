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
- **Status:** [ ] not started

### Phase 5 — Host-client signatures
- `internal/hostclient/*.go` — every method that takes a `GitRef` adds an `auth GitCredential` parameter; JSON-encodes both into the wire request.
- **Status:** [ ] not started

### Phase 6 — Host-side consumption
- `internal/host/dto.go` (or wherever) — request DTOs gain `Auth GitCredential`.
- `internal/host/service.go` — clone path takes `(endpoint, credential)`. Switches on `credential.Kind`.
- `internal/git/git.go` — `Clone` signature takes `endpoint + credential`. Routes:
  - `anonymous` → plain https URL
  - `https_basic` → askpass script
  - `ssh_agent` → scp-form URL; refuse if `CLANK_HOST_KIND` indicates remote
- New: `internal/git/askpass.go` — temp-file helper, mode 0700, cleanup closure.
- Update `internal/git/git_test.go`. Add askpass test using local file:// remote.
- **Status:** [ ] not started

### Phase 7 — TUI / voice / clankcli ingress
- `internal/tui/inbox.go:172`, `internal/tui/sessionview_compose.go:63,237`, `clankcli/clankcli.go:141`, `voice/tools.go:340` — call `hub.ParseGitEndpoint` instead of stuffing raw string.
- Refusal on parse error per Q2.
- Remove the deprecated `RemoteURL string` field from `GitRef`.
- **Status:** [ ] not started

### Phase 8 — SQLite migration
- New migration: add `git_endpoint_protocol`, `git_endpoint_host`, `git_endpoint_port`, `git_endpoint_user`, `git_endpoint_path`, `git_remote_url` (derived).
- Backfill via Go: read `remote_url`, parse, write new cols. Hard-fail on parse error and prompt user.
- Drop legacy `remote_url` column.
- Update `internal/store/*.go`.
- **Status:** [ ] not started

### Phase 9 — Cleanup & verification
- Delete `internal/hub/sessions_remote_rewrite_test.go` (salvage helpers if needed).
- Audit: `git grep -i 'RemoteURL'` returns zero hits in non-test code.
- `go test ./...` green.
- Manual smoke: register Daytona, public-repo session works; private repo errors clearly.
- **Status:** [ ] not started

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
