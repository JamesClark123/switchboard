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

The TUI tests drive two fake hosts with orchestrators in every run state and assert the Overview sections, the 15-item history cap, the Queue filter/pagination, grouped Actions/Events, the Daemons › Orchestrators flow and the removed top-level screen.

## Manual scenarios

1. **Navigation** — `shift+→` from Sandboxes: Daemons, then Jobs, then back to Sandboxes. There is no Orchestrators screen.
2. **Overview** — open Jobs: Upcoming lists both timed events with next firing times; queue a call on each orchestrator; they appear under Queued, then In progress, labelled `name @ host`. After more than 15 runs finish, History shows only the latest 15.
3. **Actions from Overview** — cancel a queued run (`x`); mark an awaiting-review worker free (`m`); press `n` on an Upcoming event and see a new queued run (and the event's next firing time unchanged); `o` on a completed run with a long report shows the whole report, scrollable.
4. **Queue tab** — with more than 25 runs, page with `]`/`[`; `f` filters to one orchestrator; `F` clears it.
5. **Actions / Events tabs** — groups per orchestrator; add an action to the second orchestrator (`a` with the cursor in its group), call it (`enter`); toggle an event (`space`).
6. **Daemons › Orchestrators** — Daemons → a daemon → Orchestrators: create, rename, open; the detail shows Workers, Reasoning, Hosts only; attach a worker, rebuild the reasoning sandbox, add a host.
7. **Partial availability** — disconnect the remote daemon: Jobs still shows the local orchestrators and names the unreachable host.
