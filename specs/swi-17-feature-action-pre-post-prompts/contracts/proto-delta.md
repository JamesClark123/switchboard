# Contract: proto delta (SWI-17)

This change is additive to `src/libs/switchboard-proto/proto/switchboard.proto`. It is mirrored in
`specs/001-sandbox-session-manager/contracts/switchboard.proto`, and the code is regenerated with `make proto`.

Field numbers continue from today's maximum in each message:

| Message | Today's max | New fields from |
|---|---|---|
| Action | 8 | 9 |
| Run | 19 | 20 |
| AgentSession | 4 | 5 |
| WorkerRun | 3 | 4 |
| NotificationKind | 8 | 9 |

No field is removed or renumbered.

```proto
// ---- Action sequence (FR-200..FR-204, FR-210) ----------------------------

enum ModelChoice {
  MODEL_CHOICE_UNSPECIFIED = 0;          // refused at save
  MODEL_CHOICE_OPUS = 1;                 // `/model opus`   -> "Set model to Opus …"
  MODEL_CHOICE_FABLE = 2;                // `/model fable`  -> "Set model to Fable …"
  MODEL_CHOICE_SONNET = 3;               // `/model sonnet` -> "Set model to Sonnet …"
  MODEL_CHOICE_HAIKU = 4;                // `/model haiku`  -> "Set model to Haiku …"
}

enum EffortChoice {
  EFFORT_CHOICE_UNSPECIFIED = 0;         // refused at save
  EFFORT_CHOICE_LOW = 1;
  EFFORT_CHOICE_MEDIUM = 2;
  EFFORT_CHOICE_HIGH = 3;
  EFFORT_CHOICE_XHIGH = 4;
  EFFORT_CHOICE_MAX = 5;
  EFFORT_CHOICE_AUTO = 6;                // read back as "Effort level set to auto"
  // ultracode is deliberately absent (spec planning amendment, research R1).
}

// Absent on an Action => defaults: Opus, medium, Clear on, no custom prompts (FR-210).
message ActionSequence {
  bool model_enabled = 1;
  ModelChoice model = 2;
  bool effort_enabled = 3;
  EffortChoice effort = 4;
  bool clear_enabled = 5;
  repeated string pre_prompts = 6;       // trimmed; <= 10; FR-204 rules
  repeated string post_prompts = 7;
}

message Action {
  // … fields 1..8 unchanged …
  ActionSequence sequence = 9;
}

// ---- Run sequence snapshot (FR-205, FR-207, FR-213, FR-217) ---------------

enum StepRole {
  STEP_ROLE_UNSPECIFIED = 0;
  STEP_ROLE_SETUP = 1;
  STEP_ROLE_ACTION = 2;
  STEP_ROLE_CLEANUP = 3;
}

enum StepKind {
  STEP_KIND_UNSPECIFIED = 0;
  STEP_KIND_MODEL = 1;
  STEP_KIND_EFFORT = 2;
  STEP_KIND_CLEAR = 3;
  STEP_KIND_CUSTOM = 4;
  STEP_KIND_ACTION_PROMPT = 5;
}

enum StepStatus {
  STEP_STATUS_UNSPECIFIED = 0;
  STEP_STATUS_PENDING = 1;               // waiting to be typed (agent busy, box not empty, run not yet closed)
  STEP_STATUS_SENT = 2;                  // typed; submit check / restart resolution pending
  STEP_STATUS_TURN = 3;                  // accepted as a model turn; waiting for the turn to end
  STEP_STATUS_HALTED = 4;                // not accepted (FR-206c); developer retries, skips or fails
  STEP_STATUS_AWAIT_CLOSE = 5;           // action step done; waiting for completion report / mark free
  STEP_STATUS_DONE = 6;                  // every step finished or dropped
  STEP_STATUS_SENDING = 7;               // persisted before DeliverWorkerStep; resolved from WorkerRun.step_index (research R5, R8)
}

enum StepObserved {
  STEP_OBSERVED_UNKNOWN = 0;
  STEP_OBSERVED_MODEL_TURN = 1;
  STEP_OBSERVED_MODEL_TURN_INPUT_ONLY = 2;   // classified only by a needs-input report (FR-206b)
  STEP_OBSERVED_AGENT_LOCAL = 3;
}

enum WorkerHold {
  WORKER_HOLD_NONE = 0;
  WORKER_HOLD_CLEANING_UP = 1;
  WORKER_HOLD_NEEDS_INPUT_FREEING = 2;
  WORKER_HOLD_NEEDS_DEVELOPER = 3;
}

message RunStep {
  StepRole role = 1;
  StepKind kind = 2;
  string text = 3;                       // exactly what is typed (ACTION_PROMPT: Run.prompt; frame added at delivery)
  string expect = 4;                     // MODEL: family ("Opus"); EFFORT: level ("medium", "auto")
}

message Run {
  // … fields 1..19 unchanged …
  repeated RunStep steps = 20;           // empty => no sequence; delivered exactly as before (FR-208)
  int32 step = 21;
  StepStatus step_status = 22;
  google.protobuf.Timestamp step_sent_at = 23;
  StepObserved step_kind_seen = 24;
  string step_note = 25;                 // not-accepted reason as shown
  bool step_sent_before_restart = 26;
  WorkerHold hold = 27;
  google.protobuf.Timestamp hold_since = 28;
  int32 step_send = 29;                  // send number of the current step's latest send: incremented for every new
                                         // send (a retry reuses it), sent as DeliverWorkerStepRequest.step_index, so a
                                         // setup restarted from step 0 after an orphan reap re-sends (research R14)
  bool step_unconfirmed = 30;            // the current step halted after an UNCONFIRMED outcome: it was typed and may
                                         // have run, so R asks the "may already have run" confirm (research R2)
}

// ---- Worker-side records (research R4, R7, R10) ---------------------------

message AgentSession {
  // … fields 1..4 unchanged …
  google.protobuf.Timestamp last_submit_at = 5;         // UserPromptSubmit
  google.protobuf.Timestamp last_turn_end_at = 6;       // Stop w/o background tasks, or backstop expiry
  google.protobuf.Timestamp last_session_start_at = 7;  // SessionStart (not "compact")
  string last_session_source = 8;                       // "startup" | "resume" | "clear"
  google.protobuf.Timestamp input_idle_since = 9;       // box empty + ready and agent not WORKING since (unset otherwise);
                                                        // sampled while controller.run is set (idle-nudge fallback, R1);
                                                        // written only when it is set or unset, never per sample
  string session_id = 10;                               // the main agent's current session; follows clear/resume
                                                        // SessionStarts from the main PID only (the main agent is
                                                        // matched by the hook's X-Sxb-Pid, not by this id; research
                                                        // R13). Used for Clear's read-back and hold release
  google.protobuf.Timestamp last_needs_input_at = 11;   // main-session needs-input Notification (R8 rule 2)
}

message WorkerRun {
  // … fields 1..3 unchanged …
  StepRole step_role = 4;                // suppresses TASK_COMPLETE unless ACTION (FR-223)
  WorkerHold hold = 5;                   // set via UpdateWorker{set_run, run}
  bool halted = 6;                       // run halted at a not-accepted step (surfacing, FR-213)
  int32 step_index = 7;                  // send number of the last step typed for this run (Run.step_send), recorded
                                         // by the guard (no double send, R3)
  google.protobuf.Timestamp step_sent_at = 8;  // when that step was typed (daemon clock)
  string step_note = 9;                  // the run's `waiting: …` / not-accepted note, written only when it changes,
                                         // so the sandbox list can surface a stuck cleanup (FR-213)
  int64 hold_gen = 10;                   // daemon-written only, on the transition into NEEDS_DEVELOPER: the agent PTY
                                         // generation then (a persisted counter). Update keeps the stored value and
                                         // ignores the request's; a startup ends the hold only from a later
                                         // generation, read from its hook token (research R13)
}

message WorkerView {
  // … fields 1..11 unchanged …
  WorkerHold hold = 12;                  // the worker's controller.run.hold (Workers view, FR-213)
  bool halted = 13;                      // controller.run.halted
  string step_note = 14;                 // controller.run.step_note
}

enum NotificationKind {
  // … 0..8 unchanged …
  NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED = 9;   // orchestrator: a step halted (FR-206c)
  NOTIFICATION_KIND_AGENT_REPLACED = 10;         // owning daemon: an agent detached by a daemon restart was stopped,
                                                 // by the developer or by the daemon; names who, the agent's last
                                                 // state and the run affected (research R14)
}

// ---- New RPCs --------------------------------------------------------------

service Switchboard {
  // Worker side (owning daemon). Types one step and runs its submit check (contracts/worker-step.md).
  rpc DeliverWorkerStep(DeliverWorkerStepRequest) returns (DeliverWorkerStepResponse);
  // Orchestrator side. Retry, skip or fail a halted step (FR-222).
  rpc ResolveRunStep(ResolveRunStepRequest) returns (Run);
}

message DeliverWorkerStepRequest {
  string sandbox_id = 1;
  string orchestrator_id = 2;
  string run_id = 3;
  string action = 4;
  RunStep step = 5;                      // MODEL/EFFORT/CLEAR: the daemon types the text it derives from kind + expect
                                         // (`/model opus`…), refusing an expect outside the fixed sets, and ignores
                                         // step.text (research R13)
  bool first = 6;                        // the run is not started: record controller.run if none (idempotent for the same run_id)
  bool check_only = 7;                   // restart resolution (research R8): read back without typing
  int32 step_index = 8;                  // Run.step_send: must be greater than controller.run.step_index (monotonic),
                                         // or equal with retry
  bool retry = 9;                        // ResolveRunStep RETRY: a repeated step_index is allowed only while controller.run.halted
                                         // (or, for CLEAR, while hold == NEEDS_DEVELOPER)
  bool restart = 10;                     // R on a step halted by an owning-daemon restart: the daemon reaps the orphan
                                         // (mode restart), starts a fresh agent as guard 5 does and refuses "agent is
                                         // starting"; accepted only while the run is halted that way (research R14)
}

message AgentInput {
  // … fields 1..4 unchanged …
  message AttachInfo {
    // … fields 1..3 unchanged …
    bool replace_orphan = 4;             // `t` → Replace on an agent detached by a daemon restart (any sandbox): reap
                                         // it (an ACTION run fails, "agent replaced by the developer after a daemon
                                         // restart"), then start a fresh agent. Without it, attaching beside an
                                         // orphan is refused with the agent's last state (research R14)
  }
}

message UpdateWorkerRequest {
  // … fields 1..7 unchanged …
  string expect_run_id = 8;              // compare-and-set key: when set, the update (set or clear of controller.run, with
                                         // set_run) applies only while controller.run.id == expect_run_id; otherwise
                                         // FAILED_PRECONDITION "run moved". Never re-creates a cleared run. Every
                                         // orchestrator write to a sequenced run sets it (research R7).
                                         // A NEEDS_DEVELOPER hold is never lowered by an update: only cleared (R7).
}

enum StepOutcome {
  STEP_OUTCOME_UNSPECIFIED = 0;
  STEP_OUTCOME_MODEL_TURN = 1;
  STEP_OUTCOME_MODEL_TURN_INPUT_ONLY = 2;
  STEP_OUTCOME_AGENT_LOCAL = 3;          // accepted and finished (read-back passed for built-ins)
  STEP_OUTCOME_NOT_ACCEPTED = 4;
  STEP_OUTCOME_UNCONFIRMED = 5;          // typed, but the screen stayed stale past the extension (research R2): the
                                         // orchestrator treats the step as SENT and resolves it by evidence (R8)
}

message DeliverWorkerStepResponse {
  StepOutcome outcome = 1;
  string note = 2;                       // NOT_ACCEPTED: reason as shown (e.g. "Model 'bogus' not found")
  google.protobuf.Timestamp sent_at = 3; // when the step was typed (zero for check_only)
  google.protobuf.Timestamp submit_at = 4;  // UserPromptSubmit seen by the check (MODEL_TURN), daemon clock
}

enum StepResolution {
  STEP_RESOLUTION_UNSPECIFIED = 0;
  STEP_RESOLUTION_RETRY = 1;
  STEP_RESOLUTION_SKIP = 2;              // refused for CLEAR
  STEP_RESOLUTION_FAIL = 3;
}

message ResolveRunStepRequest {
  string orchestrator_id = 1;
  string run_id = 2;
  int32 step = 3;                        // must equal the run's halted step (stale UI guard)
  StepResolution resolution = 4;
}
```

## Errors

**`DeliverWorkerStep`**

| Condition | Code / result |
|---|---|
| Sandbox not RUNNING; controller missing or another orchestrator's; agent not IDLE or not ready; input box not empty and ready; (`first`) another run already recorded; (not `first`) `controller.run.id ≠ run_id` | `FAILED_PRECONDITION` with the reason. Nothing is typed, and the orchestrator retries (research R6); a refusal lasting over `stepWaitNote` shows on the row. |
| `req.step_index ≤ controller.run.step_index` without a valid `retry` (already typed, or a replay) | `ALREADY_EXISTS`. Nothing is typed; the orchestrator reads `controller.run.step_sent_at` from the sandbox record, treats the step as SENT and resolves it per research R8. |
| MODEL/EFFORT `expect` outside the fixed sets (research R13) | `INVALID_ARGUMENT`, before the guard commits. Nothing is recorded or typed. The orchestrator halts the step with the error as its note (never retries it). |
| No live agent (and not a `first` step on an unheld sandbox, which starts one) | `FAILED_PRECONDITION` `no live agent`. The broadcaster is never created for a sandbox with no live agent. |
| `restart` on a run not halted by a daemon restart | `FAILED_PRECONDITION` (`not a restart halt`) |
| Beside an orphan, or while the restart probe hasn't finished (`Registry.Session`'s gate, research R14) | `FAILED_PRECONDITION` with the gate's reason (`agent detached by a daemon restart; last seen …` / `checking for a detached agent`); nothing spawns and nothing is reaped |
| Unknown sandbox | `NOT_FOUND` |
| An older daemon | `UNIMPLEMENTED`. The orchestrator **unclaims** the run, marks that worker unable to take sequenced runs until its link reconnects, and re-queues the run. It fails the run ("no worker supports pre/post prompts; update their daemons") only when no attached worker is eligible (research R6). |

**`ResolveRunStep`**

| Condition | Code |
|---|---|
| Run not found | `NOT_FOUND` |
| Run not halted (except RETRY of a CLEAR step under a NEEDS_DEVELOPER hold), or `step` ≠ its halted step | `FAILED_PRECONDITION` ("the run moved on; reload") |
| SKIP on a CLEAR step | `FAILED_PRECONDITION` ("Clear cannot be skipped") |
| SKIP on the ACTION_PROMPT step | `FAILED_PRECONDITION` ("the action prompt cannot be skipped; retry or fail") |
