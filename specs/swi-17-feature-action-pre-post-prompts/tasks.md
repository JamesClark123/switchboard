# Tasks: Action Pre & Post Prompts

**Input**: Design documents from `/specs/swi-17-feature-action-pre-post-prompts/` (plan.md, spec.md, research.md,
data-model.md, contracts/proto-delta.md, contracts/worker-step.md, contracts/jobs-ui.md, quickstart.md)

**Tests**: Included. The constitution (Rule VI) requires automated tests and ≥90% coverage per module, so every
phase carries its tests.

**Organization**: Setup and Foundational build the shared step machinery: proto, the screen reader, the submit
check and the sequence snapshot. Then come the user stories in priority order:
- **US1**: built-in steps, cleanup, holds, and the controls for a step that was not accepted. A halt with no way
  out would strand a worker, so the controls are part of the MVP.
- **US2**: custom prompts.
- **US3**: the full sequence view, restart resolution and the remaining surfaces.

Paths are relative to the repository root:
- `SBD/` = `src/services/switchboardd/internal/`
- `UI/` = `src/apps/switchboard-tui/internal/ui/`
- `PROTO` = `src/libs/switchboard-proto/proto/switchboard.proto`

## Deferred items (accepted by the developer, 2026-10-28; follow-up SWI-19)

The developer accepted PR #21's *Proposed deferrals*. **This list overrides the task text below** wherever they
conflict; research R14 lists the same items as known limitations.

- **No bulk Replace.** No `ReplaceDetachedAgent` RPC (T003, T047) and no `R` on the Workers view (T031). Each
  detached worker is replaced with `t` → Replace (`AttachAgent.replace_orphan`). The `ReapOrphan` call-site guard
  test (T047) allows two callers: `AttachAgent` with `replace_orphan` and `DeliverWorkerStep.restart`.
- **No aggregated start-up notification.** Every orphan record gets its own `NOTIFICATION_KIND_AGENT_DETACHED` once
  (T047 *Surfacing*); there is no host-level notice or `enter` → Workers routing (T027).
- **No `NOTIFICATION_KIND_AGENT_CHECK_FAILED` and no run-note overlay.** Field number 11 of `NotificationKind` is
  reserved, not added (T003). After `orphanUnknownLimit` or 3 failures the sandbox becomes `agent_check`
  CHECK_FAILED and the row note reads `can't check for a detached agent: <error>`; `controller.run.step_note` is
  **not** touched, and the `t` dialog offers *Restart sandbox* / *Wait* (T047, T027, T029).
- **No probe priority order.** Start-up probes run in list order (T047).
- **Generic busy message.** Every `runtimeMu` refusal is `ErrBusy` with the text `sandbox is busy; try again`; it
  doesn't name the operation (T047, T048).
- **Plain orphan notes.** The row note is `agent detached by a daemon restart — t to Replace or Wait`, and halt notes
  say `agent detached by a daemon restart`, without the last-seen state or time. `AgentCheck` keeps its fields;
  `last_status` is still filled (for the `t` dialog), `processes` may stay 0.
- **No PID-1/subreaper refusal** of first binds (T045/T046 as written otherwise), **no shared rate-limited
  `ListStatuses`** for probe pre-checks, and **no `runtimeMu` gating of `sbx mcp load/unload` or `sbx ports`**.
- **The pre-stop drain is best effort** (T047, as already written): no ordering against `sbx stop`, and nothing
  promised about what survives a failed stop.

Tests that exist only for a deferred item are dropped.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US3 from spec.md

---

## Phase 1: Setup

**Purpose**: Start from the prerequisites, move the VT renderer to a library, and land the proto delta.

- [X] T001 **Prerequisites (research R12).** **PR #17 (SWI-15) has merged.** Start the implement branch from `main`
  once **SWI-9's implementation** has merged too. Check both are there:
  - from SWI-15, in `SBD/agent/hooks.go`: the `SessionStart` registration, `hookPayload.Source`, `sessionStart()`;
    also `Ready`/`MarkReady`, `AGENT_STATUS_STARTING` and `startAgent`;
  - from SWI-9: `UI/formguard.go` and the enlargeable `spec.text` fields.

  If SWI-9 is still open, stop and ask the developer. A partial cherry-pick would leave step guard 5 or the form
  guard with nothing to call.
- [X] T002 Create the library module `src/libs/switchboard-termview` (`go.mod` module path
  `github.com/jamesclark123/switchboard/libs/switchboard-termview`) by **moving**
  `src/apps/switchboard-tui/internal/termview/{termview.go,termview_test.go}`, with `git mv` so history is kept.
  - Add it to `go.work`. The Makefile's module list derives from `go.work` (SWI-11), so check that `make lint test
    cover` pick it up.
  - Switch the TUI imports (`UI/terminal.go` and any other users found by `grep -rn internal/termview`).
  - Add `func (s *Screen) Lines() []Line`, where `Line{Text string; Faint []bool}` is the live screen without
    scrollback, with each cell's faint attribute, and a `WithoutScrollback()` option for the daemon (the TUI keeps
    its 5000 lines). Test both in `termview_test.go`. Coverage must stay ≥ 90% for the new module.
- [X] T003 Apply contracts/proto-delta.md to `PROTO`, then run `make proto` (`src/libs/switchboard-proto/gen.sh`).
  - **Enums:** `ModelChoice`, `EffortChoice`, `StepRole`, `StepKind`, `StepStatus` (incl. `SENDING = 7`),
    `StepObserved`, `WorkerHold`, `StepOutcome`, `StepResolution`, `AgentCheck.State`.
  - **Messages:** `ActionSequence`, `RunStep`, `AgentCheck`, `DeliverWorkerStepRequest/Response`,
    `ResolveRunStepRequest`, `ReplaceDetachedAgentRequest`.
  - **Fields:** `Action.sequence` 9, `Run` 20–30, `AgentSession` 5–12, `WorkerRun` 4–10, `WorkerView` 12–14,
    `UpdateWorkerRequest.expect_run_id` 8, and `NotificationKind` 9–11 (`RUN_STEP_NOT_ACCEPTED`, `AGENT_DETACHED`,
    `AGENT_CHECK_FAILED`).
  - **RPCs:** `DeliverWorkerStep`, `ResolveRunStep`, `ReplaceDetachedAgent`.
  - Add labels for the new enums in `src/libs/switchboard-proto/types.go`, alongside `AgentStatusLabel`.
- [X] T004 [P] Mirror the proto delta in `specs/001-sandbox-session-manager/contracts/switchboard.proto`.

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: Everything a step needs: the daemon screen, the UI reader, hook evidence, the step RPC, and the action
sequence model. No user story can be delivered before this phase.

### Daemon screen and UI reader (research R2)

- [X] T005 In `SBD/terminal/broadcaster.go`, keep a `termview.Screen` (no scrollback) per broadcaster **whose sandbox
  has an orchestrator controller**; other sandboxes get none (research R2):
  - `readLoop` hands each chunk to a per-broadcaster parser goroutine through a bounded channel (`screenQueue`, 256)
    and goes straight on to fan-out. Parsing never runs under the broadcaster mutex; the screen has its own lock.
  - On overflow, drop the chunk and mark the screen **stale**. The parser goroutine then drains, **resets** the
    screen and issues a debounced resize nudge itself (at most 1/s), so the screen recovers on its own. While stale,
    `Screen()` reports `stale = true`.
  - `EnableScreen()` adds a screen to an **existing** broadcaster (one created before its sandbox became a worker)
    and nudges a repaint. It is called at attach and on a sandbox's first step.
  - Resize it in `applyResizeLocked` with the PTY.
  - A broadcaster created with **no client attached** sets the PTY and the screen to `stepScreenSize` (140×45),
    then nudges a resize (one row smaller, then back) so the agent repaints.
  - Add `func (b *Broadcaster) Screen() []termview.Line`, which returns a copy.
  - Add `func (r *Registry) Screen(sandboxID string) ([]termview.Line, bool)`, which **never creates** a broadcaster
    (false when there is none), and `BroadcasterExisting(sandboxID)`. Its provider is a new `agents.Existing(id)`,
    which returns the live session or an error **atomically**: there's no check-then-act window, and it never spawns
    `claude`.
  - Tests in `SBD/terminal/broadcaster_test.go`: written bytes appear in `Screen()`; resize follows the PTY; a
    client-less broadcaster gets the fixed size and a nudge; `Screen()` races cleanly with `readLoop` under `-race`;
    a slow parser never blocks fan-out; overflow marks the screen stale, then reset + nudge recover it; an uncontrolled
    sandbox keeps no screen; `EnableScreen` on an existing broadcaster; `Screen`/`BroadcasterExisting` never spawn,
    including when the agent exits mid-call.
- [X] T006 [P] Add the R1 screen fixtures under `SBD/agent/testdata/claudeui/`, as plain text plus a faint-mask
  sidecar:
  - `startup-placeholder`, `empty-after-command`;
  - `model-set-opus`, `model-set-fable`, `model-set-sonnet`, `model-set-haiku`, `model-not-found`;
  - `effort-set-low|medium|high|xhigh`, `effort-max-session`, `effort-auto`, `effort-invalid`;
  - `clear-banner`, `model-picker`, `auto-mode-dialog`;
  - `text-left-in-box`, `typing-slash-autocomplete`;
  - `framed-paste-in-box` (the multi-line action prompt as pasted), `long-prompt-wrapped`.

  Capture them with a small `go test -run TestCaptureFixtures` helper behind an env var (`SXB_CAPTURE_CLAUDE=1`).
  It drives a local `claude` in a PTY the way research R1 did. It is skipped by default and never run in CI.
  - The helper also records, for R1's open items, a real skill turn's `UserPromptSubmit` latency, and whether the
    `Notification` hook carries `notification_type`. Record the numbers in research.md R1. If the latency exceeds
    half of `submitWindow`, raise `submitWindow`.
  - It also checks that `$PPID` in a hook command is the `claude` process that fired the hook (research R13). If it
    isn't, record which ancestor is, and switch T046 to the PTY-device fallback.
  - **Back up and restore `~/.claude/settings.json` around it**: `/model` and `/effort` save defaults there.
- [X] T007 Implement `SBD/agent/claudeui.go` as pure functions over `[]termview.Line`:
  - `InputBox(lines) (text string, present, ready bool)`. The box is every row between the last two horizontal
    rules, starting at `❯ `; wrapped rows are joined. Faint text after `❯ ` counts as empty. `ready` is false while
    any modal marker is on screen: `Esc to cancel`, `Enter to select`, `Enter to set as default`, a `Select …`
    header with numbered `❯ n.` rows, or the startup dialogs' option rows.
  - `StepInBox(text, stepText) bool`: a prefix match after trimming, or a paste placeholder `[Pasted text #N …]`.
  - `ModalOpen(lines) bool`.
  - `ConfirmationAfter(lines, stepText) (string, bool)`: the `⎿` line under the last echoed `❯ <stepText>`.
  - `ModelConfirmed(line, family) bool`, `EffortConfirmed(line, level) bool`.

  Tests in `SBD/agent/claudeui_test.go`, table-driven over every T006 fixture, include the negative cases: the
  placeholder reads empty; autocomplete is not a modal; the picker is not ready; `Model 'bogus' not found` is not
  confirmed; the framed paste and the wrapped prompt read as the step still in the box.

### Hook evidence, hold release and typing (research R3, R4, R7)

- [X] T008 In `SBD/agent/hooks.go`:
  - add `type HookEvent{Event, Source, NotificationType string; BackgroundTasks int; At time.Time}` and
    `func (s *HookServer) Tap(sandboxID string) (<-chan HookEvent, func())`, a buffered, best-effort fan-out after
    `dispatch` applies the status;
  - record the evidence through the status updater, from **main-session** events only (T046):
    - `UserPromptSubmit` → `last_submit_at`;
    - `Stop` with no background tasks, and `backstopExpired` → `last_turn_end_at`;
    - a needs-input `Notification` → `last_needs_input_at`;
    - `SessionStart` (not `compact`) → `last_session_start_at`/`last_session_source`;
  - **end a hold**: on `SessionStart` with source `startup` or `clear`, if the sandbox's `controller.run.hold ==
    NEEDS_DEVELOPER` **and the event is corroborated** (T046), clear `controller.run` (FR-216, research R7);
  - sample `input_idle_since` on each hook and every 5 s while `controller.run` is set: box empty and ready, agent
    not WORKING (the idle-nudge fallback). Write it **only when it changes** (set or unset).

  Extend the `StatusUpdater` interface accordingly. Tests in `SBD/agent/hooks_test.go`: tap order; evidence per
  event; the hold ends on a corroborated `startup` and `clear` but not on `resume` or `compact`; no hold → nothing
  cleared; `input_idle_since` set and unset, and repeated samples in the same state write nothing.
- [X] T009 In `SBD/sandbox/manager.go`:
  - add `SetAgentEvidence(id, func(*pb.AgentSession))`, which writes the fields and emits once, beside
    `SetAgentStatus`;
  - add `ClearHeldRun(id)` for T008's hold release: a CAS that clears only while the hold is still NEEDS_DEVELOPER;
  - in `SBD/worker/controller.go` `Update`, honour `expect_run_id`: with `set_run`, for a **set or a clear**, apply only
    while `controller.run.id == req.expect_run_id`, never re-creating a cleared run (FAILED_PRECONDITION `run
    moved`);
  - make `leaveRunning` keep `controller.run` when it has any `hold` or `step_role == CLEANUP`, and set its `hold =
    NEEDS_DEVELOPER` (data-model.md WorkerRun);
  - **agent exit during cleanup**: in `SetAgentStatus`, on EXITED while the run **still has cleanup to run**
    (`step_role == CLEANUP` or `hold != NONE`), set `hold = NEEDS_DEVELOPER` and `hold_gen` (T046). No hook reports
    an exit; it arrives from `agent.Registry.onExit`. A run with nothing left to clean gets no hold;
  - in the controller `Update`, never lower a `NEEDS_DEVELOPER` hold: a set that would change it to anything but a
    clear is refused.

  Tests in `SBD/sandbox/manager_test.go`: stop during CLEANING_UP and NEEDS_INPUT_FREEING keeps the run as
  NEEDS_DEVELOPER; stop during SETUP/ACTION clears it as today; an exit during cleanup followed at once by a
  corroborated `startup` ends the hold; an update can't lower NEEDS_DEVELOPER; an `expect_run_id` mismatch, for set or
  clear, changes nothing.
- [X] T010 In `SBD/agent/pty.go`, add `TypeStep(sandboxID string, spec *pb.AgentSpec, text string) (sentAt time.Time,
  err error)`: one bracketed paste, `\r` after `promptSubmitDelay`, and **no** `ensureSubmitted`. Also add
  `RePress(sandboxID string, n int) error`, which sends `\r` for n = 0 and keypad Enter after that, as
  `ensureSubmitted` does today. Tests in `SBD/agent/pty_test.go` with the fake PTY.

### The step RPC (contracts/worker-step.md)

- [X] T011 Implement `SBD/worker/step.go` `func (s *Service) DeliverStep(ctx, req *pb.DeliverWorkerStepRequest)
  (*pb.DeliverWorkerStepResponse, error)`:
  1. **Read the screen without creating anything** (`terms.Screen`). Create a broadcaster only through
     `terms.BroadcasterExisting` (provider `agents.Existing`, atomic), so the step path never spawns an agent (except
     guard 5's explicit `first` start). Call `EnableScreen` if the broadcaster has none.
  2. **Guard**, checks 1–6 in one `reg.Update`, **committing nothing unless every check passes**:
     - `first` accepts no run, or the same `run_id` (a retried first step), and records `WorkerRun{id, action,
       step_role}`; otherwise the run id must match and `step_role` is updated;
     - check 3a: refuse `ALREADY_EXISTS` unless `step_index` is **greater** than the recorded one, or equal with
       `retry` while `controller.run.halted` (or a retried CLEAR under a NEEDS_DEVELOPER hold); refuse other CLEANUP
       steps under NEEDS_DEVELOPER;
     - only after checks 4–6 pass, write `step_index`, `step_sent_at` and `halted = false` (and the run, for
       `first`);
     - with `first`, no live agent and no hold, start one as SWI-15's `Deliver` does (`startAgent`, through `Registry.Session`,
       whose orphan gate refuses beside an orphan, T048) and refuse `agent is starting`. In every other case with no live agent, refuse `no live
       agent` and start nothing;
     - for MODEL/EFFORT, refuse `INVALID_ARGUMENT` **before** committing if `expect` is outside the fixed sets.
  3. Open the hook tap, count the step's echoes on screen (`before`), then `TypeStep`. The text is derived from
     `kind` + `expect` for MODEL/EFFORT/CLEAR (ignoring `step.text`), `Frame(...)` for `ACTION_PROMPT`, and the bare
     text for CUSTOM. The daemon doesn't filter text: untrusted values were checked before substitution (T044).
     On a type error, roll back the guard's record (the run for `first`, else `step_index`), as `Deliver` does.
  4. Run the **submit check** loop: tap events from the main session only (T046) plus a 50 ms screen poll, re-presses
     only while `StepInBox` (max 3), `modalGrace` (1 s, capped at 2 s) for a needs-input report when a modal
     appears, a stale screen treated as unknown (window extended, ≤ 1 s in total, never a halt), and the read-back
     per kind against an echo **added after `before`**. Return the outcome, `sent_at` and `submit_at`.
  5. `check_only` runs the read-back without guard 3–6 or typing, for MODEL/EFFORT/CLEAR only; any other kind, or no
     live agent, is NOT_ACCEPTED.

  The submit window and re-presses are injectable for tests. Inject the hook tap, the screen source and the clock
  through small interfaces on `Service`.
- [X] T012 Tests in `SBD/worker/step_test.go`, one per row of contracts/worker-step.md §1 and §3, using a fake tap
  and a scripted screen built from the T006 fixtures:
  - MODEL_TURN on `UserPromptSubmit`;
  - INPUT_ONLY on a needs-input `Notification`;
  - `SessionStart` is not a turn start;
  - a picker gives NOT_ACCEPTED with no Enter after `modalGrace`; a skill dialog followed within the grace by a
    needs-input report gives INPUT_ONLY;
  - an older identical echo on screen doesn't pass the read-back; only a new one does;
  - built-in text is derived from `kind` + `expect`, whatever `step.text` says;
  - a lower or equal `step_index` without `retry` is refused, and a gap after a skip is accepted;
  - no live agent: the factory is never called, and only `first` on an unheld sandbox starts one;
  - text in the box is re-pressed 3× and then NOT_ACCEPTED, including the framed paste placeholder;
  - AGENT_LOCAL with a model/effort read-back pass and fail, and Clear with and without `SessionStart clear`;
  - each guard refusal types nothing;
  - a repeated `step_index` gives `ALREADY_EXISTS` and types nothing; with `retry` on a halted run it types once,
    and a second `retry` is refused;
  - a refusal at check 6, then a retry, types exactly once; a refused `retry` stays retryable;
  - a custom CLEANUP step under NEEDS_DEVELOPER is refused; a retried Clear under it is accepted;
  - a retried `first` for the same run is accepted; for another run it is refused;
  - `first` with no live agent starts one and refuses;
  - a rollback on a type error;
  - `check_only`;
  - the 10 s bound.
- [X] T013 Wire `DeliverWorkerStep` in `SBD/grpc/worker_rpcs.go` (same auth and attribution as
  `DeliverWorkerPrompt`). Add it to the `worker.API` interface, `worker.Local` and `remoteAPI`
  (`SBD/worker/api.go`, `SBD/worker/local.go`).
  - An `UNIMPLEMENTED` from a remote daemon maps to `worker.ErrUnsupported`. An `ALREADY_EXISTS` maps to
    `worker.ErrStepSent`; the orchestrator reads the time from `controller.run.step_sent_at` in the sandbox record,
    since a gRPC status carries no timestamp.
  - Tests in `SBD/grpc/worker_rpcs_test.go`.

### Action sequence model (research R9)

- [X] T014 In `SBD/orchestrator/actions.go`, add:
  - `effectiveSequence(a *pb.Action) *pb.ActionSequence` (absent = Opus, medium, Clear, empty lists; FR-210);
  - `buildSteps(a, args) []*pb.RunStep`, in FR-205 order, with `expect` set for Model/Effort and the text from
    `composeStep`;
  - `composeStep(text, a, args)`: the same placeholder fill as `compose`, with nothing appended (FR-207).

  Extend `validateAction` with the FR-204 rules for `pre_prompts`/`post_prompts`:
  - trim each prompt;
  - refuse an empty one, more than `maxStepPrompts` (10, in `SBD/orchestrator/constants.go`), or one over
    `maxArgumentBytes`;
  - refuse an undeclared placeholder;
  - refuse a placeholder in the first whitespace-delimited word;
  - refuse any placeholder when the prompt starts with `!`;
  - refuse UNSPECIFIED enums.

  Errors name the entry (`pre-prompt 2: …`). `normalizeAction` stores the trimmed text.
- [X] T015 Tests in `SBD/orchestrator/actions_test.go`, covering every FR-204 rule and every edge case in the spec's
  list:
  - `{skill} for {ticket}`, `{cmd}`, `/{skill}`, `!git checkout {branch}`, `  !echo {x}` after trimming, and an 11th
    entry;
  - `effectiveSequence` on a legacy action;
  - `buildSteps` order for all built-ins on and all off (no steps);
  - placeholder fill with defaults and with a missing optional argument;
  - an argument referenced only by a pre-prompt is still appended to the action prompt (FR-207).
- [X] T016 In `SBD/orchestrator/runs.go` `callAction`, snapshot `Run.steps = buildSteps(...)` next to `Run.prompt`.
  The `ACTION_PROMPT` step's text is the composed prompt. Test: a later `PutAction` doesn't change a queued run's
  steps (FR-207).
- [X] T017 Add `workerRunFor(run, live *pb.WorkerRun) *pb.WorkerRun` in `SBD/orchestrator/runs.go`. It copies
  **every** field from the live record and overlays the orchestrator's (`needs_review`, `hold`, `halted`). Route
  every existing `WorkerRun` write through it: `deliver`'s local mirror, `syncReviewFlag`, `repairReviewFlag`. Every
  write to an existing run sends `expect_run_id`.
  Tests: a review-flag sync keeps `hold`, `halted`, `step_role` and `step_index`; a late write after the daemon
  released the hold doesn't re-create the run.

### Security and restart recovery (research R13, R14)

- [X] T044 [P] **Untrusted values (R13 §1).** Add `checkValue(v string) (string, error)` in
  `SBD/orchestrator/values.go`; the orchestrator is its only user, so there's no import cycle. It:
  1. normalises CRLF and lone CR to LF;
  2. refuses invalid UTF-8;
  3. refuses ESC (0x1B), other C0 controls except `\n`/`\t`, DEL (0x7F) and C1 (U+0080–U+009F).

  Use it **only on substituted values**: in `validateCall` on every argument value (storing the normalised value),
  on argument defaults in `validateAction`, and again in `compose`/`composeStep` just before substitution (a
  failure there fails the run before anything is typed). Authored text, `TypeStep` and `Registry.Prompt`
  (escape-hatch results) are **not** filtered.

  Tests:
  - `ESC[201~\r!id\r` in a call value is refused;
  - a CRLF value is accepted as LF;
  - an invalid-UTF-8 0x9B byte is refused;
  - an action body containing ANSI is saved unchanged;
  - an escape-hatch result with ANSI output is still delivered.
- [ ] T045 **Per-agent hook token (R13 §2).** In `SBD/agent/pty.go`, `SBD/agent/hooks.go`,
  `SBD/escapehatch/inject.go` and the daemon store:
  - **Generation and token.** `agent.Registry` numbers each PTY it spawns per sandbox from a counter **persisted** in
    the daemon store (it only grows, across restarts too), and draws a random `hookTokenBytes` token for it. The
    store keeps `{generation → token}` per sandbox, never on `pb.Sandbox`.
  - **Passed in the environment, not argv.** The PTY factory sets `SXB_HOOK_TOKEN` and `SXB_AGENT_GEN` in the `sbx
    exec` client's environment and passes them by name (`sbx exec -e SXB_HOOK_TOKEN -e SXB_AGENT_GEN …`), so they are
    set before the first hook, inherited by every child, and absent from the host process list. The `-e` flag is
    documentation-derived: pin it with an argv-asserting test and reconcile it first; only without it fall back to an
    `export` in the launch string. No log line includes the launch string or the environment. No file is written,
    so nothing races the spawn.
  - **Settings carry no secret.** `BuildSettings`' hook command sends `-H "X-Sxb-Token: $SXB_HOOK_TOKEN" -H
    "X-Sxb-Gen: $SXB_AGENT_GEN" -H "X-Sxb-Pid: $PPID"` (T046), so
    `RefreshHooks` rewriting it changes no agent's token. Every hook, `UserPromptSubmit` and `PreToolUse` included,
    now forwards its input (`--data-binary @-`).
  - **Checking it.** `Handle` looks up `X-Sxb-Gen` in an **in-memory** map `{sandbox → {generation → token}}` (loaded
    from the store at start, updated on spawn and reap; no store read per hook) and compares that one token in
    constant time; a match gives the event's generation. A token the sandbox doesn't have gets 403, and the event is dropped. **No** token
    (an agent launched before this feature) is accepted for status display only, never for evidence, holds or steps.
    A generation's token is retired only once its agent is **known** gone (T047): a probe that finds it gone, an
    operation whose outcome is STOPPED or destroyed, or the developer stopping the orphan. An exec-client exit alone
    never retires it.
  - **Main PID persisted.** The store keeps `{generation → token, main PID, start time}`. The main PID (T046) is
    written when it is bound and loaded at start. A probe's `agent.pid` of the same generation fills it **only when
    none is bound**, and only after checking that `/proc/<pid>/exe` is the agent binary, its command line starts
    with the fixed flags, and its parent matches the probe shell's own parent: both ppid 0, or both non-zero with the
    same `/proc/<ppid>/exe` that isn't a shell, `script`, `tmux` or the agent binary. If T006 finds an intermediate
    shell, the comparison applies to the grandparent. Don't hard-code ppid 0 (research R13). The file's tty is only a consistency check, as it is
    attacker-writable. Otherwise the bind is refused and logged. A mismatch is logged, and the stored PID stays (T047).
  - **Reading the body.** It is parsed as a JSON token stream that keeps only the needed fields and skips large
    values without buffering them, under a 16 MiB hard cap.
  - **The escape hatch.** `/escape-hatch/run` requires a live generation's token. `writeWrapper`'s `curl` sends the
    same header from `$SXB_HOOK_TOKEN`, and wrappers are re-injected at daemon start alongside `RefreshHooks`. A
    tokenless call gets 403 `no agent token in this environment; run the escape hatch from the agent's session`.
    Document in the escape-hatch docs (T041) and quickstart that only the agent's own process tree can run hatch
    commands.

  Tests:
  - a wrong token, or another sandbox's token, is dropped;
  - a tokenless call updates status but not evidence;
  - the values are set only in that command's `cmd.Env`, never with `os.Setenv`: a host escape-hatch command and an
    ON_HOST service started after a spawn have no `SXB_HOOK_TOKEN` in their environment;
  - the `sbx exec` argv passes `-e SXB_HOOK_TOKEN -e SXB_AGENT_GEN` and contains no token value; the first hook of a
    new agent is accepted; a hook with a wrong `X-Sxb-Gen` is dropped;
  - a surviving agent's token still works after `RefreshHooks` and a daemon restart, and is refused after its reap;
  - an orphan whose exec client was killed (SIGKILL) keeps a working token: its next hook is accepted and counts as
    main. Any client exit keeps the token until its probe; a gone probe retires it;
  - restart the daemon mid-action: the orphan's `Stop` hook updates `last_turn_end_at` (the main PID came from the
    store); with no stored main PID, the start-up probe's `agent.pid` binds it; an `agent.pid` rewritten to name
    another live process doesn't change an existing binding (the mismatch is logged); with no stored main PID, an
    `agent.pid` naming a `sleep` process (wrong exe), or a `claude --dangerously-skip-permissions` started from
    inside the sandbox (`script -qc …`, its parent `script`, with the file's tty rewritten to match), is not bound; the bind succeeds
    with the fake runtime in both reference shapes (probe parent 0, and probe parent a non-shell exec agent);
  - the generation counter after a restart is greater than any recorded `hold_gen`;
  - a generated wrapper's call carries the token and runs; a tokenless call to `/escape-hatch/run` gets 403;
  - a 5 MiB PreToolUse body is read and its `session_id` found;
  - `ListSandboxes` output contains no token.
- [ ] T046 **Corroboration and the main session (R13 §2).** In `SBD/agent/hooks.go`, `SBD/agent/pty.go`,
  `SBD/worker/controller.go` and `SBD/worker/deliver.go`:
  - **`hold_gen`.** Written by the daemon only on the **transition into** NEEDS_DEVELOPER (controller `Update`,
    `SetAgentStatus`, `leaveRunning`, T047), with the current generation. The controller `Update` always keeps the
    stored value and ignores the request's, and a write that keeps an existing NEEDS_DEVELOPER hold doesn't
    re-record it.
  - **Main agent by process.** The hook command sends `X-Sxb-Pid: $PPID` (or, per T006, the PTY-device fallback).
    Bind the generation's **main PID** from its first non-`compact` SessionStart (any source) carrying the current
    token, persist it with the token (T045), and let a probe's `agent.pid` of the same generation fill it only when none is bound (T047). An event is main when its token and PID match.
    Until the PID is bound, events count for status only.
  - The tap, the evidence fields and hold release use main-agent events only.
  - **`startup`** ends a hold only from a generation greater than `hold_gen`, spawned by `AttachAgent` (including
    `t` → Replace on an orphan, T047) or guard 5.
  - **Session id follows the main process.** A `clear` or `resume` SessionStart **from the main PID** updates the
    recorded `session_id`; one from any other PID (a nested `claude -c -p`, `claude -p --resume`) never does.
    `resume` never ends a hold.
  - **`clear`** ends a hold, or passes Clear's read-back, only with a new `session_id` and the post-clear screen,
    polled for up to `clearSettle` (1 s) after the hook.
  - **No spawn while held.** `Registry.Prompt` and `EnsureDrained` refuse to spawn while a hold is set (`sandbox is
    held; open its terminal`).

  Tests:
  - a forged `clear` with the old `session_id`, or with the conversation still on screen after `clearSettle`, ends
    nothing; a real `clear` whose redraw lands 300 ms after the hook ends the hold;
  - a `startup` from the same generation, or from a `claude -p` nested in it, ends nothing;
  - a SessionStart from a process left by the previous generation doesn't bind the main PID;
  - a developer's `/clear` while the screen is stale still updates the session id;
  - a developer's `/resume` mid-run updates the session id, and the next turn's `Stop` still finishes the step; the
    `resume` ends no hold;
  - a nested `claude -c -p` (SessionStart `resume`, current token, another PID) changes nothing, and the main
    agent's next `Stop` still finishes the step;
  - an orchestrator write with `hold_gen = 0` (a stale view) and a re-sent NEEDS_DEVELOPER (a `step_note` mirror)
    both leave the stored `hold_gen` unchanged;
  - an escape-hatch result to a held sandbox with no live agent spawns nothing;
  - a nested session's `UserPromptSubmit` doesn't classify a step;
  - a `--continue` agent (first SessionStart `resume`) binds its session.
- [ ] T047 **Owning-daemon restart (R14).** The agent **survives** a daemon restart (`agentCommand`), so this task
  handles it as an orphan, decided by **evidence**, never by state flags. The daemon **detects and surfaces**
  orphans and **never stops one on its own** (simplified in run-590):
  - **Record generation, PID, start time and PTY.** In `SBD/agent/pty.go` `agentCommand`: `mkdir -p ~/.sxb; echo
    "<gen> $$ $(cut -d' ' -f22 /proc/$$/stat) $(tty)" > ~/.sxb/agent.pid; … exec claude …`.
  - **Probe** (`SBD/agent/orphan.go`): one read-only `sbx exec <ref> sh -c …`.
    - It reports *alive* `{gen, pid, start}` only if `/proc/<pid>/stat` has the recorded start time.
    - With no PID file, or one that doesn't parse, it reports **every** process whose executable is the agent
      binary and whose command line starts with the fixed flags (`claude --dangerously-skip-permissions`), excluding
      those whose parent is such a process.
    - **Orphan** = alive and no live session of that generation.
    - It binds the generation's main PID from `agent.pid` only when none is bound and the exe/command-line/exec-parent
      check passes (T045/T046).
    - It writes `AgentSession.agent_check` (CLEAR / UNKNOWN / DETACHED / CHECK_FAILED, last status, last-seen
      time, error, process count) only when it changes.
  - **`runtimeMu`** (`SBD/sandbox/manager.go`): a per-sandbox mutex, separate from `m.lifecycle` and the ops latch.
    - **Who holds it.** These hold it from just before their runtime call until the resulting state is written:
      `Stop`, `Destroy`, `Refresh` (**continuously** from its stop through its re-seed and bring-up), `AddKit` (the
      recreate) and `Restart` (the bring-up).
      `Rename`, source edits and MCP attach don't take it.
    - **Probes and `ReapOrphan`.** Each runs its `sbx exec` under it with a **try-lock**, after re-reading that the
      record is RUNNING and that `ListStatuses` lists the sandbox as running. A probe that finds it held is dropped. A
      developer's Replace or restart that finds it held is refused with `ErrBusy`, which names the operation
      (`sandbox is stopping` / `restarting` / `being refreshed` / `being recreated for a kit` / `being destroyed`).
    - **Check and spawn hold it together.** `Registry.Session` try-locks `runtimeMu` before the gate check and holds
      it until the factory's `cmd.Start` returns and the session is registered (T048). The try-lock never waits, so
      there is no lock cycle with `r.mu`.
    - **A stop drains exec clients first, best effort** (research R14, simplified in run-629). `Stop` (and the stops
      inside `Destroy` and `Refresh`), under the lock and before `sbx stop`: closes the sandbox's agent session and
      broadcaster with `closing` set and sends its client TERM, waiting on the watcher's `done` channel (closed by
      `onExit`), **never a second `cmd.Wait`** (`Registry.Close` returns it, and the drain waits only after `Close`
      has released `r.mu` and the global lock); reads `(ref, pgid)` from every still-starting service instance's
      handle **before** recording it FAILED through `markFailed` (whose `release` deletes the handle)
      (`LAUNCH_FAILED`, `start interrupted by a sandbox stop; if the stop failed it may still be running in the
      sandbox`), which cancels its context and unpublishes its port; sends each collected in-sandbox group TERM
      through `sbx exec`; waits **once**, up to `orphanKillWait` in total, then sends KILL to the agent client if
      still alive and to every collected group unconditionally (TERM-all → one wait → KILL-all); leaves RUNNING
      services alone (`gone(h)` reads their clients); then runs `sbx stop`. Each step is bounded, and anything still
      pending is logged and abandoned. No ordering against `sbx stop` and nothing about a failed stop is promised.
      `bringUp` defers to whoever cancelled it: after cancellation it skips `stopProc`, and its writes apply only
      while the instance is STARTING (checked inside the `insts.Update` mutate); a skipped write is not published
      (no event, no notification). There is **no exec guard** (research R14, simplified in run-613): only developer
      requests are gated. `bringUp`'s IN_SANDBOX launch and `Supervisor.Stop` try-lock `runtimeMu` like
      `Registry.Session` (a held lock → `sandbox is busy; try again`). Execs from the lock holder or its on-change
      hook (`Restart`, probe/reap, `StopAll` teardown on CREATING/ERROR) are never gated. A failed `sbx stop`
      afterwards surfaces its error and leaves the sandbox RUNNING and *unknown*, so the outcome rule probes it;
      RUNNING services stay RUNNING and tracked, and the interrupted ones are FAILED with the *may still be running*
      detail, never claimed stopped.
    - **Lifecycle operations block** on it, which waits out an in-flight probe or stop.
    - The agent session is closed by the on-change hook as today, except for the stop's drain above.
    - **Outcome rule**, applied as the operation releases the lock, from the state it wrote:
      - STOPPED or destroyed → drop the orphan record, clear *unknown*, retire the token, and EXITED by the gone rule;
      - RUNNING with an orphan record or *unknown* → mark *unknown* and probe once;
      - anything else → keep the record and *unknown*.

      `Refresh` applies the STOPPED branch explicitly after its stop's STOPPED write, still under the lock. The
      STOPPED/destroyed branch also runs from the on-change hook, **only** on a confirmed stop or destroy: a published
      STOPPED, or the DESTROYING emitted after the record is deleted. That covers external stops seen by `Announce`.
      CREATING and ERROR never clear the record, *unknown* or the token, because the container may still be up (AddKit's
      pre-recreate CREATING, Refresh's failed-stop CREATING, `m.fail`'s ERROR). When the hook's teardown closes a
      live agent session on one of them, it marks the sandbox *unknown*. `Refresh` after a failed stop writes STOPPED
      only when `ListStatuses` succeeds and doesn't list the sandbox; a listing error writes CREATING (change
      `listedRunning`'s error case for this caller).
  - **Probe triggers** (research R14), each on RUNNING sandboxes only, and none of them stops anything:
    - **daemon start** (clear by default): list running sandboxes (`ListStatuses`). Sandboxes a **successful**
      listing reports stopped or absent get agent status EXITED and their unretired tokens retired, and are never
      probed. If the listing **errors**, or reports an unrecognised status for a sandbox, nothing is retired and no
      EXITED is written for those sandboxes; each may-have-agent sandbox is marked *unknown*, and the listing is
      retried with its probes. A running sandbox is *unknown* only if it has an unretired generation
      token, or (first start after the upgrade, no store yet) a set, non-EXITED agent status; every other running
      sandbox is CLEAR at once and dispatchable. Mark those *unknown* synchronously, start the servers, then probe
      asynchronously in priority order (workers with no run, then workers with a run, then plain sandboxes) with
      `orphanProbeConcurrency` (8), each bounded by `orphanProbeTimeout` (10 s). A gone result clears the sandbox
      and re-kicks WarmStart and dispatch;
    - **exec-client exit**: `Registry.onExit` for a session that isn't `closing`. It **always** marks the sandbox
      *unknown*, releases `r.mu`/`statusMu`, and probes **asynchronously**. The exit status is not used. The result
      is applied under the locks: alive → orphan record, run state kept; gone → EXITED;
    - **gate refusal**: each refusal schedules a re-probe, debounced to one per sandbox per 10 s;
    - a main-agent **`SessionEnd`** from the orphan's generation;
    - **backstop**: every `orphanBackstop` (60 s), while an orphan record **or an *unknown* sandbox** holds a run.

    On a **gone** probe: drop the record, retire the token, and write EXITED only when the recorded status is set and
    not EXITED.

    **Probe failures (*unknown* is bounded).** A failed probe leaves the sandbox *unknown*, retried with backoff
    capped at 60 s. After `orphanUnknownLimit` (60 s, counted from the sandbox's first probe attempt, not from being queued) or 3
    failures in a row, whichever comes first, surface it once:
    `agent_check` = CHECK_FAILED, emit `NOTIFICATION_KIND_AGENT_CHECK_FAILED`, set the row's note to `can't check for
    a detached agent: <error>`, and, if it holds a run, set `controller.run.step_note` to `can't confirm the agent is
    still running: <error> — t to Wait or Restart sandbox (fails the run)`. The run is not failed automatically. The
    note is an **overlay**: keep the previous `step_note` and restore it in the same update that leaves CHECK_FAILED
    (clear → previous note; DETACHED → the detached note; gone → previous note, in the update that writes EXITED). Any
    other `step_note` write while the overlay shows updates the saved note instead, and the overlay stays. At
    daemon start, drop a leftover check-failed note (fixed prefix) while marking the sandbox *unknown*.
    The TUI then offers **Restart sandbox** or **Wait**. There is no unchecked stop.
  - **Surfacing.** When a probe first creates an orphan record, emit `NOTIFICATION_KIND_AGENT_DETACHED` (sandbox,
    last state, run), once per record. The records found by one daemon start's first-pass probes are aggregated into
    one notification (`N agents detached by a daemon restart (M on idle workers) — R on the Workers view replaces
    the idle ones`), with empty `sandbox_id` and the daemon's `host_id`. It is sent once every first-pass probe has
    returned once, or `orphanUnknownLimit` after the first probe started, whichever is first, and never with N = 0.
    Orphans found after that, including by retries, get individual notifications.
    Set the worker row's note to `agent detached by a daemon restart — t to
    Replace or Wait`.
  - **Phases.** For each orphan whose sandbox has a `controller.run` with a `step_role` or `hold`:
    - **ACTION:** left as is (in flight; hooks and the MCP report still work). At close, the cleanup steps are
      dropped, and the hold is NEEDS_DEVELOPER with the note `agent detached by a daemon restart; t → Replace frees
      the worker` (+ `custom post-prompts were dropped` when there were any);
    - **SETUP**, or an action step not yet accepted: marked so the orchestrator halts the step with the note `agent
      detached by a daemon restart; last seen <state> <time>; R restarts setup with a fresh agent` (and
      `sent_before_restart` when it was sent). `X` fails the run and leaves the hold NEEDS_DEVELOPER;
    - **CLEANUP or any hold:** `hold = NEEDS_DEVELOPER` with `hold_gen`;
    - **a worker with no run:** nothing changes on the record. Delivery is refused by the gate (FAILED_PRECONDITION),
      and dispatch skips the worker until the developer replaces the agent.
  - **`ReapOrphan(ctx, id, mode)`**, the developer's stop:
    - RUNNING sandboxes only, under `runtimeMu` (try-lock).
    - In one script: re-probe with the start-time and binary checks, TERM **each** live matched process, then KILL
      after `orphanKillWait`.
    - A failed exec stops and spawns nothing and returns its error.
    - Then retire the generation's token, drop the orphan record, log the stop and write EXITED.
    - For a replaced ACTION run, write the reason `agent replaced by the developer after a daemon restart` into
      `controller.run.step_note` in the same update.
    - For `mode = restart`, write no EXITED (the fresh agent's STARTING follows).
  - **Callers that may stop an orphan**, and no others:
    - `AttachAgent` with `AttachInfo.replace_orphan`, on any sandbox;
    - `DeliverWorkerStep.restart` (T024);
    - `ReplaceDetachedAgent` (new RPC, the TUI's bulk `R`): on a DETACHED sandbox that holds no run and whose
      `agent_check.last_status` is IDLE (otherwise FAILED_PRECONDITION), `ReapOrphan`
      under the try-lock, then `agent_check` CLEAR, EXITED and WarmStart. Errors per contracts/proto-delta.md.

    The daemon itself never calls `ReapOrphan`. A guard test fails if a call site appears outside these three.
  - **Orchestrator** (`SBD/orchestrator/sequence.go`): a run failed by EXITED uses `controller.run.step_note` as its
    reason when set. It never uses a check-failed note (fixed prefix) as a reason; it uses `agent exited` instead.
    A run failed by a replace sends **no** custom post-prompts and no Clear.

  Tests:
  - in `SBD/sandbox/manager_test.go`:
    - each phase at daemon start;
    - a stopped sandbox is never probed (no `sbx exec`), and its status becomes EXITED;
    - a recorded non-EXITED status with a dead PID, or a reused PID (start time differs), is **not** an orphan;
    - the servers start before the probes finish, and the gate refuses every *unknown* sandbox meanwhile; probes run
      at most 8 at a time; a probe timeout leaves the sandbox refused and re-probed; after 60 s or 3 failures `t` offers
      *Restart sandbox* and *Wait*;
    - **ordering, with a fake runner that records argv order and a blocking fake prober**, for each of `Stop`,
      `Refresh`, `Destroy`, `AddKit` and `Restart`:
      - a probe in flight when the operation starts finishes before its runtime call;
      - a probe or Replace triggered during the operation runs no `sbx exec` (dropped or refused `sandbox is busy`);
      - no `sbx exec` follows `sbx stop`;
      - the gate refuses every spawn path while the lock is held, with the operation's `ErrBusy` reason;
      - the gate passes, then `Stop` starts while the factory is blocked: `Stop` waits for `cmd.Start` and the
        registration, so its stop ends the new agent; no agent survives the STOPPED write;
      - with a fake exec client that "reaches the runtime" (marks the fake sandbox running) only after a delay, and
        that ignores TERM: `Stop` sends it TERM then KILL and reaps it within the bound before `sbx stop` (best
        effort: no assertion about a client that outlives the bound);
      - a `sbx stop` that fails after the drain leaves the sandbox RUNNING and *unknown*; its probe makes an orphan
        record (surfaced), and no second agent spawns;
      - a service in its readiness loop during `Stop`: the loop is cancelled and its `bringUp` issues no further
        `sbx exec` and writes nothing; a service start or a developer service stop attempted
        while the stop holds the lock is refused (`sandbox is busy`), and the start keeps no port;
      - `AddKit` (CREATING under the lock) with a running IN_SANDBOX service: the hook's teardown signals it through
        `sbx exec`, which is not refused; the same holds for a Refresh whose stop fails;
      - a `Stop` with one RUNNING IN_SANDBOX service and one whose PGID has arrived but whose readiness takes 30 s:
        the second is FAILED (`start interrupted by a sandbox stop; …`) with its port unpublished, and the fake
        `sbx exec` sees `kill -TERM -<pgid>` and then `kill -KILL -<pgid>` even though its host client has already
        exited; a fake readiness that succeeds as it is cancelled leaves it FAILED, not RUNNING, with exactly one
        `SERVICE_FAILED` notification. With three starting services the drain takes one `orphanKillWait`, not three.
        If `sbx stop` then fails, the RUNNING one is
        still RUNNING, tracked and stoppable by `p` → stop, and the error is returned; if it succeeds, the hook
        records it STOPPED. A fake client that survives KILL is logged and doesn't block the stop;
      - under `-race`, a fake agent client that exits only on KILL: the drain waits outside `r.mu` (`onExit` runs),
        and the stop completes after `orphanKillWait`;
      - the drain records `sbx stop` only after the fake agent client's `onExit` has run (no second `Wait`);
    - an external stop seen by `Announce` drops the orphan record and retires the token through the on-change hook;
      **with an orphan present**, an `AddKit` whose recreate fails (CREATING then ERROR, container still listed), a
      `Refresh` whose stop fails with the container still listed (CREATING), and a `Refresh` whose stop fails while
      `ListStatuses` errors each keep the record, *unknown* and the token, and nothing spawns at the next RUNNING;
      at daemon start, a `ListStatuses` error (and, separately, a `starting` status) with a surviving agent retires
      no token, writes no EXITED, leaves the sandbox *unknown*, and spawns nothing once the listing recovers until its
      probe has run
      until a probe finds the agent gone. A hook teardown of a live session on CREATING/ERROR marks the sandbox
      *unknown*. A sandbox stopped at daemon start has its token retired;
      `Refresh` with an orphan drops it and retires the token before its bring-up;
    - daemon start: an idle worker with no unretired token is CLEAR and receives a run with no probe; probes run in
      priority order; one aggregated AGENT_DETACHED (empty `sandbox_id`) is emitted after the first pass, with a probe hung past
      the limit not delaying it; an orphan found by a retry after it gets an individual one; `ReplaceDetachedAgent` on two idle DETACHED
      workers replaces both, refuses one holding a run, refuses one last seen WORKING, and reports a busy one;
    - 20 may-have-agent sandboxes at start with probes that each take 9 s: none becomes CHECK_FAILED, because the
      clock starts at each one's first attempt;
    - an *unknown* sandbox holding a run whose probe keeps failing: the backstop keeps probing, and within 60 s it is
      CHECK_FAILED with AGENT_CHECK_FAILED emitted once and the run's step note set; the run is not failed. Then:
      a successful probe restores the previous note (a `waiting: …` note survives, including one written during
      CHECK_FAILED, which is what is restored); a gone probe fails the run with
      `agent exited`, not the check-failed text; a daemon restart in CHECK_FAILED drops the leftover note;
    - `Rename` of a sandbox with an orphan doesn't take `runtimeMu`, and keeps the record and the token;
    - outcomes: a failing `runner.Stop` with an orphan keeps the record and the token, and re-probes; a successful
      stop drops the record and retires the token; a kit attach that recreates the container probes once and writes
      EXITED;
    - the `sbx exec` client exits with code 0, 1, 137 or by SIGKILL: each probes. A live agent becomes an orphan
      record (no EXITED, no second agent on the next `t`). A dead one is EXITED. `onExit` returns without waiting
      on the probe, and `SetAgentStatus` from another goroutine isn't blocked;
    - a gone probe on a sandbox whose recorded status is none or EXITED writes nothing;
    - an orphan that exits on its own is noticed by the next gate refusal, by its `SessionEnd`, or (crashed, mid-run)
      by the 60 s backstop, and its run fails with `agent exited`;
    - **no automatic stop**: an idle worker with no run, an orphan whose run completed (with and without custom
      post-prompts), and one after `X` at a restart halt each keep their orphan. Dispatch skips the worker, the hold
      is NEEDS_DEVELOPER, and AGENT_DETACHED is emitted once;
  - in `SBD/agent/pty_test.go`:
    - the launch command creates `~/.sxb` and records gen, PID and start time;
    - `ReapOrphan` sends TERM, then KILL, and a reused PID (start time differs) is never signalled;
    - a missing or unparseable PID file falls back to the binary-and-flags match. That match finds **both** of two
      pre-feature agents (and a stop ends both), matches one launched with since-changed spec args, and spares a
      nested `claude -p`;
  - in `SBD/orchestrator/sequence_test.go`:
    - an orphan's completion report closes an ACTION run, and the hold is NEEDS_DEVELOPER; `t` → Replace's fresh
      `startup` ends it;
    - replace on an ACTION orphan fails the run with the replace reason and sends no post-prompt;
    - a SETUP step halts, and `R` stops the orphan, starts a fresh agent and re-sends every pre-step from step 0;
      the action prompt is typed only after `/model` and `/effort` were confirmed on the fresh agent.
- [ ] T048 **One orphan gate for every spawn (R14).** In `SBD/agent/pty.go`, add `Registry.SetOrphanGate(g
  OrphanGate)`. `Registry.Session` calls the gate **before the factory**. It never blocks (the terminal registry calls
  `Session` under its global lock), and it decides **only from the orphan record and `runtimeMu`** (T047), never from
  run flags:
  - no record, and the try-lock on `runtimeMu` succeeds → spawn, **holding the lock** until `cmd.Start` returns and
    the session is registered;
  - a record, or *unknown* → `ErrOrphan` with the agent's last state (`agent detached by a daemon restart; last seen
    working 10:42`, or `checking for a detached agent`);
  - an operation holds `runtimeMu` → `ErrBusy` naming the operation (`sandbox is stopping`, `restarting`, `being
    refreshed`, `being recreated for a kit`, `being destroyed`). The try-lock never waits.

  Nothing spawns, and **the gate never stops anything**. No caller checks for orphans itself. The two developer
  callers allowed to stop one call `ReapOrphan` first (T047). Best-effort callers log the error and are re-kicked by
  the next `sandbox_changed`.

  Test, in `SBD/agent/orphan_gate_test.go`: a table over **every** spawn path, each driven through the real daemon
  wiring with a fake factory that counts spawns and a fake prober and reaper:
  - `WarmStart` (`startAgent`);
  - `Deliver`'s start (`startAgent`);
  - `AttachAgent` (the broadcaster factory, `grpc/server.go`), plain and with `replace_orphan`;
  - guard 5's `first` start, and `DeliverWorkerStep.restart`;
  - `Registry.Prompt` (an escape-hatch result);
  - `EnsureDrained`.

  For each path, and each state (ACTION; SETUP not yet halted; SETUP halted; hold; no run; uncontrolled; unknown;
  `runtimeMu` held), assert:
  - the factory never runs while an orphan record exists or the lock is held, including when an operation takes
    `runtimeMu` between the gate check and the factory (impossible by construction: the test blocks the factory and
    asserts `Stop` waits for it);
  - **only** `replace_orphan` and `restart` stop an orphan;
  - in particular, an escape-hatch result on a SETUP run that hasn't halted yet stops nothing and fails nothing;
  - exactly one agent exists afterwards, and two concurrent stops signal once;
  - each refusal schedules a re-probe (at most one per sandbox per 10 s), and when it finds the orphan gone, the next
    spawn on that path succeeds.

  A guard test fails if a new `Registry.factory` call site appears outside `Session`.

**Checkpoint**: a step can be typed and classified on a worker, never twice, with no injectable text and no
forgeable evidence; actions carry sequences; runs snapshot them.

---

## Phase 3: User Story 1 — Built-in setup and cleanup on every run (Priority: P1) 🎯 MVP

**Goal**:
- Run `/model`, `/effort`, the action, then `/clear` after the report, with defaults on new and existing actions.
- Cleanup holds the worker; holds and their release work.
- A step that is not accepted can be retried, skipped or failed.

**Independent Test**: quickstart Scenarios 1, 4 and 5. Scenario 5 runs steps 2–6 with the Clear step only; step 1
needs a custom post-prompt, which arrives in US2.

### Orchestrator sequence (research R5–R7)

- [ ] T018 [US1] Create `SBD/orchestrator/sequence.go` with a per-sandbox **stepper**: one goroutine per sandbox
  with a run in sequence, **single-flight**, started on demand and stopped when the run is done or the orchestrator
  stops. `sandbox_changed`, the retry ticker and `ResolveRunStep` only *kick* it. Kicks during a call are
  **coalesced, never dropped**, and after **every** response the stepper re-evaluates `stepFinished` against the
  latest sandbox record. It owns:
  - **`deliverStep(run, i)`**: for a new send, increments `run.step_send` (a retry reuses it); persists it with
    `step_status = SENDING`; then calls `api.DeliverWorkerStep` with `step_index = run.step_send` (a send number,
    so a setup restarted from step 0 after an orphan reap passes guard 3a, T024) and `first` while the run is
    QUEUED.
    - The first time text was typed (any outcome other than FailedPrecondition), call `startRun` (RUNNING,
      `StartedAt`, sandbox fields).
    - MODEL_TURN or INPUT_ONLY → `step_status = TURN`, `step_kind_seen`, `step_sent_at = resp.sent_at`.
    - AGENT_LOCAL → finish the step, then `advance`.
    - NOT_ACCEPTED → `HALTED`, `step_note`, `controller.run.halted = true` via `workerRunFor`, and a
      `RUN_STEP_NOT_ACCEPTED` notification.
    - FailedPrecondition → back to `PENDING`. Once refusals have lasted `stepWaitNote` (10 s), set `step_note =
      "waiting: <reason>"`; clear it when the step is sent (R6).
    - `ErrStepSent` → SENT with the recorded `step_sent_at`, then resolved per R8 (T037).
    - Transport error (on any step, including the first) → stays SENDING, and is resolved by T022.
    - `ErrUnsupported` while the run is QUEUED → **unclaim** it (back to QUEUED, no sandbox) and mark the worker, in
      memory, as unable to take sequenced runs until its link reconnects. Fail the run ("no worker supports pre/post
      prompts; update their daemons") only when no attached, eligible worker remains (research R6).
    - UNCONFIRMED (the screen stayed stale, research R2) → SENT with `step_sent_at = resp.sent_at`, resolved by
      evidence per R8 (T022 in the MVP, T037 in full): a turn hook finishes it; a built-in is read back with
      `check_only` once the screen is fresh, and halts only if that read-back fails. A step that halts after
      UNCONFIRMED is marked `step_unconfirmed` (shown `(may have run)`; `R` asks the "may already have run" confirm).
    - A response for a run **cancelled or failed before delivery** with no hold and no cleanup pending:
      - **not typed** (FailedPrecondition, INVALID_ARGUMENT) → clear the daemon record with `UpdateWorker{set_run,
        expect_run_id}`;
      - **typed** (any other outcome, `ErrStepSent`, or a SENDING step later resolved as typed by T022) → Clear-only
        cleanup when Clear is enabled (`hold = CLEANING_UP`, Clear once idle, then the record is cleared), since
        the step reached the agent (FR-214 as amended); with Clear disabled, clear the record.

      Any other late response (e.g. the action step's,
      after the report started cleanup) only updates its step and never clears the record.
    - `INVALID_ARGUMENT` (an `expect` outside the fixed sets) → HALTED with the error as its note; never retried.
    - The step's `step_note` is mirrored to `WorkerRun.step_note` through `workerRunFor`, only when it changes.
  - **`advance(run)`**: the next step, by role.
    - After the ACTION step: `AWAIT_CLOSE`.
    - After close: the first CLEANUP step, once the agent is IDLE.
    - After the last step: `DONE`, and clear `controller.run`.
  - **`stepFinished(run, sb)`**:
    - for TURN: IDLE with `last_turn_end_at > step_sent_at`;
    - for INPUT_ONLY: IDLE after a needs-input with `last_event_at > step_sent_at` (FR-206b);
    - for INPUT_ONLY, the fallback in research R1: `input_idle_since` is after `step_sent_at` and at least 60 s
      old.
  - Persist through `putRun` and `emitChanged` on every transition.

  Add `stepRetryEvery = 2s` and `stepWaitNote = 10s` to `SBD/orchestrator/constants.go`. The retry ticker kicks
  steppers only while some run has a `PENDING` step on a reachable host.
- [ ] T019 [US1] In `SBD/orchestrator/dispatch.go`, `dispatch` claims the worker for a run with `len(steps) > 0` and
  kicks its stepper; it **never waits** on a step. Runs without steps keep `deliver` unchanged (FR-208).
  - **Claim:** record `sandbox_id`/`sandbox_host_id` on the still-QUEUED run before the first call. Dispatch skips
    QUEUED runs with a sandbox set, and `free()` counts that sandbox as busy.
  - Cancelling or failing a claimed QUEUED run clears the daemon's record (`UpdateWorker{set_run, expect_run_id}`
    with no run).
- [ ] T020 [US1] In `SBD/orchestrator/runs.go`:
  - `foldStatus` kicks the stepper for runs with steps; it does no step work inline.
  - Gate **`runAwaitingReview` itself** (so `armReview` and `reconcileInFlight`'s IDLE branch both obey it) on the action
    step having been accepted (`step_status` TURN or AWAIT_CLOSE on the ACTION step; FR-209, FR-214).
  - EXITED, or a sandbox that isn't RUNNING, during SETUP → `failRun("agent exited during setup step N/M (<text>)")`
    (FR-212). `failRunsOn` (`onSandboxRemoved`, `Detach`) names the step the same way for a run in setup.
  - The same during CLEANUP → drop the remaining steps and set the hold `NEEDS_DEVELOPER` with `hold_since = now`
    (FR-216). The daemon may already have set it (`leaveRunning`, T009).
  - `toolReportCompletion` in `SBD/orchestrator/tools.go` refuses until the action prompt was delivered: the action
    step is TURN or AWAIT_CLOSE, **or** `controller.run.step_index` equals its index (only the response was lost).
    Otherwise: "the action prompt has not been delivered yet" (FR-211).
- [ ] T021 [US1] Run close and holding runs in `SBD/orchestrator/runs.go` and `SBD/orchestrator/store.go`:
  - `completeRun` and `MarkWorkerFree` close the run as today (FR-215). When CLEANUP steps exist, instead of
    `clearWorkerRun` they set `hold = CLEANING_UP` on the Run and `WorkerRun`, and set `step` to the first cleanup
    step, `PENDING`. If the sandbox isn't RUNNING, or its agent can't be typed into at close (EXITED, or an orphan
    after a daemon restart, T047), drop the cleanup steps and set `NEEDS_DEVELOPER` instead (FR-216). This applies
    only when cleanup steps remain; otherwise no hold is set.
  - **A run that failed before its action prompt was delivered** (FR-214, amended) gets no custom post-prompt. If any
    step was typed (a pre-step, or the action prompt typed but not accepted) and the agent is live, Clear (if enabled) is its only cleanup step. If the agent exited, it
    gets nothing and no hold.
  - `Service.ListRuns` puts closed runs that hold a sandbox or are halted in `in_flight` and keeps them out of
    `history` (FR-213). The summary's in-flight count (`service.go`) counts the same runs.
  - `runOn(sandbox)` is **unchanged**. Add `holdingRunOn(sandbox)` for a closed run with `hold != NONE` or
    `step_status == HALTED`, used by the stepper, folding, `ResolveRunStep` and hold release.
  - `onSandboxRemoved` and `Detach` of a held sandbox end its holding run's cleanup (DONE, `hold = NONE`).
  - `trimHistory` skips holding runs (FR-213 retention).
- [ ] T022 [US1] Reconcile against the daemon record (`SBD/orchestrator/sequence.go`), on `sandbox_changed` and in
  `reconcileInFlight`:
  - **Hold release:** a holding run whose sandbox's `controller.run` is no longer this run is finished: DONE, `hold
    = NONE`, any waiting retried Clear dropped (FR-216, run-538). The daemon ended the hold (T008).
  - **SENDING:** resolved from `controller.run.step_index` against `run.step_send`: equal → SENT with `controller.run.step_sent_at` (calling
    `startRun` for a first step on a still-QUEUED run); lower → PENDING. This is in the MVP, so a lost response or a
    link blip never strands a run. Evidence-based finishing of SENT/TURN steps after a restart is T037.
  - **Halted repair:** a HALTED run whose `controller.run.halted` is false is re-pushed (as `repairReviewFlag` does).
  - **Stale records:** a worker record naming a finished run that isn't holding is cleared with `expect_run_id`.
- [ ] T023 [US1] Reasoning sandbox (`SBD/orchestrator/reasoning.go`): `RebuildReasoningSandbox` is allowed while its
  sandbox is held. The old sandbox's removal ends the holding run's cleanup (T021), and the new sandbox starts free
  (research R7, FR-222).
- [ ] T024 [US1] `ResolveRunStep` in `SBD/orchestrator/sequence.go`, wired in `SBD/grpc/orchestrator_rpcs.go`
  (through the stepper):
  - **Guard**: the run is halted, or its CLEAR step is under a NEEDS_DEVELOPER hold (RETRY only), and `req.step`
    equals its step. SKIP on CLEAR or on ACTION_PROMPT is refused.
  - **RETRY**: back to PENDING; the next `DeliverWorkerStep` carries `retry`, which the daemon accepts once while the
    run is halted, or for Clear under the hold (guard 3a). A retried Clear never starts an agent (guard 5).
  - **SKIP**: `advance`.
  - **A step halted by a daemon restart** (T047): `R` is a **restart**. It resets the run to step 0 with every pre-step
    PENDING and sends step 0 with `DeliverWorkerStep.restart`. On that flag (accepted only while the run is halted
    by a restart), the owning daemon stops the orphan (`ReapOrphan`, `mode = restart`), starts a fresh agent as guard 5 does, and
    refuses `agent is starting`; the stepper retries without the flag once the agent is ready. Each pre-step is re-sent as a new send (`step_send` grows), so the action prompt never reaches an agent without its
    model and effort (SC-001). Its single confirm reads `Stop the detached agent (last seen <state> <time>) and
    restart setup from the first step? Anything it was doing is lost. (y/N)`; the stop is logged and noted in `step_note`. `S` is refused for such a halt (`the agent was replaced; restart setup with R`). `X` fails the run.
  - **FAIL**, by role (FR-222):
    - SETUP or ACTION_PROMPT → `failRun` per FR-212, naming the step. No custom post-prompt follows; only Clear, if
      enabled (FR-214 as amended);
    - a custom CLEANUP step → drop the remaining custom steps, then free the sandbox per FR-216 (Clear still sent if
      enabled);
    - CLEAR → hold `NEEDS_DEVELOPER`, keeping `R` on the row (FR-216, FR-222).
  - Clear `controller.run.halted` when the run leaves HALTED (SKIP, FAIL; RETRY clears it on the daemon), and with
    it `step_unconfirmed` and `step_sent_before_restart`, which are also cleared whenever `step` changes (test: a
    later halt of another step never shows `(may have run)`).
- [ ] T025 [US1] Tests in `SBD/orchestrator/sequence_test.go`, with a fake `WorkerAPI` that scripts step outcomes,
  delays and `sandbox_changed`:
  - **Order and advance:** the default order; AGENT_LOCAL advances; TURN waits for the turn end; the INPUT_ONLY
    fallback finishes after 60 s of an empty box.
  - **Delivery:** PENDING retries on a status change and on the ticker; `waiting:` after 10 s; a kick during a call
    is not lost, and a turn that ended during the call finishes.
  - **Lost responses (MVP):** SENDING resolves from the daemon record to SENT or PENDING; a report whose action step
    is SENDING but recorded on the daemon is accepted; a response for a cancelled run clears the record; a stale
    worker record is cleared.
  - **Mixed versions:** `ErrUnsupported` re-queues the run to another worker, and fails it only with none eligible.
  - **Review gating:** `reconcileInFlight` on link-up doesn't move a run in setup to AWAITING_REVIEW.
  - **Close on a stopped sandbox:** cleanup is dropped and the hold is NEEDS_DEVELOPER.
  - **Halted action step:** SKIP refused; FAIL fails the run naming it, then cleanup runs.
  - **Never typed twice:** a 10 s scripted step call doesn't block dispatch of another worker; concurrent kicks make
    one call; SENDING is persisted before the call; `ErrStepSent` → SENT.
  - **First step:** a NOT_ACCEPTED first step gives a RUNNING, HALTED run; retrying it is accepted; a transport
    error on it leaves it SENDING, not failed.
  - **Claim:** a claimed QUEUED run is not dispatched to a second worker; cancelling it frees the worker.
  - **Clear under the hold:** after `X` on Clear, `R` is accepted and its fresh session ends the hold.
  - **Review and report gating:** no review timer while the action step is PENDING or HALTED; a report before the
    action step is accepted is refused.
  - **Setup and cleanup:** an exit during setup fails the run naming the step; cleanup after the report and after
    mark free (completed and failed); the sandbox is not free during cleanup, so a queued run of either type waits
    (SC-004); an exit during cleanup gives NEEDS_DEVELOPER.
  - **Hold release:** the record cleared by the daemon finishes the run, and a waiting retried Clear is dropped.
  - **Holding runs:** `failRunsOn` on a held sandbox returns (no loop); removal ends the hold; a held reasoning
    sandbox can be rebuilt; retention skips holding runs.
  - **`WorkerRun` writes:** a review-flag sync during a hold keeps it.
  - **`ResolveRunStep`:** every branch and guard.
  - **Other:** `ErrUnsupported`; a run without steps still goes through `DeliverWorkerPrompt` (SC-005).

  In `SBD/grpc/orchestrator_rpcs_test.go`: `ResolveRunStep` errors per contracts/proto-delta.md.

### Notifications (FR-223, research R10)

- [ ] T026 [P] [US1] In `SBD/agent/hooks.go`, skip `EmitNotification(TASK_COMPLETE, …)` on `Stop` and in
  `backstopExpired` when the sandbox's `controller.run.step_role` is SETUP or CLEANUP. Read it through the status
  updater's sandbox lookup. Tests in `SBD/agent/hooks_test.go`.
- [ ] T027 [P] [US1] `UI/notifications.go`: a label and message for `NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED`, routed
  like `RUN_FAILED`, and for `NOTIFICATION_KIND_AGENT_DETACHED` ("<sandbox>: agent detached by a daemon restart (last
  seen <state>)[ on <run>] — t to Replace or Wait"; the start-up aggregate, which has an empty `sandbox_id`, reads "N agents detached by a daemon
  restart (M on idle workers) — R on the Workers view replaces the idle ones" and is shown at host level. It is
  never suppressed by attachment, and it is acked by id like any other. In the notification list its item id is the
  notification id, not the empty `sandbox_id`, so aggregates from several hosts stay distinct. `enter` on it opens
  the Workers view of the orchestrator owning that host's workers (or, with none, the sandbox list filtered to that
  host) instead of `selectByID("")`. Test both) and for `NOTIFICATION_KIND_AGENT_CHECK_FAILED`
  ("<sandbox>: can't check for a detached agent: <error>[ on <run>] — t to Wait or Restart sandbox"), never
  suppressed, since the daemon never resolves an orphan on its own (research R14).

### TUI: built-ins, run rows and controls (contracts/jobs-ui.md)

- [ ] T028 [US1] In `UI/orchestrator_forms.go`:
  - `actionFormVals` gains `model`, `effort` (strings incl. `off`) and `clearAfter bool`;
  - `openActionForm` adds the Model Select, the Effort Select and the `Clear after run` Confirm after Arguments;
  - new actions get `opus`/`medium`/yes (FR-203);
  - existing actions load from the effective sequence: an absent `sequence` shows the defaults (FR-210);
  - `off` keeps the stored value (`*_enabled = false`);
  - `submitOrchOpForm` always sends `Action.sequence`;
  - in `UI/formguard.go` (SWI-9), the action snapshot includes the new fields, so the guard sees them.
- [ ] T029 [US1] Run rows in `UI/jobs_overview.go` `jobsRunLine` and `UI/run_actions.go` `runState`: show
  setting up n/m, cleaning up n/m, `not accepted: <note>`, `waiting: <reason>`, sent before a restart, and the holds
  (contracts/jobs-ui.md).
  - Holding and halted closed runs arrive in `in_flight` from the server (T021), so *In progress* shows them once and
    *History* doesn't (FR-213).
  - `UI/jobs_queue.go` uses the same line.
  - `t`/`T` (`runTerminal`) also open the terminal of a closed run that holds its sandbox, not only one in flight.
  - `openRunOfEvent` (`UI/orchestrator_forms.go`) skips closed runs, so a holding run in `in_flight` never triggers a
    spurious "already open" prompt for its timed event.
- [ ] T030 [US1] Keys in `UI/run_actions.go` `runKeyAction` and `UI/jobs.go` help (after T029, same file):
  - `R` retry, `S` skip (hidden for Clear) and `X` fail, only on halted runs;
  - the confirms use `UI/confirm.go`, defaulting to **No**. `X` is only offered on halted runs and `x` only on queued
    ones, so a Shift slip opens a confirm or does nothing, never an action. For a step sent before a restart, `S` is
    listed first and `R` asks with the warning;
  - `S` is hidden for Clear and for the action step;
  - all three call `ResolveRunStep`, and a FAILED_PRECONDITION shows and reloads;
  - for a step halted by an owning-daemon restart, `R` reads **Restart setup** with its own confirm, and `S` is
    hidden (contracts/jobs-ui.md);
  - **Detached agent** (`UI/terminal.go` attach, and `T`), on **any** sandbox, worker or not: when the attach is
    refused because of a detached agent, show the Wait/Replace dialog with its last state and run (default Wait if
    it was working or waiting for input, Replace if idle); Replace re-attaches with `AttachInfo.replace_orphan` and
    shows `replacing the detached agent…`. A busy refusal shows its operation-specific message.
    After 60 s or 3 failed checks (`can't check for a detached agent: <error>`), the dialog offers **Restart sandbox** (behind
    a confirm naming the run it fails) and **Wait**, with no Replace. Test every choice, on a worker and on a plain
    sandbox, with the fake daemon;
  - a step marked `step_unconfirmed` lists `S` first and `R` asks the "may already have run" confirm;
  - **bulk Replace** (`UI/workers.go`): `R` on the Workers view opens one confirm listing the DETACHED workers
    last seen IDLE with no run (others counted as `K more detached agents were busy — use t on each`), all ticked (`Replace N idle detached agents? Each is stopped and a fresh agent starts.`); `y` calls
    `ReplaceDetachedAgent` per sandbox on its owning daemon and shows `replaced 5 · busy 1 (stopping) · failed 1:
    <error>`; with none, `no idle detached agents`. Help: `R replace idle detached`. `R` is bound only on the Workers view; on the sandbox list (including the
    Orchestrator Sandboxes tab) it stays Rename. Test both, with the fake daemon.

  Add `ResolveRunStep` and `ReplaceDetachedAgent` to the TUI `Daemon` interface and its fakes.
- [ ] T031 [P] [US1] Sandbox list and Workers view:
  - `UI/sandbox_list.go` `needsDeveloper` adds `hold ∈ {NEEDS_INPUT_FREEING, NEEDS_DEVELOPER}` and
    `controller.run.halted`;
  - `sandboxItem`'s worker suffix shows each hold, incl. `needing the developer — open its terminal and run /clear`;
  - a `CLEANING_UP` worker is surfaced, with `cleaning up · waiting: <reason>`, once its step has a `waiting:` note
    or is halted (contracts/jobs-ui.md);
  - the `waiting:` text comes from `controller.run.step_note` (`WorkerRun.step_note`, T018), the only copy the
    sandbox list can see;
  - `UI/orchestrator_views.go` Workers view shows the same text from `WorkerView.hold`/`halted`/`step_note`, set in
    `SBD/orchestrator/workers.go` alongside `availability()`.
- [ ] T032 [US1] TUI tests with the `jobsWith`/`fakeDaemon` helpers:
  - in `UI/orchestrator_forms_test.go`: the defaults on a new action; a legacy action shows the defaults and saves
    them explicitly; the option lists match the spec (no ultracode, US1 #5); `off` round-trips and keeps the value;
    the guard reports dirty after a Model change;
  - in `UI/jobs_overview_test.go`: rows for each state; held and halted runs listed under *In progress*;
  - in `UI/run_actions_test.go`: R/S/X availability, confirms (default No) and RPC calls; S hidden for Clear and the
    action step; the sent-before-restart ordering and warning; `t` on a holding run opens its terminal;
  - in `UI/sandbox_list_test.go` and `UI/orchestrator_views_test.go`: hold suffixes; surfacing to the top on
    All/host/group tabs; CLEANING_UP stays hidden while it progresses and surfaces once it waits.

**Checkpoint**: US1 is fully functional. Run quickstart Scenarios 0, 1, 4, 5 (steps 2–6), 8 (built-ins only) and
10.

---

## Phase 4: User Story 2 — Custom pre-prompts and post-prompts (Priority: P1)

**Goal**: The developer's own setup and teardown, in order, with placeholders and validation.

**Independent Test**: quickstart Scenarios 2, 3, 5 (step 1) and 6.

- [ ] T033 [US2] In `UI/orchestrator_forms.go`:
  - add `Pre-prompts` and `Post-prompts` Text fields through `spec.text` (enlargeable). They hold one prompt per
    line; the description says so and notes that a prompt can't span lines;
  - on save, trim lines and drop empty ones, then send `pre_prompts`/`post_prompts`;
  - show server refusals verbatim on the form status (contracts/jobs-ui.md);
  - `actionLine` appends the FR-219 summary (`pre: /model opus · /effort medium · 1 custom — post: 1 custom ·
    /clear`), omitted when there are no steps. It is used by `UI/jobs_groups.go` `renderGroups` and the
    orchestrator Actions view.
- [ ] T034 [US2] Freeing during cleanup (FR-216):
  - In `SBD/orchestrator/runs.go` `MarkWorkerFree` and `SBD/orchestrator/sequence.go`: when a run holds a sandbox
    in CLEANING_UP and the agent is NEEDS_INPUT, the custom cleanup steps are dropped and the hold becomes
    `NEEDS_INPUT_FREEING`.
  - Clear (if enabled) is then delivered under the normal guard, once the box is empty and ready. The sandbox is
    free once Clear finishes; with no Clear, it is free at once.
  - The MarkWorkerFree path for "no run + NEEDS_INPUT" (`clearNeedsInput`) is unchanged for runs without steps.
  - In `UI/run_actions.go` `markFreeKey`, also accept a sandbox with `hold == CLEANING_UP` whose agent is NEEDS_INPUT
    (contracts/jobs-ui.md). The run is already closed, so this frees directly: **no outcome/note dialog**, and the
    run's outcome is never changed.
- [ ] T035 [US2] Tests:
  - in `SBD/orchestrator/sequence_test.go`: custom pre and post order around the built-ins (US2 #2); placeholders
    filled per call; a needs-input during a pre-prompt gives an AWAITING_INPUT run that continues after idle
    (US2 #4); FR-216 freeing with and without Clear;
  - in `UI/orchestrator_forms_test.go`: the prompts round-trip; empty lines are dropped; a refusal is shown; the
    `actionLine` summary;
  - in `UI/run_actions_test.go`: mark free is offered on a cleaning-up worker asking for input, and frees it with no
    outcome dialog.
- [ ] T036 [P] [US2] e2e in `src/services/switchboardd-e2e/`: `PutAction` with prompts that break each FR-204 rule
  is refused naming the entry; a valid action round-trips its `sequence`; a legacy action without `sequence` reads
  back with no `sequence`, and the orchestrator applies the defaults.

**Checkpoint**: US1 and US2 work together. Run quickstart Scenarios 2, 3, 5 and 6.

---

## Phase 5: User Story 3 — See where a run is across restarts (Priority: P2)

**Goal**: The full sequence in the prompt overlay, restarts resolved without re-sending, and the e2e coverage of
the run surfaces.

**Independent Test**: quickstart Scenarios 7 and 9, and US3 #1–#6.

- [ ] T037 [US3] Restart resolution (research R8) in `SBD/orchestrator/runs.go` `reconcileInFlight` and at
  orchestrator start. Hook evidence keeps arriving from an orphan after an owning-daemon restart (T047), so rules 1–2
  apply to it too; the screen read-back does not.
  SENDING was already resolved by T022:
  - **TURN** (classified before the restart): skip the turn-start test; finished when IDLE with `last_turn_end_at >
    step_sent_at` (or the input-only rule), otherwise awaited;
  - **SENT**: a model turn if `last_submit_at > step_sent_at`, or `last_needs_input_at > step_sent_at` (main session
    only, FR-217); finished if `last_turn_end_at > step_sent_at` and IDLE, otherwise awaited;
  - otherwise built-ins → `DeliverWorkerStep{check_only}`, with Clear using `last_session_source == "clear"` after
    the send;
  - otherwise HALTED with `step_sent_before_restart = true`, **written to the daemon** (`controller.run.halted`,
    through `workerRunFor` with `expect_run_id`) so that `R` passes guard 3a.

  It never re-sends, and it only compares daemon-clock times.
- [ ] T038 [US3] Tests in `SBD/orchestrator/sequence_test.go` for restarts:
  - a TURN step classified INPUT_ONLY finishes or waits, never halting;
  - a SENT step with a needs-input report after the send is awaited;
  - a restart halt sets `controller.run.halted`, and `R` then types it once;
  - a turn finished during the outage; still working;
  - a built-in confirmed; Clear confirmed;
  - no evidence → halted with the marker;
  - a lost response on a later step resolved on reconnect.
- [ ] T039 [P] [US3] Prompt overlay in `UI/run_actions.go` `promptView`: steps in order, marking `▶` current and `✓`
  done; without steps it is unchanged (FR-220). Tests in `UI/run_actions_test.go`.
- [ ] T040 [P] [US3] e2e in `src/apps/switchboard-tui-e2e/`: an action form with built-ins and prompts saves and
  reloads; a halted run shows `R`/`S`/`X`.

**Checkpoint**: all stories work. Run quickstart Scenarios 7 and 9.

---

## Phase 6: Polish & Cross-Cutting Concerns

- [ ] T041 [P] Docs: the README's orchestrator section covers the action sequence, the defaults, the holds and the
  R/S/X controls. `CLAUDE.md` (SPECKIT block) marks the implementation as done. The `specs/009-orchestrator`
  cross-reference notes that delivery is per step for runs with steps.
- [ ] T042 Gates: `make proto` output checked in, then `golangci-lint fmt ./...` and `make fmt-check vet lint
  cover`. Coverage must be ≥ 90% for `switchboardd`, `switchboard-tui`, `switchboard-termview` and
  `switchboard-proto`. Then `make e2e`.
- [ ] T043 Manual: quickstart.md Scenarios 0–11 on a real worker (Scenario 0 first). Record the agent version and any
  fixture or matcher change in research.md R1.

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)**: T001 first. T002 and T003 can start right after; T004 after T003.
- **Foundational (Phase 2)**: depends on Setup. It blocks every story.
  - Daemon screen: T005 → T007 (T006 [P] alongside).
  - T008 → T009.
  - T010.
  - Then T011 → T012 → T013.
  - Security and recovery: T044 [P] after T014; T045 → T046 after T008; T047 after T009 and T046 (it records `hold_gen`); T048 with T047.
  - The action model chain T014 → T015 → T016 → T017 is independent of the daemon-screen chain.
- **US1 (Phase 3)**:
  - Orchestrator: T018 → T019 → T020 → T021 → T022 → T023 → T024 → T025.
  - T026 and T027 [P].
  - TUI: T028; T029 → T030; T031 [P]; T032 after the TUI tasks. These run in parallel with the orchestrator chain;
    T030 needs T024 (the RPC).
- **US2 (Phase 4)**: needs US1. T033; T034 → T035; T036 [P].
- **US3 (Phase 5)**: needs US1. T037 → T038; T039 and T040 [P].
- **Polish (Phase 6)**: after all stories.

### Parallel opportunities

- Two independent chains in Phase 2: the daemon screen and step RPC (T005–T013, then T045–T048), and the action
  model (T014–T017, plus T044).
- In US1, the TUI (T028–T032) and the notifications (T026, T027) alongside the orchestrator chain (T018–T025).
- US2 and US3 can proceed in parallel once US1 is done. They touch different files, except `UI/run_actions.go`:
  T034's `markFreeKey` and T039's `promptView` are separate functions, but sequence them to avoid conflicts.

### Parallel example: User Story 1

```text
Orchestrator: T018 → T019 → T020 → T021 → T022 → T023 → T024 → T025
TUI:          T028 → T029 → T030 (after T024) → T031 → T032
Daemon/TUI:   T026, T027
```

## Implementation Strategy

- **MVP**: Setup, Foundational, then **US1**: built-in model, effort and clear, cleanup holds, and the R/S/X
  controls, on every action. This alone delivers the ticket's main value (spec US1 "Why this priority"), and no
  halt strands a worker. Validate with the US1 checkpoint scenarios.
- **Increment 2**: **US2**, custom prompts.
- **Increment 3**: **US3**, the sequence overlay and evidence-based restart resolution. The MVP already resolves a
  lost response (T022) and an owning-daemon restart (T047). Until US3 lands, the remaining limitation is an
  orchestrator restart while a remote worker's step is SENT (typed, not yet classified): it stays unresolved until
  the developer acts, and is never re-sent. A TURN step still finishes normally (T018).
- One implement PR, stacked on this plan + tasks PR, per the repo's speckit workflow.
