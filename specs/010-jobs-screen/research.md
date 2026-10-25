# Research: Jobs Screen & Orchestrator UI Reorganisation (010)

All unknowns from the plan's Technical Context are resolved below. Format per item: Decision · Rationale · Alternatives considered.

## R1 — Operations act on an explicit target orchestrator

- **Decision**: Introduce an `orchTarget` (host id, host display name, orchestrator id, orchestrator name, and its current record) and refactor today's per-orchestrator operations — open a run's prompt, cancel a run, mark a worker free, Enqueue now, add/edit/delete/call actions, add/edit/delete/toggle events — to take a target instead of reading `m.orchDetail`. The Jobs tabs build the target from the highlighted row (or the cursor's group); the remaining detail views build it from `orchDetail`.
- **Rationale**: These operations currently assume "the open orchestrator detail". Jobs needs the same operations on any orchestrator from one screen; a single parameterised implementation keeps behaviour identical (spec FR-010, FR-011, FR-020, FR-021, FR-027) and the existing tests transfer.
- **Alternatives considered**: Temporarily swapping `m.orchDetail` to the target before calling the old code (hidden coupling, breaks the open detail's state); duplicating the forms for Jobs (two copies to keep in sync).

## R2 — Data source: client-side fan-out, no new RPC

- **Decision**: Jobs loads with one `ListOrchestrators` per connected host (summaries already carry each orchestrator's actions and timed events, including `next_fire_at`) and then one `ListRuns` per orchestrator, issued concurrently per host with the existing per-operation timeout. Results are merged client-side.
- **Rationale**: Everything the spec needs is already exposed per host; `ListRuns` returns all retained runs (queued, in flight, and the full retained history, newest first). The change stays TUI-only (spec Constraints; FR-018 keeps retention unchanged).
- **Alternatives considered**: A new daemon RPC `ListAllRuns` (a proto change for no new information and still per-host); loading lazily per tab (Overview needs runs + events together anyway).

## R3 — Live updates

- **Decision**: Reuse the existing per-host `Event.orchestrator_changed` handling (`syncOrchestratorChanged`): when Jobs is the screen (or has been loaded), reload just the changed orchestrator's summary and runs and re-merge. A full reload happens on entering Jobs and on `r`. Hosts connecting/disconnecting trigger a reload of that host's rows.
- **Rationale**: Meets FR-005 without polling; bounded work per event.
- **Alternatives considered**: Periodic polling (wasteful, laggy); reloading everything per event (O(orchestrators) RPCs per change).

## R4 — Overview composition and ordering

- **Decision**: Upcoming = every enabled timed event with a `next_fire_at`, soonest first; Queued = queued runs ordered by received time (oldest first, as dispatch order); In progress = running/awaiting input/awaiting review ordered by start time; History = finished runs merged across orchestrators by `ended_at` (newest first), truncated to 15 (package constant `jobsOverviewHistory = 15`). Every row shows host and orchestrator name.
- **Rationale**: Matches FR-006–FR-009 and the spec's assumptions (one Upcoming entry per event; combined 15 cap).
- **Alternatives considered**: Per-orchestrator 15 cap (rejected by the spec's assumption); listing several future occurrences per event (noise).

## R5 — Open results

- **Decision**: A modal overlay backed by `bubbles/viewport` showing, for a finished run: the action, host/orchestrator, state and end time, then the full `Run.summary` (or "closed by developer: <note>" when `closed_by_developer`), and for failed/cancelled runs the `reason`. Scroll with ↑/↓/PgUp/PgDn, close with `esc`. Text is wrapped to the overlay width; nothing is truncated. Content is passed through the package's existing sanitising for terminal output (strip control sequences) before rendering.
- **Rationale**: `Run.summary` already holds the full report; truncation is purely in today's one-line rendering (FR-012).
- **Alternatives considered**: Opening the run's sandbox terminal (shows the agent session, not the report; not available once the worker is reused); expanding rows inline (breaks list layout).

## R6 — Queue tab: filter and pagination

- **Decision**: Flatten all runs (queued, then in progress, then history newest first). The filter cycles with `f` through "all orchestrators" and each orchestrator (labelled `name @ host`); `F` clears it. Pagination is page-based with a package constant page size (`jobsQueuePageSize = 25`); `]`/`pgdown` next, `[`/`pgup` previous; the header shows "page N of M (K runs)". When the page no longer exists (runs trimmed or filter changed) the page clamps to the last page; when the filtered orchestrator disappears the filter resets to all and says so.
- **Rationale**: FR-013–FR-016 and the spec's edge cases; a fixed page size keeps paging predictable regardless of terminal height.
- **Alternatives considered**: Infinite scroll (no page indicator; the spec asks for pagination); a filter picker modal (more keystrokes for the common "cycle" use).

## R7 — Constants, not configuration

- **Decision**: `jobsOverviewHistory = 15`, `jobsQueuePageSize = 25`, and the existing per-RPC timeout are package constants; no settings or env vars.
- **Rationale**: Constitution VIII and the project's "constants, not env vars" convention (features 005–009).
- **Alternatives considered**: `settings.toml` knobs (not requested).

## R8 — Navigation and entry points

- **Decision**:
  - The top-level cycle becomes Sandboxes → Daemons → Jobs; `screenOrchestrators` is removed and its header label is replaced by "Jobs".
  - Daemons gains an **Orchestrators** option (after MCP gateway and Secrets) opening `screenDaemonOrchs`: that daemon's orchestrators with `n` create, `R` rename, `d` remove, `enter` open; the opened detail (`screenOrchDetail`) has views Workers, Reasoning, Hosts; `esc` returns to the daemon's Orchestrators list, then to Daemons.
  - Entry points that previously landed on the Orchestrators screen are retargeted: the sandbox list's mark-free dialog (opened over an orchestrator detail) and the launch wizard's reasoning rebuild keep using the orchestrator detail, now reached via Daemons › Orchestrators; any notification that pointed at runs opens Jobs › Overview.
- **Rationale**: FR-001, FR-022–FR-025.
- **Alternatives considered**: Keeping an Orchestrators top-level screen with only three views (the ticket says to delete it).

## R9 — Enqueue now outside the Events view

- **Decision**: Overview's Enqueue now calls the same fire path as the Events view (the SWI-4 implementation: per-fire token, model-level in-flight record keyed by orchestrator, repeat window, dedupe-off confirmation when the orchestrator's loaded runs show an open run of the event). Its status messages go to the Jobs status line; the in-flight guard is the model-level record, so it holds across Jobs and the Events tab alike.
- **Rationale**: FR-011 requires identical behaviour; one implementation (R1) guarantees it.
- **Alternatives considered**: A Jobs-specific fire path (behaviour drift).
