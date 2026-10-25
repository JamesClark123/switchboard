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

- [ ] T001 Add Jobs package constants `jobsOverviewHistory = 15`, `jobsQueuePageSize = 25` and `jobsRunsParallel = 4` (research R7) with doc comments in `UI/jobs.go`
- [ ] T002 [P] Add the `orchTarget` type (host, hostName, id, name, rec) and its `label()` (`name @ hostName`) and `key()` helpers, the `orchSurface` type (data-model.md), and `connectedDaemon(host) (Daemon, error)` — errors `<host> is not connected` when the host isn't `HostConnected`, never falls back to the active daemon (R12, FR-030) — in `UI/run_actions.go`
- [ ] T040 [P] Add `sanitizeTerminalText` (strip CSI/OSC/DCS/SOS/PM/APC and other ESC sequences, C0 except `\n`/`\t`, DEL, C1, invalid UTF-8 → U+FFFD) and `sanitizeLine` (also folds `\n`/`\t`; truncates by display width via `ansi.Truncate`, never splitting a rune or sequence) in `UI/sanitize.go`, with table tests (OSC 52, `ESC[2J`, `\r`, 8-bit CSI, truncated sequence at the end, multi-byte runes at the cut) in `UI/sanitize_test.go` (R10, FR-029, SC-008)

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: Refactors every story depends on — operations on an explicit target (R1), the Jobs screen shell and data load (R2, R3), and the navigation change (R8).

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete.

- [ ] T003 Refactor run operations — open prompt, cancel, mark worker free — to take an `orchTarget` and an `orchSurface` instead of `m.orchDetail`, resolving the daemon with `connectedDaemon`; keep the existing Queue view calling them; render the prompt overlay through `sanitizeTerminalText` (prompt and arguments); move them into `UI/run_actions.go` (from `UI/orchestrator_forms.go`)
- [ ] T004 Refactor Enqueue now (`startOrchFire` and its reply handling, model-level in-flight record, repeat window, dedupe-off confirm) so the target and the surface are parameters: the per-view guard (`firing`, `fireToken`, `lastFireAt`, `lastFireEv`) moves from `orchDetail` to `orchSurface`, and `applyOrchDone` decides "current" against the originating surface's token; a failure arriving while that surface has a form open goes to the footer, named (R9). Keep today's Events view behaviour, in `UI/orchestrator_forms.go` and `UI/orchestrators.go`
- [ ] T005 Refactor the action form (add/edit), action delete, call-action form, event form (add/edit), event delete and enable/disable to act on an `orchTarget` and be hosted by an `orchSurface` (daemon re-checked with `connectedDaemon` on submit) in `UI/orchestrator_forms.go`
- [ ] T041 Route hosting by originating surface (R1): `orchDoneMsg`, `orchFormErrMsg` and `runPromptMsg` carry the originating screen; `applyOrchDone` (status, errors, `closeForm`, `reload`), `applyOrchFormErr`, `applyRunPrompt` and `forwardOrchestrators` act on that surface; `enterConfirm` returns to the originating screen; `View()`'s confirm background and `topLevel()` handle `screenJobs` and `screenDaemonOrchs`; `markFreeFromList` keeps returning to the sandbox list, in `UI/orchestrators.go`, `UI/app.go` and `UI/run_actions.go`
- [ ] T006 [P] Update the existing tests for the refactored operations (Queue/Actions/Events/Enqueue now behaviour unchanged) in `UI/orchestrator_runs_test.go` and `UI/orchestrator_views_test.go`; add surface-routing tests (a confirm, a refusal, a prompt and a fire reply started on one surface land on it while another screen is showing; an operation on a disconnected host is refused and no RPC reaches the active daemon)
- [ ] T007 Add `screenJobs` and wire the top-level cycle **Sandboxes → Daemons → Jobs**, wrapping at both ends (remove `screenOrchestrators` from the cycle, header strip, `topLevel()`; `screenNextLabel`/`screenPrevLabel` name the wrapped neighbour); on Jobs `esc` → Sandboxes, `q`/`ctrl+c` quit, `,` settings (R8) in `UI/app.go`
- [ ] T008 Add `jobsState` (tab, orchs, per-host load state, derived unreachable line incl. orchestrators whose runs failed, data revision, identity-keyed cursors, surface, queueFilter, queuePage), the tab bar (reusing `activeTabStyle`/`inactiveTabStyle` like `tabBar()`), `←`/`→` tab switching, `r` reload and the empty/partial states in `UI/jobs.go`
- [ ] T009 Implement the Jobs fan-out load: one `tea.Cmd` per connected host (hosts in parallel, each with its own `ctxTimeout()` — not today's sequential `orchLoadCmd`), `ListOrchestrators` then `ListRuns` per orchestrator at most `jobsRunsParallel` at a time; each host's reply is applied as it arrives (others stay navigable, "loading: <host>" shown); a per-host in-flight guard and generation; merge into `jobsState.orchs` ordered by host then orchestrator name (R2), in `UI/jobs.go`
- [ ] T010 Hook `Event.orchestrator_changed` into a single-orchestrator reload (`GetOrchestrator` + `ListRuns`) with a per-orchestrator generation (older replies dropped) and coalescing (one in flight + `dirty`), only while Jobs is visible — otherwise mark `stale` and reload on entering Jobs; host connect/reconnect (including snapshots before the first load completes) loads that host; disconnect keeps its orchestrators as last known, `reachable = false` (R3), in `UI/notifications.go`, `UI/hosts.go` and `UI/jobs.go`
- [ ] T042 Identity-keyed cursors (R11): a shared `jobsCursor{index, key}` with `reanchor(rows)` called after every re-derive (load, event reload, filter, page), moving to the row with the same key (Queue: to its page) and clamping only when it is gone; derived rows cached per data revision, in `UI/jobs.go`
- [ ] T011 [P] Tests for the Jobs shell: top-level cycle wraps and has no Orchestrators screen, `esc`/`q` on Jobs, tab order Overview/Actions/Events/Queue with Overview first, fan-out across two fake hosts (a slow host does not block the other's rows), unreachable host named, host connecting after Jobs opened (and during the first load) appears, a disconnected host's rows stay dimmed and refuse operations, event-driven single-orchestrator reload, an out-of-order reply is dropped, a burst of events coalesces to ≤2 reloads, no reloads while Jobs isn't visible (stale reloaded on entry), cursor stays on the same item after a reorder, in `UI/jobs_test.go`

**Checkpoint**: Jobs screen opens with its tabs and loaded data; existing per-orchestrator behaviour unchanged.

---

## Phase 3: User Story 1 — See all orchestrator work in one place (Priority: P1) 🎯 MVP

**Goal**: Jobs › Overview shows Upcoming, Queued, In progress and History (latest 15) across all orchestrators, each row labelled `name @ host`.

**Independent Test**: Two fake hosts, orchestrators with runs in every state and enabled/disabled timed events → every run/event appears once in the right section, labelled, history capped at 15, disabled events absent, live update on an `orchestrator_changed` event.

- [ ] T012 [P] [US1] Overview row derivation helpers — `upcoming` (enabled events with `next_fire_at`, soonest first, `willSkip` when dedupe is on and a run of the event is open), `queued` (by `received_at`), `inProgress` (by `started_at`, worker display name), `history` (by `ended_at` desc, first `jobsOverviewHistory`) — in `UI/jobs_overview.go`
- [ ] T013 [P] [US1] Unit tests for the derivation helpers (ordering, 15 cap across orchestrators, disabled events excluded, events without `next_fire_at` excluded, `willSkip` true/false, worker name fallback) in `UI/jobs_overview_test.go`
- [ ] T014 [US1] Render the Overview sections with section headings, row formats from contracts/jobs-ui.md (fixed display-width columns, truncated; snippets/reasons/origin labels through `sanitizeLine`; "next firing skipped — run open"), and an identity-keyed cursor that walks all rows, in `UI/jobs_overview.go`
- [ ] T015 [US1] TUI test: Overview across two hosts (labels, sections, history cap, partial availability line incl. an orchestrator whose runs failed, update after a run changes state, a History row whose summary contains OSC 52 / `ESC[2J` / `\r` renders without them, long names truncated) in `UI/jobs_overview_test.go`

**Checkpoint**: MVP — the cross-orchestrator overview is usable read-only.

---

## Phase 4: User Story 2 — Act on runs and upcoming events from the Overview (Priority: P1)

**Goal**: From the Overview: open prompt, cancel, mark free (today's Queue actions), Enqueue now on Upcoming, Open results on finished runs.

**Independent Test**: Cancel a queued run, open a prompt, mark an awaiting-review worker free, Enqueue now an upcoming event, and read the full long report of a completed run — all from the Overview, each acting on the right host/orchestrator.

- [ ] T016 [US2] Overview key handling per row kind (`enter` prompt, `x` cancel queued, `m` mark free when awaiting review, `n` Enqueue now on Upcoming, `o` Open results on finished) via the target-based operations hosted by the Jobs surface, refused on unreachable rows (FR-030), with outcomes on the Jobs status line, in `UI/jobs_overview.go`
- [ ] T017 [P] [US2] Open results overlay: scrollable `bubbles/viewport` with action, `name @ host`, state, end time, then the full `Run.summary` / "closed by developer: …" / failure or cancel `reason` / "no result was reported"; all text through `sanitizeTerminalText`; viewport sized to the modal's inner size minus `modalStyle` padding/border, resized on `tea.WindowSizeMsg`, never taller than the terminal (scroll indicator and hint always visible); `↑`/`↓`/`pgup`/`pgdown` scroll, `esc` closes (R5), in `UI/run_results.go`
- [ ] T018 [P] [US2] Tests for the Open results overlay (a 2000-char report at 100×40 fits the terminal with the hint visible and every word present across scroll positions, no double-wrapped orphan lines, resize re-fits, closed-by-developer note, failed reason, empty report message, control sequences stripped) in `UI/run_results_test.go`
- [ ] T019 [US2] TUI tests: each Overview action reaches the right fake host/orchestrator (`CancelRun`, `MarkWorkerFree` dialog, `FireTimedEvent` with SWI-4 rules incl. dedupe-off confirm and repeat window), `o` not offered on in-progress runs, `n` after an Upcoming reorder fires the event the cursor was on, an action on a disconnected host is refused with no RPC to another daemon, in `UI/jobs_overview_test.go`
- [ ] T020 [US2] Key hints (FR-028) for the Overview (row-sensitive) and the Open results overlay in `UI/jobs.go`; Queue, Actions, Events and Daemons › Orchestrators hints are part of T023, T026, T027 and T030

**Checkpoint**: US1 + US2 deliver the full Overview.

---

## Phase 5: User Story 3 — Browse every run with a filter (Priority: P2)

**Goal**: Jobs › Queue lists every retained run, uncapped, paginated, filterable by orchestrator, with the Overview's per-run actions.

**Independent Test**: More than 25 runs across two orchestrators → page through all, filter to one orchestrator, clear it; per-run actions work.

- [ ] T021 [P] [US3] Queue derivation: flatten queued ++ in progress ++ all history, apply `queueFilter`, paginate by `jobsQueuePageSize`, clamp `queuePage`, reset a vanished filter with a status note, in `UI/jobs_queue.go`
- [ ] T022 [P] [US3] Unit tests for flatten/filter/paginate/clamp/vanished-filter in `UI/jobs_queue_test.go`
- [ ] T023 [US3] Render the Queue tab (header `filter: … · page N of M (K runs)`, rows as in Overview) and keys `f` cycle filter, `F` clear, `]`/`pgdown` next, `[`/`pgup` previous, plus `enter`/`x`/`m`/`o` per row; row-sensitive key hints, in `UI/jobs_queue.go`
- [ ] T024 [US3] TUI test: paging through >25 runs, filter cycle and clear, per-run action from the Queue tab, the selected run stays selected (moving page if needed) when a new run finishes, the filter survives its host disconnecting but resets when the orchestrator is removed, in `UI/jobs_queue_test.go`

**Checkpoint**: Queue tab complete.

---

## Phase 6: User Story 4 — Manage actions and events for all orchestrators (Priority: P2)

**Goal**: Jobs › Actions and Jobs › Events list every orchestrator's actions / timed events, grouped by orchestrator, with today's operations acting on the right orchestrator.

**Independent Test**: Add an action to the second orchestrator from its group and call it; add and toggle an event on the first; empty groups still accept "add".

- [ ] T025 [US4] Grouped list rendering and cursor model (group headings `name @ host`, empty-group line, cursor positions on headings/empty lines resolve to that group's target) shared by both tabs in `UI/jobs_groups.go`
- [ ] T026 [US4] Actions tab keys `a` add (cursor's group), `e` edit, `d` delete (confirmed), `enter` call — via the target-based forms — plus key hints, in `UI/jobs_groups.go`
- [ ] T027 [US4] Events tab keys `a` add (cursor's group), `e` edit, `d` delete (confirmed), `space` enable/disable, `n` Enqueue now — via the target-based forms — plus key hints, in `UI/jobs_groups.go`
- [ ] T028 [P] [US4] TUI tests: grouping/order across two hosts, add/call/edit/delete on the intended orchestrator, event toggle and Enqueue now from the Events tab, empty group add, `space` after another orchestrator gains an event still toggles the highlighted event, groups keep their order across reloads, in `UI/jobs_groups_test.go`

**Checkpoint**: All four Jobs tabs complete.

---

## Phase 7: User Story 5 — Orchestrator administration under Daemons (Priority: P2)

**Goal**: Daemons gains an Orchestrators option (create/rename/remove/open per daemon); the detail has Workers, Reasoning, Hosts; the old top-level screen and its Actions/Queue/Events views are removed; entry points retargeted.

**Independent Test**: From Daemons › a daemon › Orchestrators create and open an orchestrator, attach a worker, rebuild reasoning, add a host; shift+←/→ never shows an Orchestrators screen.

- [ ] T029 [US5] Add "Orchestrators" to `daemonOptions` (offered only for a connected daemon) and route it to a new `screenDaemonOrchs` for the selected daemon in `UI/daemons.go` and `UI/app.go`
- [ ] T030 [US5] Daemons › Orchestrators list for one daemon (reusing the orchestrator list loader filtered to that host): `n` create (machine fixed to this daemon — no `orchMachineOptions` picker), `R` rename, `d` remove (confirmed), `enter` open, `r` reload, `shift+←/→` and `,` as on the old screen, `esc` back to Daemons, `q` quits; key hints; every operation via `connectedDaemon` (a form submitted after a disconnect is refused, stays open); on disconnect of that daemon return to Daemons with a note (as `applyHosts` does for MCP), in `UI/daemon_orchestrators.go` and `UI/hosts.go`
- [ ] T031 [US5] Reduce the orchestrator detail to Workers / Reasoning / Hosts (drop the Actions/Queue/Events views and their hints), `esc` back to where the detail was opened from (the daemon's Orchestrators list, or the sandbox list for the launch-wizard reasoning flow and `markFreeFromList`), in `UI/orchestrator_views.go`
- [ ] T032 [US5] Remove the old Orchestrators screen code paths (list rendering/keys/help for `screenOrchestrators`) in `UI/orchestrators.go` and `UI/app.go`, keeping the shared loader
- [ ] T033 [US5] Retarget entry points: sandbox-list mark-free dialog and launch-wizard reasoning flow return to the detail under Daemons › Orchestrators; notifications that pointed at runs open Jobs › Overview, in `UI/orchestrator_forms.go`, `UI/launch.go`, `UI/orchestrator_attach.go` and `UI/notifications.go`
- [ ] T034 [P] [US5] TUI tests: Daemons options include Orchestrators; create/rename/remove/open from it; detail views are exactly Workers/Reasoning/Hosts; mark-free from the sandbox list and the reasoning flow land correctly (and `esc` returns there); no `screenOrchestrators` reachable; create after the daemon disconnected is refused and nothing is created on the active daemon; the screen leaves to Daemons on disconnect; create offers no machine picker, in `UI/daemon_orchestrators_test.go`
- [ ] T035 [US5] Remove or migrate obsolete tests of the old Orchestrators screen and detail Actions/Queue/Events views to their Jobs equivalents in `UI/orchestrator_views_test.go`, `UI/orchestrator_runs_test.go` and `UI/orchestrators_test.go`

**Checkpoint**: Reorganisation complete; no capability lost (FR-027).

---

## Phase 8: Polish & Cross-Cutting Concerns

- [ ] T043 [P] Route every remaining agent/remote-supplied string the orchestrator detail renders (Hosts reachability errors, the reasoning error, worker run action labels) through `sanitizeLine`, and make `runLine`/`truncate` use it so no path keeps byte-based truncation of untrusted text (FR-029 applied beyond Jobs for consistency), in `UI/orchestrator_views.go`, `UI/orchestrator_reasoning.go` and `UI/kit_editor.go`
- [ ] T036 [P] Update README: replace the Orchestrators screen section with the Jobs screen (tabs, keys) and Daemons › Orchestrators, in `README.md`
- [ ] T037 [P] Capability inventory check against today's Orchestrators screen (every operation reachable in the new layout, FR-027 / SC-005) recorded in `specs/010-jobs-screen/quickstart.md`
- [ ] T038 Run `make fmt-check vet test env-check cover` (≥90% per module) and fix any regressions
- [ ] T039 Walk the manual scenarios in `specs/010-jobs-screen/quickstart.md` against a local daemon (and a remote one when available) and note results in the implement PR

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)** → **Foundational (Phase 2)** → user stories → **Polish (Phase 8)**.
- Setup: T040 (sanitiser) is needed by T003, T014, T017 and T043.
- Foundational blocks every story: T003–T005 and T041 (target-based operations hosted per surface) and T007–T010, T042 (Jobs shell, load, live updates, identity cursors).

### User story dependencies

- **US1 (P1)**: after Phase 2. MVP.
- **US2 (P1)**: after US1 (adds actions to the Overview rows).
- **US3 (P2)**: after Phase 2; reuses US2's per-row action handling (T016) — do after US2, or extract the row-action dispatcher first.
- **US4 (P2)**: after Phase 2; independent of US1–US3.
- **US5 (P2)**: after Phase 2; T032/T035 (removing the old screen) should land after US1–US4 so no capability is lost in between.

### Within each story

- Derivation helpers and their unit tests first ([P]), then rendering/keys, then TUI tests.

### Parallel opportunities

- T002 and T040 with T001; T006 and T011 once their code exists.
- US1: T012 ∥ T013. US2: T017 ∥ T018. US3: T021 ∥ T022.
- US4 can proceed in parallel with US1–US3 after Phase 2 (different files: `jobs_groups.go`).
- Polish T036 ∥ T037 ∥ T043.

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
