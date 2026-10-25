# Research: Jobs Screen & Orchestrator UI Reorganisation (010)

All unknowns from the plan's Technical Context are resolved below. Format per item: Decision · Rationale · Alternatives considered.

## R1 — Operations act on an explicit target orchestrator, hosted by the screen that started them

- **Decision**: Introduce an `orchTarget` (host id, host display name, orchestrator id, orchestrator name, and its current record) and refactor today's per-orchestrator operations — open a run's prompt, cancel a run, mark a worker free, Enqueue now, add/edit/delete/call actions, add/edit/delete/toggle events — to take a target instead of reading `m.orchDetail`. The Jobs tabs build the target from the highlighted row (or the cursor's group); the remaining detail views build it from `orchDetail`.
  - **Hosting is refactored too, not only the operations.** Today the code that *hosts* an operation is hard-wired to `screenOrchDetail`:
    - the forms, the prompt overlay (`orchDetail.prompt`, drawn only by `viewOrchDetail`) and the status line;
    - `enterConfirm(returnTo: screenOrchDetail)`, plus `View()`'s confirm background and `topLevel()`;
    - `applyOrchDone`, `applyOrchFormErr`, `applyRunPrompt` and `forwardOrchestrators`;
    - the Enqueue now guard fields `firing`, `fireToken`, `lastFireAt` and `lastFireEv`.
  - These move into an **`orchSurface`** (form + form values/kind, prompt overlay, results overlay, status line, fire guard: firing / fireToken / lastFireAt / lastFireEv). Jobs owns one and the orchestrator detail owns one.
  - Every operation records the **originating screen** in its messages (`orchDoneMsg`, `orchFormErrMsg`, `runPromptMsg`). Completion, refusals, the prompt and form closing are routed to that surface, whatever screen is showing when the reply lands.
  - The confirm gate returns to the originating screen; `View()` and `topLevel()` know `screenJobs` and `screenDaemonOrchs`.
  - The model-level `orchFires` record (keyed by host + orchestrator id) still holds across surfaces.
  - The existing `markFreeFromList` path (a dialog hosted by the detail surface, returning to the sandbox list) is kept.
- **Rationale**: These operations currently assume "the open orchestrator detail". Jobs needs the same operations on any orchestrator from one screen. A single parameterised implementation keeps behaviour identical (spec FR-010, FR-011, FR-020, FR-021, FR-027) and the existing tests transfer. Without per-surface routing, the operations would misbehave from Jobs:
  - cancelling would return to the detail screen;
  - a refusal would be shown nowhere;
  - a fire reply would be treated as stale.
- **Alternatives considered**:
  - Temporarily swapping `m.orchDetail` to the target before calling the old code: hidden coupling, and it breaks the open detail's state.
  - Duplicating the forms for Jobs: two copies to keep in sync.

## R2 — Data source: client-side fan-out, no new RPC, parallel per host

- **Decision**: Jobs loads with one `ListOrchestrators` per connected host, then one `ListRuns` per orchestrator. `ListOrchestrators` summaries already carry each orchestrator's actions and timed events, including `next_fire_at`.
  - **Each host is loaded by its own command** (hosts in parallel), each with its own `ctxTimeout()`.
  - Within a host, the `ListRuns` calls run in parallel, bounded at 4 at a time (`jobsRunsParallel`).
  - **Each host's result is applied as it arrives**, so data from fast daemons is shown and navigable while a slower one is still loading (SC-007, spec edge case "Slow daemon"). A per-host "loading" marker is shown until it answers.
  - A full load is guarded by an in-flight flag per host: entering Jobs, or pressing `r` while that host's load is running, does not stack a second one.
  - This is **not** today's `orchLoadCmd`, which walks hosts sequentially under one shared 60 s context. A hung host there delays every host and can use up the deadline of the hosts after it.
- **Payload note**: `ListRuns` returns stored runs whole, including each run's composed `prompt` and a summary of up to 2000 chars, for up to about 200 history runs per orchestrator. That is acceptable under the no-proto constraint, given R3's "reload one orchestrator, only while visible" rule. The gRPC default 4 MB receive limit is a known ceiling for an orchestrator with very large prompts; it is recorded here and not changed by this feature.
- **Rationale**: Everything the spec needs is already exposed per host; `ListRuns` returns all retained runs (queued, in flight, and the full retained history, newest first). The change stays TUI-only (spec Constraints; FR-018 keeps retention unchanged).
- **Alternatives considered**:
  - A new daemon RPC `ListAllRuns`: a proto change for no new information, and still per host.
  - A runs RPC without prompts: also a proto change; deferred.
  - Loading lazily per tab: the Overview needs runs and events together anyway.

## R3 — Live updates: one orchestrator at a time, ordered, coalesced, only while visible

- **Decision**:
  - **Event-driven reload of one orchestrator.** `Event.orchestrator_changed(host, id)` reloads just that orchestrator: `GetOrchestrator` for its record (actions, timed events) and `ListRuns` for its runs. Then the merged rows are re-derived (FR-005: no other orchestrator is re-read).
  - **Ordered replies.** Each orchestrator has a load **generation**. Every reload request increments it, and the reply carries the generation it was issued with. A reply older than the latest issued is dropped, so an older `ListRuns` can never overwrite a newer one (FR-005: the most recent state wins).
  - **Host loads share the per-orchestrator generations.** A full host load carries a per-host generation for the orchestrator *list*. When it issues an orchestrator's `ListRuns`, it also bumps that orchestrator's generation and records the value it issued. Each orchestrator's part of the host reply is applied only if that orchestrator's generation is still the recorded one. A slow host load that lands after a newer event reload of one of its orchestrators therefore keeps the newer state for that orchestrator, and still applies the rest.
  - **Coalescing.** At most one reload per orchestrator is in flight; a host load's `ListRuns` for an orchestrator counts as that orchestrator's in-flight reload. Events that arrive meanwhile set a `dirty` flag, and one more reload is issued when the current one lands. A burst such as queued → running → awaiting review, plus a scheduler tick, costs at most two reloads.
  - **Only while visible.** Reloads run only while Jobs is the visible screen, including a form, confirm or overlay opened from Jobs. On other screens an event only marks the orchestrator `stale`. Entering Jobs reloads stale orchestrators (and does a full load the first time). Nothing is fetched in the background for the rest of the session.
  - **Removed orchestrators.** An event reload whose `GetOrchestrator` or `ListRuns` returns **NotFound** removes that `jobsOrch` from every tab. If it was the Queue filter, the filter resets to all with a note. Any other error is recorded as the orchestrator's `err`. A host load drops the orchestrators its `ListOrchestrators` no longer returns.
  - **Host connects or reconnects**, including during the first load: that host's orchestrators are loaded and merged, and it leaves the "could not read" line (FR-004, US1 scenario 6). Host snapshots received before the first load completes are not ignored; they mark that host for loading.
  - **Host connection lost or failing** (`HostConnecting`, or `HostDisconnected` with a connection error): its orchestrators are **kept as last known, marked unreachable**, as today's `orchLoadCmd` does. The host is named on the "could not read" line, its rows are dimmed, and operations on them are refused with a clear message (R12). A transient drop therefore does not make in-flight runs vanish or reset the Queue filter.
  - **Host the developer disconnected or never connected** (`HostDisconnected` with no error, or removed from the host list): its rows are removed and it is **not** listed as unreadable (FR-004). A Queue filter on one of its orchestrators resets to all with a note.
  - **Per-orchestrator load errors** (its `ListRuns` failed) are named on the "could not read" line on **every** tab, as `name @ host (runs)`, not only as a heading note on the Events tab.
- **Rationale**: Meets FR-004 and FR-005 without polling, with bounded work per event and no stale overwrites. It follows the spec's edge case that entries of a daemon that disconnects meanwhile stay and refuse operations.
- **Alternatives considered**:
  - Periodic polling: wasteful and laggy.
  - Reloading everything per event: O(orchestrators) RPCs per change.
  - Background reloads after the first visit: unbounded traffic while Jobs isn't shown.

## R4 — Overview composition and ordering

- **Decision**:
  - **Upcoming**: every enabled timed event with a `next_fire_at`, soonest first. When the event's dedupe is on and its orchestrator's loaded runs show an open (queued or in-flight) run of that event, the row says `next firing skipped — run open` (FR-007, US2 scenario 3). This is the same check as `openRunOfEvent`, using `Run.origin == event id`.
  - **Queued**: queued runs ordered by received time (oldest first, as dispatch order).
  - **In progress**: running, awaiting input and awaiting review runs, ordered by start time. The row names the worker by its sandbox display name. It is resolved from the TUI's aggregated sandbox list by (`run.sandbox_host_id` mapped through `entryForDaemonHost`, `run.sandbox_id`), falling back to the short sandbox id. `Worker` in the orchestrator record has no display name (only `WorkerView` from `GetOrchestrator` does), so this lookup works the same after a host load and after an event reload, with no RPC.
  - **History**: finished runs merged across orchestrators by `ended_at` (newest first), truncated to 15 (package constant `jobsOverviewHistory = 15`).
  - Every row shows host and orchestrator name. Group and filter order is host display name, then orchestrator name, ties broken by host entry id (FR-019, FR-015).
  - **Row layout**: columns are padded **and truncated** to fixed display widths (`lipgloss.Width` / `ansi.Truncate`, never bytes), so long names cannot push columns out of line. The History result snippet takes the remaining width.
  - **Caching**: derived rows are computed once per data revision (bumped on every applied load), not on every frame.
- **Rationale**: Matches FR-006–FR-009 and the spec's assumptions (one Upcoming entry per event; combined 15 cap).
- **Alternatives considered**:
  - Per-orchestrator 15 cap: rejected by the spec's assumption.
  - Listing several future occurrences per event: noise.

## R5 — Open results

- **Decision**:
  - **Content**: a modal overlay backed by `bubbles/viewport` showing, for a finished run:
    - the action, host/orchestrator, state and end time;
    - then the full `Run.summary`, or "closed by developer: <note>" when `closed_by_developer`;
    - for failed or cancelled runs, the `reason`.
  - **Behaviour**: scroll with ↑/↓/PgUp/PgDn; close with `esc`. Text is wrapped to the overlay width and nothing is truncated. All text passes through the R10 sanitiser before wrapping.
  - **Sizing**: the viewport's width and height are the modal's inner size **minus `modalStyle`'s horizontal and vertical padding and border**. Otherwise wrapped lines wrap a second time, and the drawn height no longer matches the scroll maths. The overlay is resized on every `tea.WindowSizeMsg`, and the modal never exceeds the terminal height: the scroll indicator and `esc close` hint always stay visible.
- **Rationale**: `Run.summary` already holds the full report; truncation is purely in today's one-line rendering (FR-012).
- **Alternatives considered**:
  - Opening the run's sandbox terminal: it shows the agent session, not the report, and isn't available once the worker is reused.
  - Expanding rows inline: breaks the list layout.

## R6 — Queue tab: filter and pagination

- **Decision**:
  - **Rows**: flatten all runs (queued, then in progress, then history newest first).
  - **Filter**: `f` cycles through "all orchestrators" and each orchestrator (labelled `name @ host`), in group order (FR-015); `F` clears it.
  - **Pagination**: page-based with a package constant page size (`jobsQueuePageSize = 25`). `]`/`pgdown` goes to the next page and `[`/`pgup` to the previous; the header shows "page N of M (K runs)".
  - **Edge cases**: when the page no longer exists (runs trimmed or filter changed), the page clamps to the last page. When the filtered orchestrator is **removed**, the filter resets to all and says so. An orchestrator that is only unreachable keeps the filter (R3).
  - **Cursor**: identity-based (R11). After a reload the cursor moves to the page that now holds the selected run.
- **Rationale**: FR-013–FR-016 and the spec's edge cases. A fixed page size keeps paging predictable regardless of terminal height.
- **Alternatives considered**:
  - Infinite scroll: no page indicator, and the spec asks for pagination.
  - A filter picker modal: more keystrokes for the common "cycle" use.

## R7 — Constants, not configuration

- **Decision**: `jobsOverviewHistory = 15`, `jobsQueuePageSize = 25`, `jobsRunsParallel = 4` and the existing per-RPC timeout are package constants; no settings or env vars.
- **Rationale**: Constitution VIII and the project's "constants, not env vars" convention (features 005–009).
- **Alternatives considered**: `settings.toml` knobs (not requested).

## R8 — Navigation and entry points

- **Decision**:
  - **Top-level cycle.** It becomes Sandboxes → Daemons → Jobs, and `screenOrchestrators` is removed. Its header label is replaced by "Jobs".
  - **The cycle now wraps** (shift+→ on Jobs reaches Sandboxes, shift+← on Sandboxes reaches Jobs). This deliberately changes today's no-wrap behaviour: with three screens, wrapping makes Jobs one key away from Sandboxes. `screenNextLabel` / `screenPrevLabel` name the wrapped neighbour at both ends.
  - **`esc` and `q` keep today's top-level meaning on Jobs.** `esc` goes back to Sandboxes and `q` / `ctrl+c` quits, as on every top-level screen today, including the Orchestrators screen being replaced.
  - **Daemons gains an Orchestrators option.** It sits after MCP gateway and Secrets and is offered only for a connected daemon. It opens `screenDaemonOrchs`: that daemon's orchestrators, with `n` create, `R` rename, `d` remove and `enter` open.
    - The create form's machine is **fixed to that daemon**; there is no machine picker on this path.
    - `shift+←/→` and `,` (settings) work there as on the old Orchestrators screen.
    - `esc` goes back to Daemons and `q` quits.
    - If the daemon disconnects while the screen is open, it returns to Daemons with a status note, as the MCP view already does (`applyHosts`).
  - **Orchestrator detail.** The opened detail (`screenOrchDetail`) has the views Workers, Reasoning and Hosts. `esc` returns to **where the detail was opened from**:
    - the daemon's Orchestrators list when opened from it;
    - the launch wizard's origin (the sandbox list) when reached from its reasoning flow;
    - the sandbox list for the mark-free dialog (`markFreeFromList`, unchanged).
  - **Retargeted entry points.** The sandbox list's mark-free dialog and the launch wizard's reasoning rebuild keep using the orchestrator detail. Any notification that pointed at runs opens Jobs › Overview.
- **Rationale**: FR-001, FR-022–FR-025. Keeping `q` = quit avoids a key that means "quit" on two top-level screens and "back" on the third.
- **Alternatives considered**:
  - Keeping an Orchestrators top-level screen with only three views: the ticket says to delete it.
  - `q` = leave Jobs: inconsistent with the other screens.
  - No wrap: Jobs would be two key presses from Sandboxes.

## R9 — Enqueue now outside the Events view

- **Decision**: The Overview's Enqueue now calls the same fire path as the Events view: the SWI-4 implementation, with a per-fire token, a model-level in-flight record keyed by host + orchestrator, the repeat window and the dedupe-off confirmation when the orchestrator's loaded runs show an open run of the event.
  - The per-view guard state (`firing`, `fireToken`, `lastFireAt`, `lastFireEv`) lives on the **originating surface** (R1). Whether a reply is "current" is decided against that surface's token, not against `orchDetail`'s.
  - Status messages go to that surface's status line. A failure that arrives while that surface has a form open goes to the footer, named with the orchestrator, so it isn't read as a refusal of the form.
  - The model-level record holds across Jobs and the detail alike.
- **Rationale**: FR-011 requires identical behaviour; one implementation (R1) guarantees it.
- **Alternatives considered**: A Jobs-specific fire path (behaviour drift).

## R10 — Sanitising untrusted text (FR-029, SC-008)

- **Decision**:
  - **A new sanitiser.** Nothing in `internal/ui` strips control or escape sequences today; `runLine` and the prompt overlay render raw text, and the daemon only bounds a summary's length. A new `sanitizeTerminalText(s string) string` is added in `UI/sanitize.go`. It removes:
    - ESC-introduced sequences: CSI (`ESC [` … final byte), OSC (`ESC ]` … BEL or ST), DCS, SOS, PM and APC (`ESC P/X/^/_` … ST), and two-character escapes;
    - every other C0 control except `\n` and `\t` (backspace, BEL and so on). `\r\n` and a lone `\r` become `\n`, so line breaks are kept (FR-029);
    - DEL and C1 controls (U+0080–U+009F, including 8-bit CSI/OSC);
    - Unicode bidirectional and invisible formatting characters: U+061C, U+200B–U+200F, U+202A–U+202E, U+2060–U+2064, U+2066–U+2069 and U+FEFF. They could reorder or hide text, or make it appear in another column;
    - invalid UTF-8, replaced by U+FFFD.

    It then **expands `\t` to spaces up to the next multiple of 4 columns**. The tab is kept visually, as FR-029 requires, but measured correctly: `ansi.StringWidth`, `lipgloss.Width` and `ansi.Wrap` count a raw tab as zero columns, while a terminal draws up to 8. Raw tabs would make wrapped overlay lines run past the modal border.
  - **A one-line variant.** `sanitizeLine` also folds `\n`/`\t` to spaces and then truncates by **display width** (`ansi.Truncate`/`lipgloss.Width`). It is used for row snippets and never splits a rune or leaves a partial sequence; today's `truncate` cuts by byte.
  - **Where it applies.** Both are applied to every agent- or remote-supplied string Jobs shows:
    - in the Overview, Queue and History rows: summaries, reasons and origin labels;
    - in the results overlay;
    - in the prompt overlay: the composed prompt and its arguments;
    - in the detail views that render them;
    - beyond Jobs (FR-029): in Daemons › Orchestrators, and in the notifications that lead into this feature (the notification list's message and filter text, such as a failed or awaiting-review run's notification) and the sandbox list's review prompt and mark-free dialog.
  - **Placement.** Sanitising happens where text is rendered, never on stored data.
- **Rationale**: These strings are written by agents that handle untrusted input (PR bodies, issue text). A prompt-injected agent can report OSC 52 (clipboard write), `ESC[2J` (screen clear) or cursor moves that spoof the UI.
- **Alternatives considered**:
  - Escaping instead of stripping (showing `^[`): noisier, with no added value for a report.
  - Sanitising in the daemon: it would still need a client guard for older daemons and other fields.

## R11 — Cursors keyed by identity

- **Decision**:
  - **Identity, not index.** Every Jobs cursor remembers the **identity** of the selected item, not just its index:
    - a run is (host, orchestrator id, run id);
    - an event is (host, orchestrator id, event id);
    - an action is (host, orchestrator id, action name);
    - a group heading or empty line is (host, orchestrator id).
  - **Re-anchoring.** After every re-derive (load, event reload, filter or page change), the cursor is moved to the item with that identity. In the Queue tab this includes switching to its page. Only when the item is gone does it move to its nearest neighbour: the row now at its old position in the same section, else the previous row (FR-031). Operations read the item from the cursor's key, not its index, so they never act on an item that moved into its place.
- **Rationale**: Rows reorder under the developer. Upcoming re-sorts every time an event fires or is skipped (its `next_fire_at` moves). Each finished run shifts newest-first history by one, and groups gain or lose items. With index cursors, `n` (no confirmation when dedupe is on), `space`, `x` or `m` can act on a different item than the one highlighted. `applyOrchRows` already re-anchors the orchestrator list this way.
- **Alternatives considered**: Freezing the rows while the cursor is on them (the data goes stale, and FR-005 is violated).

## R12 — Operations go only to the hosting daemon (FR-030)

- **Decision**:
  - **A connected-host check.** Every Jobs and Daemons › Orchestrators operation resolves its daemon with a new `connectedDaemon(host) (Daemon, error)`. It returns an error, such as `<host> is not connected`, when the host isn't in the manager or isn't `HostConnected`.
  - **No fallback.** Operations never use `daemonForHost`, which silently falls back to the active daemon (`m.daemon`).
  - **Where the check runs.** It runs when the operation is started (key press) **and** again when a form is submitted. The host can drop while a form is open: a create, rename or call submitted after a disconnect is refused with the message, and the form stays open.
  - **Detail views too.** The opened orchestrator detail (Workers detach and mark free, Reasoning edit and rebuild, Hosts add and remove) resolves its daemon the same way. Opening an orchestrator from Daemons › Orchestrators is refused when that daemon is not connected (FR-030).
  - **Single-daemon setups.** Without a manager, the lone daemon is "connected".
- **Rationale**: `daemonForHost` falls back to the active daemon. A Jobs row or the Daemons › Orchestrators list of a host that dropped would otherwise create orchestrators, or act, on the wrong daemon, or fail with a misleading NotFound.
- **Alternatives considered**: Changing `daemonForHost` itself (it is used by many sandbox paths that rely on the fallback).
