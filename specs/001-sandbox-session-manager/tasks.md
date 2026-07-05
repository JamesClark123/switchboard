---
description: "Task list for Sandbox Session Manager (Switchboard) implementation"
---

# Tasks: Sandbox Session Manager (Switchboard)

**Input**: Design documents from `/specs/001-sandbox-session-manager/`

**Prerequisites**: plan.md (required), spec.md (required), research.md, data-model.md, contracts/switchboard.proto, quickstart.md

**Tests**: Test tasks ARE included. Rule VI (Multi-Level Testing Discipline) is NON-NEGOTIABLE and
mandates a unit/integration/E2E pyramid with a **90% coverage floor**. Per the plan's Constitution
Check, the Go-toolchain substitutes are `go test` + `testify`, `teatest` (TUI golden), and a PTY/`vhs`
E2E harness in place of Playwright.

**Organization**: Tasks are grouped by user story (P1–P3) so each story is independently implementable
and testable. The MVP is User Story 1 alone (single local host, default duplicate mode).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story the task belongs to (US1–US5)
- Exact file paths are included in every task

## Project layout (from plan.md — three Go modules under the category taxonomy, wired by `go.work`)

- `src/libs/switchboard-proto/` — shared gRPC/protobuf contract + domain types
- `src/services/switchboardd/` — per-host daemon (`cmd/switchboardd`, `internal/...`)
- `src/apps/switchboard-tui/` — Bubble Tea client (`cmd/switchboard`, `internal/...`)
- `src/apps/switchboard-tui-e2e/`, `src/services/switchboardd-e2e/` — E2E siblings (Rule VI)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Reconcile the scaffold to the mandated Go layout and stand up the toolchain.

- [X] T001 Reconcile workspace scaffold per plan Structure Decision: remove the stray top-level `apps/`
      and `packages/` dirs and reduce `pnpm-workspace.yaml` to the repo's outer shell (root scripts +
      Husky only), in `pnpm-workspace.yaml` and repo root.
- [X] T002 Create the Go workspace file `go.work` at repo root tying the three modules together.
- [X] T003 [P] Initialize the proto/lib module manifest `src/libs/switchboard-proto/go.mod` (module path
      `github.com/jamesclark123/switchboard/libs/switchboard-proto` per the proto `go_package`).
- [X] T004 [P] Initialize the daemon module manifest `src/services/switchboardd/go.mod`.
- [X] T005 [P] Initialize the TUI module manifest `src/apps/switchboard-tui/go.mod`.
- [X] T006 [P] Initialize the E2E module manifests `src/apps/switchboard-tui-e2e/go.mod` and
      `src/services/switchboardd-e2e/go.mod`.
- [X] T007 [P] Add `.golangci.yml` at repo root configuring `golangci-lint` (errcheck, staticcheck,
      govet, gofmt/goimports) — the Biome substitute for Go (Rule I/II adaptation).
- [X] T008 [P] Add a `Makefile` at repo root with `build`, `lint`, `vet`, `test`, `cover`, and
      `env-check` targets that drive the Rule V gate intent over `go.work`.
- [X] T009 Configure the Husky `pre-commit` hook in `.husky/pre-commit` to run `gofmt -l`,
      `golangci-lint run`, `go vet ./...`, `go test ./...`, and `env:check`; add the `prepare: husky`
      script to the root `package.json` (Rule V).
- [X] T010 [P] Add the protobuf codegen config + script (`buf.gen.yaml` or a `protoc` wrapper) in
      `src/libs/switchboard-proto/` to generate `gen/` from `proto/switchboard.proto`.
- [X] T011 [P] Add `.editorconfig` (utf-8, LF, final-newline) and `.gitignore` entries (`.env`,
      `dist/`, `cover.out`, `gen/` policy) at repo root.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The shared contract, persistence, transport, and app skeletons that every user story
builds on.

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete.

- [X] T012 Mirror `specs/001-sandbox-session-manager/contracts/switchboard.proto` into
      `src/libs/switchboard-proto/proto/switchboard.proto` and generate the Go gRPC stubs into
      `src/libs/switchboard-proto/gen/` (depends on T010).
- [X] T013 [P] Implement shared domain type helpers + enum mappings (per data-model.md) in
      `src/libs/switchboard-proto/types.go` (`libs` imports nothing else — dependency direction).
- [X] T014 [P] Daemon config package: env schema, fail-fast validation at startup, single typed `Config`
      in `src/services/switchboardd/internal/config/config.go`, plus committed
      `src/services/switchboardd/.env.example` and an `env:check` script entry (Rule VIII adaptation).
- [X] T015 [P] TUI config package: env schema + fail-fast validation in
      `src/apps/switchboard-tui/internal/config/config.go`, plus committed
      `src/apps/switchboard-tui/.env.example` and an `env:check` script entry (Rule VIII adaptation).
- [X] T016 bbolt registry store base (open `registry.db`, Sandbox CRUD, buckets) in
      `src/services/switchboardd/internal/registry/registry.go` (FR-002a/b, R4).
- [X] T017 gRPC server skeleton + `serve` subcommand listening on a Unix domain socket in
      `src/services/switchboardd/internal/grpc/server.go` and `src/services/switchboardd/cmd/switchboardd/main.go` (R1).
- [X] T018 gRPC client connection layer for the local Unix socket (`grpc.WithContextDialer`) in
      `src/apps/switchboard-tui/internal/client/client.go` (FR-003, R1).
- [X] T019 Bubble Tea root model + app bootstrap in `src/apps/switchboard-tui/internal/ui/app.go` and
      `src/apps/switchboard-tui/cmd/switchboard/main.go`.
- [X] T020 Client TOML store base (XDG config paths under `$XDG_CONFIG_HOME/switchboard/`, load/save) in
      `src/apps/switchboard-tui/internal/store/store.go` (R4).
- [X] T021 Implement `GetDaemonInfo` RPC (host_id, hostname, versions, `workspace_root`) in
      `src/services/switchboardd/internal/grpc/info.go` and wire the client handshake in
      `src/apps/switchboard-tui/internal/client/client.go` (FR-006).

**Checkpoint**: Contract generated, daemon serves on a socket, client connects locally, stores wired —
user stories can begin.

---

## Phase 3: User Story 1 - Fan out parallel work by duplicating directories (Priority: P1) 🎯 MVP

**Goal**: On a single local host, select directories, launch sandboxes seeded by verbatim duplicates
into the daemon-controlled folder, and manage their lifecycle (stop/restart/destroy) — originals never
touched.

**Independent Test**: On one machine with a local daemon, select two directories, launch in duplicate
mode, confirm a `running` sandbox appears, the controlled folder holds byte-for-byte copies, and the
originals are unchanged; launch three more and confirm four independent sandboxes (SC-001/002).

### Tests for User Story 1 ⚠️

- [X] T022 [P] [US1] gRPC contract/integration test for Launch/Stop/Restart/Destroy/List over an
      in-process Unix socket in `src/services/switchboardd/internal/grpc/sandbox_rpcs_test.go`.
- [X] T023 [P] [US1] Verbatim-duplication unit test (byte-identical copy, source unchanged, progress
      reported) in `src/services/switchboardd/internal/duplicate/duplicate_test.go` (SC-002, FR-028).
- [X] T024 [P] [US1] Registry re-adoption test (running containers re-attached on restart) in
      `src/services/switchboardd/internal/registry/readopt_test.go` (FR-002a, SC-012).
- [X] T025 [P] [US1] teatest golden test for the sandbox list + launch wizard in
      `src/apps/switchboard-tui/internal/ui/launch_test.go`.

### Implementation for User Story 1

- [X] T026 [P] [US1] Verbatim duplication package: streaming recursive copy reporting
      `bytes_copied`/`bytes_total`/`current_path`, read-only source access, writes only under the
      controlled folder, in `src/services/switchboardd/internal/duplicate/duplicate.go`
      (FR-010/010a/028, SC-002).
- [X] T027 [P] [US1] Pre-launch resource check (estimate copy size, disk/resource warnings) in
      `src/services/switchboardd/internal/resources/resources.go`, backing `CheckResources` (FR-012f, SC-013).
- [X] T028 [P] [US1] Source-candidate enumeration (list dirs/repos under a root, `is_repo` detection) in
      `src/services/switchboardd/internal/sandbox/sources.go`, backing `ListSourceCandidates` (FR-007).
- [X] T029 [US1] Sandbox lifecycle via `sbx`: launch (duplicate/clone), stop, restart-from-retained-copy,
      destroy-and-delete-copy in `src/services/switchboardd/internal/sandbox/lifecycle.go`
      (FR-001/008/009/012a/012b/012c; depends on T026, T016).
- [X] T030 [US1] Registry re-adoption on startup (load registry, list live containers, re-attach running
      → `running`, others → `stopped`, surface orphans) in
      `src/services/switchboardd/internal/registry/readopt.go` (FR-002a/b, SC-012).
- [X] T031 [US1] Implement `LaunchSandbox` (streams `LaunchProgress`), `StopSandbox`, `RestartSandbox`,
      `DestroySandbox`, `RenameSandbox`, `ListSandboxes`, `ListSourceCandidates`, `CheckResources` RPCs in
      `src/services/switchboardd/internal/grpc/sandbox_rpcs.go` (depends on T029, T027, T028).
- [X] T032 [P] [US1] TUI sandbox-list view rendering running/stopped state, display name/config label,
      seeded sources (FR-017/017a) in `src/apps/switchboard-tui/internal/ui/sandbox_list.go`.
- [X] T033 [US1] TUI launch wizard: source selection, duplicate(default)/clone toggle, live copy-progress
      indicator in `src/apps/switchboard-tui/internal/ui/launch.go` (FR-007/008/009/028).
- [X] T034 [US1] Client-side launch/stop/restart/destroy/rename calls wired to the list + progress stream
      in `src/apps/switchboard-tui/internal/client/sandbox.go`.
- [X] T035 [US1] Display-name defaulting (config label or generated) with user override, applied in the
      registry write path `src/services/switchboardd/internal/registry/registry.go` and surfaced via
      `RenameSandbox` in the TUI list `src/apps/switchboard-tui/internal/ui/sandbox_list.go` (FR-012e).

**Checkpoint**: US1 is a complete, demonstrable MVP — single local host, default duplicate mode, full
lifecycle.

---

## Phase 4: User Story 2 - Save and reuse sandbox configurations (Priority: P2)

**Goal**: Compose a configuration covering 100% of the `sbx` option surface, save it under a name, and
relaunch from it without re-entering options.

**Independent Test**: Create a config setting several `sbx` options, save it, launch from it, and confirm
the sandbox reflects exactly those options (SC-003/004).

### Tests for User Story 2 ⚠️

- [X] T036 [P] [US2] Test `sbx` option-manifest introspection + launch-time validation (unknown keys fail
      loudly) in `src/services/switchboardd/internal/sbxkit/manifest_test.go` (FR-014).
- [X] T037 [P] [US2] Test config TOML store round-trip + teatest of the config editor covering all manifest
      options in `src/apps/switchboard-tui/internal/store/config_test.go`.

### Implementation for User Story 2

- [X] T038 [P] [US2] `sbx` option enumeration → `OptionManifest` (prefer machine-readable schema, else
      parse `sbx --help`; version-stamped) in `src/services/switchboardd/internal/sbxkit/manifest.go`
      (FR-014; carries research R6 residual risk — confirm against a real `sbx`).
- [X] T039 [US2] `GetOptionManifest` RPC + launch-time `kit_options` validation against the manifest
      (fail loudly naming the offending key) in `src/services/switchboardd/internal/grpc/manifest.go`.
- [X] T040 [P] [US2] Configuration TOML store (`configs/*.toml` create/save/edit; edits apply to future
      launches only) in `src/apps/switchboard-tui/internal/store/config.go` (FR-013/015/016).
- [X] T041 [US2] TUI config editor rendering 100% of the manifest options plus the optional `AgentSpec`
      in `src/apps/switchboard-tui/internal/ui/config_editor.go` (FR-014/016a/016b).
- [X] T042 [US2] Wire a saved configuration into the launch flow as a frozen `ConfigSnapshot` (stored in
      the registry) in `src/apps/switchboard-tui/internal/ui/launch.go` and
      `src/services/switchboardd/internal/sandbox/lifecycle.go` (FR-002b/012d).

**Checkpoint**: US1 + US2 both work independently — fast, consistent repeat launches.

---

## Phase 5: User Story 3 - Manage sandboxes across multiple hosts over SSH (Priority: P2)

**Goal**: Connect the TUI to a local daemon and one or more remote daemons over SSH simultaneously; view
and manage sandboxes per host with clear connection state and resync on reconnect.

**Independent Test**: With one local and one SSH-reachable daemon, connect to both, launch a sandbox on
each, and confirm the host-grouped view attributes each to the correct host (SC-005/006).

### Tests for User Story 3 ⚠️

- [X] T043 [P] [US3] Test the SSH `dial-stdio` dialer + multi-daemon manager (drop/reconnect resyncs the
      same sandbox set) in `src/apps/switchboard-tui/internal/client/ssh_test.go` (SC-010).
- [X] T044 [P] [US3] Test the known-hosts TOML store in
      `src/apps/switchboard-tui/internal/store/hosts_test.go`.

### Implementation for User Story 3

- [X] T045 [P] [US3] `dial-stdio` subcommand bridging stdio ↔ the daemon's Unix socket in
      `src/services/switchboardd/cmd/switchboardd/main.go` and
      `src/services/switchboardd/internal/grpc/dialstdio.go` (R1).
- [X] T046 [P] [US3] Known-hosts TOML store (`hosts.toml`: local socket + SSH targets/options) in
      `src/apps/switchboard-tui/internal/store/hosts.go` (FR-002d).
- [X] T047 [US3] SSH `dial-stdio` client dialer (`ssh <host> switchboardd dial-stdio` → `net.Conn` via
      `grpc.WithContextDialer`) in `src/apps/switchboard-tui/internal/client/ssh.go` (FR-004, R1).
- [X] T048 [US3] Multi-daemon connection manager (N concurrent hosts, per-host connection state,
      reconnect + resync without losing/duplicating sandboxes) in
      `src/apps/switchboard-tui/internal/client/manager.go` (FR-005/021, SC-010).
- [X] T049 [US3] Host-grouped view, per-host connection-state indicators, and target-host selection at
      launch in `src/apps/switchboard-tui/internal/ui/hosts.go` (FR-012/020/021, SC-006).

**Checkpoint**: US1–US3 work independently — single interface spanning many hosts.

---

## Phase 6: User Story 4 - Prompt agents and get notified (Priority: P3)

**Goal**: Prompt a sandbox's coding agent from the TUI (or attach it in another terminal) and receive
in-TUI + OS desktop notifications on task-complete / needs-prompting, replayed if missed while
disconnected.

**Independent Test**: Prompt an agent, let a task finish and confirm a notification identifying that
sandbox within 5s; trigger an input-wait and confirm a needs-prompting notification (SC-008).

### Tests for User Story 4 ⚠️

- [X] T050 [P] [US4] Test hook injection + callback → `AgentStatus`/`NotificationEvent` transitions in
      `src/services/switchboardd/internal/agent/agent_test.go` (FR-024/025).
- [X] T051 [P] [US4] Test `Subscribe` replay of undelivered notifications on reconnect in
      `src/services/switchboardd/internal/grpc/subscribe_test.go` (FR-026b).
- [X] T052 [P] [US4] teatest test for the prompt pane + notification list/badges + navigate-to-sandbox in
      `src/apps/switchboard-tui/internal/ui/notify_test.go`.

### Implementation for User Story 4

- [X] T053 [P] [US4] Agent PTY management (`creack/pty`): `PromptAgent` writes a line; `AttachAgent` bidi
      raw-byte stream with resize, in `src/services/switchboardd/internal/agent/pty.go` (FR-022/023, R8).
- [X] T054 [P] [US4] Claude Code hook injection (`.claude/settings.local.json` with `Stop` +
      `Notification` matchers) and the daemon HTTP callback endpoint in
      `src/services/switchboardd/internal/agent/hooks.go` (R2, FR-024/025).
- [X] T055 [US4] `AgentSession` status transitions (idle/working/needs_input/exited) + `NotificationEvent`
      emit and daemon-side buffering, persisted in the registry, in
      `src/services/switchboardd/internal/agent/session.go` (FR-024/025/026b).
- [X] T056 [US4] `Subscribe` RPC (streams `sandbox_changed` + `notification`, replays undelivered) and
      `AckNotification` in `src/services/switchboardd/internal/grpc/subscribe.go` (FR-026b).
- [X] T057 [P] [US4] OS desktop notifications via `beeep` + in-TUI persistent notification list/badges in
      `src/apps/switchboard-tui/internal/notify/notify.go` (FR-026a, R7).
- [X] T058 [US4] TUI prompt pane + `AttachAgent` rendering + "open agent in another terminal"
      (`switchboard attach <host> <sandbox>`) in `src/apps/switchboard-tui/internal/ui/prompt.go`
      (FR-022/023).
- [X] T059 [US4] Notification → navigate-to-sandbox wiring and reconnect-replay surfacing in
      `src/apps/switchboard-tui/internal/ui/notifications.go` (FR-026/026b).

**Checkpoint**: US1–US4 work independently — fan-out stays manageable via attention routing.

---

## Phase 7: User Story 5 - Organize, navigate, and open in VSCode (Priority: P3)

**Goal**: Group sandboxes (cross-host), navigate quickly by keyboard, and open any sandbox (local or
remote) in VS Code.

**Independent Test**: Create a group, assign sandboxes, navigate between them, and open one in VS Code
confirming it opens that sandbox's files (SC-007/009).

### Tests for User Story 5 ⚠️

- [X] T060 [P] [US5] Test the groups TOML store (cross-host membership, ordering, stale-member pruning) in
      `src/apps/switchboard-tui/internal/store/groups_test.go` (FR-018).
- [X] T061 [P] [US5] Test the VS Code URI builder (local `attached-container+<hex>`; remote
      `DOCKER_HOST=ssh://…`) in `src/apps/switchboard-tui/internal/vscode/vscode_test.go` (R3).

### Implementation for User Story 5

- [X] T062 [P] [US5] Groups TOML store (`groups.toml`: cross-host `SandboxRef` members + order) in
      `src/apps/switchboard-tui/internal/store/groups.go` (FR-018).
- [X] T063 [P] [US5] `GetVSCodeTarget` RPC (container name, in-container workspace path, ssh_target) in
      `src/services/switchboardd/internal/grpc/vscode.go` (FR-027, R3).
- [X] T064 [P] [US5] VS Code URI builder + `code` launcher (`vscode-remote://attached-container+<hex>`;
      `DOCKER_HOST=ssh://<target>` for remote) in `src/apps/switchboard-tui/internal/vscode/vscode.go` (R3).
- [X] T065 [US5] TUI group management + keyboard navigation between sandboxes/groups in
      `src/apps/switchboard-tui/internal/ui/groups.go` (FR-019, SC-007).
- [X] T066 [US5] "Open in VS Code" action (local + remote) wired into the sandbox list in
      `src/apps/switchboard-tui/internal/ui/sandbox_list.go` (FR-027, SC-009).

**Checkpoint**: All five user stories independently functional.

---

## Phase 8: Polish & Cross-Cutting Concerns

**Purpose**: E2E coverage, gates, and docs spanning all stories.

- [X] T067 [P] PTY/`vhs` E2E harness for the TUI (US1 launch flow + US3 multi-host flow) in
      `src/apps/switchboard-tui-e2e/` (Rule VI Playwright substitute, R7).
- [X] T068 [P] Daemon E2E against real Docker (launch→stop→restart→destroy + re-adoption) in
      `src/services/switchboardd-e2e/` (SC-011/012).
- [X] T069 [P] Coverage gate asserting the 90% floor via `go test -coverprofile` + `go tool cover -func`
      in the `Makefile` `cover` target and CI (Rule VI threshold preserved).
- [X] T070 [P] Per-module `README.md` documenting the justified layout deviations (colocated `_test.go`,
      env loader) and the duplication defaults (symlink-as-is, preserve mode bits — research R5) in
      `src/libs/switchboard-proto/README.md`, `src/services/switchboardd/README.md`,
      `src/apps/switchboard-tui/README.md`.
- [X] T071 Run the `quickstart.md` story-by-story validation and the automated gates; confirm all green.
- [X] T072 [P] CI workflow (gofmt/golangci-lint/go vet/go test+cover/E2E) mirroring the Rule V gates in
      `.github/workflows/ci.yml`.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately.
- **Foundational (Phase 2)**: Depends on Setup — BLOCKS all user stories.
- **User Stories (Phases 3–7)**: All depend on Foundational. Recommended order is priority order
  (US1 → US2 → US3 → US4 → US5); once Foundational is done they may proceed in parallel if staffed.
- **Polish (Phase 8)**: Depends on the user stories it exercises being complete.

### User Story Dependencies

- **US1 (P1)**: Depends only on Foundational. No dependency on other stories. The MVP.
- **US2 (P2)**: Depends on Foundational; integrates with US1's launch flow (T042) but the manifest +
  config store are independently testable.
- **US3 (P2)**: Depends on Foundational; reuses US1 RPCs over a new (SSH) transport but is independently
  testable with two daemons.
- **US4 (P3)**: Depends on Foundational; needs a running sandbox (US1) to prompt, but the agent/hook/event
  machinery is independently testable.
- **US5 (P3)**: Depends on Foundational; needs sandboxes (US1) to group/open, independently testable.

### Within Each User Story

- Tests are written first and MUST fail before implementation.
- Daemon packages (duplicate/resources/sources/registry) before the RPCs that compose them.
- RPCs before the client/TUI code that calls them.
- Story complete before moving to the next priority.

### Parallel Opportunities

- All `[P]` Setup tasks (T003–T008, T010–T011) run together.
- Foundational `[P]` tasks (T013–T016 across distinct modules/files) run together after T012.
- Once Foundational completes, all five user stories can start in parallel (if staffed).
- Within a story, `[P]` tasks touch distinct files — e.g. US1 daemon packages T026/T027/T028 and the
  list view T032 run together; all per-story test tasks run together.

---

## Parallel Example: User Story 1

```bash
# Launch all US1 test tasks together (write first, confirm they fail):
Task T022: "gRPC contract/integration test in src/services/switchboardd/internal/grpc/sandbox_rpcs_test.go"
Task T023: "Verbatim-duplication unit test in src/services/switchboardd/internal/duplicate/duplicate_test.go"
Task T024: "Registry re-adoption test in src/services/switchboardd/internal/registry/readopt_test.go"
Task T025: "teatest golden test in src/apps/switchboard-tui/internal/ui/launch_test.go"

# Then launch the independent US1 daemon packages together:
Task T026: "Verbatim duplication package in src/services/switchboardd/internal/duplicate/duplicate.go"
Task T027: "Resource pre-check in src/services/switchboardd/internal/resources/resources.go"
Task T028: "Source-candidate enumeration in src/services/switchboardd/internal/sandbox/sources.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup.
2. Complete Phase 2: Foundational (CRITICAL — blocks all stories).
3. Complete Phase 3: User Story 1.
4. **STOP and VALIDATE**: run the US1 section of `quickstart.md` — duplicate, run, verify originals
   unchanged, lifecycle (SC-001/002/011/012).
5. Demo the MVP (single local host, default duplicate mode).

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. US1 → validate → demo (MVP).
3. US2 (saved configs) → validate → demo.
4. US3 (multi-host SSH) → validate → demo.
5. US4 (agents + notifications) and US5 (groups + VS Code) → validate → demo.

### Parallel Team Strategy

Once Foundational is done: Dev A on US1, Dev B on US2, Dev C on US3, then US4/US5 — each story is
independently completable and testable.

---

## Notes

- `[P]` = different files, no incomplete-task dependencies.
- `[Story]` label maps each task to a user story for traceability; Setup/Foundational/Polish carry none.
- Verify tests fail before implementing; keep each story independently demoable.
- Commit after each task or logical group.
- Carry the one residual unknown (R6: real `sbx` introspection entry point) into T038 and confirm
  against a real `sbx` before finalizing the manifest mechanism.
