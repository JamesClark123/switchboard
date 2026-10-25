# Feature Specification: Orchestrator

**Feature Branch**: `009-orchestrator`

**Created**: 2026-10-12

**Status**: Draft

**Input**: User description: "We want to add a new feature called `Orchestrator`. At its core the
orchestrator is a new special type of daemon that can be spun up with access to many other sandboxes
across any other given machines. Its primary purpose is to act as a prompt queue. It can receive
inbound input from external sources such as GitHub or maybe other agents through the daemon. These
inputs indicate some action that the orchestrator should take (usually running a prompt on a
tethered sandbox) and the orchestrator will process them in the order received as capacity is
available." — followed by the numbered requirements the developer maintains in the **Requirements
Outline** below (extended by the developer on 2026-10-13 and 2026-10-14; the rest of the document
follows it).

## Requirements Outline *(working list — edit freely)*

This is the overview list of what the feature must do, in the developer's own numbering. Each item
points at the formal, testable requirement(s) that spell it out below. **To add a requirement, add
a numbered item here** (keep the `N` / `N.a` / `N.a.i` scheme) and, when ready, a matching `FR-`
entry under *Requirements*. Items marked *(spec author)* were derived from the opening description
rather than the numbered list; adjust or remove them.

1. Sandboxes can be attached to an orchestrator in a controller/worker relationship → FR-107, FR-112
   - 1.a The relationship is unique on the sandbox side: a sandbox is controlled by at most one
     orchestrator at a time → FR-108
   - 1.b Existing sandboxes can be attached to an orchestrator, or removed from it, dynamically →
     FR-109, FR-111
   - 1.c New sandboxes can be attached to an orchestrator during their creation process → FR-110
   - 1.d Sandboxes attached to an orchestrator do not appear in the TUI sandbox lists, except →
     FR-113, FR-116
     - 1.d.i when the sandbox needs input from the user: it then appears, and the list orders
       sandboxes needing input at the top → FR-114
     - 1.d.ii when the user navigates to a new tab in the sandbox group tabs called
       **Orchestrator Sandboxes** — hidden by default, shown only while the user is viewing it,
       reached by a key combination on the TUI home screen → FR-115
   - 1.e The orchestrator can run prompts on attached sandboxes, but only when the sandbox is
     free: not working on a task and not waiting on user input → FR-117, FR-118, FR-119
   - 1.f Sandboxes attached to the orchestrator should get context given to the AI detailing how to use and interact as an orchestrator AI sandbox. The general expected behaviors, what mcp calls are available, the general workflow for the varios types of actions and when and what to do should all be made away to the AI in the sandbox so that it doesn't have to guess. → FR-154, FR-152
2. The orchestrator can queue **actions** and processes them in order → FR-120, FR-123, FR-134
   - 2.a Actions come in multiple formats, discriminated by a `type` field that decides which
     fields the orchestrator expects and validates for that action → FR-121
   - 2.b Actions are pre-composed by the user. The orchestrator gives the user a place to create and edit actions. → FR-135
   - 2.c The first supported type is `prompt`: when processed it runs a prompt on an available,
     supported sandbox → FR-122
   - 2.d Actions are fundamentally a recipe on how to create a prompt for an AI and the requirements for that prompt → FR-136
   - 2.e Actions accept a title/name field to be distinguisable → FR-137
   - 2.f Actions require a body. The body field is the main text that becomes the prompt. The body field can accept placeholder values that match arguments. → FR-138
   - 2.g Actions accept specified named arguments as input. Arguments whos name is used within the body field are filled into those placeholder values. Arguments that do not have placeholder value are appended to the end of the prompt when it is created. → FR-139
   - 2.h Complete actions represent a template telling the orchestrator how to construct the prompt and how to execute it. When an action is called, and arguments supplied, the action should ultimately turn into a prompt that gets run on a sandbox → FR-140
   - 2.i The second supported type is `reason`: when this option is supplied and the action is called it should create a prompt that get's run on the orchestrator's personal sandbox (see 6) → FR-141
   - 2.j When actions are completed they should notify the orchestrator via the mcp gateway with a short summary of what was completed. In instances where the AI might fail to do this (since it is possible) a sandbox that becomes idle should still require a human to look into the sandbox and a manual way to mark the sandbox as free (assuming that if the AI fails to notify of completion status that it would be stuck without human input) → FR-155, FR-156
3. *(spec author)* The orchestrator is its own kind of daemon and reaches sandboxes on any machine
   whose daemon it can connect to → FR-105, FR-106
4. *(spec author)* The developer can see and manage the orchestrator: its workers, its queue, and
   what each action did as well as what machine the orchestrator is placed on → FR-124 … FR-127, FR-131 … FR-133, FR-153
   - 4.a This should live in a new page specifically for orchestrators. Right now we have shift+arrow keys to navigate to the daemon sreen. We should add the orchestrator screen to this navigation system. We need to make it clearer on the TUI that shift + arrow keys is moving between these separate main screens by showing them at the very top of the TUI where is says `switchboard` → FR-142, FR-143
   - 4.b The orchestrator dashboard should support creating and editing actions, creating and editing events, triggering user events and managing any other important aspect of the orchestrator → FR-142, FR-135, FR-129, FR-130, FR-144
   - 4.c The orchestrator dashboard should show the current list of queued actions, the currently processing actions, and the history of completed actions. Actions that have been completed should have a summary of what was done via the AI that was submitted via the mcp gateway → FR-131, FR-125
5. The orchestrator can recieve **events**. Events are a general concept and can be one of many things but fundamentally they are what triggers an action and turns that into a prompt waiting to be run on a sandbox → FR-128
  - 5.a User events - this is when a user manually selects an action, fills out all of the arguments, and runs it/adds it to the queue → FR-129
  - 5.b Timed/Repeatable events - this is an event a user can setup that runs an event (calls an action) every set amount of time. An example would be setting up an event to run a `check-github` action every 5 minutes that checks github issues and triggers AI code reviews → FR-130
    - 5.b.i Timed/Repeatable events need an option to indicate if they should be deduped in the queue. (Meaning if there is a delay in the queue whether a timed event should be added if another copy of it already exists in the queue). The default option should be to dedupe the queue (meaning a timed event should not be readded to the queue if another copy of it is already present). → FR-157
  - 5.c Triggered events - these are events that are triggered progamatically via an mcp call or webhook or something similar. → FR-144
6. The orchestrator has access to it's own personal sandbox that it can use for reasoning → FR-145
  - 6.a The user can optionally specify the setup of this sandbox and any potential folders it needs → FR-146
  - 6.b By default this sandbox should be an empty context to run generic AI reasoning queries → FR-147
7. The orchestrator should be **nameable**. This is to allow users to make it easy to see and remember each orchestrator if there are many → FR-148
8. The sanbox descriptor in the TUI should gain a third line. This line should include: 1. the mcp server list (if any) and 2. the orchestrator it is attached to by name (if any). If neither exists the line should disappear → FR-149
9. The orchestrator is reachable from controlled sandboxes via the mcp gateway. → FR-150
  - 9.a AI sandboxes that are being controlled by an orchestrator need to be able to list all available actions with descriptions and arg requirements and be able to call actions via the mcp gateway → FR-151
  - 9.b AI sanboxes controlled by the orchestrator should get the context required to understand what the orchestrator mcp is and how to use it properly. The AI shouldn't have to do any discovery on what or how to use the orchestrator. → FR-152

## User Scenarios & Testing *(mandatory)*

Today every prompt an agent runs is typed by the developer, one sandbox at a time, in the sandbox
that is in front of them. Work that arrives from elsewhere — an issue to triage, a pull request to
review, a task another agent wants delegated — has to be noticed by a person and relayed by hand.
**Orchestrator** adds a new role to the daemon: an orchestrator is placed on a machine of the
developer's choosing, given a name, and run by that machine's daemon, and its job is to turn such
work into prompts and hand each one to a sandbox that is free to do it. A developer attaches
sandboxes to an orchestrator as **workers**; the orchestrator becomes their **controller**, and
workers step out of the everyday sandbox list until a human is needed. The developer writes
**actions**: reusable recipes with a name, a body, and named arguments that say how to build a
prompt and where to run it. **Events** call actions — the developer by hand, a timer, or a
worker's agent through the orchestrator's MCP tools. Each call becomes a **run**: a composed
prompt, queued in order of receipt and delivered when
capacity allows. Two action types exist: `prompt`, run on a free worker, and `reason`, run on the
orchestrator's own **reasoning sandbox**. A worker's agent is briefed on all of this when it is
attached, and when it finishes a run it **reports completion** through the gateway with a short
summary of what it did; an agent that goes idle without reporting hands its sandbox to the
developer for review instead of being assumed done.

## Clarifications

### Session 2026-10-14

- Q: How is an orchestrator spun up on its machine? → A: **Hosted in-process by the machine's
  existing daemon** — creating an orchestrator adds a persistent record that the daemon runs; no
  new binary, no new process; the TUI reaches it over the connection it already has to that
  machine.
- Q: How does an orchestrator on machine M reach a worker whose daemon is on machine N? → A: **The
  orchestrator's daemon dials worker machines' daemons over SSH**, exactly as the TUI does, using
  M's own SSH keys or agent; host entries for the orchestrator are set up on the Orchestrators
  screen (the TUI can offer its own host list to copy from); worker MCP calls and status ride back
  over that connection.
- Q: Are webhook sources (HTTP intake from outside switchboard) in scope for this feature? → A:
  **Deferred** — triggered events are gateway calls from workers only; admitted sources, webhook
  credentials and idempotency keys are removed and noted as a follow-up.
- Q: For a deduped timed event, does a run that is already in progress count as "a copy in the
  queue"? → A: **Queued or in flight** — a firing is skipped while any run from the event is
  queued, running, awaiting input or awaiting review, so there is never more than one run of a
  deduped event at a time.
- Q: What identifies an orchestrator once it is renamed? → A: **Stable internal identity plus a
  changeable name** — each orchestrator gets an opaque identity at creation; worker records, runs,
  gateway entries and host entries reference it; the name is a label shown everywhere, and
  renaming changes only the label.

### User Story 1 - Create a named orchestrator on a machine (Priority: P1)

*Outline items 3, 4, 4.a, 4.b, 7.* From the sandbox list, shift+→ now cycles **Sandboxes →
Daemons → Orchestrators**, and the strip at the very top of the TUI — where it says `switchboard`
— names the three screens with the current one highlighted, so what the keys do is visible rather
than remembered. On the Orchestrators screen the developer creates an orchestrator: they name it
and pick the machine it is placed on (any machine whose daemon the TUI is connected to). It starts
there, outlives the TUI, and is listed by name with its machine, whether it is reachable, how many
workers it has, and how deep its queue is. Opening it gives the management views — Workers,
Actions, Queue, Events, Reasoning sandbox — the one place every aspect of the orchestrator is
managed: actions and events created and edited, user events triggered, workers detached or marked
free, host entries kept, the reasoning sandbox set up. Removing it, after confirmation,
detaches its workers, destroys its reasoning sandbox, and discards its queue.

**Why this priority**: Everything else needs an orchestrator to exist, to be found by name, and to
be reachable; the screen and header strip are how the developer gets to it.

**Independent Test**: Create an orchestrator named `review-bot` on a remote machine from the TUI,
quit and restart the TUI, confirm it is listed on the Orchestrators screen with that machine and is
reachable, confirm a second `review-bot` is refused, then remove it and confirm the name is free.

**Acceptance Scenarios**:

1. **Given** the sandboxes screen, **When** the developer presses shift+→ twice, **Then** the
   Orchestrators screen is shown, the header strip highlights it, shift+← walks back through
   Daemons to Sandboxes, and each screen's selection is where it was left.
2. **Given** the Orchestrators screen, **When** the developer creates an orchestrator with name N
   on machine M, **Then** N is listed with machine M, reachable, zero workers, an empty queue and a
   default reasoning sandbox, and it is still listed and reachable after the TUI quits and restarts.
3. **Given** an orchestrator named N exists, **When** the developer creates another named N,
   **Then** it is refused and the existing one is named.
4. **Given** an orchestrator whose machine is not connected, **Then** it is listed as unreachable
   with the reason and no operation that cannot work is offered.
5. **Given** an orchestrator with workers, queued runs and a reasoning sandbox, **When** the
   developer removes it and confirms, **Then** its workers are detached and reappear in the
   everyday lists, its reasoning sandbox is destroyed, its queue and history are gone, and its name
   is free.
6. **Given** two orchestrators on two machines, **Then** both are listed with their machines and
   each is reachable through the connection the TUI already has to that machine.

---

### User Story 2 - Attach and detach worker sandboxes (Priority: P1)

*Outline items 1, 1.a, 1.b, 1.c.* On the sandbox list the developer picks an existing sandbox,
chooses *attach to orchestrator*, and selects an orchestrator by name. The sandbox becomes that
orchestrator's worker immediately — no stop, no restart, its agent session untouched — and leaves
the everyday list. Later they detach it the same way, and it reappears. When launching a new
sandbox they can pick an orchestrator in the launch flow, so the sandbox is a worker from the
moment it exists. A sandbox that already has a controller cannot be attached to a second one; the
developer is told which orchestrator holds it.

**Why this priority**: The controller/worker relationship is the foundation the queue builds on
and is independently useful as a way to park sandboxes under an orchestrator's management.

**Independent Test**: With one orchestrator and two sandboxes on different hosts, attach both from
the TUI, confirm each shows the orchestrator as its controller on both hosts' daemons, confirm a
second orchestrator is refused on either, detach one, and confirm it is free to attach elsewhere.

**Acceptance Scenarios**:

1. **Given** a reachable orchestrator and an unattached sandbox, **When** the developer attaches
   the sandbox to it from the sandbox list, **Then** the sandbox's owning daemon records that
   orchestrator as its controller, the sandbox's running state and agent session are unchanged,
   and the orchestrator lists it as a worker.
2. **Given** a sandbox attached to orchestrator A, **When** the developer tries to attach it to
   orchestrator B, **Then** the attach is refused and the message names A as the current controller.
3. **Given** a sandbox attached to orchestrator A, **When** the developer detaches it, **Then** the
   owning daemon clears the controller, the sandbox reappears in the everyday lists, and A no
   longer lists it.
4. **Given** the launch flow, **When** the developer picks an orchestrator before confirming the
   launch, **Then** the new sandbox is a worker of that orchestrator before its agent starts and
   never appears in the everyday lists.
5. **Given** a worker on a machine other than the orchestrator's, **When** either side restarts,
   **Then** the relationship survives: the owning daemon still reports the controller and the
   orchestrator still lists the worker once both are reachable.
6. **Given** a worker whose orchestrator is unreachable, **When** the developer views the sandbox,
   **Then** it still shows its controller (marked unreachable) and the developer can detach it.
7. **Given** a worker on which a run is in flight (running, awaiting input or awaiting review),
   **When** the developer tries to detach it, **Then** the detach is refused with the run named;
   once the run is closed the detach succeeds.
8. **Given** a sandbox on a machine the orchestrator has no host entry for, **When** the developer
   attaches it, **Then** the TUI offers to add that host to the orchestrator (copying its own entry
   for the machine), and the attach completes only once the orchestrator's daemon has reached the
   sandbox's daemon over SSH; if it cannot, the attach is refused with the SSH error.

---

### User Story 3 - Author an action and run it on a free worker (Priority: P1)

*Outline items 2, 2.a–2.h, 2.j, 4.c, 5.a.* In an orchestrator's **Actions** view the developer
creates an action named `review-pr` of type `prompt`, with a description ("Review a pull request
and leave comments"), a body — "Review pull request {number} in {repo} and leave review comments."
— and three named arguments: `number` and `repo` (required, both used in the body) and `focus`
(optional, not used in the body). They run it by hand: a form asks for the three arguments, they
fill them in and submit. The orchestrator composes the prompt — placeholders replaced by the
values, `focus` appended at the end under its name — queues the run, and delivers it to a free
worker that satisfies the action's requirements. When the worker's agent is done it reports
completion through the gateway with a short summary ("Left 3 comments on #412; no blocking
issues"). The **Queue** view shows the run move from *queued* to *in progress* to its place in the
history with that summary; the developer can open any run to see the exact prompt that was sent.
If the agent goes idle without reporting, the run is *awaiting review*: the worker surfaces in the
everyday list, the developer looks into it, and marks it free by hand.

**Why this priority**: Actions and the queue are the orchestrator's purpose. Authoring a recipe
once and calling it with arguments is what makes every event in Story 5 possible.

**Independent Test**: Attach two workers, create the action above, call it three times with
different arguments, and observe: the first two runs start at once on the two workers with
correctly composed prompts, the third starts only when one is reported complete, no worker ever
has two runs, each completed run shows the summary its agent reported, and a run whose agent went
idle without reporting is held for review until the developer marks the worker free.

**Acceptance Scenarios**:

1. **Given** the Actions view, **When** the developer saves an action with a unique name, a type,
   a non-empty body whose placeholders all name declared arguments, and well-formed arguments,
   **Then** it is listed and callable, and it is still there after the orchestrator restarts.
2. **Given** the Actions view, **When** the developer saves an action with an empty body, an
   unknown type, a duplicate name, an argument name used twice, or a placeholder naming an
   undeclared argument, **Then** the save is refused with the problem named.
3. **Given** `review-pr` and a free worker, **When** the developer calls it with `number`, `repo`
   and `focus`, **Then** the composed prompt has both placeholders replaced, `focus` appended at
   the end labelled with its name, and nothing else changed; it is delivered to the worker's agent
   exactly as the developer's own prompt path would deliver it; the run is *running* on delivery
   and *completed*, with the summary recorded, when the agent reports completion through the
   gateway.
4. **Given** `review-pr`, **When** it is called without `repo`, or with an argument it does not
   declare, **Then** the call is refused with the problem named and nothing is queued.
5. **Given** all eligible workers busy, **When** an action is called, **Then** the run is queued
   with its position and starts when an eligible worker becomes free — before any run received
   after it that the same worker could have taken.
6. **Given** a running run whose agent asks for input, **Then** the run is *awaiting input*, the
   worker is not free, and the run returns to *running* when the agent continues and to
   *completed* when it reports completion.
7. **Given** delivery to a worker fails (daemon unreachable, agent exited, sandbox stopped),
   **Then** the run is *failed* with the reason, the worker stays attached, and the orchestrator
   moves on to the next run and worker.
8. **Given** queued and in-flight runs, **When** the orchestrator restarts, **Then** every queued
   run is still queued in the same order, and each in-flight run is reconciled against its
   sandbox's current state (awaiting review if the agent is now idle and no completion was
   reported, left as is if working or awaiting input, failed if the sandbox is gone).
9. **Given** an action with runs queued, **When** the developer edits the action, **Then** the
   queued runs keep the prompt composed when they were called, and calls made after the edit use
   the new recipe.
10. **Given** an action whose requirements name a worker tag, **When** it is called, **Then** only
    workers carrying that tag are eligible; if none is free the run waits and the Queue view says
    why; it is never delivered to a worker without the tag.
11. **Given** a queued run, **When** the developer cancels it, **Then** it is *cancelled* and never
    runs; a run in flight offers no cancel.
12. **Given** a running run, **When** the worker's agent reports completion through the gateway
    with a summary, **Then** the run is *completed* with that summary shown in the Queue view's
    history, and the worker is free again once its agent is idle.
13. **Given** a running run, **When** the worker's agent goes idle without having reported
    completion, **Then** the run is *awaiting review*, the worker is not free, it appears at the
    top of the everyday sandbox list marked as needing review, and an inbox notification names the
    orchestrator and the action.
14. **Given** a run awaiting review, **When** the developer looks into the sandbox and marks it
    free (optionally noting what they found), **Then** the run is closed as *completed* — or
    *failed* if the developer says so — with the note as its summary and the record showing the
    developer closed it, and the worker is free again. **When** instead the agent reports
    completion before the developer acts, **Then** the run is *completed* with the agent's summary.
15. **Given** a sandbox with no run in flight, or a sandbox that is not the orchestrator's worker,
    **When** it reports completion, **Then** the report is refused and nothing changes.

---

### User Story 4 - Workers stay out of the way; rows say what they are attached to (Priority: P2)

*Outline items 1.d, 1.d.i, 1.d.ii, 2.j, 8.* With four workers attached, the developer's sandbox
list shows only the sandboxes they are driving themselves. When a worker needs the developer —
its agent asks for input, or it went idle without reporting its run complete and is awaiting
review — that worker appears in the list at the very top, above every sandbox that needs nothing,
so the developer can answer it with the ordinary prompt path or look into it and mark it free;
once dealt with it drops out again. When the developer wants to see all workers, a key combination
on the sandbox list opens the **Orchestrator Sandboxes** tab: it is not in the tab bar otherwise,
it lists every worker and reasoning sandbox across every orchestrator with its controller and the
run it is on, and every per-sandbox operation works there. Moving to another tab hides the tab
again. Wherever a sandbox row is shown, it has a third line naming its attached MCP servers and
the orchestrator it belongs to; a row with neither has no third line.

**Why this priority**: Hiding workers keeps a developer with many orchestrated sandboxes
productive; surfacing those that need input keeps the orchestrator from stalling silently; the
third line is how a developer tells, at a glance, what any sandbox is wired to.

**Independent Test**: Attach three workers, confirm none appear on the All, host or group tabs,
cause one agent to ask for input, confirm it appears at the top within seconds and disappears after
the developer answers, let another finish a run without reporting, confirm it surfaces marked for
review and leaves once marked free, open the Orchestrator Sandboxes tab with the key combination,
confirm all three are listed with their controller and that each row's third line names it, and
confirm switching tabs hides the tab again.

**Acceptance Scenarios**:

1. **Given** a sandbox becomes a worker, **Then** it is absent from the All tab, its host's tab and
   every group tab it belongs to, while its group memberships are preserved.
2. **Given** a worker whose agent asks for input, or on which a run is awaiting review, **Then**
   within 5 seconds it appears in the tabs it would otherwise be hidden from, at the top, visibly
   marked as needing input or review and as a worker.
3. **Given** the developer answers the worker through the ordinary prompt path, or marks it free,
   **Then** it leaves those tabs again as soon as it no longer needs the developer.
4. **Given** any tab, **When** several sandboxes need the developer, **Then** all of them are
   listed above every sandbox that does not, whether or not they are workers.
5. **Given** the sandbox list, **When** the developer presses the documented key combination,
   **Then** the Orchestrator Sandboxes tab appears in the tab bar, is selected, and lists every
   worker and reasoning sandbox of every reachable orchestrator with its controller, state,
   availability and current run.
6. **Given** the Orchestrator Sandboxes tab is selected, **When** the developer switches to another
   tab, **Then** the Orchestrator Sandboxes tab leaves the tab bar.
7. **Given** a worker shown anywhere, **When** the developer uses any per-sandbox operation,
   **Then** it works exactly as on an unattached sandbox, and *detach* and — while a run is
   awaiting review on it — *mark free* are offered as well.
8. **Given** a sandbox with attached MCP servers and a controller, **Then** its row has a third
   line naming the servers and the orchestrator; **Given** it has only one of the two, **Then** the
   third line names just that; **Given** it has neither, **Then** the row has two lines.

---

### User Story 5 - Events call actions without the developer (Priority: P2)

*Outline items 5, 5.b, 5.b.i, 5.c.* The developer sets up a **timed event** in the orchestrator's
**Events** view: call `check-github` every 5 minutes with fixed arguments. Every 5 minutes a run is
created, queued and delivered like a hand-made one, and shows the event as its origin. The event's
**dedupe** option, on by default, keeps runs from piling up: while a run from the event is still
waiting in the queue or in flight, the firing is skipped, so there is never more than one run of
the event at a time; switched off, every firing adds a run. The other unattended caller is a
worker's agent: a **triggered** event is a worker calling `review-pr` with arguments through the
orchestrator's gateway tools whenever its own task calls for it (Story 6); the run shows that
worker as its origin. A call from anything that is not currently one of the orchestrator's
sandboxes is refused and never queued. Intake over HTTP from outside switchboard (webhooks) is
deliberately not part of this feature.

**Why this priority**: Unattended events are why the orchestrator is a daemon rather than a TUI
feature, but actions are fully usable by hand (Story 3) without them.

**Independent Test**: Create a timed event on a 1-minute interval and confirm one run per minute
with the event as origin; hold every worker busy, then let one run take several minutes, and
confirm the event never has more than one run queued or in flight while dedupe is on, and one per
minute once it is off; from a worker's agent call an
action through the gateway and confirm the run's origin is that worker; call from a detached
sandbox and confirm rejection with nothing queued.

**Acceptance Scenarios**:

1. **Given** a timed event calling action A every N minutes, **Then** a run of A with the event's
   fixed arguments is queued every N minutes, each showing the event as its origin, and the next
   firing time is shown in the Events view.
2. **Given** a timed event with dedupe on (the default) whose previous run is still queued or in
   flight (running, awaiting input or awaiting review), **When** its interval elapses, **Then** no
   second run is queued and the skip is recorded on the event; the next firing after that run is
   closed queues one. **Given** dedupe is off, **Then** every firing queues a run however many are
   already waiting or in flight.
3. **Given** a timed event, **When** the developer disables it, **Then** it stops firing until
   enabled again; **When** the orchestrator restarts, **Then** enabled events resume on schedule
   and firings missed while it was down are not replayed.
4. **Given** a timed event whose action is deleted, **Then** the event is disabled with that reason
   and shown so in the Events view.
5. **Given** a worker's agent, **When** it calls an action with arguments through the gateway,
   **Then** the call is validated exactly as a hand-made call, acknowledged with the run's
   identifier and position, and the run shows that worker as its origin.
6. **Given** a call from a sandbox that is not currently one of the orchestrator's sandboxes,
   **Then** it is rejected and nothing is queued or run.

---

### User Story 6 - A worker's agent reaches the orchestrator through its MCP gateway (Priority: P3)

*Outline items 1.f, 2.j, 9, 9.a, 9.b.* The moment a sandbox becomes a worker, its agent can see
the orchestrator among the tools in its MCP gateway, and it has been **briefed** — in plain written
instructions, without having to discover anything — on what the orchestrator is, that it is called
`review-bot`, how an orchestrated sandbox is expected to behave, which tools it has and when to use
each, how a `prompt` run and a `reason` run differ, and that every run it is given ends with a
completion report carrying a short summary. The agent does its work and reports completion; it
lists the actions, calls `review-pr` with arguments to delegate a review to another worker, gets
back the run's identifier and position, and can ask later how that run is doing. A sandbox that is
not one of the orchestrator's workers gets nothing; a detached worker's calls are refused.

**Why this priority**: Agent-to-orchestrator calls are the most powerful trigger, but they depend
on attachment (Story 2), actions (Story 3) and events (Story 5) all being in place.

**Independent Test**: Attach a worker, confirm from inside it that the orchestrator's tools are
listed in the gateway and that its briefing names the orchestrator, its tools, the expected
behaviours and the completion report; run an action on it and confirm the agent reports completion
unprompted; list the actions and call one from the agent; confirm the run is queued with that
worker as origin; detach and confirm a further call is refused.

**Acceptance Scenarios**:

1. **Given** a sandbox becomes a worker (attach or launch-attach), **Then** without a restart its
   gateway offers the orchestrator's tools and its agent holds a briefing naming the orchestrator,
   stating how an orchestrated sandbox behaves, listing every tool with when and how to use it,
   describing the workflow for each action type, and requiring a completion report with a summary
   at the end of every run.
2. **Given** a worker's agent lists the orchestrator's actions, **Then** it receives every action's
   name, description, type, arguments (with descriptions and whether required) and requirements.
3. **Given** a worker's agent calls an action with arguments, **Then** the call is validated like
   any other, a run is queued with that worker as its origin, and the agent receives the run's
   identifier and position (or the refusal reason).
4. **Given** a run's identifier, **When** the agent asks about it, **Then** it receives the run's
   current state, outcome and summary.
5. **Given** a run in flight on a worker, **When** its agent reports completion with a summary,
   **Then** the run is *completed* with that summary and the agent is told so; **When** a sandbox
   with no run in flight reports, **Then** it is refused.
6. **Given** a sandbox that is not a worker of the orchestrator, or one that has been detached,
   **When** it calls, **Then** the call is refused.
7. **Given** the developer adds, edits or removes an action, **Then** workers' agents see the new
   set the next time they list, and their briefing remains accurate.

---

### User Story 7 - The orchestrator reasons in its own sandbox (Priority: P3)

*Outline items 6, 6.a, 6.b, 2.i.* Every orchestrator is created with a **reasoning sandbox** on its
own machine — by default an empty context: no folders, the daemon's default agent, nothing else —
for generic reasoning queries. An action of type `reason` runs there instead of on a worker: the
developer creates `triage` (type `reason`, body "Given these issues: {issues} — decide which need
code changes and reply with a list"), calls it, and the composed prompt runs on the reasoning
sandbox under the same free rules as any worker; its agent carries the same briefing and ends the
run with the same completion report — here the summary *is* the answer. In the **Reasoning
sandbox** view the developer may give it a setup — folders it needs, a saved configuration, kits —
and rebuild it; the reasoning sandbox is hidden like a worker, listed on the Orchestrator
Sandboxes tab marked as such, surfaces when its agent needs input or review, and cannot be
detached or attached elsewhere.

**Why this priority**: `reason` is the second action type and gives the orchestrator a place to
think, but `prompt` actions and workers deliver the core value without it.

**Independent Test**: Create an orchestrator, confirm a default reasoning sandbox exists on its
machine and is absent from the everyday lists, create and call a `reason` action, confirm it runs
there and not on any worker, give the sandbox a folder in its setup, rebuild, and confirm the
folder is present.

**Acceptance Scenarios**:

1. **Given** an orchestrator is created, **Then** a reasoning sandbox exists on its machine with
   the default empty setup, hidden from the everyday lists and listed on the Orchestrator Sandboxes
   tab as that orchestrator's reasoning sandbox.
2. **Given** a `reason` action, **When** it is called, **Then** its run is delivered only to the
   reasoning sandbox, only when that sandbox is free, and follows the same states and completion
   report as a `prompt` run; the reported summary is shown in the Queue view's history.
3. **Given** the Reasoning sandbox view, **When** the developer saves a setup with folders and a
   configuration, **Then** the reasoning sandbox is rebuilt with that setup; **Given** a run is on
   it, **Then** the rebuild is refused until the run ends.
4. **Given** the reasoning sandbox's agent asks for input, or goes idle without reporting its run
   complete, **Then** it surfaces at the top of the everyday lists like a worker and the developer
   can answer it or mark it free.
5. **Given** the reasoning sandbox, **When** the developer tries to detach it or attach it to
   another orchestrator, **Then** it is refused; **When** the orchestrator is removed, **Then** it
   is destroyed.

---

### Edge Cases

- **Two attach attempts race** for the same unattached sandbox from two orchestrators: exactly one
  succeeds; the other is refused naming the winner.
- **An orchestrator is renamed**: no worker record changes; every row, tab, picker and notification
  shows the new name at once, workers' briefings are refreshed as their daemons are reachable, and
  a worker whose daemon is unreachable keeps showing the last name it knew until it reconnects.
- **A worker is destroyed** (from the TUI or outside switchboard) while attached: the relationship
  goes with it; any run on it is *failed* with that reason; its gateway access is moot.
- **A worker is stopped** while attached: it stays attached but is not free; runs wait for another
  eligible worker or for it to be started by the developer. The orchestrator does not start
  sandboxes on its own.
- **A worker's agent exits**: the worker is not free; a run on it is *failed*.
- **The orchestrator is unreachable** (its machine disconnected, or it is down): workers stay hidden
  (their owning daemon holds the relationship) and remain operable and detachable; nothing is
  queued or started until it returns; timed events fire again on their schedule once it is back.
- **A worker's owning daemon is unreachable from the orchestrator** (the SSH dial from the
  orchestrator's machine fails, or the daemon there is down): its workers are shown as unreachable
  and are not free; runs are not delivered to them; the orchestrator keeps retrying the dial.
- **The orchestrator's machine has no SSH access to a worker's machine**: attaching a sandbox there
  is refused with the SSH error; the developer fixes access (keys, agent) on the orchestrator's
  machine, not in switchboard.
- **The orchestrator restarts with runs in flight**: the queue is durable and in-flight runs are
  reconciled, never silently dropped or re-sent (FR-134).
- **The developer prompts a worker by hand**: allowed; the worker is busy until its agent is idle
  again, and the orchestrator yields.
- **The developer answers a worker's input request**: the run resumes; the orchestrator does not
  need to know what was answered.
- **A run's requirements match no attached worker** (no worker carries the required tag): the run
  is queued and the Queue view says it is waiting for an eligible worker; the developer can attach
  one or cancel the run. It never blocks other runs.
- **An action is deleted while its runs are queued**: the runs keep their composed prompt and the
  action's name, and still run; new calls to that name are refused.
- **A body's placeholder names an argument that is later removed** from the action: the save that
  removes the argument is refused until the placeholder is removed too.
- **An argument value is very large, or the composed prompt exceeds the documented maximum**: the
  call is refused with the limit named.
- **A call carries arguments the action does not declare**: refused; unknown names are never
  silently dropped, so a mistyped argument cannot vanish into the prompt.
- **A timed event's interval elapses while the orchestrator is down**: no catch-up firings; the
  schedule resumes from the next interval after it returns.
- **A worker's agent calls an action whose requirements exclude the calling worker**: allowed; the
  run goes to an eligible worker, not back to the caller.
- **A worker's agent calls a `reason` action**: allowed; it runs on the reasoning sandbox.
- **An agent reports completion while the orchestrator is unreachable**: the call fails; if the
  agent then goes idle, the run is *awaiting review* once the orchestrator is back, and the
  developer closes it by hand. The briefing tells the agent that a failed report does not end the
  run.
- **An agent reports completion twice, or for a run that is not on its sandbox**: the second or
  misdirected report is refused; a run has exactly one summary.
- **An agent reports completion and then keeps working**: the run is *completed* at the report;
  the sandbox is free only once the agent is idle, as after any developer prompt.
- **The developer prompts a worker that is awaiting review**: allowed; if the agent then reports,
  the run closes normally; if it goes idle again without reporting, the run is still awaiting
  review.
- **A worker awaiting review is stopped**: the run stays awaiting review and can still be closed
  by marking the worker free; **destroyed**: the run is *failed* with that reason.
- **A timed event with dedupe off while every worker is busy**: its runs accumulate in the queue in
  order of firing; the Events view shows how many are waiting, each can be cancelled, and
  switching dedupe on affects only later firings.
- **The reasoning sandbox is destroyed outside switchboard**: `reason` runs fail with that reason;
  the Reasoning sandbox view offers rebuilding it.
- **An orchestrator is removed while runs are in flight**: the confirmation says so; in-flight runs
  are *failed*, queued runs *cancelled*, workers detached.
- **A worker needs input while the orchestrator cannot reach its daemon**: the owning daemon still
  surfaces it in the TUI (needs-input is the daemon's own signal), so the developer is not blocked
  by the orchestrator's outage.
- **The Orchestrator Sandboxes tab is open when the last worker is detached**: the tab stays open
  and empty until the developer leaves it; it is not shown again by default.
- **Group membership of a worker**: preserved while hidden; the group tab simply omits it, and it
  reappears in the group when detached.
- **A sandbox's MCP server list is long**: the third line truncates with an indication, never wraps
  into a fourth line.

## Requirements *(mandatory)*

Requirement numbering continues from the prior feature (FR-104). Outline item references are given
in brackets. Terms: an **action** is a user-authored recipe; calling an action with arguments
creates a **run** (the composed prompt, queued and delivered); an **event** is whatever calls an
action; a **worker** is a sandbox attached to an orchestrator; the **reasoning sandbox** is the
orchestrator's own sandbox; a **completion report** is the agent's own statement, sent through the
gateway with a short summary, that its run is done; a run is **in flight** while it is *running*,
*awaiting input* or *awaiting review*. An orchestrator is run by the daemon of the machine it is
placed on, so "the orchestrator restarts" means that daemon restarts.

### Orchestrator daemon, naming and placement [3, 4, 7]

- **FR-105** [3]: An **orchestrator** MUST be a distinct role that a machine's daemon runs
  **in-process**: the developer creates one deliberately from the Orchestrators screen by naming
  it and choosing the **machine it is placed on** — any machine whose daemon the TUI is connected
  to — and that daemon records it persistently and runs it from then on, with no separate process
  and no separate binary to install. It MUST run independently of the TUI, survive the TUI
  quitting and restarting, run again whenever its daemon starts, and be reachable by the TUI
  through the connection it already has to that machine. Any number MAY exist, on the same or on
  different machines.
- **FR-106** [3]: An orchestrator MUST be able to control sandboxes on any machine whose daemon it
  can reach, including machines other than its own, through those sandboxes' owning daemons. It
  MUST never act on a sandbox except through its owning daemon. The orchestrator's daemon MUST
  reach a remote daemon by **dialling it over SSH from the orchestrator's machine**, the same way
  the TUI reaches a remote daemon, using that machine's own SSH keys or agent — no new port, no
  new credential store. Each orchestrator MUST hold its own **host entries** (one per machine its
  workers live on, the same shape as the TUI's), managed on the Orchestrators screen, which MUST
  offer copying an entry from the TUI's own host list. Everything between the orchestrator and a
  worker — prompts, status, the worker's gateway calls — MUST travel over that connection, so a
  worker's machine never needs a path to the orchestrator's machine.
- **FR-148** [7]: Every orchestrator MUST have a developer-given **name**, chosen at creation,
  changeable later, and unique among the orchestrators the TUI knows. The name MUST be how the
  orchestrator is shown everywhere the developer sees it: the Orchestrators screen, a sandbox
  row's third line, the Orchestrator Sandboxes tab, attach pickers, notifications, run records and
  the briefing given to workers' agents. Underneath, every orchestrator MUST have a stable, opaque
  **identity** assigned at creation that never changes; worker records on owning daemons, runs,
  gateway entries and host entries MUST reference the identity, never the name, so that renaming
  changes only the label — instantly, on the orchestrator's own machine, with no record elsewhere
  rewritten — and the new name MUST appear live wherever the orchestrator is shown and in workers'
  briefings as soon as their daemons are reachable (clarified 2026-10-14).
- **FR-153** [4]: The developer MUST be able to remove an orchestrator behind a confirmation that
  states the consequences: its workers are detached (and reappear in the everyday lists), its
  reasoning sandbox is destroyed, queued runs are *cancelled*, in-flight runs are *failed*, its
  actions, events and history are discarded, and its name becomes free.

### Controller/worker attachment [1, 1.a, 1.b, 1.c]

- **FR-107** [1]: A sandbox MUST be attachable to an orchestrator in a controller/worker
  relationship. The relationship MUST be recorded by the sandbox's **owning daemon** (the controller
  — its identity and last-known name, FR-148 — is part of the sandbox's record) and reflected to
  every client live, so the TUI can treat the sandbox as a worker without consulting the
  orchestrator. Becoming a worker also gives the sandbox's agent the orchestrator's tools and
  briefing (FR-150, FR-152, FR-154).
- **FR-108** [1.a]: A sandbox MUST have at most one controller. An attach request for a sandbox
  that already has a controller MUST be refused with the current controller named; the developer
  detaches first. Concurrent attach attempts MUST resolve to exactly one controller.
- **FR-109** [1.b]: The developer MUST be able to attach an existing sandbox to an orchestrator
  chosen by name, and detach a worker, from the TUI with a single documented keystroke on the
  sandbox, at any time and in any sandbox state. Attach and detach MUST NOT stop, restart or
  recreate the sandbox, and MUST NOT disturb its agent session, terminal attachments or services.
- **FR-110** [1.c]: The launch flow MUST offer choosing an orchestrator by name for the new
  sandbox. When one is chosen, the sandbox MUST be that orchestrator's worker before its agent
  starts, so it is never shown in the everyday lists and its agent has the orchestrator's tools and
  instructions from its first prompt. Choosing none launches exactly as today.
- **FR-111** [1.b]: Detaching a worker on which a run is in flight (*running*, *awaiting input* or
  *awaiting review*) MUST be refused with that run named; detaching is allowed once the run has
  ended — for a run awaiting review, once the developer has marked the worker free (FR-156).
  Destroying a worker MUST remove the relationship and fail any run on it (FR-124).
- **FR-112**: The relationship MUST survive restarts of the owning daemon, of the orchestrator and
  of the TUI, and an orchestrator's unreachability. A worker whose orchestrator is unreachable
  MUST still show its controller (marked unreachable) and remain detachable.

### Visibility in the TUI [1.d, 1.d.i, 1.d.ii, 8]

- **FR-113** [1.d]: A worker MUST NOT appear on the All tab, on its host's tab, or on any group tab,
  except as FR-114 and FR-115 provide. Group memberships are preserved while hidden. A reasoning
  sandbox is hidden the same way (FR-145).
- **FR-114** [1.d.i]: A worker that **needs the developer** — its agent is **awaiting input**, or
  a run on it is **awaiting review** (FR-156) — MUST appear in the tabs it is otherwise hidden
  from, within 5 seconds of the need arising, and MUST leave them again once the need is gone
  (the agent is no longer awaiting input; the run has been closed). Every tab MUST order sandboxes
  that need the developer above all others, whether or not they are workers; among sandboxes with
  the same need, the existing order is kept. A surfaced worker MUST be marked as a worker, say
  which need it has, and name its controller.
- **FR-115** [1.d.ii]: The sandbox list MUST have an **Orchestrator Sandboxes** tab that is absent
  from the tab bar by default, is shown and selected by a single documented key combination on the
  sandbox list, and leaves the tab bar when the developer switches to any other tab. It MUST list
  every worker and reasoning sandbox of every reachable orchestrator — across hosts — with its
  controller, state, availability and current run, MUST mark reasoning sandboxes as such, and
  MUST say which orchestrators are unreachable.
- **FR-116**: Every per-sandbox operation available on an unattached sandbox (terminal, popout,
  VS Code, stop/start, destroy, rename, tag, sources, kits, MCP, services, log, prompt) MUST work
  on a worker wherever it is shown, plus *detach* and — while a run is awaiting review on it —
  *mark free* (FR-156).
- **FR-149** [8]: Every sandbox row in the TUI MUST gain a **third line** carrying, in this order:
  the names of the MCP servers attached to the sandbox, if any, and the name of the orchestrator it
  is attached to, if any. When the sandbox has neither, the row MUST have no third line. The line
  MUST update live as servers are attached or the controller changes, and MUST truncate rather
  than wrap. This supersedes where feature 008 (FR-086) showed the attached-server set.

### Running on workers [1.e]

- **FR-117** [1.e]: The orchestrator MUST deliver a run to a sandbox only when that sandbox is
  **free**: it is running, its agent is idle (not working, not awaiting input, not exited), and no
  run is in flight on it — none *running*, *awaiting input* or *awaiting review*. The
  orchestrator MUST never deliver to a sandbox that is awaiting input or working, regardless of
  what caused that state, and MUST never treat an idle agent as free while its run has not been
  closed (FR-156). The same rule applies to the reasoning sandbox for `reason` runs.
- **FR-118** [1.e]: At most one run MAY be in flight on a sandbox at a time.
- **FR-119** [1.e]: Developer activity on a worker (a prompt typed in its terminal, a prompt sent
  through the prompt path, an attach or edit operation) MUST take precedence: the worker is not
  free until its agent is idle again, and the orchestrator MUST NOT interrupt or queue behind it in
  any way that affects the developer's session.

### Completion and review [2.j]

- **FR-155** [2.j]: A run MUST be completed by its agent's **completion report**: a call through
  the orchestrator's gateway tools (FR-151) carrying a short **summary** of what was done. On a
  report from a sandbox with a run in flight, the orchestrator MUST mark that run *completed*,
  record the summary on it, acknowledge the report, and treat the sandbox as free once its agent is
  idle. A report from a sandbox with no run in flight, or from a sandbox that is not the
  orchestrator's worker or reasoning sandbox, MUST be refused and change nothing; a run has exactly
  one summary. The briefing (FR-154) MUST tell every agent to end each run with this report.
- **FR-156** [2.j]: Because an agent can fail to report, an idle agent MUST NOT be taken as proof
  of completion. When the agent of a sandbox with a run *running* or *awaiting input* becomes idle
  and no completion report for that run has arrived, the run MUST become **awaiting review**: it
  stays on the sandbox, the sandbox is not free, it surfaces in the everyday lists as needing the
  developer (FR-114), and an inbox notification is raised (FR-133). The developer, after looking
  into the sandbox, MUST be able to **mark the sandbox free** — a documented keystroke on the
  sandbox row wherever it is shown, and an offer in the Workers and Queue views — which closes the
  run as *completed* (or *failed*, if the developer says so) with an optional developer note
  recorded as its summary and the record stating that the developer closed it; the sandbox is then
  free. A completion report that arrives while the run is awaiting review MUST close it normally.
  Nothing else closes a run awaiting review except the sandbox being destroyed (FR-124) or the
  orchestrator being removed (FR-153).

### Actions: user-authored recipes [2.a, 2.b, 2.c, 2.d, 2.e, 2.f, 2.g, 2.h, 2.i]

- **FR-135** [2.b]: Actions MUST be **pre-composed by the developer**: each orchestrator MUST offer
  an **Actions** view in which the developer creates, edits and deletes its actions. Actions are
  persisted with the orchestrator and survive its restart. Validation (FR-121, FR-137–FR-139)
  happens at save, so an action that exists is always callable.
- **FR-136** [2.d]: An action MUST be a complete recipe for one prompt: **how to construct it**
  (body plus arguments, FR-138–FR-140) and **the requirements for running it** — its type, which
  fixes where it runs (FR-122, FR-141), and for `prompt` actions an optional **worker tag**
  requirement: when set, only workers carrying that tag (the sandbox's existing purpose tag) are
  eligible; when unset, any worker of the orchestrator is. A sandbox that satisfies an action's
  requirements is a *supported* sandbox for it.
- **FR-137** [2.e]: Every action MUST have a **name** — unique within its orchestrator, the
  identifier used in every call — and a **description** stating what it does, shown to the
  developer and to workers' agents (FR-151).
- **FR-138** [2.f]: Every action MUST have a non-empty **body**: the text that becomes the prompt.
  The body MAY contain **placeholders**, each naming one of the action's declared arguments, in one
  documented placeholder form. A save whose body names an undeclared argument MUST be refused with
  the placeholder named.
- **FR-139** [2.g]: An action MAY declare named **arguments**, each with a name (unique within the
  action), a description, whether it is required, and an optional default value; values are text.
  When a prompt is composed from supplied values: every placeholder is replaced by its argument's
  value (every occurrence); each supplied argument whose name appears in no placeholder is
  **appended** to the end of the prompt, labelled with its name, in declaration order; an optional
  argument not supplied takes its default if it has one, and otherwise its placeholders are
  replaced by nothing and nothing is appended.
- **FR-140** [2.h]: **Calling** an action means naming it and supplying argument values. The
  orchestrator MUST validate the call — every required argument present, no argument the action
  does not declare, every value within the documented size limit — and refuse it with the problem
  named if it fails. A valid call MUST compose the prompt per FR-139 **at call time**, create a run
  carrying the action's name and type, the supplied arguments and the composed prompt, and queue it
  (FR-120). Later edits to the action MUST NOT change a run already created.
- **FR-121** [2.a]: Every action MUST carry a **`type`** that decides the field set the orchestrator
  expects and validates for that action at save time, and where its runs execute. A save with an
  unknown type, or with a field its type does not define, MUST be refused with the problem named.
  Adding a type MUST NOT change how existing types are validated or run.
- **FR-122** [2.c]: The **`prompt`** type MUST be supported first. A `prompt` run MUST be delivered
  to a free worker of the orchestrator that satisfies the action's requirements (FR-136), as the
  developer's own prompt path would deliver it; it is *running* on delivery, *awaiting input* while
  the agent asks for input, *completed* when the agent reports completion (FR-155), and *awaiting
  review* if the agent goes idle without reporting (FR-156).
- **FR-141** [2.i]: The **`reason`** type MUST be supported second. A `reason` run MUST be
  delivered only to the orchestrator's reasoning sandbox (FR-145), when it is free, with the same
  states and the same completion report as a `prompt` run. `reason` actions declare no worker
  requirement.

### Queue and runs [2, 4, 4.c]

- **FR-120** [2]: The orchestrator MUST keep one queue of **runs** in order of receipt, whichever
  event created them. The queue MUST be durable: a restart of the orchestrator loses no queued run
  and keeps their order.
- **FR-123** [2]: Runs MUST start in order of receipt: whenever a sandbox becomes free, the earliest
  queued run that sandbox can satisfy starts. An earlier run that no free sandbox can satisfy MUST
  NOT block a later run that a free sandbox can; it keeps its place and starts when a suitable
  sandbox frees. The orchestrator MUST never start a run before an earlier one that the same free
  sandbox could have taken.
- **FR-134** [2]: After an orchestrator restart, every run that was in flight MUST be reconciled
  against its sandbox's current state within 30 seconds of that sandbox's daemon being reachable:
  *awaiting review* if the agent is idle and no completion report was recorded (the orchestrator
  cannot know what was done while it was down), left in place if it is working or awaiting input,
  *failed* if the sandbox is gone or its agent exited; a run already awaiting review stays so. No
  such run is re-delivered.
- **FR-124** [4]: A run whose delivery or execution fails — unreachable daemon, agent exited,
  sandbox stopped or destroyed while the run is running or awaiting input, destroyed while awaiting
  review, rejected by the daemon — MUST be marked *failed* with the reason, the sandbox MUST stay
  attached (unless destroyed), and the orchestrator MUST continue with the next run and sandbox.
  Failed runs are not retried automatically.
- **FR-125** [4, 4.c]: Each run MUST record: identifier, action name and type, the supplied
  arguments, the composed prompt, the event that created it and its origin, time received, time
  started, time ended, state (*queued*, *running*, *awaiting input*, *awaiting review*,
  *completed*, *failed*, *cancelled*), the sandbox it ran on, the **summary** reported by its
  agent (or the developer's note when the developer closed it, marked as such), and an outcome
  reason where applicable.
- **FR-126** [4]: The developer MUST be able to cancel a *queued* run; it is marked *cancelled* and
  never runs. Runs in flight cannot be cancelled in this feature; a run awaiting review is closed
  by marking its sandbox free (FR-156).
- **FR-127** [4]: Finished runs (completed, failed, cancelled) MUST be retained for a documented,
  bounded period or count and then discarded; queued and in-flight runs are never discarded.

### Events [5, 5.a, 5.b, 5.b.i, 5.c]

- **FR-128** [5]: An **event** is the only thing that creates a run: it names an action, supplies
  its arguments, and so turns the action into a prompt waiting to run. Three kinds MUST exist —
  user (FR-129), timed (FR-130) and triggered (FR-144). Every kind MUST go through the same call
  validation and composition (FR-140), MUST be acknowledged with the run's identifier and queue
  position or refused with the reason, and MUST leave its kind and origin on the run (FR-125).
- **FR-129** [5.a]: A **user event** is the developer choosing an action in the Actions view and
  filling in its arguments: the TUI MUST present a form built from the action's declared arguments
  (names, descriptions, required marks, defaults), MUST refuse submission while a required argument
  is empty, and on submission MUST call the action and show the run's position.
- **FR-130** [5.b]: A **timed event** MUST let the developer have an action called on a fixed
  interval: it has a name, an action, fixed argument values, an interval of at least one minute,
  an enabled flag and a dedupe option (FR-157). Each firing calls the action. Timed events are
  persisted with the orchestrator, resume on schedule after a restart without replaying missed
  firings, show their next firing time and how many of their runs are waiting, and are created,
  edited, enabled, disabled and deleted in the orchestrator's **Events** view. An event whose
  action is deleted MUST be disabled with that reason. **Enqueue now (SWI-4)**: the Events view
  MUST also let the developer queue one run of the selected event immediately (`n`, no
  confirmation) via `FireTimedEvent`; the run is created exactly as a scheduled firing would be
  (timed kind, origin = the event, the event's fixed arguments) and counts in its waiting column.
  It is subject to the event's dedupe (FR-157) — refused with the existing run named while one is
  queued or in flight — works on a disabled event, and MUST NOT change the event's next firing
  time, last fired/skipped times or skip count.
- **FR-157** [5.b.i]: Every timed event MUST have a **dedupe** option, **on by default**. While it
  is on, a firing MUST be skipped — and the skip recorded on the event — if a run created by that
  event is still *queued* or in flight (*running*, *awaiting input* or *awaiting review*) when the
  firing is due, so a deduped event never has more than one run anywhere at a time (clarified
  2026-10-14); the next firing after that run is closed proceeds. While it is off, every firing
  queues a run regardless of what is waiting or in flight. Changing the option affects only later
  firings; runs already queued are untouched.
- **FR-144** [5.c]: A **triggered event** is a programmatic call from a worker's (or the reasoning
  sandbox's) agent through the orchestrator's MCP tools (FR-151): the caller names the action and
  supplies its arguments. Every triggered call MUST be attributed to the calling sandbox; a call
  that cannot be attributed to one of the orchestrator's current sandboxes MUST be refused without
  being queued or run (FR-150). Intake over HTTP from outside switchboard — webhook sources with
  their own credentials — is out of scope for this feature (clarified 2026-10-14) and is the
  natural next event kind.

### Reasoning sandbox [6, 6.a, 6.b]

- **FR-145** [6]: Every orchestrator MUST have exactly one **reasoning sandbox**, created on the
  orchestrator's machine when the orchestrator is created, used for `reason` runs (FR-141). It is
  attached to its orchestrator like a worker — hidden per FR-113, surfaced per FR-114, listed on
  the Orchestrator Sandboxes tab marked as the reasoning sandbox, named on its row's third line —
  but it MUST NOT be detachable or attachable to another orchestrator, MUST be destroyed when the
  orchestrator is removed, and MUST be rebuildable from the orchestrator's **Reasoning sandbox**
  view when no run is on it (for example after it was destroyed outside switchboard).
- **FR-146** [6.a]: The developer MAY specify the reasoning sandbox's **setup** in the Reasoning
  sandbox view — the folders it needs and the same launch choices an ordinary sandbox has (saved
  configuration, kits, MCP servers). Saving a changed setup MUST rebuild the reasoning sandbox with
  it; the rebuild MUST be refused while a run is on the sandbox.
- **FR-147** [6.b]: By default the reasoning sandbox MUST be an **empty context**: no folders, the
  daemon's default agent and configuration, nothing else — suitable for generic reasoning queries.

### Reachable from workers through the MCP gateway [1.f, 9, 9.a, 9.b]

- **FR-150** [9]: The orchestrator MUST be reachable from each of its workers, and from its
  reasoning sandbox, as a server in that sandbox's MCP gateway. Attaching an existing sandbox MUST
  make it available live (no stop or restart); a launch-attached sandbox MUST have it before its
  agent starts. A call from a worker on another machine MUST travel through the worker's owning
  daemon and over the orchestrator's connection to it (FR-106). The orchestrator MUST attribute
  every call to the calling sandbox, MUST refuse calls from any sandbox that is not currently its
  worker or reasoning sandbox, and MUST refuse a detached sandbox's calls from the moment of
  detach.
- **FR-151** [9.a]: Through the gateway a worker's agent MUST be able to: **list** the
  orchestrator's actions with each one's name, description, type, arguments (name, description,
  required, default) and requirements; **call** an action by name with arguments and receive the
  run's identifier and queue position, or the refusal reason; **check** a run by identifier,
  receiving its state, outcome and summary; and **report completion** of the run on its own
  sandbox with a short summary (FR-155), receiving confirmation or the refusal reason.
- **FR-152** [9.b]: A worker's agent MUST be given the context to use the orchestrator correctly
  without discovery: the written **briefing** of FR-154, delivered when the sandbox becomes a
  worker (attach or launch-attach), present before the agent's first orchestrated prompt, and kept
  accurate as actions change or the orchestrator is renamed. The agent MUST NOT need to probe or
  guess to find or use the orchestrator or to know what is expected of it.
- **FR-154** [1.f]: The briefing MUST tell the agent, in plain written instructions: that its
  sandbox is a worker (or the reasoning sandbox) of the orchestrator, named; how an orchestrated
  sandbox is expected to behave — do the run it is given, ask for input through the normal path
  when it must, and end every run with a completion report carrying a short summary (FR-155);
  every orchestrator tool available to it (FR-151), with when and how to use each; the workflow
  for each action type — a `prompt` run arrives as a prompt on this sandbox and ends with a
  report, a `reason` run does the same on the reasoning sandbox, and the agent may delegate by
  calling an action and checking its run; and the actions currently offered. The reasoning
  sandbox's agent MUST receive the same briefing.

### Orchestrators screen and observability [4, 4.a, 4.b, 4.c]

- **FR-142** [4.a, 4.b]: The TUI MUST have a third top-level screen, **Orchestrators**, in the
  shift+→ / shift+← cycle with the sandboxes and daemon screens (extending feature 008's FR-068);
  each screen's selection is preserved when leaving and returning, and the settings keystroke is
  unchanged. The screen MUST list every known orchestrator with its name, the machine it is placed
  on, whether it is reachable, its worker count and its queued, in-flight and awaiting-review
  counts; MUST offer create, rename and remove; and opening one MUST offer its views — Workers,
  Actions, Queue, Events, Reasoning sandbox — which together are the one place the orchestrator is
  managed: creating and editing actions (FR-135), creating and editing timed events (FR-130),
  triggering user events (FR-129), detaching and marking workers free (FR-132), managing its host
  entries (FR-106), and setting up the reasoning sandbox (FR-146).
- **FR-143** [4.a]: The header strip at the very top of the TUI — where `switchboard` is shown —
  MUST name every top-level screen (Sandboxes, Daemons, Orchestrators) with the current one
  highlighted and the shift+←/→ hint, on every top-level screen, so the navigation is visible
  rather than remembered.
- **FR-131** [4, 4.c]: The orchestrator's **Queue** view MUST show three groups: the runs
  **queued** (in order, with position), the runs **in progress** (running, awaiting input,
  awaiting review — each with its sandbox), and the **history** of finished runs (completed,
  failed, cancelled; newest first, within the retention of FR-127). Every run shows action, type,
  event kind and origin, time received, state, sandbox and outcome; a completed run shows the
  summary its agent reported (or the developer's closing note, marked as such). The view MUST
  update live, MUST show a run's composed prompt on request, MUST offer cancelling a queued run,
  and MUST offer marking free the sandbox of a run awaiting review.
- **FR-132** [4]: The orchestrator's **Workers** view MUST list every worker with host, sandbox
  state and availability (free, busy on which run, awaiting input, awaiting review, not running,
  unreachable) and MUST offer detach and, for a worker awaiting review, mark free.
- **FR-133** [4]: A failed run, a worker (or reasoning sandbox) that needs input, and a run that
  becomes awaiting review MUST each raise a notification in the existing inbox naming the
  orchestrator, the action and the reason. A reported completion MUST NOT raise an inbox
  notification (the Queue view's history records it with its summary), so the inbox stays about
  things that need the developer.

### Key Entities

- **Orchestrator**: a persistent record run in-process by the daemon of the **machine** it is placed
  on, created by the developer with a unique **name** and a stable opaque **identity** that every
  other record references; owns its host entries (the machines it
  dials over SSH), one set of actions, one queue of runs, one set of timed events, one reasoning
  sandbox, and the controller side of zero or more worker relationships.
- **Worker relationship**: the controller/worker link between an orchestrator and a sandbox.
  Recorded on the sandbox by its owning daemon (controller identity, last-known controller name
  for display, time attached); at most one per sandbox; survives restarts; removed by detach or
  destroy. Carries the worker's gateway access to the orchestrator.
- **Sandbox availability**: derived, never stored — *free* (running, agent idle, no run in flight
  on it), *busy* (agent working, for the orchestrator or the developer), *awaiting input*,
  *awaiting review* (agent idle, run not reported complete), *not running* (stopped, error,
  creating), *unreachable* (owning daemon not reachable from the orchestrator).
- **Action**: a developer-authored recipe owned by an orchestrator — name (unique within it),
  description, `type` (`prompt` or `reason`), body with placeholders, declared arguments (name,
  description, required, default), and requirements (worker tag, `prompt` only). Persisted;
  validated at save.
- **Run**: one call of an action — identifier, action name and type, supplied arguments, composed
  prompt, event kind and origin, timestamps, state, sandbox, outcome, and the summary from its
  completion report (or the developer's closing note). Durable from
  acceptance until retention expires; its prompt is fixed at call time.
- **Completion report**: the agent's statement through the gateway that the run on its sandbox is
  done, with a short summary; one per run. Its absence when the agent goes idle is what puts a run
  into *awaiting review*.
- **Briefing**: the written instructions every worker's and reasoning sandbox's agent receives on
  attach — the orchestrator's name, expected behaviour, tools, per-type workflow, current actions
  — kept accurate as actions change.
- **Event**: what calls an action. *User* (the developer, via the arguments form), *timed* (a
  persisted schedule: name, action, fixed arguments, interval, enabled flag, dedupe flag, next
  firing, last skip), *triggered* (a worker's or the reasoning sandbox's agent through the
  gateway).
- **Reasoning sandbox**: the orchestrator's own sandbox on its machine — default empty context,
  optional developer-specified setup (folders, configuration, kits, MCP) — the only target of
  `reason` runs; hidden and surfaced like a worker; not detachable.
- **Orchestrator Sandboxes tab**: a view in the sandbox list, hidden by default, listing every
  worker and reasoning sandbox across orchestrators and hosts; not a group, not persisted.
- **Sandbox row third line**: the row's third line naming the sandbox's attached MCP servers and
  its orchestrator; present only when at least one exists.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of workers and reasoning sandboxes are absent from the All, host and group tabs
  while they need nothing from the developer; one whose agent asks for input, or whose run is
  awaiting review, appears at the top of those tabs within 5 seconds, and disappears within 5
  seconds of being answered or marked free.
- **SC-002**: A queued run starts on a free sandbox that satisfies it within 10 seconds of that
  sandbox becoming free (or of the call, when a suitable sandbox is already free), in 100% of
  cases.
- **SC-003**: Across any sequence of calls, no run ever starts before an earlier run that the same
  free sandbox could have taken — order of receipt holds in 100% of dispatches.
- **SC-004**: Zero sandboxes ever have two runs in flight, and zero runs are ever delivered to a
  sandbox whose agent is working or awaiting input, or whose previous run has not been closed.
- **SC-005**: 100% of action saves with an unknown type, an undefined field, an empty body, a
  duplicate name or an undeclared placeholder are refused with the problem named; 100% of calls
  with a missing required argument, an undeclared argument or an oversized value are refused with
  the problem named, and nothing is queued.
- **SC-006**: For every valid call, the composed prompt equals the body with every placeholder
  replaced and every unreferenced supplied argument appended under its name — verifiable from the
  run's record in 100% of cases.
- **SC-007**: Zero sandboxes ever have two controllers; 100% of attach attempts on a controlled
  sandbox are refused naming the current controller.
- **SC-008**: An orchestrator restart loses zero queued runs, keeps their order, keeps every
  action and timed event, and reconciles every in-flight run within 30 seconds of its sandbox's
  daemon being reachable.
- **SC-009**: Attaching or detaching an existing sandbox completes within 5 seconds of
  confirmation, with no change to its running state, agent status or terminal attachments, and
  the orchestrator's tools are visible in the worker's gateway within that time.
- **SC-010**: The Orchestrator Sandboxes tab is one key combination away from the sandbox list and
  leaves the tab bar on 100% of tab switches away from it; the Orchestrators screen is at most two
  shift+arrow presses from any top-level screen, and the header strip highlights the current screen
  100% of the time.
- **SC-011**: A developer can create an orchestrator in under 60 seconds, author a `prompt` action
  with two arguments in under 2 minutes, and call an action by hand in under 30 seconds of
  interaction.
- **SC-012**: A timed event fires within 10 seconds of each scheduled time while the orchestrator
  is up, and with dedupe on never has more than one of its runs queued or in flight, in 100% of
  intervals.
- **SC-013**: 100% of triggered calls from the orchestrator's own sandboxes are acknowledged within
  2 seconds with the run's identifier and position; 100% of calls from any other sandbox are
  refused with nothing queued.
- **SC-014**: 100% of failed runs, input requests and runs that become awaiting review raise an
  inbox notification within 5 seconds; zero reported completions do.
- **SC-015**: Developer activity on a worker is never interrupted: zero deliveries to a sandbox
  whose agent is busy with a developer-initiated prompt.
- **SC-016**: 100% of sandbox rows with an attached MCP server or a controller show a third line
  naming them, and 100% of rows with neither show two lines.
- **SC-017**: A newly attached worker's agent can list the orchestrator's actions, call one and
  report completion without any discovery step, in 100% of attachments; every `reason` run
  executes on the reasoning sandbox and never on a worker.
- **SC-018**: 100% of completion reports from a sandbox with a run in flight close that run as
  completed with the reported summary within 2 seconds; 100% of reports from any other sandbox are
  refused with nothing changed.
- **SC-019**: 100% of runs whose agent goes idle without a completion report become awaiting
  review rather than completed, are never treated as free until the developer marks the sandbox
  free, and carry the developer's note (or the fact that the developer closed them) afterwards.

## Key Decisions

1. **Actions are recipes; runs are calls** — the developer writes an action once (name, body with
   placeholders, declared arguments, type, requirements) and every event calls it with values.
   Separating the recipe from the call is what lets a timer, an agent and the developer all
   produce identical, validated prompts, and what makes the Actions view a catalogue rather
   than a scratchpad.
2. **Compose at call time, validate at save time** — a run's prompt is fixed when the call is made,
   so editing an action never rewrites a queued run; and an action's shape (type, fields,
   placeholders versus arguments) is checked when it is saved, so a call can only fail on its own
   values.
3. **Unreferenced arguments are appended, never dropped** — a supplied value always reaches the
   prompt: substituted where the body names it, appended under its name otherwise. Silently losing
   an argument would make a recipe lie.
4. **Events are the single entry point** — user, timed and triggered events differ only in who
   calls and when; they share validation, composition, acknowledgement and attribution. There is no
   back door that creates a run.
5. **The owning daemon holds the relationship, not the orchestrator** — the controller is part of
   the sandbox's own record. That is the only place the one-controller rule can be enforced
   without a race, it lets the TUI hide and surface workers without talking to the orchestrator,
   and it keeps workers correct through an orchestrator outage.
6. **Hidden, not removed** — a worker is a normal sandbox that the everyday lists omit. Every
   operation still works on it; group memberships persist; the Orchestrator Sandboxes tab and the
   needs-input rule are the two ways back in; the third line keeps its wiring visible wherever it
   does appear.
7. **"Free" is the agent's own word; "done" is the agent's report** — availability is derived
   from the agent status the daemon already tracks (idle / working / awaiting input / exited)
   plus "no run in flight on it". The orchestrator never guesses from timing or output, and the
   developer's own prompts count as busy exactly like orchestrated ones. Completion is not
   inferred from idleness either: the agent says it is done, with a summary, through the gateway.
8. **Order of receipt, capacity-aware** — runs are never reordered, but a run nobody can take yet
   does not hold up one that a free sandbox can. Strict head-of-line blocking was considered and
   rejected: one busy target, or one `reason` run waiting on the reasoning sandbox, would idle every
   other sandbox.
9. **The reasoning sandbox is a worker with a reserved role** — it follows every worker rule
   (free, hidden, surfaced, listed) so nothing about `reason` runs is special except their target;
   it is simply not transferable or detachable, because it belongs to the orchestrator.
10. **Access through the gateway follows attachment** — a sandbox gets the orchestrator's tools
    because it is a worker and loses them because it is not; the orchestrator refuses on identity
    rather than trusting that a gateway entry was removed. Instructions are pushed to the agent so
    delegation works on the first prompt, not after exploration.
11. **Orchestrators are placed, not registered, and hosted, not spawned** — an orchestrator is a
    role the machine's existing daemon runs in-process, so creating one needs no new binary,
    process, host entry, credential or port; the daemon's own lifecycle, registry and connection
    carry it, and its name is the handle everywhere the developer looks. A separate orchestrator
    process per orchestrator was considered and rejected as a second thing to ship and supervise
    for no isolation the developer asked for.
12. **Durable queue, reconciled restart** — an orchestrator that restarts must not lose or repeat
    work; in-flight runs are re-checked against their sandboxes rather than re-sent; timed events
    resume without replaying what they missed.
13. **The developer always wins** — manual prompts, stop, destroy and detach all take precedence;
    the orchestrator yields and records the consequence (busy, failed, detached) instead of
    fighting it. Detach is refused only while a run is actually on the worker, to keep the run's
    record honest.
14. **No cancelling in flight** — cancelling a running prompt means interrupting an agent mid-task;
    that is a sandbox operation, not a queue one, and is out of scope here.
15. **Idle without a report is a review, not a completion** — an agent can forget to report, and
    an agent that stopped early looks the same as one that finished. Treating idleness as success
    would free the worker and bury the mistake; treating it as failure would discard good work.
    Holding the run for the developer, surfacing the worker and offering one keystroke to mark it
    free keeps the queue honest at the cost of a human glance.
16. **Dedupe means one run at a time, per event, on by default** — the point of a polling event is
    the latest state, not a backlog, and a run in progress already reflects it; counting in-flight
    runs as well as queued ones guarantees a deduped event never runs back-to-back when a run
    outlasts its interval. Counting only queued copies was considered and rejected for exactly
    that pile-up. Events that genuinely want every firing switch dedupe off.
17. **The briefing is pushed and complete** — an orchestrated agent is told what it is, how to
    behave, which tools it has, how each action type flows and that every run ends with a report,
    before its first prompt. Nothing about the orchestrator is left for the agent to discover.
18. **The orchestrator dials out over SSH, like the TUI** — one direction, one mechanism, no new
    port or credential store: the orchestrator's machine needs SSH access to its workers' machines
    and nothing else. Worker daemons dialling in was rejected because it spreads the orchestrator's
    address and a credential to every worker machine; a TUI relay was rejected because it would
    make unattended orchestration impossible.
19. **Name for people, identity for records** — the developer only ever sees and types the name,
    but every record that points at an orchestrator (worker records on other machines, runs, host
    entries, gateway entries) holds a stable identity. That is what makes rename a one-machine
    label change instead of a rewrite across every daemon, some of which may be unreachable at
    the time.

## Assumptions

- **Placement means "run by that machine's daemon"**: an orchestrator is created on a machine the
  TUI is connected to, hosted in-process by that machine's daemon (clarified 2026-10-14) and
  reached through the TUI's existing connection to it. Its reasoning sandbox is launched on that
  same machine.
- **Reaching worker daemons**: the orchestrator's daemon dials sandboxes' owning daemons over SSH
  from its own machine with that machine's keys or agent (clarified 2026-10-14); host entries are
  per orchestrator and copyable from the TUI's list. It never needs a direct path to a sandbox,
  and a worker's machine never needs a path back.
- **Worker requirements are a tag in this feature**: the only requirement a `prompt` action can
  declare is a worker tag (the sandbox's existing purpose label). Requirements on kits, sources or
  agent kind are natural later additions to the same field.
- **Placeholder form**: one documented form (for example a braced argument name) chosen in planning;
  the spec requires only that a placeholder names a declared argument.
- **Appended-argument format**: each appended argument is written on its own line as its name
  followed by its value; the exact layout is chosen in planning.
- **Attach at creation is a launch-flow choice**: the launch wizard offers the orchestrator pick;
  saved configurations are unchanged in this feature (they do not record an orchestrator).
- **The orchestrator does not start sandboxes**: a stopped worker is not free until the developer
  starts it. Auto-starting workers is a natural follow-up, not part of this feature.
- **Completion is reported, not observed**: the orchestrator does not capture or interpret agent
  output; the summary in the completion report is the only account of a run the orchestrator
  holds, results are read in the sandbox's session as today, and an agent that delegated a run
  learns its state, outcome and summary through the check tool. A short grace period between the
  agent going idle and the run becoming awaiting review (for a report still in transit) is chosen
  in planning.
- **Mark free closes the run as completed unless the developer says otherwise**: the keystroke
  offers completed (default) or failed and an optional note; the run record always shows that the
  developer, not the agent, closed it.
- **The briefing's carrier is chosen in planning**: it may be written into the sandbox's
  workspace, delivered as agent instructions, or both; the spec requires only its content, its
  timing and that it stays accurate.
- **Timed events dedupe by default and do not replay**: at most one run per event queued or in
  flight while dedupe is on, and no catch-up after downtime, because the example use (polling an
  external system every few minutes) wants the latest state, not a backlog.
- **No HTTP intake in this feature**: webhook sources (an HTTP endpoint with per-source
  credentials and idempotency keys) were deferred on 2026-10-14; external systems such as a code
  host are reached by polling them from a timed event's prompt. When webhooks are added they will
  be a fourth event kind calling a named action in the orchestrator's own call shape, not a parser
  of third-party payloads.
- **Needs-input ordering applies to the whole list**: the "needing input at the top" rule is
  applied to every sandbox in every tab, not only to workers, because the rule is about what needs
  the developer and a split ordering would be confusing.
- **Notification policy**: reported completions are silent in the inbox; failures, input requests
  and runs awaiting review are not. This can be revisited if completions turn out to be wanted.
- **Scheduling is FIFO only**: no priorities, deadlines, automatic retries or rate limits in this
  feature; each is a candidate later action field or orchestrator setting.
- **One controller per sandbox, no sharing between orchestrators**: orchestrators do not share
  queues, actions or workers; a sandbox is moved by detach then attach.
- **Retention**: finished runs are kept for a bounded, documented window (count or age), chosen in
  planning; the queue itself is never trimmed.
- **Prompt and argument size**: a documented maximum applies to the composed prompt and to each
  argument value; the values are chosen in planning to match what the agent's prompt path accepts.
- **Default agent for the reasoning sandbox**: the default empty setup uses the machine daemon's
  default agent and configuration, the same ones a bare launch would use.
