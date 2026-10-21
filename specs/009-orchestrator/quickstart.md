# Quickstart & Validation: Orchestrator

Runnable scenarios proving the feature end-to-end, mapped to the spec's user stories and success
criteria. Mechanics: [research.md](./research.md); entities: [data-model.md](./data-model.md);
wire and tool contracts: [contracts/](./contracts/).

## Prerequisites

- `sxbd serve` on each target host and `sxb` connected, all built from this feature's branch (an
  older daemon answers the new RPCs with `UNIMPLEMENTED`; update it with `u`).
- For the real path: Docker + `sbx` ≥ 0.36, signed in, on each daemon host. Without them,
  `make test` and `make e2e` cover everything except Scenario 0 via stub `sbx` and a stub `ssh`.
- **Two machines** (or two daemons reachable over SSH from each other) make Scenarios 2, 5 and 8
  meaningful; the orchestrator's machine must reach the worker's machine with keys or an agent
  (`ssh <target> true` works non-interactively there).
- A workspace folder to seed worker sandboxes from.

## Scenario 0 — Reconciliation gate (research R4/R5/R17) ⚠ FIRST, on a host with real `sbx`

Record each answer in `research.md` R17 before trusting anything below:

```bash
# 1. a loopback http:// remote server registers (warning tolerated?) and loads live
python3 -m http.server 8799 --bind 127.0.0.1 &            # any listener; replaced by the daemon in step 3
sbx mcp add probe-orch --url http://127.0.0.1:8799/orchestrator/w/deadbeef/mcp --skip-auth
sbx mcp ls                                                 # probe-orch listed; note any SSRF warning text from `add`
sbx mcp load probe-orch --sandbox <running-sandbox>        # rc 0 and "loaded … (live)"?
# 2. the gateway dials from the HOST (loopback reachable) or from the sandbox (host.docker.internal)?
#    with `sxbd serve --debug` running, attach a sandbox (Scenario 2) and watch which URL the
#    gateway hits the daemon's /orchestrator/w/<token>/mcp route on; if nothing arrives on
#    127.0.0.1, switch `worker.gatewayHost` to host.docker.internal and repeat.
# 3. the gateway speaks streamable HTTP and tolerates a stateless server:
#    inside the worker sandbox, ask the agent to list tools — the four orchestrator_* tools appear.
# 4. the `sxb-orch-…` registration is hidden from the TUI's MCP gateway view and from the row's mcp: list.
sbx mcp rm probe-orch --force
```

Expect: both argv rows in [contracts/orchestrator-mcp.md](./contracts/orchestrator-mcp.md)
confirmed or amended; the gateway host constant settled; the four tools visible to the agent.

## Scenario 1 — Create, find, rename, remove (US1; SC-010, SC-011)

1. From the sandbox list press **shift+→** twice: the **Orchestrators** screen; the header strip
   reads `Sandboxes · Daemons · Orchestrators` with the current one highlighted on every
   top-level screen; **shift+←** twice returns with the list cursor where it was.
2. `n` → name `review-bot`, pick a connected machine → listed within seconds with that machine,
   *reachable*, 0 workers, empty queue; the Reasoning view shows the reasoning sandbox coming up
   and then ready (`reasoning-<id8>`, absent from the everyday list).
3. Quit and restart `sxb`: still listed and reachable. Create another `review-bot` on any
   connected machine: refused naming the existing one.
4. `R` → `review-bot-2`: every row, the third line of any worker, the Orchestrator Sandboxes tab
   and the workers' `CLAUDE.md` block show the new name without a restart.
5. Disconnect that machine's daemon (`x` on the daemon screen): the row says *unreachable* with the
   reason; no operation is offered. Reconnect.
6. `d` → the confirmation names workers detached, reasoning sandbox destroyed, runs
   failed/cancelled, history discarded; confirm → gone, name free, reasoning sandbox destroyed.

## Scenario 2 — Attach and detach, local and remote (US2; SC-007, SC-009)

1. On the sandbox list, select a sandbox **on the orchestrator's machine**, press **o**, pick
   `review-bot`: the row leaves the list within 5 s; `sxbd serve --debug` on that host shows
   `mcp add sxb-orch-<id8> --url http://127.0.0.1:…` then `mcp load …`; no `stop`/`rm`/`create`;
   an attached terminal (`t`) stays connected; the orchestrator's Workers view lists it *free*.
2. Select a sandbox **on another machine**, press **o**: the TUI offers to add that host to
   `review-bot` (copying its own entry); accept → the orchestrator's daemon dials it over SSH and
   the attach completes; on a machine the orchestrator's daemon cannot SSH to, the attach is
   refused with ssh's error and nothing changes.
3. Try **o** again on a worker from a second orchestrator: refused naming `review-bot`.
4. Restart either daemon: the worker still shows `orchestrator: review-bot` on its third line and
   the Workers view lists it once both are up (SC-009 for the attach itself: ≤ 5 s).
5. **o** on a worker → *detach* (confirm): it reappears in the everyday lists; `mcp rm` ran on its
   host; the `CLAUDE.md` block is gone.
6. Launch (`n`) with **O** → pick `review-bot`: the new sandbox is never shown in the everyday
   list; its agent has the tools and briefing from its first prompt (ask it to list tools).

## Scenario 3 — Author an action, run it, report, review (US3; SC-002…SC-006, SC-018, SC-019)

1. Open `review-bot` → **Actions** → `a`: name `review-pr`, type `prompt`, body
   `Review pull request {number} in {repo} and leave review comments.`, arguments `number`
   (required), `repo` (required), `focus` (optional). `ctrl+s` → listed. Saving with an empty body,
   a `{nope}` placeholder, a duplicate name or `focus` declared twice is refused naming the problem.
2. `enter` on it → the form asks for the three arguments; submit with `number=412`,
   `repo=acme/web`, `focus=tests`. The **Queue** view shows the run *queued* then *running* on a
   free worker within 10 s; `enter` on it shows the composed prompt: both placeholders replaced and
   `focus: tests` appended at the end (SC-006). The worker's agent receives it framed with the run
   id and the completion instruction.
3. Call it twice more with two workers attached: two runs start at once, the third waits with
   position 1 and starts only when a worker frees (SC-002/SC-003); no worker ever has two runs.
4. When the agent calls `orchestrator_report_completion`, the run moves to history as *completed*
   with the summary within 2 s (SC-018) and raises **no** inbox notification; the worker is free
   once idle.
5. Prompt a worker by hand (`t`, type something): the orchestrator does not deliver to it until it
   is idle again (SC-015). Stop a worker: runs wait for another eligible worker.
6. Make an agent finish **without** reporting (tell it, via `t`, to reply "done" and nothing else
   to a hand prompt, then call an action on it; or kill the report tool by detaching the gateway):
   ~10 s after it goes idle the run is *awaiting review*, the worker appears at the top of the
   everyday list marked *worker · review*, and the inbox gets *run awaiting review* naming
   `review-bot` and `review-pr` (SC-019, SC-014). Press **m** on the row, keep *completed*, add a
   note → the run closes with the note as summary, marked *closed by developer*; the worker is free.
7. Add `worker_tag = ci` to `review-pr` and tag one worker `#ci`: calls go only to that worker; with
   it busy, the Queue view says *waiting for an eligible worker* and later runs still dispatch.
8. `x` on a queued run cancels it; a running run offers no cancel.

## Scenario 4 — Hidden workers, surfacing, the tab, the third line (US4; SC-001, SC-016)

1. With three workers attached: none on All, host or group tabs; group memberships intact (`g`).
2. Make one worker's agent ask for input: within 5 s it appears at the top of every tab it belongs
   to, marked *worker · needs input*; answer it through `t`; it leaves within 5 s.
3. Press **O**: the **Orchestrator Sandboxes** tab appears, selected, listing all three plus the
   reasoning sandbox (marked) with controller, state, availability and current run; every
   per-sandbox key works there (`t`, `S`, `M`, `l`, …) and `o` detaches, `m` marks free when
   applicable; `left`/`right` to another tab hides the tab again.
4. Rows: a sandbox with attached MCP servers and a controller shows a third line
   `mcp: notion · orchestrator: review-bot`; one with only MCP shows `mcp: notion`; one with
   neither shows two lines; a long server list truncates with `…` and never wraps (SC-016).

## Scenario 5 — Timed and triggered events (US5; SC-012, SC-013)

1. **Events** → `a`: name `poll`, action `review-pr`, fixed arguments, interval 1 minute, enabled,
   dedupe on. One run per minute appears with origin `poll`; the next firing time is shown.
2. Hold every worker busy for 3 minutes: no second run of `poll` is queued; the event shows skips;
   once the run closes the next firing queues one (SC-012). Switch dedupe off: a run per minute
   accumulates; each can be cancelled.
3. `space` disables the event: it stops; restart the orchestrator's daemon with it enabled: it
   resumes on its phase and the missed minutes are not replayed. Delete `review-pr`: the event is
   disabled with that reason.
4. From a worker's agent (`t`): "call the orchestrator action review-pr with number 7 and repo x":
   the run appears with that worker as origin and the agent is told the run id and position within
   2 s (SC-013). Detach that worker and ask again: refused, nothing queued.

## Scenario 6 — The worker's agent and the briefing (US6; SC-017)

1. Attach a worker; in its terminal ask the agent "what orchestrator are you attached to and what
   tools does it give you?" — it answers from `CLAUDE.md` without probing: the name, the four
   tools, the completion rule. `cat <workspace>/CLAUDE.md` shows the managed block between the
   markers; user content around it is untouched.
2. Ask it to list the actions, call one, then check that run: the three tools round-trip; the run's
   origin is this worker.
3. Add an action on the Orchestrators screen: the worker's next listing includes it and its
   `CLAUDE.md` block names it (reachable hosts only; an unreachable host catches up on reconnect).
4. Detach: the block is removed; a further tool call fails.

## Scenario 7 — The reasoning sandbox (US7)

1. On a fresh orchestrator the Reasoning view shows `reasoning-<id8>` ready with no folders and the
   default agent; it is on the Orchestrator Sandboxes tab marked *reasoning* and nowhere else.
2. **Actions** → `a`: `triage`, type `reason`, body `Given these issues: {issues} — decide which
   need code changes and reply with a list`. Call it: the run executes on the reasoning sandbox,
   never on a worker; its completion summary appears in the history.
3. Reasoning view → `e`: add a folder and a kit, `ctrl+s`: rebuilt (progress streamed); the folder
   is present inside; with a run on it the rebuild is refused until the run ends. `o` on it is
   refused (not detachable). `sbx rm` it on the host: `reason` runs fail with that reason and the
   view offers `b` rebuild.

## Scenario 8 — Restart and reconcile (SC-008; FR-134)

1. With two runs queued and one running, stop the orchestrator's daemon (`sxbd stop`) and start it
   again: the queued runs are still queued in order; within 30 s of the worker's host being
   reachable the running run is *awaiting review* if its agent is now idle with no report, left
   *running* if still working, *failed* if the sandbox is gone; nothing is re-delivered.
2. While the orchestrator's daemon is down: workers stay hidden, `t` and `o` (detach) still work on
   them, a worker that needs input still surfaces (the owning daemon's own signal); nothing is
   queued; on return the timed event fires on its next slot.
3. Kill the SSH path to a worker's machine (e.g. remove the key temporarily): the Workers view
   shows that host *unreachable*, runs are not delivered there, the link retries and recovers when
   access returns.

## Automated coverage map

| Layer | Covers |
|---|---|
| `switchboardd/internal/orchestrator` unit | action validation + composition (R8, SC-005/006), dispatch order property (SC-003/004), scheduler with fake clock incl. dedupe and phase (SC-012), run state machine + reconcile table (FR-134), retention, link call relay timeouts |
| `switchboardd/internal/worker` unit (stub `sbx`) | `mcp add/load/rm` argv pinning, token mint/revoke, CAS attach (SC-007), guarded delivery incl. PTY-drain and the run frame, briefing block write/rewrite/strip, MCP endpoint via the SDK client over `httptest` (four tools, attribution, 404 on bad token) |
| `switchboardd/internal/agent` | EXITED on PTY exit; `Hub.Publish` under the mutex |
| `switchboardd/internal/sandbox` | `EmptyWorkspace` launch/refresh |
| `switchboardd/internal/grpc` (two in-process daemons, injected `dial.Func`) | attach → link → deliver → status folding → report/review → detach over real gRPC; `UNIMPLEMENTED` skew message; every new status code |
| `libs/switchboard-dial` | the moved transport tests (stdio bridge, classification) |
| TUI `ui` (`teatest`) | three-screen cycle + header strip, per-host event streams, hidden/surfaced ordering, `O` tab lifetime, third-line delegate, `o`/`m` flows, launch `O`, the orchestrator views and `huh` forms |
| E2E (two stub daemons, stub `ssh`) | create → attach local + "remote" → call → report through a stub tool call → history; restart keeps the queue |
