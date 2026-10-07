# Implementation Plan: MCP Gateway Manager & Daemon Screen (+ kit schema v2)

**Branch**: `008-mcp-gateway-manager` | **Date**: 2026-10-06 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/008-mcp-gateway-manager/spec.md`

## Summary

Give each daemon a managed view of its host's **MCP gateway** — list, register, remove and
authorize MCP server registrations through the host CLI — plus a switchboard-owned,
per-daemon **attach-by-default** mark and **default attach mode** that the daemon applies at
every launch (additive: dynamic gateway + `sbx mcp load` after create; exclusive: `--static-mcp`).
Add a second top-level **daemon screen** (shift+←/→ from the sandbox list) that hosts the gateway
view and connect/disconnect, a **settings screen** with the first TUI-persisted setting, and a
startup **sign-in sequence** over saved remote hosts (centered prompts, retry/skip, never
blocking). In the same change, move kits to **Docker kit schema version 2** — author v2 only,
migrate saved v1 kits on load, refuse (never strip) attaches the runtime would reject — and
declare + check a **minimum runtime version** per host, reconciling every assumed `sbx` surface
against it.

Technical core: one new daemon package (`internal/mcp`) that shells out to the documented
`sbx mcp` verbs (research [R1](./research.md#r1--sbx-mcp-cli-surface-the-daemon-drives)) with a
register-first/authorize-separately flow bounded at 10 min
([R2](./research.md#r2--authorization-flow-register-first-authorize-separately-bounded-url-surfaced));
marks + mode persisted in a new bbolt bucket ([R3](./research.md#r3--where-marks-and-the-attach-mode-live));
launch integration via an `McpAttach` resolved by the gRPC layer
([R4](./research.md#r4--applying-defaults-at-launch)); two Sandbox fields, four DaemonInfo fields,
one Event arm, seven RPCs ([contract](./contracts/switchboard-mcp-gateway.proto)); TUI screens for
daemons/gateway/settings and a sign-in state machine ([R5](./research.md#r5--daemon-screen-and-shift-navigation)–[R7](./research.md#r7--startup-sign-in-sequence));
a v2 kit model with on-load migration and attach pre-check
([R8](./research.md#r8--kit-schema-v2-model-migration-and-attach-pre-check)); and the baseline +
surface inventory ([R9](./research.md#r9--runtime-baseline-and-surface-reconciliation-fr-096097)).
Validation scenarios in [quickstart.md](./quickstart.md); entities in [data-model.md](./data-model.md).

## Technical Context

**Language/Version**: Go (existing `go.work` monorepo: `switchboardd`, `switchboard-tui`,
`switchboard-proto`, + e2e modules). Node ≥22 / pnpm only for repo tooling, not this feature.

**Primary Dependencies**: gRPC (existing connection; six unary + one server-stream RPC added);
`os/exec` with process-group kill (the escape-hatch pattern) for `sbx mcp`; `bbolt` (one new
bucket in the existing registry); `gopkg.in/yaml.v3` (kit v2 render/migrate, already a dep);
`go-toml/v2` (`settings.toml`, already a dep); Bubble Tea/`teatest`. **No new third-party
dependencies.**

**Storage**: Daemon — bbolt `registry.db`: new bucket `mcp` (one `McpGatewaySettings` record) and
two new `Sandbox` fields (21–22); no migration (absent = defaults). Client — `settings.toml` (new,
atomic `SaveTOML`) and `kits/<id>/spec.yaml` rewritten as v2 on save (v1 read via migration).

**Testing**: Go `testing` via `make test`/`cover` (≥90% per module, Rule VI). All daemon
mechanics run against a stub `sbx` script (argv pinning, table/JSON parsing, authorize URL capture
+ timeout kill, load ordering); gRPC over the in-process socket; `teatest` for every screen and the
sign-in state machine; stub-`sbx` E2E (stub must report ≥ baseline version). Real-runtime items
are confined to quickstart Scenario 0.

**Target Platform**: Linux/macOS hosts running `switchboardd`; client local or remote over the
existing SSH `dial-stdio` path. Authorization URLs are shown as text so a remote daemon needs no
browser.

**Project Type**: CLI/daemon (Go) — TUI client + per-host daemon. Not a web app.

**Performance Goals**: Gateway list ≤ 60 s class (one `ls` + N `auth status` calls; N is tens at
most); add/remove ≤ 2 min; authorize bounded 10 min (SC-013); additive launch adds ≤ 60 s per
marked server to the launch, visible in the launch log; startup sign-in ≤ 30 s per host attempt
(SC-007). Screen switches are instantaneous (SC-005).

**Constraints**: Registrations are host truth — switchboard never caches them past a list
(I-M1); marks/mode are daemon-owned and reconciled on read (I-M2); the attached set is fixed at
launch and never re-applied (I-S1/I-S2); a stale or unauthorized mark never fails a launch
(FR-083/084); kit v1 is never written (FR-091); attach refusals happen before any RPC (FR-095);
passwords are never persisted (FR-090). **No new env vars** (all bounds are package constants;
`MinSbxVersion` is a constant). **No new `sbx` flags beyond the documented table**
([contracts/sbx-mcp-cli.md](./contracts/sbx-mcp-cli.md)).

**Scale/Scope**: 7 RPCs + 1 enum + 6 messages + 6 fields + 1 Event arm (additive proto); one new
daemon package (`internal/mcp`, ~5 files) + edits in `sandbox`, `grpc`, `sbxkit`, `registry`; TUI:
three new screens (`daemons.go`, `mcp.go`, `settings.go`), one startup sequence file
(`signin.go`), kit model re-shape + migration (`store/kit.go`, new `store/kit_migrate.go`,
`store/settings.go`), editor section updates, two client files; e2e stub extension. No new module,
no new deployable.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack
(Principles I–VIII, Tooling Standards). Features 001–007 are implemented in **Go**, a deviation
recorded and justified in **001's Constitution Check + Complexity Tracking** (where a constitution
amendment is recommended). Feature 008 stays inside that established deviation and **adds no new
ones**:

| Principle | Status for 008 |
|-----------|----------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue of the Biome gate, per 001). |
| III Type Safety | ✅ Go static typing; additive proto codegen; `McpAttachMode`/`McpServerKind`/`McpAuthState` are enums, not strings; kit v2 is a typed model with typed migration report. |
| IV Naming & Layout | ✅ `kebab-case` files, colocated `_test.go`; one new daemon package `internal/mcp` (the 005/006 precedent); TUI files inside existing packages. |
| V Verification Before Merge | ✅ Same `make` gates (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`). No `--no-verify`. |
| VI Multi-Level Testing | ✅ Go unit + integration ≥90% per module; every daemon mechanic tests against a stub `sbx`; TUI via `teatest`; arc via stub-`sbx` E2E. Storybook/Playwright/MSW remain TS-only, out of scope under 001's deviation. |
| VII Containerized Deployment | ✅ N/A — host binaries (001 carve-out); no new deployable. |
| VIII Env Discipline | ✅ **No new env vars**: timeouts and `MinSbxVersion` are package constants; the new client setting is TUI-persisted TOML (clarified Q4), not environment. `env-check` surface unchanged. |
| Repository Structure | ✅ Additive within existing Go modules; no new module, no new top-level category. |

**Result**: PASS (within the pre-recorded 001 Go deviation; no new deviation). No entries required
in Complexity Tracking. **Re-checked after Phase 1 design — still PASS**: the design adds Go code
in one new daemon package plus existing packages, an additive proto revision, one bbolt bucket, one
client TOML file, and three TUI screens; no new tooling, deployable, dependency, module, or
environment variable.

## Project Structure

### Documentation (this feature)

```text
specs/008-mcp-gateway-manager/
├── plan.md                               # This file
├── research.md                           # Phase 0 — R1..R12 decisions + surface inventory
├── data-model.md                         # Phase 1 — entities, invariants, launch resolution, state machines
├── quickstart.md                         # Phase 1 — validation guide (Scenario 0 = reconciliation gate)
├── contracts/
│   ├── switchboard-mcp-gateway.proto     # additive proto revision (7 RPCs, enum, messages, fields, Event arm)
│   ├── sbx-mcp-cli.md                    # documented sbx argv the daemon drives (pinned by tests)
│   └── kit-schema-v2.md                  # v1→v2 mapping, attach blockers, v3 refusal
├── checklists/requirements.md            # spec quality checklist
└── tasks.md                              # Phase 2 — created by /speckit-tasks (NOT here)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/
├── proto/switchboard.proto               # + MCP RPCs/messages/enums; Sandbox 21–22; DaemonInfo 6–9; Event 6
└── gen/*.pb.go                           # regenerated via `make proto`

src/services/switchboardd/
├── internal/mcp/                         # NEW package: gateway manager
│   ├── cli.go                            #   argv table + exec (CombinedOutput; pgid kill for auth) — R1/R2
│   ├── parse.go                          #   `mcp ls` header-offset table parser; `auth status --json`
│   ├── manager.go                        #   List (reconcile marks) / Add / Remove / Authorize (stream) / SetDefault / SetMode
│   ├── settings.go                       #   McpGatewaySettings via registry `mcp` bucket — R3
│   ├── availability.go                   #   gateway available? (baseline, `sbx mcp`, signed in) + reason
│   └── *_test.go                         #   stub-sbx tests
├── internal/registry/registry.go         # + `mcp` bucket helpers (GetMcpSettings / UpdateMcpSettings)
├── internal/sbxkit/
│   ├── version.go                        # NEW: MinSbxVersion, parse/compare `sbx --version`
│   └── manifest.go                       # drop `options --json`; `--help` is the source (R9)
├── internal/sandbox/
│   ├── manager.go                        # Launch takes McpAttach: static list → runner; additive loads after create; record fields
│   └── runner.go                         # LaunchSpec.StaticMcp → `--static-mcp`; Runner.LoadMcp(ref, name)
├── internal/grpc/
│   ├── mcp_rpcs.go                       # NEW: the seven RPC handlers; FAILED_PRECONDITION when unavailable
│   ├── sandbox_rpcs.go                   # LaunchSandbox resolves McpAttach; kit RPCs gate on baseline (FR-096)
│   ├── server.go                         # DaemonInfo fields; mcp manager wiring; Event.mcp_gateway_changed emit
│   └── subscribe.go                      # new Event arm fan-out
└── cmd/sxbd/main.go                      # version probe + baseline + mcp manager construction

src/apps/switchboard-tui/
├── internal/store/
│   ├── kit.go                            # v2 model (schemaVersion "2"; permissions/credentials/setup/agentInstructions/requires)
│   ├── kit_migrate.go                    # NEW: v1 detection + mapping + Migration report (R8)
│   ├── kit_validate.go                   # + AttachBlockers(); v3 detection
│   └── settings.go                       # NEW: Settings (auto_connect_hosts) ↔ settings.toml (R6)
├── internal/client/
│   ├── mcp.go                            # NEW: the seven RPC wrappers (Authorize streams McpAuthProgress)
│   └── conn.go                           # DaemonInfo cache (baseline/gateway fields)
├── internal/ui/
│   ├── daemons.go                        # NEW: screenDaemons rows/options; connect/disconnect reuse (R5)
│   ├── mcp.go                            # NEW: screenMcp — list, add form, authorize wait + countdown, mark, mode
│   ├── settings.go                       # NEW: screenSettings (`,`) — toggles persisted via store.Settings
│   ├── signin.go                         # NEW: startup sign-in state machine + centered prompt/failure cards (R7)
│   ├── hosts.go                          # password prompt/connect generalized with a return screen
│   ├── kit_editor.go                     # v2 sections (Network permissions, Credential services, Setup files, Agent instructions); migration banner
│   ├── kit_picker.go                     # attach pre-check refusal (AttachBlockers) before the confirm
│   ├── sandbox_list.go                   # `mcp:` badge (FR-086); shift+←/→ dispatch
│   ├── keys.go                           # + ScreenNext/ScreenPrev (shift+right/left), Settings (`,`), gateway-view keys
│   └── app.go                            # screens, Daemon interface (+7), Init → sign-in kick-off, Event arm handling
└── cmd/sxb/main.go                       # settings store wiring

src/apps/switchboard-tui-e2e/harness.go   # stubSbx: `mcp` verbs + `--version` ≥ baseline
```

**Structure Decision**: Extend the daemon + TUI in place, like 003–007. The gateway manager is a
new daemon package because it is a distinct external surface (`sbx mcp`) with its own persistence
and streaming flow — the same reasoning that gave 005 `internal/escapehatch` and 006
`internal/portforward`. Everything else lands in existing packages: launch integration is a few
lines in `sandbox`, the baseline check belongs with the existing version probe in `sbxkit`, and the
TUI gets one file per new screen (the `services.go`/`sources.go` precedent). The proto revision is
purely additive.

## Complexity Tracking

> No Constitution Check violations beyond the pre-recorded 001 Go-stack deviation (which this
> feature does not widen). No new entries required.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none for 008)_ | — | — |

## Risks carried into implementation

| Risk | Handling |
|---|---|
| **Every `sbx mcp` argv and the `--version` format are documentation-derived** (`sbx` absent in dev). | Pinned by argv-asserting tests; quickstart **Scenario 0** reconciles each row of `contracts/sbx-mcp-cli.md` first; R9's inventory also settles `sbx start`, `ls --json`, `options --json`. |
| `sbx mcp add` output/`ls` table layout differs from the docs (column spacing, extra columns). | Header-offset parser tolerant of extra columns; `auth status --json` is the only JSON relied on; parse failure ⇒ `UNKNOWN` state, never a failed list. |
| The authorize child prints the URL to a TTY-only path or buffers it. | Run under a pty if the pipe path yields no URL within 10 s (fallback documented in `cli.go`); the stream still ends at the 10-min bound either way. |
| `--static-mcp` does not compose with `--kit` on `create`, or `sbx mcp load` needs the agent running. | Scenario 0 checks both; fallback for exclusive mode is `create` then `load` (R4 additive path) with a logged note that discovery stays on — spec'd behavior degrades, never fails. |
| Shift+arrow sequences swallowed by some terminals/multiplexers. | Help footer names the keys; add `]`/`[` aliases if real-terminal checks show loss (additive). |
| Startup sign-in misclassifies a network failure as "needs password" (or vice versa). | Classification on ssh's own stderr (`Permission denied`/`publickey`); either misclassification still ends in retry/skip — never a block. |
| Kit migration drops something a user relied on. | Nothing is written until save; the banner lists every drop by path; the previous file is recoverable from git/backups the user keeps. |
| E2E stub `sbx` reports `0.0` → baseline gate refuses kit ops in E2E. | Stub updated to a ≥-baseline version string in the same PR as the gate. |
| Older daemon + newer client version skew. | New RPCs return `UNIMPLEMENTED`, surfaced as-is; the `u` update fan-out (feature 002) is the remedy. |
