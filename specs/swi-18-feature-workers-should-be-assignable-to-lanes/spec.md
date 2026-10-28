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
- Any change to lanes, a worker's lanes or an action's lanes starts a dispatch pass and refreshes the workers' briefings (FR-314a, FR-321).
- A queued run follows its action's current lanes. If the action is deleted, the run keeps the lanes the action last had, even if a new action is later saved under the same name. A later lane delete removes the lane from such runs too (FR-314b, FR-305).
- A save that would leave no open-lane worker while un-laned `prompt` actions exist warns first (FR-308a).
- "Waiting for a lane" is new: today a queued run shows no reason it is waiting. A worker counts as able to take a run if it is attached, whatever its state (FR-315).
- Lane-aware clients mark their action saves, and an unmarked save, which can only come from an older client, is refused. An older client therefore can't silently clear an action's lanes (FR-318a). Membership saves made against a lane list that has changed since the picker opened are refused (FR-307a).

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
6. **Given** a queued run of an exclusive action and no worker in any of its lanes, **When** the developer views the queue, **Then** the run says it is waiting for a worker in the named lanes, and an agent checking the run sees the same reason (FR-315).

---

### User Story 2 - Let lane workers help with a kind of work (Priority: P1)

A developer marks the `review-pr` action **inclusive** to the `reviewers` lane. Its runs go to a free worker in `reviewers` when there is one, and otherwise to any free open-lane worker, so reviews never wait just because the reviewer workers are busy.

**Why this priority**: Inclusive is the second of the two modes the ticket asks for. It lets lane workers take extra work without making that work wait for them.

**Independent Test**: Make one action inclusive to a lane with one worker. Call it while that worker is free, and again while it is busy. The first run goes to the lane worker, and the second goes to an open-lane worker.

**Acceptance Scenarios**:

1. **Given** an action inclusive to `reviewers`, a free worker in `reviewers` and a free open-lane worker, **When** the action is called, **Then** the run goes to the worker in `reviewers` (FR-312, FR-314).
2. **Given** an action inclusive to `reviewers`, no free worker in `reviewers`, and a free open-lane worker, **When** the action is called, **Then** the run goes to the open-lane worker (FR-312).
3. **Given** an action inclusive to `reviewers` and a free worker that is only in an unrelated lane `frontend`, **When** the action is called, **Then** the run is not delivered to that worker (FR-312).

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
6. **Given** one open-lane worker and an un-laned `prompt` action, **When** the developer puts that worker in a lane and saves, **Then** a warning says no open-lane worker will be left to run un-laned actions, and names them. The save goes ahead only once the developer confirms (FR-308a).
7. **Given** a queued run of an action exclusive to `frontend` and no worker in `frontend`, **When** the developer adds a free worker to `frontend`, **Then** the run is delivered to it without any other change (FR-314a).

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
- **Worker detached from the orchestrator**: it leaves all its lanes. If it is attached again, it starts in the open lane (FR-308).
- **Worker on another host**: lanes are the orchestrator's, so a worker on any of the orchestrator's hosts can be in any of its lanes. While a host is unreachable its workers keep their lanes; they just aren't free.
- **Deleting the last lane of an inclusive action**: the action becomes un-laned and runs on open-lane workers only. Before, it could also run on lane workers. The delete confirm says so for each affected action, and counts the queued runs that will be re-routed (FR-305).
- **Taking the last open-lane worker**: un-laned actions, which is every action right after upgrade, would wait until an open-lane worker exists again. The save warns first (FR-308a).
- **Timed event on an action no worker can take**: with dedupe on (the default), it skips while its run is queued. With dedupe off, it queues a run every interval, each showing why it waits (FR-315). Queuing every interval is unchanged from worker tags today; the waiting reason is new.
- **Lane worker stopped or on an unreachable host**: it still counts as able to take the run, so FR-315's reason is not shown. The run waits for it, as it would for any worker that isn't free.
- **Remote worker's host unreachable when its lanes change**: the orchestrator routes by its own record at once. The sandbox list on the worker's host shows the last-known lanes until the host is reachable again and the change is pushed (FR-309a).
- **All workers are in lanes**: actions with no lanes wait in the queue until an open-lane worker exists. The queue shows that they are waiting for an open-lane worker (FR-315).
- **Lane picker or lane editor open while lanes change elsewhere** (another TUI, the other picker, or the Manage lanes dialog): a save made against a lane list or membership that has changed since it opened is refused with the reason, and the picker reloads, so one save never silently undoes another (FR-307a).
- **Action deleted while its runs are queued**: the runs still run, as before this feature, and keep the lanes the action last had (FR-314b).
- **Agent-triggered and timed calls**: they follow the same lane rules as developer calls, because lanes belong to the action, not the caller.

## Requirements *(mandatory)*

### Functional Requirements

#### Lanes

- **FR-301**: The orchestrator detail MUST have a fourth view, **Lanes**, alongside Workers, Reasoning and Hosts, reached the same way as the other views. It MUST list every lane on the orchestrator with its name, description, member workers and the actions that use it (and whether each is exclusive or inclusive).
- **FR-302**: A developer MUST be able to create a lane from the Lanes view with a name and an optional description. Lanes belong to one orchestrator.
- **FR-303**: A lane name MUST be 1–64 characters of lowercase letters, digits and hyphens, starting with a letter or digit (the action-name rule). It MUST be unique within the orchestrator. A refused name MUST say why.
- **FR-304**: A developer MUST be able to rename a lane and change its description. A rename MUST keep every worker's and action's membership, and MUST NOT change which runs go where.
- **FR-305**: A developer MUST be able to delete a lane. When workers or actions use it, a confirm MUST list them first and say which actions will be left with no lanes and so run on open-lane workers only. The confirm MUST also count the queued runs whose routing changes, including runs of deleted actions that kept the lane (FR-314b). On confirm, the lane MUST be removed from every worker, action and such run; on cancel nothing changes. An unused lane is deleted after a plain confirm.

#### Worker assignment and display

- **FR-306**: The Workers view MUST offer, on a worker row, a lane picker that sets that worker's lanes (none, one or many) from the orchestrator's lanes. With no lanes on the orchestrator, the picker MUST say so and offer to create one.
- **FR-307**: A lane's editor in the Lanes view MUST let the developer change the lane's member workers. It MUST edit the same assignment as FR-306.
- **FR-307a**: A membership save (FR-306, FR-307) made against lanes or memberships that have changed since the picker opened MUST be refused with the reason, and the picker MUST reload. One save MUST NOT silently undo another.
- **FR-308**: A worker MAY be in any number of the orchestrator's lanes. A worker in no lane is in the **open lane**. A worker that is detached leaves all its lanes.
- **FR-308a**: A save that would take the orchestrator's last open-lane worker while it has un-laned `prompt` actions MUST warn first, naming those actions, and MUST go ahead only on confirm.
- **FR-309**: A worker's lanes MUST be shown on its Workers-view row and on its sandbox-list row, wherever the list shows it: the Orchestrator Sandboxes tab, and any other tab while it needs the developer. A worker in no lane MUST read as being in the open lane in the same places. The reasoning sandbox and sandboxes that are not workers show no lane information.
- **FR-309a**: A worker's lanes MUST reach the daemon that runs it, the same way the orchestrator's name does, so the sandbox list shows them whichever host runs the worker. While that host is unreachable, routing uses the orchestrator's record, and the list on that host shows the last-known lanes until the change is pushed.
- **FR-310**: The reasoning sandbox MUST NOT be assignable to a lane and MUST NOT be offered in any lane picker.

#### Routing

- **FR-311**: A run of an action **exclusive** to one or more lanes MUST be delivered only to a worker in at least one of those lanes.
- **FR-312**: A run of an action **inclusive** to one or more lanes MUST be delivered only to a worker in at least one of those lanes or to an open-lane worker.
- **FR-313**: A run of an action with **no lanes** MUST be delivered only to an open-lane worker. Apart from that, it is delivered exactly as before this feature.
- **FR-314**: Eligibility MUST be decided at dispatch, from the action's and the workers' lanes at that moment. When an inclusive run can go to both a free worker in a matching lane and a free open-lane worker, it MUST go to the lane worker, which keeps open-lane workers for un-laned work. Apart from lanes, dispatch order and the free-worker rules are unchanged.
- **FR-314a**: Any change to a lane, to a worker's lanes or to an action's lanes MUST start a dispatch pass, so a queued run that has become deliverable is delivered without waiting for an unrelated event.
- **FR-314b**: A queued run MUST follow its action's current lanes. A queued run whose action has been deleted MUST keep the lanes the action had when it was deleted. Those kept lanes win over a new action later saved under the same name. Deleting a lane MUST also remove it from these runs (FR-305), and a run left with no lanes is un-laned.
- **FR-315**: A queued run that no attached worker could take under FR-311–FR-313, whatever those workers' states (no worker in any of its lanes, or no open-lane worker for an un-laned action), MUST say so. It MUST name the lanes it is waiting for, or "an open-lane worker", wherever queued runs are shown and to an agent checking the run. The run stays queued. Nothing shows this today, so it is new.

#### Actions

- **FR-316**: The action form MUST let the developer pick any number of the orchestrator's lanes for a `prompt` action. With at least one lane picked, the developer MUST choose **exclusive** or **inclusive**, preselected to exclusive. With none picked there is no mode. `reason` actions MUST NOT be offered lanes, and saving a `reason` action with lanes MUST be refused. Switching an action from `prompt` to `reason` in the form MUST clear its lanes and say so.
- **FR-317**: The action form MUST offer **Manage lanes**, which opens a dialog with the same lane list and the same create, edit, member and delete operations as the Lanes view, for the action's orchestrator. Lane changes made there MUST be saved when made, not when the action is saved. When the dialog closes, the form's lane choices MUST reflect them (a deleted lane is no longer picked, a renamed one shows its new name), and every other value in the form MUST be intact.
- **FR-318**: The action **worker tag** MUST be removed: from the action form, from what is saved, and from every description of an action. An action saved with a worker tag before this feature MUST load with no lanes and no worker tag, and MUST NOT fail to load.
- **FR-318a**: A client that knows about lanes MUST mark each action save as one that sets lanes, even when it sets none. An action save without that mark comes from an older client, and MUST be refused with a message to update the client. An older client therefore cannot silently clear an action's lanes.
- **FR-319**: The sandbox tag MUST stay a plain purpose label, settable and shown as today, and MUST NOT affect dispatch.
- **FR-320**: Jobs › Actions MUST show each action's lanes and mode. An action with no lanes shows none.
- **FR-321**: The orchestrator's briefing to its workers and its list-actions tool MUST describe an action's lanes and mode instead of a worker tag, so an agent calling an action can tell where it will run. Any lane change that alters this MUST refresh the briefing, as an action change does today.

#### General

- **FR-322**: Lanes, worker assignments and action lanes MUST persist across daemon restarts and orchestrator reloads.
- **FR-323**: The new forms, pickers and dialogs MUST follow the app's existing form conventions, including its keys and its unsaved-changes guard where forms have one.

### Key Entities

- **Lane**: a named group of workers on one orchestrator. It has a name (unique within the orchestrator), an optional description and a stable identity that survives renames. It is owned by the orchestrator and deleted with it.
- **Worker lane membership**: which lanes a worker is in (zero or more). Zero means the open lane. It belongs to the worker's place on the orchestrator's roster, is copied to the daemon that runs the worker for display, and is dropped when the worker is detached.
- **Action lane setting**: an action's lanes (zero or more) and, when there is at least one, its mode: exclusive or inclusive. Only `prompt` actions have one.
- **Open lane**: not a stored lane. It means "a worker with no lanes".

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In a test of 100 dispatched runs across exclusive, inclusive and un-laned actions, every run lands on a worker its lane rules allow (FR-311–FR-313). Zero runs land on a disallowed worker.
- **SC-002**: When both are free, an inclusive run goes to a matching-lane worker over an open-lane worker in 100% of cases (FR-314).
- **SC-003**: Starting from the action form, a developer can create a lane, put a worker in it and make the action exclusive to it in under 2 minutes, using Manage lanes and without leaving the form. Starting from the orchestrator detail, the same takes under 2 minutes using the Lanes view and then the action form.
- **SC-004**: While the worker's host is reachable, a worker's lanes appear on its Workers row and its sandbox-list row within one refresh of saving the change.
- **SC-005**: After upgrade, 100% of orchestrators saved with worker tags load without error, and their formerly tagged actions run on open-lane workers.
- **SC-006**: A developer looking at a stuck queued run can tell from the queue alone that it is waiting for a lane, and which one (FR-315).
- **SC-007**: After a worker is added to a lane, a queued run waiting for that lane is delivered within one dispatch pass, with no other action needed (FR-314a).

## Assumptions

- Lane naming follows the action-name rule, and names are unique per orchestrator. Lanes are not shared between orchestrators.
- Inclusive runs prefer lane workers over open-lane workers (FR-314). The ticket allows either; this choice keeps open-lane workers for un-laned work.
- An exclusive or un-laned run that no worker can take stays queued, as worker-tagged runs do today, rather than failing.
- Lane changes from the Manage lanes dialog are saved immediately, like changes in the Lanes view, because lanes belong to the orchestrator and not to the action being edited.
- A detached worker forgets its lanes. Re-attaching starts it in the open lane.
- Queued runs re-route when their action's lanes change, rather than keeping the lanes they were called with. This follows from lanes being checked at dispatch (FR-314).
- The unsaved-changes guard in FR-323 depends on SWI-9 (PR #19). If that has not merged when this is implemented, the new forms behave like the existing forms.
- Existing worker tags are not converted to lanes (clarified). Developers who used worker tags recreate the grouping with lanes.

## Out of Scope

- Lanes for `reason` actions or the reasoning sandbox.
- Weighting, capacity limits or priorities between lanes beyond FR-314's preference.
- Converting existing worker tags or sandbox tags into lanes.
- Removing or changing the sandbox tag itself.
- Lanes shared across orchestrators.
