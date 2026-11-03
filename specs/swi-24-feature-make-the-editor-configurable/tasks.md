# Tasks: Configurable Editor for the Open-in-Editor Key (SWI-24)

**Input**: Design documents from `/specs/swi-24-feature-make-the-editor-configurable/` (plan.md, spec.md,
research.md, data-model.md, contracts/settings-file.md, contracts/editor-launch.md, contracts/settings-ui.md,
quickstart.md)

**Tests**: Included. The constitution (Rule VI) requires automated tests and ≥ 90 % coverage per module, and the
spec names tests in SC-003 (argv per editor), SC-004 (80×24 layout on the list) and SC-008 (settings screen,
terminal screen and the hold).

**Organization**: a shared foundation (catalogue, rules, launcher, store, status line) comes first, then one
phase per user story in priority order: US1 pick an editor (P1), US2 override variables (P2), US4 remote
sandboxes (P2), US3 Custom command (P3), then polish. US3 is last because it needs the guard surface and the
input, which nothing else needs.

Paths are relative to the repository root. `ED/` = `src/apps/switchboard-tui/internal/editor/`,
`UI/` = `src/apps/switchboard-tui/internal/ui/`, `ST/` = `src/apps/switchboard-tui/internal/store/`,
`CF/` = `src/apps/switchboard-tui/internal/config/`, `E2E/` = `src/apps/switchboard-tui-e2e/`,
`TUI/` = `src/apps/switchboard-tui/`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US4 from spec.md

---

## Phase 1: Setup

**Purpose**: the catalogue every later task reads, and its environment surface.

- [ ] T001 Create package `ED/` with `catalog.go`: `Editor{ID, Name, Command, EnvVar string; Remote RemoteKind}`,
  `RemoteKind` (`RemoteSSHOption`, `RemoteSSHAddress`, `RemoteNone`, `RemoteCustom`), the eleven FR-501 rows in
  order (`vscode` … `sublime`, `custom`; `sublime`'s command is `subl`), `Catalogue() []Editor`,
  `Lookup(id) (Editor, bool)`, `Default = "vscode"`, `Custom = "custom"`, and `Command(e Editor, bins
  map[string]string) (bin string, overridden bool)` (FR-502: blank after `TrimSpace` = unset; a set value is
  returned raw). The package imports only the standard library (research R1).
  Tests in `ED/catalog_test.go`: every row's fields against the contract's §1/§5, `Command` with unset, blank
  and set values.
- [ ] T002 [P] `CF/config.go`: replace the `SWITCHBOARD_CODE_BIN` row's handling and add one schema row per
  built-in editor other than VS Code, generated at init from `editor.Catalogue()` (`EnvVar`, `def` = `Command`,
  `doc` = `"<Name> CLI used to open sandboxes."`); keep `SWITCHBOARD_CODE_BIN` first among them with its
  existing doc. Add `EditorBins map[string]string` (editor ID → the **raw** env value, `""` when unset or blank;
  research R2) and `func (c *Config) EditorBin(id string) (bin string, set bool)`; keep `Config.CodeBin` (set to
  `EditorBins["vscode"]` or `code`) until T026 deletes it with the old opener, so the module keeps building;
  `Load` reads the editor rows raw (no default substitution) and every other row as today. Add tests (and in T026 update `CF/config_extra_test.go` and `CF/envexample_test.go`, where
  `TestLoadDefaults` asserts `cfg.CodeBin`, for the removed field): each variable present in `SchemaKeys()`, raw
  value kept, blank = unset, `SWITCHBOARD_CODE_BIN` unchanged.
- [ ] T003 [P] `TUI/.env.example`: add the nine variables from contracts/editor-launch.md §5 with their defaults
  and a comment naming the editor and that a value is one executable (a wrapper script or Custom for a command
  with arguments). `make env-check` must pass (`TestEnvExampleLockstep`).
- [ ] T004 [P] `README.md`: the TUI env table gains the nine rows (contracts/editor-launch.md §5), the
  `SWITCHBOARD_CODE_BIN` row says "VS Code CLI (the default editor)", the Prerequisites bullet about VS Code
  becomes "one of the supported editors (VS Code by default)", and the `v` key row reads "Open the selected
  sandbox in the configured editor (VS Code by default; `,` to change)". The Editor section is T055.

**Checkpoint**: `go build ./...` and `go test ./...` in the TUI module (`main.go` still uses the old opener
through `cfg.CodeBin`; T026 switches it).

---

## Phase 2: Foundational (blocking)

**Purpose**: the rules, the launcher, the merging store and the status-line machinery that every story extends.

### Editor rules and launch

- [ ] T005 [P] `ED/shellwords.go`: `Word{Text string; TildeOK bool}` and `Split(text string) ([]Word, error)` per
  contracts/editor-launch.md §2 (states unquoted/single/double/escape; `TildeOK` for an unquoted unescaped
  leading `~` that is the whole word or followed by `/`). Errors are `*CheckError{Rule string}` with rules
  `unbalanced-quotes`, `trailing-backslash`, `control-character` (anything `unicode.IsControl` but `\t`),
  `shell-syntax` (unquoted or double-quoted `$` or `` ` ``; unquoted `|`, `;`, `&`, `<`, `>`); the error text
  never includes the input (FR-532). Table tests in `ED/shellwords_test.go` for every FR-531 bullet:
  `"/Applications/My Editor.app/bin/edit" {path}`, `/My\ Editor/edit`, `\$`, `\&`, `'$HOME'` accepted, `"$HOME"`
  rejected, `` `x` `` rejected, `a|b` rejected, `'a|b'` accepted, `""`/`''` empty words, `~`, `~/x`, `'~/x'`,
  `--dir=~/x` (not expandable), `~user`, a tab separator, a trailing `\`, a stray `"`.
- [ ] T006 [P] `ED/check.go`: `Check(words []Word) error` (`empty`, `empty-executable`, `marker-in-executable`,
  `assignment` with `^[A-Za-z_][A-Za-z0-9_]*=` on the split first word); `Expand(words, home string) ([]string,
  error)` (`no-home` when a `TildeOK` word meets `home == ""`); `Substitute(args []string, path, host string)
  (argv []string, hadPath bool)` (single pass over the original args; `{path}`/`{host}` replaced inside each
  arg; an arg exactly `{host}` dropped when `host == ""`; path appended when no arg had `{path}`; a marker
  inside a substituted value not expanded again). Tests in `ED/check_test.go`: every FR-506 bullet, `FOO=1 x`
  and `"FOO=1" x` both rejected, `./FOO=1/edit` accepted, `{path}/bin/edit` first word rejected, a path
  containing a literal `{host}` not re-expanded, `kitty -e sh -c 'cd "$1" && nvim .' sh {path}` giving the
  path as a separate argument.
- [ ] T007 [P] `ED/pathcheck.go`: `checkPath(path string, limited bool) *Refusal` — not absolute or any control
  character (including `\n`) → `path-unsafe`; when `limited`, invalid UTF-8 or a rune outside letters, marks,
  digits, ASCII space and `/ . _ - + = @ , :` → `path-limited` with `Detail` = `'x'` or `U+XXXX`
  (`unicode.IsPrint` false). Tests: `/home/josé`, `/a b/c`, `/x:y,z` accepted; `~`, `(`, `#`, `$`, `` ` ``, a
  zero-width space (`U+200B` in the detail), invalid UTF-8, a newline, a relative path.
- [ ] T008 [P] `ED/zed.go`: `ZedAddress(target, path string) (string, *Refusal)` per contracts/editor-launch.md
  §1 (strip `ssh://`, user at the last `@`, bracket a bare IPv6, `url.URL` build, round-trip check; `target-dash`
  on the normalised user or host, `zed-target` otherwise). Tests: the six examples in the contract, `ssh://-oX`,
  a port, an IPv6 with a port already bracketed, a path with `%`.
- [ ] T009 `ED/resolve.go`: `Request`, `Plan`, `Refusal` (data-model §4), `Precheck(req) (exe string, *Refusal)`
  (contract §3 steps 1–5: host error; `RemoteNone` on a remote; Custom `Split`+`Check`+`Expand`; `-` on the target
  as given; Zed address; the resolved executable) and `Resolve(req) (Plan, *Refusal)` (steps 6–8: path checks
  with `limited = remote && (custom || overridden)` where `overridden` comes from `Command(editor, req.Bins)`,
  so a blank value doesn't count; argv per §1/§2; `RemoteNoHost`; `EnvVar`). Depends on
  T001, T005–T008. Tests in `ED/resolve_test.go`: **SC-003** — a table with every catalogue editor × {local,
  remote} asserting the exact argv (VS Code local/remote byte-identical to today's `codeArgs`); the refusal order
  (`host-lookup` beats everything; `no-remote` beats `target-dash`, so WebStorm with a `-oX` target refuses
  `no-remote`; for Custom a bad command beats `target-dash`); the override variable used raw with
  spaces; the limit applied only for Custom or when `Command` reports the selected editor's own variable as
  `overridden` (another editor's variable set → no limit; a spaces-only value → no limit, FR-517/FR-502); `{host}`
  empty on local; `RemoteNoHost`.
- [ ] T010 [P] `ED/launch.go`: `Launcher interface{ Launch(argv []string) (*Started, error) }`, `NewLauncher()`,
  `Started{Report <-chan ExitReport}`, `ExitReport{Status int; Signal string}`; `exec.Command` with
  `SysProcAttr{Setsid: true}`, nil stdio, `Start`, one goroutine that `Wait`s until the editor exits (always
  reaping it, FR-511) and a second that selects on that result and `time.After(exitGrace)` (`var exitGrace = 3 *
  time.Second`): a non-zero exit or signal before the grace sends a report, otherwise it closes the channel; a `Start`
  failure returns the error with the innermost OS reason reachable through `errors.Unwrap` (FR-518). Tests in
  `ED/launch_test.go` with scripts written to `t.TempDir()`: `exit 0` → closed; `exit 3` → `{3}`; `kill -TERM
  $$` → signal; `sleep` past a shortened grace → closed and the test doesn't wait for the child; a missing
  executable → error; the child's `Setsid` observed via `ps -o sid=` or `/proc/<pid>/stat` on Linux (skip
  elsewhere).

### Store

- [ ] T011 [P] `ST/store.go`: add `SaveTOMLPrivate(name string, v any) error`: marshal, `os.CreateTemp(dir,
  "."+name+"-*.tmp")` (so `.settings.toml-<random>.tmp`, mode `0600`), write, close, `os.Rename`, then
  `os.Chmod(full, 0o600)`. `SaveTOML` is unchanged. Tests in `ST/store_test.go`: the final mode is `0600`; a
  pre-existing `0644` file becomes `0600`; a leftover `0644` `.settings.toml-stale.tmp` is not used and the result
  is still `0600` (the review's case, spec Notes); no temp file remains after the save.
- [ ] T012 `ST/settings.go`: `Settings` gains `Editor`, `EditorCustomCommand` (raw strings); `settingsFile` gains
  the two pointer fields (`editor`, `editor_custom_command`); `Load` copies them when present; replace
  `Save(Settings)` with `Update(c SettingsChange) error` where `SettingsChange{AutoConnectHosts *bool; Editor
  *string; EditorCustomCommand *string}` (research R3, contracts/settings-file.md): re-read into `settingsFile`;
  a read or parse error → `*SettingsUnreadableError{Err}` and no write; copy each non-nil change over the file's
  pointer field, so only those keys change and an absent, unchanged key stays absent; write via
  `SaveTOMLPrivate`. Depends on T011. Update `ST/settings_test.go`: absent keys stay absent
  after a toggle save; `editor = 'future-ide'` survives a toggle save and `Load` returns it raw; a Custom save
  writes both keys; hand-edited `editor` written between `Load` and `Update` survives (SC-007); a broken file
  → `SettingsUnreadableError` and the bytes unchanged (SC-006); `editor = 5` is a parse error; comments dropped.

### Status line, size check and hold (TUI-wide)

- [ ] T013 `UI/status.go` (new): constants `statusMaxLines = 6`, `terminalStatusLines = 2`, `outsideTextCols =
  120`, `osReasonCols = 40`, `valueCols = 60`, `vHoldRecent = 3 * time.Second`; `statusLines(text string,
  width, limit int) []string` (`sanitizeTerminalText`, newlines to spaces, `ansi.Wrap(s, width, "")` — not
  `Wordwrap`, which leaves a token longer than the width unbroken — cut to `limit` with the last line
  `ansi.Truncate(line, width-1, "…")`, never below 1 line); `cutMiddle(s string, cols
  int) string` (`sanitizeLine(s, 0)` then `Truncate(20, "")` + `…` + `TruncateLeft(w-39, "")`, dropping one
  column when the result exceeds `cols`); `cutEnd(s, cols)` = `sanitizeLine`. Tests in `UI/status_test.go`:
  wrap at spaces, a 120-column token with no spaces broken so no line exceeds the width, the cut marker, a
  10-tab string cut to 120 **after** sanitising, the spec's 60-column executable example, a wide rune on the
  cut, an empty status → no lines.
- [ ] T014 `UI/app.go` + `UI/status.go`: `activeSurface()` enum (`surfaceList`, `surfaceItemList{which}`,
  `surfaceForm{which}`, `surfaceWindowed` (Jobs, the lanes picker, the settings screen's list and input modes
  once T020/T034 make them read `bodyHeight`), `surfaceStatic`, `surfaceTerminal`) derived like `activeGuard`;
  `m.statusLimit()` and `m.statusLineCount()` computed **without rendering** (contracts/settings-ui.md §2:
  `min(6, max(1, height - 2 - 1 - helpLines - minBody))`, min body `9 + 2 + banner` for the list, `3` for a
  resizable surface, the fixed lines + 3 for a windowed view (the settings input: fixed lines + 1 note line), `0`
  for a static view, constant 2 on the terminal screen); `bodyHeight()` becomes `m.height - 6 - (statusLineCount - 1)` (floor
  3) and must never call a view function; `footer(hb, limit)` renders `statusLines`, red when `m.err != nil`, the
  status starts with `error`, or `m.status == m.vFail.text`; `chrome` measures the room left by the rendered body
  (`height - 2 - lipgloss.Height(body) - 1 - helpLines`, at least 1) and passes `min(limit, room)`, so a static
  view cuts the message to fit. `termRows()` becomes `m.height - 8` (floor 3), independent of the status.
  Depends on T013. Tests in `UI/status_test.go`: at 80×24 a 4-line status on the
  list keeps the header on screen; Daemons with 8 rows cuts to 1 line with the header on screen and the list
  shows the whole message afterwards; the terminal screen cuts at 2 lines and `termRows` equals the value with
  no status; a 1-line status gives today's `bodyHeight`; Jobs during a 4-line status keeps its header and its
  windowed rows at their minimum.
- [ ] T015 `UI/status.go` + `UI/app.go`: the size check. `sizeKey{status, banner string; width, height, tabs int;
  surface activeSurface; markFree bool}` stored on `Model`; `(m *Model) checkSizes() tea.Cmd` run in the `Update`
  wrapper **last** (after the release, T017), skipped on `surfaceTerminal`; on a key change: list →
  `SetSize(bodyWidth, mainListHeight())` when `Height()` differs; the open item list of the surface (groups,
  hosts, configs picker, kit picker, notifications, runs, services) → `SetSize(bodyWidth, bodyHeight)` when it
  differs; the open huh form → `WithHeight(bodyHeight)` only when the stored `lastHeight` differs (add
  `lastHeight int` beside each of the 7 form pointers, set where they're built), then `form.Update(nil)` in the
  same pass with its command returned. Depends on T014. Tests: each key field changed in turn resizes (status
  grows, banner arrives via `applyUpdateAvailable`, help footer changes height via `MarkFree`, tabs change,
  returning to the list from Settings during a long message); an unchanged key doesn't call the target (count
  via a test hook); a form shrunk then restored keeps the last field drawn (`Update(nil)` effect).
- [ ] T016 `UI/status.go` + `UI/terminal.go` + `UI/orphan.go` + `UI/app.go`: hold fields `vFail{text; at}`,
  `vHold{text; seq; dialog}`, `termFail` on `Model` (data-model §6); `attachTerminal` (in-place only, i.e. the
  caller isn't `T`'s replace path) sets `vHold.seq = m.termSeq`, clears `termFail`, and copies a recent
  (`nowFunc().Sub(vFail.at) < vHoldRecent`, non-zero `at`) status equal to `vFail.text` into `vHold.text` when
  empty; `confirmReplaceOrphan` with `!external` sets `vHold.dialog = true` with the same copy, `onAccept` sets
  `vHold.seq = m.termSeq + 1` **and** `vHold.dialog = false` (otherwise an accepted dialog never releases),
  `onCancel` sets `vHold.dialog = false`; `handleTermOpened`/`handleTermClosed` clear `vHold.seq` only when
  `msg.seq == vHold.seq`; the three error assignments (`not opened — you moved on`, `external terminal already
  attached`, `terminal: …`) also set `termFail`. `(m *Model) setVFail(text string)` used by T024/T038: it always
  sets `vFail = {text, nowFunc()}` (the error style applies wherever the text is shown, FR-529); when
  `vHold.seq != 0 || vHold.dialog || m.screen == screenTerminal` it also sets `vHold.text` (and `m.status` on the
  terminal screen, where it's drawn cut to 2 lines), else `m.status`.
- [ ] T017 `UI/status.go` + `UI/app.go`: `(m *Model) releaseHeld()` run in the `Update` wrapper in the order
  `reconcileGuardLayers`, `openParkedOverride`, `openDeferredMcpFollowups`, release, size check: when
  `vHold.text != ""`, `vHold.seq == 0`, `!vHold.dialog` and `m.screen != screenTerminal`: `status = (status != ""
  && status == termFail ? sanitizeLine(status, 120) + "; " : "") + vHold.text`; `vFail = {status, time.Time{}}`;
  `vHold = {}`. Depends on T016. Tests in `UI/hold_test.go` (step `nowFunc`; use the `fakeDaemon` attach hooks):
  **SC-008** — a failure arriving while the terminal is open is shown in full and red on `ctrl+q`, replacing
  `detached from`; one arriving while opening; a first attach superseded by a second (only the second's message
  clears the hold); a Replace dialog declined and accepted (accepted > 3 s after the refusal); a refusal then `t`
  held; a refusal older than 3 s not held; `terminal: <reason>; <refusal>` joined, red, error first; a second
  `t` within 3 s after a release shows only its own error; `moved on` and `external already attached` joined; an
  accepted Replace whose attach fails joined the same way; an open terminal whose session dies (`SendData`
  failure through `handleTermClosed` while showing) joined with the held failure; a released message's next
  plain status not red; a `v` failure arriving after the error is showing replaces it.

**Checkpoint**: `go test ./...` passes in the TUI module with every existing test green (`footer` unchanged for
one-line statuses).

---

## Phase 3: User Story 1 — Pick which editor `v` opens (Priority: P1) 🎯 MVP

**Goal**: an Editor row on the settings screen, saved in `settings.toml`, used by `v` and named in the footer.

**Independent Test**: choose WebStorm on the settings screen; `v` on a local sandbox starts `webstorm <path>`
(recording launcher); restart the model from the same store and WebStorm is still selected.

- [ ] T018 [US1] `UI/settings.go`: `settingsState` gains `mode`, `listCursor`, `fileUnreadable error`,
  `lastSaveFailed error`, `notSaved map[string]bool` (data-model §3) and drops the existing `notice string`
  field (the notices are derived now; `TestSettingsUnreadableFileShowsNoticeAndDefaults` reads the view instead);
  `WithSettings` sets `fileUnreadable` on a `Load` error, keeps `m.prefs` raw, and sets `m.status` to the
  startup notices joined by `; ` (FR-529, best effort). Replace `settingItem` with a row kind (`rowToggle`, `rowEditor`) and add the Editor row after the
  sign-in row (`label: "Editor"`, desc `Which editor v opens the sandbox's workspace folder in`). Add
  `sessionEditor() (editor.Editor, unknown string)` (unknown values → VS Code with the value cut to 60 columns
  via `cutEnd`, control characters removed), `settingsNotices()`, `vNotices()` (data-model §3). Depends on T012,
  T013.
- [ ] T019 [US1] `UI/settings.go`: saves go through `saveSetting(key string, c store.SettingsChange, apply
  func(*store.Settings))` (`c` is what's written, `apply` what the session takes on): store
  nil or `fileUnreadable != nil` → session only, `notSaved[key] = true`, status per contracts/settings-file.md
  (restart wording only for `fileUnreadable`); `*SettingsUnreadableError` → session only, `lastSaveFailed`,
  `notSaved[key] = true`, "fix the file" with no restart; other error → session only + `notSaved` + `error:
  could not save settings: …`; success → `notSaved[key] = false`, `lastSaveFailed = nil`, `saved settings`. The
  toggle row uses it with its own key (FR-509, FR-530). Tests in `UI/settings_test.go`: the existing four
  updated; a toggle with a broken-at-startup file shows `(not saved)` and "restart"; a file broken after
  startup shows "fix the file" and `(not saved)`, and the next successful save clears both; two presses restore a
  toggle's value and clear the mark.
- [ ] T020 [US1] `UI/settings.go`: list mode per contracts/settings-ui.md §1 — `space`/`enter` on the Editor row
  opens the list with `listCursor` on the session's editor (unknown → VS Code); `↑/↓`/`j`/`k` move within
  `windowRange(len, cursor, avail)`; `enter` on a built-in entry calls `saveSetting("editor",
  store.SettingsChange{Editor: &id}, set Editor = id)`
  (always, even on the marked entry, FR-505) and returns to rows; `enter` on Custom calls
  `enterEditorCommand()` (T034; until it lands, a stub that returns to rows with the status `Custom command
  input: not implemented yet` and saves nothing, so this phase's tests run); `esc` returns to rows; `space`,
  `q`, `,` ignored. `viewSettings` in list mode renders only the header's title line `Editor` and the entries
  windowed by `windowRange(len, listCursor, m.bodyHeight()-1)` (so the view reads `bodyHeight` as it renders,
  like Jobs) with the current one marked; in rows mode it renders the notices
  (instruction before detail), the rows and descriptions, each wrapped with `ansi.Wrap(s, m.width, "")`, the Editor
  value per data-model §3 (`Name`, `Custom: <cutMiddle(exe, 60)>` + `…`, `Custom (invalid command)`, ` (not
  saved)`). `settingsHelp` names both row kinds and the list's keys. Depends on T018, T019. Tests: open the
  list, move, confirm → file has `editor = 'webstorm'`; `esc` saves nothing; confirming VS Code over
  `future-ide` replaces it and clears the notice; toggling sign-in leaves `future-ide` (FR-508); the list window
  on a 12-row terminal keeps the cursor visible; `q` in the list does nothing.
- [ ] T021 [P] [US1] `UI/settings_test.go`: **SC-008 (settings screen)** at `tea.WindowSizeMsg{80, 24}` — rows with
  both notices and a `(not saved)` mark on a 90-column executable: no rendered line wider than 80
  (`lipgloss.Width` of every line), the header line present, the mark present; the open list; (the open input
  case is T037). Depends on T020.
- [ ] T022 [US1] `UI/app.go` + `UI/editor_open.go` (new): `Model` gains `launcher editor.Launcher` and
  `editorBins map[string]string`; `WithEditor(l editor.Launcher, bins map[string]string) Model` replaces the
  opener argument of `WithGroups` (`WithGroups(gs)` keeps the store; update every caller: the `nil` sites in
  `guard_surfaces_test.go`, `coverage_test.go` (two), `sandbox_list_test.go`, `tabs_test.go`, and the three
  non-nil sites in `groups_test.go`, whose `recordingOpener` and `TestOpenVSCode*` are deleted here, replaced by
  T024/T031; remove the `vscode` import from every test file); `sshTargetForHost(hostID) (target string,
  err error)` returns the lookup error (`hostStore.Get` failure) instead of `""` (FR-520). Message types
  `editorOutcomeMsg{sandboxID, text string; failed bool; wait <-chan editor.ExitReport; exe, where string}` (`exe`
  = `Plan.Executable` cut to 60 columns, `where` = the override variable or the settings-screen phrase, both
  known only inside the `v` command) and `editorExitMsg{sandboxID, text string}` (data-model §7). Depends on
  T009, T010, T016.
- [ ] T023 [US1] `UI/editor_open.go`: `openEditorCmd(hostID, sandboxID string) tea.Cmd` — on the update loop:
  `launcher == nil` → `editorOutcomeMsg{text: "<Name> opener not configured"}`; build `editor.Request{Editor:
  sessionEditor(), Bins: editorBins, Custom: prefs.EditorCustomCommand, SSHTarget, HostErr, Home:
  os.UserHomeDir()}`; `exe, ref := editor.Precheck(req)`; a refusal → `editorOutcomeMsg{failed: true, text:
  assemble(ref)}` returned as an immediate command. Otherwise the command: `exec.LookPath(exe)` → `not-found`
  text with the reason cut to 40; `d.VSCodeTarget` → the error cut to 120 then the notices; `editor.Resolve` →
  refusal text; `launcher.Launch(plan.Argv)` → `not-found`/`launch-failed`; success → `opened <id8> in <Name>`
  plus the notices (+ `the path is on the remote host` when `RemoteNoHost`) and `wait`. Message assembly per
  contracts/editor-launch.md §4 with `vNotices()` appended as ` (a; b)` (FR-529); every value through `cutEnd`/
  `cutMiddle` before assembly (FR-533). `sandbox_list.go` calls it for `v`; `groups.go`'s `openVSCodeCmd` and the
  `vscode` import are removed. Depends on T022.
- [ ] T024 [US1] `UI/editor_open.go` + `UI/app.go`: handlers — `editorOutcomeMsg`: `delete(m.busy, id)` (only this
  row), no reload; `failed` → `setVFail(text)` else `m.status = text` (and `m.vFail.text` cleared if it equalled
  the old status, so the style ends); when `wait != nil` return `waitExitCmd(wait, id, msg.exe, msg.where, vNotices())`, which
  takes the executable and the "where" from the message and captures `vNotices()` at that moment, blocks on the
  channel and returns `editorExitMsg{id, text}` with FR-523's text assembled per
  contracts/editor-launch.md §4, or `nil` when the channel closes. `editorExitMsg`: `setVFail(msg.text)` only
  (FR-523: no `busy`, no reload). Depends on T023. Tests in `UI/editor_open_test.go` with a recording `fakeLauncher` (argv, a
  controllable `Report` channel): `v` with VS Code and no settings produces `[code <path>]` and
  `opened <id8> in VS Code` byte-identical to the old test (SC-002); `v` with WebStorm selected produces
  `[webstorm <path>]`; only the pressed row's `busy` entry is cleared and no reload command is returned; a
  daemon error is cut to 120 and followed by the notice; the exit report replaces the status, is red, leaves
  another row's spinner, and a completion arriving after it doesn't overwrite it; the unknown-editor and
  unreadable notices appear on every outcome kind; `VS Code opener not configured` → `<Name> opener not
  configured`.
- [ ] T025 [US1] `UI/keys.go`: `listHelp()` rebuilds the `v` binding as `key.NewBinding(key.WithKeys("v"),
  key.WithHelp("v", id))` with the session editor's identifier (`custom` for Custom), so the default still reads
  `vscode` (FR-519). Test: the footer contains `v webstorm` after selecting WebStorm and `v vscode` by default.
- [ ] T026 [US1] `TUI/cmd/sxb/main.go`: `m.WithGroups(st.Groups()).WithEditor(editor.NewLauncher(),
  cfg.EditorBins)`; delete `TUI/internal/vscode/` (its argv tests are covered by T009); delete `Config.CodeBin`
  and update `CF/config_extra_test.go` and `CF/envexample_test.go` (`TestLoadDefaults`). Depends on T002, T022,
  T024. `go build ./...`, `go vet`, and the whole module's tests pass.

**Checkpoint**: US1 complete — quickstart Scenario 1 passes with a stand-in `SWITCHBOARD_WEBSTORM_BIN`.

---

## Phase 4: User Story 2 — Point an editor at a different command (Priority: P2)

**Goal**: the override variables work for every editor, the not-found message names the variable, and the
remote path limit applies when the selected editor's variable is set.

**Independent Test**: `SWITCHBOARD_WEBSTORM_BIN` = a stand-in script path; select WebStorm; `v` starts that
executable with the folder.

- [ ] T027 [US2] `UI/editor_open_test.go`: with `editorBins["webstorm"] = "/opt/My Tools/ws"` the argv is `[/opt/My
  Tools/ws <path>]` (one executable, never split); a blank value gives the default; `SWITCHBOARD_CODE_BIN`'s
  value reaches `vscode` unchanged; `config.Load` → `EditorBins` → `WithEditor` round trip in a `cmd/sxb`-level
  or `config` test (FR-502).
- [ ] T028 [US2] `UI/editor_open.go`: FR-518's message for a built-in editor names the executable (`cutMiddle`),
  the OS reason (`cutEnd(innermost, 40)` via `errors.Unwrap` to the `*fs.PathError`/`syscall.Errno`), the
  variable, and the single-executable reminder; for Custom it points to the settings screen
  (contracts/editor-launch.md §4). Tests: `exec.ErrNotFound` from `LookPath`, `EACCES` from `Launch`, a
  60-column executable; **the tightest case** (60-column executable, 40-column reason,
  `SWITCHBOARD_CODE_INSIDERS_BIN`, the 60-column unknown-editor notice) wraps to ≤ 4 lines at 80 columns with
  `ansi.Wrap`; if it doesn't, shorten the wording in the order the contract gives and update the contract.
- [ ] T029 [P] [US2] `ED/resolve_test.go`: the FR-517 limit is applied with VS Code selected and `bins["vscode"]`
  set on a remote path containing `(`; not applied with `bins["zed"]` set and VS Code selected; not applied on a
  local sandbox. (Extends T009's table.)
- [ ] T030 [P] [US2] `CF/config.go` doc strings and `README.md` env rows: each variable's doc names its editor and
  default and says a value is one executable (FR-503). `make env-check` passes.

**Checkpoint**: quickstart Scenario 2 passes.

---

## Phase 5: User Story 4 — Open a remote sandbox in the chosen editor (Priority: P2)

**Goal**: Remote-SSH for the VS Code family, the `ssh://` address for Zed, a refusal for the rest, and a refusal
for a failed host lookup.

**Independent Test**: with a saved SSH host, Cursor selected, `v` on a remote sandbox produces
`cursor --remote ssh-remote+<target> <path>`; WebStorm selected → the refusal, nothing launched.

- [ ] T031 [US4] `UI/editor_open_test.go`: a remote host store (`store.KnownHost{Kind: "ssh", SSHTarget:
  "user@box"}`) — Cursor → `[cursor --remote ssh-remote+user@box <path>]`; Zed with `/srv/ws/my proj` → `[zed
  ssh://user@box/srv/ws/my%20proj]`; WebStorm → `no-remote` text, red, the launcher never called, and the
  refusal returned **before** `VSCodeTarget` is called (the fake daemon counts calls); a `hostStore.Get` error
  → `host-lookup` text for VS Code too (FR-520); a target `-oProxyCommand=x` → `target-dash`; a target
  `user@box/evil` with Zed → `zed-target`.
- [ ] T032 [US4] `UI/editor_open.go`: the remote-only notice `the path is on the remote host` appended last for a
  Custom command with no `{host}` (FR-515, FR-529): test the three-notice message
  `opened abc in Custom (settings couldn't be read at startup, changes aren't saved; the path is on the remote
  host)` and that it wraps to ≤ 3 lines at 80 columns.
- [ ] T033 [P] [US4] `UI/groups_test.go`: confirm nothing of the old opener remains (T022 deleted
  `TestOpenVSCode*` and `recordingOpener`) and add a group-tab remote case: `v` from a group tab targets the
  sandbox's own host's SSH target (`daemonForHost` + `sshTargetForHost(hostID)`), not the active host's.
  `sshTargetForHost`'s new error return is covered by T031.

**Checkpoint**: quickstart Scenario 4 passes against a real remote host (Scenario 0 rows still open).

---

## Phase 6: User Story 3 — Use a custom editor command (Priority: P3)

**Goal**: Custom in the list opens a guarded command input; the command is checked on confirm and on every `v`,
split without a shell, with markers substituted.

**Independent Test**: Custom with `~/bin/logedit --open {path}`; `v` starts that script with the folder as one
argument; `cd {path} && nvim .` is rejected with the shell-syntax reason.

- [ ] T034 [US3] `UI/settings_command.go` (new): `settingsState` gains `input textinput.Model`, `inputInitial
  string`, `inputRewrite bool`; `enterEditorCommand()` (from the list's Custom entry): `textinput.New()` with `Prompt = "› "`, `Width
  = bodyWidth - 4`, `SetValue(prefs.EditorCustomCommand)`, `CursorEnd`, `inputInitial = TrimSpace(Value())`,
  `mode = settingsInput`; `inputRewrite = inputInitial != TrimSpace(prefs.EditorCustomCommand)` (cleaning
  changed it), drawn as `normalizeNotice([]string{"Custom command"})` under the input (2 body lines, FR-528),
  cleared when the input closes. `updateSettingsInputKey`: `esc` → `requestExit(
  gEditorCommand)`; `enter` → `submitEditorCommand`; everything else to the input. `submitEditorCommand()`:
  `text = TrimSpace(Value())`; `editor.Split` + `Check` failure → status = the input rejection text for the rule
  (contracts/editor-launch.md §4, no command text), red, input open, session unchanged; otherwise write first
  with `store.SettingsChange{Editor: &custom, EditorCustomCommand: &text}`: store nil or `fileUnreadable` →
  apply to the session (`Editor = custom`, command), `notSaved["editor"] = true`, close to rows; `Update` returns
  `SettingsUnreadableError` → same, `lastSaveFailed`; other write error →
  input stays open with `error: could not save settings: …`, session unchanged; success → session updated,
  `notSaved["editor"] = false`, close to rows, `saved settings`. `viewSettings` in input mode: the title line
  `Custom command`, the input, a blank, the rewrite notice when `inputRewrite`, the FR-521 note (≤ 10 lines at
  80 columns, wrapped with `ansi.Wrap`; its room is `bodyHeight() - fixed lines`, never below 1, so the view reads
  `bodyHeight` as it renders and its last lines are replaced by `…` when the room is short, the input never
  pushed off), help `enter save · esc back`. Depends on T020, T005, T006.
- [ ] T035 [US3] `UI/formguard.go`: add `gEditorCommand` to `guardSurface`; `activeGuard` returns it for
  `screenSettings` with `mode == settingsInput`; `guardDirty` → `TrimSpace(input.Value()) != inputInitial`;
  the Save branch → `submitEditorCommand` (**not** closing first); Discard → `mode = settingsList`, input
  blurred; `guardForm` nil; `forceClose`/`closeSandboxGuard` need no case (the settings screen isn't removed by
  events). Add it to `UI/guard_surfaces_test.go`'s table. Depends on T034.
- [ ] T036 [US3] `UI/settings_command_test.go` (use `noKeyGrace(t)`): the input pre-filled with the session
  command; an empty submit → `a command is required`, input open, previous editor still used by `v`; `cd {path}
  && nvim .` → the shell-syntax rejection mentioning `sh -c` and a wrapper script, no command text in the
  status; `esc` with a change opens the dialog, `s` acts like `enter` (a rejection keeps it open), `c` returns to
  the input, `d` returns to the list with nothing saved; `esc` unchanged → the list, `esc` in the list → rows,
  never closing the screen; `q`, `,`, `j`, `k`, space typed as text; a saved command with a tab opens with the
  rewrite notice and `enter` saves the cleaned text; a write error keeps the input open and `v` keeps the old
  editor; a nil store closes the input with `(not saved)`; after a successful save the row shows `Custom:
  <exe>…` and the file has both keys; a session-only Custom then Zed confirmed → Zed saved, mark cleared,
  Custom again pre-fills the command (FR-530).
- [ ] T037 [US3] `UI/settings_test.go`: **SC-008 (input)** at 80×24 — the open input with a rejection showing:
  the header, the whole note and the input on screen, no line wider than 80; at 80×18 the note ends in `…` and
  the input is still on screen (FR-534). Depends on T034.
- [ ] T038 [US3] `UI/editor_open.go` + tests: Custom on `v` — FR-522's re-check (`Split` + `Check` on the session
  command every press; a hand-edited `API_TOKEN=abc123 x {path}` refuses with the `assignment` rule and the row
  shows `Custom (invalid command)`, `abc123` nowhere in the view or status); `~/bin/edit {path}` expands `~` at
  launch with `Home`; `Home == ""` → `no-home`; `{path}` as one argument with spaces; no marker → appended;
  `{host}` dropped locally and substituted remotely; the FR-523 report for Custom points to the settings screen
  and names only the executable; the completion reads `opened <id8> in Custom`. Depends on T023, T034.
- [ ] T039 [P] [US3] `ED/check_test.go`: **SC-005** — `kitty -e sh -c 'cd "$1" && nvim .' sh {path}` with
  `/tmp/x$(touch pwned)` and `/tmp/a b` gives the path as the last argv element unchanged, and `Substitute`
  never produces a new argument from a path containing spaces or quotes (extends T006).

**Checkpoint**: quickstart Scenario 3 passes.

---

## Phase 7: Polish & cross-cutting

- [ ] T040 [P] `UI/status_test.go`: **SC-004** at `tea.WindowSizeMsg{80, 24}` on the sandbox list with the update
  banner (`applyUpdateAvailable`): for each of — a 120-column daemon error with the unknown-editor notice; a
  daemon error containing 10 tabs; FR-518's longest case (60-column executable, 40-column reason, the variable);
  FR-520's lookup error; FR-523's report; the list opened after a 3-line status was set on the settings screen;
  a banner arriving during a 4-line message — assert the header line and the whole message (every notice and
  "where to fix it") appear in `m.View()`, no line wider than 80, and the list's `Height()` equals
  `mainListHeight()`. The joined case (SC-008): `terminal: <120-column reason>; <FR-518 at its limits>` on the
  list with the banner is cut with `…` on its last line, the header and the whole error are on screen, and
  `m.status` still holds the full text. A separate test: Daemons with 8 daemons cuts the message with `…` and
  keeps the header; back on the list the whole message shows.
- [ ] T041 [P] `UI/status_test.go`: **SC-008 (terminal)** — a long status while the terminal is open sends no
  resize (`fakeDaemon`'s session records `SendResize`) and keeps the header; a terminal opened during a wrapped
  message gets the same PTY size as one opened with none. (The hold cases are T017.)
- [ ] T042 [P] `UI/settings_test.go`: the startup notices are set in `m.status` by `WithSettings` and shown by
  the settings screen; an unknown editor value with control characters and 80 columns is shown cut to 60 with
  the characters removed (FR-508, FR-529, FR-533).
- [ ] T043 [P] `ST/settings_test.go`: `Load` of a file written before this feature (only `auto_connect_hosts`)
  gives VS Code and the saved toggle (Edge case *Older settings file*); a `Load` after a downgrade-style file
  (unknown keys) succeeds.
- [ ] T044 `E2E/editor_e2e_test.go` (new, `//go:build e2e`): a helper `stubEditor(t) (dir, logPath string)`
  writing `webstorm` and `logedit` scripts (`printf '%s\n' "$@" >> <log>`; a `exit3` variant) into a temp dir;
  `spawnTUIWithEnv` with `PATH=<dir>:$PATH`. Test 1: `,` → Editor row → list → WebStorm → `enter`; expect
  `saved settings`, `settings.toml` has `editor = 'webstorm'`; `esc` to the list, `v` on the seeded sandbox;
  expect `opened` and the log holding the daemon's workspace path. Test 2: Custom `logedit --open {path}` →
  expect the log `--open` then the path. Test 3: `SWITCHBOARD_GOLAND_BIN=<dir>/exit3`, GoLand selected, `v`;
  expect the status `exited with status 3` naming `SWITCHBOARD_GOLAND_BIN`. Depends on T026, T034.
- [ ] T045 [P] `E2E/settings_e2e_test.go`: extend `TestTUISettingsPersistE2E` to check `-rw-------` on
  `settings.toml` after the save (FR-527).
- [ ] T046 Run `make fmt-check vet lint test cover env-check` and `make e2e`; raise any module under the 90 %
  floor (expect `editor` near 100 %, `ui` touched files covered by T019–T041).
- [ ] T047 [P] `README.md`: the Editor section under *Settings & startup sign-in* per research R15 and FR-524: the
  list and each editor's remote support, the override variables with the re-parsing warning and the remote
  path limit, the Custom command rules (FR-506, FR-511, FR-516, FR-521, FR-531) with the positional `kitty -e sh
  -c 'cd "$1" && nvim .' sh {path}` example and the one-command-for-every-host limitation, plain-text storage and
  `ps` visibility, the settings screen as the recommended way to set a command with quotes with the TOML
  literal forms, and that hand edits take effect at the next start and comments aren't kept.
- [ ] T048 [P] `CLAUDE.md`: rewrite the SWI-24 paragraph in the SPECKIT block to the as-built summary (catalogue,
  `internal/editor`, settings keys, `Update` merge, status-line limits and the hold) and mark it implemented.
- [ ] T049 Manual: quickstart Scenarios 0–7 on a real machine; record Scenario 0's results in
  `contracts/editor-launch.md` §1's *Verified* column (Windsurf and Cursor rows; Zed's installed version) with the
  date, and fix the Windsurf catalogue row if its authority differs (research R8).

---

## Dependencies

- Phase 1 (T001–T004) → Phase 2. T002 needs T001. T003/T004 are independent.
- Phase 2: T005–T008, T010, T011 are parallel; T009 needs T001, T005–T008; T012 needs T011; T013 → T014 → T015;
  T016 needs T014 (fields) and T017 needs T016.
- US1 (T018–T026): T018 needs T012, T013; T019 needs T018; T020 needs T019; T022 needs T009, T010, T016; T023
  needs T022, T018; T024 needs T023, T017; T025 needs T018; T026 needs T002, T022–T024. **MVP = Phase 1 + Phase 2 +
  US1.**
- US2 (T027–T030) needs US1. US4 (T031–T033) needs US1. US2 and US4 are independent of each other. T026 is
  the last US1 task: it deletes the old opener and `CodeBin` only once T022–T024 have replaced every use.
- US3 (T034–T039) needs US1 and T005/T006; T035 needs T034; T036/T037 need T035; T038 needs T034 and T023.
- Polish: T040–T043, T045, T047, T048 after their phases; T044 after T026 and T034; T046 last before T049.

## Parallel execution examples

- After T001: T002, T003, T004, T005, T006, T007, T008, T010, T011 together (nine files).
- After T014: T015 and T016 in parallel (T017 after T016).
- After US1: US2 (T027–T030) and US4 (T031–T033) in parallel.
- Polish: T040, T041, T042, T043, T045, T047, T048 together.

## Implementation strategy

1. **MVP**: Phases 1–2 and US1 give a working, tested editor choice with the whole status-line machinery in
   place (the machinery is foundational because every `v` message depends on it).
2. US2 and US4 are small on top of US1 (messages and the remote branch of `Resolve`, both already in `editor`).
3. US3 adds the input and the guard surface last, since nothing else needs them.
4. Polish pins SC-004/SC-008 layouts, the e2e flows and the docs, then the manual quickstart settles the
   documentation-derived rows.

Format check: every task above has a checkbox, an ID, a `[P]` marker only where parallel, a `[USn]` label in the
story phases, and names its file(s).
