# Implementation Plan: Orchestrator

**Branch**: `009-orchestrator` | **Date**: 2026-10-14 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/009-orchestrator/spec.md`

## Summary

Give the daemon a new **orchestrator** role: a named, persistent prompt queue the developer places
on a machine, which attaches sandboxes on any reachable machine as **workers**, turns developer-
authored **actions** (name, body with `{placeholders}`, declared arguments, type, worker-tag
requirement) into **runs** whenever an **event** calls them (by hand, on a timer, or from a
worker's agent through MCP), and delivers each composed prompt to a free sandbox through that
sandbox's own daemon — `prompt` runs to workers, `reason` runs to the orchestrator's own
**reasoning sandbox**. Runs end only with the agent's **completion report** (an MCP tool call
carrying a summary); an agent that goes idle without reporting hands its sandbox to the developer
as **awaiting review**, surfaced at the top of the sandbox list with a one-key **mark free**. The
TUI gains a third top-level **Orchestrators** screen in the shift+←/→ cycle with a header strip
naming all three screens, workers hidden from the everyday tabs except when they need the
developer, a hidden-by-default **Orchestrator Sandboxes** tab, and a third row line naming a
sandbox's MCP servers and orchestrator.

Technical core: one new daemon package for the orchestrator side and one for the worker side
([R1](./research.md#r1--hosting-model-one-daemon-package-one-runtime-per-orchestrator)); the TUI's
SSH/stdio dialer extracted into a shared library so the orchestrator's daemon can dial worker
machines exactly as the TUI does, with per-orchestrator host entries
([R2](./research.md#r2--reaching-worker-daemons-a-shared-dial-library-and-per-orchestrator-host-entries));
the controller recorded on the sandbox by its **owning** daemon with five worker-side RPCs
([R3](./research.md#r3--the-owning-daemon-holds-the-relationship-sandboxcontroller-and-five-worker-side-rpcs));
a per-worker MCP registration pointing the host gateway at a token-addressed endpoint on the
daemon's existing hook HTTP server, served with the official Go MCP SDK and relayed to the
orchestrator over a bidi link stream
([R4](./research.md#r4--gateway-exposure-and-attribution-one-registration-per-worker-token-in-the-url)–[R6](./research.md#r6--the-orchestrator-link-a-bidi-stream-the-orchestrator-opens-relaying-tool-calls));
guarded delivery, status folding, the review grace and two `internal/agent` gaps closed
([R7](./research.md#r7--free-delivery-status-tracking-review-and-two-daemon-gaps-it-closes)); a
durable, receipt-ordered run bucket with capacity-aware dispatch
([R9](./research.md#r9--queue-and-runs-bbolt-layout-dispatch-order-retention)); and the TUI work
including per-host event streams
([R13](./research.md#r13--tui-per-host-event-streams-a-third-screen-keys-the-hidden-tab-the-third-line)).
Contracts: [switchboard-orchestrator.proto](./contracts/switchboard-orchestrator.proto) (additive)
and [orchestrator-mcp.md](./contracts/orchestrator-mcp.md) (the sandbox-facing tools, briefing
and run frame). Validation in [quickstart.md](./quickstart.md); entities in
[data-model.md](./data-model.md).

## Technical Context

**Language/Version**: Go 1.26 (existing `go.work` monorepo: `switchboardd`, `switchboard-tui`,
`switchboard-proto`, `switchboard-update`, two e2e modules) **plus one new library module**
`src/libs/switchboard-dial` (R2). Node/pnpm only for repo tooling.

**Primary Dependencies**: gRPC (existing connections; 24 additive RPCs, one bidi stream);
`bbolt` (two new buckets); `github.com/google/uuid` (orchestrator identity, already a dep);
`os/exec` for `sbx mcp add/load/rm` and `ssh … sxbd dial-stdio`; **one new third-party
dependency**: `github.com/modelcontextprotocol/go-sdk v1.8.0` (the MCP server the gateway talks
to, R5); Bubble Tea/`huh`/`teatest` as today. Timers from the standard library (no cron).

**Storage**: Daemon — bbolt `registry.db`: buckets `orchestrators` and `orchestrator_runs` (R9),
plus `Sandbox` field 23 `controller` (and a daemon-local `worker_token`); no migration (absent =
not a worker). Files — the managed `<workspace>/CLAUDE.md` block (R12). Host runtime — one
`sxb-orch-<sandbox8>` MCP registration per worker (R4). Client — nothing new persisted (host
entries are per orchestrator, on the daemon).

**Testing**: Go `testing` via `make test`/`cover` (≥90% per module, Rule VI). Daemon mechanics
against stub `sbx`; the two-daemon path in-process over two Unix sockets with an injected dial
function; the MCP endpoint with the SDK's own client; scheduler/dispatcher with a fake clock and
order properties; `teatest` for every screen; E2E with two stub daemons and a stub `ssh` (R16).
Real-runtime and real-gateway facts are confined to quickstart Scenario 0 (R17).

**Target Platform**: Linux/macOS hosts running `switchboardd`; orchestrator and workers on the
same or different machines; the orchestrator's machine needs SSH (keys/agent) to worker machines
and nothing else; no new port — the MCP endpoint rides the existing hook server.

**Project Type**: CLI/daemon (Go) — TUI client + per-host daemon. Not a web app.

**Performance Goals**: dispatch ≤ 10 s of a sandbox freeing (SC-002; a dispatch pass per status
event); surfacing/hiding ≤ 5 s (SC-001; one `sandbox_changed` per transition, per-host streams);
worker tool calls acknowledged ≤ 2 s (SC-013; one link round trip); reports close runs ≤ 2 s
(SC-018); attach ≤ 5 s (SC-009; one `mcp add` + one `mcp load`, each bounded by the 008 timeouts);
timed events fire ≤ 10 s of schedule (SC-012); reconcile ≤ 30 s of a host being reachable (SC-008).

**Constraints**: one controller per sandbox, settled in one bbolt write (I-C1); at most one run in
flight per sandbox (I-R2); runs never re-delivered (I-R3); the orchestrator acts on a sandbox only
through its owning daemon (FR-106); the worker's machine never dials the orchestrator's (Key
Decision 18); attach/detach never stop, restart or recreate (FR-109); developer activity wins
(FR-119); completions are reported, not inferred (Key Decision 15); the worker token never leaves
the owning daemon (I-C4); **no new environment variables** (every bound is a package constant,
R15); **no new `sbx` verbs** — two new argv rows on existing verbs (`mcp add --url` to loopback,
`mcp load` of it), reconciled first (R17).

**Scale/Scope**: 24 RPCs + 4 enums + 18 messages + 3 fields + 1 Event arm + 3 NotificationKinds
(additive proto); two new daemon packages (`internal/orchestrator` ~9 files, `internal/worker`
~6 files) + edits in `agent`, `sandbox`, `registry`, `grpc`, `mcp`, `escapehatch` (shared managed
block), `cmd/sxbd`; one new library module (`switchboard-dial`, moved code); TUI: one new
top-level screen with six views and three forms, per-host subscriptions, list delegate, tab,
keys, launch sub-mode, inbox kinds (~10 new/edited files); e2e harness: second daemon + stub
`ssh`. No new deployable.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack
(Principles I–VIII, Tooling Standards). Features 001–008 are implemented in **Go**, a deviation
recorded and justified in **001's Constitution Check + Complexity Tracking** (where a constitution
amendment is recommended). Feature 009 stays inside that established deviation and **adds no new
ones**:

| Principle | Status for 009 |
|-----------|----------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue of the Biome gate, per 001); the new library module joins `MODULES` in the Makefile and `scripts/cover.sh`. |
| III Type Safety | ✅ Go static typing; additive proto codegen; `ActionType`/`RunState`/`EventKind`/`WorkerAvailability` are enums; tool inputs/outputs are typed Go structs with generated JSON schemas (R5). |
| IV Naming & Layout | ✅ `kebab-case` files, colocated `_test.go`; two new daemon packages (the 005/006/008 precedent); the shared dialer lives under `src/libs/` as the repository-structure rule prescribes for shared code (the `switchboard-update` precedent). |
| V Verification Before Merge | ✅ Same `make` gates (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`). No `--no-verify`. |
| VI Multi-Level Testing | ✅ Go unit + integration ≥90% per module (incl. the new library); daemon mechanics against stub `sbx`; cross-daemon path in-process; TUI via `teatest`; arc via two-daemon stub E2E (R16). Storybook/Playwright/MSW remain TS-only, out of scope under 001's deviation. |
| VII Containerized Deployment | ✅ N/A — host binaries (001 carve-out); no new deployable; the orchestrator is a daemon role, not a service. |
| VIII Env Discipline | ✅ **No new env vars**: timeouts, retention, limits, backoff and the gateway host are package constants (R15); host entries are daemon-persisted records. `env-check` surface unchanged. |
| Repository Structure | ✅ Additive within existing modules plus one new `libs` module registered in `go.work`, `Makefile` and `cover.sh`; no new top-level category. |

**Result**: PASS (within the pre-recorded 001 Go deviation; no new deviation). The one new
third-party dependency (the MCP Go SDK) is a library choice, not a tooling substitution, and is
justified in R5. **Re-checked after Phase 1 design — still PASS**: the design adds Go code in two
new daemon packages, one moved-code library module, an additive proto revision, two bbolt buckets
and one sandbox field, a managed-file block, TUI screens/views, and no new tooling, deployable,
or environment variable.

## Project Structure

### Documentation (this feature)

```text
specs/009-orchestrator/
├── plan.md                               # This file
├── research.md                           # Phase 0 — R1..R17 decisions + reconciliation items
├── data-model.md                         # Phase 1 — entities, invariants, run state machine, dispatch, bbolt layout
├── quickstart.md                         # Phase 1 — validation guide (Scenario 0 = reconciliation gate)
├── contracts/
│   ├── switchboard-orchestrator.proto    # additive proto revision (24 RPCs, enums, messages, fields, Event arm, kinds)
│   └── orchestrator-mcp.md               # sandbox-facing surface: registration argv, endpoint, 4 tools, briefing, run frame
├── checklists/requirements.md            # spec quality checklist
└── tasks.md                              # Phase 2 — created by /speckit-tasks (NOT here)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/
├── proto/switchboard.proto               # + orchestrator RPCs/messages/enums; Sandbox 23; LaunchSandboxRequest 7; Event 8; NotificationKind 6–8
└── gen/*.pb.go                           # regenerated via `make proto`

src/libs/switchboard-dial/                # NEW library module (moved from the TUI's internal/client) — R2
├── go.mod                                # + go.work `use`, Makefile MODULES, scripts/cover.sh
├── dial.go                               #   DialLocal, DialSSH, SSHCommand (BatchMode for daemons), DialCommand, stdioConn
├── conn.go                               #   Conn + GetDaemonInfo handshake, cached DaemonInfo accessors
├── errors.go                             #   DialError, DialErrorKind, ClassifyDialStderr
└── *_test.go                             #   moved tests

src/services/switchboardd/
├── internal/orchestrator/                # NEW package: the orchestrator side — R1
│   ├── service.go                        #   NewService; load records; start/stop runtimes; create/rename/remove; hosts
│   ├── store.go                          #   `orchestrators` + `orchestrator_runs` buckets; receipt-ordered keys; retention — R9
│   ├── actions.go                        #   validation at save; placeholders; composition; call validation — R8
│   ├── runs.go                           #   Run state machine; CallAction; cancel; mark free; notifications — R7/R14
│   ├── dispatch.go                       #   the dispatch pass (free × eligible, order of receipt) — R9
│   ├── scheduler.go                      #   timed events: single timer, phase-preserving, dedupe — R10
│   ├── links.go                          #   host links: dial/backoff, Subscribe folding, OrchestratorLink client, reconcile — R2/R6
│   ├── workers.go                        #   attach/detach/adopt; bundle; briefing text; reasoning sandbox lifecycle — R3/R11/R12
│   ├── tools.go                          #   the four tool semantics behind link `call` frames — R5
│   └── *_test.go                         #   fake clock, fake links, two-daemon tests
├── internal/worker/                      # NEW package: the worker side every daemon runs — R1
│   ├── controller.go                     #   AttachController CAS / DetachController / UpdateWorker on the sandbox record — R3
│   ├── gateway.go                        #   token mint/revoke; `mcp add/load/rm sxb-orch-…` argv; hide prefix — R4
│   ├── endpoint.go                       #   /orchestrator/w/{token}/mcp: go-sdk stateless handler, per-token servers, 4 tools — R5
│   ├── relay.go                          #   OrchestratorLink server side: hello index, call/result matching, linkCallTimeout — R6
│   ├── deliver.go                        #   DeliverWorkerPrompt: guards, ensure broadcaster, run frame, record run — R7
│   ├── briefing.go                       #   managed CLAUDE.md block via the shared managedblock helper — R12
│   └── *_test.go                         #   stub sbx, SDK client over httptest
├── internal/managedblock/                # NEW small package: marker-delimited block writer (extracted from escapehatch/rule.go)
├── internal/agent/
│   ├── pty.go                            # wait on the PTY child → EXITED via StatusUpdater; forget the session — R7
│   └── hub.go                            # Publish takes the mutex — R7
├── internal/sandbox/manager.go           # LaunchRequest.EmptyWorkspace; Refresh with zero sources; launch-attach load after create — R11/R4
├── internal/registry/registry.go         # + orchestrator buckets + helpers
├── internal/mcp/manager.go               # List hides `sxb-orch-` registrations — R4
├── internal/escapehatch/rule.go          # uses managedblock
├── internal/grpc/
│   ├── orchestrator_rpcs.go              # NEW: the 19 orchestrator-side handlers + status mapping
│   ├── worker_rpcs.go                    # NEW: the 5 worker-side handlers incl. the OrchestratorLink stream
│   ├── sandbox_rpcs.go                   # LaunchSandbox applies LaunchSandboxRequest.controller before the agent starts
│   └── server.go                         # wiring; Event.orchestrator_changed; emit hook clears runs on non-RUNNING
└── cmd/sxbd/main.go                      # mux route for the MCP endpoint; worker + orchestrator services; dial.Func

src/apps/switchboard-tui/
├── internal/client/
│   ├── client.go / ssh.go                # thin over switchboard-dial (Manager, HostEntry, askpass stay here)
│   └── orchestrator.go                   # NEW: typed wrappers for the 19 orchestrator-side RPCs + DetachController
├── internal/ui/
│   ├── orchestrators.go                  # NEW: screenOrchestrators — rows, create/rename/remove, machine picker
│   ├── orchestrator_views.go             # NEW: Workers / Actions / Queue / Events / Reasoning / Hosts views + keys
│   ├── orchestrator_forms.go             # NEW: action editor, call form (from declared arguments), timed-event form, mark-free dialog (`huh`)
│   ├── orchestrator_attach.go            # NEW: `o` attach picker (+ add-host offer), detach confirm, launch `O` sub-mode
│   ├── sandbox_list.go                   # hide/surface + stable partition; `O` tab; third line; `o`/`m` dispatch; shift cycle
│   ├── list_delegate.go                  # NEW: 3-line delegate (Spacing 0) — R13
│   ├── app.go                            # screen enum; header strip; per-host subscriptions; Event.orchestrator_changed; Daemon interface (+20)
│   ├── notifications.go                  # per-host streams + ack; new kinds' titles/icons
│   ├── launch.go                         # `O` orchestrator pick; LaunchSandboxRequest.controller from GetWorkerBundle
│   ├── daemons.go                        # ScreenNext → orchestrators; dynamic help labels
│   └── keys.go                           # + OrchestratorAttach (`o`), MarkFree (`m`), OrchestratedTab (`O`), view keys
└── cmd/sxb/main.go                       # unchanged wiring (dial moved underneath)

src/apps/switchboard-tui-e2e/harness.go   # second stub daemon; stub `ssh` that execs `sxbd dial-stdio`; `mcp add/load/rm` rows in sbx.log
src/services/switchboardd-e2e/            # orchestrator lifecycle against a real runtime (skips without sbx)
```

**Structure Decision**: Extend the daemon + TUI in place, like 003–008, with two new daemon
packages because the feature has two distinct surfaces on (potentially) two machines: the
orchestrator side (records, queue, scheduler, links — TUI-facing) and the worker side (controller
record, gateway registration, MCP endpoint, relay, delivery — sandbox-facing), which every daemon
must run even when it hosts no orchestrator. The SSH/stdio dialer becomes a library module because
both the TUI and the daemon need the same code and the repository-structure rule puts shared code
under `src/libs/` (`switchboard-update` is the precedent); copying it would fork the only bridge
to remote daemons. The marker-delimited block writer is extracted into a tiny package rather than
having `worker` import `escapehatch`. The proto revision is purely additive.

## Complexity Tracking

> No Constitution Check violations beyond the pre-recorded 001 Go-stack deviation (which this
> feature does not widen). No new entries required.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none for 009)_ | — | — |

## Risks carried into implementation

| Risk | Handling |
|---|---|
| **The gateway cannot reach a loopback `http://` registration**, or dials from inside the sandbox rather than the host (`sbx` absent in dev; R4 is documentation-derived). | Quickstart **Scenario 0** settles it first; `worker.gatewayHost` is one constant (`127.0.0.1` ↔ `host.docker.internal`, the latter already reachable and policy-allowed); `--skip-ssrf-check` only if the warning turns out to block. |
| The gateway's upstream client disagrees with a **stateless** streamable-HTTP server (sessions, GET streams, protocol version). | SDK stateful mode is a one-option change; the SDK's legacy SSE handler is the last resort; Scenario 0 step 3 checks tool listing from inside a sandbox. |
| **Impersonation between the developer's own sandboxes** via `mcp-add` of another worker's `sxb-orch-…` registration in dynamic mode. | Documented assumption (R4); opaque names hidden from the views; exclusive attach mode removes discovery; nothing stronger is available without runtime support. |
| An agent **never reports** completion, or reports before it is really done. | The review path (FR-156) is the designed fallback for the first; the second is the agent's call and the summary is recorded as given; the briefing + run frame + tool descriptions (R12) state the rule three ways. |
| **Prompt typed into a busy or wrong agent** (status races between the dispatch pass and a developer's keystroke). | `DeliverWorkerPrompt` re-checks state/status/controller/run under the owning daemon's write and refuses; a refused delivery returns the run to *queued* (R7). |
| **Unattended PTY stalls** on a full buffer during a long run. | Delivery ensures the terminal broadcaster (reader + ring) exists before typing (R7); the escape hatch inherits the fix. |
| **Lost SSH access** to a worker machine, or a daemon there too old for the worker-side RPCs. | Links retry with backoff and show *unreachable*; runs are not dispatched there; `UNIMPLEMENTED` is surfaced as "daemon on <host> is too old — update with `u`". |
| Duplicate inbox lines for a worker that **needs input** (owning daemon's `NEEDS_PROMPTING` + orchestrator's `RUN_NEEDS_INPUT`). | Accepted and documented (R14): suppressing the daemon's would blind the developer when the orchestrator is down; revisit with a TUI-side collapse if it annoys. |
| Per-host event streams change TUI behaviour for everyone (remote `sandbox_changed` now arrives). | It fixes a latent gap (remote rows never updated live); covered by `teatest` with two fake hosts; re-subscribe on drop is bounded by the existing reload path. |
| The **3-line list delegate** changes paging/height maths. | `Spacing() == 0` keeps the visual rhythm identical for two-line rows; `mainListHeight` uses the delegate's height; `teatest` snapshots pin it. |
| Older client + newer daemon or vice versa. | New RPCs answer `UNIMPLEMENTED` on an old daemon; an old client ignores field 23 and shows workers as ordinary rows (hidden only by new clients) — degraded, never broken. |
| Moving the dialer into a library touches every connection path. | Pure move with the tests; the TUI's `client` keeps its public surface; `make test` + e2e exercise both local and stub-SSH paths. |
