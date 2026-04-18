# Hub/Host Refactor — Principal Engineer Code Review

**Reviewer perspective:** unfiltered, skeptical. Goal is to call out drift,
shortcuts, and rationalizations between `hub_host_refactor.md` and the code
that actually landed. Treat this as a punch-list, not as a pat on the back.

**TL;DR.** Phase 0 is real. Phase 1 is *partial* (no supervisor in any
meaningful sense). Phase 2 is *partial* (no `internal/hub/mux/`, voice
landed in a third top-level package, Hub still type-asserts Host
internals). Phase 3 is in flight but the legacy path-shaped wire and
`SessionInfo` fields haven't been retired in lockstep, which means the
"path-free" claim is false today. Three load-bearing decisions in the
design doc have been quietly inverted in code with after-the-fact
justifications written into godoc rather than the design doc itself.

The implementing agent has been honest about deviating, but the pattern
is "rationalize and continue" instead of "course-correct or amend the
spec." That has to stop before Phase 3 goes further, otherwise we end
up with a refactor whose intent only lives in the head of one agent.

---

## 1. Major deviations from the design

### 1.1 `internal/hub/mux/` does not exist

The doc's Phase 2 "done when" criterion is that Hub HTTP wiring lives in
`internal/hub/mux/`, symmetric with `internal/host/mux/`. In reality:

- `internal/host/mux/` is properly extracted (`mux.go`, `sessions.go`,
  `repos.go`, `catalog.go`).
- The Hub's HTTP handlers hang directly off `(s *Service)` in
  `internal/hub/sessions.go` (972 lines), `events.go`, `repos.go`,
  `voice.go`, with `routes.go` doing the wiring.

`sessions.go` mixes domain logic, persistence, and HTTP marshaling in
one file. This is the exact thing the refactor was supposed to dissolve.
Phase 2 is not done. Calling it done in the doc is misleading.

**Required:** extract `internal/hub/mux/` mirroring the host side. The
domain methods stay on `hub.Service`; the mux package owns the
`http.ServeMux` and the request/response shape. Do not skip this.

### 1.2 Voice landed in `internal/voice/`, not in `hub.Service`

The design says voice is "Hub-resident" and explicitly shows it
migrating into the Hub package in Phase 2. What actually landed is a
peer top-level package `internal/voice/` (`voice.go`, `bridge.go`,
`tools.go`, `audio_ws.go`).

This is arguably *better* layering — voice has enough surface to deserve
its own package — but the design doc was never updated to reflect it,
and the Hub still has `internal/hub/voice.go` glue plus a
`hubToolProvider`. So now we have voice in two places.

**Required:** either (a) move it back under `internal/hub/voice/` so
it's still hub-owned but cohesive, or (b) update the design doc to
ratify `internal/voice/` as a peer package and define exactly what
remains in `internal/hub/voice.go`. Pick one, write it down.

Human comment: Possibly keeping voice as isolated package, but hub uses the client and registers its tool capabilities. Can keep tool regstering in voice package for now if it's easier.

### 1.3 `hostclient.Client` is an interface — Decision #3 violated

The doc's Decision #3 is unambiguous: no Host Go interface, concrete
types only. The code has an interface (`internal/host/client/client.go`)
with two implementations (`HTTP`, `InProcess`). The package godoc
defends the deviation:

> Tests need a way to inject a fake or in-process Host without spinning
> subprocess + HTTP. The interface gives us that without mocking — the
> InProcess impl IS the production code path for the local case.

This is wrong on both counts:

1. **`InProcess` is not the production code path.** Production
   (`cmd/clankd`) always spawns `clank-host` via the supervisor and
   uses `hostclient.HTTP` against the Unix socket. `InProcess` is only
   used in tests. The "two processes always" rule from the design is
   absolute; `InProcess` undercuts it.
2. **`InProcess` is a soft mock.** It wraps `*host.Service` and bypasses
   the wire. Tests using it never exercise serialization, never catch
   wire-shape regressions, and let path-bearing fields like
   `host.CreateInfo.{ProjectDir, WorktreeDir}` survive precisely because
   they're never re-encoded. AGENTS.md says "NEVER mock dependencies."
   This is a mock dressed as a "thin adapter."

**Required:** delete `inprocess.go`. Delete the `Client` interface and
let the Hub depend on `*hostclient.HTTP` directly. Migrate any test
using `InProcess` to `httptest.NewServer(hostmux.New(svc))` +
`hostclient.NewHTTP(server.URL)`. That's the integration test the
AGENTS.md guidance points at.

Human comment: ok, my mistake allowing the client interface. Please update.

### 1.4 Supervisor doesn't supervise

`internal/cli/daemoncli/host_supervisor.go` is 151 lines and openly
admits at line 18: `// Phase 1 limitation: no restart-on-crash.` What
exists today is "spawn once, signal on shutdown." Specifically:

- No restart loop. If `clank-host` dies, the Hub holds a dead client
  and every subsequent request fails until the user restarts `clankd`.
- No exponential backoff (doc says 1→2→4…→30s).
- No health-probe loop.
- 5s grace before SIGKILL (doc says 10s).
- `os.Interrupt` is used as "SIGTERM-equivalent" — fine on Unix but the
  comment papers over a real portability question worth resolving.
- Race: the supervisor `os.Remove`s the socket and then `clank-host`
  also `os.Remove`s it before binding. Two writers, no coordination.

This is the **highest-risk gap** in the refactor. The doc's open
Failure Mode #3 ("host crash") assumes the supervisor restarts the
child. Today, nothing does. Until this is fixed, "two processes always"
is a reliability downgrade compared to the monolith, not an upgrade.

**Required:** implement the supervisor properly. Minimum: restart-loop
goroutine, exponential backoff with cap, no-restart on clean
signal-driven exit, 10s SIGTERM grace, single owner of socket file
(supervisor removes; child must not).

Human comment: This was agreed upon as fine and noted for later. Don't do this yet.

### 1.5 Hub still type-asserts Host internals

`internal/hub/agents_models.go` imports `internal/host` and does a
concrete type-assertion to `*host.OpenCodeBackendManager` for the
warm-cache path. This violates Decision #6 ("BackendManagers live on
the Host"). The Hub is reaching across the boundary to grab a concrete
type. If this is a perf optimization, it has to move behind a method on
`hostclient.HTTP` (e.g. `WarmAgents(ctx)` returning the cached slice
over the wire). If it's not load-bearing, delete it.

Human comment: Yes I'm not sure why it's even in the hub to begin with (but im not an expert)

### 1.6 Phase 3 is half-done, in the worst way

`StartRequest` got the new path-free fields (`HostID`, `RepoRemoteURL`,
`Branch`). Good. But:

- `SessionInfo` still carries `ProjectDir`, `WorktreeDir`, `ProjectName`.
- Many Hub paths still use `ProjectDir` as session identity — e.g.
  `serverURLs[info.ProjectDir]` and the fork flow copies these fields.
- `POST /sessions/discover` still takes `{project_dir}` on the wire.
- `host.CreateInfo` ships `{ProjectDir, WorktreeDir}` back over the
  wire as part of `CreateSession`'s response (see the `Client` interface
  godoc actively documenting this leak as if it were a feature).
- `git.RemoteURL` is called inside the Hub during discovery — Hub
  touching git is exactly what Decision #6 forbids.

Phase 3's whole point is killing path-as-identity. Today, paths are
still identity, just with new fields layered on top. This is the worst
intermediate state: two parallel identity systems live in the same
struct, and call sites pick whichever is convenient. Pick a deadline
and rip the path fields out.

Human comment: I think the agent is perhaps aware of some of the issues here, but yes I'm not fully satisfied with the direction we're going here. There may have been a flaw in the initial design. I want to discuss it. There's still a lot of weird places where we pass a SECOND argument workDir just to "move it out of the StartRequest wire struct" but that's NOT how you do that separation of concerns in Golang. Either you have a pure domain-struct or you have a wire-purpose struct that knows how to encode/decode, the current StartRequest seems to mix this concern by using it in both BackendManager's CreateBackend but also as a wire-transferable struct (it has json tags). I don't like how "workDir" is slapped on as a second parameter: First of all, because it looks out of place (usually you have a single struct options, or the flat param you pass is the first ones, and it has to be an obviously important and mandatory one (otherwise it would be in the "options struct")), but secondly also because I'm not even sure this should be a top level argument that anyone cares about AT ALL. Rather, the host can resolve this entirely themselves when they receive a repo+worktreebranch param, by just storing that translation on disk somewhere. I think a lot of the problems we have currently stem from the fact that our Host layer currently doesn't have any external storage, and thus is offloading responsibilities to a lot of other layers instead of properly taking ownership.

### 1.7 `RegisterRepoOnHost` punches paths through the wire

The TUI calls `client.RegisterRepoOnHost(ctx, hostID, ref, root)` from
`internal/tui/inbox.go`, sending a host-side filesystem path over the
wire. This is a legitimate bootstrap (shell context → API identity, the
same role `ResolveRepo(cwd)` plays), but the doc claims "no paths on
the wire" full stop. Either:

- Carve out `RegisterRepo` and `ResolveRepo` as the two designated
  shell-context-bridging endpoints in the doc, with a clear rule
  ("paths only flow client→host as bootstrap, never host→client, never
  in steady-state ops"), **or**
- Push the path resolution server-side and require clients to send only
  `RepoRef`, having the host walk its known roots.

The first is more honest. Either way, the doc has to stop pretending.

Human comment: RegisterRepoOnHost even existing seems weird, why isnt this just something the host handles automatically? I'm saying when requesting a session to start on the host, it receives a git repo url it hasn't seen before and thus clones it. If it receives a branch it hasn't seen before, similarly for branches (out of scope for now). I've also always been skeptical of ResolveRepo. Same with RepoRef tbh (seems overly complicated, doesnt deserve own struct, just have them as fields in the request). So I'd rather we model things in another way, could perhaps be related to the above comment in 1.6. I'm just seeing a lot of code-smells related to this git repo and filesystem stuff.

---

## 2. Smaller issues

- **Log file still named `daemon.log`.** Half-rename. Either commit to
  `clankd.log` or document why the legacy name stays.
- **`s.hostClient` shortcut field in `hub.Service` doubles
  `s.hosts["local"]`.** Almost every call site uses the shortcut and
  bypasses the catalog. Either delete the catalog (it's vestigial) or
  delete the shortcut and force everyone through the catalog. As-is
  it's two sources of truth for the same thing.
- **`host.Service.Run` is misleading.** Godoc implies it ties lifetime
  to `ctx`; the body just initializes and returns. Rename to `Init` or
  actually block on `ctx.Done()`.
- **`host.Service.Shutdown` not idempotent under concurrent
  `CreateSession`.** No mutex around the registry teardown vs. inserts.
  Easy to hit during signal-triggered shutdown.
- **Socket-file ownership race** (see 1.4). Single owner.
- **`host.CreateInfo` over the wire** (see 1.6). Path leak.
- **`hostclient` package godoc is currently a defense brief for
  Decision #3 deviation.** Once the interface is gone, delete the
  defense.

---

## 3. Pattern: rationalize-in-godoc instead of amend-the-doc

The three biggest deviations (1.2 voice, 1.3 hostclient interface, 1.6
path fields surviving) all share a tell: the deviation is documented in
a godoc comment near the offending code, not in `hub_host_refactor.md`.
That makes the design doc actively misleading — readers think Decision
#3 holds, then trip over a Go interface in the next file.

**Process fix:** any deviation from the design doc must edit the design
doc in the same change. If you can't justify the edit to the design
doc, you can't justify the deviation. Godoc is for explaining the
*code*; the design doc is the contract.

---

## 4. Recommended path forward

> **SUPERSEDED by §7 below.** The original sketch in this section was
> revised during discussion; §7 is the approved, executable plan. Kept
> here only for context on how we got there.

In strict order. Do not start the next item until the previous lands.

1. **Supervisor.** Real restart loop, exponential backoff (1→2→4…→30s),
   no-restart on clean signal exit, 10s SIGTERM grace, single socket
   owner. Test by killing `clank-host` mid-session and asserting the
   next Hub request succeeds.
2. **Extract `internal/hub/mux/`.** Mirror host side. Move every HTTP
   handler off `*hub.Service`; service methods become pure domain.
   Split `sessions.go` into per-endpoint files.
3. **Delete `hostclient.InProcess` and the `Client` interface.** Hub
   depends on `*hostclient.HTTP`. Tests use `httptest.NewServer` over
   `hostmux.New(svc)`. Update the design doc to reaffirm Decision #3.
4. **Decide voice's home.** Either move `internal/voice/` to
   `internal/hub/voice/`, or amend the design doc to ratify the peer
   package. Document the split between `internal/hub/voice.go` glue
   and the voice package.
5. **Finish Phase 3.** Drop `ProjectDir`/`WorktreeDir`/`ProjectName`
   from `SessionInfo` and from `host.CreateInfo`. Hub derives display
   paths on demand via a `GET /hosts/{id}/repos/{repoID}/worktrees/{branch}`
   call when (and only when) it needs to render one. `POST
   /sessions/discover` takes `{host_id, repo_id, branch}`. Move
   `git.RemoteURL` calls out of the Hub.
6. **Carve out `RegisterRepo` / `ResolveRepo` in the design doc** as
   the two shell-context bridges; reaffirm "no paths on the wire" for
   every other endpoint.
7. **Then** resume Phase 3 follow-ons (host catalog persistence, etc).

---

## 5. Opportunities (per AGENTS.md)

- **Reliability.** Until #1 above lands, recommend adding a Hub
  startup-time assertion that pings `clank-host` once per second and
  panics if it disappears. Loud failure beats silent dead-client today.
- **Tests.** No integration test currently spans `clankd → unix socket
  → clank-host → real openagent backend`. Add one before Phase 3
  finishes; otherwise wire-shape regressions land silently.
- **DX.** Splitting `internal/hub/sessions.go` (972 lines) is overdue
  on its own merits, independent of the mux extraction.
- **Security.** Confirm `~/.clank/host.sock` is created with 0600
  perms; it currently inherits umask. The Hub→Host channel is
  unauthenticated and assumes filesystem perms are the boundary.

---

## 6. What's actually good

To not be a one-note critic:

- `host/mux/` extraction is clean and is the template the Hub side
  should copy.
- `RepoRef` / `RepoID` typing in `internal/host/types.go` is solid and
  the right primitive to build Phase 3 around.
- `ResolveRepo(cwd)` as a permanent shell→API bridge is the right call
  and well-named.
- `StartRequest` shape is correct; the problem is just that
  `SessionInfo` and `CreateInfo` haven't caught up. (Human comment: I think im partially responsible for the sessioninfo still including projectdir, because we wanted TUI to be able to open native cli's still - but on second thought, it only needs the claude/opencode session ID for that, not directory info, right?)
- The fact that the implementing agent left `// Phase 1 limitation`
  comments instead of hiding them is good — that's how we knew where to
  look. Don't stop doing that; just escalate them to design-doc edits
  rather than leaving them as TODOs in code.

---

## 7. Approved plan (replaces §4)

This is the contract. Step ordering is strict — do not start step N+1
until step N lands and is tested.

### 7.1 Decisions locked

| Topic | Decision |
|---|---|
| Adoption mechanism | Option A: client sends `Dir` cwd hint in `CreateSession`; Host adopts implicitly. No `RegisterRepoOnHost`, no `ResolveRepo` Hub endpoint. |
| Local-only adoption | Local Host never auto-clones; passing `Dir` is required for unknown repos unless `AllowClone=true`. |
| Cloning | Only when client sets `AllowClone=true` (intended for remote hosts). Cloned into `~/.clank/repos/<sanitized-gitref>/`, host-managed. |
| Host storage | SQLite at `~/.clank/host.db`. Real SQLite in tests, no mocks. |
| `HostID` → `Hostname` | Renamed. |
| `RepoID` / `RepoRef` | Removed. Replaced by `GitRef` struct (see §7.2). |
| `Branch` → `WorktreeBranch` | Renamed everywhere. Restoring earlier intent. |
| `WorkDir` on the wire | Cut. Yagni; no consumer. Power-user escape hatch can be added later. |
| `BackendInvocation` DTO | Introduced for `BackendManager.CreateBackend`. Minimal: `{WorkDir, ResumeExternalID}`. |
| `StartRequest` vs DTO | Wire struct stays, BackendManager gets a separate small DTO. |
| `SessionInfo.ProjectDir`/`WorktreeDir`/`ProjectName` | Removed. TUI uses `ExternalID`+`Backend` for native-CLI shell-out, derives display name from `GitRef`. |
| `host.CreateInfo` | Removed. Path leak. |
| `hostclient.Client` interface | Deleted. Hub depends on `*hostclient.HTTP`. Tests via `httptest.NewServer(hostmux.New(svc))`. |
| Voice | Stays as `internal/voice/` peer package. Ratified in design doc. |
| Sub-client refactor | Both `internal/hub/client/` (TUI→Hub) and `internal/host/client/` (Hub→Host) get chained shape: `.Host(name).Repo(ref).Worktree(branch).X()`. |
| Auto branch provisioning | Out of scope this round. `// TODO(branch-auto-provision)` left where applicable. |
| Clank-session-ID ownership | Stays Hub-owned. `// TODO(brainstorm): consider moving to Host` comment only. |

### 7.2 `GitRef` (replaces `RepoRef`/`RepoID`)

```go
type GitRefKind string

const (
    GitRefRemote GitRefKind = "remote"
    GitRefLocal  GitRefKind = "local"
)

type GitRef struct {
    Kind      GitRefKind `json:"kind"`
    URL       string     `json:"url,omitempty"`        // required when Kind=remote
    Path      string     `json:"path,omitempty"`       // required when Kind=local; canonical absolute path
    CommitSHA string     `json:"commit_sha,omitempty"` // optional pinning hint, currently advisory
}

func (g GitRef) Validate() error
func (g GitRef) Canonical() string
func (g GitRef) Equal(other GitRef) bool
```

Validation rules:
- Empty `Kind` → reject.
- `Kind=remote` requires non-empty `URL`; rejects non-empty `Path`.
- `Kind=local` requires absolute `Path`; rejects non-empty `URL`.
- `CommitSHA` optional in both kinds, advisory only.

Canonicalization:
- remote: lowercased, scheme-normalized, `.git`-stripped (e.g. `github.com/acksell/clank`).
- local: absolute path as-is, no case-folding (Unix). Windows TBD.
- Used as `repos.git_ref` PK and as URL path component (URL-encoded).

`Equal(other)` defined as `Canonical() == other.Canonical()`.

Unit tests required for: validate × every required-field permutation;
canonical scheme/case/`.git` normalization; abs-path enforcement;
canonical idempotence; equality reflexivity & symmetry.

### 7.3 `StartRequest` (final wire shape)

```go
type StartRequest struct {
    Backend         BackendType    `json:"backend"`
    Hostname        string         `json:"hostname,omitempty"`     // default "local"
    GitRef          GitRef         `json:"git_ref"`
    WorktreeBranch  string         `json:"worktree_branch,omitempty"`
    Dir             string         `json:"dir,omitempty"`          // cwd hint for adoption
    AllowClone      bool           `json:"allow_clone,omitempty"`  // permit clone of unknown remote
    Prompt          string         `json:"prompt"`
    SessionID       string         `json:"session_id,omitempty"`   // backend-external session ID for resume
    TicketID        string         `json:"ticket_id,omitempty"`
    Agent           string         `json:"agent,omitempty"`
    Model           *ModelOverride `json:"model,omitempty"`
}
```

Validation (unit-tested):
- `Backend` required and known.
- `GitRef.Validate()` must pass.
- `Prompt` required.
- `Dir` and `AllowClone=true` mutually exclusive (clone vs adopt).
- `AllowClone=true` and `GitRef.Kind=local` → reject (can't clone a local-path ref).

Doc comment on `SessionID` cross-references the three session-ID
concepts (clank session ID, backend external ID, this resume hint) to
prevent future confusion.

### 7.4 `BackendInvocation` DTO

```go
// agent.BackendInvocation is the host-resolved, backend-only view of a
// session start. Constructed inside host.Service.CreateSession after
// repo/worktree resolution; never on the wire.
type BackendInvocation struct {
    WorkDir          string // resolved by Host from GitRef + WorktreeBranch (+ Dir for local)
    ResumeExternalID string // backend's own session ID for resume; empty = new session
}

type BackendManager interface {
    CreateBackend(ctx context.Context, inv BackendInvocation) (SessionBackend, error)
    // ...
}
```

Trim is intentional — `CreateBackend` only spins the backend server up;
`Prompt`/`Agent`/`Model` go through `SessionBackend` after creation.

### 7.5 `CreateSession` resolution algorithm

`host.Service.CreateSession(req StartRequest) → (SessionBackend, error)`:

1. `key := req.GitRef.Canonical()`.
2. Lookup `key` in `repoStore`. If found:
   - If `req.Dir != ""` and `req.Dir != stored.RootDir` → error: "host already knows this GitRef at `<stored>`; rebind via `POST /repos/{gitref}/rebind` if you moved it".
   - Else use `stored.RootDir`.
3. Not found, `req.GitRef.Kind == local` → adopt directly: `RootDir = req.GitRef.Path`. Verify it's a git repo. Persist.
4. Not found, `req.GitRef.Kind == remote`, `req.Dir != ""` → adopt: verify `req.Dir` is a git repo whose remotes (any of them) canonicalize to `key`. Persist `(key → req.Dir)`. Mismatch → error.
5. Not found, `req.GitRef.Kind == remote`, `req.AllowClone` → clone `req.GitRef.URL` into `~/.clank/repos/<sanitized-key>/`. Persist.
6. Else → error: "repo unknown to host; pass `dir` to adopt or set `allow_clone=true` to clone".

Worktree resolution: call `git worktree list` on `RootDir` (already
implemented as `git.ListWorktrees` / `git.FindWorktreeForBranch`). Git
is the source of truth for worktrees; we don't cache them. If
`WorktreeBranch == ""` use repo root. If set but no worktree exists,
error (auto-provision deferred). Then build `BackendInvocation` and
invoke the backend manager.

### 7.6 Storage schema

The host's repo registry persists to a single JSON file. SQLite was the
original plan but is overkill for a `(canonical → rootDir)` map of <50
entries that is read once at startup and rewritten on register/delete.
Atomic durability uses `github.com/google/renameio/v2` (temp file +
fsync + rename, with parent-dir fsync on Linux).

File layout (versioned envelope so future migrations are a `switch`):

```json
{
  "version": 1,
  "repos": [
    {
      "git_ref":    "github.com/acksell/clank",
      "kind":       "remote",
      "root_dir":   "/Users/me/work/clank",
      "created_at": 1737200000
    }
  ]
}
```

Worktrees are intentionally **not** persisted. Every existing host
operation (`listBranches`, `resolveWorktree`, `removeWorktree`,
`mergeBranch`) already shells out to git for ground truth; a persisted
mirror would only let us lie when the user mutates worktrees outside
clank. `git worktree list` is sub-millisecond on local disk.

Real filesystem in tests, isolated tmp file per test.

### 7.7 Sub-client API shape

Both `internal/hub/client/` and `internal/host/client/`:

```go
hub.Host(hostname)                                      // *HostClient
hub.Host(hostname).Repo(gitRef)                         // *RepoClient
hub.Host(hostname).Repo(gitRef).Branches(ctx)
hub.Host(hostname).Repo(gitRef).Worktree(branch).Resolve(ctx)
hub.Host(hostname).Repo(gitRef).Worktree(branch).Remove(ctx, force)
hub.Host(hostname).Repo(gitRef).Worktree(branch).Merge(ctx, msg)
hub.Host(hostname).Backend(backend).Agents(ctx, hint)
hub.Host(hostname).Backend(backend).Models(ctx, hint)
hub.Host(hostname).Backend(backend).Discover(ctx, hint)
hub.Sessions().Create(ctx, req)
hub.Sessions().Get(ctx, id)
hub.Sessions().Subscribe(ctx)
```

Sub-clients carry bound hostname/gitref/branch as private fields. Leaf
methods take only the args genuinely scoped to that op.

### 7.8 Execution sequence (strict)

1. **Supervisor.** Restart loop, exponential backoff (1→2→4…→30s), no-restart on clean signal-driven exit, 10s SIGTERM grace, single socket owner. Integration test: SIGKILL `clank-host` mid-session, assert next request succeeds.
2. **Extract `internal/hub/mux/`** mirroring `internal/host/mux/`. Move HTTP handlers off `*hub.Service`. Split `sessions.go`. Service methods become pure domain.
3. **[DONE] Delete `hostclient.InProcess` + `Client` interface.** Hub depends on `*hostclient.HTTP`. Tests migrate to `httptest.NewServer(hostmux.New(svc))`. Decision #3 deviation (§1.3) is closed: `internal/host/client/` now exposes only `*HTTP`, plus a one-line compile assertion that `*httpSessionBackend` satisfies `agent.SessionBackend`.
   - Hub-side cleanup change: `hub.Service.shutdown` now calls `stopActiveBackends()` to release sessions over HTTP before `wg.Wait`, replacing the deleted `s.host.Shutdown()` cascade. Without it the SSE relay goroutines deadlock when the host plane outlives hub (test fixture or production host supervisor).
   - Host-side wire fix: `POST /sessions/{id}/fork` no longer rejects empty `message_id` — empty means "fork from start" and the in-process path always allowed it. The host mux validation was a regression introduced when handlers moved off `*host.Service`.
   - Test fixture: `internal/hub/service_test.go` builds an httptest-fronted host on demand via `ensureHostFixture`; `internal/cli/daemoncli/server_test.go` does the same inline. No test mocks the host transport.
4. **[DONE] Naming + types sweep.** `HostID→Hostname`, drop `RepoID`/`RepoRef`, introduce `GitRef` struct, `Branch→WorktreeBranch`. Pure rename + struct introduction. Unit tests for `GitRef.{Validate, Canonical, Equal}`.
   - Step 1 (supervisor) skipped per user; tracked under separate shared-binary plan.
   - JSON-tag changes shipped as part of this step (per §7.1 we own all clients): `agent.SessionInfo.Branch` → `WorktreeBranch` with tag `worktree_branch`; `agent.StartRequest.Branch` → `WorktreeBranch` with tag `worktree_branch`. Hostname tag unchanged in this step.
   - `host.Repo` field `ID host.RepoID` deleted; identity is `Repo.Ref.Canonical()`. `host.Service.Repo` lookup is typed: `Repo(GitRef)`. All `*ByRepo` host service / mux methods take `gitRef string` (canonical). `RegisterRepo*` takes `host.GitRef` (typed; needs Kind/URL).
   - HTTP route params: hub mux `{repoID}` → `{gitRef}`; host mux `{id}` → `{ref}`. URL-encoded canonical strings (`url.PathEscape`) round-trip cleanly through Go's `net/http.ServeMux` `r.PathValue`.
   - Wire body for `POST /hosts/{hostname}/repos` (`registerRepoOnHostRequest`) keeps `{remote_url, root_dir}` for now and rejects non-remote `GitRef.Kind` at the hub client. Full local-kind support arrives with §7.5 (step 6).
   - `host.GitRef.Canonical()` is whitespace-trimmed via `strings.TrimSpace`; the `Validate`/`Canonical` paths reject `://invalid`-style URLs that fail `url.Parse` to keep the canonical form deterministic.
5. **[DONE] Host persistent storage.** `repoStore` w/ JSON file per §7.6. Wire `cmd/clank-host` to open it. Behavior unchanged (write-through from existing in-memory path).
   - **JSON, not SQLite.** A `(canonical → rootDir)` map of <50 entries read once at startup and rewritten on mutation does not justify SQLite. JSON file via `github.com/google/renameio/v2` for atomic write (temp + fsync + rename, parent-dir fsync on Linux). Stays human-readable: `cat ~/.clank/host/host.json`. Hub's session store keeps SQLite — different cardinality, different access pattern.
   - Versioned envelope (`{"version": 1, "repos": [...]}`) so future format changes are a `switch` on the version field. Pinned in `TestStore_FileShape`.
   - Schema persists only the `repos` table equivalent. Worktrees are git's domain; see §7.6 rationale.
   - `RepoStore` is an **interface** in `internal/host` (`SaveRepo`, `ListRepos`, `DeleteRepo`); `internal/host/repostore.Store` is the JSON-file implementation. Tests can supply in-memory implementations. Host owns canonical-derivation rules; the store is dumb storage.
   - `RegisterRepo` is in-memory-first, store-second with **rollback on store failure**: if `SaveRepo` errors, the in-memory map is reverted to the prior state (or the entry deleted) so callers never see a registration that won't survive restart.
   - `host.New` preloads existing rows at construction time. A load failure logs a warning and proceeds; a successful preload logs `loaded N persisted repos`.
   - `cmd/clank-host` adds `--db` flag (kept the name despite no longer being a DB; renaming the flag is cosmetic and would break operators); default is `<socket-dir>/host.json` (co-located with the Unix socket since hub owns the socket dir's lifecycle).
   - Malformed file on `Open` is a hard error: refusing to start beats silently zeroing out persisted state. Missing file is empty (cold start).
   - `DeleteRepo` on an unknown canonical is a no-op, not an error: callers can issue safe retries.
   - For `GitRefRemote` rows we store only the canonical string; reconstruction sets `GitRef{Kind: GitRefRemote, URL: canonical}` because `Canonical()` is idempotent. For `GitRefLocal`, `URL` is empty and `Path == RootDir` by construction.
   - Tests: 11 unit tests in `internal/host/repostore` (real filesystem tmp files, incl. file-shape pin, missing-file, malformed-file, idempotent-delete) + `TestService_RepoStore_PersistsAcrossRestart` in `internal/host` exercising the full register → shutdown → reopen → preload → CreateSession round-trip.
6. **Implicit adoption / clone in `CreateSession`** per §7.5. Add `POST /repos/{gitref}/rebind`. Delete `RegisterRepoOnHost` from wire, hub→host client, TUI. Delete `ResolveRepo` Hub endpoint; CLI/TUI inlines `git rev-parse --show-toplevel` + `git remote get-url origin`. Integration tests: adopt-local, adopt-remote-with-Dir, clone-with-AllowClone, rebind, mismatch errors, restart-and-rediscover-from-SQLite.
7. **Sub-client refactor** per §7.7. Walk all call sites.
8. **`StartRequest` finalization + `BackendInvocation` DTO** per §7.3, §7.4. Add `Dir`/`AllowClone` to wire. Strip `ProjectDir`/`WorktreeDir`/`ProjectName` from `SessionInfo` and `host.CreateInfo`. Migrate TUI off deleted fields. `BackendManager.CreateBackend` takes only the DTO.
9. **Voice** stays in `internal/voice/`. Ratify in design doc.
10. **Design doc updates** ship in the same commit as each step that changes a load-bearing decision. No more godoc-rationalizations.

Cross-cutting (AGENTS.md):
- Every behavior change ships with a test.
- No mocks. `httptest.Server`+`hostmux`, real SQLite, real git in temp dirs.
- Add the missing end-to-end `clankd → unix socket → clank-host → real backend` integration test before step 8 lands.
- Every bug found gets a regression test before fix.
