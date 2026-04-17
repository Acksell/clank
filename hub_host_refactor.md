# Hub / Host Refactor Design

## Purpose

This document defines the long-term architecture for Clank's remote-coding and mobile-continuation feature, plus a concrete phased implementation plan for getting there.

It is intended to be read standalone by a future implementation agent or engineer.

## Product Goal

Enable a user to:

- see all their coding sessions from their phone
- continue those sessions from their phone without their laptop
- transition smoothly between laptop and phone
- self-host the system easily
- support a future managed hosted service
- avoid wasteful sandbox spend (especially with BYO sandbox keys)

The first remote target is Daytona. The system is not coupled to Daytona.

## Executive Summary

The architecture is **two cooperating processes per machine**: a **Hub** (control plane) and a **Host** (execution plane). They always communicate over HTTP — Unix socket on the laptop, HTTPS over the public internet to remote hosts.

- The **Hub** (`clankd`) is what clients connect to. It owns the API surface, session registry, host catalog, event fanout, permission brokering, and routing. It does not touch git, filesystems, or agent processes.
- A **Host** (`clank-host`) is what runs the work. It owns BackendManagers, the repo cache, worktrees, and active sessions. It exposes an HTTP API the Hub calls.
- On the laptop, `clankd` supervises a local `clank-host` child over a Unix socket. On Daytona, `clankd` spawns a remote `clank-host` inside a Daytona machine and reaches it over HTTPS. **Same code path, different dialer.**
- The local hub is fully self-sufficient and never depends on a remote service.
- A **remote hub** (hosted by us or self-hosted) exists for mobile access and managed hosts. Local and remote hubs sync session metadata bidirectionally. **Deferred.**

## Decisions

1. **Two processes per machine**: Hub and Host. Always separate. `clankd` supervises a local `clank-host` child even on the laptop. — Process isolation is free; one transport keeps the codebase narrow; the laptop case isn't a special path that bit-rots.
2. **One transport: HTTP.** Unix socket locally (perms 0600), HTTPS remotely (bearer token issued at provision time). — One serialization story, one auth story split by location.
3. **No Host Go interface.** Hub holds a concrete HTTP client; Host holds a concrete service struct. — Single implementation; interface adds no value. Extract later if a second transport appears.
4. **Two API resources only**: Hosts and Sessions. Git ops are scoped under `/hosts/{id}/repos/{id}/`. — Users think in sessions; everything else is internal.
5. **Backends, agents, models are host+backend scoped.** No Hub-level aggregation. — Different backends have different shapes; lowest-common-denominator hides useful structure.
6. **BackendManagers live on the Host.** The Hub never holds one. — They manage processes; processes live where the filesystem is.
7. **Host providers are concrete packages**, not a `SandboxProvider` interface. Daytona = `internal/host/daytona/`, ~one package, ~few hundred LOC. — One concrete provider doesn't justify abstraction. Add interface when there are 2–3.
8. **Voice is a Hub-resident client.** It calls the same Hub API as TUI/mobile. — A true assistant has the user's full capability set.
9. **Local-first.** Laptop always runs its own hub; never depends on a remote hub.
10. **Session IDs are globally unique UUIDs.** — Required for hub-to-hub sync later.
11. **V1 scope**: BYO Daytona key via local hub only. Remote hub and mobile are deferred.

## Two API Resources

Hosts and Sessions. Repo caches, worktrees, and path resolution are internal to a Host — exposed only via host-scoped git endpoints, never as a separate "Workspace" resource.

## Hub HTTP API

Client-facing. Same surface for TUI, mobile, and (later) hub-to-hub proxying.

```
# Hosts
GET    /hosts                                              — list available hosts
GET    /hosts/{hostID}                                     — host info/status

# Backend / agent / model catalogs (per-host, per-backend)
GET    /hosts/{hostID}/backends                            — backends installed on host
GET    /hosts/{hostID}/backends/{backend}/agents
GET    /hosts/{hostID}/backends/{backend}/models

# Git ops scoped to a host's repo
GET    /hosts/{hostID}/repos
GET    /hosts/{hostID}/repos/{repoID}/branches
GET    /hosts/{hostID}/repos/{repoID}/worktrees
POST   /hosts/{hostID}/repos/{repoID}/worktrees
DELETE /hosts/{hostID}/repos/{repoID}/worktrees/{name}
POST   /hosts/{hostID}/repos/{repoID}/merge

# Sessions — the main resource
POST   /sessions                                           — start session (host, repo, branch, backend, agent, model, prompt)
GET    /sessions                                           — list across all hosts
GET    /sessions/{id}
POST   /sessions/{id}/message
POST   /sessions/{id}/abort
POST   /sessions/{id}/resume
POST   /sessions/{id}/fork
POST   /sessions/{id}/compact
GET    /sessions/{id}/messages
POST   /sessions/{id}/permissions/{permID}/resolve
GET    /sessions/discover                                  — find pre-existing sessions on hosts

# Event stream
GET    /events                                             — SSE of session events
```

**Routing rule**: every host-scoped route (`/hosts/{id}/...`) is forwarded by the Hub to the Host that owns it. Every other route (`/sessions/...`, `/events`) is implemented in the Hub itself, calling the host client only for the parts that touch the host's filesystem or processes.

## Host HTTP API

Hub-facing. The Hub is the only client; this surface is internal.

```
GET    /status

# Catalogs
GET    /backends
GET    /backends/{backend}/agents
GET    /backends/{backend}/models

# Repos / worktrees
GET    /repos
GET    /repos/{repoID}/branches
GET    /repos/{repoID}/worktrees
POST   /repos/{repoID}/worktrees
DELETE /repos/{repoID}/worktrees/{name}
POST   /repos/{repoID}/merge

# Sessions
POST   /sessions                                           — body is StartRequest; returns session metadata
GET    /sessions/discover
POST   /sessions/{id}/message
POST   /sessions/{id}/abort
POST   /sessions/{id}/resume
POST   /sessions/{id}/fork
POST   /sessions/{id}/compact
GET    /sessions/{id}/messages
POST   /sessions/{id}/permissions/{permID}/resolve

# Per-session event stream (Hub subscribes once per session)
GET    /sessions/{id}/events                               — SSE bridging SessionBackend.Events()
```

The Host API is stable enough that an older `clank-host` and newer `clankd` should interoperate within a major version (deferred to when actual versioning is needed).

## Package Layout

```
internal/hub/                 pkg hub        — hub.Service: registry, host catalog, fanout, permissions, routing
internal/hub/mux/             pkg hubmux     — hub.Service's HTTP server (handlers + ServeMux for clients)
internal/hub/client/          pkg hubclient  — HTTP client used by TUI / CLI / voice to talk to the Hub

internal/host/                pkg host       — host.Service: BackendManagers, repos, worktrees, sessions
internal/host/mux/            pkg hostmux    — host.Service's HTTP server (handlers + ServeMux for the Hub)
internal/host/client/         pkg hostclient — HTTP client used by hub.Service to talk to a host (Unix socket or HTTPS)

internal/host/daytona/        pkg daytona  — Daytona machine launcher (added in Phase 5)

internal/agent/                            — preserved (BackendManager, SessionBackend, Event)
internal/git/                              — preserved (used by host.Service)
internal/store/                            — preserved (Hub session registry)

cmd/clankd/                                — wires hub.Service + hub/mux; supervises a local clank-host child
cmd/clank-host/                            — wires host.Service + host/mux; binds Unix socket or TCP
```

Both `mux` packages own their `http.ServeMux` and contain only transport concerns (decode, call domain method, encode). All domain logic lives in `hub.Service` and `host.Service`. This keeps both Service types testable as pure Go without spinning up HTTP.

**Package naming**: the two `mux` directories use distinct package names (`hubmux`, `hostmux`) and the two `client` directories use distinct package names (`hubclient`, `hostclient`) to avoid ambiguity in importing code. When the doc says "the client" without qualification, it means whichever client is contextually relevant; in code the names disambiguate.

**Socket paths**: `~/.clank/hub.sock` for the Hub's client-facing listener (TUI, voice, CLI dial here); `~/.clank/host.sock` for `clank-host`'s Hub-facing listener on the laptop. Two distinct sockets — clients must never dial the host socket.

## Topology

### End-state topology (Phases 6–7)

This is the long-term picture. Phases 0–5 focus on the left-hand side only (laptop hub + optional remote `clank-host` on the user's own Daytona). Phases 6–7 add the right-hand side (remote hub, hub-to-hub sync, mobile).

```
                     ┌────────────────────┐       ┌────────────────────┐
                     │      Daytona       │       │      Daytona       │
                     │ User's own account │       │  Our own account   │
                     │  ┌──────────────┐  │       │  ┌──────────────┐  │
                     │  │  clank-host  │  │       │  │  clank-host  │  │
                     │  └──────────────┘  │       │  └──────────────┘  │
                     └────────┬───────────┘       └────────┬───────────┘
                        optional BYO key                   │
                              │                            │
┌──────────────┐ ┌───────────▼────────┐       ┌──────────▼─────────┐
│  clank-host  │ │  Laptop local hub  │◄─────►│     Remote hub     │
│  (laptop,    │◄│      (clankd)      │ sync  │ Hosted/self-hosted │
│  supervised) │ └───────────▲────────┘(later)└──────────▲─────────┘
└──────────────┘             │                           │
                     ┌───────┴───────────┐       ┌───────┴──────────┐
                     │ Laptop TUI client │       │  Mobile client   │
                     └───────────────────┘       └──────────────────┘
```

Client routing in the end state:
- Laptop TUI → local hub (Unix socket)
- Mobile → remote hub (HTTPS / SSE / WebSocket)
- Optional LAN bridge (deferred): mobile directly to laptop hub on same network

Each hub presents all sessions it knows about to its clients — a unified view including sessions synced from other hubs. Owning hub (the one whose host runs the session) is always source of truth.

### V1 target topology (what Phases 0–5 deliver)

```
Laptop:

   ┌────────────────┐
   │  TUI / voice   │
   └───────┬────────┘
           │ HTTP (Unix socket)
   ┌───────▼────────┐         supervises          ┌──────────────────┐
   │   clankd       │ ──────── child ──────────► │   clank-host     │
   │ (hub.Service)  │ ◄──────── HTTP ──────────── │ (host.Service)   │
   └───────┬────────┘   (Unix socket)             └──────────────────┘
           │
           │ HTTPS + bearer (optional, when user supplies Daytona key)
           │
           ▼
   ┌──────────────────────────────────────┐
   │ Daytona machine (user's account)     │
   │   ┌──────────────────┐               │
   │   │    clank-host    │               │
   │   │  (host.Service)  │               │
   │   └──────────────────┘               │
   └──────────────────────────────────────┘
```

The Hub never knows whether it's talking to a Unix socket or HTTPS; only the dialer in `internal/host/client/` cares.

## System Roles

### Hub (`hub.Service`, exposed via `hub/mux`)

- The API surface for clients (TUI, mobile, voice)
- Session registry: durable record of which sessions exist, which host runs each, status, metadata
- Host catalog: configured/reachable hosts and their status
- Event fanout: subscribes to each active host's per-session SSE, broadcasts to connected clients
- Permission broker: holds pending permissions, routes client replies back to the right host
- Request router: forwards host-scoped routes to the right host client
- Auth (deferred beyond local socket peer / bearer)

The Hub owns no filesystem state, no git, no agent processes. Pure coordination.

### Host (`host.Service`, exposed via `host/mux`)

- Manages BackendManagers (opencode, claude, …)
- Owns the repo cache and worktrees
- Runs sessions; exposes `agent.SessionBackend` for each
- Streams events from `SessionBackend.Events()` over per-session SSE
- Lists its own backends, agents, models, repos, branches, worktrees, existing sessions

### Persistence ownership

- **Hub** owns the **session registry**: durable metadata about every session (ID, owning host, status, repo, branch, backend, timestamps, message counts, pending permissions). Stored in `internal/store/` (SQLite locally; a different store later for the remote hub). This registry is what hub-to-hub sync replicates in Phase 6.
- **Host** is **stateless across restarts with respect to the registry**. It does not persist its own session list. On boot, `host.Service.DiscoverSessions()` re-discovers what's on disk by asking each BackendManager (OpenCode persists sessions to disk; Claude does not).
- **Backend session files** (OpenCode's session JSON, repo clones, worktrees) are durable on the Host's filesystem. The Host owns these files; they make hub-to-host session transfer in Phase 7 a matter of moving files + flipping the registry's `owning_host` field.
- A session is "live" when both the Hub registry and the Host's BackendManager agree it exists. Discrepancies are reconciled at host reconnect via `DiscoverSessions`.

### `clankd`

Composition only:
- starts `hub.Service`
- binds the Hub mux on the user's listener (Unix socket today; later: also TCP for LAN bridge)
- spawns and supervises a local `clank-host` child:
    - restart on unexpected exit with exponential backoff (1s → 2s → 4s → … capped at 30s)
    - no restart if the child exits cleanly in response to `clankd`'s shutdown signal
    - on shutdown: send SIGTERM to child, wait up to 10s, then SIGKILL
- on user-supplied Daytona key: triggers `internal/host/daytona/` to provision a remote `clank-host` and registers it in the Hub's host catalog

### `clank-host`

Composition only:
- starts `host.Service`
- binds the Host mux on Unix socket (laptop) or TCP+TLS (remote)
- enforces auth (peer ownership locally, bearer remotely)

## Core Method Shapes

These are concrete types on `host.Service`, also reflected on the Hub's `client.Client`.

```go
// host.Service is the in-process domain. host/mux is its HTTP wrapper.
type Service struct { /* ... */ }

func (s *Service) CreateSession(ctx context.Context, req agent.StartRequest) (agent.SessionBackend, error)
func (s *Service) DiscoverSessions(ctx context.Context) ([]agent.SessionInfo, error)

func (s *Service) ListBackends(ctx context.Context) ([]BackendInfo, error)
func (s *Service) ListAgents(ctx context.Context, backend string) ([]AgentInfo, error)
func (s *Service) ListModels(ctx context.Context, backend string) ([]ModelInfo, error)

func (s *Service) ListRepos(ctx context.Context) ([]Repo, error)
func (s *Service) ListBranches(ctx context.Context, repoID RepoID) ([]BranchInfo, error)
func (s *Service) ListWorktrees(ctx context.Context, repoID RepoID) ([]WorktreeInfo, error)
func (s *Service) CreateWorktree(ctx context.Context, repoID RepoID, branch string) (WorktreeInfo, error)
func (s *Service) RemoveWorktree(ctx context.Context, repoID RepoID, name string) error
func (s *Service) MergeBranch(ctx context.Context, repoID RepoID, branch string) error

func (s *Service) Status(ctx context.Context) (HostStatus, error)
```

`agent.StartRequest` is updated in Phase 3 to drop `ProjectDir`/`WorktreeDir` and carry `RepoRef`+`Branch` instead.

**`RepoID` definition**: a URL-safe slug derived from `RepoRef.RemoteURL` — e.g. `github.com/acksell/clank`. Stable across reboots. One repo on a host has exactly one `RepoID`. The Host computes it deterministically from `RepoRef`; the Hub uses it as a path component in HTTP routes.

### Wire vs. in-process: how `CreateSession` returns `SessionBackend` over HTTP

`agent.SessionBackend` is a Go interface with channels and methods — not directly serializable. The split:

- **Inside `clank-host`**, `host.Service.CreateSession` returns the real `SessionBackend` (the one wrapping the running OpenCode/Claude process). `host/mux` does NOT serialize it; it serializes the session metadata (ID, status, host-side info) into the HTTP response. The real `SessionBackend` stays on the host, owned by `host.Service`, and feeds events into the per-session SSE endpoint.
- **Inside `clankd`**, `hostclient.Client.CreateSession` receives the metadata response and constructs a **client-side `SessionBackend` adapter**: an in-process struct that implements the `SessionBackend` interface by translating each method call into HTTP requests against the host (e.g., `SendMessage` → `POST /sessions/{id}/message`; `Events()` → goroutine that subscribes to `GET /sessions/{id}/events` SSE and pumps a local channel; `Abort` → `POST /sessions/{id}/abort`).
- The Hub uses the adapter exactly as the daemon uses today's in-process `SessionBackend`. Caller code is identical.

This is the only place the `SessionBackend` interface having two implementations (real + HTTP-adapter) earns its keep.

## Gaps in the Current Codebase

The implementation agent should be aware:

1. **Daemon size**: `internal/daemon/daemon.go` is ~2200 lines mixing Hub and Host responsibilities. Must be split.
2. **Transport coupling**: `internal/daemon/client.go` is Unix-socket-only. The new client lives under `internal/host/client/` and dials either Unix or TCP+TLS.
3. **Path-centric API contracts**: `StartRequest` and `SessionInfo` use `ProjectDir`/`WorktreeDir`/`WorktreeBranch`. Shift to `HostID` + `RepoRef` + branch.
4. **Git ops mixed in daemon**: worktree lifecycle is owned directly by `daemon.go`. The seam to move into `host.Service`.
5. **Persistence**: `internal/store/store.go` is local SQLite. Sufficient for the local Hub; remote hub will need its own store.
6. **In-memory pending permissions**: fine for local; remote hub will need durability.
7. **Optional manager capabilities** (`AgentLister`, `ModelLister`, `SessionDiscoverer` on `BackendManager`): these become regular methods on `host.Service`. The Host always supports them.

## Client Migration (TUI, CLI, Voice)

Clients — TUI, future CLI, voice — all consume the Hub HTTP API. They never dial `clank-host` directly; the Host API is Hub-internal.

### Shared client package: `internal/hub/client/`

- Exposes a `Client` type with methods mirroring the Hub HTTP API (session CRUD, worktree ops, event subscription).
- Owns the Unix-socket dialer and SSE subscription logic.
- Provides a `ResolveRepo(cwd string) (RepoRef, branch string, err error)` helper that shells out to git locally. The TUI calls this to turn the user's current directory into the `RepoRef`+`Branch` the Hub expects. Resolution runs client-side so the Hub stays strict about host+repo+branch identity.
- `ResolveRepo` is permanent infrastructure, not a migration shim. It translates **shell context** (cwd) into API identity (`RepoRef`). The deprecated translation is `ProjectDir` → `RepoRef`, which dies in Phase 3 when the wire format stops carrying paths.
- Replaces every existing `internal/daemon/client` import.

### TUI migration by phase

- **Phase 0**: no TUI changes.
- **Phase 1**: no TUI changes. The daemon keeps exposing its current client surface unchanged; the TUI is oblivious to the internal `host.Service` split.
- **Phase 2**: mechanical rename of TUI imports from `internal/daemon/client` to `internal/hub/client`. Same methods, same behavior. Voice logic migrates server-side into `hub.Service` at this point; `internal/tui/voice.go` retains only UI affordances (indicators, toggles).
- **Phase 3**: TUI adopts host+repo+branch via `hubclient.ResolveRepo(cwd)`. Path fields (`ProjectDir`, `WorktreeDir`) retired from request payloads. `hostID` is hardcoded `"local"` for now; `repoID` is derived from the resolved `RepoRef` once at session-start and cached on the session view.
- **Phase 5**: host selection UX **deferred**. Expectation is that dirty-sync (Phase 7) will handle most cross-host placement automatically in the background; explicit selection (e.g., an `alt+enter` menu from the session-start overlay) to be designed later. Until then, the TUI always uses `hostID="local"`.

### Voice

Voice is a Hub-resident client per Decision #8. The current `internal/tui/voice.go` has its capture / transcription / command routing logic migrate into `hub.Service` during Phase 2. The file in `internal/tui/` keeps only UI-level concerns (state indicator, enable/disable toggle) and calls the Hub through `hubclient` like any other TUI feature.

## Implementation Phases

### Phase 0: Domain types + daemon split prep

- Add `internal/host/` value types: `HostID`, `RepoID`, `RepoRef`, `BackendInfo`, `AgentInfo`, `ModelInfo`, `BranchInfo`, `WorktreeInfo`, `HostStatus`, `Repo`.
- Split `internal/daemon/daemon.go` into focused files (session ops, workspace ops, HTTP routes, voice). No behavior change. Goal: each resulting file is cohesive (one responsibility) and ideally under 800 lines. The point is navigability, not a hard line limit.
- Tag each method as `// HUB` or `// HOST` in comments — preparation for the extraction.
- **Split `daemon_test.go` alongside the source split.** At ~4600 lines it will be unnavigable after the source split; test files should mirror the new source-file boundaries.

Done when: types compile with unit tests for ID generation and `RepoRef` parsing; `daemon.go` is split into cohesive files; all existing tests pass.

### Phase 1: Build `host.Service` + `host/mux` + `host/client` + `clank-host`; rewire daemon to use them

**Status: ✅ COMPLETE** (2026-04-17)

- Implement `host.Service` with the method set above (extract repo cache, worktree mgmt, BackendManager wiring out of the daemon).
- Implement `host/mux` HTTP handlers wrapping the Service.
- Implement `host/client` HTTP client as a **clean rewrite** — do not copy or rename `internal/daemon/client.go`. The old client hard-codes Unix-socket assumptions in its URL construction; the new one must be dialer-agnostic from the start.
- Implement `cmd/clank-host` binary that wires Service + mux + Unix socket listener.
- Modify `clankd` to spawn and supervise a `clank-host` child instead of holding host state directly.
- Replace daemon's direct calls to its old workspace/session helpers with calls to `hostclient`.
- **The daemon keeps exposing its existing client-facing HTTP surface unchanged through Phase 1.** The TUI is oblivious to the host split — it still imports `internal/daemon/client` and sees the same endpoints. Only the daemon's *internals* now go through `hostclient` instead of in-process helpers. The TUI rename happens in Phase 2.

Done when: full session lifecycle works end-to-end through Unix socket; existing TUI works identically; integration tests cover create-session / send / events / abort / fork via the host client.

**Phase 1 deviations from spec:**
- Decision #3 said `hostclient.HTTP` would be the only client. We added a `hostclient.Client` **interface** with two adapters (`InProcess`, `HTTP`) so tests can run end-to-end without spawning a subprocess. Rationale documented in `internal/host/client/client.go`. Production clankd uses `HTTP` over Unix socket.
- `openCodeServerURLs` (in `daemon/agents_models.go`) still concrete-type-asserts `*host.OpenCodeBackendManager`. Deferred to Phase 2 (will be folded into `host.SessionInfo` reshape).
- `daemon.BackendManagers` field retained as input-only bootstrap contract for tests (`d.BackendManagers[X] = mgr` ergonomic). Removed in Phase 2 when daemon stops constructing the host.
- `host.Service.CreateSession` does NOT call `StartRequest.Validate()` because the watch-only activation path (re-attaching to a historical session) creates a backend without a prompt. Validation lives at the boundaries (mux HTTP handler + daemon's send-message path).
- `handleDebugOpenCodeServers` removed entirely (debug-only; can be reimplemented later if needed).

### Phase 2: Extract `hub.Service`; daemon becomes pure composition

- Move Hub concerns out of `daemon.go` into `internal/hub/`: session registry, host catalog, event fanout, permission broker, routing.
- Move client-facing handlers into `internal/hub/mux/`.
- `cmd/clankd` is now: start `hub.Service`, mount `hub/mux`, supervise `clank-host`.
- Event fanout: Hub subscribes to each active host's `/sessions/{id}/events` SSE stream, broadcasts to its own connected clients via `/events`.
- Session creation accepts `host_id` (defaults to `"local"` for backward compat).
- Add `internal/hub/client/` (pkg `hubclient`) and rename TUI imports from `internal/daemon/client`. See [Client Migration](#client-migration-tui-cli-voice).
- Migrate voice logic (capture, transcription, command routing) from `internal/tui/voice.go` into `hub.Service`. TUI keeps only UI affordances.
- **Voice migration is the highest-risk item in this phase.** If it bloats Phase 2 timeline, defer the voice extraction to Phase 3 — voice continues to work against the existing TUI-side implementation in the meantime. Do not let voice block the Hub extraction.

#### Phase 2A complete (in progress: 2B+)

- `internal/hub/client/` created (pkg `hubclient`) — canonical home of `Client`, `SocketPath`, `PIDPath`, `IsRunning`, `parseSSEStream`, and the SSE regression tests.
- TUI (`sidebar.go`, `mergeoverlay.go`, `inbox.go`, `sessionview.go`, `sessionview_compose.go`, `voice.go`) and `clankcli` import `hubclient` directly. Worktree wire types now reference `host.X`.
- Worktree wire types (`BranchInfo`, `WorktreeInfo`, `CreateWorktreeRequest`, `RemoveWorktreeRequest`, `MergeWorktreeRequest`, `MergeWorktreeResponse`) consolidated into `internal/host/types.go` (canonical), with `internal/daemon/host_aliases.go` re-exporting them as type aliases for daemon-internal callers.
- `internal/daemon/client.go` shrunk to a backwards-compat shim (`Client = hubclient.Client`, `NewClient`/`NewDefaultClient` thin wrappers); `daemon.SocketPath`/`PIDPath`/`IsRunning` delegate to `hubclient`.
- Socket file is still `daemon.sock`; rename to `hub.sock` deferred to Phase 2F to avoid breaking running daemons.
- `daemon.{Daemon,sessions,events,permissions,...}` not yet moved — that is Phase 2B onward.

#### Phase 2B complete (in progress: 2C+)

- `internal/hub/` package introduced with the `hub.Service` skeleton: constructor, host catalog primitives (`RegisterHost`, `UnregisterHost`, `Host`, `Hosts`), `Run`/`Shutdown`/`SetLogOutput`. Catalog handles arbitrary `host.HostID`s; the multi-host case is exercised lightly until Phase 4 wires TCP+TLS.
- Service is **not yet wired into clankd** — it is a destination for the migrations in 2C–2E. `internal/daemon` still owns the live session registry, event fanout, and permission broker.
- Unit tests for the catalog primitives use the real `hostclient.NewInProcess(host.New(...))` rather than mocks (per AGENTS.md). Shutdown's host-close-error swallowing is covered.

#### Phase 2C–2E complete (deferred: 2F)

- `internal/daemon/` package fully drained: `daemon.go` (→ `service.go`), `sessions.go`, `events.go`, `permissions.go`, `agents_models.go`, `persistence.go`, `voice.go`, `routes.go`, `worktrees.go`, `host_aliases.go`, and every `*_test.go` `git mv`'d into `internal/hub/`. The directory has been removed; `internal/daemon/client.go` was deleted (TUI and clankcli already imported `hubclient` after 2A).
- `daemon.Daemon` → `hub.Service`; method receivers renamed `(d *Daemon)` → `(s *Service)`; `daemonToolProvider` → `hubToolProvider`. Test packages renamed `daemon_test` → `hub_test`. Logger prefix `[clank-daemon]` → `[clank-hub]`.
- The merged `Service` struct adds the host catalog (`hostsMu`, `hosts`) on top of the legacy daemon fields. `SetHostClient(c)` is now equivalent to `RegisterHost("local", c)`; the in-process host built in `Run()` registers itself the same way, so the catalog and the legacy `s.hostClient` shortcut stay in sync.
- A new public `Service.Shutdown()` closes registered hosts + the persistence store without touching the HTTP listener — for tests that never called `Run()` and need to release the host client they registered. The Run-owned `(s *Service) shutdown(server)` path is unchanged and still does the file/listener cleanup in production.
- `internal/cli/daemoncli/daemoncli.go` now imports `hub` and `hubclient` (no `daemon` import anywhere in production code). `RunStart`'s foreground path calls `hub.New()` + `hub.Service.Run()`; `runStop`/`runStatus` use `hubclient.IsRunning` and `hubclient.NewDefaultClient`.
- Socket file is still `daemon.sock` (Phase 2F item — needs a coordinated stop+restart of any running production daemon to avoid orphan sockets).
- `cmd/clankd/main.go` was already a trivial delegator to `daemoncli.Command()`; no change needed.
- Pre-existing rot surfaced but **not fixed** in this commit (out of scope, no behaviour change):
  - `internal/tui/sessionview_integration_test.go` (build tag `integration`) still references the long-deleted `daemon.NewDefaultBackendFactory` / `BackendFactory` / `OnShutdown` API. Default `go test ./...` skips it; needs a follow-up to either modernize or delete.
  - `mockBackend` retains the racy Stop-vs-SendMessage interaction noted in Phase 1; still grandfathered.
  - `openCodeServerURLs` (now in `internal/hub/agents_models.go`) still concrete-type-asserts `*host.OpenCodeBackendManager`; deferred to Phase 3.

Done when: `internal/daemon/` is gone or reduced to a thin entry point; integration tests cover Hub→Host routing across the socket.

### Phase 3: Drop path-centric session creation

- `agent.StartRequest`: replace `ProjectDir`/`WorktreeDir` with `HostID` + `RepoRef` + `Branch`.
- `POST /sessions` accepts the new shape.
- Git ops moved fully under `/hosts/{id}/repos/{id}/...`.
- TUI updated: path fields removed from outbound requests; `hubclient.ResolveRepo(cwd)` resolves the current directory to `RepoRef`+`Branch` at session-start. See [Client Migration](#client-migration-tui-cli-voice).
- No server-side legacy path field. The Hub accepts host+repo+branch only. Path-to-identity translation is a client-side concern.

Done when: no Hub or Host method takes a filesystem path as identity.

### Phase 4: TCP+TLS transport + bearer auth

- `host/mux` and `host/client` both gain a TCP+TLS mode alongside Unix socket.
- Bearer auth middleware on `host/mux` (skipped on Unix socket where filesystem perms are the auth).
- Validate by running a second `clank-host` on localhost over TCP and registering it as a remote host in the Hub.

Done when: a Hub can drive two hosts simultaneously (one local Unix socket, one localhost TCP+TLS) and both pass the full session lifecycle test.

### Phase 5: Daytona launcher

- `internal/host/daytona/` provisions a Daytona machine, deploys `clank-host`, returns a dial URL + bearer.
- Hub registers the resulting remote host in its catalog.
- One Daytona machine per user (shared repo cache, multiple worktrees, multiple sessions — avoids duplicate clones and cost multiplication that per-session sandboxes would incur).
- CLI / config for user to provide their own Daytona API key.
- Deployment of `clank-host`: TBD (snapshot bake vs. boot-time fetch). Decide as part of this phase, not now.
- TUI host selection UX deferred; until then, TUI uses `hostID="local"` always. Expectation: dirty-sync (Phase 7) handles most cross-host placement automatically; an explicit picker (e.g., via `alt+enter`) to be designed later.

Done when: user with a Daytona key can `clank session start --host=daytona ...` and it just works.

### Phase 6: Remote hub + hub-to-hub sync

Goal: mobile access and cross-device session visibility.

- Remote hub as a standalone `hub.Service` deployment.
- Hub-to-hub metadata sync protocol.
- Hub proxying for cross-hub session commands.
- Managed Daytona billing.

Owning hub (the one whose host runs the session) is always source of truth for live state. Synced copies are read-only replicas.

### Phase 7: Cross-host workspace sync

Seamless device switching with live workspace state. Most correctness-sensitive part. Deferred until product validates the need.

## Open Failure Modes

Document, don't solve yet.

1. **Host disconnects mid-session**: Hub marks host offline; active sessions become unreachable but not lost. Hub surfaces this to clients.
2. **Hub unreachable from remote host**: Remote host re-registers when Hub is back. Local-mode keeps working since `clank-host` is its own process.
3. **`clank-host` child crash on laptop**: `clankd` supervisor restarts it; sessions backed by an OpenCode server survive; in-memory Claude sessions don't.
4. **Stale metadata in Hub**: Host reports state on reconnect / heartbeat.
5. **Hub-to-hub sync conflicts**: Owning hub is source of truth.

## Testing Strategy

Per project guidelines: never mock dependencies. Integration tests with real filesystems, real SQLite, real HTTP.

- **Phase 0**: unit tests for value types.
- **Phase 1**: integration tests for `host.Service` using real temp git repos. End-to-end test `clankd` ↔ supervised `clank-host` over Unix socket.
- **Phase 2**: integration tests for Hub → Host routing and event fanout.
- **Phase 3**: API contract tests for the new `StartRequest` shape.
- **Phase 4**: integration tests with a second `clank-host` over localhost TCP+TLS.
- **Phase 5**: opt-in smoke tests against real Daytona sandboxes (env-guarded).

## Risks

- **Premature rewrite** — mitigate by keeping local mode working at every phase.
- **Path identity leaks** — introduce host+repo+branch identity early; treat paths as host-internal.
- **Dual-authority drift** — cross-host workspace sync stays deferred.
- **Coupling to Daytona** — Daytona lives in one package; nothing else knows about it.
- **Supervisor complexity** — keep `clankd`'s child supervision narrow: spawn, restart on exit with backoff, propagate shutdown signal. No fancier process management.

## Concrete Guidance For The Next Implementation Agent

### Immediate target: Phase 0 and Phase 1

1. Add `internal/host/` value types.
2. Split `daemon.go` (and `daemon_test.go`) into focused files.
3. Build `host.Service` by extracting repo / worktree / BackendManager logic from the daemon.
4. Build `host/mux` and `host/client` (the client is a clean rewrite, not a rename of the old one).
5. Build `cmd/clank-host`.
6. Rewire `clankd` to supervise a `clank-host` child and use `host/client` for everything host-shaped.
7. Existing local TUI UX must keep working at every step.

### What to preserve

- `agent.BackendManager` and `agent.SessionBackend` — clean separation already; `host.Service.CreateSession` returns `SessionBackend` directly.
- `internal/git/git.go` — used by `host.Service` unchanged.
- `internal/store/store.go` — used by `hub.Service` for the session registry.
- The current event relay pattern (`SessionBackend.Events()` → broadcast → SSE) — split across the wire as: `host.Service` publishes per-session SSE; `hub.Service` subscribes once per session and re-broadcasts to its clients.

### Avoid

1. Do not introduce a Go interface for Host. One concrete `host.Service`, one concrete `client.Client`. Add interface only when a second implementation appears.
2. Do not introduce a Workspace API resource. Repo/worktree mgmt is internal to the host.
3. Do not introduce a `SandboxProvider` interface. One concrete Daytona package, no abstraction.
4. Do not aggregate agents/models at the Hub. Always host+backend scoped.
5. Do not run any host work in-process inside `clankd` — always via `clank-host` over HTTP.
6. Do not add new transports beyond Unix socket and TCP+TLS bearer.
