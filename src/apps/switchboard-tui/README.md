# @tms/switchboard-tui — Switchboard TUI client

The Bubble Tea terminal client (`switchboard`). Connects to the local daemon over
its Unix socket and presents the sandbox manager: a sandbox list and a launch
wizard (duplicate-seeded fan-out, stop/restart/destroy/rename).

## Run

```bash
switchboard   # connects to $SWITCHBOARD_LOCAL_SOCKET; start `switchboardd serve` first
```

Keys: `n` launch · `s` stop · `S` restart · `d` destroy · `R` rename · `r` refresh ·
`j/k` navigate · `q` quit.

Configuration is read once at startup via `internal/config` (see `.env.example`).

## Layout & testing

- The daemon is reached through the `ui.Daemon` interface (`*client.Conn` implements
  it), so the UI is unit-tested with a fake and via `teatest` golden/interaction
  tests — the Go analog of Storybook interaction tests. `teatest` substitutes for the
  JS visual stack; the PTY/`vhs` E2E harness (Rule VI's justified Playwright
  substitute for a terminal UI) lives in the sibling `switchboard-tui-e2e` package.
- Tests are colocated `_test.go` siblings (see the daemon README for the rationale).
- `make cover` enforces the **90% coverage floor** (Rule VI), excluding generated
  stubs, the `cmd/` entrypoint, and the E2E package.

See the daemon README for the full list of justified constitution deviations.
