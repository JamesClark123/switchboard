# Data Model: Jobs Screen (010)

Client-side view model only — no persisted or wire changes. Source types are the existing protobuf messages (`OrchestratorSummary` → `Orchestrator` {actions, timed_events, workers}, `OrchestratorDetail`, `ListRunsResponse` {queued, in_flight, history}, `Run`, `TimedEvent`).

## orchTarget

The orchestrator an operation acts on (research R1).

| Field | Meaning |
|-------|---------|
| host | host entry id of the daemon hosting it |
| hostName | its display name (shown in rows and headings) |
| id | orchestrator id |
| name | orchestrator name |
| rec | the orchestrator record as last loaded (actions, timed events, workers) |

Identity: `(host, id)`. Display label: `name @ hostName`. Its daemon is resolved with `connectedDaemon(host)` at start and again at submit; when the host isn't connected, the operation is refused, never sent elsewhere (R12, FR-030).

## orchSurface (where an operation is hosted)

One per hosting screen: Jobs and the orchestrator detail (R1).

| Field | Meaning |
|-------|---------|
| screen | the screen it belongs to (`screenJobs` / `screenOrchDetail`); replies carry it and are routed back to it |
| form, formVals, formKind | the open form, if any |
| prompt | run shown in the prompt overlay |
| results | run shown in the Open results overlay (+ its viewport) |
| status | the surface's status line |
| firing, fireToken, lastFireAt, lastFireEv | Enqueue now guard for this surface (R9); the model-level `orchFires` stays shared |

## jobsOrch (one loaded orchestrator)

| Field | Meaning |
|-------|---------|
| target | its `orchTarget` |
| summary | `OrchestratorSummary` (from `ListOrchestrators`, or from `GetOrchestrator` on an event reload) |
| runs | `ListRunsResponse` (queued, in_flight, history newest first) |
| err | load error for this orchestrator, if its runs could not be read (named on every tab: R3) |
| reachable | false while its host is disconnected; rows stay, dimmed, and operations are refused (R3, R12) |
| gen | latest load generation issued; replies with an older generation are dropped (R3) |
| inFlight, dirty | coalescing: one reload in flight; `dirty` asks for one more when it lands (R3) |
| stale | an event arrived while Jobs wasn't visible; reloaded on entering Jobs (R3) |

## jobsState

| Field | Meaning |
|-------|---------|
| tab | `overview` \| `actions` \| `events` \| `queue` (order fixed: FR-002) |
| orchs | loaded orchestrators, ordered by host display name then orchestrator name (stable group order: FR-019) |
| hosts | per host: `loading` (load in flight), `gen` (host load generation), `err` (could not be listed) |
| unreachable | derived: display names of hosts that could not be listed or are disconnected, plus `name @ host (runs)` for orchestrators with `err` (FR-004) |
| rev | data revision, bumped whenever a load is applied; derived rows are cached per `rev` (R4) |
| cursors | one per tab: `{index, key}`, where `key` is the selected item's identity (R11) |
| surface | the Jobs `orchSurface` (R1) |
| queueFilter | `""` (all) or an `orchTarget` identity |
| queuePage | 0-based page index of the Queue tab |

## Derived rows

**Overview** (in this section order: FR-006):

- `upcoming`: for each enabled `TimedEvent` with `next_fire_at` → `{target, event, willSkip}`, sorted by `next_fire_at` ascending (FR-007). `willSkip` = dedupe on (`!dedupe_disabled`) and the orchestrator's loaded runs contain a queued or in-flight run with `origin == event.id`.
- `queued`: all `runs.queued` → `{target, run}`, sorted by `received_at` ascending.
- `inProgress`: all `runs.in_flight` → `{target, run, worker}`, sorted by `started_at` ascending; `worker` = the sandbox display name from `rec.workers` (fallback: short sandbox id).
- `history`: all `runs.history` → `{target, run}`, sorted by `ended_at` descending, first **15** (`jobsOverviewHistory`, FR-009).

**Queue tab**: `queued` ++ `inProgress` ++ all `history` (no cap: FR-013), filtered by `queueFilter`; page `p` shows items `[p*25, (p+1)*25)` (`jobsQueuePageSize`); `pages = max(1, ceil(n/25))`; `queuePage` clamps to `pages-1` (FR-014, edge case "page beyond the end"); after a reload the page follows the selected run (R11).

**Actions tab / Events tab**: one group per `jobsOrch` (heading `name @ hostName`), containing its actions / timed events in their existing order; an empty group renders an empty-state line and is still a valid cursor position for "add" (FR-019, FR-021).

All agent- or remote-supplied text in these rows (summary, reason, origin label) is rendered through `sanitizeLine`, and overlays through `sanitizeTerminalText` (R10, FR-029).

## Item identity (cursor keys, R11)

| Item | Key |
|------|-----|
| run | `(host, orchestrator id, run id)` |
| upcoming / event row | `(host, orchestrator id, event id)` |
| action row | `(host, orchestrator id, action name)` |
| group heading / empty line | `(host, orchestrator id)` |

After every re-derive, each cursor moves to the row with its key; only if that row is gone does it fall back to its old index, clamped.

## Row actions (by row kind and run state)

| Row | Actions |
|-----|---------|
| upcoming event | Enqueue now (FR-011) |
| queued run | open prompt; cancel |
| in-flight run | open prompt; mark worker free when awaiting review |
| finished run (completed/failed/cancelled) | open prompt; Open results (FR-012) |
| action (Actions tab) | call; edit; delete — add uses the cursor's group |
| event (Events tab) | edit; delete; enable/disable; Enqueue now — add uses the cursor's group |

Every action on a row of an unreachable orchestrator is refused with `<host> is not connected` (FR-030).

## State transitions

- Enter Jobs → first time: full load (every connected host, one command per host, results applied as each arrives); later: reload `stale` orchestrators and hosts not yet loaded.
- `Event.orchestrator_changed(host, id)` while Jobs is visible → reload that orchestrator (`GetOrchestrator` + `ListRuns`, new `gen`, coalesced) → re-derive; a removed orchestrator disappears; if it was the queue filter, the filter resets to all with a status note. While Jobs isn't visible → mark it `stale`.
- Host connects or reconnects (including before the first load completes) → load that host's orchestrators and merge; it leaves `unreachable`.
- Host disconnects → its orchestrators stay as last known with `reachable = false`, dimmed, listed in `unreachable`; operations on them are refused. Dropped only when the host is removed from the host list.
- Reply with `gen` older than the latest issued for that orchestrator (or host) → dropped.
