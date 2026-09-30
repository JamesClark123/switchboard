# Implementation Plan: Edit Sandbox Sources (Add & Remove Folders)

**Branch**: `007-edit-sandbox-sources` | **Date**: 2026-09-26 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/007-edit-sandbox-sources/spec.md`

## Summary

Make an existing sandbox's seeded folder set **editable in place** (research option A): add
folders — copied/cloned exactly as at launch — and remove them, on a **live** sandbox, with no
container stop/restart/recreate and no interruption to agent sessions, attached terminals, or
running services. The recorded `Sandbox.sources` stays the single source of truth: display,
refresh, relaunch, and escape-hatch workspace scoping all follow the edited record (FR-064).

The technical core is deliberately small. Adds **stage inside the workspace's own
`.switchboard/staging/` and `os.Rename` into place** (research
[R2](./research.md#r2--add-mechanics-stage-inside-switchboard-then-rename-into-place)), so a live
agent never sees a half-copied tree and FR-058's atomicity is structural; removals are a guarded
`RemoveAll` on the workspace *child* — the bind mount is never disturbed, which is exactly what
distinguishes this from `Refresh` (which deletes the mount root and must stop the container
first). A new per-sandbox **operation latch** (shared with `Refresh`) enforces FR-066. Because
everything is host-filesystem work plus the existing record/emit machinery, the feature needs
**zero new `sbx` surface** — the only runtime assumption is
[R1](./research.md#r1--live-visibility-of-workspace-edits-inside-a-running-sandbox)'s live mount
propagation, which features 003/005 already ship on and quickstart Scenario 0 verifies first.

All unknowns are resolved in [research.md](./research.md) (R1–R8); entities and invariants in
[data-model.md](./data-model.md); the additive wire contract in
[contracts/switchboard-edit-sources.proto](./contracts/switchboard-edit-sources.proto);
validation scenarios in [quickstart.md](./quickstart.md).

## Technical Context

**Language/Version**: Go (existing `go.work` monorepo: `switchboardd`, `switchboard-tui`,
`switchboard-proto`, + e2e modules). Node ≥22 / pnpm only for repo tooling, not this feature.

**Primary Dependencies**: gRPC (existing connection; one new server stream + one new unary);
`os`/`path/filepath` (stage/rename/delete); the existing `internal/duplicate` copier and
`Runner.CloneRepo`; Bubble Tea + the existing `confirm.go`/launch-browser machinery (TUI);
`bbolt` (registry, unchanged schema). **No new third-party dependencies.**

**Storage**: bbolt registry — `Sandbox.sources` (existing proto field 7) becomes mutable; **no
new fields, no migration**. Both operations are transient/in-memory (data-model.md); the
operation latch is in-memory and clears on daemon restart, whose startup pass also purges
leftover `staging/` debris (R2).

**Testing**: Go `testing` via `make test`/`cover` (≥90% per module, Rule VI). Manager mechanics
(staging, rename-commit, rollback, latch, guards) are testable against a temp workspace root
with **no `sbx` at all**; gRPC round-trips over the in-process socket; `teatest` for the overlay
and confirm; stub-`sbx` E2E for the full arc. The single real-runtime item is quickstart
Scenario 0 (R1).

**Target Platform**: Linux/macOS hosts running `switchboardd`; client local or remote over the
existing SSH `dial-stdio` path (US4 needs nothing new — the RPCs ride the existing connection).

**Project Type**: CLI/daemon (Go) — TUI client + per-host daemon. Not a web app.

**Performance Goals**: Adds are bounded by verbatim-copy throughput, exactly like launch, and
stream the same progress (FR-055/SC-001: <30 s interaction, copy time excluded). Removal is one
`RemoveAll`. Client timeouts: add 30 min (the `refreshTimeout` class), remove 10 min — package
constants (R8). Post-copy visibility inside the sandbox is mount-propagation, i.e. immediate
(SC-008's 10 s bound is slack).

**Constraints**: Sandbox state is **never** driven by an edit — no stop, no CREATING, no ERROR
(FR-054/FR-058, invariant I7). Record == disk after every operation (I1); ≥1 source always
(I2); deletion only `within()` the controlled folder (I6); originals opened read-only (I5).
Destructive removal is client-confirmed (FR-060, `confirm.go` semantics). **No new env vars**
(Rule VIII surface unchanged) and **no new `sbx` argv** (R8).

**Scale/Scope**: Two additive RPCs + two request messages (no new enum/Event arm/notification);
~2 new daemon files in existing packages (`internal/sandbox/manager_sources.go` + latch,
`internal/grpc` handler wiring); one new TUI screen file (`ui/sources.go`) + client methods +
one keybinding (`S`); no new module, no new package, no new deployable.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack
(Principles I–VIII, Tooling Standards). Features 001–006 are implemented in **Go**, a deviation
recorded and justified in **001's Constitution Check + Complexity Tracking** (where a
constitution amendment is recommended). Feature 007 stays inside that established deviation and
**adds no new ones**:

| Principle | Status for 007 |
|-----------|----------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue of the Biome gate, per 001). |
| III Type Safety | ✅ Go static typing; additive proto codegen; removal targets are exact recorded paths, not stringly indexes. No `any`-equivalent escape. |
| IV Naming & Layout | ✅ `kebab-case` files, colocated `_test.go`; changes land inside existing `internal/` packages — no new package, no layout novelty. |
| V Verification Before Merge | ✅ Same `make` gates as 001–006 (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`). No `--no-verify`. |
| VI Multi-Level Testing | ✅ Go unit + integration ≥90% per module; the entire daemon mechanic tests without `sbx` (temp-dir workspaces), TUI via `teatest`, arc via stub-`sbx` E2E. Storybook/Playwright/MSW remain TS-only, out of scope under 001's deviation. |
| VII Containerized Deployment | ✅ N/A — host binaries (001 carve-out); no new deployable. |
| VIII Env Discipline | ✅ **No new env vars** (R8): staging dir name and client timeouts are package constants. `env-check` surface unchanged; `.env.example` untouched. |
| Repository Structure | ✅ Additive within existing Go modules; no new module, no new top-level category. |

**Result**: PASS (within the pre-recorded 001 Go deviation; no new deviation). No entries
required in Complexity Tracking. **Re-checked after Phase 1 design — still PASS**: the design
adds only Go code inside existing packages, an additive proto revision (2 RPCs + 2 messages), an
in-memory latch, and one TUI screen; no new tooling, deployable, dependency, package, or
environment variable.

## Project Structure

### Documentation (this feature)

```text
specs/007-edit-sandbox-sources/
├── plan.md              # This file
├── research.md          # Phase 0 — R1..R8 decisions + reconciliation risks
├── data-model.md        # Phase 1 — mutable-sources invariants, operation lifecycles, latch
├── quickstart.md        # Phase 1 — validation guide (Scenario 0 = R1 reconciliation gate)
├── contracts/
│   └── switchboard-edit-sources.proto  # additive proto revision (2 RPCs + 2 messages)
├── checklists/
│   └── requirements.md  # spec quality checklist (from /speckit-specify)
└── tasks.md             # Phase 2 — created by /speckit-tasks (NOT here)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/
├── proto/switchboard.proto              # + AddSandboxSources (stream LaunchProgress),
│                                        #   RemoveSandboxSources (returns Sandbox),
│                                        #   AddSandboxSourcesRequest, RemoveSandboxSourcesRequest
└── gen/*.pb.go                          # regenerated via `make proto`

src/services/switchboardd/
├── internal/sandbox/
│   ├── manager_sources.go               # NEW: AddSources (validate → gate → stage → rename →
│   │                                    #   record → emit; rollback on failure, R2/R6) and
│   │                                    #   RemoveSources (validate → within() → RemoveAll →
│   │                                    #   record-follows-disk, R3); staging purge on startup
│   ├── oplock.go                        # NEW: per-sandbox try-acquire operation latch (FR-066)
│   ├── manager.go                       # Refresh acquires the latch; no other change
│   └── *_test.go                        # colocated: staging/rollback/latch/guard/validation
├── internal/grpc/
│   ├── sandbox_rpcs.go                  # + AddSandboxSources (progressSender + blocked gate,
│   │                                    #   mirroring LaunchSandbox) + RemoveSandboxSources
│   │                                    #   (service guard vs portforward instances, FR-062)
│   └── server.go                        # no new wiring beyond the two handlers
└── (internal/duplicate, internal/resources, internal/portforward)  # consumed, unchanged

src/apps/switchboard-tui/
├── internal/ui/sources.go               # NEW: `S` sources overlay — recorded-folder list,
│                                        #   `a` → existing launch browser for adds, `space`+`d`
│                                        #   → confirm.go removal flow (FR-060); in-flight
│                                        #   row badge "adding <folder> — NN%" (FR-055)
├── internal/ui/keys.go                  # + Sources binding (`S`, verified free)
├── internal/ui/app.go                   # screenSources routing; Daemon interface + 2 methods
├── internal/client/sandbox.go           # AddSources (stream consume, blocked/override round-
│                                        #   trip like Launch) + RemoveSources (unary)
└── internal/ui/*_test.go                # teatest: overlay, confirm, busy/refusal surfaces
```

**Structure Decision**: Extend the existing daemon + TUI in place, like 003–006 — but this
feature is small enough that it adds **no new package at all**: the daemon side is two files in
`internal/sandbox` plus handlers in `internal/grpc`, because the operations are (by design)
nothing but the manager's existing disk + record + emit vocabulary applied to children of the
workspace. The client side gets one screen file, mirroring `services.go`. The proto revision is
purely additive.

## Complexity Tracking

> No Constitution Check violations beyond the pre-recorded 001 Go-stack deviation (which this
> feature does not widen). No new entries required.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none for 007)_ | — | — |

## Risks carried into implementation

| Risk | Handling |
|---|---|
| **R1: a real `sbx` snapshots the workspace instead of mounting it live** — added folders would not appear in a running sandbox. | The feature's one runtime assumption, and the same bet 003/005 already ship on. Quickstart **Scenario 0** verifies it before anything else; degradation is the spec's stated contingency — fall back to option B (stop→mutate→bring-up) **inside the same RPCs**, no contract change. |
| Concurrent seed-mutating operations interleave on one workspace (pre-existing gap: `Refresh` today has no exclusion either). | The per-sandbox latch (R4) is acquired by add, remove, **and refresh**; try-acquire semantics surface "busy" immediately (FR-066) instead of queueing behind a 30-minute copy. |
| Container-created root-owned files make a removal's `RemoveAll` fail EACCES mid-batch. | Record follows disk (R3): deleted folders stay dropped, the failed folder stays recorded, the error names it. Same exposure `Destroy` already has; not widened here. |
| Daemon crash mid-add strands staged gigabytes. | Staging lives under the sandbox's own `.switchboard/staging/`; best-effort purge on daemon startup (R2). Never a corrupt workspace — staged trees are outside the seeded set. |
| Older daemon + newer client version skew. | Both RPCs return `UNIMPLEMENTED`, surfaced as-is; the `u` update fan-out (feature 002) is the remedy (contract header). |
