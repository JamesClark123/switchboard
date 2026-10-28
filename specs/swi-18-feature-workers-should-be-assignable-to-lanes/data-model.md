# Data Model: Worker Lanes (SWI-18)

Phase 1 of [plan.md](./plan.md). The proto text is in [contracts/proto-delta.md](./contracts/proto-delta.md); this
file describes each entity, where it lives, its rules and its state changes. "Record" means the persisted
`pb.Orchestrator` in the `orchestrators` bolt bucket.

## Entities

### Lane (`pb.Lane`, on the record, `Orchestrator.lanes` = 11)

| Field | Rule |
|-------|------|
| `id` | `ln-` + 12 hex; minted at create; never changes; never reused on this orchestrator |
| `name` | `^[a-z0-9][a-z0-9-]*$`, 1–64 chars, unique per orchestrator, not `open` (FR-303) |
| `description` | trimmed; single line, no control characters; ≤ 200 runes; optional (FR-302) |
| `created_at` | set at create |

- At most 32 per orchestrator (FR-302). Deleted with the orchestrator.
- **Rename / describe** changes only `name`/`description`; every reference is by `id`, so routing is unchanged
  (FR-304).
- **Delete** (FR-305), in one `UpdateOrchestratorRuns` transaction: removed from every `Worker.lane_ids`,
  `Action.lane_ids`, queued `Run.kept_lanes.lane_ids` and `AttachTicket.lane_ids`. An action left with no lanes gets
  `lane_mode = UNSPECIFIED`. A kept setting left with no lanes means un-laned. Guarded by FR-308a (R10).

### Roster entry (`pb.Worker`, `Orchestrator.workers` = 10, extended)

| Field | New | Rule |
|-------|-----|------|
| `sandbox_id`, `host_id`, `attached_at`, `reasoning` | – | as today |
| `lane_ids` | 5 | ids of existing lanes; empty for reasoning; deduplicated |
| `lanes_pending` | 6 | set only by adoption through an invalid ticket; cleared by any membership save that covers this worker |
| `revision` | 7 | sandbox revision this entry was last written from (FR-308c); rewritten only when the roster changes, not on every status event |
| `written_seq` | 8 | `roster_seq` at the last write (listing-absence rule) |

Derived, never stored:
- **open-lane worker**: `!reasoning && !lanes_pending && len(lane_ids) == 0`.
- **detaching**: membership in the in-memory `rt.detaching` set (cleared by restart, FR-322).
- **live roster**: every entry (entries leave only on evidence, R4). **unreachable**: no cached record, or host link
  down. **stopped**: cached record not RUNNING.
- **free** (dispatch): live, not reasoning, not lanes pending, not detaching, host up, record RUNNING, agent IDLE,
  `controller.run == nil`.

State changes of one entry:

```text
           adopt (no ticket)            membership save
  (none) ───────────────────▶ open ◀──────────────────▶ in lanes
     │      adopt (valid ticket) ─────────────────────────▲
     │      adopt (bad/expired ticket)                    │ membership save (clears pending)
     └────────────────────────▶ lanes pending ────────────┘ (or "open lane" choice → open)

  any state ── guard+mark ──▶ detaching ── unlink ok ──▶ (dropped, mark at reply revision)
                                   └──── unlink failed / restart ──▶ previous state
  any state ── fresh record names other/no controller, removal, listing absence ──▶ (dropped)
```

### High-water mark (`pb.HighWaterMark`, `Orchestrator.high_water_marks` = 13, `map<string, HighWaterMark>`)

| Field | Rule |
|-------|------|
| `revision` | max(revision of the record/reply that caused the drop, the entry's revision) |
| `removed` | true after a removal event, a DESTROYING record or a listing absence; terminal |
| `set_seq` | `roster_seq` when set |
| `removed_at` | when `removed` became true |
| `host_id` | the host the sandbox lived on; a listing prunes only its own host's marks |

- A record for a sandbox not on the roster adopts only if `revision > mark.revision` and `!mark.removed`. A fresh
  adoption deletes the mark.
- **Pruned** when a listing of its host (`host_id`) with `requested_seq ≥ set_seq` lacks the sandbox, or 24 h after
  `removed_at` (FR-308c).
- Not sent to clients: summaries clear this map before returning.

### Roster clock (`Orchestrator.roster_seq` = 15)

A `uint64` incremented by every roster write. Reconcile reads it *before* listing (`requested_seq`).

### Sandbox revision (`Sandbox.revision` = 24, owning daemon)

Stamped by `registry.Put`/`registry.Update` inside the write transaction: `stored + 1`, or 1 for a new record.
Callers cannot set it. Revision 0 means "host predates lanes": always fresh, leaves no mark (R16).

### Action lane setting (`Action.lane_ids` = 10, `Action.lane_mode` = 11)

| Field | Rule |
|-------|------|
| `lane_ids` | ids of existing lanes (save refused with `NotFound` otherwise, FR-316a); `prompt` only |
| `lane_mode` | `EXCLUSIVE` or `INCLUSIVE` when `lane_ids` is non-empty; `UNSPECIFIED` when empty |
| `worker_tag` (6) | **deprecated**: read on load only, to drop and record it; never written; never reused |

`PutActionRequest.sets_lanes` must be true (FR-318a). `reason` with lanes is refused (FR-316).

### Kept lane setting (`Run.kept_lanes` = 31, `pb.KeptLaneSetting{lane_ids, mode}`)

- Written in the `DeleteAction` transaction onto every QUEUED run of the deleted action (FR-314b).
- Wins over a later action of the same name. Stripped of a deleted lane by lane delete. Leaves with the run (L8).

**Effective lane setting** of a QUEUED `prompt` run:
1. `kept_lanes` if set (empty `lane_ids` = un-laned);
2. else the current action's setting, if the action exists;
3. else **missing** → dispatch fails the run with "its action was deleted".

### Waiting reason (`Run.waiting_reason` = 32)

Derived on `ListRuns`/`GetRun` only; `putRun` clears it before writing (FR-315). Separate from `Run.reason`. Rules
in [research R9](./research.md#r9-waiting-reasons-derived-at-read-time-fr-315-sc-006).

### Attach ticket (`pb.AttachTicket`, `Orchestrator.attach_tickets` = 12)

| Field | Rule |
|-------|------|
| `id` | 16 hex; carried as `SandboxController.attach_ticket` (9) |
| `lane_ids` | lane ids; lane delete strips them |
| `host_id` | target host (launch and attach) |
| `sandbox_id` | set for attach only |
| `issued_at` | expiry = `issued_at + 24h` |

Lifecycle: issued (bundle or attach) → consumed by the first adoption or attach commit (same write) | deleted on a
definite attach refusal | swept after expiry. Presenting an unknown, expired or mismatched id → `lanes_pending`.

### Dropped worker tags (`Orchestrator.dropped_worker_tags` = 14, `pb.DroppedWorkerTag{action, tag, dropped_at}`)

Appended on load whenever an action has a `worker_tag` (FR-318b). Cleared by `DismissDroppedWorkerTags`.

### Display copy on the worker's host (`SandboxController.lane_names` = 7, `lanes_pending` = 8)

Written by `UpdateWorker{lanes}` only. Names breaking the lane rule are dropped and logged. Never read by any
orchestrator (FR-309b).

### In-memory only (orchestrator's daemon)

| State | Owner | Notes |
|-------|-------|-------|
| `rt.sbs` cached sandbox records | `rt.mu` | refreshed when `revision > cached.revision`; a record older than the cache never changes the roster (contracts/dispatch.md) |
| `rt.detaching` | `rt.mu` | cleared by restart |
| `lanePusher` dirty set | its own mutex | per-worker display pushes, coalesced |
| `briefer` pending flag | its own mutex | briefing pushes, coalesced |
| per-pass dispatch state (reservations, claimed, out) | the pass | discarded at the end of the pass |

## Persistence summary (FR-322)

Persisted on the record: lanes, worker `lane_ids`/`lanes_pending`/`revision`/`written_seq`, `roster_seq`,
high-water marks, action lane settings, attach tickets, dropped worker tags. Persisted on runs: `kept_lanes`. Not
persisted: detaching, waiting reasons, the sandbox-record cache.

## Validation messages (shown verbatim by the TUI)

| Case | Message |
|------|---------|
| bad lane name | `lane name must be 1–64 lowercase letters, digits or hyphens, starting with a letter or digit` |
| duplicate | `a lane named "x" already exists` |
| reserved | `"open" is reserved: it names workers in no lane` |
| description | `description must be one line of at most 200 characters` |
| cap | `this orchestrator already has 32 lanes` |
| unknown lane on save | `lane "<id or last-known name>" no longer exists` |
| stale worker picker | `<worker>'s lanes changed since the picker opened — reloaded` |
| stale lane editor | `lane <name>'s members changed since the editor opened — reloaded` |
| old client | `this client predates lanes — update the client` |
| reason with lanes | `reason actions run on the reasoning sandbox and take no lanes` |
| missing mode | `choose exclusive or inclusive for an action with lanes` |
| run lost its action | `its action was deleted` |
