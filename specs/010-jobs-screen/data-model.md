# Data Model: Jobs Screen (010)

Client-side view model only — no persisted or wire changes. Source types are the existing protobuf messages (`OrchestratorSummary` → `Orchestrator` {actions, timed_events}, `ListRunsResponse` {queued, in_flight, history}, `Run`, `TimedEvent`).

## orchTarget

The orchestrator an operation acts on (research R1).

| Field | Meaning |
|-------|---------|
| host | host entry id of the daemon hosting it |
| hostName | its display name (shown in rows and headings) |
| id | orchestrator id |
| name | orchestrator name |
| rec | the orchestrator record as last loaded (actions, timed events) |

Identity: `(host, id)`. Display label: `name @ hostName`.

## jobsOrch (one loaded orchestrator)

| Field | Meaning |
|-------|---------|
| target | its `orchTarget` |
| summary | `OrchestratorSummary` as returned by `ListOrchestrators` |
| runs | `ListRunsResponse` (queued, in_flight, history newest first) |
| err | load error for this orchestrator, if its runs could not be read |

## jobsState

| Field | Meaning |
|-------|---------|
| tab | `overview` \| `actions` \| `events` \| `queue` (order fixed: FR-002) |
| orchs | loaded orchestrators, ordered by host display name then orchestrator name (stable group order: FR-019) |
| unreachable | display names of hosts that could not be listed (FR-004) |
| loading | a full load is in flight |
| cursors | one cursor per tab |
| status | Jobs status line (outcomes, refusals) |
| queueFilter | `""` (all) or an `orchTarget` identity |
| queuePage | 0-based page index of the Queue tab |

## Derived rows

**Overview** (in this section order: FR-006):

- `upcoming`: for each enabled `TimedEvent` with `next_fire_at` → `{target, event}`, sorted by `next_fire_at` ascending (FR-007).
- `queued`: all `runs.queued` → `{target, run}`, sorted by `received_at` ascending.
- `inProgress`: all `runs.in_flight` → `{target, run}`, sorted by `started_at` ascending.
- `history`: all `runs.history` → `{target, run}`, sorted by `ended_at` descending, first **15** (`jobsOverviewHistory`, FR-009).

**Queue tab**: `queued` ++ `inProgress` ++ all `history` (no cap: FR-013), filtered by `queueFilter`; page `p` shows items `[p*25, (p+1)*25)` (`jobsQueuePageSize`); `pages = max(1, ceil(n/25))`; `queuePage` clamps to `pages-1` (FR-014, edge case "page beyond the end").

**Actions tab / Events tab**: one group per `jobsOrch` (heading `name @ hostName`), containing its actions / timed events in their existing order; an empty group renders an empty-state line and is still a valid cursor position for "add" (FR-019, FR-021).

## Row actions (by row kind and run state)

| Row | Actions |
|-----|---------|
| upcoming event | Enqueue now (FR-011) |
| queued run | open prompt; cancel |
| in-flight run | open prompt; mark worker free when awaiting review |
| finished run (completed/failed/cancelled) | open prompt; Open results (FR-012) |
| action (Actions tab) | call; edit; delete — add uses the cursor's group |
| event (Events tab) | edit; delete; enable/disable; Enqueue now — add uses the cursor's group |

## State transitions

- Enter Jobs → full load (all connected hosts) → rows derived.
- `Event.orchestrator_changed(host, id)` → reload that orchestrator's summary + runs → re-derive; a removed orchestrator disappears; if it was the queue filter, the filter resets to all with a status note.
- Host connects/disconnects → reload that host's orchestrators; a disconnected host's orchestrators are dropped and the host is listed in `unreachable`.
