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

---

### Edge Cases

- **Agent reports completion during a pre-prompt**: a completion report before the action prompt has been delivered is refused and changes nothing; the sequence continues.
- **Agent goes idle after the action prompt without reporting**: unchanged from today (009 FR-156) — the run becomes *awaiting review*; post-prompts are **not** sent until the developer marks the worker free, and then they are (FR-214).
- **Run cancelled while queued**: nothing was delivered, so no pre- or post-prompts are sent.
- **Worker's agent exits, or the sandbox stops or is destroyed, during a pre-prompt**: the run fails with the reason named (it never reached its action prompt); no further steps are sent.
- **…during the action prompt**: unchanged from today.
- **…during a post-prompt**: the run stays *completed* with its summary; the remaining post-prompts are dropped and the worker is reported as needing the developer (it may still hold the old session).
- **A post-prompt makes the agent ask for input**: the run is already completed; the worker is shown as needing input (as a worker parked needs-input is today) and is not free until it is idle again and its remaining post-prompts have been sent, or the developer frees it.
- **A step never finishes** (the agent stays busy far longer than any reasonable step): it is treated like any long-running prompt — no timeout ends a step on its own; the developer sees which step the run is on (US3) and can act on the worker.
- **Developer types in the worker's terminal between steps**: developer activity takes precedence (009 FR-119): the next step is sent only once the agent is idle again.
- **Orchestrator or daemon restarts mid-sequence**: the run resumes from the step it was on (a step that had been sent and not confirmed finished is not re-sent if the agent has since gone idle); no step is sent twice to a settled agent.
- **Action edited while one of its runs is mid-sequence**: the in-flight run keeps the lists it was created with (009 FR-140).
- **Empty or whitespace-only custom prompt**: refused at save with the offending entry named.
- **Custom prompt that composes to nothing** (e.g. `{branch}` alone, with `branch` optional and not supplied): skipped for that run; the sequence moves to the next step.
- **Placeholder in a pre/post prompt names an undeclared argument**: refused at save with the placeholder named, as for the body.
- **Built-in step text in a custom list** (e.g. a custom pre-prompt `/clear`): allowed; it is sent as written in its position. The editor does not de-duplicate.
- **Worker's agent does not understand a built-in step** (e.g. an agent other than Claude Code): the step is sent as text like any other; the developer switches the built-ins off for such actions.

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
- **FR-204**: Saving an action MUST validate the new fields and refuse a save with the problem named when: a custom prompt is empty or whitespace-only; a list has more than **10** entries; a custom prompt exceeds the size limit that applies to argument values (009 FR-140); a built-in value is not one of its listed options; a custom prompt names an undeclared argument in a placeholder (the placeholder named, as for the body).

#### Sequence

- **FR-205**: When a run is delivered, the orchestrator MUST send its steps in this order: Model (if enabled), Effort (if enabled), the custom pre-prompts in order, the **action prompt** (composed and framed exactly as today), then — per FR-214 — the custom post-prompts in order, then Clear (if enabled).
- **FR-206**: The orchestrator MUST send a step only when the sandbox's agent has **finished** the previous step — idle and ready for input — and no developer activity is in progress (009 FR-119). The first step is sent under today's delivery rule (the sandbox is free, 009 FR-117). No timeout ends a step: a step that stays busy is shown as the current step (FR-213) and left to the developer.
- **FR-207**: A run MUST snapshot its sequence **at call time**, together with its composed prompt: the built-in choices and each custom pre/post prompt with its placeholders replaced by the call's values (every occurrence; an optional argument not supplied takes its default, otherwise its placeholders are replaced by nothing — 009 FR-139). Supplied arguments that no placeholder references — in the body or in any custom prompt — are appended **only to the action prompt**, never to a pre/post prompt. A custom prompt that composes to empty or whitespace-only text is skipped (not sent). Later edits to the action MUST NOT change a run already created (as 009 FR-140).
- **FR-208**: A run with no enabled built-in steps and no custom prompts MUST behave exactly as runs do today.

#### Run and worker state

- **FR-209**: While pre-prompts are being sent the run MUST be *running* (or *awaiting input* while the agent asks for input), as from delivery today. Idle between pre-prompts MUST NOT make the run *awaiting review*; only idle without a report **after the action prompt** does (009 FR-156).
- **FR-210**: Actions that exist when this feature is installed MUST be treated as having all three built-in steps enabled with the defaults (Opus, medium, Clear) and empty custom lists, and MUST show so in their editor.
- **FR-211**: A completion report received before the action prompt has been delivered MUST be refused and change nothing.
- **FR-212**: If the agent exits, or the sandbox stops or is destroyed, before the action prompt is delivered, the run MUST become *failed* with a reason naming the step it was on.
- **FR-213**: The run MUST record which step it is on (setting up *n*/*m*, action, cleaning up *n*/*m*), and the TUI MUST show it wherever the run's state is shown (Jobs screen rows, orchestrator views) and on the worker while it cleans up.
- **FR-214**: Post-prompts MUST be sent after the run is closed by the agent's **completion report**, and after the developer **marks the worker free** from *awaiting review* (009 FR-156) — in both cases once the agent is idle. They MUST NOT be sent for a run cancelled before delivery or for a run that failed before its action prompt was delivered.
- **FR-215**: Closing the run (completion report or mark free) MUST happen when it does today — the run's outcome and summary do not wait for post-prompts. The **worker** MUST NOT be free (009 FR-117) until its post-prompts have all been sent and the agent is idle after the last one.
- **FR-216**: If a post-prompt makes the agent ask for input, the worker MUST be shown as needing input (as a parked needs-input worker is today) and receive no new run; the remaining post-prompts continue once the agent is idle. If the agent exits or the sandbox stops during post-prompts, the remaining post-prompts MUST be dropped and the worker shown as needing the developer.
- **FR-217**: An orchestrator restart MUST resume each in-flight sequence at the step it was on, without sending any step twice to an agent that has already finished it.

#### Editing and visibility

- **FR-218**: The action create and edit forms (Jobs › Actions and the orchestrator's Actions view) MUST let the developer: switch each built-in step on or off; choose the Model and Effort values from their lists; add, edit, reorder and remove custom pre- and post-prompts.
- **FR-219**: The action's details as shown to the developer MUST summarise its sequence (e.g. `pre: /model opus · /effort medium · 1 custom — post: 1 custom · /clear`).
- **FR-220**: A run's prompt overlay MUST show its full snapshotted sequence in order: pre-prompts, the composed action prompt, post-prompts.
- **FR-221**: The actions offered to workers' agents through the orchestrator's tools (009 FR-151) MUST NOT expose pre/post prompts as arguments; calling an action is unchanged.

### Key Entities

- **Action** (extended): gains *custom pre-prompts* (ordered list of text, may contain `{name}` placeholders), *custom post-prompts* (same), and *built-in steps*: Model (enabled, choice), Effort (enabled, level), Clear (enabled).
- **Run** (extended): gains its *sequence snapshot* (the steps it will send, in order, placeholders already filled) and its *current step* (setting up *n*/*m*, action, cleaning up *n*/*m*, done).
- **Worker** (extended state): may be *cleaning up* after a closed run — not free, with the run it is cleaning up after.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: With the defaults untouched, 100% of runs start with the selected model and effort and end with the agent's session cleared before the worker takes its next run.
- **SC-002**: A developer can add a pre-prompt and a post-prompt to an existing action, and switch a built-in step off, in under 1 minute from the action list.
- **SC-003**: In 100% of runs, the action prompt is delivered only after every pre-prompt finished, and no post-prompt is delivered before the run's completion report (or the developer's mark free).
- **SC-004**: No worker receives a new run while any post-prompt of its previous run is still pending.
- **SC-005**: An action with every built-in step off and no custom prompts produces a run indistinguishable from today's.
- **SC-006**: At any moment during a run, the developer can tell from the Jobs screen which step the run is on without opening the worker's terminal.

## Assumptions

- **Built-in step text**: the Model step sends the agent's model-switch command with the chosen model (`/model opus`, `/model fable`, `/model sonnet`, `/model haiku`); Effort sends `/effort <level>`; Clear sends `/clear`. The exact model identifiers are confirmed against the agent during planning.
- **"Finished" means idle again**: a step is finished when the agent is idle and ready for input — the same signal the orchestrator already uses for delivery and awaiting review. ⚠️ **Risk for planning**: built-in slash commands (`/model`, `/effort`, `/clear`) are handled by the agent locally and may not produce the same busy → idle signals as a model turn; the plan must confirm, on a real worker, how each is detected as finished (and fall back to a short bounded settle wait if no signal arrives).
- **No timeout** ends a step automatically; a stuck step is visible (FR-213) and handled by the developer, as a stuck action prompt is today.
- **Limits**: at most 10 custom pre-prompts and 10 custom post-prompts per action; each within the existing argument-value size limit.
- **Agent**: workers and the reasoning sandbox run Claude Code, which understands the three built-in commands. Other agents can switch the built-ins off.
- **Run frame**: only the action prompt carries the orchestrator's run frame and completion-report instruction; pre/post prompts are sent bare so slash commands work.
- **Timed events and MCP calls** create runs exactly as today; the sequence comes from the action, not the caller.
- **No change** to queueing, capacity, retention, or how a worker is chosen.

## Out of Scope

- Per-call overrides of the sequence (e.g. a caller choosing the model).
- Per-orchestrator or global defaults other than the fixed defaults above.
- Conditional steps (e.g. run a post-prompt only on failure).
