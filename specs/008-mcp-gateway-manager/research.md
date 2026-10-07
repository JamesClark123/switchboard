# Phase 0 Research: MCP Gateway Manager & Daemon Screen (+ kit schema v2)

Decisions R1–R12 resolve every unknown in the plan's Technical Context. Two things shape the whole
feature: the host runtime's **MCP gateway is host-scoped and CLI-managed** (`sbx mcp …`), so the
daemon is the only place a gateway manager can live; and `sbx` is **still not installed in the
development environment**, so — as in 004–007 — every argv below is documentation-derived from the
current Docker Sandboxes docs (CLI 0.46.0, kit reference "v2", MCP gateway page, release notes
0.43–0.46) and MUST be reconciled first (quickstart Scenario 0). This feature deliberately turns
that recurring risk into a declared, checked **runtime baseline** (R9) so drift is reported per host
rather than discovered at launch.

## R1 — `sbx mcp` CLI surface the daemon drives

**Decision**: The gateway manager shells out to the host CLI exactly as the docs show, one argv per
operation, each pinned by an argv-asserting test:

| Operation (spec) | argv | Notes |
|---|---|---|
| List registrations (FR-073) | `sbx mcp ls` | Table `NAME TYPE URL/COMMAND`; parsed by header column offsets. No documented `--json` → the parser is header-driven and tolerant of extra columns. |
| Authorization state (FR-073) | `sbx mcp auth status <name> --json` | Documented `--json`. Run per *remote* server after `ls`; failure ⇒ state "unknown", never a list failure. |
| Register remote (FR-074) | `sbx mcp add <name> --url <url> --skip-auth` | Always `--skip-auth`: registration never waits on a browser (R2). |
| Register host command (FR-074/076) | `sbx mcp add <name> --command <cmd> [--args a,b] [--dir <dir>]` | Comma-joined args per docs; `--dir` only with `--command`. |
| Remove (FR-074) | `sbx mcp rm <name>` | Prints follow-up `sbx secret rm` hints → relayed verbatim (FR-074). 0.45+: errors when not registered. |
| Authorize (FR-075) | `sbx mcp auth <name>` | Blocks until the browser flow completes; prints `Open this URL to authorize … : <url>` (R2). |
| Attach to a created sandbox, additive mode (FR-100) | `sbx mcp load <name> --sandbox <sandboxName>` | Works in both gateway modes; persists across restarts; live tool-list update to connected agents. |
| Pre-load, exclusive mode (FR-100) | `sbx create … --static-mcp a,b` | Appended to the existing `create` argv. Static mode = no discovery tools. |

**Rationale**: these are the only documented verbs; `sbx mcp catalog` was removed in 0.45 and is
not used. Switchboard never invents a flag the docs don't show (the `options --json` lesson, R9).

**Alternatives considered**: driving the gateway through its in-sandbox meta-tools (`mcp-add`) —
rejected: those are agent-facing, dynamic-mode only, and bypass the host's registration list.

## R2 — Authorization flow: register first, authorize separately, bounded, URL surfaced

**Decision**: Every registration uses `--skip-auth`; authorization is always its own step
(`sbx mcp auth <name>`) run by the daemon as a child process in its own process group with a
**10-minute** deadline (`mcpAuthTimeout`, package constant — the clarified bound). The daemon scans
the child's stdout/stderr for the first `https://…` URL and streams it to the client immediately
(`AuthorizeMcpServer` server stream: `url` → progress lines → terminal `authorized` | `error`);
cancel or timeout kills the group (the escape-hatch `killPgid` pattern) and reports *unauthorized*.
The registration is untouched in every outcome.

**Rationale**: the docs state `sbx mcp add` "opens an authorization flow **before** it stores the
registration" — killing a stalled `add` would lose the registration, violating FR-075's "kept and
reported as unauthorized". Splitting the steps makes timeout/cancel structurally safe and gives
"register now, authorize later" for free. Showing the URL as text (never opening it) is what a
remote daemon requires (Key Decision 5).

**Alternatives considered**: plain `sbx mcp add` without `--skip-auth` and killing on timeout —
rejected for the reason above. Opening the URL on the TUI's machine automatically — rejected (the
TUI may be on a headless box); a copyable URL plus the existing browser-open affordance is enough.

## R3 — Where marks and the attach mode live

**Decision**: A new bbolt bucket `mcp` in the existing `registry.db`, holding one proto record
`McpGatewaySettings{ default_attach_mode, marks: map<string,bool> }` for the daemon (not per
sandbox). Read-modify-write under the registry's existing transaction helpers; reconciled against
`sbx mcp ls` on every list and at every launch (stale names dropped, FR-083).

**Rationale**: the runtime has no notion of a mark; it is switchboard's own state, daemon-scoped
like the registrations it decorates. Reusing the registry file keeps the daemon's persistence in one
place and under one `SWITCHBOARDD_DATA_DIR` (no new env var).

**Alternatives considered**: storing marks client-side — rejected: launches from any client must
see the same marks (FR-080). A sidecar JSON file — rejected: a second persistence mechanism for a
map.

## R4 — Applying defaults at launch

**Decision**: `Manager.Launch` receives an `McpAttach{Mode, Servers}` resolved by the gRPC layer
from the gateway manager (marks ∩ host registrations). *Exclusive* ⇒ `--static-mcp` on `sbx create`
(the runtime pins the set for the sandbox's life). *Additive* ⇒ after `sbx create` returns and
**before** the agent's PTY session is started, one `sbx mcp load <name> --sandbox <name>` per server
(60 s each; a failure is logged to the launch stream and skipped — never a failed launch,
FR-083/084). Both record `Sandbox.mcp_servers` + `Sandbox.mcp_attach_mode`, fixed thereafter;
`RestartSandbox`/`Refresh`/`bringUp` never re-apply marks (the runtime persists loaded/static sets).

**Rationale**: the agent is started by the daemon (`agent/pty.go`: `sbx exec … exec claude`) after
`Launch` returns, so the create→load window genuinely precedes the agent. Even if an agent were
already attached, `sbx mcp load` delivers a live tool-list update, so additive mode degrades to
"visible moments later", never to a restart.

**Alternatives considered**: additive via `--static-mcp` plus a dynamic flag — the runtime offers
no such combination. Re-applying marks on restart — rejected: FR-082 fixes the set at launch.

## R5 — Daemon screen and shift+←/→ navigation

**Decision**: A second top-level screen `screenDaemons` beside `screenList`, toggled with Bubble
Tea's `shift+right` / `shift+left` key strings (`tea.KeyShiftRight`/`KeyShiftLeft`) on both screens;
each keeps its own cursor so selection survives round-trips (FR-068). Rows come from
`client.Manager.List()` (every known host incl. local, with `HostState`), kept live by the existing
`hostsMsg` refresh path (FR-071). A connected row opens an option list (`enter`) — *MCP gateway*
first; `c`/`x` connect/disconnect reuse `enterHostPassword`/`connectHostCmd` generalized to a
return-screen parameter (clarified Q2). The MCP gateway view is `screenMcp`, rendered in the body
(not an overlay) because it is a working list with a form, like `screenServices`.

**Rationale**: a peer screen (rather than an overlay) is what "navigate between the sandboxes screen
and the daemon screen" asks for; reusing the hosts flows avoids a second password prompt.

**Risk**: some terminal multiplexers swallow shift+arrow sequences. The help footer names the
keys; if reconciliation on real terminals shows loss, add `]`/`[` as documented aliases (additive).

## R6 — Settings screen and persisted client settings

**Decision**: New `store.Settings` persisted as `settings.toml` in the client config dir (`absent
key ⇒ default`, so an older file never disables a default-on setting): `auto_connect_hosts = true`.
New `screenSettings`, reachable with `,` from either top-level screen (free against every binding;
shown in both help footers as ", settings"), listing each setting with its value; `space`/`enter`
toggles and saves immediately; an unreadable file shows a notice and defaults apply (FR-101).

**Rationale**: the user asked for a TUI-persisted toggle on its own screen (clarified Q4); TOML
under the config dir is how every other client preference is stored (FR-002c), and `SaveTOML` is
already atomic. Not an env var: Rule VIII surface unchanged.

**Alternatives considered**: a third stop in the shift cycle — rejected to keep that cycle the
two-screen toggle the feature asked for (spec Assumptions).

## R7 — Startup sign-in sequence

**Decision**: After `Init`, when `settings.AutoConnectHosts` is on, the model runs a small state
machine over saved hosts with `Kind == "ssh"` not already connected, strictly sequential: attempt
with an empty password (key/agent); classify failure — an authentication failure (`Permission
denied`/`publickey` in ssh's stderr, surfaced by `DialSSH`) opens the **centered** masked prompt
(reusing the hosts password input, rendered via `overlayCenter` with host name + reason); any other
failure shows a centered failure card with `r` retry / `esc` skip. Each attempt is bounded by a
30-second `signInTimeout` (SC-007). Submitting a password re-attempts once with it; failure returns
to the card. The queue ends → normal operation; the local daemon is never in the queue; passwords
live only in the attempt.

**Rationale**: "connects on its own where it can" needs a keyless attempt first; the only
observable signal for "needs a password" is the auth failure of that attempt. Modal-per-prompt
removes ambiguity about which host a password is for (Key Decision 7).

**Alternatives considered**: prompting for every ssh host unconditionally — noisy for key-auth
hosts. Parallel attempts — rejected: prompts would interleave.

## R8 — Kit schema v2 model, migration and attach pre-check

**Decision**: `store.Kit` is re-shaped to the v2 grammar (`schemaVersion: "2"`, `kind: mixin`,
`requires.agent` optional, `permissions.network.{allow,deny}`, `environment.variables`,
`credentials: []{service, description, required, apiKey{name, proxyManaged, inject[]{domain, header,
format}}}`, `setup.{install,files,startup}`, `agentInstructions.content`). Loading detects v1 (by
`schemaVersion: "1"`, a `network:`/`commands:`/`credentials.sources` key, or `agentContext`/`memory`)
and migrates with the runtime's published mapping, producing a `Migration` report (carried fields,
dropped items with reasons) shown in the editor as a banner; the file is rewritten as v2 only on
save. Attach pre-check `Kit.AttachBlockers()` returns the sections `sbx kit add` rejects on an
existing sandbox — everything except `environment.variables`, `setup.install`,
`permissions.network.allow` — and the `A` flow refuses before any RPC when non-empty (FR-095).

Mapping (v1 → v2): `network.allowedDomains/deniedDomains → permissions.network.allow/deny`;
`commands.install → setup.install`; `commands.initFiles → setup.files`; `commands.startup →
setup.startup`; `agentContext`/`memory → agentInstructions.content`; `credentials.sources.<id>.env[]`
→ one credential `service: <id>` with `apiKey.name` = first env name, `proxyManaged: true` when that
name appears in v1 `environment.proxyManaged`, empty `inject` (reported: "declare injection
domains"); extra env names and any field with no v2 equivalent → dropped + reported.

**Rationale**: v2 is the grammar Docker documents as current and the one the built-in `claude`
agent composes with (v3 cannot); v1 is accepted-but-legacy. The editor already owns every field it
writes, so the re-shape is a rename pass plus the credentials model change v2 forces.

**Alternatives considered**: v3 — rejected (Key Decision 8). Dual-grammar output — rejected:
switchboard must never write v1 again (FR-091).

## R9 — Runtime baseline and surface reconciliation (FR-096/097)

**Decision**: `sbxkit.MinSbxVersion = "0.36.0"` (first release with schema v2 per the kit
reference). At startup the daemon parses the first `MAJOR.MINOR.PATCH` in `sbx --version` output;
`DaemonInfo` gains `sbx_min_version`, `runtime_baseline_met`, `mcp_gateway_available`,
`mcp_gateway_reason`. Kit RPCs (`ValidateKit`, `AddSandboxKit`, launches with kits) return
`FAILED_PRECONDITION` naming both versions when unmet; MCP RPCs return it with the reason when
the gateway is unavailable (CLI missing, below baseline, `sbx mcp` absent, not signed in). Kit-less
sandbox operations are untouched.

Surface inventory against current docs (0.46), each row an explicit reconciliation item in
quickstart Scenario 0:

| Daemon uses | In current docs? | Action |
|---|---|---|
| `sbx create --name N claude <ws> [--kit …]` | ✅ | confirm `--static-mcp` composes with `--kit` |
| `sbx exec …` | ✅ | — |
| `sbx ports <N> --publish …` | ✅ | — |
| `sbx policy allow network …` | ✅ | — |
| `sbx kit add` / `sbx kit validate` | ✅ | confirm v2 rejection message for blocked sections |
| `sbx ls --json` | ⚠ `ls` ✅, `--json` unconfirmed | confirm or switch `IsRunning` to the documented form |
| `sbx stop` / `sbx rm` | ✅ | — |
| `sbx start` | ❌ not documented | confirm; fallback per docs: `sbx run -d --name <N>` re-starts an existing sandbox |
| `sbx options --json` | ❌ not documented | drop the branch; `--help` parsing (already the fallback) becomes the only source |
| `sbx --version` | ✅ | pin the exact output format |

**Rationale**: FR-096/097 make the implicit "whatever is installed" explicit; the inventory is
what planning needs to turn the long-standing "documentation-derived" caveat into checked facts.

## R10 — Multi-client propagation of gateway changes

**Decision**: One additive `Event.mcp_gateway_changed { host_id }` arm emitted after every
successful registration/mark/mode change; clients with the gateway view open re-list on it. No new
`NotificationKind` — these are developer actions (the 006 "silent on developer action" precedent).

## R11 — Timeouts (package constants, no env vars)

`mcpListTimeout` 60 s (the shared RPC class); `mcpMutateTimeout` 2 min (`add`/`rm` may fetch
metadata); `mcpAuthTimeout` 10 min (clarified); `mcpLoadTimeout` 60 s per server at launch;
`signInTimeout` 30 s per host attempt.

## R12 — Testing without `sbx`

Daemon: a stub `sbx` script per test (the `fakeSbx` pattern) emits the documented `mcp ls` table,
`auth status --json`, an authorize URL line then blocks, and records argv; the gateway manager,
reconciler and launch integration are tested against it. gRPC round-trips over the in-process
socket. TUI: `teatest` for the daemon/MCP/settings screens, the startup sequence (fake manager with
scripted dial outcomes), and the kit migration/attach pre-check (pure functions). E2E: the TUI
harness's `stubSbx` gains `mcp` verbs and must report a version ≥ baseline (today it prints
`sbx-e2e 0.0`, which would now fail the gate). The real-runtime items are all in Scenario 0.
