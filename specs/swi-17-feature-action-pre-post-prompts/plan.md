# Implementation Plan: Action Pre & Post Prompts

**Branch**: `jamesloganclark/swi-17-feature-action-pre-post-prompts` (spec) → `…-tasks` (plan + tasks) → `…-implement` | **Date**: 2026-10-28 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/swi-17-feature-action-pre-post-prompts/spec.md`

## Summary

Every action gets a **sequence** around its prompt:

- built-in **Model** and **Effort** pre-steps and a **Clear** post-step, on by default, including for existing
  actions;
- the developer's own **custom pre-prompts** and **post-prompts**.

The orchestrator sends the steps one at a time, and each is sent only once the previous one has **finished**,
judged by evidence after its submit.

1. **The planning gate, run on a real agent (research R1).** Claude Code 2.1.312 was probed in a PTY:
   - `/model` and `/effort` raise **no hook**. Their only result is a confirmation line on screen, within ~150 ms.
   - An invalid value leaves an empty, ready box, so **read-back is required**.
   - `/clear` raises `SessionStart` with `source: "clear"`.
   - `ultracode` turned out to be a sticky session toggle, not an effort level. With the developer's agreement it is
     **dropped from the Effort options**, and the spec is amended in this PR.
2. **The daemon reads the worker's screen (R2).** The TUI's VT renderer moves to a library module
   (`src/libs/switchboard-termview`). The daemon's single PTY reader also feeds a per-sandbox screen. A small reader,
   `agent/claudeui.go`, pinned by fixtures from R1, tells whether the input box is empty and ready, whether a step's
   text is still in it, whether a picker is open, and which confirmation line follows a step.
3. **One step = one RPC (R3).** The orchestrator owns the sequence and its persisted state, on the `Run`. The
   sandbox's owning daemon types each step through a new **`DeliverWorkerStep`** and runs the **submit check**
   locally, using a hook tap and the screen. It returns MODEL_TURN, AGENT_LOCAL (with read-back for built-ins) or
   NOT_ACCEPTED with the reason, and Enter is re-pressed only while the step's text is visible.
   - Runs with no steps keep `DeliverWorkerPrompt` unchanged (FR-208).
4. **Run state (R5–R6, R8).** The run snapshots its steps at call time and advances setup → action → close →
   cleanup.
   - The run closes exactly when it does today.
   - Cleanup keeps the sandbox's `controller.run` set, so the existing *free* rule holds it.
   - A step that can't be typed yet waits, and says why on its row after 10 s.
   - Steps are delivered by one goroutine per sandbox, so dispatch never blocks on a submit check. Each step is
     persisted as SENDING before the call, and the daemon's guard records the step index it typed, so **no step is
     ever typed twice**.
   - A restart resolves sent steps from new recorded hook evidence: `last_submit_at`, `last_turn_end_at`,
     `last_session_start_at/source`.
7. **Prerequisites (R12).** SWI-15 (PR #17: `SessionStart`, agent readiness) has merged. SWI-9's implementation
   (form guard, enlargeable text fields) must merge first.
8. **Security (R13).**
   - Control characters (ESC, so the paste-end marker too; C0 except `\n`/`\t`; DEL; C1) and invalid UTF-8 are
     refused in the **untrusted substituted values** only: call argument values and defaults, after CRLF is
     normalised, checked at call and compose time. Authored text and escape-hatch output are left as they are.
   - Hook callbacks carry a **per-agent token**, drawn for each spawned PTY generation and passed in the agent's
     environment (`SXB_HOOK_TOKEN`), so it is set before the first hook. The daemon persists `{generation → token}`,
     so an agent that survives a restart keeps working hooks. The escape-hatch wrapper sends the same header and is
     re-injected at daemon start.
   - Hold release and Clear's read-back are corroborated by the daemon's own spawn generation and screen. Only the
     main agent session counts.
   - `DeliverWorkerStep` derives and checks built-in text, and accepts only increasing step indices.
9. **Daemon restarts (R14).** The agent **survives** a daemon restart (`agentCommand`), but the daemon loses its
   PTY, so the agent is an **orphan**: hooks and its MCP report still work, but it can't be typed into or read.
   - An action in flight stays in flight.
   - Its terminal can't be opened beside it; the developer may **replace** it, which fails the run.
   - A setup step halts; `R` replaces the orphan and **restarts setup from step 0**, so model and effort are set
     again on the fresh agent.
   - Cleanup becomes *needing the developer*.
   - **One choke point:** every spawn goes through `agent.Registry.Session`, whose orphan gate refuses to start an
     agent beside an unreaped orphan, and reaps it (by its recorded PID and start time) where the phase allows. So
     two agents never share a workspace, whichever path spawns.

## Technical Context

**Language/Version**: Go 1.26 (workspace `go.work`). The modules touched are `src/services/switchboardd`,
`src/apps/switchboard-tui`, `src/libs/switchboard-proto`, and the **new** `src/libs/switchboard-termview`.

**Primary Dependencies**: the existing ones: gRPC/protobuf, bbolt, Bubble Tea/huh. `termview` moves rather than a VT
library being added, so there is **no new dependency for the repository**. The **daemon module** does gain, through
`switchboard-termview`, `charmbracelet/x/ansi`, `charmbracelet/x/cellbuf` and their transitive packages, which the
TUI already uses.

**Storage**: the bbolt `orchestrators` and `orchestrator_runs` buckets (additive proto fields on `Action` and `Run`),
and the sandbox registry (additive `AgentSession` and `WorkerRun` fields). No migration is needed: a missing
`Action.sequence` reads as the defaults (FR-210).

**Testing**:
- `go test` with the existing fakes (`fakeDaemon` in the TUI; orchestrator runtime tests with a fake `WorkerAPI`).
- New unit tests for `claudeui` against the R1 screen fixtures, and for the submit check with a fake hook tap and a
  scripted screen.
- e2e (`switchboardd-e2e`, `switchboard-tui-e2e`) for the step RPC and the form/rows.
- Gates: `make fmt-check vet lint cover` (≥ 90% per module), `make e2e`, and `make proto` regeneration checked in.

**Target Platform**: the Linux/macOS daemon (`sxbd`) and TUI (`sxb`). Workers run Claude Code ≥ 2.1.312 (quickstart
Scenario 0 re-checks the matchers).

**Project Type**: a daemon plus a TUI client, with a gRPC contract change.

**Performance Goals**:
- A built-in step finishes in ≈ `submitWindow` (2.5 s) plus a typing delay. The confirmation lands in ~150 ms, but the
  window must pass before a step counts as agent-local (FR-206b).
- Only orchestrator-controlled sandboxes keep a daemon screen. Each chunk is parsed on a per-broadcaster goroutine fed
  by a bounded channel, outside the broadcaster mutex, so fan-out and `Attach` never wait on parsing. Overflow marks
  the screen stale rather than blocking, and the screen keeps no scrollback (R2).
- Evidence fields are written only when they change, so `input_idle_since` sampling causes no `sandbox_changed`
  traffic of its own.
- The pending-step retry ticker runs only while a step waits: one RPC per waiting sandbox every 2 s.

**Constraints**:
- No timed wait decides a step; only evidence after its submit does (spec planning gate).
- Enter is never pressed on an empty box or into a modal (FR-206d).
- Nothing is typed into a box that holds text (FR-206c, 009 FR-119).
- A held sandbox takes no run of either type (FR-215).
- No new env vars; all bounds are package constants.
- Older worker daemons answer `UNIMPLEMENTED`, which fails a sequenced run's delivery with an "update the worker
  daemon" reason.

**Scale/Scope**:
- ~6 daemon packages: `orchestrator`, `worker`, `agent`, `terminal`, `sandbox`, `grpc`.
- ~8 TUI files, 1 library module moved, and the proto.
- At most 2 × 10 custom prompts plus 3 built-ins per action.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack. Features 001–010 and
SWI-9 are implemented in **Go**, a deviation recorded and justified in **001's Constitution Check + Complexity
Tracking**. SWI-17 stays inside that deviation:

| Principle | Status for SWI-17 |
|-----------|-------------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue per 001). |
| III Type Safety | ✅ Go static typing. Sequence, step and hold states are proto enums. `claudeui` returns typed results, not strings to re-parse. |
| IV Naming & Layout | ✅ New files in existing packages (`orchestrator/sequence.go`, `worker/step.go`, `agent/claudeui.go`), with colocated `_test.go` and `testdata/`. |
| V Verification Before Merge | ✅ Same `make` gates, plus `make proto` output checked in. |
| VI Multi-Level Testing | ✅ Unit tests (claudeui fixtures, submit check, sequence state machine, validation, TUI form/rows/keys), daemon e2e for `DeliverWorkerStep`/`ResolveRunStep`, TUI e2e for the form. ≥ 90% coverage held per module, including the new `switchboard-termview`. |
| VII Containerized Deployment | ✅ N/A: daemon/client binaries (001 carve-out). |
| VIII Env Discipline | ✅ No env vars added. |
| Repository Structure | ⚠️ A **new library module** `src/libs/switchboard-termview`. It sits at depth `src/<category>/<name>/` and is a framework-agnostic lib with no UI framework dependency (`charmbracelet/x/ansi` and `charmbracelet/x/cellbuf`) and no DB I/O. It is added to `go.work` and the Makefile's module list, which SWI-11 derives from `go.work`. This follows the 009 precedent for `switchboard-dial`, so there is no violation. |

**Result**: PASS. **Re-checked after Phase 1 design: still PASS.** The new module is the only structural addition,
and it follows the `libs` rules.

## Project Structure

### Documentation (this feature)

```text
specs/swi-17-feature-action-pre-post-prompts/
├── spec.md              # amended in this PR: ultracode dropped (planning amendment)
├── plan.md              # This file
├── research.md          # Phase 0: R1 (agent probe, planning gate) … R14
├── data-model.md        # Phase 1: Action.sequence, Run steps/state, WorkerRun hold, AgentSession evidence
├── quickstart.md        # Phase 1: validation guide (Scenario 0 re-checks the R1 matchers)
├── contracts/
│   ├── proto-delta.md   # additive proto: enums, messages, DeliverWorkerStep, ResolveRunStep
│   ├── worker-step.md   # the guard, typing and submit check of one step
│   └── jobs-ui.md       # form fields, rows, keys, overlay, sandbox-list holds
├── checklists/          # (from the specify phase)
└── tasks.md             # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/proto/switchboard.proto   # proto delta (contracts/proto-delta.md); gen/ regenerated
specs/001-sandbox-session-manager/contracts/switchboard.proto   # mirror

src/libs/switchboard-termview/            # NEW module: moved from apps/switchboard-tui/internal/termview
├── go.mod / termview.go / termview_test.go   #   + a Lines() snapshot with each cell's faint attribute (claudeui needs it)

src/services/switchboardd/internal/
├── terminal/broadcaster.go   # controlled sandboxes only: chunks → parser goroutine (bounded, stale on overflow) →
│                             #   termview.Screen (no scrollback; resized with the PTY; fixed 140×45 with no client,
│                             #   plus a repaint nudge); Screen() snapshot; EnableScreen; Registry.Screen/BroadcasterExisting never spawn
├── agent/
│   ├── claudeui.go           # NEW: InputBox, ConfirmationAfter, modal markers, built-in matchers (R2)
│   ├── testdata/claudeui/    # NEW: R1 screen fixtures
│   ├── hooks.go              # Tap(); evidence (last_submit_at, last_turn_end_at, session start); TASK_COMPLETE suppressed
│   │                         #   for step_role SETUP/CLEANUP (Stop + backstop); SessionStart via SWI-15 (R12);
│   │                         #   corroborated startup/clear SessionStart ends a NEEDS_DEVELOPER hold (R7, R13);
│   │                         #   per-agent token (header from $SXB_HOOK_TOKEN, set at launch); every hook forwards
│   │                         #   its input, streamed; PTY generation + main-session filter (R13)
│   └── pty.go                # TypeStep(): paste + Enter without blind ensureSubmitted; RePress(); agents.Existing;
│                             #   PID + start time recorded at launch; orphan gate in Session + ReapOrphan (R14);
│                             #   no spawn from Prompt while held
├── worker/
│   ├── step.go               # NEW: DeliverWorkerStep guard (step_index: never typed twice) + submit check + check_only
│   ├── deliver.go            # unchanged path for runs without steps; Frame() reused
│   └── controller.go         # Update: set_run carries hold/halted/step_role
├── sandbox/manager.go        # SetAgentEvidence; ClearHeldRun; leaveRunning keeps held/cleanup runs as NEEDS_DEVELOPER;
│                             #   SetAgentStatus(EXITED) escalates cleanup to NEEDS_DEVELOPER; restart recovery (R14)
├── orchestrator/
│   ├── sequence.go           # NEW: per-sandbox single-flight stepper (deliverStep, advance), hold handling,
│   │                         #   ResolveRunStep, retry ticker
│   ├── actions.go            # effectiveSequence, buildSteps, composeStep; validateAction FR-204 rules
│   ├── values.go             # checkValue: untrusted call values only (CRLF→LF, UTF-8, control chars; R13)
│   ├── runs.go               # callAction snapshots steps; foldStatus/armReview per step role; completeRun/MarkWorkerFree
│   │                         #   start cleanup instead of clearing the worker run; holdingRunOn (runOn unchanged);
│   │                         #   workerRunFor (every WorkerRun write keeps all fields); restart resolution
│   ├── dispatch.go           # runs with steps: claim and kick the stepper, never wait on a step
│   ├── workers.go            # WorkerView.hold/halted
│   ├── reasoning.go          # rebuild allowed while the reasoning sandbox is held
│   ├── tools.go              # report_completion refused until the action step is accepted (FR-211); list_actions unchanged
│   ├── store.go              # trimHistory skips holding/halted runs
│   └── constants.go          # maxStepPrompts, stepRetryEvery, stepWaitNote
└── grpc/
    ├── worker_rpcs.go        # DeliverWorkerStep
    └── orchestrator_rpcs.go  # ResolveRunStep

src/apps/switchboard-tui/internal/
├── termview/                 # removed (imports switched to the lib)
└── ui/
    ├── orchestrator_forms.go # action form fields; actionLine sequence summary; submit builds ActionSequence
    ├── formguard.go          # snapshot includes the new action fields (SWI-9 guard)
    ├── run_actions.go        # promptView lists steps; R/S/X keys + confirms → ResolveRunStep; runState with step text
    ├── jobs_overview.go      # In progress includes holding/halted runs; jobsRunLine step/hold text
    ├── jobs_queue.go / jobs.go # rows and help bindings
    ├── sandbox_list.go       # needsDeveloper + worker suffix for holds/halts
    ├── orchestrator_views.go # Workers view hold text
    └── notifications.go      # RUN_STEP_NOT_ACCEPTED label
```

**Structure Decision**:
- The sequence logic is concentrated in two new files: `orchestrator/sequence.go` (state and decisions) and
  `worker/step.go` (typing and observation). There is also one pure reader, `agent/claudeui.go`.
- Existing files change only where they hand off to these files or show their state.
- `termview` becomes a library so the daemon and TUI share one VT renderer.

## Complexity Tracking

No new constitution violations. The Go-stack deviation is inherited from 001 and unchanged. The new
`switchboard-termview` module follows the `libs` rules and the 009 `switchboard-dial` precedent.
