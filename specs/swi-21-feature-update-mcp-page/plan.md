# Implementation Plan: MCP Page Quality of Life — Edit, In-Page Secrets, Scopes

**Branch**: `jamesloganclark/swi-21-feature-update-mcp-page` (spec) → `…-tasks` (plan + tasks) → `…-implement`
| **Date**: 2026-10-31 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/swi-21-feature-update-mcp-page/spec.md`

## Summary

The MCP gateway view gains **Edit**, **secret fields on the form**, a **client-secret field** and **OAuth
scopes**. Ticket item 5 (fixed callback port) is deferred to SWI-22.

1. **Shared rules** (R1). A new pure package `switchboard-proto/mcprules` holds every definition rule
   (headers, placeholders, the credential-header rule and its conversion, scopes, client ID, authorization
   server, URL/argument warnings). The daemon and the TUI both call it, so FR-420 holds by construction.
2. **One save operation** (R2, R3). A new `SaveMcpServer` RPC runs every form save on the daemon under a
   per-server-name lock, in the spec's order: fingerprint check → secret snapshot → (remove) → stores → add →
   identity clear → rollback. It runs on a detached context, so it finishes if the TUI goes away. The lock and
   the authorization in-flight record are checked and taken in one `Manager.mu` section (FR-424).
3. **Switchboard's record** (R4, R5, R6, R11). Definitions and pending replaces are stored beside the marks in
   `McpGatewaySettings`. `GetMcpServerDefinition` merges the record with `sbx mcp inspect --json` and
   `auth status --json`, redacts credential literals, flags host-side changes in normalised form, and returns
   an HMAC fingerprint under a per-start key.
4. **Failure states** (R7, R8). Remove and add are reaped before any re-check. A failed remove is re-listed;
   a failed add is never re-checked: stored secrets roll back and the replace becomes a persisted **pending
   replace** with Retry (held or previous definition) and Cancel from every client. A name listed again ends
   it as *registered by another path*.
5. **Client binding** (R9). After a client change saved with the new secret, the daemon removes
   `mcp:<s>:client_secret:identity` and re-checks it. Without a secret the binding is left in place and the
   server is marked *client re-bind pending*. The plan resolves both spec open items: there is no automatic
   retry of the clear, and the FR-413 removal offer is extended to the public-client case.
6. **Scopes** (R10). `--scope` on add, plus `--scope` / `--no-scope` on a one-time authorize.
7. **Launch and sandboxes** (R12). A marked server that is being replaced is skipped at launch and recorded on
   the sandbox (`Sandbox.mcp_skipped`). Retry turns the entry into *not attached at launch*; Cancel or
   registration by another path clears it. After a replace the TUI lists the attached sandboxes (FR-419).
8. **TUI** (R14, R15). A two-group add/edit form in `ui/mcp_form.go` (definition, then secrets), `y`-only
   confirmations in the spec's order, an outcome panel, in-place reload on a refused save, the new keys `e`,
   `S`, `R`, `X`, and pending-replace rows. All of it is gated on the `mcp-edit` capability.

## Technical Context

**Language/Version**: Go 1.26 (`go.work`; modules `switchboardd`, `switchboard-tui`, `switchboard-proto`,
`switchboard-tui-e2e`)

**Primary Dependencies**: gRPC + protobuf (`make proto`), bbolt, Bubble Tea / Bubbles / Lip Gloss, `huh`
v1.0.0, standard-library `crypto/hmac` + `crypto/sha256` + `crypto/rand`. There are **no new dependencies**.

**Storage**: bbolt. The existing `mcp` bucket record `pb.McpGatewaySettings` gains `definitions` (3) and
`pending_replaces` (4). Sandbox records gain `mcp_skipped` (25).

**Testing**: `go test` per module. Daemon tests use the scripted fake `sbx` in `internal/mcp/manager_test.go`
(`scriptedSbx`, argv log), extended with `inspect`, `secret` and failure cases, and `internal/grpc/mcp_rpcs_test.go`.
`mcprules` has table tests, which both sides' tests reuse. TUI tests use `fakeDaemon` / `update` / `press`.
E2E uses `switchboard-tui-e2e` (`stubSbx`). Gates are `make fmt-check vet lint test cover` (≥ 90% per module),
`make env-check` and `make e2e`.

**Target Platform**: Linux/macOS hosts running `sxbd`; terminals running `sxb`.

**Project Type**: daemon + TUI client + shared proto (existing three-module change)

**Performance Goals**: The definition read issues three CLI calls (`ls`, `inspect`, `auth status`) in
parallel, bounded by the existing 60 s list bound. A save runs O(secrets) CLI calls in sequence; before any
mutation, every save (including a mark-only one) repeats the three definition reads (fingerprint check) plus
`secret ls`, while it holds the per-name lock. A registration change adds one extra registry write (the
crash-safety in-progress record, research R2). Recovery of leftovers runs on one background goroutine, so
`NewManager` makes no host call and daemon start never waits on `sbx`. No new polling.

**Constraints**:
- Secret values only travel request → CLI stdin. They never reach argv, logs, errors, responses, or the
  registry (SC-007). A test greps every captured log and argv for the values.
- No new env vars. Timeouts (`saveTimeout` 10 min, existing per-step bounds) and limits (32 scopes,
  256-character scopes, 128-character placeholders) are package constants.
- Proto numbers continue from the current maxima (checked: `Sandbox` 24, `AddMcpServerRequest` 8,
  `McpServer` 8, `McpGatewaySettings` 2, `Event.McpGatewayChanged` 1).

**Scale/Scope**: Tens of servers per daemon and a handful of secrets per server. About 8 daemon files, 6 TUI
files, 1 new lib package, 1 proto file, and 2 e2e tests.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack. Features
001–010, SWI-9, SWI-17 and SWI-18 are implemented in **Go**, a deviation recorded and justified in **001's
Constitution Check + Complexity Tracking**. SWI-21 stays inside that deviation and **adds no new ones**:

| Principle | Status for SWI-21 |
|-----------|-------------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint`. |
| III Type Safety | ✅ Typed proto messages and enums (`McpSaveOutcome.State/Identity`, `McpSkipState`, `McpFieldFlag.Kind`, `McpSaveRefusal.Reason`); no `interface{}` payloads (inspect JSON decodes into typed structs with optional fields). |
| IV Naming & Layout | ✅ New files in existing packages (`mcp/save.go`, `mcp/definition.go`, `mcp/pending.go`, `mcp/hostflags.go`, `ui/mcp_form.go`) and one new package directory `libs/switchboard-proto/mcprules/`; tests colocated. |
| V Verification Before Merge | ✅ Same `make` gates. |
| VI Multi-Level Testing | ✅ Rule tables, argv-asserting daemon tests, failure-injection tests for every FR-406 branch, a leak test for secret values, TUI model tests, and e2e for US1/US2 and the pending replace; ≥ 90% coverage per module. |
| VII Containerized Deployment | ✅ N/A: daemon and client binaries (001 carve-out). |
| VIII Env Discipline | ✅ No env vars added. |
| Repository Structure | ✅ `libs` import nothing from other categories (`mcprules` uses only the standard library and the same module's `gen`). Changes stay in `src/services/switchboardd`, `src/apps/switchboard-tui`, `src/apps/switchboard-tui-e2e`, `src/libs/switchboard-proto`, and docs. |

**Result**: PASS. **Re-checked after Phase 1 design: still PASS.**

## Project Structure

### Documentation (this feature)

```text
specs/swi-21-feature-update-mcp-page/
├── plan.md              # This file
├── research.md          # Phase 0: findings F1–F5, decisions R1–R16
├── data-model.md        # Phase 1: definition, pending replace, skipped server, outcome, view
├── quickstart.md        # Phase 1: validation scenarios 0–6
├── contracts/
│   ├── proto-delta.md   # RPCs, messages, fields, status codes
│   ├── sbx-cli-delta.md # argv rows + behaviour flags (documentation-derived)
│   └── mcp-form-ui.md   # gateway view keys, form groups, confirmations, outcome panel
├── checklists/
└── tasks.md             # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/
├── proto/switchboard.proto          # additive delta (contracts/proto-delta.md)
├── gen/                             # regenerated (make proto)
└── mcprules/                        # NEW: rules.go, headers.go, credential.go, scopes.go, normalize.go + _test.go
src/services/switchboardd/
├── cmd/sxbd/main.go                 # wire onReplaceEnded → sandbox sweep; secret manager into the MCP manager
└── internal/
    ├── mcp/
    │   ├── cli.go                   # argv: --scope on add, auth --scope/--no-scope, inspect; runBounded
    │   ├── hostflags.go             # NEW: R16 behaviour flags
    │   ├── locks.go                 # NEW: per-name lock + auth exclusion (R3)
    │   ├── definition.go            # NEW: GetDefinition, inspect/auth-status parsing, merge, redaction, fingerprint
    │   ├── save.go                  # NEW: Save (R2, R7, R9)
    │   ├── pending.go               # NEW: pending replace retry/cancel/another-path (R8)
    │   ├── manager.go               # reconcile keeps locked/pending names; Add records definitions; Authorize scopes; ResolveAttach.BeingReplaced
    │   └── parse.go                 # inspect + auth status scope parsing
    ├── secret/manager.go            # ListGlobal helper; Remove returns output for re-check
    ├── sandbox/manager.go, manager_mcp.go # McpAttach.BeingReplaced → Sandbox.mcp_skipped; sweep helper; attach clears entry
    ├── registry/registry.go         # (unchanged API; settings record grows)
    └── grpc/
        ├── mcp_rpcs.go              # 4 new handlers, status mapping, scopes on authorize, list pending
        └── server.go                # DaemonInfo.capabilities += "mcp-edit"
src/apps/switchboard-tui/internal/
├── client/mcp.go                    # wrappers for the new RPCs; authorize scopes
└── ui/
    ├── mcp.go                       # keys e/S/R/X, pending rows, outcome panel, capability gate
    ├── mcp_form.go                  # NEW: add/edit form, secrets group, conversion, confirmations, reload
    ├── sandbox_mcp.go               # skipped entries in the overlay; attach clears
    └── formguard.go                 # masked fields count as dirty
src/apps/switchboard-tui-e2e/
├── harness.go                       # stubSbx: inspect, secret ls/set/rm state, refuse.invalid add failure
└── mcp_e2e_test.go                  # edit, secrets, pending-replace flows
```

**Structure Decision**: Existing three-module layout. The only new directory is the `mcprules` package inside
the existing `switchboard-proto` lib module (research R1).

## Complexity Tracking

No new constitution violations. The Go-stack deviation is inherited from 001.
