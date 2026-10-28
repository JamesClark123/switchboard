# Feature Specification: Action Pre & Post Prompts

**Feature Branch**: `jamesloganclark/swi-17-feature-action-pre-post-prompts`

**Created**: 2026-10-27

**Status**: Draft

**Input**: User description (Linear SWI-17, "Feature - Action pre & post prompts"), quoted verbatim:

> We want to support actions being able to trigger setup and teardown prompts before the targeted prompt ever fires. How this would work:
>
> When creating or editing an action there is a section for pre-prompts and post-prompts. Each prompt entered in these sections will be fired before or after respectively in the agent relative to the action prompt being run. So, for example, if I want to run `/pre-setupskill` and `/tear-down-skill` before and after `some prompt to do something` I could add those skills or prompts to this section and the orchestrator would know to send the setup skill prompt and wait for it to finish before sending the action prompt. Then to fire the tear down skill prompt once the action prompt has completed.
>
> Default pre&post prompts. We know there are a set of prompts users will want to simply enable or disable rather than have to type out for every action. So in the UI we want to give them a convenience selector to enable or disable these prompts. Here is the list we want to add:
>
> 1. An (enabled by default) prompt to `/clear` at the end of an action. This post-prompt will result in clearing the agent memory and allow a new session to be started fresh for the next action
> 2. An (enabled by default) prompt to `/model [input]` before an action runs. This will allow users to set the model they want to run for an action. `[input]` here indicates that the user should have the ability to select from a set of options for the pre-prompt. The default should be claude `Opus` with options for `Fable` `Sonnet` and `Haiku`
> 3. An (enabled by default) prompt to `/effort [level]` before an action runs. This will allow users to set the model's effort they want for an action before it runs. `[level]` here indicates that the user should have the ability to select from a set of options for the pre-prompt. The default should be `medium` but there should be options for `low` `high` `xhigh` `max` `auto` and `ultracode`

Builds on the orchestrator (`specs/009-orchestrator`: actions, runs, delivery to free workers, completion report, awaiting review, mark free) and the Jobs screen (`specs/010-jobs-screen`). Requirement numbers continue from 010's (FR-0xx there) in a new range, **FR-200 onward**, so references stay unambiguous.

## Clarifications

### Session 2026-10-27

- Q: Do the three defaults (`/model` Opus, `/effort` medium, `/clear`) turn on for actions that already exist when this ships, or only for new ones? → A: **All actions** — existing actions get the three defaults enabled on upgrade, exactly as a new action would. (FR-210)
- Q: When do an action's post-prompts (including `/clear`) run? → A: **After the agent's completion report, and after the developer marks a reviewed worker free** — so the worker is always cleaned up before its next run. Not after a cancel (nothing was delivered). (FR-214)
- Q: Do pre/post prompts apply to `reason` actions as well as `prompt` actions? → A: **Both types.** The reasoning sandbox is set up and cleaned up the same way. (FR-201)
- Q: Spec folder name? → A: `specs/swi-17-feature-action-pre-post-prompts` (the Linear branch name without the user prefix).
- Q: May custom pre/post prompts use the action's argument placeholders? → A: **Yes, with the body's `{name}` rules** — filled at call time from the call's values; an undeclared name is refused at save; unreferenced arguments are appended only to the action prompt, never to pre/post prompts. (FR-200, FR-204, FR-207)
- Q: Does a bound end a pre/post step that never finishes? → A: **No timeout** — as with the action prompt today, the run shows which step it is on and the developer acts on the worker. (FR-206)

### Session 2026-10-28 (PR #20 review, run-481)

- Q: What tells the orchestrator a step has finished? → A: **Evidence observed after that step was submitted, never an idle state that was already there.** A step that starts a model turn is finished when the agent reports it started working on it and then ended that turn with no background work pending. A step the agent handles itself (no model turn — the built-ins, or a custom slash command) is finished when the agent is back at an empty, ready input box with no picker or dialog open, and, for a built-in whose setting the agent reports, that setting reads back as chosen. A step for which neither is confirmed within the submit check (today's re-press window) is **not accepted**: the sequence halts there, nothing more is typed, and the developer retries, skips or fails it. This bounded check is about acceptance; an accepted step still has no timeout. (FR-206, FR-206a–c, FR-222)
- Q: May a call's value decide what kind of input a bare step is? → A: **No.** A custom prompt must start with the author's own text: a placeholder in the first position is refused at save. (FR-204)
- Q: Does an argument used only in a pre/post prompt still reach the action prompt? → A: **Yes.** Whether an argument is appended to the action prompt depends on the **body** alone, exactly as today. (FR-207)
- Q: Do setup and cleanup turns raise "task complete" notifications? → A: **No.** Only the action prompt's turn may; a step that needs the developer still notifies as today. (FR-223)
- Q: What does freeing a sandbox mid-cleanup do, and do post-prompts follow a mark free as *failed*? → A: Freeing drops the remaining **custom** post-prompts but still sends Clear (if enabled) before the sandbox is free; post-prompts follow a mark free with either outcome. An agent that exits mid-cleanup comes back in a new session, which counts as cleared. (FR-214, FR-216)

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Built-in setup and cleanup on every run (Priority: P1)

A developer runs actions on workers through an orchestrator. Today each run lands in whatever session the worker's agent was left in: the previous run's conversation is still in context, and the model and effort are whatever someone last set. With this feature every action carries three built-in steps, on by default: before the action prompt the worker's agent is switched to a chosen **model** (Opus unless changed) and **effort** level (medium unless changed); after the run completes, its session is **cleared** so the next run starts fresh. The developer can switch each built-in step off, and pick the model and effort, per action.

**Why this priority**: It is the feature's main value: runs become repeatable (same model, same effort, clean context) with no typing, and it applies to every existing action from day one.

**Independent Test**: Create an action with the defaults untouched, call it, and watch the worker's terminal: `/model opus`, then `/effort medium`, then the action prompt, then — after the agent's completion report — `/clear`. Change the model to Haiku and switch `/clear` off; the next run sends `/model haiku` and no `/clear`.

**Acceptance Scenarios**:

1. **Given** a new action saved with the defaults untouched, **When** it is called and delivered to a free worker, **Then** the worker's agent receives, in order and each only after the previous one has finished: `/model` with Opus, `/effort medium`, the action prompt; and after the agent's completion report, `/clear`.
2. **Given** an action whose model is set to Sonnet and effort to `high`, **When** it runs, **Then** the pre-prompts sent are the model selection for Sonnet and `/effort high`.
3. **Given** an action with all three built-in steps switched off and no custom prompts, **When** it runs, **Then** only the action prompt is delivered — exactly today's behaviour.
4. **Given** an action that existed before this feature shipped, **When** it is next called, **Then** it runs with the three defaults enabled (Opus, medium, `/clear`), and its editor shows them enabled.
5. **Given** the action editor, **When** the developer opens the model choice, **Then** it offers Opus (default), Fable, Sonnet and Haiku; **and** the effort choice offers low, medium (default), high, xhigh, max, auto and ultracode.

---

### User Story 2 - Custom pre-prompts and post-prompts (Priority: P1)

The developer adds their own setup and teardown to an action: a list of **pre-prompts** sent before the action prompt (for example `/pre-setupskill`) and a list of **post-prompts** sent after the run completes (for example `/tear-down-skill`). Each is any text the agent accepts as a prompt — a skill or slash command, or plain words. The orchestrator sends each one, waits for the agent to finish it, then sends the next, so the action prompt starts only once setup is done and teardown happens only after the run's completion report.

**Why this priority**: This is the capability the ticket asks for; the defaults in US1 are a convenience on top of it.

**Independent Test**: Give an action the pre-prompt `/pre-setupskill` and the post-prompt `/tear-down-skill`, call it, and confirm in the worker's terminal that the action prompt arrives only after the setup skill finished, and the teardown skill only after the completion report.

**Acceptance Scenarios**:

1. **Given** an action with pre-prompts A, B and post-prompts C, D (defaults off), **When** it runs, **Then** the agent receives A, then B, then the action prompt, then — after its completion report — C, then D; each step is sent only once the agent has finished the previous one.
2. **Given** an action with defaults on and custom prompts, **When** it runs, **Then** the order is: built-in `/model`, built-in `/effort`, custom pre-prompts in their listed order, the action prompt, custom post-prompts in their listed order, built-in `/clear` last.
3. **Given** the action editor, **When** the developer adds, edits, reorders or removes pre- or post-prompts and saves, **Then** the next call of the action uses the new lists; runs already created keep the lists they were created with.
4. **Given** a pre-prompt that makes the agent ask the developer a question, **When** that happens, **Then** the run shows *awaiting input* exactly as it would for the action prompt, and the sequence continues once the agent is idle again.

---

### User Story 3 - See where a run is in its sequence (Priority: P2)

While a run is in flight the developer can tell which step it is on — setting up (pre-prompt *n* of *m*), running the action prompt, or cleaning up (post-prompt *n* of *m*) — and the run's composed prompt view lists every step that was or will be sent, so it is clear what the worker received.

**Why this priority**: Pre- and post-prompts add time and steps to a run; without visibility a slow setup skill looks like a stuck run.

**Independent Test**: Call an action with a slow pre-prompt and confirm the run's row on the Jobs screen and in the orchestrator's views says it is setting up, then running, then (after the report) cleaning up; open the run's prompt and see the full sequence.

**Acceptance Scenarios**:

1. **Given** a run whose worker is executing its second of three pre-prompts, **When** the developer looks at the run on the Jobs screen, **Then** the row says it is setting up and which step (2 of 3).
2. **Given** a completed run whose post-prompts are still being sent, **When** the developer looks at the worker, **Then** the worker shows it is cleaning up after that run and is not yet free.
3. **Given** any run, **When** the developer opens its prompt, **Then** the overlay shows the pre-prompts, the composed action prompt and the post-prompts that the run was created with, in order.
4. **Given** a run whose `/model` step was not accepted (it stayed in the input box, or the agent opened a picker), **When** the developer looks at the run, **Then** the row says the step was not accepted, nothing further has been typed into the worker, and the developer can retry the step, skip it, or fail the run.

---

### Edge Cases

- **Agent reports completion during a pre-prompt**: a completion report before the action prompt has been delivered is refused and changes nothing; the sequence continues.
- **Agent goes idle after the action prompt without reporting**: unchanged from today (009 FR-156) — the run becomes *awaiting review*; post-prompts are **not** sent until the developer marks the worker free, and then they are (FR-214).
- **Run cancelled while queued**: nothing was delivered, so no pre- or post-prompts are sent.
- **Worker's agent exits, or the sandbox stops or is destroyed, during a pre-prompt**: the run fails with the reason named (it never reached its action prompt); no further steps are sent.
- **…during the action prompt**: unchanged from today.
- **…during a post-prompt**: the run stays *completed* with its summary; the remaining post-prompts are dropped and the sandbox is reported as needing the developer. It is not free until its agent is running again; the restarted agent is a new session, so it counts as cleared (FR-216).
- **A post-prompt makes the agent ask for input**: the run is already completed; the sandbox is shown as needing input (as a worker parked needs-input is today) and is not free until it is idle again and its remaining post-prompts have been sent. If the developer frees it instead, the remaining custom post-prompts are dropped, Clear (if enabled) is still sent, and the sandbox is free once Clear has finished (FR-216).
- **A step never finishes** (the agent stays busy far longer than any reasonable step): it is treated like any long-running prompt — no timeout ends an accepted step on its own; the developer sees which step the run is on (US3) and can act on the worker.
- **A step is not accepted** (it stays in the agent's input box, or a built-in opens a picker or is rejected, e.g. an option the agent's account does not offer): the sequence halts at that step and shows it as *not accepted*; nothing further is typed, so two steps are never sent as one line. The developer retries the step, skips it, or fails the run (FR-206c, FR-222).
- **The agent is already idle when a step is sent**: that idle state does not finish the step; only evidence observed after the step's submit does (FR-206a).
- **Developer types in the worker's terminal between steps**: developer activity takes precedence (009 FR-119): the next step is sent only once the agent is idle again.
- **Orchestrator or daemon restarts mid-sequence**: the run resumes from the step it was on. A step that was sent but whose finish was not recorded is resolved by the same evidence as FR-206b: a built-in whose setting reads back as chosen is finished and one that does not is sent again; any other step is finished if the agent is at an empty, ready input box, and otherwise the sequence halts as *not accepted* for the developer (FR-217).
- **Action edited while one of its runs is mid-sequence**: the in-flight run keeps the lists it was created with (009 FR-140).
- **Empty or whitespace-only custom prompt**: refused at save with the offending entry named.
- **Custom prompt that starts with a placeholder** (e.g. `{skill} for {ticket}`, or `{cmd}` alone): refused at save with the placeholder named — a bare step's first character must be the author's, so a call's value cannot turn it into a slash command or a shell escape (FR-204). Because every custom prompt starts with literal text, none composes to nothing.
- **Placeholder in a pre/post prompt names an undeclared argument**: refused at save with the placeholder named, as for the body.
- **Argument used only in a pre/post prompt**: still appended to the action prompt if the body does not reference it, exactly as today (FR-207).
- **Built-in step text in a custom list** (e.g. a custom pre-prompt `/clear`): allowed; it is sent as written in its position. The editor does not de-duplicate.
- **Worker's agent does not understand a built-in step** (e.g. an agent other than Claude Code): the step is not accepted (FR-206c) and the sequence halts; the developer switches the built-ins off for such actions.

## Requirements *(mandatory)*

### Functional Requirements

#### Action fields

- **FR-200**: An action MUST carry, in addition to its existing fields, an ordered list of **custom pre-prompts** and an ordered list of **custom post-prompts**. Each entry is non-empty text sent to the agent as a prompt, without the run frame. An entry MAY contain **placeholders** naming the action's declared arguments, in the same form as the body (009 FR-138); they are filled per FR-207. Either list MAY be empty.
- **FR-201**: Pre- and post-prompts MUST be supported on every action type (`prompt` and `reason`) with identical behaviour; for `reason` actions they are sent to the orchestrator's reasoning sandbox.
- **FR-202**: An action MUST carry three **built-in steps**, each independently enabled or disabled:
  - **Model** (pre): selects the agent's model; its value is one of **Opus** (default), **Fable**, **Sonnet**, **Haiku**. Sent as the agent's `/model` command for that choice.
  - **Effort** (pre): sets the agent's effort; its value is one of **low**, **medium** (default), **high**, **xhigh**, **max**, **auto**, **ultracode**. Sent as `/effort <level>`.
  - **Clear** (post): clears the agent's session. Sent as `/clear`.
- **FR-203**: A newly created action MUST have all three built-in steps **enabled**, with Model = Opus and Effort = medium.
- **FR-204**: Saving an action MUST validate the new fields and refuse a save with the problem named when: a custom prompt is empty or whitespace-only; a list has more than **10** entries; a custom prompt exceeds the size limit that applies to argument values (009 FR-140); a built-in value is not one of its listed options; a custom prompt names an undeclared argument in a placeholder (the placeholder named, as for the body); a custom prompt whose first non-space character is part of a placeholder (the placeholder named). Custom prompts are sent without the run frame (FR-200), so the author's text, not a call's value, decides whether a step is a slash command, a shell escape or plain words.

#### Sequence

- **FR-205**: When a run is delivered, the orchestrator MUST send its steps in this order: Model (if enabled), Effort (if enabled), the custom pre-prompts in order, the **action prompt** (composed and framed exactly as today), then — per FR-214 — the custom post-prompts in order, then Clear (if enabled).
- **FR-206**: The orchestrator MUST send a step only when the previous step has **finished** (FR-206a–b) and no developer activity is in progress (009 FR-119). The first step is sent under today's delivery rule (the sandbox is free, 009 FR-117). No timeout ends an accepted step: a step that stays busy is shown as the current step (FR-213) and left to the developer.
  - **FR-206a** (no stale idle): only evidence observed **after the step was submitted** counts. An idle or ready state that was already present when the step was sent MUST NOT finish it.
  - **FR-206b** (finished, per kind): a step that starts a **model turn** is finished when the agent reports that it began working on that step and then that the turn ended with no background work pending. A step the agent **handles itself** (no model turn follows: the built-ins, or a custom slash command the agent runs locally) is finished when the agent is back at an **empty, ready input box with no picker or dialog open**, checked after the submit; for a built-in whose setting the agent reports, that setting MUST also read back as the chosen value.
  - **FR-206c** (not accepted): if neither condition of FR-206b is confirmed within the submit check (today's re-press window after typing a prompt), the step is **not accepted**. The sequence MUST halt at that step and type nothing further, so text is never appended to a step left in the input box; the run shows the step as not accepted (FR-213) and raises a needs-developer notification. This bound applies only to confirming acceptance, never to how long an accepted step runs.
- **FR-207**: A run MUST snapshot its sequence **at call time**, together with its composed prompt: the built-in choices and each custom pre/post prompt with its placeholders replaced by the call's values (every occurrence; an optional argument not supplied takes its default, otherwise its placeholders are replaced by nothing — 009 FR-139). Whether a supplied argument is appended to the action prompt depends on the **body alone**, exactly as today: an argument the body does not reference is appended to the action prompt even if a custom prompt references it. Nothing is ever appended to a pre/post prompt. Later edits to the action MUST NOT change a run already created (as 009 FR-140).
- **FR-208**: A run with no enabled built-in steps and no custom prompts MUST behave exactly as runs do today.

#### Run and worker state

- **FR-209**: While pre-prompts are being sent the run MUST be *running* (or *awaiting input* while the agent asks for input), as from delivery today. Idle between pre-prompts MUST NOT make the run *awaiting review*; only idle without a report **after the action prompt** does (009 FR-156).
- **FR-210**: Actions that exist when this feature is installed MUST be treated as having all three built-in steps enabled with the defaults (Opus, medium, Clear) and empty custom lists, and MUST show so in their editor.
- **FR-211**: A completion report received before the action prompt has been delivered MUST be refused and change nothing.
- **FR-212**: If the agent exits, or the sandbox stops or is destroyed, before the action prompt is delivered, the run MUST become *failed* with a reason naming the step it was on. A run the developer fails at a halted pre-prompt (FR-222) fails the same way.
- **FR-213**: The run MUST record which step it is on (setting up *n*/*m*, action, cleaning up *n*/*m*) and whether that step is *not accepted* (FR-206c), and the TUI MUST show it wherever the run's state is shown (Jobs screen rows, orchestrator views) and on the sandbox while it cleans up.
- **FR-214**: Post-prompts MUST be sent after the run is closed by the agent's **completion report**, and after the developer **marks the sandbox free** from *awaiting review* (009 FR-156) with either outcome, *completed* or *failed* — in every case once the agent is idle. They MUST NOT be sent for a run cancelled before delivery or for a run that failed before its action prompt was delivered.
- **FR-215**: Closing the run (completion report or mark free) MUST happen when it does today — the run's outcome and summary do not wait for post-prompts. The **sandbox** that ran it — a worker, or the reasoning sandbox for a `reason` run — MUST NOT be free (009 FR-117) until its post-prompts have all finished (FR-206b) or been dropped per FR-216; until then it receives no new run of either type.
- **FR-216**: If a post-prompt makes the agent ask for input, the sandbox (worker or reasoning sandbox) MUST be shown as needing input (as a parked needs-input worker is today) and receive no new run; the remaining post-prompts continue once the agent is idle. If the developer **frees** it instead, the remaining **custom** post-prompts MUST be dropped and Clear (if enabled) MUST still be sent; the sandbox is free once Clear has finished. If the agent exits or the sandbox stops during post-prompts, the remaining post-prompts MUST be dropped and the sandbox shown as needing the developer; it is not free until its agent is running again, and the restarted agent's new session counts as cleared.
- **FR-217**: An orchestrator restart MUST resume each in-flight sequence at the step it was on, without sending any step twice to an agent that has already finished it. A step sent but not recorded finished MUST be resolved by the evidence of FR-206b: a built-in whose setting reads back as chosen is finished, and one that does not is sent again; any other step is finished if the agent is at an empty, ready input box, and otherwise halts as not accepted (FR-206c).

#### Editing and visibility

- **FR-218**: The action create and edit forms (Jobs › Actions and the orchestrator's Actions view) MUST let the developer: switch each built-in step on or off; choose the Model and Effort values from their lists; add, edit, reorder and remove custom pre- and post-prompts.
- **FR-219**: The action's details as shown to the developer MUST summarise its sequence (e.g. `pre: /model opus · /effort medium · 1 custom — post: 1 custom · /clear`).
- **FR-220**: A run's prompt overlay MUST show its full snapshotted sequence in order: pre-prompts, the composed action prompt, post-prompts.
- **FR-221**: The actions offered to workers' agents through the orchestrator's tools (009 FR-151) MUST NOT expose pre/post prompts as arguments; calling an action is unchanged.
- **FR-222**: For a run halted at a step that was not accepted (FR-206c), the developer MUST be able, from wherever the run is shown, to **retry** the step (sent again once the input box is empty), **skip** it (the sequence moves on), or **fail** the run (a pre-prompt: the run fails per FR-212; a post-prompt: the run keeps its outcome and the sandbox is freed per FR-216).

#### Notifications

- **FR-223**: Only the turn of the **action prompt** MAY raise the agent's task-complete notification for a run. The turns of pre-prompts and post-prompts MUST NOT raise task-complete notifications (inbox or desktop). A step that needs the developer (asks for input, or is not accepted) MUST still notify as needs-input does today.

### Key Entities

- **Action** (extended): gains *custom pre-prompts* (ordered list of text, may contain `{name}` placeholders), *custom post-prompts* (same), and *built-in steps*: Model (enabled, choice), Effort (enabled, level), Clear (enabled).
- **Run** (extended): gains its *sequence snapshot* (the steps it will send, in order, placeholders already filled), its *current step* (setting up *n*/*m*, action, cleaning up *n*/*m*, done) and whether that step is *not accepted*.
- **Sandbox** (extended state, workers and the reasoning sandbox alike): may be *cleaning up* after a closed run — not free, with the run it is cleaning up after.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: With the defaults untouched, 100% of runs either start with the selected model and effort confirmed (FR-206b) or halt visibly before the action prompt (FR-206c); and the sandbox never takes its next run until the previous run's session has been cleared — by Clear, or by the agent restarting in a new session (FR-216).
- **SC-002**: A developer can add a pre-prompt and a post-prompt to an existing action, and switch a built-in step off, in under 1 minute from the action list.
- **SC-003**: In 100% of runs, the action prompt is delivered only after every pre-prompt finished, and no post-prompt is delivered before the run's completion report (or the developer's mark free).
- **SC-004**: No sandbox (worker or reasoning sandbox) receives a new run while any post-prompt of its previous run is still pending (not finished and not dropped per FR-216).
- **SC-005**: An action with every built-in step off and no custom prompts produces a run indistinguishable from today's.
- **SC-006**: At any moment during a run, the developer can tell from the Jobs screen which step the run is on without opening the worker's terminal.

## Assumptions

- **Built-in step text**: the Model step sends the agent's model-switch command with the chosen model (`/model opus`, `/model fable`, `/model sonnet`, `/model haiku`); Effort sends `/effort <level>`; Clear sends `/clear`. The exact model identifiers **and effort levels** are confirmed against the agent during planning; an option the agent does not accept is removed from its list (a spec amendment), and one an account cannot use at run time is *not accepted* (FR-206c).
- **"Finished" is evidence after the submit** (FR-206a–c), not "idle". Today a typed prompt is confirmed by the agent's prompt-submit hook moving it to working, and the delivery path re-presses Enter when that does not arrive; a model turn ends with the turn-end hook. ⚠️ **Planning gate**: built-in slash commands (`/model`, `/effort`, `/clear`) are handled by the agent locally and are expected to raise neither hook. Before the step tracker is designed, the plan MUST establish on a real worker, for each built-in, how FR-206b's two checks are made (empty ready input box; the setting read back where the agent reports it). If a built-in has no confirmable signal, the plan MUST apply that setting by a mechanism that has one (for example at the agent's launch) and record the deviation, rather than fall back to a timed wait.
- **No timeout** ends an accepted step; the only bound is the submit check that decides acceptance (FR-206c). A stuck step is visible (FR-213) and handled by the developer, as a stuck action prompt is today.
- **Limits**: at most 10 custom pre-prompts and 10 custom post-prompts per action; each within the existing argument-value size limit.
- **Agent**: workers and the reasoning sandbox run Claude Code, which understands the three built-in commands. Other agents can switch the built-ins off.
- **Run frame**: only the action prompt carries the orchestrator's run frame and completion-report instruction; pre/post prompts are sent bare so slash commands work. Because a bare step's first character decides whether it is a slash command or a shell escape, it must be the author's (FR-204). Values in the rest of the step are delivered inside one bracketed paste, so a newline in a value cannot submit a second prompt.
- **Timed events and MCP calls** create runs exactly as today; the sequence comes from the action, not the caller.
- **No change** to queueing, capacity, retention, or how a worker is chosen.

## Out of Scope

- Per-call overrides of the sequence (e.g. a caller choosing the model).
- Per-orchestrator or global defaults other than the fixed defaults above.
- Conditional steps (e.g. run a post-prompt only on failure).
