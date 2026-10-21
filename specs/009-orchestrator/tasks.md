# Tasks: Orchestrator

**Input**: Design documents from `/specs/009-orchestrator/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md) (R1–R17),
[data-model.md](./data-model.md), [contracts/](./contracts/) (`switchboard-orchestrator.proto`,
`orchestrator-mcp.md`), [quickstart.md](./quickstart.md)

**Tests**: Included — Rule VI's ≥90% per-module coverage floor is constitutional, and every prior
feature ships colocated tests with its implementation. Test tasks follow their implementation
tasks within each story (the repo convention), not TDD-first. Every `sbx` argv is pinned by an
argv-asserting test against a stub script (research R16); the two-daemon path runs in-process over
two Unix sockets with an injected dial function; the MCP endpoint is exercised with the SDK's own
client.

**Organization**: Tasks are grouped by user story so each story is an independently testable
increment. US1 (create an orchestrator; the third screen) is the MVP; US2 (attach/detach workers)
adds the worker side every daemon runs; US3 (actions, calls, runs, review) is the orchestrator's
purpose and the largest story; US4 (hidden workers, the tab, the third line) is TUI-only on top of
US2's record; US5 (timed + triggered events) builds on US3's call path; US6 (the worker's MCP
tools + briefing) completes the sandbox-facing surface; US7 (reasoning sandbox + `reason` runs)
is last. Two `sbx` argv rows are documentation-derived — T003 reconciles them on a real host and
gates merge, not coding.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1 (create/manage, P1), US2 (attach/detach, P1), US3 (actions + runs, P1), US4
  (visibility, P2), US5 (events, P2), US6 (worker MCP + briefing, P3), US7 (reasoning sandbox, P3)
- Every task names exact file paths

## Path Conventions

Go `go.work` monorepo (plan.md Project Structure): proto in `src/libs/switchboard-proto/`, the
**new** dial library in `src/libs/switchboard-dial/`, daemon in `src/services/switchboardd/`, TUI
in `src/apps/switchboard-tui/`, PTY-driven E2E in `src/apps/switchboard-tui-e2e/`, real-runtime
daemon E2E in `src/services/switchboardd-e2e/`. Tests are colocated `_test.go` siblings (001's Rule
IV deviation). Package constants, never env vars, for every bound (research R15).

---

## Phase 1: Setup (Contract, dependency, module + reconciliation gate)

**Purpose**: The additive wire contract both daemons build against, the one new dependency, the
new library module every connection path will run through, and the one item only a real host can
answer.

- [X] T001 Add the feature-009 additions to `src/libs/switchboard-proto/proto/switchboard.proto` per `specs/009-orchestrator/contracts/switchboard-orchestrator.proto`: enums `ActionType`, `RunState`, `EventKind`, `WorkerAvailability`; messages `SandboxController`, `WorkerRun`, `WorkerBundle`, `OrchestratorHost`, `ActionArgument`, `Action`, `TimedEvent`, `ReasoningSetup`, `Worker`, `Orchestrator`, `OrchestratorSummary`, `WorkerView`, `Run`, `OrchestratorLinkFrame` (+ nested `Hello`/`Call`/`Result`/`Ping`) and every request/response payload; the 24 RPCs on `service Switchboard` in a new `// --- Orchestrator (feature 009, FR-105..FR-157) ---` block (19 orchestrator-side, 5 worker-side, `OrchestratorLink` bidi, `RebuildReasoningSandbox` server-stream of `LaunchProgress`); `Sandbox.controller = 23`; `LaunchSandboxRequest.controller = 7` (`WorkerBundle`); `Event.orchestrator_changed = 8` with nested `OrchestratorChanged { host_id, orchestrator_id }`; `NotificationKind` `RUN_FAILED = 6`, `RUN_AWAITING_REVIEW = 7`, `RUN_NEEDS_INPUT = 8`. Carry the contract file's comments (which side serves what; UNIMPLEMENTED skew; CAS semantics; `dedupe_disabled` negation). **No wire field for `worker_token`.**
- [X] T002 Regenerate Go bindings with `make proto` and commit `src/libs/switchboard-proto/gen/switchboard.pb.go` + `switchboard_grpc.pb.go` (depends on T001)
- [ ] T003 [P] Reconciliation gate (quickstart Scenario 0) on a host with a real `sbx` ≥ 0.36: run every step of `specs/009-orchestrator/quickstart.md` Scenario 0 and record the answers in `specs/009-orchestrator/research.md` R17 — a loopback `http://127.0.0.1:<port>/…` `--url` registration is accepted (SSRF warning text noted), `mcp load` of it into a running sandbox delivers tools live, whether the gateway dials from the host (loopback) or from inside the sandbox (`host.docker.internal`), that it speaks streamable HTTP to a stateless server, and that `sxb-orch-…` rows appear in `mcp ls`. Amend `contracts/orchestrator-mcp.md`, the `worker.gatewayHost` constant (T030) and the pinned argv tests (T036) accordingly. **Gates merge, not coding** — stub-based tasks proceed on the documented argv meanwhile.
- [X] T004 Create the library module `src/libs/switchboard-dial/` (research R2) by **moving** the transport primitives out of `src/apps/switchboard-tui/internal/client/`: `go.mod` (`module github.com/jamesclark123/switchboard/libs/switchboard-dial`, go 1.26, deps proto + grpc only); `dial.go` with `DialLocal(ctx, socketPath)`, `DialCommand(ctx, cmd)`, `SSHCommand(target string, opts []string, password string) *exec.Cmd` plus a new `SSHCommandBatch(target, opts)` variant that sets `-o BatchMode=yes` and no askpass (the daemon never has a password, FR-106), `DialSSH(ctx, target, opts, password)`, the `stdioConn`/`stdioAddr`/`newStdioConn` adapter and `tailBuffer`; `conn.go` with `Conn{Info *pb.DaemonInfo; …}`, `API()`, `HostID()`, the `handshake` (`GetDaemonInfo`) and the cached accessors (`DaemonVersion`, `WorkspaceRoot`, `SbxVersion`, `SbxMinVersion`, `RuntimeBaselineMet`, `McpGateway`), `Close()`; `errors.go` with `DialError`, `DialErrorKind` (+ `String`), `ClassifyDialStderr`, `lastLine`; move the matching tests from `client/ssh_test.go`, `client/ssh_internal_test.go`, `client/client_test.go` into `dial_test.go`/`errors_test.go`; register the module in `go.work` (`use ./src/libs/switchboard-dial`), `Makefile` `MODULES`, and `scripts/cover.sh`'s module list. The askpass helper (`RunAskpassIfRequested`, `askpassProgram`) **stays** in the TUI (depends on T002)
- [X] T005 Re-point the TUI on the library: `src/apps/switchboard-tui/internal/client/client.go` becomes `type Conn = dial.Conn` plus the typed RPC wrappers that remain (`UpdateDaemon`, …); `client/ssh.go` keeps only askpass and thin `DialSSH`/`DialLocal` forwards that inject `askpassProgram()`; `client/manager.go`'s `defaultDial` calls `dial.DialSSH`/`dial.DialLocal`; `DialError` references in `ui/signin.go`, `ui/hosts.go`, `ui/daemons.go` and their tests switch to `dial.DialError`; `go.mod` of the TUI gains the `require` + `go.work` resolves it; `make build test` green for both modules (depends on T004)
- [X] T006 [P] Add the MCP server dependency to `src/services/switchboardd/go.mod`: `github.com/modelcontextprotocol/go-sdk v1.8.0` (research R5; transitively `google/jsonschema-go`, `x/oauth2`, `x/time`); `go mod tidy`; confirm `golangci-lint` and `go vet` stay clean on the daemon module with the import present (a `_ = mcp.NewServer` smoke reference in `internal/worker/doc.go` until T031 lands)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The persistence, daemon-side plumbing and TUI skeleton every story needs: registry
buckets, the controller field's helpers, the two agent-package gaps (R7), the shared managed-block
writer (R12), the per-host event streams (R13), the third screen's skeleton and the header strip,
the client-side `Daemon` interface additions, and the E2E harness's second daemon.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T007 [P] Registry buckets in `src/services/switchboardd/internal/registry/registry.go` (research R9): `CreateBucketIfNotExists` for `orchestratorsBucket = "orchestrators"` and `runsBucket = "orchestrator_runs"` in `Open`; `PutOrchestrator(*pb.Orchestrator)`, `GetOrchestrator(id)`, `ListOrchestrators()`, `DeleteOrchestrator(id)` (also deletes every `runsBucket` key with prefix `<id>/`), `UpdateOrchestrator(id, mutate)` (read-modify-write in one tx); `NextRunSeq(orchID) (uint64, error)` (counter under `<id>/_seq`, same tx), `PutRun(*pb.Run)` (key `<orch>/<seq%020d>` derived from a new unexported `runKey`), `GetRun(orchID, runID)`, `ListRuns(orchID)` (prefix scan, key order), `DeleteRun(key)`; the worker token is **not** a `Sandbox` field — keep the sandbox record pure proto and add `PutWorkerToken(sandboxID, token)` / `GetWorkerToken(sandboxID)` / `DeleteWorkerToken(sandboxID)` on a third new bucket `worker_tokens` (key = sandbox id) so the token never serializes into `Sandbox` (I-C4; data-model.md "bbolt layout"); tests in `internal/registry/orchestrator_test.go` (round-trips, prefix ordering with 25 runs, seq monotonic across tx, delete clears runs + token)
- [X] T008 [P] `internal/agent` gaps (research R7): in `src/services/switchboardd/internal/agent/pty.go` have `PTYFactory` start a goroutine that `cmd.Wait()`s the agent child and, on exit, invokes a new `Registry.onExit(sandboxID)` hook that forgets the session and reports `AGENT_STATUS_EXITED` through the `StatusUpdater` the registry now holds (`NewRegistry(factory, status StatusUpdater)` — update the `cmd/sxbd/main.go` call site; `Session`/`Prompt` after exit start a fresh agent); in `src/services/switchboardd/internal/agent/hub.go` make `Publish` take `h.mu` like `PublishSandbox` does; tests in `internal/agent/pty_test.go` (fake session whose `cmd` exits ⇒ EXITED reported once; a later `Prompt` recreates) and `internal/agent/agent_test.go` (concurrent `Publish` + `Subscribe` under `-race`)
- [X] T009 [P] Shared managed-block writer: new package `src/services/switchboardd/internal/managedblock/` with `Write(path, begin, end, body string) error` (create the file if missing; replace the delimited block or append it, preserving everything outside the markers), `Strip(path, begin, end string) error` (remove the block; leave the file if other content remains), and the pure helpers `replace(content, begin, end, block)`/`remove(content, begin, end)` lifted from `src/services/switchboardd/internal/escapehatch/rule.go` (`replaceBlock`) and `inject.go` (`removeBlock`); re-point `escapehatch/rule.go` `writeRuleBlock` and `inject.go` `stripRuleBlock` onto it with their existing markers so `escapehatch`'s tests (`inject_rule_test.go`, `inject_test.go`) stay green; tests in `internal/managedblock/managedblock_test.go` (append, replace, strip, file absent, content outside untouched, two different marker pairs coexist in one file)
- [X] T010 [P] Sandbox-record helpers for the controller in `src/services/switchboardd/internal/sandbox/manager.go`: `Manager.Controller(id) (*pb.SandboxController, error)`; in `Manager.emit`/`SetAgentStatus` nothing changes (the record carries field 23 automatically); in `Manager.Destroy` and the `removed` path clear the token via a new `Manager.SetOnDestroy(fn func(sb *pb.Sandbox))` hook the worker service (T033) registers; on `Stop`/`fail`/any transition **out of RUNNING** clear `controller.run` inside the registry update (a run cannot stay recorded on a non-running sandbox — the orchestrator learns it from the status event, R7) **except** when `controller.run.needs_review` is set (stopped while awaiting review keeps the run, FR-156 edge case); tests in `internal/sandbox/manager_controller_test.go`
- [X] T011 [P] `src/services/switchboardd/internal/sandbox/manager.go` `LaunchRequest.EmptyWorkspace bool` (research R11): when set, `Launch` accepts zero sources, creates the workspace directory empty (skips `seed`/`duplicate.CopyAll`), records no sources; `Refresh` on a record with zero sources and `EmptyWorkspace` semantics (no sources ⇒ recreate the empty dir) instead of refusing at `manager.go:515`; `RemoveSources`' last-source rule untouched; `LaunchRequest.Controller *pb.SandboxController` + `ControllerBriefing string` carried so T034's launch-attach can run before the agent PTY starts; tests in `internal/sandbox/manager_test.go` (empty launch creates the dir, record has no sources, refresh works; zero sources without the flag still refused)
- [X] T012 [P] Hide the per-worker registration in `src/services/switchboardd/internal/mcp/manager.go`: `List` filters rows whose name has the `sxb-orch-` prefix (exported `const WorkerServerPrefix = "sxb-orch-"` in `internal/mcp/cli.go`), `reconcile` never drops or creates marks for them, `Add` refuses a developer-supplied name with the prefix (INVALID_ARGUMENT "reserved prefix"); tests in `internal/mcp/manager_test.go`
- [X] T013 Daemon wiring skeleton in `src/services/switchboardd/internal/grpc/server.go` + `cmd/sxbd/main.go`: `Config.Worker *worker.Service` and `Config.Orchestrator *orchestrator.Service` (both nil-safe: every new handler answers `codes.Unavailable "orchestrator support not configured"` when nil so T002's generated interface compiles before T031/T044); new files `src/services/switchboardd/internal/grpc/orchestrator_rpcs.go` and `worker_rpcs.go` holding the 24 stubbed handlers + a shared `orchStatus(err) error` mapper (`orchestrator.ErrNotFound`→NOT_FOUND, `ErrNameTaken`/`ErrAlreadyControlled`→ALREADY_EXISTS, `ErrInvalid`→INVALID_ARGUMENT, `ErrPrecondition`→FAILED_PRECONDITION, `ErrUnreachable`→UNAVAILABLE, `ErrForbidden`→PERMISSION_DENIED); fan the `Event.orchestrator_changed` arm through `subscribe.go` unchanged (the hub carries any `*pb.Event`); tests in `internal/grpc/orchestrator_rpcs_test.go` asserting the nil-config codes (depends on T002, T007)
- [X] T014 [P] Client-side interface growth in `src/apps/switchboard-tui/internal/ui/app.go`: extend the `Daemon` interface with the 19 orchestrator-side wrappers + `DetachController` (names below in T022/T037/T054/T065/T074/T082) and implement them on `fakeDaemon` in `src/apps/switchboard-tui/internal/ui/launch_test.go` as scripted fields (`orchestrators []*pb.OrchestratorSummary`, `orchDetail`, `runs *pb.ListRunsResponse`, `attachErr`, `calls []string` recording method+args) so every later `teatest` task has a fake to drive; the real implementations land per story in `src/apps/switchboard-tui/internal/client/orchestrator.go` (file created here with the `Conn` method set returning `codes.Unimplemented` until filled)
- [X] T015 Per-host event streams in `src/apps/switchboard-tui/internal/ui/notifications.go` + `app.go` (research R13): `eventMsg` gains `hostID string`; `subscribeCmd()` becomes `subscribeHostCmd(hostID string, d Daemon)` issued for **every** connected host on `hostsMsg` (tracked in `m.subscribed map[string]bool`, cleared on disconnect, re-issued on reconnect — `hostsMsg` after `Connect`); `recvCmd` tags each event with its host; `handleEvent` routes `sandbox_changed`/`removed` into `hostAgg` for that host (not only `m.sandboxes`), notifications keep `host_id` so `ackVisibleCmd` acks on the originating daemon; the dropped-stream branch at `app.go:649` re-subscribes after the reload path; `teatest` tests in `src/apps/switchboard-tui/internal/ui/notify_test.go` with two fake hosts (a remote `sandbox_changed` updates the remote row live; ack goes to the right fake; reconnect re-subscribes)
- [X] T016 Third-screen skeleton + header strip in `src/apps/switchboard-tui/internal/ui/app.go`, `keys.go`, `daemons.go` and new `src/apps/switchboard-tui/internal/ui/orchestrators.go` (FR-142/FR-143): `screenOrchestrators` in the enum; `ScreenNext` from `screenDaemons` → orchestrators (no wrap), `ScreenPrev` from orchestrators → daemons; `keys.go` help labels become dynamic via `m.screenNextLabel()`/`screenPrevLabel()` ("daemons" | "orchestrators" | "sandboxes"); `header()` renders `⚡ Switchboard   Sandboxes · Daemons · Orchestrators  (shift+←/→)` on its **existing line** with the current screen in `selectedStyle` on every top-level screen (`bodyHeight()` unchanged); `orchState{rows []orchRow; cursor int; …}` with a placeholder body `(no orchestrators)` and `esc`/`shift+←` back, cursor preserved across switches; `teatest` tests in `src/apps/switchboard-tui/internal/ui/orchestrators_test.go` (cycle both ways preserves list cursor, daemon cursor and orchestrator cursor; the strip highlights the current screen on all three; labels change per screen) (depends on T014)
- [X] T017 [P] E2E harness growth in `src/apps/switchboard-tui-e2e/harness.go` (research R16): `startSecondDaemon(t, daemonBin, sbxDir)` starting a second `sxbd` on its own socket/data dir/workspace root with `SWITCHBOARDD_HOST_ID=remote`; `stubSSH(t, remoteSock, daemonBin) dir` writing an `ssh` script onto `PATH` that ignores its arguments and `exec`s `<daemonBin> dial-stdio` with `SWITCHBOARDD_SOCKET=<remoteSock>`; `stubSbx` learns `mcp add`/`mcp load`/`mcp rm` (exit 0, logged to `sbx.log`) if the 008 stub does not already; `spawnTUIWithHosts(t, tuiBin, sock, srcRoot, configDir)` seeding `hosts.toml` with the remote entry (`kind = "ssh"`, `ssh_target = "remote"`); a smoke test in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUITwoDaemonsE2E` that connects both and sees both hosts on the daemon screen

**Checkpoint**: Buckets, controller helpers, EXITED, hub mutex, managed blocks, empty-workspace launches, per-host streams, the third screen + strip, the fake daemon and the two-daemon harness exist — US1–US7 can proceed (US1 and US2 in parallel if staffed).

---

## Phase 3: User Story 1 - Create a named orchestrator on a machine (Priority: P1) 🎯 MVP

**Goal**: From the Orchestrators screen, create an orchestrator by name on any connected machine;
it is hosted in-process by that machine's daemon, persisted, listed with its machine, reachability,
worker count and queue depths, renamed, and removed behind a confirmation that names the
consequences. Opening one shows the six management views (populated by later stories) and the
Hosts view with copy-from-my-hosts.

**Requirements**: FR-105, FR-106, FR-142, FR-143, FR-148, FR-153.

**Independent Test**: Create `review-bot` on a remote machine from the TUI, quit and restart the
TUI, confirm it is listed with that machine and reachable, confirm a second `review-bot` is refused,
then remove it and confirm the name is free (quickstart Scenario 1).

### Implementation for User Story 1 — daemon

- [X] T018 [US1] Create package `src/services/switchboardd/internal/orchestrator/` (research R1): `doc.go`; `errors.go` with the sentinel errors T013 maps; `constants.go` with every R15 constant (`linkDialTimeout`, `linkRetryMin/Max`, `linkCallTimeout`, `deliverTimeout`, `reviewGrace`, `reconcileTimeout`, `minIntervalSeconds`, `historyKeep`, `maxArgumentBytes`, `maxPromptBytes`); `store.go` wrapping T007's registry calls (`loadAll`, `save`, `update`, run put/list/get/delete, `trimHistory(orchID)` keeping the newest `historyKeep` finished runs); `service.go` with `Service{reg, mgr *sandbox.Manager, hub *agent.Hub, hostID string, dial dial.Func, local WorkerAPI, clock, runtimes map[string]*runtime}`, `NewService(Deps)`, `Start(ctx)` (load records, start one `runtime` per record), `Stop()`, `List() []*pb.OrchestratorSummary` (derived counts from the runs bucket), `Get(id) (*pb.OrchestratorDetail, error)`, `Create(name) (*pb.Orchestrator, error)` (name `1–64`, `ErrNameTaken` on this daemon, `uuid.NewString()` id, `host_id` = daemon's, empty hosts/actions/events, starts the runtime, emits `orchestrator_changed`; the reasoning-sandbox launch is wired in US7 T080), `Rename(id, name)` (label only; pushes `UpdateWorker{name}` to reachable workers via the runtime — no-op until US2), `Remove(id) (*pb.RemoveOrchestratorResponse, error)` (stop runtime; fail in-flight runs + cancel queued ones (US3 fills the run side); detach reachable workers with `force`; destroy the reasoning sandbox (US7); delete record + runs; emit); `SetHosts(id, hosts)` (host ids unique; `ErrPrecondition` naming workers when an entry with workers is removed); `emitChanged(id)` publishing `Event.orchestrator_changed{host_id, orchestrator_id}` through `hub.Publish`
- [X] T019 [US1] Host links in `src/services/switchboardd/internal/orchestrator/links.go` (research R2/R6, the client half): `type WorkerAPI interface` = the five worker-side calls + `ListSandboxes` + `Subscribe` (so the local host is served in-process by an adapter over `worker.Service`/`sandbox.Manager`/`hub` — `localHost` — and a remote host by `pb.SwitchboardClient` over a `dial.Conn`); `hostLink{entry *pb.OrchestratorHost; api WorkerAPI; up bool; lastErr string; cancel}`; `runtime.dialLoop(entry)` using `dial.DialCommand(ctx, dial.SSHCommandBatch(target, opts))` under `linkDialTimeout`, backoff `linkRetryMin`→`linkRetryMax` while down, `UNIMPLEMENTED` on the first worker-side call surfaced as `lastErr = "daemon on <host> is too old — update with u"`; on (re)connect run `reconcile(host)` (US2 fills it) and open the `Subscribe(replay=false)` stream feeding `runtime.onSandboxEvent` (US3 folds it); `OrchestratorLink` client stream opened with `hello{id, name}` first, `ping` every 30 s, `call` frames dispatched to `tools.go` (US6) and answered with `result`; `reachability(host) (bool, string)` for `OrchestratorDetail.hosts[].reachable/error`
- [X] T020 [US1] gRPC handlers in `src/services/switchboardd/internal/grpc/orchestrator_rpcs.go` for `ListOrchestrators`, `GetOrchestrator`, `CreateOrchestrator`, `RenameOrchestrator`, `RemoveOrchestrator`, `SetOrchestratorHosts` over `s.orchestrator` with `orchStatus`; construct `orchestrator.NewService` in `src/services/switchboardd/cmd/sxbd/main.go` after the MCP manager with `dial.DialCommand`-based `dial.Func`, start it before `Serve` and stop it on shutdown (depends on T018, T019)

### Tests for User Story 1 — daemon

- [X] T021 [P] [US1] `src/services/switchboardd/internal/orchestrator/service_test.go`: create assigns a uuid and persists; duplicate name ⇒ `ErrNameTaken`; rename changes only `name` (id, hosts, actions byte-identical); remove deletes the record and every run key; `SetHosts` refuses removing a host that still has workers; `List` counts derive from the runs bucket; `Start` on a registry with two records starts two runtimes; `orchestrator_changed` emitted on every mutation (fake hub) · `src/services/switchboardd/internal/orchestrator/links_test.go`: a fake `dial.Func` that fails ⇒ `reachable=false` with the error and a retry after backoff (fake clock); a fake that returns an in-process `pb.SwitchboardClient` answering UNIMPLEMENTED ⇒ the "too old" message; `hello` is the first link frame; `ping` cadence under the fake clock
- [X] T022 [P] [US1] `src/services/switchboardd/internal/grpc/orchestrator_rpcs_test.go` over the in-process socket: each of the six RPCs round-trips; ALREADY_EXISTS on a duplicate name; NOT_FOUND on an unknown id; FAILED_PRECONDITION from `SetOrchestratorHosts` carries the worker names

### Implementation for User Story 1 — client

- [X] T023 [US1] `src/apps/switchboard-tui/internal/client/orchestrator.go`: `ListOrchestrators`, `GetOrchestrator`, `CreateOrchestrator`, `RenameOrchestrator`, `RemoveOrchestrator`, `SetOrchestratorHosts` typed wrappers (errors pass through with their codes; `UNIMPLEMENTED` wrapped as `client.ErrDaemonTooOld` naming the host); fill the matching `fakeDaemon` methods from T014 with scripted results
- [X] T024 [US1] Orchestrators screen in `src/apps/switchboard-tui/internal/ui/orchestrators.go` (data-model "Client-side view models", FR-142): `orchRow{host string; hostName string; summary *pb.OrchestratorSummary; reachable bool; reason string}` built by fanning `ListOrchestrators` over every connected host on entry, on `hostsMsg`, on `Event.orchestrator_changed` and on `r`; rows `name · @machine · reachable|unreachable (reason) · N workers · queued Q / in flight F / review R`; `n` create = a `huh` form (name, validated non-empty ≤ 64 and unique across **all** rows — refused naming the existing one — plus a machine select over connected hosts) → `CreateOrchestrator` on that host; `R` rename (`huh` input) → `RenameOrchestrator`; `d` remove behind `confirm.go` whose text names the consequences from FR-153 (workers detached, reasoning sandbox destroyed, queued runs cancelled, in-flight runs failed, history discarded) → `RemoveOrchestrator`; an unreachable row offers only `d`-less, read-only display (no operation that cannot work, US1 scenario 4); `enter` opens the detail (T025); `orchestratorsHelp()` lists `n · R · d · enter · r · shift+←/→ · , · esc`
- [X] T025 [US1] Detail views scaffold in new `src/apps/switchboard-tui/internal/ui/orchestrator_views.go`: `orchDetailState{host, id string; detail *pb.OrchestratorDetail; view orchView (Workers|Actions|Queue|Events|Reasoning|Hosts); cursors [6]int; loading bool}` loaded by `GetOrchestrator` (+ `ListRuns` from US3 onward) and refreshed on `orchestrator_changed` for that id; `left`/`right` switch views with a tab bar `Workers · Actions · Queue · Events · Reasoning · Hosts`; each view renders its empty state now and is filled by its story (Workers T040, Actions/Queue T055, Events T068, Reasoning T084); the **Hosts** view is complete here: rows `display name · target · reachable|unreachable: <error>`, `a` add = pick from the TUI's own `store.HostStore.List()` minus hosts already present (copies `KnownHost{ID, DisplayName, SSHTarget, SSHOptions}` into `pb.OrchestratorHost`, never a password), `d` remove (confirm) → `SetOrchestratorHosts` with the edited list (FAILED_PRECONDITION text shown verbatim); `esc` back to the list with the list cursor intact; `orchDetailHelp()` per view
- [X] T026 [US1] Wire the host store into the UI model for T025's copy-from-my-hosts (`WithHostStore(st.Hosts())` in `src/apps/switchboard-tui/cmd/sxb/main.go` unless the model already holds it; a `fakeHostStore` for tests) and register `screenOrchestrators` + the detail/forms in `handleKey`, `forward` and `View` in `src/apps/switchboard-tui/internal/ui/app.go`

### Tests for User Story 1 — client

- [X] T027 [P] [US1] `src/apps/switchboard-tui/internal/ui/orchestrators_test.go` (`teatest`, two fake hosts): rows aggregate across hosts with machine names; `n` with a duplicate name across hosts is refused naming the existing one; create sends `CreateOrchestrator` to the chosen host; `R` renames; `d` shows the consequences text and sends `RemoveOrchestrator` only on confirm; an unreachable host's row says so and ignores `R`/`d`; `orchestrator_changed` reloads only that host; `enter` opens the detail and `esc` returns with the cursor kept
- [X] T028 [P] [US1] `src/apps/switchboard-tui/internal/ui/orchestrator_views_test.go` additions for the Hosts view: `a` lists only hosts not yet on the orchestrator, copies the entry without a password, sends `SetOrchestratorHosts`; `d` is confirm-gated; a FAILED_PRECONDITION message appears verbatim; `left`/`right` cycle the six views and keep per-view cursors
- [X] T029 [P] [US1] E2E in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUIOrchestratorCreateE2E`: `shift+→` twice reaches the Orchestrators screen (strip highlights it), `n` → `review-bot` on the local daemon → row listed *reachable*; quit and relaunch the TUI → still listed; `d` + confirm → gone

**Checkpoint**: US1 fully functional — an orchestrator exists on a machine, survives the TUI, is found by name, renamed and removed; the third screen, strip and host entries work. MVP deliverable.

---

## Phase 4: User Story 2 - Attach and detach worker sandboxes (Priority: P1)

**Goal**: The worker side every daemon runs — the controller record (CAS), the per-worker gateway
registration, the briefing block — plus the orchestrator's attach/detach/adopt over its host
links, the launch-flow pick, and the `o` keystroke with the add-host offer. No stop, restart or
recreate, ever.

**Requirements**: FR-107, FR-108, FR-109, FR-110, FR-111, FR-112, FR-132 (listing + detach),
FR-150 (the registration; the tools themselves are US6).

**Independent Test**: With one orchestrator and two sandboxes on different hosts, attach both from
the TUI, confirm each shows the orchestrator as its controller on both hosts' daemons, confirm a
second orchestrator is refused on either, detach one, and confirm it is free to attach elsewhere
(quickstart Scenario 2).

### Implementation for User Story 2 — daemon (worker side)

- [X] T030 [US2] Create package `src/services/switchboardd/internal/worker/` (research R1/R3/R4): `doc.go`; `constants.go` with `workerTokenBytes = 32`, `gatewayHost = "127.0.0.1"` (⚠ T003 may flip it to `host.docker.internal`), `mcpMutateTimeout` reuse from `mcp`; `gateway.go` with `mintToken() string` (hex of `crypto/rand` bytes), `serverName(sandboxID) string` (`mcp.WorkerServerPrefix + first 8 hex`), `endpointURL(hookPort, token) string` (`http://<gatewayHost>:<port>/orchestrator/w/<token>/mcp`), and `register(ctx, sandboxID, ref, token) error` = `sbx mcp add <name> --url <url> --skip-auth` (an SSRF warning line is logged, exit 0 required) then `sbx mcp load <name> --sandbox <ref>` (skipped with a log line when the sandbox is not running; retried from the bring-up hook), `unregister(ctx, sandboxID) error` = `sbx mcp rm <name> --force` (errors logged, never fatal), all through `mcp.CLI` argv helpers added to `src/services/switchboardd/internal/mcp/cli.go` (`argvAddRemote` reused; `argvRemove`; `argvLoad`)
- [X] T031 [US2] `src/services/switchboardd/internal/worker/controller.go`: `Service{mgr *sandbox.Manager; reg *registry.Registry; agents *agent.Registry; terms *terminal.Registry; hub *agent.Hub; cli *mcp.CLI; hookPort string; hostID string; links *linkIndex}`; `Attach(ctx, req *pb.AttachControllerRequest) (*pb.Sandbox, error)` = `reg.Update` CAS (`controller == nil` or same `orchestrator_id` ⇒ set `{orchestrator_id, name, host_id, attached_at=now, reasoning}`, keep `run`; else `ErrAlreadyControlled{Name}`), `PutWorkerToken`, `gateway.register` (failure after the CAS is logged and retried at bring-up; the controller stands), `briefing.Write` (T032), emit `sandbox_changed`; `Detach(ctx, sandboxID, orchID string, force bool) (*pb.Sandbox, error)` = refuse `ErrForbidden` when `orchID` ≠ recorded, `ErrPrecondition "reasoning sandbox"` unless `force`, revoke the token **first**, `gateway.unregister`, `briefing.Strip`, clear `controller` in one `reg.Update`, emit; `Update(ctx, req *pb.UpdateWorkerRequest)` = PERMISSION_DENIED on identity mismatch, apply `name`, `set_run`/`run`, `briefing` (rewrite the block), emit; `OnDestroy(sb)` registered via T010's hook: revoke the token and `unregister` (best effort); `OnBringUp(sb)` registered on the manager's bring-up path: re-run `mcp load` for a controlled sandbox whose registration exists (retry of the not-running skip)
- [X] T032 [US2] `src/services/switchboardd/internal/worker/briefing.go` (research R12, `contracts/orchestrator-mcp.md` §Briefing): `const blockBegin/blockEnd = "<!-- switchboard:orchestrator:begin -->"/"…end -->"`; `Write(workspacePath, body string) error` and `Strip(workspacePath) error` over `managedblock`; the **template renderer lives orchestrator-side** (`orchestrator/briefing.go`, T038) because it needs the action list — the worker side only writes what it is given
- [X] T033 [US2] Worker-side gRPC handlers in `src/services/switchboardd/internal/grpc/worker_rpcs.go`: `AttachController`, `DetachController`, `UpdateWorker` over `s.worker` (status mapping: `ErrAlreadyControlled` ⇒ ALREADY_EXISTS with message `sandbox is controlled by <name>`); `DeliverWorkerPrompt` and `OrchestratorLink` stay stubbed until US3/US6; construct `worker.NewService` in `src/services/switchboardd/cmd/sxbd/main.go` (hook port from `cfg.HookAddr`), register `OnDestroy`/`OnBringUp` on the manager, pass it into `sbgrpc.Config.Worker` and into `orchestrator.NewService` as the local `WorkerAPI` (depends on T030–T032)
- [X] T034 [US2] Launch-attach in `src/services/switchboardd/internal/grpc/sandbox_rpcs.go` `LaunchSandbox` + `src/services/switchboardd/internal/sandbox/manager.go` (FR-110, R4): when `req.controller` is set, pass `LaunchRequest.Controller`/`ControllerBriefing`; in `Manager.Launch`, right after `runner.Launch` and `applyMcp` and **before** the sandbox is marked RUNNING/emitted (the same point additive marks are loaded), call a new `Manager.SetControllerApplier(fn func(ctx, sb, controller, briefing) error)` that the worker service registers (CAS + token + `mcp add`/`load` + briefing); failure is logged (`orchestrator: skipped — <err>`) and never fails the launch; the record is emitted once with field 23 set so the TUI never shows it unattached; tests in `internal/sandbox/manager_controller_test.go` and `internal/grpc/sandbox_rpcs_test.go`

### Implementation for User Story 2 — daemon (orchestrator side)

- [X] T035 [US2] `src/services/switchboardd/internal/orchestrator/workers.go` (research R3/R6): `runtime.attach(hostID, sandboxID) (*pb.WorkerView, error)` = find the host link (`ErrPrecondition "no host entry for <host>"` when the orchestrator has none; the local host always exists), `ErrUnreachable{lastErr}` when the link is down, then `AttachController{sandbox_id, controller{id, name, host_id=orchestrator's host, reasoning=false}, briefing=render()}` over the link; on success add to the roster and persist; `runtime.detach(sandboxID) error` = `ErrPrecondition` naming the run when `controller.run != nil` on the latest record (FR-111) or the sandbox is the reasoning one, else `DetachController{force=false}` over the link, drop from the roster, persist; `runtime.reconcile(hostID)` = `ListSandboxes` on the link, **adopt** records whose `controller.orchestrator_id` is ours (launch-attached or recovered), **drop** roster entries whose record no longer carries us (fail any run on them "worker detached" — US3 fills `failRun`), push `UpdateWorker{name, briefing}` to each adopted worker, then US3's in-flight re-check; `runtime.workerViews() []*pb.WorkerView` with availability from the data-model table (`host link down` ⇒ UNREACHABLE, …, `controller.run != nil` ⇒ BUSY); `bundle() *pb.WorkerBundle` = `{controller{id, name, host_id}, briefing}`; `Service.Rename` now pushes `UpdateWorker{name, briefing}` to every reachable worker
- [X] T036 [US2] Handlers `GetWorkerBundle`, `AttachWorker`, `DetachWorker` in `src/services/switchboardd/internal/grpc/orchestrator_rpcs.go` (`ErrUnreachable` ⇒ UNAVAILABLE carrying ssh's stderr tail; `ErrAlreadyControlled` from the far side passes through as ALREADY_EXISTS naming the controller) (depends on T035)

### Tests for User Story 2 — daemon

- [X] T037 [P] [US2] `src/services/switchboardd/internal/worker/controller_test.go` + `gateway_test.go` with a stub `sbx` logging argv: attach records the controller, mints a 64-hex token stored only in the `worker_tokens` bucket (never in the `Sandbox` bytes), runs **exactly** `mcp add sxb-orch-<8> --url http://127.0.0.1:<port>/orchestrator/w/<token>/mcp --skip-auth` then `mcp load sxb-orch-<8> --sandbox <ref>` (argv pinned for T003), writes the block to `<workspace>/CLAUDE.md` preserving surrounding content; a second attach with another id ⇒ `ErrAlreadyControlled` naming the first and no `sbx` call; same id ⇒ idempotent re-register; 50 concurrent attaches from two ids ⇒ exactly one controller (SC-007); detach runs `mcp rm … --force`, strips the block, clears the controller, and the token is gone **before** `rm` runs (stub `rm` fails ⇒ still detached); detach by the wrong id ⇒ `ErrForbidden`; reasoning without force ⇒ `ErrPrecondition`; not-running sandbox skips `load` and `OnBringUp` runs it later; `OnDestroy` revokes + `rm`; attach/detach never call `stop`/`create`/`rm <sandbox>` (I-C2)
- [X] T038 [P] [US2] `src/services/switchboardd/internal/orchestrator/workers_test.go` with two in-process daemons (`startServer` ×2, `dial.Func` injected to the second socket; research R16): attach a sandbox on the "remote" daemon — its record there carries the controller; attach on a host with no entry ⇒ `ErrPrecondition "no host entry"`; a failing dial ⇒ `ErrUnreachable` with the ssh text; detach clears it there; reconcile adopts a record attached out of band (launch-attach) and drops one detached directly via `DetachController` (TUI path); rename pushes the new name into the remote record and its `CLAUDE.md`; `workerViews` availability table (fake records for each row) · `src/services/switchboardd/internal/grpc/worker_rpcs_test.go` + `orchestrator_rpcs_test.go` additions: the three worker RPCs round-trip; `AttachWorker` through a real link; ALREADY_EXISTS text names the controller; `LaunchSandbox` with `controller` yields a record with field 23 and runs `mcp add`/`load` after `create` and before RUNNING

### Implementation for User Story 2 — client

- [X] T039 [US2] `src/apps/switchboard-tui/internal/client/orchestrator.go`: `GetWorkerBundle`, `AttachWorker`, `DetachWorker`, `DetachController` (the direct worker-side call for the unreachable-orchestrator case, FR-112); fake additions
- [X] T040 [US2] Attach/detach keystroke in new `src/apps/switchboard-tui/internal/ui/orchestrator_attach.go` + `keys.go` (`OrchestratorAttach` = `o`, help "o attach/detach"): on an **unattached** sandbox `o` opens a picker over every orchestrator the TUI knows (T024's rows, reachable only) — choosing one whose `hosts[]` lacks the sandbox's host first shows `confirm.go` "add host <name> to <orchestrator>?" which copies the TUI's `KnownHost` via `SetOrchestratorHosts`, then `AttachWorker` on the orchestrator's host; on a **worker** `o` opens a detach confirm → `DetachWorker` on the orchestrator's host, or, when that host is not connected/unreachable, `DetachController` on the sandbox's own daemon (FR-112) — the reasoning sandbox refuses with "reasoning sandbox — not detachable"; errors verbatim in the status line (ALREADY_EXISTS "controlled by A", UNAVAILABLE ssh text, FAILED_PRECONDITION naming the run); the **Workers** view in `orchestrator_views.go` lists `WorkerView`s (`name · @host · state · availability (run action)`; reasoning rows marked) with `x` detach through the same confirm (FR-132) — `m` mark free lands in US3 T056
- [X] T041 [US2] Launch wizard `O` in `src/apps/switchboard-tui/internal/ui/launch.go` (FR-110): `launchState.orchestrator *orchRow`; `O` opens an orchestrator pick sub-mode (reachable rows whose host set includes the launch host, or the launch host itself); the modal shows `orchestrator: <name>`; `startLaunch` fetches `GetWorkerBundle` on that orchestrator's host and sets `LaunchSandboxRequest.controller`; `launchHelp()` gains `O orchestrator`

### Tests for User Story 2 — client

- [X] T042 [P] [US2] `src/apps/switchboard-tui/internal/ui/orchestrator_attach_test.go` (`teatest`): `o` on a plain sandbox lists reachable orchestrators and sends `AttachWorker` to the right host; a missing host entry triggers the add-host confirm and `SetOrchestratorHosts` before the attach; `o` on a worker confirms and sends `DetachWorker`; with the orchestrator's host disconnected it sends `DetachController` to the sandbox's host; ALREADY_EXISTS/UNAVAILABLE/FAILED_PRECONDITION messages appear verbatim; the Workers view `x` detaches · `src/apps/switchboard-tui/internal/ui/launch_test.go` additions: `O` picks an orchestrator and the launch request carries the bundle's controller
- [X] T043 [P] [US2] E2E in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUIAttachLocalAndRemoteE2E`: create `review-bot` on the local daemon; `o` on a local sandbox → `sbx.log` of the local stub shows `mcp add sxb-orch-… --url http://127.0.0.1:` then `mcp load`, and **no** `stop`/`rm`/`create`; `o` on a remote sandbox → add-host offer → accept → the remote stub's `sbx.log` shows the same two rows (the orchestrator's daemon reached it through the stub `ssh`); `o` again → detach → `mcp rm … --force` logged

**Checkpoint**: US1 + US2 — sandboxes on any reachable machine become workers and come back, with the controller on their own daemon, the gateway registration and briefing in place, and nothing restarted. Rows are still visible everywhere (hiding is US4).

---

## Phase 5: User Story 3 - Author an action and run it on a free worker (Priority: P1)

**Goal**: Actions (validated at save), user calls (validated and composed at call time), the
durable receipt-ordered queue, capacity-aware dispatch through guarded delivery, status folding
(running / awaiting input / awaiting review / failed), the completion-report and mark-free close
paths, cancel, retention, restart reconcile, the Actions + Queue views and inbox notifications.

**Requirements**: FR-117, FR-118, FR-119, FR-120, FR-121, FR-122, FR-123, FR-124, FR-125,
FR-126, FR-127, FR-128 (user kind), FR-129, FR-131, FR-133, FR-134, FR-135, FR-136, FR-137,
FR-138, FR-139, FR-140, FR-155, FR-156.

**Independent Test**: Attach two workers, create `review-pr`, call it three times with different
arguments, and observe: the first two runs start at once with correctly composed prompts, the
third starts only when one is reported complete, no worker ever has two runs, each completed run
shows the summary its agent reported, and a run whose agent went idle without reporting is held
for review until the developer marks the worker free (quickstart Scenario 3).

### Implementation for User Story 3 — daemon (actions, runs, dispatch)

- [X] T044 [US3] `src/services/switchboardd/internal/orchestrator/actions.go` (research R8, data-model "Action"): `validateAction(a *pb.Action, existing []*pb.Action) error` (name `[a-z0-9][a-z0-9-]*` ≤ 64 unique-or-replace, type ∈ {PROMPT, REASON}, body non-empty, every `{name}` placeholder names a declared argument — `{{` is a literal — argument names unique and `[A-Za-z_][A-Za-z0-9_]*`, `worker_tag` only on PROMPT; each refusal is `ErrInvalid` with the data-model message); `placeholders(body) []string`; `validateCall(a, args map[string]string) error` (required present or defaulted, no undeclared name, each value ≤ `maxArgumentBytes`); `compose(a, args) (string, error)` (replace every occurrence; unsupplied optional ⇒ default or empty; each **supplied** argument with no placeholder appended in declaration order as `\n\n<name>: <value>`, multi-line as `<name>:\n<value>`; result ≤ `maxPromptBytes` else `ErrInvalid` naming the limit); `Service.PutAction(id, a)` (replace by name, `updated_at`, persist, emit, push briefings via the runtime), `Service.DeleteAction(id, name) (disabled []string)` (US5 disables the timed events; queued runs untouched)
- [X] T045 [US3] `src/services/switchboardd/internal/orchestrator/runs.go` (data-model "Run"): `newRunID(orchID, seq)` = `run-<orch8>-<seq>`; `Service.CallAction(id, action, args, kind pb.EventKind, origin, originLabel string) (*pb.Run, error)` = validate + compose, `NextRunSeq`, `PutRun{state: QUEUED, received_at}`, emit, trigger a dispatch pass, return with `position`; `ListRuns(id) (*pb.ListRunsResponse)` (queued in key order with positions, in flight, history newest first); `GetRun`; `CancelRun` (QUEUED only, else `ErrPrecondition`); the transitions as methods on `runtime`: `startRun(run, sandbox)` (RUNNING, `started_at`, `sandbox_id`, `sandbox_host_id`), `runNeedsInput(run)`, `runResumed(run)`, `runAwaitingReview(run)` (+ `UpdateWorker{set_run, run{needs_review: true}}` + notification), `completeRun(run, summary, byDeveloper, failed bool)` (`ended_at`, `summary` exactly once, `closed_by_developer`; `UpdateWorker{set_run, run: nil}`; `trimHistory`), `failRun(run, reason)` (`UpdateWorker{run: nil}` when the sandbox still exists; `RUN_FAILED` notification); `MarkWorkerFree(id, sandboxID, failed, note)` (the run AWAITING_REVIEW on that sandbox, else `ErrPrecondition`); `Remove` now fails in-flight and cancels queued runs; notifications via `hub.EmitNotification(workerSandboxID, kind, msg, now)` with messages `orchestrator <name>: run <id> (<action>) …` (FR-133; the notification carries the worker's sandbox id so the inbox lands on the row)
- [X] T046 [US3] `src/services/switchboardd/internal/orchestrator/dispatch.go` (research R9, data-model "Dispatch pass"): `runtime.dispatch()` serialized by a mutex and coalesced by a `pending` flag; `free(w)` and `eligible(r)` exactly as the data model (REASON ⇒ the reasoning sandbox only; PROMPT with `worker_tag` ⇒ workers whose `Sandbox.tag` equals it); walk QUEUED runs in key order, claim the first free eligible sandbox not yet claimed in this pass, call `DeliverWorkerPrompt{sandbox_id, orchestrator_id, run_id, action, prompt}` over its host link under `deliverTimeout` — accepted ⇒ `startRun`; FAILED_PRECONDITION ⇒ run stays QUEUED and the sandbox is treated as not free for this pass; UNAVAILABLE/other ⇒ `failRun(run, reason)` (FR-124); triggers: run queued, sandbox event (status/state/controller), link up, run closed, reconcile
- [X] T047 [US3] Status folding + review grace + reconcile in `src/services/switchboardd/internal/orchestrator/runs.go` and `links.go` (research R7, FR-134): `runtime.onSandboxEvent(hostID, ev)` keeps the latest record per worker (`sandbox_changed`) and handles `removed`; for the run on that sandbox: `NEEDS_INPUT` ⇒ AWAITING_INPUT + `RUN_NEEDS_INPUT` notification (once per entry into the state); `WORKING` ⇒ RUNNING; `IDLE` with no summary ⇒ arm `reviewGrace` (fake-clock-driven `clock.AfterFunc`), on expiry still IDLE and unreported ⇒ AWAITING_REVIEW; `EXITED`, state `STOPPED`/`ERROR`/`DESTROYING` while RUNNING/AWAITING_INPUT, or `removed` ⇒ FAILED with that reason (removed while AWAITING_REVIEW ⇒ FAILED; stopped while AWAITING_REVIEW ⇒ stays); every change triggers `dispatch`; `reconcileInFlight(hostID)` on link up: for each in-flight run on that host read the latest record — IDLE + unreported ⇒ AWAITING_REVIEW, WORKING/NEEDS_INPUT ⇒ unchanged, gone/EXITED ⇒ FAILED, already AWAITING_REVIEW ⇒ unchanged; **never** re-deliver (I-R3); bounded by `reconcileTimeout`
- [X] T048 [US3] Guarded delivery on the worker side in `src/services/switchboardd/internal/worker/deliver.go` (research R7, `contracts/orchestrator-mcp.md` §Run frame): `Service.Deliver(ctx, req) (bool, error)` = inside one `reg.Update`: refuse `ErrPrecondition` unless `state == RUNNING`, `agent.status == IDLE`, `controller.orchestrator_id == req.orchestrator_id` and `controller.run == nil`; set `controller.run{id, action}`; after the write: `terms.Broadcaster(sandboxID)` to ensure the PTY reader + ring exist (unattended output drained), `agents.Prompt(sandboxID, spec, frame(req))` with `frame` = the documented constant (`[switchboard orchestrator "<name>" · run <id> · action "<action>"]\n\n<prompt>\n\n[When this run is finished, call orchestrator_report_completion …]`), emit `sandbox_changed`; a failed `Prompt` rolls `controller.run` back and returns the error; the same broadcaster-ensure is applied to `escapehatch`'s prompt path by having `cmd/sxbd/main.go` wrap `agents.Prompt` with `worker.EnsureDrained(terms)` for both callers; handler `DeliverWorkerPrompt` in `src/services/switchboardd/internal/grpc/worker_rpcs.go` (depends on T031)
- [X] T049 [US3] Handlers in `src/services/switchboardd/internal/grpc/orchestrator_rpcs.go`: `PutAction`, `DeleteAction`, `CallAction` (kind USER, origin ""), `ListRuns`, `GetRun`, `CancelRun`, `MarkWorkerFree`; `server.go`'s existing emit of sandbox transitions guarantees `controller.run` clearing from T010 reaches the orchestrator as a `sandbox_changed` (depends on T044–T047)

### Tests for User Story 3 — daemon

- [X] T050 [P] [US3] `src/services/switchboardd/internal/orchestrator/actions_test.go`: table of every save refusal from the data model (empty body, unknown type, duplicate name, argument declared twice, `{nope}`, `worker_tag` on REASON, bad name) with the exact messages; `{{` literal; composition golden cases (both placeholders replaced, `focus` appended, multi-line value layout, unsupplied optional with default, unsupplied optional without default ⇒ empty + nothing appended; SC-006); call refusals (missing required, undeclared, oversized value, oversized prompt) name the problem and queue nothing (SC-005)
- [X] T051 [P] [US3] `src/services/switchboardd/internal/orchestrator/dispatch_test.go` with fake links/records and a fake clock: order-of-receipt property over 200 random call sequences with random free/busy flips — no run starts before an earlier run the same sandbox could have taken (SC-003); a run with no eligible free sandbox never blocks later ones; `worker_tag` filters; REASON runs only reach the reasoning sandbox; a FAILED_PRECONDITION delivery leaves the run QUEUED and the sandbox unclaimed; an UNAVAILABLE delivery fails the run and moves on; at most one in-flight run per sandbox across the whole sequence (SC-004) · `runs_test.go`: state machine table (every edge in the data-model diagram, including the two "stopped while awaiting review keeps it" and "removed fails it" cases); `reviewGrace` elapses ⇒ AWAITING_REVIEW + `UpdateWorker{needs_review}` + `RUN_AWAITING_REVIEW` notification; a report during the grace ⇒ COMPLETED and no review; `MarkWorkerFree` closes with the note, `closed_by_developer`, and frees; second summary refused; cancel only when QUEUED; `historyKeep` trims the oldest finished run only; restart reconcile table (IDLE+unreported ⇒ review, WORKING ⇒ unchanged, gone ⇒ FAILED, no re-delivery) within `reconcileTimeout`; notifications: RUN_FAILED/RUN_NEEDS_INPUT/RUN_AWAITING_REVIEW raised, completions silent (SC-014)
- [X] T052 [P] [US3] `src/services/switchboardd/internal/worker/deliver_test.go`: refusal table (not RUNNING, agent WORKING, agent NEEDS_INPUT, wrong orchestrator, run already recorded) with no prompt typed; acceptance records `controller.run`, creates the broadcaster before typing (fake `terminal.Registry` provider observed), types the exact run frame through a fake `agent.Registry` (the composed prompt untouched inside), emits; a failing `Prompt` rolls back · `src/services/switchboardd/internal/grpc/worker_rpcs_test.go` additions: `DeliverWorkerPrompt` round-trips both outcomes; the two-daemon test in `orchestrator_rpcs_test.go` runs attach → call → delivery on the remote daemon → fake hook `NEEDS_INPUT`/`IDLE` events → review → `MarkWorkerFree` → free again, asserting the remote record's `controller.run` at each step
- [X] T053 [P] [US3] `src/services/switchboardd/internal/escapehatch/service_test.go` addition: the escape-hatch result delivery now goes through the broadcaster-ensuring prompt wrapper (a detached sandbox's PTY has a reader after a result is pushed)

### Implementation for User Story 3 — client

- [X] T054 [US3] `src/apps/switchboard-tui/internal/client/orchestrator.go`: `PutAction`, `DeleteAction`, `CallAction`, `ListRuns`, `GetRun`, `CancelRun`, `MarkWorkerFree`; fake additions (scripted `runs`, recorded calls)
- [X] T055 [US3] Actions + Queue views in `src/apps/switchboard-tui/internal/ui/orchestrator_views.go` and forms in new `src/apps/switchboard-tui/internal/ui/orchestrator_forms.go` (FR-129/FR-131/FR-135; the 008 `huh` pattern: heap-allocated values, `ctrl+s` saves, `esc` cancels): **Actions** rows `name (type) · description · tag` with `a` add / `e` edit (form: name, description, type select, body multi-line, worker tag (prompt only), arguments as a repeatable sub-form name/description/required/default), `d` delete (confirm; the response's `disabled_events` shown), `enter` = **call** — a form built from the declared arguments (label = name + description, `*` when required, default pre-filled; submission refused while a required value is empty) → `CallAction` → status `queued at position N`; daemon INVALID_ARGUMENT text shown verbatim; **Queue** rendered in three groups — queued (position, action, event kind + origin label, received), in progress (state, sandbox, started), history newest first (state, summary or `closed by developer: <note>`, reason) — `enter` opens a read-only overlay with the composed prompt (`GetRun`), `x` cancels a queued run (confirm), `m` marks free the sandbox of an awaiting-review run (T056's dialog); both views reload on `orchestrator_changed` for this id
- [X] T056 [US3] Mark-free dialog + keystroke in `src/apps/switchboard-tui/internal/ui/orchestrator_forms.go` + `sandbox_list.go` + `keys.go` (`MarkFree` = `m`, help "m mark free", FR-156): a `huh` form (outcome select *completed* (default) / *failed*, optional note) → `MarkWorkerFree` on the controller's host (`controller.host_id`); offered on a sandbox row **only** while `controller.run.needs_review`, in the Workers view for AWAITING_REVIEW rows and in the Queue view (T055); FAILED_PRECONDITION shown verbatim
- [X] T057 [US3] Inbox kinds in `src/apps/switchboard-tui/internal/ui/notifications.go`: `notifTitle`/`notifIcon` for `RUN_FAILED` ("run failed", `✗`), `RUN_AWAITING_REVIEW` ("run awaiting review", `⚑`), `RUN_NEEDS_INPUT` ("worker needs input", `?`); `enter` on one selects the worker's row (the notification's `sandbox_id`/`host_id`) exactly as today's kinds do

### Tests for User Story 3 — client

- [X] T058 [P] [US3] `src/apps/switchboard-tui/internal/ui/orchestrator_views_test.go` additions (`teatest`): the action form round-trips every field into `PutAction`; an INVALID_ARGUMENT reply shows verbatim and keeps the form open; `d` is confirm-gated and shows disabled events; the call form is built from the fake action's arguments with defaults pre-filled and refuses submission with an empty required field; a successful call shows the position; the Queue view renders the three groups from a scripted `ListRuns`, `enter` shows the prompt overlay, `x` cancels only a queued row, `m` on an awaiting-review row opens the dialog and sends `MarkWorkerFree` with outcome + note · `src/apps/switchboard-tui/internal/ui/notify_test.go` additions: the three new kinds render titles/icons and `enter` lands on the worker row on its host
- [X] T059 [P] [US3] E2E in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUIActionRunE2E`: create `review-bot`, attach a stub-launched sandbox, author `review-pr` with two arguments, call it with values → the Queue view shows it *running* and the local daemon's agent PTY (stub `sbx` `exec … claude` script that echoes stdin to a file) received the framed prompt containing both substituted values and the appended argument; a second call waits at position 1

**Checkpoint**: US1–US3 — the orchestrator's purpose works end to end by hand: actions, composed runs, ordered capacity-aware dispatch, honest completion (reported or reviewed), cancel, retention, restart reconcile, and the Actions/Queue views.

---

## Phase 6: User Story 4 - Workers stay out of the way; rows say what they are attached to (Priority: P2)

**Goal**: Workers and reasoning sandboxes leave the All/host/group tabs unless they need the
developer (then they lead every tab), the hidden **Orchestrator Sandboxes** tab is one key away,
every per-sandbox operation works on a worker wherever shown, and every row gains a third line
naming its MCP servers and orchestrator.

**Requirements**: FR-113, FR-114, FR-115, FR-116, FR-149.

**Independent Test**: Attach three workers, confirm none appear on the All, host or group tabs,
cause one agent to ask for input, confirm it appears at the top within seconds and disappears after
the developer answers, let another finish a run without reporting, confirm it surfaces marked for
review and leaves once marked free, open the Orchestrator Sandboxes tab, confirm all three are
listed with their controller and that each row's third line names it, and confirm switching tabs
hides the tab again (quickstart Scenario 4).

### Implementation for User Story 4

- [X] T060 [US4] Hide, surface and order in `src/apps/switchboard-tui/internal/ui/sandbox_list.go` (FR-113/FR-114, data-model I-C3): `needsDeveloper(sb) bool` = `agent.status == NEEDS_INPUT || controller.run.needs_review`; `hiddenWorker(sb) bool` = `controller != nil && !needsDeveloper(sb)`; `currentTabRows()` omits hidden workers on the All, host and group tabs (group memberships untouched in `store`), then **stable-partitions** every tab's rows so those with `needsDeveloper` come first (existing order kept within each half); `sandboxTitle`/`sandboxItem` add `worker · needs input` / `worker · review` (+ `reasoning` for reasoning sandboxes) in `warnStyle` on surfaced rows; `mergeSandboxUpdate`/`removeSandbox` re-partition so a status event moves a row within one render (SC-001)
- [X] T061 [US4] Orchestrator Sandboxes tab in `src/apps/switchboard-tui/internal/ui/sandbox_list.go` + `keys.go` (`OrchestratedTab` = `O`, help "O orchestrated", FR-115): a `tabOrchestrated` kind with `m.showOrchTab bool`; `O` on the list sets the flag, rebuilds tabs and selects it; `switchTab` to any other tab clears the flag (the tab leaves the bar); `rebuildTabs()` keeps the tab while the flag is set; its rows = every sandbox in `hostAgg` with a controller (all hosts), reasoning ones marked `reasoning`, shown with `controller.name`, state, availability derived client-side (same table as the daemon's, minus reachability) and `controller.run.action`; a status line names orchestrators the TUI knows to be unreachable (from T024's rows); the tab stays open and empty if the last worker is detached while viewing it
- [X] T062 [US4] Three-line delegate in new `src/apps/switchboard-tui/internal/ui/list_delegate.go` (research R13, FR-149): `sandboxDelegate` implementing `list.ItemDelegate` with `Height() == 3`, `Spacing() == 0`, rendering title / desc / third line in the default delegate's styles; `thirdLine(sb) string` = `mcp: <a, b>` + ` · orchestrator: <controller.name>` (either half omitted; empty when both are), truncated to the row width with `…`, never wrapped; `newSandboxList` uses it; the 008 `mcp:` badge is **removed** from `sandboxItem`'s line 2; `mainListHeight` reads the delegate's height; `list_common.go`'s item list keeps the default delegate for the other lists
- [X] T063 [US4] Per-sandbox keys on workers wherever shown (FR-116) in `src/apps/switchboard-tui/internal/ui/sandbox_list.go` + `keys.go`: `updateListKey` dispatches every existing key (`t`, `T`, `v`, `s`, `d`, `R`, `#`, `S`, `A`, `M`, `p`, `l`, prompt path) on `tabOrchestrated` rows and surfaced workers unchanged, plus `o` (T040) and `m` (T056); `listHelp()` adds `o`, `m` (contextual: only while `needsReview`) and `O`; `coverage_test.go`'s wide-footer width adjusted if needed

### Tests for User Story 4

- [X] T064 [P] [US4] `src/apps/switchboard-tui/internal/ui/sandbox_list_test.go` (new or `tabs_test.go` additions, `teatest`, two fake hosts): three controlled sandboxes are absent from All/host/group tabs and their group memberships survive; a `sandbox_changed` to `NEEDS_INPUT` surfaces the row at the top marked `worker · needs input` and a later `IDLE` hides it; `controller.run.needs_review` surfaces it marked `review` and clearing hides it; an unattached `NEEDS_INPUT` sandbox also leads its tab; order within each half is stable; `O` shows and selects the tab listing all controlled sandboxes across hosts with reasoning marked, `left`/`right` hides it, and it stays open-and-empty when the last worker detaches; `o`/`m`/`t`/`d` dispatch on the tab's rows
- [X] T065 [P] [US4] `src/apps/switchboard-tui/internal/ui/list_delegate_test.go`: third line `mcp: notion · orchestrator: review-bot` / `mcp: notion` / `orchestrator: review-bot` / blank; truncation with `…` at width; `Height()==3`, `Spacing()==0`; a `teatest` snapshot in `model_test.go` pins a 2-row list's rendered height; the 008 badge no longer appears on line 2 (`mcp_test.go`/`sandbox_mcp_test.go` assertions moved to line 3)
- [X] T066 [P] [US4] E2E in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUIHiddenWorkersE2E`: after attaching, the row is gone from All; `O` shows the tab with the row and its third line `orchestrator: review-bot`; `right` hides the tab; a stub hook POST (`needs_input`) to the daemon's hook server surfaces the row at the top marked `needs input`

**Checkpoint**: US4 — orchestrated sandboxes are out of the way, back on demand, surfaced when needed, operable everywhere, and every row says what it is wired to.

---

## Phase 7: User Story 5 - Events call actions without the developer (Priority: P2)

**Goal**: Timed events (interval ≥ 1 min, enabled flag, dedupe on by default counting queued +
in-flight, phase-preserving no-replay restart, disabled with reason when the action is deleted)
in the **Events** view, and triggered events attributed to a calling sandbox (the tool semantics
behind `orchestrator_call_action`, exposed to agents in US6).

**Requirements**: FR-128 (timed + triggered kinds), FR-130, FR-144, FR-157.

**Independent Test**: Create a timed event on a 1-minute interval and confirm one run per minute
with the event as origin; hold every worker busy and confirm the event never has more than one
run queued or in flight while dedupe is on, and one per minute once it is off; from a worker's
agent call an action through the gateway (US6) and confirm the run's origin is that worker; call
from a detached sandbox and confirm rejection (quickstart Scenario 5).

### Implementation for User Story 5 — daemon

- [X] T067 [US5] `src/services/switchboardd/internal/orchestrator/scheduler.go` (research R10, data-model "TimedEvent"): `Service.PutTimedEvent(id, ev)` (`interval_seconds ≥ minIntervalSeconds`, action must exist, id `tev-<seq>` on create, `enabled` default true on create, `next_fire_at = now + interval` on create or interval change; persist, emit, re-arm); `DeleteTimedEvent`; `DeleteAction` now sets `enabled=false` + `disabled_reason = "action <name> was deleted"` on events naming it and returns them; `runtime.scheduler` holds one `clock.Timer` armed for the earliest enabled `next_fire_at`; on fire: if `!dedupe_disabled` and any run with `origin == ev.id` is QUEUED or in flight ⇒ `last_skipped_at = now`, `skipped_count++` (skip), else `CallAction(ev.action, ev.arguments, TIMED, ev.id, ev.name)` and `last_fired_at = now`; either way `next_fire_at += interval` (repeat while `≤ now` to keep phase without replay) and persist; on runtime start: `next_fire_at = last_fired_at + k·interval` for the smallest `k` with a future value (never replays), or `now + interval` when never fired; disable stops the timer for that event
- [X] T068 [US5] Triggered calls in `src/services/switchboardd/internal/orchestrator/tools.go` (FR-144, FR-150): `runtime.handleCall(hostID, call *pb.OrchestratorLinkFrame_Call) *Result` dispatching on `call.tool` — this task implements `orchestrator_call_action` (parse `args_json` `{action, arguments}`; refuse unless `call.sandbox_id` is a current worker or the reasoning sandbox of **this** orchestrator on **that** host — `"not a worker of orchestrator <name>"`; `CallAction(kind TRIGGERED, origin = sandbox id, origin_label = display name)`; result `{run_id, position, state: "queued"}` or `{ok:false, error}`); `orchestrator_list_actions`, `_check_run`, `_report_completion` are filled in US6 T074; handlers `PutTimedEvent`, `DeleteTimedEvent` in `src/services/switchboardd/internal/grpc/orchestrator_rpcs.go`

### Tests for User Story 5 — daemon

- [X] T069 [P] [US5] `src/services/switchboardd/internal/orchestrator/scheduler_test.go` with the fake clock: fires at `next_fire_at` within the tick and queues a TIMED run with origin = event id and the fixed arguments; dedupe skips while a run is QUEUED, RUNNING, AWAITING_INPUT or AWAITING_REVIEW (records the skip) and fires again after close (SC-012); `dedupe_disabled` queues every firing; `interval 59` refused; unknown action refused; disable stops firing, enable resumes; restart with `last_fired_at` in the past resumes on phase without replay (3 missed intervals ⇒ 0 runs, next fire at the next slot); deleting the action disables with the reason and `DeleteAction` returns the event name · `tools_test.go` (call_action part): a call from a current worker queues a TRIGGERED run with that origin (SC-013); from a detached or foreign sandbox ⇒ refused, nothing queued; validation errors pass through verbatim; a `reason` action from a worker is accepted and targets the reasoning sandbox (US7)

### Implementation for User Story 5 — client

- [X] T070 [US5] `src/apps/switchboard-tui/internal/client/orchestrator.go`: `PutTimedEvent`, `DeleteTimedEvent`; fake additions
- [X] T071 [US5] Events view in `src/apps/switchboard-tui/internal/ui/orchestrator_views.go` + `orchestrator_forms.go` (FR-130/FR-157): rows `name · action · every Nm · next <relative time> · waiting <count of QUEUED runs with origin = id> · skipped <n>` with `[on]`/`[off]` and `disabled: <reason>` when set; `a` add / `e` edit (`huh` form: name, action select over the orchestrator's actions, interval minutes ≥ 1, fixed arguments built from the action's declared arguments like the call form, enabled toggle, **dedupe** toggle defaulting to on with the one-line explanation "skip a firing while a run of this event is queued or in flight"), `d` delete (confirm), `space` toggles enabled → `PutTimedEvent`; reload on `orchestrator_changed`

### Tests for User Story 5 — client

- [X] T072 [P] [US5] `src/apps/switchboard-tui/internal/ui/orchestrator_views_test.go` additions (`teatest`): the event form round-trips into `PutTimedEvent` with `dedupe_disabled=false` by default and `true` when toggled off; interval below 1 refused client-side; `space` flips `enabled`; rows show next-fire, waiting count (from scripted `ListRuns`) and `disabled: action review-pr was deleted`
- [X] T073 [P] [US5] E2E in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUITimedEventE2E`: create a timed event `poll` on `review-pr` (interval 1 minute, dedupe on) through the Events form; the row shows `every 1m`, `[on]`, a next-fire time and `dedupe`; `space` flips it to `[off]` and back; quit and relaunch the TUI — the event is still listed (persisted on the daemon). Firing itself is covered by T069 under the fake clock (no env var or test-only clock in the daemon binary, research R15)

**Checkpoint**: US5 — the orchestrator runs unattended: timers fire on schedule without pile-ups, and agents' calls are attributed and validated like the developer's.

---

## Phase 8: User Story 6 - A worker's agent reaches the orchestrator through its MCP gateway (Priority: P3)

**Goal**: The MCP endpoint on the hook server (go-sdk, stateless streamable HTTP, token-addressed,
per-sandbox attribution), the four tools relayed over the link, the completion report, and the
briefing rendered from the template and kept accurate on rename and action changes.

**Requirements**: FR-150, FR-151, FR-152, FR-154, FR-155 (the report tool).

**Independent Test**: Attach a worker, confirm from inside it that the orchestrator's tools are
listed in the gateway and that its briefing names the orchestrator, its tools, the expected
behaviours and the completion report; run an action on it and confirm the agent reports completion
unprompted; list the actions and call one from the agent; confirm the run is queued with that
worker as origin; detach and confirm a further call is refused (quickstart Scenario 6).

### Implementation for User Story 6 — daemon

- [X] T074 [US6] Tool semantics in `src/services/switchboardd/internal/orchestrator/tools.go` (`contracts/orchestrator-mcp.md` §Tools): `orchestrator_list_actions` ⇒ `{orchestrator, actions[{name, description, type, worker_tag, arguments[{name, description, required, default}]}]}`; `orchestrator_check_run{run_id}` ⇒ `{run_id, action, state (lower-snake), reason, summary, closed_by_developer}` or error on unknown id; `orchestrator_report_completion{summary}` (1–2000 chars) ⇒ the run **recorded on the calling sandbox** (`controller.run.id` from the latest record, or the in-flight run whose `sandbox_id` matches) — RUNNING/AWAITING_INPUT/AWAITING_REVIEW ⇒ `completeRun(summary)` and `{ok:true, run_id, state:"completed"}`; no run, already summarized, or caller not a current worker/reasoning sandbox ⇒ `{ok:false, error}` (FR-155, SC-018); a report cancels a pending review-grace timer; every tool refuses callers that are not current sandboxes of this orchestrator
- [X] T075 [US6] Briefing renderer in `src/services/switchboardd/internal/orchestrator/briefing.go` (`contracts/orchestrator-mcp.md` §Briefing, FR-154): `renderBriefing(name string, reasoning bool, actions []*pb.Action) string` producing the template verbatim (role sentence variant for the reasoning sandbox; the tool list; the per-type workflow; "Actions currently offered" with each action's description and arguments, `(none yet)` when empty); `renderInstructions(name, reasoning)` = the short summary for the MCP server's `instructions`; `runtime.pushBriefings()` after `PutAction`/`DeleteAction`/`Rename` sends `UpdateWorker{briefing}` (+ `name` on rename) to every reachable worker and the reasoning sandbox and is retried by `reconcile` for hosts that were down (FR-152 "kept accurate")
- [X] T076 [US6] Link acceptor + relay on the worker side in `src/services/switchboardd/internal/worker/relay.go` (research R6, the server half): `linkIndex` keyed by orchestrator id; `OrchestratorLink` handler in `src/services/switchboardd/internal/grpc/worker_rpcs.go` requires `hello` first (INVALID_ARGUMENT otherwise), indexes the stream (a newer `hello` for the same id replaces and closes the older), answers `ping`, routes `result` frames to the waiting call by id; `Service.Call(ctx, orchID, sandboxID, tool, argsJSON) (bodyJSON string, err error)` sends `call{id, sandbox_id, tool, args_json}` and waits up to `linkCallTimeout`; link absent or timeout ⇒ `ErrLinkDown{name}` rendered as the documented MCP error text (`orchestrator "<name>" is unreachable right now; retry in a moment — a failed report does not end the run`)
- [X] T077 [US6] MCP endpoint in `src/services/switchboardd/internal/worker/endpoint.go` (research R5, `contracts/orchestrator-mcp.md` §Endpoint): `Service.Handler() http.Handler` = `mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true})` mounted by `cmd/sxbd/main.go` at `mux.Handle("/orchestrator/w/", …)`; `getServer(req)` extracts `<token>` from `/orchestrator/w/<token>/mcp` (404 for any other shape), resolves it to a sandbox via a token index loaded from the `worker_tokens` bucket at start and kept in sync by attach/detach (nil ⇒ 404), and returns one cached `*mcp.Server{Name: "switchboard-orchestrator", Version: daemon version, Instructions: workerInstructions(controller.name, controller.reasoning)}` (a short local template naming the orchestrator, the four tools and the completion rule — the worker side never needs the action list for this) per live token with four `mcp.AddTool` registrations whose typed Go input/output structs match the contract JSON and whose handlers call `Service.Call(orchID, sandboxID, tool, args)` and return the body or an `IsError` tool result with the relay/refusal text; `GET`/`DELETE` ⇒ 405 by the SDK's stateless mode; token revocation drops the cached server
- [X] T078 [US6] Worker-side wiring in `src/services/switchboardd/cmd/sxbd/main.go`: mount T077's handler on the existing `mux` beside `/hook` and `/escape-hatch/run`; confirm the startup `sbx policy allow network localhost:<hookport>` (005) already covers the endpoint and say so in a comment; pass the daemon version string into `worker.NewService`

### Tests for User Story 6 — daemon

- [X] T079 [P] [US6] `src/services/switchboardd/internal/worker/endpoint_test.go` using the SDK's client (`mcp.NewClient` + `mcp.NewStreamableClientTransport`) against `httptest.NewServer(mux)`: `tools/list` returns exactly the four tools with the contract descriptions; `tools/call` on each reaches a fake link with `sandbox_id` = the token's sandbox (attribution before the handler); unknown/revoked token ⇒ 404; `GET` ⇒ 405; with no link the tool result is the documented unreachable text and `IsError`; the `instructions` field names the orchestrator · `relay_test.go`: `hello` required first; a replacing `hello` closes the older stream; `call` ↔ `result` matching by id; `linkCallTimeout` under a fake clock; `ping` answered
- [X] T080 [P] [US6] `src/services/switchboardd/internal/orchestrator/tools_test.go` + `briefing_test.go`: `list_actions` output golden; `check_run` for each state; `report_completion` closes RUNNING/AWAITING_INPUT/AWAITING_REVIEW with the summary within the same tick (SC-018), refuses no-run / second report / foreign sandbox, cancels the review grace; the rendered briefing golden against the contract template for a worker and for the reasoning sandbox with two actions and with none; `pushBriefings` after `PutAction`, `DeleteAction` and `Rename` reaches a two-daemon remote worker's `CLAUDE.md` and is retried on reconnect when the host was down (SC-017) · `src/services/switchboardd/internal/grpc/worker_rpcs_test.go` end-to-end over two daemons: a tool call made with the SDK client on the remote daemon's endpoint is relayed over a real `OrchestratorLink` to the orchestrator and answered

### Tests for User Story 6 — E2E

- [X] T081 [P] [US6] `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUIWorkerToolsE2E`: attach a sandbox on the second ("remote") daemon; POST an MCP `tools/call` of `orchestrator_call_action` to the remote daemon's hook port with the token read from its data dir (test helper under the `e2e` tag) ⇒ the TUI's Queue view shows a run with origin = that sandbox's name; `orchestrator_report_completion` after a delivered run ⇒ the run moves to history with the summary; `cat` the workspace `CLAUDE.md` ⇒ the block names `review-bot` and the four tools; after detach the same POST ⇒ 404

**Checkpoint**: US6 — a worker's agent has the tools and the briefing from the moment it is attached, can list, call, check and report, and loses access the moment it is detached.

---

## Phase 9: User Story 7 - The orchestrator reasons in its own sandbox (Priority: P3)

**Goal**: Every orchestrator gets a reasoning sandbox on its machine (empty workspace, default
agent) at creation; `reason` actions run only there under the same free rules; the **Reasoning**
view shows it, edits its setup and rebuilds it (destroy + launch); it is hidden/surfaced like a
worker, not detachable, destroyed with the orchestrator.

**Requirements**: FR-141, FR-145, FR-146, FR-147.

**Independent Test**: Create an orchestrator, confirm a default reasoning sandbox exists on its
machine and is absent from the everyday lists, create and call a `reason` action, confirm it runs
there and not on any worker, give the sandbox a folder in its setup, rebuild, and confirm the
folder is present (quickstart Scenario 7).

### Implementation for User Story 7 — daemon

- [X] T082 [US7] Reasoning sandbox lifecycle in `src/services/switchboardd/internal/orchestrator/workers.go` (research R11): `runtime.launchReasoning(ctx, setup *pb.ReasoningSetup, onLog)` = `mgr.Launch(LaunchRequest{DisplayName: "reasoning-<orch8>", EmptyWorkspace: len(setup.sources)==0, Sources: setup.sources, Config: setup.config, KitSources: resolved kits, Mcp: from setup.mcp_servers, Controller: {id, name, host_id, reasoning: true}, ControllerBriefing: renderBriefing(name, true, actions)})` on the orchestrator's own host, recording `reasoning_sandbox_id` and the roster entry; `Service.Create` now kicks it off asynchronously after returning (progress lines kept in a per-orchestrator `reasoningLog` surfaced through `OrchestratorSummary.reasoning_ready/_error`); `Service.RebuildReasoningSandbox(id, setup, onProgress)` = save `setup` when given, `ErrPrecondition "run <id> is on the reasoning sandbox"` while `controller.run != nil`, `mgr.Destroy(old)` then `launchReasoning` streaming `LaunchProgress`; `Remove` destroys it (`DetachController{force}` is unnecessary — destroy clears everything); `detach(reasoning)` ⇒ `ErrPrecondition "reasoning sandbox is not detachable"`; `removed` for the reasoning sandbox ⇒ `reasoning_ready=false` + `reasoning_error = "destroyed outside switchboard — rebuild"` and `reason` runs fail with that reason until rebuilt (dispatch has no target)
- [X] T083 [US7] Handler `RebuildReasoningSandbox` (server stream, reusing `sandbox_rpcs.go`'s `LaunchProgress` streaming helper) in `src/services/switchboardd/internal/grpc/orchestrator_rpcs.go`; `CreateOrchestrator`'s reply is unchanged (the sandbox arrives via `sandbox_changed` + `orchestrator_changed`) (depends on T082)

### Tests for User Story 7 — daemon

- [X] T084 [P] [US7] `src/services/switchboardd/internal/orchestrator/reasoning_test.go` (stub `sbx`): create launches `reasoning-<8>` with an empty workspace, no sources, the default agent and `controller.reasoning=true` on the orchestrator's host; a `reason` run dispatches only there and never to a worker, with the same states and report (SC-017); rebuild with a setup containing a folder runs `destroy` then `create` with that source and the record shows it; rebuild while a run is on it ⇒ `ErrPrecondition`; detach ⇒ refused; remove orchestrator ⇒ destroyed; `removed` out of band ⇒ `reasoning_error` set and `reason` runs fail with it; the reasoning sandbox surfaces/hides by the same record rules (asserted via field 23 only)

### Implementation for User Story 7 — client

- [X] T085 [US7] `src/apps/switchboard-tui/internal/client/orchestrator.go`: `RebuildReasoningSandbox(ctx, id, setup, onUpdate func(LaunchUpdate)) (*pb.Sandbox, error)` draining the stream like `Refresh`; fake additions
- [X] T086 [US7] Reasoning view in `src/apps/switchboard-tui/internal/ui/orchestrator_views.go` + `orchestrator_forms.go` (FR-145–FR-147): shows `reasoning-<8>` with state, availability, current run, and `coming up…` / `error: <reasoning_error>` from the summary; the saved setup (folders, configuration name, kits, MCP servers) or `default: empty context`; `e` edit setup = a sub-flow reusing the launch browser (`startBrowse` on the orchestrator's host root), the config picker and the kit picker, ending with `ctrl+s` → `RebuildReasoningSandbox{setup}` streamed into the op log (`l`) with a progress badge; `b` rebuild with the saved setup (confirm); FAILED_PRECONDITION shown verbatim; the sandbox list's `o` on a reasoning row refuses locally with "reasoning sandbox — not detachable"; the Orchestrator Sandboxes tab marks it `reasoning` (T061)

### Tests for User Story 7 — client

- [X] T087 [P] [US7] `src/apps/switchboard-tui/internal/ui/orchestrator_views_test.go` additions (`teatest`): the Reasoning view renders ready/coming-up/error states from the scripted summary; `e` walks the browser + pickers and sends `RebuildReasoningSandbox` with the chosen folder and kit; `b` is confirm-gated; a FAILED_PRECONDITION reply shows verbatim; `o` on a reasoning row is refused without any RPC
- [X] T088 [P] [US7] E2E in `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go` `TestTUIReasoningSandboxE2E`: creating `review-bot` makes the stub `sbx.log` show a `create` for `reasoning-…` with no source flags, the sandbox is absent from All and present on the `O` tab marked `reasoning`; a `reason` action call delivers to it (the stub agent file receives the frame); `b` rebuild logs `rm` then `create`

**Checkpoint**: All seven stories independently verified.

---

## Phase 10: Polish & Cross-Cutting Concerns

- [X] T089 [P] README: document the Orchestrators screen (shift+←/→ cycle and the header strip, `n`/`R`/`d`/`enter`, the six views and their keys), attach/detach (`o`, the add-host offer, SSH access requirements on the orchestrator's machine, BatchMode), `m` mark free and the review rule, `O` Orchestrator Sandboxes tab, the third row line, actions (placeholders `{name}`, appended arguments, limits), timed events + dedupe, the worker tools and briefing (`CLAUDE.md` block, run frame), the reasoning sandbox, the loopback gateway registration + the impersonation caveat (R4), the duplicate needs-input inbox line (R14), and the key table rows in `README.md`; add `src/libs/switchboard-dial` to the "Repository layout" and "Testing" sections
- [ ] T090 [P] Update `specs/009-orchestrator/contracts/orchestrator-mcp.md` and `research.md` R4/R5/R17 with T003's reconciled answers; if `gatewayHost` changed, update `worker/constants.go` and the pinned argv in T037; add the SSRF warning text, if any, to the contract's notes column
- [X] T091 Run the full gate from the repo root (`Makefile` targets): `make fmt-check vet lint test cover env-check e2e`; fix any module below the 90% floor (new packages `internal/orchestrator`, `internal/worker`, `internal/managedblock`, the `switchboard-dial` module, and `internal/ui`'s new files are the likely gaps); confirm `go.work`, `MODULES` and `cover.sh` all list the four modules + two e2e modules
- [ ] T092 Run `specs/009-orchestrator/quickstart.md` Scenarios 1–8 end-to-end on two real hosts (after T003 confirmed Scenario 0) and record SC-001..SC-019 results in `specs/009-orchestrator/quickstart.md` (a "Results" section)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: T001 → T002 (codegen); T004 after T002 → T005; T006 in parallel; T003 runs on a real host any time before merge
- **Foundational (Phase 2)**: T007–T012, T014, T017 in parallel after T002 (T009 is independent of the proto); T013 after T007; T015 after T014; T016 after T014 — **BLOCKS all user stories**
- **US1 (Phase 3)**: after Phase 2 — daemon T018 → T019 → T020; client T023 → T024 → T025 → T026; tests T021–T022, T027–T029 in parallel after their implementation
- **US2 (Phase 4)**: after US1 (needs `orchestrator.Service` + the screen) — worker side T030 → T031/T032 → T033 → T034; orchestrator side T035 → T036 (T035 needs T033's local `WorkerAPI`); client T039 → T040 → T041; tests T037–T038, T042–T043 afterwards. **T030–T033 (the worker package) depend only on Phase 2** and can start in parallel with US1 if staffed
- **US3 (Phase 5)**: after US2 — T044 → T045 → T046 → T047 (T047 also needs T019's `Subscribe` plumbing); T048 after T031; T049 after T044–T047; client T054 → T055 → T056 → T057; tests T050–T053, T058–T059 afterwards
- **US4 (Phase 6)**: after US2 (the controller record) and T015 (per-host streams); T060 → T061 → T062 → T063; tests T064–T066 afterwards. Independent of US3 except the `m` key (T056), which T063 wires if present
- **US5 (Phase 7)**: after US3 (`CallAction`, run states) — T067 and T068 in parallel; client T070 → T071; tests T069, T072–T073 afterwards
- **US6 (Phase 8)**: after US3 (run close) and US5's T068 (the `call_action` tool) — T074/T075 in parallel; T076 → T077 → T078; tests T079–T081 afterwards
- **US7 (Phase 9)**: after US3 (dispatch targets) and T011 (empty workspace) — T082 → T083; client T085 → T086; tests T084, T087–T088 afterwards
- **Polish (Phase 10)**: after every desired story

### User Story Dependencies

- **US1 (P1)**: Phase 2 only. MVP.
- **US2 (P1)**: US1 (service + screen). Its worker package is Phase-2-only and parallelizable.
- **US3 (P1)**: US2 (workers to deliver to; `DeliverWorkerPrompt` on the worker side).
- **US4 (P2)**: US2 + T015 — fully TUI-side; parallel with US3 if staffed (different files: `sandbox_list.go`, `list_delegate.go` vs `orchestrator_views.go`, `orchestrator_forms.go`; the shared `keys.go` edits are one-line bindings — land `o`/`m`/`O` together).
- **US5 (P2)**: US3.
- **US6 (P3)**: US3 + US5's T068.
- **US7 (P3)**: US3 + T011.

### Within Each User Story

- Worker side before orchestrator side within US2 (the local `WorkerAPI` is what the orchestrator calls)
- Daemon before client within a story (the client's fake can be scripted earlier, but the RPC round-trip tests need the handlers)
- Tests follow implementation and are parallel across files

### Parallel Opportunities

- Phase 1: T003, T004→T005 and T006 in parallel after T002
- Phase 2: T007, T008, T009, T010, T011, T012, T014, T017 in parallel; T013, T015, T016 once their inputs land
- After Phase 2 with three developers: A → US1 then US2's orchestrator side then US3; B → US2's worker package (T030–T033) then US6's worker side (T076–T077) then US7; C → US4 then US5's client then docs
- Every `_test.go` task marked [P] runs in parallel with its siblings
- `harness.go` is edited by T017 only; later E2E tasks add test files, not harness changes (coordinate any further stub verbs through T017's helpers)

---

## Parallel Example: Foundational + US2 worker side

```bash
# Phase 2, after `make proto`:
Task: "T007 registry — orchestrators/orchestrator_runs/worker_tokens buckets + run keys"
Task: "T008 agent — EXITED on PTY exit; Hub.Publish under the mutex"
Task: "T009 managedblock — marker-delimited block writer shared with escapehatch"
Task: "T010 sandbox — controller helpers; clear run on leaving RUNNING"
Task: "T011 sandbox — LaunchRequest.EmptyWorkspace"
Task: "T012 mcp — hide sxb-orch- registrations"
Task: "T014 ui — Daemon interface + fakeDaemon growth"
Task: "T017 e2e harness — second daemon + stub ssh"

# US2 worker package, in parallel with US1:
Task: "T030 worker/gateway.go — token, server name, mcp add/load/rm argv"
Task: "T031 worker/controller.go — CAS attach, detach, update, destroy/bring-up hooks"
Task: "T032 worker/briefing.go — managed CLAUDE.md block"
Task: "T037 worker tests — argv pinning, CAS race, token never in Sandbox bytes"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Phase 1 (T001–T002, T004–T006) and Phase 2 (T007–T017); start T003 on a real host in parallel
2. Phase 3 (US1): an orchestrator placed on a machine, listed, renamed, removed; the third screen and strip
3. **STOP and VALIDATE**: quickstart Scenario 1 on two daemons (SC-010, SC-011)

### Incremental Delivery

1. + US2 → workers attached and detached across machines with the gateway registration and briefing in place (Scenario 2; SC-007, SC-009)
2. + US3 → actions, ordered dispatch, honest completion and review, the Actions/Queue views (Scenario 3 + 8; SC-002–SC-006, SC-008, SC-014, SC-015, SC-018, SC-019)
3. + US4 → workers hidden/surfaced, the `O` tab, the third line (Scenario 4; SC-001, SC-016) — can ship before US3 if staffed in parallel
4. + US5 → timed and triggered events (Scenario 5; SC-012, SC-013)
5. + US6 → the worker's tools and briefing end to end (Scenario 6; SC-017)
6. + US7 → the reasoning sandbox and `reason` runs (Scenario 7)
7. Polish → docs + reconciliation write-back + full gate + quickstart results

### Notes

- Every new `sbx` argv lives in `internal/worker/gateway.go` (through `internal/mcp/cli.go`'s helpers) and nowhere else; T003's findings change that file, `worker/constants.go` (`gatewayHost`) and their pinned tests only
- `worker_token` never serializes into `Sandbox`: it lives in the `worker_tokens` bucket (T007) and the in-memory token index (T077); grep the proto and `Sandbox` marshalling tests for it (I-C4)
- No env vars: every bound is a constant in `orchestrator/constants.go` or `worker/constants.go` (research R15)
- The two `internal/agent` fixes (T008) ship with Phase 2 because US3's status folding (`EXITED` ⇒ failed) and the orchestrator's continuous `Publish` depend on them
- Commit after each task or logical group; stop at any checkpoint to validate the story independently
