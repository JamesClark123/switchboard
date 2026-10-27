# Quickstart: validating the Jobs screen (010)

Validation guide; see [contracts/jobs-ui.md](./contracts/jobs-ui.md) for keys and [data-model.md](./data-model.md) for ordering rules.

## Prerequisites

- A build of `sxb` and `sxbd` from this branch (`make build`), a local daemon, and ideally one remote daemon reachable over SSH.
- Two orchestrators (one per daemon if possible), each with at least one worker, one action and one enabled timed event (short interval, e.g. 1 minute).

## Automated checks

```bash
make fmt-check vet test     # unit + TUI tests (fake daemons) for the new screens
make cover                  # ≥90% per module
```

The TUI tests drive two fake hosts with orchestrators in every run state and assert the Overview sections, the 15-item history cap, the Queue filter/pagination, grouped Actions/Events, the Daemons › Orchestrators flow and the removed top-level screen, plus the review-driven rules: sanitised rows and overlay (FR-029), operations refused on a disconnected host (FR-030), identity-anchored cursors, dropped out-of-order replies, coalesced reloads, a slow host not blocking others, and the results overlay fitting the terminal.

## Manual scenarios

1. **Navigation** — `shift+→` from Sandboxes: Daemons, then Jobs, then back to Sandboxes. There is no Orchestrators screen.
2. **Overview** — open Jobs: Upcoming lists both timed events with next firing times; queue a call on each orchestrator; they appear under Queued, then In progress, labelled `name @ host`. After more than 15 runs finish, History shows only the latest 15.
3. **Actions from Overview** — cancel a queued run (`x`); mark an awaiting-review worker free (`m`); press `n` on an Upcoming event and see a new queued run (and the event's next firing time unchanged); `o` on a completed run with a long report shows the whole report, scrollable.
4. **Queue tab** — with more than 25 runs, page with `]`/`[`; `f` filters to one orchestrator; `F` clears it.
5. **Actions / Events tabs** — groups per orchestrator; add an action to the second orchestrator (`a` with the cursor in its group), call it (`enter`); toggle an event (`space`).
6. **Daemons › Orchestrators** — Daemons → a daemon → Orchestrators: create, rename, open; the detail shows Workers, Reasoning, Hosts only; attach a worker, rebuild the reasoning sandbox, add a host.
7. **Partial availability** — disconnect the remote daemon: Jobs still shows the local orchestrators and names the unreachable host; its rows stay dimmed and `x`/`n` on them say it is not connected (nothing reaches the local daemon). Reconnect it with Jobs open: its rows return without `r`. With Daemons › <remote> › Orchestrators open, disconnect it: the screen returns to Daemons.
8. **Escape sequences** — have a worker report a summary containing `\x1b]52;c;aGVsbG8=\x07`, `\x1b[2J` and `\r`: plus a U+202E (right-to-left override), a tab-indented code block and several lines: the History row and Open results show plain text; the screen, title and clipboard are unchanged, nothing spills into another column, line breaks are kept and indented lines stay inside the overlay border.
9. **Cursor stability** — highlight an Upcoming event, let another event fire (the list re-sorts): the highlight stays on the same event and `n` enqueues it.
10. **Key behaviour** — on Jobs, `esc` goes to Sandboxes and `q` quits; shift+→ on Jobs wraps to Sandboxes.

## Capability inventory (FR-027 / SC-005)

Every operation of the removed Orchestrators screen and its six-view detail, and where it lives
now. Each row is covered by an automated test (file in brackets, under
`src/apps/switchboard-tui/internal/ui/`).

| Operation (feature 009) | Where it was | Where it is now |
|-------------------------|--------------|-----------------|
| List orchestrators of every daemon (reachability, workers, queue depths) | Orchestrators screen | Daemons › *daemon* › Orchestrators, per daemon [`daemon_orchestrators_test.go`]; every orchestrator across daemons on Jobs [`jobs_test.go`] |
| Create (`n`), rename (`R`), remove (`d`, confirmed) | Orchestrators screen | Daemons › *daemon* › Orchestrators — create targets that daemon [`daemon_orchestrators_test.go`] |
| Open an orchestrator (`enter`) | Orchestrators screen | Daemons › *daemon* › Orchestrators [`daemon_orchestrators_test.go`] |
| Workers: detach (`x`), mark free (`m`, incl. a parked worker) | Detail › Workers | Detail › Workers, unchanged [`orchestrator_attach_test.go`, `orchestrator_runs_test.go`] |
| Actions: add (`a`), edit (`e`), delete (`d`), call (`enter`) | Detail › Actions | Jobs › Actions, on the cursor's / highlighted item's orchestrator [`orchestrator_runs_test.go`, `jobs_groups_test.go`] |
| Queue: show prompt (`enter`), cancel queued (`x`), mark free (`m`) | Detail › Queue | Jobs › Overview and Jobs › Queue [`orchestrator_runs_test.go`, `jobs_overview_test.go`, `jobs_queue_test.go`] |
| Queue: read a finished run's result | Detail › Queue (one truncated line) | Jobs › Overview / Queue `o` Open results — full text, scrollable [`run_results_test.go`] |
| Events: add, edit, delete, on/off (`space`) | Detail › Events | Jobs › Events [`orchestrator_runs_test.go`, `jobs_groups_test.go`] |
| Events: Enqueue now (`n`) with dedupe, dedupe-off confirm, in-flight guard, repeat window | Detail › Events | Jobs › Events and Jobs › Overview › Upcoming, one implementation [`orchestrator_runs_test.go`, `jobs_overview_test.go`] |
| Reasoning: edit setup (`e`), rebuild (`b`), output (`l`) | Detail › Reasoning | Detail › Reasoning, unchanged [`orchestrator_reasoning_test.go`] |
| Hosts: add from my hosts (`a`), remove (`d`) | Detail › Hosts | Detail › Hosts, unchanged [`orchestrator_views_test.go`] |
| Live refresh on `orchestrator_changed` | Orchestrators screen + detail | Daemons › Orchestrators, the detail, and Jobs (that orchestrator only) [`daemon_orchestrators_test.go`, `jobs_test.go`] |
| Sandbox list `m` mark free (dialog over the detail, back to the list) | Detail | Detail (reached under Daemons › Orchestrators), unchanged [`orchestrator_runs_test.go`] |
| Sandbox list `o` attach / detach; launch wizard `O` | Orchestrator rows | Same rows (the shared loader) [`orchestrator_attach_test.go`, `launch_orch_test.go`] |
| Unreachable orchestrators kept with the reason; `R`/`d` refused on them | Orchestrators screen | Daemons › Orchestrators [`daemon_orchestrators_test.go`]; Jobs names the unreadable daemons [`jobs_test.go`] |

Nothing on the old screen is without a home in the new layout.
