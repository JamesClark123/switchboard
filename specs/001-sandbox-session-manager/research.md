# Phase 0 Research: Sandbox Session Manager (Switchboard)

Resolves the unknowns in [plan.md](./plan.md) Technical Context. Format per decision:
**Decision / Rationale / Alternatives considered**. Web-verified items cite sources.

---

## R1. Daemon ↔ client transport (local + over SSH)

**Decision**: gRPC over a single connection. Local = Unix domain socket
(`switchboardd serve --socket <path>`). Remote = the **Docker-CLI `dial-stdio` pattern**: the
client runs `ssh <host> switchboardd dial-stdio`, which bridges its stdio to the remote daemon's
Unix socket; the client wraps that stdio pipe as a `net.Conn` and hands it to gRPC via
`grpc.WithContextDialer`. A server-streaming `Subscribe` RPC pushes live sandbox/agent events.

**Rationale**:
- Rides the user's **existing SSH** — no new listening network port, no separate authN/Z system
  (spec Assumption: SSH provides auth). Matches FR-003/FR-004/FR-005.
- Unix socket gives local-only access control via filesystem permissions.
- gRPC gives typed contracts (the `contracts/` artifact), bidi streaming for the PTY attach, and
  server streaming for notifications (FR-024–FR-026b, SC-008).

**Alternatives considered**:
- *HTTP/WebSocket over an SSH local-forward* — workable but requires managing forwarded ports and
  lifecycle; dial-stdio is portless and is the proven Docker approach.
- *Daemon runs its own SSH server (charm `wish`)* — `wish` serves TUIs over SSH; our model runs the
  TUI locally and connects out, so it's the wrong tool. Rejected.
- *net/rpc + gob or hand-rolled JSON lines* — loses streaming ergonomics and codegen. Rejected.

---

## R2. Agent state detection — "task complete" vs "needs prompting" (FR-024/FR-025)

**Decision**: Inject **Claude Code hooks** into each sandbox so the agent calls back to the daemon:
- **`Stop`** hook (matcher `""`) → **task complete** → daemon emits `TASK_COMPLETE`.
- **`Notification`** hook matched on **`permission_prompt`** and **`idle_prompt`** → **needs
  prompting** → daemon emits `NEEDS_PROMPTING`.
Callback uses hook `type: "http"` POSTing to the daemon (cleanest), or `type: "command"` running
`curl` to `http://host.docker.internal:<port>/hook` as a fallback. Hooks are injected per sandbox by
writing `.claude/settings.local.json` into the workspace at launch (highest-precedence project
settings, gitignored), carrying the `session_id`/`cwd`/`hook_event_name` correlation keys.

**Rationale**: Verified against official docs (`code.claude.com/docs/en/hooks`). `Stop` fires when
Claude finishes responding; `Notification` fires when it waits for input/permission, sub-typed by
matcher — exactly the two states the spec needs. Hooks are async push, so the agent runs normally
while the daemon is notified. `type: "http"` removes the per-hook shell shim.

**Alternatives considered**:
- *Headless `claude -p --output-format stream-json`* (NDJSON event stream: `message_stop`, etc.) —
  viable when the daemon runs the agent itself, but worse for supervising **interactive** sessions.
  Kept as a complementary option, not the primary mechanism.
- *Scraping the PTY/transcript for prompts* — brittle; rejected in favor of first-class hooks.

⚠️ **Corrected during research**: event names `TaskCompleted`, `PermissionRequest`, `SubagentStart`,
etc. are **not real** Claude Code events (a sub-agent pass hallucinated them). The real set is
`SessionStart/End`, `UserPromptSubmit`, `Stop`/`StopFailure`, `PreToolUse`/`PostToolUse`,
`Notification`, `SubagentStop`, `PreCompact`. Design only against these.

---

## R3. Open a sandbox in VS Code (FR-027) — local and remote

**Decision**: The **daemon** returns the container's canonical name and in-container workspace path
(`GetVSCodeTarget`); the **client** builds the URI and runs `code` locally.
- **Local container**:
  `code --folder-uri "vscode-remote://attached-container+<HEX>/<abs-path>"`
  where `<HEX> = hex.EncodeToString([]byte(fmt.Sprintf(`{"containerName":"/%s"}`, name)))`
  (lowercase hex of the JSON object; **leading slash** on the name is required).
- **Remote host container** (single-command path, "Approach A"): set
  `DOCKER_HOST=ssh://<user@host>` for the `code` invocation, then use the **same** attached-container
  URI. VS Code's Dev Containers extension attaches across the SSH tunnel.

**Rationale**: Web-verified (cspotcode, VS Code remote docs, gist). `<HEX>` is the hex of a small JSON
config, **not** the raw id — a critical, easy-to-miss detail now captured in the contract comment.
`code` always runs on the user's **local** machine; the container must be **running**. Required
extensions: `ms-vscode-remote.remote-containers` (+ `remote-ssh` for Approach B); Container Tools
provides the `DOCKER_HOST` plumbing.

**Alternatives considered**:
- *Two-hop Remote-SSH then "Attach to Running Container"* ("Approach B") — better for large remote
  filesystems / no local Docker client, but step 2 is an **interactive** palette action not driveable
  from one `code` call. Documented as the fallback.
- *`@devcontainers/cli` `devcontainer open`* — that subcommand **no longer exists**; the CLI is
  editor-agnostic. Rejected; building the URI directly is more robust.
- *Combined nested `ssh-remote+...attached-container+...` URI* — requested upstream and **closed as
  "not planned"**. Not available.

---

## R4. Persistence — daemon registry & client state

**Decision**: Daemon = **bbolt** (`go.etcd.io/bbolt`) embedded KV at the daemon data dir, storing the
sandbox registry (id → Sandbox incl. `container_ref` for re-adoption). Client = **TOML files** under
`$XDG_CONFIG_HOME/switchboard/` (`hosts.toml`, `configs/*.toml`, `groups.toml`) via
`pelletier/go-toml/v2`.

**Rationale**: Registry must survive daemon restart for re-adoption (FR-002a/b, SC-012); bbolt is
pure-Go, transactional, zero-ops. Client state is small, user-editable, and is the **source of
truth** (FR-002c) — human-readable TOML fits and is trivially portable across machines (SC-014).

**Alternatives considered**: SQLite (modernc, pure-Go) — fine but heavier than needed for a small KV
registry; JSON blobs — no transactions/locking. bbolt+TOML split keeps each store matched to its job.

**Re-adoption mechanism**: on startup the daemon loads the registry, lists live `sbx`/Docker
containers, and re-attaches those whose `container_ref` is still running (→ `running`); the rest load
as `stopped`. Orphan containers with no registry entry are surfaced, not silently adopted.

---

## R5. Verbatim duplication (FR-010a, FR-028, SC-002)

**Decision**: Recursive copy of every selected source into `<workspace-root>/<sandbox-id>/` using a
streaming walk that reports `bytes_copied/bytes_total` as `LaunchProgress.CopyProgress`. Sources are
opened read-only; nothing is written outside the controlled folder. A pre-walk sizes the copy to feed
`CheckResources` (FR-012f).

**Rationale**: Spec mandates byte-identical copies including ignored/untracked files; a plain
recursive copy (no ignore filtering) satisfies it. Progress streaming satisfies FR-028; read-only
source access guarantees SC-002.

**Deferred to implementation** (spec Assumption): symlink handling (copy as link vs deref) and
permission/timestamp preservation — pick sane defaults (preserve mode bits; copy symlinks as-is)
and document in the package README.

---

## R6. sbx option surface enumeration (FR-014)

**Decision**: At startup the daemon builds an **OptionManifest** by introspecting the host's `sbx`
CLI, preferring a machine-readable schema if `sbx` exposes one (e.g. a `--help=json`/`completion`/
schema subcommand); otherwise parsing `sbx --help` output into typed options. The manifest is
version-stamped (`sbx_version`) and served via `GetOptionManifest`; the client editor renders 100% of
it. Launch validates a config's `kit_options` against the manifest and **fails loudly** on unknown
keys (spec edge case).

**Rationale**: Decoupling the daemon (which knows the local `sbx`) from the client editor lets the TUI
cover every option without hardcoding the option set, and keeps coverage correct as `sbx` versions
differ across hosts.

⚠️ **Residual risk / to confirm against the real CLI**: `sbx` is not installed in this dev sandbox, so
the exact introspection entry point is unverified. The first implementation task MUST inspect a real
`sbx` and pick schema-introspection if available, else help-parsing. This is the one unknown carried
into implementation; it does not block the architecture.

---

## R7. Testing stack (Rule VI intent on a Go TUI/daemon)

**Decision**:
- Unit/golden TUI: **`github.com/charmbracelet/x/exp/teatest`** — `NewTestModel(t, m,
  WithInitialTermSize(w,h))`, drive with `Send`/`Type`, assert with `WaitFor` + `RequireEqualOutput`
  (golden via `-update`).
- Logic/contract: `go test` + `testify`; gRPC tests over an in-process Unix socket.
- E2E (TUI binary): **`creack/pty`** + **`Netflix/go-expect`** (`ExpectString`/`SendLine`), and/or
  **`charmbracelet/vhs`** `.tape` scripts emitting `.txt`/`.ascii` golden output with `Wait` regexes
  (resilient to timing). Lives in `src/apps/switchboard-tui-e2e/`.
- Coverage: `go test -coverprofile` with a **90% floor** asserted in CI (preserves Rule VI's
  non-negotiable threshold via a different provider).
- Desktop notifications: **`github.com/gen2brain/beeep`** — `beeep.Notify(title, msg, icon)` /
  `beeep.Alert(...)`; cross-platform (Linux D-Bus, macOS, Windows).

**Rationale**: All verified on pkg.go.dev / official repos. This is the idiomatic Go analog of
Vitest+Storybook+Playwright; Playwright cannot target a terminal UI, which Rule VI explicitly allows
substituting with justification.

**Alternatives considered**: pure-`vhs` for everything (great for demos/golden frames but weaker for
fine assertions than teatest); raw PTY without go-expect (more boilerplate). Chosen mix balances fast
unit tests with real end-to-end coverage.

---

## R8. Agent prompting & "open in another terminal" (FR-022, FR-023)

**Decision**: The daemon runs each sandbox's agent under a PTY (`creack/pty`). `PromptAgent` writes a
line to that PTY. `AttachAgent` is a bidi gRPC stream of raw PTY bytes (+ resize) so the client can
render an inline prompt pane **and** "open in another terminal" = the client spawns a new terminal
running `switchboard attach <host> <sandbox>` (which opens the same `AttachAgent` stream), or locally
`docker exec -it`. The agent's hook callbacks (R2) drive `AgentStatus`.

**Rationale**: One PTY per agent gives both programmatic prompting and faithful interactive attach
without a second mechanism; raw-byte streaming keeps full-screen agents (and their own TUIs) intact.

---

## Summary of resolved unknowns

| Unknown (from Technical Context) | Resolved by |
|----------------------------------|-------------|
| VS Code URI for local/remote container | R3 (verified) |
| Claude Code hook events + settings schema + callback | R2 (verified) |
| teatest API + desktop-notification lib | R7 (verified) |
| sbx option enumeration | R6 (decision + flagged residual risk to confirm on real `sbx`) |
| Transport over SSH | R1 |
| Persistence choice | R4 |

No blocking NEEDS CLARIFICATION remain. The single residual implementation risk (R6, `sbx`
introspection entry point) is isolated to the first daemon task and does not affect the architecture
or contracts.
