# Quickstart: validating Action Pre & Post Prompts (SWI-17)

This is a run guide. The behaviour each scenario proves is defined in [spec.md](./spec.md). The interfaces are in
[contracts/](./contracts/), and the state is in [data-model.md](./data-model.md).

## Prerequisites

- A build of `sxbd`/`sxb` from this feature's branch, **rebased on SWI-15 (PR #17) and SWI-9's implementation**
  (research R12).
- One orchestrator with one worker running **Claude Code 2.1.312 or later**, the version research R1 was probed on.
  Add a second machine for Scenario 9.
- Gates green: `make fmt-check vet lint cover` and `cd src/apps/switchboard-tui-e2e && go test -tags e2e ./...`.

## Scenario 0 — the R1 matchers still hold on this agent version

1. Open the worker's terminal (`t`). Type `/model opus`, `/effort medium`, `/model bogus` and `/clear`.
2. Each confirmation line must match the fixtures in `internal/agent/testdata/claudeui/`:
   - `Set model to Opus …`
   - `Set effort level to medium …`
   - `Model 'bogus' not found`
   - after `/clear`, the redraw with an empty prompt.
3. **If any line differs**, update the fixture and matcher (`agent/claudeui.go`) **before** the other scenarios. A
   mismatch shows up as *not accepted* halts, never as wrong typing.

## Scenario 1 — defaults on a new and an existing action (US1 #1, #4, #5; FR-203, FR-210)

1. Create an action with only a name and a body.
   - The form shows Model `opus`, Effort `medium`, and Clear after run `yes`.
   - The Model options are opus, fable, sonnet and haiku; the Effort options are low through auto, **with no
     ultracode**.
2. Call it. The worker's terminal shows `/model opus`, then `/effort medium`, then the framed action prompt.
3. After the agent's completion report, it shows `/clear`.
4. The Jobs row reads `setting up 1/2` → `setting up 2/2` → `running` → `completed · cleaning up 1/1` → completed.
5. An action created **before** upgrading behaves the same and opens in the editor with the defaults.

## Scenario 2 — custom prompts in order (US2 #1–#3; FR-205, FR-207)

1. Give an action the pre-prompt `/pre-setupskill for {ticket}` and the post-prompt `/tear-down-skill`, then call it
   with `ticket=SWI-1`.
2. The order is `/model`, `/effort`, `/pre-setupskill for SWI-1`, the action prompt, then (after the report)
   `/tear-down-skill`, then `/clear`.
3. The action prompt waits for the setup skill's turn to end.
4. Edit the action while a run is in flight. That run keeps its steps; the next call uses the new lists.

## Scenario 3 — validation (FR-204; edge cases)

Each save below is refused, naming the entry:
- `{skill} now` (placeholder in the first word);
- `!git checkout {branch}`;
- `{undeclared}`;
- an 11th pre-prompt;
- a whitespace-only line. *Expect no refusal:* the line is simply dropped by the form.

The prompt `  !echo hi` is saved trimmed as `!echo hi`.

## Scenario 4 — not accepted, retry, skip, fail (US3 #4; FR-206c, FR-222)

1. Set Model to `opus` on an account without Opus, or type text into the worker's box as the step is sent.
2. The row shows `not accepted: …`, nothing more is typed, and a `RUN_STEP_NOT_ACCEPTED` notification arrives.
3. Try each control:
   - `S` skips: the sequence continues.
   - `R` retries after the box is clear.
   - `X` fails a pre-prompt: the run fails, naming the step.

## Scenario 5 — cleanup holds (US3 #2, #5, #6; FR-215, FR-216)

1. While `/tear-down-skill` runs, the worker shows `cleaning up <run>` and takes no queued run.
2. Make Clear not accepted, then `X` fail it. The worker sits at the top of the All tab:
   `needing the developer — open its terminal and run /clear`.
3. `t`, then `/clear`, frees it, and a queued run is then delivered.
4. Repeat, then exit the agent instead (`/exit`). `t` starts a fresh agent (`SessionStart startup`) and frees it.
5. With an agent spec that launches with `--continue`, the hold stays after `t` until `/clear`.
6. Press `R` on the halted Clear while no agent runs, then `t`. The hold ends, and the waiting Clear is dropped:
   nothing is typed into the free worker.

## Scenario 6 — freeing during cleanup input (FR-216)

1. A post-prompt asks a question, and the developer marks the worker free.
2. The worker shows `needs input (freeing)`.
3. Answer the question in `t`. `/clear` is sent once the box is empty, then the worker is free.

## Scenario 7 — restart mid-sequence (FR-217; research R8, R14)

1. With a **remote** worker, restart the orchestrator's daemon during a long setup skill turn. On reconnect the run
   continues to the action prompt, and nothing is re-sent.
2. Restart the **worker's own** daemon (for a local worker, the orchestrator's daemon). The agent keeps running in
   the sandbox, but the daemon can no longer type into it.
   - An *agent detached* notification names each sandbox with a detached agent, once. The daemon never stops one
     on its own.
   - **During the action prompt:** the run stays in progress, and the agent's completion report closes it. The
     worker then shows *needing the developer* (`t → Replace frees the worker`, plus a note when custom post-prompts
     were dropped); Replace starts a fresh agent and frees it. Pressing `t` before the report shows the Wait/Replace dialog (default Wait); **Replace** fails the run
     (`agent replaced by the developer after a daemon restart`) and starts one fresh agent.
   - **During a setup step:** the step halts with `agent detached by a daemon restart; last seen …`. An escape-hatch
     result arriving meanwhile kills nothing. `R` (*Restart setup*, after its confirm) stops the old agent, starts a
     fresh one, and sends every pre-step again from the first; the action prompt is typed only after `/model` and
     `/effort` were confirmed on the new agent. `S` isn't offered.
   - **During cleanup:** the worker shows *needing the developer*. `t` shows the dialog (default Replace, the agent
     is idle); Replace stops the old agent, starts a fresh one, and that frees the worker.
   - **An idle worker** keeps its detached agent: its row reads `agent detached by a daemon restart — t to Replace
     or Wait`, and queued runs go to other workers until you press `t` → Replace. The restart raises **one**
     notification (`N agents detached by a daemon restart (M on idle workers) — R on the Workers view replaces the
     idle ones`). `R` on the Workers view lists the idle detached workers; `y` replaces them all and reports
     `replaced N`; a worker holding a run is not listed.
   - **An idle worker with no agent** (its agent had exited before the restart, so no token is outstanding) is
     dispatchable at once: a queued run is delivered with no `checking for a detached agent` note.
   - **A check that can't complete:** make the probe fail (for example, block `sbx exec` on the host). Within 60 s the
     row reads `can't check for a detached agent: <error>`, a *can't check* notification appears, and a run on it
     shows `can't confirm the agent is still running: … — t to Wait or Restart sandbox (fails the run)`. The run
     is not failed on its own. Unblock `sbx exec`: the note goes back to what it was. Block it again and kill the
     agent: the run fails with `agent exited`.
   - **A plain sandbox** where you were mid-turn: `t` shows the dialog with default **Wait**; the turn is not
     killed unless you choose Replace.
   - **A sandbox stopped while the daemon was down** is not started by the restart, and gets no dialog.
   - The daemon answers the TUI at once after the restart; a worker shows `checking for a detached agent` for at
     most a few seconds.
   - **Without a daemon restart:** on the host, kill the `sbx exec` client of a working worker's agent. The run stays
     in progress (no `agent exited`), and `t` shows the dialog; `ps` in the sandbox still shows one agent. Then
     `kill -9` the old agent inside the sandbox: within a minute the run fails with `agent exited`.
   - **Stop during a check:** restart the daemon and press Stop on a worker while it still shows `checking for a
     detached agent`. The sandbox stops and stays stopped (no `sbx exec` restarts it), and its row clears. Pressing
     `t` meanwhile shows `sandbox is stopping` (or the operation in progress: restarting, being refreshed, …).
   - **External stop:** `sbx stop` the sandbox of a detached worker from the host. The row's detached note clears
     and the next start gets one fresh agent.
   - In every case, `ps` inside the sandbox never shows two agents, and no agent is stopped except by your Replace,
     `R`, or a sandbox stop/restart.
3. Drop the link to a remote worker just after a step is typed (for example by killing the SSH dial). On reconnect
   the step resolves from the daemon's record to sent, then finishes as usual.

## Scenario 8 — notifications (FR-223)

During a run with a setup skill and a cleanup skill, only the action prompt's turn raises "agent task complete".
Needs-input from a setup skill still notifies.

## Scenario 9 — cross-machine (FR-201, FR-223)

With the worker on a second machine, Scenarios 1, 5 and 8 behave the same. The hold shows on the worker's own
sandbox list and on the orchestrator machine's Workers view.

## Scenario 10 — no sequence (FR-208, SC-005)

With every built-in off and no custom prompts, the run is delivered through `DeliverWorkerPrompt` exactly as before:
one framed prompt, and no step text in the row.

## Scenario 11 — injection and forged hooks (research R13)

1. Call an action with an argument value containing `ESC[201~` followed by `\r!id\r` (for example from a worker's
   `orchestrator_call_action`). The call is refused: `argument <name> contains a control character`. Nothing is
   typed.
2. From inside a held worker's sandbox, `curl` the hook URL with `SessionStart` `source=clear` and the old session
   id. The hold stays. With another sandbox's token, the request gets 403; with no token, it changes only the status
   display.
3. Restart the daemon while a worker's agent is working an action step. Its hooks keep working afterwards (its
   generation's token is persisted), and its escape hatch still runs (the re-injected wrapper sends the token).
   Inside the sandbox, `grep -c SXB_HOOK_TOKEN .claude/settings.local.json` finds only the variable, never a
   token.
4. Have an escape-hatch command print coloured output. Its result still reaches the agent.
5. Run `claude -p "hi"` in a worker's workspace during a setup step. That nested session's hooks don't classify or
   finish the step.
6. On the host, `ps -ef | grep SXB_HOOK_TOKEN` shows no token value. From your own `sbx exec -it <sb> bash` shell,
   the escape-hatch wrapper gets 403 `no agent token in this environment; …`; from the agent it runs.
7. During a run, type `/resume` in the worker's terminal and pick an earlier session. The run's next step still
   finishes when that turn ends. Then have the agent run `claude -c -p "hi"` from a tool during a setup step: the
   step still finishes when the main agent's turn ends.
