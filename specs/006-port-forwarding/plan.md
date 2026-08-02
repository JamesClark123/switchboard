# Implementation Plan: Port Forwarding

**Branch**: `006-port-forwarding` | **Date**: 2026-08-02 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/006-port-forwarding/spec.md`

## Summary

Let a kit declare the long-running **services** a sandbox can offer — a name, a start command, the port
that command listens on, and whether it runs *inside* the sandbox or *on the daemon's host* — then let
the developer pick which ones to start from the client and reach each on an automatically-allocated free
port **on their own machine**.

The technical core is a **client-owned local listener relayed to the daemon over a new bidirectional
`ForwardPort` stream on the existing connection** (research [R1](./research.md#r1--where-the-developer-machine-port-lives-and-how-traffic-crosses-hosts)).
That single path serves local and remote hosts identically, makes the client the sole allocator of ports
on the developer's machine (so FR-049's uniqueness holds by construction, even across hosts), and needs
no second SSH session — which matters because the client's SSH password is deliberately never retained.
Daemon-side, in-sandbox services are reached by publishing a loopback-bound host port with
`sbx ports --publish`; on-host services are dialled directly. A service is only reported `RUNNING` once a
dial actually succeeds, and a readiness failure inside a sandbox is diagnosed against `/proc/net/tcp` so
the very common loopback-only bind is named, with its remedy, instead of appearing as a mystery timeout.

Declared services persist on the sandbox (`Sandbox.services`, resolved later-kit-wins); running
instances are in-memory and session-scoped, so a daemon restart leaves nothing running and nothing
claiming to be. All unknowns are resolved in [research.md](./research.md); entities and invariants in
[data-model.md](./data-model.md); wire and CLI additions in [contracts/](./contracts/).

## Technical Context

**Language/Version**: Go (existing `go.work` monorepo: `switchboardd`, `switchboard-tui`,
`switchboard-proto`, + e2e modules). Node ≥22 / pnpm only for repo tooling, not this feature.

**Primary Dependencies**: gRPC (Unix socket locally; `dial-stdio` over SSH for remote hosts) including
its **bidirectional streaming** support, already exercised by `AttachAgent`; `net` (client listener,
readiness dialling); `os/exec` (`sbx`, host processes); Bubble Tea + `huh` (TUI); `bbolt` (registry);
`yaml.v3` (kit sidecar). **No new third-party dependencies.**

**Storage**: bbolt registry persists the resolved `Sandbox.services` (additive proto field 20, no
migration). Service **instances are in-memory only** (research R7). Client kits gain a
`kits/<id>/services.yaml` sidecar; services are never written into the opaque `spec.yaml`.

**Testing**: Go `testing` via `make test`/`cover` (≥90% per module, Rule VI). Host-`sbx` argv asserted
with stub scripts (feature 001 R6); the `ForwardPort` relay is testable against a local echo server with
no `sbx` present. No Vitest/Storybook/Playwright/MSW (TS-stack, deviated in 001).

**Target Platform**: Linux/macOS host running `switchboardd`; the client may be on a different machine
from the daemon, which is the case this feature exists to serve.

**Project Type**: CLI/daemon (Go) — TUI client + per-host daemon. Not a web app.

**Performance Goals**: Not throughput-bound — the relay carries dev-server traffic, not bulk data. Bounds
that matter (research R9, all package constants): readiness window **60 s** default (per-service
override), stop grace period **10 s**, captured output **1 MiB** tail-retained, relay chunk **32 KiB**.

**Constraints**: The kit's declared list is the allowlist — `StartSandboxService` takes a **name**, never
a command, so nothing outside the declaration can run (the same boundary feature 005 draws). Forwarded
ports bind `127.0.0.1` only, on both the daemon host and the client, so nothing is exposed to the wider
network. Nothing auto-starts, ever. No new env vars, keeping the Rule VIII surface unchanged.

**Scale/Scope**: One new daemon package (`internal/portforward`), one new client package
(`internal/forward`), an additive proto revision (3 unary RPCs + 1 bidi stream + 3 enums + 3 messages +
2 additive fields + 1 Event arm + 1 NotificationKind), two new `sandbox.Runner` methods, a kit-editor
section, and a per-sandbox services screen. Builds on 003 (event hub, client-independent execution),
004 (kits), and 005 (structured kit sidecars, resolution, bounded execution).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack
(Principles I–VIII, Tooling Standards). Features 001–005 are implemented in **Go**, a deviation recorded
and justified in **001's Constitution Check + Complexity Tracking** (where a constitution amendment is
recommended). Feature 006 stays inside that established deviation and **adds no new ones**:

| Principle | Status for 006 |
|-----------|----------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue of the Biome gate, per 001). |
| III Type Safety | ✅ Go static typing; additive proto codegen. No `any`-equivalent escape. Relay frames are a typed `oneof`, not opaque bytes with a side-channel. |
| IV Naming & Layout | ✅ `kebab-case` files, colocated `_test.go`; new packages follow the `internal/<name>` convention of 003/004/005. Service **names** are `kebab-case`, validated in-editor. |
| V Verification Before Merge | ✅ Same `make` gates as 001–005 (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`). No `--no-verify`. |
| VI Multi-Level Testing | ✅ Go unit + integration, ≥90% per module. Storybook/Playwright/MSW are TS-only and already out of scope under 001's deviation. The relay and the state machine are testable without `sbx`; the three `sbx` argv mappings are stub-asserted. |
| VII Containerized Deployment | ✅ N/A — no package under `src/apps/` or `src/services/` in the constitutional sense; `switchboardd`/`sxb` are host binaries (001 deviation). No new deployable. |
| VIII Env Discipline | ✅ **No new env vars** (research R9): every bound is a package constant. `env-check` surface unchanged; `.env.example` untouched. |
| Repository Structure | ✅ Additive within existing Go modules; no new module, no new top-level category. |

**Result**: PASS (within the pre-recorded 001 Go deviation; no new deviation). No entries required in
Complexity Tracking. **Re-checked after Phase 1 design — still PASS**: the design adds only Go code, an
additive proto revision, two in-memory stores, and two `Runner` methods; no new tooling, deployable,
dependency, or environment variable.

## Project Structure

### Documentation (this feature)

```text
specs/006-port-forwarding/
├── plan.md              # This file
├── research.md          # Phase 0 — R1..R9 decisions + reconciliation risks
├── data-model.md        # Phase 1 — entities, state machine, invariants
├── quickstart.md        # Phase 1 — validation guide (5 scenarios + gates)
├── contracts/
│   ├── switchboard-port-forwarding.proto  # additive proto revision
│   └── sbx-ports-cli.md                   # `sbx ports` / `sbx exec` argv contract
├── checklists/
│   └── requirements.md  # spec quality checklist (from /speckit-specify)
└── tasks.md             # Phase 2 — created by /speckit-tasks (NOT here)
```

### Source Code (repository root)

```text
src/libs/switchboard-proto/
├── proto/switchboard.proto              # + KitService/ServiceInstance/SandboxService,
│                                        #   ServiceLocation/ServiceState/ServiceFailureReason,
│                                        #   List/Start/StopSandboxService + ForwardPort (bidi),
│                                        #   KitSpec.services, Sandbox.services,
│                                        #   Event.service_instance, NOTIFICATION_KIND_SERVICE_FAILED
└── gen/*.pb.go                          # regenerated via `make proto`

src/services/switchboardd/
├── internal/portforward/                # NEW package
│   ├── resolve.go                       # union of attached kits' services, later-kit-wins (R8)
│   ├── instances.go                     # in-memory store; at-most-one non-terminal per (sandbox,name)
│   ├── supervisor.go                    # start/stop lifecycle, state machine, cascade on teardown
│   ├── launch_host.go                   # on-host exec: setpgid, PORT env, {{port}} substitution (R4)
│   ├── launch_sandbox.go                # in-sandbox exec via Runner.Exec; setsid + PGID capture (R3)
│   ├── ports.go                         # host-port allocation (bind :0) + publish/unpublish (R2)
│   ├── probe.go                         # readiness dial w/ backoff; /proc/net/tcp loopback verdict (R5)
│   ├── stop.go                          # tree SIGTERM → grace → SIGKILL; port-free wait (R6)
│   ├── relay.go                         # ForwardPort stream handler (dial + bidi pipe)
│   ├── output.go                        # bounded, TAIL-retaining ring buffer (R9)
│   └── *_test.go                        # colocated, per Rule IV
├── internal/sandbox/
│   ├── runner.go                        # + PublishPort/UnpublishPort/Exec (argv-asserted)
│   └── manager.go                       # resolve + persist Sandbox.services on launch/refresh/AddKit;
│                                        #   stop all instances in the non-RUNNING teardown hook
├── internal/grpc/
│   ├── service_rpcs.go                  # NEW: List/Start/StopSandboxService
│   ├── forward_rpc.go                   # NEW: ForwardPort bidi stream
│   ├── server.go                        # wire the portforward supervisor
│   └── subscribe.go                     # emit Event.service_instance + SERVICE_FAILED notification
└── cmd/sxbd/main.go                     # construct the supervisor (Runner, emitter, clock)

src/apps/switchboard-tui/
├── internal/forward/                    # NEW package: local listeners + relay to ForwardPort
│   ├── manager.go                       # open/close per instance; 127.0.0.1:0 allocation
│   ├── relay.go                         # accept loop; one stream per connection
│   └── *_test.go
├── internal/store/kit.go                # + Services []KitService; services.yaml sidecar; ToSpec
├── internal/ui/kit_editor.go            # + secServices itemized section + item form
├── internal/ui/services.go              # NEW: per-sandbox service list (start/stop/open/copy)
├── internal/ui/sandbox_list.go          # running-services indicator on the sandbox row (FR-045)
├── internal/ui/notifications.go         # handle Event.service_instance + SERVICE_FAILED in the inbox
├── internal/ui/keys.go                  # `p` → services (verified free)
├── internal/client/sandbox.go           # List/Start/StopSandboxService + ForwardPort client methods
└── internal/ui/app.go                   # screenServices routing; forward manager lifetime
```

**Structure Decision**: Extend the existing Go daemon + TUI in place — the shape features 003
(`internal/terminal`), 004 (`internal/kit`), and 005 (`internal/escapehatch`) all used. One new daemon
package owns resolution, the instance store, the lifecycle state machine, both launchers, probing,
stopping, and the relay; one new client package owns the developer-machine listeners, which is the only
genuinely new client-side concern. The proto revision is purely additive. No new module, no new
top-level category, no new deployable.

## Complexity Tracking

> No Constitution Check violations beyond the pre-recorded 001 Go-stack deviation (which this feature
> does not widen). No new entries required.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _(none for 006)_ | — | — |

## Risks carried into implementation

| Risk | Handling |
|---|---|
| **`sbx exec` argv is assumed, not documented** — three call sites depend on it (R3, R5, R6). | Isolated behind one `Runner` method; pinned by an argv test; reconciliation checklist in [contracts/sbx-ports-cli.md](./contracts/sbx-ports-cli.md). Highest-risk item in the feature. |
| `sbx ports --publish` may not accept an explicit `127.0.0.1` host IP. | Falls back to an all-interfaces bind, which would break the "not exposed to the wider network" assumption — flagged in R2 as needing either a firewall note or a relay fallback, decided at reconciliation. |
| Host-port allocation is TOCTOU (bind `:0`, close, publish). | Publish immediately; retry the whole allocation 3× on a bound-port rejection (R2). |
| Client-owned listeners mean the local address dies with the client — for local hosts too, and may change on reconnect. | Accepted and documented in R1; the spec's edge case permits re-establishing the access path, and the **service** itself is unaffected because it is daemon-owned. |
| A long-running service can outproduce any buffer. | 1 MiB **tail**-retaining ring (deliberately the opposite retention from escape hatch, whose failures are at the head) with truncation made evident (R9). |
