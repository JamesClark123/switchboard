---

description: "Task list for Escape Hatch implementation"
---

# Tasks: Escape Hatch

**Input**: Design documents from `/specs/005-escape-hatch/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/](./contracts/), [quickstart.md](./quickstart.md)

**Tests**: Test tasks ARE included. Rule VI (Multi-Level Testing Discipline) is NON-NEGOTIABLE and the repo enforces ≥90% per-module coverage via `make test`/`make cover`; `quickstart.md` enumerates the required assertions. Go unit tests are colocated as `*_test.go` (Rule IV).

**Organization**: Tasks are grouped by user story so each can be implemented, tested, and delivered independently.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (US1–US5)
- Exact file paths are included in every task

## Path Conventions

Go monorepo (`go.work`), paths relative to repo root:

- Daemon: `src/services/switchboardd/`
- TUI client: `src/apps/switchboard-tui/`
- Contract: `src/libs/switchboard-proto/`

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the additive contract revision and create the new daemon package skeleton.

- [X] T001 [P] Add escape-hatch enums and messages (`ConsentMode`, `EscapeHatchRunStatus`, `EscapeHatchCommand`, `EscapeHatchRun`, `DecideEscapeHatchRunRequest/Response`, `ListEscapeHatchRunsRequest/Response`) to `src/libs/switchboard-proto/proto/switchboard.proto` per `specs/005-escape-hatch/contracts/switchboard-escape-hatch.proto`
- [X] T002 Add additive fields to existing messages in `src/libs/switchboard-proto/proto/switchboard.proto`: `KitSpec.escape_hatch = 3`, `Sandbox.escape_hatch_commands = 19`, `Event.escape_hatch_run = 4`, and the two new `NotificationKind` values (`…NEEDS_APPROVAL = 3`, `…RUN_COMPLETE = 4`) plus the two new `rpc` entries on `service Switchboard` (depends on T001, same file)
- [X] T003 Regenerate Go bindings with `make proto` and commit `src/libs/switchboard-proto/gen/switchboard.pb.go` + `switchboard_grpc.pb.go` (depends on T002)
- [X] T004 [P] Create the new daemon package skeleton at `src/services/switchboardd/internal/escapehatch/doc.go` with a package doc citing research decisions R1–R7 and the security invariant (name-only allowlist, never a caller-supplied command string)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Resolution, persistence, run storage, and event plumbing that every execution story depends on.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T005 [P] Implement resolution in `src/services/switchboardd/internal/escapehatch/resolve.go`: merge attached client-authored kits' `escape_hatch` lists in attach order with **later-kit-wins** on name collision (clarification Q1); reject entries with `CONSENT_MODE_UNSPECIFIED` or empty name/command
- [X] T006 [P] Unit tests in `src/services/switchboardd/internal/escapehatch/resolve_test.go`: later-kit override of a same-named command, attach-order preservation, rejection of unspecified consent mode and blank fields
- [X] T007 Persist the resolved set in `src/services/switchboardd/internal/sandbox/manager.go`: populate `Sandbox.EscapeHatchCommands` during `Launch` and `AddKit`, and replay it on container recreate (depends on T003, T005)
- [X] T008 [P] Unit test in `src/services/switchboardd/internal/sandbox/manager_escapehatch_test.go`: `Sandbox.escape_hatch_commands` round-trips through the bbolt registry and pre-feature records decode with an empty set (no migration)
- [X] T009 [P] Implement the in-memory, session-scoped run store in `src/services/switchboardd/internal/escapehatch/runs.go`: `ehr-<seq>` ids, mutex-guarded map + ring trim (mirroring `agent.Hub`'s bounded buffer), `Create/Get/Update/ListBySandbox`
- [X] T010 [P] Unit tests in `src/services/switchboardd/internal/escapehatch/runs_test.go`: id sequencing, ring trim at capacity, list filtering by sandbox, concurrent access
- [X] T011 Implement the service scaffolding in `src/services/switchboardd/internal/escapehatch/service.go`: dependency struct (sandbox lookup, run store, hub emitter, agent-prompt func, clock) plus helpers that emit `Event.escape_hatch_run` and the new `NotificationKind`s on every run-state change (depends on T009)
- [X] T012 [P] Unit tests in `src/services/switchboardd/internal/escapehatch/service_test.go`: each run-state transition emits exactly one event and the correct notification kind

**Checkpoint**: Resolution, persistence, run storage, and event emission ready — user stories can begin.

---

## Phase 3: User Story 1 - Author escape-hatch commands on a kit (Priority: P1) 🎯 MVP

**Goal**: A developer can add, edit, and remove escape-hatch commands on a kit in the TUI editor; they persist with the kit and never pollute the Docker `spec.yaml`.

**Independent Test**: Open the kit editor, add one command with every field, save, reopen — all fields persist and the kit still validates; abandoning a partial edit leaves the stored kit unchanged.

### Implementation for User Story 1

- [X] T013 [P] [US1] Add `KitEscapeHatchCommand` struct and the `Kit.EscapeHatch []KitEscapeHatchCommand` field to `src/apps/switchboard-tui/internal/store/kit.go`, explicitly excluded from `SpecYAML()` rendering (data-model §5)
- [X] T014 [US1] Implement sidecar persistence in `src/apps/switchboard-tui/internal/store/kit.go`: `Save`/`Get` write and read `kits/<id>/escape-hatch.yaml` alongside `spec.yaml`, and `Delete` removes it (depends on T013)
- [X] T015 [US1] Populate `pb.KitSpec.EscapeHatch` from `Kit.EscapeHatch` in `Kit.ToSpec()` in `src/apps/switchboard-tui/internal/store/kit.go` (depends on T003, T013)
- [X] T016 [P] [US1] Implement client-side validation in `src/apps/switchboard-tui/internal/store/kit_validate.go`: non-empty name/command/when-to-use, kebab-case name, name unique within the kit, relative non-escaping `working_dir`, non-negative max duration (data-model §6)
- [X] T017 [US1] Add the `secEscapeHatch` itemized section to `src/apps/switchboard-tui/internal/ui/kit_editor.go`: enum constant before `secCount`, plus `title()`, `blurb()`, `itemized()`, and `kitSectionCount` cases
- [X] T018 [US1] Add the escape-hatch item form and its `applyKitForm` case to `src/apps/switchboard-tui/internal/ui/kit_editor.go`: huh fields for name, command, when-to-use, a `huh.NewConfirm` for requires-approval, working dir, and max duration — read back through bound pointers, never `Form.GetString` (depends on T017)
- [X] T019 [P] [US1] Unit tests in `src/apps/switchboard-tui/internal/ui/kit_editor_escapehatch_test.go`: add / edit / remove an item via the itemized section, and an abandoned edit leaves the stored kit and its sidecar untouched
- [X] T020 [P] [US1] Unit tests in `src/apps/switchboard-tui/internal/store/kit_escapehatch_test.go`: sidecar round-trip, escape-hatch absent from rendered `spec.yaml`, `ToSpec()` carries the commands, and every validation rule

**Checkpoint**: Kits can declare escape-hatch commands end-to-end in the client, reviewable and shareable on their own.

---

## Phase 4: User Story 2 - AI runs an auto-run command and continues (Priority: P1) 🎯 MVP

**Goal**: The agent invokes an auto-run command; the exact predefined string runs on the daemon's host in the sandbox's workspace; the outcome and output are delivered back to the agent even with no terminal attached.

**Independent Test**: With a kit declaring one auto-run command attached, have the agent invoke it — confirm the exact command ran on the host in the sandbox's workspace, its effects are visible inside the sandbox, and the agent received exit status + output including after every terminal detaches.

### Implementation for User Story 2

- [X] T021 [P] [US2] Implement the host executor in `src/services/switchboardd/internal/escapehatch/executor.go`: `exec.CommandContext(ctx, "/bin/sh", "-c", command)` with `cmd.Dir` = `workspace_path` joined with the optional workspace-relative `working_dir`, streaming via `StdoutPipe`/`StderrPipe` + `Start` + scanner goroutine + `Wait`. Re-validate `working_dir` daemon-side (defense-in-depth per analysis S1): `filepath.Clean` the joined path and refuse to run if it escapes `workspace_path`, independent of the client-side check in T016
- [X] T022 [US2] Add bounded output capture to `src/services/switchboardd/internal/escapehatch/executor.go`: `maxCapturedOutput = 1 MiB` constant, head-retained truncation, and an `output_truncated` flag (depends on T021)
- [X] T023 [US2] Add duration bounding to `src/services/switchboardd/internal/escapehatch/executor.go`: `context.WithTimeout` using the command's `max_duration_seconds` or the `defaultMaxDuration = 30 * time.Minute` constant, killing the process group on expiry and reporting `TIMED_OUT` (depends on T021)
- [X] T024 [US2] Add the cancellation registry to `src/services/switchboardd/internal/escapehatch/executor.go`: mutex-guarded `map[sandboxID][]context.CancelFunc` plus a `Cancel(sandboxID)` that terminates in-flight runs as `CANCELLED` with no orphaned host process (depends on T021)
- [X] T025 [US2] Invoke `Cancel(sandboxID)` from the non-RUNNING teardown hook in `src/services/switchboardd/internal/grpc/server.go`, alongside the existing PTY and terminal-broadcaster teardown (depends on T024)
- [X] T026 [US2] Implement the invocation endpoint in `src/services/switchboardd/internal/escapehatch/http.go`: `POST /escape-hatch/run` accepting `{sandbox_id, name}` **only**, resolving the name against the sandbox's persisted `escape_hatch_commands`, returning `404` for an unknown name/sandbox with zero execution, `400` for bad method/payload, and an immediate `{run_id, status}` response (contracts/escape-hatch-http.md; depends on T009, T021)
- [X] T027 [US2] Implement the agent callback in `src/services/switchboardd/internal/escapehatch/service.go`: on every terminal outcome, invoke the agent-prompt func (`agent.Registry.Prompt`) with a message carrying the command name, status, exit status, and bounded output (depends on T011, T021)
- [X] T028 [US2] Wire the service in `src/services/switchboardd/cmd/sxbd/main.go`: construct the escapehatch service with the sandbox lookup, `agent.Registry.Prompt` (the `agents` registry already built at `main.go:171`), the hub, and the callback base URL derived from `cfg.HookAddr`, then register `/escape-hatch/run` on the existing hook `mux` next to `/hook` (depends on T026, T027)
- [X] T029 [US2] Implement wrapper injection in `src/services/switchboardd/internal/escapehatch/inject.go`: write `<workspace>/.switchboard/escape-hatch` mode `0755` embedding this sandbox's id and the callback URL, sending only the command name; remove it when the resolved set is empty
- [X] T030 [US2] Call the injector from bring-up in `src/services/switchboardd/internal/sandbox/manager.go` — in `Launch`, `Refresh`, and `AddKit`, alongside the existing hook and workspace-marker injection (depends on T029)

### Tests for User Story 2

- [X] T031 [P] [US2] Executor tests in `src/services/switchboardd/internal/escapehatch/executor_test.go`: runs in the workspace dir, honours a relative `working_dir`, captures stdout+stderr, reports exit codes, and reports a launch failure as `FAILED` with diagnostics
- [X] T032 [P] [US2] Bounds tests in `src/services/switchboardd/internal/escapehatch/executor_bounds_test.go`: >1 MiB output truncated with the flag set, an over-running command hits `TIMED_OUT` with the process killed, `Cancel` yields `CANCELLED` with no orphan, two concurrent runs keep their captured output un-interleaved via per-run buffers (spec edge case), and a `working_dir` that escapes the workspace is refused (S1)
- [X] T033 [P] [US2] Endpoint tests in `src/services/switchboardd/internal/escapehatch/http_test.go`: unknown name ⇒ 404 and zero execution, a payload attempting to supply a command string cannot influence what runs (SC-004), bad method/JSON ⇒ 400, and the response is immediate rather than held for the run's duration
- [X] T034 [P] [US2] Callback test in `src/services/switchboardd/internal/escapehatch/callback_test.go`: each terminal outcome invokes the prompt func with outcome + output, and delivery happens with no terminal attached (SC-005)
- [X] T035 [P] [US2] Injection test in `src/services/switchboardd/internal/escapehatch/inject_test.go`: wrapper written at `0755`, embeds the sandbox id and callback URL, passes only the name, and is removed when the resolved set is empty

**Checkpoint**: The headline capability works — an out-of-sandbox command runs to completion and its result reaches the agent unattended. **US1 + US2 together are the MVP.**

---

## Phase 5: User Story 3 - The AI knows when and how to use the commands (Priority: P2)

**Goal**: A sandbox with escape-hatch commands attached gains a context rule enumerating them, so the agent reaches for the escape hatch instead of trying the equivalent inside the sandbox.

**Independent Test**: Attach a kit with escape-hatch commands; inspect the agent's context and confirm it lists exactly those commands with when-to-use guidance and the runs-on-host statement; a sandbox with none has no rule and nothing invokable.

### Implementation for User Story 3

- [X] T036 [US3] Implement rule rendering and injection in `src/services/switchboardd/internal/escapehatch/inject.go`: render a `<!-- switchboard:escape-hatch:begin -->`/`:end` delimited block enumerating the resolved commands (name, when-to-use, consent mode, exact wrapper invocation, runs-on-host and asynchronous-result notes) into `<workspace>/CLAUDE.md`, creating the file when absent, replacing the block in place when present, and removing the block when the resolved set is empty (research R3; depends on T029)

### Tests for User Story 3

- [X] T037 [P] [US3] Tests in `src/services/switchboardd/internal/escapehatch/inject_rule_test.go`: the block lists exactly the resolved commands with when-to-use and the on-host statement, re-injection is idempotent, pre-existing user `CLAUDE.md` content is preserved, and an empty resolved set removes the block entirely

**Checkpoint**: The agent is told what exists, when to use it, and that it runs outside the sandbox.

---

## Phase 6: User Story 4 - Approval-gated commands (Priority: P2)

**Goal**: A `requires-approval` command pauses before any host execution until the supervising developer explicitly approves; denial or silence means nothing runs.

**Independent Test**: Attach a kit whose command requires approval; have the agent invoke it — confirm no host execution before approval, that approving runs it and returns the result, and that denying (or not responding within the window) results in no execution and a "declined" result to the agent.

### Implementation for User Story 4

- [X] T038 [US4] Implement the consent gate in `src/services/switchboardd/internal/escapehatch/consent.go`: create the run as `PENDING_APPROVAL`, block on a per-run decision channel with the `approvalWindow = 5 * time.Minute` constant, and resolve to `DENIED` on explicit denial, window elapse, or any non-approval input (deny-by-default; clarification Q4)
- [X] T039 [US4] Branch on consent mode in `src/services/switchboardd/internal/escapehatch/http.go`: `REQUIRES_APPROVAL` creates a pending run and emits the `NEEDS_APPROVAL` notification without executing, while `AUTO_RUN` dispatches immediately (depends on T026, T038)
- [X] T040 [US4] Implement the `DecideEscapeHatchRun` RPC in `src/services/switchboardd/internal/grpc/escapehatch_rpcs.go`: resolve the run's decision channel, idempotent no-op on an already-resolved run, returning the resolved status (depends on T038)
- [X] T041 [P] [US4] Add the `DecideEscapeHatchRun` client method to `src/apps/switchboard-tui/internal/client/sandbox.go` (depends on T003)
- [X] T042 [US4] Implement the approval modal in `src/apps/switchboard-tui/internal/ui/approval.go` following the `confirm.go` pattern: naming the sandbox, the exact command, and that it runs on the host; `y`/`enter` approve, `n`/`esc` deny, every other key ignored (never consent)
- [X] T043 [US4] Route the approval screen in `src/apps/switchboard-tui/internal/ui/app.go` (add the `screenApproval` constant, the `Daemon` interface method, the `handleKey` case, and the `View` overlay) and trigger it from a `PENDING_APPROVAL` run event in `src/apps/switchboard-tui/internal/ui/notifications.go` (depends on T041, T042)

### Tests for User Story 4

- [X] T044 [P] [US4] Daemon tests in `src/services/switchboardd/internal/escapehatch/consent_test.go`: zero host execution before approval, approval proceeds to execution, explicit denial ⇒ `DENIED`, elapsed window ⇒ `DENIED`, and a duplicate or late decision is a no-op (SC-003)
- [X] T045 [P] [US4] Client tests in `src/apps/switchboard-tui/internal/ui/approval_test.go`: an unrecognized key is not consent, cancel/deny is the default, and the decision command dispatches with the correct run id and host routing

**Checkpoint**: High-risk commands are human-gated without sacrificing the autonomy of auto-run ones.

---

## Phase 7: User Story 5 - Observe and audit escape-hatch runs (Priority: P3)

**Goal**: A supervising developer can see which command is running on which sandbox and review each run's outcome within the session.

**Independent Test**: Trigger a run and confirm it appears while in progress, and that afterwards its command, sandbox, status, exit code, and captured output are retrievable in the current session.

### Implementation for User Story 5

- [X] T046 [US5] Implement the `ListEscapeHatchRuns` RPC in `src/services/switchboardd/internal/grpc/escapehatch_rpcs.go`, returning the session's runs for a sandbox (empty `sandbox_id` ⇒ all sandboxes on this host) (depends on T009)
- [X] T047 [P] [US5] Add the `ListEscapeHatchRuns` client method to `src/apps/switchboard-tui/internal/client/sandbox.go`
- [X] T048 [US5] Handle the `Event_EscapeHatchRun` arm in `src/apps/switchboard-tui/internal/ui/notifications.go` and add titles/icons for the two new kinds to `notifTitle`/`notifIcon`
- [X] T049 [US5] Add an in-progress / awaiting-approval badge to the sandbox row in `src/apps/switchboard-tui/internal/ui/sandbox_list.go` (alongside `agentBadge` in `sandboxTitle`), kept live by the run events from T048 (depends on T048)
- [X] T050 [US5] Add the run-review screen in `src/apps/switchboard-tui/internal/ui/runs.go` listing command, sandbox, status, exit status, and captured output, plus its key binding in `src/apps/switchboard-tui/internal/ui/keys.go` and routing in `src/apps/switchboard-tui/internal/ui/app.go` (depends on T047)

### Tests for User Story 5

- [X] T051 [P] [US5] Client tests in `src/apps/switchboard-tui/internal/ui/runs_test.go`: the row badge reflects running and awaiting-approval states, and the review screen shows command, sandbox, status, exit status, and (truncation-marked) output
- [X] T052 [P] [US5] Daemon test in `src/services/switchboardd/internal/grpc/escapehatch_rpcs_test.go`: `ListEscapeHatchRuns` returns the session's runs, filters by sandbox, and returns all when `sandbox_id` is empty

**Checkpoint**: All five user stories are independently functional.

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T053 [P] Confirm no new env vars were introduced: run `make env-check` and verify `src/services/switchboardd/internal/config/config.go` schema and `.env.example` are unchanged (research R7)
- [X] T054 [P] Document the escape-hatch surface in `README.md`: authoring on a kit, the host-execution semantics, consent modes, and the new key bindings
- [~] T055 Run the full verification gate from the repo root `Makefile`. **`fmt-check`, `vet`, `test`, `env-check` PASS; feature-005 code is lint-clean.** `cover`: proto 96.3% and switchboardd 91.5% pass the 90% floor; the `switchboard-tui` module is 89.6% — a **pre-existing** shortfall (89.3% on clean `main`, i.e. already below the floor before this feature; feature 005 *raised* it). `lint`: feature-005 code adds **zero** issues, but the module gate stays red on **pre-existing** feature-003 test-file `errcheck` debt (`terminal/broadcaster_test.go`, `termview/termview_test.go`, `client/attach_test.go`). Both pre-existing gate failures are out of scope for this feature and left untouched.
- [X] T056 Verify the regenerated contract is committed and the tree is clean after `make proto` (`src/libs/switchboard-proto/gen/`) — `make proto` is idempotent (second run: 0 diff).
- [~] T057 Execute the manual walkthrough in `specs/005-escape-hatch/quickstart.md` covering US1–US5. **Blocked in dev: `sbx` + Docker are not installed** (the residual constraint recorded in 001–004; host-`sbx` argv stays stub-asserted). Every quickstart assertion is covered by the automated unit/integration tests instead (resolve/allowlist/exec/timeout/bounds/cancel/approval/callback/injection/kit-editor/proto).
- [X] T058 [P] Security pass over `src/services/switchboardd/internal/escapehatch/`: confirm the endpoint is name-only with no code path that runs a caller-supplied string, the allowlist is read from the persisted `Sandbox.escape_hatch_commands`, and unknown names / unapproved runs are deny-by-default (SC-003, SC-004)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — start immediately. T003 gates everything that touches generated proto types.
- **Foundational (Phase 2)**: Depends on Setup — **BLOCKS all user stories**.
- **User Stories (Phases 3–7)**: All depend on Foundational. US1 is client-only and US2–US5 are mostly daemon-side, so US1 can proceed fully in parallel with US2.
- **Polish (Phase 8)**: Depends on all desired stories being complete.

### User Story Dependencies

- **US1 (P1)** — independent. Client-only (store + kit editor). No dependency on any other story.
- **US2 (P1)** — independent of US1 at the code level (it consumes the resolved set built in Foundational). End-to-end manual validation is easiest once US1 can author a kit, but US2 is testable with a fixture kit.
- **US3 (P2)** — extends `inject.go` from US2 (T029); sequence T036 after T029.
- **US4 (P2)** — extends the endpoint from US2 (T026) with the approval branch; sequence T039 after T026.
- **US5 (P3)** — consumes the run store and event emission from Foundational; independent of US3/US4, though the awaiting-approval badge is only exercisable once US4 lands.

### Within Each User Story

- Models/types before services, services before endpoints, core before integration.
- Tests are colocated and may be written alongside or before implementation; all must pass before the story's checkpoint.

### Parallel Opportunities

- **Setup**: T001 and T004 in parallel; T002→T003 are serial (same file, then codegen).
- **Foundational**: T005, T006, T009, T010 in parallel; T008, T012 in parallel after their subjects.
- **US1**: T013 and T016 in parallel; T019 and T020 in parallel.
- **US2**: T021 opens the file, then T022/T023/T024 are serial on `executor.go`; the five test tasks T031–T035 all run in parallel (distinct files).
- **US4**: T041 parallel with T038/T040; T044 and T045 in parallel.
- **US5**: T047 parallel with T046; T051 and T052 in parallel.
- **Cross-story**: US1 (client) and US2 (daemon) can be built simultaneously by two developers with no file overlap.

---

## Parallel Example: User Story 2

```bash
# After the executor + endpoint + injector land, launch all US2 tests together:
Task: "Executor tests in internal/escapehatch/executor_test.go"
Task: "Bounds tests in internal/escapehatch/executor_bounds_test.go"
Task: "Endpoint tests in internal/escapehatch/http_test.go"
Task: "Callback test in internal/escapehatch/callback_test.go"
Task: "Injection test in internal/escapehatch/inject_test.go"
```

## Parallel Example: Foundational

```bash
Task: "Implement resolution in internal/escapehatch/resolve.go"
Task: "Resolution tests in internal/escapehatch/resolve_test.go"
Task: "Implement run store in internal/escapehatch/runs.go"
Task: "Run store tests in internal/escapehatch/runs_test.go"
```

---

## Implementation Strategy

### MVP (User Stories 1 + 2)

1. Phase 1 Setup → Phase 2 Foundational (blocking).
2. Phase 3 (US1) and Phase 4 (US2) — buildable in parallel; no shared files.
3. **STOP and VALIDATE**: author a kit with one auto-run command, attach it, have the agent invoke it, confirm the host command ran in the workspace and the result reached the agent with every terminal detached.
4. This is a shippable increment: the escape hatch works for auto-run commands.

Note: US1 alone is a valid but thin increment (a reviewable declaration with nothing to execute it). US1 + US2 is the first increment that delivers the feature's actual value, and both are P1.

### Incremental Delivery

1. Setup + Foundational → foundation ready.
2. + US1 + US2 → **MVP**: auto-run escape hatch end-to-end.
3. + US3 → the agent reliably chooses the escape hatch instead of failing inside the sandbox.
4. + US4 → high-risk commands become safely usable (human-gated).
5. + US5 → supervision and audit of everything that crossed the boundary.

### Parallel Team Strategy

1. Everyone lands Setup + Foundational together (T001–T012).
2. Then split: Developer A on US1 (client store + kit editor), Developer B on US2 (daemon executor/endpoint/injection).
3. Developer B continues into US3 and US4 (both extend US2's files); Developer A picks up US5's client surfaces once Foundational's event emission is in.

---

## Notes

- `[P]` = different files, no dependency on an incomplete task.
- `[Story]` labels map tasks to spec user stories for traceability; Setup, Foundational, and Polish carry no story label.
- The security invariant is load-bearing and appears in T026, T033, and T058: the endpoint accepts a **name**, never a command string, and resolves it against the persisted allowlist.
- Commit after each task or logical group; stop at any checkpoint to validate a story independently.
- `sbx` is not installed in the dev environment — host-CLI argv stays stub-asserted (feature 001 R6); this feature adds no new `sbx` subcommand surface.
