# Feature Specification: MCP Gateway Manager & Daemon Screen

**Feature Branch**: `008-mcp-gateway-manager`

**Created**: 2026-10-06

**Status**: Draft

**Input**: User description: "We want to add integration with docker sandbox mcp gateway. (1) a per-daemon MCP gateway *manager* that manages the gateway's server entries through the host sandbox CLI; (2) a new TUI *daemon* screen, reachable from the sandbox list with shift+←/→, where the MCP gateway is one management option per daemon; (3) a quick win: on launch, loop over connected remote hosts, prompt for passwords where needed in the middle of the screen, surface and contain failures without blocking, behind a flag that is on by default; (4) when adding or editing an MCP server on a daemon, an *attach to every sandbox by default* option — for now the only way an MCP server reaches a launching sandbox — which the daemon applies automatically at launch. Addition (2026-10-06): update switchboard to work with the Docker Sandboxes **kit schema at minimum version 2** — the project predates it and still authors the earlier grammar."

## User Scenarios & Testing *(mandatory)*

Every sandbox the host runtime starts comes with an MCP gateway: one endpoint inside the sandbox
through which the agent reaches Model Context Protocol servers, while the *host* keeps the list of
registered servers and their credentials. Today switchboard ignores this entirely. A developer who
wants their agents to have, say, an issue tracker or a documentation server has to register it by
hand on every machine that runs a daemon, remember to pass it at every launch, and gets no view of
what each daemon has. **MCP Gateway Manager** gives each daemon a managed view of its gateway's
server registrations, lets the developer mark servers that every sandbox on that daemon should
start with, and applies those marks automatically at launch. It arrives with a new **daemon
screen** — a per-daemon management surface the sandbox list has lacked — and a startup quick win:
remote hosts are signed into as the TUI starts, with password prompts where needed, so a
multi-daemon developer lands on a fully connected view instead of visiting the hosts screen first.
Finally, the kits switchboard authors move to the runtime's **current kit schema (version 2)**: the
project predates that schema and still writes the legacy grammar, which the runtime accepts today
but no longer documents as current — the move is made here, with saved kits migrated in place.

## Clarifications

### Session 2026-10-06

- Q: When a sandbox launches on a daemon that has attach-by-default servers, does pre-loading them put the gateway in static mode (the agent loses discovery) or keep dynamic mode and attach them after creation? → A: Per-daemon **default attach mode** setting — *additive* (default: dynamic mode, marked servers attached after creation and before the agent starts, discovery kept) or *exclusive* (marked servers pre-loaded as the fixed static set, no discovery).
- Q: Does the daemon screen let the developer connect/disconnect daemons, or is that left to the hosts screen? → A: The daemon screen offers **connect** on a disconnected row (same masked password prompt as the hosts screen) and **disconnect** on a connected one; adding, removing and choosing the active host stay on the hosts screen.
- Q: How long does switchboard wait for an OAuth authorization (register or re-authorize) to complete before giving up? → A: **10 minutes**, cancellable by the developer at any time; on timeout or cancel the registration is kept and reported as unauthorized, and can be authorized later from the list.
- Q: Where does the automatic host sign-in flag live (environment variable, command-line switch, or a TUI-persisted toggle)? → A: A **toggle on a new TUI settings screen**, persisted by the TUI across restarts, on by default — the settings screen is new in this feature and the toggle is its first entry.

### User Story 1 - Manage a daemon's MCP server registrations (Priority: P1)

From the sandbox list, a developer switches to the daemon screen, picks one of their connected
daemons, and opens its **MCP gateway** option. They see every MCP server registered on that
daemon's host — its name, what kind of server it is (a remote endpoint, or a server the host
launches locally), where it points, whether it is authorized, and whether it is marked to attach to
every sandbox. They register a new server by name and target, remove one they no longer want,
re-authorize one whose access lapsed, and change a server's switchboard-side settings. Each
operation acts on *that daemon only*: registrations are per host, exactly as the runtime keeps them,
and a server registered on a laptop's daemon is not registered on a remote build box's daemon
unless the developer registers it there too.

**Why this priority**: This is the foundation everything else builds on. Without a per-daemon view
and the register/remove/authorize operations, there is nothing to attach at launch and nothing for
the daemon screen to manage.

**Independent Test**: With two daemons connected (local and one remote), register a remote-endpoint
server on the remote daemon from the TUI. Confirm it appears in that daemon's list with the right
kind and target, is absent from the local daemon's list, is visible to the host CLI on the remote
machine, and that removing it from the TUI removes it there too.

**Acceptance Scenarios**:

1. **Given** a connected daemon whose host has servers registered, **When** the developer opens its
   MCP gateway option, **Then** every registered server is listed with name, kind, target,
   authorization state and attach-by-default mark, and the list matches what the host CLI reports.
2. **Given** the MCP gateway option is open, **When** the developer registers a remote-endpoint
   server with a valid name and address, **Then** it appears in the list, the host CLI on that
   daemon lists it, and no other daemon's list changes.
3. **Given** a remote server that requires authorization, **When** the developer registers it,
   **Then** the authorization link is shown *in the TUI* (the daemon may be on another machine, so
   the developer opens it where they are) together with the time remaining, the registration
   completes once authorization finishes, and the developer can instead choose to register now and
   authorize later, or cancel the wait — the registration is kept either way and shown as
   unauthorized until authorized.
4. **Given** a registered server whose authorization has lapsed or was skipped, **When** the
   developer chooses *authorize*, **Then** the same link-in-the-TUI flow runs with the same bound
   and the list reflects the new authorization state.
5. **Given** the developer registers a server the host would launch locally (a command the host
   runs), **When** they confirm, **Then** they are warned beforehand that such a server runs on the
   daemon's host outside sandbox isolation, and it is registered only after they accept.
6. **Given** a registered server, **When** the developer removes it, **Then** it disappears from the
   list and from the host CLI, its attach-by-default mark is cleared, and the developer is told
   about any credential material the runtime leaves behind on the host.
7. **Given** a daemon whose host cannot manage a gateway (the sandbox CLI is missing, too old, or
   not signed in), **When** the developer opens the MCP gateway option, **Then** a plain reason is
   shown, no operation is offered that cannot work, and the rest of the TUI is unaffected.
8. **Given** an operation fails on the host (duplicate name, unreachable address, rejected
   registration), **Then** the host's own diagnostic is shown verbatim and the list is unchanged.

---

### User Story 2 - Sandboxes launch with the daemon's default MCP servers attached (Priority: P1)

While registering or editing a server on a daemon, the developer ticks **attach to every sandbox**.
From then on, every sandbox *launched on that daemon* starts with that server already available to
its agent — no per-launch choice, no extra step. How the defaults are given is the daemon's **default
attach mode**: *additive* (the default) hands the agent the marked servers and leaves it free to
discover and attach any other server registered on that daemon; *exclusive* gives the agent the
marked servers and nothing else. A daemon with three default servers gives every new
sandbox all three; a daemon with none launches sandboxes exactly as it does today. Which servers a
sandbox was given is recorded on the sandbox and visible in the sandbox list, so the developer can
tell at a glance what an agent has to work with.

**Why this priority**: Attaching is the point of registering. Without it, registrations are
inert; with it, the daemon-wide default is the one-time setup that pays off on every launch — and,
for now, it is the *only* way switchboard attaches a server to a launching sandbox.

**Independent Test**: Mark one registered server attach-by-default on a daemon and launch a
sandbox there. Confirm the agent inside the sandbox can see that server's tools without any further
action, the sandbox's record names that server, and a sandbox launched on a *different* daemon
(with no defaults) has no servers attached.

**Acceptance Scenarios**:

1. **Given** a daemon with servers A and B marked attach-by-default and C unmarked, **When** a
   sandbox launches on that daemon, **Then** A and B are attached to it before the agent starts, C is
   not, and the sandbox's record lists exactly A and B as attached by switchboard.
2. **Given** the daemon has no servers marked, **When** a sandbox launches, **Then** the launch
   behaves as before this feature — the agent keeps the runtime's own ability to discover and attach
   registered servers during its session — and the record shows no servers attached by switchboard.
3. **Given** a developer marks server D attach-by-default while sandbox S is already running on that
   daemon, **Then** S is unchanged (its attached set is what it was given at launch) and the *next*
   sandbox launched on that daemon receives D.
4. **Given** a server marked attach-by-default is removed from the daemon (from the TUI or directly
   on the host), **When** a sandbox launches afterwards, **Then** the launch does not fail on the
   stale name: the server is simply not attached and the launch output names what was skipped.
5. **Given** a default server exists but is not yet authorized, **When** a sandbox launches,
   **Then** the launch proceeds, the server is attached in whatever unauthorized state the runtime
   permits, and the launch output records that authorization is outstanding.
6. **Given** two daemons, **When** server E is marked attach-by-default on daemon 1 only, **Then**
   sandboxes on daemon 2 never receive E.
7. **Given** a sandbox launched with default servers, **When** it is stopped and started again, or
   refreshed, **Then** it keeps the same attached servers (the set is fixed at launch).
8. **Given** a daemon in *additive* mode (the default) with server A marked and C unmarked, **When** a
   sandbox launches and its agent looks for C, **Then** the agent can discover and attach C itself;
   **Given** the same daemon switched to *exclusive* mode, **When** a sandbox launches, **Then** its
   agent has A and cannot discover or attach C.
9. **Given** the developer changes the daemon's default attach mode, **Then** no existing sandbox
   changes; only sandboxes launched afterwards use the new mode.

---

### User Story 3 - Kits are authored against kit schema version 2 (Priority: P1)

A developer opens the kit editor and saves a kit. What switchboard writes — and what the daemon hands
to the host runtime at launch or attach — is a kit in the runtime's **current grammar, schema
version 2**: credentials declared as the services a kit needs and how they are injected (never where
their values come from), network access as allow/deny permissions, environment variables, setup
split into install commands, files and startup commands, and agent instructions. A kit the developer
saved before this change, in the legacy grammar, opens in the editor already translated into the new
shape, with anything that has no equivalent called out rather than silently dropped, and is written
back in the new grammar the next time it is saved. Validation remains the host runtime's: a saved
kit passes the runtime's own check on a runtime at or above the version that introduced schema 2,
and the daemon reports whether each host meets that baseline.

**Why this priority**: Every kit switchboard writes passes through the runtime, and the runtime
documents schema 2 as current and the earlier grammar as legacy. Staying on the legacy grammar is
the one thing in this feature that gets *worse* with time; moving now, while the editor still owns
every field it writes, is the cheapest it will ever be. It is independent of the MCP stories (a kit
carries no MCP servers in this feature) and testable on its own.

**Independent Test**: Save a kit with one entry in every section from the editor. Confirm the file
declares schema version 2 and the runtime's validator accepts it on a baseline-version host. Open a
kit saved by the previous release (legacy grammar) and confirm the editor shows the same intent in
the new shape, lists anything it could not carry over, and re-saves as schema 2. Attach a kit that
declares a startup command to a *running* sandbox and confirm the attach is refused with the reason
and the alternative, and that the same kit applies in full at launch.

**Acceptance Scenarios**:

1. **Given** the kit editor, **When** the developer saves a kit, **Then** the stored kit declares
   schema version 2 and uses the version-2 grammar for every section the editor renders, and the
   runtime's validator on a baseline-version host accepts it without deprecation warnings.
2. **Given** a kit saved in the legacy grammar, **When** it is opened, **Then** the editor shows it
   translated into the version-2 shape (network allow/deny, credential services, environment,
   install/files/startup, agent instructions) and marks it as migrated; **When** it is saved,
   **Then** it is written in schema 2 and the legacy form is gone.
3. **Given** a legacy kit that declares where a credential's value comes from on the host, **When**
   it is migrated, **Then** the credential's *service* and injection settings are carried over, the
   host-source declaration is dropped because schema 2 has no such notion, and the developer is told
   that the value is now supplied and bound on the host.
4. **Given** a kit that declares startup commands or files, **When** the developer attaches it to a
   running sandbox, **Then** the attach is refused *before* anything is sent, naming the sections the
   runtime does not accept on an existing sandbox and pointing at the launch-time path; **When** the
   same kit is selected at launch, **Then** every section applies.
5. **Given** a kit that declares only environment variables, install commands and network allow
   entries, **When** attached to a running sandbox, **Then** the attach proceeds as today.
6. **Given** a host whose runtime predates schema 2, **When** the daemon starts, **Then** it
   records and reports the runtime version and that it is below the baseline; kit operations on that
   host are refused with that reason, while sandboxes without kits keep working.
7. **Given** a kit in schema version 3, **When** the developer tries to attach it, **Then** it is
   refused with the reason that the runtime's built-in agents are version-2 environments and do not
   compose with version-3 kits.
8. **Given** an equivalent kit in the legacy and the new grammar, **When** each is applied to a
   fresh sandbox, **Then** the sandbox's network access, environment, installed tooling, startup
   behavior and agent instructions are the same.

---

### User Story 4 - A daemon screen beside the sandbox list (Priority: P2)

The sandbox list is one of two top-level screens. Pressing **shift+→** moves to the **daemon
screen**; **shift+←** moves back. The daemon screen lists every daemon the developer knows about
(one per host) with its connection state and, for a connected daemon, a short list of management
options of which **MCP gateway** is the first. A disconnected daemon can be connected right there
(with the same password prompt the hosts screen uses) and a connected one disconnected; adding or
removing hosts and choosing the active host remain hosts-screen actions. Opening an option shows that
daemon's management view in place; backing out returns to the daemon list; switching screens and
returning preserves where the developer was.

**Why this priority**: The daemon screen is the vehicle for User Story 1 and the natural home for
future per-daemon surfaces. It is P2 only because the MCP gateway view could, in a pinch, be reached
another way; the two-screen layout is what makes the feature discoverable.

**Independent Test**: With two daemons known and one disconnected, press shift+→ from the sandbox
list. Confirm the daemon screen appears with both daemons and their states, the disconnected one
offers *connect* (and no management options) with its reason shown, the connected one offers *MCP
gateway* and *disconnect*, and shift+← returns to the sandbox list with the previously highlighted
sandbox still selected.

**Acceptance Scenarios**:

1. **Given** the sandbox list is showing, **When** the developer presses shift+→, **Then** the
   daemon screen appears; **When** they press shift+←, **Then** the sandbox list returns with its
   selection and scroll position intact.
2. **Given** the daemon screen is showing, **When** the developer presses shift+←, **Then** the
   sandbox list appears, and shift+→ returns them to the daemon they had highlighted.
3. **Given** a connected daemon is highlighted, **When** the developer opens it, **Then** its
   management options are listed with *MCP gateway* among them, and choosing it opens that daemon's
   gateway view.
4. **Given** a daemon that is disconnected, **When** it is highlighted, **Then** its state and the
   reason (if known) are shown, no management option is offered, and *connect* is; **When** the
   developer chooses connect, **Then** the same masked password prompt as the hosts screen appears
   for a remote host (blank = key/agent authentication), success turns the row connected and reveals
   its options, and failure shows the reason in place.
4a. **Given** a connected daemon, **When** the developer chooses *disconnect*, **Then** the row turns
   disconnected, its management options disappear, and any open management view for it closes with a
   message (FR-071).
5. **Given** the developer is inside a daemon's gateway view, **When** they press the back key,
   **Then** they return to that daemon's option list, and once more to the daemon list.
6. **Given** either top-level screen, **Then** the help footer names the shift+←/→ navigation and
   the sandbox-list keys that have no meaning on the daemon screen are not offered there.

---

### User Story 5 - Remote hosts are signed into as the TUI starts (Priority: P2)

A developer with saved remote hosts starts the TUI. Instead of landing with only the local daemon
connected, the TUI works through the saved remote hosts one at a time: a host that connects on its
own (key or agent authentication) just connects; a host that needs a password raises a masked prompt
in the middle of the screen naming the host; the developer types it (or leaves it blank for key
authentication) and the next host follows. A host that fails — wrong password, unreachable, no
daemon running there — shows its failure in place with the choice to retry or skip, and the
developer can always move on; nothing about a failed host prevents using every other one. The whole behavior sits behind a toggle on a new **settings screen** — on by default, flipped in
the TUI and remembered across restarts — and when it is off, startup is exactly as it is today.

**Why this priority**: Cheap and immediately felt by every multi-daemon developer: it removes a
screen visit and several keystrokes from every start, and it means the daemon screen (User Story 4)
shows connected daemons from the first moment.

**Independent Test**: Save two SSH hosts, one accepting key authentication and one requiring a
password. Start the TUI with the setting on. Confirm the first connects silently, a centered prompt
appears for the second, entering the right password connects it, and the sandbox list then shows
sandboxes from both. Repeat with a wrong password and confirm the failure is shown, skipping lands on
the sandbox list with the other host connected, and the failed host can still be connected later from
the hosts screen. Open the settings screen, turn the toggle off, restart the TUI and confirm no prompt appears
and the toggle is still off.

**Acceptance Scenarios**:

1. **Given** the toggle is on and saved remote hosts exist, **When** the TUI starts, **Then** each
   remote host is attempted in turn; hosts that need no password connect without a prompt.
2. **Given** a host that requires a password, **When** its turn comes, **Then** a masked prompt
   appears centered on screen naming the host; **When** the developer submits a password, **Then**
   the connection is attempted with it; **When** they submit blank, **Then** key/agent
   authentication is attempted.
3. **Given** a connection attempt fails, **Then** the failure and its reason are shown for that
   host, the developer may retry (re-prompt) or skip, and skipping proceeds to the next host.
4. **Given** the developer skips a prompt or a host fails, **Then** no other host's attempt is
   affected and the TUI is fully usable once the sequence ends; the skipped or failed host remains
   connectable from the hosts screen as before.
5. **Given** the toggle is off, **When** the TUI starts, **Then** no connection attempt is made
   beyond the local daemon and no prompt is shown; **Given** the toggle was changed, **When** the
   TUI is restarted, **Then** it starts with the value last chosen.
6. **Given** a password was typed at a startup prompt, **Then** it is never stored anywhere; a
   later start prompts again.
7. **Given** a host is already connected when its turn comes (for example adopted from a previous
   step), **Then** it is not prompted for again.

---

### Edge Cases

- A server name that the runtime rejects (disallowed characters, already registered): the host's
  diagnostic is shown and nothing changes.
- Registrations changed directly on the host (via the CLI) while the TUI is open: the TUI's list
  is refreshed on demand and whenever a daemon operation completes, so the view converges on the
  host's truth; switchboard-side marks for a name that no longer exists are dropped on reconcile.
- Authorization never completes (the developer closes the link, or the provider times out): the
  wait ends at the 10-minute bound (or earlier on cancel), the registration is kept and reported as
  unauthorized rather than hanging the TUI, and the developer can authorize later or remove it.
- The developer is attached to a remote daemon whose host has no browser: the authorization link is
  shown as text to open elsewhere — never auto-opened on the daemon's host.
- A locally launched server's command fails at sandbox start: the sandbox still launches; the
  launch output records the failure (the server's health is the runtime's concern, not the
  sandbox's state).
- Two TUIs edit the same daemon's registrations: last write wins on the host; each TUI's next
  refresh shows the merged result; switchboard-side marks follow the host's name list.
- The persisted settings are missing or unreadable at startup: defaults apply (sign-in on), the
  settings screen notes it, and the first change rewrites them.
- Many saved hosts at startup: prompts are strictly sequential and each can be skipped; the local
  daemon is never part of the sequence.
- The startup sequence is still running when the developer presses a key meant for the sandbox
  list: the prompt owns the keyboard while it is showing (it is modal), but skipping is always one
  key away and the sequence never retries on its own.
- The daemon screen is open when a host disconnects or reconnects: its row updates in place and an
  open management view for that daemon is closed with a message rather than left acting on a dead
  connection.
- A daemon's host supports sandboxes but not the gateway commands: the daemon screen shows the
  gateway option as unavailable with the reason; launches on that daemon ignore attach-by-default
  marks (there can be none) and proceed as today.
- A daemon in exclusive mode has no marked servers: there is nothing to restrict with, so the launch
  follows FR-085 (runtime default) and the launch output says so.
- The default attach mode is changed while a launch is in progress: the launch uses the mode it read
  when it started; the change applies from the next launch.
- A legacy kit uses a field with no schema-2 equivalent (a credential's host source, an old
  settings/persistence block): migration carries everything that maps, lists what it dropped, and
  never fails the open; the developer decides whether to save.
- A kit was hand-edited into schema 2 with sections the editor does not render: the editor keeps
  owning only the sections it renders (as today) and says so, rather than pretending to round-trip
  them.
- Two daemons at different runtime versions: the baseline check is per host; a kit attach is refused
  only on the host that fails it, and the daemon screen shows each host's runtime version.
- The runtime adds schema-2 fields later: unknown fields in a hand-edited kit are not an error for
  switchboard; validation stays the runtime's.

## Requirements *(mandatory)*

### Daemon screen & navigation

- **FR-068**: The TUI MUST have two top-level screens — the existing **sandboxes** screen and a new
  **daemon** screen — and MUST switch between them with shift+→ (sandboxes → daemon) and shift+←
  (daemon → sandboxes). Each screen MUST preserve its selection and scroll position across switches.
- **FR-069**: The daemon screen MUST list every known daemon (one per saved host, local included)
  with its display name and connection state, and for a connected daemon MUST offer a list of
  management options of which **MCP gateway** is one distinct entry. Disconnected daemons MUST show
  their state (and reason when known) and offer no management options. The daemon screen MUST offer
  **connect** on a disconnected daemon — for a remote host via the same masked password prompt the
  hosts screen uses (blank = key/agent authentication), with failures shown in place — and
  **disconnect** on a connected one. Adding and removing hosts and choosing the active host remain
  hosts-screen actions.
- **FR-070**: Opening a management option MUST show that daemon's management view in place; the
  back key MUST return to the option list, then to the daemon list. Keys that act on sandboxes MUST
  NOT be active on the daemon screen, and both screens' help footers MUST name the shift+←/→
  navigation.
- **FR-071**: The daemon screen MUST reflect host connection changes live (a row's state updates
  without a manual reload), and a management view open for a daemon that disconnects MUST close with
  a message rather than continue to act on it.

### MCP gateway management (per daemon)

- **FR-072**: Each daemon MUST own exactly one MCP gateway manager scoped to its host: every
  listing and every operation acts on that host's registrations only, and no operation on one daemon
  MUST ever change another daemon's registrations or marks.
- **FR-073**: The gateway view MUST list the daemon's registered servers with, for each: name, kind
  (remote endpoint, or host-launched local server), target (address or command), authorization state
  where the runtime reports one, and the attach-by-default mark. The view MUST also show the
  daemon's **default attach mode** (FR-100) and let the developer change it. The list MUST be
  refreshable on demand and MUST be refreshed after every completed operation.
- **FR-074**: The developer MUST be able to register a server by name with either a remote address
  or a host-run command (with arguments and optional working directory), and MUST be able to remove a
  registered server. Removal MUST clear the server's attach-by-default mark and MUST relay any note
  the runtime gives about credential material left behind.
- **FR-075**: Registering a server that requires authorization MUST surface the authorization link
  *in the TUI* and complete when authorization finishes; the developer MUST be able to choose to
  register without authorizing and to run *authorize* later on any registered server. The link MUST
  never be opened automatically on the daemon's host. Every authorization wait MUST be bounded at
  **10 minutes** (a package constant, like the existing operation timeouts) and MUST be cancellable
  by the developer at any time; the TUI MUST show the time remaining while it waits. On timeout or
  cancel the registration MUST be kept and reported as unauthorized — never dropped — and MUST be
  authorizable later from the list.
- **FR-076**: Registering a host-launched local server MUST warn, before anything is registered,
  that such a server runs on the daemon's host outside sandbox isolation, and MUST proceed only on
  explicit acceptance.
- **FR-077**: Editing MUST distinguish switchboard-side settings (the attach-by-default mark) from
  the registration itself: changing a mark MUST NOT re-register or re-authorize the server; changing
  the registration (target, command, kind) MUST be presented as a replace, with a warning when the
  runtime would discard the server's authorization as a result.
- **FR-078**: Every failure reported by the host for a gateway operation MUST be shown to the
  developer verbatim, and the list MUST remain as it was before the failed operation.
- **FR-079**: A daemon whose host cannot manage a gateway (sandbox CLI missing, too old, or not
  signed in) MUST present the gateway option as unavailable with the reason; no operation that
  cannot work may be offered, and the rest of the TUI MUST be unaffected.

### Attach by default at launch

- **FR-080**: Every registered server on a daemon MUST carry an **attach to every sandbox** mark,
  settable when registering and changeable afterwards, owned by that daemon and persisted across
  daemon restarts. Marks are per daemon: the same server name on two daemons has two independent
  marks.
- **FR-081**: When a sandbox is launched on a daemon, the daemon MUST attach every server marked
  attach-by-default at that moment, before the agent starts, and MUST record on the sandbox the
  exact set of servers attached. For now this is the only path by which *switchboard* attaches a server to a
  launching sandbox: launches MUST NOT accept per-launch server selections and kits MUST NOT declare
  servers.
- **FR-082**: The set switchboard attaches MUST be fixed at launch: later mark or mode changes MUST
  NOT alter running or stopped sandboxes, and the set MUST survive the sandbox's stop/start and
  refresh. Servers an agent attaches for itself (additive mode) are the runtime's affair and are not
  part of switchboard's record. Attaching servers to an existing sandbox from switchboard is out of
  scope for this feature.
- **FR-083**: A mark whose server no longer exists on the host MUST NOT fail a launch: the daemon
  MUST reconcile marks against the host's registrations before attaching, skip stale names, drop
  their marks, and record what was skipped in the launch output.
- **FR-084**: A default server that is not yet authorized MUST NOT block a launch; the launch output
  MUST record that the server's authorization is outstanding.
- **FR-085**: When a daemon has **no** attach-by-default servers, a launch MUST (in either attach
  mode) leave the sandbox's gateway in the runtime's default *dynamic* mode — the agent may itself discover and
  attach any server registered on that daemon during its session. Attach-by-default is therefore
  the only way *switchboard* attaches a server at launch; it does not restrict what an agent may
  attach for itself on a sandbox launched without marks. (A sandbox launched *with* marks receives
  its fixed pre-loaded set, per FR-082.)
- **FR-086**: The sandbox list MUST show, for each sandbox, which servers switchboard attached to
  it at launch (or that none were) and the attach mode in effect, and the launch output MUST name
  each server attached or skipped.
- **FR-100**: Each daemon MUST have a **default attach mode** setting, changed from its MCP gateway
  view and persisted with the marks, read at launch time: *additive* (the default) — the sandbox is
  created in the runtime's dynamic mode and the marked servers are attached to it after creation
  and before the agent starts, so the agent also keeps discovery of every other server registered on
  that daemon; *exclusive* — the marked servers are pre-loaded as the sandbox's fixed static set and
  the agent cannot discover or attach any other. A mode change MUST NOT alter existing sandboxes.
  With no marks, FR-085 applies in either mode.

### Startup sign-in for remote hosts

- **FR-087**: A client setting — **automatic host sign-in** — MUST exist as a toggle on the
  settings screen (FR-101), MUST default to on, MUST be persisted by the TUI so a restart starts
  with the last chosen value, and MUST be documented with the other client settings. When off,
  startup MUST behave exactly as before this feature.
- **FR-101**: The TUI MUST have a **settings screen**, reachable by a single documented keystroke
  from either top-level screen and listed in their help footers, that shows every TUI-persisted
  setting with its current value and lets the developer change it in place. Changes MUST take effect
  immediately where they can (the sign-in toggle governs the *next* start) and MUST be persisted
  client-side, per user, without the daemon's involvement. In this feature the automatic host
  sign-in toggle is the screen's first entry; the screen exists so later settings slot in without
  another navigation change. If the persisted settings cannot be read, defaults MUST apply and the
  screen MUST say so rather than fail.
- **FR-088**: When on, the TUI MUST at startup attempt every saved remote host in turn (the local
  daemon excluded, already-connected hosts skipped), connecting without a prompt where no password is
  needed and otherwise showing a masked password prompt centered on screen that names the host.
  Submitting a blank password MUST attempt key/agent authentication.
- **FR-089**: A failed attempt MUST show the failure and its reason for that host and MUST offer
  retry (re-prompt) or skip; skipping MUST proceed to the next host. No host's failure may prevent
  any other host's attempt, and the TUI MUST be fully usable when the sequence ends.
- **FR-090**: Passwords typed at startup prompts MUST never be persisted; failed or skipped hosts
  MUST remain connectable from the hosts screen exactly as before.

### Kit schema version 2 baseline

- **FR-091**: Every kit switchboard authors, stores or materializes for the host runtime MUST
  declare kit schema version 2 and use the version-2 grammar. Switchboard MUST NOT write the legacy
  (version-1) grammar anywhere.
- **FR-092**: A saved kit in the legacy grammar MUST be migrated to schema 2 when it is loaded,
  section for section per the runtime's published legacy-to-2 mapping; the migration MUST be
  lossless for every section that has an equivalent, MUST report anything it drops rather than drop
  it silently, MUST never fail the load, and MUST persist the version-2 form on the next save.
- **FR-093**: The kit editor MUST author the version-2 sections: identity (name, display name,
  description, version), credentials as the list of services the kit needs with their injection
  settings and required flag, network permissions (allow and deny), environment variables, setup
  (install commands, files, startup commands), agent instructions, and the optional base-agent
  requirement. The editor MUST NOT offer a host-side credential *source*; it MUST state that values
  are supplied and bound on the host. The switchboard-owned sidecars (escape-hatch commands,
  services) are unchanged and stay outside the kit.
- **FR-094**: Validation remains the host runtime's own check. Switchboard MUST validate the
  version-2 rendering through it before a kit is attached or offered at launch, and MUST show the
  runtime's diagnostics verbatim; a kit that fails validation MUST NOT be attached.
- **FR-095**: Attaching a kit to an *existing* sandbox MUST respect the runtime's rule that only
  environment variables, install commands and network allow entries can be added to a running
  sandbox. A kit declaring any other section (files, startup commands, network deny, and so on) MUST
  be refused for attach before anything is sent, with the blocking sections named and the launch-time
  path offered. Sections MUST NOT be silently stripped to make an attach succeed.
- **FR-096**: The daemon MUST declare a minimum supported host runtime version — the earliest
  release that accepts kit schema 2 — MUST read the host's runtime version at startup, MUST report
  both (so the daemon screen can show them), and MUST refuse kit operations with that reason on a host
  below the baseline while leaving kit-less sandbox operations working.
- **FR-097**: Every interaction switchboard has with the host runtime (launch, attach, refresh,
  exec, ports, network policy, option discovery, listing, lifecycle) MUST work against the baseline
  release. An interaction that depends on a command or flag absent from that release MUST be
  replaced or retired as part of this feature, not left to fail at runtime.
- **FR-098**: Moving to schema 2 MUST NOT change what a sandbox receives: an equivalent kit in the
  legacy and the version-2 grammar MUST yield the same network access, environment, installed
  tooling, startup behavior and agent instructions.
- **FR-099**: Kits in schema version 3 are out of scope: attaching or launching with one MUST be
  refused with the reason that the runtime's built-in agents are version-2 environments that do not
  compose with version-3 kits.

### Key Entities

- **Daemon**: one switchboard daemon per host (local or remote), already known to the TUI through
  its saved hosts; now also the owner of one MCP gateway manager and the subject of the daemon screen.
- **MCP server registration**: an entry in a daemon host's gateway: name (unique on that host),
  kind (remote endpoint, or host-launched local server), target (address, or command + arguments +
  working directory), authorization state as the runtime reports it. Owned by the host runtime;
  switchboard reads and edits it through the host's own tooling.
- **Attach-by-default mark**: switchboard-owned, per daemon, keyed by server name; true means every
  sandbox launched on that daemon receives the server. Reconciled against the host's registration
  list; dropped when its server disappears.
- **Default attach mode**: switchboard-owned, per daemon, *additive* (default) or *exclusive*;
  decides how marked servers reach a launching sandbox (FR-100). Persisted with the marks; changed
  from the daemon's MCP gateway view.
- **Sandbox attached-server set**: the exact list of server names switchboard gave a sandbox at
  launch, plus the attach mode in effect, recorded on the sandbox, fixed for its life, shown in the
  sandbox list.
- **Client settings**: the TUI-persisted, per-user preferences shown on the settings screen; in
  this feature one entry, **automatic host sign-in** (on by default), gating the startup sign-in
  sequence. Distinct from the environment-driven client configuration, which is unchanged.
- **Startup sign-in attempt**: one host's outcome during the sequence — connected, skipped, or
  failed with reason — shown in place and discarded when the sequence ends.
- **Kit (schema 2)**: a client-authored kit in the runtime's current grammar — identity, credential
  services with injection settings, network allow/deny permissions, environment variables, setup
  (install / files / startup), agent instructions, optional base-agent requirement — plus the
  switchboard-owned sidecars that never enter the kit. Carries a migration note when it was
  translated from the legacy grammar.
- **Runtime baseline**: the minimum host runtime version switchboard supports (the first that
  accepts kit schema 2), compared per host against the version the daemon reads at startup and
  surfaced on the daemon screen.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can register a remote-endpoint server on a chosen daemon from the TUI in
  under 60 seconds of interaction (excluding any third-party authorization step), and it is visible
  to the host's own tooling immediately afterwards.
- **SC-002**: 100% of sandboxes launched on a daemon with N attach-by-default servers start with
  exactly those N servers attached by switchboard, in the daemon's default attach mode, with no
  per-launch action by the developer.
- **SC-003**: Zero cross-daemon leakage: across any sequence of registrations, removals and mark
  changes on one daemon, every other daemon's registration list and marks are byte-for-byte
  unchanged.
- **SC-004**: Every failed gateway operation shows the host's own diagnostic within 5 seconds of the
  failure, and leaves the list unchanged, in 100% of cases.
- **SC-013**: No authorization wait exceeds 10 minutes; 100% of timed-out or cancelled waits leave
  the registration present and marked unauthorized, with the TUI responsive throughout.
- **SC-005**: Switching between the sandboxes and daemon screens is a single keystroke in each
  direction and preserves the selection on both sides 100% of the time.
- **SC-006**: With the sign-in setting on and K saved remote hosts, the developer reaches a sandbox
  list showing every reachable host's sandboxes after at most K prompts and without visiting the
  hosts screen; with the setting off, zero prompts appear.
- **SC-007**: A host that cannot be connected at startup is reported with a reason within 30 seconds
  of its attempt beginning and never delays or prevents any other host's connection.
- **SC-008**: A stale attach-by-default mark (server removed on the host) causes zero failed
  launches; the skip is visible in the launch output every time.
- **SC-009**: 100% of kits saved by the editor declare schema version 2 and pass the runtime's own
  validation on a baseline-version host with no deprecation warnings.
- **SC-010**: Every kit saved by the previous release opens without error; 100% of sections with a
  schema-2 equivalent are carried over, and every dropped item is listed to the developer.
- **SC-011**: 100% of attach attempts that the runtime would reject for an existing sandbox are
  refused by switchboard beforehand with the blocking section named — zero round-trips to the host
  that end in a runtime rejection for that reason.
- **SC-012**: A daemon on a host below the runtime baseline starts, reports the version mismatch
  within its normal startup time, and serves kit-less sandbox operations normally.

## Key Decisions

1. **The host runtime owns registrations; switchboard owns the marks** — registrations live where
   the runtime keeps them and are read and edited through the host's own tooling, so the TUI, the
   host CLI, and the agent's gateway always agree. The one thing the runtime has no notion of —
   "attach this to every sandbox launched here" — is switchboard's own per-daemon record, keyed by
   server name and reconciled against the host's list whenever it is read.
2. **Per-daemon, not global** — a gateway manager exists per daemon because the runtime's
   registrations are per host. There is deliberately no "register everywhere" action in this
   feature; the developer registers on each daemon they want it on, and the daemon screen makes that
   a short trip.
3. **Attach at launch as a fixed pre-loaded set** — sandboxes receive their default servers before
   the agent starts and keep that set for life (it survives stop/start and refresh). Hot-attaching to
   an existing sandbox is a known runtime capability but out of scope here; it is the natural next
   feature once attach-by-default proves out.
4. **Edit = change the mark, or replace the registration** — the runtime has no in-place edit, and
   replacing a registration can discard its authorization. The TUI therefore keeps mark changes
   cheap and side-effect-free and makes registration changes an explicit, warned replace.
5. **Authorization links are shown, never opened** — the daemon may be on another machine; the
   developer opens the link where they are. This is the only workable model for remote daemons and
   is harmless for local ones.
6. **No marks means the runtime's default, not a lockdown** — a sandbox launched on a daemon with
   no attach-by-default servers is left exactly as the runtime would leave it: its agent may discover
   and attach registered servers itself. Marks add a pre-loaded set; they never subtract a
   capability. Restricting agents to marked servers only was considered and deferred — it would
   trade the runtime's discovery feature for predictability nobody has yet asked for.
7. **Startup sign-in is sequential, modal per prompt, and always escapable** — one prompt at a
   time is the only arrangement that is unambiguous about *which* host a password is for; skip is
   always available so a stubborn host never holds the session hostage; passwords are never stored.

8. **Schema 2, not 3** — the runtime's built-in agents (the ones switchboard launches) are
   version-2 environments, and version-3 kits do not compose with them. Schema 2 is therefore the
   only grammar that can both be current and work with switchboard's sandboxes; version 3 is
   revisited only if the runtime moves its built-in agents to it.
9. **Migrate on load, write on save** — saved kits are translated the moment they are opened, so
   the developer always edits the current shape, but nothing on disk changes until they choose to
   save. Drops are reported, never silent; the migration never blocks opening a kit.
10. **Refuse, never strip** — the runtime accepts only a subset of sections on an existing sandbox.
    Quietly removing the rest to make an attach succeed would give the developer a kit that is not
    the one they chose; refusing with the blocking sections named keeps the kit's meaning intact and
    points at the path where it applies in full.
11. **A declared, checked baseline instead of "whatever is installed"** — every prior feature
    assumed the host CLI from documentation alone. Declaring the minimum runtime version and reading
    the host's at startup turns silent drift into a reported mismatch per host, and gives planning a
    fixed target to reconcile every assumed command against.

12. **Attach mode is a per-daemon choice, additive by default** — pre-loading a static set makes
    the gateway drop the agent's discovery tools, so with a single global behavior marking one
    server would silently *remove* a capability the no-marks decision deliberately keeps. Additive
    mode (dynamic gateway, defaults attached right after creation) keeps marks purely additive;
    exclusive mode is there for daemons whose owner wants "what is marked is all the agent gets".
    Making it per daemon rather than per sandbox matches where the marks live and keeps launch
    free of per-launch choices.
## Assumptions

- **Host tooling surface**: the host sandbox CLI provides the gateway operations this feature needs
  — list, register (remote address or host-run command), remove, authorize, and a way to pre-load a
  named set of servers into a sandbox at creation — and reports its own diagnostics on failure. As
  with prior features, that CLI is not installed in the development environment, so the exact
  surface MUST be reconciled against the real runtime early in planning; the operations above are
  what this spec requires, not how they are invoked.
- **Signed-in host**: managing the gateway requires the daemon's host to be signed into the sandbox
  tooling; when it is not, the feature reports that rather than attempting to sign in on the
  developer's behalf.
- **Secrets are out of scope**: servers that need stored secrets (API-key headers, confidential
  client secrets) can be registered, but entering those secrets is done on the host with the
  runtime's own secret tooling; the TUI shows whatever secret/authorization state the runtime reports
  and does not collect secret values.
- **Advanced registration options are out of scope**: custom request headers, OAuth client
  identifiers and scopes, and registry/manifest-based local servers are not offered in the TUI in
  this feature; a developer who needs them registers on the host directly and the TUI lists and
  marks the result like any other registration.
- **No per-sandbox or per-kit server selection**: the attach-by-default mark is the only attachment
  path; launch wizard and kits are unchanged except that the launch records and shows the attached
  set.
- **Daemon screen content**: in this feature the daemon screen's only management option is the MCP
  gateway; the option list exists so future per-daemon surfaces slot in without another navigation
  change. Connect/disconnect on the daemon screen reuse the hosts screen's existing flows (one
  password prompt, one connection path); the hosts screen itself is unchanged.
- **Saved hosts are the sign-in population**: the startup sequence covers hosts the developer has
  already saved; adding hosts remains a hosts-screen action.
- **Setting surface**: the automatic sign-in flag is a TUI-persisted toggle on the new settings
  screen (clarified), not an environment variable; the existing environment-driven client
  configuration is unchanged. The settings screen is reached by a dedicated keystroke from either
  top-level screen rather than as a third stop in the shift+←/→ cycle, keeping that cycle the
  two-screen toggle the feature asked for.
- **Baseline version**: the runtime documents kit schema 2 as supported from Docker Sandboxes
  0.36; that release is the expected baseline, and the exact value is confirmed during planning
  against the runtime (not installed in the development environment). Hosts below it keep kit-less
  sandbox operation; kit features report the mismatch.
- **Legacy-to-2 mapping is the runtime's**: the published mapping (network block → network
  permissions; credential sources → credential services with injection; proxy-managed environment →
  credential setting; commands/init files → setup install/files/startup; memory/agent context →
  agent instructions) is the migration contract; switchboard adds no mapping of its own.
- **Kit static files and arguments stay out of the editor**: schema 2 also allows a bundled files
  tree and declared arguments; neither is authored by the editor in this feature (hand-authored kits
  that use them are listed and attached like any other). They are the natural follow-up for shipping
  skills and other files through kits.
- **Credential values and bindings are host-side**: the editor declares which credential services
  a kit needs and how they are injected; supplying values and approving bindings happens on the host
  with the runtime's own tooling, as schema 2 requires.
