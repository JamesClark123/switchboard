# UI Contract: Jobs Screen & Daemons › Orchestrators (010)

The TUI's user-facing interface for this feature: screens, tabs, rows and keys. No wire/proto contract changes (research R2).

## Top level

| Key | Effect |
|-----|--------|
| `shift+→` / `shift+←` | cycle **Sandboxes → Daemons → Jobs** (wraps); header strip names the three screens with the current one highlighted |

The **Orchestrators** top-level screen no longer exists.

## Jobs screen

Tab bar (same look as the Sandboxes group tabs): **Overview · Actions · Events · Queue**. Overview is selected the first time Jobs opens; the last tab is remembered for the session.

### Keys common to all Jobs tabs

| Key | Effect |
|-----|--------|
| `←` / `→` | previous / next tab |
| `↑`/`k`, `↓`/`j` | move the cursor |
| `r` | reload from all connected daemons |
| `esc` / `q` | leave Jobs (back to the previous top-level screen) |

### Overview

Sections in order: **Upcoming**, **Queued**, **In progress**, **History (latest 15)**. Every row shows `orchestrator @ host`.

| Row | Key | Effect |
|-----|-----|--------|
| upcoming event | `n` | Enqueue now (SWI-4 behaviour: dedupe, confirmation when dedupe is off and a run is open, repeat ignored, schedule unchanged) |
| any run | `enter` | show the run's composed prompt |
| queued run | `x` | cancel the run |
| run awaiting review | `m` | mark the worker free (outcome + note dialog) |
| finished run | `o` | Open results |

Row formats (illustrative):

```
Upcoming     poll            → review-pr   alpha-bot @ local     next in 3m
Queued       #1 review-pr    timed poll    alpha-bot @ local     received 12s ago
In progress  review-pr       running       beta-bot @ build-box  on worker-wa · 2m
History      review-pr       completed     alpha-bot @ local     ended 5m ago — Reviewed PR 412 and…
```

### Actions

Groups: one heading per orchestrator (`alpha-bot @ local`), its actions beneath; empty groups show "no actions — press a to add one".

| Key | Effect |
|-----|--------|
| `a` | add an action to the cursor's orchestrator |
| `e` | edit the highlighted action |
| `d` | delete the highlighted action (confirmed) |
| `enter` | call the highlighted action (argument form) |

### Events

Groups as for Actions.

| Key | Effect |
|-----|--------|
| `a` | add a timed event to the cursor's orchestrator |
| `e` / `d` | edit / delete (confirmed) the highlighted event |
| `space` | enable / disable |
| `n` | Enqueue now |

### Queue

Every run the daemons keep (queued, in progress, history), no cap, paginated.

| Key | Effect |
|-----|--------|
| `f` | cycle the orchestrator filter: all → `alpha-bot @ local` → `beta-bot @ build-box` → … → all |
| `F` | clear the filter (all orchestrators) |
| `]` / `pgdown` | next page |
| `[` / `pgup` | previous page |
| `enter` / `x` / `m` / `o` | as in Overview |

Header: `filter: <all | name @ host> · page N of M (K runs)`.

### Open results overlay

Shows action, `orchestrator @ host`, state, end time, then the full reported result (`closed by developer: <note>` when closed by hand; the failure/cancel reason for failed/cancelled runs; "no result was reported" when empty). `↑`/`↓`/`pgup`/`pgdown` scroll; `esc` closes.

### Empty / partial states

- No orchestrators: "No orchestrators yet — create one under Daemons › <daemon> › Orchestrators."
- Unreachable daemons: a dim line "could not read: <host>, <host>" above the content.

## Daemons › Orchestrators

On the Daemons screen, a connected daemon's options are **MCP gateway · Secrets · Orchestrators**.

### Orchestrators list (one daemon)

| Key | Effect |
|-----|--------|
| `n` | create an orchestrator on this daemon |
| `R` | rename the highlighted orchestrator |
| `d` | remove it (confirmed) |
| `enter` | open it |
| `esc` | back to Daemons |

### Orchestrator detail

Views (`←`/`→`): **Workers · Reasoning · Hosts**, each with today's keys (Workers: `x` detach, `m` mark free; Reasoning: `e` edit setup, `b` rebuild, `l` output; Hosts: `a` add from my hosts, `d` remove). `esc` returns to the daemon's Orchestrators list.
