# Feature Specification: Edit Sandbox Sources (Add & Remove Folders)

**Feature Branch**: `007-edit-sandbox-sources`

**Created**: 2026-09-26

**Status**: Draft

**Input**: User description: "Please spec a feature according to the previous research on allowing the addition and removal of folders from a sandbox. We will go with option A from the research" (option A: live, in-place editing of an existing sandbox's seeded folder set — no container restart).

## User Scenarios & Testing *(mandatory)*

A sandbox is seeded at launch with a fixed set of folders, and today that set is frozen for the
sandbox's whole life. Mid-task, a developer routinely discovers the set is wrong: the agent needs a
sibling repository (a shared library, a second service, a docs tree) that wasn't selected at
launch, or a seeded folder turns out to be irrelevant and is wasting disk and confusing the agent.
The only remedies today are heavyweight and destructive — destroy and relaunch (losing installed
packages and agent history) or refresh (re-copying *everything* and destroying all uncommitted
agent work). **Edit Sandbox Sources** makes the seeded folder set editable in place: a developer
adds a folder and the agent can use it moments later, or removes one, all without restarting the
sandbox or interrupting the work already in flight.

### User Story 1 - Add a folder to an existing sandbox (Priority: P1)

A developer's agent is mid-task in a running sandbox when it becomes clear the task also needs a
second repository. From the sandbox list, the developer opens the sandbox's source editor, browses
the host's directories exactly as they would at launch, selects one or more folders, and confirms.
Each folder is copied into the sandbox's workspace the same way it would have been at launch
(honoring the sandbox's recorded seeding mode), copy progress is visible throughout, and when the
copy completes the folder is available to the agent — the sandbox never stops, the agent session
and any attached terminals are never interrupted, and the sandbox's recorded folder list now
includes the addition.

**Why this priority**: This is the headline capability. "The agent needs one more repo *now*" is
the motivating moment, and its whole value is that the answer is seconds of interaction on a live
sandbox rather than a destroy-and-relaunch or an everything-destroying refresh.

**Independent Test**: Launch a sandbox seeded with one repository and start its agent working. Add
a second repository. Confirm the second repository's copy appears inside the sandbox, the agent can
be prompted about its contents without any restart, the original directory is byte-for-byte
unchanged, the agent session was never interrupted, and the sandbox's displayed folder list shows
both.

**Acceptance Scenarios**:

1. **Given** a running sandbox seeded with repository A in duplicate mode, **When** the developer
   adds repository B, **Then** B is copied verbatim into that sandbox's workspace, the sandbox's
   recorded folder list gains B, and the agent can reach B's files without the sandbox restarting.
2. **Given** an add of a large folder is in progress, **When** the developer keeps using the TUI,
   **Then** copy progress is visible for that sandbox (the operation is never indistinguishable
   from a hang) and the rest of the interface remains usable.
3. **Given** a running agent session and an attached terminal, **When** an add completes, **Then**
   neither was disconnected or restarted at any point during the operation.
4. **Given** a sandbox that was seeded in clone mode, **When** the developer adds a folder that is
   a git repository, **Then** it is added per clone semantics; **When** they select a folder that
   is not a repository, **Then** the add is refused with a reason before anything is copied.
5. **Given** an add that fails partway (for example the host runs out of disk), **Then** any
   partially copied content is removed, the sandbox's recorded folder list and workspace are as
   they were before the attempt, the sandbox remains healthy (it is never pushed into an error
   state by a failed add), and the failure reason is shown to the developer.
6. **Given** a target host low on disk, **When** the developer initiates an add, **Then** they are
   warned before any copying begins and may override or cancel (mirroring the launch-time
   low-resource warning).
7. **Given** any add, **Then** the original source directory is never modified.

---

### User Story 2 - Remove a seeded folder from a sandbox (Priority: P1)

A developer decides a seeded folder no longer belongs in a sandbox. From the sandbox's source
editor they select one or more of the currently seeded folders to remove. A confirmation names the
sandbox, each folder to be removed, and the consequence — everything inside those folders in the
sandbox, including uncommitted agent work, is lost. On confirm, the folders' copies are deleted
from the sandbox's workspace, the recorded folder list is updated, and the sandbox keeps running.
The original directories on the host are untouched.

**Why this priority**: Removal is the other half of "the seeded set is editable", and it is the
destructive half — so the confirmation flow is part of the story, not a nicety. Without removal,
correcting an over-seeded sandbox still requires destroy-and-relaunch.

**Independent Test**: Launch a sandbox seeded with two repositories. Remove one, confirming the
dialog. Verify its copy is gone from the sandbox's workspace, the displayed folder list shows only
the survivor, the sandbox is still running with its agent session intact, and the original
directory on the host is unchanged.

**Acceptance Scenarios**:

1. **Given** a running sandbox seeded with folders A and B, **When** the developer removes B and
   confirms, **Then** B's copy is deleted from the sandbox's workspace, the recorded folder list
   shows only A, the sandbox stays running, and the original B directory is untouched.
2. **Given** the removal confirmation is shown, **Then** cancel is the default and unrecognised
   keys are never read as consent; the confirmation names the sandbox, every folder selected for
   removal, and that uncommitted work inside them is lost.
3. **Given** a sandbox with exactly one seeded folder, **When** the developer attempts to remove
   it, **Then** the removal is refused with an explanation (a sandbox must retain at least one
   seeded folder).
4. **Given** a declared service is starting or running with its working directory inside the
   folder selected for removal, **Then** the removal is refused, naming the service and the remedy
   (stop the service first); nothing is deleted.
5. **Given** a seeded folder whose copy is already missing on disk (for example the agent deleted
   it), **When** the developer removes it, **Then** the record is cleaned up and the operation
   succeeds (a missing copy is treated as already gone).

---

### User Story 3 - Edited records stay truthful everywhere downstream (Priority: P2)

After editing a sandbox's folders, everything that reads the sandbox's seeded-folder record keeps
telling the truth: the sandbox list displays the updated set, a later refresh re-copies exactly the
updated set (added folders included, removed folders not resurrected), and a stop/start cycle
brings the sandbox back with the edited set.

**Why this priority**: The recorded folder list already drives display, refresh, and relaunch. An
edit that changed the disk but not the record (or vice versa) would silently corrupt refresh and
mislead the developer scanning the list — the record staying authoritative is what makes the
feature safe to ship at all.

**Independent Test**: Edit a sandbox's folders (one add, one remove), then: read the sandbox list
and compare against the workspace contents; run a refresh and verify exactly the updated set is
re-copied; stop and start the sandbox and verify the edited set is what comes back.

**Acceptance Scenarios**:

1. **Given** a sandbox whose folders were edited, **When** the developer views the sandbox list,
   **Then** the displayed seeded folders match the sandbox's actual workspace contents.
2. **Given** edited folders, **When** the developer refreshes the sandbox, **Then** exactly the
   updated set is re-copied from source — the added folder is included and the removed folder does
   not come back.
3. **Given** edited folders, **When** the sandbox is stopped and started again, **Then** it comes
   back with the edited folder set.
4. **Given** a command authorization scoped by workspace folders (escape hatch), **When** folders
   are added or removed, **Then** those authorizations evaluate against the current folder set —
   a newly added folder that matches becomes usable and a removed folder stops matching, with no
   stale grants.

---

### User Story 4 - Edit sources on stopped sandboxes and remote hosts (Priority: P3)

The same add/remove flows work on a stopped sandbox (its retained copy is edited; the next start
reflects the edits) and on sandboxes owned by any connected host (the folder browser shows *that*
host's filesystem, and copies land in that host's controlled workspace folder).

**Why this priority**: Switchboard manages sandboxes across hosts and across the running/stopped
divide; an editing feature that only worked on running local sandboxes would leave arbitrary gaps.
It builds directly on stories 1–2 and is separately verifiable.

**Independent Test**: On a stopped sandbox, add one folder and remove another; start it and verify
the edits took effect. Repeat both flows on a remote host's sandbox and verify the browser showed
the remote filesystem and the edits landed there.

**Acceptance Scenarios**:

1. **Given** a stopped sandbox, **When** the developer adds and removes folders, **Then** the
   retained copy is edited in place and the next start reflects the edited set.
2. **Given** a sandbox on a remote host, **When** the developer opens the source editor, **Then**
   the folder browser enumerates the remote host's filesystem and the edit executes on that host.
3. **Given** several connected clients, **When** one edits a sandbox's folders, **Then** the
   others see the updated sandbox promptly without manual reloads.

---

### Edge Cases

- **Folder-name collision**: seeded folders live side by side under names derived from their final
  path segment, so adding a source whose folder name matches an already-seeded folder is refused
  before anything is copied, naming the collision. Selecting two new sources with the same folder
  name in one operation is refused the same way.
- **Reserved name**: the workspace contains an internal bookkeeping folder owned by switchboard;
  adding a source with that reserved name is refused.
- **Source vanished**: the chosen source path is deleted or renamed on the host between selection
  and copy — the add fails cleanly with the reason, and the sandbox is unchanged.
- **Source modified mid-copy**: as at launch, the resulting copy must be internally consistent
  enough to use and the original must not be modified.
- **Concurrent operations on one sandbox**: while an add, remove, refresh, or launch is in flight
  for a sandbox, further folder edits on that same sandbox are refused with a clear message rather
  than interleaved.
- **Agent working inside a removed folder**: processes in the sandbox may have the folder as their
  working directory or hold files in it; removal proceeds (the confirmation covers the loss), the
  sandbox itself is unaffected, and only the sandbox's copy is deleted — never the original.
- **Disk exhaustion mid-add**: the partial copy is removed and the failure is reported; the
  sandbox and its record are exactly as before the attempt.
- **Removal of the record when the copy is gone**: a seeded folder whose on-disk copy has vanished
  can still be removed from the record (treated as already deleted).

## Requirements *(mandatory)*

### Adding folders

- **FR-053**: The client MUST offer a per-sandbox action to add one or more folders to an existing
  sandbox. Folder selection MUST use the same browsing experience as launch (browse the owning
  host's filesystem, multi-select, repository detection), and each added folder MUST be seeded into
  the sandbox's existing workspace under its own folder name using the sandbox's recorded seeding
  mode — verbatim copy for duplicate-mode sandboxes, clone for clone-mode sandboxes (which MUST
  refuse non-repository folders before copying anything). On success the sandbox's recorded folder
  list MUST be updated to include the additions. Original source directories MUST never be
  modified.
- **FR-054**: Adding MUST NOT stop, restart, or recreate the sandbox. A running sandbox MUST stay
  running throughout, with agent sessions, attached terminals, and running services uninterrupted,
  and the added folder MUST be reachable by the sandbox's agent once the copy completes, without
  any restart.
- **FR-055**: Copy progress MUST be surfaced while an add is in flight (an add is never
  indistinguishable from a hang), and the client MUST remain usable during the copy.
- **FR-056**: An add MUST be refused before any copying when: the folder name collides with an
  already-seeded folder or another selection in the same operation; the folder name is the
  reserved internal bookkeeping name; or the source path does not exist or is not a folder. The
  refusal MUST name the offending folder and reason.
- **FR-057**: When the owning host is low on disk or resources, the developer MUST be warned
  before any copying begins and MUST be able to override or cancel (mirroring the launch-time
  warning, FR-012f).
- **FR-058**: A failed add MUST be atomic from the developer's point of view: any partially copied
  content MUST be removed, the sandbox's recorded folder list MUST be unchanged, the sandbox MUST
  NOT enter an error state (its pre-existing workspace remains fully usable), and the failure
  reason MUST be surfaced.

### Removing folders

- **FR-059**: The client MUST offer a per-sandbox action to remove one or more currently seeded
  folders. Removal MUST delete each chosen folder's copy from the sandbox's workspace, update the
  recorded folder list, and touch nothing outside the sandbox's workspace — original directories
  are never affected. Deletion MUST refuse to operate on any path that resolves outside the
  controlled workspace folder.
- **FR-060**: Removal MUST be gated behind an explicit confirmation that names the sandbox, every
  folder selected for removal, and the consequence (everything inside those folders in the
  sandbox, including uncommitted work, is lost). Cancel MUST be the default; unrecognised keys
  MUST NOT be read as consent; and the destructive action MUST NOT share a key with any
  non-destructive action on the same screen.
- **FR-061**: Removal MUST be refused when it would leave the sandbox with zero seeded folders; a
  sandbox MUST always retain at least one.
- **FR-062**: Removal MUST be refused while a declared service instance whose working directory
  lies inside a selected folder is starting or running; the refusal MUST name the service and the
  remedy (stop it first). Nothing is deleted on refusal.
- **FR-063**: Removing a seeded folder whose on-disk copy is already missing MUST still remove it
  from the recorded folder list and succeed.

### Record fidelity, scope & concurrency

- **FR-064**: The recorded folder list MUST remain the single source of truth after edits:
  the sandbox list display, refresh (which re-copies exactly the current recorded set), any
  container relaunch path, and workspace-scoped command authorizations MUST all follow the edited
  record, and the record MUST equal the workspace's actual seeded folders after every successful
  operation.
- **FR-065**: Folder edits MUST work on both running and stopped sandboxes (a stopped sandbox's
  retained copy is edited in place and the next start reflects it), and on sandboxes of any
  connected host (browsing that host's filesystem, editing that host's workspace).
- **FR-066**: Folder edits on a sandbox MUST be refused while another folder edit, refresh, or
  launch is in flight for that same sandbox, with a clear in-progress message; operations MUST
  never interleave on one workspace.
- **FR-067**: A completed edit MUST be visible promptly to every connected client observing that
  host (the sandbox's updated record is broadcast, not discovered by manual reload).

### Key Entities

- **Seeded Source Folder**: one folder a sandbox was seeded with — the host-side origin path,
  whether it is a repository, and its folder name inside the sandbox's workspace. Today created
  only at launch; this feature makes the sandbox's collection of them mutable over its life.
- **Sandbox (record)**: the existing per-sandbox record; its seeded-folder list changes from
  launch-time-frozen to editable, and everything downstream of it (display, refresh, relaunch,
  workspace-scoped authorizations) follows the edited value.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can make an additional repository available to a running sandbox's agent
  in under 30 seconds of interaction time (excluding copy duration), with zero sandbox restarts.
- **SC-002**: Agent sessions and attached terminals survive 100% of folder adds and removals on
  their sandbox — zero disconnections or restarts caused by an edit.
- **SC-003**: Original source directories are byte-for-byte unchanged by every add and every
  removal, verified by before/after comparison — zero modifications, ever.
- **SC-004**: After any successful edit, the sandbox's displayed folder list matches its actual
  workspace contents in 100% of cases, and a subsequent refresh re-copies exactly the edited set.
- **SC-005**: 100% of failed adds leave the sandbox usable and unchanged — no partial folder on
  disk, no record drift, no error state.
- **SC-006**: Zero unconfirmed destructive removals: every removal is preceded by an explicit
  confirmation, and unrecognised input never confirms.
- **SC-007**: The same edit flows succeed on a remote host's sandbox with no additional setup
  beyond the existing host connection.
- **SC-008**: For a folder of typical repository size (≤ 1 GB), the added folder is usable by the
  agent within 10 seconds of the copy completing.

## Key Decisions

1. **Live, in-place editing (research option A)** — edits happen on the sandbox's workspace while
   the sandbox keeps running; no stop/restart cycle. The motivating use case is "the agent needs
   another repo *now*", and a restart would cost the agent's momentum, attached terminals, and the
   terminal session — exactly the things the persistent-session feature exists to protect. Host-side
   workspace additions becoming visible inside a live sandbox is the same behavior existing
   features already rely on (externally delivered command results, injected agent rules).
2. **Explicit add and remove operations, not "sync to a desired set"** — each direction has its own
   flow and its own confirmation semantics (removal is destructive, addition is not), and partial
   failure stays simple: the record reflects exactly the operations that completed.
3. **The recorded folder list is the single source of truth** — every edit updates disk and record
   together; refresh, display, and relaunch all read the record. An edit path that touched one
   without the other is prohibited outright (FR-064).
4. **Removal refuses rather than auto-stops running services** — consistent with the port-forwarding
   principle that nothing ever auto-starts: nothing auto-stops either. The developer is told which
   service blocks the removal and stops it deliberately.
5. **A failed add never poisons the sandbox** — unlike launch or refresh, where a failure leaves an
   unusable workspace and an error state is honest, a failed add leaves a fully usable pre-existing
   workspace; the operation cleans up after itself and reports, and the sandbox stays healthy.

## Assumptions

- **Live visibility of workspace changes**: the sandbox observes its workspace copy live, so
  host-side folder additions and removals inside the workspace are visible to a running sandbox
  without a restart. Existing features already depend on this (externally executed command results
  appear to the agent; rules injected after seeding are read by the agent). As with prior features,
  the host sandbox tooling is not installed in the development environment, so this MUST be
  reconciled against the real runtime early in planning; if a runtime is found not to propagate
  live, the degradation is "visible after next restart" — a planning-time contingency, not a
  change to what this spec requires of the record or the flows.
- **Scope — seeded top-level folders only**: only whole folders seeded as sources are addable and
  removable. Files, sub-folders, folders the agent created inside the workspace, and the
  internal bookkeeping folder are out of scope for editing.
- **No rename/move**: renaming a seeded folder or re-pointing it at a different origin is out of
  scope; that is a remove plus an add.
- **No single-folder re-copy**: re-copying one folder from its origin ("refresh just this repo")
  is out of scope for this feature; refresh remains whole-workspace.
- **Launch-time copy semantics are reused**: verbatim duplication (every file, symlinks as-is,
  originals opened read-only) and clone semantics are exactly those of launch — this feature adds
  no new copy mode.
- **The launch configuration snapshot stays frozen**: the sandbox's recorded launch configuration
  is history and is not rewritten by edits; the live seeded-folder list is the record that changes.
- **Nothing is automatic**: folders are added or removed only by explicit developer action —
  never as a side effect of kit attachment, refresh, restart, or daemon lifecycle.
