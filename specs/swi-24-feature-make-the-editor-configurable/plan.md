# Implementation Plan: Configurable Editor for the Open-in-Editor Key

**Branch**: `jamesloganclark/swi-24-feature-make-the-editor-configurable` (spec) → `…-tasks` (plan + tasks) →
`…-implement` | **Date**: 2026-11-03 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/swi-24-feature-make-the-editor-configurable/spec.md`

## Summary

`v` opens a sandbox's workspace folder in the editor chosen on the settings screen instead of always VS Code.
The feature is **TUI-only**: the daemon, the proto and bbolt are unchanged.

1. **Catalogue and launch** (research R1, R7, R8). A new package `internal/editor` replaces `internal/vscode`.
   It holds the ten built-in editors plus Custom (FR-501), the FR-531 tokenizer and the FR-506 checks, the
   FR-517 path limit, Zed's `ssh://` address (confirmed from Zed's source, F5) and a detached launcher
   (`Setsid`, `/dev/null` stdio, a reaper that reports a non-zero exit within 3 s). `Precheck` runs everything
   that doesn't need the workspace path before the daemon call (FR-526); `Resolve` runs the path checks after.
2. **Override variables** (R2). One `SWITCHBOARD_<EDITOR>_BIN` per built-in editor, declared in `config.schema`
   from the catalogue so env-check and the README list them (FR-503); the raw value is kept so "set" is known
   (FR-502, FR-517). `SWITCHBOARD_CODE_BIN` is unchanged.
3. **Settings** (R3, R4, R11). `settings.toml` gains `editor` and `editor_custom_command`. Every save re-reads
   the file and writes only the changed keys through a new owner-only writer (`0600`, fresh temp, FR-527,
   FR-530). Three file conditions (unreadable at startup, broken mid-session, unknown editor) each have one
   field and one derived notice. The settings screen gains an **Editor** row, a list and a guarded command input
   (SWI-9's dialog through a new `gEditorCommand` surface).
4. **`v` pipeline** (R5, R12, R13). `openEditorCmd` builds the request from the session, refuses at once where it
   can, runs `LookPath` and the daemon call in its command, and reports through two new messages that clear
   only the row's spinner and never reload. Messages are assembled from typed refusals with the notices appended
   and outside text cut first (FR-529, FR-532, FR-533).
5. **Status line** (R9, R10). `footer` wraps and cuts the status to a per-screen limit, the lines come out of
   the body, and a keyed size check in the `Update` wrapper resizes the active surface when its height differs
   from its target. The terminal screen keeps a fixed PTY size and a 2-line status. A `v` failure or refusal
   has its own error style and is held through an in-place attach or its Replace dialog, then released, joined
   to a terminal error when one is showing (FR-533, SC-008).
6. **Docs and tests** (R14, R15). README env table, key table and an Editor section; unit tests per package, SC-003
   argv tests, SC-004/SC-008 layout tests at 80×24, and three e2e flows with a stub editor on `PATH`.

Both spec open items are settled: Zed's address form, encoding and target normalisation come from Zed's
`open_listener.rs` (F5); Windsurf's `--remote` support could not be verified from an official source, so its row
(and Cursor's) ship as documentation-derived and quickstart Scenario 0 confirms them (F6, R8).

## Technical Context

**Language/Version**: Go 1.26 (`go.work`; modules `switchboard-tui`, `switchboard-tui-e2e`; nothing in
`switchboardd` or `switchboard-proto` changes)

**Primary Dependencies**: Bubble Tea v1.3.10, Bubbles v1.0.0 (`textinput`, `list`), huh v1.0.0, Lip Gloss,
`charmbracelet/x/ansi v0.11.6` (`Wrap`, `Truncate`, `TruncateLeft`), `pelletier/go-toml/v2 v2.2.3`, the
standard library (`os/exec`, `syscall.SysProcAttr`, `net/url`, `unicode`). **No new dependencies.**

**Storage**: `settings.toml` under the client config dir (two new keys, contract `settings-file.md`). No daemon
state.

**Testing**: `go test` per module; `ui` tests use `fakeDaemon`, `update`, `press`, `sized`, `noKeyGrace` and a
recording launcher; `editor` tests use stand-in scripts for the launcher and tables for the rules; `store` tests
check modes and merges in `t.TempDir()`; e2e (`switchboard-tui-e2e`) drives `sxb` through a PTY with a stub
editor on `PATH`. Gates: `make fmt-check vet lint test cover` (≥ 90 % per module), `make env-check`, `make e2e`.

**Target Platform**: Linux and macOS clients (`sxb`); the release targets are `darwin`/`linux` only, so
`Setsid` needs no build tags.

**Project Type**: TUI client change inside the existing three-module layout (one module touched, one e2e).

**Performance Goals**: `v` adds a few microseconds of parsing on the update loop and one `exec.LookPath` inside
its command. An empty or one-line status adds no help render per frame: `statusLineCount` returns before it
computes a limit, and `chrome` renders the help once from its own `hb` and reuses the count (the help calculation is
≈ 295 µs and 453 KB at 80×24 with 8 sandboxes, measured in review, so `BenchmarkViewList` keeps this visible; R9). The
size check runs on every message but computes its target only when its key changes (status,
size, banner, surface, tabs, mark-free); wrapping a 280-column status is ≈ 7 µs, the list target ≈ 300 µs (spec
Notes). No new polling; per launch, one goroutine waits for the editor to exit (reaping it, FR-511) and one
decides the report within 3 s.

**Constraints**:
- No shell: Custom commands run through `exec.Command` with the split argv; markers are replaced inside words.
- Secrets: outside the input only the executable is shown; rejections name rules, never text (FR-532).
- `settings.toml` is `0600` and never overwritten when it can't be read (FR-525, FR-527).
- Backwards compatibility: with no settings and no new variables, `v`'s argv, footer text and completion
  message are byte-identical to today's (SC-002); `SWITCHBOARD_CODE_BIN` keeps its name and meaning.
- Every spec-defined `v` message fits in 4 lines at 80 columns with values at their limits (SC-004); FR-518's
  built-in case is on the edge and its wording is measured and shortened per `contracts/editor-launch.md` §4.
- Limits are package constants: 3 s exit grace, 6 status lines, 2 on the terminal screen, 60/120/78/40 columns.

**Scale/Scope**: 11 catalogue rows, 2 settings keys, 9 new env vars. About 8 new files in `internal/editor`, 3
new and ~11 edited files in `internal/ui`, 2 in `internal/store`, 1 in `internal/config`, `main.go`, README,
`.env.example`, and 3 e2e tests.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The constitution mandates a TypeScript/pnpm/Biome/Vitest/Storybook/Playwright/MSW/Docker stack. Features 001–010,
SWI-9, SWI-17, SWI-18 and SWI-21 are implemented in **Go**, a deviation recorded and justified in **001's
Constitution Check + Complexity Tracking**. SWI-24 stays inside that deviation and **adds no new ones**:

| Principle | Status for SWI-24 |
|-----------|-------------------|
| I Formatting / II Linting | ✅ `gofmt` + `goimports` + `golangci-lint` via `make fmt-check`/`lint`. |
| III Type Safety | ✅ Typed catalogue (`editor.Editor`, `RemoteKind`), typed refusals (`*editor.Refusal{Rule}`), typed store error (`*store.SettingsUnreadableError`); no untyped payloads. |
| IV Naming & Layout | ✅ One new package directory `internal/editor/` (replacing `internal/vscode/`); new files in existing packages (`ui/editor_open.go`, `ui/settings_command.go`, `ui/status.go`); tests colocated `_test.go`. |
| V Verification Before Merge | ✅ Same `make` gates; env-check covers the nine new variables. |
| VI Multi-Level Testing | ✅ Unit tables (tokenizer, checks, argv per editor, address, path limit, merge, modes), model tests (settings flows, guard, `v` messages, hold, size check, 80×24 layout for SC-004/SC-008), e2e through a PTY; ≥ 90 % per module. |
| VII Containerized Deployment | ✅ N/A: client binary (001 carve-out). |
| VIII Env Discipline | ✅ Nine new variables, all declared in `config.schema` (the single env surface), with defaults, docs and `.env.example` in lockstep (FR-503); no `os.Getenv` outside `config`. |
| Repository Structure | ✅ Changes stay in `src/apps/switchboard-tui`, `src/apps/switchboard-tui-e2e` and docs; no lib imports anything new. |

**Result**: PASS. **Re-checked after Phase 1 design: still PASS.**

## Project Structure

### Documentation (this feature)

```text
specs/swi-24-feature-make-the-editor-configurable/
├── plan.md                    # This file
├── research.md                # Phase 0: findings F1–F8, decisions R1–R16
├── data-model.md              # Phase 1: catalogue entry, settings, session state, request/plan/refusal, hold
├── quickstart.md              # Phase 1: scenarios 0–7
├── contracts/
│   ├── settings-file.md       # settings.toml keys, read/write rules, failure modes
│   ├── editor-launch.md       # argv per editor, Custom grammar, checks, rule → message table, env vars
│   └── settings-ui.md         # settings screen modes/keys, status line limits, size check, hold table
├── checklists/
└── tasks.md                   # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
src/apps/switchboard-tui/
├── .env.example                       # + 9 SWITCHBOARD_<EDITOR>_BIN rows
├── cmd/sxb/main.go                    # WithEditor(editor.NewLauncher(), cfg.EditorBins) replaces the vscode opener
└── internal/
    ├── config/config.go               # schema rows from editor.Catalogue(); EditorBins (raw)
    ├── editor/                        # NEW (replaces internal/vscode)
    │   ├── catalog.go                 # Editor, RemoteKind, Catalogue, Lookup, Command
    │   ├── refusal.go                 # Refusal (the typed result every check returns)
    │   ├── shellwords.go              # Split (FR-531), Word.TildeOK, rule errors
    │   ├── check.go                   # Check, Expand, Substitute (FR-506, FR-515, FR-516)
    │   ├── pathcheck.go               # path checks and the remote limit (FR-517)
    │   ├── zed.go                     # ZedAddress (FR-513)
    │   ├── resolve.go                 # Request, Plan, Precheck, Resolve (FR-510–FR-526)
    │   └── launch.go                  # Launch, Started, ExitReport, exitGrace (FR-511, FR-523)
    ├── store/
    │   ├── store.go                   # + SaveTOMLPrivate (0600, CreateTemp, rename)
    │   └── settings.go                # Editor, EditorCustomCommand; Update(SettingsChange); SettingsUnreadableError
    └── ui/
        ├── app.go                     # Model fields; wrapper order (release, size check); bodyHeight; footer; chrome; activeHelp; forwardScreen and resize for the input
        ├── status.go                  # NEW: statusLines, limits, cutMiddle, sizeKey/activeSurface check, hold + release
        ├── editor_open.go             # NEW: openEditorCmd, editorOutcomeMsg/editorExitMsg, message assembly, notices
        ├── settings.go                # Editor row, list mode, notices, wrapping, Update-based saves, (not saved)
        ├── settings_command.go        # NEW: command input, gEditorCommand submit/discard, note
        ├── formguard.go               # gEditorCommand in activeGuard/guardDirty/guardSave/guardDiscard
        ├── terminal.go                # termRows fixed; vHold.seq; termFail; 2-line status
        ├── orphan.go                  # confirmReplaceOrphan: holdsV, onAccept seq, clears termFail
        ├── confirm.go                 # confirmState.holdsV
        ├── hosts.go                   # closeOrchAdminOnDrop: its disconnected message sets termFail
        ├── jobs.go                    # jobsFixedLines, shared with viewJobs and the status limit
        ├── orchestrator_lanes.go      # lanesFixedLines, shared with lanesView and the status limit
        ├── keys.go                    # listHelp: v label = editor id
        ├── groups.go                  # openVSCodeCmd removed; sshTargetForHost returns the lookup error
        └── sandbox_list.go            # v → openEditorCmd
src/apps/switchboard-tui-e2e/
└── editor_e2e_test.go                 # NEW: stub editor on PATH; list pick + v argv; Custom; exit-3 report
README.md                              # env table, key row, Prerequisites, Editor section (FR-524)
```

**Structure Decision**: existing layout; the only new directory is `internal/editor` inside the TUI module,
replacing `internal/vscode` (research R1).

## Complexity Tracking

No new constitution violations. The Go-stack deviation is inherited from 001.
