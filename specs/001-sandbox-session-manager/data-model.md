# Data Model: Sandbox Session Manager (Switchboard)

Derived from [spec.md](./spec.md) Key Entities + Functional Requirements. Two persistence domains:
**client-side** state (source of truth: configs, groups, known hosts — FR-002c/d) and
**daemon-side** state (per-host sandbox registry — FR-002a/b). Field types are language-agnostic;
the Go/proto mappings live in [contracts/switchboard.proto](./contracts/switchboard.proto).

## Ownership map

| Entity | Stored by | Backing store | Lifetime |
|--------|-----------|---------------|----------|
| KnownHost | Client | TOML (`hosts.toml`) | Until user removes |
| Configuration | Client | TOML (`configs/*.toml`) | Until user deletes |
| Group | Client | TOML (`groups.toml`) | Until user deletes |
| Sandbox | Daemon | bbolt (`registry.db`) | Until destroyed; survives daemon restart |
| AgentSession | Daemon | bbolt (embedded in Sandbox) | While sandbox exists |
| NotificationEvent | Daemon (emitted) → Client (history) | daemon stream + client ring buffer | Transient/log |

---

## Client-side entities (source of truth)

### KnownHost (FR-002d, FR-004)
A saved connection to a daemon.

| Field | Type | Notes |
|-------|------|-------|
| `id` | string (slug) | Stable local identifier |
| `display_name` | string | User label, e.g. "build-box" |
| `kind` | enum `local` \| `ssh` | `local` ⇒ Unix socket; `ssh` ⇒ dial-stdio |
| `socket_path` | string? | For `local`: path to daemon Unix socket |
| `ssh_target` | string? | For `ssh`: `[user@]host[:port]` passed to `ssh` |
| `ssh_options` | string[]? | Extra `ssh` args (identity file, jump host) |
| `last_connected_at` | timestamp? | For ordering/recents |

**Rules**: exactly one of `socket_path`/`ssh_target` set per `kind`. No secrets stored — auth is
delegated to the user's SSH agent/config (spec Assumption: SSH provides auth).

### Configuration (FR-013–FR-016b)
A named, savable set of sandbox-kit (`sbx`) options piped to a sandbox on launch.

| Field | Type | Notes |
|-------|------|-------|
| `id` | string (slug) | |
| `name` | string | Shown as default sandbox label (FR-012e) |
| `kit_options` | map<string,Value> | Full `sbx` option surface (FR-014); validated against the host's option manifest at launch |
| `seeding_mode` | enum `duplicate` \| `clone` | Default `duplicate` (FR-008/009) |
| `agent` | AgentSpec? | Optional; when set, launches are **fixed** to it (FR-016a); when null, user picks at launch (FR-016b) |
| `default_sources` | SourceRef[]? | Optional pre-filled source selection |
| `updated_at` | timestamp | Edits apply to future launches only (FR-016) |

**Rules**: `kit_options` keys MUST be a subset of the target host's advertised option manifest;
unknown/unsupported keys fail loudly at launch naming the offending key (spec edge case).

### Group (FR-018–FR-020)
A user-defined, **cross-host** collection of sandboxes.

| Field | Type | Notes |
|-------|------|-------|
| `id` | string (slug) | |
| `name` | string | Duplicate names allowed; distinguished by id |
| `members` | SandboxRef[] | `{host_id, sandbox_id}` — MAY span hosts (FR-002c) |
| `order` | int | Sort position in the TUI |

**Rules**: membership is independent of sandbox running state (FR-018); a member whose sandbox was
destroyed is pruned/marked stale on next sync.

### AgentSpec (value object, embedded in Configuration / launch request)
| Field | Type | Notes |
|-------|------|-------|
| `kind` | string | e.g. `claude-code` |
| `args` | string[]? | Extra agent CLI args |
| `model` | string? | Optional model hint |

### SourceRef / SourceSelection (FR-007, FR-008)
| Field | Type | Notes |
|-------|------|-------|
| `path` | string | Host-side absolute path to a git project/directory |
| `is_repo` | bool | Whether `path` is a git repo (enables `clone`) |

A **SourceSelection** is `{ sources: SourceRef[], seeding_mode }` — one or many directories seed a
single sandbox (FR-007). `clone` is only valid when every selected source `is_repo`.

---

## Daemon-side entities (per-host registry)

### Sandbox (FR-001, FR-002a/b, FR-012a–FR-017a)
A Docker sandbox session on this host.

| Field | Type | Notes |
|-------|------|-------|
| `id` | string (uuid) | Unique auto-generated id (FR-012e) |
| `display_name` | string | Defaults to config label/generated; user-overridable (FR-012e) |
| `state` | enum `creating` \| `running` \| `stopped` \| `destroying` \| `error` | See transitions |
| `host_id` | string | Owning host (set by client context) |
| `config_snapshot` | Configuration | Frozen copy of the config used (FR-002b, FR-012d) |
| `config_label` | string? | Name of the source config, if any |
| `sources` | SourceRef[] | What it was seeded with (FR-012d) |
| `seeding_mode` | enum `duplicate` \| `clone` | |
| `workspace_path` | string | Path of the retained verbatim copy in the controlled folder |
| `container_ref` | string? | Underlying `sbx`/Docker handle for re-adoption (FR-002a) |
| `agent` | AgentSession | Embedded |
| `group_hints` | string[]? | Advisory; authoritative grouping is client-side |
| `created_at` / `updated_at` | timestamp | |

**State transitions**:
```
(none) --launch--> creating --copy+sbx ok--> running
running --stop--> stopped            (workspace_path RETAINED; FR-012a)
stopped --restart--> running         (re-seed from retained copy; FR-012b)
running|stopped --destroy--> destroying --> (removed; workspace_path DELETED; FR-012c)
creating|running --error--> error    (surface reason; e.g. low disk per FR-012f)
daemon restart: running containers re-adopted into running; others reloaded as stopped (FR-002a)
```

**Rules**:
- `stop` MUST NOT delete `workspace_path`; only `destroy` deletes it (FR-012a/c).
- Verbatim duplication writes only under the controlled folder; source dirs are never modified (SC-002).
- On launch, if host disk/resources are low, daemon returns a **warning** the client can override (FR-012f).

### AgentSession (FR-022–FR-026b)
The coding agent inside a sandbox.

| Field | Type | Notes |
|-------|------|-------|
| `spec` | AgentSpec | Which agent + args |
| `status` | enum `idle` \| `working` \| `needs_input` \| `exited` | Driven by injected hooks |
| `last_event_at` | timestamp | |
| `pty_attached` | bool | Whether a terminal is attached |

**Rules**: `status` is updated by Claude Code **Stop** hook (→ `idle`/task complete) and
**Notification** hook (→ `needs_input`), which call back to the daemon (see research.md). Each
transition to `needs_input` or task-complete emits a NotificationEvent.

### NotificationEvent (FR-024–FR-026b, SC-008)
| Field | Type | Notes |
|-------|------|-------|
| `id` | string | |
| `sandbox_id` | string | Subject sandbox |
| `host_id` | string | Subject host (FR-026) |
| `kind` | enum `task_complete` \| `needs_prompting` | |
| `message` | string | Human-readable |
| `created_at` | timestamp | |
| `delivered` | bool | Client marks delivered; undelivered are replayed on reconnect (FR-026b) |

**Rules**: events are buffered daemon-side so a client disconnected at emit time receives them on
reconnect (FR-026b); the client renders them in its in-TUI list **and** raises an OS desktop
notification (FR-026a).

---

## Cross-cutting validation

- **Identity**: `Sandbox.id` is globally unique per host; `(host_id, sandbox_id)` is the global key
  used by client Groups (FR-012e, FR-002c).
- **Config/example lockstep** (Rule VIII intent): each module's env config has a committed example
  with keys in lockstep, validated at startup.
- **Option-surface coverage** (FR-014): the daemon advertises an `sbx` option manifest; the client
  config editor MUST cover 100% of it — see research.md for the enumeration mechanism.
