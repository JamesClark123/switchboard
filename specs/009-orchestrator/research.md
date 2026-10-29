# Phase 0 Research: Orchestrator

Decisions R1–R17 resolve every unknown in the plan's Technical Context. Three facts shape the
whole feature. First, the spec's clarifications (2026-10-14) fix the shape: an orchestrator is a
**role the machine's daemon runs in-process**, it **dials worker machines over SSH** like the TUI,
and webhooks are out. Second, the only way a sandbox's agent can reach anything is the daemon's
**agent-hook HTTP server** (`SWITCHBOARDD_HOOK_ADDR`, `0.0.0.0:8765`) and the host runtime's
**MCP gateway** (`sbx mcp add … --url`, `sbx mcp load`), both already in use by features 005/008
— so the orchestrator's tools are an MCP endpoint the *worker's own daemon* serves and relays.
Third, the daemon has **no client-side dialer** today (`grpc.NewClient` and the SSH bridge exist
only in the TUI's `internal/client`), so reaching another daemon means sharing that code.

As in 004–008, `sbx` is **not installed in the development environment**: the two new `sbx`
usages below (a loopback `--url` registration and `mcp load` of it) are documentation-derived and
reconciled first in quickstart Scenario 0 (R17).

## R1 — Hosting model: one daemon package, one runtime per orchestrator

**Decision**: A new daemon package `switchboardd/internal/orchestrator` holds everything
orchestrator-side: the persisted records (R9), the per-orchestrator **runtime** (scheduler,
dispatcher, reconciler, link manager — one goroutine set per orchestrator, started at `serve` for
every record and on create), the action/call validation and prompt composition (R8), and the
gRPC handlers' backing service. `cmd/sxbd/main.go` constructs `orchestrator.NewService(reg,
mgr, hub, dialer, …)` after the MCP manager and starts it before `Serve`. Nothing new is spawned:
no binary, no process, no port, no systemd unit; an orchestrator lives and dies with
`switchboardd` on its machine and restarts with it (FR-105).

A second, smaller package `switchboardd/internal/worker` holds the **worker side** that every
daemon runs for sandboxes *it* owns, whoever the controller is: the controller record on the
sandbox (R3), the per-worker gateway registration and MCP endpoint (R4/R5), the briefing writer
(R12), and the link acceptor (R6). The split matters because the two sides run on different
machines in the remote case and the worker side must work without any orchestrator code loaded.

**Rationale**: one package per distinct external surface is the 005/006/008 precedent; here there
are two surfaces (TUI-facing orchestrator management; sandbox-facing worker tools) on two
daemons. Identity (FR-148): `uuid.NewString()` at creation, the same generator sandboxes use.

**Alternatives considered**: a separate `sxb-orchestrator` binary — rejected in the spec (Key
Decision 11). A single package — rejected: the worker side would import the scheduler for no
reason, and the remote case makes the boundary real.

## R2 — Reaching worker daemons: a shared dial library and per-orchestrator host entries

**Decision**: Extract the TUI's transport primitives into a new library module
`src/libs/switchboard-dial` (package `dial`): `DialLocal`, `DialSSH`, `SSHCommand`,
`DialCommand`, the `stdioConn` adapter, `DialError`/`ClassifyDialStderr`, and the `Conn`
handshake (`GetDaemonInfo` → cached `DaemonInfo`). They import only the proto, gRPC and the
standard library today, so the move is mechanical; the TUI's `internal/client` keeps `Manager`,
`HostEntry`, the typed RPC wrappers and the askpass helper and calls `dial` underneath. The daemon
dials with **`BatchMode=yes` always** — keys or agent only, never a password (FR-106; a daemon
has no one to ask).

Each orchestrator persists its own **host entries** `{host_id, display_name, ssh_target,
ssh_options}` (the TUI's `KnownHost` shape minus the password, keyed by the remote daemon's
advertised `host_id`); the Orchestrators screen manages them and offers *copy from my hosts*
(FR-106). The runtime keeps one **host link** per entry: a `dial.Conn` to `ssh <target> sxbd
dial-stdio`, redialled with backoff (R15) while down, over which it holds one `Subscribe` stream
(status), one `OrchestratorLink` stream (R6) and makes unary worker-side calls (R3). The
orchestrator's own machine is a host link too, implemented in-process (`localHost`), so the
dispatcher never special-cases locality. A worker's `host_id` picks its link; a sandbox on a host
with no entry cannot be attached until one exists (US2 scenario 8).

**Rationale**: the spec fixed the direction (orchestrator dials out) and the mechanism (SSH, same
as the TUI). Sharing the code rather than copying it keeps `ClassifyDialStderr` and the stdio
bridge single-sourced; a library module is the `switchboard-update` precedent and what the
repository-structure rule means by `libs`.

**Alternatives considered**: copying `ssh.go` into the daemon — rejected (two divergent bridges).
Putting the dialer into `switchboard-proto` — rejected: that module is the contract, not code.
`golang.org/x/crypto/ssh` in-process — rejected: the TUI deliberately uses the user's `ssh` so
agents, config and known_hosts behave exactly as at the shell.

## R3 — The owning daemon holds the relationship: `Sandbox.controller` and five worker-side RPCs

**Decision**: `Sandbox` gains field 23 `SandboxController{orchestrator_id, name (last known),
host_id (the orchestrator's machine), attached_at, reasoning, run{id, action, needs_review}}`,
persisted in the sandbox record by its **owning daemon**. The one-controller rule (FR-108) is a
compare-and-set inside `registry.Update`: attach succeeds only when `controller == nil` or the
same identity; concurrent attempts serialize on bbolt's single writer, so exactly one wins.

Five additive RPCs form the **worker-side surface** every daemon exposes (called by an
orchestrator's daemon over its link, or in-process):

| RPC | Does |
|---|---|
| `AttachController{sandbox_id, controller, briefing}` | CAS the controller; register + load the per-worker gateway server (R4); write the briefing (R12); emit `sandbox_changed`. `ALREADY_EXISTS` names the current controller. Idempotent for the same controller (re-registers, rewrites the briefing). |
| `DetachController{sandbox_id, orchestrator_id, force}` | Clear the controller; `mcp rm` the registration; strip the briefing. Refused for a reasoning sandbox unless `force` (orchestrator removal). The **TUI may call this directly** when the orchestrator is unreachable (FR-112) — the orchestrator fails the run as "worker detached" when it reconciles (R6). |
| `UpdateWorker{sandbox_id, orchestrator_id, name?, run?, briefing?}` | Rename (last-known name), set/clear the current run and its `needs_review` flag, rewrite the briefing. Emits `sandbox_changed`, which is how the TUI hides, surfaces and labels workers without talking to the orchestrator (Key Decision 5). |
| `DeliverWorkerPrompt{sandbox_id, orchestrator_id, run_id, action, prompt}` | The guarded delivery of R7: refused unless RUNNING, agent IDLE, controller matches and no run is recorded; records `controller.run`; ensures the PTY is drained; types the prompt. |
| `OrchestratorLink(stream)` | The call relay of R6. |

An older daemon answers all five with `UNIMPLEMENTED`; attach reports "daemon on <host> is too
old" and the `u` fan-out (002) is the remedy. The TUI's everyday logic reads **only the sandbox
record**: hidden when `controller != nil`, surfaced when `agent.status == NEEDS_INPUT ||
controller.run.needs_review`, labelled with `controller.name`.

**Rationale**: Key Decision 5 verbatim — the record is where the race is settled, where the TUI
reads, and what survives the orchestrator's outage. `UpdateWorker` carrying `needs_review` is what
lets *awaiting review* surface a worker whose orchestrator the TUI may not even be connected to.

**Alternatives considered**: the orchestrator holding the roster alone — rejected (race and
outage). A `GetSandbox` RPC — not needed; `ListSandboxes` on a host is tens of records.

## R4 — Gateway exposure and attribution: one registration per worker, token in the URL

**Decision**: When a sandbox becomes a worker, its **owning daemon** (N) registers a remote MCP
server for *that sandbox only* and loads it:

```
sbx mcp add sxb-orch-<sandbox8> --url http://127.0.0.1:<hookport>/orchestrator/w/<token>/mcp --skip-auth
sbx mcp load sxb-orch-<sandbox8> --sandbox <ref>
```

`<token>` is 32 random bytes (hex) minted by N at attach and stored only by N, in a daemon-local
bbolt bucket keyed by sandbox id (`worker_token`, never on the wire to the TUI and never inside
the `Sandbox` record); the endpoint is a new route on the **existing
hook HTTP server** (`main.go`'s `ServeMux`, beside `/hook` and `/escape-hatch/run`), served by the
go-sdk's streamable-HTTP handler (R5) whose `getServer(req)` callback maps the token in the path
to that worker's identity — so every tool call is **attributed to exactly one sandbox** before any
handler runs (FR-144/FR-150), and an unknown or revoked token is a 404. Detach, destroy and
orchestrator removal run `sbx mcp rm sxb-orch-<sandbox8> --force` and revoke the token; the
orchestrator also refuses on identity (Key Decision 10). The registration name carries the
`sxb-orch-` prefix: `ListMcpServers` **hides** it (it is switchboard-internal, not a server the
developer registered), it is **not** appended to `Sandbox.mcp_servers` (the third line names the
orchestrator separately, FR-149), and the launch-attach path loads it after `create` exactly where
additive marks are loaded (before the agent PTY starts, FR-110). Loading works on static sandboxes
too (008 reconciled `mcp load` on `--static-mcp` sandboxes).

Why loopback: Docker's gateway documentation says header-bearing registrations "require the host
to connect to the server" and that `sbx` registers a private/loopback/link-local URL with a
warning (suppressible with `--skip-ssrf-check`) — i.e. the local gateway dials from the host. The
daemon tolerates the warning line rather than passing a flag that may postdate the baseline. If
Scenario 0 shows the gateway dialling from inside the sandbox instead, the URL becomes
`http://host.docker.internal:<port>/…` (already reachable and policy-allowed for `/hook`) — a
one-constant change (`worker.gatewayHost`).

**Known limit (documented assumption)**: the gateway presents no per-sandbox identity upstream, so
attribution is "the registration the call came through". An agent in a *dynamic-gateway* sandbox
on the same host could `mcp-add` another worker's `sxb-orch-…` registration by name and
impersonate it. Those are the developer's own sandboxes on their own machine; the names are
opaque, the Secrets/MCP views hide them, and *exclusive* attach mode (008) removes discovery
entirely. Stronger isolation would need runtime support.

**Alternatives considered**: a credential `--header` with a `${placeholder}` secret
(`mcp:<server>:<placeholder>`) — rejected: same impersonation exposure, plus a `sbx secret` row per
worker in the Secrets view and the runtime's "restart after changing a header secret" caveat. One
shared registration with the sandbox id passed as a tool argument — rejected: self-reported
identity (the `/hook` weakness) for calls that close runs. A separate port/listener — rejected:
the hook server already binds `0.0.0.0` and is already allowed by `sbx policy`.

## R5 — MCP server implementation: the official Go SDK, stateless streamable HTTP

**Decision**: Add `github.com/modelcontextprotocol/go-sdk v1.8.0` (`go 1.25`; transitively
`google/jsonschema-go`, `x/oauth2`, `x/time`) — the first new third-party dependency since 006,
justified because the client is a third-party gateway whose protocol version and session
behaviour switchboard cannot see. The worker side mounts `mcp.NewStreamableHTTPHandler(getServer,
&mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true})` at
`/orchestrator/w/{token}/mcp`: **stateless** so a gateway that never sends `DELETE` or that
reconnects with a stale `Mcp-Session-Id` is never answered 404 (every POST is self-contained;
server-initiated `tools/list_changed` is not needed because the spec requires list-time accuracy,
US6 scenario 7); localhost protection off because the server legitimately answers on a
non-loopback interface for `host.docker.internal` callers. `getServer` returns one cached
`*mcp.Server` per live token (tools closed over the worker identity), nil for unknown tokens.

Four tools, JSON in/out (schemas generated from Go structs by `mcp.AddTool`), named so the
briefing can refer to them verbatim (FR-151) — the full contract is
[contracts/orchestrator-mcp.md](./contracts/orchestrator-mcp.md):

| Tool | Input | Output |
|---|---|---|
| `orchestrator_list_actions` | — | actions: name, description, type, arguments (name, description, required, default), worker_tag |
| `orchestrator_call_action` | `action`, `arguments{}` | `run_id`, `position` — or the refusal reason |
| `orchestrator_check_run` | `run_id` | state, outcome reason, summary |
| `orchestrator_report_completion` | `summary` | ok — or "no run in flight on this sandbox" |

The server's `instructions` field carries the briefing summary (R12) so even an agent that ignores
`CLAUDE.md` is told what the tools are for when it lists them.

**Alternatives considered**: hand-rolled JSON-RPC over `net/http` — rejected: version
negotiation, SSE framing and batching rules are where a 300-line implementation drifts from a
real client. `mark3labs/mcp-go` — rejected: unofficial. The SDK's **stateful** mode — rejected
for the session-expiry reason above.

## R6 — The orchestrator link: a bidi stream the orchestrator opens, relaying tool calls

**Decision**: `rpc OrchestratorLink(stream OrchestratorLinkFrame) returns (stream
OrchestratorLinkFrame)` on every daemon; the **orchestrator's daemon is the gRPC client** and
opens one link per host entry (local included, in-process). Frames: `hello{orchestrator_id, name}`
(first frame, M→N; N indexes the link by orchestrator id), `call{id, sandbox_id, tool, args_json}`
(N→M: a worker's gateway call, attributed by N per R4), `result{id, ok, body_json, error}` (M→N),
`ping{}` both ways every 30 s. N's tool handler blocks at most `linkCallTimeout` (10 s) for the
matching `result`; with the link down it returns the MCP error *"orchestrator <name> is
unreachable; retry — a failed report does not end the run"* (the briefing says the same). Status
flows the other way on the host link's **`Subscribe`** stream (existing RPC): `sandbox_changed`
(agent status, state, controller) and `removed`, filtered to this orchestrator's workers.

On every (re)connect the runtime **reconciles** the host: `ListSandboxes`; adopt records whose
`controller.orchestrator_id` is this orchestrator (self-healing after a launch-attach or a lost
record); drop workers whose record no longer carries it (fail any run on them as *worker
detached*); push `UpdateWorker{name, briefing}` to every worker so renames and action changes
land; re-check every in-flight run per FR-134 within `reconcileTimeout`. Links retry with backoff
(R15); the Workers view shows *unreachable* for a host whose link is down, and runs are never
dispatched there (edge case list).

**Rationale**: gRPC streams are client-initiated and the spec forbids a path from the worker's
machine back to the orchestrator's; a bidi stream on the connection the orchestrator already holds
is the 006 `ForwardPort` pattern. Reusing `Subscribe` for status keeps the link to one job.

**Alternatives considered**: polling N for pending calls — rejected (latency, SC-013 ≤ 2 s).
N opening a connection to M — rejected by the spec (Key Decision 18).

## R7 — Free, delivery, status tracking, review (and two daemon gaps it closes)

**Decision**: *Free* (FR-117) is computed on the orchestrator from N's record: `state == RUNNING
&& agent.status == IDLE && controller.run == nil`. Hooks fire for developer-typed prompts too, so
`WORKING`/`NEEDS_INPUT` from any cause makes the worker busy (FR-119) with no extra signal.

> **Since SWI-17** (`specs/swi-17-feature-action-pre-post-prompts`): a run with steps (an action
> sequence) is delivered **per step** with `DeliverWorkerStep`, one step at a time, each after the
> previous one finished by evidence; only runs without steps still use `DeliverWorkerPrompt` below.

**Delivery** is `DeliverWorkerPrompt` (R3) on the owning daemon, which: re-checks the three
conditions under the registry write (refusing with `FAILED_PRECONDITION` if a developer got
there first — the run goes back to *queued*), **ensures the terminal broadcaster exists**
(`terms.Broadcaster(id)`) so the PTY is drained into the scrollback ring even with no client
attached — today a PTY created by `PromptAgent` alone has **no reader** and a long unattended
turn would stall on a full buffer; the escape hatch shares the exposure and inherits the fix —
then types the composed prompt wrapped in the fixed run frame (R12) through `agent.Registry.Prompt`
(the developer's own path: the body as one bracketed paste `ESC[200~ … ESC[201~`, 50 ms, then `\r`
as a separate write — the paste terminator is what keeps the Enter a distinct keystroke when the
`sbx exec` transport delivers both writes in one read; a bare body + `\r` in one read leaves the
prompt sitting unsent in the agent's input box). The submit is then **verified, not assumed**: an
idle agent that received a prompt reports `WORKING` through its `UserPromptSubmit` hook within a
moment, so while the agent is still recorded idle with no hook event 2.5 s after the Enter, the
registry presses Enter again — once more as `\r`, then as the keypad Enter `ESC O M`, which the
agent maps to the same Return key and which a transport that rewrites carriage returns leaves alone
(a line feed only inserts a newline) — up to three times, logging each retry (seen on macOS hosts:
the paste landed, the Enter typed through `sbx exec` did not submit, the developer's own Enter
did). An extra Enter on an agent that did submit is a no-op. The run is recorded
(`controller.run{id, action}`) on acceptance and the daemon emits
`sandbox_changed`. The run is *running* on acceptance.

**Tracking**: the runtime folds N's `sandbox_changed` events per worker: `NEEDS_INPUT` →
*awaiting input* (+ `RUN_NEEDS_INPUT` notification); `WORKING` → *running*; `IDLE` with no report
recorded → start `reviewGrace` (10 s, for a report still in flight); if nothing arrives → *awaiting
review*: `UpdateWorker{run.needs_review: true}` + `RUN_AWAITING_REVIEW` notification; `EXITED`,
`STOPPED`/`ERROR`/`DESTROYING`, or `removed` → *failed* with that reason (FR-124; destroyed while
awaiting review also fails it; stopped while awaiting review keeps it; the worker stays
attached). A **completion report** (R5) from
the sandbox the run is on → *completed* with the summary, `UpdateWorker{run: nil}`; the worker is
free once `IDLE`. A second report, or one from a sandbox with no run, is refused (one summary per
run). **Mark free** (`MarkWorkerFree{failed?, note?}`) closes an *awaiting review* run as
completed (default) or failed with `closed_by_developer = true` and the note as summary, then
`UpdateWorker{run: nil}`. On a worker with **no run** whose agent is recorded `NEEDS_INPUT` it is
the developer's override instead: `UpdateWorker{clear_needs_input: true}` sets the agent back to
`IDLE` on its owning daemon and an empty run is returned; with a run in flight the wait belongs to
the run (*awaiting input*) and the call is refused.

**Idle is free.** Claude Code's `Notification` hook fires for several `notification_type`s;
`idle_prompt` ("Claude is waiting for your input", after a minute idle with nothing on screen) is
not a request for input. The daemon's original mapping (every Notification → `NEEDS_INPUT`) parked
every worker a minute after its run finished, so no queued run could ever reach it and mark free
had nothing to close. The hook command now forwards Claude Code's hook input (the query names the
event and sandbox; the body is the stdin JSON) and the daemon maps `idle_prompt` → `IDLE` (a
developer's own sandbox still gets the needs-prompting nudge; a controlled one is silently
between runs), `auth_success` → nothing, and `permission_prompt` / `elicitation_dialog` / anything
else → `NEEDS_INPUT`. A sandbox still running the original hook command forwards no type: a
Notification that arrives while the agent is recorded `IDLE` can only be the idle nudge (prompts
for the developer happen mid-turn, after `WORKING`), so it maps to `IDLE` too. The daemon rewrites
every live workspace's hook settings on start so the new command reaches older sandboxes when
their agent restarts (a running Claude Code keeps the hooks it started with).

Two gaps in `internal/agent` are closed because the feature depends on them: (1)
`AGENT_STATUS_EXITED` is **never set today** — the registry now waits on the PTY child and, on
exit, reports `EXITED` through the existing `StatusUpdater` and forgets the session (the next
prompt or attach starts a fresh agent); (2) `Hub.Publish` fans out **without the hub mutex** unlike
its siblings — fixed in passing since the orchestrator publishes through it continuously.

**Alternatives considered**: inferring completion from idleness — rejected by the spec (Key
Decision 15). Reusing `PromptAgent` for delivery — rejected: it checks nothing (state, status,
controller) and cannot record the run.

## R8 — Actions: placeholder form, composition, validation, limits

**Decision**: Placeholders are **`{name}`** where `name` matches `[A-Za-z_][A-Za-z0-9_]*`
(the spec's own examples); a literal brace is written `{{`. Composition (FR-139): replace every
occurrence of each declared placeholder with the value (an optional argument with no value and no
default → empty); then, for each **supplied** argument whose name appears in no placeholder, in
declaration order, append `\n\n<name>: <value>` (a multi-line value goes on the lines after
`<name>:`). Validation at save (FR-121/FR-136/FR-137/FR-138): name unique within the orchestrator and
`[a-z0-9][a-z0-9-]*` ≤ 64; description any; body non-empty; every placeholder names a declared
argument; argument names unique; `type` ∈ {`prompt`, `reason`}; `worker_tag` only on `prompt`.
Validation at call (FR-140): required present (a default counts), no undeclared name, each value ≤
`maxArgumentBytes` (16 KiB), composed prompt ≤ `maxPromptBytes` (64 KiB). Composition happens at
call time and the result is frozen on the run (Key Decision 2). The prompt path already carries
1 MiB escape-hatch results, so 64 KiB is comfortably inside what the agent accepts.

Action **edits are a replace**: `PutAction` with the same name overwrites; deleting an action
disables the timed events that name it (FR-130) and leaves queued runs untouched.

## R9 — Queue and runs: bbolt layout, dispatch order, retention

**Decision**: Two new buckets in `registry.db`: `orchestrators` (key = id, value = proto
`Orchestrator`: name, host, host entries, actions, timed events, reasoning setup and sandbox id,
worker roster — small and edited rarely, so one record) and `orchestrator_runs` (key =
`<orchestrator_id>/<receipt seq, 20-digit zero-padded>`, value = proto `Run`), so a prefix scan
yields runs in **order of receipt** with no in-memory index to rebuild — the durable queue of
FR-120. Run ids are
`run-<orchestrator8>-<seq>`.

**Dispatch** (FR-123, SC-003) runs on every trigger (new run, worker became free, link up, run
closed): walk queued runs in key order; for each, take the first **free, eligible** sandbox not yet
claimed in this pass (`prompt`: a worker whose `tag` equals `worker_tag` when set; `reason`: the
reasoning sandbox) and deliver; a run with no free eligible sandbox is skipped for this pass and
keeps its place. This gives order-of-receipt without head-of-line blocking (Key Decision 8). A
refused delivery (R7) returns the run to *queued* and the pass continues. `CancelRun` is accepted
only in *queued* (FR-126). **Retention** (FR-127): the newest `historyKeep = 200` finished runs
per orchestrator are kept; older ones are deleted when a run closes; queued and in-flight runs are
never trimmed. Queue position is derived at read time.

**Alternatives considered**: a separate ordering bucket — unnecessary with the zero-padded key.
Age-based retention — rejected for determinism in tests; the count is a package constant.

## R10 — Timed events: a per-orchestrator timer, phase-preserving, deduped by default

**Decision**: Timed events live on the orchestrator record (`id`, `name`, `action`, fixed
`arguments`, `interval_seconds ≥ 60`, `enabled`, `dedupe_disabled` — stored negated so an absent
field reads as *dedupe on*, the proto3-default trick — `next_fire_at`, `last_fired_at`,
`last_skipped_at`, `skipped_count`, `disabled_reason`). The runtime keeps one `time.Timer` armed
for the earliest enabled `next_fire_at`. A firing calls the action exactly like a user event
(event kind `TIMED`, origin = event id), then advances `next_fire_at += interval`. With dedupe on,
a firing is **skipped** when any run with this event as origin is *queued* or in flight
(`running`, `awaiting input`, `awaiting review`), recording the skip (FR-157). After a restart the
next firing is the first slot `last_fired_at + k·interval > now` — no catch-up, phase kept (US5
scenario 3). Disabling (by hand, or because the action was deleted) stops the timer for that event.

## R11 — Reasoning sandbox: an empty-workspace launch, a persisted setup, rebuild by replace

**Decision**: `sandbox.LaunchRequest` gains `EmptyWorkspace bool`: with it, zero sources are
accepted, the workspace directory is created empty, and the record carries no sources (the three
"at least one source" guards in `Launch`, `duplicate.CopyAll` and `Refresh` learn the flag;
`RemoveSources`' last-source rule is untouched). The reasoning sandbox is launched that way on the
orchestrator's machine right after `CreateOrchestrator` returns (asynchronously, progress visible
in the Reasoning view; `reason` runs wait until it exists), named `reasoning-<orchestrator8>` so an
orchestrator rename never renames a sandbox (`Rename` destroys the container), with
`controller{…, reasoning: true}` and the default agent (FR-147). The **setup** (`ReasoningSetup
{sources, config, kits, mcp_servers}`, the launch wizard's own fields) is stored on the
orchestrator; saving a changed setup or pressing *rebuild* is **destroy + launch** with it,
streamed as `LaunchProgress`, refused with `FAILED_PRECONDITION` while a run is on the sandbox
(FR-146). Removal of the orchestrator destroys it; `DetachController` refuses it without `force`.

**Alternatives considered**: a `Refresh` on the same container — rejected: the setup changes the
container (kits, static MCP), which only `create` honours.

## R12 — Briefing carrier: a managed `CLAUDE.md` block plus the run frame plus tool metadata

**Decision**: Three carriers, all generated from one template (`contracts/orchestrator-mcp.md`
§Briefing) and one data set (orchestrator name, sandbox role, current actions):

1. A **managed block** in the workspace `CLAUDE.md` delimited by
   `<!-- switchboard:orchestrator:begin -->` / `…:end -->`, written by the owning daemon at
   attach and launch-attach (before the agent starts), rewritten on `UpdateWorker{briefing}`
   (rename, action changes, pushed by the orchestrator per R6), removed at detach. The
   escape-hatch rule-block writer (`escapehatch/rule.go`) is generalized into a shared
   `managedblock` helper both features use. Claude Code reads `CLAUDE.md` at session start, so a
   live session sees it next session.
2. The **run frame**: every delivered prompt is `[switchboard orchestrator "<name>" · run <id> ·
   action "<action>"]\n\n<composed prompt>\n\n[When this run is finished, call
   orchestrator_report_completion with a short summary. If the call fails, retry; a failed report
   does not end the run.]` — so the essential instruction reaches a live session on the very first
   orchestrated prompt (FR-152). The recorded `Run.prompt` is the composed prompt; the frame is a
   documented constant.
3. The MCP server's `instructions` and each tool's description restate role, workflow per action
   type and the completion rule, for agents that read tool metadata.

**Rationale**: the spec requires content and timing, not a mechanism; `CLAUDE.md` injection is
the proven 005 carrier, and the run frame closes the live-session gap it leaves.

## R13 — TUI: per-host event streams, a third screen, keys, the hidden tab, the third line

**Decision**:

- **Per-host subscriptions.** Today the TUI subscribes to the *local* daemon only; remote
  `sandbox_changed` events never arrive and a dropped stream is never re-opened. Workers and
  orchestrators live on any host, so `subscribeCmd` becomes per connected host (`eventMsg{hostID,
  ev}`), re-subscribed on reconnect, with notifications acked on their originating host. This is
  what makes SC-001's 5-second surfacing and FR-131's live Queue view true for remote hosts.
- **Screens.** `screenOrchestrators` joins the shift cycle: list → daemons → orchestrators, no
  wrap; the `ScreenNext`/`ScreenPrev` help labels become dynamic ("daemons" / "orchestrators" /
  "sandboxes"). The list screen: `n` create (name + machine picker over connected hosts), `R`
  rename, `d` remove (confirm names the consequences), `enter` opens an orchestrator whose body is
  tabbed **Workers / Actions / Queue / Events / Reasoning / Hosts** (`left`/`right`); per view:
  Workers `x` detach, `m` mark free; Actions `a`/`e`/`d`, `enter` = call (a `huh` form built from
  the declared arguments, FR-129); Queue `enter` shows the prompt, `x` cancels a queued run, `m`
  marks free; Events `a`/`e`/`d`, `space` enable/disable; Reasoning `e` edit setup (reuses the
  launch browser, kit picker and config picker), `b` rebuild; Hosts `a` add (pick from the TUI's
  hosts), `d` remove. Forms follow the 008 `huh` pattern (heap-allocated vals, `ctrl+s` saves).
- **Header strip.** `header()` renders `⚡ Switchboard  Sandboxes · Daemons · Orchestrators
  (shift+←/→)` on its existing line with the current screen highlighted, so `bodyHeight()`'s
  two-line assumption and every overlay stay as they are (FR-143).
- **Sandbox list.** `o` = attach (picker over every orchestrator the TUI knows; offers to add the
  sandbox's host to the orchestrator first when missing) or, on a worker, detach (confirm); `m` =
  mark free, offered while `controller.run.needs_review` (the outcome dialog) or when the worker
  has no run and its agent is `NEEDS_INPUT` (freed outright, no dialog). **`O`** toggles the
  **Orchestrator Sandboxes** tab: a `tabOrchestrated` kind with its own `showOrchTab` flag that
  survives `rebuildTabs()` and is cleared by `switchTab`; it lists every sandbox with a controller
  across hosts, the reasoning ones marked. `currentTabRows()` hides controlled sandboxes on All /
  host / group tabs unless they need the developer, and **stable-partitions** every tab's rows so
  those that need the developer come first (FR-114). Launch wizard: `O` opens an orchestrator
  pick sub-mode; the request carries the bundle from `GetWorkerBundle` as
  `LaunchSandboxRequest.controller` (FR-110).
- **Third line.** The bubbles default delegate is fixed at two lines, so the list gets a custom
  delegate with `Height() == 3` and `Spacing() == 0`: rows with MCP servers or a controller use the
  third line (`mcp: a, b · orchestrator: review-bot`, truncated with `…`, never wrapped); rows
  with neither leave it blank, which is exactly today's one-line gap (FR-149). The 008 `mcp:`
  badge moves from line 2 to line 3.

## R14 — Notifications and events

**Decision**: Three `NotificationKind`s raised by the **orchestrator's** daemon, each carrying the
worker's `sandbox_id` and the worker's `host_id` so the inbox's `enter` lands on the row:
`RUN_FAILED` (6), `RUN_AWAITING_REVIEW` (7), `RUN_NEEDS_INPUT` (8); messages name the
orchestrator, the action and the reason (FR-133). Reported completions are silent. The owning
daemon's existing `NEEDS_PROMPTING` still fires for a worker (it is the daemon's own signal and
keeps the outage case covered), so a worker that asks for input yields two inbox lines — accepted
and documented rather than suppressed, because suppressing the daemon's would blind the developer
whenever the orchestrator is down. One additive `Event.orchestrator_changed{host_id,
orchestrator_id}` arm after any orchestrator mutation or run transition; the TUI re-fetches the
summary and whichever view is open (the 008 `mcp_gateway_changed` pattern).

## R15 — Constants (package-level; no new environment variables)

| Constant | Value | Where |
|---|---|---|
| `linkDialTimeout` | 30 s | per SSH dial attempt (matches `signInTimeout`) |
| `linkRetryMin` / `linkRetryMax` | 15 s / 2 min | backoff while a host link is down |
| `linkCallTimeout` | 10 s | worker tool call waiting for the orchestrator's result |
| `deliverTimeout` | 15 s | `DeliverWorkerPrompt` end to end |
| `reviewGrace` | 10 s | idle without a report before *awaiting review* |
| `reconcileTimeout` | 30 s | in-flight reconciliation after a link comes up (SC-008) |
| `minIntervalSeconds` | 60 | timed events |
| `historyKeep` | 200 | finished runs retained per orchestrator |
| `maxArgumentBytes` / `maxPromptBytes` | 16 KiB / 64 KiB | call validation |
| `workerTokenBytes` | 32 | per-worker URL token |
| `gatewayHost` | `127.0.0.1` | host in the loopback `--url` (R4; reconciled in Scenario 0) |

## R16 — Testing without `sbx` or SSH

**Decision**: Daemon — stub `sbx` scripts (the `loggingSbx`/`scriptedSbx` patterns) pin the two
new argv rows; the **two-daemon** path runs in one test process: two `startServer` instances on
two Unix sockets with the orchestrator's `dial.Func` injected to connect to the second socket, so
attach, link, delivery, status folding, reconcile and detach are exercised over real gRPC without
SSH; the MCP endpoint is tested with the SDK's own client (`mcp.NewClient` +
`StreamableClientTransport`) against `httptest`; scheduler and dispatcher use an injected clock and
table-driven order assertions (SC-003 as a property over random call sequences). TUI — `teatest`
for the screen cycle, header strip, hidden tab, ordering, third line, attach/detach/mark-free
flows and the forms, with `fakeDaemon` gaining the new methods. E2E — the TUI harness starts **two
stub daemons** and puts a stub `ssh` on `PATH` that ignores its arguments and `exec`s the second
daemon's `sxbd dial-stdio`, so the remote path runs end to end on any machine.

## R17 — Surface reconciliation items (quickstart Scenario 0)

| Item | Why it is documentation-derived | Fallback |
|---|---|---|
| `sbx mcp add <n> --url http://127.0.0.1:<port>/… --skip-auth` is accepted and the gateway reaches a loopback `http://` URL | docs show only `https://` examples; loopback only mentioned via the SSRF warning | `host.docker.internal` (one constant); or `--skip-ssrf-check` if the warning turns out to block |
| The gateway speaks **streamable HTTP** to a remote `--url` server and tolerates a stateless server | docs say "remote HTTP endpoint" without naming the transport | SDK stateful mode; legacy SSE handler as last resort |
| `sbx mcp load` of an `http://` loopback server into a running sandbox delivers the tools live | confirmed for Notion over HTTPS (008) | restart-free path lost ⇒ document; attach still records the controller |
| `sbx mcp ls` lists `sxb-orch-…` rows the daemon must hide | — | filter by prefix (already planned) |
| A `claude` agent calls `orchestrator_report_completion` unprompted after a run with the briefing in place | model behaviour, not CLI | the review path (FR-156) is the designed fallback |
