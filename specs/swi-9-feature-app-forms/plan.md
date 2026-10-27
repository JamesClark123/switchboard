# Implementation Plan: App Forms — Enlargeable Text Fields & Unsaved-Changes Guard

**Branch**: `jamesloganclark/swi-9-feature-app-forms` (spec) → `…-tasks` (plan + tasks) → `…-implement` | **Date**: 2026-10-27 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/swi-9-feature-app-forms/spec.md`

## Summary

This plan formalises how the TUI's forms and inputs handle long text and leaving with unsaved edits:

1. **Enlarge.** ctrl+e on any multi-line field opens an in-app, full-terminal, editable view of the field.
   - The view is a plain Bubbles textarea with no line cap. It writes back into the same bound value when it
     closes (esc / ctrl+e), when the form is saved (ctrl+s), or before ctrl+c leaves the form.
   - `ctrl+r` reverts the view to the text it opened with.
   - huh's external editor is removed for good through a shared keymap with a keyless `Editor` binding (R1–R2).
2. **Unsaved-changes guard.** Every editing surface gets a guard: the six multi-field form surfaces (config, MCP,
   secrets, orchestrator create/rename, action/event/call, mark-free), the kit editor's item forms and overview, the launch wizard and reasoning setup, the sources overlay and add browser, and the four inline
   inputs.
   - Each surface's exits (esc; ctrl+c in forms; `q` on the kit overview and the sources overlay; esc in the add
     browser) go through one dispatcher. It leaves immediately when the surface's current values equal its opening baseline (R3). Otherwise it
     opens one shared three-choice dialog: **s** Save / **c** Cancel / **d** Discard, with Cancel highlighted and
     esc = Cancel (R4–R5). On the reasoning setup Save is **r** Rebuild, which opens a destructive confirm built from the
     **edited** setup. FR-017 is amended in this PR for that extra confirm (R5). For 150 ms after the dialog opens only esc/ctrl+c act, so keys typed after a stray esc never act.
   - Save reuses each surface's existing save or confirm unchanged. Discard runs today's esc path.
3. **Related fixes.**
   - Nested and follow-up behaviour: kit item → kit overview levels; MCP "register anyway?" returns to the form;
     the sources removal confirm completes or drops the exit (R6).
   - Forced closes (disconnect, removed object) tear the guard down with an "unsaved edits lost: …" status (R7).

The change is **TUI-only**: no proto, daemon, storage or env change.

## Technical Context

**Language/Version**: Go 1.26 (workspace `go.work`; module `src/apps/switchboard-tui`)

**Primary Dependencies**: Bubble Tea, Bubbles (`textarea`, `textinput`, `key`), Lip Gloss, `huh` v1.0.0. There are
no new dependencies. huh is not forked; behaviour is changed only through its public `KeyMap`, `ExternalEditor`,
`Value` and `GetFocusedField` APIs.

**Storage**: N/A. Baselines and dialog state are in-memory per open surface. The kit file format is unchanged.

**Testing**: `go test` with the package's existing helpers (`update`/`press`/`runCmd`/`sized`, `kitModel`, `mcpModel`,
`secretsModel`, `openDaemonOrchs`, `jobsWith`, `sourcesModel`, `fakeDaemon`). Gates are
`make fmt-check vet lint test cover` (≥ 90% per module) and `make e2e`. The SC-006 timings ship as Go benchmarks.

**Target Platform**: Linux/macOS terminals (the TUI `sxb`)

**Project Type**: CLI/TUI client (single Go module change)

**Performance Goals**: SC-006 requires each keystroke's result within 100 ms on a 200×50 terminal with 5,000 lines of
text.
- Measured worst case: 54.6 ms, typing in the focused inline `huh.Text` holding 5,000 lines. The enlarged view is
  ~35 ms (R9).
- Paging in the enlarged view batches cursor moves and repositions the view once (~37 ms), instead of replaying N
  arrow keys (~377 ms).

**Constraints**:
- No external editor anywhere (FR-009).
- The host-password and sign-in overlays are always drawn last, over every screen and layer, so the prompt is never
  hidden while it captures keys (R5).
- `reconcileGuardLayers` runs after every message and must stay O(1) (R4).
- The dialog never discards on a single stray key (FR-019): Cancel is the default, and enter discards only when
  Discard is highlighted.
- At 80×24 the enlarged view shows 21 text lines with a 76-cell hint, and the dialog's hint fits its 49-cell content
  width (FR-021, SC-003).
- No new env vars; all sizes are package constants.

**Scale/Scope**:
- ~15 guarded surfaces across ~20 files in `internal/ui`.
- 17 multi-line fields: 13 kit, 1 MCP, 3 orchestrator.
- Text up to the textarea's 10,000-line limit (FR-009a).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack. Features 001–010 are
implemented in **Go**, a deviation recorded and justified in **001's Constitution Check + Complexity Tracking**.
SWI-9 stays inside that deviation and **adds no new ones**:

| Principle | Status for SWI-9 |
|-----------|------------------|
| I Formatting / II Linting | ✅ `gofmt` + `golangci-lint` via `make fmt-check`/`lint` (the Go analogue of Biome, per 001). |
| III Type Safety | ✅ Go static typing; guarded surfaces are a typed enum with switch dispatch; snapshots are typed structs and maps compared with `==`/`maps.Equal`/`slices.Equal`, with no `interface{}` payloads. |
| IV Naming & Layout | ✅ New files in the existing `internal/ui` package with colocated `_test.go` (`formguard.go`, `enlarge.go`, `unsaved.go`). |
| V Verification Before Merge | ✅ Same `make` gates (`fmt-check`, `vet`, `lint`, `test`, `cover`, `env-check`, `e2e`). |
| VI Multi-Level Testing | ✅ Unit tests for the dirty snapshots, dialog and enlarged view; TUI behaviour per surface through the existing model-driving helpers and `fakeDaemon`; benchmarks for SC-006; ≥ 90% coverage held for `switchboard-tui`. Storybook/Playwright/MSW remain TS-only, out of scope under 001's deviation. |
| VII Containerized Deployment | ✅ N/A: client binary (001 carve-out). |
| VIII Env Discipline | ✅ No env vars added; no `$EDITOR` read any more. |
| Repository Structure | ✅ Changes confined to `src/apps/switchboard-tui`, plus spec/README/CLAUDE.md docs. |

**Result**: PASS. **Re-checked after Phase 1 design: still PASS.** The design adds TUI code and docs only, with no new
module, dependency, deployable, proto field or env var.

## Project Structure

### Documentation (this feature)

```text
specs/swi-9-feature-app-forms/
├── plan.md              # This file
├── research.md          # Phase 0: R1..R10 decisions (incl. the FR-009a round-trip check and SC-006 measurements)
├── data-model.md        # Phase 1: guarded surface, baseline/snapshot, requested exit, enlarged view, dialog
├── quickstart.md        # Phase 1: validation guide
├── contracts/
│   └── forms-ui.md      # Phase 1: keys, dialog, enlarged view and the per-surface exit/save table
├── checklists/          # (from the specify phase)
└── tasks.md             # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
src/apps/switchboard-tui/internal/ui/
├── formguard.go           # NEW: formKeyMap() (was kitFormKeyMap; keyless Editor + Quit), formSpec + text() registry,
│                          #   guardSurface enum, activeGuard / guardDirty / guardSave / guardLeave dispatch, requestExit,
│                          #   formKeys pre-handler (esc / ctrl+c / ctrl+e / ctrl+s before huh), forceClose, kitFingerprint
├── enlarge.go             # NEW: enlargeState (textarea MaxHeight 0, baseline, bound field + ptr), keys (close, ctrl+s, ctrl+c,
│                          #   ctrl+r, pgup/pgdn, tab no-op), write-back, full-screen view, resize
├── unsaved.go             # NEW: unsavedState dialog (Save|Rebuild / Cancel / Discard, highlight, keys), modal render
├── app.go                 # handleKey: dialog layer → enlarged layer before the screen switch; View: enlarged view replaces
│                          #   chrome, dialog overlays; WindowSize re-fits the enlarged view; footer adds `ctrl+e enlarge`
├── keys.go                # dialog / enlarge bindings
├── confirm.go             # confirmState.onAccept hook (y-only effects: sources Save closes + resets the overlay;
│                          #   Rebuild goes to the detail) and graceUntil (dequeued add-overrides ignore y/enter 150 ms)
├── terminal.go            # termOpenedMsg switches screens only from screenList (else detaches); termClosedMsg only
│                          #   leaves screenTerminal
├── config_editor.go       # heap *configFormVals (manifest option maps); guard (gConfig); formKeyMap
├── mcp.go                 # spec.text for Headers; guard (gMcp); FR-012a: keep vals across "register anyway?", reopen on back-out
├── secrets.go             # guard (gSecrets)
├── orchestrators.go       # create/rename guard (gOrchAdmin); orchestrator-removed closes (R7)
├── orchestrator_forms.go  # spec.text for Body / Arguments / Fixed arguments; op-form guard (gOpForm); drop StateAborted path
├── run_actions.go         # newOrchForm on formKeyMap; mark-free baseline; ctrl+c ≡ esc incl. return to list
├── orchestrator_views.go  # detail form keys through the guard
├── daemon_orchestrators.go# admin form keys through the guard
├── jobs.go                # Jobs op form keys through the guard
├── kit_editor.go          # spec.text for 13 fields; gKitItem (apply) + gKitOverview (save; baseline after migration)
├── launch.go              # gLaunch snapshot/guard; footer help aligned in reasoning mode
├── orchestrator_reasoning.go # gReasoning (Save labelled Rebuild → confirmRebuildFromSetup: confirm built from the
│                             #   edited setup); baseline = pre-filled saved setup
├── sources.go             # gSources (Save → removal confirm with exitAfter) + gSourcesBrowse; parked add-override (R7)
├── tageditor.go           # gTag
├── hosts.go               # gHostAdd; forced-close teardown on host drop
├── groups.go              # gGroupAdd
├── sandbox_list.go / notifications.go # sandbox removed → forceClose rename/tag/sources
└── *_test.go              # formguard_test.go, enlarge_test.go, unsaved_test.go, guard_surfaces_test.go,
                           #   enlarge_perf_test.go (BenchmarkSC006*), plus updates to existing surface tests
```

**Structure Decision**: a single-module change inside `src/apps/switchboard-tui/internal/ui`. The three new files hold
the shared machinery (keymap and registry, enlarged view, dialog). Each surface file changes only to register its
Text fields, give its baseline/snapshot, and route its exits through `requestExit`.

## Complexity Tracking

No new constitution violations. The Go-stack deviation is inherited from 001 and unchanged.
