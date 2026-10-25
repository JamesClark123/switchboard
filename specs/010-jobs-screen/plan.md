# Implementation Plan: Jobs Screen & Orchestrator UI Reorganisation

**Branch**: `jamesloganclark/swi-5-feature-updates-to-orchestrator-ui` (spec) → `…-tasks` (plan + tasks) → `…-implement` | **Date**: 2026-10-25 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/010-jobs-screen/spec.md`

## Summary

Replace the **Orchestrators** top-level screen with a **Jobs** screen whose four tabs — Overview, Actions, Events, Queue — aggregate every orchestrator on every connected daemon, and move orchestrator administration (create/rename/remove; Workers, Reasoning, Hosts) to a new **Orchestrators** option under each daemon on the **Daemons** screen. The change is **TUI-only**: the existing per-host RPCs (`ListOrchestrators`, which already carries each orchestrator's actions and timed events; `ListRuns`; `CancelRun`; `MarkWorkerFree`; `FireTimedEvent`; action/event CRUD) and the existing per-host `Event.orchestrator_changed` stream provide everything; the client fans out per host and merges. No proto, daemon, storage or environment change. Today's per-orchestrator Actions/Queue/Events code is refactored to act on an explicit target orchestrator so the Jobs tabs and the remaining detail views share one implementation, hosted per screen; untrusted text is sanitised, operations never fall back to another daemon, and live updates are ordered and coalesced (research R1–R12).

## Technical Context

**Language/Version**: Go 1.26 (workspace `go.work`; module `src/apps/switchboard-tui`)

**Primary Dependencies**: Bubble Tea, Bubbles (`list`, `textinput`, `viewport` — viewport is new to this package but already part of the Bubbles dependency), Lip Gloss, `huh` forms; generated gRPC client `libs/switchboard-proto`

**Storage**: N/A (no persisted state added; Jobs state is in-memory per TUI session)

**Testing**: `go test` with the package's existing `fakeDaemon` / `teatest` patterns; `make fmt-check vet test cover` (≥90% per module)

**Target Platform**: Linux/macOS terminals (the TUI `sxb`)

**Project Type**: CLI/TUI client (single Go module change)

**Performance Goals** (SC-007: with 3 daemons × 10 orchestrators × 200 finished runs, cursor moves and tab switches respond within 100 ms; each daemon's data appears within 1 s of it answering, independently of slower daemons; a change re-reads only that orchestrator; no re-reads while Jobs is off screen):
- **Event reload.** An `orchestrator_changed` event reloads only that orchestrator (`GetOrchestrator` + `ListRuns`), not every orchestrator. Reloads are ordered by a per-orchestrator generation that host loads share (stale replies, including a slow host load's older part, are dropped; NotFound removes the orchestrator), coalesced to one in flight per orchestrator, and issued only while Jobs is visible; otherwise the orchestrator is marked stale and reloaded on entry (R3).
- **Full load.** One command per connected host, hosts in parallel, each with its own timeout: `ListOrchestrators`, then `ListRuns` per orchestrator, at most `jobsRunsParallel = 4` at a time. Each host's result is applied as it arrives, and a per-host in-flight guard stops loads stacking (R2).
- **Rendering.** Derived rows are cached per data revision, so cursor moves and tab switches never wait on a daemon or re-sort thousands of runs per frame (R4).

**Constraints**: All agent- or remote-supplied text is sanitised before display (FR-029, R10); operations go only to the hosting daemon and are refused when it isn't connected (FR-030, R12). No proto/daemon change; no new env vars (page size, history cap and refresh bounds are package constants); remote hosts reached only over the existing connections; daemon run retention unchanged (200 finished runs per orchestrator — spec FR-018)

**Scale/Scope**: Tens of orchestrators × up to ~200 retained runs each (a few thousand runs in the Queue tab, paginated). `ListRuns` returns each run's composed prompt, so a full load can be several MB; the gRPC 4 MB default receive limit per `ListRuns` reply is a known ceiling, recorded and not changed here (R2)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack. Features 001–009 are implemented in **Go**, a deviation recorded and justified in **001's Constitution Check + Complexity Tracking**. Feature 010 stays inside that deviation and **adds no new ones**:

| Principle | Status for 010 |
|-----------|----------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue of Biome, per 001). |
| III Type Safety | ✅ Go static typing; Jobs rows/tabs are typed structs and enums; no `interface{}` payloads. |
| IV Naming & Layout | ✅ New files in the existing `internal/ui` package with colocated `_test.go` (e.g. `jobs.go`, `jobs_overview.go`, `daemon_orchestrators.go`, `run_results.go`). |
| V Verification Before Merge | ✅ Same `make` gates (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`). |
| VI Multi-Level Testing | ✅ Unit tests for merge/sort/paginate/filter helpers; TUI behaviour via `fakeDaemon` + `teatest` (per-host fan-out with two fake hosts); ≥90% coverage held for `switchboard-tui`. Storybook/Playwright/MSW remain TS-only, out of scope under 001's deviation. |
| VII Containerized Deployment | ✅ N/A — client binary (001 carve-out). |
| VIII Env Discipline | ✅ No new env vars; constants only (R7). |
| Repository Structure | ✅ Changes confined to `src/apps/switchboard-tui`; plus spec/README/CLAUDE.md docs. |

**Result**: PASS. **Re-checked after Phase 1 design — still PASS**: the design adds TUI code and docs only; no new module, dependency outside Bubbles, deployable, proto field or env var.

## Project Structure

### Documentation (this feature)

```text
specs/010-jobs-screen/
├── plan.md              # This file
├── research.md          # Phase 0 — R1..R12 decisions
├── data-model.md        # Phase 1 — client-side view model (rows, tabs, ordering, pagination)
├── quickstart.md        # Phase 1 — validation guide
├── contracts/
│   └── jobs-ui.md       # Phase 1 — screen/tab/key contract (the TUI's user-facing interface)
└── tasks.md             # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
src/apps/switchboard-tui/internal/ui/
├── app.go                    # screens: screenJobs replaces screenOrchestrators; screenDaemonOrchs added;
│                             #   top-level cycle Sandboxes → Daemons → Jobs; header strip; Update routing
├── jobs.go                   # NEW: jobsState, tabs, fan-out load (jobsLoadCmd), event-driven reload, key routing
├── jobs_overview.go          # NEW: Overview sections (Upcoming/Queued/In progress/History≤15), per-row actions
├── jobs_queue.go             # NEW: Queue tab — flattened runs, orchestrator filter, pagination
├── jobs_groups.go            # NEW: Actions & Events tabs grouped by orchestrator (cursor → target orchestrator)
├── run_results.go            # NEW: Open results overlay (scrollable viewport over Run.summary/reason)
├── run_actions.go            # NEW (extracted): orchTarget, orchSurface, connectedDaemon; prompt / cancel / mark free / Enqueue now on an explicit target
├── sanitize.go               # NEW: sanitizeTerminalText / sanitizeLine (strip control & escape sequences; display-width truncation) — R10
├── daemon_orchestrators.go   # NEW: Daemons › Orchestrators list (create/rename/remove/open) for one daemon
├── daemons.go                # daemonOptions gains "Orchestrators" (connected daemons only)
├── orchestrators.go          # list loader kept (reused by Jobs + Daemons › Orchestrators); old screen code removed
├── orchestrator_views.go     # detail reduced to Workers / Reasoning / Hosts
├── orchestrator_forms.go     # action/event/call forms take an explicit orchTarget instead of m.orchDetail
├── notifications.go          # orchestrator_changed → reload one Jobs orchestrator (or mark stale); entry points retargeted (R8)
├── hosts.go                  # host connect/disconnect → Jobs per-host load / keep-as-unreachable; leave Daemons › Orchestrators on disconnect
└── *_test.go                 # tests per file above
README.md                     # Jobs screen + Daemons › Orchestrators docs; Orchestrators screen section replaced
CLAUDE.md                     # agent context: active feature 010
```

**Structure Decision**: Single-module TUI change inside `src/apps/switchboard-tui/internal/ui`, following that package's file-per-screen convention. Shared run/action/event operations are extracted into target-parameterised helpers (R1) so the Jobs tabs and the remaining orchestrator detail never diverge.

## Complexity Tracking

No constitution violations beyond the pre-recorded 001 Go deviation; nothing to justify.
