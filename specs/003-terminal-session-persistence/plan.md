# Implementation Plan: Terminal Session Persistence & Sandbox Tags

**Branch**: `003-terminal-session-persistence` | **Date**: 2026-07-08 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/003-terminal-session-persistence/spec.md`

## Summary

Turn the daemon's currently-ephemeral per-sandbox PTY into a **persistent, multi-client terminal
session**. Today `sxbd` already spawns one `agent.Session` (a `creack/pty`-backed
`io.ReadWriteCloser`) per sandbox via `sbx exec -it`, and the client attaches over the gRPC
`AttachAgent` bidi stream. The gaps this feature closes are exactly the ones the research
(`docs/research/terminal-session-persistence.md`) predicts: a single raw reader consumes the PTY
bytes (no fan-out), a reconnecting client sees a **blank screen** (no snapshot/replay), nobody
tracks how many clients are attached, and detaching can tear the session down.

The technical approach follows the report's recommended **VT-snapshot** model. A new
`internal/terminal` package in the daemon wraps each `agent.Session` in a **broadcaster** that reads
the PTY once, feeds every byte into (a) a headless VT emulator holding current screen state +
(b) a bounded scrollback ring buffer, and fans live bytes out to N attached client channels. On
attach the broadcaster sends a **snapshot** (reconstructed screen for an immediate redraw) then
streams live output; the session stays alive across detach so in-flight AI prompts keep running with
no client attached. The client side gives the Bubble Tea TUI (`sxb`) an **in-place** terminal view
(no more stop-TUI / open / relaunch-TUI dance), a new `sxb attach` mode that external terminals and
the VSCode-workspace auto-open path both use, per-sandbox **attachment counts** on the list, a
**one-external-terminal-per-sandbox** guard with bring-to-front, and a mutable **tag** on each
sandbox (mirroring the existing `RenameSandbox` pattern). This builds entirely on feature 001's Go
workspace, gRPC contract, and daemon registry; it adds one contract revision and one daemon package,
no new modules.

## Technical Context

**Language/Version**: Go 1.26 (`go.work` multi-module workspace from feature 001). No new module —
changes land in the existing `switchboardd` service, `switchboard-tui` app, and `switchboard-proto`
lib.

**Primary Dependencies** (existing unless marked NEW):
- PTY: `github.com/creack/pty` v1.1.24 — already a dependency of `switchboardd` and the e2e app.
- Transport/contract: `google.golang.org/grpc` + `protobuf` — extend `switchboard.proto`.
- TUI: `github.com/charmbracelet/bubbletea` / `bubbles` / `lipgloss` — already present.
- **NEW — headless VT emulator**: `github.com/charmbracelet/x/vt` (pure-Go terminal emulator in the
  Charm ecosystem the project already uses) as the screen-state model **daemon-side** (snapshot
  source) and the renderer **client-side** (draw PTY bytes inside the Bubble Tea viewport).
  Fallback: `github.com/hinshun/vt10x` (the report's pure-Go muxing backend). See research.md R1.
- Daemon registry store: `go.etcd.io/bbolt` — already present; gains a `tag` field per sandbox.

**Storage**:
- **Terminal sessions are in-memory and ephemeral** — they live in the daemon process for the running
  lifetime of their sandbox (VT screen state + scrollback ring buffer + attachment set). They are
  intentionally NOT persisted to disk: stopping a sandbox ends its session (FR-006), and durability
  across a daemon crash is explicitly relaxed by the spec (edge case: "running work is never lost"
  but replay may be incomplete).
- **Tags are persisted** in the bbolt sandbox registry alongside `display_name`, so they survive TUI
  restarts and sandbox stop/restart (FR-024) and are visible to any connecting client.
- **Workspace marker**: each controlled workspace copy gets a small `.switchboard/session.json`
  marker (host id, sandbox id, socket descriptor) written at launch, so `sxb` can resolve "which
  sandbox owns this directory" by walking up from `cwd` (FR-017/018).

**Testing**:
- Unit/integration: `go test` + `testify`; broadcaster fan-out, snapshot correctness, scrollback
  bounding, resize arbitration, and single-external-attach rejection tested with a **fake Session**
  (the `Session` interface already supports this — see `agent/pty_test.go`).
- Contract: gRPC `AttachAgent` snapshot-then-stream and `SetSandboxTag` tested over an in-process
  socket.
- TUI: `teatest` golden tests for the in-place terminal view, list-page attachment count, and tag
  editing.
- E2E: the existing PTY/`vhs` harness (`switchboard-tui-e2e`, `switchboardd-e2e`) drives
  detach→reconnect-shows-prior-output and one-external-terminal against a real `sbx` sandbox.

**Target Platform**: Linux + macOS (same as 001). External-terminal spawning and bring-to-front are
platform-specific (research.md R5); Windows remains out of scope.

**Project Type**: Systems software — extends the per-host daemon + local terminal client from 001.
No web surface.

**Performance Goals** (from spec Success Criteria):
- In-TUI terminal open and return each < 2s and never restart the TUI (SC-003).
- Attachment count on the list correct within one refresh of attach/detach (SC-005).
- Snapshot-on-attach yields an immediate current-screen redraw (report's core rationale).

**Constraints**:
- One PTY has exactly one window size → a resize-reconciliation policy is mandatory when >1 client is
  attached (research.md R3: **smallest-of-attached** interactive clients).
- Detach MUST NOT `Close()` the session or interrupt in-flight work (FR-002); only sandbox stop ends
  it (FR-006).
- Scrollback is bounded (default 5 MB ring per session) so persistent sessions don't grow unbounded
  on a shared host daemon (research.md R2).

**Scale/Scope**: Single developer; tens of sandboxes per host; typically 0–2 clients attached per
session (in-TUI + optional external). In-memory session state is small (screen + bounded ring).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

**Source of truth**: `.specify/memory/constitution.md` is the ratified constitution (v2.3.1 per
`governance.md`); the binding rules are vendored under `.claude/rules/shared/`. This feature inherits
the **exact same platform-driven deviations** already recorded and justified in
`specs/001-sandbox-session-manager/plan.md` (Go toolchain in place of TypeScript/pnpm/Biome/Vitest/
Playwright/Docker-per-package; `gofmt`+`golangci-lint`; `go test` + 90% `coverprofile` floor;
`teatest`/PTY E2E; colocated `_test.go`; host-daemon distributed as a binary not an image). It does
**not introduce any new category of deviation**.

| Rule | Status for THIS feature | Notes |
|------|-------------------------|-------|
| Repository Structure (six categories, `src/<category>/<name>/`) | ✅ No change | All work lands in the existing `apps/switchboard-tui`, `services/switchboardd`, `libs/switchboard-proto`. One new daemon-internal package `internal/terminal`; no new module or category. |
| Rule I/II Formatting & Linting | ✅ (as adapted in 001) | `gofmt`/`goimports` + `golangci-lint`, same as the rest of the repo. |
| Rule III Type Safety | ✅ | Statically typed Go; raw PTY bytes handled as `[]byte`, not `any`. VT lib is typed. |
| Rule IV Naming & test layout | ✅ (as adapted in 001) | kebab-case dirs; colocated `_test.go` (Go requirement, per 001's documented deviation). |
| Rule V Verification Before Merge | ✅ | Same `go test ./...` + `golangci-lint` gate; new deps vendored via `go mod`. |
| Rule VI Testing Discipline (90% floor) | ✅ | New `internal/terminal` package and TUI views carry unit/integration tests to the preserved 90% `coverprofile` floor; detach/reconnect exercised at the PTY E2E layer. |
| Rule VII Containerized Deployment | ✅ (as adapted in 001) | No web-deployable surface added; the daemon remains a host process. |
| Rule VIII Env Var Discipline | ✅ | No new env vars anticipated (scrollback bound / resize policy are constants with sane defaults; if exposed, they go through 001's config package + `.env.example`). |
| Tooling Standards | ✅ (as adapted in 001) | Adds two candidate Go libraries (VT emulator); no toolchain change. |
| Governance | ✅ | No new conflicts to surface beyond 001's recommended amendment. |

**Gate decision**: PASS. No new constitutional deviations; the inherited Go-vs-JS deviations are
already justified in 001's Complexity Tracking and remain covered by the recommended amendment. The
Complexity Tracking table below is therefore intentionally empty for this feature.

**Post-Design Re-check** (after Phase 1): The design artifacts (research.md, data-model.md,
contracts/, quickstart.md) add one daemon-internal package, one contract revision (backward-shaped:
new fields + one new RPC + an enriched `AttachAgent`), and one NEW Go dependency (a VT emulator) with
a documented fallback. No new modules, no new category deviations, coverage floor preserved. Gate
decision unchanged: **proceed**.

## Project Structure

### Documentation (this feature)

```text
specs/003-terminal-session-persistence/
├── plan.md              # This file (/speckit-plan output)
├── research.md          # Phase 0 output — 6 resolved decisions (R1–R6)
├── data-model.md        # Phase 1 output — entities & state transitions
├── quickstart.md        # Phase 1 output — runnable validation scenarios
├── contracts/           # Phase 1 output
│   ├── switchboard-terminal.proto   # proto delta: AttachAgent v2, tags, counts, workspace resolve
│   └── cli-attach.md                # `sxb attach` + auto-open CLI contract
├── checklists/
│   └── requirements.md  # from /speckit-specify
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code (repository root)

Changes are deltas onto feature 001's existing tree. **NEW** and **CHANGED** files are marked; the
rest of the workspace is unchanged.

```text
src/
├── apps/
│   ├── switchboard-tui/
│   │   ├── cmd/sxb/main.go                 # CHANGED: detect workspace marker → `sxb attach`;
│   │   │                                    #   add `sxb attach --host --sandbox` mode
│   │   └── internal/
│   │       ├── ui/
│   │       │   ├── terminal.go              # CHANGED: in-place VT-rendered session view (US2)
│   │       │   ├── sandbox_list.go          # CHANGED: attachment count + tag column (US3, US5)
│   │       │   ├── keys.go                  # CHANGED: `t` in-TUI, `T` external, tag-edit key
│   │       │   └── tageditor.go             # NEW: tag edit overlay → SetSandboxTag (US5)
│   │       ├── client/
│   │       │   ├── attach.go                # NEW: attach stream w/ snapshot handling + resize
│   │       │   └── extterm.go               # NEW: spawn external terminal, track & focus (US3)
│   │       └── termview/                    # NEW: client-side VT render into a bubbletea viewport
│   └── switchboard-tui-e2e/                 # CHANGED: detach/reconnect + one-external E2E
├── services/
│   └── switchboardd/
│       └── internal/
│           ├── terminal/                    # NEW package: persistent session layer
│           │   ├── broadcaster.go           #   read PTY once → vt + ring + fan-out to clients
│           │   ├── vt.go                     #   VT screen-state wrapper (snapshot render)
│           │   ├── ring.go                   #   bounded scrollback ring buffer
│           │   ├── attachments.go            #   per-session client set; kinds; resize arbiter
│           │   └── *_test.go
│           ├── agent/
│           │   ├── pty.go                    # CHANGED: session persists across detach; no Close on detach
│           │   └── hub.go                    # CHANGED: publish attachment-count changes as events
│           ├── grpc/
│           │   ├── attach.go                 # NEW/CHANGED: AttachAgent v2 (snapshot, kind, single-external)
│           │   └── sandbox_rpcs.go           # CHANGED: SetSandboxTag; tag+count in Sandbox
│           ├── registry/                     # CHANGED: persist `tag`
│           └── duplicate/                    # CHANGED: write `.switchboard/session.json` marker
└── libs/
    └── switchboard-proto/
        ├── proto/switchboard.proto          # CHANGED: mirror contracts/switchboard-terminal.proto
        └── gen/                             # regenerated
```

**Structure Decision**: Extend the three existing modules in place. The only new compilation unit is
`services/switchboardd/internal/terminal`, which owns the persistence layer (VT + ring + fan-out +
attachment/resize policy) and sits between the raw `agent.Session` (unchanged PTY ownership) and the
gRPC `AttachAgent` handler. Client-side rendering gets a small `termview` package so the persistence
concern is symmetric (same VT library both ends). This keeps the constitution's category taxonomy and
dependency direction (`apps → libs`, `services → libs`) intact.

## Complexity Tracking

> No **new** constitutional violations are introduced by this feature. The inherited Go-vs-JS
> platform deviations are documented and justified in
> `specs/001-sandbox-session-manager/plan.md` (Complexity Tracking) and covered by that plan's
> recommended MAJOR amendment. Nothing to add here.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none for this feature)_ | — | — |
