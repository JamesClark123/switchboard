# Data Model: Escape Hatch

**Feature**: `005-escape-hatch` | **Date**: 2026-07-23 | **Spec**: [spec.md](./spec.md)

Entities below map the spec's Key Entities onto concrete proto messages, the daemon's in-memory run
store, and the client kit struct. Field numbers are additive on the tail of existing messages
(proto's unknown-field tolerance + the registry's `pb.Sandbox` marshaling means **no migration** — same
guarantee feature 004 documented for `Sandbox.kits`).

---

## 1. `EscapeHatchCommand` — an authorized command declared on a kit

New proto message; the structured, switchboard-owned unit that travels with a client-authored
`KitSpec` (research R2). Also modeled client-side as a `store` struct in the kit editor.

| Field | Type | Rules |
|-------|------|-------|
| `name` | string | Required. Stable, human-readable. Unique **within a kit** (client-validated). Across a sandbox's kits, later-attached kits override a same-named command (clarification Q1). Used as the wrapper argument and the allowlist key. |
| `command` | string | Required. The **fixed, trusted prefix** run via `sh -c`. The agent supplies no part of it; any agent-supplied arguments (below) are appended as **positional parameters**, never re-parsed by the shell. |
| `when_to_use` | string | Required. Plain-language guidance surfaced in the injected rule (FR-037). |
| `consent_mode` | enum `ConsentMode` | `CONSENT_MODE_AUTO_RUN` \| `CONSENT_MODE_REQUIRES_APPROVAL`. Required (no `UNSPECIFIED` accepted at attach). |
| `working_dir` | string | Optional. **Workspace-relative**; joined onto `workspace_path`. Rejected if it escapes the workspace. The **default** dir, used when `workspaces` is empty. |
| `max_duration_seconds` | uint32 | Optional. `0` ⇒ daemon default (30 min, Q3). |
| `subcommands` | repeated string | Optional (enhancement). Allowed exact argument strings the agent may choose (friendly form). Mutually exclusive with `args_pattern`. |
| `args_pattern` | string | Optional (enhancement). Anchored regex the agent's argument string must **fully** match (power form). A loose pattern broadens which arguments reach the fixed command — hence a client-side authoring warning. Mutually exclusive with `subcommands`. |
| `workspaces` | repeated string | Optional (enhancement). Workspace-relative **literal paths or globs** (e.g. `src/apps/*`). When set, the agent picks one via `--workspace`; the daemon validates the choice matches an entry and stays inside the workspace. Empty ⇒ `working_dir`, no selection. |

**Invocation surface** (agent-facing, injected wrapper + rule):
`escape-hatch <name> [--workspace <dir>] [-- <args...>]`. The wrapper POSTs `{sandbox_id, name,
workspace, args}`; the daemon rejects any argument not matching `subcommands`/`args_pattern` (HTTP 400)
and any workspace not matching `workspaces` (HTTP 400). Both agent inputs are **human-gated**: nothing
the agent supplies escapes the author's allowlist, and arguments run as positional parameters so shell
metacharacters are inert.

**Lifecycle**: authored → validated (client-side) → attached (rides the kit) → **resolved** per sandbox
→ persisted on the sandbox → injected as rule + wrapper. It has no identity of its own beyond `name`
within its resolved set.

**Wire placement**: `repeated EscapeHatchCommand escape_hatch = 3;` added to `KitSpec`
(`spec`-arm of `KitRef` only; external `source` kits carry none).

---

## 2. Resolved escape-hatch set — persisted on the sandbox

The daemon computes, at `LaunchSandbox` / `AddSandboxKit`, the **resolved** set = the union of every
attached client-authored kit's `escape_hatch`, applied in attach order with **later-kit-wins** on
name collision (Q1). It is stored so (a) the allowlist is enforceable on every invocation, (b) a
container recreate replays the same set, and (c) the rule/wrapper can be re-injected on refresh.

- New field on `Sandbox`: `repeated EscapeHatchCommand escape_hatch_commands = 19;`
  (next free number after `kits = 18`). Persisted verbatim in the bbolt registry via the existing
  `pb.Sandbox` marshaling — no schema change to the registry.
- Invariant (FR-036): a sandbox's invokable set is **exactly** `escape_hatch_commands` — the endpoint
  rejects any name not in it.

---

## 3. `EscapeHatchRun` — one execution triggered by the agent

In-memory only (Q2 / research R6); lives in `internal/escapehatch`'s run store for the daemon's uptime.
Surfaced to clients over the event stream and `ListEscapeHatchRuns`; **not** in bbolt.

| Field | Type | Notes |
|-------|------|-------|
| `id` | string | `ehr-<seq>`, like `agent.Hub`'s `evt-<seq>` ids. |
| `sandbox_id` | string | Owning sandbox (registry key). |
| `command_name` | string | The resolved command's `name`. |
| `command` | string | The exact string that ran (echoed for audit; agent could not alter it). |
| `status` | enum `EscapeHatchRunStatus` | `PENDING_APPROVAL` \| `RUNNING` \| `SUCCEEDED` \| `FAILED` \| `TIMED_OUT` \| `CANCELLED` \| `DENIED`. |
| `exit_status` | int32 | Set for SUCCEEDED/FAILED (process exit code). |
| `output` | string | Bounded (≤1 MiB), possibly truncated. |
| `output_truncated` | bool | True when the cap clipped the capture. |
| `started_at` / `ended_at` | timestamp | `started_at` set when execution begins (post-approval); `ended_at` on terminal outcome. |
| `args` | string | Agent-supplied arguments that ran (audit; empty if none). |
| `working_dir` | string | Resolved workspace-relative directory it ran in (audit; empty = workspace root). |

**State transitions**:

```
                approve (DecideEscapeHatchRun)         exit 0
PENDING_APPROVAL ───────────────────────────► RUNNING ─────────► SUCCEEDED
   │  │                                          │  │  exit≠0
   │  │ deny / 5-min window elapses / bad input  │  └──────────► FAILED
   │  └──────────────────────────────────────►  │  timeout(cmd|30m)
   │            DENIED                            ├────────────► TIMED_OUT
   │ (AUTO_RUN skips PENDING_APPROVAL)            │  sandbox stop/destroy/refresh
   └─────────────────────────────────────────►   └────────────► CANCELLED
                                              launch failure ──► FAILED (diagnostics in output)
```

Every terminal state triggers the **agent callback** (`agent.Registry.Prompt` with an outcome message)
and a `RUN_COMPLETE` notification; `PENDING_APPROVAL` triggers a `NEEDS_APPROVAL` notification.

---

## 4. `AgentEscapeHatchRule` — injected guidance (derived, not stored as an entity)

Not a persisted record: it is **rendered on demand** by the daemon from `Sandbox.escape_hatch_commands`
at bring-up and written into the workspace (research R3):

- `<workspace>/CLAUDE.md` — a marker-delimited managed block enumerating the resolved commands (name,
  when-to-use, consent mode, the exact wrapper invocation, "runs on the host, asynchronously").
- `<workspace>/.switchboard/escape-hatch` — the executable wrapper (embeds `sandbox_id` + callback URL;
  takes the command **name** only).

Both are (re)written on `Launch`/`Refresh`/`AddSandboxKit` and **removed** when the resolved set is
empty (FR-037's "no rule / nothing invokable when none attached").

---

## 5. Client kit struct (editor + storage)

`store.Kit` (`src/apps/switchboard-tui/internal/store/kit.go`) gains:

```go
// EscapeHatch is a switchboard-owned section: it is NOT rendered into spec.yaml
// (sbx knows nothing of it) — it travels as structured proto on KitSpec.
EscapeHatch []KitEscapeHatchCommand

type KitEscapeHatchCommand struct {
    Name             string
    Command          string
    WhenToUse        string
    RequiresApproval bool     // false => auto-run
    WorkingDir       string   // optional, workspace-relative (default when Workspaces empty)
    MaxDurationSecs  uint32   // optional, 0 => daemon default
    Subcommands      []string // optional; allowed agent argument strings (mutually exclusive with ArgsPattern)
    ArgsPattern      string   // optional; anchored regex for agent arguments (loose = lower safety)
    Workspaces       []string // optional; workspace-relative literal paths or globs the agent may target
}
```

**Storage note**: today `KitStore` persists only the rendered `spec.yaml` and reloads by unmarshaling
it (`kit.go:208-242`). Because escape-hatch commands must **not** enter `spec.yaml`, they are persisted
in a **sidecar** `kits/<id>/escape-hatch.yaml` written/read alongside `spec.yaml` in `Save`/`Get`. The
sidecar is client-owned metadata; the daemon never sees it as a file — the client sends the commands as
structured proto on `KitSpec.escape_hatch`.

**Conversion**: `Kit.ToSpec()` populates `pb.KitSpec.EscapeHatch` from `Kit.EscapeHatch`; `SpecYAML()`
is unchanged (escape-hatch excluded), so `sbx kit validate` still sees only the Docker schema.

---

Th## 6. Validation rules (client-side, in the editor)

Enforced before `saveKit`, surfaced like the existing `ValidateKit` diagnostics:

- `name`: non-empty, unique within the kit's escape-hatch list, `kebab-case` (Rule IV).
- `command`: non-empty.
- `when_to_use`: non-empty (the rule is useless without it).
- `working_dir`: if set, relative and non-escaping (`filepath.Clean` must stay within the workspace).
  The daemon **re-validates** this containment at execution time (defense-in-depth), independent of the
  client check — see the executor task in `tasks.md`.
- `max_duration_seconds`: `>= 0` (0 = default).
- `consent_mode`: exactly one of auto-run / requires-approval.

The Docker portion of the kit is still validated by `sbx kit validate` (daemon `ValidateKit` RPC),
unchanged.
