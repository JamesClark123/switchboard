# Phase 1 Data Model: Port Forwarding

**Feature**: `006-port-forwarding` | **Spec**: [spec.md](./spec.md) | **Research**: [research.md](./research.md)

Three new entities, one extended existing entity, and one purely client-side entity. Wire shapes are in
[contracts/switchboard-port-forwarding.proto](./contracts/switchboard-port-forwarding.proto); this
document is the semantic model — fields, invariants, validation, lifecycle.

---

## Entity map

```
KitService ──declared on──► Kit ──attached to──► Sandbox
    │                                               │
    │                            resolve (later-kit-wins, R8)
    │                                               ▼
    └──────────one per start──────────────► ServiceInstance (in-memory, daemon)
                                                    │
                                                    ▼
                                         LocalForward (in-memory, client)
```

Persistence split (R7): **declarations persist, instances do not.**

| Entity | Owner | Lifetime | Storage |
|---|---|---|---|
| `KitService` | client | authored, edits with the kit | `kits/<id>/services.yaml` sidecar |
| `Sandbox.services` (resolved) | daemon | sandbox lifetime | bbolt registry (additive proto field) |
| `ServiceInstance` | daemon | daemon uptime | in-memory `InstanceStore` |
| `LocalForward` | client | client uptime | in-memory |

---

## KitService

One long-running service declared on a kit. The authoring contract (FR-043).

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | ✅ | `kebab-case`, unique **within the kit**. The stable key services are addressed by, and the key later-kit-wins resolves on. |
| `command` | string | ✅ | Run via `/bin/sh -c`. May contain the `{{port}}` token (R4). |
| `listen_port` | uint32 | ✅ | 1–65535. The port the command binds **in its own execution environment**. |
| `location` | enum | ✅ | `IN_SANDBOX` \| `ON_HOST`. `UNSPECIFIED` is rejected at authoring and at attach — there is no safe default. |
| `is_website` | bool | — | Enables the browser-open action; otherwise the client offers a copyable address (FR-050). |
| `working_dir` | string | — | Workspace-relative. Must not escape the workspace. |
| `readiness_timeout_seconds` | uint32 | — | 0 ⇒ the 60 s default (R9). |

### Validation (client-side at authoring; re-validated daemon-side at attach)

1. `name` non-empty, `kebab-case`, unique within the kit → else reject naming the field (US1-3, US1-4).
2. `command` non-empty after trimming.
3. `listen_port` in 1–65535.
4. `location` explicitly set.
5. `working_dir`, when set, is relative and `filepath.Rel(workspace, join(workspace, dir))` does not
   begin with `..` — reusing `escapehatch.resolveWorkdir`'s containment check verbatim, as
   defence-in-depth on both sides.
6. An abandoned edit mutates nothing (US1-5) — the sidecar is written only on save, like
   `escape-hatch.yaml`.

### Resolution onto a sandbox

`Sandbox.services` = ordered union over attached kits, in attachment order, **later kit wins** on equal
`name` (FR-044). Identical to `escapehatch.Resolve`, and it runs at the same two points: sandbox
creation and `AddSandboxKit`.

---

## ServiceInstance

One execution attempt of a `KitService` for one sandbox. Daemon-owned, in-memory, session-scoped (R7).

| Field | Type | Notes |
|---|---|---|
| `id` | string | `svc-<seq>`, mirroring `ehr-<seq>`. |
| `sandbox_id` | string | |
| `service_name` | string | Resolves against `Sandbox.services` — the allowlist check. |
| `state` | enum | `STOPPED` \| `STARTING` \| `RUNNING` \| `FAILED` (FR-047). |
| `effective_port` | uint32 | What the command actually binds (R4). Equals `listen_port` except for an on-host `{{port}}` service. |
| `host_endpoint_port` | uint32 | Port on the **daemon host** that `ForwardPort` dials. In-sandbox: the published port (R2). On-host: `effective_port`. |
| `local_port` | uint32 | On the **developer's machine**. Set by the client, echoed back for display; 0 while not forwarded. |
| `failure_reason` | enum | See below. Set iff `state == FAILED`. |
| `failure_detail` | string | Human-readable, names the remedy where one exists. |
| `output` | string | Bounded 1 MiB, **tail-retained** (R9). |
| `output_truncated` | bool | |
| `started_at` / `ended_at` | timestamp | |
| `pgid` | int32 | Internal (not on the wire): in-sandbox process-group id from R3, for the R6 kill. |

### State machine

```
                    start ─────────────────────────────┐
                      │                                 │
  STOPPED ──────► STARTING ───(dial succeeds)───► RUNNING ───(stop)──► STOPPED
                      │                                 │
                      │                                 └──(process exits)──► FAILED
                      └──(launch error / port in use / window elapses)─────► FAILED
                                                                                │
                                                                    (start)     │
                                                          STOPPED ◄─────────────┘
```

Invariants:

- **`RUNNING` implies reachable.** The transition requires a successful TCP dial of the host endpoint —
  never merely "the process started" (FR-047, Key Decision 6, SC-007).
- **Terminal states release resources.** Both `STOPPED` and `FAILED` release the local port, unpublish
  the sandbox port, and leave no process (FR-048, SC-004).
- **Start is idempotent.** Starting a service already `STARTING`/`RUNNING` returns the existing instance
  unchanged — no second process, same local address (FR-048, US4-6).
- **At most one non-terminal instance per (sandbox, service name).** Enforced in `InstanceStore` under
  its mutex; this is what makes idempotence race-free.
- **Sandbox teardown cascades.** Stop/destroy/refresh stops every instance of that sandbox — the same
  hook `escapehatch.Service.Cancel` already attaches to (FR-048, US4-5).
- Only `FAILED` raises a notification; every transition publishes an event (FR-052).

### Failure reasons

Exactly the set FR-051 enumerates, plus the R5 loopback verdict:

| Reason | Raised when |
|---|---|
| `LAUNCH_FAILED` | The command could not be started (not found, not executable, bad working dir). |
| `PORT_IN_USE` | The effective port is already held in the execution environment — probed *before* launch for on-host services (R4). |
| `NOT_LISTENING` | Readiness window elapsed; nothing is listening on the declared port. |
| `NOT_LISTENING_LOOPBACK` | Readiness window elapsed and `/proc/net/tcp` inside the sandbox shows the port bound to loopback only (R5). Detail names binding to all interfaces as the fix. |
| `EXITED_EARLY` | The process exited before becoming ready. |
| `EXITED_UNEXPECTEDLY` | The process exited after reaching `RUNNING`. |
| `SANDBOX_NOT_RUNNING` | An in-sandbox start was attempted against a stopped sandbox — refused with **no port allocated** (FR-046, US2-6). |
| `NO_LOCAL_PORT` | No free port could be allocated on the developer's machine; nothing was left started (FR-049). |
| `HOST_UNREACHABLE` | The path to a remote host was lost while running — displayed as unreachable, never as a working address (FR-050, US5-2). |

---

## Sandbox (existing, extended)

One additive field, following `escape_hatch_commands` exactly:

```protobuf
repeated KitService services = 20;  // resolved, later-kit-wins; persisted
```

Persisted for the same three reasons feature 005 persists its allowlist: it is the **enforcement set**
that `StartSandboxService` validates a name against, a container recreate must replay it, and refresh
must re-resolve it. Additive field number ⇒ no registry migration.

---

## LocalForward (client-side)

Not on the wire. One per forwarded instance, owned by the client's forward manager.

| Field | Notes |
|---|---|
| `instance_id` | Correlates with the daemon's `ServiceInstance`. |
| `listener` | `net.Listener` bound to `127.0.0.1:0`; the OS is the port allocator (R1). |
| `local_port` | Reported back to the daemon so it appears in the instance record for display. |
| `conns` | Live relayed connections, each holding one `ForwardPort` stream. |

Lifecycle: opened when an instance reaches `RUNNING`; closed on any terminal transition, on host
disconnect, and on client exit. Closing the listener closes every relayed connection.

**Uniqueness** (FR-049) is structural: one process binding `:0` per forward means the OS never hands
out the same port twice while both are held — across sandboxes, across hosts, and across services
declaring the same `listen_port`.

---

## Entity → requirement coverage

| Requirement | Where it lands |
|---|---|
| FR-043 authoring + validation | `KitService` fields + validation rules 1–6 |
| FR-044 attach, persist, later-kit-wins | `Sandbox.services` resolution |
| FR-045 per-sandbox list, nothing auto-starts | `ListSandboxServices`; no start path outside `StartSandboxService` |
| FR-046 execution location, refuse when stopped | `location`; `SANDBOX_NOT_RUNNING` |
| FR-047 states, readiness, unexpected exit | state machine + `RUNNING`-implies-reachable invariant |
| FR-048 stop, cascade, idempotence | terminal-state invariants + at-most-one-instance invariant |
| FR-049 free local port, unique, released | `LocalForward` + terminal-state release |
| FR-050 traffic reaches, browser/copy, unreachable | `host_endpoint_port` → `local_port` relay; `is_website`; `HOST_UNREACHABLE` |
| FR-051 bounded output, failure reasons | `output` (tail-retained) + failure-reason table |
| FR-052 live updates, failure notification | event on every transition; notification on `FAILED` only |
