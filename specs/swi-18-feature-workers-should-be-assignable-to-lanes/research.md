# Research: Worker Lanes (SWI-18)

Phase 0 of [plan.md](./plan.md). Each entry records a **Decision**, its **Rationale** and the **Alternatives
considered**. Code references are to `origin/main` at `48c5f0b`. `O/` is
`src/services/switchboardd/internal/orchestrator/`, `W/` is `…/internal/worker/`, `UI/` is
`src/apps/switchboard-tui/internal/ui/` and `P` is `src/libs/switchboard-proto/proto/switchboard.proto`.

## Findings that shape the plan

Reading today's code turned up behaviour that the spec's requirements change. These findings are why several
decisions below exist:

- **F1. Deleted actions lose their routing.** `dispatch` reads the tag from the *current* action
  (`O/dispatch.go:90-94`). A queued run whose action was deleted gets `""` and can run on any worker. FR-314b fixes
  this.
- **F2. Call and delete are not serialised.** `callAction` reads the in-memory record (`rt.record()`, a clone,
  `O/runtime.go:97-101`), then calls `NextRunSeq` and `PutRun` as two separate bolt transactions, with no lock shared with `DeleteAction` (`O/runs.go:43-79`, `O/actions.go:215-247`).
  No transaction spans the orchestrator record and its runs, except `DeleteOrchestrator`.
- **F3. Reconcile holds a lock across the network** (`reconcileMu` around `api.ListSandboxes`,
  `O/workers.go:16-21`). It also subscribes to the host's events only *after* reconcile returns
  (`O/links.go:226-233`), so changes made in between are missed.
- **F4. The hub drops events silently.** `fanout` does a non-blocking send into a 64-slot channel
  (`agent/hub.go:74-81`). A slow subscriber, including the orchestrator's own-host link (`W/local.go:58-65`), loses
  `sandbox_changed` events without knowing.
- **F5. Sandbox records have no revision.** Only `updated_at` (a timestamp) exists. Writes go through
  `registry.Put` (`registry/registry.go:63`) and `registry.Update` (`:131`), plus about twenty `store.Update` call
  sites in `sandbox/`, `worker/`.
- **F6. Events never adopt or drop roster entries.** Only reconcile, attach, detach and removal change the roster
  (`O/runs.go:546-587`). A launched worker waits for the next reconnect.
- **F7. Briefing pushes are synchronous.** `PutAction`, `DeleteAction` and `Rename` call `pushBriefings()`, which
  calls `UpdateWorker` on every worker in turn with a 30 s timeout (`O/briefing.go:82-92`, `O/stubs.go:24-43`).
- **F8. The TUI cannot tell a daemon's features apart.** `DaemonInfo` has no capability list. The only old-daemon
  signal is `codes.Unimplemented` → `DaemonTooOldError` (`client/orchestrator.go:25-42`). An old daemon silently
  drops unknown fields on `PutAction`.
- **F9. SWI-9 is not on `main`.** Its forms guard (`formguard.go`, `requestExit`) exists only as a spec and plan
  (PR #18 merged plan + tasks; implementation pending). Forms use `kitFormKeyMap()` and esc closes at once.
- **F10. SWI-17 (PR #21, open) also extends the proto.** It claims `Action` 9, `Run` 20–30, `WorkerView` 12–14,
  `WorkerRun` 4–10, `UpdateWorkerRequest` 8, `NotificationKind` 9–10, `AgentSession` 5–11, `AgentInput.AttachInfo` 4
  and the RPCs `DeliverWorkerStep`/`ResolveRunStep` (its latest plan, PR #21 head). This plan's numbers are disjoint from
  those, so either PR can land first; the second rebases only the `.proto` text and regenerates.

---

## R1. Where lanes live, and lane identity

**Decision.** Lanes are a repeated `Lane` on the persisted `pb.Orchestrator` record (new field 11). Each lane has
an opaque `id` (`ln-` + 12 random hex characters) minted by the daemon at create, a `name`, an optional
`description` and `created_at`. Workers, actions, kept settings on runs and attach tickets all refer to lanes by
`id`. Lane names are checked with the action-name regex `^[a-z0-9][a-z0-9-]*$`, 1–64 characters, unique per
orchestrator, `open` reserved. Descriptions are trimmed, single line (no `\n`/`\r`, no control characters), at most
200 runes. At most 32 lanes (`maxLanes`, a constant in `O/constants.go`).

**Rationale.** The orchestrator record is already the unit of atomic change (`registry.UpdateOrchestrator`), and
lanes are owned by it and deleted with it (Key Entities). Ids make rename a single-field change that cannot affect
routing (FR-304) and let saves name lanes by identity (FR-316a). The 32-lane cap lets the dispatcher hold a lane set
as a `uint32` bitmask (R8).

**Alternatives considered.**
- *A separate `lanes` bolt bucket.* Rejected: every roster, action and lane change would need a cross-bucket
  transaction, and deleting the orchestrator would have one more thing to clean up.
- *Lanes keyed by name.* Rejected: rename would have to rewrite every reference, and a save racing a rename would
  point at the wrong lane.

## R2. Roster entry fields, *lanes pending* and *detaching*

**Decision.** `pb.Worker` gains `lane_ids` (5), `lanes_pending` (6), `revision` (7, the sandbox revision the entry
was last written from, R3) and `written_seq` (8, the roster clock at the write, R4). *Detaching* is **not**
persisted. It is an in-memory set `rt.detaching map[sandboxID]struct{}`, read under `rt.mu`, so it is cleared by
any restart (FR-322, L1). An **open-lane worker** is `!reasoning && !lanes_pending && len(lane_ids)==0`.
`WorkerView` gains `lane_ids` (15), `lanes_pending` (16) and `detaching` (17) for the TUI.

**Rationale.** `lane_ids`, `lanes_pending` and `revision` must persist (FR-322); detaching must not. Keeping
detaching in memory needs no load-time clearing step.

**Alternatives considered.** *A persisted `detaching` flag cleared on load.* Rejected: an extra write on every
load, and a crash between write and clear would need care that the in-memory set gets for free.

## R3. Sandbox revision (FR-308c)

**Decision.** Add `uint64 revision = 24` to `pb.Sandbox`. The registry stamps it inside the bolt transaction of
every write: `registry.Update` sets `revision = stored.revision + 1` after `mutate` returns; `registry.Put` sets
`revision = stored.revision + 1` when a record exists, else 1. Callers never set it, and a value set by a caller is
overwritten. `Manager.Announce`'s `updated_at` staleness check is left as it is.

**Rationale.** `registry.Put`/`Update` are the single choke point for sandbox writes (F5), so stamping there covers
every write, today's and future ones, without touching the ~20 call sites. A per-record counter in the record's own
transaction is monotonic without a global lock. Sandbox ids are never reused (L2), so a restart cannot reset a
revision: it is read back from the stored record.

**Alternatives considered.**
- *Use `updated_at`.* Rejected: wall-clock timestamps can repeat or go backwards (clock steps), and two writes in
  one clock tick would compare equal.
- *A daemon-global sequence.* Works, but needs a shared counter key in every write transaction for no benefit.

## R4. Roster freshness engine (FR-308, FR-308c, L2)

**Decision.** One pure function in a new `O/roster.go` applies sandbox records to the record:

```go
// applyRecords folds observed sandbox records into rec. It is called inside rt.update, so the
// freshness check and the change are one atomic write. now is used only for marks.
func applyRecords(rec *pb.Orchestrator, in rosterInput, now time.Time) rosterResult
```

`rosterInput` names the source (`listing` with its host and `requestedSeq`, `event`, `attachReply`,
`detachReply`, `launchReply` for the reasoning sandbox, `destroyReasoning`, `removal`) and carries the records.
It also carries **`cache`**: a snapshot of the `rt.sbs` entries for the input's sandbox ids, taken under `rt.mu`
inside the `rt.update` callback (rt.mu is innermost in the lock order, R7). Every rule that reads "the cached
record" reads this snapshot, so the function stays pure and the property tests model the cache explicitly (revised
in review run-593).
`applyRecords` is the only code that applies sandbox records to `rec.workers`, `rec.reasoning_sandbox_id` and
`rec.high_water_marks`. That includes the reasoning launch commit and `destroyReasoning` (`O/reasoning.go:108-150`)
and today's `dropWorker` (`O/workers.go:247`). Lane writes change only the lane fields of existing entries. `rosterResult` reports the adopted, dropped and changed workers,
so the caller can fail runs on dropped workers, push lanes and briefings, and trigger dispatch after the write.

The rules, all applied inside the one `rt.update`:
- **Roster clock.** `Orchestrator.roster_seq` (15) is incremented on every roster write. Each written entry gets
  `written_seq = roster_seq`.
- **Fresh.** A record changes an entry only if `record.revision > entry.revision` and `≥` the cached record's
  revision. For a sandbox not on the roster it must beat its high-water mark instead of an entry. Entries are
  **not** rewritten on status-only events: `applyRecords` reports "no change" and the caller only refreshes the cache,
  so status folding never takes `recMu` or writes bolt. The cache comparison is what makes a record that is newer
  than the entry but older than the latest seen state stale. Attach, detach and launch **replies** are applied as the
  newer of the reply and the cached record, keeping the reply's permissions (reasoning adoption, attach ticket).
  Events from the same write burst may already be cached, so a plain comparison could judge a reasoning launch reply
  stale and never adopt it. Exempting replies outright would instead let an old reply override a newer direct detach.
- **Removal of unrelated sandboxes.** A removal event or DESTROYING record for a sandbox that is not on the roster
  sets a *removed* mark only if the id already has a mark or is cached as naming this orchestrator. Anything else is
  ignored, as `onSandboxRemoved` does today (`O/runs.go:571-579`), so other sandboxes never grow the record.
- **High-water marks.** `Orchestrator.high_water_marks` (13) is a `map<string, HighWaterMark>` keyed by sandbox id:
  `{revision, removed, set_seq, removed_at, host_id}`. `host_id` is recorded because once the entry is gone the
  mark is the only record of where the sandbox lived. A non-removal drop sets `revision = max(cause revision, entry
  revision)` and `set_seq = roster_seq`. A fresh adoption deletes the mark.
- **Removal is classified by state, before freshness** (revised in review run-593). Today's owning daemon emits a
  destroy as a bare `{id, state: DESTROYING}` record **after** the store delete (`sandbox/manager.go:257,866`), so it
  carries revision 0, no `host_id` and no controller, on updated hosts too. A `removed` event or any DESTROYING
  record is therefore handled before the freshness check, whatever its revision. Its `host_id` comes from the roster
  entry or the cached record, and it **never replaces the cache**: once the rules have read the cached record, the
  cache entry is evicted.
- **Removal is terminal.** A `removed` event, a DESTROYING record, or a listing absence drops the entry whatever its
  revision, evicts the cached record and sets `removed=true, removed_at=now`. A later record for a removed id is
  ignored.
- **Listing absences.** Reconcile reads `roster_seq` *before* it lists (`requestedSeq`). A roster worker on that
  host missing from the listing is dropped only if `entry.written_seq <= requestedSeq`.
- **Pruning.** In the same update, a listing prunes marks with `host_id` equal to its host whose
  `set_seq <= requestedSeq` and whose sandbox is absent. A listing absence marks *removed* only for roster entries;
  for a sandbox already off the roster, absence prunes its mark. Removed marks older than 24 h (`removedMarkTTL`) are pruned on load and on every listing.
- **Adoption** uses the ticket rules of R11.
- **Reasoning.** A record naming this orchestrator as reasoning is adopted only from the reasoning launch reply,
  or from a listing while `reasoning_sandbox_id` is empty (today's recovery, `O/workers.go:51`); never from an event
  otherwise. `destroyReasoning` drops the old entry with a *removed* mark, so a late event from it cannot re-adopt
  it during a rebuild.
- **Cache.** `rt.sbs` is refreshed when `record.revision > cached.revision`, independent of the roster rule (L2:
  stale records still refresh the cache when newer than the cache). A revision-0 record (a host that predates
  lanes, R16) replaces it; a removal evicts it.
- **Revision-0 events never change the roster** (R16). For a host whose records carry no revision, only listings and
  this orchestrator's own replies adopt or drop, as today; its events refresh the cache and fold status only.
  Removals are still terminal (above).

The rules live in one pure function so the property tests (T-roster) can feed it shuffled, duplicated and late
records without a network or a daemon.

**Rationale.** The spec's freshness rules are subtle and must apply identically from three sources (L2). A pure
function over the record, called from one place inside `rt.update`, is the only way to keep them identical and to
test them exhaustively.

**Alternatives considered.**
- *Keep `reconcileMu` and add checks at each call site.* Rejected: it holds a lock across the network (F3), which
  FR-308c forbids, and spreads the rules over four call sites.
- *Wall-clock "written_at" instead of a roster clock.* Rejected: the listing-absence rule compares "written before
  the listing was requested"; a monotonic counter in the same record makes that exact.

## R5. No lock across the network; subscribe before list (FR-308c, L2)

**Decision.**
- `rt.update` takes a new `rt.recMu` around the bolt transaction **and** the cache store of the result, so updates
  and `rt.rec` agree on order. It is held only across local bolt I/O.
- `reconcileMu` is deleted. Reconcile becomes: read `requestedSeq` → `ListSandboxes` (no lock) →
  `rt.update(applyRecords(listing))`.
- `hostLink.serve` (`O/links.go:226-251`; today it reconciles, then subscribes, and reads events only after
  `OpenLink`) opens `Subscribe` **first** and starts the event reader **at once**, then
  reconciles (off the reader goroutine), then opens the link stream. Events that arrive during the listing are
  applied by the same freshness rule, so the overlap is harmless, and they never pile up in the hub's 64-slot buffer
  (`agent/hub.go:48`) while the reconcile's own pushes emit events (revised in review run-593).
- Attach and detach make their owning-daemon call with no lock and apply the reply through `applyRecords`.
- Status folding (`foldStatus`, `O/runs.go`) keeps updating `rt.sbs` under `rt.mu` only, never waiting on
  `recMu`.

**Rationale.** FR-308c's "No waiting on the network" and "No gaps" bullets, and L2's "One atomic write".

**Alternatives considered.** *Subscribe after reconcile but re-list once after subscribing.* Rejected: doubles the
listing cost on every reconnect and still has a window.

## R6. Event-stream overflow is reported (FR-308c "No gaps")

**Decision.** Add `EventsDropped events_dropped = 9` to the `Event` oneof. The hub keeps the last slot of each
subscriber's 64-slot channel in reserve. When an event does not fit in the first 63 slots, the hub drops it, sets
the subscriber's `overflowed` flag, and puts an `EventsDropped{}` marker into the reserved slot if none is pending.
While the flag is set, further events are dropped (the marker already covers them). The orchestrator's link re-reconciles that host on `events_dropped` (coalesced with
any reconcile already pending). That reconcile **lists only**: it pushes no worker state (names, briefings, lanes),
so it emits nothing and cannot overflow the stream again. The flag is cleared at the next fan-out that finds the
subscriber's channel empty (`len(ch) == 0`), which is observable on a buffered channel, unlike "the marker was read". The TUI treats it as "reload this host's sandbox list".

**Rationale.** The spec requires the stream to *say* it dropped events (Assumptions). A reserved slot guarantees the
marker is never itself dropped, without blocking the publisher. Old clients ignore the unknown oneof arm.

**Alternatives considered.**
- *Close the subscription on overflow.* Rejected: the Jobs screen treats an ended stream as a lost host
  (010 R3), which would flash "unreachable" for a slow client.
- *A larger buffer.* Rejected: it only moves the threshold.

## R7. One transaction for the record and its runs; call/delete serialisation (FR-314b, L7)

**Decision.** The registry gets one new method:

```go
// UpdateOrchestratorRuns runs fn in one bolt write transaction with the record and its run bucket.
func (r *Registry) UpdateOrchestratorRuns(id string, fn func(rec *pb.Orchestrator, runs RunTx) error) (*pb.Orchestrator, error)

type RunTx interface {
	NextSeq() (uint64, error)
	Put(*pb.Run) error
	Get(runID string) (*pb.Run, error)
	Queued() ([]*pb.Run, error) // receipt order
}
```

- `callAction` moves into it: it looks up the action **in the transaction's record**, composes, takes the next
  sequence and puts the run, all in one transaction. A call that finds no action fails with "unknown action".
- `DeleteAction` uses it too: it removes the action and copies the action's lane setting onto every QUEUED run of
  that action as `Run.kept_lanes` (31, `KeptLaneSetting{lane_ids, mode}`), in the same transaction.
- Bolt allows one write transaction at a time, so a call and a delete are serialised by construction.
- **Lock order** (revised in review run-593): `runs.mu` → `recMu` → bolt → `rt.mu`. The `rt.update`/`rt.updateRuns`
  callback may touch only the record, `RunTx` and in-memory state under `rt.mu` (innermost). It never takes
  `runs.mu`, never calls `failRun`, `putRun` or any `rt.svc.reg.*` method (bolt's writer lock isn't re-entrant), and
  never notifies or emits. Failures, notifications, `closedRun` and emits run **after** the commit, from the
  callback's result. A run "still missing" inside `rt.updateRuns` is returned to the caller and failed afterwards
  through `failRun` (which re-checks it is still QUEUED).
- The orchestrator's `runs.mu` is still taken around run state transitions as today. `rt.updateRuns` is used for
  call, action delete and lane delete, and for **every guarded write** (action save, membership saves, detach mark),
  because the guard's list includes queued runs of deleted actions and must be evaluated in the same atomic update
  as the change (FR-308a). `CheckLaneChange` reads through the matching read-only view.
- A read-only `ViewOrchestratorRuns(id, fn)` (one bolt `View`) gives dispatch a consistent snapshot of the record and
  its queued runs. Without it, a `DeleteAction` committed between `ListRuns` and `rt.record()` would make a kept run
  look *missing*. Before failing a run as "its action was deleted", dispatch re-reads it inside `rt.updateRuns` and
  fails it only if it is still missing there.
- **Effective lane setting** of a queued run (`effectiveLanes(rec, run)`): `run.kept_lanes` if set; else the
  current action's setting if the action exists; else **missing**. Dispatch fails a missing run with "its action
  was deleted" (L7).
- Lane delete (R10) strips the lane from `kept_lanes` of queued runs in the same transaction; a kept setting left
  with no lanes means un-laned (FR-314b).

**Rationale.** FR-314b requires one atomic change covering the record and the runs, and a call that either records
its run before the delete or fails. Bolt's single writer gives both without an extra mutex.

**Alternatives considered.**
- *A Go mutex shared by call and delete, with separate transactions.* Rejected: a crash between the record write
  and the run writes would leave runs with no copy, which FR-314b forbids.
- *Freeze the lane setting onto every run at call time.* Rejected: FR-314b and the Assumptions say queued runs
  follow the action's *current* setting.

## R8. Dispatch pass with reservations (FR-314, L6, SC-001, SC-002)

**Decision.** Split `dispatch` into a pure **planner** and the existing delivery loop.

1. **Snapshot**: the record and the queued runs from one `ViewOrchestratorRuns` (R7), plus `rt.sbs` and host
   reachability.
2. **Index** (`O/lanes_dispatch.go`): one pass over free workers builds
   - `byLane[laneIdx]`: free workers in that lane, ordered by (number of lanes, roster order). The workers are
     bucketed by lane count (0–32) in roster order, so building the lists costs no comparison sort;
   - `open`: free open-lane workers in roster order;
   - each worker's lane set as a `uint32` mask (R1's 32-lane cap).
   A free worker is live, not reasoning, not lanes pending, not detaching, on an up host, RUNNING, agent IDLE,
   `controller.run == nil` (today's `free` plus the three exclusions).
3. **Reserve.** For each queued exclusive run in queue order: look at the first unreserved worker in each of its
   lanes' lists (a per-lane cursor that skips reserved workers lazily) and reserve the one with the fewest lanes
   outside the run's mask (`bits.OnesCount32(w.mask &^ run.mask)`), ties to roster order.
4. **Deliver.** For each queued run in queue order, build its candidate iterator per the L6 table and try each
   candidate: **re-check and claim** it (below), then `deliver`. Outcomes:
   - accepted → the worker is claimed; the run releases any reservation it held elsewhere;
   - a failed sandbox or roster re-check, or a precondition refusal → the worker leaves the pass (`out`), any
     reservation on it is released, and the run tries its next candidate;
   - a failed **lane** re-check (the worker is fine but no longer allowed for *this* run) → the run skips it and tries
     its next candidate; the worker stays available to later runs, which re-check it themselves;
   - any other error → the run fails as today, and the worker leaves the pass;
   - no candidate left → the run releases its reservation and stays queued.
   Candidate (3), "reserved for a later run", needs no queue lookup: runs are walked in queue order and earlier
   runs have already released or used their reservations, so any reservation still held belongs to a later run.
   Stealing it clears that run's reservation.
5. `reason` runs keep today's path, outside lanes and reservations.

**Re-check and claim** (revised in review run-593; FR-311–FR-314, SC-001). Today's pass re-checks eligibility
against the live record just before delivering (`O/dispatch.go:117`, `!free(live, liveUp) || !eligible(run, w,
live, tag)`). With lanes, eligibility lives in the orchestrator record, which can change while a pass runs: a pass
can deliver for many seconds (`deliverTimeout` is 15 s per delivery). So the re-check validates the worker against
the **current record**, under the same lock and record generation the reservation was planned from:
- `rt.recGen` is an in-memory counter bumped by every `rt.update`/`rt.updateRuns` commit, stored with `rt.rec` under
  `recMu` and read under `rt.mu`. The pass reads `gen0 := rt.recGen` under `rt.mu` before its `ViewOrchestratorRuns`
  snapshot. The reservations and candidate lists are planned from that snapshot.
- `recheckAndClaim(w, r)` runs under `rt.mu`, holding no other lock and making no call:
  1. the sandbox record is still free (today's check: `free(rt.sbs[w], up)`);
  2. if `rt.recGen == gen0`, nothing was committed since the snapshot, and the planned eligibility stands;
  3. otherwise it re-reads `rt.rec` (O(the run's lanes)): w is still a roster worker, not reasoning, not
     *lanes pending*, not in `rt.detaching`, and w's current lane mask is allowed by the run's **current** effective
     setting (FR-311–FR-313, FR-314b);
  4. on success it adds w to `rt.delivering` in the same critical section. The entry is removed when `deliver`
     returns and the run's start is recorded.
- **Linearisation.** A lane write that commits before the claim is seen by step 3. One that commits after the claim
  is ordered after the delivery: the worker was allowed at the moment the pass delivered to it, which is FR-314's
  "decided at dispatch … at that moment". The write's own `triggerDispatch` (FR-314a) re-plans the queue.
- **Detach vs delivery.** `markDetaching` (R10) runs under `rt.mu` and is refused with `FailedPrecondition`
  `"<worker> is receiving a run — try again in a moment"` while the worker is in `rt.delivering`; a claim refuses a
  worker in `rt.detaching`. The two are mutually exclusive, so a detach can never clear a controller whose run is
  being delivered (`W/controller.go` `Detach` clears `Run` with no check).
- No lock is held across `deliver` (FR-308c).

**Cost.** Building the index is O(Σ lane memberships of free workers). Each run looks at O(its lanes) list heads,
and every lazy skip retires one (worker, lane) membership for the rest of the pass, so the pass is O(Σ lanes per
queued run + Σ lane memberships of free workers), as FR-314's Cost bullet requires.

**On the reservation preference (spec amended).** As first written, FR-314 asked a run to prefer "the worker with the
fewest lanes outside the run's own lanes" over **all** candidates. That exact minimum costs O(members of the run's
lanes) per run, which breaks FR-314's own Cost bullet. Counter-example: for a run in lanes {A,B}, with workers
v {A,X}, s {B,Y} and u {A,B}, every worker has two lanes; only a scan of all members finds u (no outside lanes). The
spec is therefore amended in this PR (Clarifications, "Plan review 2026-10-28"; FR-314 and L6): a run compares the
first unreserved worker of each of its lanes, with each lane's workers ordered by fewest lanes and then roster order,
and takes the one with the fewest lanes outside the run's lanes, then roster order. The preference is a tie-breaker.
SC-002's two outcome properties hold whichever reservable worker is chosen, because of the fallback chain.

**Testing.** A seeded property test (`TestDispatchProperties`) generates random rosters (1–12 workers, 0–4 lanes
each from 0–6 lanes), random queues (exclusive, inclusive, un-laned) and random delivery refusals, runs passes
against a fake deliverer, and asserts after every pass: every delivery is allowed by FR-311–FR-313 (SC-001); no run
is still queued while a free, accepting worker it could take is unclaimed; no run waits for a later run (SC-002).
It runs 100 seeds × 100 runs in CI. A concurrent variant (`TestDispatchLaneRace`, run with `-race`) applies random
lane writes (memberships, action modes, lane deletes, detaches) between and during deliveries and asserts that every
delivery was allowed by the record committed at its claim.

**Alternatives considered.**
- *Bipartite matching (max-flow) per pass.* Rejected: higher cost, harder to explain, and the spec fixes queue
  order rather than maximum throughput.
- *No reservations; first-fit in queue order.* Rejected: an earlier inclusive run would take the only lane worker a
  later exclusive run needs while an open-lane worker sat free (US2 scenario 4).

## R9. Waiting reasons, derived at read time (FR-315, SC-006)

**Decision.** `ListRuns` and `GetRun` build one `laneCounts` index per read, from the record and `rt.sbs`:

- per lane, and for the open lane: `live` (roster workers in it, *detaching* ones included, since FR-308 keeps them on
  the live roster) and `available` (of those: record RUNNING and host up). A detaching worker is therefore live and
  available until its detach completes, so a run only it could take shows no reason meanwhile (FR-315; revised in
  review run-593);
- `pending`: workers with `lanes_pending`.

Each QUEUED `prompt` run gets `Run.waiting_reason` (32), a string set only on the read path and never stored
(`putRun` clears it). The rules, from the run's effective setting:

| Run | No live worker can take it | All that can are unavailable |
|-----|----------------------------|------------------------------|
| Exclusive `a,b` | `waiting for a worker in a, b` | `waiting for w1, w2, stopped or unreachable` |
| Inclusive `a` | `waiting for a worker in a or an open-lane worker` | as above |
| Un-laned | `waiting for an open-lane worker` | as above |

When `pending > 0`, each reason gets `; N workers awaiting lane assignment`. Worker names in the second column are
the live workers that could take the run, by display name, at most 5 then `+N more`. Busy but available workers
give no reason: the run is simply queued. A run whose effective setting is *missing* gets no reason (dispatch fails
it). The reason is a separate field from `Run.reason` (failure/cancel reason).

**Agents get a lane-only reason** (revised in review run-593). FR-315 shows the reason "to an agent checking the
run", and `orchestrator_check_run` returns any run of the orchestrator by id (`O/tools.go:174-179`), which today
exposes no worker names. So `waitingReason(run, counts, forAgent)` drops the names for the tool result: the
"stopped or unreachable" row reads `waiting: every worker that could take it is stopped or unreachable`, and the
*lanes pending* suffix keeps only its count. The TUI (`ListRuns`/`GetRun`) gets the names.

**Cost.** The index is O(roster memberships) once per read; each run then costs O(its lanes). Names for the second
column come from a per-lane list built only for lanes whose `available` count is zero.

**Rationale.** FR-315 requires derivation at read time, separation from the failure reason, and no per-run scan of
every worker.

**Alternatives considered.** *Store the reason on the run and refresh it on roster changes.* Rejected by FR-315
("never stored"), and it would go stale on changes that start no pass.

## R10. The open-lane guard and change impact (FR-305, FR-308a)

**Decision.** One RPC reports a change's impact, and every guarded change carries the confirmation it was shown.

- `CheckLaneChange(CheckLaneChangeRequest)` takes `orchestrator_id` and a `oneof change` of the guarded requests:
  `SetWorkerLanesRequest`, `SetLaneMembersRequest`, `PutActionRequest`, `DeleteLaneRequest`,
  `DetachWorkerRequest`. It returns `LaneChangeImpact` and changes nothing:
  - `unlaned_actions`, `unlaned_runs`: the guard's list (empty when the guard does not fire);
  - for a lane delete: `workers`, `actions` (with mode), `actions_left_unlaned`, `rerouted_runs` (count, including
    kept copies).
- Each guarded request carries `LaneGuardConfirm guard` with the `unlaned_actions` and `unlaned_runs` the developer
  saw. The change is evaluated inside its own `rt.updateRuns` transaction (R7), which sees the record and the queued
  runs together: if the guard fires and the current list is not within
  the confirmed one, the RPC fails with `FailedPrecondition` and a `LaneChangeImpact` status detail; the client
  shows the new list in a second confirm (FR-308a "Confirm").
- **Guard rule** (`guardFires(before, after, change)`): with `open(x)` = live, non-detaching open-lane workers and `work(x)`
  = un-laned `prompt` actions plus queued runs of deleted actions whose kept setting is un-laned (runs of existing
  un-laned actions are covered by listing the action):
  `(open(before) ≥ 1 && open(after) == 0 && work(after) ≠ ∅) || (open(after) == 0 && the change saves an un-laned
  prompt action)`. "Saves" covers a new action, one made un-laned, and a re-save of one that already is (FR-308a,
  "save an un-laned `prompt` action … while it has none").
- **Detach** through the orchestrator: one `rt.updateRuns` evaluates the guard and, in its callback under `rt.mu`,
  adds the worker to `rt.detaching`, refusing while the worker is in `rt.delivering` (R8). The mark is removed on
  **any** `rt.updateRuns` error, the commit included, so it never leaks. The `DetachController` call runs with no
  lock; the reply is applied through `applyRecords(detachReply)`; on error the mark is removed (L5).

**Rationale.** FR-308a's single shape (check, refuse-or-apply, confirm) is the same for five changes; one impact
message and one confirm field keep the TUI to one confirm flow. FR-305's delete confirm needs the same counts.

**Alternatives considered.** *A `check` flag on each RPC.* Rejected: `PutAction` returns `Action` and other RPCs
return their own types, so a check answer would need a different shape per RPC.

## R11. Attach tickets (FR-306a, FR-306b, L3, L4)

**Decision.** `Orchestrator.attach_tickets` (12) holds `AttachTicket{id, lane_ids, host_id, sandbox_id, issued_at}`.
`SandboxController.attach_ticket` (9) carries the ticket id to the owning daemon.

- **Launch**: `GetWorkerBundleRequest` gains `host_id` (2, the **daemon** host id the target daemon advertises, as
  `daemonHostID` gives it in the TUI, not a TUI host entry id) and `lane_ids` (3). A `host_id` that is neither the
  orchestrator's own host nor in `rec.hosts` is refused (`InvalidArgument` "unknown host <id>"). With lanes, the
  orchestrator writes a ticket bound to that host and returns its id in `bundle.controller.attach_ticket`. The launch passes the
  controller unchanged, so the owning daemon records the ticket id.
- **Attach**: `AttachWorkerRequest` gains `lane_ids` (4). With lanes, the orchestrator writes a ticket bound to the
  host and sandbox, then calls `AttachController` with the ticket id in the controller, then commits the reply
  through `applyRecords(attachReply)`. `lane_ids` on a sandbox that is **already** on the roster is refused
  (`FailedPrecondition` "already a worker of <orchestrator> — change its lanes with l"), so a re-attach never
  bypasses `SetWorkerLanes`' expected-value check and guard.
- **Unknown lanes**: every id in `lane_ids` must exist when the ticket is written, or the request is refused with
  `NotFound` `lane "<id>" no longer exists`. Ids are never stripped silently; a lane deleted after the ticket is written
  is stripped from the ticket by lane delete (FR-305), and a ticket left with no lanes adopts as *lanes pending*.
- **The owning daemon** (`W/controller.go` `AttachController`) builds the controller field by field
  (`:146-149`). The new code records `attach_ticket` from the request and carries `lane_names`, `lanes_pending` and
  `lanes_known` forward on a same-orchestrator re-attach, so an idempotent re-attach doesn't blank the display.
  The ticket id is never cleared on the host: it is read only at adoption, and an adoption after a drop always
  follows a new `AttachController` or launch, which rewrites it (a drop happens only when the controller stops
  naming this orchestrator, or on removal, which is terminal).
- **Adoption** (`applyRecords`): a ticket that exists, is unexpired and matches the host (and sandbox if set) → its
  *current* lanes, ticket deleted in the same write; a ticket id that doesn't → `lanes_pending`; **no ticket id** →
  open lane, except when it can't be trusted (below).
- **Hosts that predate lanes fail closed** (revised in review run-593). An older owning daemon's `AttachController`
  drops `attach_ticket` (its field-by-field rebuild, `W/controller.go:146-149`), so its records arrive with no
  ticket id even when lanes were picked. For a revision-0 record with no ticket id: an unexpired ticket bound to its
  sandbox id → that ticket's lanes (ticket consumed); otherwise an unexpired launch ticket bound to its host →
  `lanes_pending`. Only with neither is it an open-lane worker, as today. The TUI also hides the lane step when the
  **target host's** daemon lacks the `lanes` capability (R15), so this is the second line of defence.
- **Commit after an adoption** finds the ticket gone and the entry present; it changes nothing (L4.4).
- **Definite refusal** (`AlreadyControlledError`, `FailedPrecondition`, `NotFound`) deletes the ticket; a timeout or
  a commit failure keeps it.
- **Expiry**: 24 h (`attachTicketTTL`). Expired tickets are swept on load and on every record update that touches
  tickets.
- The ticket id is 16 random hex characters. A ticket from another host or orchestrator never matches (FR-306b
  "Not lane data").

**Rationale.** The ticket ties the picked lanes to whichever path adopts the sandbox first (event, reconcile or
commit), so no run is dispatched before the lanes apply (FR-306b).

**Alternatives considered.** *Send the lanes themselves to the owning daemon and read them back on adoption.*
Rejected by FR-309b: the roster is the only source of membership.

## R12. Showing lanes on the worker's host (FR-309, FR-309a, FR-309b)

**Decision.**
- `SandboxController` gains `lane_names` (7), `lanes_pending` (8) and `lanes_known` (10), display only. `lanes_known`
  is set by the first `UpdateWorker{lanes}`, so the sandbox list never shows a confident "open lane" for lanes it
  hasn't been told (an older orchestrator daemon, a push still pending, or the orchestrator's host down).
- `UpdateWorkerRequest` gains `WorkerLaneDisplay lanes = 9` (`{repeated string names; bool pending}`); presence of
  the message means "set".
- The owning daemon (`W/controller.go` `Update`) drops and logs any name that breaks the lane-name rule and applies
  the rest of the request (FR-309b).
- The orchestrator never reads `lane_names` back.
- **Pushes.** A per-runtime `lanePusher` keeps a dirty set of worker ids. Roster and lane changes mark only the
  workers whose displayed names or pending flag changed (a rename marks every member). A goroutine drains the set
  per host, coalescing, with no lock held during the call. A host that is down is skipped; reconnect pushes all of
  its workers' current lanes in `pushBriefingsTo` (now `pushWorkerStateTo`), alongside the name and briefing.
- **Ordering and retry** (revised in review run-593). The pusher and `pushWorkerStateTo` both push through the
  per-worker `pushMu` (`O/runtime.go:55`) and read the worker's lanes from `rt.rec` **while holding it**, so the last
  push to land always carries the latest value. A push that fails while the host link is up puts the worker back in
  the dirty set (retried on the next drain); one that fails because the host went down is left to the reconnect push.
- **TUI.** `thirdLine(sb)` (`UI/list_delegate.go:76`) adds `lanes: a, b` / `open lane` / `lanes pending` for
  non-reasoning workers whose `lanes_known` is set (nothing otherwise), with each name through `sanitizeLine`. Because `renderFieldsDiffer` already compares
  `thirdLine`, lane changes redraw the row (SC-004). The patch also sanitises the existing MCP and orchestrator
  names on that line.

**Rationale.** It reuses the orchestrator-name path (F7, `UpdateWorkerRequest.name`), as FR-309a asks.

**Alternatives considered.** *The TUI joins workers to the orchestrator's roster itself.* Rejected: the orchestrator's
daemon may not be connected to this client, and the spec wants the list correct "whichever host runs the worker".

## R13. Briefing and list-actions tool (FR-321)

**Decision.**
- `renderBriefing` drops the worker-tag sentence and writes, per laned action, `Runs only on workers in lanes
  a, b.` (exclusive) or `Runs on workers in lanes a, b, or on any open-lane worker.` (inclusive). An un-laned
  action adds `Runs only on open-lane workers (workers in no lane).` (one lane reads `lane a`; contracts/dispatch.md
  has the table) Lane descriptions are never included.
- The list-actions tool's `toolAction` replaces `worker_tag` with `lanes` (names) and `lane_mode`
  (`"exclusive"`/`"inclusive"`/omitted). `W/endpoint.go`'s mirror `ActionInfo` changes the same way.
- **Background, coalesced refresh.** A `briefer` per runtime, shaped like `triggerDispatch` (pending flag + one
  goroutine): `requestBriefing()` marks it pending; the goroutine pushes to every reachable host's workers with no
  lock held. Triggers: action added/changed/deleted, orchestrator renamed, worker attached or adopted, host
  reconciled (today's), plus lane renamed or deleted **when an action uses it**. Membership and description
  changes do not trigger it.
- `PutAction`, `DeleteAction` and `Rename` return before the push completes (FR-321 changes today's synchronous
  push on purpose).

**Alternatives considered.** *Keep the synchronous push and only add lane triggers.* Rejected by FR-321.

## R14. Retiring the worker tag (FR-318, FR-318a, FR-318b)

**Decision.**
- `Action.worker_tag = 6` stays in the proto, marked `[deprecated = true]`, documented as "read only on load to drop
  it; never written; never reused". New lane fields are `Action.lane_ids` (10) and `Action.lane_mode` (11,
  `LaneMode` enum: `UNSPECIFIED`, `EXCLUSIVE`, `INCLUSIVE`).
- **Lint.** `[deprecated = true]` makes the generated `GetWorkerTag` deprecated, and staticcheck (SA1019) flags its
  uses. The single load-time read and its tests carry
  `//nolint:staticcheck // SA1019: read only to drop the retired worker tag (FR-318b)`.
- **Load** (`Service.load`, and `rt` creation): for every action with a non-empty `worker_tag`, clear it, append
  `DroppedWorkerTag{action, tag, dropped_at}` to `Orchestrator.dropped_worker_tags` (14), log
  `orchestrator %s: dropped worker tag %q from action %q`, and rewrite the record once. This repeats after a
  downgrade/re-upgrade.
- **Dismiss**: `DismissDroppedWorkerTags(OrchestratorIdRequest)` clears the list and emits `orchestrator_changed`.
- **Save marking**: `PutActionRequest.sets_lanes` (3). A request without it is refused with `FailedPrecondition`
  "this client predates lanes — update the client". A request with `worker_tag` set is
  refused the same way.
- **Validation**: `reason` actions with lanes are refused; `prompt` actions with lanes need `EXCLUSIVE` or
  `INCLUSIVE`; with no lanes the mode must be `UNSPECIFIED`. Every lane id must exist, or the save is refused with
  `NotFound` "lane … no longer exists" (FR-316a).
- **Daemon capabilities**: `DaemonInfo.capabilities` (10), a repeated string; this feature adds `"lanes"`. The TUI
  reads it at the handshake. Against a daemon without it, the TUI hides the Lanes view and lane pickers, and refuses
  action saves locally with "the daemon on <host> predates lanes — update with u" (FR-318a reverse; `u` on the
  Sandboxes list updates every connected daemon, `UI/sandbox_list.go:668`, matching today's `errTooOld`).

**Rationale.** Keeping field 6 lets the daemon *see* the old tag to drop and record it; marking it deprecated and
never writing it satisfies "never reused". A capability list (rather than a version compare) is robust to
dev builds.

**Alternatives considered.**
- *`reserved 6;`* Rejected: the daemon could not read the old value to log and record it (FR-318b).
- *Version comparison.* Rejected: dev builds carry non-semver versions (`dev`), and capabilities are what the TUI
  actually needs to know.

## R15. TUI design (FR-301–FR-307, FR-309, FR-316–FR-317, FR-320, FR-323)

**Decision.** Full key and screen details are in [contracts/lanes-ui.md](./contracts/lanes-ui.md). In summary:

- **Lanes panel component**: the lane list, lane form, delete confirm and picker live in one self-contained
  `lanesPanel` (`UI/orchestrator_lanes.go`) that carries its own `orchTarget` and op state, and reads the
  orchestrator detail from its host through a getter, so every reload the host does is seen at once (no private copy;
  revised in review run-593). It
  is drawn by the orchestrator detail (as the Lanes view) and by the Jobs screen (as the Manage lanes dialog over the
  action form, which lives on Jobs: `UI/jobs.go:254-260`, `UI/jobs_groups.go:69-72`). Each screen routes keys to the
  panel first while it is open, so `esc` closes the dialog and not the form.
- **Lanes view**: `viewLanes` is added before `orchViewCount` and hosts the panel. Rows show name, description, members,
  and actions with mode. Keys: `a` add, `e` edit (name, description), `w` members, `d` delete (impact confirm), `r` reload.
- **Lane form**: a huh form (name, description) on the existing op-form plumbing (`setOpForm`, `newOrchForm`).
- **Member and worker pickers**: one shared `lanePicker` overlay (space toggles, enter saves, esc closes), modelled
  on the host picker in `orchestrator_views.go`. It is used for a lane's members (rows = workers, reasoning
  excluded), a worker's lanes (rows = lanes, with "no lanes = open lane"), the attach step and the launch step. It
  remembers what it opened with and sends that as the expected value (FR-307a).
- **Action form**: the worker-tag input is replaced by a `MultiSelect` of lanes and a `Select` of mode, both hidden
  for `reason` (`WithHideFunc`). The mode select is hidden while no lane is ticked. `ctrl+l` opens **Manage lanes**,
  a Lanes-view dialog over the form, and closing it rebuilds only the lane options and drops ticks for deleted lanes
  (FR-317). Switching to `reason` clears the ticks and sets the status `lanes cleared: reason actions have no
  lanes`.
- **Capability before guard calls**: every `CheckLaneChange` call (detach `x`, sandbox-list `o` detach, pickers,
  action save) is made only when the orchestrator's daemon has the `lanes` capability. Against an older daemon the
  detach goes ahead as today, so a new client can still detach.
- **Sandbox-list detach through the orchestrator**: `o` on a worker whose orchestrator daemon *is* connected calls
  `DetachWorker` (`UI/orchestrator_attach.go:188-205`). That is a guarded detach (FR-308a), so it runs
  `CheckLaneChange{detach_worker}` and folds the list into the existing confirm, like `x` on the Workers view.
- **Capability per daemon.** Lane pickers need the `lanes` capability on the **orchestrator's** daemon; the attach
  and launch lane steps also need it on the **target host's** daemon (R11). Without it the step is not offered.
- **Display**: Workers rows add a lane cell (`lanes: a, b` / `open lane` / `⚠ lanes pending` / `detaching…`); the
  sandbox list's third line per R12; Jobs › Actions rows replace the `#tag` badge with `⇢ a, b · exclusive`; queued
  run rows (`jobsRunLine`) add the waiting reason in the remaining width, dimmed.
- **Dropped-tag notice**: a line at the top of the orchestrator detail and of Jobs › Actions for that orchestrator,
  `worker tags dropped on upgrade: build-ui (#frontend), … — D dismiss`.
- **Guard (FR-323)**: SWI-9 is not on `main` (F9). If SWI-9's implementation merges first, the lane form, the
  pickers and Manage lanes are registered as guarded surfaces; otherwise they behave like the existing forms, as the
  spec's Assumptions allow. The tasks include both branches as one conditional task.

**Alternatives considered.** *Lane membership edited inside a huh form.* Rejected: huh's MultiSelect has no
"expected value" hook for FR-307a reloads, and the host picker pattern already exists.

## R16. Old clients and mixed versions

**Decision.**
- An old TUI talking to a new daemon: its action saves are refused (R14), its Workers view shows no lanes, and its
  sandbox list ignores the new `SandboxController` fields. It can still call actions, cancel runs and attach
  (attaching with no lanes, as today).
- A new TUI talking to an old orchestrator daemon: no `lanes` capability → lanes are hidden and action saves are
  refused locally (R14).
- A new orchestrator daemon with an old worker daemon (revised in review run-593):
  - `UpdateWorker` with the unknown `lanes` field is ignored by the old daemon (display only).
  - `Sandbox.revision` is absent (0). **Events** from such a host never adopt or drop (R4): only listings and this
    orchestrator's own replies change the roster for it, which really is today's behaviour, so a buffered pre-detach
    event can't re-adopt a detached worker. Revision-0 records refresh the cache and leave no high-water mark.
    Removals are classified by state and stay terminal.
  - Its `AttachController` drops `attach_ticket`. Adoption fails closed (R11): a sandbox-bound ticket gives its
    lanes, a host-bound launch ticket gives *lanes pending*, and the TUI doesn't offer the lane step for that host.
  - The Daemons screen already flags version skew; the quickstart notes that all hosts should be updated together.
- **Downgrade** (unverified): Go protobuf keeps unknown fields when an older binary rewrites a record, so a downgraded
  host would carry its last `revision` forward unchanged, and its later writes would look stale to the roster until
  its next listing. Downgrading a worker host while lanes are in use is unsupported; detach and re-attach its workers
  after a downgrade.

**Rationale.** Lanes are a routing feature of the orchestrator's daemon. With events from revision-less hosts kept
out of roster changes and adoption failing closed, mixed versions degrade display and freshness, never routing
correctness.

## R17. Testing strategy

**Decision.** Go tests in each touched package, using the existing helpers:

- **Daemon unit**: `O/roster_test.go` (freshness property test: random interleavings of events, listings and replies
  never re-adopt a detached sandbox or drop a fresh one), `O/dispatch_test.go` (R8 property and race tests + the US1/US2
  acceptance tables), `O/waiting_test.go` (extend), `O/lanes_test.go` (CRUD, validation, guard, impact),
  `O/actions_test.go` (sets_lanes), `O/service_test.go` (the worker-tag migration), `registry` (revision stamping, `UpdateOrchestratorRuns`),
  `agent/hub_test.go` (overflow marker), `W/controller_test.go` (display lanes, invalid names dropped).
- **TUI**: `fakeDaemon` gets the six new RPCs and a `capabilities` list; tests drive `Model` with `update`/`press`
  (Lanes view, pickers, stale refusal reload, Manage lanes round trip, waiting reasons, notice, old-daemon refusal).
- **E2E** (`switchboard-tui-e2e`): the action-form field order changes (worker tag removed, lanes added), so the
  existing `p.tab()` sequences at `orchestrator_e2e_test.go:304,468` are updated; one new scenario covers US1.
- **Gates**: `make fmt-check vet lint test cover` (≥ 90% per module) and `make e2e`, as for every feature.
