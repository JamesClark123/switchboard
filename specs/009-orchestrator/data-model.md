# Phase 1 Data Model: Orchestrator

Derived from [spec.md](./spec.md) Key Entities + FR-105–FR-157, with mechanics fixed by
[research.md](./research.md). Wire shapes are in
[contracts/switchboard-orchestrator.proto](./contracts/switchboard-orchestrator.proto); the
sandbox-facing tool surface in [contracts/orchestrator-mcp.md](./contracts/orchestrator-mcp.md).

## Entity map

| Entity | Kind | Stored | Owner |
|--------|------|--------|-------|
| `Orchestrator` (name, hosts, actions, timed events, reasoning setup, roster) | persisted | bbolt bucket `orchestrators`, key = id | hosting daemon (R1/R9) |
| `Run` | persisted | bbolt bucket `orchestrator_runs`, key = `<orch id>/<seq%020d>` | hosting daemon (R9) |
| `Sandbox.controller` | persisted (field 23) | bbolt sandbox record | **owning** daemon (R3) |
| `worker_token` (daemon-local, never on the wire) | persisted | bbolt bucket `worker_tokens`, key = sandbox id | **owning** daemon (R4) |
| Per-worker gateway registration `sxb-orch-<sandbox8>` | host truth | `sbx mcp` on the owning host | runtime, driven by the owning daemon (R4) |
| Briefing block | file | `<workspace>/CLAUDE.md` managed block | owning daemon (R12) |
| Host link (SSH conn + Subscribe + OrchestratorLink streams) | transient | never | orchestrator runtime (R2/R6) |
| Worker availability | derived | never | orchestrator runtime (R7) |
| Review grace timer, dispatch pass, scheduler timer | transient | never | orchestrator runtime (R7/R9/R10) |
| TUI: Orchestrator Sandboxes tab flag, screen cursors, forms | transient | never | TUI model (R13) |

## Orchestrator (persisted; proto `Orchestrator`)

| Field | Type | Rules |
|---|---|---|
| `id` | uuid | assigned at create; never changes; referenced by every other record (FR-148) |
| `name` | string | 1–64, unique among this daemon's orchestrators (the TUI checks all connected daemons); the only thing rename touches |
| `host_id` | string | the hosting daemon's id |
| `hosts[]` | `OrchestratorHost` | one per worker machine; `host_id` unique; the orchestrator's own host needs none |
| `actions[]` | `Action` | names unique; validated at save (below) |
| `timed_events[]` | `TimedEvent` | ids `tev-<seq>` |
| `reasoning_setup` | `ReasoningSetup` | empty = default empty context |
| `reasoning_sandbox_id` | string | set once the reasoning sandbox is launched; re-set on rebuild |
| `workers[]` | `Worker{sandbox_id, host_id, attached_at, reasoning}` | roster; **re-derived from owning daemons' records on every reconcile** (the record is the authority, Key Decision 5) |

Invariants: I-O1 name unique per daemon and never used as a key; I-O2 the roster never contains a
sandbox whose record names another controller (reconcile drops it); I-O3 removing an
orchestrator removes its runs bucket prefix, its reasoning sandbox and, for every reachable
worker, the controller record (FR-153).

## SandboxController (persisted on the sandbox; proto `SandboxController`)

| Field | Set by | Cleared by |
|---|---|---|
| `orchestrator_id`, `host_id`, `attached_at`, `reasoning` | `AttachController` (CAS: only when unset or the same identity) | `DetachController` (`force` required for `reasoning`) |
| `name` | `AttachController`; refreshed by `UpdateWorker{name}` | with the controller |
| `run{id, action, needs_review}` | `DeliverWorkerPrompt` (id, action); `UpdateWorker{set_run}` (needs_review or clear) | run close (orchestrator), detach, destroy |
| `worker_token` (bucket `worker_tokens`, not on the wire) | `AttachController` (32 random bytes) | detach / destroy |

Invariants: I-C1 at most one controller per sandbox, settled inside one bbolt write (FR-108,
SC-007); I-C2 attach/detach never change `state`, the agent session or attachments (FR-109);
I-C3 the TUI derives everything from this record: `hidden := controller != nil &&
!needsDeveloper`, `needsDeveloper := agent.status == NEEDS_INPUT || controller.run.needs_review`
(FR-113/FR-114); I-C4 the token never appears in logs, events or RPC responses.

## Action (persisted on the orchestrator; proto `Action`; FR-136–FR-140)

Validation at save (`PutAction`, FR-121/FR-137/FR-138/FR-139):

| Check | Refusal |
|---|---|
| `name` matches `[a-z0-9][a-z0-9-]*`, ≤ 64, unique within the orchestrator (replace allowed on the same name) | "action name …" |
| `type` ∈ {PROMPT, REASON} | "unknown action type" |
| `body` non-empty | "body is empty" |
| every `{name}` placeholder in `body` names a declared argument | "placeholder {x} names no argument" |
| argument names unique, each `[A-Za-z_][A-Za-z0-9_]*` | "argument x declared twice" |
| `worker_tag` only when `type == PROMPT` | "reason actions take no worker requirement" |
| an argument removed while the body still names it | refused (edge case) |

Eligibility: a `prompt` run may go to any worker when `worker_tag == ""`, else only to workers whose
`Sandbox.tag == worker_tag`; a `reason` run only to the reasoning sandbox.

## Run (persisted; proto `Run`; FR-120, FR-124, FR-125) — state machine

```
                 CallAction (user | timed | triggered): validate → compose → QUEUED
QUEUED ──CancelRun──► CANCELLED
QUEUED ──dispatch: DeliverWorkerPrompt accepted──► RUNNING
RUNNING ◄──agent WORKING | WAITING──┐
RUNNING ──agent NEEDS_INPUT──► AWAITING_INPUT ──agent WORKING | WAITING──► RUNNING
RUNNING | AWAITING_INPUT ──report──► COMPLETED(summary)
RUNNING | AWAITING_INPUT ──agent IDLE, no report, reviewGrace elapsed──► AWAITING_REVIEW
AWAITING_REVIEW ──agent WORKING | WAITING (it resumed; needs_review cleared, SWI-12)──► RUNNING
AWAITING_REVIEW ──report──► COMPLETED(summary)
AWAITING_REVIEW ──MarkWorkerFree──► COMPLETED | FAILED (summary = note, closed_by_developer)
RUNNING | AWAITING_INPUT ──agent EXITED | sandbox STOPPED/ERROR/removed | worker detached──► FAILED(reason)
AWAITING_REVIEW ──sandbox removed | orchestrator removed──► FAILED(reason)   (stopped: stays)
any in flight ──orchestrator removed──► FAILED ;  QUEUED ──orchestrator removed──► CANCELLED
```

`WAITING` (SWI-12) is an agent whose turn ended with background work it started still running
(the Stop hook's `background_tasks`); Claude Code wakes it when that work finishes. It never arms
the review grace. The daemon's hook server turns a WAITING agent IDLE after `backgroundWaitMax`
(30 min), so a background task that never ends cannot hold a run forever.

Fields fixed at creation: `action`, `type`, `arguments`, `prompt` (composed, R8), `event_kind`,
`origin`, `origin_label`, `received_at`. Set at start: `started_at`, `sandbox_id`,
`sandbox_host_id`. Set at close: `ended_at`, `summary` (exactly one), `reason`,
`closed_by_developer`. Derived: `position` (1-based among QUEUED, in key order).

Invariants: I-R1 key order = order of receipt (zero-padded sequence); I-R2 at most one run in
flight per sandbox (FR-118) — enforced by `controller.run` on the owning daemon; I-R3 a run is never
re-delivered (FR-134) — reconcile only re-reads state; I-R4 retention trims COMPLETED/FAILED/
CANCELLED beyond the newest 200 per orchestrator and never a QUEUED or in-flight run (FR-127).

## Dispatch pass (transient; R9)

```
free(w)      := w.state == RUNNING && w.agent == IDLE && w.controller.run == nil && link(w.host).up
eligible(r)  := r.type == REASON ? {reasoning} : {w ∈ workers | r.worker_tag == "" || w.tag == r.worker_tag}
claimed := ∅
for r in QUEUED runs in key order:
    s := first w ∈ eligible(r) with free(w) && w ∉ claimed
    if s == nil: continue                      # keeps its place; never blocks later runs
    claimed += s; DeliverWorkerPrompt(s, r)    # refused ⇒ r stays QUEUED, s treated as not free
```

Triggered by: run queued, worker became free (status event), link up, run closed, reconcile.
Guarantees SC-003 (no run starts before an earlier one the same sandbox could have taken) and
Key Decision 8 (no head-of-line blocking).

## Worker availability (derived; proto `WorkerAvailability`; FR-117)

| Condition (first match) | Availability |
|---|---|
| host link down | UNREACHABLE |
| `state != RUNNING` | NOT_RUNNING |
| `controller.run.needs_review` | AWAITING_REVIEW |
| `agent == NEEDS_INPUT` | AWAITING_INPUT |
| `agent == WORKING` or `agent == WAITING` or `controller.run != nil` or `agent == EXITED` | BUSY |
| otherwise | FREE |

## TimedEvent (persisted on the orchestrator; proto `TimedEvent`)

Rules: `interval_seconds ≥ 60`; `action` must exist at save; `enabled` default true on create;
`dedupe_disabled` default false (= dedupe on). Scheduler: `next_fire_at` initialised to
`now + interval`; on fire → `CallAction(event.arguments)` as `TIMED` with origin = event id, then
`last_fired_at = now`, `next_fire_at += interval`; dedupe skip when any run with this origin is
QUEUED or in flight → `last_skipped_at`, `skipped_count++`, `next_fire_at += interval`. After a
restart: `next_fire_at = last_fired_at + k·interval` for the smallest k with a future value (no
replay). Deleting the action → `enabled = false`, `disabled_reason` set (FR-130).

**Active window (SWI-10)**: optional `window: ActiveWindow{start, end, weekdays, timezone}`, a
daily `HH:MM`–`HH:MM` window (end exclusive; `end < start` crosses midnight) in an IANA timezone,
optionally limited to weekdays (0=Sun..6=Sat, the day the window opens). Absent = around the clock,
exactly as above. With a window, the event fires at each opening `o` and at `o + k·interval` while
before the close; the phase restarts at every opening. `next_fire_at` is the first such slot strictly
after now — on save (never an immediate fire), after each firing or skip, and after a restart (no
replay). A changed window restarts the phase like a changed interval. `FireTimedEvent` (Enqueue now)
ignores the window. Validation: `HH:MM` 24h, `start != end`, weekdays 0–6, a loadable IANA timezone.

## ReasoningSetup and the reasoning sandbox (R11)

`ReasoningSetup{sources, config, kits, mcp_servers}`; empty = `LaunchRequest{EmptyWorkspace:
true}` with the daemon's default config. Launch: display name `reasoning-<orchestrator8>`,
`controller{…, reasoning: true}`, on the orchestrator's host, right after create (async) and on
rebuild (destroy + launch, streamed). Invariants: I-S1 exactly one per orchestrator (FR-145);
I-S2 never detachable or attachable elsewhere; I-S3 destroyed with the orchestrator; I-S4 rebuild
refused while `controller.run != nil`.

## Host link (transient; R2/R6)

```
entry (host_id, ssh_target, ssh_options)
  └─ dial.Conn  ──  redial with backoff 15 s → 2 min while down; local host = in-process
       ├─ Subscribe(replay=false)          → sandbox_changed / removed for this orchestrator's workers
       ├─ OrchestratorLink (hello first)   → call ← / result →
       └─ unary: ListSandboxes, AttachController, DetachController, UpdateWorker, DeliverWorkerPrompt
on (re)connect: reconcile = adopt(records with my id) ∪ drop(records without it) ; push name+briefing ; re-check in-flight runs (FR-134, ≤ 30 s)
```

## bbolt layout (registry.db)

| Bucket | Key | Value | Notes |
|---|---|---|---|
| `sandboxes` (existing) | sandbox id | `Sandbox` (+ field 23) | unchanged otherwise; the record never carries the token |
| `worker_tokens` (new) | sandbox id | the hex token | written at attach, deleted at detach/destroy; also indexed in memory by the MCP endpoint |
| `orchestrators` (new) | orchestrator id | `Orchestrator` | one record per orchestrator |
| `orchestrator_runs` (new) | `<orchestrator id>/<seq %020d>` | `Run` | prefix scan = receipt order; `seq` from a per-orchestrator counter stored under `<orchestrator id>/_seq` |

## Client-side view models (R13)

- **Orchestrators screen row**: `{host (TUI's host entry), summary: OrchestratorSummary, reachable
  (host connected && daemon answered)}` from `ListOrchestrators` on every connected daemon.
- **Orchestrator detail**: `OrchestratorDetail` + the current view (`Workers | Actions | Queue |
  Events | Reasoning | Hosts`) and its cursor; refreshed on `Event.orchestrator_changed`.
- **Sandbox row**: line 1 as today; line 2 as today minus the `mcp:` badge; line 3 `mcp: <names>
  · orchestrator: <controller.name>` (either half omitted when absent; blank when both are),
  truncated to the row width. A surfaced worker's line 1 adds `worker · needs input` /
  `worker · review`.
- **Orchestrator Sandboxes tab**: `tabOrchestrated`, shown while `showOrchTab`; rows = every
  sandbox with a controller across `hostAgg`, reasoning ones marked; an unreachable orchestrator
  is named in the tab's status line. Every per-sandbox key works on a worker wherever it is
  shown, plus `o` detach and `m` mark free (FR-116).
- **Attach picker**: `[{orchestrator summary, host}]` across connected daemons; choosing one whose
  `hosts[]` lacks the sandbox's host first offers *add host <name> to <orchestrator>* (copies the
  TUI's `KnownHost` into `SetOrchestratorHosts`), then `AttachWorker`.
- **Mark-free dialog**: outcome (completed default / failed) + optional note → `MarkWorkerFree`.
- **Event stream per host**: `eventMsg{hostID, ev}`; inbox items keep their `host_id` for ack.
