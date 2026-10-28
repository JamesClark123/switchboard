# Contract: `DeliverWorkerStep` and the submit check (SWI-17)

The RPC is served by the sandbox's **owning** daemon (`internal/worker/step.go`) and called by the orchestrator
through `WorkerAPI`, for local and remote hosts alike. It implements FR-206 and FR-206a–d for one step. Background is in research R2–R4.

## 1. Guard (one `reg.Update`, as `Deliver` today)

**Before the guard**, the daemon reads the screen **without creating anything**: `terms.Screen(id)` returns the live
broadcaster's screen, or nothing. Creating a broadcaster runs the agent factory (`agents.Session`), which **spawns
`claude`** when none is live (`grpc/server.go`), so the step path creates one only when `agents.Live(id)` is true
(`terms.BroadcasterIfLive`), where the factory returns the existing session. With no live agent the step is refused
(guard 5); the only path that starts an agent is guard 5's explicit `first` on an unheld sandbox.

A broadcaster created with no client attached sets the PTY and its screen to `stepScreenSize` (140×45) and nudges a
resize, so the agent repaints and the input box is on the screen (research R2).

Each check refuses with `FAILED_PRECONDITION` and the reason given. Nothing is typed.

| # | Check | Reason |
|---|---|---|
| 1 | The sandbox is RUNNING | `sandbox is <state>` |
| 2 | `controller.orchestrator_id == req.orchestrator_id` | `not this orchestrator's worker` |
| 3 | `first` (the run is not started): `controller.run == nil` → record `WorkerRun{id, action, step_role}`; `controller.run.id == run_id` → accepted (a retried first step). Otherwise `controller.run.id == run_id` → set `step_role` | `run X is already on this sandbox` / `run moved` |
| 3a | `req.step_index > controller.run.step_index` (indices only grow; a skipped step leaves a gap), or nothing recorded yet, or `req.step_index == controller.run.step_index` with `retry` while `controller.run.halted` (or, for a CLEAR step, while `hold == NEEDS_DEVELOPER`). A CLEANUP step other than a retried Clear is refused while `hold == NEEDS_DEVELOPER` (`cleanup dropped`) | `ALREADY_EXISTS` `step already sent` (never typed twice, never replayed) |
| 4 | Agent status IDLE (WAITING is **not** at a prompt for a step) | `agent is <status>` |
| 5 | A live agent (`agents.Live`) that is ready (SWI-15 `Ready`). With `first`, no live agent and no hold, the daemon starts one as SWI-15's `Deliver` does (`startAgent`, which creates the broadcaster) and refuses this call. In every other case with no live agent it refuses and starts nothing: a retried Clear, a cleanup step, or a held sandbox never gets an agent started | `agent is starting` / `no live agent` |
| 6 | `claudeui.InputBox(screen)` is present, ready and **empty** | `input box not empty` / `not at an input box` |

**Commit.** Only when **every** check passes does the same update write `step_index`, `step_sent_at` and `halted =
false` (and, for `first`, record the run). A refusal at any check writes nothing, so a refused step is never taken
for a typed one and a refused retry stays retryable.

If typing fails, the guard's record is rolled back as `Deliver` does today (`rollbackRun`): the whole run for a
`first` step that recorded it, otherwise only `step_index`/`step_sent_at`.

## 2. Type

1. Open `tap := hooks.Tap(sandboxID)` **before** typing, so no report is missed.
2. Type the text:
   - for `MODEL`/`EFFORT`/`CLEAR`, the text the daemon **derives** from `kind` + `expect` (`/model opus`, `/effort
     medium`, `/clear`); `step.text` is ignored, so a caller can't type an arbitrary command as a built-in;
   - for `ACTION_PROMPT`, `worker.Frame(orchName, runID, action, step.text)`;
   - for `CUSTOM`, `step.text` as is.

   `TypeStep` **refuses** (`INVALID_ARGUMENT`, nothing typed) any text containing ESC (0x1B), another C0 control
   other than `\n` and `\t`, DEL (0x7F), or a C1 control (U+0080–U+009F). In particular the paste-end marker
   `ESC[201~` can never appear inside the paste. This is defence in depth: the orchestrator already refuses such
   characters in action text and in call argument values (research R13), and `Registry.Prompt` (runs without steps)
   applies the same check.

   Either is sent as **one bracketed paste**, then `\r` after 50 ms (`agent.Registry.TypeStep`). This is
   `Registry.Prompt` **without** its blind `ensureSubmitted`.
3. Record `sent_at`.

## 3. Submit check (`window = submitWindow`, 2.5 s)

Polling: the hook tap as events arrive, plus the screen every 50 ms. Only tap events that passed the hook token
check **and** carry the main agent's `session_id` (`AgentSession.session_id`) count; a nested `claude -p` in the
workspace has its own session and is ignored (research R13).

| Observation (since `sent_at`) | Result |
|---|---|
| A tap event `UserPromptSubmit` or `PreToolUse` | **MODEL_TURN**: accepted, return now |
| A tap event `Notification` with a type other than `idle_prompt`/`auth_success` (needs input) | **MODEL_TURN_INPUT_ONLY**, unless a turn-start event also arrived (then MODEL_TURN). Return now. |
| A tap event `SessionStart`/`SessionEnd` | **Not** a turn start. It is recorded as evidence (CLEAR read-back). |
| Screen: a modal marker (picker or dialog) with **no** turn-start event yet | Wait up to `modalGrace` (1 s; T006 measures hook latency and may raise it) for a needs-input `Notification` or a turn start, pressing nothing. If one arrives, classify as above (a skill's own permission or question dialog). Otherwise **NOT_ACCEPTED** `picker opened`, with **no Enter** (FR-206d). |
| `window` ends and `InputBox` still shows `step.text`: the box's rows joined (a long step wraps) and prefix-matched after trimming, **or** a paste placeholder `[Pasted text #N …]` (a multi-line step such as the framed action prompt) | **Re-press** Enter if fewer than 3 re-presses so far (first `\r`, then keypad Enter, as today), and extend `window` by `submitWindow`. Otherwise NOT_ACCEPTED `text still in the input box`. |
| `window` ends with no turn-start event, status IDLE, and `InputBox` empty and ready | **AGENT_LOCAL**, after the read-back below |
| Anything else when `window` ends (box missing, status changed without a turn-start event) | **NOT_ACCEPTED** `not at an empty input box` |

**Read-back for AGENT_LOCAL:**

| Kind | Must hold | Otherwise NOT_ACCEPTED with |
|---|---|---|
| MODEL | `claudeui.ConfirmationAfter(screen, step.text, before)` starts with `Set model to <expect>`, where `before` is the number of `❯ <step.text>` echoes on screen just before typing: only an echo **added after `sent_at`** counts, so an older identical echo (the previous run's `/model opus`, or a retried step) never passes | the line seen (e.g. `Model 'bogus' not found`), or `no confirmation` |
| EFFORT | the line under a **new** echo (as for MODEL) starts with `Set effort level to <expect>`, or equals `Effort level set to auto` when `expect == "auto"` | the line seen, or `no confirmation` |
| CLEAR | a tap `SessionStart` with `source == "clear"` arrived after `sent_at`, **corroborated** (research R13): a `session_id` different from the recorded one, and the screen redrawn to the empty post-clear state | `no new session after /clear` |
| CUSTOM, ACTION_PROMPT | nothing more (a local custom command is finished once the check passes, spec edge case) | — |

Enter is **never** pressed on an empty box or while a modal is shown. A step whose text was replaced in the box by
developer typing reads as "box not empty". Under the guard that cannot happen before typing; after typing it ends
in NOT_ACCEPTED `text still in the input box`, and nothing is appended (FR-206c).

## 4. `check_only` (restart resolution, research R8)

Steps 1 (guard checks 1–2 only, never recording) and 3's read-back run against the **current** screen and recorded
evidence. Nothing is typed. It applies to MODEL, EFFORT and CLEAR only: the RPC returns AGENT_LOCAL if the read-back
passes, otherwise NOT_ACCEPTED, and always NOT_ACCEPTED for CUSTOM and ACTION_PROMPT (an empty box alone never
finishes a step, FR-217). With no live agent (after an owning-daemon restart the PTY is gone) it returns
NOT_ACCEPTED `no live agent` and creates nothing (research R14).

## 5. Notifications (FR-223)

`step_role` recorded in step 1 makes the daemon's `HookServer` skip `EmitNotification(TASK_COMPLETE, …)` on `Stop`
and on the WAITING backstop while the role is `SETUP` or `CLEANUP`. `NEEDS_PROMPTING` is unchanged.

## 6. Timing bound

The worst case is `submitWindow × (1 + 3)` = 10 s, plus typing. That is inside the orchestrator's 15 s
`deliverTimeout`. The check holds no registry lock while waiting.

## 7. Test fixtures

`internal/agent/testdata/claudeui/` holds the screens captured in research R1. Each has its expected
`InputBox`/`ConfirmationAfter` result:
- the startup placeholder;
- an empty box after a command;
- each confirmation and error line;
- the `/model` picker;
- the auto-mode dialog;
- the post-`/clear` banner;
- text left in the box;
- a framed action prompt (multi-line paste) left in the box, and a long custom prompt wrapped over several rows.
