<!--
SYNC IMPACT REPORT
==================
Version change: (unfilled template) → 2.3.1
Rationale: First materialization of the already-ratified constitution into
  `.specify/memory/constitution.md`. The normative content was previously
  vendored only as the rule files under `.claude/rules/shared/`. This file now
  mirrors that ratified state verbatim in substance; it introduces NO new
  semantic obligations, so the version tracks the existing ratified version
  (2.3.1) rather than incrementing.

Principles (materialized from `.claude/rules/shared/`):
  I.    Automated Formatting Is the Source of Truth      (formatting.md)
  II.   Linting Is Non-Negotiable                        (linting.md)
  III.  Type Safety (NON-NEGOTIABLE)                     (type-safety.md)
  IV.   Consistent Naming and File Layout                (naming-and-layout.md)
  V.    Verification Before Merge                        (verification-before-merge.md)
  VI.   Multi-Level Testing Discipline (NON-NEGOTIABLE)  (testing-discipline.md)
  VII.  Containerized Deployment Surface                 (containerized-deployment.md)
  VIII. Environment Variable Discipline (NON-NEGOTIABLE) (environment-variables.md)

Added sections (beyond the 5-principle template scaffold):
  + Three additional principles (template shipped 5 slots; constitution has 8).
  + Repository Structure         (repository-structure.md)
  + Tooling Standards            (tooling-standards.md)
  + Development Workflow         (development-workflow.md)

Removed sections: none.

Templates / artifacts checked:
  ✅ .specify/templates/plan-template.md — Constitution Check gate is generic
       (`[Gates determined based on constitution file]`); reads this file
       dynamically, no edit required.
  ✅ .specify/templates/spec-template.md — no constitution-specific obligations; no edit required.
  ✅ .specify/templates/tasks-template.md — no constitution-specific obligations; no edit required.
  ✅ .specify/templates/checklist-template.md — no constitution references; no edit required.
  ✅ CLAUDE.md — already cites Version 2.3.1 / ratified 2026-05-03; consistent.
  ✅ .claude/rules/shared/*.md — canonical source of normative text; this file
       summarizes and MUST NOT contradict them (see Governance).

Follow-up TODOs: none.
-->

# Switchboard Constitution

The full normative text of each principle lives in the ratified rule files under
`.claude/rules/shared/`. This document is the authoritative index and summary of that
ratified set; where this summary and a rule file appear to diverge, the rule file's
detailed text governs and this summary MUST be corrected.

## Core Principles

### I. Automated Formatting Is the Source of Truth

All committed source code MUST be formatted by the project's configured formatter (Biome
for TypeScript, JavaScript, JSX/TSX, JSON, and CSS). Manual formatting decisions (spacing,
line breaks, quote style, trailing commas) are NOT permitted; the formatter's output is
authoritative. Code that does not match formatter output MUST be rejected at review and
MUST fail CI.

**Rationale**: Eliminates style debates in review, keeps diffs focused on behavior changes,
and removes a class of bikeshedding from the workflow. (See `formatting.md`.)

### II. Linting Is Non-Negotiable

Biome's linter MUST run on every TypeScript/JavaScript file with the project's shared
configuration. Lint errors MUST fail CI. Lint warnings MUST be triaged before merge —
either fixed or explicitly suppressed inline with a justification comment
(`// biome-ignore <rule>: <reason>`). Blanket suppressions (file- or directory-level
disables, or wide `overrides`/`includes` exemptions in `biome.json`) require approval in
the PR description.

**Rationale**: Linting catches real bugs (unused vars, accidental promises, unsafe types)
and enforces consistency formatters cannot. One Biome installation for both formatting and
linting collapses two toolchains into one. (See `linting.md`.)

### III. Type Safety (NON-NEGOTIABLE)

All TypeScript packages MUST compile under `"strict": true` (implying `strictNullChecks`,
`noImplicitAny`, and the rest of the strict family). `any` is prohibited except at
well-documented external boundaries, and each use MUST carry an inline comment naming the
boundary. `// @ts-ignore` is forbidden; use `// @ts-expect-error -- <reason>` so the
suppression fails loudly when the underlying issue is fixed.

**Rationale**: A monorepo accumulates implicit contracts between packages; strict typing
turns those contracts into enforced ones and prevents silent drift. (See `type-safety.md`.)

### IV. Consistent Naming and File Layout

Naming MUST follow these conventions across all packages: files and directories in
`kebab-case`; TypeScript types/interfaces/classes/React components in `PascalCase`;
variables/functions/methods in `camelCase`; top-level immutable configuration constants
(including env var names) in `UPPER_SNAKE_CASE`. Workspace package names are
`@tms/<kebab-name>` (the name need not encode category). Each package keeps source under
`src/` and emitted output under `dist/`. Test files sit next to the code they cover in a
`__tests/` subdirectory mirroring the filename (`pkg/src/foo.ts` ↔
`pkg/src/__tests/foo.test.ts`). Packages MAY document and justify a different layout in
their README, but the `__tests/` colocation pattern is the default.

**Rationale**: Predictable layout makes the monorepo navigable without tooling assistance
and prevents per-package drift as the codebase grows. (See `naming-and-layout.md`.)

### V. Verification Before Merge

These checks MUST pass on every pull request, blocking merge on failure:
`pnpm -r run format:check`, `lint`, `typecheck`, `test:unit`, `test:integration`,
`test:e2e` (Playwright against the local Docker build), and `env:check`. In addition, a
Husky-managed `pre-commit` hook MUST run the fast subset — `format:check`, `lint`,
`typecheck`, `test:unit`, and `env:check` — locally before commit; integration and E2E
layers are CI-only because their runtime is unsuitable for an interactive gate. Husky MUST
be installed at the workspace root and bootstrapped via a `prepare` script
(`"prepare": "husky"`). The hook MAY use a staged-files runner for formatter/linter scope,
but typecheck, unit tests, and `env:check` MUST run against the full workspace. Bypassing
the hook or CI (`--no-verify`, force-merge, skipping required checks) requires explicit
approval recorded in the PR description. The local hook does NOT replace CI; both are
mandatory.

**Rationale**: Quality checks only protect the codebase if executed automatically and
consistently. CI is the merge gate; the Husky hook catches violations before push, saving a
CI round-trip and keeping main green. (See `verification-before-merge.md`.)

### VI. Multi-Level Testing Discipline (NON-NEGOTIABLE)

Automated testing is mandatory at three levels. **Unit** tests exercise a single unit in
isolation: Vitest for functional code; Storybook (Vitest addon, headless) for visual code,
covering all five modes — snapshot, visual regression, accessibility, interaction, render.
**Integration** tests exercise the seam between units or between code and dependencies:
Vitest for functional code, Storybook interaction mode only for visual code (the other four
modes belong at the unit layer and MUST NOT be duplicated), with all HTTP/network boundaries
mocked via MSW (ad-hoc fetch/SDK mocks are forbidden). **E2E** tests drive a deployed app via
Playwright; they live in a dedicated `<app>-e2e` package sibling to the target inside the
same category, MUST honor the `E2E_TARGET` env contract (local-Docker vs. remote), and CI
MUST run them against the local Docker build on every PR. Unit and integration tests MUST NOT
make real network requests. Coverage (NON-NEGOTIABLE): every package's `test:unit` and
`test:integration` suites MUST reach at least 90% on lines, branches, functions, and
statements, measured per package (not as a workspace average) with `@vitest/coverage-v8`;
falling below any metric fails CI. Reviewers MUST examine the coverage delta, not just the
total. E2E coverage is intentionally out of scope for this threshold.

**Rationale**: The three layers each catch a different class of bug; standardizing on
Vitest + Storybook + MSW + Playwright keeps onboarding cheap, and the 90% floor turns "we
have tests" into a verifiable, non-eroding property. (See `testing-discipline.md`.)

### VII. Containerized Deployment Surface

Every package under `src/apps/` and `src/services/` MUST be fully containerized, and the
repo MUST be bootable with a single command. Each app/service MUST publish a self-contained
`Dockerfile` at its package root (multi-stage for TS packages; no `.git`, dev deps, test
fixtures, or `.env` baked in — enforce via `.dockerignore`) AND a per-package
`docker-compose.yml` whose `services:` block contains exactly what the package owns.
Supporting infrastructure (databases, queues, MSW mocks, object storage) MUST be declared in
the per-package compose file of the package that owns its schema/contract/client. The
repo-root `docker-compose.yml` is an index of `include:` directives only — inline
`services:`/`volumes:`/`networks:` are NOT permitted — and MUST `include:` every app, every
service, and every infra-owning package; adding any of these REQUIRES creating its
per-package compose file and appending one `include:` line in the same PR. `docker compose up`
from the root MUST bring the entire merged stack online; intentionally non-default packages
MUST use a named compose `profile` documented in the README. Inter-service ordering MUST be
expressed via `depends_on` (with `condition: service_healthy` where a healthcheck exists).
This same merged stack is the local-Docker `E2E_TARGET` for Principle VI.

**Rationale**: Constraining all deployable units to one container runtime and one merged
compose stack makes "boot the whole system" a single command, keeps ownership local, and
keeps CI and laptop environments in lockstep. (See `containerized-deployment.md`.)

### VIII. Environment Variable Discipline (NON-NEGOTIABLE)

Every package that reads any environment variable MUST: (1) own a package-local, gitignored
`.env` for local development and never walk up the tree for values; (2) expose exactly one
`env.ts` declaring a **Zod** schema, parsing `process.env` at module load, and exporting a
single typed `env` object — direct `process.env.<X>` reads outside `env.ts` are forbidden
(the only exception is the `dotenv`/`dotenvx` loader call in the entrypoint), and parse
failure MUST throw at startup naming the offending key(s); (3) ship a committed
`.env.example` with safe placeholder values documenting every schema key; (4) keep the schema
key-set and `.env.example` key-set in exact lockstep. The `env:check` script enforces this
equality programmatically and verifies that `.env.example` is tracked while `.env` is not;
any drift fails CI. A package that genuinely consumes no env vars MAY omit these files until
it adds its first `process.env.<X>` access.

**Rationale**: Centralizing env access through one Zod-parsed module turns runtime config
errors into actionable startup failures, gives the type system real knowledge of required
configuration, and prevents the most common drift mode — a new schema key nobody else's
`.env` knows about. (See `environment-variables.md`.)

## Repository Structure

All source lives under `src/`, whose first level is restricted to exactly six canonical
categories; adding a seventh requires a constitution amendment:

| Category       | Path                | Contains |
|----------------|---------------------|----------|
| `apps`         | `src/apps/`         | Deployable frontends (anything an end user sees) |
| `services`     | `src/services/`     | Deployable backends (anything exposing a reachable API) |
| `libs`         | `src/libs/`         | Shared, framework-agnostic TS — no UI, no DB I/O |
| `repositories` | `src/repositories/` | All DB-touching code (schemas, migrations, query layers, ORMs) |
| `components`   | `src/components/`   | Reusable presentational UI components |
| `features`     | `src/features/`     | Drop-in end-to-end feature slices (UI + data + actions) |

Every package MUST live exactly one level deep — `src/<category>/<name>/`; never depth-one
under `src/`, never depth-three without an amendment. The implied pnpm workspace pattern is
`src/*/*`, and `pnpm-workspace.yaml` MUST reflect it. Cross-category dependency direction
(recommended): `apps → features → (components, services, repositories) → libs`; `libs` MUST
NOT import from any other category, and cross-category cycles MUST be avoided. E2E packages
mirror their target inside the same category (`src/apps/<app>-e2e/`,
`src/services/<svc>-e2e/`) and do NOT warrant their own top-level category.
(See `repository-structure.md`.)

## Tooling Standards

The following tools are the canonical implementations of these principles; substitutions
require a constitution amendment:

- **Package manager**: pnpm with workspaces; `pnpm-lock.yaml` MUST be committed.
- **Formatter + linter**: Biome only, configured by a single root `biome.json` — separate
  `prettier`/`eslint` toolchains MUST NOT be reintroduced.
- **Type checker**: TypeScript `"strict": true`; each package has a `tsconfig.json` (MAY
  extend a shared base).
- **Unit/integration runner**: Vitest; per-package `vitest.config.ts` extends a shared base.
- **Coverage**: `@vitest/coverage-v8` with `provider: 'v8'` and 90% per-metric thresholds
  (Principle VI). Alternative providers require an amendment.
- **Component workshop / visual testing**: Storybook with colocated stories, run headlessly
  in CI across all five visual modes.
- **HTTP mocking**: MSW (`msw`); a shared `handlers/` module MAY be exposed via a workspace
  package.
- **E2E framework**: Playwright; each `<app>-e2e` package ships a `playwright.config.ts`
  honoring the `E2E_TARGET` contract.
- **Env validation**: **Zod** inside each package's `env.ts`; loader is **`dotenv`**/
  `dotenvx` (or the framework-native loader for Vite/Next.js apps). Alternatives (`yup`,
  `joi`, `valibot`, hand-rolled) require an amendment.
- **Container runtime**: Docker, composed from a single root `docker-compose.yml` using
  `include:` directives (Principle VII).
- **Git hook manager**: Husky at the workspace root, hooks checked into `.husky/`,
  bootstrapped by the `prepare` script.
- **Editor config / line endings**: a root `.editorconfig` MUST define charset (utf-8), EOL
  (lf), final-newline (true), and indentation consistent with Biome. LF only; CRLF MUST NOT
  enter the repository.

(See `tooling-standards.md`.)

## Development Workflow

Formatter, linter, type-check, and test scripts MUST be exposed with uniform names so they
run workspace-wide via `pnpm -r run <script>`: `format`, `format:check`, `lint`, `typecheck`,
`test:unit`, `test:integration`, `test:e2e`, `test` (alias for `test:unit`), and `env:check`.
Packages with no scope for a given check expose a no-op so the recursive run still succeeds;
only `<app>-e2e` packages need implement `test:e2e`; every package that ships an `env.ts` MUST
implement `env:check`. E2E packages MUST be siblings named `<app>-e2e` in the target's
category and MUST read `E2E_TARGET` to switch targets without code changes. Generated,
vendored, or third-party files (`dist/`, build outputs, `node_modules/`) MUST be excluded from
Biome formatting/linting and from `coverage.exclude` in `vitest.config.ts`. New packages under
`src/` MUST inherit the workspace-wide tooling configuration, including the `env.ts` +
`.env.example` scaffold when they read any env var; opt-outs MUST be documented in the
package README. Code review MUST verify that inline suppressions (`biome-ignore`,
`@ts-expect-error`, `any`) carry justification comments and that no `process.env.<X>` reads
exist outside `env.ts`. (See `development-workflow.md`.)

## Governance

This constitution supersedes ad-hoc style preferences and individual contributor habits. When
code, review feedback, or tooling configuration conflicts with any principle, the constitution
wins until amended. Day-to-day implementation guidance lives in `CLAUDE.md` and the active
feature plans under `specs/`; those documents MUST NOT contradict these principles, and if they
appear to, the constitution governs and the guidance file MUST be corrected. The detailed
normative text for each principle is the corresponding file under `.claude/rules/shared/`; this
document and those files MUST stay in sync.

**Amendment procedure**:

1. Open a PR that modifies `.specify/memory/constitution.md` and the affected rule file(s)
   under `.claude/rules/`.
2. Include a Sync Impact Report (as an HTML comment at the top of this file) describing the
   version bump, changed principles, and templates touched.
3. Bump `CONSTITUTION_VERSION` per semantic versioning:
   - **MAJOR**: Removing a principle, redefining a principle in a backwards-incompatible way,
     or changing governance procedure.
   - **MINOR**: Adding a new principle or materially expanding an existing one.
   - **PATCH**: Wording clarifications, typo fixes, non-semantic refinements.
4. Update `LAST_AMENDED_DATE` to the merge date (ISO `YYYY-MM-DD`).

**Compliance review**: Reviewers MUST confirm constitutional compliance before approving a PR.
Plans generated via `/speckit-plan` MUST gate on the Constitution Check section against the
rules in `.claude/rules/`; violations MUST be recorded in the plan's Complexity Tracking table
with explicit justification, or the plan revised to comply.

**Version**: 2.3.1 | **Ratified**: 2026-05-03 | **Last Amended**: 2026-05-09
