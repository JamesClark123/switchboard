# Tasks: Jobs Screen & Orchestrator UI Reorganisation

**Input**: Design documents from `/specs/010-jobs-screen/` (plan.md, spec.md, research.md, data-model.md, contracts/jobs-ui.md, quickstart.md)

**Tests**: Included. The constitution (Rule VI) requires automated tests and ≥90% coverage per module, so each story carries its TUI tests (Go `fakeDaemon` / `teatest` patterns already used in `internal/ui`).

**Organization**: Tasks are grouped by user story (spec.md US1–US5) so each story can be implemented and tested on its own.

All paths are relative to the repository root; `UI/` = `src/apps/switchboard-tui/internal/ui/`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US5 from spec.md

---

## Phase 1: Setup

**Purpose**: Constants and scaffolding shared by the stories.

- [ ] T001 Add Jobs package constants `jobsOverviewHistory = 15` and `jobsQueuePageSize = 25` (research R7) with doc comments in `UI/jobs.go`
- [ ] T002 [P] Add the `orchTarget` type (host, hostName, id, name, rec) and its `label()` (`name @ hostName`) and `key()` helpers (data-model.md) in `UI/run_actions.go`

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: Refactors every story depends on — operations on an explicit target (R1), the Jobs screen shell and data load (R2, R3), and the navigation change (R8).

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete.

- [ ] T003 Refactor run operations — open prompt, cancel, mark worker free — to take an `orchTarget` instead of `m.orchDetail`; keep the existing Queue view calling them; move them into `UI/run_actions.go` (from `UI/orchestrator_forms.go`)
- [ ] T004 Refactor Enqueue now (`startOrchFire` and its reply handling, model-level in-flight record, repeat window, dedupe-off confirm) so the target and the status sink are parameters, keeping today's Events view behaviour, in `UI/orchestrator_forms.go` and `UI/orchestrators.go`
- [ ] T005 Refactor the action form (add/edit), action delete, call-action form, event form (add/edit), event delete and enable/disable to act on an `orchTarget` in `UI/orchestrator_forms.go`
- [ ] T006 [P] Update the existing tests for the refactored operations (Queue/Actions/Events/Enqueue now behaviour unchanged) in `UI/orchestrator_runs_test.go` and `UI/orchestrator_views_test.go`
- [ ] T007 Add `screenJobs` and wire the top-level cycle **Sandboxes → Daemons → Jobs** (remove `screenOrchestrators` from the cycle, header strip, `topLevel()`, `screenNextLabel`/`screenPrevLabel`) in `UI/app.go`
- [ ] T008 Add `jobsState` (tab, orchs, unreachable, loading, cursors, status, queueFilter, queuePage), the tab bar (reusing `activeTabStyle`/`inactiveTabStyle` like `tabBar()`), `←`/`→` tab switching, `r` reload and the empty/partial states in `UI/jobs.go`
- [ ] T009 Implement the Jobs fan-out load `jobsLoadCmd` (per connected host: `ListOrchestrators`, then `ListRuns` per orchestrator, concurrently per host; unreachable hosts collected) and the merge into `jobsState.orchs` ordered by host then orchestrator name, in `UI/jobs.go`
- [ ] T010 Hook `Event.orchestrator_changed` and host connect/disconnect into a single-orchestrator / single-host Jobs reload (R3) in `UI/notifications.go` and `UI/jobs.go`
- [ ] T011 [P] Tests for the Jobs shell: top-level cycle has no Orchestrators screen, tab order Overview/Actions/Events/Queue with Overview first, fan-out across two fake hosts, unreachable host named, event-driven single-orchestrator reload, in `UI/jobs_test.go`

**Checkpoint**: Jobs screen opens with its tabs and loaded data; existing per-orchestrator behaviour unchanged.

---

## Phase 3: User Story 1 — See all orchestrator work in one place (Priority: P1) 🎯 MVP

**Goal**: Jobs › Overview shows Upcoming, Queued, In progress and History (latest 15) across all orchestrators, each row labelled `name @ host`.

**Independent Test**: Two fake hosts, orchestrators with runs in every state and enabled/disabled timed events → every run/event appears once in the right section, labelled, history capped at 15, disabled events absent, live update on an `orchestrator_changed` event.

- [ ] T012 [P] [US1] Overview row derivation helpers — `upcoming` (enabled events with `next_fire_at`, soonest first), `queued` (by `received_at`), `inProgress` (by `started_at`), `history` (by `ended_at` desc, first `jobsOverviewHistory`) — in `UI/jobs_overview.go`
- [ ] T013 [P] [US1] Unit tests for the derivation helpers (ordering, 15 cap across orchestrators, disabled events excluded, events without `next_fire_at` excluded) in `UI/jobs_overview_test.go`
- [ ] T014 [US1] Render the Overview sections with section headings, row formats from contracts/jobs-ui.md and a cursor that walks all rows, in `UI/jobs_overview.go`
- [ ] T015 [US1] TUI test: Overview across two hosts (labels, sections, history cap, partial availability line, update after a run changes state) in `UI/jobs_overview_test.go`

**Checkpoint**: MVP — the cross-orchestrator overview is usable read-only.

---

## Phase 4: User Story 2 — Act on runs and upcoming events from the Overview (Priority: P1)

**Goal**: From the Overview: open prompt, cancel, mark free (today's Queue actions), Enqueue now on Upcoming, Open results on finished runs.

**Independent Test**: Cancel a queued run, open a prompt, mark an awaiting-review worker free, Enqueue now an upcoming event, and read the full long report of a completed run — all from the Overview, each acting on the right host/orchestrator.

- [ ] T016 [US2] Overview key handling per row kind (`enter` prompt, `x` cancel queued, `m` mark free when awaiting review, `n` Enqueue now on Upcoming, `o` Open results on finished) via the target-based operations, with outcomes on the Jobs status line, in `UI/jobs_overview.go`
- [ ] T017 [P] [US2] Open results overlay: scrollable `bubbles/viewport` with action, `name @ host`, state, end time, then the full `Run.summary` / "closed by developer: …" / failure or cancel `reason` / "no result was reported"; sanitised terminal text; `↑`/`↓`/`pgup`/`pgdown` scroll, `esc` closes, in `UI/run_results.go`
- [ ] T018 [P] [US2] Tests for the Open results overlay (long report fully present and scrollable, closed-by-developer note, failed reason, empty report message, control sequences stripped) in `UI/run_results_test.go`
- [ ] T019 [US2] TUI tests: each Overview action reaches the right fake host/orchestrator (`CancelRun`, `MarkWorkerFree` dialog, `FireTimedEvent` with SWI-4 rules incl. dedupe-off confirm and repeat window), `o` not offered on in-progress runs, in `UI/jobs_overview_test.go`
- [ ] T020 [US2] Key hints for the Overview (row-sensitive) in `UI/jobs.go`

**Checkpoint**: US1 + US2 deliver the full Overview.

---

## Phase 5: User Story 3 — Browse every run with a filter (Priority: P2)

**Goal**: Jobs › Queue lists every retained run, uncapped, paginated, filterable by orchestrator, with the Overview's per-run actions.

**Independent Test**: More than 25 runs across two orchestrators → page through all, filter to one orchestrator, clear it; per-run actions work.

- [ ] T021 [P] [US3] Queue derivation: flatten queued ++ in progress ++ all history, apply `queueFilter`, paginate by `jobsQueuePageSize`, clamp `queuePage`, reset a vanished filter with a status note, in `UI/jobs_queue.go`
- [ ] T022 [P] [US3] Unit tests for flatten/filter/paginate/clamp/vanished-filter in `UI/jobs_queue_test.go`
- [ ] T023 [US3] Render the Queue tab (header `filter: … · page N of M (K runs)`, rows as in Overview) and keys `f` cycle filter, `F` clear, `]`/`pgdown` next, `[`/`pgup` previous, plus `enter`/`x`/`m`/`o` per row, in `UI/jobs_queue.go`
- [ ] T024 [US3] TUI test: paging through >25 runs, filter cycle and clear, per-run action from the Queue tab, in `UI/jobs_queue_test.go`

**Checkpoint**: Queue tab complete.

---

## Phase 6: User Story 4 — Manage actions and events for all orchestrators (Priority: P2)

**Goal**: Jobs › Actions and Jobs › Events list every orchestrator's actions / timed events, grouped by orchestrator, with today's operations acting on the right orchestrator.

**Independent Test**: Add an action to the second orchestrator from its group and call it; add and toggle an event on the first; empty groups still accept "add".

- [ ] T025 [US4] Grouped list rendering and cursor model (group headings `name @ host`, empty-group line, cursor positions on headings/empty lines resolve to that group's target) shared by both tabs in `UI/jobs_groups.go`
- [ ] T026 [US4] Actions tab keys `a` add (cursor's group), `e` edit, `d` delete (confirmed), `enter` call — via the target-based forms — in `UI/jobs_groups.go`
- [ ] T027 [US4] Events tab keys `a` add (cursor's group), `e` edit, `d` delete (confirmed), `space` enable/disable, `n` Enqueue now — via the target-based forms — in `UI/jobs_groups.go`
- [ ] T028 [P] [US4] TUI tests: grouping/order across two hosts, add/call/edit/delete on the intended orchestrator, event toggle and Enqueue now from the Events tab, empty group add, in `UI/jobs_groups_test.go`

**Checkpoint**: All four Jobs tabs complete.

---

## Phase 7: User Story 5 — Orchestrator administration under Daemons (Priority: P2)

**Goal**: Daemons gains an Orchestrators option (create/rename/remove/open per daemon); the detail has Workers, Reasoning, Hosts; the old top-level screen and its Actions/Queue/Events views are removed; entry points retargeted.

**Independent Test**: From Daemons › a daemon › Orchestrators create and open an orchestrator, attach a worker, rebuild reasoning, add a host; shift+←/→ never shows an Orchestrators screen.

- [ ] T029 [US5] Add "Orchestrators" to `daemonOptions` and route it to a new `screenDaemonOrchs` for the selected daemon in `UI/daemons.go` and `UI/app.go`
- [ ] T030 [US5] Daemons › Orchestrators list for one daemon (reusing the orchestrator list loader filtered to that host): `n` create, `R` rename, `d` remove (confirmed), `enter` open, `esc` back to Daemons, in `UI/daemon_orchestrators.go`
- [ ] T031 [US5] Reduce the orchestrator detail to Workers / Reasoning / Hosts (drop the Actions/Queue/Events views and their hints), `esc` back to the daemon's Orchestrators list, in `UI/orchestrator_views.go`
- [ ] T032 [US5] Remove the old Orchestrators screen code paths (list rendering/keys/help for `screenOrchestrators`) in `UI/orchestrators.go` and `UI/app.go`, keeping the shared loader
- [ ] T033 [US5] Retarget entry points: sandbox-list mark-free dialog and launch-wizard reasoning flow return to the detail under Daemons › Orchestrators; notifications that pointed at runs open Jobs › Overview, in `UI/orchestrator_forms.go`, `UI/launch.go`, `UI/orchestrator_attach.go` and `UI/notifications.go`
- [ ] T034 [P] [US5] TUI tests: Daemons options include Orchestrators; create/rename/remove/open from it; detail views are exactly Workers/Reasoning/Hosts; mark-free from the sandbox list and the reasoning flow land correctly; no `screenOrchestrators` reachable, in `UI/daemon_orchestrators_test.go`
- [ ] T035 [US5] Remove or migrate obsolete tests of the old Orchestrators screen and detail Actions/Queue/Events views to their Jobs equivalents in `UI/orchestrator_views_test.go`, `UI/orchestrator_runs_test.go` and `UI/orchestrators_test.go`

**Checkpoint**: Reorganisation complete; no capability lost (FR-027).

---

## Phase 8: Polish & Cross-Cutting Concerns

- [ ] T036 [P] Update README: replace the Orchestrators screen section with the Jobs screen (tabs, keys) and Daemons › Orchestrators, in `README.md`
- [ ] T037 [P] Capability inventory check against today's Orchestrators screen (every operation reachable in the new layout, FR-027 / SC-005) recorded in `specs/010-jobs-screen/quickstart.md`
- [ ] T038 Run `make fmt-check vet test env-check cover` (≥90% per module) and fix any regressions
- [ ] T039 Walk the manual scenarios in `specs/010-jobs-screen/quickstart.md` against a local daemon (and a remote one when available) and note results in the implement PR

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)** → **Foundational (Phase 2)** → user stories → **Polish (Phase 8)**.
- Foundational blocks every story: T003–T005 (target-based operations) and T007–T010 (Jobs shell, load, live updates).

### User story dependencies

- **US1 (P1)**: after Phase 2. MVP.
- **US2 (P1)**: after US1 (adds actions to the Overview rows).
- **US3 (P2)**: after Phase 2; reuses US2's per-row action handling (T016) — do after US2, or extract the row-action dispatcher first.
- **US4 (P2)**: after Phase 2; independent of US1–US3.
- **US5 (P2)**: after Phase 2; T032/T035 (removing the old screen) should land after US1–US4 so no capability is lost in between.

### Within each story

- Derivation helpers and their unit tests first ([P]), then rendering/keys, then TUI tests.

### Parallel opportunities

- T002 with T001; T006 and T011 once their code exists.
- US1: T012 ∥ T013. US2: T017 ∥ T018. US3: T021 ∥ T022.
- US4 can proceed in parallel with US1–US3 after Phase 2 (different files: `jobs_groups.go`).
- Polish T036 ∥ T037.

## Parallel Example: User Story 1

```text
Task: "T012 [P] [US1] Overview row derivation helpers in UI/jobs_overview.go"
Task: "T013 [P] [US1] Unit tests for the derivation helpers in UI/jobs_overview_test.go"
```

## Implementation Strategy

### MVP first (US1)

1. Phase 1 + Phase 2 (refactor + Jobs shell).
2. US1 (read-only cross-orchestrator Overview). **Stop and validate** with quickstart scenarios 1–2.

### Incremental delivery

1. US2 → full Overview (actions, Enqueue now, Open results).
2. US3 → Queue tab. US4 → Actions/Events tabs (can run alongside).
3. US5 → move administration under Daemons and remove the old screen last.
4. Polish: README, capability inventory, gates, manual walk-through.

## Notes

- No proto, daemon, storage or env changes (plan Constraints); if any task appears to need one, stop and raise it.
- Keep behaviour identical to today for every moved operation (FR-027): the refactor in Phase 2 is behaviour-preserving and guarded by the existing tests.
