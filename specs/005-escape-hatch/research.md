# Research: Escape Hatch

**Feature**: `005-escape-hatch` | **Date**: 2026-07-23 | **Spec**: [spec.md](./spec.md)

This feature adds no new module or language: it is Go, in the existing `switchboardd` daemon +
`switchboard-tui` client, building on the agent-hook channel (feature 003) and agent kits
(feature 004). The research below resolves the open design questions the spec deliberately left to
planning. There are **no `NEEDS CLARIFICATION` markers** left — the four spec clarifications
(name-collision override, in-memory run records, 30-min default duration, 5-min approval window)
plus the decisions here fully constrain the design.

---

## R1 — How the in-sandbox AI reaches the host, and how the result gets back

**Decision**: Reuse the existing **agent-hook HTTP channel**. The AI invokes an escape-hatch command
by running a daemon-injected **wrapper** that does a short `POST` to a **new HTTP endpoint on the
existing hook server** (`host.docker.internal:<SWITCHBOARDD_HOOK_ADDR port>`, default `8765`). The
call is **async**: the daemon validates the invocation, enqueues a run, and returns immediately. When
the run reaches a terminal outcome the daemon **pushes the result back into the agent** by injecting a
prompt into the sandbox's PTY via the existing `agent.Registry.Prompt` path (the same mechanism
`PromptAgent` uses to deliver a prompt to a detached agent).

**Why**:
- The **only** channel from inside a sandbox to the host is the hook server the daemon already stands
  up at `cmd/sxbd/main.go:176,182` (`http://host.docker.internal:<port>/hook`) and already reaches via
  fire-and-forget `curl` in `agent.BuildSettings` (`internal/agent/hooks.go:55-81`). Adding a sibling
  route (`/escape-hatch/run`) on the same `mux` is a minimal, proven extension — `curl` is already
  present in the sandbox image (the status hooks depend on it).
- The **only** daemon→agent push path is `agent.Registry.Prompt` → `Session.Write(text+"\n")` on the
  single per-sandbox PTY master (`internal/agent/pty.go:53-61`). This writes into the running agent as
  if a user typed it, and — critically — **works with no terminal attached** and **survives detach**
  (the docker-exec agent child persists independently of any client attachment; verified by the
  feature-003 T002 spike, `internal/agent/pty.go:100-105`). This is exactly the spec's "trigger a
  callback to the ai to let it know the command has finished" (FR-040, SC-005).

**Alternatives considered**:
- *Blocking invocation (the wrapper holds the HTTP connection open until the run finishes, printing the
  output as its own stdout).* Rejected: the invoking agent (Claude Code) runs the wrapper through its
  Bash tool, which caps individual command duration (single-digit-to-ten minutes). A 30-minute
  `test:e2e` run (the motivating case) would exceed that tool timeout and the agent would lose the
  result even though the host command completed — breaking SC-005. Async + PTY-injected callback has no
  such ceiling.
- *A new gRPC method the sandbox calls directly.* Rejected: the sandbox has no gRPC path to the daemon
  (the gRPC socket is a host-side Unix socket, reached by clients over SSH `dial-stdio`, never from
  inside a container). The HTTP hook server is the sanctioned sandbox→daemon surface.
- *Expose each command as a Claude Code slash-command or MCP tool.* Rejected for v1 as heavier than a
  one-line wrapper and redundant with the injected rule; the wrapper + rule already make each command a
  "discrete command the agent can invoke" (FR-037).

**Consequence**: The wrapper returns quickly, so the agent's rule (R3) must tell it the command runs
asynchronously and that it should await the follow-up result message before proceeding.

---

## R2 — Where escape-hatch commands live in the data model (NOT in the opaque kit YAML)

**Decision**: Escape-hatch commands are a **structured, switchboard-owned** part of a client-authored
kit — a new `EscapeHatchCommand` proto message carried in `KitSpec` — **not** a section of the Docker
`spec.yaml`. They never enter the opaque `spec_yaml` blob the daemon materializes for `sbx`.

**Why**:
- Feature 004 deliberately keeps `KitSpec.spec_yaml` **opaque** and lets the host `sbx kit validate` be
  the schema authority, because Docker's kit schema is experimental (`switchboard.proto:185-195`,
  004 spec Key Decision 3). Escape hatch is a **switchboard** concept `sbx` knows nothing about;
  injecting an `escapeHatch:` key into `spec.yaml` would risk `sbx kit validate` rejecting or dropping
  it, and would make the daemon parse YAML it currently treats as bytes.
- The daemon **needs the commands structured**, not opaque: it must enforce the allowlist, resolve
  name-collisions across kits, run the exact string, and inject the rule. Structured proto gives it all
  of that with proto's unknown-field forward-compat and the bbolt registry's existing
  `pb.Sandbox`-marshaling (no migration — same pattern 004 used for `Sandbox.kits`).

**Validation split** (FR-035 "validated alongside the rest of the kit"): the Docker portion is
validated by `sbx kit validate` exactly as today; the **escape-hatch entries are validated
client-side** in the editor (non-empty name + command, name unique *within the kit*, valid consent
mode, non-negative duration, workspace-relative working dir). External-source kits (`KitRef.source`)
are opaque to us and therefore **carry no escape-hatch commands** — only client-authored `KitSpec`
kits can declare them.

**Alternatives considered**: a namespaced `x-switchboard-escape-hatch:` block inside `spec.yaml`
(rejected — still rides the opaque blob and depends on `sbx` tolerating unknown keys); a separate
top-level kit artifact (rejected — over-engineered; the commands belong to the kit and travel with it).

---

## R3 — Turning commands into an agent-invokable surface + injecting the rule

**Decision**: On every sandbox bring-up that (re)establishes the workspace — `Launch`, `Refresh`, and
`AddSandboxKit` — the daemon injects two things into the bind-mounted workspace, exactly as it already
injects `.claude/settings.local.json` and `.switchboard/session.json`:

1. **A single wrapper** at `<workspace>/.switchboard/escape-hatch` (executable), embedding this
   sandbox's id and the callback base URL (mirroring how `BuildSettings` embeds the sandbox id +
   callback URL). It takes one argument — the command **name** — and POSTs `{sandbox_id, name}` to
   `/escape-hatch/run`. It sends **only the name**; it can never send a command string.
2. **A managed rule block** delimited by `<!-- switchboard:escape-hatch:begin -->` /
   `:end` markers, written into the workspace root `CLAUDE.md` (created if absent, block replaced
   in place if present). The block enumerates the sandbox's **resolved** commands — each with its
   name, when-to-use note, consent mode, the exact wrapper invocation, and the statement that it runs
   on the host outside the sandbox and completes asynchronously (await the result message). When the
   resolved set is empty, the block (and wrapper) are **removed**, satisfying FR-037's "no rule when
   none attached".

**Why the daemon generates the rule (not the kit's `agentContext`)**: `agentContext` is per-kit static
text rendered client-side into `spec.yaml` and appended to agent memory by `sbx`
(`internal/store/kit.go:44`). The escape-hatch rule must reflect the **resolved per-sandbox set** —
the union of all attached kits' commands with later-kit-wins override (clarification Q1) — and must
embed the concrete wrapper path + this sandbox's id. Only the daemon, at bring-up, knows that resolved
set. This is the same reason the daemon (not the kit) owns hook injection. The bring-up injector hook
already exists (`Manager.SetHookInjector`, `cmd/sxbd/main.go:177`; called from `Launch`/`Refresh`,
`internal/sandbox/manager.go`); it is extended to also lay down the wrapper + rule block.

**CLAUDE.md managed-block approach**: Claude Code auto-loads workspace `CLAUDE.md`; a marker-delimited
managed block is the least-surprising, idempotent injection (the repo's own tooling uses the same
`<!-- SPECKIT START/END -->` pattern). Re-injection replaces only the block, leaving the user's own
`CLAUDE.md` content intact; refresh (which wipes the workspace) re-creates it, mirroring how the marker
and hooks are re-injected today (`internal/sandbox/manager.go:426-429`).

---

## R4 — Host execution: exec, working directory, timeout, output bound, cancellation

**Decision**: Add a new daemon package `internal/escapehatch` with an **executor** that runs the exact
command with `exec.CommandContext(ctx, "/bin/sh", "-c", command)`, `cmd.Dir` set to the sandbox's
`workspace_path` (joined with the optional command-relative `working_dir`), streaming stdout+stderr via
`StdoutPipe`/`StderrPipe` + `cmd.Start()` + a scanner goroutine into a **bounded** capture buffer, then
`cmd.Wait()`. Bounds:
- **Timeout**: `context.WithTimeout` using the command's `max_duration_seconds`, or the package constant
  **`defaultMaxDuration = 30 * time.Minute`** when unset (clarification Q3). On expiry the process group
  is killed and the run is reported `TIMED_OUT`.
- **Output cap**: capture is capped at **`maxCapturedOutput = 1 MiB`**; on overflow the buffer keeps the
  head, stops appending, and the run's output is marked truncated (the full output remains on the host
  where the command ran, per the spec's "retrievable where the command ran").
- **Cancellation**: the executor keeps a `map[sandboxID][]cancelFunc` under a mutex. It is drained/
  cancelled from the existing non-RUNNING teardown hook in `internal/grpc/server.go:102-118` (the same
  place PTYs + terminal broadcasters are torn down when a sandbox leaves RUNNING), so a `Stop`/`Destroy`/
  `Refresh` cancels in-flight runs and kills their host processes — no orphans (FR-041, SC-008).

**Why**: The daemon **always runs locally on the sandbox's host** (it serves only a Unix socket; SSH is
a pure client-side `dial-stdio` concern — `internal/grpc/server.go:133-152`, TUI `client/ssh.go`).
So "run on the supervising daemon's host" == "run in the daemon process", and `workspace_path` is
directly on the daemon's filesystem and **bind-mounted into the container at the same path**
(bidirectional; `internal/agent/pty.go:100-105`, `internal/sandbox/manager.go:395`). Host edits (e.g.
`pnpm install` writing `node_modules/`) are therefore immediately visible inside the sandbox (FR-038).
The existing sbx wrapper buffers with `CombinedOutput` and sets no `cmd.Dir`
(`internal/sandbox/runner.go:84-96`); the escape-hatch executor is a new, purpose-built streamer
(the PTY path in `internal/agent/pty.go` is the closest precedent for a live, killable host child).

**Launch failure** (command not found / not executable): surfaced as a `FAILED` run with the exec error
as diagnostics, never silently dropped (FR-041).

**Shell**: `/bin/sh -c "<exact string>"` runs the whole authored command (allowing pipes/`&&` the author
wrote) while the agent still supplies **nothing** — the string is fixed and looked up by name. It is
**not** a general shell for the agent (SC-004): the endpoint accepts a name, resolves it against the
persisted allowlist, and runs only that stored string.

---

## R5 — Consent gating and the approval round-trip

**Decision**: The executor is the consent gate. For an `AUTO_RUN` command it starts immediately. For a
`REQUIRES_APPROVAL` command it creates the run in state `PENDING_APPROVAL`, emits an approval-request
(an `Event` + a `NEEDS_APPROVAL` notification) to subscribed clients, and **blocks the run** on a
per-run decision channel with a **5-minute** timeout (`approvalWindow = 5 * time.Minute`,
clarification Q4). The client shows an **approval modal** (new `screenApproval`, built on the
`confirm.go` pattern) naming the sandbox, the exact command, and that it runs on the host; the user's
choice is sent by a new unary RPC **`DecideEscapeHatchRun(run_id, approved)`**. Approve → the run
proceeds to execution; deny **or** window-elapse → the run is `DENIED` and the agent is told it was
declined.

**Deny-by-default (SC-003, FR-039)**: the decision channel defaults to *deny* — anything other than an
explicit approve (an explicit deny, a 5-minute timeout with no answer, or a lost/duplicate decision)
resolves the run to `DENIED` with **zero host execution**. The approval modal reuses `confirm.go`'s
"unrecognized key is not consent" fallthrough (`internal/ui/confirm.go:43-63`) so no stray keystroke
can approve.

**Why gate in the executor, not the client**: the security boundary must hold even if a client is
malicious, buggy, or absent. The daemon owns the run lifecycle; the client's `Decide…` RPC is only an
input to the daemon's gate. If no client is connected to answer, the window simply elapses to denial —
which is the spec's "approval never answered → declined" edge case.

---

## R6 — Run records, observability, and notifications (in-memory, session-scoped)

**Decision**: `internal/escapehatch` holds an **in-memory** run store (a slice/map guarded by a mutex,
ring-trimmed like `agent.Hub`'s 512-entry buffer), **not** persisted to bbolt (clarification Q2 —
"session" = daemon uptime). Each `EscapeHatchRun` records the command, sandbox, status, start/end,
exit status, and bounded output. Live state changes flow to clients over the existing `Subscribe`
event stream via a **new `Event.escape_hatch_run` oneof arm** (so the sandbox row can show a
"running"/"awaiting approval" badge via `mergeSandboxUpdate` the same way the agent badge updates), plus
**two new `NotificationKind`s** (`NEEDS_APPROVAL`, `RUN_COMPLETE`) that land in the existing inbox. The
client reviews outcomes for the current session via a new unary **`ListEscapeHatchRuns(sandbox_id)`**.

**Why**: This reuses the whole feature-003 event/notification/inbox machinery
(`internal/agent/hub.go`, `internal/ui/notifications.go`) rather than inventing a parallel surface, and
keeping runs in-memory matches the clarified scope and avoids growing bbolt with unbounded captured
output.

---

## R7 — Configuration surface (no new env vars)

**Decision**: The three bounds (`defaultMaxDuration = 30m`, `approvalWindow = 5m`,
`maxCapturedOutput = 1 MiB`) are **package constants** in `internal/escapehatch`, in the spirit of the
existing `maxTagLen`/`maxBuffer` constants — **no new `SWITCHBOARDD_*` env var** is introduced.

**Why**: Rule VIII (env discipline) requires every new env var to be declared in the daemon config
schema and kept in lockstep with `.env.example` (verified by `env-check`). Feature 005 needs no runtime
knob, so keeping these as constants avoids new env surface and its drift risk. Per-command duration is
already author-controlled via the kit (`max_duration_seconds`); the constants are only fallbacks/caps.
Should a future need make them tunable, they would be added to `internal/config/config.go`'s `schema`
and `.env.example` together — but that is explicitly out of scope here.

---

## Summary of decisions

| # | Decision |
|---|----------|
| R1 | Async invoke over the existing hook HTTP channel; result pushed back via `agent.Registry.Prompt` PTY injection (survives detach). |
| R2 | Escape-hatch commands are structured proto on `KitSpec`, never in the opaque `spec.yaml`; validated client-side. |
| R3 | Daemon injects, at bring-up, a wrapper (`.switchboard/escape-hatch`) + a marker-delimited managed rule block in workspace `CLAUDE.md`; both removed when the resolved set is empty. |
| R4 | New `internal/escapehatch` executor: `sh -c` in `workspace_path`, streamed + bounded (1 MiB), `context.WithTimeout` (cmd or 30m), cancelled from the non-RUNNING teardown hook. |
| R5 | Consent gated in the daemon; `REQUIRES_APPROVAL` blocks on a decision channel (5-min window), decided by a new `DecideEscapeHatchRun` RPC; deny-by-default. |
| R6 | In-memory session-scoped run store; live via a new `Event.escape_hatch_run` arm + two `NotificationKind`s; reviewed via `ListEscapeHatchRuns`. |
| R7 | Bounds are daemon constants — no new env vars (keeps Rule VIII surface unchanged). |
