# Contract: TUI surfaces (SWI-17)

This delta is on top of `specs/010-jobs-screen/contracts/jobs-ui.md` and SWI-9's forms. Agent- and remote-provided
text is sanitised before display, as today (010 R10).

## Action form (Jobs › Actions `a`/`e`, and the orchestrator's Actions view) — FR-218, FR-203, FR-210

New fields are added after **Arguments**, in this order:

| Field | Control | Values / default |
|---|---|---|
| Model | Select | `off`, `opus` (default), `fable`, `sonnet`, `haiku` |
| Effort | Select | `off`, `low`, `medium` (default), `high`, `xhigh`, `max`, `auto` |
| Clear after run | Confirm | yes (default) |
| Pre-prompts | Text (4 lines, enlargeable with ctrl+e) | one prompt per line, sent in order before the action prompt |
| Post-prompts | Text (4 lines, enlargeable) | one prompt per line, sent in order after the run closes |

- **Off keeps the value.** Choosing `off` keeps the last value in `ActionSequence` (`model_enabled = false`), and
  choosing a value again re-enables the built-in.
- **Existing actions** open with their effective sequence. An action saved before this feature shows the defaults
  (FR-210).
- **Saving.** On save, the lines are trimmed and empty lines are dropped. Server validation errors (FR-204) show on
  the form's status line verbatim, naming the entry: `pre-prompt 2: {skill} may not be in the first word`.
- **Unsaved-changes guard (SWI-9).** The guard's snapshot includes the five new fields.

## Action line (Jobs › Actions, orchestrator Actions view) — FR-219

`name (type) description #tag {args*}` gains a second segment when the action has any step:

```text
triage (prompt) Triage a ticket {ticket*}   pre: /model opus · /effort medium · 1 custom — post: 1 custom · /clear
```

The segment is omitted when no step is enabled.

## Run rows (Overview *In progress*/*History*, Queue tab) — FR-213, SC-006

The state word is followed by the step while a sequence is in flight:

| Run | Row (after the action name) |
|---|---|
| Setup step 2 of 3 | `running · setting up 2/3 on alpha-w1 · 12s` |
| Action step | `running on alpha-w1 · 3m` (as today) |
| Closed, cleaning up 1 of 2 | `completed · cleaning up 1/2 on alpha-w1 — <summary snippet>` |
| Halted | `running · setting up 1/3 not accepted: Model 'bogus' not found` / `completed · Clear not accepted: picker opened` |
| Halted after a restart | `… not accepted (sent before a restart)` |
| Halted after an unconfirmed send | `… not accepted (may have run)` |
| A step waiting over 10 s | `running · setting up 2/3 waiting: input box not empty` |
| Holding its sandbox | `completed · needing the developer on alpha-w1` / `… needs input (freeing) …` |

*In progress* lists every run that holds a sandbox or is halted, including closed ones (FR-213). The server returns
them in `ListRunsResponse.in_flight` and not in `history`, so they appear once, and the same way in every view.

`t`/`T` on such a row opens the worker's terminal, also for a closed run that still holds its sandbox: the terminal
is the way out of a hold. After an owning-daemon restart, a sandbox may have a **detached agent** (an orphan,
research R14). The daemon only detects and surfaces it (an *agent detached* notification, once); only the developer
stops it:
- **`t`/`T` on any sandbox with a detached agent** (a worker or a plain sandbox) opens a dialog instead of the
  terminal: "<sandbox>'s agent was detached by a daemon restart (last seen <working|waiting for input|idle> <time>[
  on <run>]). It can't be shown or typed into. Replace it? Replacing stops it[ and fails <run>]." The choices are
  **Wait** and **Replace**. Wait is the default when the agent was last seen working or waiting for input; Replace
  is the default when it was last seen idle. Replace re-attaches with `replace_orphan` and shows `replacing the
  detached agent…` until the terminal opens. While the sandbox is being stopped, restarted, refreshed, recreated or
  destroyed, Replace is refused with `sandbox is busy (<operation>); try again`, and `t` shows the gate's
  operation-specific message (`sandbox is restarting`, `… being refreshed`, …), never "stopping" for a start.
- **After a daemon restart, only sandboxes that may have a surviving agent are checked**; every other worker is
  dispatchable at once. While a check runs the row reads `checking for a detached agent…`, idle workers first.
- **An idle worker or a closed run with a detached agent** is never freed automatically: the worker row reads `agent
  detached by a daemon restart — t to Replace or Wait`, dispatch skips the worker, and a closed run's hold is
  *needing the developer* until Replace.
- **Bulk Replace: `R` on the orchestrator's Workers view** (run-595). It opens one confirm listing every worker whose
  `agent_check` is DETACHED with no run, all ticked (space toggles):
  `Replace N idle detached agents? Each is stopped and a fresh agent starts.`
  - `y` calls `ReplaceDetachedAgent` on each worker's owning daemon (`connectedDaemon(host)`) and reports
    `replaced 5 · busy 1 (stopping) · failed 1: <error>`.
  - With none, the status reads `no idle detached agents`.
  - Workers with a run are not listed; they go through `t` → Replace with its own confirm, because that fails the
    run.
  - Help: `R replace idle detached`.
- **A sandbox whose detached-agent check keeps failing** (still unknown after `orphanUnknownLimit`, 60 s, or 3 probe
  failures) raises one *agent check failed* notification and shows `can't check for a detached agent: <error>`. A run
  it holds gets the note `can't confirm the agent is still running: <error> — t to Wait or Restart sandbox; x cancels
  the run`, so it is never silently in flight. `t` offers **Restart sandbox** (behind a confirm naming the run it
  fails, if any) and **Wait**. A stop
  of the agent alone would need the same failing `sbx exec`; restarting the sandbox ends every process in it.
- **A worker whose detached agent asks for input** has the note `detached agent asks for input; it can't be answered
  here — replace it (fails the run) or wait`.
- **A step halted by the restart** carries its evidence in the note (`agent detached by a daemon restart; last seen
  working 10:42; R restarts setup with a fresh agent`), so the developer can judge without a terminal.
- **Every detached agent** raises one *agent detached* notification when it is found, naming the sandbox, its last
  state and its run. Those found by the start-up probes are reported together in **one** notification per daemon
  start: `N agents detached by a daemon restart (M on idle workers) — R on the Workers view replaces the idle ones`. Only the developer stops it (Replace, `R`, or a sandbox stop/restart); a replaced ACTION run
  fails with the usual *run failed* notification.

`openRunOfEvent` (the timed-event "already open" check) skips closed runs, so a holding run listed in `in_flight`
doesn't block its event. The orchestrator summary's in-flight count matches the list.

## Run-row keys — FR-222

These are added to the run-row help only when the run is halted:

| Key | Action | Notes |
|---|---|---|
| `R` | Retry the step | For a step *sent before a restart*, it first opens the confirm "This step may already have run — check the worker's terminal. Retry anyway? (y/N)". While the step's text is still in the box the row says `clear the step's text in the worker's terminal (t) to retry`. The same confirm is used for a step marked *may have run* (UNCONFIRMED, research R2). For a step halted by an **owning-daemon** restart (note `agent detached by a daemon restart`), `R` is **Restart setup** instead, with one confirm whatever else the step is marked: "Stop the detached agent (last seen <state> <time>) and restart setup from the first step? Anything it was doing is lost. (y/N)" (default No). Then the orphan is stopped, a fresh agent starts, and every pre-step is sent again from the first (research R14). |
| `S` | Skip the step | Not offered for Clear or for the action step (skipping it would leave the run waiting for a report that can't come), nor for a step halted by an owning-daemon restart (the agent is replaced, so setup restarts with `R`). Listed **before** `R` for a step sent before an orchestrator restart, or marked *may have run*. |
| `X` | Fail | Confirm, defaulting to **No**: "Fail <run> at <step>? (y/N)". A pre-prompt or the action step fails the run, naming the step (FR-212). No custom post-prompt follows, and Clear (if enabled) is sent once the agent is idle (FR-214 as amended). A custom post-prompt drops the remaining custom post-prompts; **Clear (if enabled) is still sent**, and the sandbox is free once it finishes (FR-216, FR-222). Clear leaves the sandbox *needing the developer*, and `R` stays on the row to retry Clear (FR-222). `X` is offered only on halted runs and `x` (cancel) only on queued ones, so a Shift slip opens a confirm or does nothing. |

All three call `ResolveRunStep{run_id, step, resolution}`. A `FAILED_PRECONDITION` ("the run moved on") shows on the
status line and the view reloads.

## Prompt overlay (`enter` on a run) — FR-220

The overlay lists the snapshot in order, marking the current step with `▶` and finished ones with `✓`:

```text
pre   ✓ /model opus
      ✓ /effort medium
      ▶ /pre-setupskill
action  [switchboard orchestrator …] (composed prompt, as today)
post    /tear-down-skill
        /clear
```

A run without steps shows only the composed prompt, as today.

## Sandbox list and Workers view — FR-213, FR-216

**Worker suffix** (`sandboxItem`), with `<run>` = the run id:

| Condition | Suffix | Surfaced first |
|---|---|---|
| `controller.run.hold == CLEANING_UP` | `⚑ worker · cleaning up <run>`, or `… · waiting: <reason>` once its step has waited over 10 s | no while it progresses; **yes** once it waits or is halted |
| `== NEEDS_INPUT_FREEING` | `⚑ worker · needs input (freeing) <run>` | yes |
| `== NEEDS_DEVELOPER` | `⚑ worker · needing the developer — open its terminal and run /clear <run>` | yes |
| `controller.run.halted` | `⚑ worker · <run> halted: step not accepted` | yes |

The orchestrator's **Workers view** reads `WorkerView.hold`/`halted` (proto-delta) for the same text.

**Mark free on a held worker.** `markFreeKey` also accepts a sandbox with `hold == CLEANING_UP` whose agent is
NEEDS_INPUT (FR-216 freeing). The run is already closed, so it calls `MarkWorkerFree` **directly**: no outcome or
note dialog, and the run's outcome is not changed.

**Surfacing.** `needsDeveloper(sb)` adds the last three rows to today's rules, so they sort to the top of the All,
host and group tabs (009 surfacing rule).

**Opening the terminal.** `t` on a held worker opens its terminal as today. Attaching starts an agent if none is
running, which reports `SessionStart` (`startup`), and that ends a *needing the developer* hold (FR-216). A stopped
sandbox answers "start the sandbox before opening its terminal", as today.

## Notifications

`RUN_STEP_NOT_ACCEPTED` shows in the inbox and on the desktop like `RUN_FAILED`: "<run> (<action>): <step> not
accepted — <reason>".
