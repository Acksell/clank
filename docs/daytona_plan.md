# Daytona Host Plan

Plan for adding Daytona-backed remote hosts to Clank. Pairs with
`hub_host_refactor.md` (target architecture, ASCII diagram, vision).
This document is the working plan for Phases 4–5 of that long-term
refactor, plus the smallest TUI/CLI surface needed to actually use it.

Owner: update this doc as the plan evolves; treat it as the source of
truth for what's in scope, what's deferred, and what the demo bar is.

## Goal

Run `clank-host` inside a Daytona sandbox and stream session progress
back to the laptop TUI.

**Definition of done (MVP — server reachable):**

1. `clankd start` then `clank connect daytona` exits 0 and prints the
   sandbox id + registered host id.
2. `GET /hosts` lists both `local` and `daytona`, both reachable.
3. `GET /hosts/daytona/backends` returns a backend list (empty for
   MVP — no claude/opencode binaries inside the sandbox yet).
4. An SSE roundtrip against the daytona host completes without
   transport errors.
5. `clankd stop` deletes the sandbox.

**Definition of done (demo-quality — Phase F):** the TUI sidebar lists
hosts, the user selects `daytona`, presses connect, and starts a real
session there. (Real sessions still depend on Phase G — LLM keys + agent
binaries inside the sandbox — which is explicitly punted.)

## Why Daytona's "Preview URL" is the right transport

Daytona exposes any HTTP service inside a sandbox listening on ports
3000–9999 at `https://{port}-{sandboxId}.proxy.daytona.work`.
Authentication is a token sent in the `x-daytona-preview-token` header
(standard preview; rotates on sandbox restart). The proxy terminates
TLS, so `clank-host` inside the sandbox can speak plain HTTP. SSE
streams pass through fine.

Our `internal/host/client/http.go` is already dialer-agnostic
(`NewHTTP(baseURL, transport)`); the daytona case is a new factory plus
a header-injecting `RoundTripper`. The Hub stays oblivious — it sees
`*hostclient.HTTP` like any other registered host.

## Architecture (demand-driven)

```
Laptop                                                      Daytona
┌──────────────┐
│   TUI        │  sidebar: [hosts] local ✓
└──────┬───────┘                          [hosts] daytona  (c → connect)
       │ HTTP unix
┌──────▼───────────────┐                 ┌──────────────────────────┐
│  clankd / hub.Svc    │                 │ clank-host --addr :8080  │
│   host catalog:      │  on-demand      │   --allow-public         │
│   • local (always)   │ ──provision──►  └──────────▲───────────────┘
│   • daytona (lazy)   │                            │
└──────┬───────────────┘                            │
       │  HTTPS + x-daytona-preview-token           │
       └────────────────────────────────────────────┘
                   Daytona preview proxy
```

The daytona sandbox is provisioned **on demand** when the user runs
`clank connect daytona` (Phase E) or selects "connect" in the TUI
sidebar (Phase F). Until then no Daytona resources are spent.

One Daytona sandbox per `clankd` lifetime. Multiple coding sessions run
in parallel inside that one sandbox — `host.Service` is already
multi-session.

## Phases

### Phase A — `clank-host` TCP listener  ✅ DONE

Add TCP support to `cmd/clank-host`.

- Add `--addr <host:port>`, mutually exclusive with `--socket`.
- Add `--allow-public`. When `--addr`'s host part is not loopback
  (`127.0.0.0/8`, `::1`), refuse to start unless `--allow-public` is
  set. Loud, fail-fast — matches AGENTS.md "no fallbacks".
- Skip the unix-socket-only paths (chmod 0600, stale-socket cleanup) in
  TCP mode.

Tests:

- Build the binary, start on `127.0.0.1:0`, hit `/status`, assert OK.
- Start on `0.0.0.0:0` without `--allow-public` → expect non-zero exit.

**Implementation notes:**
- `cmd/clank-host/main.go` now accepts both `--socket` and `--addr`,
  with `--allow-public` gating non-loopback binds. Listener creation is
  factored into `openListener` / `openUnixListener` / `openTCPListener`.
- `isLoopbackAddr` special-cases `localhost` (no DNS lookup), accepts
  any 127/8 + ::1, and rejects unspecified addresses (`0.0.0.0`, `::`,
  empty) and arbitrary hostnames.
- Tests live in `cmd/clank-host/main_test.go`: pure unit (table-driven
  loopback matrix, transport-misconfig, public-bind guard at run()
  level) plus two binary-build integrations (TCP /status roundtrip,
  public-bind exits non-zero).

### Phase B — `hostclient.NewRemoteHTTP(baseURL, headers)`  ✅ DONE

Generic header-injecting `http.RoundTripper`. Single chokepoint covers
`do`, `Stream`, and SSE. Daytona-agnostic — any header bag.

Test with `httptest.Server` asserting headers reach the server on both
a normal call and an SSE subscription.

**Implementation notes:**
- `internal/host/client/remote.go` adds `NewRemoteHTTP(baseURL, headers)`
  + private `headerTransport` RoundTripper. Single chokepoint covers
  `do` and `streamEvents` (both go through the same `*http.Client`).
- Header bag is cloned at construction (caller mutation post-construction
  cannot strip auth from in-flight clients).
- Inbound request is `Clone`d before mutation, per `net/http.RoundTripper`
  contract.
- Fail-fast on empty `baseURL` or empty `headers` — "remote without
  auth" is almost certainly a bug; use `NewHTTP` for unauthenticated
  clients (e.g. tests).
- Tests in `internal/host/client/remote_test.go`: validation, header
  injection across multiple calls + multiple endpoints, clone
  semantics, no-mutation, baseURL concat.

### Phase C — `internal/host/daytona/` launcher  ✅ DONE

```
internal/host/daytona/
   launcher.go      Launch(ctx, opts) → (*hostclient.HTTP, *Handle, error)
   sandbox.go       Daytona SDK wrapper: Create / Delete / wait-ready
   binary.go        Cross-compile + upload clank-host
   serve.go         Start clank-host as background session command; readiness
   handle.go        Handle.Stop() deletes the sandbox
   options.go       LaunchOptions{APIKey, Region, ListenPort, BinaryPath, ...}
   launcher_test.go Env-guarded smoke test (CLANK_DAYTONA_KEY=...)
```

Launch sequence:

1. `daytona.NewClientWithConfig({APIKey})` → `Create(ctx, ...)`.
   Default arch: arm64 (cheapest, most regions; cross-compile target
   matches).
2. **Binary delivery (MVP):** if `BinaryPath` set, upload it. Else, on
   the laptop: `GOOS=linux GOARCH=arm64 go build -o /tmp/clank-host-linux-arm64 ./cmd/clank-host`,
   then `sandbox.FileSystem.Upload`. Cache by SHA so reruns are
   instant.
3. `sandbox.Process.CreateSession("clank-host")` then
   `ExecuteSessionCommand(sid, "/home/daytona/clank-host --addr 0.0.0.0:8080 --allow-public", runAsync=true)`.
4. Pipe `GetSessionCommandLogsStream` into the clankd log file
   (prefixed `[daytona-host]`) so Hub-side observability of crashes
   matches the local child.
5. Poll preview URL `/status` until 200 (timeout 5s — Daytona startup
   is sub-second in practice; 5s covers the cold path + binary upload).
6. `sandbox.GetPreviewLink(8080)` → `(url, token)`. Build
   `client := hostclient.NewRemoteHTTP(url, {"x-daytona-preview-token": token})`.
7. Return `Handle{sandbox, cmdID, client}`. `Stop()` deletes the
   sandbox; the running clank-host dies with it.

Failure surface (each logs the real cause, no fallbacks): create
failure, upload failure, host not ready in 5s, preview-link
unavailable.

**Implementation notes:**
- Files match the planned layout: `doc.go`, `options.go`, `binary.go`,
  `sandbox.go`, `serve.go`, `handle.go`, `launcher.go`, plus
  `options_test.go` (unit) and `launcher_test.go` (env-guarded smoke).
- **Default arch corrected to amd64** (not arm64 as originally
  planned). First live smoke test against Daytona returned 502 from
  the preview proxy with arm64; switching to amd64 worked first try.
  Daytona's stock snapshots are linux/amd64. `LaunchOptions.Arch`
  exposes the override; cache key includes arch so both can coexist.
- **Empty `Snapshot` works.** `SnapshotParams{Snapshot: ""}` lets
  Daytona pick the default image; no need for an explicit value yet.
- **`PreviewLink` is a struct** `{URL, Token string}`, not a
  `(url, token, error)` triple as the original plan assumed. Updated
  in `launcher.go`.
- **Diagnostics:** when `/status` readiness fails, the launcher
  best-effort fetches the clank-host process logs via
  `Process.GetSessionCommandLogs` and appends them to the returned
  error. Caught the arch mismatch instantly on the second test run.
- **Cleanup on partial failure:** `Launch` uses a closure (`cleanup`)
  with a fresh 15s context to delete the sandbox if any post-creation
  step fails, avoiding leaked Daytona spend even when the parent
  context is already cancelled by timeout.
- **Readiness probe:** `waitForStatus` exponential backoff 200ms →
  1.5s cap. Live test was ready after one extra poll (~7s total
  Launch wall time).
- Live smoke test (`TestLaunch_Smoke`) passes end-to-end:
  create → upload binary → start clank-host → preview URL roundtrip
  → `/status` 200 → `Stop()` deletes sandbox. Skipped without
  `DAYTONA_API_KEY`.

Long-term (Phase F+ snapshot path): `binary.go` becomes a one-time bake
script producing a Daytona snapshot containing `clank-host` + `claude`
+ `opencode` + an entrypoint that auto-runs `clank-host`. `Launch` then
becomes `Create({Snapshot: "clank-host:vN"})` + `GetPreviewLink`.

### Phase D — Hub `POST /hosts` registration endpoint ✅ DONE

Need a way to add to the host catalog after startup.

- New `hub.Service.ProvisionHost(ctx, kind, opts)`:
  - For `kind="daytona"` → calls `daytona.Launch`, then
    `RegisterHost("daytona", client)`.
  - Stores the `Handle` in a new
    `hub.remoteHandles map[Hostname]daytona.Handle` so
    `Service.Shutdown()` can `Stop()` them.
  - Idempotent: if a daytona host is already registered, return it
    without re-provisioning.
- New mux route in `internal/hub/mux/`:
  `POST /hosts {kind, region?, ...}` → `{host_id, status}`.
- Single config knob: `DAYTONA_API_KEY` env, read at provisioning
  time. No fallback to other env names.

**Implementation notes:**
- `internal/hub/hosts_provision.go`: `HostLauncher` + `RemoteHostHandle`
  interfaces, `Service.RegisterHostLauncher`, `Service.ProvisionHost`,
  `Service.stopRemoteHandles` (concurrent shutdown, 30s/host cap).
- `internal/hub/mux/hosts.go`: `GET /hosts` and `POST /hosts {kind}` →
  `{host_id, status:"ready"}`.
- `internal/hub/client/host.go`: `Client.Hosts(ctx)` and
  `Client.ProvisionHost(ctx, kind)`.
- `internal/hub/hosts_provision_test.go`: 5 tests using a real
  httptest-backed launcher (no mocks per AGENTS.md). All passing.
- `internal/cli/daemoncli/daytona_launcher.go`: thin adapter wrapping
  `daytona.Launch` to `hub.HostLauncher`. Registered from `RunStart`
  iff `DAYTONA_API_KEY` is set in env (no-op otherwise — surfaces a
  clear "no launcher registered" error to the user instead of silent
  defaults). Adapter lives in daemoncli (not daytona) so the daytona
  package stays independent of hub.

### Phase E — CLI: `clank connect daytona` ✅ DONE

Smallest possible MVP trigger. This is what proves "server reachable".

- New `internal/cli/clankcli/connect.go` (or similar — one file per
  command per AGENTS.md). Subcommand: `clank connect daytona`.
- Calls `hubclient.ProvisionHost(ctx, "daytona")`.
- On success prints:

  ```
  Connected daytona host:
    sandbox_id: 28a3...
    host_id:    daytona
    status:     ready
  ```

The `connect` verb is the user-facing surface; future hosts (e.g. a
self-hosted SSH host) get registered the same way:
`clank connect <kind>`.

**Implementation notes:**
- `internal/cli/clankcli/connect.go`: `clank connect <kind>` subcommand;
  90s timeout (Daytona end-to-end is ~7-10s; gives headroom for cold
  binary upload). Synchronous, no retry — errors from the hub
  (missing API key, sandbox failure) propagate verbatim.
- Output is shorter than originally planned: `host_id` + `status`
  only. `sandbox_id` is intentionally omitted from the wire response
  to keep the hub→host-kind coupling minimal; users who want the
  Daytona ID can read the daemon log.

### Phase F — TUI sidebar host section + selection ✅ DONE

Sidebar gets a new top section:

```
HOSTS
  ✓ local       (4 sessions)
    daytona     [disconnected]   [c] connect
─────────────────
WORKTREES
  ...
SESSIONS
  ...
```

- Reads `GET /hosts` to populate.
- Cursor selection on a host sets the TUI's "active host" state. New
  sessions started from the inbox use that host's id.
- Keybinding `c` on a disconnected host row calls
  `POST /hosts {kind: <row>}` and re-renders.
- When `active host != "local"`, the session-start path drops
  `LocalPath` and sends only `GitRef{RemoteURL, Branch}` — the daytona
  host then auto-clones into its `ClonesDir` (existing behavior, see
  `TestCreateSession_RemoteRef_ClonesIntoClonesDir`).
- Active host name shown in the inbox status line so the user always
  knows where the next session will run.

Suggested file split (per AGENTS.md "per-method files"):

- `internal/tui/sidebar_hosts.go` — render + interaction
- `internal/tui/active_host.go` — active-host state model

#### TUI state persistence

Active-host selection (and other UI prefs like sidebar collapsed state)
persists to `~/.clank/tui-state.json`. New tiny package
`internal/tui/uistate/` owns load/save. Schema starts as:

```json
{
  "active_host": "daytona",
  "sidebar_collapsed": false
}
```

Synchronous JSON read on TUI startup; debounced write on change. Treat
unknown keys as forward-compatible (preserve on round-trip).

#### Implementation notes (as built)

- `internal/tui/uistate/` (F.1): load/save `~/.clank/tui-state.json`,
  forward-compatible via raw-message preservation; 7 unit tests.
- `internal/tui/active_host.go` (F.2): `ActiveHost` wraps uistate, with
  `LoadActiveHost`, `Name`, `IsLocal`, `Set`. `Set` tolerates a nil
  state pointer (used for tests + uistate-load failure fallback).
  `KnownHostKinds = []string{"daytona"}` is hardcoded here — when we
  add a third launcher, append to this slice.
- `internal/tui/sidebar_hosts.go` (F.3): `hostsSection` owns the host
  rows. `applyLoaded` merges `/hosts` results with `KnownHostKinds`
  (so daytona shows as a `[disconnected]` row when not yet
  provisioned). `provision` calls `POST /hosts` with a 90s timeout.
- `internal/tui/sidebar.go` (F.4): `sidebarSection` enum
  (`sectionHosts`, `sectionWorktrees`); a single linear `cursor`
  spans both sections. Helpers `totalRows`, `cursorSection`,
  `branchIndex`, `cursorOnHost`, `activateSelectedHost`. Keys: `j`/`k`
  traverse the whole list; `c` provisions on a disconnected host row;
  `n` is gated to the worktrees section; `r` refreshes both.
- `internal/tui/inbox.go` (F.5): `NewInboxModel` calls
  `LoadActiveHost` (best-effort fallback to a detached `ActiveHost`
  on error). Header shows `@<hostname>` badge — muted for local,
  success-bold for remote. Sidebar Enter routed: host row →
  `activateSelectedHost`; branch row → existing pane-switch.
- `internal/tui/sessionview_compose.go` (F.6): `NewSessionViewComposing`
  takes a `host.Hostname`; `launchSession` drops `LocalPath` from the
  GitRef when the active host is not local, leaving the daytona host
  to auto-clone via `ClonesDir`.
- `internal/cli/clankcli/clankcli.go` (F.7): `activeHostFromState()`
  reads uistate (defaults to local on any error). Both `codeCmd` and
  `runComposing` honor it for `req.Hostname` and the LocalPath gate.
- Tests: `active_host_test.go` (7 tests), `sidebar_test.go` (7 tests
  covering merge of KnownHostKinds, no-duplicate-on-already-connected,
  cursor traversal across sections, branch-index offset accounting,
  cursorOnHost predicate, activate on connected vs disconnected rows,
  `n` only in worktrees section). `go test ./...` clean.

### Phase G — LLM keys + agent binaries (deferred, sketch)

Not in this milestone. Three options ranked when we get there:

1. **Env passthrough on launch.** `LaunchOptions.Env` carries
   `ANTHROPIC_API_KEY` (and similar) from the laptop into the sandbox
   process env before `clank-host` starts. Simplest. Plain-text in the
   sandbox process env.
2. **Upload `~/.local/share/opencode/auth.json`** to the sandbox via
   `sandbox.FileSystem.Upload`. Works for opencode; doesn't help
   claude.
3. **Daytona's secrets feature** (set on snapshot or sandbox at
   creation). Cleanest; pair with the snapshot path.

For agent binaries (`claude`, `opencode` CLIs): bake into the snapshot
once we have one. Until then, sessions on daytona will fail at backend
launch — the host control plane is reachable, but agents won't run.
That's fine for the MVP bar (server reachable).

## Out of scope (explicitly punted)

- LLM key injection (Phase G).
- claude / opencode binary availability inside the sandbox (Phase G).
- Bearer auth on `clank-host`'s TCP mode (deferred until non-Daytona
  public deployment exists).
- Private repo cloning (needs git creds in the sandbox).
- Snapshot-based provisioning (Phase F+).
- Phase 3 of the hub/host refactor (`StartRequest` shape change).
  Independent of this work.
- Restart-on-crash for the remote host (matches existing local-host
  limitation; same supervisor pattern applies later).

## Decisions log

1. **TCP guard:** loopback default; `--allow-public` opt-in. Daytona
   launcher passes `--allow-public` (sandbox boundary is the security
   perimeter).
2. **Sandbox arch:** amd64 by default (Daytona's stock snapshots are
   linux/amd64; first live test confirmed arm64 returned 502 from the
   preview proxy). `LaunchOptions.Arch` overrides for ARM snapshots.
3. **TUI state persistence:** `~/.clank/tui-state.json`, JSON, owned by
   `internal/tui/uistate/`. Used for active host now; will house other
   UI prefs (sidebar collapse, etc.) over time.
4. **`POST /hosts` is synchronous.** Blocks until the sandbox is ready.
   Daytona startup is sub-second; the wait is dominated by binary
   upload, capped at 5s. Async + SSE-progress can come later if it
   stops feeling instant.
5. **CLI verb:** `clank connect <kind>` (not `clank daytona connect`).
   Generic over future host types.
6. **Lifecycle:** one daytona sandbox per `clankd` lifetime, lazily
   provisioned, killed on `clankd` shutdown.
7. **Multi-session per host:** confirmed already supported; no new
   work.

## Open questions

None blocking. Will be revisited at Phase G design time:

- Exact key-injection mechanism (Phase G option 1 vs 3).
- Snapshot bake / version pinning workflow.
- Private repo auth strategy in the sandbox.
