# Feature Specification: Worker Lanes

**Feature Branch**: `jamesloganclark/swi-18-feature-workers-should-be-assignable-to-lanes`

**Created**: 2026-10-28

**Status**: Draft

**Input**: User description (Linear SWI-18, "Feature - Workers should be assignable to `lanes`"), quoted verbatim:

> We want to introduce a concept of `lanes` for workers attached to an orchestrator. Currently users can mark certain jobs/actions as only runnable on workers with a specific tag. This will be replacing that. The high level expectation is that actions may (key word, not required) be marked exclusively runnable on specific lane types OR optionally runnable on specific lane types. Meaning an action can be marked such that it can only run on certain lane OR it can be marked that it can run on a lane type (but still be runnable on generic, un-lanned workers). The requirements for the feature is below:
>
> 1. Users can create lanes on an orchestrator
>    1. This can be done in the `Orchestrators` section of the Daemon top tab. A new tab called `Lanes` should be added along with `Workers` `Reasoning` and `Hosts`
>    2. This can also be done dynamically from the action creation/edit screen. The user should have the option to create/edit lanes. This option should open a dialog that shows the same options that are shown on the `Lanes` tab in 1.a
> 2. Workers can be assigned to lanes
>    1. Workers can be assigned to one or more lanes
>    2. Sandboxes assigned to lanes have their lanes displayed on the list/overview page
>    3. Workers without a lane are considered to be an `open lane`
> 3. Actions can be marked exclusively or inclusively to one or more lanes
>    1. When marked exclusively to one or more lanes, that action can ONLY be run on workers that have a matching lane tag (only one match is enough in the case of multiple lane tags being assigned)
>    2. When marked inclusively the action may run on workers with a matching lane tag OR on any `open lane` workers
>    3. Actions without assigned lanes should continue to running on `open lane` works as they have in the past

## Clarifications

### Session 2026-10-28

- Q: Should a worker in a lane still take runs from actions that name no lanes? → A: **No. Lanes reserve workers.** An action with no lanes runs only on open-lane workers, so a laned worker only takes runs from actions that name one of its lanes. This changes today's behaviour, where an action with no worker tag can run on any worker. (FR-312, FR-313)
- Q: Lanes replace the action "worker tag" requirement. What happens to existing actions that have a worker tag? → A: **The requirement is dropped.** On upgrade, an action's worker tag is discarded and the action becomes un-laned. The developer recreates lanes by hand. The sandbox's own tag stays as a plain purpose label and no longer affects where runs go. (FR-318, FR-319)
- Q: Where is a worker assigned to lanes? → A: **On both the Workers tab and the Lanes tab.** A key on a Workers-tab row opens a lane picker for that worker, and a lane's editor on the Lanes tab picks its member workers. Both edit the same assignment. (FR-306, FR-307)
- Q: What does deleting a lane do while workers or actions still use it? → A: **Confirm, then remove it everywhere.** The confirm lists the affected workers and actions. On yes, the lane is removed from them, and an action left with no lanes becomes un-laned and runs on open-lane workers. The confirm says so. (FR-305)

### Spec review 2026-10-28 (pre-PR review, resolved before the PR opened)

- "List/overview page" in the ticket means the **sandbox list**. The app has no per-sandbox overview screen, and workers are hidden from the All, host and group tabs unless they need the developer. Lanes therefore show on the Orchestrator Sandboxes tab, on any worker row surfaced on the other tabs, and on the Workers view (FR-309).
- A worker's lanes are kept on the orchestrator. They are also pushed to the daemon that runs the worker, the same way the orchestrator's name is, so that daemon's sandbox list can show them. While that host is unreachable, the last-known lanes are shown (FR-309a).
- Any change to lanes, a worker's lanes or an action's lanes starts a dispatch pass (FR-314a). Briefings are refreshed only when what they describe changes (FR-321; refined in run-555 below).
- A queued run follows its action's current lanes. If the action is deleted, the run keeps the lanes the action last had, even if a new action is later saved under the same name. A later lane delete removes the lane from such runs too (FR-314b, FR-305).
- A save that would leave no open-lane worker while un-laned `prompt` actions exist warns first (FR-308a).
- "Waiting for a lane" is new: today a queued run shows no reason it is waiting. A worker counts as able to take a run if it is attached, whatever its state (FR-315).
- Lane-aware clients mark their action saves, and an unmarked save, which can only come from an older client, is refused. An older client therefore can't silently clear an action's lanes (FR-318a). Membership saves made against a lane list that has changed since the picker opened are refused (FR-307a).

### PR review 2026-10-28 (pr-review run `run-c275c8f2-552`, resolved in pr-response run `run-c275c8f2-555`)

- **The reasoning sandbox is never an open-lane worker.** An open-lane worker is a *non-reasoning* worker with no lanes. This is the definition FR-308a, FR-313 and FR-315 count with (FR-308, Key Entities).
- **Pushes are targeted and off the save path.**
  - A membership change pushes lanes only to the workers it affects.
  - Briefings, which list actions only, are refreshed only when an action's lanes or mode change, or when a lane an action uses is renamed or deleted.
  - Both kinds of push run in the background and are coalesced.
  - A host's reconnect push carries its workers' lanes. (FR-309a, FR-321)
- **The waiting reason (FR-315)** covers inclusive runs too. It is derived whenever runs are read, like queue position, never stored. It is separate from a run's failure or cancel reason.
- **Runs of a deleted action** keep its lanes, by lane identity, **and its mode**, and they follow lane renames (FR-314b, FR-304).
- **The orchestrator's roster is the only source of lane membership.** Lanes pushed to a worker's host are for display only and are never read back. A worker that reconcile adopts and that is not already on the roster starts in the open lane (FR-309b).
- **Action saves name lanes by identity.** A save naming a lane that no longer exists is refused, and the form reloads its lane choices (FR-316a).
- **The open-lane warning (FR-308a)**:
  - fires on any change that leaves *none*, including bulk member edits and detach (refined in run-561: guarded on the orchestrator's daemon, visible otherwise);
  - also fires on saving an un-laned action while there are none;
  - is enforced by the daemon, which needs an explicit confirmation.
- **Inclusive runs give way.** They prefer lane workers, except when a later queued run needs that lane worker (FR-314; superseded in run-561 by reservation dispatch, below). SC-002 is reworded to match.
- **Worker tags dropped on upgrade** are logged and listed once in the TUI (FR-318b).
- **A sandbox can be given its lanes in the same step that makes it a worker** (FR-306a).
- **Lane descriptions** are single-line and capped at 200 characters, and are never sent to agents (FR-302, FR-321).
- **Lane names from another machine** are validated and sanitised before display (FR-309b).
- **Lanes are routing, not access control** (Assumptions).
- **FR-307a** refuses only saves whose own worker or lane changed.
- **Nits:**
  - The lane name `open` is reserved (FR-303).
  - The retired worker-tag field is never reused.
  - A new client refuses lane editing on a daemon without lanes (FR-318a).
  - Orchestrator removal with unreachable hosts gets an edge case.
  - The "as before" wording for deleted actions is corrected.

### PR re-review 2026-10-28 (pr-review run `run-c275c8f2-558`, resolved in pr-response run `run-c275c8f2-561`)

The three majors share one cause: the spec never said when a worker becomes eligible, or which workers count. One model now answers both.

- **Live roster** (FR-308): the workers that count, for routing, for the open-lane safeguard and for waiting reasons. (Refined in run-567: a worker stops counting only on evidence, see below and the Lifecycle section.)
- **Lanes before eligibility** (FR-306a, FR-306b): a worker is never dispatched to before the lanes picked for it apply.
  - **Attach** writes the roster entry, with its lanes, before the sandbox is linked. (Superseded in run-567: attach keeps today's link-then-commit order, and the attach ticket covers the gap. See Lifecycle L4.)
  - **Launch** uses an **attach ticket**. The orchestrator issues it with the worker bundle and keeps the picked lanes under it. The launched sandbox carries only the ticket's identifier, and adoption consumes the ticket.
  - The ticket is the orchestrator's own token, not lane data, so FR-309b still holds: lanes are never read back from a host.
- **Open-lane safeguard in two tiers** (FR-308a, FR-308b):
  - **Guarded:** changes made through the orchestrator's daemon (membership saves, action saves, detaching through the orchestrator) are checked by that daemon. It applies one rule: the number of open-lane workers would drop from at least one to zero, or an un-laned action is saved while there are none. A guarded change is refused with one refusal shape that lists the affected actions. The client folds that list into the change's single confirm and resends it, so there is never a second dialog.
  - **Visible:** changes the orchestrator cannot see coming are made visible instead. These are a direct detach through the sandbox's own daemon, reconcile drops, sandbox removal and hosts going away. A direct detach warns from the client's last-known data, and everything after that shows as waiting reasons (FR-315).
- **Reservation dispatch** (FR-314): queued exclusive runs reserve lane workers first. (Refined in run-567 with a fallback chain, see below and Lifecycle L6.)
- **Minors:**
  - FR-321's triggers add to today's instead of replacing them.
  - FR-315 counts only the live roster, and gains a reason for "every worker that could take it is stopped or unreachable".
  - FR-309b drops bad lane names instead of refusing the whole push.
  - Lanes are capped at 32 per orchestrator (FR-302).
  - A new client refuses action saves on a daemon without lanes, so it can't erase a worker tag (FR-318a).
- **Nits:**
  - The dropped-tag notice is persisted on the orchestrator record and dismissed through the daemon (FR-318b).
  - A deleted action's lane setting is kept on the orchestrator record, written in the same change as the delete (FR-314b). (Superseded in run-567: the setting is copied onto the queued runs, see FR-314b.)

### PR re-review 2026-10-28 (pr-review run `run-c275c8f2-564`, resolved in pr-response run `run-c275c8f2-567`)

The last two rounds each found new gaps in the roster, reservation and attach mechanisms. So this round first wrote out the lifecycle end to end: daemon restart, reconcile, launch, attach, detach, dispatch, and lane and action changes. That walk-through is now the **Lifecycle** section, and every FR below was checked against it.

- **The live roster is based on evidence** (FR-308, L1). A roster worker stops counting only on evidence: its last-known record names another controller or none, or reconcile drops it. After a restart the persisted roster still holds every worker's lanes. Workers with no record yet count as *live but unreachable*: never dispatched to, still counted by the open-lane safeguard, and named by FR-315's "stopped or unreachable" reason.
- **Reservation dispatch has a fallback chain** (FR-314, L6).
  - An exclusive run tries its reservation, then an unreserved worker in its lanes, then one reserved for a *later* run.
  - A worker that fails the re-check or refuses delivery leaves the pass, and its reservation is released.
  - At the end of a pass, no run is still queued while a free, accepting worker it could take is unclaimed, and no run waits for a later one. SC-002 tests exactly this.
- **Attach keeps today's order, link then commit, and reconcile tolerates the gap through the attach ticket** (FR-306a, L4). Launch and attach now share one adoption path (L2).
- **Attach tickets** (FR-306b) are bound to the host, and on attach to the sandbox. They are stored on the record and swept when they expire. Lane deletes strip them. An expired, unknown or mismatched ticket adopts the sandbox with *lanes pending* (not dispatchable, flagged) rather than in the open lane.
- **Guarded changes use a check-then-confirm handshake** (FR-308a). The confirmed request carries the affected actions the developer saw. The daemon accepts only if the current set is within it, and otherwise refuses with the new list; that rare second confirm is stated.
- **FR-308b** says "cannot check open-lane workers" when the client has no data.
- **Kept lane settings travel on the queued runs**, written in one atomic change with the action delete (FR-314b, L7). They leave with the run, so nothing needs cleaning up, and nothing is keyed by name.
- **Consistency check against the lifecycle** (done before committing). Further fixes:
  - **Reason runs:** FR-311–FR-315 and L6 now cover `prompt` runs only; `reason` runs go to the reasoning sandbox as today.
  - **Adoption and drops** happen on sandbox-changed events as well as on reconcile, so a launched worker joins at once, and a directly detached worker loses its roster entry and lanes (L2).
  - **The attach commit** applies the ticket's current lanes. The ticket is kept when the outcome is unknown.
  - ***lanes pending*** is shown and pushed (FR-309, FR-309a). It is covered by FR-307a, and cleared by any membership save that sets its lanes.
  - **FR-315** mentions *lanes pending* workers as a hint, not as possible takers.
  - **The detach guard** is evaluated before unlinking, under a lock that serialises guarded changes.
  - **"Refuses delivery"** means a precondition refusal; other errors fail the run, as today.
  - **Reservation** prefers the worker with the fewest lanes outside the run's own.
  - **The guard** also counts queued un-laned runs of deleted actions.
- **Nits:** FR-321 lists today's refresh triggers, including attach and reconcile, and the operations they hold up. FR-318b applies whenever a worker tag is loaded. The dispatch bound is restated per lane and per membership.


## Background (current behaviour this feature changes)

- An orchestrator's `prompt` actions can name one optional **worker tag**. A run of such an action is delivered only to a worker whose sandbox tag (its single, free-text purpose label) equals it. An action with no worker tag runs on any free worker, tagged or not.
- The sandbox tag is a general-purpose label, set on any sandbox (not only workers) and shown as `#tag` on the sandbox list. It doubles as the routing key, so a worker can belong to only one "group", and renaming the label silently changes which actions it can run.
- The orchestrator detail (Daemons › *daemon* › Orchestrators › *orchestrator*) has three views: **Workers**, **Reasoning** and **Hosts**. Actions are created and edited from Jobs › Actions.
- Workers' agents learn the offered actions, including any worker tag, from the orchestrator's briefing and its list-actions tool.
- Workers appear on the sandbox list only on the Orchestrator Sandboxes tab (`O`), or on the other tabs while they need the developer. The daemon that runs a worker knows the orchestrator only by its controller record (identity and last-known name). The roster, which this feature extends with lanes, lives on the orchestrator's daemon.
- A queued run shows only its position and action. Nothing says why it is waiting, and changing an action does not start a new dispatch pass.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Reserve workers for a kind of work (Priority: P1)

A developer has five workers. Two of them are set up for front-end work, so they create a `frontend` lane, put those two workers in it, and mark the `build-ui` action **exclusive** to `frontend`. From then on, `build-ui` runs only on those two workers. The other actions, which name no lanes, run only on the three open-lane workers, so the front-end workers stay free for front-end work.

**Why this priority**: Reserving workers for a kind of work is the core of the feature and replaces the worker-tag requirement. Without it nothing else in this feature has an effect.

**Independent Test**: Create a lane, assign one worker to it, and make one action exclusive to it. Call that action and an un-laned action several times each. The exclusive action's runs go only to the laned worker, and the un-laned action's runs go only to open-lane workers.

**Acceptance Scenarios**:

1. **Given** an action exclusive to lane `frontend` and a free worker in `frontend`, **When** the action is called, **Then** the run is delivered to that worker (FR-311).
2. **Given** an action exclusive to `frontend` and no free worker in `frontend`, but free open-lane workers, **When** the action is called, **Then** the run stays queued until a worker in `frontend` is free (FR-311).
3. **Given** an action exclusive to `frontend` and `backend`, and a free worker that is in `backend` only, **When** the action is called, **Then** the run may be delivered to it: one matching lane is enough (FR-311).
4. **Given** an action with no lanes, a free worker in `frontend` and no free open-lane worker, **When** the action is called, **Then** the run stays queued and is not delivered to the laned worker (FR-313).
5. **Given** an action with no lanes and a free open-lane worker, **When** the action is called, **Then** it runs there exactly as it did before this feature (FR-313).
6. **Given** a queued run of an exclusive action and no worker in any of its lanes, **When** the developer views the queue, **Then** the run says it is waiting for a worker in the named lanes, and an agent checking the run sees the same reason. Once a worker is added to one of those lanes, the reason is gone the next time runs are read (FR-315).

---

### User Story 2 - Let lane workers help with a kind of work (Priority: P1)

A developer marks the `review-pr` action **inclusive** to the `reviewers` lane. Its runs go to a free worker in `reviewers` when there is one, and otherwise to any free open-lane worker, so reviews never wait just because the reviewer workers are busy.

**Why this priority**: Inclusive is the second of the two modes the ticket asks for. It lets lane workers take extra work without making that work wait for them.

**Independent Test**: Make one action inclusive to a lane with one worker. Call it while that worker is free, and again while it is busy. The first run goes to the lane worker, and the second goes to an open-lane worker.

**Acceptance Scenarios**:

1. **Given** an action inclusive to `reviewers`, a free worker in `reviewers` and a free open-lane worker, **When** the action is called, **Then** the run goes to the worker in `reviewers` (FR-312, FR-314).
2. **Given** an action inclusive to `reviewers`, no free worker in `reviewers`, and a free open-lane worker, **When** the action is called, **Then** the run goes to the open-lane worker (FR-312).
3. **Given** an action inclusive to `reviewers` and a free worker that is only in an unrelated lane `frontend`, **When** the action is called, **Then** the run is not delivered to that worker (FR-312).
4. **Given** one free worker in `reviewers`, one free open-lane worker, and two queued runs, an inclusive-`reviewers` run followed by an exclusive-`reviewers` run, **When** a dispatch pass runs, **Then** the inclusive run takes the open-lane worker and the exclusive run takes the `reviewers` worker, so neither waits (FR-314).

---

### User Story 3 - Manage lanes on the orchestrator (Priority: P1)

A developer opens an orchestrator's detail and switches to the new **Lanes** view. They can see every lane with its workers and the actions that use it, create a lane, rename it, change its description, change its member workers, and delete it.

**Why this priority**: Lanes have to exist before workers or actions can use them. This is the primary place to manage them, as the ticket asks.

**Independent Test**: In the Lanes view, create a lane, add two workers, rename it, then delete it. Each change shows at once in the Lanes view and the Workers view.

**Acceptance Scenarios**:

1. **Given** an orchestrator detail, **When** the developer moves between its views, **Then** the views are Workers, Reasoning, Hosts and Lanes (FR-301).
2. **Given** the Lanes view, **When** the developer creates a lane with a name that is new on this orchestrator, **Then** it is saved and listed. A name already used by another lane on this orchestrator, or one that breaks the naming rule, is refused with the reason (FR-302, FR-303).
3. **Given** a lane used by workers and actions, **When** the developer renames it, **Then** those workers and actions keep the lane under its new name and routing does not change (FR-304).
4. **Given** a lane used by two workers and one exclusive action whose only lane it is, **When** the developer deletes it, **Then** a confirm names the two workers and the action and says the action will run on open-lane workers. On yes the lane is removed from all three; on no nothing changes (FR-305).
5. **Given** a lane's editor, **When** the developer changes its member workers and saves, **Then** the Workers view shows the new membership (FR-307).

---

### User Story 4 - Assign workers to lanes and see them (Priority: P2)

A developer selects a worker in the Workers view and opens its lane picker. They tick `frontend` and `reviewers` and save. Both the Workers row and the worker's row on the sandbox list show both lanes. A worker with no lanes shows that it is in the open lane.

**Why this priority**: Assignment is needed for lanes to matter, but it is also reachable from the Lanes view (US3), so the Workers-view picker and the display are second priority.

**Independent Test**: Assign a worker to two lanes from the Workers view, then check the Workers row and the worker's row on the sandbox list's Orchestrator Sandboxes tab. Clear its lanes and check that both show the open lane.

**Acceptance Scenarios**:

1. **Given** a worker in the Workers view, **When** the developer opens its lane picker, ticks two lanes and saves, **Then** the worker is in exactly those two lanes (FR-306, FR-308).
2. **Given** a worker in two lanes, **When** the developer looks at the Workers view or the worker's sandbox-list row (Orchestrator Sandboxes tab, or another tab while the worker needs the developer), **Then** both lanes are shown (FR-309).
3. **Given** a worker in no lanes, **When** it is shown in those places, **Then** it reads as being in the open lane (FR-309).
4. **Given** the reasoning sandbox, **When** the developer looks for a lane picker, **Then** there is none: the reasoning sandbox is never in a lane (FR-310).
5. **Given** an orchestrator with no lanes, **When** the developer opens a worker's lane picker, **Then** it says there are no lanes yet and offers to create one (FR-306).
6. **Given** one open-lane worker and an un-laned `prompt` action, **When** the developer puts that worker in a lane and saves (or detaches it), **Then** a warning says no open-lane worker will be left to run un-laned actions, and names them. The change goes ahead only once the developer confirms (FR-308a).
7. **Given** a queued run of an action exclusive to `frontend` and no worker in `frontend`, **When** the developer adds a free worker to `frontend`, **Then** the run is delivered to it without any other change (FR-314a).
8. **Given** a sandbox being attached as a worker, or launched with the orchestrator chosen in the launch wizard, **When** the developer attaches or launches it, **Then** they can pick its lanes in the same step, and no run is delivered to it before those lanes apply (FR-306a, FR-306b).
9. **Given** a sandbox launched with lanes picked, whose host was unreachable until after the attach ticket expired, **When** the orchestrator adopts it, **Then** it shows as *lanes pending* on the Workers view and receives no runs until the developer sets its lanes or chooses the open lane (FR-306b).

---

### User Story 5 - Set an action's lanes, and manage lanes from the action form (Priority: P2)

While creating or editing a `prompt` action, a developer chooses its lanes and whether the action is exclusive or inclusive to them. The lane they need does not exist yet, so they open **Manage lanes** from the form. A dialog with the same options as the Lanes view opens. They create the lane, close the dialog, and pick the new lane in the form.

**Why this priority**: Setting an action's lanes is how lanes affect routing. Managing lanes from the form is a convenience on top of the Lanes view.

**Independent Test**: Open the action form, open Manage lanes, create a lane, close the dialog, pick the lane as exclusive and save. The action shows the lane and its mode in Jobs › Actions, and its runs follow US1.

**Acceptance Scenarios**:

1. **Given** the action form for a `prompt` action, **When** the developer picks one or more lanes, **Then** they must also choose exclusive or inclusive, and exclusive is preselected (FR-316).
2. **Given** the action form, **When** the developer opens Manage lanes, **Then** a dialog shows the same lane list and lane operations as the Lanes view, for the same orchestrator (FR-317).
3. **Given** the Manage lanes dialog, **When** the developer creates or renames a lane and closes the dialog, **Then** the action form's lane choices reflect the change at once, and every value already entered in the form is intact (FR-317).
4. **Given** the action form with lane `frontend` picked, **When** the developer deletes `frontend` in the dialog, **Then** the form no longer has it picked when the dialog closes (FR-317).
5. **Given** a `reason` action, **When** the developer edits it, **Then** no lane choice is offered. Switching a `prompt` action with lanes to `reason` clears its lanes, and the form says so (FR-316).
6. **Given** Jobs › Actions, **When** an action has lanes, **Then** its row shows its lanes and its mode (FR-320).

---

### User Story 6 - Existing worker tags stop routing runs (Priority: P3)

After upgrading, a developer's actions that had a worker tag have no lanes and run on open-lane workers. The worker-tag field is gone from the action form. Sandbox tags are still shown and editable as before, but no longer change where runs go.

**Why this priority**: The developer chose to drop worker tags rather than convert them, so this story only makes sure the change is clean and visible.

**Independent Test**: Start from a saved orchestrator with an action that has a worker tag. Upgrade, and check that the action has no lanes, its runs go to open-lane workers, and the action form has no worker-tag field.

**Acceptance Scenarios**:

1. **Given** an action saved with a worker tag before this feature, **When** the daemon first loads it after the upgrade, **Then** the action has no lanes and no worker tag (FR-318).
2. **Given** a sandbox with a tag, **When** runs are dispatched, **Then** its tag plays no part in which runs it can take (FR-319).
3. **Given** the action form, the action list, the orchestrator briefing and the agents' list-actions tool, **When** they describe an action, **Then** none of them mention a worker tag (FR-318, FR-321).

---

### Edge Cases

- **Exclusive action with no worker in any of its lanes**: the run stays queued indefinitely, as a worker-tagged run does today. The queue shows why (FR-315). It is not failed, because the developer may be about to add a worker to the lane.
- **Lane changes while runs are queued**: lanes are checked when a run is dispatched, not when it is called. Any lane change starts a dispatch pass, so queued runs follow it at once. A run already delivered is never moved (FR-314, FR-314a).
- **Lane changes while a worker is running a run**: the run carries on. The worker's new lanes apply to its next run.
- **Worker detached from the orchestrator**: it leaves all its lanes. If it is attached again, it starts in the open lane unless lanes are picked as it is attached (FR-306a, FR-308). Detaching through the orchestrator is guarded by FR-308a. Detaching directly, through the sandbox's own daemon while the orchestrator's daemon is not connected, gets FR-308b's best-effort warning.
- **Worker re-adopted by reconcile** (after a daemon restart or reconnect): a worker already on the roster keeps its lanes. One that is not, starts in the open lane, or as its attach ticket says (FR-306b, L2). Lanes shown on the worker's host are never read back (FR-309b).
- **Orchestrator removed while a worker's host is unreachable**: that worker keeps its controller record, and so its last-known lanes, on its host until the host is reconciled, as it keeps the orchestrator's name today. Those lanes are display-only and route nothing (FR-309b).
- **Worker on another host**: lanes are the orchestrator's, so a worker on any of the orchestrator's hosts can be in any of its lanes. While a host is unreachable its workers keep their lanes; they just aren't free.
- **Deleting the last lane of an inclusive action**: the action becomes un-laned and runs on open-lane workers only. Before, it could also run on lane workers. The delete confirm says so for each affected action, and counts the queued runs that will be re-routed (FR-305).
- **Leaving no open-lane worker**: un-laned actions, which is every action right after upgrade, would wait until an open-lane worker exists again. Changes made through the orchestrator's daemon that would do this are refused until the developer confirms (FR-308a). This covers one worker or several moving into lanes, a detach, and saving an un-laned action while there are none. A direct detach warns from the client's last-known data. Changes the developer does not make (reconcile drops, removal, a host going away) are not warned about; the affected runs show why they wait (FR-308b, FR-315).
- **Timed event on an action no worker can take**: with dedupe on (the default), it skips while its run is queued. With dedupe off, it queues a run every interval, each showing why it waits (FR-315). Queuing every interval is unchanged from worker tags today; the waiting reason is new.
- **Lane worker stopped or on an unreachable host**: it is still on the live roster, so it can take the run once it is free. The run is not *unservable*. If every live worker that could take it is stopped or unreachable, the run says so and names them (FR-315).
- **Remote worker's host unreachable when its lanes change**: the orchestrator routes by its own record at once. The sandbox list on the worker's host shows the last-known lanes until the host is reachable again and the change is pushed (FR-309a).
- **All workers are in lanes**: actions with no lanes wait in the queue until an open-lane worker exists. The queue shows that they are waiting for an open-lane worker (FR-315).
- **Lane picker or lane editor open while lanes change elsewhere** (another TUI, the other picker, or the Manage lanes dialog): a save made against a lane list or membership that has changed since it opened is refused with the reason, and the picker reloads, so one save never silently undoes another (FR-307a).
- **Action deleted while its runs are queued**: the runs still run and keep the lanes and mode the action last had (FR-314b). This is new: before this feature such a run lost its worker tag and could run on any worker.
- **Agent-triggered and timed calls**: they follow the same lane rules as developer calls, because lanes belong to the action, not the caller. A worker's agent can call an exclusive action whatever its own lanes: lanes decide where a run goes, not who may call it (Assumptions).
- **Inclusive and exclusive runs competing for lane workers**: queued exclusive runs reserve lane workers first. An inclusive run takes an unreserved lane worker, else an open-lane worker, else a reserved one whose exclusive run is later in the queue (FR-314).
- **Launched worker before adoption**: its picked lanes wait on its attach ticket. It is not on the roster, so it is not dispatched to. If its host stays unreachable past the ticket's 24-hour expiry, it is adopted with *lanes pending* and flagged, never silently into the open lane (FR-306b, L3).
- **Orchestrator daemon restarts**: lanes and memberships are persisted. Until each host reconnects, its workers count as unreachable: not free, but still counted (FR-308, L1).

## Lifecycle (reference for the FRs)

This section walks through the whole life of lanes, workers and runs. Each step names the requirements it relies on, and the requirements must agree with it.

**What is kept where.**
- **The orchestrator's record** is persisted, and survives restarts (FR-322). It holds the lanes, the roster (each worker's lanes, and a *lanes pending* flag), each action's lane setting, the open attach tickets and the dropped worker tags.
- **Each queued run** carries a copy of its deleted action's lane setting, when it has one (FR-314b).
- **The orchestrator's daemon** also keeps, in memory only, the last-known record of each worker's sandbox and whether each host's link is up.
- **Hosts** hold only display copies of lanes (FR-309b).

**L1. The orchestrator's daemon restarts.**
- The record is loaded. Any worker tag found is dropped and recorded (FR-318b).
- The roster is the persisted one, lanes included. Nothing about lanes has to be rebuilt.
- The cache of sandbox records starts empty, so every roster worker counts as **live but unreachable** until its host reconciles (FR-308). Live but unreachable means:
  - it is not free, so it is never dispatched to;
  - it still counts as an open-lane worker or a lane worker for the open-lane safeguard (FR-308a);
  - a run that only it could take says "stopped or unreachable" (FR-315).
- Expired attach tickets are swept (FR-306b).

**L2. Adoption and drops.** These run when a host's link comes up (reconcile), **and** on every sandbox-changed event from a connected host. Both hold the reconcile lock, as reconcile does today.
- **Workers that are gone:** a roster worker on that host whose sandbox is no longer this orchestrator's, according to the host's listing or a sandbox-changed event, is dropped together with its lanes. That is the evidence that ends its place on the live roster. Today an event only updates the cache; this feature makes it drop the roster entry too, so a later re-attach starts clean.
- **Sandboxes to adopt:** a sandbox that names this orchestrator but is not on the roster is adopted in the same write, as follows:
  - With **no attach ticket**: it joins the open lane, as today.
  - With a **valid ticket for this host** (and this sandbox, if the ticket names one): it joins with the ticket's lanes, and the ticket is consumed.
  - With an **unknown, expired or mismatched ticket**: it joins with **lanes pending**. It is not dispatchable and is flagged on the Workers view until the developer sets its lanes, or chooses the open lane (FR-306b).
- The cache is filled from the listing or the event. Then a dispatch pass runs.
- An event that names this orchestrator for a sandbox not on the roster adopts it at once. A launched worker on a connected host therefore joins within moments of its launch, not at the next reconnect. Today it waits for the next reconnect or an explicit attach.

**L3. Launch with lanes picked** (launch wizard, orchestrator chosen).
1. The client asks the orchestrator for the worker bundle, sending the picked lanes and the target host. The orchestrator checks the lanes, writes an attach ticket (the lanes, bound to that host, expiring after 24 h) to its record, and returns its identifier with the bundle (FR-306b).
2. The target host launches the sandbox with its controller record carrying the ticket identifier.
3. The sandbox is not on the roster, so dispatch cannot reach it (dispatch only offers roster workers). The launch's sandbox-changed event, or the next reconcile if the host's link is down, adopts it under L2 and consumes the ticket.
4. If the launch fails, the ticket is never presented, and it is swept when it expires.
5. If the host stays unreachable for more than 24 h, the ticket expires, and adoption gives *lanes pending*, never the open lane.

**L4. Attach with lanes picked.**
1. The orchestrator writes an attach ticket bound to the host and this sandbox, holding the picked lanes by identity.
2. It links the sandbox, setting its controller with the ticket identifier, as today.
3. It then commits under the reconcile lock (FR-306a). It creates the roster entry if needed and sets the entry's lanes from the ticket's *current* lanes (lane deletes may have stripped some), consuming the ticket. This keeps today's order, link then commit.
4. **If an adoption runs between steps 2 and 3,** it adopts the sandbox through the ticket under L2, with the right lanes, and consumes the ticket. Step 3 then finds the ticket already consumed and the entry already holding its lanes.
5. **If the link is definitely refused** (for example, already controlled), the attach deletes its ticket. If the outcome is unknown (a timeout), or the link succeeds but the commit fails, the ticket is kept. Adoption then applies it, or it expires into *lanes pending*.
6. Attaching with no lanes picked issues no ticket, and the worker joins the open lane, as today.

**L5. Detach.**
- **Through the orchestrator's daemon:** the guard (FR-308a) is evaluated before the owning daemon is asked to unlink, while a lock serialises it against every other guarded change on this orchestrator. The roster entry, and its lanes, go. Runs on the worker fail as today.
- **Directly, through the sandbox's own daemon** (while the orchestrator's daemon is not connected): the client warns on a best-effort basis (FR-308b). The orchestrator learns of it from the sandbox-changed event or the next reconcile. That is the evidence that ends its place on the live roster and drops its roster entry and lanes (L2).

**L6. Dispatch pass** (FR-314). This covers `prompt` runs. `reason` runs go to the reasoning sandbox as today, outside lanes, reservations and waiting reasons.
- **Free workers** are live roster workers that are not reasoning, not *lanes pending*, have a record that is running and idle with no run, and are on a host whose link is up.
- **Effective lane setting** of each queued run: its action's current setting, or its kept copy if the action was deleted (FR-314b).
- **Reserve:** walk the queued exclusive runs in queue order. Each reserves one free worker in its lanes that is not already reserved. It prefers the worker with the fewest lanes outside the run's own lanes, then roster order, which leaves multi-lane workers for runs that need them.
- **Deliver:** walk all queued runs in queue order. Each run tries its candidates in order (below). For each candidate, the worker's latest record is re-checked and delivery is attempted, as today. A worker that fails the re-check, or refuses delivery because it is not free after all (a precondition refusal, as today), leaves the pass, and any reservation on it is released. The run then tries its next candidate. Any other delivery error fails the run, as today, and the worker leaves the pass. Delivery claims the worker. A run that is delivered elsewhere, or tries every candidate without success, releases any reservation it still holds.
- **Candidates:**

| Run | Candidates, in order |
|-----|---------------------|
| Exclusive | (1) its own reservation; (2) a free worker in its lanes that nobody has reserved; (3) a free worker in its lanes reserved for a run *later* in the queue, whose run then loses the reservation |
| Inclusive | (1) a free worker in its lanes that nobody has reserved; (2) a free open-lane worker; (3) a free worker in its lanes reserved for a later run, as above |
| Un-laned | a free open-lane worker |

- **Two properties follow:**
  - At the end of a pass, no run is still queued while a free worker it could take, which accepts delivery, is unclaimed.
  - A run never waits for a run later in the queue.

**L7. Lane and action changes.**
- Any change to a lane, a worker's lanes or an action's lanes starts a dispatch pass (FR-314a).
- Deleting a lane removes it from workers, actions, the kept settings on queued runs, and open attach tickets (FR-305).
- Deleting an action copies its lane setting onto each of its queued runs, in one atomic change covering the record and the runs (FR-314b).

**L8. A run leaves the queue.** Its kept copy goes with it. No separate clean-up is needed.

## Requirements *(mandatory)*

### Functional Requirements

#### Lanes

- **FR-301**: The orchestrator detail MUST have a fourth view, **Lanes**, alongside Workers, Reasoning and Hosts, reached the same way as the other views. It MUST list every lane on the orchestrator with its name, description, member workers and the actions that use it (and whether each is exclusive or inclusive).
- **FR-302**: A developer MUST be able to create a lane from the Lanes view with a name and an optional description. A description MUST be a single line of at most 200 characters. An orchestrator MUST have at most 32 lanes; creating one more is refused with the reason. Lanes belong to one orchestrator.
- **FR-303**: A lane name MUST be 1–64 characters of lowercase letters, digits and hyphens, starting with a letter or digit (the action-name rule). It MUST be unique within the orchestrator. The name `open` is reserved, because it would read as the open lane. A refused name MUST say why.
- **FR-304**: A developer MUST be able to rename a lane and change its description. A rename MUST keep the membership of every worker, every action and every queued run that kept lanes (FR-314b), and MUST NOT change which runs go where.
- **FR-305**: A developer MUST be able to delete a lane. When workers or actions use it, a confirm MUST list them first and say which actions will be left with no lanes and so run on open-lane workers only. If those actions would have no open-lane worker, the confirm MUST say so as well, as FR-308a's guard requires. The confirm MUST also count the queued runs whose routing changes, including runs of deleted actions that kept the lane (FR-314b). On confirm, the lane MUST be removed from every worker, action, kept setting on a queued run and open attach ticket (L7); on cancel nothing changes. An unused lane is deleted after a plain confirm.

#### Worker assignment and display

- **FR-306**: The Workers view MUST offer, on a worker row, a lane picker that sets that worker's lanes (none, one or many) from the orchestrator's lanes. With no lanes on the orchestrator, the picker MUST say so and offer to create one.
- **FR-306a**: Every way a sandbox becomes a worker of the orchestrator MUST let the developer pick its lanes in the same step. This covers attaching from the sandbox list and launching with the orchestrator chosen in the launch wizard. Attach MUST keep today's order: link the sandbox, then commit its roster entry under the reconcile lock. When lanes are picked, the link MUST carry an attach ticket (FR-306b). The commit MUST create the entry if needed and set its lanes from the ticket's current lanes, consuming the ticket. An adoption that runs between the link and the commit therefore adopts the sandbox with the right lanes, and the commit finds the ticket consumed. A sandbox is never dispatched to before it is on the roster, because dispatch only offers roster workers (L4).
- **FR-306b**: A worker MUST NOT be dispatched to before the lanes picked for it apply. Picked lanes reach the orchestrator's adoption through an **attach ticket**:
  - **Issue.** A ticket is issued only when lanes are picked. For a launch it is issued with the worker bundle and bound to the target host. For an attach it is bound to the host and the sandbox.
  - **Storage and expiry.** Tickets are stored on the orchestrator's record and hold lanes by identity. They are single-use and expire 24 hours after issue, and expired tickets are swept. An attach whose link is definitely refused deletes its ticket. One whose outcome is unknown, or whose commit fails, keeps it (L4).
  - **Lane deletes.** Deleting a lane removes it from open tickets (FR-305).
  - **Adoption** (L2), on reconcile or on a sandbox-changed event, of a sandbox not on the roster:
    - no ticket: the open lane;
    - a valid ticket bound to its host (and to the sandbox, if the ticket names one): the ticket's lanes, consuming the ticket in the same write;
    - an unknown, expired or mismatched ticket: the worker is adopted with **lanes pending**. Such a worker is not dispatchable and not an open-lane worker, and the Workers view flags it until the developer sets its lanes or chooses the open lane. Any membership save that sets its lanes, from the picker or a lane's editor, clears the flag.
  - **Not lane data.** The ticket identifier is the orchestrator's own token, not lane data, so this does not breach FR-309b. Presenting another host's ticket gets only *lanes pending*.
- **FR-307**: A lane's editor in the Lanes view MUST let the developer change the lane's member workers. It MUST edit the same assignment as FR-306.
- **FR-307a**: A membership save made against stale data MUST be refused with the reason, and the picker MUST reload. One save MUST NOT silently undo another. A worker's picker (FR-306) is refused only if **that worker's** lanes or *lanes pending* flag have changed since it opened, or a lane it ticks has been deleted. A lane's editor (FR-307) is refused only if **that lane's** members have changed, or the lane has been deleted. Unrelated changes never refuse a save.
- **FR-308**: A worker MAY be in any number of the orchestrator's lanes. A non-reasoning worker in no lane that is not *lanes pending* (FR-306b) is an **open-lane worker**, said to be in the **open lane**. The reasoning sandbox is never an open-lane worker. A worker that is detached leaves all its lanes. The **live roster** is the set of workers that counts for routing (FR-311–FR-314), for the open-lane safeguard (FR-308a) and for waiting reasons (FR-315). A roster worker MUST leave it only on evidence: its last-known sandbox record names another controller or none, or an adoption-and-drop (L2) removes it from the roster. Both happen on reconcile and on sandbox-changed events. A worker whose record is not yet known stays on the live roster as **unreachable**, for example after the orchestrator's daemon restarts and before its host reconnects. An unreachable worker is never free, but it still counts as an open-lane or lane worker (L1).
- **FR-308a**: The orchestrator's daemon MUST guard every change made through it that would either drop the live roster's open-lane workers from at least one to zero while the orchestrator has un-laned `prompt` actions, or save an un-laned `prompt` action (including clearing an action's lanes) while it has none. Queued un-laned runs of deleted actions (FR-314b) count as un-laned work too, so the guard lists them alongside the actions. The guarded changes are membership saves (one worker or several), action saves, lane deletes that leave an action un-laned, and detaching through the orchestrator. The guard has one shape:
  - **Check.** A *check* request returns the un-laned actions the change would affect, and changes nothing.
  - **Refuse or apply.** The change itself is refused with that same list unless it is confirmed. The daemon evaluates the guard and makes the change while holding a lock that serialises every guarded change on the orchestrator. For a detach, the guard is evaluated before the owning daemon is asked to unlink (L5).
  - **Confirm.** A confirmed request carries the list the developer was shown. It is accepted only when the current list is within it; otherwise it is refused with the current list.
  - **The client** fetches the list first and shows it in the change's single confirm. A detach folds it into the detach confirm it already shows. A second confirm appears only when the situation changed while the first was open.
- **FR-308b**: Changes the orchestrator's daemon cannot check are made visible instead of guarded. When the developer detaches directly, through the sandbox's own daemon while the orchestrator's daemon is not connected, the detach confirm MUST warn from the client's last-known roster and actions if the detach would leave no open-lane worker. If the client has no last-known data for that orchestrator, the confirm MUST say it cannot check open-lane workers. Reconcile drops, sandbox removal and hosts going away are not warned about. Runs left with no worker able to take them show why through FR-315.
- **FR-309**: A worker's lanes MUST be shown on its Workers-view row and on its sandbox-list row, wherever the list shows it: the Orchestrator Sandboxes tab, and any other tab while it needs the developer. A worker in no lane MUST read as being in the open lane in the same places, and a *lanes pending* worker MUST read as *lanes pending*. The reasoning sandbox and sandboxes that are not workers show no lane information. Lane names MUST be sanitised for the terminal wherever they are shown.
- **FR-309a**: A worker's lanes, and its *lanes pending* flag, MUST reach the daemon that runs it, the same way the orchestrator's name does, so the sandbox list shows them whichever host runs the worker. A membership change MUST be pushed only to the workers it changes, in the background and coalesced, never holding up the save. The push a host gets when its link reconnects MUST carry its workers' current lanes. While that host is unreachable, routing uses the orchestrator's record, and the list on that host shows the last-known lanes until the change is pushed.
- **FR-309b**: The orchestrator's roster MUST be the only source of lane membership. Lanes pushed to a worker's host are for display only and MUST never be read back into the roster or used for routing. A worker that reconcile adopts and that is not already on the roster MUST start in the open lane, unless its attach ticket says otherwise (FR-306b, L2). A worker already on the roster keeps its lanes. A daemon receiving lanes for display MUST drop, and log, any name that breaks FR-303's rule, and MUST still accept the rest of the push, including the orchestrator's name.
- **FR-310**: The reasoning sandbox MUST NOT be assignable to a lane and MUST NOT be offered in any lane picker.

#### Routing

- **FR-311**: A run of a `prompt` action **exclusive** to one or more lanes MUST be delivered only to a worker in at least one of those lanes.
- **FR-312**: A run of a `prompt` action **inclusive** to one or more lanes MUST be delivered only to a worker in at least one of those lanes or to an open-lane worker.
- **FR-313**: A run of a `prompt` action with **no lanes** MUST be delivered only to an open-lane worker (FR-308). Apart from that, it is delivered exactly as before this feature.
- **FR-314**: This covers `prompt` runs; `reason` runs go to the reasoning sandbox as today, outside lanes, reservations and waiting reasons. Eligibility MUST be decided at dispatch, from the live roster and each run's effective lane setting (FR-314b) at that moment. Runs are offered workers in queue order, and the free-worker rules are unchanged. A *free* worker here is never reasoning or *lanes pending*. Each pass MUST work as Lifecycle L6 describes:
  - **Reserve.** Walk the queued exclusive runs in queue order. Each reserves one free worker in its lanes that is not reserved yet, preferring the worker with the fewest lanes outside the run's own lanes, then roster order.
  - **Deliver.** Walk all queued runs in queue order, each trying its candidates in order.
    - An **exclusive** run tries (1) its reservation, (2) a free unreserved worker in its lanes, then (3) a free worker in its lanes reserved for a run later in the queue, which loses that reservation.
    - An **inclusive** run tries (1) a free unreserved worker in its lanes, (2) a free open-lane worker, then (3) as for exclusive.
    - An **un-laned** run takes a free open-lane worker.
  - **Failed attempts.** A worker that fails the pre-delivery re-check, or refuses delivery with a precondition refusal (not free after all, as today), leaves the pass, and its reservation is released. The run moves to its next candidate. Any other delivery error fails the run, as today, and the worker leaves the pass.
  - **Releasing reservations.** A run delivered elsewhere, or out of candidates, releases any reservation it still holds.
  - **Outcomes.** At the end of a pass, no run is still queued while a free, accepting worker it could take is unclaimed, and no run waits for a run later in the queue.
  - **Cost.** A pass MUST cost O(Σ lanes per queued run + Σ lane memberships of free workers), with each capped at 32 by FR-302. There is no per-run lookahead.
- **FR-314a**: Any change to a lane, to a worker's lanes or to an action's lanes MUST start a dispatch pass, so a queued run that has become deliverable is delivered without waiting for an unrelated event.
- **FR-314b**: A queued run MUST follow its action's current lane setting. When an action is deleted, its lane setting MUST be copied onto each of its runs queued at that moment, in one atomic change covering the record and those runs. That means its lanes (by lane identity, so renames follow, FR-304) **and** its mode. Those runs MUST be routed by their copy, which wins over a new action later saved under the same name. A copy leaves with its run, so it needs no separate clean-up. Deleting a lane MUST also remove it from these copies (FR-305), and a run left with no lanes is un-laned.
- **FR-315**: For `prompt` runs, a queued run MUST say why it is waiting when no live-roster worker (FR-308) could take it under FR-311–FR-313, or when every live-roster worker that could is stopped or unreachable. The reason is shown wherever queued runs are shown and to an agent checking the run. The run stays queued. Nothing shows this today, so it is new.
  - An exclusive run with no worker in any of its lanes is "waiting for a worker in <lanes>".
  - An inclusive run with no worker in its lanes and no open-lane worker is "waiting for a worker in <lanes> or an open-lane worker".
  - An un-laned run with no open-lane worker is "waiting for an open-lane worker".
  - A run whose only possible workers are all stopped or unreachable is "waiting for <workers>, stopped or unreachable".
  - When any worker is *lanes pending*, each of these reasons also says how many workers are awaiting lane assignment, because setting their lanes may unblock the run.
  - The reason MUST be worked out whenever runs are read, like queue position, and never stored. It therefore follows attaches, detaches and lane changes at once, including those that start no dispatch pass.
  - The reason MUST be kept apart from a run's failure or cancellation reason.
  - Working it out MUST cost no more than one count of workers per lane for each read, not a scan of every worker for every run.

#### Actions

- **FR-316**: The action form MUST let the developer pick any number of the orchestrator's lanes for a `prompt` action. With at least one lane picked, the developer MUST choose **exclusive** or **inclusive**, preselected to exclusive. With none picked there is no mode. `reason` actions MUST NOT be offered lanes, and saving a `reason` action with lanes MUST be refused. Switching an action from `prompt` to `reason` in the form MUST clear its lanes and say so.
- **FR-316a**: An action's lanes MUST be saved by lane identity, so a rename elsewhere does not break them. An action save naming a lane that no longer exists MUST be refused with the reason, and the form MUST reload its lane choices, keeping every other value. An action MUST never lose its lanes silently because a lane was deleted while its form was open.
- **FR-317**: The action form MUST offer **Manage lanes**, which opens a dialog with the same lane list and the same create, edit, member and delete operations as the Lanes view, for the action's orchestrator. Lane changes made there MUST be saved when made, not when the action is saved. When the dialog closes, the form's lane choices MUST reflect them (a deleted lane is no longer picked, a renamed one shows its new name), and every other value in the form MUST be intact.
- **FR-318**: The action **worker tag** MUST be removed: from the action form, from what is saved, and from every description of an action. An action saved with a worker tag before this feature MUST load with no lanes and no worker tag, and MUST NOT fail to load. The stored worker-tag setting is retired and MUST never be reused for another meaning.
- **FR-318a**: A client that knows about lanes MUST mark each action save as one that sets lanes, even when it sets none. An action save without that mark comes from an older client, and MUST be refused with a message to update the client. An older client therefore cannot silently clear an action's lanes. The reverse also holds. Against a daemon that does not support lanes, a client MUST NOT offer lane editing and MUST refuse action saves, saying that the daemon needs updating. Its action form has no worker-tag field, so a save would silently erase the action's worker tag on a daemon that still routes by it.
- **FR-318b**: Whenever the daemon loads a worker tag, whether on the first load after upgrade or after a downgrade and re-upgrade, it MUST drop the tag, rewrite the orchestrator record, log the action and its dropped tag, and add it to the list of dropped tags persisted on the orchestrator record. The TUI MUST show that list as a notice in the orchestrator detail and on Jobs › Actions until a developer dismisses it. Dismissing clears the list on the daemon, so every client stops showing it.
- **FR-319**: The sandbox tag MUST stay a plain purpose label, settable and shown as today, and MUST NOT affect dispatch.
- **FR-320**: Jobs › Actions MUST show each action's lanes and mode. An action with no lanes shows none.
- **FR-321**: The orchestrator's briefing to its workers and its list-actions tool MUST describe an action's lane names and mode instead of a worker tag, so an agent calling an action can tell where it will run. Lane descriptions MUST NOT be sent to agents. The briefing keeps every refresh it gets today: action added, changed or deleted, orchestrator renamed, worker attached, and host reconciled. It MUST also be refreshed when a lane an action uses is renamed or deleted. Membership and lane-description changes alone MUST NOT refresh it. Refreshes MUST run in the background and be coalesced, never holding up the operation that triggered them. This deliberately changes today's push during a save or rename, which holds the operation up, into a background one.

#### General

- **FR-322**: Lanes, worker assignments and *lanes pending* flags, action lanes, kept settings on queued runs, open attach tickets and the dropped-tag list MUST persist across daemon restarts and orchestrator reloads.
- **FR-323**: The new forms, pickers and dialogs MUST follow the app's existing form conventions, including its keys and its unsaved-changes guard where forms have one.

### Key Entities

- **Lane**: a named group of workers on one orchestrator. It has a name (unique within the orchestrator, `open` reserved), an optional single-line description of at most 200 characters, and a stable identity that survives renames. Workers, actions and kept runs refer to it by that identity. It is owned by the orchestrator and deleted with it.
- **Worker lane membership**: which lanes a worker is in (zero or more). Zero means the open lane, unless the worker is *lanes pending*. It belongs to the worker's place on the orchestrator's roster, which is its only source (FR-309b). It is copied to the daemon that runs the worker for display only, and is dropped when the worker is detached. A worker adopted through an expired, unknown or mismatched attach ticket is *lanes pending* until the developer sets its lanes (FR-306b).
- **Action lane setting**: an action's lanes (zero or more) and, when there is at least one, its mode: exclusive or inclusive. Only `prompt` actions have one. When an action is deleted it becomes a kept lane setting (FR-314b).
- **Waiting reason**: why a queued run cannot be taken by any live-roster worker, or why every worker that could take it is stopped or unreachable (FR-315). It is derived when runs are read, never stored, and separate from a run's failure or cancellation reason.
- **Open lane**: not a stored lane. It means "a non-reasoning worker with no lanes" (FR-308).
- **Live roster**: the roster workers with no evidence that they are gone (FR-308). Evidence arrives on reconcile and on sandbox-changed events (L2). A worker whose record is not yet known counts as unreachable. It is what routing, the open-lane safeguard and waiting reasons count.
- **Attach ticket**: a single-use token the orchestrator issues when lanes are picked for a sandbox being launched or attached. It holds those lanes, is bound to the host (and, on attach, the sandbox), is stored on the orchestrator record, holds lanes by identity, is consumed by the attach commit or by adoption, whichever comes first, and expires after 24 hours (FR-306b).
- **Kept lane setting**: a deleted action's lane setting, copied onto each run queued when it was deleted. It leaves with the run (FR-314b).
- **Dropped worker tags**: the list of worker tags dropped on upgrade, persisted until dismissed (FR-318b).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In a test of 100 dispatched runs across exclusive, inclusive and un-laned actions, every run lands on a worker its lane rules allow (FR-311–FR-313). Zero runs land on a disallowed worker.
- **SC-002**: In dispatch tests mixing exclusive, inclusive and un-laned runs over lanes with several workers, including workers that refuse delivery and workers in several lanes, at the end of every pass no run is still queued while a free, accepting worker it could take is unclaimed. No run waits for a run later in the queue. An inclusive run takes a lane worker that a later exclusive run reserved only when no other worker it could take is free (FR-314). Holds in 100% of cases.
- **SC-003**: Starting from the action form, a developer can create a lane, put a worker in it and make the action exclusive to it in under 2 minutes, using Manage lanes and without leaving the form. Starting from the orchestrator detail, the same takes under 2 minutes using the Lanes view and then the action form.
- **SC-004**: While the worker's host is reachable, a worker's lanes appear on its Workers row and its sandbox-list row within one refresh of saving the change.
- **SC-005**: After upgrade, 100% of orchestrators saved with worker tags load without error, and their formerly tagged actions run on open-lane workers.
- **SC-006**: A developer looking at a stuck queued run can tell from the queue alone why it waits: which lane it needs, or which stopped or unreachable workers it is waiting for (FR-315).
- **SC-007**: After a worker is added to a lane, a queued run waiting for that lane is delivered within one dispatch pass, with no other action needed (FR-314a).

## Assumptions

- Lane naming follows the action-name rule, and names are unique per orchestrator. Lanes are not shared between orchestrators.
- Inclusive runs prefer unreserved lane workers, then open-lane workers, then lane workers reserved for later runs (FR-314). The ticket allows any order; this one keeps open-lane workers for un-laned work without starving exclusive work, and never makes an earlier run wait for a later one. Reservation is greedy, but the fallback chain means no run is left queued while a worker it could take is free and unclaimed (L6).
- Planning note: the registry has no single change spanning the orchestrator record and its runs today. FR-314b needs one, which the underlying store supports.
- **Lanes are routing, not access control.** Any worker's agent can call any action, an exclusive one included, and can check any run of its orchestrator, including its waiting reason, which names lanes. Lanes decide only where a run is delivered. They are not an isolation boundary, and lane names are not secret.
- Planning note: the sandbox list redraws a row only when a displayed field changes, so lanes must join that change check for SC-004.
- An exclusive or un-laned run that no worker can take stays queued, as worker-tagged runs do today, rather than failing.
- Lane changes from the Manage lanes dialog are saved immediately, like changes in the Lanes view, because lanes belong to the orchestrator and not to the action being edited.
- A detached worker forgets its lanes. Re-attaching starts it in the open lane, unless lanes are picked as it is attached (FR-306a).
- Queued runs re-route when their action's lanes change, rather than keeping the lanes they were called with. This follows from lanes being checked at dispatch (FR-314).
- The unsaved-changes guard in FR-323 depends on SWI-9 (PR #19). If that has not merged when this is implemented, the new forms behave like the existing forms.
- Existing worker tags are not converted to lanes (clarified). Developers who used worker tags recreate the grouping with lanes.

## Out of Scope

- Lanes for `reason` actions or the reasoning sandbox.
- Weighting, capacity limits or priorities between lanes beyond FR-314's preference.
- Converting existing worker tags or sandbox tags into lanes.
- Removing or changing the sandbox tag itself.
- Lanes shared across orchestrators.
