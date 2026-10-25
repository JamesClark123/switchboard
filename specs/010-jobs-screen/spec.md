# Feature Specification: Jobs Screen & Orchestrator UI Reorganisation

**Feature Branch**: `jamesloganclark/swi-5-feature-updates-to-orchestrator-ui`

**Created**: 2026-10-25

**Status**: Draft

**Input**: User description (Linear SWI-5, "Feature - Updates to Orchestrator UI"): "We want to make some updates to how Orchestrators and their queues show in the sandbox UI. 1. Add a `Jobs` tab to the top bar of the UI (the section navigated with shift + arrow keys). This jobs tab will hold much of the functional behaviour of the orchestrators. It should have its own tab groups shown in a similar UI to the `Sandboxes` tab: `Overview`, `Actions`, `Events` and `Queue`. `Overview` (first) is very similar to the Queue tab: it shows the history, in-progress and queued actions across ALL orchestrators, each entry showing which host & orchestrator it is on, plus an `upcoming` section of upcoming scheduled events. Its entries keep the current Queue actions; `upcoming` entries get `Enqueue now` (same behaviour as the Events tab's Enqueue now); completed entries get `Open results` to see the agent's full report (currently cut off when long); its history is capped at 15. `Actions` (second) is the current Actions section grouped by orchestrator. `Events` (next) is the current Events tab grouped by orchestrator. `Queue` (last) lists everything with a filter by orchestrator, no cap, paginated if necessary. 2. `Workers`, `Reasoning` and `Hosts` move from the `Orchestrators` top tab to a section under `Daemons` named `Orchestrators`, which behaves essentially like the previous Orchestrators top tab with only those three sections. The old Orchestrators top tab is deleted."

## Clarifications

### Session 2026-10-25

- Q: The ticket says the Jobs › Queue tab shows "a list of all events" — what does it list? → A: **Runs** (every queued, in-progress and finished run across orchestrators); timed events stay in the Events tab. (FR-017)
- Q: "The list should not have a cap" — daemons keep the 200 most recent finished runs per orchestrator; what should the Queue tab show? → A: Everything the daemons keep; the 200-per-orchestrator retention is **unchanged** (a UI-only change). (FR-018)
- Q: Which order are the Jobs tabs in? → A: **Overview, Actions, Events, Queue**. (FR-002, FR-026)

## Background (current behaviour this feature changes)

Switchboard's terminal UI has three top-level screens, cycled with shift+←/→: **Sandboxes**, **Daemons** and **Orchestrators**. The Orchestrators screen lists every orchestrator on every connected daemon; opening one shows six views — **Workers**, **Actions**, **Queue**, **Events**, **Reasoning** and **Hosts** — for that single orchestrator. Its Queue view shows queued, in-progress and finished runs (with keys to open a run's prompt, cancel a queued run and mark a worker free), and a finished run's reported result is shown on one line, so a long result is cut off. The Sandboxes screen groups its list into tabs (All, one per daemon, one per user group) switched with ←/→. The Daemons screen lists daemons, each with per-daemon sections (MCP gateway, Secrets).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - See all orchestrator work in one place (Priority: P1)

A developer running several orchestrators across several machines opens a single **Jobs › Overview** view and sees, across all of them, what is queued, what is running, what recently finished, and which scheduled events are coming up next — each entry labelled with its daemon (host) and orchestrator — without opening each orchestrator in turn.

**Why this priority**: This is the core value of the feature: today the developer must open every orchestrator separately to know what is happening. A cross-orchestrator overview is useful on its own, even before any other tab exists.

**Independent Test**: With two orchestrators on two daemons, each with queued, running and finished runs and an enabled timed event, open Jobs › Overview and confirm every run and upcoming event appears once, in the right section, labelled with its host and orchestrator.

**Acceptance Scenarios**:

1. **Given** orchestrators on two connected daemons with runs in different states, **When** the developer opens Jobs › Overview, **Then** it shows four sections — Upcoming, Queued, In progress and History — containing the runs of all those orchestrators, each entry naming its host and orchestrator.
2. **Given** more than 15 finished runs across all orchestrators, **When** the Overview is shown, **Then** its History section lists only the 15 most recently finished runs, newest first.
3. **Given** an enabled timed event with a next firing time, **When** the Overview is shown, **Then** the Upcoming section lists that event with its orchestrator, host, action and next firing time; a disabled event is not listed.
4. **Given** a daemon that is disconnected or unreachable, **When** the Overview is shown, **Then** the runs of the reachable daemons are still shown and the view says which daemon could not be read.
5. **Given** the Overview is open, **When** a run changes state on any orchestrator (for example it starts or completes), **Then** the Overview reflects the change without the developer reopening it.

---

### User Story 2 - Act on runs and upcoming events from the Overview (Priority: P1)

From the Overview, the developer can do to any run what they can do in an orchestrator's Queue today (open its prompt, cancel a queued run, mark an awaiting-review worker free), can trigger an upcoming scheduled event immediately with **Enqueue now**, and can **Open results** on a finished run to read the agent's full report.

**Why this priority**: An overview the developer cannot act on forces them back into each orchestrator, which defeats its purpose; reading full results fixes a reported pain point (long reports are cut off today).

**Independent Test**: From the Overview alone: cancel a queued run, open a run's prompt, mark an awaiting-review worker free, Enqueue now an upcoming event, and open the full results of a completed run whose report is longer than one line.

**Acceptance Scenarios**:

1. **Given** a queued run in the Overview, **When** the developer cancels it, **Then** it is cancelled on its own orchestrator exactly as from that orchestrator's Queue view, and the Overview updates.
2. **Given** a run awaiting review, **When** the developer marks its worker free from the Overview, **Then** the worker is freed exactly as from the orchestrator's Queue view.
3. **Given** an entry in the Upcoming section, **When** the developer chooses Enqueue now, **Then** one run of that event is queued on its orchestrator with the same rules as the Events view's Enqueue now (dedupe, schedule unchanged, confirmation when dedupe is off and a run is already open, repeat presses ignored briefly), and the outcome is reported.
4. **Given** a completed run whose reported result is longer than the line it is shown on, **When** the developer chooses Open results, **Then** the complete report is shown in a view they can scroll through and close, with nothing cut off.
5. **Given** a failed or cancelled run, **When** the developer chooses Open results, **Then** whatever the run reported (or the reason it ended) is shown; when there is nothing reported, the view says so.

---

### User Story 3 - Browse every run with a filter (Priority: P2)

In **Jobs › Queue**, the developer sees every run the daemons still hold — queued, in progress and finished — across all orchestrators, can narrow the list to one orchestrator, and pages through long histories.

**Why this priority**: The Overview deliberately caps history at 15; a full, filterable list is how the developer investigates older runs.

**Independent Test**: With more runs than fit on one page across two orchestrators, open Jobs › Queue, page through all of them, filter to one orchestrator, and confirm only its runs remain and paging still works.

**Acceptance Scenarios**:

1. **Given** runs on several orchestrators, **When** the developer opens Jobs › Queue, **Then** every run held by the daemons is listed (no 15-item cap), each labelled with host and orchestrator.
2. **Given** more runs than fit on one page, **When** the developer moves to the next or previous page, **Then** the list advances by a page and shows which page of how many is displayed.
3. **Given** the developer selects one orchestrator in the filter, **When** the list is shown, **Then** only that orchestrator's runs are listed; clearing the filter shows all runs again.
4. **Given** the Queue tab, **When** the developer acts on a run, **Then** the same per-run actions as the Overview are available (open prompt, cancel, mark free, open results).

---

### User Story 4 - Manage actions and events for all orchestrators (Priority: P2)

In **Jobs › Actions** and **Jobs › Events**, the developer sees the actions and timed events of every orchestrator, grouped under each orchestrator's name, and can do everything the per-orchestrator Actions and Events views allow today (add, edit, delete and call actions; add, edit, delete, enable/disable and Enqueue now events).

**Why this priority**: These are today's per-orchestrator views made cross-orchestrator; they are needed to remove the old Orchestrators screen without losing capability.

**Independent Test**: With two orchestrators, add an action to the second one from Jobs › Actions, call it, then add and toggle a timed event for the first one from Jobs › Events, and confirm each change lands on the intended orchestrator.

**Acceptance Scenarios**:

1. **Given** two orchestrators with actions, **When** the developer opens Jobs › Actions, **Then** actions are listed under a heading per orchestrator (with its host), in a stable order.
2. **Given** the cursor is within an orchestrator's group, **When** the developer adds an action, **Then** the new action is created on that orchestrator; editing, deleting and calling act on the highlighted action's orchestrator.
3. **Given** two orchestrators with timed events, **When** the developer opens Jobs › Events, **Then** events are listed under a heading per orchestrator, and add, edit, delete, enable/disable and Enqueue now act on that orchestrator exactly as the current Events view does.
4. **Given** an orchestrator with no actions (or no events), **When** the corresponding tab is shown, **Then** its group still appears with an empty-state line, so a first action or event can be added to it.

---

### User Story 5 - Orchestrator administration under Daemons (Priority: P2)

Orchestrator administration — creating, renaming and removing orchestrators, and their **Workers**, **Reasoning** and **Hosts** views — moves to an **Orchestrators** section under the **Daemons** screen. The old Orchestrators top-level screen is removed, and **Jobs** takes its place in the top bar.

**Why this priority**: Completes the reorganisation: day-to-day work lives in Jobs, setup lives with the daemons that host the orchestrators.

**Independent Test**: From Daemons, open a daemon's Orchestrators section, create an orchestrator, attach a worker in Workers, rebuild its reasoning sandbox in Reasoning, add a host in Hosts; confirm shift+←/→ now cycles Sandboxes → Daemons → Jobs and no Orchestrators screen remains.

**Acceptance Scenarios**:

1. **Given** the top bar, **When** the developer cycles with shift+←/→, **Then** the screens are Sandboxes, Daemons and Jobs (in that order), and there is no Orchestrators screen.
2. **Given** a connected daemon on the Daemons screen, **When** the developer opens its options, **Then** an **Orchestrators** section is offered alongside MCP gateway and Secrets.
3. **Given** the Orchestrators section of a daemon, **When** it is opened, **Then** it lists that daemon's orchestrators and allows create, rename, remove and open, as the old Orchestrators screen did for that daemon.
4. **Given** an orchestrator opened from that section, **When** its detail is shown, **Then** it has exactly three views — Workers, Reasoning and Hosts — behaving as they do today; Actions, Queue and Events are no longer there (they are in Jobs).
5. **Given** a notification or shortcut that used to open the Orchestrators screen (for example a run that needs review), **When** it is followed, **Then** it opens the equivalent place in the new layout (Jobs for runs, the daemon's Orchestrators section for workers).

---

### Edge Cases

- **No orchestrators anywhere**: every Jobs tab shows an empty state that explains orchestrators are created under Daemons › Orchestrators.
- **Only some daemons reachable**: Jobs tabs show what the reachable daemons return and name the unreachable ones; actions on entries of a daemon that disconnects meanwhile fail with a clear message.
- **Same orchestrator name on two daemons**: entries and group headings always include the host, so they stay distinguishable.
- **Orchestrator removed while shown**: its entries and group disappear on the next refresh; a pending action on it reports that it no longer exists.
- **Filter selection disappears** (the filtered orchestrator is removed or its daemon disconnects): the filter falls back to "all orchestrators" and says so.
- **Upcoming list for an event whose action was deleted**: the event is disabled by the existing rules and therefore not listed as upcoming.
- **Page beyond the end** after runs are trimmed or filtered: the list moves to the last available page instead of showing an empty page.
- **Open results on a run still in progress**: the action is not offered (it applies to finished runs); a finished run with an empty report says "no result was reported".
- **Very long reports**: the results view scrolls; nothing is truncated.

## Requirements *(mandatory)*

### Functional Requirements

**Navigation & layout**

- **FR-001**: The top-level screens MUST be **Sandboxes**, **Daemons** and **Jobs**, cycled in that order with shift+←/→; the **Orchestrators** top-level screen MUST be removed.
- **FR-002**: The Jobs screen MUST have four tabs, switched the same way as the Sandboxes screen's tabs (←/→, with the active tab shown): **Overview**, **Actions**, **Events**, **Queue**, in that order, with Overview selected the first time Jobs is opened.
- **FR-003**: Every Jobs tab MUST cover all orchestrators on all connected daemons, and every run, action, event and group heading MUST show its host (daemon) and orchestrator.
- **FR-004**: Jobs tabs MUST keep showing data from reachable daemons when others are unreachable, and MUST name the daemons that could not be read.
- **FR-005**: Jobs tabs MUST update when runs, actions or events change on any covered orchestrator, without the developer reopening them.

**Overview**

- **FR-006**: The Overview MUST show four sections: **Upcoming**, **Queued**, **In progress** and **History**.
- **FR-007**: Upcoming MUST list each enabled timed event of every covered orchestrator once, with its next firing time, ordered by next firing time (soonest first); disabled events MUST NOT be listed.
- **FR-008**: Queued and In progress MUST list all queued and in-flight runs (running, awaiting input, awaiting review) of every covered orchestrator.
- **FR-009**: History MUST list the 15 most recently finished runs across all covered orchestrators, newest first.
- **FR-010**: Each run in the Overview MUST offer the same actions the current Queue view offers for a run in that state: open its prompt; cancel a queued run; mark the worker free for a run awaiting review.
- **FR-011**: Each Upcoming entry MUST offer **Enqueue now**, with exactly the behaviour of the Events view's Enqueue now (one run queued as that event; the event's dedupe applies and a refusal is explained; the schedule is unchanged; confirmation when dedupe is off and a run of the event is already open; an immediate repeat press is ignored).
- **FR-012**: Each finished run (completed, failed or cancelled) MUST offer **Open results**, which shows the run's full reported result (or the reason it ended) in a scrollable view that can be closed, with no truncation; when nothing was reported it MUST say so.

**Queue tab**

- **FR-013**: The Queue tab MUST list every run the daemons hold for the covered orchestrators — queued, in progress and finished — with no item cap.
- **FR-014**: The Queue tab MUST be paginated when the runs do not fit, MUST let the developer move to the next and previous page, and MUST show the current page and the number of pages.
- **FR-015**: The Queue tab MUST let the developer filter the list to a single orchestrator and clear the filter; the filter MUST name the orchestrator and its host.
- **FR-016**: Runs in the Queue tab MUST offer the same per-run actions as the Overview (FR-010, FR-012).
- **FR-017**: The Queue tab MUST list **runs** (the queue items: queued, in progress and finished), not timed events; timed events are listed in the Events tab.
- **FR-018**: "No cap" applies to the Queue tab itself: it MUST show every run the daemons keep. The daemons' existing retention (the 200 most recent finished runs per orchestrator) is unchanged by this feature.

**Actions & Events tabs**

- **FR-019**: The Actions tab MUST list every action of every covered orchestrator, grouped under a heading per orchestrator (with its host), groups in a stable order; an orchestrator with no actions MUST still show its group with an empty-state line.
- **FR-020**: The Actions tab MUST offer the current Actions view's operations — add, edit, delete and call — acting on the orchestrator of the group the cursor is in (add) or of the highlighted action (edit, delete, call).
- **FR-021**: The Events tab MUST list every timed event of every covered orchestrator, grouped the same way, and MUST offer the current Events view's operations — add, edit, delete, enable/disable and Enqueue now — acting on the corresponding orchestrator with today's rules.

**Orchestrator administration under Daemons**

- **FR-022**: Each connected daemon on the Daemons screen MUST offer an **Orchestrators** section alongside its existing sections.
- **FR-023**: The Orchestrators section MUST list the orchestrators hosted by that daemon and allow creating, renaming, removing and opening one, with today's behaviour.
- **FR-024**: An orchestrator opened from that section MUST show exactly three views — **Workers**, **Reasoning**, **Hosts** — each behaving as it does today.
- **FR-025**: Every existing entry point into the removed Orchestrators screen (notifications, shortcuts from the sandbox list such as mark free or attach-to-orchestrator, review prompts) MUST lead to the equivalent place in the new layout: runs and their actions in Jobs; workers, reasoning and hosts in Daemons › Orchestrators.
- **FR-026**: The Jobs tab order is **Overview, Actions, Events, Queue** (as in FR-002), matching the ticket's detailed descriptions.

**General**

- **FR-027**: All capabilities available today for orchestrators MUST remain available after the reorganisation (nothing is lost by removing the Orchestrators screen).
- **FR-028**: Key hints for each Jobs tab and the new Daemons › Orchestrators section MUST list the actions available there.

### Key Entities

- **Orchestrator**: a named prompt queue hosted by one daemon (host); owns actions, timed events, runs, workers, a reasoning sandbox and a host list. Identified to the developer by name plus host.
- **Run**: one queued, in-progress or finished execution of an action on an orchestrator; has a state, the event that caused it, received/finished times, and (when finished) a reported result.
- **Action**: a reusable prompt template on an orchestrator that runs are created from.
- **Timed event**: a schedule on an orchestrator that calls an action at an interval; has an enabled flag, dedupe option and next firing time ("upcoming").
- **Host (daemon)**: the machine whose daemon hosts an orchestrator; may be connected, disconnected or unreachable.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can see the state of all orchestrators' work (queued, running, recent history, upcoming) from one screen, opening zero per-orchestrator views — down from one view per orchestrator today.
- **SC-002**: Every run and upcoming event shown in Jobs is attributable to its host and orchestrator at a glance, with no two entries from different orchestrators indistinguishable.
- **SC-003**: The full result of any finished run can be read in full (100% of its reported text) from the Jobs screen.
- **SC-004**: A developer can trigger an upcoming scheduled event from the Overview in a single action plus, at most, one confirmation.
- **SC-005**: Every operation possible on the old Orchestrators screen remains possible after the change (no capability regression), verified by an inventory of today's operations.
- **SC-006**: With hundreds of runs across orchestrators, the Queue tab lets the developer reach any run, and narrowing to one orchestrator takes a single filter choice.

## Assumptions

- The Queue tab can only show runs the daemons still hold: the 200 most recent finished runs per orchestrator are kept (unchanged), so "no cap" means no additional cap in the UI.
- "All orchestrators" means every orchestrator on every daemon the developer is currently connected to (local and remote), matching how the current Orchestrators screen lists them.
- The Jobs tab takes the old Orchestrators tab's position in the top bar (after Daemons).
- The Overview's History cap (15) applies to the combined list across all orchestrators, not per orchestrator.
- Upcoming shows one entry per enabled timed event (its next firing), not several future occurrences.
- "Open results" shows the result text the agent reported when it completed (or the developer's note when it was closed by hand); it does not reconstruct the agent's terminal output.
- Creating actions and events from Jobs targets the orchestrator whose group the cursor is in; there is no separate orchestrator picker unless the cursor is on no group (then the developer picks one).
- The hidden "Orchestrator Sandboxes" tab and the per-sandbox orchestrator line on the Sandboxes screen are unchanged.
- No new configuration settings or environment variables are introduced.
