# Phase 1 Data Model: MCP Gateway Manager & Daemon Screen (+ kit schema v2)

Derived from [spec.md](./spec.md) Key Entities + FR-068–FR-101, with mechanics fixed by
[research.md](./research.md). Wire shapes are in
[contracts/switchboard-mcp-gateway.proto](./contracts/switchboard-mcp-gateway.proto).

## Entity map

| Entity | Kind | Stored | Owner |
|--------|------|--------|-------|
| `McpServer` | derived (read from host) | never — the host runtime owns it | runtime (`sbx mcp`) |
| `McpGatewaySettings` | persisted | bbolt bucket `mcp`, one record per daemon | daemon (R3) |
| `Sandbox.mcp_servers`, `Sandbox.mcp_attach_mode` | persisted (new fields 21–22) | bbolt sandbox record | daemon (R4) |
| `DaemonInfo` baseline/gateway fields | derived at startup | never | daemon (R9) |
| Authorization wait | transient | never | daemon (R2) |
| `Kit` (schema 2) + `Migration` | persisted (client) | `kits/<id>/spec.yaml` + sidecars; migration report in memory | TUI store (R8) |
| `Settings` | persisted (client) | `settings.toml` | TUI store (R6) |
| Startup sign-in sequence | transient | never | TUI model (R7) |

## McpServer (derived; proto `McpServer`)

| Field | Type | Source | Notes |
|---|---|---|---|
| `name` | string | `sbx mcp ls` NAME | identity on one host; `[A-Za-z0-9._-]+` (runtime rule) |
| `kind` | enum `MCP_SERVER_KIND_{REMOTE,LOCAL}` | TYPE column | remote endpoint vs host-launched (command or OCI) |
| `target` | string | URL/COMMAND column | address, or command line as the runtime prints it |
| `auth_state` | enum `{NOT_APPLICABLE, AUTHORIZED, UNAUTHORIZED, UNKNOWN}` | `auth status --json` (remote only) | UNKNOWN when the status call fails; never fails the list |
| `attach_by_default` | bool | `McpGatewaySettings.marks[name]` | switchboard-owned decoration |

Invariants: the list is **always** the host's list (I-M1); a mark with no matching name is dropped
on read and never shown (I-M2); operations are host-scoped — one daemon never reads or writes
another's (I-M3, SC-003).

## McpGatewaySettings (persisted; proto `McpGatewaySettings`)

| Field | Type | Default | Notes |
|---|---|---|---|
| `default_attach_mode` | enum `McpAttachMode {UNSPECIFIED, ADDITIVE, EXCLUSIVE}` | `ADDITIVE` (UNSPECIFIED reads as ADDITIVE) | clarified Q1 / FR-100 |
| `marks` | map<string,bool> | empty | key = server name; `true` only entries are kept |

Mutations: `SetMcpServerDefault(name, on)` (refused if `name` is not registered), `SetMcpAttachMode`,
and `RemoveMcpServer` (clears the mark, FR-074). Every mutation emits `Event.mcp_gateway_changed`.

## Sandbox — new fields

| Field | # | Type | Set | Notes |
|---|---|---|---|---|
| `mcp_servers` | 21 | repeated string | once, at launch | exactly the names switchboard attached (post-reconcile); `[]` when none |
| `mcp_attach_mode` | 22 | `McpAttachMode` | once, at launch | the daemon's mode at launch time |

Invariants: fixed for the sandbox's life (I-S1, FR-082); never re-applied by restart/refresh/bring-up
(I-S2); agent self-attachments in additive mode are *not* recorded (I-S3).

## Launch resolution (transient)

```
marks(daemon) ∩ registered(host)  →  attach set
  mode = settings.default_attach_mode
  EXCLUSIVE & set ≠ ∅ : create --static-mcp <set>
  ADDITIVE  & set ≠ ∅ : create ; for s in set: mcp load s --sandbox <name>  (skip + log on failure)
  set = ∅ (either mode): create as today (FR-085)
  skipped (stale/failed) names → launch log lines; stale marks dropped from settings
```

## Authorization wait (transient; proto `McpAuthProgress` stream)

States: `STARTED → URL_SHOWN → (AUTHORIZED | UNAUTHORIZED{timeout|cancelled|failed})`.
Bound: 10 min (`mcpAuthTimeout`). Side effects: none on the registration in any terminal state.
Concurrency: one wait per `(daemon, server)`; a second request while one is pending is refused with
`FAILED_PRECONDITION` ("authorization already in progress").

## DaemonInfo — new fields

| Field | # | Type | Notes |
|---|---|---|---|
| `sbx_min_version` | 6 | string | `sbxkit.MinSbxVersion` ("0.36.0") |
| `runtime_baseline_met` | 7 | bool | parsed `sbx --version` ≥ min; false when unparseable |
| `mcp_gateway_available` | 8 | bool | CLI present, baseline met, `sbx mcp` answers, signed in |
| `mcp_gateway_reason` | 9 | string | human reason when unavailable (FR-079) |

## Kit (schema 2) — client model (`store.Kit`)

```yaml
schemaVersion: "2"
kind: mixin
name: <slug>                 # required, 1–64 lowercase alnum + hyphens
displayName: …               # optional
description: …               # optional
version: …                   # optional (new in v2 identity)
requires: { agent: claude }  # optional base-agent pin
permissions:
  network: { allow: [..], deny: [..] }
environment:
  variables: { K: V }
credentials:
  - service: <kebab>         # required per entry
    description: …
    required: false
    apiKey: { name: ENV_NAME, proxyManaged: false, inject: [{domain, header, format}] }
setup:
  install:  [{command, user, description}]
  files:    [{path, content, mode, onlyIfMissing, description}]
  startup:  [{command: [..], user, background, description}]
agentInstructions:
  content: |
    …
```

Switchboard-owned sidecars (`escape-hatch.yaml`, `services.yaml`) are unchanged and never enter
`spec.yaml`.

### Migration (v1 → v2; transient `store.Migration`)

| Field | Type | Notes |
|---|---|---|
| `from_version` | string | "1" |
| `carried` | []string | section names mapped losslessly |
| `dropped` | []{path, reason} | e.g. `credentials.sources.github.env[1]` "v2 declares no host source" |
| `notes` | []string | e.g. "credential github: declare injection domains" |

Rules: detection by `schemaVersion: "1"` or any v1-only key; never fails the load; the file is
rewritten as v2 only on save (Key Decision 9). Mapping table: research R8.

### Attach pre-check

`Kit.AttachBlockers() []string` → non-empty when the kit declares any of: `permissions.network.deny`,
`setup.files`, `setup.startup`, `credentials`, `agentInstructions` (anything but
`environment.variables`, `setup.install`, `permissions.network.allow`). The `A` flow refuses with the
list (FR-095); launch is unaffected.

## Settings (client; `settings.toml`)

| Key | Type | Default | Notes |
|---|---|---|---|
| `auto_connect_hosts` | bool | `true` | absent key ⇒ default; written on first change |

Unreadable/invalid file ⇒ defaults + notice on the settings screen (FR-101).

## Startup sign-in sequence (transient; TUI model)

```
queue = saved hosts where kind == ssh && !connected        (local never queued)
for host in queue (one at a time):
  attempt(password="")  — 30 s bound
    ok                      → next
    auth failure            → PROMPT(host)  ──submit pw──▶ attempt(pw) ─ok→ next
                                             │                          └fail→ FAILED(host, reason)
                                             └esc (skip)──────────────▶ next
    other failure           → FAILED(host, reason) ── r retry → attempt("") ; esc skip → next
end → normal operation; nothing persisted
```

## Client-side view models

- **Daemon screen row**: `{host id, display name, kind, state, reason, daemon version, sbx version,
  baseline met, gateway available}` — from `client.Manager.List()` + cached `DaemonInfo`.
- **Gateway view**: `{servers []McpServer, default_attach_mode, pending auth (name, url, remaining)}`.
- **Op log** (existing, feature "sbx output"): launch logs gain the `mcp load`/`--static-mcp` lines
  and skipped-server notes (FR-086).
