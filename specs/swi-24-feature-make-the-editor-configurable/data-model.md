# Data Model: Configurable Editor (SWI-24)

Phase 1 of `/speckit-plan`. Nothing here touches the daemon, the proto or bbolt: every entity is client-side,
in the TUI's `settings.toml` or in the Bubble Tea model. Field names are the ones the tasks use.

## 1. Editor (catalogue entry) — `editor.Editor`

| Field         | Type         | Notes |
|---------------|--------------|-------|
| `ID`          | `string`     | FR-501 identifier; what `settings.toml` stores and the footer shows. |
| `Name`        | `string`     | Display name (`VS Code`, `Custom`, …). |
| `Command`     | `string`     | Default command (`code`, `subl`, …); empty for Custom. |
| `EnvVar`      | `string`     | Override variable (`SWITCHBOARD_<X>_BIN`); empty for Custom. |
| `Remote`      | `RemoteKind` | `RemoteSSHOption` (`--remote ssh-remote+<target> <path>`), `RemoteSSHAddress` (Zed, one `ssh://` argument), `RemoteNone` (refuse), `RemoteCustom` (markers). |

`editor.Catalogue()` returns the rows in FR-501's order; `editor.Lookup(id) (Editor, bool)`; `editor.Default` is
`vscode`. `editor.Command(e, bins map[string]string) (bin string, overridden bool)` returns the raw variable
value when `strings.TrimSpace(bins[e.ID]) != ""`, else `e.Command`. `overridden` is true only for a set value that
differs from `e.Command` (FR-517).

## 2. Settings — `store.Settings` and the file

```go
type Settings struct {
    AutoConnectHosts    bool   // existing
    Editor              string // raw saved identifier; "" = absent (VS Code); unknown values are kept
    EditorCustomCommand string // raw command text as saved; "" = none
}
type settingsFile struct {           // on-disk shape, pointer = key present
    AutoConnectHosts    *bool   `toml:"auto_connect_hosts"`
    Editor              *string `toml:"editor"`
    EditorCustomCommand *string `toml:"editor_custom_command"`
}
```

Rules:

- `Load() (Settings, error)`: a missing file is the defaults; an unreadable or unparseable file (including a
  wrong type) returns the defaults **and** the error (unchanged contract).
- `Update(c SettingsChange) error` with `SettingsChange{AutoConnectHosts *bool; Editor *string;
  EditorCustomCommand *string}`: re-read → copy each non-nil change over the file's pointer field → write. A key
  absent in the file and nil in the change stays absent; a key present stays as read. A read or parse failure
  returns `*SettingsUnreadableError{Err}` and writes nothing (FR-525, SC-006). Written with `SaveTOMLPrivate`
  (`0600`, fresh unique temp `.settings.toml-*.tmp`, final chmod; FR-527).
- Validation of `Editor` and `EditorCustomCommand` is **not** done in the store: the session decides what an
  unknown or invalid value means (FR-508, FR-522).

## 3. Session settings state — `ui.settingsState`

| Field              | Type                 | Meaning |
|--------------------|----------------------|---------|
| `cursor`           | `int`                | Row cursor (existing). |
| `returnTo`         | `screen`             | Screen to return to (existing). |
| `mode`             | `settingsMode`       | `settingsRows` · `settingsList` · `settingsInput`. |
| `listCursor`       | `int`                | Cursor in the Editor list; starts on the session's editor. |
| `input`            | `textinput.Model`    | The Custom command input (`Width` set on open and on every resize, sideways scrolling; focused, with its non-key messages forwarded). |
| `inputInitial`     | `string`             | Cleaned, trimmed starting value (SWI-9 baseline). |
| `inputRewrite`     | `bool`               | Cleaning changed the saved command when the input opened: the rewrite notice is drawn under the input (2 body lines) until it closes (FR-528). |
| `fileUnreadable`   | `error`              | Non-nil = unreadable at startup (FR-525 state 1): nothing is written this session. |
| `lastSaveFailed`   | `error`              | Non-nil = broken mid-session (FR-525 state 2): cleared by the next successful `Update`. |
| `notSaved`         | `map[string]bool`    | Row keys whose shown value was never written (`auto_connect_hosts`, `editor`); drawn as `(not saved)`. |

Plus on `Model`: `prefs store.Settings` (the session's values, existing), `editorBins map[string]string` (from
`config`), `launcher editor.Launcher` (the start hook, overridable in tests).

Derived (never stored): `sessionEditor() (editor.Editor, unknown string)` — `prefs.Editor` looked up, falling
back to VS Code with `unknown` = the raw value cut to 60 columns (FR-508, FR-533); `settingsNotices() []string` —
the screen's unknown-editor notice, the unreadable-at-startup notice, the mid-session notice, in that order
(FR-508, FR-525); `vNotices() []string` — the `v` wordings for the first two conditions only (`unknown editor
'<value>' in settings`, `settings couldn't be read at startup, changes aren't saved`; contracts/settings-ui.md
§4, FR-529).

Row rendering (FR-504, FR-532, FR-534): Editor row value = `Name`, or `Custom: <cutMiddle(executable, 60)>` plus
`…` when more arguments, or `Custom (invalid command)`; then ` (not saved)` when `notSaved["editor"]`.

### State transitions (settings screen)

```
rows ──space/enter on Editor──▶ list ──enter on built-in──▶ rows (Update{Editor: &id})
                                 │  ──enter on Custom──▶ input ──enter (checks pass, Update{Editor: &custom, EditorCustomCommand: &text})──▶ rows
                                 │                         │   ──enter (rejected / write failed)──▶ input (reason)
                                 │                         │   ──esc clean──▶ list
                                 │                         └──esc dirty──▶ unsaved dialog: Save=enter · Cancel=input · Discard=list
                                 └──esc──▶ rows (nothing saved)
rows ──space/enter on toggle──▶ rows (Update{AutoConnectHosts: &flipped})
rows ──esc/q/,──▶ returnTo
```

In every save: store nil or `fileUnreadable` → session only, `notSaved[key] = true`, status says so (restart
wording only for `fileUnreadable`); `SettingsUnreadableError` → session only, `lastSaveFailed` set, `notSaved[key]
= true`, status "fix the file" with no restart; other error → toggle/list: session only + `notSaved` + error
status; input: stays open with the error, session unchanged (FR-528); success → `notSaved[key] = false`,
`lastSaveFailed = nil`, `saved settings`.

## 4. Open request — `editor.Request` → `editor.Plan` | `editor.Refusal`

```go
type Request struct {
    Editor        Editor
    Bins          map[string]string // raw override values by editor ID
    Custom        string            // session custom command (raw)
    SSHTarget     string            // "" = local
    HostErr       error             // host lookup failed (FR-520) → refuse for every editor
    WorkspacePath string            // "" before the daemon reply (Precheck), set for Resolve
    Home          string            // from os.UserHomeDir; "" = unknown
}
type Plan struct {
    Argv       []string // argv[0] is the executable as resolved (override, default, or the custom first word)
    Executable string   // for messages (FR-532); the raw executable, cut by the caller
    RemoteNoHost bool   // Custom on a remote sandbox with no {host}: completion warns (FR-515)
    EnvVar     string   // where to fix it for a built-in editor; "" for Custom (settings screen)
}
type Refusal struct {
    Rule   string // one of the rule names in contracts/editor-launch.md §4
    Detail string // safe detail only: the rejected character as 'x' / U+XXXX; never command text
    Where  string // "settings screen" | env var name | "the daemon or the host entry" | ""
}
```

Pipeline: `Precheck(req)` (no path) → `Resolve(req)` (with path) → `Launch(plan.Argv)`. `Precheck` resolves the
executable too, so the caller can `LookPath` it. Custom commands pass through `Split` → `Check` → `Expand(home)`
→ `Substitute(path, host)` in that order (FR-531).

## 5. Launch result — `editor.Started` / `editor.ExitReport`

| Field    | Type                | Notes |
|----------|---------------------|-------|
| `Report` | `<-chan ExitReport` | At most one value: a non-zero exit or signal within `exitGrace` (3 s). Closed otherwise. |
| `Status` | `int`               | Exit status, or -1 for a signal. |
| `Signal` | `string`            | Signal name when killed. |

## 6. Status-line state on `Model` — `ui/status.go`

| Field        | Type                                     | Meaning |
|--------------|------------------------------------------|---------|
| `vFail`      | `struct{ text string; at time.Time }`    | Last `v` failure/refusal and arrival time; error style while `status == text`. |
| `vHold`      | `struct{ text string; seq uint64 }`      | Held message; pending in-place attach number (0 = none). |
| `confirm.holdsV` | `bool` on `confirmState`             | This confirm is a Replace dialog for an in-place attach. `m.vHoldDialog()` = `m.screen == screenConfirm && m.confirm.holdsV`, derived so that no way of closing the dialog can leave it stale. |
| `termFail`   | `string`                                 | Last attach/terminal error text (joined on release); cleared by `attachTerminal`. |
| `sizeKey`    | `sizeKey`                                | `{status, width, height, banner, surface, tabs, markFree}` last checked. |
| per form     | `lastHeight int`                         | Last `WithHeight` applied (huh has no getter); stored beside each form pointer. |

Constants: `statusMaxLines = 6`, `terminalStatusLines = 2`, `outsideTextCols = 120`, `osReasonCols = 40`,
`valueCols = 60` (`cutMiddle` keeps 20 + 39), `vHoldRecent = 3 * time.Second`.

### Hold state machine

```
none ──v failure/refusal arrives──▶ showing(vFail.at = now)
showing ──in-place attach starts or Replace dialog opens, age < 3 s──▶ held(seq | dialog)
none/showing ──failure arrives while terminal open/opening or dialog open──▶ held (replaces any held text)
held(dialog) ──decline, or the dialog dropped unanswered (closeOrchAdminOnDrop)──▶ release
held(dialog) ──accept (onAccept: seq = termSeq+1)──▶ held(seq)
held(seq) ──termOpened/termClosed with that seq, terminal not showing──▶ release
held(seq) ──terminal closes (ctrl+q / closed)──▶ release
release: status = (termFail showing ? cut120(termFail) + "; " : "") + text; vFail = {text, zero time}; vHold = {}
```

A message with a zero `vFail.at` is never held again.

## 7. Messages (Bubble Tea)

| Message            | Fields                                                   | Handler effect |
|--------------------|----------------------------------------------------------|----------------|
| `editorOutcomeMsg` | `sandboxID, text string; failed bool; wait <-chan editor.ExitReport; exe, where string` | `delete(busy, id)` and `refreshListItems()` (rebuilds the row, not a reload); status (+ `vFail` when failed, held per §6; `m.err = nil` when not failed); when `wait != nil` returns `waitExitCmd(wait, id, exe, where, vNotices())`. No reload. `exe` is `Plan.Executable` already cut to 60 columns; `where` is the override variable or `the Custom command on the settings screen (,)`. |
| `editorExitMsg`    | `sandboxID, text string`                                 | Status + `vFail` only (FR-523): no `busy`, no reload. |

Both carry already-assembled, sanitised text, so the message is self-contained. The outcome's text is built
in the `v` command; the exit report's text (executable, status or signal, where to fix it) is built by
`waitExitCmd`, which takes the executable and the "where" from the outcome message and `vNotices()` from the
session when the outcome is handled (FR-529),
so a later settings change doesn't alter a report already in flight.
