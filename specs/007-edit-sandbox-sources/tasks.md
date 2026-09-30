# Tasks: Edit Sandbox Sources (Add & Remove Folders)

**Input**: Design documents from `/specs/007-edit-sandbox-sources/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md) (R1–R8),
[data-model.md](./data-model.md), [contracts/switchboard-edit-sources.proto](./contracts/switchboard-edit-sources.proto),
[quickstart.md](./quickstart.md)

**Tests**: Included — Rule VI's ≥90% per-module coverage floor is constitutional, and every prior
feature ships colocated tests with its implementation. Test tasks follow their implementation
tasks within each story (the repo convention), not TDD-first.

**Organization**: Tasks are grouped by user story so each story is an independently testable
increment. US1 (add) is the MVP; US2 (remove) completes the editing pair; US3/US4 are largely
verification of properties the US1/US2 mechanics must already provide.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1 (add, P1), US2 (remove, P1), US3 (record fidelity, P2), US4 (stopped/remote, P3)
- Every task names exact file paths

## Path Conventions

Go `go.work` monorepo (see plan.md Project Structure): proto in `src/libs/switchboard-proto/`,
daemon in `src/services/switchboardd/`, TUI in `src/apps/switchboard-tui/`, PTY-driven E2E in
`src/apps/switchboard-tui-e2e/`. Tests are colocated `_test.go` siblings (001's Rule IV deviation).

---

## Phase 1: Setup (Contract)

**Purpose**: The additive wire contract both sides build against.

- [X] T001 Add the feature-007 additions to `src/libs/switchboard-proto/proto/switchboard.proto` per `specs/007-edit-sandbox-sources/contracts/switchboard-edit-sources.proto`: `rpc AddSandboxSources(AddSandboxSourcesRequest) returns (stream LaunchProgress)` and `rpc RemoveSandboxSources(RemoveSandboxSourcesRequest) returns (Sandbox)` on `service Switchboard` (new `// --- Edit sandbox sources (feature 007…) ---` block after the port-forwarding RPCs), plus messages `AddSandboxSourcesRequest {sandbox_id, repeated SourceRef sources, override_resource_warning}` and `RemoveSandboxSourcesRequest {sandbox_id, repeated string source_paths}` — carrying over the contract file's comments (in-place/no-restart semantics, exact-path removal targets, FAILED_PRECONDITION on busy). **No new enum, no Event arm, no NotificationKind, no `Sandbox` field.**
- [X] T002 Regenerate Go bindings with `make proto` and commit `src/libs/switchboard-proto/gen/switchboard.pb.go` + `switchboard_grpc.pb.go` (depends on T001)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: The per-sandbox operation latch (research R4, FR-066) that BOTH stories' operations
and `Refresh` must acquire — the one piece neither story can ship without.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete.

- [X] T003 [P] Implement the per-sandbox try-acquire operation latch in `src/services/switchboardd/internal/sandbox/oplock.go`: mutex-guarded `map[sandboxID]label`, `acquire(id, label) error` returning a `FAILED_PRECONDITION`-mappable error naming the in-flight label ("<label> in progress for this sandbox"), `release(id)`; never blocks (research R4)
- [X] T004 [P] Unit tests in `src/services/switchboardd/internal/sandbox/oplock_test.go`: second acquire refused with the first's label in the message, release frees, N concurrent acquires for one sandbox yield exactly one winner, distinct sandboxes never contend
- [X] T005 Acquire the latch in `Manager.Refresh` in `src/services/switchboardd/internal/sandbox/manager.go` (label "refresh"; released on every return path) and assert in `src/services/switchboardd/internal/sandbox/refresh_test.go` that a refresh while the latch is held is refused without touching the workspace (depends on T003)

**Checkpoint**: Latch exists and `Refresh` participates — US1 and US2 can now proceed (in parallel if staffed).

---

## Phase 3: User Story 1 - Add a folder to an existing sandbox (Priority: P1) 🎯 MVP

**Goal**: From the sandbox list, add one or more folders to a live sandbox — staged copy →
atomic rename, progress streamed, record extended — with zero container work and zero
interruption to the agent (FR-053..058).

**Independent Test**: Launch a sandbox seeded with one repo, keep its terminal attached, add a
second repo via `S`→`a`; verify the folder appears inside the sandbox with no restart, the
original is byte-identical, the record shows both, and a mid-copy failure leaves everything
untouched (quickstart Scenarios 1 & 5).

### Implementation for User Story 1 — daemon

- [X] T006 [P] [US1] Implement add-side validation in `src/services/switchboardd/internal/sandbox/manager_sources.go` (new file): folder derivation (`basename(path)`), refusals per research R6's table — source missing/not-a-dir (daemon-side `os.Stat`), folder collision vs recorded sources ∪ on-disk top-level workspace entries ∪ the same batch, reserved `.switchboard`, clone-mode `is_repo` re-verified via the same `.git` probe `sources.go` uses, state eligibility RUNNING/STOPPED only — each refusal naming the offending folder and reason, and nothing copied on any refusal (FR-056)
- [X] T007 [US1] Implement `Manager.AddSources(ctx, id, sources, onProgress, onLog)` in `src/services/switchboardd/internal/sandbox/manager_sources.go`: acquire latch ("add sources") → validate (T006) → stage each source into `<workspace>/.switchboard/staging/<folder>` via `duplicate.CopyAll` (duplicate mode) or `m.runner.CloneRepo` (clone mode) with progress/log callbacks → when ALL staged, `os.Rename` each into `<workspace>/<folder>` then `RemoveAll` the staging dir → `store.Update` appending the `SourceRef`s + `updated_at` → `m.emit` — with full rollback on ANY failure (staging + already-renamed folders from this op deleted; record untouched; **sandbox state never changes**, FR-054/FR-058, research R2) (depends on T006, T003)
- [X] T008 [P] [US1] Implement startup staging purge: `Manager.PurgeStaging()` in `src/services/switchboardd/internal/sandbox/manager_sources.go` best-effort-removing `<workspace>/.switchboard/staging` for every registered sandbox, called once next to the existing `Readopt` call in `src/services/switchboardd/cmd/sxbd/main.go` (research R2 / risk 3)
- [X] T009 [US1] Implement the `AddSandboxSources` handler in `src/services/switchboardd/internal/grpc/sandbox_rpcs.go`: resource gate first — duplicate-mode only, `resources.Check(srcPaths, s.workspaceRoot)`, reply `LaunchProgress.blocked` when not OK and `override_resource_warning` unset, exactly like `LaunchSandbox` (FR-057) — then `progressSender(stream)` into `s.mgr.AddSources`, terminal `LaunchProgress.done` carrying `s.withTerminalCounts(sb)` (depends on T002, T007)

### Tests for User Story 1 — daemon

- [X] T010 [P] [US1] Unit tests in `src/services/switchboardd/internal/sandbox/manager_sources_test.go` (temp workspace root + fake `Runner`, no sbx): folder appears at the seeded path only after completion (staged under `.switchboard/staging` mid-copy — assert via a progress-callback probe of the workspace root); rollback on injected copy failure leaves record, workspace, and `Sandbox.state` untouched (SC-005); every T006 refusal fires before any byte lands; clone-mode add drives `CloneRepo` into staging and refuses non-repos; success appends `SourceRef`s and emits exactly one changed `Sandbox`; second concurrent add refused via latch (FR-066)
- [X] T011 [P] [US1] gRPC round-trip tests in `src/services/switchboardd/internal/grpc/sandbox_rpcs_test.go` over the in-process socket: `AddSandboxSources` streams copy progress then `done` with the extended `sources`; low-disk yields `blocked` without override and proceeds with it; unknown sandbox → NOT_FOUND; busy latch → FAILED_PRECONDITION naming the in-flight operation

### Implementation for User Story 1 — client

- [X] T012 [P] [US1] Add `AddSources(ctx, id string, sources []*pb.SourceRef, onUpdate func(client.LaunchUpdate)) (*pb.Sandbox, *pb.ResourceReport, error)` to `src/apps/switchboard-tui/internal/client/sandbox.go`, consuming the stream exactly as `Launch` does (copy/log updates via callback, `blocked` returned as the `ResourceReport`, `done` as the sandbox)
- [X] T013 [US1] Create the sources overlay in `src/apps/switchboard-tui/internal/ui/sources.go` (new file, modeled on `services.go` + the `launch.go` browser): `S` on a sandbox row opens an overlay listing the recorded folders (`basename (path)` + repo marker, `refresh.go`'s rendering); `a` enters the existing launch filesystem browser (reuse `startBrowse`/`fsEntry` machinery) rooted at the owning host's launch root, multi-select with `is_repo` capture; confirm dispatches `AddSources` with a 30-minute package-constant timeout; an in-flight add registers in a per-sandbox in-flight map so the sandbox row and the overlay badge "adding <folder> — NN%" off the shared spinner tick while the row **stays RUNNING/STOPPED** (FR-054/FR-055, research R7)
- [X] T014 [US1] Wire the surface: `Sources` binding (`S`, "sources") in `src/apps/switchboard-tui/internal/ui/keys.go`, `screenSources` routing in `src/apps/switchboard-tui/internal/ui/app.go`, `AddSources` added to the `ui.Daemon` interface in `app.go` (+ the fake daemon in `src/apps/switchboard-tui/internal/ui/model_test.go` grows the method) (depends on T012, T013)
- [X] T015 [US1] Implement the low-resource override round-trip in `src/apps/switchboard-tui/internal/ui/sources.go`: a `blocked` reply surfaces the warning with the existing launch-flow override prompt; accepting re-sends with `override_resource_warning=true`, declining cancels cleanly (FR-057) (depends on T013)

### Tests for User Story 1 — client

- [X] T016 [P] [US1] `teatest` UI tests in `src/apps/switchboard-tui/internal/ui/sources_test.go` against the fake daemon: `S` lists recorded folders; the add flow browses, selects, and lands the updated set in the overlay + row; the progress badge renders during a slow fake add while the rest of the TUI stays responsive; `blocked` → override prompt → override proceeds

**Checkpoint**: US1 fully functional — adds work end-to-end on a live sandbox. MVP deliverable.

---

## Phase 4: User Story 2 - Remove a seeded folder from a sandbox (Priority: P1)

**Goal**: Remove seeded folders behind an explicit destructive confirmation — copy deleted,
record updated, sandbox untouched, originals never affected (FR-059..063).

**Independent Test**: On a sandbox seeded with two repos, remove one via `S`→`space`→`d`→confirm;
verify the copy is gone, the survivor and the original are intact, the last-source and
running-service guards refuse correctly (quickstart Scenario 2).

### Implementation for User Story 2 — daemon

- [X] T017 [US2] Implement `Manager.RemoveSources(ctx, id, paths []string)` in `src/services/switchboardd/internal/sandbox/manager_sources.go`: acquire latch ("remove sources") → validate ALL targets as exact recorded `SourceRef.path` values (unknown → NOT_FOUND naming it, nothing deleted), refuse when the batch empties the record (FR-061) or state is ineligible → per folder in request order: `within(workspaceRoot, …)` re-check → `os.RemoveAll(<workspace>/<folder>)`, treating an already-missing folder as success (FR-063) → on mid-batch failure STOP, keep already-deleted folders dropped (record follows disk, research R3), error naming the failed folder → `store.Update` dropping exactly the deleted/already-gone `SourceRef`s → `m.emit`; **no state change ever** (depends on T003, and T006's shared helpers)
- [X] T018 [US2] Implement the `RemoveSandboxSources` handler in `src/services/switchboardd/internal/grpc/sandbox_rpcs.go` including the FR-062 service guard BEFORE calling the manager: walk `s.supervisor.Instances().ActiveBySandbox(id)`, join each active instance to its declaration in `Sandbox.services` by name, refuse with FAILED_PRECONDITION naming the service and the remedy ("stop it first") when its workspace-relative `working_dir` equals a selected folder or sits beneath it — a root/empty `working_dir` does NOT block (research R4); on success return the updated `Sandbox` via `s.withTerminalCounts` (depends on T002, T017)

### Tests for User Story 2 — daemon

- [X] T019 [P] [US2] Unit tests in `src/services/switchboardd/internal/sandbox/manager_sources_test.go`: removal deletes the copy + drops the ref + emits; already-missing copy still cleans the record and succeeds; unknown path → nothing deleted; last-source refusal before any deletion; mid-batch failure (unwritable folder) keeps deleted folders dropped, failed folder recorded, error names it; origin directories never touched (byte-compare)
- [X] T020 [P] [US2] gRPC guard tests in `src/services/switchboardd/internal/grpc/sandbox_rpcs_test.go`: an active (STARTING/RUNNING) instance with `working_dir` inside a selected folder refuses naming the service; `working_dir` at workspace root does not block; after the instance stops, the same removal proceeds

### Implementation for User Story 2 — client

- [X] T021 [P] [US2] Add `RemoveSources(ctx, id string, paths []string) (*pb.Sandbox, error)` to `src/apps/switchboard-tui/internal/client/sandbox.go` (unary, like `Stop`)
- [X] T022 [US2] Implement the removal flow in `src/apps/switchboard-tui/internal/ui/sources.go`: `space` toggles removal marks on listed folders, `d` opens a `confirm.go` `confirmState` naming the sandbox, EVERY marked folder (`basename (path)`), and the uncommitted-work consequence — cancel default, unrecognised keys ignored, key distinct from every non-destructive action on the overlay (FR-060) — confirm dispatches `RemoveSources` with a 10-minute package-constant timeout; add `RemoveSources` to the `ui.Daemon` interface + fake daemon (depends on T021)

### Tests for User Story 2 — client

- [X] T023 [P] [US2] `teatest` tests in `src/apps/switchboard-tui/internal/ui/sources_test.go`: the confirm names sandbox + folders + consequence; `esc` and unrecognised keys never confirm (SC-006); confirmed removal updates overlay + row; a daemon-side guard refusal (service running / last source) surfaces its message verbatim

**Checkpoint**: US1 + US2 — the seeded set is fully editable, both directions guarded.

---

## Phase 5: User Story 3 - Edited records stay truthful downstream (Priority: P2)

**Goal**: Prove (and where needed, make) every downstream reader follow the edited record:
display, refresh, relaunch, escape-hatch scoping (FR-064).

**Independent Test**: After one add + one remove: list row matches workspace contents; `F`
re-seeds exactly the edited set; stop/start comes back with it (quickstart Scenario 3).

- [X] T024 [P] [US3] Integration test in `src/services/switchboardd/internal/sandbox/refresh_test.go`: seed {A}, add B, remove A, then `Refresh` → workspace ends with exactly {B} freshly copied (added folder re-seeded, removed folder not resurrected), and the refresh confirm's source list (via `Sandbox.sources`) showed exactly {B}
- [X] T025 [P] [US3] Integration test in `src/services/switchboardd/internal/sandbox/manager_sources_test.go`: after edits, `bringUp`'s relaunch path (fake `Runner` argv assertion) receives the edited `Sources` in its `LaunchSpec`, and a stop→start cycle preserves the edited set in the record
- [X] T026 [P] [US3] Escape-hatch scoping test in `src/services/switchboardd/internal/escapehatch/match_test.go`: a `workspaces` glob that matches an added folder resolves it once the folder exists, and stops offering a removed folder — no stale grants, no code change expected (spec US3-4; document in the test comment that this is a regression guard for FR-064)
- [X] T027 [US3] `teatest` assertion in `src/apps/switchboard-tui/internal/ui/sources_test.go` (or `model_test.go`): an `Event.sandbox_changed` carrying edited `sources` re-renders the sandbox row's seeded-folder display and an open sources overlay without any manual reload

**Checkpoint**: Record-as-truth verified across every downstream reader.

---

## Phase 6: User Story 4 - Stopped sandboxes and remote hosts (Priority: P3)

**Goal**: The same flows on stopped sandboxes (retained copy edited in place) and on any
connected host's sandboxes (browse THAT host's filesystem), with edits broadcast to all
connected clients (FR-065, FR-067).

**Independent Test**: Edit a stopped sandbox's folders and start it; edit a remote host's
sandbox from the TUI; watch a second client update without reloading (quickstart Scenario 4).

- [X] T028 [P] [US4] State-eligibility tests in `src/services/switchboardd/internal/sandbox/manager_sources_test.go`: add and remove succeed on a STOPPED sandbox (retained copy edited on disk immediately); CREATING, DESTROYING, and ERROR are refused with clear messages (data-model eligibility matrix)
- [X] T029 [US4] Owning-host targeting in `src/apps/switchboard-tui/internal/ui/sources.go`: the overlay's browser and both RPC dispatches use `daemonForHost(sb.HostId)` and that host's launch root — never the active host — with a `teatest` two-fake-host test proving a remote sandbox's add browses and lands remotely (FR-065)
- [X] T030 [P] [US4] Broadcast test in `src/services/switchboardd/internal/grpc/serve_test.go` (or `sandbox_rpcs_test.go`): a second `Subscribe` stream receives `Event.sandbox_changed` with the edited `sources` after each of add and remove (FR-067)

**Checkpoint**: All four stories independently verified.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T031 [P] Extend the PTY-driven TUI E2E in `src/apps/switchboard-tui-e2e/` (stub `sbx`, runs anywhere) with the full arc: launch seeded {A} → `S` add B → verify record + workspace → `F` refresh re-seeds {A,B} → `S` remove A behind the confirm → verify {B} remains — asserting the sandbox was never stopped by an edit (FR-054)
- [X] T032 [P] Documentation: add `S` to the key table in `README.md` (sandbox list keys) and note the sources overlay under "Using the TUI"; mention the `.switchboard/staging` mechanism in `src/services/switchboardd/README.md`'s duplication-semantics section
- [X] T033 Run the [quickstart.md](./quickstart.md) validation scenarios end-to-end with the stub harness (Scenarios 1–5), and record Scenario 0 (live mount propagation, research R1) as **pending real-`sbx` reconciliation** in the quickstart — the documented fallback (option B inside the same RPCs) stays untriggered until then
- [X] T034 Full gates before merge: `make all` (fmt-check + vet + lint + test), `make cover` (≥90% per module incl. the new files), `make e2e`, `make env-check` (surface unchanged — the feature adds no env vars)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: T001 → T002. Nothing else can compile against the new RPCs until T002.
- **Foundational (Phase 2)**: T003 → {T004, T005}. Blocks all story phases (both operations and
  `Refresh` share the latch).
- **US1 (Phase 3)**: needs T002 + T003. Internally: T006 → T007 → T009; T008 after T007;
  T012/T013 → T014 → T015; tests follow their implementation tasks.
- **US2 (Phase 4)**: needs T002 + T003 + T006 (shared validation helpers) — **not** the rest of
  US1; a remove-only build is testable with launch-seeded sandboxes. Internally: T017 → T018;
  T021 → T022.
- **US3 (Phase 5)**: needs US1 + US2 mechanics to exercise (it is fidelity verification).
- **US4 (Phase 6)**: T028/T030 need US1+US2 daemon mechanics; T029 needs T013/T022.
- **Polish (Phase 7)**: after all desired stories; T031 needs the full UI surface.

### User Story Dependencies

- **US1 (P1)**: independent once Foundational is done — the MVP.
- **US2 (P1)**: shares only T006's validation helpers with US1; otherwise independent and
  independently testable.
- **US3 (P2)**: consumes US1/US2 behavior; adds no new surface.
- **US4 (P3)**: consumes US1/US2 surfaces; T029 touches only the overlay file.

### Parallel Opportunities

- T003/T004 in parallel after T002 lands (different files); T001 can even proceed while T003 is
  written (latch has no proto dependency).
- Within US1: T006, T008, T012 are [P] against each other's files; T010/T011/T016 all [P] once
  their targets exist.
- **US1 and US2 daemon work can proceed in parallel** after T006 (different functions in
  `manager_sources.go` — coordinate on one file, or land T006+file skeleton first).
- All of Phase 5 (T024–T026) is [P]; Phase 6's T028/T030 are [P].

## Parallel Example: User Story 1

```bash
# After T002 + T003 + T006 land, in parallel:
Task: "T007 Manager.AddSources in src/services/switchboardd/internal/sandbox/manager_sources.go"
Task: "T008 staging purge (manager_sources.go + cmd/sxbd/main.go)"
Task: "T012 client.AddSources in src/apps/switchboard-tui/internal/client/sandbox.go"
# Then, once T007/T009 + T013 exist, in parallel:
Task: "T010 manager add-path unit tests"
Task: "T011 gRPC add round-trip tests"
Task: "T016 teatest sources-overlay tests"
```

## Implementation Strategy

**MVP first (US1 only)**: T001–T016 delivers the headline capability — add a repo to a live
sandbox with no restart — independently shippable and demoable (quickstart Scenario 1). Stop and
validate there.

**Incremental delivery**: +US2 (T017–T023) completes editing; +US3 (T024–T027) locks the
record-as-truth property with regression tests; +US4 (T028–T030) closes the stopped/remote/
broadcast corners; Polish (T031–T034) rides the E2E arc, docs, and the merge gates. Each
checkpoint leaves `main`-mergeable, independently tested behavior.

**Reconciliation note**: quickstart **Scenario 0** (R1 live-mount propagation) is the one item
that requires a real `sbx` and gates nothing in this task list — but it MUST run before the
feature is trusted on a new runtime; the fallback (option B inside the same RPCs) is a
daemon-only change touching T007/T017 call sites.

## Notes

- [P] = different files, no dependency on an incomplete task. `manager_sources.go` is shared by
  T006/T007/T008/T017 — those are sequenced, not [P], except where noted.
- Every task maps to spec FRs via research/data-model references given inline; the invariants
  that must never regress are data-model I1 (record == disk), I2 (≥1 source), and I7 (edits
  never drive sandbox state).
- Commit after each task or logical group; every checkpoint is a valid stopping point.
