# @tms/switchboardd — Switchboard daemon

The per-host daemon (`sxbd`). Manages docker sandbox lifecycle, owns the
controlled workspace folder for **verbatim duplicates**, persists a bbolt sandbox
registry (re-adopted on restart), and serves the gRPC contract over a Unix socket.

## Run

```bash
sxbd serve            # listen on $SWITCHBOARDD_SOCKET
sxbd serve --debug    # …also log every RPC action and error to stderr
sxbd dial-stdio       # bridge stdio <-> the local socket (SSH remoting; US3)
```

Configuration is read once at startup via `internal/config` (see `.env.example`).

## Constitution deviations (justified — see specs/.../plan.md Complexity Tracking)

This module uses the **Go toolchain** rather than the constitution's TS/Biome/Vitest
stack, because the system is a Bubble Tea TUI + Go daemon. Concretely:

- **Tests are colocated `_test.go` siblings**, not a `__tests/` subdir — Go's test
  tooling discovers tests package-locally (Rule IV's "documented per-package layout"
  clause).
- **Formatting/lint**: `gofmt` + `golangci-lint` substitute for Biome.
- **Coverage**: `go test -coverprofile` with the **90% floor preserved** (Rule VI),
  enforced by `make cover`. Generated stubs (`/gen`), entrypoint `cmd/` mains, and
  E2E packages are narrowly excluded from measurement.
- **No Dockerfile/compose**: the daemon is a host-level process that drives the
  host's Docker, filesystem, and sshd; containerizing it is self-defeating (Rule VII
  carve-out). Distribution is a compiled binary.
- **Env discipline**: `internal/config` is the single typed, validated config surface
  parsed at startup; `.env.example` is kept in lockstep by an in-language `env:check`
  test (Rule VIII intent).

## Duplication semantics (research R5)

Verbatim copy of every selected file. Defaults: **symlinks copied as-is** (not
dereferenced), **mode bits preserved**, non-regular files (FIFOs/sockets/devices)
skipped. Sources are opened read-only; nothing is written outside the workspace root.

**Editing seeded folders in place (feature 007).** `AddSandboxSources` copies (or clones) each
new source into the workspace's own `.switchboard/staging/<folder>` and renames it to
`<workspace>/<folder>` only once *every* source in the request has staged — a same-filesystem
rename, so an agent live in the workspace never observes a partial tree. Any failure removes the
staging area plus the folders that operation had already renamed, leaving the record and the
sandbox state untouched (a failed add never ERRORs a sandbox). `RemoveSandboxSources` deletes
only the workspace *child* (the bind-mount root is never disturbed, which is why — unlike a
refresh — nothing has to stop); on a mid-batch failure the record follows the disk. A per-sandbox
try-acquire latch serializes add / remove / refresh, and leftover staging debris from a daemon
crash is purged on startup.

## Residual risk (research R6)

`internal/sandbox/runner.go` (`SbxRunner`) encodes the assumed `sbx` subcommand
surface (`create/stop/start/rm/status/clone`). The exact CLI was unverified in the
dev environment and MUST be reconciled against a real `sbx`.
