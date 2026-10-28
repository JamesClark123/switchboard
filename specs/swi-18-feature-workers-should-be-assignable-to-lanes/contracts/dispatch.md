# Contract: dispatch pass, roster freshness and agent-facing text (SWI-18)

Internal contracts of the orchestrator's daemon that tests pin. Research references: R4 (freshness), R8 (dispatch),
R9 (waiting reasons), R13 (briefing).

## Dispatch pass (FR-311–FR-314, L6)

```text
pass():
  gen0 := rt.recGen                                                 // under rt.mu, before the snapshot
  rec, runs := record + queued runs (receipt order), one bolt View  // one consistent snapshot (ViewOrchestratorRuns)
  free   := live roster workers that are free (data-model)
  index  := byLane[lane] ordered (lane count, roster order), by bucketing, no sort; open (roster order); mask per worker
  for r in runs where r is prompt and effective(r).mode == EXCLUSIVE:        // Reserve
      w := over the first unreserved worker of each of r.lanes' lists (lists ordered by lane count,
           then roster order), the one with fewest lanes outside r (popcount(w.mask &^ r.mask)), then roster order
      if w: reserve(w, r)
  for r in runs:                                                              // Deliver
      if r is reason: today's path; continue
      if effective(r) is missing: failIfStillMissing(r); continue   // re-read inside updateRuns before failing
      for w in candidates(r):          // lazily, skipping claimed/out workers
          switch recheckAndClaim(w, r, gen0):   // under rt.mu; adds w to rt.delivering on ok
            sandbox/roster fails: out(w); release(w); continue
            lane fails:           continue            // w stays for later runs, which re-check it
          switch deliver(r, w):                 // no lock held; w leaves rt.delivering when it returns
            accepted:      claim(w); release(r's other reservation); next run
            precondition:  out(w); release(w); continue
            other error:   fail(r, …); out(w); next run
      release(r's reservation)         // no candidate accepted
```

| Run | candidates(r) in order |
|-----|------------------------|
| exclusive | own reservation → unreserved free in r.lanes → free in r.lanes reserved by a later run (steal) |
| inclusive | unreserved free in r.lanes → free open-lane → free in r.lanes reserved by a later run (steal) |
| un-laned | free open-lane |

**Re-check and claim** (`recheckAndClaim`, under `rt.mu`, research R8). The sandbox record must still be free
(today's check). If `rt.recGen != gen0`, a record write committed since the snapshot, so the worker is re-validated
against the current `rt.rec`: still on the roster, not reasoning, not *lanes pending*, not detaching, and its current
lane mask allowed by the run's current effective setting. On success the worker joins `rt.delivering` in the same
critical section; `markDetaching` refuses a worker in `rt.delivering` and the claim refuses one in `rt.detaching`.
A lane write that commits after the claim is ordered after the delivery (FR-314: decided at dispatch).

**Guarantees tested** (`TestDispatchProperties`, 100 seeds × 100 runs, random refusals):
1. Every delivery is allowed by FR-311–FR-313.
2. After a pass, no queued run has an unclaimed, free, accepting worker it could take.
3. No run waits for a run later in the queue.
4. Cost: the planner touches each (worker, lane) membership at most twice and each run's lanes once (counted by an
   instrumented test hook).
5. Mid-pass lane changes (`TestDispatchLaneRace`, `-race`): lane writes, action saves, lane deletes and detaches
   applied concurrently with a pass (a blocking fake `deliver` holds it mid-pass) never let a delivery through that
   the record committed at its claim forbids, and never detach a worker while a run is being delivered to it.

**Triggers** (`triggerDispatch`): today's, plus every lane create/rename/delete, membership save, action save or
delete, ticket consumption, adoption or drop (FR-314a, L2).

## Roster freshness (FR-308c, L2)

`applyRecords(rec, input, now)` is the only code that applies **sandbox records** (listings, events, attach/detach
replies, reasoning launch replies, removals) to `rec.workers`, `rec.reasoning_sandbox_id` and
`rec.high_water_marks`. Lane writes (`SetWorkerLanes`, `SetLaneMembers`, `DeleteLane`) change only the lane fields
of existing entries.

**Fresh** means `record.revision > entry.revision` and `record.revision ≥ cached.revision` for a roster sandbox, and
`record.revision > mark.revision` (mark not removed) and `≥ cached.revision` for a non-roster one. The cache may
already hold this very record (a listing and an event can carry the same revision), hence `≥`; a record older than
the cache is stale even when it is newer than the entry, because entries are not rewritten on status-only events.
Revision 0 (host predates lanes) refreshes the cache and leaves no mark; as an **event** it never adopts or drops
(listings and this orchestrator's own replies still do, as today). Records from one host's event stream are handled
in order by that link's reader; a roster-changing record is re-checked inside `rt.update`.

**Removals come first.** A removal event or a DESTROYING record is classified by state before the freshness check,
whatever its revision: the owning daemon emits a destroy as a bare `{id, DESTROYING}` record after deleting it, so
it has revision 0 and no host. Its host comes from the roster entry or the cached record; the cache entry is evicted
after the rules have read it, never replaced by the bare record.

**Cache input.** `input.cache` is a snapshot of `rt.sbs` for the input's ids, taken under `rt.mu` inside the
`rt.update` callback; `applyRecords` reads no shared state.

**Replies use the newer of the reply and the cache.** `attachReply`, `detachReply` and `launchReply` come from this
orchestrator's own call. Status events from the same write burst (controller applied, RUNNING, agent STARTING/IDLE)
may already have put a newer record in the cache. When the cached record is newer than the reply, `applyRecords`
applies the **cached record** in the reply's place, under the normal entry and mark rules. The reply's source
permissions (a `launchReply` may adopt as reasoning; an attach ticket) still apply to that record. A launch therefore
still adopts from its newer cached STARTING/IDLE record. An attach followed at once by a direct detach (cached record
names no controller) is never adopted, and a detach reply older than a cached re-attach does not drop the worker.

| Input | Roster sandbox | Non-roster sandbox |
|-------|----------------|--------------------|
| fresh, names this orchestrator, nothing else changes | **cache only, no record write** | – |
| fresh, names another/no controller | drop; mark `{revision: max(rev, entry.revision), host_id}` | cache only |
| fresh, names this orchestrator (worker) | – | adopt per ticket rules; delete mark |
| fresh, names this orchestrator as reasoning | keep | adopt as reasoning only if its id is `reasoning_sandbox_id`, or the input is a listing and `reasoning_sandbox_id` is empty (today's recovery); never from an event otherwise |
| stale or repeated | cache only | cache only |
| revision-0 event (host predates lanes) | cache only | cache only |
| revision-0 record, no ticket id, adoptable (listing or reply) | – | sandbox-bound ticket → its lanes; host-bound launch ticket → `lanes_pending`; neither → open lane |
| removal event / DESTROYING record, any revision | drop; mark `removed`; evict cache | mark `removed` only if the id already has a mark or is cached as naming this orchestrator; otherwise ignored, as today; evict cache |
| listing absence, entry `written_seq ≤ requested_seq` | drop; mark `removed` | – |
| listing absence, entry written after the request | keep | – |
| listing of host H, sandbox absent, mark `host_id == H` and `set_seq ≤ requested_seq` | – | prune mark |
| reasoning launch reply (`launchReply`) | – | adopt as reasoning at the reply's revision; set `reasoning_sandbox_id` |
| reasoning destroy (`destroyReasoning`) | drop; mark `removed`; evict cache | – |

A write happens only when an entry is adopted or dropped, a ticket is consumed, a mark changes or
`reasoning_sandbox_id` changes. Status-only events refresh `rt.sbs` and never take `recMu`, so status folding never
waits on a roster write.

**Property test** (`TestRosterFreshness`): random interleavings of attach replies, detach replies, events (late,
duplicated, reordered) and listings (requested before/after changes) never re-adopt a detached or removed sandbox
from an older record, and never drop an entry written after the listing was requested. Its generator includes
revision-0 hosts and bare DESTROYING records, and it models `input.cache` explicitly.

## Agent-facing text (FR-321)

Briefing line per action, after its description:

| Setting | Text |
|---------|------|
| exclusive `a, b` | `Runs only on workers in lanes a, b.` |
| inclusive `a` | `Runs on workers in lane a, or on any open-lane worker.` |
| none | `Runs only on open-lane workers (workers in no lane).` |

`orchestrator_list_actions` JSON per action: `"lanes": ["a","b"], "lane_mode": "exclusive"` (both omitted when
un-laned); `worker_tag` is gone. Lane descriptions are never sent to agents. `orchestrator_check_run` includes
`"waiting_reason"` when set.
