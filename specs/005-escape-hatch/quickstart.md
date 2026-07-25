# Quickstart: Escape Hatch — validation guide

**Feature**: `005-escape-hatch` | **Spec**: [spec.md](./spec.md) | **Plan**: [plan.md](./plan.md)

Runnable checks that prove the feature end-to-end. Follows the repo's Go workflow (Makefile targets;
`sbx` is not installed in dev, so host-`sbx` argv is asserted via stub scripts, per feature 001 R6).
References the contracts in `contracts/` and the entities in `data-model.md` rather than restating
them.

## Prerequisites

- Go toolchain (`go.work` covers all six modules); `make build` succeeds.
- No new external services and **no new env vars** (research R7) — nothing to add to `.env.example`.
- Daemon + client built: `make build`. Unit/coverage gates: `make test cover fmt-check vet lint env-check`.

## Automated gates (must pass before merge)

```
make fmt-check
make vet
make lint
make test          # unit; ≥90% per-module coverage (Rule VI)
make cover
make env-check     # unchanged: no new SWITCHBOARDD_* keys
make proto         # regenerate gen/*.pb.go after editing switchboard.proto; tree must be clean
```

Key new unit tests (colocated `_test.go`), each asserting one contract point:

| Area | Assertion |
|------|-----------|
| `internal/escapehatch` resolve | Later-attached kit overrides a same-named command (Q1). |
| `internal/escapehatch` allowlist | `POST /escape-hatch/run` with an unknown name ⇒ 404, zero execution (SC-004, edge case). |
| `internal/escapehatch` exec | Runs `sh -c` in `workspace_path`; captures stdout+stderr; sets `cmd.Dir`. |
| `internal/escapehatch` timeout | A command exceeding its (or the 30-min default) duration ⇒ `TIMED_OUT`, process killed, no orphan (SC-008). |
| `internal/escapehatch` output bound | Output over 1 MiB ⇒ truncated + `output_truncated=true`. |
| `internal/escapehatch` cancel | Sandbox leaving RUNNING cancels in-flight runs ⇒ `CANCELLED` (FR-041). |
| approval gate | `REQUIRES_APPROVAL` does not execute before approve; deny / 5-min window / non-approve input ⇒ `DENIED`, zero execution (SC-003, FR-039). |
| callback | Terminal outcome calls `agent.Registry.Prompt` with the outcome message even with no terminal attached (SC-005). |
| injection | Bring-up writes `.switchboard/escape-hatch` (0755) + a marker-delimited `CLAUDE.md` block; empty resolved set removes both (FR-037). |
| rule content | Injected `CLAUDE.md` block lists exactly the resolved commands with when-to-use + "runs on host" (FR-037 scenario 1). |
| kit editor | Add/edit/remove an escape-hatch command; abandoned edit leaves the stored kit + sidecar unchanged (FR-035, US1). |
| proto | New fields/enums/RPCs generate; `Sandbox.escape_hatch_commands` round-trips through the bbolt registry (no migration). |

## Manual walkthrough (maps to the user stories)

### US1 — Author a command on a kit
1. `sxb` → `K` (kit manager) → open a kit → new **Escape-hatch commands** section → `a` to add:
   name `install-deps`, command `pnpm install`, when-to-use, consent **auto-run**. Save (`ctrl+s`).
2. Reopen the kit → the command and all fields persist (from the `kits/<id>/escape-hatch.yaml`
   sidecar; **not** in `spec.yaml`). `v` (validate) still runs `sbx kit validate` on the Docker portion
   only.
3. Start a partial add, `esc` to abandon → the stored kit is unchanged.

### US2 — Auto-run and continue (headline)
1. Launch a sandbox with the kit attached (`K` in the wizard) — or attach to a running one (`A`).
2. Confirm the daemon injected `<workspace>/.switchboard/escape-hatch` and a `CLAUDE.md` escape-hatch
   block; `Sandbox.escape_hatch_commands` is persisted.
3. Have the agent invoke `install-deps` (it runs the wrapper). Observe: `pnpm install` runs **on the
   host** in the sandbox's `workspace_path`; `node_modules/` becomes visible **inside** the sandbox
   (bind mount, FR-038); the agent receives a follow-up message with exit status + output and continues.
4. Detach every terminal mid-run → the run still completes and the result still reaches the agent (SC-005).
5. A command that exits non-zero ⇒ the agent is told it FAILED with output (edge case), not stalled.

### US3 — The agent knows when/how
1. Inspect the sandbox's `CLAUDE.md` block: it lists exactly the attached commands, each with its
   when-to-use note and the on-host statement.
2. A sandbox with no escape-hatch commands ⇒ no block, no wrapper, nothing invokable (FR-037 scenario 3).

### US4 — Approval-gated command
1. Author a second command marked **requires-approval**; attach.
2. Agent invokes it → an approval prompt appears (new `screenApproval`, `confirm.go`-style) naming the
   sandbox + exact command + "runs on host"; a `NEEDS_APPROVAL` notification lands in the inbox.
3. **Approve** → it runs, result returns to the agent. **Deny** or wait out the 5-minute window or press
   an unrecognized key → no host execution; the agent is told it was declined (SC-003, FR-039).

### US5 — Observe and audit
1. While a run is in flight, the sandbox row shows a running / awaiting-approval badge (live via
   `Event.escape_hatch_run`).
2. After completion, `ListEscapeHatchRuns` (session review) shows the command, sandbox, status, exit
   status, and captured output (FR-042, SC-006). Records are in-memory — a daemon restart clears them (Q2).

## Out-of-scope reminders (do not test as features)
No general host shell, no AI-supplied arguments, no mid-run streaming to the agent, no interactive
commands, and no change to how sandboxes/kits/the daemon are otherwise provisioned (spec Out of Scope).
