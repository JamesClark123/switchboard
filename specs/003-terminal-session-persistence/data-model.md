# Phase 1 Data Model: Terminal Session Persistence & Sandbox Tags

Entities introduced or changed by this feature. New entities are **in-memory daemon state** unless
noted; the only **persisted** change is the sandbox `tag`. Cross-reference: `research.md` (R1–R6),
`contracts/switchboard-terminal.proto`.

---

## TerminalSession (NEW — daemon, in-memory)

The persistent per-sandbox session that outlives any attached client. One per running sandbox,
created lazily on first prompt/attach (as today) and kept alive across detach.

| Field | Type | Notes |
|---|---|---|
| `sandbox_id` | string | Owning sandbox (1:1 with a running sandbox). |
| `pty` | `agent.Session` | Existing `creack/pty` session (`sbx exec -it`). Unchanged ownership. |
| `screen` | VT emulator | Current screen state (cursor, cells, modes) — snapshot source (R2). |
| `scrollback` | ring buffer | Bounded byte history, default 5 MB (R2); oldest bytes evicted. |
| `attachments` | set<Attachment> | Currently-connected clients; drives the count (FR-007). |
| `size` | {rows, cols} | Reconciled PTY size = min across interactive attachments (R3). |

**Lifecycle / state transitions**:
- **Created**: first `PromptAgent` or `AttachAgent` for the sandbox → PTY spawned, broadcaster starts
  reading.
- **Attached / Detached**: an Attachment is added/removed; the session itself is **unchanged** by
  detach — the PTY keeps running (FR-002, FR-004). Count and reconciled size recompute.
- **Ended**: sandbox transitions to `STOPPED`/`DESTROYING` (feature 001) → session closed, PTY
  terminated, attachments dropped (FR-006). A new session is created on the next run (not resumed).

**Invariants**:
- Exactly one goroutine reads the PTY (fan-out is downstream) — no two readers race for bytes.
- Detach never calls `pty.Close()`; only sandbox stop/destroy does.
- Scrollback never exceeds its byte bound; the current screen is always renderable from `screen`.

---

## Attachment (NEW — daemon, in-memory)

One connected client of a `TerminalSession`. Backs both the attachment **count** and the
**one-external-terminal** rule.

| Field | Type | Notes |
|---|---|---|
| `id` | string | Per-attachment id. |
| `kind` | enum `IN_TUI` \| `EXTERNAL` | Set from the first `AttachAgent` message (R5). |
| `send` | chan bytes | Live output fan-out target for this client. |
| `size` | {rows, cols} | This client's last-reported window size (feeds the arbiter, R3). |
| `interactive` | bool | Whether this client's size participates in reconciliation (true for both kinds today; read-only observers reserved for future). |

**Rules**:
- At most **one** `EXTERNAL` attachment may exist per session at a time; a second `EXTERNAL` attach is
  rejected by the daemon with a typed `AlreadyAttachedExternally` error (FR-014, R5).
- `IN_TUI` and `EXTERNAL` MAY coexist on one session (FR-016, permitted-not-required).
- Adding/removing an attachment publishes a sandbox-changed Event so the list-page count updates
  (FR-008, SC-005).

---

## Snapshot (NEW — wire message, transient)

The one-shot redraw payload sent to a client immediately on attach, before live streaming.

| Field | Type | Notes |
|---|---|---|
| `data` | bytes | ANSI redraw reconstructing the current screen from the VT model (R1). |
| `rows`, `cols` | uint32 | Session size at snapshot time, so the client sizes its viewport. |
| `scrollback` | bytes (optional) | Bounded tail of prior output for review (FR-005). |

Not persisted; recomputed on each attach from `TerminalSession.screen` (+ `scrollback`).

---

## WorkspaceMarker (NEW — on-disk, per workspace copy)

Written by the daemon into each controlled workspace copy at launch; read by `sxb` to auto-resolve
the owning sandbox (R6, FR-017/018).

| Field | Type | Notes |
|---|---|---|
| path | `<workspace>/.switchboard/session.json` | Walk-up target from `cwd`. |
| `host_id` | string | Which daemon owns it. |
| `sandbox_id` | string | The sandbox to attach to. |
| `socket` | string | Local socket / connection descriptor for that daemon. |

**Rules**: written on launch, removed on destroy. Present-but-stale (sandbox stopped/unknown) →
client shows an actionable message / falls back to the TUI (FR-020).

---

## Sandbox (CHANGED — daemon registry, persisted)

Feature 001's `Sandbox` gains two derived/persisted fields. See `contracts/switchboard-terminal.proto`.

| Field | Type | Persisted? | Notes |
|---|---|---|---|
| `tag` | string (≤64 chars) | **Yes** (bbolt) | Mutable, non-unique purpose label; distinct from `id`/`display_name`; never affects identity or lifecycle (FR-021–024, R6). Empty = untagged. |
| `attached_terminals` | int32 | No (derived) | Live count of attachments to this sandbox's session (FR-007/008). Recomputed and published on attach/detach. |
| `external_attached` | bool | No (derived) | Whether an `EXTERNAL` attachment exists — lets any client show/deny "open external" correctly (R5). |

**Validation**:
- `tag`: trimmed; length ≤ 64; any UTF-8; empty string clears the tag. Setting it emits a
  sandbox-changed Event but changes **no** other field (SC-007).
- `attached_terminals` ≥ 0; equals `len(attachments)` for the sandbox's session (0 when no session).

---

## Entity relationships

```text
Sandbox (001, +tag persisted, +count/external derived)
   │ 1
   │
   │ 0..1   (only while RUNNING)
TerminalSession (in-memory)
   │ 1
   │
   │ 0..N
Attachment (IN_TUI | EXTERNAL; ≤1 EXTERNAL)
   └── each has a live send channel; snapshot sent once at attach

WorkspaceMarker (on-disk, 1:1 with a sandbox's workspace copy) ──resolves──▶ Sandbox
```
