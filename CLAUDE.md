# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

`switchboard` is a pnpm-workspace monorepo, currently a **bare scaffold** — `apps/` and
`packages/` exist but contain no source yet. The substance of the repo right now is its
**constitution**: a ratified set of engineering rules vendored as a git submodule at
`.claude/rules/shared/` (from `github.com:JamesClark123/claude_rules`). These rules are
binding (`MUST`/`NON-NEGOTIABLE`), supersede ad-hoc preferences, and are what you should
read in full before scaffolding any package. Day-to-day guidance here MUST NOT contradict
them; if it appears to, the constitution wins (see `governance.md`).

Toolchain baseline: Node `>=22`, pnpm `9.15.9`, `auto-install-peers=true`.

## ⚠️ Known scaffold drift to fix before adding packages

The constitution mandates a specific layout that the current scaffold does **not** yet match.
When you create the first package, reconcile these:

- **Workspace glob**: `pnpm-workspace.yaml` currently globs `packages/*` and `apps/*`, but
  `repository-structure.md` requires all code under `src/<category>/<name>/` with a workspace
  pattern of `src/*/*`. Update `pnpm-workspace.yaml` to `src/*/*` (and relocate/remove the
  stray `apps/` + `packages/` dirs) rather than placing packages where they currently point.
- **Root scripts**: root `package.json` defines only `build`/`test`/`lint`. The constitution's
  required workspace scripts (below) need to exist per-package and are invoked via `pnpm -r run <script>`.

## Repository structure (mandated)

All source lives under `src/`, one level deep inside exactly one of six canonical categories.
Adding a seventh category requires a constitution amendment.

| Category       | Path                | Contains |
|----------------|---------------------|----------|
| `apps`         | `src/apps/`         | Deployable frontends (user-facing UIs) |
| `services`     | `src/services/`     | Deployable backends (anything exposing an API) |
| `libs`         | `src/libs/`         | Framework-agnostic TS, no UI, no DB I/O |
| `repositories` | `src/repositories/` | All DB-touching code (schemas, migrations, queries) |
| `components`   | `src/components/`   | Reusable presentational UI components |
| `features`     | `src/features/`     | Drop-in end-to-end feature slices (UI + data + actions) |

- Package path: `src/<category>/<name>/` — never depth-one under `src/`, never depth-three.
- Package names: `@tms/<kebab-name>` (name need not encode category).
- Dependency direction: `apps → features → (components, services, repositories) → libs`.
  `libs` MUST NOT import any other category. Avoid cross-category cycles.
- E2E packages are siblings of their target in the same category: `src/apps/<app>-e2e/`,
  `src/services/<svc>-e2e/`.
- Within a package: source in `src/`, output in `dist/`, tests colocated in a `__tests/`
  subdirectory mirroring the filename (`src/foo.ts` ↔ `src/__tests/foo.test.ts`).

## Standard per-package scripts

Every package exposes these with uniform names so they run workspace-wide via `pnpm -r run <script>`.
Packages with no scope for a given check expose a no-op so the recursive run still succeeds.

- `format` / `format:check` — Biome formatter
- `lint` — Biome linter
- `typecheck` — `tsc` under strict mode
- `test:unit` — Vitest unit + Storybook visual modes (coverage-v8, 90% thresholds)
- `test:integration` — Vitest integration + MSW + Storybook interaction tests (90% thresholds)
- `test:e2e` — Playwright (only `<app>-e2e` packages implement this)
- `test` — alias for `test:unit` (the fast layer used by the pre-commit hook)
- `env:check` — per-package env schema/example sync validator (required if the package ships `env.ts`)

Run a single test with Vitest from within a package, e.g.
`pnpm vitest run src/__tests/foo.test.ts` or `pnpm vitest run -t "<test name>"`.

## Verification before merge (CI gates — all blocking)

```
pnpm -r run format:check
pnpm -r run lint
pnpm -r run typecheck
pnpm -r run test:unit
pnpm -r run test:integration
pnpm -r run test:e2e        # Playwright against the local Docker build
pnpm -r run env:check
```

A Husky `pre-commit` hook runs the fast subset: `format:check`, `lint`, `typecheck`,
`test:unit`, `env:check` (integration/E2E are CI-only). Husky installs via the root
`prepare` script. Bypassing a hook or CI (`--no-verify`, force-merge) requires approval
recorded in the PR description.

## Tooling standards (substitutions require a constitution amendment)

- **Formatter + linter**: Biome only — single `biome.json` at workspace root. No `prettier`/`eslint`.
- **Type checker**: TypeScript `"strict": true`; each package has a `tsconfig.json` (may extend a shared base).
- **Test runner**: Vitest; per-package `vitest.config.ts` extends the shared base.
- **Coverage**: `@vitest/coverage-v8` only, `provider: 'v8'`, 90% floors on lines/branches/functions/statements,
  enforced **per package** (not as a workspace average).
- **Visual testing**: Storybook, stories colocated, run headlessly in CI across all five modes
  (snapshot, visual regression, accessibility, interaction, render).
- **HTTP mocking**: MSW. Unit/integration tests MUST NOT hit the real network; ad-hoc fetch/SDK mocks are not allowed.
- **E2E**: Playwright with `playwright.config.ts` honoring the `E2E_TARGET` env contract.
- **Container runtime**: Docker (see below).

## Type safety & suppressions

- `any` is prohibited except at documented external boundaries, each with an inline comment.
- `// @ts-ignore` is forbidden — use `// @ts-expect-error -- <reason>` so it fails loudly when fixed.
- `biome-ignore` and `@ts-expect-error` MUST carry a justification comment; review sends back unjustified ones.

## Environment variables (Rule VIII — non-negotiable)

A package that reads any env var MUST:

1. Own a package-local `.env` (gitignored; never walk up the tree for values).
2. Expose exactly one `env.ts` that declares a **Zod** schema, parses `process.env` at load time,
   and exports a single typed `env` object. **Direct `process.env.<X>` reads outside `env.ts`
   are forbidden** (the only exception is the `dotenv`/`dotenvx` loader call in the entrypoint).
   Parse failure MUST throw at startup naming the offending key(s).
3. Ship a committed `.env.example` with safe placeholder values, documenting every schema key.
4. Keep schema keys and `.env.example` keys in exact lockstep — `env:check` enforces this and
   verifies `.env.example` is tracked while `.env` is not.

## Containerized deployment (Rule VII)

- Every package under `src/apps/` and `src/services/` ships a `Dockerfile` AND a per-package
  `docker-compose.yml` at its root (multi-stage builds for TS packages; no `.env`/`.git`/dev-deps in images).
- Supporting infra (Postgres, queues, MSW mocks) is declared in the per-package compose file of
  the package that **owns** it (e.g. Postgres lives in `src/repositories/main-postgres/docker-compose.yml`).
- The repo-root `docker-compose.yml` is an **index of `include:` directives only** — no inline
  `services:`/`volumes:`/`networks:`. Adding an app/service/infra component requires creating its
  per-package compose file and appending one `include:` line in the same PR.
- `docker compose up` from the root MUST bring the entire stack online; this same stack is the
  local-Docker `E2E_TARGET` for Rule VI. Express inter-service ordering via `depends_on`
  (with `condition: service_healthy` where a healthcheck exists).

## Governance & spec-driven workflow

- The constitution proper lives at `.specify/memory/constitution.md` (this repo uses Spec Kit;
  the `.specify/` dir is not yet present in the scaffold). Current version per `governance.md`:
  **2.3.1**, ratified 2026-05-03.
- Amendments: PR modifying `.specify/memory/constitution.md` + the affected `.claude/rules/` file(s),
  with a Sync Impact Report and a semver bump (MAJOR=remove/redefine a principle, MINOR=add/expand,
  PATCH=wording).
- Plans generated via `/speckit-plan` MUST gate on a Constitution Check against `.claude/rules/`;
  violations go in the plan's Complexity Tracking table with justification, or the plan is revised.

## Naming

- Files/directories: `kebab-case`. Types/interfaces/classes/React components: `PascalCase`.
  Variables/functions/methods: `camelCase`. Top-level immutable config constants
  (incl. env var names): `UPPER_SNAKE_CASE`.
- Line endings LF only; `.editorconfig` at root defines charset/EOL/final-newline/indent.

<!-- SPECKIT START -->
Active feature: `specs/swi-21-feature-update-mcp-page/plan.md` (MCP Page Quality of Life — Edit, In-Page Secrets,
Scopes, SWI-21; **planned, implementation pending**: `mcprules`, `mcp/save.go`, `mcp/definition.go`, `ui/mcp_form.go`
do not exist yet). Ticket item 5 (fixed OAuth callback port) is deferred to SWI-22. Shared definition rules live in a new
pure package **`src/libs/switchboard-proto/mcprules`** used by daemon and TUI (FR-420: headers, `${placeholder}` rule +
reserved `client_secret`, credential-header rule + one-step conversion + `Redact`, scopes, client ID, authorization
server). Every MCP form save is one RPC **`SaveMcpServer`**, run on `context.WithoutCancel` under a per-name lock
(`mcp/locks.go`, checked with the auth `inflight` record in one `Manager.mu` section, FR-424): fingerprint (HMAC-SHA-256
under a per-start key; `STALE_CHANGED`/`STALE_RESTARTED`; a failed re-read is `UNVERIFIED`) → secret snapshot →
classify (re-checks `inflight`) → crash-safety *in-progress* record (`saves_in_progress`, separate from pending replaces; mark stays in `marks`; a lock holder or `SetMcpServerDefault` that finds one refuses `UNVERIFIED`; resolved only by one background recovery routine for start-up leftovers (queued by `NewManager` without a host call); a `persist` that failed 3 times keeps the lock and re-runs its own per-name mutate func, and other lock takers refuse a `saving` name at once instead of waiting, `rebind_pending` on a client change, `onReplaceEnded` on a listed replace/Retry) → `rm` (re-listed on failure) → stores
(stdin) → `add` (never re-checked on failure; rollback) → identity clear of `mcp:<s>:client_secret:identity` (re-checked;
*client re-bind pending* mark) → one persist + one event. Definitions and **pending replaces** (Retry held/previous,
Cancel, ended by *registered by another path*) live in `McpGatewaySettings.definitions`/`pending_replaces`/`saves_in_progress` (3–5).
`GetMcpServerDefinition` merges the record with `sbx mcp inspect --json` + `auth status --json` (runtime wins where
positively reported; credential literals redacted in the daemon). Scopes: `--scope` on add, `--scope`/`--no-scope` on
authorize. Launch skips a marked server being replaced → `Sandbox.mcp_skipped` (25). New RPCs
`GetMcpServerDefinition`, `SaveMcpServer`, `CancelMcpPendingReplace`, `RemoveMcpServerSecrets`; capability `mcp-edit`.
Real-host flags in `mcp/hostflags.go` (all documentation-derived). See `research.md` (R1–R16), `data-model.md`,
`contracts/`, `quickstart.md`, `tasks.md`.

Prior feature: `specs/swi-17-feature-action-pre-post-prompts/plan.md` (Action Pre & Post Prompts, SWI-17;
**implemented** — T001–T042 done, T043 manual quickstart pending). Every
action gets a **sequence**: built-in **Model** (`/model opus|fable|sonnet|haiku`), **Effort** (`/effort
low|medium|high|xhigh|max|auto`; **ultracode dropped** — it is a sticky session toggle, research R1) and **Clear**
(`/clear`), on by default incl. existing actions (absent `Action.sequence` = defaults), plus custom pre/post prompts
(≤10 each, `{name}` placeholders, none in the first word, none in `!` steps). Steps go **one at a time, each only once
the previous finished by evidence after its submit**. Planning gate run on Claude Code 2.1.312 (R1): `/model`/`/effort`
raise no hook — read back from the screen's `⎿` confirmation line; `/clear` raises `SessionStart source=clear`.
`termview` moves to **`src/libs/switchboard-termview`**; the daemon's PTY reader feeds a per-sandbox screen read by
`agent/claudeui.go` (input box empty/ready, step text still in box, pickers, confirmation lines; fixtures in
`agent/testdata/claudeui/`). New worker RPC **`DeliverWorkerStep`** types one step and runs the submit check locally
(hook `Tap` + screen) → MODEL_TURN | MODEL_TURN_INPUT_ONLY | AGENT_LOCAL | NOT_ACCEPTED; runs without steps keep
`DeliverWorkerPrompt`. Orchestrator `sequence.go` owns `Run.steps/step/step_status/…` (persisted), closes runs exactly
as before, and holds the sandbox during cleanup via `controller.run.hold` (CLEANING_UP | NEEDS_INPUT_FREEING |
NEEDS_DEVELOPER — the last is ended live by the owning daemon on a corroborated `startup`/`clear` `SessionStart`). `AgentSession`
records `last_submit_at`, `last_turn_end_at`, `last_session_start_at/source` (restart resolution, FR-217). New
`ResolveRunStep` (`R` retry · `S` skip · `X` fail), `RUN_STEP_NOT_ACCEPTED`, TASK_COMPLETE suppressed for
setup/cleanup turns (`WorkerRun.step_role`). Builds on SWI-15 (PR #17, merged) for `SessionStart`; needs SWI-9's implementation for the form guard. Security
(R13): control characters and invalid UTF-8 refused in untrusted call values only (authored text and escape-hatch
output untouched); a per-agent hook token passed in the agent's environment at launch (`SXB_HOOK_TOKEN`), persisted per generation;
daemon-side corroboration for hold release and Clear. After an owning-daemon restart the agent survives as an
orphan, found by a probe of running sandboxes (a recorded agent PID alive with no live session) at start (only
sandboxes with an unretired token; the rest are clear at once), on an `sbx exec` client exit and on gate refusals;
*unknown* is bounded (`orphanUnknownLimit` 60 s or 3 failures → `AgentSession.agent_check` CHECK_FAILED, row note only). `agent.Registry.Session` is the one choke point that refuses to spawn beside
it, and try-locks the sandbox's `runtimeMu` (held by stop/recreate ops, and ordering every probe and stop) from the
gate check through `cmd.Start` (generic `ErrBusy` `sandbox is busy; try again`); a stop drains the sandbox's `sbx exec` clients
best effort before `sbx stop` (the agent's, plus still-starting services, recorded FAILED `may still be running`, groups TERM then KILL; RUNNING services keep their clients; bounded, no ordering promise; only developer-initiated execs try-lock `runtimeMu`, there is no exec guard), and the on-change hook clears orphan state only on a confirmed stop/destroy (never CREATING/ERROR).
The daemon only detects and surfaces an orphan
(one *agent detached* notification per orphan) and never stops one itself: only the developer (`t` →
Replace, `R`) or a sandbox stop/restart does (R14). **Deferred to SWI-19** (tasks.md *Deferred items*): bulk Replace /
`ReplaceDetachedAgent`, the aggregated start-up notice, `AGENT_CHECK_FAILED` + run-note overlay, probe priority,
operation-specific busy messages, detailed orphan notes.
See `research.md` (R1–R14),
`data-model.md`, `contracts/`, `quickstart.md`.

Prior feature: `specs/swi-18-feature-workers-should-be-assignable-to-lanes/plan.md` (Worker Lanes, SWI-18). **Lanes
replace the action worker tag**: a lane is a named worker group per orchestrator (`pb.Lane{id "ln-…", name, description}`
on `Orchestrator.lanes`, ≤32, action-name rule, `open` reserved); `prompt` actions are **exclusive**/**inclusive** to
lanes (`Action.lane_ids`/`lane_mode`) or un-laned (open-lane workers only = non-reasoning, no lanes, not *lanes pending*).
`Action.worker_tag` is deprecated: dropped on load, logged, listed in `Orchestrator.dropped_worker_tags` (TUI notice, `D`
dismiss); `PutActionRequest.sets_lanes` refuses old clients; `DaemonInfo.capabilities` (`"lanes"`) gates the TUI.
**Roster freshness** (`orchestrator/roster.go` `applyRecords`, the only code applying sandbox records to the roster;
lane writes touch only lane fields): owning daemon stamps
`Sandbox.revision` in `registry.Put/Update`; entries keep `revision` + `written_seq` (`Orchestrator.roster_seq`);
high-water marks (removal terminal); reconcile lists with no lock then applies once; links subscribe before listing; the
hub reports overflow via `Event.events_dropped`; events now adopt/drop. **Attach tickets** (`Orchestrator.attach_tickets`,
`SandboxController.attach_ticket`, 24 h) carry lanes picked at launch/attach; bad ticket → *lanes pending*. **Dispatch**
(`lanes_dispatch.go`): reserve for exclusive runs, then candidates per run (exclusive: own reservation → unreserved lane
worker → later run's reservation; inclusive: unreserved lane worker → open-lane → later reservation; un-laned: open-lane).
Call + action delete share `registry.UpdateOrchestratorRuns`; deleted actions' settings are kept on queued runs
(`Run.kept_lanes`). `CheckLaneChange` + `LaneGuardConfirm` implement the open-lane guard; `Run.waiting_reason` is derived
on read. Lane names pushed to worker hosts for display only (`UpdateWorkerRequest.lanes`); briefings in the background.
`DeliverWorkerPromptResponse.revision` returns the sandbox revision the delivery wrote, so the orchestrator's roster
cache never goes stale after its own delivery. TUI: Lanes view, shared `lane_picker.go`, action form lanes + `ctrl+l`
Manage lanes; one `lanesPanel` per `orchSurface` (`orchestrator_lanes.go`), every guarded write through `laneWrite`
(check → confirm → write with the shown list → re-confirm a newer list); lane UI only when `HasCapability("lanes")`. See `research.md` (R1–R17),
`data-model.md`, `contracts/`, `quickstart.md`.

Prior feature: `specs/swi-9-feature-app-forms/plan.md` (App Forms — Enlargeable Text Fields & Unsaved-Changes
Guard, SWI-9). **Planned; implementation pending, not on `main`**: the files named below (`formguard.go`,
`enlarge.go`, `unsaved.go`, …) do not exist yet, so check before relying on them (SWI-18 T059). **TUI-only**. **Enlarge**: `ctrl+e` on any multi-line `huh.Text` opens a full-terminal editable
textarea (`MaxHeight 0`, `enlarge.go`); esc/ctrl+e write back (`*ptr = value; field.Value(ptr)`), ctrl+s writes back
and saves, ctrl+c writes back then leaves the form, `ctrl+r` reverts to the text at open, pgup/pgdn batch cursor moves.
huh's external editor is gone: every form uses `formKeyMap()` (keyless `Text.Editor` **and** keyless `Quit`, since
`ExternalEditor(false)` alone only hides the binding when help renders) and every Text is built by `formSpec.text(ptr, title)`
(registry for write-back). Inline fields keep huh's 99-line enter cap; values up to 10,000 lines round-trip (verified,
R2). **Guard**: every editing surface (`guardSurface` enum in `formguard.go`: 6 multi-field form surfaces, kit item + overview,
launch, reasoning, sources overlay + add browser, rename/tag/add host/add group) routes its exits (esc; ctrl+c in forms;
`q` on the kit overview and sources overlay) through `requestExit`: clean (current values == baseline snapshot, taken
after the form is built and Text values normalised) leaves as today, dirty opens
one shared dialog (`unsaved.go`, a layer above the screen): `s` Save (reasoning: `r` or enter@Rebuild → `guardSave(gReasoning)` =
`confirmRebuildFromSetup`, a destructive confirm built from the edited setup; FR-017 exception) / `c`·esc·ctrl+c Cancel (default highlight) / `d` Discard; enter activates the highlight;
only esc/ctrl+c act for `unsavedKeyGrace` (a var, 150 ms; story tests zero it with `noKeyGrace(t)`) after it opens.
`confirmState.onAccept func(*Model)` carries y-only effects (sources Save closes + resets the overlay; Rebuild → detail);
never an `acceptTo screen` field, since `screenList` is the zero value. Builders are split into `fill…Vals` + `build…Form(spec, vals)` so `reopenForm` keeps edits;
`View` applies the password/sign-in overlays last over every layer. Save = the surface's existing save/confirm, Discard =
its esc path. MCP "register anyway?" back-out returns to the form; sources Save → removal confirm (decline = Cancel);
a save failing on an enter-completed huh form rebuilds it (`reopenForm`); dialog/enlarge layers are closed by
`reconcileGuardLayers` when their surface goes away; forced closes (host drop, sandbox/orchestrator removed) drop the guard with `unsaved edits lost: <reason>`, also when
the surface is suspended under its own confirm (`suspendedGuard`); confirm lifecycle (research R7 C1–C4): only key handlers and the
override queue open confirms, so a showing confirm is never replaced; every async add-override is queued and opens on
a non-key message only on the plain list (not filtering) or its own sandbox's overlay, with no credential prompt up
(queue purged on sandbox removal/host drop); guard-opened and queued confirms are
`yOnly` (accept on y only; enter ignored; no timer). SC-006
timings are `BenchmarkSC006*` (worst measured 55 ms/keystroke at 5,000 lines). As built: the form key pre-handler is
`formKey(g, msg)`; `forceClose(g, reason, close)` takes the surface's close step (`forceCloseOr` when g may not be the
guard), `closeSandboxGuard`/`closeOrchGuard` find the target; the queue is `pendingOverrides`, opened by
`openParkedOverride` in the `Update` wrapper; R7 tests live in `force_close_test.go`. See `research.md` (R1–R10),
`data-model.md`, `contracts/forms-ui.md`, `quickstart.md`.

Prior feature: `specs/010-jobs-screen/plan.md` (Jobs Screen & Orchestrator UI Reorganisation, SWI-5).
**TUI-only**: the **Orchestrators** top-level screen is replaced by a **Jobs** screen (cycle Sandboxes →
Daemons → Jobs, wrapping; `esc` → Sandboxes, `q` quits) with tabs **Overview · Actions · Events · Queue** covering every orchestrator on every
connected daemon (rows labelled `name @ host`). Overview = Upcoming (enabled timed events, `n` Enqueue now)
+ Queued + In progress + History capped at 15; per-run `enter` prompt, `x` cancel, `m` mark free, `o` Open
results (full `Run.summary`/`reason` in a scrollable viewport overlay). Actions/Events = today's views grouped
by orchestrator. Queue = every retained run (daemon retention unchanged at 200 finished/orchestrator), `f`/`F`
filter, `]`/`[` pages of 25. Orchestrator admin (create/rename/remove; Workers/Reasoning/Hosts) moves to a new
**Orchestrators** option under Daemons. Data via client fan-out (`ListOrchestrators` + `ListRuns` per host,
`Event.orchestrator_changed` reloads one orchestrator); operations refactored onto an explicit `orchTarget`
hosted by a per-screen `orchSurface` (R1). Rules: agent/remote text sanitised before display (`sanitize.go`, R10,
FR-029); cursors keyed by item identity (R11); operations only via `connectedDaemon(host)`, never the
`daemonForHost` fallback (R12, FR-030); reloads per orchestrator, ordered by one process-wide call sequence (`jobsSeq`, stamped as each call is issued; NotFound = removed); lost hosts = event stream ended or `Unavailable`,
coalesced, only while Jobs is visible (R3); worker names from the aggregated sandbox list (R4). No proto/daemon/env change. See `research.md` (R1–R12), `data-model.md`, `contracts/jobs-ui.md`, `quickstart.md`.

Prior feature: `specs/009-orchestrator/plan.md` (Orchestrator). A new **daemon role**: an
orchestrator is a named, persistent prompt queue the developer places on a machine (hosted
in-process by that machine's `switchboardd`, no new binary/process/port), with a stable opaque
identity and a changeable name. It attaches sandboxes on any reachable machine as **workers**
(controller recorded on the sandbox by its **owning** daemon — `Sandbox.controller`, field 23 —
one controller per sandbox, settled in one bbolt write), composes prompts from developer-authored
**actions** (`{placeholder}` body + declared arguments + `prompt`/`reason` type + optional
worker-tag requirement; unreferenced supplied arguments are appended `name: value`) whenever an
**event** calls them (user form, timed event with dedupe-on-by-default, or a worker's agent through
MCP; webhooks deferred), and delivers each **run** in order of receipt, capacity-aware, through the
owning daemon's new guarded `DeliverWorkerPrompt` (RUNNING + agent IDLE + no run recorded; ensures
the PTY broadcaster exists so unattended output is drained). Runs end only with the agent's
**completion report** (MCP tool `orchestrator_report_completion` with a summary); idle without a
report ⇒ *awaiting review*, surfaced at the top of the sandbox list with `m` **mark free**. Cross-
machine: the orchestrator's daemon **dials worker machines over SSH** (`ssh <target> sxbd
dial-stdio`, BatchMode, per-orchestrator host entries) using the TUI's dialer **moved into a new
library module `src/libs/switchboard-dial`**, holds `Subscribe` + a bidi `OrchestratorLink` stream
per host, and reconciles on every reconnect. Workers reach the orchestrator through the host MCP
gateway: the owning daemon registers one **per-worker** server `sxb-orch-<sandbox8>` →
`http://127.0.0.1:<hookport>/orchestrator/w/<token>/mcp` (hidden from the gateway view, not in
`mcp_servers`; **loopback URL documentation-derived — quickstart Scenario 0 first**), served on
the existing hook HTTP server by the **official Go MCP SDK (`modelcontextprotocol/go-sdk v1.8.0`,
stateless streamable HTTP — the one new dependency)** with four tools (`orchestrator_list_actions`,
`_call_action`, `_check_run`, `_report_completion`), calls relayed over the link. Briefing = managed
`CLAUDE.md` block (shared `managedblock` helper) + a fixed run frame + tool metadata. Each
orchestrator has a **reasoning sandbox** (`LaunchRequest.EmptyWorkspace`, `reasoning-<id8>`, not
detachable, rebuilt by destroy+launch). Daemon packages: new `internal/orchestrator` (records in
bbolt buckets `orchestrators` + receipt-ordered `orchestrator_runs`, dispatch, scheduler, links)
and `internal/worker` (controller CAS, gateway registration, endpoint, relay, delivery, briefing);
`internal/agent` now sets `AGENT_STATUS_EXITED` on PTY exit and `Hub.Publish` takes the mutex.
Additive proto: 24 RPCs (19 orchestrator-side, 5 worker-side), `Event.orchestrator_changed` (8),
`NotificationKind` 6–8 (`RUN_FAILED`, `RUN_AWAITING_REVIEW`, `RUN_NEEDS_INPUT`; completions
silent), `LaunchSandboxRequest.controller` (7). TUI: third top-level **Orchestrators** screen in
the shift+←/→ cycle (views Workers/Actions/Queue/Events/Reasoning/Hosts, `huh` forms), header
strip naming all three screens, **per-host event streams** (today only the local daemon is
subscribed), workers hidden from All/host/group tabs unless they need the developer (needs-input
or review rows ordered first on every tab), `O` toggles the hidden **Orchestrator Sandboxes** tab,
`o` attach/detach (offers to copy the TUI's host entry), launch wizard `O`, and a 3-line list
delegate (`Spacing 0`) whose third line is `mcp: … · orchestrator: …`. **No new env vars**; all
bounds are constants (R15). See `research.md` (R1–R17), `data-model.md`, `contracts/`,
`quickstart.md`.

Prior feature: `specs/008-mcp-gateway-manager/plan.md` (MCP Gateway Manager & Daemon Screen + kit
schema v2). Each daemon gets a **gateway manager** — new package `switchboardd/internal/mcp` — that
drives the host runtime's documented `sbx mcp` verbs (`ls`, `auth status --json`, `add …
--skip-auth`, `rm`, `auth`, `load --sandbox`, and `create --static-mcp`; **all
documentation-derived, `sbx` still not installed in dev — quickstart Scenario 0 reconciles every row
of `contracts/sbx-mcp-cli.md` first**). Registration is always `--skip-auth`; **authorization is a
separate, 10-min-bounded, killable `sbx mcp auth` stream** whose URL is shown in the TUI (never
opened on the host). Switchboard owns a per-daemon **attach-by-default mark** + **default attach
mode** (`McpGatewaySettings` in a new bbolt bucket `mcp`): *additive* (default) = dynamic gateway +
`mcp load` after `create`, before the agent PTY starts; *exclusive* = `--static-mcp`. Launch records
`Sandbox.mcp_servers`/`mcp_attach_mode` (fields 21–22), fixed for life; stale/unauthorized marks are
skipped + logged, never a failed launch. Additive proto: 7 RPCs,
`McpAttachMode`/`McpServerKind`/`McpAuthState`, `DaemonInfo` 6–9 (`sbx_min_version`,
`runtime_baseline_met`, `mcp_gateway_available`/`_reason`), `Event.mcp_gateway_changed`. TUI:
top-level **daemon screen** (`shift+→/←`, cursors preserved; connect/disconnect reuse the hosts
password flow), **MCP gateway view**, **settings screen** (`,`, `settings.toml`, first toggle
`auto_connect_hosts`=true), and a **startup sign-in state machine** (keyless attempt → auth failure
opens a centered masked prompt → retry/skip, 30 s per attempt, never persisted). Kits: `store.Kit`
re-shaped to **schema v2** (`permissions.network`, `credentials[]` services,
`setup.{install,files,startup}`, `agentInstructions`), v1 **migrated on load, written on save** with
a drop report; `AttachBlockers()` refuses `A` before any RPC for sections `sbx kit add` rejects; v3
refused. `sbxkit.MinSbxVersion = "0.36.0"` checked per host; kit/MCP RPCs answer FAILED_PRECONDITION
below it; `sbx options --json` dropped (`--help` only); `sbx start`/`ls --json` flagged for
reconciliation. **Reconciled on a real host 2026-10-10**: the version command is `sbx version`
(`sbx version: v0.47.0 <commit>`), `--skip-auth` registers without a browser, `mcp ls` prints `NAME
TRANSPORT STATUS GATEWAY DETAIL` (no URL column — kind from TRANSPORT, STATUS/DETAIL carried on
`McpServer` 6–8), `auth status --json` is a `[{server_name,status}]` array, `mcp auth` prints the URL on its
own line, `mcp rm` prompts `(y/N)` unless `--force` (always passed), `sbx create … --kit … --static-mcp a,b`
composes, `mcp load <name> --sandbox <N>` works right after create in both gateway modes, and `sbx kit add`
recreates the container under the same name and refuses blocked sections with `error: kit "<kit>" declares
<section>, which the kit-add recreate flow does not yet apply; …`; `sbx ls --json` matches `ListStatuses`' three keys (formerly `IsRunning`; SWI-13);
`sbx start` **does not exist** — `SbxRunner.Start` is now the no-op `sbx exec <N> true`, which starts a
stopped sandbox in place (`bringUp` had always fallen back to destroy + relaunch; `sbx run -d --name` also
resumes but leaves a detached agent). Scenario 0 / T003 complete 2026-10-12; only T063 (real-host
quickstart run) remains. **FR-102 (added 2026-10-12)**: `M` on the sandbox list opens a per-sandbox MCP
overlay (attached servers + `a` picker of the daemon's other registered servers) backed by one additive
RPC `AttachMcpServer` → `Manager.AttachMcp` → `sbx mcp load` under the op latch, appending to
`Sandbox.mcp_servers` (SWI-3: `d` in the overlay detaches via `DetachMcpServer` → `sbx mcp unload`, behind a confirm). **FR-103/FR-104 (added 2026-10-12)**: remote MCP
registrations take `headers[]` (`"Name: value"` → one `--header` each, validated in `mcp.normalizeHeaders`;
credentials as `${placeholder}` ↔ secret `mcp:<server>:<placeholder>`), and each daemon has a **Secrets**
option (new package `switchboardd/internal/secret` driving `sbx secret ls/set/rm`; values only in
`SetSecretRequest` → CLI stdin, never argv/logs; `Event.secrets_changed`; gated on the runtime baseline).
Table parsing shared via `internal/sbxtable`. `sbx secret` argv is documentation-derived — T073 reconciles it. **No new env vars** (timeouts + baseline are constants). See `research.md` (R1–R12),
`data-model.md`, `contracts/`, `quickstart.md`.

Prior feature: `specs/007-edit-sandbox-sources/plan.md` (Edit Sandbox Sources). Makes an existing
sandbox's seeded folder set **editable in place** — add folders (copied/cloned exactly as at launch)
and remove them, on a **live** sandbox: no container stop/restart/recreate, no interruption to agent
sessions, terminals, or services, and the sandbox's state is **never** driven by an edit (a failed add
never ERRORs it). Two additive RPCs: `AddSandboxSources` (streams the existing `LaunchProgress` shape,
incl. the `blocked` resource gate) and `RemoveSandboxSources` (unary, targets = **exact recorded
`SourceRef.path` values**); **no new proto field** — `Sandbox.sources` just becomes mutable, and edits
reach other clients via the existing `Event.sandbox_changed` emit. Adds **stage under the workspace's
own `.switchboard/staging/` then `os.Rename` into place** (live agent never sees a half-copied tree;
failure = rollback, record untouched); removals are a `within()`-guarded `RemoveAll` on the workspace
*child* (the mount root is never disturbed — that's why, unlike `Refresh`, no stop is needed), and on
partial batch failure the **record follows the disk**. Guards: folder-name collision vs record ∪ disk ∪
batch, reserved `.switchboard`, clone-mode re-verifies `is_repo`, ≥1 source always remains, removal
refused while a declared service's `working_dir` sits in the folder (refuse, never auto-stop), and a new
per-sandbox try-acquire **operation latch** (also taken by `Refresh`) serializes seed-mutating ops.
Eligible states: RUNNING/STOPPED only. TUI: `S` sources overlay (`a` add via the launch browser,
`space`+`d` remove behind a `confirm.go` dialog); add timeout 30 min / remove 10 min, package constants.
**Zero new `sbx` surface, no new package, no new env vars.** ⚠️ Sole runtime assumption: the sandbox sees
host-side workspace edits **live** (the 003/005 bind-mount bet) — quickstart **Scenario 0** reconciles it
first; if a runtime snapshots instead, fall back to stop→mutate→bring-up inside the same RPCs. See
`spec.md` (FR-053..067), `research.md` (R1–R8), `data-model.md`, `contracts/switchboard-edit-sources.proto`,
`quickstart.md`.

Prior feature: `specs/006-port-forwarding/plan.md` (Port Forwarding). A kit declares long-running
**services** (name, start command, **listen port**, `IN_SANDBOX` | `ON_HOST`, optional `is_website` +
workspace-relative `working_dir`) in a second switchboard-owned sidecar — `kits/<id>/services.yaml` +
`pb.KitSpec.services`, never in the opaque `spec.yaml` — resolved per sandbox **later-kit-wins** and
persisted as `Sandbox.services`. The developer picks what to start from a per-sandbox list (`p`);
**nothing ever auto-starts** (not on kit attach, sandbox create/start, or daemon restart). Each running
service gets a free port **on the developer's own machine**: the **client** binds `127.0.0.1:0` and
relays each connection to the daemon over a new **bidi `ForwardPort` stream on the existing connection**
(one path for local *and* SSH hosts — no second ssh session, and the client is the sole port allocator so
FR-049 uniqueness is structural). Daemon-side, in-sandbox services are reached via
`sbx ports --publish 127.0.0.1:P:<listen>/tcp`; on-host services are dialled directly. `RUNNING` means
**reachable** — set only after a successful dial; a readiness failure inside a sandbox is diagnosed
against `/proc/net/tcp` so a **loopback-only bind** is named with its remedy. On-host port collisions are
the author's choice: a `{{port}}` token in the command gets a freshly allocated host port (concurrent
sandboxes coexist), otherwise the declared port is used and a second instance fails `PORT_IN_USE`. Stop
signals the **whole process tree**, waits a 10 s grace, force-kills, and releases the port only once the
listen port is observed free. Instances are **in-memory only**; observability via `Event.service_instance`
+ `NOTIFICATION_KIND_SERVICE_FAILED` (failures only — starts/stops are silent). New packages
`switchboardd/internal/portforward` and `switchboard-tui/internal/forward`; two new `Runner` methods
(`PublishPort`/`UnpublishPort`, `Exec`). See `spec.md`, `research.md` (R1–R9), `data-model.md`,
`contracts/` (switchboard-port-forwarding.proto, sbx-ports-cli.md), `quickstart.md`. Additive proto
revision, **no new env vars**. ⚠️ `sbx` still not installed in dev — and **`sbx exec` argv is assumed by
analogy, not documented**: it is the highest-risk reconciliation item (3 call sites).

Prior feature: `specs/005-escape-hatch/plan.md` (Escape Hatch). Lets a sandbox's AI run a small,
human-authored set of commands with a **fixed, trusted prefix** *outside* the sandbox — on the daemon's
host, in the sandbox's bind-mounted `workspace_path` — and delivers each result back to the agent even
with no terminal attached. Commands are a **structured, switchboard-owned** section on a client kit (a new
`kits/<id>/escape-hatch.yaml` sidecar + `pb.KitSpec.escape_hatch`), deliberately **NOT** in the opaque
Docker `spec.yaml` (`sbx` never sees them). Resolved per sandbox with **later-kit-wins** on name
collision, persisted as `Sandbox.escape_hatch_commands`. The AI invokes via a daemon-injected wrapper
(`<workspace>/.switchboard/escape-hatch <name> [--workspace <dir>] [-- <args...>]`) that POSTs to a **new
`/escape-hatch/run` route on the existing agent-hook HTTP server** (`host.docker.internal:8765`); the
daemon validates the name against the allowlist, validates any agent-supplied **arguments** (author's
`subcommands` list OR `args_pattern` regex — passed as **positional params**, never shell-re-parsed, so a
loose regex broadens args but can't inject) and **workspace** (`workspaces` literal/glob allowlist;
`--workspace` picks one), then runs `sh -c` with a bounded timeout (per-command or **30-min** default) +
**1 MiB** output cap + cancel-on-stop, and **pushes the outcome back into the agent via
`agent.Registry.Prompt`** (the detached-prompt PTY path; carriage-return-submitted). A rejected
argument/workspace is HTTP 400, nothing runs. New daemon package `internal/escapehatch` (executor/consent/runs/resolve/
inject/http). `requires-approval` commands block on a **5-min deny-by-default** window decided by a new
`DecideEscapeHatchRun` RPC + a `confirm.go`-style approval modal. Runs are **in-memory only** (session =
daemon uptime); observability via a new `Event.escape_hatch_run` arm + two `NotificationKind`s +
`ListEscapeHatchRuns`. Kit editor gains an escape-hatch section. See `spec.md`, `research.md` (R1–R7),
`data-model.md`, `contracts/` (switchboard-escape-hatch.proto, escape-hatch-http.md), `quickstart.md`.
Builds on 003 (hooks/PTY/hub) + 004 (kits); additive proto revision, one new daemon package, **no new
env vars**. ⚠️ `sbx` still not installed in dev — host argv stays stub-asserted.

Prior feature: `specs/004-sandbox-refresh-and-kits/spec.md` (Sandbox Refresh & Agent Kits). Two
additions to the sandbox list page. **Refresh** (`F`, confirmation-gated): deletes a sandbox's
retained workspace copy, re-seeds from its recorded sources, and brings it back up on the *same*
container (`Manager.Refresh`); the copy must be deleted, not copied over, because `duplicate`
crashes `EEXIST` on symlinks and never deletes. **Agent kits**: client-authored Docker Sandboxes
kits (`kind: mixin`), stored as real `kits/<id>/spec.yaml` under the client config dir and rendered
with `yaml.v3`; the daemon (`internal/kit`) only materializes the YAML into `SWITCHBOARDD_KIT_ROOT`
for `sbx`. Attach at creation (`--kit` per kit, launch wizard `K`) or to a running sandbox
(`sbx kit add`, list-page `A` — restarts the sandbox, so the daemon tears down its PTY first).
Kit manager is `K`; validation shells out to `sbx kit validate`. See that feature's `spec.md` and
`contracts/switchboard-kits.proto`. ⚠️ `sbx` is not installed in dev, so the kit CLI surface is
documentation-derived — `SbxRunner.AddKit`/`ValidateKit`/`kitFlags` are the call-sites to reconcile
first, each pinned by an argv-asserting test.

Prior feature: `specs/003-terminal-session-persistence/plan.md` (Terminal Session Persistence
& Sandbox Tags — the daemon keeps each running sandbox's PTY session alive so clients can
detach/reattach and see prior output, and AI prompts keep running with no terminal attached). A new
`services/switchboardd/internal/terminal` broadcaster reads the PTY once and tees it into a VT
emulator (`charmbracelet/x/vt`, fallback `hinshun/vt10x`) + a bounded scrollback ring + fan-out to N
clients, sending a snapshot on attach. Client side: an in-place TUI terminal view (`t`), an `sxb
attach` mode used by external terminals (`T`, one-per-sandbox, bring-to-front) and by workspace
auto-open (bare `sxb` inside a sandbox workspace), per-sandbox attachment counts, and a mutable
`tag` on each sandbox (daemon registry, mirrors `RenameSandbox`). See that feature's `spec.md`,
`research.md`, `data-model.md`, `contracts/` (switchboard-terminal.proto, cli-attach), and
`quickstart.md`. Builds on 001; adds one contract revision, no new module.

Earlier feature: `specs/002-release-channel/plan.md` (GitHub-Only Release Channel — GoReleaser-on-tag
GitHub Releases + a `curl | sh` SHA-256-verified install/update channel for `sxb`/`sxbd`; install
script and the `switchboard-update` in-app updater share one asset-naming + checksum contract).

Foundation: `specs/001-sandbox-session-manager/plan.md` (Sandbox Session Manager — Bubble Tea TUI +
per-host Go daemon; gRPC contract, bbolt registry, `sbx` orchestration). All features are
implemented in **Go**, which deviates from the constitution's TypeScript/pnpm/Biome/Vitest tooling;
deviations are recorded and justified in 001's Constitution Check and Complexity Tracking (a
constitution amendment is recommended). Feature 003 adds no new deviations.
<!-- SPECKIT END -->
