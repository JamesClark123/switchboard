# Tasks: MCP Gateway Manager & Daemon Screen (+ kit schema v2)

**Input**: Design documents from `/specs/008-mcp-gateway-manager/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md) (R1–R12),
[data-model.md](./data-model.md), [contracts/](./contracts/) (proto, `sbx-mcp-cli.md`,
`kit-schema-v2.md`), [quickstart.md](./quickstart.md)

**Tests**: Included — Rule VI's ≥90% per-module coverage floor is constitutional, and every prior
feature ships colocated tests with its implementation. Test tasks follow their implementation
tasks within each story (the repo convention), not TDD-first. Every `sbx` argv is pinned by an
argv-asserting test against a stub script (research R12).

**Organization**: Tasks are grouped by user story so each story is an independently testable
increment. US1 (gateway registrations) is the MVP; US2 (attach by default) makes registrations
useful at launch; US3 (kit schema v2) is independent of MCP and can proceed in parallel; US4
(daemon screen) completes the surface whose *skeleton* is foundational; US5 (startup sign-in) is
independent of everything but the settings store.

> **Status (2026-10-07)**: every task is implemented and the full gate passes (`make all`, `cover`
> ≥ 90 % per module, `env-check`, `e2e`) **except T003 and T063**, which need a host with a real
> `sbx` ≥ 0.36 and are deliberately left open: T003 is the reconciliation gate for the
> documentation-derived `sbx mcp` argv / `--version` format (see `contracts/sbx-mcp-cli.md`), and
> T063 is the real-host run of quickstart Scenarios 1–5. Both gate merge, not coding.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1 (registrations, P1), US2 (attach by default, P1), US3 (kit schema v2, P1),
  US4 (daemon screen, P2), US5 (startup sign-in, P2)
- Every task names exact file paths

## Path Conventions

Go `go.work` monorepo (plan.md Project Structure): proto in `src/libs/switchboard-proto/`,
daemon in `src/services/switchboardd/`, TUI in `src/apps/switchboard-tui/`, PTY-driven E2E in
`src/apps/switchboard-tui-e2e/`. Tests are colocated `_test.go` siblings (001's Rule IV deviation).
Package constants, never env vars, for every bound (plan Constraints).

---

## Phase 1: Setup (Contract + reconciliation gate)

**Purpose**: The additive wire contract both sides build against, and the one item only a real
host can answer.

- [X] T001 Add the feature-008 additions to `src/libs/switchboard-proto/proto/switchboard.proto` per `specs/008-mcp-gateway-manager/contracts/switchboard-mcp-gateway.proto`: enums `McpAttachMode`, `McpServerKind`, `McpAuthState`; messages `McpServer`, `McpGatewaySettings`, `ListMcpServersRequest/Response`, `AddMcpServerRequest` (+ nested `LocalCommand`), `RemoveMcpServerRequest/Response`, `AuthorizeMcpServerRequest`, `McpAuthProgress`, `SetMcpServerDefaultRequest`, `SetMcpAttachModeRequest`; seven RPCs on `service Switchboard` in a new `// --- MCP gateway (feature 008, FR-072..FR-100) ---` block; `Sandbox.mcp_servers = 21` + `Sandbox.mcp_attach_mode = 22`; `DaemonInfo` fields 6–9; `Event.mcp_gateway_changed = 6` with nested `McpGatewayChanged { host_id }`. Carry the contract file's comments (skip-auth/authorize-separately, fixed-at-launch, UNIMPLEMENTED/FAILED_PRECONDITION skew semantics). **No new NotificationKind.**
- [X] T002 Regenerate Go bindings with `make proto` and commit `src/libs/switchboard-proto/gen/switchboard.pb.go` + `switchboard_grpc.pb.go` (depends on T001)
- [ ] T003 [P] Reconciliation gate (quickstart Scenario 0) on a host with a real `sbx` ≥ 0.36: run every command listed in `specs/008-mcp-gateway-manager/quickstart.md` Scenario 0 and record the answers in `specs/008-mcp-gateway-manager/research.md` under R1/R2/R9 (`--version` format, `mcp ls` layout, `auth status --json` fields, whether `add --skip-auth` registers without a browser, `auth` URL line + Ctrl-C behavior, `--static-mcp` with `--kit`, `mcp load` on a running sandbox, `kit add` rejection text, existence of `sbx start` / `ls --json` / `options --json`). Amend `contracts/sbx-mcp-cli.md` and the pinned argv tests (T012, T030, T034) accordingly. **Gates merge, not coding** — stub-based tasks proceed on the documented argv meanwhile.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The runtime baseline + gateway availability every MCP/kit story gates on, the
daemon-side persistence and exec plumbing the gateway manager needs, and the daemon-screen
*skeleton* that gives US1's gateway view a home.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T004 [P] Create `src/services/switchboardd/internal/sbxkit/version.go`: `const MinSbxVersion = "0.36.0"` (research R9), `ParseVersion(output string) (string, bool)` (first `\d+\.\d+\.\d+`), `AtLeast(have, min string) bool` (numeric compare; unparseable ⇒ false); tests in `internal/sbxkit/version_test.go` (table: `sbx version 0.46.0`, `0.36.0`, `sbx-e2e 0.0`, garbage)
- [X] T005 [P] Drop the undocumented `sbx options --json` branch from `src/services/switchboardd/internal/sbxkit/manifest.go` (research R9): `Build` goes straight to `fromHelp`; delete `fromJSON`/`jsonOption`; update `internal/sbxkit/manifest_test.go` so the stub no longer answers `options`
- [X] T006 [P] Add the `mcp` bucket to `src/services/switchboardd/internal/registry/registry.go` (research R3): `CreateBucketIfNotExists(mcpBucket)` in `Open`; `GetMcpSettings() (*pb.McpGatewaySettings, error)` (absent ⇒ zero value) and `UpdateMcpSettings(mutate func(*pb.McpGatewaySettings) error) (*pb.McpGatewaySettings, error)` (read-modify-write in one tx, proto-marshalled under key `"settings"`); tests in `internal/registry/registry_test.go` (round-trip, absent-is-default, concurrent updates serialize)
- [X] T007 [P] Create package `src/services/switchboardd/internal/mcp/` with `cli.go`: `type CLI struct{ Bin string }`, `run(ctx, args...) (out string, err error)` (CombinedOutput; error carries the `outputTail` like `sandbox/runner.go`), and `runStreaming(ctx, onLine func(string), args...) error` for `auth` — child in its own process group (`setPgid`/`killPgid` copied from `internal/escapehatch/process_unix.go`), killed on ctx cancel; the full argv table from `contracts/sbx-mcp-cli.md` as named helpers (`argvList`, `argvAuthStatus(name)`, `argvAddRemote(name,url)`, `argvAddLocal(name,cmd,args,dir)`, `argvRemove(name)`, `argvAuthorize(name)`, `argvLoad(name,sandbox)`); package doc in `internal/mcp/doc.go`
- [X] T008 [P] Create `src/services/switchboardd/internal/mcp/availability.go`: `Availability{Available bool; Reason string}` computed once at startup and re-checkable: CLI resolvable on PATH → `sbx --version` parses and `AtLeast(MinSbxVersion)` → `sbx mcp ls` exits 0 (an "not signed in"/"login" line in output ⇒ reason "host is not signed in to the sandbox tooling — run `sbx login` there"); each failure yields a human reason (FR-079); tests in `internal/mcp/availability_test.go` with stub scripts per failure mode
- [X] T009 Wire the baseline into the daemon: in `src/services/switchboardd/cmd/sxbd/main.go` probe `sbx --version` via `sbxkit.ParseVersion`, construct `mcp.Availability`, and pass `SbxMinVersion`, `RuntimeBaselineMet`, `McpAvailability` through `sbgrpc.Config` into `Server` (`src/services/switchboardd/internal/grpc/server.go`); `GetDaemonInfo` fills `DaemonInfo` fields 6–9; tests in `internal/grpc/server_test.go` (depends on T002, T004, T008)
- [X] T010 [P] Client-side `DaemonInfo` cache: in `src/apps/switchboard-tui/internal/client/conn.go` fetch `GetDaemonInfo` on dial and expose `Info() *pb.DaemonInfo` (keeping `DaemonVersion()`/`WorkspaceRoot()` as accessors over it); add `Info()`/`SbxVersion()`/`RuntimeBaselineMet()`/`McpGateway() (bool, string)` to the `Daemon` interface in `src/apps/switchboard-tui/internal/ui/app.go` and to `fakeDaemon` in `src/apps/switchboard-tui/internal/ui/launch_test.go` (fields `info *pb.DaemonInfo`)
- [X] T011 Daemon-screen skeleton (research R5): add `screenDaemons` + `screenMcp` to the screen enum and `ScreenNext`/`ScreenPrev` bindings (`shift+right`/`shift+left`, help "shift+→/← daemons|sandboxes") in `src/apps/switchboard-tui/internal/ui/keys.go`; create `src/apps/switchboard-tui/internal/ui/daemons.go` with `daemonsState{rows []daemonRow; cursor int; options bool; optCursor int}` built from `m.manager.List()` (local daemon included) on every `hostsMsg`, a body view listing `name · kind · state`, `enter` on a connected row opening its option list (`MCP gateway` as the only entry), `enter` on the option entering `screenMcp` (placeholder view "coming in US1" until T022), `esc` stepping back; dispatch `shift+←/→` from `updateListKey` and `updateDaemonsKey` in `src/apps/switchboard-tui/internal/ui/sandbox_list.go`/`app.go`, preserving `m.list` selection and `daemonsState.cursor` across switches (FR-068); `View()` renders both screens through `chrome`; `teatest` tests in `src/apps/switchboard-tui/internal/ui/daemons_test.go` (switch both ways preserves cursors; option list opens for connected rows only) (depends on T010)

**Checkpoint**: Baseline + availability reported per daemon; `mcp` persistence and exec plumbing exist; the daemon screen is reachable with shift+←/→ — US1–US5 can proceed (US1/US3/US5 in parallel if staffed).

---

## Phase 3: User Story 1 - Manage a daemon's MCP server registrations (Priority: P1) 🎯 MVP

**Goal**: From the daemon screen's **MCP gateway** option, list a daemon's registered MCP servers
(name, kind, target, authorization state), register remote or host-command servers, remove them,
and authorize OAuth-backed ones with the link shown in the TUI and a 10-minute cancellable wait —
each operation scoped to that daemon only, host diagnostics relayed verbatim.

**Independent Test**: With two daemons connected, register a remote server on the remote daemon
from the TUI; it appears there with kind/target/state, is absent from the local daemon's list, is
visible to `sbx mcp ls` on that host, and removing it from the TUI removes it there (quickstart
Scenario 1).

### Implementation for User Story 1 — daemon

- [X] T012 [US1] `src/services/switchboardd/internal/mcp/parse.go`: `parseList(out string) ([]listRow, error)` — header-offset parser for the `NAME  TYPE  URL/COMMAND` table (column starts taken from the header line; extra columns ignored; blank/`No servers` ⇒ empty), `kindOf(typeCol string) pb.McpServerKind` (`remote` ⇒ REMOTE, else LOCAL); `parseAuthStatus(out string) pb.McpAuthState` (JSON per T003's reconciled field names; any parse failure ⇒ UNKNOWN); tests in `internal/mcp/parse_test.go` with the documented table sample and malformed inputs
- [X] T013 [US1] `src/services/switchboardd/internal/mcp/manager.go`: `Manager{cli *CLI; reg *registry.Registry; hostID string; emit func(*pb.Event)}`; `List(ctx) ([]*pb.McpServer, *pb.McpGatewaySettings, error)` = `mcp ls` → per-REMOTE `auth status --json` (parallel, bounded by `mcpListTimeout = 60s`) → decorate with marks from `reg.GetMcpSettings()`, dropping marks whose name is absent (I-M2, persisted via `UpdateMcpSettings`); `Add(ctx, req *pb.AddMcpServerRequest) (*pb.McpServer, error)` = refuse `command` without `acknowledge_local_execution` (FR-076), refuse args containing commas, `argvAddRemote/Local` **always with `--skip-auth`** (R2), then re-list to return the row; `Remove(ctx, name) ([]string notes, error)` = `mcp rm` + clear mark + collect lines mentioning `sbx secret rm`; every success emits `Event.mcp_gateway_changed{host_id}` (R10); timeouts `mcpMutateTimeout = 2m` as package constants
- [X] T014 [US1] Authorization stream in `src/services/switchboardd/internal/mcp/manager.go` (research R2): `Authorize(ctx, name, onFrame func(*pb.McpAuthProgress)) error` — one in-flight wait per name (`FAILED_PRECONDITION` "authorization already in progress" otherwise), `runStreaming(argvAuthorize)` under `context.WithTimeout(mcpAuthTimeout = 10*time.Minute)`, first `https://` token in any line ⇒ `url` frame (then subsequent lines ⇒ `message`), every frame carries `deadline_unix`; exit 0 ⇒ `done: AUTHORIZED`; timeout/cancel/non-zero ⇒ `done: UNAUTHORIZED` (+ `error` with the host output tail); the registration is never touched; emit `mcp_gateway_changed` on AUTHORIZED
- [X] T015 [US1] gRPC handlers in new `src/services/switchboardd/internal/grpc/mcp_rpcs.go`: `ListMcpServers`, `AddMcpServer`, `RemoveMcpServer`, `AuthorizeMcpServer` (server stream; client cancel ⇒ wait cancelled); every handler first checks `s.mcpAvailability` and returns `codes.FailedPrecondition` with `mcp_gateway_reason` when unavailable (FR-079); host diagnostics pass through verbatim as the error message (FR-078); wire `mcp.Manager` into `Server`/`Config` in `src/services/switchboardd/internal/grpc/server.go` and construct it in `src/services/switchboardd/cmd/sxbd/main.go`; fan the new `Event` arm out in `src/services/switchboardd/internal/grpc/subscribe.go` (depends on T013, T014)

### Tests for User Story 1 — daemon

- [X] T016 [P] [US1] `src/services/switchboardd/internal/mcp/manager_test.go` with a stub `sbx` script (`fakeMcpSbx(t)` emitting the documented `ls` table, `auth status --json`, logging argv): argv pinned for list/add remote/add local/remove (exact flags incl. `--skip-auth`, `--args` comma join, `--dir`); local add without acknowledgement refused before exec; comma in args refused; stale mark dropped on list; UNKNOWN state on status failure never fails the list; `mcp rm` notes collected; event emitted on success only
- [X] T017 [P] [US1] `src/services/switchboardd/internal/mcp/authorize_test.go`: stub `auth` prints `Open this URL to authorize "x": https://example/auth` then sleeps — url frame arrives before completion, deadline is set, cancel kills the process group (no orphan: assert the child exits), second concurrent authorize refused, exit 0 ⇒ AUTHORIZED, non-zero ⇒ UNAUTHORIZED with tail; a 10-minute bound asserted via an injected clock/timeout var (`authTimeoutForTest`)
- [X] T018 [P] [US1] `src/services/switchboardd/internal/grpc/mcp_rpcs_test.go` over the in-process socket: each RPC round-trips; unavailable gateway ⇒ FAILED_PRECONDITION carrying the reason; authorize stream delivers url then done; client-side cancel ends the wait

### Implementation for User Story 1 — client

- [X] T019 [US1] `src/apps/switchboard-tui/internal/client/mcp.go`: `ListMcpServers`, `AddMcpServer`, `RemoveMcpServer`, `AuthorizeMcpServer(ctx, name, onFrame func(McpAuthUpdate)) (pb.McpAuthState, error)` (drains the stream; `McpAuthUpdate{URL, Message, Deadline time.Time, Done *pb.McpAuthState, Err string}`); add the four methods to the `Daemon` interface in `src/apps/switchboard-tui/internal/ui/app.go` and to `fakeDaemon` in `src/apps/switchboard-tui/internal/ui/launch_test.go` (scripted `mcpServers`, `mcpAuthFrames`, error fields)
- [X] T020 [US1] Gateway view in new `src/apps/switchboard-tui/internal/ui/mcp.go` (replacing T011's placeholder): `mcpState{host, hostName string; servers []*pb.McpServer; settings *pb.McpGatewaySettings; cursor int; loading bool; unavailable string; form *huh.Form; auth *mcpAuthWait}`; enter from the daemon option list loads via `ListMcpServers` (`mcpLoadedMsg`); body lists `name · kind · target · state` with `(no servers registered)` empty state and the availability reason when FAILED_PRECONDITION (FR-079); keys: `a` add, `d` remove (behind `confirm.go` naming the server), `A` authorize, `r` reload, `esc` back to the daemon option list; reload on `Event.mcp_gateway_changed` for the open host (handled in `app.go` event dispatch); help via `mcpHelp()`; register `screenMcp` in `handleKey`/`forward`/`View`
- [X] T021 [US1] Add form in `src/apps/switchboard-tui/internal/ui/mcp.go`: `huh` form — name (validated `[A-Za-z0-9._-]+`), kind select (remote endpoint / host command), url **or** command + args (comma-separated, validated: no comma inside an arg) + optional dir, *attach to every sandbox* checkbox (wired in US2, T037 — present but inert until then); choosing *host command* shows the outside-isolation warning and requires an explicit confirm before `AddMcpServer` is called with `acknowledge_local_execution = true` (FR-076); host errors shown verbatim in the view's status line, list unchanged (FR-078)
- [X] T022 [US1] Authorize wait in `src/apps/switchboard-tui/internal/ui/mcp.go`: `mcpAuthWait{name, url string; deadline time.Time; ch chan tea.Msg; cancel context.CancelFunc}` driven by the `streamOpCmd`-style goroutine + `waitForMsg` pattern (see `ui/oplog.go`); renders an overlay (`overlayCenter`) with the URL as copyable text, "open it where you are — never opened on the host", time remaining ticking on the shared spinner tick, `esc` cancels (ctx cancel ⇒ daemon kills the wait); terminal frame updates the row's state and closes the overlay; never auto-opens a browser (FR-075)

### Tests for User Story 1 — client

- [X] T023 [P] [US1] `src/apps/switchboard-tui/internal/ui/mcp_test.go` (`teatest`): list renders rows/empty/unavailable states; add remote → `fakeDaemon.addedMcp` carries name/url and `attach_by_default=false`; host-command add refused until the warning is confirmed, then sent with the acknowledgement; remove is confirm-gated and sends the name; host error text appears verbatim and rows unchanged; authorize overlay shows the fake's URL and a countdown, `esc` cancels and the row shows *unauthorized*; `mcp_gateway_changed` for the open host triggers a reload, for another host does not
- [X] T024 [P] [US1] `src/apps/switchboard-tui/internal/client/mcp_test.go` against an in-process server: wrappers map responses; `AuthorizeMcpServer` drains frames into `McpAuthUpdate`s and returns the terminal state

**Checkpoint**: US1 fully functional — a daemon's registrations are listed, registered, removed and authorized from the TUI, host-scoped, with diagnostics verbatim. MVP deliverable.

---

## Phase 4: User Story 2 - Sandboxes launch with the daemon's default MCP servers attached (Priority: P1)

**Goal**: A per-daemon **attach-by-default** mark on each registration and a **default attach
mode** (additive, the default: `mcp load` after create; exclusive: `--static-mcp`), applied by the
daemon at every launch, recorded on the sandbox and shown in the list; stale or unauthorized marks
never fail a launch.

**Independent Test**: Mark one server attach-by-default on a daemon and launch there: the agent
sees that server's tools without further action, the sandbox record names it, the launch log shows
the `mcp load` (or `--static-mcp`) line, and a sandbox on a different daemon has none (quickstart
Scenario 2).

### Implementation for User Story 2 — daemon

- [X] T025 [US2] Marks + mode in `src/services/switchboardd/internal/mcp/manager.go`: `SetDefault(ctx, name string, on bool) (*pb.McpServer, error)` (NOT_FOUND unless `name` is in the current `mcp ls`; `UpdateMcpSettings` sets/deletes `marks[name]`), `SetMode(ctx, mode pb.McpAttachMode) (*pb.McpGatewaySettings, error)` (UNSPECIFIED ⇒ ADDITIVE), both emitting `mcp_gateway_changed`; `ResolveAttach(ctx) (McpAttach, error)` = marks ∩ `mcp ls` names, dropping stale marks (persisted) and returning `McpAttach{Mode pb.McpAttachMode; Servers []string; Skipped []string}` (data-model "Launch resolution"); `Load(ctx, name, sandboxName string) error` = `argvLoad` under `mcpLoadTimeout = 60s`
- [X] T026 [US2] Launch integration in `src/services/switchboardd/internal/sandbox/manager.go` + `runner.go` (research R4): `LaunchRequest.Mcp *McpAttach`; `LaunchSpec.StaticMcp []string` rendered by `SbxRunner.Launch` as one `--static-mcp a,b` flag after the kit flags; new `Runner.LoadMcp(ctx, ref, name string, log func(string)) error` (→ `sbx mcp load <name> --sandbox <ref>`); in `Manager.Launch`, after `runner.Launch` and **before** the sandbox is marked RUNNING/emitted (i.e. before any agent PTY is started), for `ADDITIVE` call `LoadMcp` per server, logging `mcp: attached <name>` / `mcp: skipped <name>: <err>` via `onLog` (failure never fails the launch, FR-083/084); set `Sandbox.McpServers` (successfully attached names) and `Sandbox.McpAttachMode`; log `mcp: skipped stale mark <name>` for `Skipped`; `RestartSandbox`/`Refresh`/`bringUp` do **not** re-apply (I-S2) — add a guard comment + test
- [X] T027 [US2] `src/services/switchboardd/internal/grpc/sandbox_rpcs.go` `LaunchSandbox`: when the gateway is available, call `s.mcp.ResolveAttach` and pass `Mcp` into `LaunchRequest`; when unavailable, pass nil (launch as today, FR-085/edge case); `src/services/switchboardd/internal/grpc/mcp_rpcs.go`: `SetMcpServerDefault`, `SetMcpAttachMode` handlers (depends on T025, T026)

### Tests for User Story 2 — daemon

- [X] T028 [P] [US2] `src/services/switchboardd/internal/mcp/manager_test.go` additions: SetDefault on unknown name ⇒ NOT_FOUND; mark round-trips through the registry; ResolveAttach drops and persists-dropping stale marks and reports them in `Skipped`; mode defaults to ADDITIVE when unset
- [X] T029 [P] [US2] `src/services/switchboardd/internal/sandbox/manager_test.go` + `runner_test.go` additions (stub `sbx` logging argv): ADDITIVE launch runs `mcp load <name> --sandbox <ref>` per server **after** `create` and before the record turns RUNNING; a failing load is logged and skipped, launch still RUNNING, `McpServers` excludes it; EXCLUSIVE launch passes `--static-mcp a,b` on `create` and no `load`; empty set ⇒ neither; `RestartSandbox`/`Refresh` never call `load` or pass `--static-mcp` again; record fields persist across `store.Put/Get`
- [X] T030 [P] [US2] `src/services/switchboardd/internal/grpc/mcp_rpcs_test.go` + `sandbox_rpcs_test.go` additions: SetDefault/SetMode round-trips + events; LaunchSandbox with marks streams the `mcp:` log lines and returns a sandbox whose `mcp_servers`/`mcp_attach_mode` are set

### Implementation for User Story 2 — client

- [X] T031 [US2] `src/apps/switchboard-tui/internal/client/mcp.go`: `SetMcpServerDefault`, `SetMcpAttachMode`; add both to the `Daemon` interface (`src/apps/switchboard-tui/internal/ui/app.go`) and `fakeDaemon` (`src/apps/switchboard-tui/internal/ui/launch_test.go`)
- [X] T032 [US2] Gateway view additions in `src/apps/switchboard-tui/internal/ui/mcp.go`: `[x]`/`[ ]` default column per row toggled with `space` (`SetMcpServerDefault`), a header line `default attach mode: additive|exclusive` toggled with `m` (`SetMcpAttachMode`, with a one-line explanation of each mode), the add form's *attach to every sandbox* checkbox now sent as `attach_by_default` (T021); help updated
- [X] T033 [US2] Sandbox list badge + launch log in `src/apps/switchboard-tui/internal/ui/sandbox_list.go`: `sandboxDesc` appends `mcp: a, b (additive)` / `mcp: a (exclusive)` when `GetMcpServers()` is non-empty (FR-086); nothing else changes — the `mcp:` launch lines already land in the op log (`l`) via `LaunchProgress.log_line`

### Tests for User Story 2 — client

- [X] T034 [P] [US2] `src/apps/switchboard-tui/internal/ui/mcp_test.go` additions: `space` toggles and sends the mark; `m` flips the mode and sends it; the add form's checkbox is sent; `sandbox_list_test.go` (or `model_test.go`) asserts the `mcp:` badge for both modes and its absence when empty
- [X] T035 [P] [US2] E2E in `src/apps/switchboard-tui-e2e/mcp_e2e_test.go` with `stubSbx` extended in `src/apps/switchboard-tui-e2e/harness.go` (`mcp ls` table incl. one server, `mcp auth status` JSON, `mcp add/rm/load` exit 0, `--version` ⇒ `sbx version 0.46.0`): mark a server, launch, assert `sbx.log` contains `mcp load <name> --sandbox` after `create` (additive) and `--static-mcp` after switching mode; the list row shows the `mcp:` badge

**Checkpoint**: US1 + US2 — registrations are managed and applied at launch in either mode; the record and the list tell the developer what each sandbox got.

---

## Phase 5: User Story 3 - Kits are authored against kit schema version 2 (Priority: P1)

**Goal**: The kit editor authors only schema-2 mixins; saved v1 kits migrate on load (written on
save) with every drop reported; attaching a kit the runtime would reject on an existing sandbox is
refused before any RPC; kit operations are gated on the runtime baseline; v3 kits are refused.

**Independent Test**: Save a kit with one entry in every section → file is v2 and validates on a
baseline host; open a legacy kit → migrated banner with drops, re-saves as v2; `A` a kit with a
startup command onto a running sandbox → refused naming `setup.startup`, no RPC; same kit at launch
applies in full (quickstart Scenario 3).

### Implementation for User Story 3 — client store

- [X] T036 [US3] Re-shape `src/apps/switchboard-tui/internal/store/kit.go` to schema 2 (data-model "Kit (schema 2)"): `SchemaVersion "2"`, identity gains `Version`, optional `Requires *KitRequires{Agent string}`, `Permissions *KitPermissions{Network *KitNetworkPerms{Allow, Deny []string}}`, `Environment *KitEnvironment{Variables map[string]string}` (no `ProxyManaged`), `Credentials []KitCredential{Service, Description string; Required bool; APIKey *KitAPIKey{Name string; ProxyManaged bool; Inject []KitInject{Domain, Header, Format string}}}`, `Setup *KitSetup{Install []KitInstallCommand; Files []KitInitFile; Startup []KitStartupCommand}`, `AgentInstructions *KitAgentInstructions{Content string}`; `Normalize` drops empty sections and sets `"2"`/`mixin`; `SpecYAML` unchanged in spirit; escape-hatch/services sidecars untouched; update every existing test in `internal/store/kit_test.go`, `kit_escapehatch_test.go`, `kit_services_test.go` to the new shape and assert the rendered YAML keys (`permissions:`, `setup:`, `agentInstructions:`, `credentials:` list)
- [X] T037 [US3] New `src/apps/switchboard-tui/internal/store/kit_migrate.go` (research R8, `contracts/kit-schema-v2.md`): `type Migration struct{FromVersion string; Carried []string; Dropped []Drop{Path, Reason string}; Notes []string}`; `isLegacy(raw map[string]any) bool` (schemaVersion "1", or any of `network`, `commands`, `credentials.sources`, `agentContext`, `memory`, `environment.proxyManaged`); `migrateV1(raw) (*Kit, *Migration)` applying the mapping table (allowed/denied → permissions; commands.* → setup.*; agentContext|memory → agentInstructions; sources.<id>.env[0] → credential service + apiKey.name, proxyManaged from the v1 list, remaining env names + unknown fields dropped with paths/reasons, "declare injection domains" note); `KitStore.Get` decodes raw YAML first, migrates when legacy, and returns the `*Migration` alongside (`Get(id) (*Kit, *Migration, error)` — update callers); nothing is written until `Save`
- [X] T038 [US3] `src/apps/switchboard-tui/internal/store/kit_validate.go`: `(*Kit) AttachBlockers() []string` returning section paths among `permissions.network.deny`, `setup.files`, `setup.startup`, `credentials`, `agentInstructions` that are non-empty (FR-095); `DetectSchema(raw) (version string, kind string)` and `ErrSchemaV3` for `schemaVersion "3"` / `kind: workload|set` (FR-099), used by `Get` to refuse loading v3 into the editor with a clear error (the kit stays listed with a `(v3 — not editable)` marker)

### Tests for User Story 3 — client store

- [X] T039 [P] [US3] `src/apps/switchboard-tui/internal/store/kit_migrate_test.go`: golden v1 fixture (every v1 section incl. `credentials.sources.github.env: [GITHUB_TOKEN, GH_TOKEN]`, `environment.proxyManaged: [GITHUB_TOKEN]`, `commands.*`, `agentContext`) → v2 kit with the expected shape; `Dropped` lists `GH_TOKEN` and an unknown field by path; `Notes` mentions injection domains; a v2 file is not migrated (`Migration == nil`); `Save` after migration writes `schemaVersion: "2"` and no v1 keys; a `memory:` kit maps like `agentContext`
- [X] T040 [P] [US3] `src/apps/switchboard-tui/internal/store/kit_validate_test.go` additions: `AttachBlockers` empty for env+install+allow-only kits and lists each blocking section; v3 detection by version and by kind

### Implementation for User Story 3 — editor, attach, daemon gating

- [X] T041 [US3] Kit editor in `src/apps/switchboard-tui/internal/ui/kit_editor.go`: sections renamed/re-formed for v2 — Identity (+ version, + base agent `requires.agent`), **Network permissions** (allow/deny lists), Environment (variables only), **Credential services** (list of services; per-item form: service, description, required, API-key env name, proxy-managed, inject entries domain/header/format; a fixed hint "values are supplied and bound on the host — `sbx secret set`"), Install commands, **Setup files**, Startup commands, **Agent instructions**; footer `kind: mixin · schemaVersion: 2`; a `migrated from schema 1` banner listing `Dropped`/`Notes` when `Get` returned a `Migration`, cleared on save; update `kit_editor_test.go`, `kit_sections_test.go`, `kit_editor_keymap_test.go`, `kit_ui_test.go` to the new sections
- [X] T042 [US3] Attach pre-check in `src/apps/switchboard-tui/internal/ui/kit_picker.go`: before `confirmAttachKit`, if `k.AttachBlockers()` is non-empty show a refusal modal (reusing `confirm.go` in notice mode or a status line) naming the sections and "apply this kit at launch instead (`n` → `K`)"; no RPC is made; v3 kits in the picker show `(v3 — cannot attach)` and are refused the same way (FR-099)
- [X] T043 [US3] Daemon baseline gating in `src/services/switchboardd/internal/grpc/sandbox_rpcs.go`: `ValidateKit`, `AddSandboxKit`, and `LaunchSandbox` with non-empty `kits` return `codes.FailedPrecondition` `"host sbx <have> is below the minimum <MinSbxVersion> required for kit schema 2"` when `!runtimeBaselineMet` (FR-096); kit-less launches untouched; the TUI surfaces the message verbatim (no client change needed beyond existing error display)
- [X] T044 [US3] Update the e2e stub in `src/apps/switchboard-tui-e2e/harness.go` so `--version` prints `sbx version 0.46.0` (≥ baseline; otherwise T043 refuses every kit op in e2e) and `src/apps/switchboard-tui-e2e/kits_e2e_test.go` asserts the materialized `spec.yaml` on the daemon side is `schemaVersion: "2"` with `permissions:`/`setup:` keys

### Tests for User Story 3 — editor, attach, daemon gating

- [X] T045 [P] [US3] `src/apps/switchboard-tui/internal/ui/kit_editor_test.go` additions (`teatest`): each v2 section renders and round-trips through its form into the model; the credential form never offers a host source; the migration banner appears for a legacy kit and lists the drops; saving clears it
- [X] T046 [P] [US3] `src/apps/switchboard-tui/internal/ui/kit_ui_test.go` additions: `A` with a startup-command kit is refused naming `setup.startup` and `fakeDaemon.addKitID` stays empty; an env+install+allow kit attaches as before; a v3 kit is refused with the v3 reason
- [X] T047 [P] [US3] `src/services/switchboardd/internal/grpc/sandbox_rpcs_test.go` additions: with `RuntimeBaselineMet=false`, `ValidateKit`/`AddSandboxKit`/kit launch ⇒ FAILED_PRECONDITION naming both versions; kit-less launch succeeds

**Checkpoint**: US3 — switchboard writes only schema 2, migrates what it reads, refuses what the runtime would reject, and knows whether each host can take kits at all.

---

## Phase 6: User Story 4 - A daemon screen beside the sandbox list (Priority: P2)

**Goal**: Complete the daemon screen: every known daemon with state and reason, runtime version
and baseline/gateway availability, **connect** on disconnected rows (reusing the hosts password
prompt) and **disconnect** on connected ones, live updates, an open gateway view closing when its
daemon disconnects, sandbox keys inert, help footers naming shift+←/→.

**Independent Test**: With two daemons known and one disconnected: shift+→ shows both with states;
the disconnected one offers `c` and no options; `c` → password prompt → connected → `enter` reveals
*MCP gateway*; `x` disconnects and closes an open gateway view; shift+← returns to the previously
highlighted sandbox (quickstart Scenario 4).

### Implementation for User Story 4

- [X] T048 [US4] Generalize the hosts password flow in `src/apps/switchboard-tui/internal/ui/hosts.go`: `enterHostPassword(id, target string, returnTo screen)` and `connectHostCmd` unchanged; the prompt renders as a **centered overlay** (`overlayCenter`) when `returnTo != screenHosts` so the daemon screen (and US5) can reuse it; on result the model returns to `returnTo` and refreshes `hostsMsg`
- [X] T049 [US4] Daemon rows + actions in `src/apps/switchboard-tui/internal/ui/daemons.go`: row shows `state` (connected/disconnected/connecting), the last connection error as reason, `daemon vX · sbx vY` from the cached `DaemonInfo`, and `below baseline (min Z)` / `gateway: unavailable — <reason>` markers (FR-069/FR-096/FR-079); `c` on a disconnected ssh row → T048 prompt (local rows connect directly), `x` on a connected row → `manager.Disconnect` + if `screenMcp` is open for that host, close it with status `"<host> disconnected — gateway view closed"` (FR-071); rows rebuild on every `hostsMsg` so state changes land live; sandbox-only keys are not dispatched on `screenDaemons`; `daemonsHelp()` lists `enter · c · x · shift+← · , · esc`
- [X] T050 [US4] Help footers: `listHelp()` in `src/apps/switchboard-tui/internal/ui/keys.go` gains `shift+→ daemons`; `daemonsHelp()` gains `shift+← sandboxes`; update `coverage_test.go`'s wide-footer width if the one-line footer no longer fits (FR-070)

### Tests for User Story 4

- [X] T051 [P] [US4] `src/apps/switchboard-tui/internal/ui/daemons_test.go` additions (`teatest`, fake manager with scripted dial outcomes): disconnected row shows reason and offers connect; `c` opens the centered prompt, submit → connected row with options; `x` disconnects and closes an open gateway view with the message; a `hostsMsg` state change re-renders the row; sandbox keys (`s`,`d`,`n`) do nothing on the daemon screen; baseline/gateway markers render from `DaemonInfo`
- [X] T052 [P] [US4] E2E in `src/apps/switchboard-tui-e2e/daemons_e2e_test.go`: `shift+→` reaches the daemon screen listing the local daemon as connected with `sbx version 0.46.0`; `enter` shows *MCP gateway*; `shift+←` returns to the list with the same selection

**Checkpoint**: US4 — the daemon screen is a complete management surface and the home of US1/US2's gateway view.

---

## Phase 7: User Story 5 - Remote hosts are signed into as the TUI starts (Priority: P2)

**Goal**: A TUI-persisted **settings screen** (`,`) whose first toggle, *automatic host sign-in*
(default on), drives a startup sequence over saved SSH hosts: keyless attempt, centered masked
prompt when a password is needed, retry/skip on failure, 30 s per attempt, nothing persisted, never
blocking.

**Independent Test**: Two saved SSH hosts (key, password): restart → the first connects silently,
a centered prompt names the second, the password connects it, both hosts' sandboxes appear; wrong
password → failure card with retry/skip; toggle off in settings → restart → zero prompts and the
toggle persisted (quickstart Scenario 5).

### Implementation for User Story 5

- [X] T053 [P] [US5] `src/apps/switchboard-tui/internal/store/settings.go`: `Settings{AutoConnectHosts bool}` with `DefaultSettings()` (true), `SettingsStore.Load() (Settings, error)` (missing file ⇒ defaults; `absent key ⇒ default` via a pointer-typed TOML shadow struct; parse error ⇒ defaults + the error returned for the notice) and `Save(Settings) error` via `SaveTOML("settings.toml", …)`; tests in `internal/store/settings_test.go` (defaults, round-trip, absent key keeps default, corrupt file)
- [X] T054 [P] [US5] Classify SSH dial failures in `src/apps/switchboard-tui/internal/client/ssh.go`: wrap the dial error as `*DialError{Kind: AuthFailed|Unreachable|NoDaemon|Other; Stderr string}` by matching ssh's stderr (`Permission denied`/`publickey` ⇒ AuthFailed; `Connection refused|timed out|Could not resolve` ⇒ Unreachable; `sxbd: command not found` ⇒ NoDaemon); `Manager.ConnectWithPassword` returns it unchanged; tests in `internal/client/ssh_test.go` with canned stderr
- [X] T055 [US5] Settings screen in new `src/apps/switchboard-tui/internal/ui/settings.go`: `screenSettings` + `Settings` binding `,` (help ", settings") on both top-level screens; `settingsState{items []settingItem; cursor int; notice string}` listing `automatic host sign-in  [on]`; `space`/`enter` toggles and `Save`s immediately (status "saved"); a load error shows `settings could not be read — defaults apply` (FR-101); `esc` returns to the screen it was opened from; wire `WithSettings(st.Settings())` in `src/apps/switchboard-tui/cmd/sxb/main.go` and the model field in `app.go` (depends on T053)
- [X] T056 [US5] Startup sign-in state machine in new `src/apps/switchboard-tui/internal/ui/signin.go` (research R7, data-model "Startup sign-in sequence"): `signInState{queue []client.HostEntry; current int; phase signInPhase (attempting|prompting|failed|done); reason string}`; kicked off from `Init` when `settings.AutoConnectHosts` (queue = saved `ssh` hosts not connected; local never queued); `attemptCmd(id, password)` = `manager.ConnectWithPassword` under `signInTimeout = 30*time.Second`; `*client.DialError{AuthFailed}` ⇒ centered masked prompt (T048's overlay, title `Sign in to <host>` + reason, blank = key auth); other failure ⇒ centered failure card (`r` retry, `esc` skip); success ⇒ next; `done` ⇒ `hostsMsg` refresh + status `signed in to N of M hosts`; the prompt/card own the keyboard while shown (modal) and `esc` always advances; passwords never stored (depends on T048, T053, T054)

### Tests for User Story 5

- [X] T057 [P] [US5] `src/apps/switchboard-tui/internal/ui/settings_test.go` (`teatest`): `,` opens from both top-level screens and `esc` returns to the origin; toggle saves through a fake/temp `SettingsStore`; corrupt file ⇒ notice + defaults
- [X] T058 [P] [US5] `src/apps/switchboard-tui/internal/ui/signin_test.go` (`teatest`, fake manager with per-host scripted outcomes): key host connects with no prompt; auth-failed host shows the centered prompt naming it; submitting connects; wrong password ⇒ failure card; `r` re-prompts; `esc` skips to the next; unreachable host ⇒ card without prompt; sequence ends with the list usable and both outcomes in status; toggle off ⇒ no attempts; local host never attempted; a host already connected is skipped
- [X] T059 [P] [US5] E2E in `src/apps/switchboard-tui-e2e/settings_e2e_test.go`: `,` → toggle off → quit → relaunch → the toggle is still off and `settings.toml` exists under the config dir

**Checkpoint**: All five stories independently verified.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T060 [P] README: document the daemon screen (shift+←/→, `enter`/`c`/`x`), the **MCP gateway** section (register/authorize/remove, attach-by-default marks, additive vs exclusive mode, the 10-minute authorization wait, secrets/advanced options out of scope → `sbx secret set` on the host), the settings screen (`,`, `settings.toml`, `auto_connect_hosts`), startup sign-in behavior, the kit schema-2 move (migration-on-load, attach refusals, minimum `sbx` 0.36), and the key table rows in `README.md`
- [X] T061 [P] Update `src/services/switchboardd/internal/sbxkit/manifest.go` doc comments and `README.md`'s daemon section to state the manifest comes from `sbx --help` only (T005) and that the daemon reports `sbx_min_version`/baseline in `GetDaemonInfo`
- [X] T062 Run the full gate from the repo root (`Makefile` targets): `make fmt-check vet lint test cover env-check e2e`; fix any module below the 90% floor via the stub-sbx tests in `src/services/switchboardd/internal/mcp`, `src/services/switchboardd/internal/sbxkit`, `src/apps/switchboard-tui/internal/store`, `src/apps/switchboard-tui/internal/ui`
- [ ] T063 Run `specs/008-mcp-gateway-manager/quickstart.md` Scenarios 1–5 end-to-end on a real host (after T003 confirmed Scenario 0) and record SC-001..SC-013 results in `specs/008-mcp-gateway-manager/quickstart.md` (a "Results" section)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: T001 → T002 (codegen); T003 runs on a real host any time before merge
- **Foundational (Phase 2)**: T004–T008 in parallel after T002; T009 after T004/T008; T010 after T002; T011 after T010 — **BLOCKS all user stories**
- **US1 (Phase 3)**: after Phase 2 — daemon T012 → T013 → T014 → T015; client T019 → T020 → T021 → T022; tests T016–T018, T023–T024 in parallel after their implementation
- **US2 (Phase 4)**: after US1 (reuses the gateway view and `mcp.Manager`) — T025 → T026 → T027; T031 → T032 → T033; tests T028–T030, T034–T035 in parallel afterwards
- **US3 (Phase 5)**: after Phase 2 only (independent of MCP) — T036 → T037 → T038 → (T039, T040) → T041 → T042; T043 after T009; T044 after T043; T045–T047 after their implementation
- **US4 (Phase 6)**: after Phase 2; T048 first (US5 depends on it too) → T049 → T050; T051–T052 afterwards
- **US5 (Phase 7)**: after T048 (prompt overlay) — T053, T054 in parallel → T055 → T056; T057–T059 afterwards
- **Polish (Phase 8)**: after every desired story

### User Story Dependencies

- **US1 (P1)**: Phase 2 only. MVP.
- **US2 (P1)**: US1 (gateway view, manager, client wrappers).
- **US3 (P1)**: Phase 2 only — fully parallel with US1/US2 (different packages: `store`, `kit_editor.go`, `kit_picker.go`; the only shared file is `sandbox_rpcs.go` for T043, which touches a different handler set than T027).
- **US4 (P2)**: Phase 2 only; T048 is also US5's prerequisite.
- **US5 (P2)**: T048 (from US4) + its own store/client tasks.

### Within Each User Story

- Daemon before client within a story (the client's fake can be scripted earlier, but the RPC
  round-trip tests need the handlers)
- Store/model before UI (US3, US5)
- Tests follow implementation and are parallel across files

### Parallel Opportunities

- Phase 2: T004, T005, T006, T007, T008, T010 in parallel; T011 once T010 lands
- After Phase 2 with three developers: A → US1 then US2; B → US3; C → US4 then US5
- Every `_test.go` task marked [P] runs in parallel with its siblings
- US2's e2e (T035) and US4's e2e (T052) and US5's e2e (T059) touch different test files and can run in parallel after the harness change in T035/T044 (coordinate the single `harness.go` edit: T044's version string and T035's `mcp` verbs land in one change)

---

## Parallel Example: Foundational + US1

```bash
# Phase 2, after `make proto`:
Task: "T004 sbxkit/version.go — MinSbxVersion, ParseVersion, AtLeast"
Task: "T005 sbxkit/manifest.go — drop options --json"
Task: "T006 registry — mcp bucket + Get/UpdateMcpSettings"
Task: "T007 internal/mcp/cli.go — argv table + exec + pgid kill"
Task: "T008 internal/mcp/availability.go — gateway availability + reason"
Task: "T010 client/conn.go — DaemonInfo cache + Daemon interface"

# US1 tests, after T015 / T022:
Task: "T016 mcp/manager_test.go — argv pinning + list decoration"
Task: "T017 mcp/authorize_test.go — url frame, cancel kills, 10-min bound"
Task: "T018 grpc/mcp_rpcs_test.go — round-trips + FAILED_PRECONDITION"
Task: "T023 ui/mcp_test.go — list/add/remove/authorize overlay"
Task: "T024 client/mcp_test.go — wrappers + stream drain"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Phase 1 (T001–T002) and Phase 2 (T004–T011); start T003 on a real host in parallel
2. Phase 3 (US1): a daemon's registrations managed from the TUI
3. **STOP and VALIDATE**: quickstart Scenario 1 on two daemons (SC-001, SC-003, SC-004, SC-013)

### Incremental Delivery

1. + US2 → marks and modes applied at launch (Scenario 2; SC-002, SC-008)
2. + US3 → kits on schema 2 with baseline gating (Scenario 3; SC-009–SC-012) — can ship before US2 if staffed in parallel
3. + US4 → complete daemon screen (Scenario 4; SC-005)
4. + US5 → settings + startup sign-in (Scenario 5; SC-006, SC-007)
5. Polish → docs + full gate + quickstart results

### Notes

- Every `sbx` argv lives in `internal/mcp/cli.go` (and `sandbox/runner.go` for `--static-mcp`/`load`) and nowhere else; T003's findings change those files and their pinned tests only
- `harness.go` (e2e stub) is edited by T035 and T044 — land them together
- No env vars: `mcpListTimeout`, `mcpMutateTimeout`, `mcpAuthTimeout`, `mcpLoadTimeout`, `signInTimeout`, `MinSbxVersion` are package constants
- Commit after each task or logical group; stop at any checkpoint to validate the story independently
