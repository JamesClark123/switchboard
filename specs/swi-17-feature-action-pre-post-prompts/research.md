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
   it drains the channel and **resets** the screen (dropped chunks may have cut an escape sequence in half). It does
   **not** nudge on its own: a resize nudge repaints every attached client's terminal, so heavy output would make a
   developer's terminal flicker. Recovery is **lazy**: when the guard or the submit check reads a stale screen, it
   requests a resize nudge (debounced, at most one per second per sandbox), the agent repaints, and the screen is
   fresh again. While stale, the guard refuses (`screen resyncing`; the stepper retries) and the submit check treats
   the screen as **unknown**: it extends its window by at most 1 s (contracts/worker-step.md §6).
   - **Still stale when the extension ends:** the check returns **UNCONFIRMED** (`screen resyncing`): the step was
     typed, but nothing confirms it was accepted. The orchestrator treats it like a lost response: SENT, resolved by
     R8's evidence rules (a turn-start or turn-end hook finishes it; a built-in is read back with `check_only` once
     the screen is fresh, and halts as not accepted only if that read-back fails). It never halts on a stale read
     alone. `check_only` requests the lazy nudge too, so its read-back gets a fresh screen.
   - **A step that halts after UNCONFIRMED may have run** (a custom local command has no turn hook, and `check_only`
     never finishes a CUSTOM step). It is marked `step_unconfirmed` (Run field 30), shown as `… not accepted (may
     have run)`, with `S` listed first and `R` asking the same "may already have run" confirm as a step sent before a
     restart (contracts/jobs-ui.md).
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
    and no cleanup pending, depends on whether that step was **typed**:
    - **Not typed** (FailedPrecondition or INVALID_ARGUMENT): the stepper clears the daemon record with
      `UpdateWorker{set_run, expect_run_id = run}` (unset run).
    - **Typed** (any other outcome, or `ALREADY_EXISTS`, or a SENDING step later resolved as typed from the daemon
      record): the step reached the agent, so under amended FR-214 the run gets **Clear-only cleanup** when Clear is
      enabled: `hold = CLEANING_UP`, Clear sent once the agent is idle, then the record is cleared as for any
      finished cleanup. With Clear disabled the record is cleared at once.
    Any other late response goes through the normal path: for example, the action step's response
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
  post-prompt** is sent. Clear (if enabled) **is** sent, once the agent is idle, if any step reached the agent:
  a pre-step, or the action prompt itself (typed but not accepted, then failed with `X`).
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
   - **A per-agent token, passed in the agent's environment** (revised in run-566). Each time the daemon spawns an
     agent PTY for a sandbox, it numbers it (the **generation**: a per-sandbox counter persisted in the daemon's store,
     so it only grows, across daemon restarts too) and draws a random 32-byte token for it. Both go into the launch
     environment of the agent (`SXB_HOOK_TOKEN=<token>`, `SXB_AGENT_GEN=<n>`; see **Exposure** for how). The agent has them before its first
     hook fires, and every process it starts (hooks, the escape-hatch wrapper, a nested `claude -p`) inherits them.
     There is no file to write, so nothing races the spawn.
     - **Persisted, never on `pb.Sandbox`.** The daemon keeps `{generation → token}` per sandbox in its own store, so
       `ListSandboxes` doesn't expose it. A generation's token stays accepted, across daemon restarts too, until that
       agent is **known** to be gone (revised in run-590): a probe found it gone, its sandbox was stopped or destroyed
       (R14's outcome rule), or the developer stopped it as an orphan (R14). A PTY exit alone
       doesn't retire it: a killed exec client leaves the agent running, and that orphan keeps working hooks, as does
       an agent that survives a daemon restart.
     - **The settings carry no secret.** `settings.local.json` is rewritten at every daemon start (`RefreshHooks`),
       written 0644 into the workspace mount. It only references the variable (`-H "X-Sxb-Token:
       $SXB_HOOK_TOKEN"`), so rewriting it never changes any agent's token.
     - **Exposure** (tightened in run-573).
       - **Not in argv where avoidable.** The daemon sets both variables in the **environment of the `sbx exec`
         client** and passes them by name (`sbx exec -e SXB_HOOK_TOKEN -e SXB_AGENT_GEN …`, as `docker exec -e NAME`
         reads the value from the client's environment), so the value never appears in the host process list.
         `sbx exec`'s env flag is documentation-derived like the rest of its argv, so T045 pins it with an
         argv-asserting test and reconciles it first. Only if the runtime has no such flag does it fall back to an
         `export` in the launch string.
       - **Only in that child's environment.** The values are set per command (`cmd.Env = append(os.Environ(),
         "SXB_HOOK_TOKEN=…", "SXB_AGENT_GEN=…")`), **never** with `os.Setenv`. The daemon's own environment is
         inherited by escape-hatch commands run on the host (005), ON_HOST services (006) and every other `sbx`
         call, and a process-wide value would also race between concurrent spawns.
       - **Never logged.** No daemon log line includes the launch string or the agent's environment, and an exec error
         is logged with the sandbox and the error only.
       - **The leak path that remains.** Every process the agent starts inherits the variable, so a tool that prints
         its environment (`env`, a crash report, a CI-style log dump) can put the token into the transcript or a
         committed file. A leaked token lets another sandbox on the same host forge evidence or end a hold for this
         sandbox (the hook server keys on the `sandbox_id` query parameter). This is accepted: the token is
         per-generation (a new agent gets a new one, and it is retired only once its agent is known gone), and hold release and Clear still need the
         daemon's own screen as corroboration. Binding calls to their source address isn't possible here: every
         sandbox reaches the host through the same `host.docker.internal` gateway.
   - **Checking it.** The hook command also sends `X-Sxb-Gen: $SXB_AGENT_GEN`. The hook server looks the generation
     up in an **in-memory** map `{sandbox → {generation → token}}`, loaded from the store at start and updated on
     spawn and reap (no store read per hook; PreToolUse fires many times a turn), and compares that one token in
     constant time. A match gives the event's **generation**. A call with a token the sandbox doesn't have is
     dropped (403). A call with **no** token (an agent launched before this feature) is accepted for **status
     display only**: it never counts as evidence, ends a hold or classifies a step. After an upgrade such an agent is
     an orphan anyway (its daemon restarted), so it is replaced before any step is typed into a sandbox (R14).
   - **The escape hatch** (`/escape-hatch/run`, which types its result into the named sandbox's PTY) requires a live
     generation's token.
     - The generated wrapper (`escapehatch/inject.go` `writeWrapper`, whose `curl` sends no token today) sends the
       same header from `$SXB_HOOK_TOKEN`, which it inherits from the agent.
     - Wrappers are re-injected at daemon start alongside `RefreshHooks` (today they are written only at bring-up),
       so no workspace keeps a tokenless wrapper.
     - A tokenless call gets 403 with `no agent token in this environment; run the escape hatch from the agent's
       session`.
     - **What this restricts** (documented in the escape-hatch docs and quickstart): only the agent's own process
       tree can run escape-hatch commands. A developer's own shell (`sbx exec -it … bash`), an IDE terminal, a
       service started by 006's `Runner.Exec`, or a script left by an earlier agent generation gets the 403. That is
       the intent of 005: the escape hatch exists for the sandbox's agent. A developer on the host can already run
       the command directly.
   - **Self-forgery is corroborated, not trusted.** A process inside the sandbox can read its own token, so the
     decisions that would let context leak into the next run (SC-001) also need daemon-observed evidence:
     - **Spawn generation.** `hold_gen` is the generation current when a `NEEDS_DEVELOPER` hold **begins**.
       - It is written by the daemon only on the **transition into** that hold (controller `Update`, `SetAgentStatus`,
         `leaveRunning`, R14), never re-recorded by a later write of the same hold (a `step_note` mirror, a re-sent
         hold).
       - It is never taken from a request: the controller `Update` always keeps the stored value, so an
         orchestrator write built from a stale view can't zero or lower it.
       - The generation counter is persisted (above), so a generation after a restart is always greater than any
         recorded `hold_gen`.
     - **Ending a hold on `startup`**: the event's generation (from its token) is greater than `hold_gen`. Only an
       agent spawned by `AttachAgent` (opening the terminal, including `t` → Replace on an orphan) or by guard 5
       counts. While a hold is set, `Registry.Prompt` and
       `EnsureDrained` refuse to spawn (`sandbox is held; open its terminal`), so an escape-hatch result arriving
       after an agent exit can't spawn the agent that ends the hold.
     - **Ending a hold on `clear`, and Clear's read-back**: the hook carries the current generation's token and the
       main PID, its `session_id` differs from the recorded main session, **and** the daemon's screen shows the post-clear state
       (an empty, ready box with no conversation above it, fixture `clear-banner`). The hook can arrive before the
       redraw, so the screen is **polled** for up to `clearSettle` (1 s) after the hook (with a lazy nudge if it is
       stale, R2), not read once at the instant of the hook.
   - **Only the main agent counts.** A nested `claude -p` in the workspace fires the same hooks with its own
     `session_id`.
     - Every hook now forwards its input (`--data-binary @-`); today `UserPromptSubmit` and `PreToolUse` post an
       empty body and carry no `session_id`. The server reads the body as a JSON token stream and keeps only the
       fields it needs (`session_id`, `hook_event_name`, `source`, `notification_type`, …). Large values such as
       PreToolUse `tool_input` are skipped without being buffered, so the 1 MiB decode cap no longer drops events
       (a 16 MiB hard read cap remains).
     - **The main agent is matched by its process, not its session id** (revised in run-579). The hook command also
       sends `X-Sxb-Pid: $PPID`: hooks run as `sh -c` children of the `claude` process that fired them, so that is
       the firing agent's PID. A hook event is **main** when it carries the current generation's token **and** its
       PID equals the generation's **main PID**.
       - The main PID is bound by the first non-`compact` SessionStart of the generation (any source). A nested
         `claude -p` can only start after the agent's own SessionStart, and it has its own PID. The main PID also
         equals the `agent.pid` recorded at launch (`exec` keeps the PID).
       - **The main PID survives a daemon restart** (revised in run-586). It is persisted with the generation's token
         (`{generation → token, main PID, start time}`, written when it is bound), so an orphan's hooks count as main
         as soon as the restarted daemon loads the store. A probe (R14) binds it from `agent.pid` only when **none** is
         bound (the daemon died before binding, or the write was lost) and the file's generation and start time match.
         It never replaces a stored binding (revised in run-590): the file can be rewritten from inside the sandbox, so
         a mismatch is logged and the stored PID stays. Even a first bind does not trust the file alone (revised in
         run-595): the same probe script also checks that `/proc/<pid>/exe` is the agent binary, that its command line
         starts with the fixed flags, and (revised in run-601, made runtime-independent in run-607) that it was **started by the
         runtime's exec path, not from inside the sandbox**. The rule doesn't hard-code ppid 0, which holds for `docker
         exec` into a plain container but is unverified on the microVM runtime, where an in-VM exec agent may be the
         parent. Instead, it compares the candidate with a reference taken on the spot. The probe script is itself
         started by `sbx exec` through the same path, so its own parent (`$PPID` of the probe shell) is what the exec
         path's parent looks like on this runtime. The candidate's parent must match it:
         - both are 0 (a plain container);
         - or both are non-zero and have the same `/proc/<ppid>/exe`, which must not be a shell, `script`, `tmux`, or
           the agent binary.

         Anything started from inside the sandbox, by the agent or by `script -qc claude` on a new pty, has a shell,
         `script` or the agent as its parent and fails. If T006 finds an intermediate shell between the exec and the
         agent, the same comparison applies to the agent's grandparent. Quickstart Scenario 0 records what the real
         runtime shows, and T045's test pins it. The stdin/tty value comes from the same writable file and is only a
         consistency check. Otherwise the bind is refused and logged, so a process in the sandbox can't name itself
         main.
       - **Fallback, if T006 finds `$PPID` isn't the `claude` process** (an intermediate shell): the hook sends
         `readlink /proc/$PPID/fd/0` (or of its parent) instead, and the main agent is the process whose stdin is the
         generation's PTY device (recorded at launch with the PID). A nested `claude -p` run from a tool has pipes,
         not that PTY.
       - **The session id follows the main process.** Any `clear` or `resume` SessionStart **from the main PID**
         updates the recorded `session_id`, since `/clear` and `/resume` switch the agent to another session. One
         from any other process (a nested `claude -c -p` or `claude -p --resume`) never does. Updating the session
         id never ends a hold on `resume` (FR-216).
       - `session_id` is still needed for Clear's read-back and hold release ("a new session after /clear"). Event
         matching no longer depends on it, so a missed update can't stall a run.
       - Until the main PID is bound (the agent is still starting, and no step can be typed yet), events count for
         status only.
     - The PID match is a filter against nested and left-over processes, **not** a security boundary: anything in
       the sandbox can send any header. The token and the screen corroboration above are the boundary.
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
- **A token file inside the sandbox** (`~/.sxb/hook-token`, the run-560 design). Rejected in run-566: it was written
  by a separate `sbx exec` that could land after the agent's first hook, and a missed write made every hook
  tokenless, cutting a new agent off from readiness, holds and evidence.
- **Bind the main session with `--session-id <uuid>`.** Not needed: `/clear` changes the id anyway, and the
  generation's token already tells this agent from a left-over process.
- **Match the main agent by session id, rebinding on `clear`/`resume`** (the run-573 design). Rejected in run-579: a
  nested `claude -c -p` or `claude -p --resume` also fires `resume` with the current token, and would take the main
  session over. Matching by process can't be taken over that way.

## R14 — Recovery when the owning daemon restarts

**Context, verified against the code.**
- **The agent survives.** The agent is launched with `sbx exec -it … exec claude` and deliberately **not** wrapped
  (`agent/pty.go` `agentCommand`). The `docker exec` child survives a hard kill of the host client and a
  controlling-PTY hangup, so an in-flight prompt keeps running across a daemon restart (FR-002). `WarmStart`
  (`worker/deliver.go`) skips a worker with a run for exactly this reason: its agent "may still be working".
- **But the daemon loses its handle on it.** The daemon's PTY master is gone, so a restarted daemon can neither
  type into the agent nor read its screen. `agents.Live(id)` is false.
- **The agent's outbound channels keep working.** Hooks, with their generation's token from R13 (persisted, so it
  survives the restart), and its MCP completion report.
- **The daemon must not spawn a second agent next to it, nor kill one behind the developer's back.** `t` or `Session()` would otherwise start another `claude`
  in the same workspace.

**Decision** (simplified in run-590). After a restart, such an agent is an **orphan**: alive but neither typeable nor
readable. The daemon **detects and surfaces** orphans and refuses to spawn a second agent beside one. It **never
stops one on its own.** An orphan is stopped only by an explicit developer action: `t` → **Replace**, `R` (*Restart
setup*), or a sandbox stop, restart, refresh, destroy or kit attach, each of which ends every process in the
container anyway. Whether a sandbox has an orphan is decided by **positive evidence** (a recorded agent process that
is alive and not owned by the daemon's current session), never by recorded state flags.

*Why this shape.* From run-570 to run-588, every review round found new races in automatic reaping:
- the daemon's own `sbx exec` against a sandbox being stopped;
- the choice of "stopping" mark;
- exit-code heuristics;
- a reap racing a stop.

Without automatic reaping, the daemon's only calls into a sandbox are read-only probes, and one lock (below) orders
them against lifecycle operations. The cost: an orphaned idle worker, or a closed run on an orphan, waits for the
developer instead of being freed automatically. It is surfaced at once (an *agent detached* notification and a
worker-row note), and dispatch skips the worker meanwhile. After a daemon restart, when every idle worker's agent
may be detached at once, the notifications are aggregated into one, and a single bulk Replace frees all the idle
workers (revised in run-595). Workers whose agent did not survive are clear immediately and are never held up.

**Recording.** The launch string records the agent's generation, PID, process start time and PTY device before
`exec`, creating the directory first: `mkdir -p ~/.sxb; echo "<gen> $$ $(cut -d' ' -f22 /proc/$$/stat) $(tty)" >
~/.sxb/agent.pid; … exec claude …`.
- `exec` keeps the PID, the start time and the PTY.
- The comm of `bash` has no spaces, so field 22 is the start time.
- The write is best-effort (`;`), so an agent still starts if it fails.

**Detection: a probe, not a flag.**
- **Only running sandboxes.** The daemon asks the runtime which sandboxes are running (`ListStatuses`). A sandbox
  that is not running has no agent: its recorded agent status is set to EXITED, and it is never probed.
  `sbx exec` would start a stopped sandbox, so nothing here ever runs `sbx exec` against one.
- **The probe** is one `sbx exec <ref> sh -c …` against a running sandbox. It reads `gen pid start tty` from
  `~/.sxb/agent.pid` and reports **alive** only if `/proc/<pid>/stat` exists with the same start time (a reused PID
  never matches).
  - **With no PID file**, or one that doesn't parse (an agent launched before this feature, or a truncated write),
    it lists every process whose executable is the agent binary and whose command line starts with the agent's
    fixed flags (`claude --dangerously-skip-permissions`; the spec's extra args may have changed since launch). It
    excludes any process whose parent is itself such a process (nested ones). It reports **all** matches with their
    start times: on main, `t` after a restart already spawns a second agent, so an upgraded host can have two.
- **Orphan** = the probe found a live agent process **and** the daemon has no live session of that generation. The
  daemon keeps an in-memory **orphan record** `{gen, [pid, start]…, last status, last evidence}` per sandbox. It
  publishes the check's state on the sandbox record as `AgentSession.agent_check` (CLEAR, UNKNOWN, DETACHED or
  CHECK_FAILED, with the last status, last-seen time, error and process count), written only when it changes. The
  Workers view, the row notes and the bulk Replace read it from there.
- **Main PID.** The main PID is persisted with the token (R13). A probe binds it only when none is bound: the file's
  generation is the current one, and the daemon died before binding or the binding write was lost. A probe never
  replaces a stored binding. On a mismatch the stored PID stays and the mismatch is logged, since anything inside the
  sandbox can rewrite the file. A first bind also needs the process itself to look like the main agent: its
  executable is the agent binary, its command line starts with the fixed flags, and its parent matches the parent of the
  probe's own `sbx exec` shell (both 0, or the same non-shell exec-agent executable), so it was started by the
  runtime's exec path and not from inside the sandbox (run-601, made runtime-independent in run-607). The recorded tty is only a
  consistency check, since it comes from the same writable file.

**One lock orders probes and kills against lifecycle operations: `runtimeMu`.**
- Each sandbox has a `runtimeMu`: a per-sandbox mutex in the manager, separate from `m.lifecycle` and the ops latch.
  These operations hold it from just before their runtime call until they have written the resulting state:
  - `Stop` and `Destroy`;
  - `Refresh`, **continuously** from its stop through its re-seed and bring-up (revised in run-595), so nothing
    spawns into the half-seeded workspace;
  - `AddKit`, for the recreate;
  - `Restart`, for the bring-up (its `sbx exec <N> true` starts the sandbox).

  `Rename`, source edits and MCP attach don't touch the container's processes and don't take it.
- **Every probe and every kill** is one `sbx exec`, run under `runtimeMu` after re-reading the record. It execs only
  if the record is RUNNING and `ListStatuses` lists the sandbox as running.
  - Probes and kills **try-lock**.
  - A probe that finds the lock held is dropped, and the operation's outcome rule (below) settles the sandbox
    instead.
  - A developer's Replace or `R` that finds it held is refused with `sandbox is busy (<operation>); try again`.
- **Lifecycle operations block** on `runtimeMu`, so they wait for an in-flight probe or kill, which is bounded by
  `orphanProbeTimeout`, or by `orphanKillWait` plus its exec. No probe or kill can overlap or follow a stop.
- **The agent session is closed by the on-change hook, as today** (`grpc/server.go`), once the non-RUNNING state is
  published. **One exception:** a stop closes it *before* `sbx stop` (below).
- **Check and spawn hold `runtimeMu` together** (revised in run-595). A non-blocking check followed by a separate
  spawn would leave a window: the gate passes, a `Stop` takes the lock and runs `sbx stop`, and then the factory's
  `sbx exec -it … claude` starts the stopped sandbox again.
  - So `Registry.Session` **try-locks** `runtimeMu` before the gate check and holds it until the factory's
    `cmd.Start` has returned and the session is registered. If the lock is held, it refuses with `ErrBusy`, which
    names the operation: `sandbox is stopping`, `… restarting`, `… being refreshed`, `… being recreated for a kit` or
    `… being destroyed`.
  - The try-lock never waits, so it is safe under the terminal registry's global lock. A lifecycle operation holds
    `runtimeMu` and may then take `r.mu` (the on-change hook's `agents.Close`), but `Session` never waits on
    `runtimeMu` while holding `r.mu`, so there is no lock cycle. A lifecycle operation waits at most for one
    `cmd.Start`.
  - **A stop drains the exec clients first** (revised in run-601, replacing a one-shot `ListStatuses` re-check).
    Holding the lock through `cmd.Start` orders only the *fork* of `sbx exec`. The client reaches the runtime later,
    with unbounded lag, and `sbx exec` starts a stopped sandbox in place. So `Stop`, and the stop inside `Destroy` and
    `Refresh`, does the following under `runtimeMu`, **before** `sbx stop`:
    - it closes the sandbox's agent session and its terminal broadcaster with `closing` set (no EXITED, no exit
      probe);
    - it **cancels only the `portforward` exec clients that are still starting** (revised in run-607, narrowed in
      run-618): an IN_SANDBOX launch that hasn't announced its PGID yet (`launch_sandbox.go`), and the readiness
      loops (`probe.go`, which poll `sbx exec` with backoff), including any readiness exec in flight. An
      **established** service, whose PGID marker has arrived, keeps its client. Its exec session is already up, so
      it can't start a stopped sandbox. It ends with the container on `sbx stop`, and the hook's `StopAll(id, true)`
      records it, as today. That matters because `gone(h)` reads that very client (`portforward/stop.go:133`), so
      killing it would leave the supervisor unable to stop or observe the service. A developer's service stop holds
      its try-lock for its whole duration, so no kill is in flight when the drain runs;
    - it **waits for every one of those `sbx exec` client processes to exit**: TERM, then KILL after
      `orphanKillWait`. It waits on the **existing watcher**, never with a second `Wait`. The agent session's
      `sessionEntry` gets a `done` channel closed by `onExit` (the registry's `watch` goroutine already owns
      `cmd.Wait`, and a second `Wait` returns at once with `Wait was already called`). The portforward instances
      expose the same kind of done signal. Killing a host process is always possible. **Where the wait happens**
      (revised in run-613): `Registry.Close` marks the entry `closing`, removes it and **returns** its `done`
      channel; the drain waits on it only **after** `Close` has released `r.mu` and the terminal registry's global
      lock, because `onExit` takes `r.mu` (`agent/pty.go:117`). The wait is bounded: TERM, then KILL after
      `orphanKillWait`, then `orphanKillWait` again. A client still alive after that (for example, stuck in
      uninterruptible sleep on a hung host mount) is logged, and the stop **proceeds** with `sbx stop` (run-618). One
      stuck host process must not block every stop. If the stop then fails, the outcome rule's RUNNING probe covers
      the agent.

    **No exec guard** (simplified in run-613, replacing the per-sandbox exec guard of run-601/607). Only a
    **developer request** can start a new `sbx exec` client while an operation holds the lock, so only the three
    request paths are gated, each with the same try-lock `Registry.Session` already uses:
    - `Registry.Session`'s factory (above);
    - an IN_SANDBOX service launch: `bringUp` try-locks `runtimeMu` around `launchInSandbox`'s `cmd.Start` and the
      instance's handle registration. If the lock is held, the instance is FAILED with `sandbox is busy; try again`
      and no port is kept (FR-046);
    - a developer's service stop (`Supervisor.Stop`, `service_rpcs.go`): the same try-lock, refused with `ErrBusy`.
      The service is being stopped by the operation anyway.

    Every other `sbx exec` is issued by the lock holder or from its on-change hook, and is **never gated**:
    - `Restart`'s `sbx exec <N> true`, and the probe and reap scripts, all run under the lock;
    - the hook's service teardown (`StopAll(id, down)`), whose kills go through `sbx exec` only for CREATING and
      ERROR. In those states the container is up, so the exec can't start a stopped sandbox. That includes `AddKit`
      and a Refresh whose stop failed, both of which publish CREATING under the lock;
    - readiness loops, which `bringUp` starts only after a gated launch, under the instance's context. The drain
      cancels that context, and `exec.CommandContext` with a cancelled context never starts.

    So after the drain, no `sbx exec` client of this sandbox is still **starting** (established service sessions
    are already connected), and none can start before `sbx stop`.

    **Assumption (verified in quickstart Scenario 0, step 5; run-618).** Only `sbx exec` starts a stopped sandbox in
    place. Two other per-sandbox calls run outside `runtimeMu`, and are assumed not to start a stopped sandbox:
    - `sbx mcp load|unload <name> --sandbox <N>` (`AttachMcp`/`DetachMcp`, the `M` overlay);
    - `sbx ports <N> --publish|--unpublish` (`SbxRunner.PublishPort`, called on the service launch path).

    If the check shows either one does start it, that call gets the same try-lock as the other developer
    requests, and the service launch holds it through `PublishPort`. This is listed under *Proposed deferrals* until
    it is verified. No exec
    is counted, and there is no state or token to classify.
    - **If `sbx stop` then fails**, the container is still up:
      - **Agent.** The sandbox stays RUNNING with an agent and no session. The outcome rule's RUNNING branch marks
        it *unknown* and probes, so it is surfaced as detached (`t` → Replace or Wait), never silently doubled.
      - **Services** (revised in run-618). Established services were never drained, so they are still RUNNING and
        tracked by their own clients, with nothing to do. A launch or readiness check that the drain cancelled
        leaves its instance FAILED through the existing `markFailed` path (detail `cancelled by a stop`), which
        releases its port first (FR-046).
- **Outcome rule.** The operation applies it as it releases the lock, judging from the state it wrote, not from the
  fact that it ran:
  - **STOPPED, or destroyed.** STOPPED is written only once the container is down, so its processes are gone. The
    operation drops the orphan record, clears *unknown*, retires the generation's token (R13) and writes EXITED by
    the gone rule. Refresh applies this branch explicitly right after its stop's STOPPED write, still under the lock,
    so its record is dropped before the re-seed and bring-up. At the final release Refresh is RUNNING with no record,
    so the RUNNING branch has nothing to do.
  - **The same STOPPED/destroyed branch also runs from the on-change hook, but only on a confirmed stop or destroy**
    (revised in run-595, narrowed in run-601). That covers a container stopped outside switchboard (Docker Desktop, a
    runtime restart, host sleep) that `Manager.Announce` picks up, so the record, *unknown* and the token never
    outlive the container's processes. "Confirmed" means:
    - a published **STOPPED**, which today is written only after a successful `sbx stop` (Stop, Refresh), by Rename
      (allowed only on a non-running sandbox), or by `Announce`/reconcile from a successful `ListStatuses`. One gap is
      closed: Refresh, after a **failed** stop, now writes STOPPED only when `ListStatuses` **succeeds** and doesn't
      list the sandbox as running. A listing error counts as still running, so it writes CREATING (today
      `listedRunning` returns false on an error);
    - the **DESTROYING** that `Destroy` and the reconcile emit **after** the record is deleted.

    **CREATING and ERROR never clear anything**, because the container (and so the orphan) may still be up: `AddKit`
    publishes CREATING *before* its recreate, Refresh publishes CREATING when its stop failed and the container is
    still listed, and `m.fail` publishes ERROR after a failed operation. They take the "anything else" branch: the
    record, *unknown* and the token are kept. The hook's existing session teardown still runs on them. When it
    closes a live agent session there, the sandbox is also marked *unknown*, since the agent can outlive its exec
    client. The next RUNNING state then probes before anything can spawn.
  - **RUNNING** (a Restart or kit attach that succeeded, or a failed `Stop` that left the record RUNNING). If the
    sandbox has an orphan record or is *unknown*, it is marked *unknown* and probed once.
  - **Anything else** (CREATING or ERROR after a failed stop or recreate). The record and *unknown* are kept, and
    the probe runs when the sandbox next becomes RUNNING (the RUNNING branch of a later operation, or daemon start).

**When the daemon probes.** It probes on the events below. None of them stops anything.
- **Daemon start: clear by default, check only what may have an agent** (revised in run-595).
  - Every sandbox that a **successful** listing reports as stopped or absent gets the STOPPED branch: its agent
    status becomes EXITED and its unretired tokens are retired, since its container's processes are gone. It is
    never probed.
  - **A listing that can't answer retires nothing** (revised in run-607). If `ListStatuses` errors at start, or
    reports a status the daemon doesn't recognise for a sandbox (a transient `starting`), no token is retired and no
    EXITED is written for the sandboxes concerned. This matches `ReconcileRecords`, which keeps the last-known state
    when the runtime is unreadable. Every may-have-agent sandbox among them is marked *unknown*. Its probe's own
    RUNNING pre-check fails and retries with backoff, the listing is retried with it, and the bounded-*unknown* rule
    surfaces it if the runtime stays unreadable. Sandboxes with no unretired token stay clear.
  - A running sandbox is marked **unknown** synchronously only if it **may** have a surviving agent: it has a
    generation token in the store that was never retired (an agent this daemon launched that isn't known to be gone),
    or, on the first start after the upgrade (no store entry), its recorded agent status is set and not EXITED.
  - Every other running sandbox is **clear** at once, with no probe and no refusal. That covers an idle worker whose
    agent had exited, or which never had one. It is dispatchable immediately, and `WarmStart` spawns its agent as
    today.
  - The gRPC and hook servers start at once, so the daemon is never unreachable and surviving agents' hooks keep
    landing.
  - The probes run asynchronously with bounded concurrency (`orphanProbeConcurrency`, 8), each bounded by
    `orphanProbeTimeout` (10 s). They run in priority order: workers with no run first (they are the capacity
    dispatch is waiting for), then workers with a run, then plain sandboxes. A **gone** result clears the sandbox at
    once, and the `sandbox_changed` that follows re-kicks `WarmStart` and dispatch.
  - The gate refuses on *unknown*, so nothing can spawn beside an agent before its probe finishes. A sandbox that
    stays *unknown* is bounded by the rule below.
  - **Orphans found at start are surfaced together.** The daemon emits **one** aggregated *agent detached*
    notification per start: `N agents detached by a daemon restart (M on idle workers) — R on the Workers view
    replaces the idle ones`.
    - **When:** once every first-pass start-up probe has returned once (alive, gone or failed), or
      `orphanUnknownLimit` after the first probe started, whichever comes first. It isn't sent when N is 0.
    - **After that:** any orphan found later, including by a *retry* of a start-up probe or after an exec client
      exits, gets its own individual notification, so none goes unreported.
    - **Shape:** a `NotificationEvent` with **empty `sandbox_id`** and the daemon's `host_id`. The TUI shows it as
      a host-level notification. It is never suppressed by being attached to a sandbox, it is acked by id like any
      other, and it is replayed on resubscribe like any other (run-601). In the TUI's list its item identity is the
      notification id, and `enter` opens the Workers view that `R` acts on, rather than selecting an empty sandbox id
      (run-607).
  - **Bulk Replace (developer action).** `R` on the orchestrator's Workers view (only there: on the sandbox list,
    including its Orchestrator Sandboxes tab, `R` is Rename) opens one confirm. It lists every worker whose orphan
    was last seen **idle** (`agent_check.last_status` IDLE) with no run, all ticked by default. An orphan last seen
    working or waiting for input may be the developer's own turn, so it is left to `t`, whose dialog defaults to
    Wait.
    `y` calls `ReplaceDetachedAgent` (contracts/proto-delta.md) on each sandbox's owning daemon. That runs the same
    `ReapOrphan` under the sandbox's `runtimeMu` try-lock, then `WarmStart`s the worker, and the TUI reports each
    result. The RPC itself refuses an orphan not last seen idle, so a stale list can't kill a turn. A busy sandbox
    is skipped with `busy (<operation>)`. The daemon still never stops an orphan on its own.
- **The exec client exits** while the sandbox is still RUNNING and the registry didn't close it (an `onExit` that
  isn't `closing`).
  - The `docker exec` child survives its host client (R14 context), so a dead client doesn't always mean a dead
    agent. The `sbx exec` exit status isn't taken as evidence either way, because which codes are the agent's own is
    not documented.
  - `onExit` therefore always marks the sandbox *unknown*, so the gate refuses from then on. It releases its locks
    (`r.mu`, `statusMu`) and schedules a probe asynchronously. No lock is held across the exec.
  - Alive → an orphan record, and the run keeps its state. Gone → EXITED by the gone rule.
  - An agent's normal exit thus costs one probe, and EXITED is delayed by that probe (typically under a second).
    Meanwhile `t` shows `checking for a detached agent…` and retries on the `sandbox_changed` that follows.
  - When the client exits because a stop is in progress, the probe finds `runtimeMu` held and is dropped. The stop's
    outcome rule then settles the sandbox.
- **The gate refuses.** Each refusal schedules a re-probe (debounced to one per sandbox per 10 s; the gate itself
  never blocks). Whatever tries to spawn, whether `t`, `WarmStart`, a delivery or an escape-hatch result, therefore
  learns within seconds that the orphan is gone.
- **A main-agent `SessionEnd` hook** from the orphan's generation (the agent exiting normally) triggers a re-probe
  at once.
- **Backstop, while an orphan record or an *unknown* sandbox holds a run** (revised in run-595): a re-probe every
  60 s. A crashed orphan fires no hook, and nothing spawns while a run is in flight, so without it a crashed ACTION
  orphan would hang its run. An agent that exits or crashes mid-run leaves its sandbox *unknown* until a probe
  succeeds, so *unknown* is covered too.
- **Gone** (on any probe): the record is dropped and the generation's token retired. EXITED is written only when
  the recorded agent status is set and not already EXITED. A sandbox whose agent never started, or whose exit was
  already handled, therefore doesn't run the exit handling twice. A run in flight then fails with `agent exited`
  (FR-212), or the worker is freed or held by the existing exit rules.
- **Probe failures: *unknown* is bounded** (revised in run-595). A failed or timed-out probe leaves the sandbox
  *unknown*, retried with backoff (capped at 60 s), and retrying continues. A sandbox that stays *unknown* for
  `orphanUnknownLimit` (60 s), or after 3 failed probes in a row, whichever comes first, is **surfaced** once. The
  60 s clock starts at the sandbox's **first probe attempt**, not when it was marked *unknown*, so time spent queued
  behind the start-up concurrency limit never raises a false alarm (run-601):
  - `NOTIFICATION_KIND_AGENT_CHECK_FAILED` (11) names the sandbox, the error and the run it holds (if any).
  - The row shows `can't check for a detached agent: <error>`.
  - If it holds a run, the run's `controller.run.step_note` (shown on the run row in Jobs) reads `can't confirm the
    agent is still running: <error> — t to Wait or Restart sandbox (fails the run)`. The run is not failed
    automatically, because the agent may still be working, but it is never silently in flight. `x` isn't offered:
    `CancelRun` cancels only queued runs (revised in run-601).
  - **The note is an overlay** (run-601). The daemon keeps the run's previous `step_note` beside the CHECK_FAILED
    state and restores it in the **same record update** that leaves CHECK_FAILED:
    - on clear, it restores the previous note;
    - on DETACHED, it writes the detached note instead;
    - on gone, it restores the previous note in the update that writes EXITED. So a run failed by that EXITED
      takes its reason from the previous note, or `agent exited` when there was none, and never from the
      check-failed text.

    **Other note writes while CHECK_FAILED** (run-607): any other write to `step_note` while the overlay shows (a
    `waiting: …` or not-accepted note from `DeliverWorkerStep`) goes into the **saved** note, the value that will
    be restored, and the overlay stays visible. The restore then brings back the latest note, never a stale one.

    The previous note is held in memory. After a daemon restart, the daemon drops a leftover check-failed note
    (recognised by its fixed prefix) while marking the sandbox *unknown*, and the check starts again.
  - `t` offers **Restart sandbox**, behind a confirm that names the run it fails (if any), and **Wait**.
  - There is no "Replace anyway". A kill needs the same `sbx exec` that is failing, while a stop and restart goes
    through the runtime and ends every process in the container. Its outcome rule then drops the record.

**Surfacing.** When a probe first creates an orphan record, the daemon emits `NOTIFICATION_KIND_AGENT_DETACHED` (10)
naming the sandbox, the agent's last known state and its run. The worker row's note reads `agent detached by a
daemon restart — t to Replace or Wait` (`R` replaces every idle one). The notification is sent once per orphan
record, never per re-probe. Orphans found by the start-up probes are reported in **one** aggregated notification per
daemon start instead (above).

**The gate: one choke point, no automatic decisions.** Every path that starts an agent goes through
`agent.Registry.Session` (verified on main):
- `WarmStart` and `Deliver`, via `startAgent`, which creates the broadcaster;
- `AttachAgent` and the gRPC terminal path, via the broadcaster's factory (`grpc/server.go`);
- `Registry.Prompt`, and `EnsureDrained` (which creates the broadcaster).

`Session` asks an `OrphanGate` (set by the daemon) before the factory runs. The gate never blocks (the terminal
registry calls `Session` under its global lock), and it decides only from the orphan record and `runtimeMu`:
- **No record, and the try-lock on `runtimeMu` succeeds** → spawn as today, holding the lock until `cmd.Start`
  returns and the session is registered.
- **A record, or *unknown*** → refuse with `ErrOrphan`, whose message names the agent's last known state (`agent
  detached by a daemon restart; last seen working 10:42`).
- **An operation holds `runtimeMu`** → refuse with `ErrBusy`, naming the operation (`sandbox is stopping`, `…
  restarting`, `… being refreshed`, `… being recreated for a kit`, `… being destroyed`).

Nothing is stopped from this path. The run's phase, `halted` and hold flags play no part. Best-effort callers
(`WarmStart`, `Deliver`'s start, `Registry.Prompt` for an escape-hatch result, `EnsureDrained`) log the error and are
re-kicked by the `sandbox_changed` that follows. A worker with an orphan therefore refuses delivery with
FAILED_PRECONDITION, and dispatch leaves the run queued and skips that worker for the pass (`dispatch.go`,
unchanged). Queued runs go to other workers.

**Who may stop an orphan: only the developer.**
1. **`t`/`T`**, on any sandbox with an orphan, controlled or not.
   - The TUI shows the **Wait/Replace** dialog instead of the terminal, naming the agent's last state and, for a
     worker, its run: "<sandbox>'s agent was detached by a daemon restart (last seen working 10:42 on <run>). It
     can't be shown or typed into. Replace it? Replacing stops it" (+ "and fails <run>" when it holds an ACTION run).
   - The default is **Wait** when the agent was last seen working or waiting for input, and **Replace** when it was
     last seen idle.
   - Replace re-attaches with `AttachInfo.replace_orphan`, and the TUI shows `replacing the detached agent…` while
     the stop runs.
   - So a developer's interactive turn in a plain sandbox is never killed just because they re-attached.
2. **`R` (Restart setup)** on a step halted by the restart (`DeliverWorkerStep.restart`).
3. **Bulk `R` on the Workers view** (run-595): `ReplaceDetachedAgent` for each idle detached worker with no run that
   the developer leaves ticked in its confirm.
4. **A sandbox stop, restart, refresh, destroy or kit attach**, which ends every process in the container (outcome
   rule above).

The daemon never stops an orphan on its own, from any trigger. Each developer stop is logged. For a worker, its
effect is written into `controller.run.step_note`, and a failed run raises the usual `RUN_FAILED`.

**Recovery by the run's phase.** This is what the developer sees, and none of it stops anything on its own.
- **ACTION** (the action step TURN or AWAIT_CLOSE): **left in flight**, as for runs without steps today.
  - Hooks keep the status current, and the agent's completion report closes the run normally.
  - At close, cleanup can't be typed into an orphan. The cleanup steps are dropped, and the hold is
    `NEEDS_DEVELOPER` with the note `agent detached by a daemon restart; t → Replace frees the worker`, plus
    `custom post-prompts were dropped` when there were any.
  - Replace's fresh agent fires `startup`, which ends the hold (FR-216). Clear isn't needed, since the session is
    fresh.
  - **If the orphan asks for input**, its `Notification` hook sets NEEDS_INPUT, so the worker is surfaced, with the
    note `detached agent asks for input; it can't be answered here — replace it (fails the run) or wait`.
  - **Replace** (case 1) fails the run with the reason `agent replaced by the developer after a daemon restart`. The
    daemon writes that reason into `controller.run.step_note` in the same update as EXITED, and the orchestrator uses
    it as the fail reason. A replaced run sends **no** custom post-prompts and no Clear: its fresh session is already
    clean.
- **SETUP**, or an action step not yet accepted: the current step **halts** with `step_sent_before_restart` (when it
  had been sent) and the note `agent detached by a daemon restart; last seen <state> <time>; R restarts setup with a
  fresh agent`.
  - A turn already running on the orphan may still finish. That changes nothing.
  - The setup already done (model, effort, custom pre-steps) lives in the orphan's session and dies with it. So `R`
    here is a **restart**, not a retry. Through `DeliverWorkerStep.restart`, it stops the orphan, starts a fresh
    agent, and **restarts the sequence from step 0**: every pre-step is sent again as a new send. `step_index` is a
    send number (contracts/proto-delta.md), so the re-sends pass guard 3a, and the action prompt never reaches an
    agent on the default model or effort (SC-001).
  - `S` is not offered for such a halt.
  - `X` fails the run (FR-212). The orphan is not stopped, since it may still be mid-turn and only the developer
    stops one. Clear can't be typed into it, so the sandbox is held `NEEDS_DEVELOPER`, and `t` → Replace ends the
    hold and frees the worker.
  - A run whose action step is in SENDING/SENT resolves first from the daemon record (R5), and becomes ACTION or
    SETUP accordingly.
- **CLEANUP, or any hold:** `hold = NEEDS_DEVELOPER`, set by the daemon itself, with `hold_gen` = the orphan's
  generation. `t` offers Replace, and the fresh agent's `startup` ends the hold (FR-216).
- **A worker with no run** whose orphan was last seen idle stays orphaned, and dispatch skips it until the developer
  replaces it, one by one with `t` or all at once with the bulk `R`. The dialog defaults to Replace, since the agent is idle.
- **An uncontrolled sandbox**: nothing happens until the developer presses `t`, which shows the dialog.

**Stopping an orphan** (developer actions only). This is one `sbx exec <ref> sh -c …` script, run under `runtimeMu`
(try-lock) on a RUNNING sandbox:
1. It re-probes, as above, in the same script. The start-time and binary checks therefore guard every signal. If
   every matched process is gone, it reports **gone** and signals nothing.
2. It sends TERM to **each** live matched process, waits up to `orphanKillWait` (5 s) for them to go, then sends KILL.
   The dialog names the count when there is more than one.

If the exec fails, nothing is stopped or spawned, and the dialog shows the error. After a stop (gone or killed), the
daemon:
- retires the generation's token (R13);
- drops the orphan record;
- writes the agent status EXITED, with the reason for a replaced run.

The exception is a `restart`: there the fresh agent's STARTING follows directly, so the orchestrator doesn't read the
stop as an exit during setup (FR-212).

**What is not claimed.** Nothing is read back from a screen that no longer exists: `check_only` answers `no live
agent`. R8's evidence-based resolution still applies to steps whose evidence arrives by hook, which keeps working
from an orphan.
