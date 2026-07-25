# Implementation Plan: Escape Hatch

**Branch**: `005-escape-hatch` | **Date**: 2026-07-23 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/005-escape-hatch/spec.md`

## Summary

Give a sandbox's AI a small, human-authored set of whole commands it may run **outside** the sandbox —
on the supervising daemon's host, in the sandbox's bind-mounted workspace — and deliver each result back
to the agent even with no terminal attached. Escape-hatch commands are authored as a new **structured,
switchboard-owned** section on a client kit (never inside the opaque Docker `spec.yaml`), attached with
the kit, and resolved per sandbox with later-kit-wins on name collisions. The AI invokes a command by
running a daemon-injected wrapper that POSTs the command **name** (never a string) to a new route on the
existing agent-hook HTTP server; a new `internal/escapehatch` daemon package validates the name against
the sandbox's persisted allowlist, runs the fixed string with a bounded timeout/output and cancellation
on sandbox stop, gates `requires-approval` commands behind a 5-minute developer-approval window
(deny-by-default), and pushes the outcome back into the agent via the existing `agent.Registry.Prompt`
PTY path (the same mechanism the `PromptAgent` RPC delegates to).
The client gains a kit-editor section, a `confirm.go`-style approval modal, and live run observability on
the sandbox list + inbox. Technical approach and all resolved unknowns are in
[research.md](./research.md); entities in [data-model.md](./data-model.md); wire additions in
[contracts/](./contracts/).

## Technical Context

**Language/Version**: Go (existing `go.work` monorepo: `switchboardd`, `switchboard-tui`,
`switchboard-proto`, + e2e/update modules). Node ≥22 / pnpm only for repo tooling, not this feature.

**Primary Dependencies**: gRPC (Unix socket, `dial-stdio` over SSH client-side); `google/protobuf`;
`net/http` (existing hook server); `os/exec` (`exec.CommandContext`); `creack/pty` (existing agent PTY);
Bubble Tea + `huh` (TUI); `bbolt` (registry); `yaml.v3` (kit sidecar). **No new third-party deps.**

**Storage**: bbolt sandbox registry persists the resolved `Sandbox.escape_hatch_commands` (additive proto
field, no migration). Escape-hatch **run records are in-memory only** (clarification Q2). Client kits gain
a `kits/<id>/escape-hatch.yaml` sidecar (the commands are **not** written into `spec.yaml`).

**Testing**: Go `testing` via `make test`/`cover` (≥90% per-module, Rule VI); host-`sbx` argv asserted
with stub scripts (feature 001 R6). No Vitest/Storybook/Playwright/MSW (TS-stack, deviated in 001).

**Target Platform**: Linux/macOS host running `switchboardd`; the command always runs on the daemon's own
host (the daemon serves one host; SSH is a client-side concern).

**Project Type**: CLI/daemon (Go) — TUI client + per-host daemon. Not a web app.

**Performance Goals**: Not latency-bound. Bounds that matter: default max run duration **30 min** (Q3),
approval window **5 min** (Q4), captured output cap **1 MiB** — all daemon constants (research R7).

**Constraints**: The escape hatch is an **allowlist of whole, fixed commands** — zero AI-supplied
arguments, no general host shell (SC-004). Delivery must survive terminal detachment (SC-005). Deny-by-
default on unknown names and on any non-approval input (SC-003). No new env vars (keeps Rule VIII surface
unchanged).

**Scale/Scope**: One new daemon package (`internal/escapehatch`), ~1 proto revision (additive), kit-editor
section + approval screen + run observability in the TUI, and injection wiring in the sandbox manager.
Builds on features 003 (agent hooks, PTY persistence, event/notification hub) and 004 (kits).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack
(Principles I–VIII, Tooling Standards). Features 001–004 are implemented in **Go**, a deviation recorded
and justified in **001's Constitution Check + Complexity Tracking** (a constitution amendment is
recommended there). Feature 005 stays inside that established deviation and **adds no new ones**:

| Principle | Status for 005 |
|-----------|----------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (Go analogue of the Biome gate, per 001). |
| III Type Safety | ✅ Go static typing; additive proto codegen. No `any`-equivalent escape. |
| IV Naming & Layout | ✅ `kebab-case` files, colocated `_test.go`; new package `internal/escapehatch` follows the `internal/<name>` convention 003/004 established. Escape-hatch command **names** are `kebab-case` (validated in-editor). |
| V Verification Before Merge | ✅ Same `make` gates as 001–004 (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`); the Husky fast subset applies. No `--no-verify`. |
| VI Multi-Level Testing | ✅ Go unit tests, ≥90% per-module coverage. Storybook/Playwright/MSW are TS-only and already out of scope under 001's deviation; host-`sbx` argv is stub-asserted. |
| VII Containerized Deployment | ✅ N/A — no package under `src/apps/` or `src/services/` in the constitutional sense; `switchboardd`/`sxb` are host binaries (001 deviation). No new deployable. |
| VIII Env Discipline | ✅ **No new env vars** (research R7): the three bounds are package constants. `env-check` surface unchanged; `.env.example` untouched. |
| Repository Structure | ✅ Additive within existing Go modules; no new top-level category. |

**Result**: PASS (within the pre-recorded 001 Go deviation; no new deviation). No entries required in the
Complexity Tracking table below. Re-checked after Phase 1 design — still PASS (the design adds only Go
code, an additive proto revision, and an in-memory run store; no new tooling, deployable, or env var).

## Project Structure

### Documentation (this feature)

```text
specs/005-escape-hatch/
├── plan.md              # This file
├── research.md          # Phase 0 — R1..R7 decisions
├── data-model.md        # Phase 1 — entities & proto placement
├── quickstart.md        # Phase 1 — validation guide
├── contracts/
│   ├── switchboard-escape-hatch.proto   # additive gRPC/proto revision doc
│   └── escape-hatch-http.md             # in-sandbox POST /escape-hatch/run + wrapper
├── checklists/
│   └── requirements.md  # spec quality checklist (from /speckit-specify)
└── tasks.md             # Phase 2 — created by /speckit-tasks (NOT here)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/
├── proto/switchboard.proto              # + EscapeHatchCommand/Run, ConsentMode, run-status enum,
│                                        #   DecideEscapeHatchRun/ListEscapeHatchRuns RPCs,
│                                        #   KitSpec.escape_hatch, Sandbox.escape_hatch_commands,
│                                        #   Event.escape_hatch_run, 2 NotificationKinds
└── gen/*.pb.go                          # regenerated via `make proto`

src/services/switchboardd/
├── internal/escapehatch/                # NEW package
│   ├── executor.go                      # sh -c in workspace_path; stream+bound; timeout; cancel registry
│   ├── runs.go                          # in-memory session-scoped run store (ring-trimmed)
│   ├── consent.go                       # approval gate: PENDING_APPROVAL + 5-min deny-by-default window
│   ├── resolve.go                       # merge attached kits' commands, later-kit-wins (Q1)
│   ├── inject.go                        # write/remove .switchboard/escape-hatch + CLAUDE.md rule block
│   ├── http.go                          # POST /escape-hatch/run handler (name-only allowlist check)
│   └── __tests/ (colocated *_test.go)   # per Rule IV
├── internal/agent/hooks.go              # (unchanged mechanism) — callback via agent.Registry.Prompt
├── internal/sandbox/manager.go          # extend bring-up injector (Launch/Refresh) to inject rule+wrapper
│                                        #   + store resolved escape_hatch_commands
├── internal/grpc/
│   ├── escapehatch_rpcs.go              # NEW: DecideEscapeHatchRun, ListEscapeHatchRuns
│   ├── server.go                        # wire escapehatch store; cancel runs in non-RUNNING teardown
│   └── subscribe.go                     # emit Event.escape_hatch_run + new NotificationKinds via Hub
└── cmd/sxbd/main.go                     # register /escape-hatch/run on the existing hook mux;
                                         #   construct escapehatch service (callback URL, agent.Registry.Prompt)

src/apps/switchboard-tui/
├── internal/store/kit.go                # + EscapeHatch []KitEscapeHatchCommand; sidecar Save/Get; ToSpec
├── internal/ui/kit_editor.go            # + secEscapeHatch itemized section + item huh form (approval bool)
├── internal/ui/approval.go              # NEW: screenApproval (confirm.go-style), DecideEscapeHatchRun cmd
├── internal/ui/sandbox_list.go          # run/awaiting-approval badge in sandboxTitle
├── internal/ui/notifications.go         # handle Event.escape_hatch_run + new NotificationKinds; inbox
├── internal/ui/keys.go                  # binding for the runs/approval action
├── internal/client/sandbox.go           # DecideEscapeHatchRun, ListEscapeHatchRuns client methods
└── internal/ui/app.go                   # Daemon iface + screenApproval routing + Model state
```

**Structure Decision**: Extend the existing Go daemon + TUI in place — the same shape features 003
(`internal/terminal`) and 004 (`internal/kit`) used. One new daemon package (`internal/escapehatch`) owns
execution, consent, run storage, resolution, injection, and the HTTP route; the proto revision is purely
additive; the client extends its existing kit-editor / confirm-modal / event-pump patterns. No new module,
no new top-level category, no new deployable.

## Complexity Tracking

> No Constitution Check violations beyond the pre-recorded 001 Go-stack deviation (which this feature does
> not widen). No new entries required.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none for 005)_ | — | — |
