# Feature Specification: Terminal Session Persistence & Sandbox Tags

**Feature Branch**: `003-terminal-session-persistence`

**Created**: 2026-07-08

**Status**: Draft

**Input**: User description: "We want to introduce terminal session persistence. The daemon needs to persist each running sandbox instance so users can disconnect and reconnect and see what was previously typed; if they prompt the AI kit then close the terminal the AI prompt should continue to run until complete. Opening the terminal in the TUI (lowercase t) should quickly navigate the user to a terminal inside the TUI to review what happened then navigate back, rather than stopping the TUI, opening the sandbox, and rerunning the TUI. Track how many terminals are open/connected per sandbox and surface it on the sandbox list page; allow only one external terminal at a time — hitting T (Shift+T) on a sandbox already open in another terminal should bring that terminal to the forefront rather than open a new one; an external terminal and a TUI terminal open at the same time is acceptable but not required. If `sxb` is run from inside a switchboard working directory, immediately open the terminal to the active session for that sandbox. Additionally: let users tag a sandbox — a transient, changeable name that identifies purpose (distinct from the permanent unique sandbox name), does not affect the sandbox, and is visible on the sandbox list page."

## User Scenarios & Testing *(mandatory)*

A developer runs many sandboxes at once through switchboard. Terminals attached to those
sandboxes are no longer throwaway windows tied to a single view: the daemon keeps each running
sandbox's terminal session alive, so the developer can drop out of a session (close a terminal,
switch machines, or open the workspace in VSCode) and jump back in later to find their work — and
any AI agent they kicked off — still running, with their prior output waiting for them. Alongside
this, developers can attach a short, changeable **tag** to any sandbox to record what it is *for*,
without touching the sandbox's permanent identity.

### User Story 1 - Drop out and jump back into a running session (Priority: P1)

A developer is working in a sandbox terminal — typing commands, and prompting the sandbox's AI
coding kit. They close the terminal (intentionally, or because their laptop sleeps, or because they
switch to another machine). The sandbox and the AI prompt keep running on the host. Later they
reconnect to that sandbox's terminal and see the prior output — what they typed and what has been
produced since — and pick up exactly where they left off.

**Why this priority**: This is the headline capability and the reason the feature exists. Without
persistent sessions, closing a terminal loses in-progress work and interrupts long-running AI
tasks — the single biggest friction in running many parallel sandboxes. It is a complete,
demonstrable MVP on its own: persistence plus reconnect-with-history delivers value even before any
of the navigation conveniences below.

**Independent Test**: In a running sandbox, start a long-running command (or an AI prompt that takes
time to finish), close/detach the terminal, confirm from the sandbox itself that the process keeps
running to completion, then reconnect and confirm the terminal shows the earlier output plus the
result produced while detached.

**Acceptance Scenarios**:

1. **Given** a running sandbox with a terminal session, **When** the developer detaches or closes
   the terminal while a command or AI prompt is still running, **Then** the sandbox's shell and that
   process continue running to completion, unaffected by the terminal closing.
2. **Given** a sandbox whose terminal was previously detached, **When** the developer reconnects to
   it, **Then** they are attached to the same ongoing session and shown its prior output reflecting
   the current state, not a blank or freshly started shell.
3. **Given** a sandbox terminal session, **When** the developer detaches and reconnects repeatedly,
   **Then** every reconnect attaches to the same continuous session rather than spawning a new one.
4. **Given** an AI prompt running in a sandbox with no terminal currently attached, **When** the
   prompt completes, **Then** its work finishes and its output is available to view on the next
   reconnect.

---

### User Story 2 - Review a session inside the TUI without leaving it (Priority: P2)

A developer viewing the sandbox list presses **t** (lowercase) on a sandbox. The TUI navigates
directly to a terminal view for that sandbox, where the developer can review what has already
happened in the session and interact with it, then navigates back to the list — all without the TUI
being torn down and relaunched.

**Why this priority**: The current behavior (stop the TUI, open the sandbox, rerun the TUI when
done) is jarring and slow. An in-place terminal view makes glancing at a session as cheap as moving
between list rows, which is what makes managing many sandboxes practical. It depends on persistent
sessions (P1) existing to attach to.

**Independent Test**: From the sandbox list, press t on a running sandbox, confirm a terminal view
for that sandbox appears within the same TUI (the TUI process is not restarted), confirm prior
session output is visible and the session is interactive, then return to the list and confirm the
list is exactly as it was.

**Acceptance Scenarios**:

1. **Given** the sandbox list, **When** the developer presses t on a running sandbox, **Then** the
   TUI shows that sandbox's terminal session in-place without stopping and relaunching the TUI.
2. **Given** the in-TUI terminal view, **When** it opens, **Then** the developer can see the
   session's prior output/scrollback and interact with the live session.
3. **Given** the in-TUI terminal view, **When** the developer chooses to go back, **Then** the TUI
   returns to the previous screen and the session keeps running in the background.
4. **Given** the developer moves between the list and the in-TUI terminal repeatedly, **When** they
   do so, **Then** navigation is quick and does not restart or reset the TUI.

---

### User Story 3 - One external terminal per sandbox, with visible connection count (Priority: P2)

A developer presses **T** (Shift+T) on a sandbox to open it in an external terminal window. If an
external terminal is already open for that sandbox, pressing T again brings the existing window to
the foreground instead of opening a second one. The sandbox list shows, per sandbox, how many
terminals are currently connected to its session, so the developer can see at a glance which
sandboxes are actively being viewed.

**Why this priority**: External terminals are the "full screen real estate" escape hatch, and
uncontrolled duplicate windows for the same session are confusing and error-prone. Surfacing
connection counts turns the daemon's session tracking into something the developer can act on. It
builds on persistence (P1) and the daemon's awareness of attached clients.

**Independent Test**: Press T on a running sandbox and confirm one external terminal opens attached
to its session; press T again on the same sandbox and confirm no second window opens and the
existing one is brought forward; confirm the sandbox list reflects the connected-terminal count for
that sandbox and updates when the terminal is closed.

**Acceptance Scenarios**:

1. **Given** a running sandbox with no external terminal open, **When** the developer presses T,
   **Then** an external terminal opens attached to that sandbox's persistent session.
2. **Given** a sandbox that already has an external terminal open, **When** the developer presses T
   again, **Then** no additional external terminal is opened and the existing external terminal is
   brought to the foreground.
3. **Given** any running sandbox, **When** terminals attach to or detach from its session, **Then**
   the sandbox list page reflects the current number of connected terminals for that sandbox.
4. **Given** a sandbox with an in-TUI terminal already attached, **When** the developer opens an
   external terminal for the same sandbox, **Then** both may be attached to the same session at once
   (permitted but not required by this feature).

---

### User Story 4 - Auto-open the session from a workspace directory (Priority: P3)

A developer opens a sandbox's workspace directory in VSCode and uses the VSCode integrated terminal.
From that terminal (which is positioned inside the sandbox's working directory) they run `sxb`.
Instead of showing the general TUI, `sxb` immediately connects them to the active terminal session
for the sandbox that owns that directory.

**Why this priority**: This makes the persistent session reachable through the natural VSCode path,
without the developer having to find the sandbox in the TUI first. It is a convenience layered on
top of persistence and session addressing, so it comes after the core flows.

**Independent Test**: From within a switchboard-managed sandbox working directory, run `sxb` and
confirm it lands directly in that sandbox's active terminal session rather than the general TUI.

**Acceptance Scenarios**:

1. **Given** a shell whose working directory is inside a switchboard-managed sandbox workspace,
   **When** the developer runs `sxb`, **Then** it opens directly into that sandbox's active terminal
   session.
2. **Given** a shell inside a nested subdirectory of a sandbox workspace, **When** the developer
   runs `sxb`, **Then** it still resolves to and opens the owning sandbox's session.
3. **Given** a shell that is not inside any switchboard-managed workspace, **When** the developer
   runs `sxb`, **Then** it behaves as it does today (opens the general TUI).

---

### User Story 5 - Tag a sandbox to record its purpose (Priority: P3)

A developer gives a sandbox a short tag such as "auth-refactor" or "flaky-test-repro" to remember
what it is for. The tag is separate from the sandbox's permanent unique name, can be changed or
cleared at any time, does not change the sandbox in any way, and shows up on the sandbox list.

**Why this priority**: With many similarly-named sandboxes, a human-meaningful purpose label makes
the list scannable. It is independent of the terminal-persistence work and small in scope, so it is
lowest priority but self-contained.

**Independent Test**: Assign a tag to a sandbox, confirm it appears on the sandbox list; change the
tag and confirm the list updates; clear the tag and confirm it is removed — and confirm through all
of this the sandbox's state (running/stopped) and identity are unchanged.

**Acceptance Scenarios**:

1. **Given** any sandbox, **When** the developer assigns it a tag, **Then** the tag is stored for
   that sandbox and displayed on the sandbox list page.
2. **Given** a sandbox with a tag, **When** the developer changes or clears the tag, **Then** the
   new value (or its absence) is reflected on the list, and no other sandbox attribute changes.
3. **Given** two different sandboxes, **When** the developer gives them the same tag, **Then** both
   accept it (tags need not be unique) and each remains individually identifiable by its permanent
   name/id.
4. **Given** a sandbox with a tag, **When** it is stopped and later restarted, **Then** its tag is
   retained.

---

### Edge Cases

- **Very long detachment**: A session reattached after a long time still shows the current session
  state; if retained scrollback has a bound, the oldest output may be truncated, but the most recent
  output and current screen are always presented.
- **Reconnect after daemon restart**: If the daemon restarts while a sandbox is running, the sandbox
  (and any AI work running inside it) keeps running and is re-adopted (per the existing re-adoption
  behavior), and the developer can reattach to its terminal session on reconnect. Output produced
  during a daemon outage window may not be fully replayable, but the running work is never lost.
- **Sandbox stopped while a terminal is attached**: Stopping the sandbox ends its terminal session;
  any attached in-TUI or external terminal clearly indicates the session has ended rather than
  appearing frozen.
- **External terminal for a remote-host sandbox**: The external terminal is opened on the machine
  running the TUI and connects to the session through the existing daemon connection; the
  one-external-terminal-per-sandbox rule still applies.
- **Bring-to-front when the external terminal cannot be focused**: If the previously opened external
  terminal can no longer be located or focused (e.g., it was closed out-of-band), pressing T opens a
  new external terminal rather than failing.
- **`sxb` from a stopped or unknown sandbox directory**: Running `sxb` inside a workspace whose
  sandbox is stopped or no longer known surfaces an actionable message (and/or falls back to the
  general TUI) rather than hanging or erroring opaquely.
- **Two attached clients of different window sizes**: When both an in-TUI terminal and an external
  terminal (or two viewers) are attached, the session remains usable for both; how differing sizes
  are reconciled is an implementation concern, not a change to the guarantees here.
- **Tag vs. name confusion**: A tag never overrides or replaces the sandbox's permanent unique name;
  both are shown, and the tag is clearly the mutable, purpose-describing field.

## Requirements *(mandatory)*

### Functional Requirements

**Session persistence (daemon)**

- **FR-001**: The daemon MUST maintain a persistent terminal session for each running sandbox that
  continues independently of whether any client (an in-TUI terminal viewer or an external terminal)
  is currently attached to it.
- **FR-002**: Detaching from or closing a terminal MUST NOT interrupt the sandbox's shell or any
  process running in the session; in-progress work — including an AI/agent kit prompt — MUST
  continue to run to completion with no terminal attached.
- **FR-003**: On reconnecting to a running sandbox, the developer MUST be attached to that sandbox's
  existing session and be shown its prior output reflecting the current state of the session, so
  they can resume where they left off (not a blank screen or a new shell).
- **FR-004**: A sandbox's persistent session MUST remain the same continuous session across repeated
  detach/reconnect cycles for the sandbox's running lifetime.
- **FR-005**: The session MUST retain enough recent output history that a reconnecting developer can
  review what has already happened in the session, not merely the final line of output.
- **FR-006**: Stopping a sandbox MUST end its terminal session; restarting the sandbox starts a new
  session. The persistent session is tied to the running sandbox, not preserved across a stop.

**Connection tracking**

- **FR-007**: The daemon MUST track, per sandbox, how many terminals are currently attached/connected
  to its session, and MUST make this count available to the TUI.
- **FR-008**: The sandbox list page MUST display, per sandbox, the current number of connected
  terminals, and MUST update as terminals attach and detach.

**In-TUI terminal viewer (t)**

- **FR-009**: Pressing **t** (lowercase) on a sandbox in the TUI MUST open a terminal view for that
  sandbox's session in-place, without stopping and relaunching the TUI process.
- **FR-010**: The in-TUI terminal view MUST present the session's prior output/scrollback for review
  and MUST allow the developer to interact with the live session.
- **FR-011**: From the in-TUI terminal view, the developer MUST be able to return to the previous
  TUI screen (e.g., the sandbox list) quickly, leaving the session running in the background.
- **FR-012**: Navigating into and out of the in-TUI terminal view MUST NOT reset or restart the TUI,
  and the prior TUI screen state MUST be preserved on return.

**External terminal (T / Shift+T)**

- **FR-013**: Pressing **T** (Shift+T) on a sandbox MUST open its persistent session in an external
  terminal.
- **FR-014**: At most one external terminal MAY be open per sandbox at a time.
- **FR-015**: When an external terminal is already open for a sandbox, pressing T again MUST bring
  the existing external terminal to the foreground instead of opening a second one.
- **FR-016**: The system MAY allow an external terminal and the in-TUI terminal to be attached to the
  same session simultaneously; this is permitted but not required.

**Auto-open from workspace directory (`sxb`)**

- **FR-017**: When `sxb` is invoked with its working directory inside a switchboard-managed sandbox
  workspace, it MUST open directly into that sandbox's active terminal session instead of the general
  TUI.
- **FR-018**: The system MUST resolve the owning sandbox for a working directory even when `sxb` is
  invoked from a nested subdirectory of that workspace.
- **FR-019**: When `sxb` is invoked outside any switchboard-managed workspace, it MUST retain its
  current behavior (open the general TUI).
- **FR-020**: When `sxb` is invoked inside a workspace whose sandbox is stopped or not known, it MUST
  surface an actionable outcome (informative message and/or fallback to the general TUI) rather than
  hanging or failing opaquely.

**Sandbox tags**

- **FR-021**: The developer MUST be able to assign a tag — a short, human-readable label — to any
  sandbox, and MUST be able to change or clear it at any time.
- **FR-022**: A tag MUST be distinct from the sandbox's permanent unique name/identifier: it is
  mutable, need not be unique across sandboxes, and MUST NOT affect the sandbox's identity,
  lifecycle, state, or behavior in any way.
- **FR-023**: Tags MUST be displayed on the sandbox list page alongside each sandbox's permanent
  name.
- **FR-024**: A sandbox's tag MUST persist across TUI restarts and across the sandbox being stopped
  and restarted.

### Key Entities *(include if feature involves data)*

- **Terminal Session**: A persistent shell session tied to a single running sandbox, maintained by
  the daemon independently of any attached client. Carries the current screen state plus retained
  recent output history for reconnect, and tracks the set of clients currently attached (which yields
  the connection count). Exists for the sandbox's running lifetime; ends when the sandbox stops.
- **Terminal Attachment (Client)**: A connection to a terminal session — either the in-TUI terminal
  viewer or an external terminal. Attachments are counted per sandbox and are the basis for the
  one-external-terminal-per-sandbox rule and the list-page connection count.
- **Sandbox Tag**: A mutable, non-unique, human-readable label attached to a sandbox to describe its
  purpose. Independent of the permanent unique sandbox name/id; display-only; changeable or clearable
  at will; persists across restarts.
- **Workspace → Sandbox Association**: The mapping from a filesystem working directory (and its
  subdirectories) to the sandbox that owns it, used to resolve which sandbox's session `sxb` should
  open when run from within a workspace.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: When a terminal is closed while an AI prompt or long-running command is in progress,
  that process completes 100% of the time, with its result available on the next reconnect.
- **SC-002**: After a detach/reconnect cycle on a running sandbox, the reattached terminal shows the
  session's prior output reflecting current state — never a blank screen or a freshly started shell
  — 100% of the time.
- **SC-003**: Opening the in-TUI terminal for a sandbox and returning to the sandbox list each
  complete in under 2 seconds and never restart the TUI, with the list restored to its prior state.
- **SC-004**: Pressing T on a sandbox that already has an external terminal open never results in a
  second external terminal for that sandbox; the existing window is brought to the foreground (or,
  only if it cannot be located, a single replacement is opened).
- **SC-005**: The connected-terminal count shown on the sandbox list matches the actual number of
  attached terminals for each sandbox within one refresh of a terminal attaching or detaching.
- **SC-006**: Running `sxb` from inside a switchboard-managed sandbox workspace lands the developer
  in that sandbox's active terminal session in a single step, with no manual navigation through the
  TUI, 100% of the time the owning sandbox is running.
- **SC-007**: A tag can be set, changed, and cleared on a sandbox, is reflected on the sandbox list
  within one refresh, and results in zero change to the sandbox's state or identity across all such
  operations.
- **SC-008**: A sandbox's tag is still present and correct after the TUI is restarted and after the
  sandbox is stopped and restarted, 100% of the time.

## Assumptions

- **Builds on the Sandbox Session Manager (feature 001)**: This feature extends the existing
  switchboard daemon (`sxbd`), Bubble Tea TUI (`sxb`), sandbox lifecycle, multi-host/SSH connectivity,
  and daemon re-adoption of still-running sandboxes. Terms (sandbox, daemon, TUI, host, workspace,
  configuration) carry their meaning from feature 001.
- **AI work runs inside the sandbox**: The AI/agent kit executes inside the sandbox container, so its
  continued execution when no terminal is attached follows from the sandbox continuing to run; the
  terminal session provides the view of that work, not the work itself.
- **Reconnect history scope**: "See what was previously typed / review what happened" is satisfied by
  presenting the current session state plus a bounded amount of recent scrollback. Retaining the
  complete session history without bound is not required; the exact buffer size is deferred to
  planning.
- **One external terminal is per sandbox**: The "only one external terminal at a time" rule is
  interpreted as one external terminal per sandbox (each sandbox may have at most one open external
  terminal), not one external terminal across all sandboxes combined.
- **External terminals open on the client machine**: Pressing T opens a terminal on the machine
  running the TUI (which then connects to the session over the daemon connection), including for
  sandboxes on remote hosts. Bringing a terminal "to the foreground" is local window/process
  management on that machine.
- **Simultaneous TUI + external attachment is optional**: Allowing both to attach to one session at
  once is desirable but explicitly not required for this feature; if it is not technically
  straightforward, restricting to one active attachment at a time is acceptable.
- **Tag storage**: A tag is user-facing display metadata on a sandbox and persists across restarts;
  whether it is stored client-side (like configurations/groups in 001) or by the daemon is an
  implementation decision deferred to planning, provided the persistence and non-interference
  guarantees hold.
- **Workspace detection**: switchboard can determine, from a working directory path, which sandbox
  (if any) owns it — leveraging the daemon-controlled workspace folders established in feature 001.
- **Single developer per session**: Consistent with feature 001, the model is one developer driving
  their sandboxes; multi-user concurrent shared terminals are not a goal of this feature (though
  multiple attachments by the same developer are).
