# UI Contract: Jobs Screen & Daemons › Orchestrators (010)

The TUI's user-facing interface for this feature: screens, tabs, rows and keys. No wire/proto contract changes (research R2).

## Top level

| Key | Effect |
|-----|--------|
| `shift+→` / `shift+←` | cycle **Sandboxes → Daemons → Jobs**, wrapping at both ends (a deliberate change: today the cycle stops at the last screen, research R8); header strip names the three screens with the current one highlighted; the help footer names the wrapped neighbour |

The **Orchestrators** top-level screen no longer exists.

## Jobs screen

Tab bar (same look as the Sandboxes group tabs): **Overview · Actions · Events · Queue**. Overview is selected the first time Jobs opens; the last tab is remembered for the session.

### Keys common to all Jobs tabs

| Key | Effect |
|-----|--------|
| `←` / `→` | previous / next tab |
| `↑`/`k`, `↓`/`j` | move the cursor |
| `r` | reload from all connected daemons (no-op for a daemon whose load is still running) |
| `,` | settings |
| `esc` | back to Sandboxes (as on every top-level screen today) |
| `q` / `ctrl+c` | quit (as on every top-level screen today) |

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
Upcoming     nightly         → triage      beta-bot @ build-box  next in 9m · next firing skipped — run open
Queued       #1 review-pr    timed poll    alpha-bot @ local     received 12s ago
In progress  review-pr       running       beta-bot @ build-box  on worker-wa · 2m
History      review-pr       completed     alpha-bot @ local     ended 5m ago — Reviewed PR 412 and…
```

Columns are padded and truncated to fixed display widths (long names end in `…`); the History snippet takes the remaining width. `on <worker>` is the worker sandbox's display name. Snippets, reasons and origin labels are sanitised single lines (FR-029). The cursor stays on the same item when rows reorder or move between sections after a refresh; if the item is gone it moves to its nearest neighbour (FR-031, research R11).

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
| `f` | cycle the orchestrator filter: all → `alpha-bot @ local` → `beta-bot @ build-box` → … → all, in group order (FR-015) |
| `F` | clear the filter (all orchestrators) |
| `]` / `pgdown` | next page |
| `[` / `pgup` | previous page |
| `enter` / `x` / `m` / `o` | as in Overview |

Header: `filter: <all | name @ host> · page N of M (K runs)`.

### Open results overlay

Shows action, `orchestrator @ host`, state, end time, then the full reported result (`closed by developer: <note>` when closed by hand; the failure/cancel reason for failed/cancelled runs; "no result was reported" when empty). `↑`/`↓`/`pgup`/`pgdown` scroll; `esc` closes. All text is sanitised (control and escape sequences and bidi/invisible formatting characters removed; line breaks kept, carriage returns shown as line breaks, tabs expanded to spaces; the terminal's screen, cursor, colours, title, hyperlinks and clipboard are never changed: FR-029). The overlay fits the terminal: content is wrapped to the overlay's inner width (after padding), the scroll indicator and `esc close` hint stay visible, and it re-fits when the terminal is resized.

### Empty / partial states

- No orchestrators: "No orchestrators yet — create one under Daemons › <daemon> › Orchestrators."
- Unreachable daemons: a dim line "could not read: <host> (<error>), <orchestrator> @ <host> (runs: <error>)" above the content, on every tab — daemons whose connection failed or was lost (event stream ended, `Unavailable`), daemons whose listing failed, and orchestrators whose runs failed to load. Errors are sanitised single lines, truncated by display width (FR-029). Daemons you disconnected or never connected are not listed, and their rows are not shown (FR-004). A lost daemon's rows stay, dimmed; acting on them shows "<host> is not connected" or "<host>: connection lost — r on Jobs retries" (FR-030). `r` on Jobs retries a lost daemon, and so do opening its Daemons › Orchestrators (which stays open for a lost daemon, its operations refused until the retry succeeds) and `r` there. On Actions and Events a lost daemon's group headings say "(connection lost)", a disconnected one's "(not connected)". Every row is cut to the body width.
- Still loading: a dim "connecting: <host>" / "loading: <host>" while a daemon is connecting or has not answered; other daemons' rows are already shown and navigable (SC-007).

## Daemons › Orchestrators

On the Daemons screen, a connected daemon's options are **MCP gateway · Secrets · Orchestrators**.

### Orchestrators list (one daemon)

| Key | Effect |
|-----|--------|
| `n` | create an orchestrator on this daemon (the machine is fixed to this daemon; no machine picker) |
| `R` | rename the highlighted orchestrator |
| `d` | remove it (confirmed) |
| `enter` | open it |
| `r` | reload |
| `shift+→` / `shift+←` / `,` | screen cycle / settings, as on the old Orchestrators screen |
| `esc` | back to Daemons |
| `q` / `ctrl+c` | quit |

Offered only for a connected daemon. If the daemon disconnects while this list (or a form on it) is open, the screen returns to Daemons with a status note; a form submitted after a disconnect is refused with "<host> is not connected", never sent to another daemon (FR-030).

### Orchestrator detail

Views (`←`/`→`): **Workers · Reasoning · Hosts**, each with today's keys (Workers: `x` detach, `m` mark free; Reasoning: `e` edit setup, `b` rebuild, `l` output; Hosts: `a` add from my hosts, `d` remove). `esc` returns to where the detail was opened from: the daemon's Orchestrators list, or the sandbox list when reached from the launch wizard's reasoning flow or the sandbox list's mark-free dialog.

### Key hints

Every Jobs tab (row-sensitive on Overview and Queue), the Open results overlay, Daemons › Orchestrators and the reduced detail list exactly the keys above (FR-028).
