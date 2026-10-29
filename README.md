# Switchboard

Switchboard is a terminal UI for managing Docker **sandbox sessions** across one or many
machines. It lets a developer fan out parallel coding tasks into isolated sandboxes — each
working from a **verbatim duplicate** of selected directories, so the originals are never
touched — and manage their whole lifecycle, prompt the coding agents inside them, get notified
when those agents finish or need input, and open any sandbox in VS Code.

It has two parts:

- **`sxbd`** — a per-host **daemon**. It wraps the host's sandbox tooling (`sbx`), owns a
  controlled workspace folder into which it copies sources, manages sandbox lifecycle
  (launch / stop / restart / destroy), persists a registry so it can re-adopt still-running
  sandboxes after its own restart, and reports agent task/notification events.
- **`sxb`** — a **Bubble Tea TUI** the developer runs locally. It connects to a local
  daemon over a Unix socket and to remote daemons over SSH, groups and navigates sandboxes across
  hosts, saves reusable configurations, prompts agents, raises notifications, and opens sandboxes
  in VS Code.

> Status: all five user stories are implemented and tested. See
> [`specs/001-sandbox-session-manager/`](specs/001-sandbox-session-manager/) for the spec, plan,
> and task breakdown.

## Architecture at a glance

```
   ┌────────────────────────┐         Unix socket (local)
   │  sxb (TUI)              │◀──────────────────────────────┐
   │  Bubble Tea client      │                               │
   │  • configs / groups     │         ssh <host>            ┌┴───────────────────┐
   │  • known hosts          │◀───────  sxbd  ──────────────▶│ sxbd                │
   │  • notifications        │         dial-stdio (remote)   │ (per-host daemon)   │
   └────────────────────────┘                               │ • sbx lifecycle     │
        source of truth: TOML under                          │ • verbatim copies   │
        $XDG_CONFIG_HOME/switchboard/                        │ • bbolt registry    │
                                                             │ • agent hooks/PTY   │
                                                             └─────────┬───────────┘
                                                                       │ shells out to
                                                                       ▼  sbx / Docker
```

Transport is gRPC over a single connection — a Unix domain socket locally, and the Docker-CLI
`dial-stdio` pattern (`ssh <host> sxbd dial-stdio`) for remote hosts, so it rides your
existing SSH with no new network port or auth system.

## Repository layout

A Go `go.work` workspace with six modules (plus E2E siblings), placed under the constitution's
category taxonomy:

```
src/
├── libs/switchboard-proto/      # shared gRPC/protobuf contract + domain helpers
├── libs/switchboard-dial/       # daemon dialer shared by the TUI and the daemon (local socket, `ssh … sxbd dial-stdio`)
├── libs/switchboard-termview/   # VT renderer shared by the TUI's terminal view and the daemon's agent screen
├── services/switchboardd/       # the daemon  (cmd/sxbd, internal/…)
├── services/switchboardd-e2e/   # daemon E2E (real Docker, gated)
├── libs/switchboard-update/     # shared self-update core (release lookup, verify, apply)
├── apps/switchboard-tui/        # the TUI client (cmd/sxb, internal/…)
└── apps/switchboard-tui-e2e/    # TUI E2E (PTY-driven)
```

Per-module details and the documented Go-toolchain deviations from the repo constitution live in
each module's `README.md`.

## Prerequisites

- **Go ≥ 1.26** (`go version`).
- **Docker** running on each host that will run sandboxes (`docker version`).
- The host's sandbox CLI **`sbx`** on `PATH` for any host that launches sandboxes.
- **SSH** access (key/agent auth) to any remote host you want to connect to.
- **VS Code** for the open-in-VS-Code feature — it opens the sandbox's controlled workspace folder
  (the copied files); add `ms-vscode-remote.remote-ssh` to open that folder on a remote host.
- For development only: `golangci-lint` at the version CI uses, needed by `make fmt` / `make fmt-check`
  as well as `make lint` (`make lint-version` prints it; any
  build made with Go ≥ 1.26 works, an older one fails with `can't load config: … lower than the
  targeted Go version`), and `protoc` + `protoc-gen-go`/`protoc-gen-go-grpc` if you regenerate the
  gRPC stubs (the generated code is committed, so this is not needed just to build).

## Install

Both `sxb` and `sxbd` are distributed as prebuilt binaries for macOS and Linux (amd64 + arm64)
from [GitHub Releases](https://github.com/jamesclark123/switchboard/releases). Install on **every
machine** that will run the TUI or the daemon — including each remote host you connect to.

### Quick install (recommended — self-update capable)

```bash
curl -fsSL https://raw.githubusercontent.com/jamesclark123/switchboard/main/install.sh | sh
```

It detects your OS/arch, downloads the latest release, verifies its SHA-256 checksum, and installs
`sxb` + `sxbd` to `/usr/local/bin` when you can write to it. Otherwise they go to `~/.local/bin`
and are symlinked into `/usr/local/bin` (the only step that uses `sudo`) — the binaries themselves
always stay owned by you, which is what lets them self-update without elevating. Pin a version with
`SWITCHBOARD_VERSION=vX.Y.Z` or change the target with `SWITCHBOARD_INSTALL_DIR=...`. Binaries
installed this way can update themselves from within the TUI (see [Updating](#updating)).

> GitHub Releases (via the installer above and the in-app updater) is the **only** distribution
> channel — there is no Homebrew tap or OS package. Every binary is SHA-256-verified before it is
> written.

### From source

```bash
git clone --recurse-submodules <repo-url> switchboard   # rules are a submodule
cd switchboard
go build -o bin/sxbd ./src/services/switchboardd/cmd/sxbd
go build -o bin/sxb  ./src/apps/switchboard-tui/cmd/sxb
```

`make build` compiles every module to verify it builds, but does not emit named binaries — use the
commands above (or `go install ./src/services/switchboardd/cmd/sxbd` and
`go install ./src/apps/switchboard-tui/cmd/sxb`) to produce runnable binaries.

> **Note:** `go install …/cmd/sxb@latest` is **not** supported — both app modules use local
> `replace` directives, which `go install pkg@version` rejects. Use the quick installer or a local
> checkout instead.

> **macOS:** binaries installed via the quick installer or the in-app updater are not
> Gatekeeper-quarantined. Only if you download a release `.tar.gz` **manually in a browser** do you
> need `xattr -d com.apple.quarantine sxb sxbd` before first run.

## Configuration

Both binaries read configuration from the environment at startup and **fail fast** if a required
variable is missing. Each ships a committed `.env.example` documenting every key; copy it to a
local `.env` (gitignored) for development.

### Daemon — `sxbd` (`src/services/switchboardd/.env.example`)

| Variable                      | Required | Default                              | Purpose |
|-------------------------------|----------|--------------------------------------|---------|
| `SWITCHBOARDD_WORKSPACE_ROOT` |          | `$HOME/switchboard/workspace`        | Controlled folder for verbatim duplicates |
| `SWITCHBOARDD_DATA_DIR`       |          | `$HOME/switchboard/data`             | Directory for the bbolt sandbox registry |
| `SWITCHBOARDD_SOCKET`         |          | `$XDG_RUNTIME_DIR/switchboard.sock`  | Unix socket the daemon listens on. Falls back to `$HOME/.local/share/switchboard/` when `XDG_RUNTIME_DIR` is unset (e.g. a bare SSH session) |
| `SWITCHBOARDD_PID_FILE`       |          | `$XDG_RUNTIME_DIR/switchboard.pid`   | PID file maintained while serving; read by `status`/`stop` (same `XDG_RUNTIME_DIR` fallback as the socket) |
| `SWITCHBOARDD_HOST_ID`        |          | machine hostname                     | Stable host id advertised to clients |
| `SWITCHBOARDD_SBX_BIN`        |          | `sbx`                                | Host sandbox CLI binary |
| `SWITCHBOARDD_HOOK_ADDR`      |          | `0.0.0.0:8765`                       | Listen addr for the agent hook callback server. Binds all interfaces so sandboxes can reach it via `host.docker.internal`; a loopback-only bind is unreachable from a sandbox |

### TUI — `sxb` (`src/apps/switchboard-tui/.env.example`)

| Variable                  | Default                              | Purpose |
|---------------------------|--------------------------------------|---------|
| `SWITCHBOARD_CONFIG_DIR`  | `$XDG_CONFIG_HOME/switchboard`        | Client TOML state (configs / groups / hosts) |
| `SWITCHBOARD_LOCAL_SOCKET`| `$XDG_RUNTIME_DIR/switchboard.sock`   | Default local daemon socket |
| `SWITCHBOARD_CODE_BIN`    | `code`                               | VS Code CLI used to open sandboxes |
| `SWITCHBOARD_SBX_BIN`     | `sbx`                                | Sandbox CLI used to open a sandbox's terminal (`t`) |
| `SWITCHBOARD_TERMINAL`    | system default                       | Terminal command prefix for the popout terminal (`T`), e.g. `kitty -e`, `gnome-terminal --`, `tmux new-window` |

## Running

### 1. Start the daemon (on every host that runs sandboxes)

The daemon is configured entirely through the environment, and every key has a default — so with
no configuration it stores state under `$HOME/switchboard` (`workspace/` and `data/`). Override any
key via the environment (or a package-local `.env`), then run the `serve` subcommand:

```bash
sxbd serve            # foreground

# …or run it in the background (detached; logs to $SWITCHBOARDD_DATA_DIR/switchboard.log):
sxbd serve --watch    # or -w

# …or have it start automatically and restart if it exits
# (systemd user service on Linux, launchd LaunchAgent on macOS):
sxbd serve --boot

# …or point it elsewhere:
export SWITCHBOARDD_WORKSPACE_ROOT=~/.local/share/switchboard/workspaces
export SWITCHBOARDD_DATA_DIR=~/.local/share/switchboard/data
sxbd serve

# …or troubleshoot: log every RPC action and error to stderr.
sxbd serve --debug
```

`sxbd` subcommands:

| Command | Purpose |
|---------|---------|
| `sxbd serve` | Listen on the Unix socket in the foreground. |
| `sxbd serve --watch` (`-w`) | Detach and run in the background; the parent returns to the shell while the daemon keeps serving (logs go to `$SWITCHBOARDD_DATA_DIR/switchboard.log`). |
| `sxbd serve --boot` | Install a boot-autostart service so the daemon starts automatically and is restarted whenever it exits — it always runs unless explicitly stopped. On **Linux** this is a **systemd user service** (`Restart=always`; requires `systemctl`, plus best-effort `loginctl enable-linger` so it starts without an interactive login). On **macOS** it is a **launchd LaunchAgent** (`~/Library/LaunchAgents`, `KeepAlive`; requires `launchctl`) that starts at login. On other OSes, use `--watch`. |
| `sxbd serve --debug` | (Combinable with the above.) Log every incoming RPC, with timing, and any error it returns. |
| `sxbd status` | Report whether the daemon is running (pid + socket reachability) and whether boot-autostart is enabled. Exits `0` when running, `3` when stopped. |
| `sxbd stop` | Stop the running daemon — a clean `systemctl --user stop` (Linux) or `launchctl unload` (macOS) when boot-managed, so the init system's auto-restart does not respawn it; otherwise `SIGTERM` to the pid-file process. |
| `sxbd dial-stdio` | Bridge stdio ↔ the local socket, used by the SSH remoting path — you do not invoke it directly. |

Only one daemon may serve a given socket at a time: `serve`/`serve --watch` refuse to start
if a live daemon is already recorded in the PID file (a stale file from a crashed daemon is
cleared automatically).

Remote hosts run the **same** `sxbd serve`; the TUI reaches them over SSH via
`ssh <host> sxbd dial-stdio`, which the daemon also provides — no extra setup, no open
network port.

### 2. Run the TUI

```bash
sxb
```

It connects to the local daemon socket by default; add remote hosts from within the UI.

### Using the TUI

The sandbox list is the home screen. Keys:

| Key | Action |
|-----|--------|
| `n` | Launch a new sandbox (pick sources, duplicate/clone) |
| `C` | Launch from a saved configuration |
| `c` | Create / edit a configuration (covers 100% of `sbx` options) |
| `h` | Hosts: connect/disconnect, add SSH host, set active host |
| `g` | Groups: create, assign sandboxes (cross-host), navigate |
| `v` | Open the selected sandbox in VS Code |
| `t` | Open the sandbox's terminal inline (suspends the TUI; resumes on exit) |
| `T` | Open the sandbox's terminal in a popout window (keeps the TUI running) |
| `i` | Notification inbox (task-complete / needs-prompting / escape-hatch; 🔔 badge) |
| `s` | Toggle the selected sandbox: stop if running, start otherwise |
| `d` | Destroy the selected sandbox |
| `K` | Manage agent kits · `A` attach a kit to the selected sandbox |
| `E` | Review the selected sandbox's escape-hatch runs (this session) |
| `S` | Edit the selected sandbox's seeded folders in place: `a` add folders (browse like launch), `space` mark, `d` remove marked (confirmed) — no restart |
| `M` | Show the MCP servers attached to the selected sandbox and attach another from its daemon's registered list — live, no restart |
| `l` | Show the `sbx` output of the selected sandbox's last launch / kit attach / refresh / source add — kit install-command output lives here; a failed launch's log stays reachable |
| `u` | Update the client and all connected hosts (shown when a newer release exists) |
| `shift+→` / `shift+←` | Cycle the three top-level screens — **Sandboxes**, **Daemons**, **Jobs** (wrapping) — named in the header strip (see below) |
| `o` | Attach the selected sandbox to an orchestrator as a worker, or detach it (offers to add the sandbox's host to the orchestrator first) |
| `O` | Show/hide the **Orchestrator Sandboxes** tab: every worker and reasoning sandbox, which the everyday tabs hide unless they need you |
| `m` | Mark a worker free: close the run it is holding for review (optionally as failed) so the orchestrator can use it again |
| `,` | Settings — TUI-persisted preferences (`settings.toml` in the config dir) |
| `F` | Refresh repos (re-seed workspace) · `R` rename · `r` reload · `j`/`k` navigate · `q` quit |

**Forms and unsaved changes.** In every form `ctrl+s` saves, and `esc` and `ctrl+c` are the
same key: they close a form you haven't changed straight away. A changed form, inline input
(rename, tag, add host, add group), kit, launch, reasoning setup or set of marked folders asks
first:

| Key | Unsaved-changes dialog |
|-----|------------------------|
| `s` | Save (the surface's own save or confirm; `r` **Rebuild** on the reasoning setup) |
| `c` · `esc` · `ctrl+c` | Cancel — back to exactly what you were editing (the default) |
| `d` | Discard the changes and leave |
| `←`/`→` · `h`/`l` · `tab`, `enter` | Move the highlight, activate it |

Keys typed in the first 150 ms after the dialog opens are ignored, apart from Cancel.

Multi-line fields (kit instructions and commands, MCP headers, action bodies and arguments,
event arguments) show `ctrl+e enlarge` while focused. There is no external editor: `ctrl+e` opens
the field's text in a full-terminal editor over the screen.

| Key | Enlarged view |
|-----|---------------|
| `esc` · `ctrl+e` | Close, keeping the edits in the field (focus stays on it) |
| `ctrl+s` | Close and save the form |
| `ctrl+r` | Revert to the text as it was when the view opened |
| `pgup` / `pgdn` | Page; `enter` inserts a newline (no line limit; inline fields stop at 99) |
| `ctrl+c` | Leave the form (asks first if it changed) |

A form, input or overlay whose sandbox, host or orchestrator goes away closes, and the status line
says `unsaved edits lost` if it had changes. A low-resource add override that arrives while you're
elsewhere waits until you're back on the list, and it accepts on `y` only.

**Agent kits are schema version 2.** The kit editor (`K`) authors Docker's current kit grammar
(`permissions.network`, `credentials` services, `setup.install/files/startup`, `agentInstructions`).
A kit saved by an earlier release (schema 1) opens migrated in place with a banner listing anything
that had no equivalent (e.g. a credential's host-side source — values are bound on the host now)
and is rewritten as schema 2 when you save. Attaching a kit to a *running* sandbox (`A`) is
refused up front when it declares sections `sbx kit add` cannot apply there (files, startup
commands, network deny, credentials, agent instructions) — launch a new sandbox with it instead.
Schema 3 kits are listed but neither editable nor attachable (they do not compose with the
built-in agents).

**Editing seeded folders after launch.** `S` opens the sources overlay for the selected
sandbox. Adds copy the new folder into the workspace's own `.switchboard/staging/` and rename it
into place only once complete, so a live agent never sees a half-copied tree; removals delete
only the sandbox's copy (never the original) behind a confirmation that names every folder.
Neither operation stops or restarts the sandbox — attached terminals, the agent session, and
running services carry on — and the recorded folder list stays the single source of truth for
the row, `F` refresh, and relaunch. A sandbox always keeps at least one seeded folder, and a
removal is refused while a running service's working directory sits inside the folder.

**MCP servers on an existing sandbox.** `M` opens the selected sandbox's MCP overlay: every
server switchboard attached to it (at launch via attach-by-default marks, or later), with the
kind and authorization state its daemon reports. `a` lists that daemon's other registered servers;
`enter` attaches one with `sbx mcp load`, which the runtime applies live — the agent sees the new
tools without a stop or restart, in additive and exclusive sandboxes alike. Servers the agent
attached for itself through the gateway's discovery tools are not listed. `d` detaches the
highlighted server (after a confirmation) with `sbx mcp unload`: its tools leave the sandbox at once,
nothing stops or restarts, and the server stays registered on the daemon so `a` can attach it again.
If the runtime says the server is no longer loaded, the sandbox record simply drops it. The
orchestrator's own per-worker server is never listed or detachable. Attach and detach accept any
name the host registered; only a name that could be read as a command-line flag (leading `-`), is
invalid UTF-8, or contains control, line/paragraph-separator or format characters (bidi
overrides, zero-width joiners) is refused. Closing the overlay while a call runs does not lose it —
reopening shows it still in progress. Register and authorize servers on the daemon screen first (`shift+→`).

### Daemon screen & MCP gateway

`shift+→` opens the **daemon screen** — every known daemon (one per saved host, local included)
with its connection state, `sxbd`/`sbx` versions, and whether it meets the runtime baseline
(`sbx` ≥ 0.36) and can manage its MCP gateway. `c` connects a disconnected daemon (the same
masked SSH-password prompt as the hosts screen; blank = key/agent auth), `x` disconnects one,
`enter` opens its management options — **MCP gateway**, **Secrets** and **Orchestrators** (see
[Orchestrators](#orchestrators)). `shift+←` returns to the sandbox list with your selection intact.

**Secrets** is the second option: the host runtime's secret store (`sbx secret`) for that daemon.
It lists every stored secret with its scope (global or one sandbox), type and the runtime's masked
value. `a` stores a service secret — the value is typed masked, sent only to that daemon, handed to
`sbx secret set` on standard input once, and never logged or shown again; an optional sandbox name
scopes it. `d` removes the highlighted secret behind a confirmation. Custom secrets
(`sbx secret set-custom`) are listed but managed on the host. Gateway header placeholders are
secrets named `mcp:<server>:<placeholder>`.

**MCP gateway** is the first option. The host runtime registers MCP servers per host and gives
every sandbox a gateway to them; switchboard manages those registrations through `sbx mcp` on
that daemon only:

- `a` registers a server — a remote endpoint URL, or a command the host runs (you must
  acknowledge that a host-run server executes **outside** sandbox isolation).
  Registration never waits on a browser: `A` runs the authorization step separately, shows the
  authorization link **in the TUI** (open it where you are — it is never opened on the daemon's
  host) and waits up to **10 minutes**; `esc` cancels and the registration is kept as
  unauthorized. On a **remote** daemon the provider redirects your browser to
  `http://localhost:<port>`, which is the host's callback listener; the TUI binds that same port
  on your machine and relays it to the host for the duration of the wait, so it just works. If the
  port is busy locally (or press `p`), paste the address your browser failed to load and the
  daemon replays it on the host. `d` removes a server (confirmed) and relays any `sbx secret rm` hints the
  runtime prints. The register form's **Headers** field takes one `Name: value` per line for a
  remote endpoint (each becomes a `--header` flag); write a credential as `${placeholder}` and store
  its value as the secret `mcp:<server>:<placeholder>` under **Secrets** (below) — the gateway fills it
  in on the host, so no token ever reaches a registration or a log. OAuth client IDs and
  registry-based local servers are still managed on the host directly.
- `space` marks a server **attach by default**: every sandbox launched on that daemon gets it.
  `m` toggles the daemon's **default attach mode** — *additive* (default: the sandbox keeps the
  runtime's dynamic gateway, so the agent can still discover other registered servers) or
  *exclusive* (`--static-mcp`: the marked set is all the agent gets). Marks and mode apply at
  launch only; a sandbox's set is fixed for its life and shown as `mcp: …` on its row. A mark
  whose server was removed on the host is skipped and reported in the launch log (`l`), never a
  failed launch.

#### MCP tool collisions

The runtime's MCP gateway puts the tools of every server loaded into a sandbox in **one flat
namespace** under their bare names, and refuses to load a server whose tool names clash with one
already loaded — e.g. Linear's `list_issues` / `list_releases` against GitHub's:

```
tool "list_issues" from server "linear" collides with an existing tool already registered for server "github"
```

`sbx mcp add/load` and `--static-mcp` cannot prefix, rename or filter tools, so switchboard cannot
namespace them for you. When a collision happens a launch skips the server and logs
`mcp: skipped linear: tool name collision with github (list_issues, list_releases); …` (the raw
runtime text follows on the next line), and attaching from the `M` overlay fails with the same
message; nothing is changed on the sandbox. In *exclusive* mode the set is pinned at create, so
a clash fails the launch itself; the launch error names the servers, tools and remedy (additive
mode would skip the clashing server instead).

**On an existing sandbox the simplest fix is often to detach the other server**: if the server it clashes with is attached to that sandbox, press `d` on it in the same `M` overlay and attach again — the attach error says so ("or detach github from this sandbox first"). Detaching only removes it from that sandbox; it stays registered on the daemon.

**Fix: filter the clashing tools out at one of the servers**, using that server's own filter
headers in the register form's **Headers** field (one `Name: value` per line). GitHub's remote
server (`https://api.githubcopilot.com/mcp/`) takes `X-MCP-Toolsets` (comma-separated toolsets)
and `X-MCP-Tools` (individual tools, additive with toolsets). It has no *exclude* header, so
build an allowlist: leave out the toolset that holds the clashing tool and add back, by exact
name, the tools you still want from it. For example, to drop both of GitHub's clashes with Linear,
`list_issues` (toolset `issues`) and `list_releases` (toolset `repos`), leave both toolsets out
and add back every other tool they hold:

```
X-MCP-Toolsets: context,pull_requests,users
X-MCP-Tools: add_issue_comment,get_label,issue_read,issue_write,list_issue_fields,list_issue_types,search_issues,sub_issue_write,update_issue_comment,create_branch,create_or_update_file,create_repository,delete_file,delete_repository,fork_repository,get_commit,get_file_contents,get_latest_release,get_release_by_tag,get_tag,list_branches,list_commits,list_repository_collaborators,list_tags,push_files,search_code,search_commits,search_repositories
```

(Toolset membership is from the
[GitHub MCP server README](https://github.com/github/github-mcp-server#tools) as of 2026-10-25
and changes over time; re-check it, and trim the list to the tools you actually use. An invalid
tool name stops the GitHub server from starting, so copy names exactly.) The filtered tools are no
longer available from that server; use the other
server's tool of the same name. Registration headers can't be edited in place: remove the server
(`d`, which also deletes its OAuth credential) and register it again, then authorize (`A`). For
other servers, use whatever tool-filter option they document.

> The recipe above comes from the servers' documentation and has **not yet been verified on a real
> host** (research R14).

Each daemon declares a **minimum `sbx` version** (0.36, the first with kit schema 2) and reads
the host's version at startup; kit and gateway operations on a host below it are refused with
both versions named, while kit-less sandboxes keep working.

### Orchestrators

An orchestrator is a named, persistent prompt queue that lives in a daemon. It attaches sandboxes
anywhere as **workers**, composes prompts from **actions**, queues a **run** whenever an **event**
calls an action, and delivers runs in order of receipt to free workers. Setting one up lives with
the daemon that hosts it; the day-to-day work of every orchestrator lives on the **Jobs** screen.

**Daemons › Orchestrators.** On the daemon screen, `enter` on a connected daemon and choose
**Orchestrators**: that daemon's orchestrators with their reachability, worker count and queue
depths. `n` creates one on this daemon, `R` renames it (its identity never changes), `d` removes it
(confirmed), `enter` opens it, `esc` returns to the daemon screen. Everything here goes to that
daemon only: if it disconnects the section closes, and nothing is ever sent to another daemon instead. An opened orchestrator has four
views (`←`/`→`; `esc` back to the list) — Lanes only when its daemon is new enough to serve lanes:

| View | What it shows and does |
|------|------------------------|
| Workers | The roster with state and availability (free / busy / needs input / awaiting review) and each worker's lanes (`lanes: a, b`, `open lane`, `⚠ lanes pending`, `detaching…`); `x` detach, `m` mark free, `l` picks the worker's lanes (none ticked = the open lane). The reasoning sandbox is listed last, marked `reasoning`; it has no lanes. |
| Reasoning | The orchestrator's **reasoning sandbox** `reasoning-<id>`, created with the orchestrator on its own host from an empty workspace unless you give it a setup: `e` picks folders (and kits, `K`) through the launch browser and rebuilds it, `b` rebuilds it from the saved setup, `l` shows the `sbx` output. `reason` actions run there and their summary is the answer. It is not detachable; a rebuild is refused while a run is on it; if it is destroyed outside switchboard the view says so and `reason` runs fail until you rebuild. |
| Hosts | The machines the orchestrator can place workers on, with reachability. `a` copies an entry from your saved SSH hosts, `d` removes one (refused while workers live there). |
| Lanes | The orchestrator's **lanes** — named groups of workers — with their members and the actions that use them. `a` adds a lane (name and description), `e` renames or re-describes it, `w` picks its members, `d` deletes it after a confirm listing the workers, actions and queued runs it affects. `D` dismisses the notice of worker tags dropped on upgrade. |

**Jobs.** `shift+→` twice (or `shift+←` from the sandbox list) opens the **Jobs** screen: every
orchestrator on every connected daemon, every row labelled `orchestrator @ host`. A daemon whose
connection failed or was lost (its event stream ended, or a call found it unavailable), or whose
orchestrators could not be read, is named above the content with its error (`could not read: …`);
its rows stay, dimmed, and refuse operations (`<host> is not connected` / `<host>: connection
lost`) until it answers again, while the others still show. A daemon still connecting is listed
as `connecting: …`, and one you disconnected yourself just drops out. Each daemon's rows appear as soon as it answers. The screen follows changes live, one
orchestrator at a time and only while it is on screen (it catches up when shown again); the
highlight stays on its item when rows re-sort. Text written by agents (reports, reasons, prompts) is
shown without terminal control sequences. Four tabs, switched with `←`/`→` (the last one is
remembered); `r` reloads, `esc` goes back to Sandboxes and `q` quits:

| Tab | What it shows and does |
|-----|------------------------|
| Overview | **Upcoming** (each enabled timed event once, soonest next firing first), **Queued** (in receipt order), **In progress** and **History** (the 15 most recently finished runs across all orchestrators). On a run: `enter` shows its composed prompt, `x` cancels a queued run, `m` marks a reviewed run's worker free, `o` **Open results** on a finished run — the agent's full report (or your note, or the reason it failed or was cancelled) in a scrollable overlay (`↑`/`↓`/`pgup`/`pgdn`, `esc` closes). On an Upcoming event: `n` **enqueue now** (below). |
| Actions | Every orchestrator's actions under a heading per orchestrator; an orchestrator with none shows a line you can add from. `a` adds to the orchestrator whose group the cursor is in, `e` edit, `d` delete, `enter` call. An action is a prompt body with `{name}` placeholders (`{{` is a literal `{`), declared arguments (name, required `*`, description, default), a type — `prompt` runs on a free worker, `reason` on the reasoning sandbox — and, for `prompt` actions, optional **lanes**: *exclusive* (only workers in those lanes) or *inclusive* (those lanes first, else open-lane workers). An action with no lanes runs on **open-lane** workers — workers in no lane. In the action form `ctrl+l` opens **Manage lanes** for that orchestrator without leaving the form. A queued run shows why it is waiting (`waiting for a worker in frontend`; `enter` shows it whole). Worker tags from older versions are dropped on upgrade and listed under the orchestrator with `D` to dismiss. Supplied arguments without a placeholder are appended as `name: value`. Values are capped at 16 KiB, the composed prompt at 64 KiB. |
| Events | Timed events, grouped the same way: `a` add, `e` edit, `d` delete, `space` on/off, `n` **enqueue now** (queues one run of the event immediately without touching its schedule; refused while a run of it is already queued or in flight when dedupe is on; with dedupe off it asks first only if a run of the event is already open; a repeat press within half a second of a successful enqueue is ignored, with a hint). An event calls an action every N minutes with fixed arguments. **Dedupe** (on by default) skips a firing while a run of that event is still queued or in flight and counts the skip; after a daemon restart the schedule resumes on its phase without replaying missed slots. Deleting an action disables the events that named it. |
| Queue | Every run the daemons keep (queued, in progress, and the history — the 200 most recent finished runs per orchestrator), with no cap of its own, in pages of 25: `]`/`pgdn` next page, `[`/`pgup` previous; `f` filters to one orchestrator (cycling through them), `F` shows all again. Runs take the Overview's `enter`/`x`/`m`/`o`. |

**Attaching workers.** `o` on a sandbox attaches it to an orchestrator (`o` again detaches; refused
while a run is in flight on it). The orchestrator's **daemon** dials the worker's machine itself —
`ssh <target> sxbd dial-stdio`, BatchMode, keys or agent only — so the orchestrator's host must be
able to reach the worker's host over SSH; when it cannot yet, `o` offers to copy your host entry to
the orchestrator first. The launch wizard's `O` attaches a new sandbox from before its agent starts.
When the orchestrator has lanes, both paths then ask which lanes the worker joins. Saving lanes,
members or an action, or detaching a worker, warns first when it would leave un-laned work with no
open-lane worker to run it.
A worker's daemon registers one gateway MCP server per worker (`sxb-orch-<id>`, hidden from the
gateway view) pointing at a loopback endpoint of that daemon's hook server; anything on that host
holding the token can impersonate the worker to its orchestrator, which is why the endpoint stays on
loopback and the token is revoked the moment the worker is detached.

**Runs and the review rule.** A run goes to the first free, eligible worker (running, agent idle,
no run on it, and in the action's lanes — or with no lanes, for an un-laned action). It ends only with the agent's **completion report**
(`orchestrator_report_completion`). A worker that goes idle without reporting is held for
**review**: the sandbox row surfaces at the top of every tab, the inbox gets a line, and `m` closes
the run with your note (as completed or failed) and frees the worker. A worker that needs input is
surfaced the same way — the inbox then carries both the agent's needs-input line and the run's.
Claude Code's "waiting for your input" nudge after a minute of idleness is not a request for input:
an idle worker is free. Should a worker still sit at `needs input` with no run on it, `m` sets it
back to idle so the queue can reach it again.
Workers that need nothing from you stay hidden from the everyday tabs; `O` shows them all, and
every orchestrated row's third line reads `mcp: … · orchestrator: …`.

**What the worker's agent sees.** A managed block in the workspace `CLAUDE.md` (between
`<!-- switchboard:orchestrator:begin -->` and `…:end -->`, rewritten on rename and action
changes, removed on detach) briefs it: the run frame it will receive, the completion rule, and
four MCP tools on the `sxb-orch-…` server — `orchestrator_list_actions`,
`orchestrator_call_action` (delegate work: a run with the calling sandbox as origin),
`orchestrator_check_run` and `orchestrator_report_completion`. Every delivered run is typed as

```
[switchboard orchestrator "<name>" · run <run_id> · action "<action>"]

<composed prompt>

[When this run is finished, call orchestrator_report_completion with a short summary. …]
```

**Action sequence.** Every action carries a **sequence** around its prompt, edited in the
action form: built-in **Model** (`/model opus|fable|sonnet|haiku`) and **Effort** (`/effort
low|medium|high|xhigh|max|auto`), typed before the prompt, and **Clear** (`/clear`), typed once the
run closes; plus up to 10 custom **pre-prompts** (before the action prompt) and 10 **post-prompts**
(after the run closes), one per line, with the same `{name}` placeholders (not in a prompt's first
word, and none at all in a prompt starting with `!`). The defaults are Model `opus`, Effort
`medium` and Clear on, with no custom prompts; an action saved before sequences existed gets the
defaults. `off` disables a built-in. Steps go one at a time: each is typed only once the agent is
ready, and the next only once the previous one has finished by evidence (a hook or the screen's
confirmation line). A run with steps is delivered step by step; a run without any keeps the
single-prompt delivery. The run's row reads `setting up 2/3` / `cleaning up 1/2` meanwhile, and the
completion report still closes the run as before (setup and cleanup turns raise no task-complete
notice).

**Holds.** After a run closes, its worker stays held until cleanup finishes: *cleaning up* (post
prompts, then Clear; surfaced only once a step is waiting), *needs input (freeing)* (the agent asked
a question during cleanup and you marked it free with `m`: the remaining post-prompts are dropped,
Clear follows once the question is answered) and *needing the developer* (Clear could not be
confirmed: open the worker's terminal and run `/clear`, which ends the hold).

**Halted steps: `R` / `S` / `X`.** A step the agent did not accept (a refused `/model`, a prompt left
in the input box) halts the run, and its row offers `R` retry, `S` skip and `X` fail, each behind a
confirm that defaults to No. Retry warns when the step may already have run; Clear can't be
skipped; failing at a setup step sends no post-prompt but still sends Clear. A run whose agent was
detached by a daemon restart halts the same way, and `R` stops the detached agent and restarts
setup from the first step. `t` on any sandbox whose agent is detached opens **Wait / Replace**
instead of the terminal; Replace stops it and attaches a fresh agent. The daemon only
detects and reports a detached agent and never stops one itself.

### Settings & startup sign-in

`,` opens the settings screen. Its first entry, **automatic host sign-in** (on by default), makes
`sxb` work through your saved remote hosts as it starts: hosts that accept key/agent auth connect
silently; a host that needs a password raises a centered prompt (blank retries keys, `esc` skips);
a host that fails shows why with `r` retry / `esc` skip. Nothing blocks, nothing is stored, and a
skipped host is still connectable from the hosts screen. Settings live in `settings.toml` under
the client config dir.

### Escape Hatch

Some commands can't run usefully inside a sandbox — `pnpm install` wants the real package cache, and
end-to-end tests want real hardware, not a microVM. **Escape Hatch** lets a sandbox's AI run a small,
human-authored set of *whole, fixed* commands **outside** the sandbox — on the daemon's host, in the
sandbox's workspace — and delivers each result back to the agent, even with no terminal attached.

- **Author on a kit.** In the kit editor (`K`), the **Escape-hatch commands** section takes, per
  command: a name, the exact command string, a when-to-use note, a consent mode (*auto-run* or
  *requires-approval*), and optional working dir + max duration. These live in a `kits/<id>/escape-hatch.yaml`
  sidecar — deliberately **not** in the Docker `spec.yaml`, so `sbx` never sees them.
- **Attach & invoke.** Attaching the kit (at launch, or `A` on a running sandbox) makes each command
  invokable by the agent by name and injects a rule into the agent's `CLAUDE.md` describing when to
  use each and that they run on the host. The agent invokes one via a daemon-injected wrapper; the
  daemon validates the name against the sandbox's allowlist and runs only the fixed, pre-authorized
  string — never anything the agent supplies.
- **Only the agent's own process tree.** The wrapper sends the agent's per-launch token, which the
  daemon passes in the agent's environment. A call without one — from your own `sbx exec … bash`
  shell, an IDE terminal or a service — gets 403 `no agent token in this environment; run the escape
  hatch from the agent's session`. On the host you can run the command directly instead.
- **Consent & bounds.** *auto-run* commands run unattended; *requires-approval* commands pause for a
  developer's approval (a 5-minute, deny-by-default window). Every run is bounded by its own or a
  30-minute default timeout, its output capped at 1 MiB, and cancelled if its sandbox stops.
- **Observe.** In-progress and awaiting-approval runs show a row badge; `E` opens the session's run
  history (command, status, exit code, captured output). Runs are in-memory for the daemon's lifetime.

## Updating

Switchboard is designed for frequent releases, so the TUI keeps itself and every connected daemon
up to date with minimal effort.

- **Notification:** `sxb` checks GitHub for the latest release on startup and every 30 minutes
  thereafter (best-effort; silent when offline, opt out with `SXB_NO_UPDATE_CHECK=1`). When a newer
  version exists, a banner appears above the sandbox list and a `u` key becomes available; a release
  that is already known is not re-announced.
- **One keystroke, all machines:** pressing `u` updates to the latest release across the board — it
  drives **every connected daemon** (local and remote, over the existing SSH-tunneled connection)
  to self-update its `sxbd` binary and restart, then swaps the local `sxb` binary and restarts the
  TUI into the new version. All hosts converge on the same version, so no version skew is left
  behind. Each download is SHA-256-verified before anything is replaced.
- **Remote hosts** are reached as `ssh <host> sxbd dial-stdio`, so their `sxbd` lives on that host.
  The `u` fan-out updates them in place; a host you have *not* connected to must be updated there
  directly by re-running the installer.
- **`permission denied` when updating?** The binary sits in a directory its user cannot write —
  typically a root-owned `/usr/local/bin` from an older installer that copied it there with `sudo`.
  Re-run the installer once on that machine; it moves the binaries to `~/.local/bin` (symlinked from
  `/usr/local/bin`) and later updates need no elevation.

## Testing

The project uses the Go toolchain throughout, driven by a top-level `Makefile`. Common targets:

```bash
make build        # compile every module (the lists come from go.work)
make fmt          # golangci-lint fmt across all modules (gofmt + goimports)
make fmt-check    # fail if any file needs formatting (golangci-lint fmt --diff)
make vet          # go vet across all modules, E2E suites included (e2e tag)
make lint         # golangci-lint across all modules, E2E suites included (e2e tag)
make test         # unit + integration tests (go test)
make cover        # tests with the 90% per-module coverage floor enforced
make env-check    # env-schema ↔ .env.example lockstep (Rule VIII)
make all          # fmt-check + vet + lint + test (the standard pre-merge gate)
make e2e          # end-to-end suites (see below)
```

- **Unit + integration** (`make test` / `make cover`): standard `go test`, including gRPC
  client↔daemon tests over an in-process socket, two-daemon orchestrator tests (an orchestrator
  on one in-process daemon driving workers on another over a real link), MCP endpoint tests with
  the official SDK client, and `teatest` golden/interaction tests for the TUI.
  Coverage is enforced at **≥90% per module** (proto, dial, update, daemon, TUI).
- **End-to-end** (`make e2e`, gated behind the `e2e` build tag):
  - The **TUI E2E** builds the real binaries and drives `sxb` through a PTY against a
    daemon backed by a stub `sbx` — it runs anywhere, no Docker required.
  - The **daemon E2E** drives the full lifecycle (launch → stop → restart → destroy + re-adoption)
    against a real Docker + `sbx` runtime, and **auto-skips** when that runtime is absent.

Run a single test from within a module, e.g.:

```bash
cd src/services/switchboardd && go test ./internal/sandbox/ -run TestLaunch -v
```

A Husky `pre-commit` hook runs the fast subset (`fmt-check`, `vet`, `lint`, `test`, `env-check`).
`vet` and `lint` cover every go.work module, so the hook also type-checks and lints the E2E suites
(with the `e2e` tag) without running them; on a cold golangci-lint cache that adds noticeably to a
commit. Running the E2E suites is CI-only (`make e2e`);
CI (`.github/workflows/ci.yml`) runs `fmt-check`, `vet`, `lint` and `env-check`, then the unit +
integration tests of the `MODULES` once via `make cover` (every package except generated stubs;
`cmd/` packages are tested but not counted towards coverage; any failing test's output is
printed), then `make e2e`.

## Regenerating the gRPC contract

The wire contract lives at `src/libs/switchboard-proto/proto/switchboard.proto` (mirrored from
`specs/001-sandbox-session-manager/contracts/`). The generated Go is committed; regenerate it with:

```bash
make proto    # requires protoc + protoc-gen-go + protoc-gen-go-grpc on PATH
```

## Releasing

Releases are built by [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`) via the
`Release` GitHub Actions workflow (`.github/workflows/release.yml`), triggered on any `v*` tag.
Each release publishes cross-platform archives (both binaries per archive) and a `checksums.txt` to
GitHub Releases — the single distribution channel (no Homebrew tap, no OS packages).

**Cut a release:**

```bash
git tag v0.1.0
git push origin v0.1.0        # the Release workflow builds + publishes
```

Version/commit/date are stamped into both binaries via `-ldflags` at build time (a source build
reports `0.1.0-dev`). Dry-run locally with `goreleaser release --snapshot --clean --skip=publish`
and inspect `dist/` (each `switchboard_<os>_<arch>.tar.gz` should contain both binaries).

The workflow needs no secrets beyond the automatically-provided `GITHUB_TOKEN` (it publishes only
to this repo's Releases — there is no separate tap repo or PAT to configure).

The build is hermetic per module (`GOWORK=off` + each module's `replace` directive), so every
module's `go.sum` must be self-contained — keep them tidy with `GOWORK=off go mod tidy` in each
module under `src/`.

## Project governance

Engineering rules are vendored as a git submodule under `.claude/rules/shared/` and the active
feature is specified under `specs/001-sandbox-session-manager/`. This feature is built in **Go**
(Bubble Tea is a Go library), which deviates from the constitution's TypeScript tooling; the
deviations and their Go-toolchain equivalents are recorded in the plan's Constitution Check and in
each module's `README.md`. See [`CLAUDE.md`](CLAUDE.md) for day-to-day contributor guidance.
```
