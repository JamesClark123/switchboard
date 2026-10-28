# Data Model: Action Pre & Post Prompts (SWI-17)

Phase 1 of [plan.md](./plan.md). Field numbers and the proto text are in [contracts/proto-delta.md](./contracts/proto-delta.md).
Every change is **additive**. Records written before this feature read as described under *Upgrade* for each entity.

## Action (extended) — `pb.Action.sequence`

| Field | Type | Rules |
|---|---|---|
| `model_enabled` | bool | Model built-in on or off (FR-202). |
| `model` | `ModelChoice` (OPUS, FABLE, SONNET, HAIKU) | Must be set and known when saved; kept while `model_enabled` is false. |
| `effort_enabled` | bool | Effort built-in on or off. |
| `effort` | `EffortChoice` (LOW, MEDIUM, HIGH, XHIGH, MAX, AUTO) | As for `model`. `ultracode` is not offered (spec planning amendment, research R1). |
| `clear_enabled` | bool | Clear built-in on or off. |
| `pre_prompts` | repeated string | Each is trimmed on save; non-empty; ≤ 16 KiB (`maxArgumentBytes`); at most 10 per list. Placeholders must be declared; none in the first word; none at all if it starts with `!` (FR-204). |
| `post_prompts` | repeated string | Same rules. |

**Upgrade (FR-210).** An action with **no** `sequence` message reads as `{model_enabled: true, model: OPUS,
effort_enabled: true, effort: MEDIUM, clear_enabled: true}`, with empty lists. `effectiveSequence(a)` is the only
reader. The editor shows these defaults, and saving writes them explicitly.

**New action (FR-203).** The TUI pre-fills the same defaults.

## Run step (new) — `pb.RunStep`, snapshotted on `pb.Run.steps` at call time (FR-207)

| Field | Type | Meaning |
|---|---|---|
| `role` | `StepRole` (SETUP, ACTION, CLEANUP) | Its place in the sequence; used for display, FR-211/FR-209 and notifications (FR-223). |
| `kind` | `StepKind` (MODEL, EFFORT, CLEAR, CUSTOM, ACTION_PROMPT) | MODEL, EFFORT and CLEAR are built-ins, agent-local by definition, with a read-back. |
| `text` | string | Exactly what is typed. Placeholders are already filled. The ACTION_PROMPT step's text is `Run.prompt` (the frame is added at delivery, as today). |
| `expect` | string | The read-back for MODEL (the family name, e.g. `Opus`) and EFFORT (the level). Empty otherwise. |

**Order (FR-205):** MODEL?, EFFORT?, custom pre in order, ACTION_PROMPT, custom post in order, CLEAR?. A run whose
action has no enabled built-in and no custom prompts gets **no steps**, and is delivered exactly as today (FR-208).

## Run (extended) — `pb.Run`

| Field | Type | Meaning |
|---|---|---|
| `steps` | repeated `RunStep` | The snapshot (FR-207). Later action edits never change it. |
| `step` | int32 | Index of the current step. It has no meaning while `steps` is empty or the run is QUEUED. |
| `step_status` | `StepStatus` (see below) | Where the current step is. |
| `step_sent_at` | Timestamp | When the current step was typed (FR-217 evidence baseline). |
| `step_kind_seen` | `StepObserved` (UNKNOWN, MODEL_TURN, MODEL_TURN_INPUT_ONLY, AGENT_LOCAL) | The kind the submit check observed (FR-206b). `MODEL_TURN_INPUT_ONLY`: classified only by a needs-input report. |
| `step_note` | string | The *not accepted* reason as shown, e.g. `text still in the input box`, `picker opened`, `Model 'bogus' not found`. |
| `step_sent_before_restart` | bool | FR-217 marker (FR-213, FR-222). Cleared whenever the run leaves HALTED or `step` changes. |
| `hold` | `WorkerHold` | Mirrors `WorkerRun.hold` while this run holds its sandbox (FR-213). |
| `hold_since` | Timestamp | When the current hold began (display only; the daemon ends the hold, research R7). |
| `step_unconfirmed` | bool | The current step halted after an UNCONFIRMED outcome: it was typed and may have run. The row shows `(may have run)`, and `R` asks the "may already have run" confirm (research R2). Cleared, like `step_sent_before_restart`, whenever the run leaves HALTED or `step` changes (with `controller.run.halted`). |
| `step_send` | int32 | Send number of the current step's latest send. Incremented for every new send (a retry reuses it) and sent as `step_index`, so guard 3a stays monotonic even when setup is restarted from step 0 after an orphan reap (research R14). |

### StepStatus transitions

```text
PENDING ──persist──► SENDING ──deliver ok──► SENT ──MODEL_TURN──► TURN ──turn end (or idle nudge)──► (next step PENDING | cleanup wait | done)
   │  ▲                              │
   │  └─FailedPrecondition (retry R6)
   │                      ├──AGENT_LOCAL──► (next step PENDING | …)
   │                      └──NOT_ACCEPTED─► HALTED ──R retry──► PENDING
   │                                           ├──S skip────► (next step PENDING)        [not for CLEAR or ACTION_PROMPT]
   │                                           └──X fail────► FR-212 (setup or action) / drop custom cleanup, Clear still
   │                                                           sent if enabled, then free (FR-222)
   │                                                           / CLEAR: hold NEEDS_DEVELOPER
   └─(after ACTION step) AWAIT_CLOSE ──completion report / mark free──► cleanup steps (PENDING, once agent IDLE)
```

- `DONE`: every step has finished, or the remaining ones were dropped (FR-216).
- `SENDING` is persisted **before** `DeliverWorkerStep` is called, so a restart or a lost link mid-call never leaves
  a typed step looking PENDING. It is resolved from the daemon's `WorkerRun.step_index`/`step_sent_at`: equal to
  this step → SENT with that `sent_at`; otherwise → PENDING (it was never typed).
- Only one step call is in flight per sandbox (research R5, single-flight).
- **A restart** resolves `SENDING`/`SENT`/`TURN` per research R8. Without evidence the step becomes `HALTED`, with
  `step_sent_before_restart = true`.

### Run state (`RunState`) — unchanged values, new rules

| While | Run state |
|---|---|
| A setup step | RUNNING, or AWAITING_INPUT while the agent asks (FR-209). Never AWAITING_REVIEW. |
| The action step | As today (RUNNING / AWAITING_INPUT / AWAITING_REVIEW). |
| Cleanup | The closed state: COMPLETED or FAILED (FR-215). The run is still listed under *In progress* while it holds its sandbox (FR-213). |
| A setup step halted *not accepted* | RUNNING, with `step_status = HALTED`. *In progress*, retention-exempt. |

## WorkerRun (extended) — `pb.WorkerRun` on the owning daemon's `Sandbox.controller.run`

| Field | Type | Meaning |
|---|---|---|
| `step_role` | `StepRole` | The role of the step last delivered. The daemon suppresses task-complete notifications while it is SETUP or CLEANUP (FR-223). |
| `hold` | `WorkerHold` (NONE, CLEANING_UP, NEEDS_INPUT_FREEING, NEEDS_DEVELOPER) | Set by the orchestrator (the existing `UpdateWorker{set_run, run}`) after the run closes; see research R7. |
| `halted` | bool | The run is halted at a not-accepted step, so the sandbox counts as needing the developer (FR-213). |
| `step_index`, `step_sent_at` | int32, Timestamp | Written by the `DeliverWorkerStep` guard in the same `reg.Update` as the typing decision. `step_index` is the send number (`Run.step_send`). A repeat for the same number is refused, so no step is typed twice (FR-217). |
| `step_note` | string | A copy of the run's `waiting:` / not-accepted note, written through `workerRunFor` only when it changes, so the sandbox list can surface a stuck cleanup (FR-213). |
| `hold_gen` | int64 | **Daemon-written only**, on the **transition into** `NEEDS_DEVELOPER`: the agent PTY generation then. The generation counter is persisted, so it only grows across restarts. The controller `Update` keeps the stored value and ignores the request's, and re-writing an existing hold doesn't re-record it. A `startup` ends the hold only from a later generation (read from the hook's token), spawned by `AttachAgent` or guard 5 (research R13). |

**Writers.** Every orchestrator write of `WorkerRun` (`deliver`'s local mirror, `syncReviewFlag`,
`repairReviewFlag`, holds) goes through one helper, `workerRunFor(run, live)`, that copies **all** fields from the
current record. `set_run` replaces the whole message, so a writer that builds `WorkerRun{Id, Action, NeedsReview}`
would drop `hold`, `halted` and `step_index`.

**Invariant:** a sandbox with `controller.run != nil` is not free. That rule exists today. Holds and halts reuse
it, so dispatch, `Deliver` and `DeliverWorkerStep` refuse a held sandbox with no extra check.

**Conditional writes.** Orchestrator writes to an existing run (hold, halt, review flag) use `UpdateWorker{set_run,
expect_run_id}`: applied only while `controller.run.id` is still that run, so a late write never re-creates a run the
daemon already released.

**Agent exit during cleanup.** When the agent exits while `step_role == CLEANUP` or `hold != NONE`, the daemon sets
`hold = NEEDS_DEVELOPER` itself (as `leaveRunning` does), and guard 3a then refuses the remaining custom cleanup
steps. A fresh session seen before the orchestrator hears of the exit therefore still ends the hold, and nothing is
typed into it.

**Stop/start:** `leaveRunning` keeps a `controller.run` with any `hold`, or with `step_role == CLEANUP`, and sets
its `hold = NEEDS_DEVELOPER` on the daemon itself, so a restarted sandbox is never free with an uncleared session
(SC-001), even before the orchestrator hears of it. Any other run is cleared as today, and the orchestrator handles
it as an exit during that phase (FR-212).

**Hold release is the daemon's.** On a live `SessionStart` with source `startup` or `clear` for a sandbox whose
`controller.run.hold == NEEDS_DEVELOPER`, the owning daemon clears `controller.run` (FR-216). No cross-machine clock
is compared; the orchestrator sees the cleared record in `sandbox_changed` and finishes the run's cleanup.

## AgentSession (extended) — `pb.AgentSession`, recorded by the sandbox's daemon from hooks (research R4)

| Field | Set on |
|---|---|
| `last_submit_at` | `UserPromptSubmit` |
| `last_turn_end_at` | `Stop` with no background tasks pending, or the WAITING backstop expiring |
| `last_session_start_at`, `last_session_source` | `SessionStart` (`startup`, `resume`, `clear`; `compact` is ignored, as in SWI-15) |
| `session_id` | The main agent's current session. The main agent is matched by **process** (the hook's `X-Sxb-Pid` equals the generation's main PID, bound by its first non-`compact` SessionStart), not by this id. The id follows `clear`/`resume` SessionStarts **from the main PID** only, never from a nested `claude -c -p` (research R13). It is used for Clear's read-back and hold release. Hook events from any other session are ignored for steps, evidence and holds. |
| `last_needs_input_at` | A needs-input `Notification` from the main session (R8 rule 2). |
| `input_idle_since` | Written **only when it changes** (set or unset), never per sample, so it doesn't flood `sandbox_changed`. Sampled every 5 s and on each hook while `controller.run` is set: when the input box is empty and ready and the agent is not WORKING, the time that began; unset otherwise. Finishes a MODEL_TURN_INPUT_ONLY step after 60 s (the idle-nudge fallback, research R1). |

## Hold (sandbox) — state and display

| Hold | Shown on the sandbox row and Workers view | Surfaced at top of All/host/group tabs |
|---|---|---|
| CLEANING_UP | `cleaning up after <run>` (`· waiting: <reason>` once its step waits over 10 s) | no while it progresses; **yes** once its step has a `waiting:` note or is halted, so a stuck cleanup is visible |
| NEEDS_INPUT_FREEING | `needs input (freeing) · <run>` | yes |
| NEEDS_DEVELOPER | `needing the developer — open its terminal and run /clear · <run>` | yes |
| (run halted not accepted) | `<run> halted: <step> not accepted` | yes |

## Constants (no env vars)

| Name | Value | Where |
|---|---|---|
| `maxStepPrompts` | 10 | orchestrator `constants.go` |
| `submitWindow` | 2.5 s (today's `submitCheckDelay`) | worker step delivery |
| `submitRePresses` | 3 (today's `submitRetries`) | worker step delivery |
| `stepRetryEvery` | 2 s | orchestrator (research R6) |
| `stepWaitNote` | 10 s | orchestrator: a guard refusal lasting longer shows as `step_note` (`waiting: <reason>`) |
| `stepScreenSize` | 140×45 | daemon: the PTY and screen size set when a broadcaster is created with no client (research R2) |
| `modalGrace` | 1 s, **capped at 2 s** | worker step delivery: how long a modal may wait for a needs-input report before NOT_ACCEPTED (T006 may raise it within the cap; contracts/worker-step.md §6) |
| `screenQueue` | 256 chunks | daemon: the parser goroutine's bounded channel; overflow marks the screen stale (research R2) |
| `hookTokenBytes` | 32 | daemon: the per-agent hook token, drawn for each spawned PTY generation and passed in the agent's environment (`SXB_HOOK_TOKEN`); persisted as `{generation → token}` and accepted until that agent is gone (research R13) |
| `clearSettle` | 1 s | daemon: how long the screen is polled for the post-clear state after a `clear` SessionStart (research R13) |
| `orphanProbeConcurrency` | 8 | daemon: start-up probes run at once, after the servers start (research R14) |
| `orphanProbeTimeout` | 10 s | daemon: each start-up probe for a detached agent (one `sbx exec` per running sandbox, run asynchronously once the servers have started); a timeout leaves the sandbox *unknown*, refused and re-probed with backoff; after 3 failures `t` offers *Replace anyway* (research R14) |
| `orphanBackstop` | 60 s | daemon: re-probe interval only while an orphan record holds a run (research R14) |
| `orphanKillWait` | 5 s | daemon: TERM, then KILL, for an orphaned agent, reaped only by the developer (`t` → Replace, `R` Restart setup) or by the daemon's two surfaced cases (research R14) |
