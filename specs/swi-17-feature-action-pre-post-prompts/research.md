# Research: Action Pre & Post Prompts (SWI-17)

Phase 0 of [plan.md](./plan.md). Each section is one decision: what was chosen, why, and what else was weighed. R1 is
the spec's **planning gate** (Assumptions: "Finished" is evidence after the submit). It was run on a real agent
before anything else was designed.

## R1 — How Claude Code handles the built-ins (planning gate, probed)

**Method.** On 2026-10-28, Claude Code **2.1.312** was driven in a real PTY (140×45, `TERM=xterm-256color`), with a
throwaway `--settings` file that logged every `UserPromptSubmit`, `PreToolUse`, `Notification`, `Stop`, `SessionStart`
and `SessionEnd` hook input, plus a `statusLine` command that logged its JSON. Each command was typed (or delivered as
one bracketed paste, as the daemon types prompts today), followed by Enter, and the screen was rendered through the
TUI's VT renderer and polled every 25 ms. No model call was made.

**Findings.**

| Command | Hooks raised | Screen after Enter (confirmation line under the echoed command) | statusLine JSON |
|---|---|---|---|
| `/model opus` · `fable` · `sonnet` · `haiku` | none | `⎿  Set model to Opus 5.5 and saved as your default for new sessions` (Fable 5.1, Sonnet 5.5, Haiku 5.5) | `model.id` = `claude-opus-5-5` / `claude-fable-5-1` / `claude-sonnet-5-5` / `claude-haiku-5-5` |
| `/effort low` · `medium` · `high` · `xhigh` | none | `⎿  Set effort level to <level> (saved as your default for new sessions): …` | `effort.level` = `<level>` |
| `/effort max` | none | `⎿  Set effort level to max (this session only): …` | `effort.level` = `max` |
| `/effort auto` | none | `⎿  Effort level set to auto` | `effort.level` = the **resolved** level (`high` in the probe), never `auto` |
| `/effort ultracode` | none | `⎿  Ultracode on (this session only): dynamic workflows on every task. Effort stays medium.` | unchanged; the border shows `ultracode` |
| `/model bogus` | none | `⎿  Model 'bogus' not found` | unchanged |
| `/effort bogus` | none | `⎿  Invalid argument: bogus. Valid options are: low, medium, high, xhigh, max, auto, ultracode [on\|off]` | unchanged |
| `/clear` | `SessionEnd` (`reason: "clear"`), then `SessionStart` (`source: "clear"`) | the screen is redrawn with the banner and an empty prompt | new `session_id` |
| `/model` (bare) | none | a **picker**: `Select model`, numbered rows with `❯` on the current one, `Enter to set as default · s to use this session only · Esc to cancel`; the input box is gone | — |

Other findings:

- **Latency.** Every confirmation line appeared 50–135 ms after Enter (typed or pasted), well inside today's 2.5 s
  submit window. The input box shows an empty `❯ ` prompt at the same time.
- **Paste.** A command delivered as one bracketed paste followed by Enter behaves exactly like a typed one.
- **No hook for the setting built-ins.** `/model` and `/effort` raise no hook at all, and an invalid value leaves the
  same empty, ready box as a valid one. So FR-206b's **read-back is required** to tell them apart. An empty box alone
  would accept `/model bogus`.
- **`/clear` is observable by hook.** It raises `SessionStart` with `source: "clear"`, the same signal FR-216 uses to
  end a hold.
- **`ultracode` is not an effort level.** It is a session-wide toggle that `/effort <level>` does not turn off and
  `/clear` does not reset, so a run choosing it would leak it into every later run on the worker. **Decision (developer, 2026-10-28):
  drop it from the Effort options.** The spec is amended (FR-202, US1 #5, Clarifications).
- **Side effect: "saved as your default for new sessions".** `/model` and `/effort` (except `max`) persist the choice
  in the **worker's** user settings. That is harmless for the feature: every run whose action enables the built-in sets
  it again. A run whose action disables a built-in inherits whatever the last run set, which is today's behaviour
  ("whatever someone last set", US1).
- **Startup prompts.** A fresh agent can open a one-off dialog before its first prompt, for example "Make auto mode your
  default permission mode?". Today's delivery already waits for an idle, ready agent. The step tracker treats such a dialog
  as "no empty, ready input box", so nothing is typed into it (FR-206d).

- **Not yet probed; closed during implementation (T006, T046).**
  - **Idle nudge.** Whether every worker's hook forwards `notification_type` was not confirmed. Old-style hooks
    infer idle only while the agent is IDLE or WAITING (`agent/hooks.go`), so a needs-input-only step could otherwise
    never finish. The spec's fallback is planned as a task: a step classified MODEL_TURN_INPUT_ONLY also finishes once
    the input box has stayed empty and ready for the nudge interval (60 s) with the agent not WORKING. The daemon
    records that as `AgentSession.input_idle_since`, sampled while a run is on the sandbox.
  - **Report latency.** No model turn was made in the probe, so a skill turn's `UserPromptSubmit` latency was not
    measured. A slow report would end the 2.5 s window as AGENT_LOCAL for a custom prompt that is really a turn;
    the capture helper measures it on a real skill, and `submitWindow` is raised if needed.
  - **Multi-line paste.** The framed action prompt is pasted as several lines; its box rendering (a paste
    placeholder) is captured as a fixture (R2).

**Decision.** The built-ins are **agent-local** with a **screen read-back**:

- Model: the confirmation line must start `Set model to <Family>` with the chosen family (Opus, Fable, Sonnet, Haiku).
- Effort: the line must be `Set effort level to <level>` for low…max, or `Effort level set to auto` for auto.
- Clear: needs the `SessionStart` hook with `source: "clear"` observed after its submit, plus an empty, ready input box.

Any other line under the echoed command (`not found`, `Invalid argument`, a picker, nothing at all) is **not
accepted** (FR-206c).

**Alternatives considered.**

- **statusLine JSON as the read-back.** Rejected as the primary signal:
  - It cannot confirm `auto`, which reads back as the resolved level.
  - Injecting a `statusLine` into the worker's `settings.local.json` would replace the developer's own status line.
  - It updates on redraw, not on the command.

  The screen line is needed anyway for the input-box checks (R2).
- **Apply model and effort at the agent's launch** (`--model`, settings). Rejected: a run's choice must apply to the
  agent that is already running, and FR-206b's read-back is available, so the spec's fallback is not needed.
- **A timed wait after each built-in.** Forbidden by the spec's planning gate.

## R2 — Reading the agent's screen on the daemon (input box, pickers, read-back)

**Context.** FR-206b–d need three facts about the worker's screen at a given moment:
- whether the input box is **empty and ready** (no picker or dialog);
- whether the **step's own text is still in the box** (the only case where Enter may be re-pressed);
- which **confirmation line** follows the echoed step (R1 read-back).

Today the daemon has no screen model. `terminal.Broadcaster` keeps only a raw byte ring for replay (`ring.go`). The
only VT renderer in the repo is the TUI's `internal/termview` (a `charmbracelet/x/ansi`-driven `Screen`).

**Decision.**
1. **Move `termview` into a library module, `src/libs/switchboard-termview`**, as 009 did with
   `switchboard-dial`. The TUI imports it from there, and nothing else about it changes.
2. **Give the daemon a screen for each controlled sandbox.** Only a broadcaster whose sandbox has an orchestrator
   controller keeps a screen; other sandboxes pay nothing. The read loop, the single PTY reader, hands each chunk to
   a per-broadcaster **parser goroutine** through a bounded channel and goes straight on to fan-out, so VT parsing
   never runs under the broadcaster mutex and never delays clients or `Attach`. On overflow (for example a large
   `cat`) the channel drops chunks and marks the screen **stale**. The parser goroutine then recovers on its own:
   it drains the channel, **resets** the screen (dropped chunks may have cut an escape sequence in half), and issues a
   debounced resize nudge itself (at most one per second), so the agent repaints and the screen is fresh again. While
   stale, the guard refuses (`screen resyncing`) and the submit check treats the screen as **unknown**: it extends
   its window rather than deciding (bounded by contracts/worker-step.md §6). It never halts on a stale read.
   - **A screen is enabled lazily.** Broadcasters persist across detach, so a sandbox whose terminal was opened before
     it became a worker already has one without a screen. When a sandbox becomes controlled (attach) or first gets a
     step, `EnableScreen` adds the screen to the existing broadcaster and nudges a repaint. The daemon's screen uses termview with
   **no scrollback** (a new option; the TUI keeps its 5000 lines). The screen is a `termview.Screen` sized to the PTY
   and resized with it (`applyResizeLocked`). Today a PTY gets no
   size until a client attaches (`pty.Start` sets no winsize), so a broadcaster created with no client sets both to
   `stepScreenSize` (140×45, the size R1 probed) and nudges a resize; the agent repaints, and a broadcaster created
   after the agent drew its box still sees one. The screen has its own lock, separate from the broadcaster's mutex. `Broadcaster.Screen()` returns a read-only snapshot (rendered lines plus each cell's
   faint attribute) on demand.
3. **Add `agent/claudeui.go`, a pure reader over that snapshot**, pinned by fixtures captured in R1:
   - `InputBox(lines) (text string, ready bool)`. The box is every row between the last two horizontal rules
     (`────`), starting at the `❯ ` row; wrapped rows are joined. A paste placeholder (`[Pasted text #N +M lines]`,
     shown for a multi-line paste such as the framed action prompt) counts as the step's text still in the box. Text drawn **faint** after `❯ ` is the placeholder hint (`Try "…"`) and counts as empty. Ready means
     the box is present and no modal marker is on screen (`Esc to cancel`, `Enter to select`, a `Select …` header
     with numbered `❯ n.` rows, the startup dialogs).
   - `ConfirmationAfter(lines, stepText) (line string, ok bool)` finds the last echoed `❯ <stepText>` and returns the
     `⎿  …` line under it.
   - Matchers for the built-ins: `Set model to <Family>`, `Set effort level to <level>`, `Effort level set to auto`.

**Rationale.**
- The screen is the only place the agent reports a `/model` or `/effort` result (R1), and the only place the input
  box can be seen. A screen model is unavoidable for FR-206b–d.
- Feeding it from the existing single reader adds no second consumer, so no read race.
- **Failure direction is safe.** Any screen the reader does not recognise reads as *not ready*, or *no
  confirmation*. That leads to a **not accepted** halt the developer can see (FR-206c), never to a step typed into
  something unexpected.
- Pinning the matchers to captured fixtures makes a Claude Code UI change fail a unit test, rather than surface as
  silent wrong behaviour.

**Alternatives considered.**
- **Import `charmbracelet/x/vt` in the daemon.** That is a new dependency, and `termview` already does what is needed.
- **Parse the raw ring.** It's unreliable with Claude Code's full-screen redraws.
- **statusLine JSON.** See R1.

## R3 — Where the step tracker lives, and how one step is delivered

**Decision.** Responsibility is split:
- **The orchestrator owns the sequence and its persisted state** (on the `Run`).
- **The sandbox's owning daemon types each step and runs its submit check locally.** It has the hooks, the agent
  status and the screen, all with low latency.

There is one new worker-side RPC, **`DeliverWorkerStep`** (contracts/worker-step.md). It is called through the
existing `WorkerAPI`, so it is the same path for local and remote hosts:

1. **Guard** in the same `reg.Update` as today's `Deliver`:
   - the sandbox is RUNNING and controlled by this orchestrator;
   - the agent is at its prompt (IDLE);
   - the screen's input box is **empty and ready** (R2).

   Additionally, for the first step (the run is not started yet) the controller has no run, or already has this
   run (a retried first step), and records `WorkerRun{id, action, step_role}`. For later steps `controller.run.id`
   must equal the run's id, and `step_role` is updated. In the same update the guard writes `step_index` and
   `step_sent_at` on `WorkerRun`, and refuses (`ALREADY_EXISTS`) a step index already recorded: **a step is never
   typed twice**, whatever the orchestrator retries (FR-217).

   Before the guard the daemon reads the screen **without creating anything**. Creating a broadcaster runs the agent
   factory, which spawns `claude` when none is live, so the step path creates one only while the agent is live (R2).
   With `first`, no live agent and no hold, it starts one as SWI-15's `Deliver` does and refuses this call (`agent
   is starting`), and a later retry types the step. In every other case with no live agent it refuses (`no live
   agent`) and starts nothing, so a retried Clear or a cleanup step can never spawn an agent whose `startup` would
   end a hold (FR-216).

   Built-in text is derived and checked on the daemon, and call values were checked for control characters at call
   time (R13).

   A guard miss returns `FailedPrecondition` with the reason. Nothing is typed, and the orchestrator tries again on
   the next status change (R6). That is how *developer activity takes precedence* (009 FR-119): text the developer
   typed keeps the box non-empty.
2. **Type** the step as one bracketed paste, then Enter (as `Registry.Prompt`). The action step is wrapped in the
   run frame (`worker.Frame`); pre- and post-steps are sent bare (spec Assumptions: Run frame).
3. **Submit check** (`submitCheck`), driven by a hook tap (R4) and the screen. It runs for `submitWindow` (2.5 s,
   today's `submitCheckDelay`), extended by each re-press, which is allowed only while `InputBox` still shows the
   step's text (FR-206d, at most 3, as today). It returns one outcome:
   - **MODEL_TURN**: a turn-start report since the submit. That is `UserPromptSubmit`, `PreToolUse`, or a
     `Notification` other than `idle_prompt`/`auth_success` (a needs-input report counts, FR-206b).
     `SessionStart`/`SessionEnd` never count (R1: `/clear` raises them).
   - **AGENT_LOCAL**: the window ended with no turn-start report, the status is still IDLE, and the box is empty and
     ready. For a built-in, its read-back must also pass: the R1 confirmation line for Model and Effort, or a
     `SessionStart` with `source: "clear"` after the submit for Clear.
   - **NOT_ACCEPTED** with a reason, one of:
     - `text_in_box`;
     - `picker` (a modal appeared before any report, which also ends the check at once with no Enter);
     - `readback` (the line seen, for example `Model 'bogus' not found`);
     - `not_ready`.
4. The RPC **returns the outcome** within `submitWindow × (1 + re-presses)` (≤ 10 s, inside the 15 s
   `deliverTimeout`).

`DeliverWorkerPrompt` stays as it is and is still used for runs with **no** sequence (FR-208, SC-005).

**Rationale.**
- The submit check needs hook events at millisecond granularity, and needs the screen. Running it next to them
  avoids streaming either over the link.
- The orchestrator keeps the decisions that must survive restarts.
- One synchronous RPC per step keeps ordering trivial: the orchestrator never has two steps outstanding on a
  sandbox.

**Alternatives considered.**
- **Orchestrator-side check over `sandbox_changed`.** Rejected: the status loses the event kind, so a `SessionStart`
  after `/clear` would look like a turn start, and the screen isn't visible there.
- **A long-lived "run the whole sequence" call on the worker.** Rejected: it would move persisted run state to the
  worker daemon, which may not be the orchestrator's, and would duplicate restart handling.

## R4 — Hook tap and new per-agent evidence

**Decision.**
- **Hook tap.** `agent.HookServer` gets `Tap(sandboxID) (<-chan HookEvent, cancel)`. `HookEvent` is `{Event, Source,
  NotificationType, BackgroundTasks, At}`, delivered after `dispatch` applies the status. Only the submit check
  subscribes, and only for its window.
- **Recorded evidence.** `AgentSession` gains these (proto, contracts/proto-delta.md):
  - `last_submit_at`: the last `UserPromptSubmit`;
  - `last_turn_end_at`: the last `Stop` with no background tasks pending;
  - `last_session_start_at` and `last_session_source`: the last `SessionStart`.

  They are set by the daemon's hook handling and persisted in the sandbox record (`Manager` writes them with the
  status). They reach the orchestrator in `sandbox_changed`.

**Rationale.**
- Ending a *needing the developer* hold (FR-216) and restart resolution (FR-217) both need to know **which** report
  arrived **when**, not just the current status.
- These fields are cheap: the hooks already arrive.
- They resolve the spec's "Restart evidence" assumption **automatically**:
  - a turn whose submit and end both happened during an outage now shows as `sent_at < last_submit_at <
    last_turn_end_at`;
  - FR-217 halts *sent before a restart* only when that evidence is absent.
- Hold release does **not** use these fields: the daemon ends a hold live from the hook itself (R7).

**Alternatives considered.**
- **Keep evidence in memory only.** Rejected: it would be lost on a daemon restart, which is exactly when FR-217
  needs it.

## R5 — Run state machine with a sequence

**Decision.** The `Run` gains a snapshot of its steps, plus `step` (index), `step_status`, `step_sent_at`,
`step_note`, `step_sent_before_restart` and `hold` (data-model.md). The orchestrator's `runtime` changes in
`internal/orchestrator/sequence.go`:

- **Delivery.**
  - `dispatch` claims the worker for a run with steps and starts `deliverStep(run, 0)`; it does not wait for it.
  - **Single-flight per sandbox.** Step delivery and `advance` run on one goroutine per held sandbox
    (`stepper`), so dispatch and `foldStatus` (which runs in the host's event handler) never block on a 2.5–10 s
    submit check, and at most one `DeliverWorkerStep` is in flight per sandbox. `sandbox_changed`, the retry
    ticker and `ResolveRunStep` only *kick* the stepper.
  - **Kicks are coalesced, never dropped.** A kick that arrives while a call is in flight sets a pending flag that
    the stepper consumes when the call returns. After **every** response the stepper also re-evaluates
    `stepFinished` against the latest sandbox record, so a fast turn whose IDLE `sandbox_changed` landed during the
    call still finishes.
  - Before each call the step is persisted as **SENDING**. The response moves it to SENT/TURN/HALTED, or back to
    PENDING on `FailedPrecondition`. `ALREADY_EXISTS` means it was typed before (a lost response): SENT with
    `controller.run.step_sent_at` read from the sandbox record (a gRPC status carries no timestamp).
  - **A step left SENDING** (a lost response, a timeout, a link blip) is resolved **in the MVP**, on the next
    `sandbox_changed` or reconnect, from the daemon's record: `controller.run.step_index` equal to the step → SENT
    with `controller.run.step_sent_at`, then finished or halted per R8; lower → PENDING (never typed). So a lost
    response never strands a run (tasks T022).
  - **A response for a run cancelled or failed before delivery** (while its first step was in flight), with no hold
    and no cleanup pending, makes the stepper clear the daemon record with `UpdateWorker{set_run, expect_run_id =
    run}` (unset run). Any other late response goes through the normal path: for example, the action step's response
    arriving after the report has closed the run and started cleanup only updates the step. It never clears the
    record, which would skip cleanup and free a worker with its context (SC-001). The
    orchestrator's reconcile also clears any worker record naming a finished run that isn't holding, so a
    cancel/claim race can never leave a worker taken forever.
  - `startRun` (RUNNING, `StartedAt`, sandbox fields) runs on **any** outcome where the first step was typed,
    including NOT_ACCEPTED, so a halted first step is a RUNNING run with its controls. A `FailedPrecondition` on the
    first step writes nothing on the daemon (the guard commits only when every check passes) and leaves the run
    QUEUED; the orchestrator retries it like any pending step.
  - **Claim.** Before the first call, dispatch records the sandbox on the run (`sandbox_id`/`sandbox_host_id`) while
    it is still QUEUED. Dispatch skips QUEUED runs with a sandbox set, and counts that sandbox as not free. Cancelling
    such a run, or failing it, clears the daemon's record with `UpdateWorker{set_run, expect_run_id}` (unset run), so
    the worker is freed.
  - A run with no steps keeps `deliver` → `DeliverWorkerPrompt`.
- **`advance(run)`**, called after each finished step and on each `sandbox_changed` while a step is waiting:
  - it sends the next step whose turn it is;
  - **setup → action → (close) → cleanup**;
  - after the action step it waits for the close (completion report or mark free, FR-214), then sends the cleanup
    steps once the agent is idle.
- **Model-turn finish.**
  - A model-turn step is finished when `sandbox_changed` shows the agent IDLE with
    `last_turn_end_at > step_sent_at`, or, for a step classified only by a needs-input report, an IDLE status from
    the idle nudge (FR-206b).
  - Whether the box is empty and ready is then checked by the **next** step's delivery guard (R3). For the last
    step it is checked by Clear's own check, or else the sandbox simply becomes free.
- **Status folding**, changed only for runs with steps:
  - **`runAwaitingReview` itself** refuses unless the action step was accepted (TURN or AWAIT_CLOSE), never while it
    is PENDING, SENDING or HALTED. Gating the function, not only `armReview`, also covers `reconcileInFlight`'s IDLE
    branch on link-up (FR-209, FR-214);
  - EXITED, or the sandbox stopping or being destroyed, during setup fails the run naming the step (FR-212). That
    includes `failRunsOn` (`onSandboxRemoved`, `Detach`), whose reason names the step for a run in setup;
  - during cleanup it drops the remaining steps and sets the hold *needing the developer* (FR-216).
- **Completion report.** `toolReportCompletion` refuses a report until the action prompt was delivered: the action
  step is TURN or AWAIT_CLOSE, **or** the daemon's `controller.run.step_index` equals the action step's index (it was
  typed, and only the response was lost). Otherwise: "the action prompt has not been delivered yet" (FR-211).
- **A halted action step.** `S` is refused for ACTION_PROMPT: skipping it would close nothing and leave the run
  waiting for a report that can't come. `X` fails the run (FR-212 wording, naming the step).
- **After a failure before the action prompt was delivered** (FR-214, amended at planning, run-560), **no custom
  post-prompt** is sent. Clear (if enabled) **is** sent, once the agent is idle, if any pre-step reached the agent.
  The pre-prompts may have left context, and SC-001 says the sandbox never takes its next run until the previous
  run's session is cleared. A run cancelled or failed before any step was typed sends nothing. If the agent can't
  receive Clear (it exited, or is an orphan, R14), the agent's next session is fresh anyway after an exit, so no hold
  is set; an orphan becomes `NEEDS_DEVELOPER`.
- **Run close and cleanup.** `completeRun`/`MarkWorkerFree` close the run exactly as today (FR-215). When cleanup
  steps exist, they **don't clear the worker's run**: they set `controller.run.hold = CLEANING_UP` through
  `UpdateWorker`. The sandbox is then not free by the existing `free()` rule (`controller.run != nil`), so it takes no
  run of either type (FR-215, SC-004).
  - If the sandbox isn't RUNNING, or its agent can't be typed into at close (EXITED, or an orphan after a daemon
    restart, R14), the cleanup steps can't run. They are dropped, and the hold is `NEEDS_DEVELOPER` straight away
    (FR-216), not a hidden `CLEANING_UP` that would stall. This applies only to runs that **still have cleanup to
    run**: a run with no cleanup steps (or one failed before delivery whose agent exited, above) sets no hold.
- **Listing.** `Service.ListRuns` puts closed runs that hold a sandbox or are halted in `in_flight`, and keeps them
  out of `history`, so the TUI's *In progress* group, its *History* group and the orchestrator's own views agree
  (FR-213).
- **Runs that hold a sandbox.**
  - `runOn(sandbox)` is **unchanged** (in-flight runs only): `failRunsOn` loops until it returns nil, and
    `MarkWorkerFree`, `toolReportCompletion` and the reasoning-rebuild check rely on it meaning "in flight".
  - A new `holdingRunOn(sandbox)` returns the closed run holding the sandbox (`hold != NONE` or halted). Folding,
    the stepper, `ResolveRunStep` and hold release use it explicitly.
  - Removing or detaching a held sandbox (`onSandboxRemoved`, `Detach`) ends its holding run's cleanup (DONE,
    `hold = NONE`).
  - `trimHistory` skips runs with `hold != NONE` or a halted step (FR-213 retention).
- **Every `WorkerRun` writer** (`deliver`'s local mirror, `syncReviewFlag`, `repairReviewFlag`, holds) goes through
  `workerRunFor(run, live)`, which copies all fields from the live record; `set_run` replaces the whole message
  (data-model.md WorkerRun).

**Rationale.**
- It reuses the existing free/claim mechanics. A held sandbox is simply one whose `controller.run` is still set, so
  every existing guard (dispatch, `Deliver`, `Detach`) already refuses it.
- Closing the run on time (FR-215) needs no new state.

## R6 — Retrying a step that couldn't be sent yet

**Decision.**
- A `FailedPrecondition` from `DeliverWorkerStep` (box not empty, agent not idle, developer typing) leaves the step
  **pending**. Once refusals have lasted `stepWaitNote` (10 s), the reason shows on the row as `waiting: <reason>`
  (`step_note`), e.g. `waiting: input box not empty`, so a stall is never silent. For a retried step that is
  FR-222's "clear the step's text to retry" message.
- The orchestrator tries again on every `sandbox_changed` for that sandbox, plus a `stepRetryEvery = 2s` ticker that
  runs only while some run has a pending step on a reachable host. The ticker covers a box the developer empties
  without any hook firing.
- **Mixed versions.** `Unimplemented` (an older daemon) on the first step **unclaims** the run (back to QUEUED, no
  sandbox, no daemon record, since the old daemon recorded nothing) and marks that worker, in memory, as unable to
  take sequenced runs until its host's link reconnects. Dispatch then picks another worker. Only when **no**
  attached, eligible worker can take a sequenced run does the run fail: "no worker supports pre/post prompts; update
  their daemons". Every action has steps by default (FR-210), so without this, upgrading only the orchestrator's
  machine would fail every run sent to an older remote worker.
- Any other error (unreachable host, timeout), on any step including the first, is treated like a lost host: the
  step stays SENDING, and on reconnect it is resolved from the daemon's `step_index` record (typed → SENT, then R8;
  not typed → PENDING). A first step resolved to SENT on a still-QUEUED run calls `startRun`. Nothing is re-typed
  blindly, and a run whose step the daemon did type is never failed for a lost response.

**Rationale.** The orchestrator never types into a busy box. The ticker is cheap: one RPC per waiting sandbox every
2 s, and only while a step is actually waiting.

## R7 — Holds on the sandbox (FR-213, FR-215, FR-216, FR-222)

**Decision.** `WorkerRun` (the run recorded on the owning daemon's `Sandbox.controller`) gains `hold` and
`step_role`. The orchestrator sets `hold` through the existing `UpdateWorker{set_run, run}` (it rewrites the whole `WorkerRun`), and keeps the same value on its
`Run`.

| `hold` | Set when | Ends when | Free? | Surfaced (sandbox list) |
|---|---|---|---|---|
| `CLEANING_UP` | the run closed and has cleanup steps | the last cleanup step finished (`controller.run` cleared) | no | hidden while it progresses; **top**, `cleaning up · waiting: <reason>`, once its step has a `waiting:` note (R6) or is halted |
| `NEEDS_INPUT_FREEING` | the developer frees a worker whose cleanup is asking for input | Clear finished (or no Clear) | no | **top**, `needs input (freeing)` |
| `NEEDS_DEVELOPER` | Clear failed by the developer, or the agent exits or the sandbox stops during cleanup | the owning daemon observes `SessionStart` with source `startup` or `clear` | no | **top**, `needing the developer — open its terminal and run /clear` |

Details:
- The sandbox list's `needsDeveloper(sb)` and the Workers view add `hold ∈ {NEEDS_INPUT_FREEING, NEEDS_DEVELOPER}`,
  plus a run halted at a not-accepted step (`controller.run.halted`), to today's NEEDS_INPUT and `needs_review`.
- The daemon's `leaveRunning` keeps a `controller.run` with any `hold`, or with `step_role == CLEANUP`, when the
  sandbox stops, and sets `hold = NEEDS_DEVELOPER` itself. The hold persists across stop/start, and a restarted
  sandbox is never free with an uncleared session, even before the orchestrator hears of the stop (FR-216, SC-001).
- **Ending a hold is the daemon's.** The owning daemon handles the hook: on a `SessionStart` with source `startup`
  or `clear` for a sandbox whose `controller.run.hold == NEEDS_DEVELOPER`, it clears `controller.run`. It does this
  live, from the hook itself, so nothing is compared across machines' clocks and nothing is inferred from recorded
  state (FR-216).
  - When the **agent exits** during cleanup with the sandbox still running, the daemon sets `hold = NEEDS_DEVELOPER`
    itself too. No hook reports an exit (`SessionEnd` isn't registered); the exit arrives as `agent.Registry.onExit`
    → `Manager.SetAgentStatus(EXITED)`, so the escalation lives in `SetAgentStatus`, so a fresh session that follows at once still ends the hold. Guard 3a refuses the remaining custom
    cleanup steps, so nothing is typed into the new session even if the orchestrator missed the exit (FR-216).
  - Orchestrator writes to an existing run use `UpdateWorker{expect_run_id}`, so a late hold, halt or review write
    never re-creates a run the daemon has released.
  - **A hold is never lowered by an update.** The daemon's `Update` refuses to change `NEEDS_DEVELOPER` to anything
    but a clear (and `workerRunFor` overlays the higher of the two holds). An orchestrator that hasn't yet seen the
    daemon's escalation can therefore never downgrade it to a hidden `CLEANING_UP` that lets custom cleanup steps
    through guard 3a.
  - **Retrying Clear under the hold.** After `X` on Clear, the step stays on the row with `R` (FR-216, FR-222: "a
    retried Clear also produces" the fresh session). `ResolveRunStep` RETRY and guard 3a accept it while `hold ==
    NEEDS_DEVELOPER`; it waits for a live, idle agent and never starts one (guard 5).
  - The orchestrator sees the cleared record in `sandbox_changed`, or on reconnect. It finishes the run's cleanup
    (DONE, `hold = NONE`) and drops a retried Clear still waiting (FR-216, run-538). Its stepper never types into a
    sandbox whose `controller.run` is no longer this run (guard 3).
  - Because the daemon owns the hold, a session start while the orchestrator is down still ends it. The spec's
    clarification was amended to say so (planning amendment, run-542).
- **Reasoning sandbox.** `RebuildReasoningSandbox` is allowed while its sandbox is held (FR-222). The rebuild
  launches a fresh agent (`startup`), which by the FR-216 rule ends the hold: the old sandbox's removal ends the
  run's cleanup (`onSandboxRemoved`), and the new sandbox starts free.

**Rationale.**
- Holds live where "free" is already decided (the controller) and where the TUI already reads worker state (the
  sandbox record). The Workers view gets `WorkerView.hold`/`halted` (fields 12–13), since `availability()` alone
  reports a held worker as BUSY.
- Marking a held worker free from the TUI is allowed when `hold == CLEANING_UP` and the agent is NEEDS_INPUT (the
  FR-216 freeing case); today's `markFreeKey` refuses any sandbox with a run that is not awaiting review. The run is
  already closed, so this goes **straight to freeing**: no outcome/note dialog, and the run's outcome is never
  changed.
- `t`/`T` on a run row also open the terminal of a run that **holds** its sandbox (`holdingRunOn`), not only one in
  flight. The terminal is the way out of a hold, and the row tells the developer to use it.
- Nothing new is needed for cross-machine display, because the hold travels with the sandbox.

## R8 — Restart resolution (FR-217)

**Decision.** `reconcileInFlight(hostID)`, which already runs when a host's link comes up and on orchestrator start,
resolves each run whose step is **sent but not finished**. A step still SENDING is first resolved from the daemon's
`WorkerRun.step_index`/`step_sent_at` (typed → SENT with that time; not typed → PENDING). Then it uses the sandbox's
recorded evidence (R4) against `step_sent_at`. Every time compared here is the **daemon's** clock (`step_sent_at`
comes from `DeliverWorkerStepResponse.sent_at` or the daemon record), so there is no cross-machine skew:

This applies when the sandbox's **owning daemon kept running** (an orchestrator restart with a remote worker, or a
lost link). When the owning daemon itself restarted, the agent may still be running, but the daemon has lost its PTY.
R14 then decides what can be typed. Evidence from hooks keeps arriving from the orphan, so rules 1–2 still apply to
it; rule 3 (screen read-back) does not.

1. **A step already `TURN`** was classified before the restart, so the turn-start test is skipped: it is finished
   when the agent is IDLE with `last_turn_end_at > step_sent_at` (or, for MODEL_TURN_INPUT_ONLY, by the input-only
   rule, FR-206b), and otherwise awaited as usual.
2. **A step `SENT`** (typed, not yet classified) is a model turn if `last_submit_at > step_sent_at`, **or** the
   main session reported needs-input after the send, `last_needs_input_at > step_sent_at` (FR-217). That field is
   session-filtered (R13); status and `last_event_at` are not, so a nested `claude -p` can't satisfy the rule. It is
   then finished if `last_turn_end_at > step_sent_at` and the status is IDLE, and otherwise awaited.
3. Otherwise, a built-in whose read-back is on screen is finished. The orchestrator asks the daemon to run the
   read-back without typing (`DeliverWorkerStep` with `check_only`). For Clear, `last_session_source == "clear"`
   after `step_sent_at` finishes it.
4. Otherwise the step **halts as not accepted, marked `sent_before_restart`** (FR-217, FR-222). An empty, ready box
   alone never finishes it. The halt is also written to the daemon (`controller.run.halted = true` through
   `workerRunFor` with `expect_run_id`), so `R`'s `retry` passes guard 3a and the worker is surfaced. On every
   reconnect the orchestrator repairs a HALTED run whose `controller.run.halted` disagrees, as `repairReviewFlag`
   does for the review flag, so a halt push lost on a down link doesn't strand a retry.

**Rationale.** It applies the spec's rule literally, and the recorded evidence (R4) shrinks the developer-decides
case to the minimum: a turn that ended with no recorded evidence. A restart of the owning daemon leaves an orphan
agent that hooks still report from, but that can't be typed into or read (R14).

## R9 — Action storage and the upgrade default (FR-200–FR-204, FR-210)

**Decision.**
- **`Action.sequence`** is a new message field: `ActionSequence { model_enabled, model, effort_enabled, effort,
  clear_enabled, pre_prompts, post_prompts }`.
  - A **missing** `sequence` (every action saved before this feature) reads as the defaults: Opus, medium, Clear,
    empty lists (FR-210).
  - `orchestrator.effectiveSequence(a)` is the only reader.
  - The TUI always sends `sequence` on save, so a saved action is explicit from then on.
- **Validation** extends `validateAction` (FR-204):
  - trim each custom prompt;
  - refuse an empty one, more than `maxStepPrompts = 10` in a list, or one over `maxArgumentBytes`;
  - refuse an UNSPECIFIED or unknown enum value;
  - refuse undeclared placeholders, using the existing `placeholders()`;
  - refuse a placeholder in the first whitespace-delimited word, and any placeholder in a prompt that starts with
    `!`.

  Each refusal names the entry (`pre-prompt 2: …`).
- **Call time.** `compose` is unchanged for the body (FR-207: unreferenced arguments are appended only to the action
  prompt). A new `composeStep(text, a, args)` applies the same placeholder fill with no appending. `callAction`
  snapshots `Run.steps`.
- **Hard-coded step text:**
  - Model is `/model opus|fable|sonnet|haiku`, expecting `Opus|Fable|Sonnet|Haiku`;
  - Effort is `/effort <level>`;
  - Clear is `/clear`.

**Alternatives considered.**
- **A `bool sequence_set` beside flat fields.** Message presence already says the same.
- **Migrating stored actions on daemon start.** It's needless, and makes downgrades lossy.

## R10 — Notifications (FR-223)

**Decision.**
- **Task complete.** The daemon suppresses `TASK_COMPLETE`, both from `Stop` and from the WAITING backstop, while
  `controller.run.step_role` is `SETUP` or `CLEANUP`. `step_role` is set by `DeliverWorkerStep` on the **owning**
  daemon (R3), which is where those notifications are raised. That covers the cross-machine case without the
  orchestrator filtering anything.
- **Needs input.** Needs-input (`NEEDS_PROMPTING`) and the orchestrator's `RUN_NEEDS_INPUT` are unchanged.
- **Not accepted.** A not-accepted halt raises a new `NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED = 9` from the
  orchestrator, naming the run, the step and the reason.

## R11 — TUI (FR-213, FR-218–FR-220, FR-222)

**Decision.**
- **Action form** (`openActionForm`) gains:
  - a `Model` Select (`off · opus · fable · sonnet · haiku`) and an `Effort` Select (`off · low · medium · high ·
    xhigh · max · auto`). Each folds *enabled + value* into one control, and the last value is kept when switched
    off;
  - a `Clear after run` Confirm;
  - `Pre-prompts` and `Post-prompts` Text fields, **one prompt per line**. Adding, editing, reordering and removing
    are line edits, and SWI-9's ctrl+e enlarges the field.

  A prompt cannot contain a newline. That is documented in the field description, and the spec sets no requirement
  for multi-line entries.
- **`actionLine`** appends the sequence summary (FR-219), e.g. `pre: /model opus · /effort medium · 1 custom — post:
  1 custom · /clear`.
- **Prompt overlay** (`promptView`): the steps in order, with the current one marked (FR-220).
- **Run rows** (`jobsRunLine`) show `setting up 2/3`, `cleaning up 1/2`, `not accepted: <reason>`, and `sent before a
  restart` (FR-213, SC-006).
- **Retry / skip / fail.** On a halted run's row: `R` retry, `S` skip (not offered for Clear), and `X` fail, behind
  the existing confirm dialog. They call the new `ResolveRunStep` RPC (FR-222). For a step sent before a restart,
  `S` is listed first and `R` asks for confirmation with the warning.
- **Jobs Overview.** `jobsInProgress` includes closed runs that hold a sandbox or are halted (FR-213).

## R12 — Dependency on SWI-15 (PR #17)

**Decision.**
- **PR #17 (SWI-15) has merged.** The implement phase starts from a `main` that includes it, and uses its
  `SessionStart` hook registration and `Source` plumbing (`hooks.go` `sessionStart`) to record R4's session-start
  evidence. The step guard also uses SWI-15's `Ready`/`MarkReady`, AGENT_STATUS_STARTING and `startAgent`.
- **SWI-9 (app forms) is a second prerequisite.** The form tasks use its `UI/formguard.go` snapshot and its
  enlargeable `spec.text` fields (ctrl+e), which exist only on the unmerged SWI-9 implement branch. Implementation
  rebases on `main` after both merge.

## R13 — Security: substituted values, hook events and the step RPC

**Context.** Steps are typed into an agent that can run slash commands and `!` shell lines. Three things decide what
gets typed or what counts as evidence, and each needs a boundary.

**Decision.**

1. **Untrusted substituted values can't carry control sequences.** Steps are sent as a bracketed paste (`ESC[200~ …
   ESC[201~`). A call **value** containing `ESC[201~` would end the paste early, and a following `\r` would submit
   what came next as a slash command or a `!` shell line. That defeats FR-204, under which the action's **author**,
   not a call value, decides the command. Call values come from worker agents that read untrusted ticket and PR
   text, so this is reachable through prompt injection.

   The rule applies **only to the untrusted substituted values**: call argument values and their defaults. It does
   not apply to the developer-authored action text, nor to other content typed into agents, such as escape-hatch
   results (which legitimately carry ANSI output and are delivered through `Registry.Prompt`, unchanged).
   - **Call time (`validateCall`).** Each value is first normalised: CRLF and lone CR become LF, since ticket and PR
     text routinely has CRLF. It is then refused if it is not valid UTF-8, or if it contains ESC (0x1B), another C0
     control other than `\n` and `\t`, DEL (0x7F), or a C1 control (U+0080–U+009F, which some terminals read as
     CSI). The error is `argument <name> contains a control character`. Invalid UTF-8 is refused because a raw 0x9B
     byte would otherwise reach the PTY as an 8-bit CSI. A refused call is visible to its caller, and nothing is
     stripped silently.
   - **Compose time (`compose`, `composeStep`).** The same check runs again on each value just before it is
     substituted. This is defence in depth for runs queued by an older daemon. A failure fails the run before
     anything is typed ("argument <name> contains a control character").
   - The daemon side (`TypeStep`, `Registry.Prompt`) doesn't re-check text. It can't tell substituted values from
     authored text or escape-hatch output, and refusing there broke escape-hatch results.
2. **Hook callbacks are bound to their sandbox.** `POST /hook` is reached from inside sandboxes over
   `host.docker.internal`, and today it trusts the `sandbox_id` query parameter.
   - **A stable per-sandbox token.** The daemon generates a random 32-byte token for each sandbox **once** and
     persists it in its own store (never on `pb.Sandbox`, so `ListSandboxes` doesn't expose it).
     - **Not rotated with the settings.** Settings are rewritten at every daemon start (`RefreshHooks`), and an
       agent keeps the hook command it was launched with, possibly across daemon restarts (R14). Rotating on rewrite
       would break every surviving agent's hooks.
     - **Rotated only when the daemon spawns a new agent PTY.** The previous token is still accepted until the agent
       launched with it is known to be gone (exited, or killed as an orphan, R14).
   - **Where the token lives.** Not in the hook command in the host-mounted `settings.local.json`, which is written
     0644 into the workspace and may be readable through another sandbox's mount. The daemon writes it with `sbx
     exec` to a file **inside the sandbox**, outside the workspace mount (`~/.sxb/hook-token`, mode 0600). The hook
     command reads it (`-H "X-Sxb-Token: $(cat ~/.sxb/hook-token)"`).
   - **Checking it.** The hook server compares the token in constant time. A call with a **wrong** token is dropped
     (403). A call with **no** token (an agent launched before this feature, still on the old hook command) is
     accepted for **status display only**: it never counts as evidence, ends a hold, or classifies a step.
   - **The escape hatch** (`/escape-hatch/run`, which types its result into the named sandbox's PTY) requires the
     same token.
   - **Self-forgery is corroborated, not trusted.** A process inside the sandbox can read its own token, so the
     decisions that would let context leak into the next run (SC-001) also need daemon-observed evidence:
     - **Spawn generation.** The daemon numbers each agent PTY it spawns for a sandbox (`agent.Registry` generation).
       When it writes a `NEEDS_DEVELOPER` hold (controller `Update`, `SetAgentStatus`, `leaveRunning`, R14), it
       records the current generation on the run as `hold_gen` (daemon-written only).
     - **Ending a hold on `startup`**: the event's generation is greater than `hold_gen`. Only an agent spawned by
       `AttachAgent` (opening the terminal) or by guard 5 counts. While a hold is set, `Registry.Prompt` and
       `EnsureDrained` refuse to spawn (`sandbox is held; open its terminal`), so an escape-hatch result arriving
       after an agent exit can't spawn the agent that ends the hold.
     - **Ending a hold on `clear`, and Clear's read-back**: the hook's `session_id` differs from the recorded main
       session, **and** the daemon's screen shows the post-clear state (an empty, ready box with no conversation
       above it, fixture `clear-banner`).
   - **Only the main agent counts.** A nested `claude -p` in the workspace fires the same hooks with its own
     `session_id`.
     - Every hook now forwards its input (`--data-binary @-`); today `UserPromptSubmit` and `PreToolUse` post an
       empty body and carry no `session_id`. The server reads the body as a JSON token stream and keeps only the
       fields it needs (`session_id`, `hook_event_name`, `source`, `notification_type`, …). Large values such as
       PreToolUse `tool_input` are skipped without being buffered, so the 1 MiB decode cap no longer drops events
       (a 16 MiB hard read cap remains).
     - The **main session** is bound to the first non-`compact` SessionStart, of any source, of each daemon-spawned
       PTY generation, and is rebound on a corroborated `clear`. Later startups within the same generation (a nested
       `claude -p`) are ignored.
     - An agent with no recorded main session (one that survived an upgrade, or `/resume`d before one was bound) has
       its events accepted until a SessionStart binds one.
     - The submit check, the evidence fields (incl. `last_needs_input_at`) and hold release use main-session events
       only. Status display keeps today's behaviour.
3. **The step RPC can't type arbitrary commands.** `DeliverWorkerStep` is served on the same authenticated surface as
   `DeliverWorkerPrompt`: the local daemon socket, and the orchestrator's SSH-dialled link. On top of that:
   - **Built-ins are derived and checked.** For MODEL, EFFORT and CLEAR the daemon types the text it derives from
     `kind` + `expect`, and refuses (`INVALID_ARGUMENT`, before the guard commits) an `expect` outside the fixed
     sets: model `opus|fable|sonnet|haiku`, effort `low|medium|high|xhigh|max|auto`. `step.text` is ignored, so a
     caller can't type `/anything`. The orchestrator maps INVALID_ARGUMENT to a HALTED step with the error as its
     note, never to a retry.
   - **Indices only grow.** `step_index` must be **greater** than the recorded one (skips leave gaps), or equal with
     `retry` on a halted run. A replayed or out-of-order request types nothing.
   - The run must be the one recorded on the sandbox for this orchestrator (guards 2–3), so a client that knows ids
     still can't type into a sandbox busy with another orchestrator's run, or with no run.

**Alternatives considered.**
- **Refuse control characters in all typed text.** Rejected: escape-hatch results and other legitimate content
  carry ANSI output. Only the substituted values are untrusted.
- **Strip control characters silently.** Rejected for values: it would change what the caller sent without telling
  them.
- **Rotate the token on every settings rewrite.** Rejected: surviving agents keep their launch-time hook command.

## R14 — Recovery when the owning daemon restarts

**Context, verified against the code.**
- **The agent survives.** The agent is launched with `sbx exec -it … exec claude` and deliberately **not** wrapped
  (`agent/pty.go` `agentCommand`). The `docker exec` child survives a hard kill of the host client and a
  controlling-PTY hangup, so an in-flight prompt keeps running across a daemon restart (FR-002). `WarmStart`
  (`worker/deliver.go`) skips a worker with a run for exactly this reason: its agent "may still be working".
- **But the daemon loses its handle on it.** The daemon's PTY master is gone, so a restarted daemon can neither
  type into the agent nor read its screen. `agents.Live(id)` is false.
- **The agent's outbound channels keep working.** Hooks, with the stable token from R13, and its MCP completion
  report.
- **The daemon must not spawn a second agent next to it.** `t` or `Session()` would otherwise start another `claude`
  in the same workspace.

**Decision.** After a restart, such an agent is an **orphan**: alive but neither typeable nor readable. The daemon
detects it: `!agents.Live(id)` while the recorded agent status is neither EXITED nor none. The recovery depends on the
run's phase:
- **ACTION** (the action step TURN or AWAIT_CLOSE): **left in flight**, exactly as for runs without steps today.
  - Hooks keep the status current, and the agent's completion report closes the run normally.
  - At close, cleanup can't be typed into an orphan, so the hold is `NEEDS_DEVELOPER` (R5's "close on a sandbox
    whose agent can't be typed into" rule).
- **SETUP**, or an action step not yet accepted: the current step **halts** with `step_sent_before_restart` (when it
  had been sent) and the note `agent detached by a daemon restart; R or S starts a fresh agent`.
  - A turn already running on the orphan may still finish; that changes nothing, since the next step can't be typed
    into it anyway.
  - `R` and `S` first **replace the orphan** (kill it, then start a fresh agent through guard 5's start path), then
    retry or skip as usual. The halt's confirm warns that the setup context is lost.
  - `X` fails the run (FR-212). A run whose action step is in SENDING/SENT resolves first from the daemon record
    (R5), and becomes ACTION or SETUP accordingly.
- **CLEANUP, or any hold:** `hold = NEEDS_DEVELOPER`, set by the daemon itself. Opening the terminal (`t`) kills the
  orphan, then starts a fresh agent, whose `startup` ends the hold (FR-216).

**Killing an orphan.** The launch command records the agent's in-sandbox PID before `exec` (`echo $$ >
~/.sxb/agent.pid; exec claude …`; `exec` keeps the PID). To kill the orphan, the daemon sends `sbx exec <ref> sh -c
'kill -TERM "$(cat ~/.sxb/agent.pid)"'` and waits up to 5 s, then sends KILL. This applies only:
- before the spawns above (a step's `R`/`S` after a restart, and `t` on a held sandbox);
- never to a sandbox whose orphan is working an ACTION step.

After the kill, the previous hook token is retired (R13).

**What is not claimed.** Nothing is read back from a screen that no longer exists: `check_only` answers `no live
agent`. R8's evidence-based resolution still applies to steps whose evidence arrives by hook, which keeps working
from an orphan.
