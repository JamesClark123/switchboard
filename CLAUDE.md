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
Active feature: `specs/004-sandbox-refresh-and-kits/spec.md` (Sandbox Refresh & Agent Kits). Two
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
