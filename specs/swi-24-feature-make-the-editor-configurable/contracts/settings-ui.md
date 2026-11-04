# Contract: settings screen, status line and the hold (SWI-24)

TUI behaviour the tests pin. Settings screen: `ui/settings.go`, `ui/settings_command.go`. Status line, size
check and hold: `ui/status.go`, with edits in `app.go`, `terminal.go`, `orphan.go`, `formguard.go`.

## 1. Settings screen (`,`)

Layout (rows mode), every line wrapped at the terminal width (FR-534):

```
Settings
persisted by the TUI in settings.toml under the config dir

⚠ unknown editor 'foo' — using VS Code; other settings loaded                      (when it applies)
⚠ settings couldn't be read at startup, changes aren't saved — fix the file and restart (parse settings.toml: …)
⚠ the last save couldn't read settings.toml — fix the file; the next save retries (…)

> [on]  Automatic host sign-in at startup
        Try every saved remote host when the TUI starts; …
  Editor: WebStorm
        Which editor v opens the sandbox's workspace folder in
  Editor: Custom: /Users/developer/Lib…port/JetBrains/Toolbox/scripts/webstorm… (not saved)
```

Keys: `↑/↓`, `j/k` move; `space`/`enter` toggle a bool row or open the Editor list; `esc`, `q`, `,` return to the
screen it was opened from. Help: `space/enter toggle/select · ↑/↓ move · esc back`.

### Editor list (list mode)

Replaces everything below the header with:

```
Editor
  VS Code
  VS Code Insiders
● Cursor          ← current (marked), cursor starts here
  …
  Custom
```

`↑/↓`, `j/k` move (the window scrolls with `windowRange`, minimum 3 rows); `enter` saves the entry under the
cursor **always**, even when marked (writes `editor`; clears the unknown notice; returns to rows); `esc` returns to
rows with nothing saved; `space`, `q`, `,` do nothing. Help: `enter select · ↑/↓ move · esc back`. Choosing Custom
opens the input instead of saving.

### Command input (input mode)

Replaces everything below the header with:

```
Custom command
› kitty -e sh -c 'cd "$1" && nvim .' sh {path}            (scrolls sideways; Width = body - 4)

Custom command: line endings/tabs/control characters will be rewritten when saved   (only when cleaning changed it)
{path} is the workspace folder; {host} the SSH target (empty for a local sandbox). With no {path} the
folder is added last. No shell: a leading ~/ expands; $VAR, && and | aren't allowed; globs stay literal.
The command runs with no terminal, so wrap a terminal editor: kitty -e nvim {path}. A program that
re-parses its arguments (sh -c) can run the path as code when a marker sits inside its script — pass the
path as an argument instead: kitty -e sh -c 'cd "$1" && nvim .' sh {path}. A {host} inside a larger
argument only works on remote sandboxes.
```

The note is ≤ 10 lines at 80 columns. The rewrite notice is part of the body (2 lines, drawn only while
`inputRewrite` is set), not the status. The input view reads `bodyHeight()` as it renders, like Jobs: the room
for the note is `bodyHeight() - fixed lines` (title, input, blank, the notice when shown), never below 1,
so on a shorter terminal the note's last lines give way to `…` **before** the status is cut and the input stays
on screen. The list mode windows its entries the same way (`windowRange(len, cursor, bodyHeight() - 1)`,
minimum 3 rows). `enter` confirms (FR-528 flow in data-model §3); `esc` with a changed trimmed value opens the SWI-9
unsaved-changes dialog (Save = enter, Cancel = back to the input, Discard = the list); `esc` unchanged → the list.
Every printable key, including `q`, `,`, `j`, `k`, space, is text; `ctrl+e` end of line; `ctrl+c` as on the rename
input. `ctrl+v` and bracketed paste both insert text, and the cursor blinks (the input is focused and `forwardScreen`
sends it non-key messages). A resize resets `Width` and the visible window, keeping the cursor, so a long command never
runs off the edge. A rejection shows in the status line only (red), the input stays open; leaving by Discard or a clean
`esc` clears it unless something has replaced it. Help: `enter save · esc back`.

## 2. Status line (`footer`) — FR-533

- The status is sanitised, then wrapped at the terminal width with `ansi.Wrap` (at spaces where it can; a token
  longer than the width is broken, so a 120-column daemon error never leaves the screen), then cut to its line
  limit with `…` on the last line. Lines come out of the body (`bodyHeight` shrinks by `lines - 1`).
- Limit, computed before anything renders: `min(6, room)` where `room = height - 2 (header) - 1 (blank) - help
  lines - smallest usable body`, never below 1. Smallest usable body: sandbox list `9 + 2 (+1 banner)`; a
  resizable list or form `3`; a windowed view that reads `bodyHeight` as it renders (Jobs, the lanes list, the
  settings screen's list and input modes) its fixed lines plus 3 (the input: plus 1 note line; the lanes list: its
  fixed lines, the 8 it reserves and 6 for three two-line entries); a static view
  (Daemons, the settings rows, Hosts, Groups) `0`. `chrome` then measures
  the room the rendered body really left and cuts the status to `min(limit, room)`, so a static view keeps its
  header. The terminal screen's limit is 2 and its PTY size is `height - 8` (floor 3), independent of the status.
- Every `v` message defined in `editor-launch.md` §4, with outside text at its limits, fits in full at 80×24 on
  the sandbox list with the update banner showing (≤ 4 lines at 80 columns). A joined message (§3) and a view that
  can't shrink are outside this promise and are cut.
- A status that's empty or fits on one line computes no limit and renders the help once per frame, in `chrome`.
  "Fits" is measured on the text that gets wrapped, with newlines already turned into spaces.
- Error style: `m.err != nil`, a status starting with `error`, or `status == vFail.text`. A `v` completion clears
  `m.err`, as `statusMsg` does today, so it's never red.

### Size check (Update wrapper, after the release, skipped on the terminal screen)

Key: `{status, width, height, banner, activeSurface, len(tabs), listHelpEntries}` plus the enlarge hint, where
`listHelpEntries` is the list help's conditional per-row entries (mark free, pause). On change, the active surface is
resized only if its height differs from its target: `m.list` → `mainListHeight()`; an open item list →
`bodyHeight()`; an open huh form → `WithHeight(bodyHeight())` when it differs from the stored `lastHeight`, then
`form.Update(nil)` in the same pass (its command kept). Tests change each key field and assert the size.

## 3. Hold — FR-533 (terminal screen and in-place attaches)

| Event | Effect |
|-------|--------|
| `v` failure/refusal arrives, no attach pending, terminal not showing | `status = text`, `vFail = {text, now}`, red |
| `v` failure/refusal arrives while an in-place attach is pending (`vHold.seq != 0`), a Replace dialog for one is open, or the terminal screen shows | `vHold.text = text` (replacing any held); also drawn (cut to 2 lines) on the terminal screen. Past the limit (`now - vHold.since >= 5 s`), off the terminal screen: `status = text`, `vHold.text = ""` |
| `v` completion arrives | `vHold.text = ""`, `vFail = {}`; the attach, if any, stays pending |
| in-place attach starts (`attachTerminal`, not `T`) | `vHold.seq = termSeq`; `vHold.since = now`; `termFail = ""`; **recency rule**: if `status == vFail.text` and `now - vFail.at < 3 s` and `vHold.text == ""` → `vHold.text = status` |
| a pending attach reaches the limit (`now - vHold.since = 5 s`; a timer set when a message is held out of sight, carrying the attach's `seq`) with `msg.seq == vHold.seq` and `vHold.text != ""` | `status = vHold.text`, or `cut120(status) + "; " + vHold.text` when the status is an error (`statusIsErr()`) or `termFail`, and isn't `opening terminal…`, `replacing the detached agent…` or the held text; red; `vFail = {status, now}`; `vHold.text = ""`, so it's no longer held; `settingsView.inputError` follows a status it matched. A superseded attach's timer does nothing, nor does a second timer for an attach whose message has been shown |
| Replace dialog opens for an in-place attach (`confirmReplaceOrphan`, `!external`) | `termFail = ""`; `confirmState.holdsV = true`, so `m.vHoldDialog()` holds for as long as it's the showing confirm; a message the recency rule would hold stays on the status line, not held; `onAccept` sets `vHold.seq = termSeq + 1` and `vHold.since = now`, and `vHold.text` to that message if nothing is held and it's still the status; a decline, or the dialog dropped unanswered when its daemon disconnects, needs nothing more |
| `termOpenedMsg` / `termClosedMsg` with `msg.seq == vHold.seq` | `vHold.seq = 0` (a superseded attach's message never clears it); then the recency rule, because both replace the status |
| terminal closes (`ctrl+q`, session ends) | the screen leaves `screenTerminal` |
| attach/terminal error set (`not opened — you moved on`, `external terminal already attached`, `terminal: …`), or a daemon drop closes the orchestrator screens (`<name> disconnected — orchestrators closed`) | `termFail = status` |
| **release** (wrapper: `vHold.text != ""`, `seq == 0`, `!m.vHoldDialog()`, screen ≠ terminal) | `status = (status != "" && status == termFail ? cut120(status) + "; " : "") + vHold.text`; `vFail = {status, zero}`; `vHold = {}`; red |
| a `v` failure arrives when an error is already showing and nothing is held | replaces the error (not joined) |
| the Custom command input is left while its error (`settingsView.inputError`) is the status | `status = ""`; nothing comes back in its place |

A message is held out of sight or showing, not both; the terminal screen, which draws it cut to 2 lines until it closes,
is the one exception. A released message (zero `vFail.at`) is never held again. `detached from <name>` is replaced, not joined. A daemon
drop's `<name> disconnected — orchestrators closed` is joined, because it's the only message explaining why the screen
changed. That covers a failure held when the drop arrives; one arriving after the drop finds nothing held and replaces
the message (last row above). An error or drop message already showing when the attach starts or the dialog opens has
nothing to do with the held failure, so it's replaced, not joined.

## 4. Settings notices on `v` (FR-529)

Every `v` outcome (completion, refusal, failure, exit report) ends with ` (<notices>)` when any apply, in the
order: `unknown editor '<value>' in settings`, `settings couldn't be read at startup, changes aren't saved`, then
for a completion only `the path is on the remote host`. Separator `; `. A mid-session broken file adds nothing.
