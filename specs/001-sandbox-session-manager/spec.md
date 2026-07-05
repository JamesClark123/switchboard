# Feature Specification: Sandbox Session Manager (Switchboard)

**Feature Branch**: `001-sandbox-session-manager`

**Created**: 2026-06-24

**Status**: Draft

**Input**: User description: "We want to build a TUI for managing docker sandbox sessions across one or many computers. From a high level this feature has two core deliverables. One is a daemon that runs on each machine that needs to connect or support the switchboard project. This daemon manages starting and stopping docker sandbox sessions and interfacing with the tui. The other is the terminal user interface (TUI). This interface will allow a user to manage and group docker sandboxes, predefine configurations and prompt agents directly from the tui or otherwise launch vscode to view the changes or the coding agent in another terminal."

## Clarifications

### Session 2026-06-24

- Q: When a running sandbox is stopped (and later destroyed), what happens to its duplicated workspace copy? → A: Stopping keeps the copy — the sandbox stays listed in a stopped state and is **restartable**; the copy is removed only by an explicit destroy/remove action. Additionally, each workspace tracks and displays the configuration it was launched with (including the named-configuration label, when one was used) and the repositories/directories it was seeded with, so a developer scanning stopped and running sandboxes understands what each one is before restarting or stopping it.
- Q: How should "task complete" and "needs prompting" notifications reach the developer? → A: Both in-TUI (a persistent notification list/badges) and OS-level desktop notifications, so the developer is alerted even when the TUI is not focused; notifications missed while disconnected are surfaced on reconnect.
- Q: If a daemon process restarts while its docker sandboxes are still running, what should it do? → A: Re-adopt — the daemon persists its sandbox registry and re-attaches to already-running containers on restart so the TUI keeps managing them seamlessly.
- Q: Where should switchboard's user-level state live (saved configurations, groups, known hosts)? → A: Hybrid — the client (TUI) is the source of truth for saved configurations, groups, and the list of known hosts (so they are portable and can span multiple daemons); each daemon additionally persists a registry of the sandboxes it launched plus their configuration/seed metadata (powering re-adoption and offline display).
- Q: Is the coding agent that runs inside a sandbox fixed or selectable? → A: A configuration MAY specify the agent; when it does, that launch is fixed to the specified agent. When the configuration does not specify an agent (or no saved configuration is used), the user chooses the agent at launch.
- Q: How should a sandbox be named/identified? → A: Each sandbox gets a unique auto-generated id; its display name defaults to the named-configuration label (or a generated name when none), and the user MAY override it with a custom name.
- Q: Should switchboard guard against resource exhaustion when launching sandboxes? → A: No hard cap on sandbox count, but the system warns before a launch when the target host is low on disk or resources, allowing the user to override.

## User Scenarios & Testing *(mandatory)*

A developer uses a terminal user interface (the "switchboard") to spin up and manage many
isolated docker sandbox sessions at once — on their own machine and on other machines they
reach over SSH — so they can run multiple coding tasks in parallel without those tasks
interfering with each other or with their primary working copy.

### User Story 1 - Fan out parallel work by duplicating directories (Priority: P1)

A developer selects one or more local git projects/directories, chooses to **duplicate** them
(the default), and launches a new sandbox. The host's daemon copies the selected directories
into a daemon-controlled workspace folder, starts a docker sandbox seeded with those copies,
and the new sandbox appears in the TUI's list of running sandboxes. The developer repeats this
to create as many independent sandboxes as they want — each working from its own copy so the
originals are never touched.

**Why this priority**: This is the headline capability and the single most important aspect of
the project. Duplicating directories into daemon-owned copies is what lets a developer take on
many tasks simultaneously with zero risk to their source of truth. Without it, the rest of the
product has no reason to exist. It is a complete, demonstrable MVP on its own (single local
host, default duplicate mode).

**Independent Test**: On a single machine running a local daemon, select two directories, launch
a sandbox in duplicate mode, confirm a running sandbox appears, confirm the daemon-controlled
folder now holds copies of the chosen directories, and confirm the original directories are
byte-for-byte unchanged. Launch three more sandboxes the same way and confirm all four run
independently.

**Acceptance Scenarios**:

1. **Given** a connected local daemon and a developer who has selected one git project, **When**
   they launch a sandbox with the default (duplicate) seeding mode, **Then** the daemon copies
   that project into its controlled workspace folder, starts a docker sandbox seeded from the
   copy, and the sandbox appears as "running" in the TUI.
2. **Given** a developer who has selected several directories at once, **When** they launch a
   sandbox, **Then** all selected directories are seeded into that single sandbox.
3. **Given** an already-running sandbox created from a directory, **When** the developer launches
   additional sandboxes from the same source, **Then** each new sandbox gets its own independent
   duplicate and runs without affecting the others or the original directory.
4. **Given** a running sandbox, **When** the developer stops it from the TUI, **Then** the daemon
   stops the docker session, the sandbox moves to a "stopped" state while its duplicated copy is
   retained, and it remains listed (showing its configuration label and seeded repositories) as
   restartable.
5. **Given** a stopped sandbox, **When** the developer restarts it, **Then** the daemon starts a
   docker session from the retained copy and the sandbox returns to "running".
6. **Given** a stopped or running sandbox the developer no longer wants, **When** they explicitly
   destroy/remove it, **Then** the daemon removes the docker session and deletes its duplicated
   copy from the controlled folder.
7. **Given** a developer launching a sandbox, **When** they choose **clone** instead of duplicate,
   **Then** the sandbox is seeded using the sandbox's clone option rather than a directory copy.

---

### User Story 2 - Save and reuse sandbox configurations (Priority: P2)

A developer composes a sandbox configuration covering the full range of options the underlying
sandbox tooling ("sandbox kits") exposes, saves it under a name, and reuses it to launch future
sandboxes without re-entering every option.

**Why this priority**: Re-specifying every option on each launch is the main friction once
fan-out (P1) works. Saved configurations make repeated launches fast and consistent, which is
what makes "create as many sandboxes as you need" practical rather than tedious.

**Independent Test**: Create a configuration that sets several sandbox-kit options, save it under
a name, then launch a new sandbox by selecting that saved configuration and confirm the resulting
sandbox reflects exactly those options.

**Acceptance Scenarios**:

1. **Given** the configuration editor, **When** a developer sets any option the sandbox kits
   support, **Then** that option is editable in the TUI and is included when the sandbox launches.
2. **Given** a completed configuration, **When** the developer saves it with a name, **Then** it
   persists and is available for future launches.
3. **Given** a saved configuration, **When** the developer launches a sandbox from it, **Then**
   the daemon pipes those options through to the sandbox unchanged.
4. **Given** a saved configuration, **When** the developer edits and re-saves it, **Then** the
   updated values are used by subsequent launches while already-running sandboxes are unaffected.

---

### User Story 3 - Manage sandboxes across multiple hosts over SSH (Priority: P2)

A developer connects the TUI to a daemon on their localhost and to one or more remote daemons
over SSH at the same time, then views and manages running sandboxes across all connected hosts,
filtering or grouping the view by host.

**Why this priority**: Multi-host reach is a core differentiator — it lets a developer harness
several machines' resources from one interface. It builds directly on the single-host flows
(P1/P2) and is what turns "across one computer" into "across one or many computers."

**Independent Test**: With two daemons running (one local, one reachable via SSH), connect to
both from a single TUI, launch a sandbox on each, and confirm the TUI lists both and can show a
host-grouped view that attributes each sandbox to the correct host.

**Acceptance Scenarios**:

1. **Given** the TUI, **When** the developer connects to a daemon on localhost, **Then** that
   host's sandboxes become visible and manageable.
2. **Given** the TUI, **When** the developer connects to a remote daemon via SSH, **Then** that
   host's sandboxes become visible and manageable alongside any local ones.
3. **Given** connections to multiple daemons, **When** the developer chooses a per-host view,
   **Then** sandboxes are organized by the host they run on.
4. **Given** an active remote connection, **When** the connection is lost, **Then** the TUI
   clearly indicates that host is disconnected and does not present its sandboxes as actionable
   until reconnected.
5. **Given** a multi-host view, **When** the developer launches a sandbox, **Then** they can
   direct it to a specific connected host, and the duplicated copies land in that host's own
   daemon-controlled folder.

---

### User Story 4 - Prompt agents and get notified (Priority: P3)

A developer prompts a coding agent inside a sandbox directly from the TUI (or launches the agent
in a separate terminal), and receives a notification when the agent's task completes or when the
agent is waiting for further input.

**Why this priority**: This closes the loop on parallel work — with many sandboxes running, the
developer cannot watch them all, so being told which one needs attention is what keeps fan-out
manageable. It depends on sandboxes and navigation already existing.

**Independent Test**: In a running sandbox, send a prompt to its agent from the TUI, let the task
run to completion, and confirm a notification surfaces identifying that sandbox; trigger a state
where the agent awaits input and confirm a "needs prompting" notification surfaces.

**Acceptance Scenarios**:

1. **Given** a running sandbox, **When** the developer types a prompt for its agent in the TUI,
   **Then** the prompt is delivered to that sandbox's agent.
2. **Given** a running agent task, **When** the task completes, **Then** the developer receives a
   notification identifying the sandbox (and host) whose task finished.
3. **Given** a running agent, **When** it pauses waiting for input, **Then** the developer
   receives a "needs prompting" notification identifying that sandbox.
4. **Given** a sandbox, **When** the developer chooses to work in a full terminal instead, **Then**
   the TUI launches the coding agent for that sandbox in another terminal.
5. **Given** notifications for several sandboxes, **When** the developer selects one, **Then** the
   TUI navigates directly to the corresponding sandbox.

---

### User Story 5 - Organize, navigate, and open in VSCode (Priority: P3)

A developer organizes running sandboxes into user-defined groups, navigates quickly between them,
and opens any sandbox in VSCode to inspect or edit files manually.

**Why this priority**: As the number of sandboxes grows, grouping and fast navigation prevent the
list from becoming unmanageable, and a VSCode escape hatch covers everything the TUI is not meant
to do. Valuable but dependent on sandboxes existing first.

**Independent Test**: With several running sandboxes, create a named group, assign sandboxes to it,
navigate between sandboxes using the TUI, and open one in VSCode confirming it opens that sandbox's
files.

**Acceptance Scenarios**:

1. **Given** several running sandboxes, **When** the developer creates a group and assigns
   sandboxes to it, **Then** the TUI displays those sandboxes under that group.
2. **Given** grouped sandboxes, **When** the developer navigates the TUI, **Then** they can move
   between sandboxes and groups quickly using keyboard navigation.
3. **Given** any sandbox (local or remote), **When** the developer chooses "open in VSCode",
   **Then** VSCode opens against that sandbox's files.
4. **Given** a sandbox the developer no longer needs, **When** they remove it from a group, **Then**
   the grouping updates without stopping or destroying the sandbox.

---

### Edge Cases

- **Insufficient disk for duplication**: When the daemon's host lacks space to copy the selected
  directories, the launch MUST fail clearly without partially seeding or corrupting the
  daemon-controlled folder, and the original directories MUST remain untouched.
- **Large or noisy sources**: Duplication copies selected directories **verbatim** by default —
  every file, including dependency folders, build artifacts, and uncommitted/untracked files — so
  the sandbox sees a byte-identical tree. Because this can be slow or large for heavy directories,
  the system MUST make progress visible (FR-028) rather than appear to hang.
- **Source modified mid-duplicate**: If a source directory changes while it is being copied, the
  resulting sandbox copy must be internally consistent enough to start, and the original must not
  be modified.
- **Remote connection drop**: If an SSH connection drops while a remote sandbox is running, the
  sandbox keeps running on its host; the TUI reflects the host as disconnected and resyncs state
  on reconnect rather than losing or duplicating sandboxes.
- **Daemon unreachable / not running**: Attempting to connect to a host with no running daemon
  surfaces an actionable error rather than hanging indefinitely.
- **VSCode unavailable**: If VSCode cannot be opened (not installed, or the sandbox is remote),
  the TUI reports why instead of failing silently.
- **Agent needs prompting while developer is away**: A "needs prompting" state that occurs while
  the developer is disconnected is still surfaced when they reconnect (FR-026b), in addition to the
  OS desktop notification fired at the time.
- **Naming collisions**: Two sandboxes seeded from the same source or sharing a display name remain
  individually identifiable via their unique auto-generated ids (FR-012e); duplicate group or
  configuration names remain distinguishable to the user.
- **Low host resources at launch**: When the target host is low on disk (e.g., for a large verbatim
  copy) or other resources, the user is warned before the launch proceeds and may override; the
  launch is never silently attempted into a failure (FR-012f).
- **Stopping vs destroying a sandbox**: Stopping a sandbox preserves its duplicated copy and keeps
  it restartable; the copy is removed only by an explicit destroy action — so a developer never
  loses sandbox work merely by stopping it, and reclaiming disk is always a deliberate choice.
- **Configuration references an unsupported option**: A saved configuration referencing a
  sandbox-kit option the host no longer supports fails loudly at launch, naming the offending
  option, rather than silently dropping it.

## Requirements *(mandatory)*

### Functional Requirements

**Daemon & connectivity**

- **FR-001**: A daemon MUST be runnable on each host that participates in switchboard, and MUST
  manage the full lifecycle (launch, stop, restart, and destroy) of docker sandbox sessions on
  that host.
- **FR-002**: The daemon MUST expose an interface the TUI uses to enumerate, launch, stop, restart,
  destroy, and observe sandboxes on its host.
- **FR-002a**: The daemon MUST persist its sandbox registry and, on daemon restart, MUST re-adopt
  (re-attach to and resume managing) any of its docker sandboxes that are still running, so the
  TUI continues to manage them without manual intervention.
- **FR-002b**: The daemon's registry MUST record, for each sandbox it launched, that sandbox's
  configuration and seed metadata (configuration label if any, seeded repositories/directories,
  state) so the information survives daemon restarts and is available to any connecting client.
- **FR-002c**: The client (TUI) MUST be the source of truth for user-level state — saved
  configurations, groups, and the list of known hosts — storing it client-side so it is portable
  across machines and can reference sandboxes spanning multiple daemons.
- **FR-002d**: The client MUST persist known-host connection entries (enough to reconnect to each
  daemon, including remote SSH targets) so the user does not re-enter connection details every
  session.
- **FR-003**: The TUI MUST be able to connect to a daemon on localhost.
- **FR-004**: The TUI MUST be able to connect to a daemon on a remote host over SSH.
- **FR-005**: The TUI MUST support being connected to multiple daemons simultaneously and
  presenting their sandboxes together.
- **FR-006**: Each daemon MUST own a controlled workspace folder on its host into which duplicated
  source directories are copied; duplicated copies for that host MUST live under that folder.

**Seeding & launching sandboxes**

- **FR-007**: When launching a sandbox, the user MUST be able to select which git projects /
  repositories / directories to include, selecting either a single directory or many directories
  to seed one sandbox.
- **FR-008**: When launching a sandbox, the user MUST be able to choose between **duplicating** the
  selected directories and **cloning** the repository (the sandbox tooling's clone option).
- **FR-009**: Duplicate MUST be the default seeding mode.
- **FR-010**: In duplicate mode, the daemon MUST copy the selected directories into its controlled
  workspace folder and seed the sandbox from those copies, leaving the original directories
  unmodified.
- **FR-010a**: Verbatim duplication is the default — the daemon MUST copy every file in the
  selected directories exactly (including dependency folders, build artifacts, and
  uncommitted/untracked files) so the sandbox's seeded tree is byte-identical to the source.
- **FR-028**: For seeding operations that may take noticeable time (large verbatim copies), the
  TUI MUST surface progress/in-flight state so a launch is never indistinguishable from a hang.
- **FR-011**: The system MUST allow a developer to create many independent sandboxes from the same
  or different sources, each isolated from the others.
- **FR-012**: In a multi-host setup, the user MUST be able to choose which connected host a new
  sandbox launches on, and the duplicated copies MUST be placed on that chosen host's daemon
  folder.
- **FR-012f**: The system MUST NOT impose a fixed cap on the number of sandboxes, but MUST warn the
  user before launching when the target host is low on disk or other resources, and MUST allow the
  user to override the warning and proceed.

**Sandbox lifecycle & persistence**

- **FR-012a**: Stopping a sandbox MUST retain its duplicated workspace copy and leave the sandbox
  listed in a "stopped" state; the daemon MUST NOT delete the copy on stop.
- **FR-012b**: The user MUST be able to restart a stopped sandbox, which seeds a docker session from
  the retained copy and returns the sandbox to "running".
- **FR-012c**: Deleting the duplicated copy MUST require an explicit destroy/remove action distinct
  from stop; that action removes the docker session (if any) and deletes the copy from the
  controlled folder.
- **FR-012d**: Each sandbox MUST record the configuration it was launched with — including the
  named-configuration label when one was used — and the repositories/directories it was seeded
  with, and MUST retain this for stopped sandboxes.
- **FR-012e**: Each sandbox MUST have a unique auto-generated identifier. Its display name MUST
  default to the named-configuration label (or a generated name when no named configuration was
  used), and the user MUST be able to override the display name with a custom name. Identity MUST
  remain unique even when two sandboxes share the same source or display name.

**Configurations**

- **FR-013**: The user MUST be able to create configurations that are piped through to the sandbox
  on launch.
- **FR-014**: Every option the sandbox kits expose MUST be achievable through the TUI (no option is
  reachable only outside switchboard).
- **FR-015**: The user MUST be able to save configurations under a name and reuse them for future
  launches.
- **FR-016**: Saved configurations MUST be editable, and edits MUST apply to subsequent launches
  without altering already-running sandboxes.
- **FR-016a**: A configuration MAY specify which coding agent a sandbox runs. When a configuration
  specifies an agent, launches from it MUST use that agent (the choice is fixed for that launch).
- **FR-016b**: When the configuration does not specify an agent — or when no saved configuration is
  used — the user MUST be able to choose the coding agent at launch.

**Viewing, grouping & navigation**

- **FR-017**: The TUI MUST show sandboxes in both running and stopped states, so a developer can
  see what is available to restart, stop, or destroy.
- **FR-017a**: For each sandbox, the TUI MUST display its identifying information — defaulting to
  the named-configuration label when one was used — along with its seeded repositories/directories
  and current state, so the developer can tell sandboxes apart without opening them.
- **FR-018**: The user MUST be able to organize sandboxes into user-defined groups.
- **FR-019**: The user MUST be able to navigate quickly between sandboxes and groups.
- **FR-020**: When connected to multiple daemons, the user MUST be able to view sandboxes organized
  by host.
- **FR-021**: The TUI MUST clearly indicate the connection state of each host and reflect
  disconnection/reconnection without losing or duplicating known sandboxes.

**Agents & notifications**

- **FR-022**: The user MUST be able to prompt a sandbox's coding agent directly from the TUI.
- **FR-023**: The user MUST be able to launch a sandbox's coding agent in another terminal instead
  of prompting from within the TUI.
- **FR-024**: The user MUST receive a notification when an agent task completes.
- **FR-025**: The user MUST receive a notification when an agent is waiting for further prompting.
- **FR-026**: A notification MUST identify the sandbox (and its host) it refers to, and selecting it
  SHOULD navigate to that sandbox.
- **FR-026a**: Notifications MUST be delivered through both a persistent in-TUI notification
  list/badges AND OS-level desktop notifications, so the developer is alerted even when the TUI is
  not focused.
- **FR-026b**: A completion or needs-prompting event that occurs while the developer is disconnected
  from that host MUST be surfaced when they reconnect (no missed events are silently dropped).

**VSCode**

- **FR-027**: Every sandbox session MUST be openable in VSCode so the developer can inspect or edit
  its files manually, including sandboxes on remote hosts.

### Key Entities *(include if feature involves data)*

- **Host / Connection**: A machine running a daemon, reached locally or over SSH. Has a connection
  state (connected/disconnected) and owns a controlled workspace folder. Sandboxes belong to
  exactly one host. Known-host connection entries (how to reach each daemon, including SSH targets)
  are persisted client-side so the user can reconnect without re-entering details.
- **Daemon**: The per-host agent that manages docker sandbox lifecycle, copies sources into the
  controlled workspace folder, serves state to the TUI, and reports agent task/notification events.
  Persists a sandbox registry so it can re-adopt still-running sandboxes after its own restart.
- **Sandbox Session**: A docker sandbox on a host in a running or stopped state, created from
  selected sources via a seeding mode (duplicate/clone) and a configuration. Has a unique
  auto-generated id and a display name (defaulting to the configuration label or a generated name,
  user-overridable), status (running/stopped), owning host, group membership, seeded sources, the
  configuration it was launched with (and that configuration's name/label, if any), the coding
  agent it runs, and a retained duplicated copy while stopped. The retained copy is deleted only by
  an explicit destroy action.
- **Configuration ("kit config")**: A named, savable set of sandbox-kit options that is piped to a
  sandbox on launch. Stored client-side (portable across hosts). MAY specify the coding agent; when
  it does, launches from it are fixed to that agent, otherwise the agent is chosen at launch.
- **Source Selection**: The set of git projects/directories chosen to seed a sandbox, plus the
  seeding mode (duplicate default, or clone).
- **Group**: A user-defined, client-side collection of sandboxes for organization and navigation
  that MAY span sandboxes on multiple hosts; membership is independent of a sandbox's running state.
- **Agent Task / Notification**: A unit of agent work whose state changes (completed, needs
  prompting) generate notifications that reference the originating sandbox and host.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can create at least 10 independent sandboxes from duplicated directories
  on one host without any sandbox interfering with another or with the original sources.
- **SC-002**: Duplicating directories never modifies the originals — verified by comparing source
  directories before and after launch with zero detected changes.
- **SC-003**: From a saved configuration, a developer can launch a new sandbox in under 30 seconds
  of interaction time (excluding image/copy duration), without re-entering options.
- **SC-004**: 100% of the options exposed by the sandbox kits are settable through the TUI.
- **SC-005**: A developer can connect to a remote host over SSH and see its running sandboxes
  within 10 seconds of initiating the connection.
- **SC-006**: With multiple hosts connected, every running sandbox is correctly attributed to the
  host it runs on in the host-grouped view 100% of the time.
- **SC-007**: Switching focus between any two sandboxes in the TUI completes in under 2 seconds.
- **SC-008**: A notification reaches the developer within 5 seconds of an agent task completing or
  entering a needs-prompting state.
- **SC-009**: Any running sandbox, local or remote, can be opened in VSCode in a single action from
  the TUI.
- **SC-010**: After a remote connection drops and is restored, the TUI shows the same set of
  sandboxes for that host as before the drop, with none lost or duplicated.
- **SC-011**: A stopped sandbox can be restarted and resumes from its retained copy with its prior
  seeded contents intact 100% of the time; stopping a sandbox results in zero deletion of its copy.
- **SC-012**: After a daemon restarts while sandboxes are running, 100% of those still-running
  sandboxes are re-adopted and remain manageable from the TUI without manual cleanup.
- **SC-013**: When a target host lacks sufficient disk/resources for a launch, the user is warned
  before any copy or container work begins, with zero silent failed launches.
- **SC-014**: A user's saved configurations, groups, and known hosts are available from their client
  against any daemon they connect to, without re-creating them per host.

## Assumptions

- **Underlying tooling**: "Sandbox kits" and the docker "sandbox" referenced here are the existing
  sandbox CLI/tooling this repo's broader environment already uses (the `sbx`-style sandbox runner).
  Switchboard orchestrates that tooling rather than reimplementing container management; its full
  option surface is the source of truth for FR-014.
- **Named dependencies / constraints (developer-facing givens, not free choices)**: the TUI is built
  with Bubble Tea (charmbracelet/bubbletea); sandboxes run on Docker; remote daemon access is over
  SSH; manual editing is via VSCode. These are fixed inputs from the request, not open decisions.
- **Configuration storage**: User-level state follows a hybrid model (FR-002b–FR-002c): the client
  (TUI) is the source of truth for saved configurations, groups, and known hosts — stored
  client-side so they are portable and can span multiple daemons — while each daemon persists a
  registry of the sandboxes it launched plus their configuration/seed metadata for re-adoption and
  offline display.
- **Agent state detection**: The daemon is assumed able to observe a sandbox's coding agent enough
  to tell "task complete" from "waiting for input" (e.g., by monitoring the agent process/session),
  which is what powers FR-024/FR-025. The exact detection mechanism is an implementation concern.
- **Notification delivery**: OS-level desktop notifications (FR-026a) assume the machine running the
  TUI exposes a desktop-notification mechanism; where none exists, the in-TUI notification list
  remains the guaranteed channel.
- **Authentication**: SSH provides authentication and transport security for remote daemon
  connections; switchboard does not introduce a separate user-account system in this scope.
- **Duplication semantics**: Duplication copies selected directories **verbatim** by default (every
  file, including ignored/untracked ones), so an agent sees an identical tree. Finer details —
  whether copies follow symlinks or preserve permissions/timestamps — are deferred to planning. A
  future opt-in to ignore-aware or git-tracked-only copying is out of scope for this feature.
- **Single developer per TUI session**: Multi-user concurrent control of the same daemon is out of
  scope for this feature; the model is one developer driving one or more daemons.
- **VSCode for remote sandboxes**: Opening remote sandboxes in VSCode is assumed to use VSCode's
  remote capabilities; environments without that support fall under the "VSCode unavailable" edge
  case.
