# Feature Specification: Port Forwarding

**Feature Branch**: `006-port-forwarding`

**Created**: 2026-07-29

**Status**: Draft

**Input**: User description: "we want to create a feature to support port forwarding. the user should be able to specify, as part of a kit, commands and what port they run on as well as if they should be run in the sandbox or outside it. then, from the terminal the user should be able to pick and choose which commands they want to start and run. when command run they should automatically be assigned to a local port that is free and the user should be able to open that port on their local machine (if its a website) or otherwise be accessible from that port"

## User Scenarios & Testing *(mandatory)*

A sandbox is isolated by design, which is exactly what makes the things running inside it unreachable.
A developer whose agent just built a dev server, a Storybook, or an API inside a sandbox has no way to
look at it: the sandbox has its own network, and reaching a port inside it takes a host-side command the
developer has to remember, run in another window, and clean up afterwards. Some pieces of a stack can't
run in the sandbox at all and belong on the host — but then they need a way to be reached too, and
their ports collide the moment a second sandbox wants the same one.

**Port Forwarding** makes "run it and look at it" a two-keystroke operation. A developer declares, on an
agent kit, the long-running **services** a sandbox using that kit can offer: a name, the command that
starts it, the port that command listens on, and whether it runs *inside* the sandbox or *outside* it on
the host. Attaching the kit gives every sandbox that menu. From the client, the developer picks which
services to start; each one that starts is automatically assigned a free port on their own machine and
is reachable there — opened in a browser when it serves a website, or connected to by address for
anything else. Nothing is started that the developer didn't pick, ports never collide between services
or between sandboxes, and stopping a service releases its port.

### User Story 1 - Declare services on a kit (Priority: P1)

A developer editing an agent kit adds a **services** section. For each service they give a short name
(e.g. "web"), the command that starts it (`pnpm dev`), the port that command listens on (`3000`), and
where it runs — *in the sandbox* or *on the host, outside the sandbox*. They optionally note whether it
serves a website (so it can be opened in a browser) and which directory inside the workspace to start it
from. They save the kit; the section is validated alongside the rest of the kit and stored with it.

**Why this priority**: Nothing can be started, forwarded, or opened until the menu exists. The declared
list is the whole contract — it defines what a sandbox can run and where — so authoring it correctly is
the foundation every other story stands on.

**Independent Test**: Open the kit editor, add one service of each execution location with every field,
save, reopen — all services and all fields persist and the kit still validates. Delivers value on its
own: a reviewable, shareable declaration of how to bring a project's stack up.

**Acceptance Scenarios**:

1. **Given** the kit editor, **When** the developer adds a service with a name, a start command, a listen
   port, and an execution location, **Then** it is saved with the kit and shown on reopen.
2. **Given** a kit with several services, **When** it is saved and reopened, **Then** every service keeps
   its own execution location and listen port.
3. **Given** a service entry missing a name, a command, or a valid listen port, **When** the developer
   tries to save, **Then** the kit is rejected with a message naming the offending field, and the stored
   kit is unchanged.
4. **Given** two services on the same kit declared with the same name, **When** the developer tries to
   save, **Then** the kit is rejected — names within one kit MUST be unique.
5. **Given** a partially edited service entry, **When** the developer abandons the edit, **Then** the
   stored kit is unchanged.

---

### User Story 2 - Start a sandbox service and open it locally (Priority: P1)

A developer has a running sandbox whose attached kit declares a "web" service (`pnpm dev`, port 3000,
runs in the sandbox). From the client they open that sandbox's service list, see "web" listed as
stopped, and start it. The command runs inside the sandbox; once it is listening, the client shows the
service as running with a local address on the developer's own machine (e.g. `localhost:49221`). They
open it and the sandbox's dev server loads in their browser. They never typed a port number, never ran
a publish command, and nothing else on their machine was disturbed.

**Why this priority**: This is the headline capability — the thing running in the isolated sandbox
becomes reachable from the developer's browser with no manual port plumbing. On its own it makes the
feature worth shipping.

**Independent Test**: Attach a kit declaring one in-sandbox service to a running sandbox, start it from
the client, and confirm the command is running inside the sandbox and that requests to the displayed
local address reach it. Confirm the assigned port was free beforehand and was chosen without user input.

**Acceptance Scenarios**:

1. **Given** a running sandbox with declared services, **When** the developer opens its service list,
   **Then** every service declared by its attached kits is listed with its current state, and none are
   running that the developer did not start.
2. **Given** a stopped in-sandbox service, **When** the developer starts it, **Then** the declared command
   runs inside the sandbox and the service is shown as starting, then running.
3. **Given** a service that has reached the running state, **When** the developer sends traffic to the
   displayed local address on their own machine, **Then** it reaches the process listening on the
   declared port in the sandbox.
4. **Given** a service marked as serving a website, **When** it is running, **Then** the developer can
   open it in their browser directly from the client.
5. **Given** a service not marked as serving a website, **When** it is running, **Then** the client shows
   a copyable local address instead of a browser action.
6. **Given** a sandbox that is not running, **When** the developer tries to start one of its in-sandbox
   services, **Then** the attempt is refused with a message explaining the sandbox must be running, and
   no port is allocated.

---

### User Story 3 - Start a service that runs outside the sandbox (Priority: P2)

Part of the developer's stack can't run in the sandbox — it needs the host's real hardware or a resource
the microVM doesn't have. They declared it on the kit as running *outside* the sandbox. They start it
the same way, from the same list. The command runs on the host that supervises the sandbox, against that
sandbox's workspace, and is reachable at an assigned free local port exactly like an in-sandbox service.
Two sandboxes each running their own copy of that service get two different local ports and do not
collide, even though both commands declare the same listen port.

**Why this priority**: It completes the "in the sandbox or outside it" half of the authoring model and
covers stacks that can't be fully sandboxed. It reuses US2's allocation and access path, so it is a
distinct but dependent slice.

**Independent Test**: Attach a kit declaring one out-of-sandbox service, start it, and confirm the
command runs on the supervising host in that sandbox's workspace and is reachable at its assigned local
port. Start the same service on a second sandbox and confirm both run concurrently on distinct local
ports.

**Acceptance Scenarios**:

1. **Given** a service declared to run outside the sandbox, **When** the developer starts it, **Then** the
   command runs on the host supervising that sandbox, in that sandbox's workspace, and not inside the
   sandbox.
2. **Given** an out-of-sandbox service running, **When** the developer uses its local address, **Then**
   traffic reaches the host-side process.
3. **Given** two sandboxes whose kits declare the same service on the same listen port, **When** both are
   started, **Then** each is assigned a distinct local port and both remain reachable.
4. **Given** an out-of-sandbox service whose declared port is already occupied on the host, **When** the
   developer starts it, **Then** the failure is reported with diagnostics and the service is shown as
   failed rather than running.

---

### User Story 4 - Manage running services (Priority: P2)

Over a working session the developer starts several services across several sandboxes. The sandbox list
marks which sandboxes have services running, and opening a sandbox's service list shows each service's
state and local address. When one crashes, its state changes to
failed and the developer can read the output that explains why. They stop a service they're done with
and its local port is released. When they stop or destroy the sandbox, everything it was running stops
with it — no stray processes, no ports left held.

**Why this priority**: Without lifecycle control and failure visibility, a started service is a
one-way door and a leaked port. It builds on US2/US3 but is separately verifiable.

**Independent Test**: Start services, confirm their states and addresses are shown; stop one and confirm
the process ends and the port is released; kill a service out-of-band and confirm it is shown as failed
with its output available; stop the sandbox and confirm all of its services stop.

**Acceptance Scenarios**:

1. **Given** a sandbox with running services, **When** the developer opens its service list, **Then** each
   service shows its name, state, and local address, and the sandbox list marks that sandbox as having
   services running.
2. **Given** a running service, **When** the developer stops it, **Then** the process is terminated and
   the local port is released and available for reuse.
3. **Given** a service whose process exits unexpectedly, **When** it does, **Then** it is shown as failed,
   its output is available for diagnosis, and its local port is released.
4. **Given** a service that starts but never listens on its declared port, **When** a bounded readiness
   window elapses, **Then** it is reported as failed to become ready rather than shown as running.
5. **Given** a sandbox with running services, **When** it is stopped or destroyed, **Then** all of its
   services stop, all of its local ports are released, and no orphaned process remains.
6. **Given** a running service, **When** the developer starts it again, **Then** it is not started twice —
   the existing run and its existing local address are kept.
7. **Given** the developer is looking at a different sandbox, **When** one of their running services fails,
   **Then** a notification announces the failure naming the sandbox, the service, and the reason.

---

### User Story 5 - Reach services on remote-host sandboxes (Priority: P3)

The developer's sandbox lives on a remote host they manage through the client. They start its "web"
service the same way as any other. The assigned local address is on *their* machine, not the remote
host's, and opening it reaches the service running remotely. Nothing about the workflow differs from
the local-host case.

**Why this priority**: Switchboard manages sandboxes across hosts, so a feature whose value is "open it
on my machine" is incomplete for remote sandboxes. It is lower priority because the local-host path
delivers the capability first and this extends the reach path only.

**Independent Test**: With a sandbox on a remote host, start a declared service and confirm the address
shown is on the client machine and that traffic to it reaches the service on the remote host.

**Acceptance Scenarios**:

1. **Given** a sandbox on a remote host, **When** the developer starts one of its services, **Then** the
   local address shown is reachable from the client machine.
2. **Given** a running service on a remote-host sandbox, **When** the connection to that host is lost,
   **Then** the client reflects that the service is unreachable rather than presenting a dead address as
   working.

---

### Edge Cases

- **No free local port available**: Starting fails with a clear message; nothing is left half-started.
- **Declared port already in use in the execution environment** (in the sandbox, or on the host for an
  out-of-sandbox service): The start fails with diagnostics and the service is marked failed; it is never
  reported as running while the developer would actually be reaching someone else's process.
- **Service command exits immediately** (bad command, missing dependency): Reported as failed with its
  output, not silently listed as stopped.
- **Service starts but never listens**: Bounded readiness window, then reported as failed to become ready.
- **Service crashes while running**: State becomes failed, output retained for diagnosis, port released.
- **Declared command is a wrapper** (`pnpm dev`, `npm start`) whose child is the real listener: stopping
  ends the whole tree, not just the launched command, so the listener never survives its service.
- **A process ignores the graceful shutdown request**: it is forcefully terminated once the grace period
  elapses; the service still reaches stopped and its port is still released.
- **Very noisy service output**: Captured output is bounded, with truncation made evident.
- **Client quits or disconnects while services run**: Services keep running (they are owned by the
  supervising daemon, not the client); on reconnect the client shows them still running with their
  addresses. A remote-host access path may need to be re-established, but the service itself is not
  restarted.
- **Two attached kits declare a service with the same name**: The more-recently-attached kit's service
  overrides (shadows) the earlier one, so a name always resolves to exactly one service — consistent with
  escape-hatch command resolution.
- **Same kit attached to several sandboxes**: Each sandbox gets its own independent services, its own
  processes, and its own local ports.
- **Starting the same service twice**: Idempotent — the running instance and its address are kept.
- **Sandbox restarted or refreshed**: Its services are not silently resurrected; the developer picks what
  to start again, matching the "nothing runs that I didn't pick" rule.
- **Daemon restarts**: No service survives it; running state is not falsely reported afterwards.
- **Non-HTTP service** (database, message queue, gRPC): Fully supported for reachability; only the
  browser-open action is withheld, replaced by a copyable address.
- **Service listens on loopback only**: A command that binds only its environment's loopback interface
  (the default for several common dev servers) is listening but unreachable from outside that environment.
  It is reported as failed to become ready, with the failure naming loopback-only binding as the cause and
  binding to all interfaces as the fix — never shown as running with an address that does not work.

## Clarifications

### Session 2026-08-01

- Q: How should a service that binds only loopback inside its execution environment be handled? → A:
  Require declared commands to listen on all interfaces; a loopback-only service fails the readiness
  window and is reported failed with a message naming the cause and the fix.
- Q: Is there a cross-sandbox view of running services, or only a per-sandbox list? → A: Per-sandbox list
  only, plus a running-services indicator on each row of the existing sandbox list.
- Q: Are service state changes announced, or only visible on the service list? → A: All state changes
  reflect live in the client; entering the failed state additionally raises a notification. Successful
  starts and developer-initiated stops are silent.
- Q: What does stopping a service terminate, and is shutdown graceful? → A: The whole process tree —
  signalled to shut down gracefully, then forcefully terminated after a bounded grace period; the local
  port is released only once nothing holds the listen port.

## Requirements *(mandatory)*

### Authoring (extends the kit editor)

- **FR-043**: A kit MUST be able to declare an ordered list of **services**, each with: a stable
  human-readable name unique within the kit; the command that starts it; the **listen port** that command
  binds in its own execution environment; an **execution location** of either *in-sandbox* or *on-host*;
  and optionally a flag marking it as serving a website (browser-openable) and a workspace-relative
  working directory to start it from. The client MUST let developers add, edit, and remove these entries
  from the kit editor, MUST validate them with the rest of the kit (rejecting a missing name, a missing
  command, an out-of-range listen port, a duplicate name within the kit, or a working directory that
  escapes the workspace), and MUST NOT mutate the stored kit on an abandoned edit.
- **FR-044**: Services MUST be attached to a sandbox by attaching the kit that declares them (at creation
  or to a running sandbox) and MUST be persisted with the sandbox so a container recreate replays the same
  service set. A sandbox's set of startable services MUST be exactly those declared by its attached kits —
  no more. When two attached kits declare a service with the same name, the more-recently-attached kit's
  service MUST override (shadow) the earlier one.

### Selection and lifecycle

- **FR-045**: The client MUST present, per sandbox, the services declared by that sandbox's attached kits
  with their current state, and MUST let the developer start and stop each one individually. This
  per-sandbox list is the only surface for starting and stopping services; the existing sandbox list MUST
  indicate which sandboxes currently have services running, and no separate cross-sandbox services view is
  required. A service MUST NOT start except by the developer's explicit selection — attaching a kit, creating a sandbox,
  starting a sandbox, or restarting the daemon MUST NOT start any service on its own.
- **FR-046**: Starting an *in-sandbox* service MUST run its command inside that sandbox; starting an
  *on-host* service MUST run its command on the host of the daemon supervising that sandbox, in that
  sandbox's workspace (or its declared working directory within it). Starting an in-sandbox service MUST
  be refused, with no port allocated, when the sandbox is not running.
- **FR-047**: Each service MUST report a state covering at least: stopped, starting, running, and failed.
  A service MUST NOT be reported as running until something is listening on its declared port and its
  local address is usable; failure to become ready within a bounded window MUST be reported as a failure,
  not as running. A declared command MUST listen on all interfaces of its execution environment to be
  reachable; a service that listens only on that environment's loopback interface MUST be treated as
  failing to become ready, and its failure MUST name loopback-only binding as the cause and binding to all
  interfaces as the remedy. A process that exits unexpectedly MUST move the service to failed.
- **FR-048**: Stopping a service MUST terminate the command **and every process it spawned**: the system
  MUST first ask the whole process tree to shut down gracefully, wait a bounded grace period, then
  forcefully terminate whatever is still alive, and MUST release the allocated local port only once nothing
  holds the service's listen port any longer. Stopping or destroying a sandbox MUST stop all of its running
  services the same way and release all of their ports, leaving no orphaned process and no held port.
  Starting an already-running service MUST be idempotent — no second process, same local address.

### Port allocation and access

- **FR-049**: When a service starts, the system MUST automatically allocate a **free port on the
  developer's own machine** without asking the developer for one, and MUST hold that port for as long as
  the service runs. Allocated ports MUST be unique across all running services on that machine — including
  services from different sandboxes, and services declaring the same listen port — and MUST be released on
  stop or failure so they can be reused. When no free port can be allocated, the start MUST fail with a
  clear message and leave nothing partially started.
- **FR-050**: While a service is running, traffic sent to its allocated local address on the developer's
  machine MUST reach the process listening on the declared port in that service's execution environment,
  whether that environment is inside the sandbox, on a local host, or on a remote host the client manages.
  The client MUST display the local address for every running service, MUST offer to open it in a browser
  for services marked as serving a website, and MUST offer a copyable address for those that are not.
  When the path to a remote host is lost, the client MUST reflect the service as unreachable rather than
  presenting a dead address as working.

### Diagnostics

- **FR-051**: For every service the developer starts, the system MUST capture the command's output
  (bounded, with truncation made evident) and make it retrievable from the client, so that a service that
  fails to start, fails to become ready, or crashes can be diagnosed without leaving the client. Failures
  MUST be surfaced with a reason — command could not be launched, port already in use, listening on
  loopback only, exited before becoming ready, exited unexpectedly — and never silently dropped.
- **FR-052**: Service state changes MUST be reflected in the client as they happen, without the developer
  reopening or refreshing the service list. A service entering the **failed** state MUST additionally raise
  a notification identifying the sandbox, the service, and the reason, so that a failure occurring while
  the developer is elsewhere in the client is announced rather than only discoverable on revisit.
  Successful starts and developer-initiated stops MUST NOT raise notifications.

### Key Entities

- **Kit Service**: A single long-running service declared on a kit. Attributes: name (unique within the
  kit); start command; listen port; execution location (in-sandbox | on-host); optional
  serves-a-website flag; optional workspace-relative working directory. It is a member of a kit and
  inherits the kit's lifecycle (authored, validated, attached, persisted).
- **Service Instance**: One running (or attempted) execution of a Kit Service for a specific sandbox.
  Attributes: the service it came from; the sandbox; state (stopped | starting | running | failed); the
  allocated local port and address; start/end times; failure reason; captured (bounded) output.
- **Local Port Allocation**: The reservation of a free port on the developer's machine for the lifetime of
  a Service Instance, and the access path that carries traffic from it to the service's listen port in its
  execution environment.
- **Kit** *(existing)*: The client-authored provisioning unit that now also carries services.
- **Sandbox / Workspace** *(existing)*: The isolated environment, its host-side workspace, and the
  supervising daemon's host — the three places a declared service can be reached from or run in.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can declare a service on a kit and reach it running in a sandbox from their own
  browser entirely through the client, without hand-editing files or running any port command themselves.
- **SC-002**: Starting a declared service and reaching it locally takes no more than two deliberate
  actions from the sandbox list (select the service, start it) and requires the developer to choose or
  type **zero** port numbers.
- **SC-003**: Across concurrent services — including multiple sandboxes running services that declare the
  same listen port — **zero** local port collisions occur, and every running service is independently
  reachable.
- **SC-004**: **100%** of stopped, failed, or sandbox-terminated services release their allocated local
  port and leave no orphaned process.
- **SC-005**: **Zero** services start without the developer explicitly selecting them — attaching a kit,
  creating a sandbox, starting a sandbox, and restarting the daemon each start nothing.
- **SC-006**: For any service that fails to start, fails to become ready, or crashes, the developer can
  determine the reason from the client without leaving it — **100%** of failures carry a reason and, where
  the command ran, its captured output — and a failure that occurs while the developer is elsewhere in the
  client is announced rather than waiting to be discovered.
- **SC-007**: A service is never displayed as running while its local address does not reach it — a
  service that never listens is reported as failed within its readiness window in **100%** of cases.
- **SC-008**: The workflow is identical for a sandbox on a remote host: the address shown is on the
  developer's machine and reaches the remote service, with no extra steps compared to a local sandbox.

## Key Decisions

1. **Services are declared on the kit, started by the human.** Authoring (what *can* run, where, on which
   port) is a reviewable, shareable, per-project decision that belongs with the kit — exactly where
   escape-hatch commands live. Starting is a per-session decision that belongs to the developer at the
   terminal. Nothing auto-starts, so attaching a kit never spends resources or opens ports by surprise.
2. **The declared port belongs to the command; the local port belongs to the developer.** The author says
   what their command binds in its own environment; the system picks a free port on the developer's
   machine. This is what lets the same project run in five sandboxes at once without the author ever
   thinking about collisions, and why the developer never types a port number.
3. **One list, two execution locations.** In-sandbox and on-host services are authored, listed, started,
   and reached the same way; only where the command runs differs. A stack that is half-sandboxable is
   brought up from one menu instead of two workflows.
4. **The developer's machine is the reference point for "local".** For a remote-host sandbox the assigned
   address is on the client machine, not the remote host — "open it in my browser" is the value being
   delivered, and it must not degrade to "ssh in and figure it out" when the sandbox happens to be remote.
5. **No consent gate on host-run services** (unlike escape hatch): the developer personally selects and
   starts each one at the terminal, so human intent is already present. The kit's declared list remains
   the allowlist — nothing outside it can be started.
6. **Running is a claim about reachability, not about process launch.** A service is only "running" once
   its address actually works; a command that launched but never listened is a failure. This keeps the
   displayed address trustworthy, which is the entire point of the feature.

## Assumptions

- This feature builds on **feature 004 (Agent Kits)** — client-authored kits, edited in the kit editor,
  attached at creation or to a running sandbox, materialized on the daemon host — and follows the
  structured-kit-section precedent set by **feature 005 (Escape Hatch)**: services are switchboard-owned
  structured data on the kit, validated and enforced by switchboard, with later-attached kits shadowing
  earlier ones on a name collision.
- Command execution on the supervising host, in the sandbox's workspace, works as established by feature
  005; on-host services reuse that execution model, differing in that they are long-running rather than
  run-to-completion.
- Services are long-running and non-interactive: they start, listen, and keep running until stopped. A
  command that needs to prompt the developer mid-start is not supported.
- Declared commands are authored to listen on all interfaces of their execution environment (not only
  loopback). Switchboard does not rewrite commands or relay from inside the sandbox to compensate; a
  loopback-only service is a diagnosable authoring error, fixed once in the kit.
- Services run under the same OS user and permissions as the environment they run in (the sandbox, or the
  supervising daemon's host user). This feature introduces no new privilege model.
- Assigned local ports are ephemeral and not stable across restarts: a service stopped and started again
  may receive a different local port. Pinning a specific local port is not offered in v1.
- Only the developer's own machine reaches the assigned port; forwarded services are not exposed to the
  wider network by this feature.
- The developer's machine has a usable browser for the browser-open action; when it does not, the copyable
  address remains available.
- Service state and captured output are session-scoped in the same sense as escape-hatch runs (they live
  for the daemon's lifetime) — a daemon restart leaves no service running and no stale state claiming
  otherwise.

## Out of Scope

- **Auto-starting services** on sandbox creation, sandbox start, or kit attachment (deliberately excluded
  by Key Decision 1; a per-service autostart flag is a possible later addition).
- **AI-initiated service control** — the sandbox's agent starting or stopping services. v1 is a
  developer-driven, terminal-driven selection.
- **Pinning or requesting a specific local port**, and reserving the same port across restarts.
- **Exposing forwarded services beyond the developer's own machine** (LAN, public URLs, tunneling to third
  parties).
- **Dependency ordering or health-gated startup between services** (e.g. "start the API only after the
  database is ready"). Each service is started independently by the developer.
- **Restart policies / supervision** — automatically restarting a service that crashes.
- **Streaming a service's live output as a terminal view.** Output is captured and retrievable for
  diagnosis; watching a service's console is served by the existing terminal attachment.
- **A dedicated cross-sandbox "all running services" screen.** Running services are viewed and controlled
  from their own sandbox's list; the sandbox list only indicates which sandboxes have any running.
- **Changing how sandboxes, kits, or the daemon are otherwise provisioned or transported.**

## Dependencies

- **Feature 004 — Sandbox Refresh & Agent Kits**: kit authoring in the kit editor, kit attachment at
  creation and to a running sandbox, kit persistence with the sandbox, and kit materialization on the
  daemon host.
- **Feature 005 — Escape Hatch**: the precedent and machinery for switchboard-owned structured kit
  sections, for running author-declared commands on the supervising daemon's host in the sandbox's
  workspace with bounded captured output, and for surfacing per-sandbox run state in the client.
- **Feature 003 — Terminal Session Persistence**: daemon-owned, client-independent execution — work
  started from the client keeps running when the client detaches — and the live event channel the client
  uses to reflect state changes.
- **Feature 001 — Sandbox Session Manager**: sandbox lifecycle, the sandbox list UI the service menu hangs
  off, and multi-host (local and remote) sandbox management.
