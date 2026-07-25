# Feature Specification: Escape Hatch

**Feature Branch**: `005-escape-hatch`

**Created**: 2026-07-18

**Status**: Draft

**Input**: User description: "We want to add a feature called `Escape Hatch`. The purpose of this feature is to allow AIs to run a specified set of commands outside of a docker sandbox. A good example of this would be `pnpm install`. The reason for this is because there are certain commands that simply can't be run in a sandbox safely or meaningfully. End to end tests are another example, often they rely on running on real hardware, not microVMs. To this end, an escape hatch serves the purpose of allowing a running ai agent to run key commands outside the sandbox and be notified of their results. Ideally the way this would work is the user would specify a list of escape hatch commands that go along with a sandbox kit. Then, these get turned into a set of specific ai commands and a rule for the underlying ai to understand when and how to run these commands. When these commands are run, they should trigger a call to the supervising switchboard daemon who then runs that command in the relevant workspace. When the command is complete, the daemon triggers a callback to the ai to let it know the command has finished."

## User Scenarios & Testing *(mandatory)*

Some commands cannot run usefully inside a sandbox. `pnpm install` wants the real package cache and
network; end-to-end tests want real hardware, not a microVM. Today an AI agent working inside a
sandbox has no way to run these — it either fails, or produces a meaningless result. **Escape Hatch**
gives the sandbox's AI a small, human-authored set of commands it is allowed to run *outside* the
sandbox: the developer lists them on an agent kit, and attaching that kit turns each into an
AI-invokable command plus a rule teaching the agent when to reach for it. When the agent invokes one,
the supervising switchboard daemon runs the exact predefined command on the host, in the sandbox's
workspace, and notifies the agent of the outcome so it can carry on — even if no terminal is watching.
The escape hatch is a deliberate, controlled break in the sandbox's isolation: only whole, named,
pre-authorized commands can cross it, and the developer decides which ones run automatically and which
require their explicit approval.

### User Story 1 - Author escape-hatch commands on a kit (Priority: P1)

A developer editing an agent kit adds an **escape-hatch commands** section. For each command they give
a short name (e.g. "install-deps"), the exact command to run (`pnpm install`), a plain-language note on
when the agent should use it, and whether it runs automatically or requires the developer's approval
each time. They save the kit; the section is validated alongside the rest of the kit and stored with it.

**Why this priority**: Nothing else in the feature exists without an authored list. The list *is* the
security boundary — the set of things the AI is permitted to run outside the sandbox — so getting its
authoring right is foundational.

**Independent Test**: Open the kit editor, add one escape-hatch command with each field, save, reopen —
the command and all its fields persist and the kit still validates. Delivers value on its own: a
reviewable, shareable declaration of what a sandbox's AI may run on the host.

**Acceptance Scenarios**:

1. **Given** the kit editor, **When** the developer adds an escape-hatch command with a name, an exact
   command string, a when-to-use note, and a consent mode, **Then** it is saved with the kit and shown
   on reopen.
2. **Given** an escape-hatch command marked "requires approval", **When** the kit is saved and reopened,
   **Then** its consent mode is preserved.
3. **Given** a partially edited escape-hatch entry, **When** the developer abandons the edit, **Then**
   the stored kit is unchanged.

---

### User Story 2 - AI runs an auto-run command and continues (Priority: P1)

An AI agent working in a sandbox reaches a point where it needs dependencies installed — something the
sandbox can't do meaningfully. It invokes the "install-deps" escape-hatch command. The supervising
daemon runs `pnpm install` on the host in that sandbox's workspace. When it finishes, the agent is
handed the result — success or failure, plus the command's output — and continues its task using the
now-installed dependencies. If the developer had closed the terminal, the install still runs to
completion and the result still reaches the agent.

**Why this priority**: This is the headline capability — an out-of-sandbox command running to completion
and its result flowing back to the agent unattended.

**Independent Test**: With a kit declaring one auto-run command attached to a sandbox, have the agent
invoke it; confirm the exact command ran on the host in the sandbox's workspace, its effects are visible
to the sandbox, and the agent received the outcome and output — including when no terminal is attached.

**Acceptance Scenarios**:

1. **Given** a sandbox whose attached kit declares an auto-run escape-hatch command, **When** the agent
   invokes it, **Then** the exact predefined command runs on the host in that sandbox's workspace and the
   agent is notified of the result (exit status + output).
2. **Given** an escape-hatch command that installs dependencies, **When** it completes on the host,
   **Then** its effects are visible to the sandbox's AI on its next action.
3. **Given** an escape-hatch command still running, **When** the developer detaches every terminal from
   the sandbox, **Then** the command continues and its result is still delivered to the agent.
4. **Given** an escape-hatch command that exits non-zero, **When** it finishes, **Then** the agent is
   told it failed and given the output, rather than the invocation stalling or being reported as success.

---

### User Story 3 - The AI knows when and how to use the commands (Priority: P2)

When a kit with escape-hatch commands is attached to a sandbox, the agent's context gains a rule
enumerating the available escape-hatch commands, describing what each is for and when to use it, and
stating that they run on the host outside the sandbox. As a result the agent reaches for
"install-deps" instead of trying to run `pnpm install` inside the sandbox, and doesn't invent
out-of-sandbox commands that don't exist.

**Why this priority**: Without the rule, the commands exist but the agent doesn't know they're there or
when they apply, so it keeps doing the wrong thing inside the sandbox. It builds on US1/US2 but is a
distinct, separately verifiable slice.

**Independent Test**: Attach a kit with escape-hatch commands to a sandbox; inspect the agent's context
and confirm it lists exactly the attached commands with their when-to-use guidance and a note that they
run on the host. Behaviourally, give the agent a task requiring one and confirm it invokes the escape
hatch rather than attempting the equivalent in the sandbox.

**Acceptance Scenarios**:

1. **Given** a kit with escape-hatch commands attached, **When** the agent starts, **Then** its context
   lists exactly those commands, each with its when-to-use guidance and a statement that it runs outside
   the sandbox.
2. **Given** a task that requires an out-of-sandbox command, **When** the agent works on it, **Then** it
   invokes the matching escape-hatch command instead of running the equivalent inside the sandbox.
3. **Given** a sandbox with no escape-hatch commands attached, **When** the agent works, **Then** its
   context contains no escape-hatch rule and no escape-hatch commands are invokable.

---

### User Story 4 - Approval-gated commands (Priority: P2)

Some commands are too consequential to run unattended. The developer marks such a command
"requires approval". When the agent invokes it, execution pauses and the supervising developer is
prompted — shown the sandbox, the exact command, and that it will run on the host — and either approves
or denies. On approval the command runs and the agent is notified of the result; on denial (or if the
developer never responds) the command does not run and the agent is told it was declined.

**Why this priority**: It makes the escape hatch usable for higher-risk commands without surrendering the
autonomy of the low-risk ones. It depends on US2's execution path but adds an independent gate.

**Independent Test**: Attach a kit whose command requires approval; have the agent invoke it; confirm no
host execution happens until the developer approves, that approving runs it and returns the result, and
that denying (or not responding) results in no execution and a "declined" result to the agent.

**Acceptance Scenarios**:

1. **Given** an approval-required command, **When** the agent invokes it, **Then** no host execution
   occurs until the developer explicitly approves.
2. **Given** an approval prompt, **When** the developer approves, **Then** the command runs and the agent
   receives its result.
3. **Given** an approval prompt, **When** the developer denies it or does not respond within the allowed
   window, **Then** the command does not run and the agent is told it was declined.
4. **Given** an approval prompt, **When** unrecognized input is received, **Then** it is NOT treated as
   approval (deny-by-default).

---

### User Story 5 - Observe and audit escape-hatch runs (Priority: P3)

A developer supervising one or more sandboxes can see escape-hatch activity: which command is running on
which sandbox, and, once finished, its outcome and output. After the fact they can review what the
agent ran outside the sandbox during the session.

**Why this priority**: Because the escape hatch crosses the isolation boundary, visibility and an audit
trail matter — but the core capability works without a rich UI, so this is lower priority.

**Independent Test**: Trigger an escape-hatch run and confirm it appears while in progress and that its
outcome (command, sandbox, status, exit code, output) is retrievable afterward in the current session.

**Acceptance Scenarios**:

1. **Given** an in-progress escape-hatch run, **When** the developer looks at the sandbox list (or
   notifications), **Then** they can see a run is in progress and for which command/sandbox.
2. **Given** a completed escape-hatch run, **When** the developer reviews it, **Then** they can see the
   command, the sandbox, the outcome, exit status, and captured output.

---

### Edge Cases

- **Command not on the allowlist**: If the agent tries to invoke a command that is not declared on a
  kit attached to its sandbox, it MUST be rejected with nothing run on the host, and the agent told the
  command is unavailable.
- **Non-zero exit / command failure**: Reported to the agent as a failure with output and exit status;
  the agent decides what to do next. Not surfaced as an internal error that stalls the run.
- **Command hangs / never finishes**: Bounded by a maximum duration (per-command or a default), after
  which it is terminated and reported to the agent as timed-out.
- **Command cannot be launched** (not found, not executable, misconfigured on the host): Reported as a
  failed run with diagnostics; never silently dropped.
- **Sandbox stopped or destroyed mid-run**: The in-progress run is cancelled and recorded as cancelled;
  no orphaned host process is left running.
- **Terminal detached mid-run**: The run continues to completion and its result is still delivered
  (mirrors detached AI-prompt behaviour from the terminal-persistence feature).
- **Concurrent invocations**: Multiple escape-hatch runs (same or different commands, same or different
  sandboxes) may be in flight; each runs independently and its output is captured separately without
  interleaving. Completion order is not guaranteed.
- **Very large output**: Captured output is bounded; when it exceeds the limit it is truncated (with the
  truncation made evident) while remaining retrievable where the command ran.
- **Remote host**: For a sandbox on a remote (ssh) host, the command runs on that daemon's host — always
  the host that supervises the sandbox, never the client machine or the microVM.
- **Approval never answered**: Treated as a denial once the approval window (default 5 minutes) elapses;
  the agent is told it was declined.
- **Kit attached to multiple sandboxes**: Each invocation runs in its own sandbox's workspace; runs are
  never shared or cross-wired between sandboxes.

## Clarifications

### Session 2026-07-23

- Q: When two kits attached to the same sandbox declare an escape-hatch command with the same name, how is the collision resolved? → A: Later-attached kit wins — its command overrides (shadows) the same-named command from an earlier-attached kit.
- Q: Do escape-hatch run records survive a daemon restart, or are they in-memory only? → A: In-memory only — run history lives for the daemon's lifetime; a daemon restart clears it ("session" = daemon uptime).
- Q: What is the default maximum run duration when a command does not specify one? → A: 30 minutes.
- Q: How long is the approval window for a requires-approval command before it is treated as denied? → A: 5 minutes.

## Requirements *(mandatory)*

### Authoring (extends the kit editor)

- **FR-035**: A kit MUST be able to declare an ordered list of **escape-hatch commands**, each with: a
  stable human-readable name; the **exact command to run** (fixed — the agent supplies no arguments and
  cannot alter the string); a plain-language description of when/why the agent should use it; a **consent
  mode** of either *auto-run* or *requires-approval*; and optionally a working directory relative to the
  workspace and a maximum run duration. The client MUST let developers add, edit, and remove these
  entries from the kit editor, validate them with the rest of the kit, and MUST NOT mutate the stored kit
  on an abandoned edit.
- **FR-036**: Escape-hatch commands MUST be attached to a sandbox by attaching the kit that declares
  them (at creation or to a running sandbox) and MUST be persisted with the sandbox so a container
  recreate replays the same command set. A sandbox's set of invokable escape-hatch commands MUST be
  exactly those declared on its attached kits — no more. When two attached kits declare a command with
  the same name, the more-recently-attached kit's command MUST override (shadow) the earlier one, so a
  given name always resolves to exactly one command.

### Agent enablement

- **FR-037**: Attaching a kit with escape-hatch commands MUST turn each declared command into a
  discrete command the sandbox's AI agent can invoke, and MUST inject a rule into the agent's context
  that enumerates the available commands, states for each what it does and when to use it, and makes
  clear they execute on the host outside the sandbox. When no escape-hatch commands are attached, no
  such commands are invokable and no such rule is present.

### Execution

- **FR-038**: When the agent invokes an available escape-hatch command, the system MUST execute the
  **exact predefined command** on the host of the daemon supervising that sandbox — outside the sandbox —
  in the sandbox's workspace. The command MUST see the sandbox's current workspace contents, and its
  effects on the workspace MUST become visible to the sandbox. The agent MUST NOT be able to change the
  command, append arguments, or otherwise run anything other than the whole, named, predefined commands;
  the escape hatch MUST NOT expose a general shell on the host.
- **FR-039**: Consent MUST be enforced per command: *auto-run* commands execute without per-invocation
  approval; *requires-approval* commands MUST pause before any host execution and obtain the supervising
  developer's explicit approval — the prompt naming the sandbox, the exact command, and that it runs on
  the host — and MUST NOT execute if denied or if approval is not granted within a bounded window
  (default **5 minutes**), after which the run is treated as denied. Unrecognized input MUST NOT be read
  as approval (deny-by-default).
- **FR-040**: On every terminal outcome — success, non-zero exit, timeout, cancellation, launch failure,
  or denial — the system MUST notify the invoking agent with the outcome and, where the command ran, its
  exit status and captured output (or, where it did not, the reason). A run in progress MUST continue and
  still deliver its result even when no terminal is attached to the sandbox.
- **FR-041**: Each run MUST be bounded by a maximum duration (the command's own, or a system default of
  **30 minutes** when the command specifies none), after which it is terminated and reported as timed-out. In-progress runs MUST be cancelled when their
  sandbox is stopped or destroyed, leaving no orphaned host process. Captured output MUST be bounded, with
  truncation made evident. A command that cannot be launched MUST be reported as a failed run with
  diagnostics, never silently dropped.

### Observability

- **FR-042**: The client MUST surface escape-hatch runs to the supervising developer: in-progress runs
  (which command, which sandbox) MUST be visible on the sandbox list or notifications, and each run's
  outcome (command, sandbox, status, exit status, captured output) MUST be retrievable for review within
  the session. Run records are held in memory for the daemon's lifetime ("session" = daemon uptime) and
  need not survive a daemon restart; they are not persisted to the registry.

### Key Entities

- **Escape-Hatch Command**: A single authorized command declared on a kit. Attributes: name; exact
  command string (fixed); when-to-use description; consent mode (auto-run | requires-approval); optional
  workspace-relative working directory; optional maximum duration. It is a member of a kit and inherits
  the kit's lifecycle (authored, validated, attached, persisted).
- **Escape-Hatch Run**: One execution of a command triggered by the sandbox's agent (the sandbox
  identifies the agent, since there is one per sandbox). Attributes: the command it ran; the sandbox and
  its workspace; status (pending-approval | running | succeeded | failed | timed-out | cancelled |
  denied); start/end times; exit status; captured (bounded) output.
- **Agent Escape-Hatch Rule**: The guidance derived from a sandbox's attached escape-hatch commands and
  injected into the agent's context (the enumerated commands, their when-to-use notes, and the
  runs-on-host statement).
- **Kit** *(existing)*: The client-authored provisioning unit that now also carries escape-hatch commands.
- **Sandbox / Workspace** *(existing)*: The isolated environment and its host-side workspace copy in
  which escape-hatch commands run.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can add an escape-hatch command to a kit and make it available to a sandbox's
  AI entirely through the client, without hand-editing any files.
- **SC-002**: When an agent invokes an auto-run command, it receives the outcome (exit status + output)
  with no human intervention and can continue its task in the same session.
- **SC-003**: 100% of invocations of a *requires-approval* command that are denied or left unanswered
  result in **zero** host execution.
- **SC-004**: The agent cannot cause any command other than the exact predefined ones to run on the
  host — **zero** arbitrary or argument-injected executions across testing.
- **SC-005**: A command that outlives every attached terminal still completes and its result reaches the
  agent — **zero** results lost to terminal detachment.
- **SC-006**: For any escape-hatch run in the current session, a supervising developer can identify the
  command, the sandbox, and the outcome.
- **SC-007** *(post-launch evaluation metric — not gated by the build)*: In evaluation, when a task
  requires an out-of-sandbox command that an attached kit provides, the agent chooses the escape hatch
  rather than attempting the equivalent inside the sandbox in at least 90% of cases. This is a
  behavioural outcome measured against a live agent after ship (its levers are the injected rule's
  content and the wrapper's discoverability, covered by T036/T037), not a unit-testable build gate.
- **SC-008**: A hung command is terminated and reported as timed-out within its configured (or default)
  duration in 100% of cases, leaving no orphaned host process.

## Key Decisions

1. **Configurable consent per command** (not global): each command is authored as *auto-run* or
   *requires-approval*, so routine commands (`pnpm install`) run unattended while consequential ones stay
   human-gated — without forcing every escape hatch through one policy.
2. **Fixed, exact commands — no AI-supplied arguments**: the agent chooses *which* authorized command to
   run and *when*, never *what* the command is. This keeps the boundary an allowlist of whole commands
   rather than a template with an injection surface.
3. **Escape-hatch commands live on the kit** (extends feature 004): kits are already the client-owned,
   attachable unit of sandbox provisioning and the natural home for "how this sandbox's agent operates".
   One authored list yields both the invokable AI commands and the agent rule.
4. **Execution is on the supervising daemon's host, in the sandbox's workspace** (not the client, not the
   microVM): "outside the sandbox but against its files" is the whole point — dependency installs and
   real-hardware tests must both see the sandbox's current work and hand their effects back to it.
5. **Results are delivered on completion, and delivery survives detachment** (mirrors detached AI prompts
   from feature 003): the agent is a first-class recipient of the outcome, so escape-hatch work fits
   unattended/overnight agent runs.
6. **The allowlist is the containment**: the commands are trusted because a human authored them; the
   feature deliberately narrows the AI's out-of-sandbox power to selecting from that human-authored set.

## Assumptions

- This feature builds on **feature 004 (Agent Kits)** — client-authored kits, edited in the kit editor,
  materialized on the daemon host — and on the daemon's existing **agent-interaction and notification**
  mechanisms from **features 003/004** (prompting an agent, delivering asynchronous results, work that
  continues with no terminal attached). Escape-hatch commands are a new section of the kit definition and
  are exposed to the agent through the same tool/command + notification channel the agent already uses.
- "The relevant workspace" is the sandbox's host-side workspace in the controlled folder, shared with the
  sandbox such that changes are mutually visible. If a sandbox's workspace is not shared bidirectionally
  with the host, the feature's value (e.g. installed dependencies reaching the sandbox) would not hold;
  FR-038 states this as a requirement rather than leaving it implicit.
- The execution host is the host of the daemon supervising the sandbox (local, or the remote ssh host for
  a remote sandbox). Commands run under the same OS user and permissions as that daemon; the feature does
  not introduce a separate privilege model.
- Escape-hatch commands are non-interactive (no TTY prompt the agent must answer mid-run); their contract
  is "run to completion, then report".
- Output capture is bounded and may be truncated; a default maximum run duration applies when a command
  does not specify one.
- The AI agent runtime can be given discrete named commands to invoke and can receive their results
  asynchronously (as it already does for prompts); the exact agent runtime is out of scope for this spec.

## Out of Scope

- A general or freeform shell on the host for the AI (explicitly excluded — only whole, named,
  pre-authorized commands run).
- AI-supplied arguments or parameterized command templates (deferred; v1 is fixed commands only).
- Streaming a command's partial output back to the agent mid-run (v1 delivers the result on completion;
  the developer may still watch progress via observability).
- Interactive commands that require input from the agent while running.
- Changing how sandboxes, kits, or the daemon are otherwise provisioned or transported.

## Dependencies

- **Feature 004 — Sandbox Refresh & Agent Kits**: kit authoring, kit attachment at creation and to a
  running sandbox, and kit materialization on the daemon host.
- **Feature 003 — Terminal Session Persistence**: agent prompting, asynchronous result delivery, and
  agent work that continues after terminal detachment.
