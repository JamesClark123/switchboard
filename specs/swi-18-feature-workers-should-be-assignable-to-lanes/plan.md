# Implementation Plan: Worker Lanes

**Branch**: `jamesloganclark/swi-18-feature-workers-should-be-assignable-to-lanes` (spec) → `…-tasks` (plan + tasks)
→ `…-implement` | **Date**: 2026-10-28 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/swi-18-feature-workers-should-be-assignable-to-lanes/spec.md`

## Summary

Lanes replace the action worker-tag requirement. A lane is a named group of workers on one orchestrator; a `prompt`
action is **exclusive** or **inclusive** to one or more lanes, or un-laned (open-lane workers only).

1. **Model** (R1, R2, R14). Lanes, attach tickets, high-water marks, dropped worker tags and a roster clock live on
   the persisted `pb.Orchestrator`. Lanes have stable ids. Roster entries gain `lane_ids`, `lanes_pending`,
   `revision` and `written_seq`. Actions gain `lane_ids` + `lane_mode`. `worker_tag` is deprecated: it is read on
   load only, dropped, logged and listed in a dismissable notice.
2. **Roster freshness** (R3–R6). The owning daemon's registry stamps a `Sandbox.revision` on every write. One pure
   function, `applyRecords`, applies listings, events and attach/detach replies inside one atomic record update,
   using revisions, high-water marks and terminal removal. Reconcile no longer holds a lock across the network,
   links subscribe and start reading before they list, and the event hub reports overflow with an `events_dropped`
   marker. Events can now adopt and drop workers, so a launched worker joins within moments; removals are classified
   by state first, and events from hosts that predate lanes (revision 0) never change the roster.
3. **Dispatch** (R7, R8). A pure planner reserves lane workers for queued exclusive runs, then delivers in queue
   order through the candidate table: exclusive → own reservation, unreserved lane worker, later run's reservation;
   inclusive → unreserved lane worker, open-lane worker, later run's reservation; un-laned → open-lane worker. Before
   each delivery the worker is re-checked and claimed under the runtime lock: when the record changed since the
   pass's snapshot, its lanes are re-validated against the current record, so a mid-pass lane change can't send a
   run where it is no longer allowed. Seeded property and race tests pin SC-001/SC-002. Call and action delete share one bolt transaction for the record and its
   runs, so a deleted action's lane setting is copied onto its queued runs atomically.
4. **Guard and impact** (R10). One `CheckLaneChange` RPC reports what a membership save, action save, lane delete or
   detach would affect. Each guarded write carries the list the developer confirmed and is re-evaluated in its own
   atomic update.
5. **Attach tickets** (R11). Lanes picked at launch or attach travel as a ticket id on the controller record. The
   first adoption or attach commit consumes the ticket; an expired or mismatched ticket adopts the worker as
   *lanes pending*. Hosts that predate lanes drop the ticket id, so adoption fails closed for them, and the TUI
   doesn't offer the lane step there.
6. **Display and agents** (R9, R12, R13). Waiting reasons are derived on every run read. Lane names are pushed to
   each worker's host for display only, in the background, coalesced and serialised per worker; the sandbox list
   shows lane text only once a push has arrived (`lanes_known`). Agents get waiting reasons without worker names. Briefings and the list-actions tool
   describe lanes and mode, and are refreshed in the background.
7. **TUI** (R15). Adds a Lanes view (fourth detail view), one shared lane picker (worker's lanes, lane members,
   attach, launch), lane cells on Workers and sandbox-list rows, lanes + mode in the action form with `ctrl+l`
   Manage lanes, a lanes column on Jobs › Actions, waiting reasons on queued rows, and the dropped-tag notice. Lane
   UI is gated on a new `DaemonInfo.capabilities` entry, `lanes`, on the orchestrator's daemon (and, for attach and
   launch, on the target host's).

## Technical Context

**Language/Version**: Go 1.26 (`go.work`; modules `switchboardd`, `switchboard-tui`, `switchboard-proto`,
`switchboard-tui-e2e`)

**Primary Dependencies**: gRPC + protobuf (regenerated with `make proto`), bbolt, Bubble Tea / Bubbles / Lip Gloss,
`huh` v1.0.0, the official Go MCP SDK (already present). There are **no new dependencies**.

**Storage**: bbolt. The `orchestrators` bucket (`pb.Orchestrator`, extended) and the `orchestrator_runs` bucket
(`pb.Run`, extended with `kept_lanes`). The new `registry.UpdateOrchestratorRuns` spans both in one write
transaction. On the owning daemon, `Sandbox.revision` is stamped in `registry.Put`/`Update`.

**Testing**: `go test` per module. Daemon tests use the existing orchestrator test harness (`testing_test.go`,
fake owning daemons) plus two seeded property tests (`TestDispatchProperties`, `TestRosterFreshness`). TUI tests use
`fakeDaemon`, `update`/`press` and `fake_server_test.go`. E2E uses `switchboard-tui-e2e`. Gates are
`make fmt-check vet lint test cover` (≥ 90% per module), `make env-check` and `make e2e`.

**Target Platform**: Linux/macOS hosts running `sxbd`; terminals running `sxb`.

**Project Type**: daemon + TUI client + shared proto (existing three-module change)

**Performance Goals**:
- A dispatch pass costs O(Σ lanes per queued run + Σ lane memberships of free workers) (FR-314), using 32-bit lane
  masks.
- A waiting reason costs O(run's lanes) after one O(roster memberships) index per read (FR-315).
- No RPC waits on a briefing or lane-display push (FR-309a, FR-321).

**Constraints**:
- No lock is held across a network call by any roster writer (FR-308c).
- Status folding never waits on a roster write.
- The proto numbers avoid SWI-17's (PR #21) claims.
- No new env vars; TTLs (24 h tickets, 24 h removed marks) and caps (32 lanes, 200-character descriptions) are
  package constants.

**Scale/Scope**:
- ≤ 32 lanes per orchestrator; rosters of tens of workers; queues of hundreds of runs (200 finished retained).
- About 12 daemon files, 10 TUI files, 1 proto file, and the e2e action-form flow.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack. Features 001–010,
SWI-9 and SWI-17 are implemented in **Go**, a deviation recorded and justified in **001's Constitution Check +
Complexity Tracking**. SWI-18 stays inside that deviation and **adds no new ones**:

| Principle | Status for SWI-18 |
|-----------|-------------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint`. |
| III Type Safety | ✅ Typed proto messages and enums (`LaneMode`); lane sets as typed masks; status details are typed (`LaneChangeImpact`), with no `interface{}` payloads. |
| IV Naming & Layout | ✅ New files in existing packages (`O/roster.go`, `O/lanes.go`, `O/lanes_dispatch.go`, `UI/orchestrator_lanes.go`, `UI/lane_picker.go`), with colocated `_test.go`. |
| V Verification Before Merge | ✅ Same `make` gates (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`). |
| VI Multi-Level Testing | ✅ Unit and property tests in the daemon, model-driven TUI tests, a gRPC fake-server test for capabilities, and an e2e scenario for US1; ≥ 90% coverage held per module. |
| VII Containerized Deployment | ✅ N/A: daemon and client binaries (001 carve-out). |
| VIII Env Discipline | ✅ No env vars added. |
| Repository Structure | ✅ Changes confined to `src/services/switchboardd`, `src/apps/switchboard-tui`, `src/apps/switchboard-tui-e2e`, `src/libs/switchboard-proto`, `src/libs/switchboard-dial` (capability accessor), and docs. |

**Result**: PASS. **Re-checked after Phase 1 design: still PASS.** The design adds proto fields, RPCs and code in
existing packages. It adds no module, dependency, deployable or env var.

## Project Structure

### Documentation (this feature)

```text
specs/swi-18-feature-workers-should-be-assignable-to-lanes/
├── plan.md              # This file
├── research.md          # Phase 0: findings F1–F10 and decisions R1–R17
├── data-model.md        # Phase 1: lanes, roster entry, marks, tickets, kept settings, waiting reasons
├── quickstart.md        # Phase 1: validation scenarios 0–7
├── contracts/
│   ├── proto-delta.md   # Phase 1: messages, fields, RPCs, status codes
│   ├── lanes-ui.md      # Phase 1: TUI views, keys, pickers, confirms, rows
│   └── dispatch.md      # Phase 1: dispatch pass, freshness table, agent-facing text
├── checklists/          # (from the specify phase)
└── tasks.md             # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/proto/switchboard.proto   # contracts/proto-delta.md; `make proto` regenerates gen/
src/libs/switchboard-dial/conn.go                    # HasCapability(name) from DaemonInfo.capabilities

src/services/switchboardd/internal/
├── registry/
│   ├── registry.go          # Put/Update stamp Sandbox.revision (R3)
│   └── orchestrator.go      # UpdateOrchestratorRuns + RunTx, ViewOrchestratorRuns (R7)
├── agent/hub.go             # reserved slot + EventsDropped marker (R6)
├── grpc/
│   ├── server.go            # DaemonInfo.capabilities = ["lanes"]
│   └── orchestrator_rpcs.go # six new RPCs; guard/impact status details; new request fields
├── worker/
│   ├── controller.go        # Update applies WorkerLaneDisplay (validated, logged, lanes_known); AttachController records
│   │                        #   attach_ticket and carries lane display forward
│   └── endpoint.go          # ActionInfo: lanes + lane_mode instead of WorkerTag
└── orchestrator/
    ├── lanes.go             # NEW: lane CRUD, validation, membership saves (expected values), impact + guard (R10)
    ├── roster.go            # NEW: applyRecords, marks, tickets on adoption, roster clock (R4, R11)
    ├── reasoning.go         # launch commit + destroyReasoning through applyRecords
    ├── lanes_dispatch.go    # NEW: index, reservations, candidate iterators (R8)
    ├── dispatch.go          # pass uses the planner; re-check and claim under rt.mu (recGen); "its action was deleted"
    ├── runs.go              # callAction via UpdateOrchestratorRuns; waiting reasons on ListRuns/GetRun (R9); events adopt/drop
    ├── actions.go           # lane validation, sets_lanes, delete copies kept settings, guard
    ├── workers.go           # reconcile without lock; attach/detach via tickets, detaching set, applyRecords
    ├── links.go             # subscribe + read before list; events_dropped → listing-only re-reconcile
    ├── runtime.go           # recMu + recGen; lock order; detaching/delivering sets; lanePusher; briefer
    ├── briefing.go          # lane text; background coalesced briefer (R13)
    ├── stubs.go             # pushWorkerStateTo: name + briefing + lanes on reconnect (R12)
    ├── tools.go             # list-actions lanes/lane_mode; check_run waiting_reason
    ├── service.go           # load-time worker-tag drop (R14); summaries clear marks; new Service methods
    └── constants.go         # maxLanes, maxLaneDescription, attachTicketTTL, removedMarkTTL

src/apps/switchboard-tui/internal/
├── client/orchestrator.go   # new RPC wrappers; LaneChangeImpact from status details; ErrLaneStale
└── ui/
    ├── orchestrator_lanes.go  # NEW: self-contained lanesPanel (own target; detail via a getter): rows/keys, lane form, delete impact
    │                          #   confirm; hosted by the detail's Lanes view and by Jobs as the Manage lanes dialog
    ├── lane_picker.go         # NEW: shared picker overlay (worker lanes, lane members, attach, launch), guard + stale flows
    ├── orchestrator_views.go  # viewLanes; dispatch, help, render, submit cases; dropped-tag notice
    ├── orchestrator_attach.go # Workers lane cell + `l`; detach guard; attach lane step; direct-detach warning (FR-308b)
    ├── orchestrator_forms.go  # action form: lanes MultiSelect + mode Select, ctrl+l, reason clears; actionLine lanes
    ├── launch.go              # lane step after `O`; GetWorkerBundle (daemon host id + lanes) before the wizard closes; H clears
    ├── list_delegate.go       # thirdLine lanes (sanitised, incl. existing names)
    ├── jobs_overview.go       # waiting reason in the QUEUED branch of jobsRunLine
    ├── jobs_groups.go         # dropped-tag notice + `D` on Jobs › Actions
    ├── jobs.go                # Manage lanes dialog layer: keys to the panel first, drawn over the action form
    └── app.go / keys.go       # capability gate; key bindings

src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go  # action-form field order; US1 scenario
```

**Structure Decision**: the existing three-module layout. New daemon logic goes in three new files in
`internal/orchestrator`: roster freshness, lanes and the dispatch planner. They are pure where possible so the
property tests need no network. TUI work adds two files: the Lanes view and the shared picker. Everything else is
an edit to the files that own today's behaviour.

## Complexity Tracking

No new constitution violations. The Go-stack deviation is inherited from 001 and unchanged.

Points called out for review, not violations:
- **FR-314's reservation preference is amended in this PR** (spec Clarifications, "Plan review 2026-10-28"). It now
  compares the first unreserved worker of each of the run's lanes, because an exact minimum over every member would
  break FR-314's own Cost bullet (research R8). The outcome properties and SC-002 are unchanged.
- **Mixed versions**: hosts whose daemon predates `Sandbox.revision` are treated as always fresh (research R16).
  Freshness guarantees hold only once every host is updated.
