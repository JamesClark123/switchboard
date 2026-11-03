# Research: Configurable Editor for the Open-in-Editor Key (SWI-24)

Phase 0 of `/speckit-plan`. Findings (F) are facts checked in the code, the dependencies' sources or the vendors'
docs on 2026-11-03. Decisions (R) resolve the spec's open items and its Notes for Planning. Each decision gives
what was chosen, why, and what was rejected.

## Findings

### F1 — How `v` works today

- `sandbox_list.go:756`: `v` calls `m.startBusy(id, "opening")` and `m.openVSCodeCmd(host, id)` (`groups.go:230`).
- `openVSCodeCmd` runs the host lookup (`sshTargetForHost`, `groups.go:254`, which re-reads `hosts.toml` through
  `LoadTOML` on every call) on the update loop, then returns a command that calls `d.VSCodeTarget` (the
  `GetVSCodeTarget` RPC, returning `pb.VSCodeTarget{container_name, workspace_path, ssh_target}`) and
  `opener.Open(tgt, sshTarget)`. It returns `statusMsg("opened <id8> in VS Code")` or `errMsg{err}`.
- `internal/vscode/vscode.go`: `Opener{CodeBin, Run}`; `codeArgs` gives `[--remote ssh-remote+<target> <path>]` or
  `[<path>]`; `Open` wraps a start failure as `vscode: launch <bin>: <err>`. `Run` defaults to `cmd.Start()` with
  no `SysProcAttr`, so the editor shares the TUI's session and controlling terminal, and nothing reaps it.
- `app.go:763` (`statusMsg`): sets `m.status`, clears `m.err`, resets `m.busy` for **every** row, sets
  `listLoading` and reloads. `errMsg` (`app.go:772`) sets `m.err` and `m.status = "error: " + err`.
- `keys.go:111`: the help text is the fixed `vscode`. `listHelp()` (`keys.go:143`) builds the footer each render.
- `config.go`: `SWITCHBOARD_CODE_BIN` (default `code`) is one of five schema fields; `Load` substitutes the
  default for an empty value, so a caller can't tell set from unset. `main.go:141` builds the opener from
  `cfg.CodeBin`. `envexample_test.go` enforces schema ↔ `.env.example` lockstep (`make env-check`).
- The sandbox's host entry: `store.KnownHost{Kind, SSHTarget}`; a remote host is `Kind == "ssh"`.

### F2 — The settings screen and store today

- `ui/settings.go`: `settingItems` is one toggle row; `updateSettingsKey` flips the value in `m.prefs`, then
  `settingsStore.Save(m.prefs)` writes the **whole** struct. `esc`, `q` and `,` all close the screen.
  `viewSettings` joins rows with no width limit; the sign-in description is 103 columns.
- `store/settings.go`: `Settings{AutoConnectHosts bool}`; `settingsFile` uses pointer fields so an absent key keeps
  its default; `Load` returns defaults **and** the error for an unreadable file; `Save` writes the whole file.
- `store/store.go:59` `SaveTOML`: `os.WriteFile(full+".tmp", b, 0o644)` then `os.Rename`. `os.WriteFile` applies
  the mode only when it creates the file, and the name is fixed, so a leftover temp file keeps its mode. The
  review confirmed that a leftover `0644` temp gives a final mode of `644`. `SaveTOML` also serves hosts, groups
  and configs.
- `WithSettings` (`settings.go:37`) loads before `Init`; the startup sign-in reads `m.prefs.AutoConnectHosts`.
- go-toml v2 (`v2.2.3`, checked by a probe): decoding `editor = 5` into a `*string` field fails with
  `toml: cannot decode TOML integer into struct field … of type *string`, so a wrong type is a parse error, as
  the spec's Edge Cases assume. Marshalling a struct with nil pointer fields omits those keys. Comments are not
  preserved by any decode/encode round trip.

### F3 — The status line, body sizing and the terminal screen

- `footer` (`app.go:1450`): one `sanitizeLine(m.status, 0)` line, red when `m.err != nil` or the status starts with
  `error`; then `renderHelp`, which already wraps at `m.width`. Bubble Tea v1.3.10 drops the **top** lines of a
  view taller than the terminal and cuts wider lines with no ellipsis.
- `bodyHeight` (`app.go:1296`) is `m.height - 6` (floor 3): header 2 + blank + one status line + help ≈ 1, with
  one status line assumed. `mainListHeight` subtracts the tab bar (2), extra help lines and the banner (1).
- 14 call sites read `bodyHeight` once when a surface is built: 7 `newItemList` (groups, configs picker, hosts,
  kit picker, notifications, runs, services) and 7 huh `WithHeight` (config editor, orchestrator forms, MCP
  form ×2, kit editor, run actions, secrets). The sandbox list's own `newItemList` (`sandbox_list.go:45`) takes
  its size from `mainListHeight` and is the one surface resized after startup, on `tea.WindowSizeMsg`. Jobs
  (`jobs.go:440`) and the lanes list read `bodyHeight` as they render and window their rows; Daemons and
  Settings render their rows with no height at all.
- The `Update` wrapper (`app.go:671`) runs `reconcileGuardLayers`, `openParkedOverride` and
  `openDeferredMcpFollowups` after every message; `sources.go:421` and `mcp.go:845` can set `m.status`.
- `applyUpdateAvailable` (`update_ui.go:107`) sets `m.updateBanner` with no `SetSize`.
- Terminal screen: `termRows` (`terminal.go:264`) is `bodyHeight() - 2`; `resizeTerminal` runs only on a window
  resize; `attachTerminal` reads `termRows()` before it sets `opening terminal…`; `viewTerminal` is header +
  blank + grid. `escHoldHint` is 101 columns.
- Hold-relevant assignments: `attachTerminal` sets `opening terminal…` / `replacingText` (`terminal.go:114`);
  `handleTermOpened` sets `m.status = ""` (`:161`) or `terminal for <name> not opened — you moved on; press t again`
  (`:150`); `handleTermClosed` sets `an external terminal is already attached to <name>` (`:190`),
  `terminal: <reason>` (`:199`) or `detached from <name>` (`:203`); `ctrl+q` sets `detached from <name>` (`:283`);
  `SendData` failure routes through `handleTermClosed` (`:306`). `termSeq` numbers attaches; a stale
  `termOpenedMsg` closes its session. `confirmReplaceOrphan` (`orphan.go:100`) opens the Replace dialog with
  `onConfirm` returning `replaceOrphanMsg`, handled in a **later** `Update` (`app.go:707` → `startReplaceOrphan` →
  `attachTerminal`). `confirmState.onAccept` runs in the same message as the accept (`confirm.go:113`).
- `nowFunc` (`unsaved.go:16`) is the test-steppable clock.
- `x/ansi v0.11.6` has `Truncate`, `TruncateLeft`, `Wrap`, `Wordwrap` and `StringWidth` (probe compiled). The
  middle cut `Truncate(s, 20, "") + "…" + TruncateLeft(s, width-39, "")` gave exactly 60 columns for the spec's
  example. `Wordwrap(msg, 80, "")` wraps the longest FR-529 example to 2 lines, but leaves a token longer than
  the width unbroken (`wrap.go`: "not breaking word boundaries"), so a 120-column unspaced daemon error would
  overflow; `Wrap(msg, 80, "")` breaks such a token and still prefers spaces and hyphens (R9).

### F4 — Inputs and the guard

- `enterRename` (`app.go:1515`): `SetValue` then `renameInitial = TrimSpace(Value())`, so the baseline is the
  cleaned text. Bubbles v1.0.0 `textinput` sanitises on `SetValue` and on paste (`runeutil`): newlines and tabs
  become spaces, other control characters are dropped. `m.rename` has `Width` 0 and doesn't scroll sideways.
- `activeGuard` (`formguard.go:302`) picks `gHostAdd` from `screenHosts` + `m.hosts.adding`; the dirty check for
  `gRename` is `TrimSpace(Value()) != renameInitial` (`formguard.go:451`); the Save branch calls `submitRename` /
  `submitHostAdd` (`formguard.go:505–513`), both of which close the input **before** acting.
- `normalizeSuffix` / `normalizeNotice` (`formguard.go:143`) are SWI-9's rewrite notice.
- `windowRange` (`jobs.go:467`) is a cursor-centred window over n lines, minimum 3.

### F5 — Zed's `ssh://` address (open item, confirmed from source)

Checked in `zed-industries/zed` `main` on 2026-11-03:

- `crates/cli/src/main.rs`: the `zed` CLI treats any argument starting with one of `zed://`, `http://`,
  `https://`, `file://`, `ssh://` as a URL and forwards it **unchanged** to the app. Plain paths are canonicalised
  locally; spaces are not special.
- `crates/zed/src/zed/open_listener.rs`: `OpenRequest::parse` sends `ssh://…` to `parse_ssh_file_path` →
  `parse_ssh_url`, which first tries `url::Url::parse`. The host is `url.host()` (`Domain`, `Ipv4` or `Ipv6`,
  brackets consumed by the parser), the user is `url.username()` percent-decoded, the port `url.port()`, and the
  path is `url.path()` **percent-decoded** with `urlencoding::decode`. A `zed://ssh/…` link is rewritten to the
  same form. A relative path (`ssh://me@host:code/x`) is rejected; `ssh://me@host:~/x` is an SCP-style fallback
  that yields `/~/x`, which the TUI doesn't use.
- Docs (`zed.dev/docs/remote-development`): `zed ssh://[<user>@]<host>[:<port>]/<path>`; SSH options come from
  the settings' matching connection and `~/.ssh/config`.

So the address is `ssh://[user@]host[:port]/<absolute path>` with the path percent-encoded as a URL path: one
`/` after the authority (the absolute path supplies it), spaces as `%20`, `#` and `?` as `%23`/`%3F`, a literal
`%` as `%25`, non-ASCII as UTF-8 percent-escapes. Go's `(&url.URL{Scheme: "ssh", User, Host, Path}).String()`
produces exactly this (probe): `ssh://user@box/srv/ws/my%20proj`, `ssh://[2001:db8::1]/x`,
`ssh://u@[2001:db8::1]:22/x`, `ssh://user@box/%C3%BC/%C3%A9`, and a target containing `/` or `?` fails the
`url.Parse` round trip (`invalid URL escape "%2F"`), which is the refusal FR-513 asks for.

### F6 — Windsurf's `--remote` (open item, documentation-derived)

- Windsurf's docs say its SSH support is "our own" implementation, that Microsoft's Remote-SSH and
  `open-remote-ssh` conflict with it and must not be installed, and that only Linux hosts are supported. The CLI
  is the Code OSS CLI, which has `--remote <authority>`.
- No official page names the authority prefix. A third-party launcher documents the deep link
  `windsurf://vscode-remote/ssh-remote+<host>/<path>`, which mirrors VS Code's own `vscode-remote` scheme, so the
  authority is very likely `ssh-remote+<host>`; it could not be fetched and verified in this run (HTTP 403), and
  no Windsurf install is available here. The same holds for Cursor, which is widely reported to accept
  `cursor --remote ssh-remote+<host> <path>` but isn't verified here either.

### F7 — Launch, reaping and executable lookup

- Go's `exec.Cmd` with `SysProcAttr{Setsid: true}` starts the child in a new session (own process group, no
  controlling terminal) on Linux and macOS; nil `Stdin`/`Stdout`/`Stderr` connect to `/dev/null`. The release
  targets are `darwin` and `linux` only (`.goreleaser.yaml`), so no build tags are needed. `cmd.Wait()` in a
  goroutine reaps the child; `ProcessState.ExitCode()` is `-1` for a signal.
- `exec.LookPath(name)` stats a file in each `PATH` directory, or the given path when the name contains `/`; a
  stale network mount in `PATH` can block it, which is why the spec puts it inside the command.
- `os.UserHomeDir()` can fail (`$HOME` unset), which is FR-531's "home directory can't be determined".

### F8 — Bubbles list minimum and the 80×24 budget

The spec's Notes measured today's layout: the sandbox list's help footer is 5 lines at 80 columns and the bubbles
list draws at least 9 rows, so a one-line message needs 20 rows (21 with the banner), leaving 4 lines for a
message at 80×24 with the banner. These are inputs to R9's limits, not re-measured here; T040's SC-004 test
measures the real text.

## Decisions

### R1 — One `editor` package replaces `vscode`

**Decision**: a new package `src/apps/switchboard-tui/internal/editor` owns the catalogue, the command rules and
the launch, and `internal/vscode` is deleted. Its files: `catalog.go` (FR-501 rows, lookup by identifier,
`Remote` kind), `shellwords.go` (FR-531 tokenizer), `check.go` (FR-506 checks and the markers, FR-515/516),
`pathcheck.go` (FR-517), `zed.go` (FR-513 address), `resolve.go` (an open request → argv or a typed refusal,
FR-510–FR-518), `launch.go` (FR-511 detached start, FR-523 reaper). It imports only the standard library (the workspace path
arrives as a string; the `pb.VSCodeTarget` stays in `ui`), never `ui`, `config` or `store`, so `config` can
import it (R2).

**Rationale**: the rules run in two places (the settings screen on confirm, `v` on every press, FR-522) and must
match; one package makes that structural. Everything but the launch is pure, so SC-003's per-editor argv test
and the tokenizer tables need no TUI.

**Alternatives**: extend `vscode` in place (the name would lie, and `Opener.Open(*pb.VSCodeTarget, ssh)` can't
carry the editor); rules inside `ui` (untestable without a model; duplicated by any future CLI).

### R2 — Override variables declared from the catalogue; set-ness preserved

**Decision**: `config.schema` gains one entry per non-custom editor other than VS Code, generated from
`editor.Catalogue()` at init: `SWITCHBOARD_CODE_INSIDERS_BIN`, `SWITCHBOARD_CURSOR_BIN`, `SWITCHBOARD_WINDSURF_BIN`,
`SWITCHBOARD_ZED_BIN`, `SWITCHBOARD_WEBSTORM_BIN`, `SWITCHBOARD_IDEA_BIN`, `SWITCHBOARD_PYCHARM_BIN`,
`SWITCHBOARD_GOLAND_BIN`, `SWITCHBOARD_SUBLIME_BIN`, each `def` = the default command and `doc` naming the
editor (FR-503). `SWITCHBOARD_CODE_BIN` keeps its row. `Config.CodeBin` is replaced by
`Config.EditorBins map[string]string` (identifier → the **raw** value, `""` when unset or blank after trimming,
FR-502). `Load` reads these keys raw instead of substituting the default, because the default is applied by
`editor.Command(e, bins)`, the one place that decides whether the variable counts as an override (FR-517).
`.env.example` lists each with its default (env-check lockstep). A value equal to the default isn't an override, so
exporting that file as it stands changes nothing (R13).

**Rationale**: FR-503 wants the vars in the one declared list the env check and docs read; deriving them from the
catalogue means adding an editor is one row. Keeping the raw value preserves "used as one executable, exactly as
given" and the set/unset distinction.

**Alternatives**: a single `SWITCHBOARD_EDITOR_BINS` map variable (not what the ticket asked for, and not
"the same way `SWITCHBOARD_CODE_BIN` works"); trimming the value (the spec says exactly as given).

### R3 — Settings keys, typed merge-on-save, owner-only mode

**Decision**: `settings.toml` gains `editor` (string, FR-501 identifier) and `editor_custom_command` (string, the
raw text). `store.Settings` becomes `{AutoConnectHosts bool; Editor string; EditorCustomCommand string}` where
`Editor` is the **raw** saved string (`""` = absent → VS Code; an unknown value is kept so the session can show
FR-508's notice and never writes the fallback). `settingsFile` gains the two pointer fields. The whole-struct
`Save` is replaced by `SettingsStore.Update(c SettingsChange) error`, where `SettingsChange{AutoConnectHosts
*bool; Editor *string; EditorCustomCommand *string}` names the keys to write (a non-nil field). `Update`
re-reads the file into `settingsFile` (a missing file is the zero value; a read or parse error returns
`*store.SettingsUnreadableError` without writing), copies each non-nil change over the corresponding pointer
field, so only the changed keys are written and an absent, unchanged key stays absent, and writes through a new
`Store.SaveTOMLPrivate(name, v)`: `os.CreateTemp(dir, ".settings.toml-*.tmp")` (fresh, unique, `0600`), write,
close, `os.Rename`, then `os.Chmod(full, 0600)` so an existing wider file is corrected (FR-527). A failure after
`CreateTemp` removes the temp file: each name is unique, so nothing else would, and it can hold the Custom command
after the developer has cleared it from settings. Unknown keys and
comments are dropped, as FR-530 states. The callers are: toggle → `{AutoConnectHosts: &v}`; built-in editor →
`{Editor: &id}`; Custom → `{Editor: &"custom", EditorCustomCommand: &text}`.

**Rationale**: the typed merge gives FR-530's "apply only the changed setting to the values just read" and SC-007
with no map handling, and go-toml's typed decode is what makes `editor = 5` a parse error (F2). A new private
writer keeps hosts, groups and configs on today's path, as the Notes asked, and `CreateTemp` removes the
leftover-temp hole the review found.

**Alternatives**: map-based merge keeping unknown keys (better for downgrades, but the spec records that unknown
keys aren't kept, and a map hides wrong types); changing `SaveTOML` for every file (a behaviour change outside
this feature); a file lock (FR-530 accepts last-write-wins).

### R4 — Session state for the settings file's three conditions

**Decision**: `settingsState` gains `fileUnreadable error` (set by `WithSettings` when `Load` failed: the
unreadable-at-startup state, FR-525 first bullet; nothing is ever written while it's set, so every save skips
the store), `lastSaveFailed error` (set when `Update` returns `SettingsUnreadableError` mid-session, cleared by
the next successful `Update`, FR-525 second bullet), and `notSaved map[string]bool` keyed by row key
(`auto_connect_hosts`, `editor`) for `(not saved)` (FR-530). `m.prefs` holds the session's values. Derived at
render: the unknown-editor notice from `m.prefs.Editor` (FR-508), the unreadable notice from `fileUnreadable`,
the mid-session notice from `lastSaveFailed`. `WithSettings` also sets `m.status` to the startup notices
(FR-529, best effort).

**Rationale**: three distinct causes with three distinct messages and lifetimes, each derived from one field, so
no save can clear a notice by accident (FR-508's "derived from the session's editor value").

### R5 — The `v` pipeline and its messages

**Decision**: `openVSCodeCmd` is replaced by `openEditorCmd` in a new `ui/editor_open.go`. On the update loop it
builds an `editor.Request{Editor, Bins, Custom, WorkspacePath: "", SSHTarget, HostErr}` from the session: the
host lookup (`sshTargetForHost` is changed to return the lookup error too, FR-520), the session editor (unknown →
VS Code), the custom command. `editor.Precheck(req)` runs every check that doesn't need the path (FR-526: host
error, no remote support, FR-522's custom checks, `-` on the target as given and normalised, the Zed address,
`~` needing a home, and returns the resolved executable). A refusal returns at once as `editorOutcomeMsg{id,
text, failed: true}`. Otherwise the command: `exec.LookPath(exe)` (FR-518's not-found refusal, in the command so
the UI never blocks), `d.VSCodeTarget`, `editor.Resolve(req with path)` (FR-517 path checks, argv), then
`editor.Launch(argv)`. It returns `editorOutcomeMsg` with the opened text, or the failure text, and for a launch
the `wait <-chan editor.ExitReport` from the reaper. The handler for any outcome deletes **only this row's** `busy`
entry and calls `refreshListItems` (a row's text is built into its list item, and the spinner tick only rebuilds while
something else is animating, so the row would keep showing `opening`), and doesn't reload. A successful outcome
also sets the status, clears `m.err` as `statusMsg` does today (or an earlier `errMsg` would show it in red, FR-529,
SC-002) and returns a command that waits on `wait`; a report
arrives as `editorExitMsg` (FR-523) and sets only the status fields. Message text is assembled in `ui` from
typed parts (`editor.Refusal{Rule, Detail, Where}`), with the notices appended per FR-529 and outside text cut
per FR-533 (R9), so the daemon error and the OS reason are cut before the notices are added.

**Rationale**: `statusMsg`/`errMsg` reset every row's spinner and reload (F1), which FR-523 forbids for the
report and the Notes flag for the completion; a dedicated message also carries the error style (R10) and the
chained wait guarantees the report never precedes the "opened" message (`tea.Batch` doesn't).

**Alternatives**: keep `statusMsg` for the completion (the notice assembly and the `busy` semantics still need
the new type; two types for one action is more state, not less); a timer-based report (`tea.Tick` after 3 s)
instead of the reaper's channel (the reaper already knows the exit; a tick would add a second clock).

### R6 — Tokenizer and checks (FR-506, FR-531)

**Decision**: a hand-written scanner, `editor.Split(text) ([]Word, error)`, over runes with the states
*unquoted*, *single*, *double* and *escape*. Each `Word{Text string; TildeOK bool}` records whether its first
character was an unquoted, unescaped `~` so `~` and `~/…` expand only then (FR-531). During the scan the FR-506
syntax rules are checked where the quoting is known: an unquoted or double-quoted `$` or backtick, and an
unquoted `|`, `;`, `&`, `<`, `>`, return `*CheckError{Rule}` naming the rule (`shell-syntax`), as do unbalanced
quotes, a trailing unquoted backslash and a control character other than a tab. After the scan, `Check(words)`
rejects: no words (`empty`), an empty first word (`empty-executable`), a marker in the first word
(`marker-in-executable`), a first word matching `^[A-Za-z_][A-Za-z0-9_]*=` (`assignment`, "quoted or not" holds
because the check runs on the split word). The error type carries only the rule, never the text (FR-532).
`Expand(words, home)` applies `~` and then `Substitute(words, path, host)` replaces markers in a single pass over
the original words, dropping a word that is exactly `{host}` on a local sandbox and appending `path` when no
word contains `{path}` (FR-515/516).

**Rationale**: the per-word quoting flags the spec needs aren't available from any splitting library in
`go.mod` (Notes), and the grammar is small (four states). Rule-only errors make FR-532 hold by type.

**Alternatives**: a regex pass over the raw text (can't tell a quoted `$` from an unquoted one); `shellwords`-style
libraries (no quoting information; a new dependency).

### R7 — Detached launch and the 3-second report (FR-511, FR-523)

**Decision**: `editor.Launch(argv []string) (*Started, error)` builds `exec.Command(argv[0], argv[1:]...)` with
`SysProcAttr{Setsid: true}`, nil stdio and the TUI's environment, calls `Start`, then runs `Wait` in a
goroutine that lives until the editor exits, however long that is, so the child is always reaped (FR-511).
`Started.Report <-chan ExitReport` receives at most one value: a second goroutine selects on the `Wait` result
and a `time.After(exitGrace)` (a package variable, 3 s): a non-zero exit or signal before the grace sends
`{Status, Signal}`; a zero exit, or the grace elapsing first, closes the channel with nothing sent. The `Started`
keeps nothing else, so the TUI never waits for the editor. Tests use
stand-in scripts (`sh -c 'exit 3'`, `sleep 10`, `kill -TERM $$`) with a shortened grace through a package
variable.

**Rationale**: `Setsid` gives its own session and no TTY on both release targets without build tags (F7);
a `Wait` goroutine is the only way the status can be known and the only way the child is reaped.

### R8 — Remote forms per editor (FR-513, FR-514)

**Decision**: the catalogue's `Remote` field is one of `RemoteSSHOption` (VS Code, VS Code Insiders, Cursor,
Windsurf: `--remote ssh-remote+<target> <path>`), `RemoteSSHAddress` (Zed: one argument, the F5 address) or
`RemoteNone` (WebStorm, IntelliJ IDEA, PyCharm, GoLand, Sublime Text: refuse with FR-514's message). Zed's
target is normalised by `editor.ZedAddress(target, path)`: strip a leading `ssh://`, split the user at the last
`@`, bracket a bare IPv6 host (more than one `:` and no `[`), build the `url.URL`, and require the string to
parse back to the same user, host and path; otherwise `*Refusal{Rule: "zed-target"}`. The `-` check (FR-517) runs
on the target as given and on the normalised user and host. Windsurf's and Cursor's `--remote` support stays
**documentation-derived** (F6): `contracts/editor-launch.md` marks both rows, and quickstart Scenario 0 confirms
them on a real install; if Windsurf's authority differs, the fix is one catalogue row.

**Rationale**: F5 settles Zed's form from source; the VS Code forks share Code OSS's CLI. Refusing is the spec's
answer for the rest.

**Alternatives**: JetBrains Gateway (`jetbrains-gateway://`) and a VS Code fallback — both ruled out by the spec.

### R9 — Status line wrapping, cutting and body resizing (FR-533, FR-534)

**Decision**: a new `ui/status.go`:

- `statusLines(width, limit) []string`: `sanitizeLine(s, 0)` (sanitised, newlines to spaces) → `ansi.Wrap(s, width, "")`
  → cut to `limit` lines, the last ending in `…` (`ansi.Truncate(last, width-1, "") + "…"`: `Truncate` adds its tail
  only to text that's too long, and a line wrapped at a space usually isn't, so passing `…` as the tail would often
  cut the message with no sign of it). `Wrap`, not `Wordwrap`:
  `Wordwrap` never breaks a token longer than the width, so a 120-column daemon error would stay on one line and
  Bubble Tea would cut it with no ellipsis; `Wrap` still prefers spaces and hyphens and breaks a long token only
  when it must. No cache: wrap and cut on each use.
- `m.statusLimit()`, computed **without rendering**: `min(statusMaxLines (6), max(1, m.height - headerLines (2)
  - 1 - helpLines - minBody))`, where `helpLines` comes from `m.activeHelp()` (the one screen → help map, which
  `viewScreen` also reads, since today only `viewScreen` knows it and assigns it together with the body) and `minBody`
  is the active surface's smallest usable body: the sandbox list
  `9 + 2 + banner`; a resizable item list or huh form `3` (`bodyHeight`'s floor); a view that reads `bodyHeight`
  as it renders and windows its rows (Jobs, the lanes list in `orchestrator_lanes.go`, and the settings
  screen's list and input modes, which this feature makes read `bodyHeight` the same way: the list windows its
  entries with `windowRange` and the input gives its note the room left) its fixed top and bottom lines plus
  `windowRange`'s minimum 3 (for the input: title + input + blank + the rewrite notice when shown, plus 1 note
  line, so the note gives way before the status, FR-534; for Jobs, `jobsFixedLines`, shared with `viewJobs`, because
  those lines vary; for the lanes list, `lanesFixedLines` + the 8 lines it reserves + 6, shared with `lanesView`,
  because it windows two-line entries); a static view with nothing to shrink (Daemons, the
  settings rows, Hosts, Groups, …) `0`, so the limit is the 6-line cap and `chrome` does the real cut (next
  bullet). The terminal screen's limit is the constant 2.
- `bodyHeight()` becomes `m.height - 6 - (lines - 1)` where `lines` is the wrapped status's line count after the
  limit (at least 1), so a one-line or empty status gives today's value. `statusLineCount` returns 1 before it
  computes a limit when the status is empty or fits on one line (`m.statusFitsOneLine()`, shared with `chrome`, measures
  `sanitizeLine(status, 0)`, the text `statusLines` wraps, because a newline has no width until it becomes a space): the
  limit can't matter there, and working out the
  help costs ≈ 295 µs and 453 KB at 80×24 with 8 sandboxes (measured in review), against ≈ 332 µs and 704 KB for a
  whole `View` today, so paying it twice more per frame would make the usual frame 2–3× as expensive for no benefit.
  Nothing in this computation renders a
  body, so a view that reads `bodyHeight` while rendering (Jobs) sees a stable value. `termRows()` stops using
  `bodyHeight` and uses the fixed `m.height - 8` (floor 3), so a terminal opened during a wrapped message gets the
  same PTY size as one opened with none (SC-008), and a status change never resizes the PTY.
- `chrome(body, hb)` renders the help once from `hb` and keeps its line count. Only when the status doesn't fit on
  one line does it compute `statusLimitFor(helpLines)` and measure the room actually left by the rendered body
  (`m.height - 2 - lipgloss.Height(body) - 1 - helpLines`, at least 1), passing `min(limit, room)` to `footer`, so a
  static view cuts the message to fit with the header on screen; `chrome` never drops lines from the bottom of the
  body. `BenchmarkViewList` keeps the frame cost visible.
- **Size check** in the `Update` wrapper, after the release (R10) and skipped on the terminal screen: a small
  `sizeKey{status, width, height, banner string, surface activeSurface, tabs int, markFree bool}` is compared
  with the one stored; on a change the active surface's height is compared with its target and set only if they
  differ: `m.list.Height() != m.mainListHeight()` → `SetSize`; an open item list (groups, hosts, configs picker,
  kit picker, notifications, runs, services) → `SetSize(bodyWidth, bodyHeight)`; an open huh form → `WithHeight`
  only when the stored `lastHeight` differs, followed by `form.Update(nil)` in the same pass with its command
  kept (the undrawn-field fix). `activeSurface()` is a new enum over the surfaces that hold a size, derived like
  `activeGuard`. Forms don't grow back a field huh has shrunk; accepted until the form closes.
- `viewSettings` wraps every row, description and notice with `ansi.Wrap(s, m.width, "")`; the executable gets the
  middle cut (`cutMiddle(s, 60)` = `sanitizeLine(s, 0)` then `Truncate(20) + "…" + TruncateLeft(width-39)`,
  dropping one column when a wide rune lands on the cut, F3); notices put the instruction before the detail.
- `host-lookup`'s error uses the same cut at 78 (`cutMiddle(s, cols)` keeps `cols - 40` + 39, and text that fits is
  unchanged). Lookup errors put the path first and the reason last, so an end cut loses the reason: at 78,
  `open /home/firstname.lastname@corp.example.com/.config/switchboard/hosts.toml…` gives none. The middle cut gives
  `open /home/firstname.lastname@corp.exa…itchboard/hosts.toml: permission denied`, and 300,000 random errors stayed
  within 78 columns and 4 lines (probed with the real `sanitizeLine` and `ansi.Wrap`).

**Rationale**: the Notes measured the costs (wrap ≈ 7 µs, target ≈ 300 µs) and ruled out a cache and a line-count
comparison; a key makes the check run only when an input changed, and the size comparison catches a surface the
developer returns to, a late banner and a help footer that changes height.

**Alternatives**: resizing at each `m.status =` (107 sites); scrolling the status (the spec says no scrolling);
a per-screen `bodyHeight` parameter (the 14 build sites would still be stale).

### R10 — Holding a `v` failure or refusal; the error style (FR-529, FR-533)

**Decision**: new `Model` fields in `ui/status.go`:

- `vFail struct{text string; at time.Time}`: the last `v` failure or refusal and when it arrived. `footer` uses
  the error style when `m.status != "" && m.status == m.vFail.text` (and keeps today's `m.err`/`error` prefix
  rule). A completion message never sets it, and clears `m.err` as `statusMsg` does today. `editorOutcomeMsg` with
  `failed` and `editorExitMsg` set `m.status`, `m.vFail` and don't touch `m.err`.
- `vHold struct{text string; seq uint64}`: a held message and the attach number it waits on (0 = none pending).
- `confirmState.holdsV bool`, with `m.vHoldDialog()` = `m.screen == screenConfirm && m.confirm.holdsV`: whether a
  Replace dialog for an in-place attach is open. It's derived from the showing confirm rather than stored beside
  the hold, because `closeOrchAdminOnDrop` closes a confirm running neither `onAccept` nor `onCancel` (`hosts.go:484`),
  and the dialog can sit over the orchestrator detail. A stored flag would stay true and swallow every later failure.
- `termFail string`: the text a release joins instead of replacing. That's the last attach or terminal error, set at
  `terminal.go:150`, `:190`, `:199` (which covers `:306`), and `closeOrchAdminOnDrop`'s `<name> disconnected —
  orchestrators closed` (`hosts.go:489`), the only message explaining why the screen changed. `attachTerminal` clears it,
  and so does `confirmReplaceOrphan` for a non-external attach, because `t` can open that dialog with no `attachTerminal`,
  and an old error still showing would otherwise join a failure held behind it. A drop while the dialog is open sets it
  after that clear, so that case still joins.

Rules: `attachTerminal` (in-place only) sets `vHold.seq = m.termSeq` and, if `m.status == vFail.text` and
`nowFunc().Sub(vFail.at) < vHoldRecent (3 s)` and `vHold.text == ""`, copies the status into `vHold.text`.
`confirmReplaceOrphan` for a non-external attach sets `holdsV` with the same copy rule and sets
`onAccept` to set `vHold.seq = m.termSeq + 1` (the number `attachTerminal` will take in the later
`replaceOrphanMsg`) in the same message, so the hold passes from the dialog to
the attach with no gap; a decline or a dropped dialog needs nothing, so the release follows at once. A `v` failure or refusal arriving while `vHold.seq != 0`
or `m.vHoldDialog()` or the terminal screen is showing replaces `vHold.text` (and is also drawn, cut to 2 lines, on
the terminal screen). `handleTermOpened` and `handleTermClosed` clear `vHold.seq` only for `msg.seq ==
vHold.seq`. The **release** step in the `Update` wrapper (after `reconcileGuardLayers`, `openParkedOverride` and
`openDeferredMcpFollowups`, before the size check): if `vHold.text != ""` and `vHold.seq == 0` and `!m.vHoldDialog()`
and `m.screen != screenTerminal`, then the message is `sanitizeLine(m.status, 120) + "; " + vHold.text` when
`m.status != "" && m.status == m.termFail`, else `vHold.text`; it's written to `m.status` and `vFail.text`,
`vFail.at` is zeroed (so it can't be held again), `vHold` is cleared. With nothing held, a terminal error stays
as it is today.

**Rationale**: each rule maps to a spec sentence or a Notes trap (superseded attach → `seq`; accept gap →
`onAccept`; style ends with the message → compare text; released once → zero `at`).

### R11 — The command input is a guarded inline input (FR-528)

**Decision**: `settingsState` gains `mode` (`settingsRows`, `settingsList`, `settingsInput`), `listCursor`,
`input textinput.Model` (with `Width = bodyWidth - 4` so it scrolls sideways) and `inputInitial string`. A new
`gEditorCommand` is returned by `activeGuard` for `screenSettings` with `mode == settingsInput`; `guardDirty`
compares `TrimSpace(Value())` with `inputInitial`; Save calls `submitEditorCommand`, which runs `editor.Split`
+ `Check` on the trimmed value (rejection → status, input stays open), then `settingsStore.Update` (write first:
a `SettingsUnreadableError` or a nil store applies for the session only, marks `(not saved)` and closes; any
other error keeps the input open with the error; success closes to the rows and clears the mark); Discard
returns to the list. On open, `SetValue(session command)`, `inputInitial = TrimSpace(Value())`, and if that
differs from the session text `inputRewrite` is set and the view draws `normalizeNotice([]string{"Custom
command"})` as two body lines under the input (the spec's 23-line budget counts it there, separate from the
status), cleared when the input closes. `esc` in the list
returns to the rows with no dialog; `q` and `,` are text in the input and ignored in the list. Opening focuses the
input and returns `Focus()`'s command, as `enterRename` does (`textinput.Update` returns early when unfocused).
`forwardScreen` gains a `screenSettings` case that sends non-key messages to the input while `mode == settingsInput`;
without it they fall to `m.list`, which loses a `ctrl+v` paste (a command returning `pasteMsg`) and the cursor blink.
The `tea.WindowSizeMsg` handler resets `Width` while `mode == settingsInput`, or a narrowed terminal cuts a long
command at the edge (FR-534). `Width` alone doesn't move the visible window until the next key, and
`SetCursor(Position())` moves it only when the cursor is outside the old window (probed), so the reset is
`pos := Position()`, set `Width`, `CursorEnd()`, `SetCursor(pos)`.

**Rationale**: the Notes rule out `gRename`'s screen-only selection and the close-then-act submits; the
textinput's own sanitising gives FR-528's cleaning for free.

### R12 — Footer label and messages (FR-519)

**Decision**: `listHelp()` rebuilds the `v` binding with `key.WithHelp("v", editorID)` (the identifier, `custom`
for Custom), so with VS Code it reads `vscode` as today. The completion message is
`opened <id8> in <DisplayName>` with the notices in one pair of parentheses, so `opened abc in VS Code` is
byte-identical to today's when no notice applies. `VS Code opener not configured` becomes
`<DisplayName> opener not configured`.

### R13 — Where the remote path limit applies (FR-517)

**Decision**: `editor.Resolve` applies the character limit only when `req.SSHTarget != ""` and either the editor
is Custom or `Command(editor, req.Bins)` reports `overridden` (the selected editor's own variable set, with
FR-502's meaning: a blank value doesn't count; nor does a value equal to the default command, which starts the same
program as leaving it unset, so exporting `.env.example` as it stands can't switch the limit on). The check is on the path's runes:
valid UTF-8, `unicode.IsLetter`, `unicode.IsMark`, `unicode.IsDigit`, ASCII space, or one of `/ . _ - + = @ , :`.
The refusal names the first rejected rune as `'x'`, or `U+00XX` when `unicode.IsPrint` is false, and says where to
fix it (SC-004): move the workspace on the daemon, or, for a built-in editor, unset the variable, or, for Custom,
which has no variable, switch editors on the settings screen.

### R14 — Tests and the e2e stand-ins

**Decision**: unit tests per package (`editor`: catalogue argv per editor and sandbox kind for SC-003, tokenizer
tables, checks, Zed address, path limit, launch with scripts; `store`: merge, unreadable, permissions, leftover
temp; `config`: schema and set-ness; `ui`: settings rows/list/input, guard, `v` messages, hold, size check,
wrapping at 80×24 for SC-004 and SC-008 using `tea.WindowSizeMsg{80, 24}` and `m.View()` line/width assertions).
E2E (`switchboard-tui-e2e`): a stub editor script on `PATH` that appends its argv to a file, driven through
`spawnTUIWithEnv` with `PATH` and `SWITCHBOARD_WEBSTORM_BIN`; one test picks WebStorm in the list and checks the
file, presses `v` and checks the argv log; one sets a Custom command with `{path}` and checks the log; one uses a
stub that exits 3 and checks the FR-523 report. Coverage stays at the 90 % floor per module (`make cover`).

### R15 — Documentation

**Decision**: README: the TUI env table gains the nine rows; the `v` key row says "in the selected editor"; a new
**Editor** subsection under *Settings & startup sign-in* covers the list, the override variables with the
re-parsing warning and the remote path limit (on while the selected editor's variable is set to something other than
its default), the Custom command rules with the positional `kitty -e sh -c 'cd "$1"
&& nvim .' sh {path}` example, the plain-text storage and `ps` visibility, the settings screen as the way to set a
command with quotes, the TOML literal forms, and that hand edits take effect at the next start and comments
aren't kept (FR-524). The Prerequisites line about VS Code becomes "an editor from the list".

### R16 — Not in this feature

Per-host editors, JetBrains Gateway, a separate remote command field, escaping markers, keeping comments or
unknown keys in `settings.toml`, scrolling status lines, and fixing the selected row scrolling off a very short
terminal. Each is recorded in the spec's Edge Cases or Assumptions.
