# Tasks: Worker Lanes (SWI-18)

**Input**: Design documents from `/specs/swi-18-feature-workers-should-be-assignable-to-lanes/` (plan.md, spec.md,
research.md, data-model.md, contracts/proto-delta.md, contracts/lanes-ui.md, contracts/dispatch.md, quickstart.md)

**Tests**: Included. The constitution (Rule VI) requires automated tests and ≥ 90% coverage per module, and SC-001,
SC-002 and FR-308c are pinned by property tests.

**Organization**: Tasks are grouped by user story (spec.md US1–US6) after a shared foundation. Daemon routing (US1,
US2) can be built and tested before any TUI work; the TUI stories (US3–US5) build on the daemon RPCs.

Paths are relative to the repository root. `O/` = `src/services/switchboardd/internal/orchestrator/`,
`W/` = `src/services/switchboardd/internal/worker/`, `R/` = `src/services/switchboardd/internal/registry/`,
`G/` = `src/services/switchboardd/internal/grpc/`, `UI/` = `src/apps/switchboard-tui/internal/ui/`,
`C/` = `src/apps/switchboard-tui/internal/client/`, `P` = `src/libs/switchboard-proto/proto/switchboard.proto`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US6 from spec.md

---

## Phase 1: Setup

**Purpose**: The contract and constants every later task uses.

- [X] T001 Apply [contracts/proto-delta.md](./contracts/proto-delta.md) to `P`:
  - the new messages (`Lane`, `LaneMode`, `AttachTicket`, `HighWaterMark`, `DroppedWorkerTag`, `KeptLaneSetting`,
    `WorkerLaneDisplay`, `LaneGuardConfirm`, `LaneChangeImpact`, `LaneActionUse`, `EventsDropped`);
  - the extended messages with the exact field numbers listed there;
  - `Action.worker_tag` left unmarked for now: `[deprecated = true]` is added in T052, once the last reader is
    gone, so `make lint` (SA1019) passes at every task in between;
  - the six RPCs and their request/response messages, appended after `OrchestratorLink`.

  Run `make proto` and commit the regenerated `src/libs/switchboard-proto/gen/`. Check that no number collides with
  SWI-17's (`Action` 9, `Run` 20–30, `WorkerView` 12–14, `WorkerRun` 4–10, `UpdateWorkerRequest` 8,
  `NotificationKind` 9–10, `AgentSession` 5–11, `AgentInput.AttachInfo` 4); if SWI-17 has merged, rebase onto it
  first.
- [X] T002 [P] Add constants to `O/constants.go`: `maxLanes = 32`, `maxLaneDescription = 200`,
  `attachTicketTTL = 24 * time.Hour`, `removedMarkTTL = 24 * time.Hour`, `laneIDPrefix = "ln-"`,
  `waitingNamesMax = 5`, `ticketSkew = 10 * time.Minute` (research R11), and the capability name
  `CapabilityLanes = "lanes"` (exported for the grpc server).
- [X] T003 [P] Advertise the capability: `G/server.go` sets
  `DaemonInfo.Capabilities = []string{orchestrator.CapabilityLanes}` (T002);
  `src/libs/switchboard-dial/conn.go` adds `HasCapability(name string) bool`; the UI `Daemon` interface in
  `UI/app.go` and `fakeDaemon` (`UI/launch_test.go`) expose it. The fake defaults to having `lanes`, with a field to
  remove it. Test in `src/libs/switchboard-dial/conn_test.go` (new file).

---

## Phase 2: Foundational (blocking)

**Purpose**: Revisions, atomic record+runs writes, the freshness engine, lane records and the guard. Every story
depends on this phase.

### Owning daemon

- [ ] T004 Stamp `Sandbox.revision` in `R/registry.go`. `Update` sets `stored.revision + 1` after `mutate`; `Put` sets
  `stored.revision + 1`, or 1 when no record exists. A caller-set value is overwritten. Tests in
  `R/registry_test.go`: monotonic across Put/Update; survives reopen; a caller cannot lower it (research R3).
- [ ] T005 [P] Event-stream overflow marker in `src/services/switchboardd/internal/agent/hub.go` (research R6):
  - keep the last slot of each subscriber channel in reserve;
  - on overflow, drop the event, set the subscriber's `overflowed` flag, and enqueue one `Event{EventsDropped}`
    into the reserved slot;
  - while flagged, drop further events; clear the flag at the next fan-out that finds the channel empty
    (`len(ch) == 0`), which is observable on a buffered channel.

  Tests in `agent/hub_test.go` (new file): a stalled subscriber gets exactly one marker; other subscribers are
  unaffected; `Publish` never blocks; after draining, the next overflow gets a new marker.

### Registry: record + runs in one transaction

- [ ] T006 Add `UpdateOrchestratorRuns(id, fn func(rec *pb.Orchestrator, runs RunTx) error)` and the `RunTx`
  interface (`NextSeq`, `Put`, `Get`, `Queued`) to `R/orchestrator.go`, with one bolt write transaction over both
  buckets, and a read-only `ViewOrchestratorRuns(id, fn func(rec *pb.Orchestrator, queued []*pb.Run) error)` in one
  bolt `View`. Add both to the `Store` interface in `O/store.go`. Tests in `R/orchestrator_test.go`: a failing `fn`
  writes nothing to either bucket; sequences match `NextRunSeq`; `Queued` is in receipt order; the view sees a
  record and runs from the same commit (research R7).

### Orchestrator runtime and roster freshness

- [ ] T007 In `O/runtime.go`:
  - add `recMu`, held by `rt.update` across the bolt transaction **and** the `rt.rec` cache store;
  - add `rt.updateRuns(fn)`, which wraps `UpdateOrchestratorRuns` the same way;
  - add `rt.recGen uint64`, bumped by every `rt.update`/`rt.updateRuns` commit together with the `rt.rec` store and
    read under `rt.mu` (research R8);
  - add `rt.detaching` and `rt.delivering` (`map[string]struct{}`) under `rt.mu`. `markDetaching` refuses a worker
    in `rt.delivering` (`FailedPrecondition` "<worker> is receiving a run — try again in a moment");
    `clearDetaching`, `isDetaching`, and `claimDelivery`/`endDelivery` for the pass;
  - change `rt.sbs` writes to keep the newer record by `revision` (a revision-0 record replaces, research R16; a
    removal evicts, T011);
  - document the lock order `runs.mu → recMu → bolt → rt.mu` on `rt.update`/`rt.updateRuns`: callbacks touch only
    the record, `RunTx` and in-memory state under `rt.mu`; they never take `runs.mu`, call `failRun`/`putRun`/
    `rt.svc.reg.*`, notify or emit. Those run after the commit, from the callback's result (research R7).

  `reconcileMu` stays until T012; its users move to `applyRecords` in T010–T012.
- [ ] T008 Create `O/roster.go` with `applyRecords(rec *pb.Orchestrator, in rosterInput, now time.Time)
  rosterResult`, exactly per [contracts/dispatch.md](./contracts/dispatch.md) "Roster freshness". It is the only
  code that applies sandbox records to `rec.workers`, `rec.reasoning_sandbox_id` and `rec.high_water_marks`.
  `rosterInput.cache` is a snapshot of `rt.sbs` for the input's ids, taken under `rt.mu` inside the `rt.update`
  callback, so the function reads no shared state. It covers:
  - the roster clock (`roster_seq`, `written_seq`);
  - freshness: `revision > entry.revision` (or `> mark.revision`) **and** `≥` the cached record's revision. For
    `attachReply`/`detachReply`/`launchReply`, the newer of the reply and the cached record is applied under the
    normal rules, keeping the reply's permissions (reasoning adoption, attach ticket);
  - removal first: a `removed` event or a DESTROYING record (any revision; today's daemon emits a bare
    `{id, DESTROYING}`) is classified before the freshness check, takes its `host_id` from the entry or the cached
    record, and asks the caller to evict the cache entry rather than replace it. For a non-roster sandbox it marks
    it *removed* only if it already has a mark or is cached as naming this orchestrator; otherwise it is ignored;
  - `rosterResult.changed == false` for status-only records, so the caller refreshes `rt.sbs` without taking
    `recMu` or writing bolt;
  - high-water marks with `host_id`: set, cleared by adoption, `removed` terminal, pruned only by a listing of their
    own host and after `removedMarkTTL`;
  - the listing-absence rule (`written_seq <= requestedSeq`) for roster entries; for a non-roster sandbox, absence
    prunes its mark;
  - reasoning: adopt only from `launchReply`, or from a listing while `reasoning_sandbox_id` is empty;
    `destroyReasoning` drops with a *removed* mark;
  - DESTROYING and removal as terminal;
  - revision-0 records: they refresh the cache and leave no mark; a revision-0 **event** never adopts or drops
    (research R16);
  - adoption rules for attach tickets (no ticket → open; valid → its current lanes, ticket deleted; else
    `lanes_pending`), and the fail-closed rule for a revision-0 record with no ticket id (sandbox-bound ticket → its
    lanes; host-bound launch ticket → `lanes_pending`; neither → open; research R11);
  - reasoning entries keep `reasoning=true` and no lanes.

  `rosterResult` lists adopted, dropped and lane-changed sandbox ids. Also sweep expired tickets
  (`sweepTickets(rec, now)`).
- [ ] T009 Tests in `O/roster_test.go`:
  - a table test for every row of the freshness table;
  - `TestRosterFreshness`, a seeded property test over random interleavings of attach replies, detach replies,
    late/duplicated/reordered events and listings. It asserts that no detached or removed sandbox is re-adopted from
    an older record, and that no entry written after a listing was requested is dropped by it;
  - ticket adoption: valid, expired, wrong host, wrong sandbox, already consumed, and a valid ticket whose lanes were
    all deleted (adopts into the open lane, spec L4 step 3);
  - a listing of host A never prunes a mark for host B, and a stale event from B then cannot re-adopt;
  - a record newer than the entry but older than the cache does not change the roster;
  - a status-only event reports no change (no bolt write);
  - an attach or launch reply whose sandbox already has a newer cached status record still adopts (from the cached
    record);
  - attach then an immediate direct detach (cached record names no controller, newer than the attach reply): not
    adopted; a detach reply older than a cached re-attach does not drop the worker;
  - a removal event for an unrelated sandbox writes nothing;
  - a bare revision-0 DESTROYING record for a roster worker drops it with a *removed* mark carrying the entry's host,
    evicts the cache, and a replayed older event then cannot re-adopt it;
  - on a revision-0 host: an event never adopts or drops; a detach reply then a buffered pre-detach event leaves the
    worker detached; a listing still adopts; a record with no ticket id but an outstanding ticket gets the ticket's
    lanes (sandbox-bound) or `lanes_pending` (host-bound launch ticket, listing only); the launch-ticket fallback
    consumes the oldest ticket issued no later than `ticketSkew` (10 min) after the sandbox's `created_at` (a host
    whose `created_at` is 5 min behind the ticket still matches, one 15 min behind doesn't), so a second ticketless
    adoption on that host joins the open lane; an `attachReply` from an attach that wrote no ticket joins the open
    lane even with a launch ticket outstanding;
  - `TestRosterFreshness` generates revision-0 hosts and bare DESTROYING records, and models `input.cache`;
  - reasoning: a late event from a destroyed reasoning sandbox does not re-adopt it during a rebuild, and a listing
    requested before a reasoning launch commit does not drop the new entry.
- [ ] T009a Move today's attach commit and detach drop onto the freshness engine (lane-free; T038 and T040 add
  tickets, the guard and *detaching* on top), so every user of `reconcileMu` moves before it is deleted:
  - the attach commit (`O/workers.go:187-202`, a direct `rt.update` that appends a `Worker`) becomes
    `rt.update(applyRecords(attachReply))`, so the entry gets `written_seq` from the roster clock;
  - the detach drop (`:237-241`, `dropWorker`) becomes `rt.update(applyRecords(detachReply))`;
  - both still take `reconcileMu` for now, because today's reconcile still holds it across its listing until T010.

  Tests in `O/workers_test.go`: attach then detach round trip; a reconcile whose listing was requested before an
  attach commit keeps the new worker (`written_seq > requested_seq`); a detach leaves a high-water mark.
- [ ] T010 Rewrite reconcile in `O/workers.go` and `O/links.go` (research R5):
  - `hostLink.serve` subscribes first and starts the event reader at once, then reconciles (off the reader
    goroutine), then opens the link;
  - reconcile reads `roster_seq` (`requestedSeq`), lists with no lock, then
    `rt.update(applyRecords(listing))`;
  - it fails runs on dropped workers (`failRunsOn(…, "worker detached")`) and keeps the reasoning-loss handling,
    `reconcileInFlight` and the emit;
  - it requests a briefing refresh (T013), except when the `events_dropped` marker triggered it.

  An `events_dropped` event on the link's subscription re-runs reconcile for that host, coalesced with any pending
  one; that reconcile lists and applies only: it requests no briefing and pushes no names or briefings (only the
  display pushes of workers it adopts, R12). Update `O/links_test.go` and
  `O/workers_test.go`: an event arriving during the listing is not lost; no lock is held across `ListSandboxes` (a
  blocking fake API with a concurrent `rt.update` succeeding); a host with 100 workers reconciles once and produces
  at most one marker; a reconcile triggered by `events_dropped` makes no briefing push.
- [ ] T011 Route `sandbox_changed` and `removed` events through `applyRecords` in `O/runs.go`
  (`onSandboxChanged`/`onSandboxRemoved`):
  - events now adopt and drop (F6);
  - a launched worker naming this orchestrator is adopted from its first event;
  - status folding (`foldStatus`) stays lock-free and never waits on `recMu`;
  - a removal or DESTROYING record evicts the `rt.sbs` entry after the rules have read it (FR-308c);
  - every adopted worker is marked for a lane display push (T014), whatever its lanes;
  - `triggerDispatch` runs after any roster change.

  Tests in `O/runs_test.go`: adoption from an event, drop when the controller changes, a stale event ignored,
  removal terminal and the cache entry gone.
- [ ] T012 Send the reasoning sandbox's roster writes through `applyRecords` in `O/reasoning.go`:
  - the launch commit (`:108-120`) applies `launchReply` (the newer of the reply and the cached record, per T008);
  - `destroyReasoning` (`:136-147`) applies `destroyReasoning`;
  - `dropWorker` (`O/workers.go:247`) is replaced by `applyRecords` inputs at its call sites;
  - delete `reconcileMu` (`O/reasoning.go:108`, `O/workers.go:17,189,239`). Its last users have now moved: attach
    and detach (T009a), reconcile (T010) and the reasoning launch (this task).

  Update `O/reasoning_test.go`: rebuild while events for the old sandbox arrive late; reconcile overlapping a launch;
  a newer status event for the new reasoning sandbox processed before the launch commit (the commit still adopts it).
- [ ] T013 Background, coalesced briefer in `O/briefing.go` + `O/runtime.go` (research R13):
  - `requestBriefing()` sets a pending flag, and one goroutine pushes to every reachable host with no lock held;
  - `PutAction`, `DeleteAction`, `Rename`, attach/adoption and reconcile call it instead of the synchronous
    `pushBriefings()`.

  Lane-specific triggers are added by the lane tasks. Tests: the RPC returns while a fake host blocks; ten triggers
  coalesce into ≤ 2 pushes.
- [ ] T014 Lane display pusher (research R12):
  - in `O/runtime.go`, a `lanePusher` (dirty set + drain goroutine per host, no lock across calls) sends
    `UpdateWorker{lanes: WorkerLaneDisplay{names, pending}}`; adoption marks the worker whatever its lanes (R12);
  - `O/stubs.go` `pushBriefingsTo` becomes `pushWorkerStateTo` and sends name + briefing + lanes on reconnect;
  - both push through the worker's `pushMu` and read its lanes from `rt.rec` while holding it, so the last push
    carries the latest value; a push that fails while the link is up puts the worker back in the dirty set;
  - `W/controller.go` `Update` applies `req.Lanes` to `controller.lane_names`/`lanes_pending`, sets `lanes_known`,
    drops and logs names that break the lane rule while applying the rest (FR-309b), and emits.

  Tests in `W/controller_test.go` (invalid name dropped, name still applied, `lanes_known` set) and
  `O/links_test.go` (reconnect push carries lanes; only marked workers are pushed; an attach with no lanes sends
  one lanes push, so `lanes_known` is set; a save never waits on a push; a
  failing fake push is retried; a pusher push and a reconnect push racing leave the latest lanes on the host).

### Lanes, effective settings, guard

- [ ] T015 Create `O/lanes.go` with the lane model:
  - `validateLaneName`, `validateLaneDescription` (data-model messages);
  - `newLaneID()` (`ln-` + 12 hex from `crypto/rand`);
  - `laneByID`, `laneNames(rec, ids)`;
  - `isOpenLane(w)`;
  - `effectiveLanes(rec, run) (ids []string, mode pb.LaneMode, ok bool)`: kept copy → current action → missing
    (data-model). A setting with no ids is returned as (`[]`, `UNSPECIFIED`), un-laned, whatever its stored mode.

  Tests in `O/lanes_test.go`.
- [ ] T016 Add the guard and impact in `O/lanes.go` (research R10):
  - `openLaneCount(rec, detaching)`;
  - `unlanedWork(rec, queued)`: un-laned prompt actions, plus queued runs of deleted actions whose kept setting is
    un-laned. Every guarded write and `CheckLaneChange` evaluate it inside `rt.updateRuns` (or its read-only view),
    so the record and the queued runs come from one transaction (FR-308a);
  - `guardFires(before, after, change)`, which also fires when the change saves an un-laned prompt action with no
    open-lane worker, including a re-save of one that is already un-laned (FR-308a);
  - `laneImpact(rec, queued, change) *pb.LaneChangeImpact`;
  - `confirmCovers(confirm, impact) bool` (current list ⊆ confirmed list);
  - `refuseGuard(impact) error`: `FailedPrecondition` with `LaneChangeImpact` as a status detail.

  Tests: from one open-lane worker to zero with un-laned actions; saving an un-laned action with none; a detaching
  worker not counted; kept un-laned runs listed; confirm covering vs not.
- [ ] T017 Wire `CheckLaneChange` in `G/orchestrator_rpcs.go` and a `Service.CheckLaneChange` in `O/service.go`. It
  dispatches on the `oneof` to the same pure "after" builder each write uses, and returns the impact without
  writing. Each write task (T018, T029, T030, T036, T040) adds its builder and its `CheckLaneChange` case with a test
  in `G/orchestrator_rpcs_test.go`; this task adds the RPC skeleton, the dispatch and the `NotFound`/empty cases. The
  top-level `orchestrator_id` wins; the nested request's id is ignored (and must be empty or equal, else
  `InvalidArgument`).

**Checkpoint**: revisions, freshness, atomic record+runs, lane records and the guard exist. Routing is unchanged.

---

## Phase 3: User Story 1 — Reserve workers for a kind of work (P1) 🎯 MVP

**Goal**: Exclusive actions run only on their lanes' workers; un-laned actions run only on open-lane workers; queued
runs say why they wait.

**Independent test**: daemon tests seed a lane and one member straight into the record (`seedLanes`, a test helper
in `O/lanes_test_helpers_test.go` that writes through `rt.update`, so US1 needs no US3/US4 RPC), make one action
exclusive, and call it and an un-laned action several times (US1 scenarios 1–6). Quickstart Scenario 1 needs US3
and US4 too.

- [ ] T018 [US1] Action lane fields in `O/actions.go`:
  - `validateAction` checks lane ids exist (`NotFound` "lane … no longer exists"), refuses `reason` with lanes,
    requires `EXCLUSIVE|INCLUSIVE` with lanes and `UNSPECIFIED` without, and deduplicates ids;
  - `normalizeAction` stops trimming `WorkerTag`;
  - `PutAction` refuses `!sets_lanes` or a non-empty `worker_tag` with `FailedPrecondition` "this client predates
    lanes — update the client";
  - `PutAction` validates and writes **inside** `rt.updateRuns`, so the lane check and the guard see the record and
    queued runs it writes against;
  - it evaluates the guard (T016) when the action ends up un-laned, honouring `PutActionRequest.guard`;
  - it calls `triggerDispatch` (FR-314a).

  Update `G/orchestrator_rpcs.go` to pass `sets_lanes` and `guard`. Tests in `O/actions_test.go` (replace the
  worker-tag case at `:39`).
- [ ] T019 [US1] Call/delete serialisation and kept settings (research R7):
  - `callAction` in `O/runs.go` runs inside `rt.updateRuns`: look up the action in the transaction's record (fail
    "unknown action"), compose, `NextSeq`, `Put`;
  - `DeleteAction` in `O/actions.go` runs inside `rt.updateRuns`: remove the action, disable its timed events (as
    today), and set `kept_lanes` on each of its QUEUED runs.

  Timed-event callers (`O/scheduler.go`) keep `fireMu`. Tests in `O/runs_test.go`: a concurrent call and delete
  either fails "unknown action" or yields a run with `kept_lanes`, over 200 iterations with `-race`; a new action
  with the deleted one's name does not change a kept run's routing.
- [ ] T020 [US1] Create `O/lanes_dispatch.go`, the planner (research R8, contracts/dispatch.md):
  - `buildIndex(rec, sbs, ups, detaching)`: free workers, `byLane` lists ordered by (lane count, roster order)
    by bucketing on lane count (0–32) in roster order, with no comparison sort; `open` list; masks;
  - `reserve(index, runs)` for exclusive runs;
  - a `candidates(run)` iterator for **exclusive** (own reservation → unreserved lane worker → later run's
    reservation, stealing it) and **un-laned** (open-lane);
  - lazy per-lane cursors that skip claimed, out and reserved workers;
  - `claim`, `out`, `release` operations.

  Inclusive is added in T025.
- [ ] T021 [US1] Rewrite the pass in `O/dispatch.go` on the planner:
  - read `gen0 := rt.recGen` under `rt.mu`, then take the snapshot of the record and queued runs from one
    `ViewOrchestratorRuns` (no separate `ListRuns` + `rt.record()`);
  - `free` excludes reasoning, `lanes_pending` and detaching workers;
  - `eligible` and the worker-tag lookup are deleted;
  - a run with a *missing* effective setting is re-read inside `rt.updateRuns`, which returns whether it is still
    missing; the pass then fails it through `failRun` **after** the commit (never inside the callback, T007);
  - per candidate: `recheckAndClaim(w, r, gen0)` under `rt.mu` (research R8: **always** sandbox still free and w
    not detaching; when `rt.recGen != gen0`, w still on the roster, not reasoning or *lanes pending*, and its
    current lane mask allowed by `effectiveLanes(rt.rec, snapshotRun)`, where a missing setting or a kept lane that
    no longer exists is a lane-fail skip; on success w joins `rt.delivering`) → `deliver` with no lock →
    `endDelivery(w)`;
  - a precondition refusal or a failed sandbox, detaching or roster re-check → `out` + release, next candidate;
  - a failed lane re-check → skip w for this run only, next candidate;
  - any other error → fail as today + `out`;
  - accepted → claim and release the run's other reservation;
  - `reason` runs keep today's path.
- [ ] T022 [US1] Tests in `O/dispatch_test.go`, replacing the tagged/untagged scenario at `:146-194`:
  - US1 acceptance scenarios 1–5 as table rows;
  - a worker refusing delivery moves the run to its next candidate;
  - a deleted action's queued run with no copy fails "its action was deleted", and a run whose action is deleted
    between the snapshot and the check is **not** failed (its kept copy is found on re-read);
  - a lanes-pending or detaching worker is never chosen;
  - `TestDispatchProperties` (contracts/dispatch.md), 100 seeds × 100 runs with random refusals, asserting
    guarantees 1–3 for exclusive and un-laned runs;
  - an instrumented cost check (guarantee 4).
- [ ] T022a [US1] Race tests in `O/dispatch_test.go`, run with `-race` (contracts/dispatch.md guarantee 5):
  - `TestDispatchLaneRace`: a fake `deliver` that blocks on a channel holds a pass mid-pass while the test, through
    `seedLanes`/`rt.update`, removes the next candidate from the run's lane, switches the action from inclusive to
    exclusive, deletes the lane, or marks the worker *lanes pending*; the run is not delivered to it, and is
    delivered to a worker that is still allowed;
  - a seeded variant applies random lane writes between and during deliveries over 100 seeds, and asserts that
    every delivery was allowed by the record committed at its claim;
  - `markDetaching` while a delivery to that worker is blocked is refused; a claim for a detaching worker is refused,
    including when the detach's callback has set the mark but not yet committed (a hook blocks the callback after
    `markDetaching`, so `rt.recGen` still equals `gen0`);
  - `DeleteAction` committed mid-pass for a run in the snapshot: the claim skips (missing setting), the run is not
    failed, and the next pass delivers it by its kept setting;
  - `DeleteAction` while a pass fails a missing run: no deadlock, and the run ends either failed or with
    `kept_lanes` (T007 lock order).
- [ ] T023 [US1] Waiting reasons in `O/runs.go` (research R9):
  - `laneCounts` is built once per `ListRuns`/`GetRun` from the record, `rt.sbs`, host reachability and
    `rt.detaching`;
  - `waitingReason(run, counts, forAgent)` returns the lane texts for all three modes (exclusive, inclusive,
    un-laned), the "stopped or unreachable" names (≤ 5 then `+N more`) and the "; N workers awaiting lane
    assignment" suffix. With `forAgent` it names no workers: `waiting: every worker that could take it is stopped
    or unreachable`;
  - *detaching* workers count as live, and as available while RUNNING with their host up (spec Clarifications);
  - set it on QUEUED prompt runs only;
  - `putRun` clears `waiting_reason` before writing.

  The `orchestrator_check_run` tool result in `O/tools.go` includes the `forAgent` reason. Tests in
  `O/waiting_test.go` cover each row, the pending suffix, a detaching-only lane, the agent text having no worker
  names, and a reason that disappears once a worker joins a lane (US1 scenario 6, through `seedLanes`).
- [ ] T024 [US1] FR-314a triggers that exist by this phase: action save or delete, adoption, drop and ticket
  consumption call `triggerDispatch`. Test in `O/dispatch_test.go`: a queued un-laned run is delivered after an
  action save makes it laned to a seeded lane with a free member, with no other event. The lane-write triggers and
  their SC-007 tests are in the tasks that add those writes (T028, T029, T030, T036).

**Checkpoint**: exclusive and un-laned routing, waiting reasons and kept settings work end to end in daemon tests.

---

## Phase 4: User Story 2 — Let lane workers help with a kind of work (P1)

**Goal**: Inclusive actions prefer unreserved lane workers, then open-lane workers, then a later run's reservation.

**Independent test**: daemon tests with one inclusive action and one lane worker, free then busy (US2 scenarios
1–4); quickstart Scenario 2.

- [ ] T025 [US2] Add the **inclusive** candidate iterator to `O/lanes_dispatch.go`: unreserved free lane worker →
  free open-lane worker → a later run's reservation (stealing it).
- [ ] T026 [US2] Tests in `O/dispatch_test.go`:
  - US2 scenarios 1–4 (scenario 4: inclusive-then-exclusive with one lane worker and one open-lane worker → both
    delivered);
  - an inclusive run takes a reserved lane worker only when no other worker it could take is free;
  - extend `TestDispatchProperties` to mix inclusive runs (SC-002).
- [ ] T027 [US2] Tests in `O/waiting_test.go` for the inclusive waiting reasons T023 returns (`waiting for a worker
  in a or an open-lane worker`, and its stopped-or-unreachable variant), with inclusive runs dispatched by T025.

**Checkpoint**: all three routing modes and SC-001/SC-002 hold.

---

## Phase 5: User Story 3 — Manage lanes on the orchestrator (P1)

**Goal**: The Lanes view lists, creates, renames, re-describes, re-members and deletes lanes.

**Independent test**: in the Lanes view, create a lane, add two workers, rename it, delete it; each change shows in
the Lanes and Workers views (quickstart Scenarios 1 and 6).

### Daemon

- [ ] T028 [US3] Implement `PutLane` in `O/lanes.go` + `O/service.go` + `G/orchestrator_rpcs.go`:
  - create: id minted, cap, name rules, `AlreadyExists`;
  - rename/describe: `NotFound` for an unknown id.

  After the write it emits, triggers dispatch, requests a briefing **only** on a rename of a lane an action uses
  (T053), and marks every member for a display push (T014). Tests in `O/lanes_test.go`, including that the write
  triggers a pass (FR-314a).
- [ ] T029 [US3] Implement `SetLaneMembers`:
  - inside `rt.updateRuns`, compare `expected_sandbox_ids` with the lane's current members as a set; `Aborted` if
    they differ, the lane is gone, or a named worker left the roster (every FR-307a case is `Aborted`, so the picker
    reloads);
  - refuse reasoning or unknown workers;
  - set or clear the lane on each worker, clearing `lanes_pending` on every worker it adds;
  - evaluate the guard with `guard`.

  Mark changed workers for display, then dispatch. Tests: stale refusal only when *this lane's* members changed
  (FR-307a); guard refusal and confirm; a queued exclusive run is delivered after a worker joins its lane with no
  other event (SC-007).
- [ ] T030 [US3] Implement `DeleteLane` in `rt.updateRuns`:
  - compute the impact (T016), refuse via the guard unless confirmed;
  - strip the lane from workers, actions (mode → `UNSPECIFIED` when empty), queued runs' `kept_lanes` (mode →
    `UNSPECIFIED` when empty) and tickets;
  - return the applied impact.

  Mark members for display, request a briefing if an action used it, then dispatch. Tests: the FR-305 impact
  counts (workers, actions with mode, actions left un-laned, re-routed runs incl. kept copies); an unused lane; the
  guard; delete an exclusive action with a queued run, then delete its only lane: the run is delivered to an
  open-lane worker.
- [ ] T031 [P] [US3] Add client wrappers in `C/orchestrator.go`:
  - `PutLane`, `DeleteLane`, `SetLaneMembers`, `SetWorkerLanes`, `CheckLaneChange`, `DismissDroppedWorkerTags`;
  - `ImpactFromError(err) (*pb.LaneChangeImpact, bool)`, which reads the status detail;
  - `ErrLaneStale` for `Aborted`.

  Tests in `C/orchestrator_test.go`.

### TUI

- [ ] T032 [US3] Add `viewLanes` in `UI/orchestrator_views.go`:
  - add it before `orchViewCount`, append "Lanes" to `orchViewNames`, and update the file comment;
  - add cases to `viewRowCount`, `orchDetailHelp`, the key dispatch and `renderOrchView`;
  - hide the view when the orchestrator's daemon lacks the `lanes` capability; `enterOrchDetail` (which carries
    `view` over, `UI/orchestrator_views.go:64`) and a capability change reset a hidden Lanes view to Workers.

  Fix the view-cycling tests that assume three views (`UI/orchestrator_views_test.go:13,123`), and add one: Lanes
  selected on an updated daemon, then an orchestrator on an older one opens on Workers.
- [ ] T033 [US3] Create `UI/orchestrator_lanes.go` with a self-contained `lanesPanel` per
  [contracts/lanes-ui.md](./contracts/lanes-ui.md). It carries its own `orchTarget`, cursor, lane form and confirm
  state, and reads the detail through a getter (`detail func() *pb.OrchestratorDetail`) on its host, so the host's
  reloads (`syncOrchestratorChanged`, `r`, the Jobs reload) reach it with no copy to refresh. It exposes
  `update(msg) (lanesPanel, tea.Cmd, handled bool)` and `view(width, height) string`, so the orchestrator detail
  (Lanes view) and the Jobs screen (Manage lanes, T047) can both host it; unhandled keys (`←`/`→`, `tab`, `q`, the
  host's shortcuts) go to the host:
  - rows: name, description, members, actions with mode, sanitised;
  - the empty state;
  - keys `a`/`e` (the panel's own lane form with `laneFormVals` → `PutLane`), `w` (member picker, T034), `d`
    (delete via `CheckLaneChange` → impact confirm → `DeleteLane` with the guard list; a guard refusal reopens the
    confirm with the new list), `r`;
  - every confirm body and warning built from daemon data (worker, action and lane names) goes through
    `sanitizeLine`, and each list is capped at 5 names, then `, and N more` (`confirmModal` sanitises only the
    title, `UI/confirm.go:79-82`).
- [ ] T034 [US3] Create `UI/lane_picker.go`, the shared `lanePicker` overlay:
  - modes `pickWorkerLanes`, `pickLaneMembers`, `pickAttach`, `pickLaunch`;
  - space toggles, enter saves, esc closes, j/k/up/down move;
  - rows (`name` or `name @ host`) through `sanitizeLine` and `fitCol`; the list scrolls;
  - remembers the opening value;
  - save path: `CheckLaneChange` → guard confirm when the impact is non-empty → save with the guard list → second
    confirm on a new refusal list → `ErrLaneStale` reloads from a fresh `GetOrchestrator`, keeps the cursor by id
    and shows the daemon message.

  The overlay is part of the `lanesPanel` (lane members) or drawn by its host screen (worker lanes on the detail,
  attach on the sandbox list, launch in the wizard), like the host picker. This task implements `pickLaneMembers`;
  T041 and T042 add the others.
- [ ] T035 [US3] Add tests in `UI/orchestrator_lanes_test.go` with `fakeDaemon`:
  - create / duplicate name refused with the daemon text / rename / description too long;
  - the member picker saves, and a stale save reloads;
  - delete with impact text (workers, actions with mode, left un-laned, N runs re-routed) and a plain confirm for an
    unused lane;
  - the guard confirm then a second confirm;
  - the view is absent without the capability;
  - a worker name with ANSI escapes is neutralised in the delete confirm and in picker rows, and a long impact list
    ends `, and N more`;
  - after `l` on the Workers view changes a worker's lanes, the Lanes view shows the new members with no `r`.

  Extend `fakeDaemon` (`UI/orchestrator_fake_test.go`) with the six RPCs, recorded in `orchCalls`.

**Checkpoint**: lanes can be managed from the orchestrator detail.

---

## Phase 6: User Story 4 — Assign workers to lanes and see them (P2)

**Goal**: Assign a worker's lanes from the Workers view, at attach and at launch; see lanes on the Workers view and
the sandbox list; the open-lane guard on detach; *lanes pending*.

**Independent test**: assign a worker to two lanes and check the Workers row and the Orchestrator Sandboxes row;
clear them and see `open lane` (quickstart Scenarios 1, 3, 4).

### Daemon

- [ ] T036 [US4] Implement `SetWorkerLanes` in `O/lanes.go`:
  - inside `rt.updateRuns`, compare `expected_lane_ids` (as a set) and `expected_lanes_pending` with the entry;
    `Aborted` if they differ, a ticked lane was deleted, or the worker left the roster (FR-307a);
  - refuse reasoning (`InvalidArgument`, FR-310);
  - set the lanes and clear `lanes_pending`;
  - evaluate the guard.

  Mark the worker for display, then dispatch. Tests: FR-307a refuses only on this worker's change; the lanes-pending
  flag clears; reasoning refused; guard.
- [ ] T037 [US4] Fill `WorkerView.lane_ids`, `lanes_pending` and `detaching` in `workerViews` (`O/workers.go`).
  Every RPC in `O/service.go` that returns an `Orchestrator` (summaries, `GetOrchestrator`, `Dismiss…`, the
  detail) clears `high_water_marks` and `attach_tickets` before returning. Tests in `O/workers_test.go` and
  `O/service_test.go`.
- [ ] T038 [US4] Attach with lanes (research R11, L4) in `O/workers.go`, on top of T009a's attach commit:
  - refuse `lane_ids` for a sandbox already on the roster (`FailedPrecondition` "already a worker of
    <orchestrator> — change its lanes with l") and any id that no longer exists (`NotFound` "lane "<id>" no longer
    exists"; never stripped silently);
  - with `lane_ids`, write an `AttachTicket` bound to the host and sandbox (at most 256 unexpired tickets per
    orchestrator: `ResourceExhausted` "too many pending attaches — try again later");
  - call `AttachController` with `controller.attach_ticket`, holding no lock;
  - apply the reply through `applyRecords(attachReply)`;
  - delete the ticket on a definite refusal (`AlreadyControlled`, `FailedPrecondition`, `NotFound`); keep it on a
    timeout or a commit failure.

  Pass `lane_ids` through `G/orchestrator_rpcs.go`. `W/controller.go` `AttachController` (which rebuilds the
  controller field by field, `:146-149`) records `controller.attach_ticket` from the request and carries
  `lane_names`, `lanes_pending` and `lanes_known` forward on a same-orchestrator re-attach. Tests: event-before-commit
  adopts with the lanes and the commit is a no-op (L4.4); refusal deletes the ticket; timeout keeps it; unknown lane
  → `NotFound` and no ticket; lanes on a roster sandbox refused; an idempotent re-attach keeps the displayed lanes.
- [ ] T039 [US4] Launch with lanes (L3):
  - `GetWorkerBundle` takes `host_id` (the target **daemon's** host id) + `lane_ids`; it refuses a `host_id` that
    is neither the orchestrator's own host nor in `rec.hosts` (`InvalidArgument` "unknown host <id>") and an unknown
    lane (`NotFound`) and applies the ticket cap, as T038; with lanes it writes a host-bound ticket and returns its id
    in `bundle.controller.attach_ticket` (`O/workers.go` `bundle()`);
  - adoption from the launch's `sandbox_changed` event consumes it (T011).

  Tests: adopted with lanes; an expired ticket → `lanes_pending` (with an injected clock, `rt.now`); another
  host's ticket → `lanes_pending`; unknown host and unknown lane refused.
- [ ] T040 [US4] Detach with the guard (L5) in `O/workers.go`, on top of T009a's detach drop:
  - one `rt.updateRuns` evaluates the guard (`DetachWorkerRequest.guard`) and, in its callback under `rt.mu`,
    `markDetaching`, which is refused while the worker is in `rt.delivering` (T007);
  - any `rt.updateRuns` error, the commit included, clears the mark;
  - `DetachController` runs with no lock;
  - on success apply the reply through `applyRecords(detachReply)` (drop + mark), then `clearDetaching`;
  - on error `clearDetaching`.

  Tests: a detaching worker is not free and not counted by a concurrent guard; a failed commit leaves no mark; a
  detach during a blocked delivery is refused; an older event cannot re-adopt after the detach (the replay that
  quickstart Scenario 7.2 describes, driven by the test's fake event stream). FR-322 restart test: after a
  restart, lanes, `lanes_pending`, tickets, marks, action settings and `kept_lanes` are back, and `detaching`,
  `delivering` and waiting reasons are not.
### TUI

- [ ] T041 [US4] Workers view in `UI/orchestrator_attach.go`:
  - `renderWorkersView` adds the lane cell (`lanes: a, b` | `open lane` | `⚠ lanes pending` | `detaching…`, names
    via `laneNames` + `sanitizeLine`; none for reasoning);
  - `l` opens `lanePicker{pickWorkerLanes}` (reasoning → status `reasoning sandbox — no lanes`; no lanes → `no lanes
    yet — a to add one`, where `a` opens the lane form and reopens the picker); without the `lanes` capability `l`
    shows the FR-318a message instead;
  - `x` detach, when the daemon has the `lanes` capability, runs `CheckLaneChange{detach_worker}` first and adds the
    guard line to the existing confirm, sending `DetachWorker` with the guard list (a refusal with a new list reopens
    the confirm). Without the capability it detaches as today;
  - help gains `l lanes`.
- [ ] T042 [US4] Attach and launch lane steps:
  - both steps are offered only when the orchestrator's daemon **and** the target host's daemon have the `lanes`
    capability. When the orchestrator has lanes but the target daemon lacks the capability, a confirm asks
    `The daemon on <host> predates lanes: attach to the open lane?` (host name sanitised); `n` cancels the attach or
    returns to the wizard;
  - `attachTo`/`attachWorkerCmd` in `UI/orchestrator_attach.go` open `lanePicker{pickAttach}` after the
    orchestrator is chosen when it has lanes, and pass `lane_ids` to `AttachWorker`; a `NotFound` reopens the picker
    reloaded;
  - `launch.go` opens `lanePicker{pickLaunch}` after the `O` picker when the orchestrator has lanes, shows
    `orchestrator: X (lanes: a, b)` in the title (sanitised, and sanitise the existing orchestrator name there too),
    and calls `GetWorkerBundle` with `host_id = m.daemonHostID(targetHost)` (`UI/launch.go:378`) and the lanes;
  - the bundle is fetched **before** the wizard closes (today it is fetched after, `UI/launch.go:793-801`), so a
    refusal returns to the lane step with the message and the picker reloaded, keeping sources, kits and name. The
    fetch is a `tea.Cmd` (never a call inside `Update`): the lane step shows `fetching bundle…`, ignores the keys
    that would launch, and `esc` cancels the request. Each fetch carries a sequence number (`bundleSeq`, bumped on
    every fetch and on `esc`, `H`, `O` and any lane change); a `bundleMsg` whose number isn't the current one is
    dropped (re-review run-610). A ticket for a launch that is then cancelled, or fails before
    the sandbox exists, is left to expire (it counts toward the 256-ticket cap meanwhile);
  - `H` after `O` clears the chosen orchestrator and lanes (`UI/launch.go:650-657`).
- [ ] T043 [US4] Sandbox-list detach (`o` on a worker) in `confirmDetach` (`UI/orchestrator_attach.go:188-205`):
  - **connected branch** (`od.DetachWorker`): this is a detach through the orchestrator (FR-308a). With the `lanes`
    capability, run `CheckLaneChange{detach_worker}` first, fold its list into the confirm and send the guard list,
    exactly as T041's `x`; without it, detach as today;
  - **not-connected branch** (FR-308b): when the
  orchestrator's daemon is not connected, compute from the last-known `m.orch` detail and actions whether the detach
  leaves no open-lane worker with un-laned actions, and add the warning line (action names sanitised, capped as in
  T033). With no last-known data, add `Cannot check open-lane workers: <name>'s daemon is not connected`.
- [ ] T044 [US4] Sandbox list in `UI/list_delegate.go`:
  - `thirdLine` appends `lanes: a, b` | `open lane` | `lanes pending` for non-reasoning workers from
    `SandboxController.lane_names`/`lanes_pending`, only when `lanes_known` is set;
  - each name, and the existing MCP and orchestrator names, go through `sanitizeLine`.

  `UI/sandbox_list.go`: an `events_dropped` event reloads that host's sandbox list. Tests in
  `UI/list_delegate_test.go`: lanes shown; nothing shown before `lanes_known`; `renderFieldsDiffer` redraws on a
  lane change (SC-004); escape sequences in a lane name are neutralised.
- [ ] T045 [US4] TUI tests in `UI/lane_picker_test.go` and `UI/orchestrator_attach_test.go`:
  - the worker picker saves, the stale reload works, and the guard confirm runs (US4 scenario 6);
  - no picker for reasoning (scenario 4);
  - the no-lanes offer (scenario 5);
  - the attach step sends `lane_ids` (scenario 8);
  - launch sends `GetWorkerBundle` with the daemon host id + lanes, and a refusal keeps the wizard open;
  - `H` after `O` clears the orchestrator and lanes;
  - a slow bundle fetch doesn't block input, and `esc` while fetching cancels it;
  - a stale bundle reply is ignored: start a fetch, change the host with `H` (and, separately, re-tick a lane), then
    deliver the old reply; nothing launches and the wizard stays on the new selection;
  - the open-lane confirm for a target daemon without `lanes`: `y` attaches with no lanes, `n` attaches nothing;
  - no `l` picker, attach step or launch step without the capability on the orchestrator's daemon, and no attach or
    launch step without it on the target host's daemon;
  - `lanes pending` and `detaching` cells;
  - the direct-detach warning with and without last-known data.

**Checkpoint**: workers can be assigned and seen everywhere; attach and launch carry lanes.

---

## Phase 7: User Story 5 — Set an action's lanes, and manage lanes from the action form (P2)

**Goal**: The action form picks lanes + mode, `ctrl+l` opens Manage lanes, and Jobs › Actions shows lanes.

**Independent test**: open the action form, Manage lanes, create a lane, close, tick it exclusive, save; Jobs ›
Actions shows it (quickstart Scenario 5).

- [ ] T046 [US5] Action form in `UI/orchestrator_forms.go`:
  - remove `workerTag` and the "Worker tag" input;
  - add `laneIDs []string` and `mode` to `actionFormVals`;
  - add a `huh.NewMultiSelect[string]` of lanes (by id, labelled with sanitised names), hidden for `reason`
    (`WithHideFunc`);
  - add a mode `huh.NewSelect` (exclusive preselected), hidden for `reason` or no ticks; the save sends
    `UNSPECIFIED` when nothing is ticked, whatever the hidden field holds;
  - switching Kind to `reason` clears the ticks and sets the status `lanes cleared: reason actions have no lanes`;
  - save sends `PutAction` with `sets_lanes: true`, lane ids and mode. When the result is un-laned it runs
    `CheckLaneChange{put_action}` first and confirms the guard (only against a daemon with the `lanes` capability;
    without it the save is refused locally, below);
  - `NotFound` keeps the form open with all values, reloads the lane options and drops ticks on missing ids
    (FR-316a).

  Without the `lanes` capability, refuse the save locally with the FR-318a message.
- [ ] T047 [US5] Manage lanes on the Jobs screen, where the action form lives (`UI/jobs.go:254-260`,
  `UI/jobs_groups.go:69-72`):
  - `ctrl+l` is caught in the Jobs form key path (`UI/jobs.go`) before `advanceOrchOpForm`
    (`UI/orchestrator_forms.go:440`) when `formKind == "action"`; it opens a `lanesPanel` (T033) for the form's
    orchestrator as a dialog layer on the Jobs state;
  - while the dialog is open, `UI/jobs.go` routes every key to the panel first (so `esc` closes the dialog, not the
    form) and `viewJobs` draws the panel over the form;
  - if the orchestrator detail can host an action form (`surfaceDetail`), it gets the same handling.

  `esc` closes the dialog, then:
  - rebuild only the lane MultiSelect options from a fresh `GetOrchestrator`;
  - drop ticks for deleted ids;
  - keep every other value and the focused field.

  The form's help shows `ctrl+l manage lanes`.
- [ ] T048 [US5] Jobs › Actions: `actionLine` in `UI/orchestrator_forms.go` replaces the `#tag` badge with
  `⇢ a, b · exclusive|inclusive` (sanitised) and shows nothing for un-laned actions (FR-320).
- [ ] T049 [US5] Tests in `UI/orchestrator_runs_test.go` (replace the worker-tag assertions at `:47,55,65` in
  `TestActionFormRoundTripAndRefusal`) and a new `UI/action_lanes_test.go`:
  - lanes + mode round trip;
  - mode defaults to exclusive;
  - reason hides and clears lanes;
  - a Manage lanes create + rename + delete keeps every other value (FR-317, US5 scenarios 3–4);
  - a `NotFound` refusal reloads the options;
  - the old-daemon refusal;
  - the `actionLine` lane cell.

**Checkpoint**: actions can be laned from the form without leaving it (SC-003).

---

## Phase 8: User Story 6 — Existing worker tags stop routing runs (P3)

**Goal**: Worker tags are dropped on load, recorded and shown once; briefings and tools describe lanes.

**Independent test**: load a record with a worker-tagged action; the action has no lanes, runs on open-lane
workers, and the notice shows until dismissed (quickstart Scenario 0).

- [ ] T050 [US6] Load-time drop in `O/service.go` (runtime creation): for each action with a non-empty `worker_tag`,
  clear it, append `DroppedWorkerTag{action, tag, now}` (the list keeps the newest 100), log
  `orchestrator %s: dropped worker tag %q from action %q`,
  and rewrite the record once. The read of the deprecated getter carries
  `//nolint:staticcheck // SA1019: read only to drop the retired worker tag (FR-318b)`, and so do the tests that set it.
  Also sweep expired tickets and old removed marks. Tests in `O/service_test.go`: an
  upgrade record loads with no error and no tag (SC-005); a repeat after a simulated downgrade appends again; the
  tag is never written back.
- [ ] T051 [US6] `DismissDroppedWorkerTags` in `O/service.go` + `G/orchestrator_rpcs.go`: clear the list and emit.
  Test.
- [ ] T052 [US6] Briefing and tools (research R13, contracts/dispatch.md "Agent-facing text"):
  - `renderBriefing` in `O/briefing.go` writes the lane sentence per action and drops the worker-tag sentence;
  - `toolAction` in `O/tools.go` replaces `worker_tag` with `lanes` + `lane_mode` (omitempty);
  - `ActionInfo` in `W/endpoint.go` mirrors it;
  - lane descriptions are never included;
  - with the last reader gone, mark `Action.worker_tag` `[deprecated = true]` in `P` with the contract's comment,
    run `make proto`, and check `make lint` passes (only T050's `//nolint` reads remain).

  Update `O/briefing_test.go:15,27`, `O/tools_test.go:101`, `W/endpoint_test.go:105`.
- [ ] T053 [US6] Lane triggers for the briefer (T013): a lane rename or delete requests a briefing only when an
  action uses the lane; membership and description changes never do. Tests in `O/briefing_test.go`.
- [ ] T054 [US6] Dropped-tag notice in the TUI:
  - the orchestrator detail (`UI/orchestrator_views.go`) and Jobs › Actions under the orchestrator heading
    (`UI/jobs_groups.go`) show `worker tags dropped on upgrade: a (#t), … — D dismiss` (sanitised);
  - `D` calls `DismissDroppedWorkerTags`.

  Tests in `UI/orchestrator_views_test.go` and `UI/jobs_test.go`.
- [ ] T055 [US6] Sandbox tags no longer affect dispatch (FR-319). Add a test in `O/dispatch_test.go` that a tagged
  and an untagged open-lane worker are equally eligible.

**Checkpoint**: upgrade is clean and visible.

---

## Phase 9: Polish & cross-cutting

- [ ] T056 Waiting reasons in the TUI (FR-315, SC-006): in the QUEUED branch of `jobsRunLine` (`UI/jobs_overview.go`),
  append `· <reason>` in the remaining width (dimmed, sanitised, `+` on truncation), copying the in-flight branch's
  `room` computation. Show the full reason in the run prompt overlay. Tests in `UI/jobs_test.go`.
- [ ] T057 [P] Update e2e `src/apps/switchboard-tui-e2e/orchestrator_e2e_test.go`:
  - remove the worker-tag `p.tab()` steps at `:304,468` and walk the new field order;
  - add a US1 scenario: create a lane in the Lanes view, add a worker, make an action exclusive, call it, and
    assert the worker that took it.
- [ ] T058 [P] Docs:
  - `README.md` (replace the worker-tag text at `:399` with lanes, the Lanes view, keys `l`/`w`/`ctrl+l`/`D`, and
    waiting reasons);
  - `CLAUDE.md` (the SWI-18 paragraph is already in place from the plan; check that it matches what was built).
- [ ] T059 FR-323 guard: if SWI-9's implementation is on `main` (as of this plan it is not: `UI/formguard.go` is
  absent), register the lane form, `lanePicker` and the launch wizard's lane step as `guardSurface`s (baselines when
  opened, exits through `requestExit`) with tests in `UI/guard_surfaces_test.go`. Manage lanes saves as it goes, so
  it has nothing to guard; when it drops the tick of a deleted lane, the action form re-baselines that field, so
  `esc` does not raise the unsaved-changes dialog for it. If SWI-9 is not on `main`, record in the PR that they follow
  the existing form conventions (spec Assumptions).
- [ ] T060 Run `make fmt-check vet lint test cover env-check e2e`; keep ≥ 90% coverage per module. Then run
  quickstart Scenarios 0–7 on a two-host setup and record the results in the PR.

---

## Dependencies & execution order

- **Setup (T001–T003)** → **Foundational (T004–T017, with T009a)** → stories.
- **US1 (T018–T024, T022a)** needs Foundational; its tests seed lanes with `seedLanes`, not US3/US4 RPCs. **US2
  (T025–T027)** needs US1's planner (T020–T021).
- **US3 (T028–T035)** needs Foundational; its TUI tasks need T031 (client wrappers). The daemon half of US3 can run
  alongside US1/US2.
- **US4 (T036–T045)**: the daemon tasks (T036–T040) need only Foundational; the TUI tasks (T041–T045) need US3's
  picker (T034).
- **US5 (T046–T049)** needs T018 (daemon action fields) and T033 (the `lanesPanel`, for Manage lanes).
- **US6 (T050–T055)** needs T018; T052–T053 are independent of the TUI.
- **Polish (T056–T060)** after the stories it touches; T056 needs T023.

Within Foundational:
- T004 → T008 (records carry revisions);
- T006 → T007;
- T007 + T008 → T009a → T010, T011, T012;
- T007 → T013, T014;
- T013 → T010, T011, T012 (reconcile and adoption request briefings);
- T009a + T010 + T011 → T012 (the `reconcileMu` deletion, once attach, detach, reconcile and the reasoning launch
  have moved off it);
- T015 → T016 → T017.

## Parallel opportunities

- T002 and T003 in parallel after T001.
- T004, T005 and T006 in parallel (registry, hub and registry-orchestrator are separate files).
- T013 (briefer) in parallel with T008–T009; T014 (lane pusher) in parallel with T008–T012.
- In US1, T023 (waiting reasons) in parallel with T020–T022 (planner).
- US3's daemon tasks (T028–T030) in parallel with US1/US2. T031 (client) in parallel with all daemon work.
- In US4, the daemon tasks T036–T040 in parallel with US3's TUI tasks T032–T035.
- In US6, T052 in parallel with T050–T051.
- T057 and T058 in parallel at the end.

## Implementation strategy

1. **MVP = Setup + Foundational + US1.** This gives exclusive and un-laned routing with waiting reasons, testable
   through daemon tests and the RPCs.
2. Add **US2** (inclusive) to complete routing, then **US3** (Lanes view) so a developer can use lanes from the TUI.
3. Then **US4** (assignment everywhere, display, attach/launch tickets), **US5** (action form) and **US6** (upgrade
   notice, briefing text).
4. Polish: queue display, e2e, docs, the guard registration, gates and the quickstart.
