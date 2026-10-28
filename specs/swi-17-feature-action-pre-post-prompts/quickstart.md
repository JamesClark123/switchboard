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

## Scenario 7 — restart mid-sequence (FR-217; research R8)

1. Restart the orchestrator's daemon during a long setup skill turn. On reconnect the run continues to the action
   prompt, and nothing is re-sent.
2. Restart the **worker's** daemon after `/effort` was typed. The step either finishes (read-back on screen) or halts
   `sent before a restart`, with `S` listed first.

## Scenario 8 — notifications (FR-223)

During a run with a setup skill and a cleanup skill, only the action prompt's turn raises "agent task complete".
Needs-input from a setup skill still notifies.

## Scenario 9 — cross-machine (FR-201, FR-223)

With the worker on a second machine, Scenarios 1, 5 and 8 behave the same. The hold shows on the worker's own
sandbox list and on the orchestrator machine's Workers view.

## Scenario 10 — no sequence (FR-208, SC-005)

With every built-in off and no custom prompts, the run is delivered through `DeliverWorkerPrompt` exactly as before:
one framed prompt, and no step text in the row.
