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

A Go `go.work` workspace with three modules (plus E2E siblings), placed under the constitution's
category taxonomy:

```
src/
├── libs/switchboard-proto/      # shared gRPC/protobuf contract + domain helpers
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
- For development only: `golangci-lint`, and `protoc` + `protoc-gen-go`/`protoc-gen-go-grpc` if you
  regenerate the gRPC stubs (the generated code is committed, so this is not needed just to build).

## Install

Both `sxb` and `sxbd` are distributed as prebuilt binaries for macOS and Linux (amd64 + arm64)
from [GitHub Releases](https://github.com/jamesclark123/switchboard/releases). Install on **every
machine** that will run the TUI or the daemon — including each remote host you connect to.

### Quick install (recommended — self-update capable)

```bash
curl -fsSL https://raw.githubusercontent.com/jamesclark123/switchboard/main/install.sh | sh
```

It detects your OS/arch, downloads the latest release, verifies its SHA-256 checksum, and installs
`sxb` + `sxbd` to `/usr/local/bin` (or `~/.local/bin`). Pin a version with
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
| `SWITCHBOARDD_HOOK_ADDR`      |          | `127.0.0.1:8765`                     | Listen addr for the agent hook callback server |

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
| `i` | Notification inbox (task-complete / needs-prompting; 🔔 badge) |
| `s` | Toggle the selected sandbox: stop if running, start otherwise |
| `d` | Destroy the selected sandbox |
| `u` | Update the client and all connected hosts (shown when a newer release exists) |
| `R` | Rename · `r` refresh · `j`/`k` navigate · `q` quit |

## Updating

Switchboard is designed for frequent releases, so the TUI keeps itself and every connected daemon
up to date with minimal effort.

- **Notification:** on startup `sxb` checks GitHub for the latest release (best-effort; silent when
  offline, opt out with `SXB_NO_UPDATE_CHECK=1`). When a newer version exists, a banner appears
  above the sandbox list and a `u` key becomes available.
- **One keystroke, all machines:** pressing `u` updates to the latest release across the board — it
  drives **every connected daemon** (local and remote, over the existing SSH-tunneled connection)
  to self-update its `sxbd` binary and restart, then swaps the local `sxb` binary and restarts the
  TUI into the new version. All hosts converge on the same version, so no version skew is left
  behind. Each download is SHA-256-verified before anything is replaced.
- **Remote hosts** are reached as `ssh <host> sxbd dial-stdio`, so their `sxbd` lives on that host.
  The `u` fan-out updates them in place; a host you have *not* connected to must be updated there
  directly by re-running the installer.

## Testing

The project uses the Go toolchain throughout, driven by a top-level `Makefile`. Common targets:

```bash
make build        # compile every module
make fmt          # gofmt -w (auto-format)
make fmt-check    # fail if any file needs formatting
make vet          # go vet across modules
make lint         # golangci-lint across modules
make test         # unit + integration tests (go test)
make cover        # tests with the 90% per-module coverage floor enforced
make env-check    # env-schema ↔ .env.example lockstep (Rule VIII)
make all          # fmt-check + vet + lint + test (the standard pre-merge gate)
make e2e          # end-to-end suites (see below)
```

- **Unit + integration** (`make test` / `make cover`): standard `go test`, including gRPC
  client↔daemon tests over an in-process socket and `teatest` golden/interaction tests for the TUI.
  Coverage is enforced at **≥90% per module** (libs, daemon, TUI).
- **End-to-end** (`make e2e`, gated behind the `e2e` build tag):
  - The **TUI E2E** builds the real binaries and drives `sxb` through a PTY against a
    daemon backed by a stub `sbx` — it runs anywhere, no Docker required.
  - The **daemon E2E** drives the full lifecycle (launch → stop → restart → destroy + re-adoption)
    against a real Docker + `sbx` runtime, and **auto-skips** when that runtime is absent.

Run a single test from within a module, e.g.:

```bash
cd src/services/switchboardd && go test ./internal/sandbox/ -run TestLaunch -v
```

A Husky `pre-commit` hook runs the fast subset (`fmt-check`, `vet`, `lint`, `test`, `env-check`);
CI (`.github/workflows/ci.yml`) runs the full gate plus `make cover` and `make e2e`.

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
