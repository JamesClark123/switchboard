# Implementation Plan: Sandbox Session Manager (Switchboard)

**Branch**: `001-sandbox-session-manager` | **Date**: 2026-06-24 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-sandbox-session-manager/spec.md`

## Summary

Switchboard is a two-part system for managing Docker sandbox sessions across one or many
machines: a **daemon** (`switchboardd`) that runs on each participating host and a **Bubble Tea
TUI client** (`switchboard`) the developer runs locally. The daemon wraps the host's existing
sandbox tooling (`sbx`/"sandbox kits"), owns a controlled workspace folder into which it makes
**verbatim duplicates** of selected directories (the headline capability enabling massive
parallel fan-out), manages sandbox lifecycle (launch/stop/restart/destroy), persists a sandbox
registry for re-adoption across restarts, and reports agent task/notification events. The client
connects to a local daemon over a Unix socket and to remote daemons over SSH using a
`dial-stdio` bridge (the Docker-CLI pattern), holds user-level state (saved configurations,
groups, known hosts) as the source of truth, groups and navigates sandboxes across hosts, opens
any sandbox in VS Code, prompts coding agents (or launches them in another terminal), and raises
both in-TUI and OS desktop notifications.

**Technical approach**: a Go workspace (`go.work`) with three modules — the TUI app, the daemon
service, and a shared library that carries the gRPC/protobuf contract and domain types. Transport
is gRPC over a single bidirectional stream (Unix socket locally; `ssh <host> switchboardd
dial-stdio` remotely) with a server-streaming event channel for live sandbox/agent updates.
Agent state ("task complete" vs "needs prompting") is detected by injecting Claude Code **Stop**
and **Notification** hooks into each sandbox that call back to the daemon.

> ⚠️ **Governance note**: This feature's mandated stack (Bubble Tea ⇒ Go) is incompatible with the
> ratified constitution's TypeScript/pnpm/Biome/Vitest/Playwright tooling. The Constitution Check
> below records the conflicts and the justified deviations; a constitution amendment is
> **recommended** (see Complexity Tracking and the Completion Report).

## Technical Context

**Language/Version**: Go 1.26 (verified available in-repo). `go.work` multi-module workspace.

**Primary Dependencies**:
- TUI: `github.com/charmbracelet/bubbletea`, `bubbles`, `lipgloss` (required by spec FR/Bubble Tea constraint).
- Transport/contract: `google.golang.org/grpc` + `google.golang.org/protobuf` (Unix socket + SSH `dial-stdio`).
- Daemon registry store: `go.etcd.io/bbolt` (embedded, transactional KV; pure Go) for the sandbox registry.
- Client state store: on-disk TOML under XDG config (`github.com/BurntForm`-style; concretely `github.com/pelletier/go-toml/v2`) — human-editable, portable.
- Sandbox runtime: the host's `sbx` CLI ("sandbox kits"); daemon shells out to it. Docker is the underlying container runtime.
- PTY / agent attach: `github.com/creack/pty` for prompting and "open in another terminal".
- Desktop notifications: `github.com/gen2brain/beeep` (cross-platform) — *pending research confirmation*.
- VS Code launch: the `code` CLI with `vscode-remote://` URIs — *exact URI scheme pending research*.

**Storage**:
- Daemon (per host): bbolt registry of sandboxes (id → state, owning config snapshot, seed metadata, container handle) + a controlled workspace folder for verbatim copies. Survives daemon restart (FR-002a/b).
- Client (per user machine, source of truth — FR-002c/d): saved configurations, groups, and known-host connection entries as TOML files under `$XDG_CONFIG_HOME/switchboard/`.

**Testing**:
- Unit/integration: `go test` with the standard `testing` package + `testify` assertions; daemon↔client contract tests against the gRPC service over an in-process Unix socket.
- TUI: `github.com/charmbracelet/x/exp/teatest` golden/output tests — *usage shape pending research*.
- E2E (TUI app): a PTY-driven harness (`creack/pty` + `go-expect`, optionally `vhs` scripts) standing in for Playwright, which cannot target a terminal UI (Rule VI permits a justified substitute).

**Target Platform**: Linux and macOS hosts (daemon needs Docker + filesystem + the host's sshd).
Client runs on the developer's Linux/macOS terminal. Windows is out of scope for v1.

**Project Type**: Systems software — a long-running host daemon + a local terminal client, sharing
a gRPC contract library. Not a web app; no browser frontend.

**Performance Goals** (from spec Success Criteria):
- ≥10 concurrent sandboxes on one host without cross-interference (SC-001).
- Remote host sandboxes visible within 10s of connecting (SC-005).
- Sandbox focus-switch < 2s (SC-007); notification latency < 5s (SC-008).

**Constraints**:
- Verbatim duplication MUST NOT modify originals (SC-002) and MUST show progress (FR-028).
- Remote connectivity rides the user's existing SSH (no new listening network port, no separate
  auth system) — daemon exposes a `dial-stdio` subcommand; local access via Unix socket only.
- Daemon must re-adopt running containers after its own restart (FR-002a, SC-012).

**Scale/Scope**: Single developer driving 1..N daemons; tens of sandboxes per host; small registry
and config datasets (KV/TOML are sufficient — no RDBMS needed).

**Open unknowns (resolved in Phase 0 research.md)**:
1. Exact VS Code `vscode-remote://` URI for opening a folder in a local attached container and in a
   container on a remote SSH host.
2. Exact Claude Code hook event names + settings schema for "task complete" (Stop) and "needs
   prompting" (Notification) callbacks, and headless/stream mode.
3. `teatest` API shape and the chosen desktop-notification library's API.
4. How to enumerate the full `sbx` option surface so the configuration editor covers 100% of kit
   options (FR-014) — schema/`--help` introspection vs a versioned option manifest.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

**Source of truth**: `.specify/memory/constitution.md` is still the unpopulated Spec Kit template,
so the *ratified* constitution (v2.3.1, per `governance.md`) is the set of binding rules vendored
under `.claude/rules/shared/`. Gates are evaluated against those.

| Rule | Status | Notes |
|------|--------|-------|
| **Repository Structure** (six categories, `src/<category>/<name>/`, `src/*/*` glob) | ⚠️ Partial | Honored at the category level: TUI → `src/apps/`, daemon → `src/services/`, shared contract → `src/libs/`, plus `*-e2e` siblings. Deviates in that packages are **Go modules**, not pnpm packages, so the `src/*/*` pnpm glob does not apply to them. |
| **Rule I/II Formatting & Linting** (Biome) | ❌ Violation | Biome cannot format/lint Go. Substitute: `gofmt`/`goimports` + `golangci-lint`. |
| **Rule III Type Safety** (`tsc --strict`, no `any`) | ⚠️ Adapted | Go is statically typed; intent honored via `go vet` + `golangci-lint` (`errcheck`, `staticcheck`) and no `interface{}`/`any` except documented external boundaries. |
| **Rule IV Naming & `__tests/` layout** | ⚠️ Partial | kebab-case dirs and the naming spirit kept; Go requires `_test.go` files **colocated as siblings** (test discovery cannot use a `__tests/` subdir), so that sub-rule is deviated per the rule's "package MAY document and justify a different layout" clause. |
| **Rule V Verification Before Merge** (`pnpm -r run …`, Husky) | ⚠️ Adapted | Same gate intent via a `Makefile`/`go test ./...` + `golangci-lint` driven by CI and a Husky `pre-commit` (Husky/Node remain at root for the hook runner; it invokes Go tooling). |
| **Rule VI Testing Discipline** (Vitest, Storybook, MSW, Playwright, 90% v8 coverage) | ❌ Violation | Vitest/Storybook/MSW/Playwright are JS-only and N/A to a Go TUI/daemon. Substitute: `go test` + `teatest`; coverage via `go test -coverprofile` with a **90% floor** preserved; Playwright→PTY/`vhs` E2E (Rule VI explicitly permits a justified Playwright substitute when it cannot target the platform — a terminal UI qualifies). |
| **Rule VII Containerized Deployment** (every app/service ships Dockerfile + compose; root compose `include:`) | ❌ Violation | The **daemon is a host-level system process** — it manages the host's Docker, copies host directories, and is reached through the host's sshd; containerizing it contradicts its purpose. The **TUI is a local terminal binary**, not a deployable web app. Both fall outside Rule VII's web-deployable model. Distribution is via compiled binaries (`go build`) / a release artifact, not images. |
| **Rule VIII Env Var Discipline** (`env.ts` + Zod + dotenv + `.env.example`) | ⚠️ Adapted | Intent (single typed, validated config surface; example committed; fail-fast on missing keys) honored via one Go config package per module that parses/validates env at startup and a committed `.env.example`; Zod/dotenv are JS-specific and not used. |
| **Tooling Standards** (pnpm, Biome, Vitest, Docker, Husky) | ❌ Violation | Superseded by the Go toolchain for these modules; see rows above. pnpm workspace remains only as the repo's outer shell (root scripts + Husky), not for the Go modules. |
| **Governance** (constitution wins; amendments via PR + Sync Impact Report + semver) | ✅ / action | Followed by surfacing every conflict here rather than silently diverging. **Recommended MAJOR amendment** to add a "non-JS/systems components" carve-out (Go toolchain equivalents) so these deviations become first-class rather than per-plan exceptions. |

**Gate decision**: The violations are **driven by a non-negotiable user requirement (Bubble Tea ⇒
Go)** that the constitution's JS-centric tooling rules never anticipated. They are recorded with
justification in **Complexity Tracking** below (Spec Kit's sanctioned mechanism) and do not block
planning. The clean long-term resolution is a constitution amendment; until then this plan carries
the exceptions explicitly. No *new* unjustified complexity is introduced beyond what the platform
requires.

**Post-Design Re-check** (after Phase 1): The design (research.md, data-model.md,
contracts/switchboard.proto, quickstart.md) introduced **no new** constitutional deviations beyond
the platform-driven set above. Three modules (not more) under the existing category taxonomy; the
gRPC contract keeps a single shared `libs` package with the mandated dependency direction; the 90%
coverage floor and the three-tier testing pyramid (unit `teatest` / integration gRPC / E2E PTY) are
preserved. The gate decision is unchanged: proceed, exceptions justified, amendment recommended.

## Project Structure

### Documentation (this feature)

```text
specs/001-sandbox-session-manager/
├── plan.md              # This file (/speckit-plan output)
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/           # Phase 1 output (gRPC/protobuf service contract)
│   └── switchboard.proto
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created here)
```

### Source Code (repository root)

A Go `go.work` workspace. Packages are placed under the constitution's category directories
(`apps`/`services`/`libs`) to honor the Repository Structure taxonomy, while using Go-idiomatic
internals (`cmd/`, `internal/`, sibling `_test.go`).

```text
go.work                                  # ties the three modules together

src/
├── apps/
│   ├── switchboard-tui/                  # @tms/switchboard-tui — Bubble Tea client (user-facing)
│   │   ├── go.mod
│   │   ├── cmd/switchboard/main.go       # entrypoint binary `switchboard`
│   │   └── internal/
│   │       ├── ui/                        # bubbletea models: sandbox list, host view, config editor,
│   │       │   …                          #   launch wizard, prompt pane, notifications
│   │       ├── client/                    # gRPC client; local-socket + ssh dial-stdio dialers
│   │       ├── store/                     # client-side TOML state: configs, groups, known hosts (truth)
│   │       ├── vscode/                    # build `code` vscode-remote URIs (local + remote)
│   │       └── notify/                    # in-TUI + OS desktop notifications
│   └── switchboard-tui-e2e/              # @tms/switchboard-tui-e2e — PTY/vhs E2E (Playwright substitute)
│       └── go.mod
├── services/
│   ├── switchboardd/                      # @tms/switchboardd — the per-host daemon (backend)
│   │   ├── go.mod
│   │   ├── cmd/switchboardd/main.go       # entrypoint: `serve` (unix socket) | `dial-stdio` (ssh bridge)
│   │   └── internal/
│   │       ├── grpc/                       # gRPC server impl of the contract
│   │       ├── registry/                   # bbolt-backed sandbox registry; re-adoption on restart
│   │       ├── sandbox/                     # lifecycle: launch/stop/restart/destroy via `sbx`
│   │       ├── duplicate/                   # verbatim directory copy into controlled workspace + progress
│   │       ├── sbxkit/                      # enumerate + map the full sbx option surface (FR-014)
│   │       ├── agent/                       # inject Claude Code hooks; receive Stop/Notification callbacks
│   │       └── resources/                   # pre-launch disk/resource checks + warnings (FR-012f)
│   └── switchboardd-e2e/                  # @tms/switchboardd-e2e — daemon E2E against real Docker
│       └── go.mod
└── libs/
    └── switchboard-proto/                 # @tms/switchboard-proto — shared contract + domain types
        ├── go.mod
        ├── proto/switchboard.proto         # mirrors specs/.../contracts/switchboard.proto
        └── gen/                            # generated Go (protoc-gen-go / -go-grpc)
```

**Structure Decision**: Three Go modules under the existing `apps`/`services`/`libs` category
taxonomy, wired by `go.work`, with `-e2e` siblings as Rule VI requires. This keeps the
constitution's directory taxonomy and dependency direction (`apps → libs`, `services → libs`;
`libs` imports nothing else) while substituting Go-idiomatic package internals and the Go
toolchain. The stray top-level `apps/` and `packages/` scaffold dirs and the `packages/*`+`apps/*`
pnpm glob are superseded; `pnpm-workspace.yaml` is reduced to the repo's outer shell (Husky/root
scripts) since the workspace members are Go modules.

## Complexity Tracking

> Constitution Check has violations driven by the mandated platform; each is justified here.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| **Go toolchain instead of TypeScript/pnpm** (Rules I/II/III/VIII, Tooling Standards) | Bubble Tea (spec requirement #5) is a Go library; the daemon shares types/transport with it, so both are Go. | "Rewrite in TS" is impossible — Bubble Tea has no TS equivalent that satisfies the requirement; a polyglot TS-client/Go-TUI split would need an extra IPC bridge and two toolchains for no benefit. |
| **gofmt/golangci-lint instead of Biome** (Rules I/II) | Biome does not process Go. | No Go formatter/linter is optional; these are the idiomatic equivalents enforcing the same "automated formatting is truth" + "lint blocks merge" intent. |
| **`go test`/`teatest` + `go -coverprofile` (90% floor) instead of Vitest/Storybook/MSW** (Rule VI) | Same testing-pyramid intent on a Go TUI/daemon. | Vitest/Storybook/MSW are JS-only; they cannot exercise Go code or a terminal UI. 90% coverage floor is **preserved**, satisfying Rule VI's non-negotiable threshold via a different provider. |
| **PTY/`vhs` E2E instead of Playwright** (Rule VI) | E2E must drive a terminal UI. | Playwright targets browsers/web; Rule VI explicitly allows a justified substitute when Playwright "cannot reasonably target the platform" — a TUI is exactly that case. |
| **No Dockerfile/compose for daemon & TUI** (Rule VII) | The daemon orchestrates the **host's** Docker/filesystem/sshd and must run on the host; the TUI is a local terminal binary. | Containerizing a host-control daemon is self-defeating (it would need the host's docker socket, host FS, and host network/ssh anyway); a TUI has no web-deploy surface. Distribution is compiled binaries. |
| **Colocated `_test.go` instead of `__tests/` subdir** (Rule IV) | Go's test tooling discovers `_test.go` files in the same package directory. | A `__tests/` subdir would break `go test` package-local discovery; Rule IV permits a documented per-package layout deviation. |

**Recommended resolution**: a MAJOR constitution amendment adding a "Systems / non-JavaScript
components" section that maps each JS-specific rule to its Go equivalent (gofmt+golangci-lint;
go test + 90% coverprofile; teatest/PTY E2E; binary distribution in place of containerization for
host daemons), so future Go work inherits first-class rules instead of per-plan exceptions.
