# Feature Specification: Configurable Editor for the Open-in-Editor Key

**Feature Branch**: `jamesloganclark/swi-24-feature-make-the-editor-configurable`

**Created**: 2026-11-01

**Status**: Draft

**Input**: Linear SWI-24 "Feature - make the editor configurable": "Currently users can only open VScode
with `v` in the TUI. We want to add a selection of all popular IDEs including WebStore. Include optional
env variables to allow users to specify the cli start command for each (in the same way we already have
one for vscode which defaults to `code`). In the settings page there should be an option to switch between
which IDE the `v` key opens (default to vscode). There should also be an option to manually specify a
custom terminal command when opening an editor"

**Builds on**: `specs/001-sandbox-session-manager` (FR-027: every sandbox is openable in VS Code, including
sandboxes on remote hosts), `specs/008-mcp-gateway-manager` (FR-101: the TUI settings screen) and
`specs/swi-9-feature-app-forms` (the unsaved-changes guard on inline inputs).
Requirements here are numbered FR-501 onward so they never collide with earlier features' numbers.

## Background *(context for reviewers)*

These facts describe how the TUI behaves today. They set the limits of this feature.

- `v` on a sandbox opens that sandbox's **controlled workspace folder** in VS Code. This folder is the
  copy of the seeded files kept on the daemon's host. It is not the running container.
- The editor always starts on the **developer's own machine**. For a sandbox on a **remote host**, VS Code
  opens the folder over SSH to that host (VS Code's Remote-SSH). For a local sandbox it opens the folder
  directly.
- The VS Code command can already be changed with an environment variable (`SWITCHBOARD_CODE_BIN`,
  default `code`). No other editor is supported.
- The settings screen (`,`) shows every setting the TUI saves, and the developer changes each one in
  place. Today it has one on/off setting: automatic host sign-in. Settings are saved per user on the
  client. The daemon is not involved.
- Editors handle remote folders differently. VS Code and its forks (Cursor, Windsurf, VS Code Insiders)
  take the same SSH option. Zed opens `ssh://` addresses. JetBrains IDEs (WebStorm, IntelliJ IDEA, and
  others) open remote projects through a separate gateway product and have no simple "open this remote
  folder" command. Sublime Text cannot open remote folders at all.

## Clarifications

### Session 2026-11-01

- Q: What does "manually specify a custom terminal command when opening an editor" mean? → A: A **Custom**
  entry in the editor list. The developer types a full command line with `{path}`/`{host}` markers, and
  it runs instead of a built-in editor (User Story 3, FR-506, FR-515, FR-516). No built-in terminal
  editors are added, and the popout-terminal setting is not reused.
- Q: What does `v` do on a remote-host sandbox when the selected editor can't open remote folders
  (JetBrains IDEs, Sublime Text)? → A: **Refuse with a message.** Nothing is started, and the status line
  says why and points to the settings screen (FR-514). No fallback to VS Code and no JetBrains Gateway.
- Q: Which built-in editors ship? → A: **The proposed list**: VS Code (default), VS Code Insiders,
  Cursor, Windsurf, Zed, WebStorm, IntelliJ IDEA, PyCharm, GoLand, Sublime Text, plus Custom (FR-501).

### PR review 2026-11-02 (re-review run `run-c275c8f2-1500`, resolved in pr-response run `run-c275c8f2-1507`)

Fixed before planning, as the review asked:

- **The command input is guarded by SWI-9.** `esc` on a changed input raises the unsaved-changes dialog, as
  on the rename input. Back-outs that drop no typed text raise nothing. (FR-528)
- **Saves merge instead of overwriting.** A save applies only the changed setting to the file as just
  re-read. Hand edits survive a save and reach the session at the next start. A session-only change isn't
  written by a later save. (FR-530, FR-525)
- **Notices travel inside the message for the action.** The status line holds one message, so the settings
  notices and the remote warning are part of each `v` outcome, on every `v`. The startup display is best
  effort, and the sign-in summary may replace it. (FR-529)

Settled in the spec:

- **The remote path limit also covers the selected editor's set override variable**, including
  `SWITCHBOARD_CODE_BIN`, because a wrapper script can re-parse its arguments. Default commands aren't limited.
  (FR-517, FR-524)
- **The `-` check also runs on the normalised SSH target**, so `ssh://-oX` is refused. (FR-513, FR-517)
- **Letters and digits in the path limit are Unicode**, with combining marks. `~`, `(` and `#` stay refused,
  and the refusal names the character. SC-005's promise is limited to paths FR-517 accepts. (FR-517, SC-005)
- **The documented `sh -c` form passes the path as a positional argument**, so the shell never parses it.
  Quoting the marker in the script is no longer advised. (FR-521, FR-524)
- **Only the executable is displayed outside the command input.** This covers status messages and the Editor
  row. The README warns that arguments are visible in the process list. (FR-532, FR-504, FR-524)
- **The temporary file is created fresh with a unique name**, so a leftover can't widen the final mode.
  (FR-527)
- **Shell syntax is rejected, not passed through.** That means unquoted `|`, `;`, `&`, `<`, `>`, backticks,
  and a first argument starting with `NAME=`. Globs and other characters stay literal. (FR-506)
- **The parser rules are defined.** Backslashes follow POSIX, an empty first argument is rejected, the raw
  text is saved, `~` expands at launch, and `v` refuses when there's no home directory. (FR-531, FR-506)
- **The late failure report names the sandbox, the executable, the status and where to fix it.** stderr stays
  discarded. It doesn't touch other rows' spinners and can't be overwritten by its own completion message.
  (FR-523)
- **The docs recommend the settings screen for commands with quotes**, and give the `'''` form for hand edits.
  (FR-524)
- **A command that can't be found is refused before the daemon call.** The launch error stays the final
  authority. (FR-526)
- **`enter` in the Editor list always saves**, even on the marked entry, and clears the unknown-editor
  notice. `esc` leaves the saved value in place. (FR-505, FR-508)

### PR review 2026-11-02 (re-review run `run-c275c8f2-1516`, resolved in pr-response run `run-c275c8f2-1523`)

Fixed before planning, as the review asked:

- **The documented `sh -c` form now has a terminal.** It's `kitty -e sh -c 'cd "$1" && nvim .' sh {path}`, and the
  note beside the input says a terminal editor needs a terminal emulator. (FR-521, FR-524, FR-511)
- **A command that fails FR-506 is never displayed.** The Editor row shows `Custom (invalid command)`, and a
  rejection or refusal names only the rule it broke. A first argument starting with `NAME=` is now rejected
  whether or not it's quoted, so it can't reach the row either. (FR-532, FR-506, FR-522)
- **One message can carry two notices.** Custom on a remote sandbox with an unreadable file needs both, so a
  message carries every notice that applies, settings notices first. The unreadable-file notice now says
  `settings couldn't be read at startup, changes aren't saved`, which stays true whichever editor is in use.
  (FR-529, FR-525)
- **The status line wraps instead of being cut off.** Lines it adds come out of the body. Long values are cut to
  60 columns and a message to 6 lines, and every message in this spec fits at 80 columns. (FR-533, SC-004)

Settled in the spec:

- **A file that breaks mid-session is its own state.** The message says to fix the file, with no restart, and the
  notice clears at the next successful save. It stays on the settings screen and isn't added to `v`, because `v`
  doesn't read the file and could only repeat a stale claim. (FR-525, FR-529)
- **Session-only changes are marked.** A row that wasn't written shows `(not saved)` until a save writes that row.
  A later save still doesn't write it, because a hand fix may have changed that key. (FR-530, FR-504, FR-509)
- **A failed write keeps the command input open**, as SWI-9 FR-017 requires. FR-525's refusal is the stated
  exception, and so is having no store. Success returns to the settings row. (FR-528, FR-505)
- **The input's cleaning is part of the spec.** Tabs and newlines become spaces and other control characters are
  dropped. FR-506's control-character check applies to saved text, and the starting value is the cleaned text. A
  saved command that opening would change shows SWI-9's rewrite notice. (FR-528, FR-506)
- **The remote path limit depends on the selected editor's variable**, as FR-502 defines set. Another editor's
  variable, or an empty one, doesn't trigger it. (FR-517, SC-002, SC-005)
- **FR-526's filesystem checks run off the UI thread.** This is a note for planning.

### PR review 2026-11-03 (re-review run `run-c275c8f2-1530`, resolved in pr-response run `run-c275c8f2-1535`)

Fixed before planning, as the review asked:

- **The embedded terminal screen is an exception to FR-533.** The PTY's size never depends on the status line, so
  a status change can't resize it or signal the agent. The status there is cut at the 2 lines that fit, so the
  header and its `ctrl+q` hint stay. A message that arrives while the terminal is open can lose its end, which is
  accepted, apart from a `v` failure or refusal since later reviews. (FR-533, SC-008)

Settled in the spec:

- **FR-533 now states a height.** A message is cut to what's left after the header, help footer and the screen's
  smallest usable body, and everything this spec defines fits in full at 80×24, on a screen whose body can shrink
  since a later review. A terminal too short for even a one-line message loses its top lines, as it does today.
  (FR-533, SC-004)
- **Body resizing has one choke point.** The `Update` wrapper checks the active surface after each message and resizes
  it. The next review changed what it compares. This is a note for planning.
- **The settings screen wraps too.** Rows, notices and the note wrap, so `(not saved)` and "fix the file" can't be
  cut. The list and the command input replace the rows while open, so the note fits at 80×24. If the screen is too
  short, the note gives way first. (FR-534, SC-008)
- **`(not saved)` describes what the row shows.** Confirming a built-in editor clears the mark, because the row now
  shows a saved value. A session-only custom command stays in the session to fill the input again, and it's lost at
  the next start unless it's confirmed. That's accepted, because the developer has moved away from it. (FR-530)
- **A toggle takes two presses to keep its value.** Pressing it flips the value first, as it always has, so the
  first press saves the opposite value and the second saves the original. (FR-530)
- **FR-526's no-blocking rule covers only the executable search.** The host lookup and the settings re-read and
  write stay on the update loop, because this feature doesn't change the kind of work they do. (FR-526)

### PR review 2026-11-03 (re-review run `run-c275c8f2-1542`, resolved in pr-response run `run-c275c8f2-1544`)

The review asked for the first two to be settled first, because they decide how FR-533 gets built:

- **The body is resized by comparing sizes, not line counts.** After each message the active surface's height is
  compared with its target. That also covers a surface the developer returns to, a banner that arrives later, and a
  help footer that changes height. (FR-533, SC-004, Notes for Planning)
- **`chrome` has no bottom-drop fallback.** It could hide the row `v` acts on, and it contradicted FR-533. A view still
  taller than the terminal loses its top lines, as it does today. (FR-533)

Settled in the spec:

- **Text from outside the spec is cut before the notices are added.** In a `v` outcome message it's cut to 120 columns,
  and FR-518's OS reason to 40, so a long error can't push out a notice or where to fix it. FR-520's lookup error is cut
  to 78, in the middle, since later reviews. (FR-533, FR-518, SC-004)
- **A `v` failure on the terminal screen is held, not lost.** One that arrives while a terminal is open or opening is
  shown in full when the terminal closes, in place of `detached from …`. Pressing `v` again is no longer the way to get
  it back, because that repeats the launch. Other messages aren't held. The next review added refusals. (FR-533, SC-008)
- **An executable is cut in the middle.** It keeps its first 20 and last 39 columns, so its name stays readable. An
  unknown editor value is still cut at its end. A trailing `…` on the Editor row now only means there are more
  arguments. (FR-533, FR-534)
- **The list and the input replace everything below the header.** One title line is left, so the 23-line budget at
  80×24 holds. (FR-534)

### PR review 2026-11-03 (re-review run `run-c275c8f2-1548`, resolved in pr-response run `run-c275c8f2-1552`)

The review asked for the first two to be settled first, because they change what FR-533 requires:

- **The hold covers refusals too.** FR-517's path checks run after the daemon replies, so a refusal can arrive after
  `t` is pressed, and it would be wiped when the terminal opens. The hold now covers every `v` outcome except a
  completion. (FR-533, SC-008)
- **The hold lasts through the Replace dialog.** An attach that's refused because of a detached agent opens the dialog
  and can still lead to another attach, which would clear the status. So the hold lasts until the dialog is declined or
  the attach it starts has ended. A failure that arrives while that dialog is open is held too. (FR-533, SC-008)

Settled in the spec:

- **A superseded attach can't end the hold, and a held failure keeps its error styling.** The marker holds the pending
  attach's number. The notes also set the wrapper's order. (FR-533, SC-008, Notes for Planning)
- **Text is sanitised before it's cut or wrapped.** A tab has no width until sanitising expands it, so cutting first
  breaks the limits. The middle cut starts from sanitised text. (FR-533, SC-004)
- **The wrap cache is gone, and the size check runs only when something it reads has changed.** The target height is the
  expensive step, not the wrap, and a cached cut would go stale. Forms store the last height applied, because huh has no
  height getter. This is a note for planning.
- **The 80×24 promise covers screens whose body can shrink.** A view that can't, such as Daemons with many rows, cuts the
  message to fit, and it isn't given scrolling for this feature. The whole message stays in the status, so it shows in
  full on a screen with room unless something has replaced it. (FR-533)
- **`chrome` doesn't drop lines from the bottom of the body**, but that's no promise the selected row stays visible. On a
  very short terminal it can scroll off the top, as it does today. FR-534's rule for its own note still stands. (FR-533)

### PR review 2026-11-03 (re-review run `run-c275c8f2-1555`, resolved in pr-response run `run-c275c8f2-1560`)

The review asked for the first one to be settled first, because it changes what FR-533 requires:

- **The hold also covers a recent `v` failure or refusal that's already showing.** Most refusals are immediate, so `v`
  then `t` would otherwise lose them when the attach replaces the status, while a slower refusal would be held. One that
  arrived in the last 3 seconds is now held, and an older one isn't, so an old failure can't come back after a long
  session. The 3 seconds are checked when the attach starts or its Replace dialog opens, because the dialog can stay open
  for longer than that. (FR-533, SC-008)

Settled in the spec:

- **`v` failures and refusals have a defined style.** Each one shows in the error style whenever it's shown, and it can't
  change the style of a later message. It's kept in a field of its own rather than `m.err`, which would stay set and turn
  later messages red. (FR-529, SC-008, Notes for Planning)
- **A huh field shrunk during a long message doesn't grow back, and that's accepted until the form closes.** Rebuilding
  an open form would risk the developer's input. A shrink can also leave the focused field undrawn until the form handles
  a message, so a no-op message follows each `WithHeight`. This settles the open item from the last review, and it's a
  note for planning.

### PR review 2026-11-03 (re-review run `run-c275c8f2-1567`, resolved in pr-response run `run-c275c8f2-1571`)

The review asked for this to be settled before planning, because it changes what FR-533 requires and what SC-008 tests:

- **A released `v` message no longer hides a terminal error.** `v` refused and then `t` is a common path, and if the
  attach failed, the only thing on screen was a message about the editor, which read as the reason `t` failed. An
  attach or terminal error and the held message now show together, with the error first. Letting the terminal error win
  was the other option. It was rejected because a held FR-518 or FR-523 failure only comes back by launching the editor
  again. `detached from <name>` is still replaced, because nothing is lost. (FR-533, SC-008, Notes for Planning)
- **A released message can't be held again.** It used to stay recent for the rest of its 3 seconds, so a quick retry of
  `t` would have hidden the same failed attach a second time. Once released, it's an ordinary message that the next
  attach replaces. (FR-533, SC-008)
- **A joined message is outside the 80×24 promise.** The two parts together can be longer than the room on the sandbox
  list, so the end of the held message can be cut with `…`. The error comes first, so it's never what's lost, and the
  status keeps the whole message. (FR-533, SC-004, SC-008)

### PR review 2026-11-03 (re-review run `run-c275c8f2-1575`, resolved in pr-response run `run-c275c8f2-1578`)

The review offered two options for this, and the first was taken. It changes no FR or SC:

- **A `v` outcome arriving after the error isn't joined.** The join runs only when a hold ends, so a `v` failure or
  refusal that arrives once an attach or terminal error is showing replaces the error, as FR-523 already says. That's
  accepted, because recovery is one key press and the report names the editor, so it's less likely to be read as the
  reason `t` failed. Joining on arrival was the other option. It was rejected because the error would need its own
  arrival time and 3-second limit, which adds more state to the hold. (Edge Cases)

### PR review 2026-11-03 (plan re-review run `run-c275c8f2-1616`, resolved in pr-response run `run-c275c8f2-1618`)

Found while re-reviewing the plan, and changed here because it changes what the spec says:

- **FR-520's lookup error is cut in the middle.** These errors put the path first and the reason last, so at 78 columns
  an end cut can remove `permission denied` or the name of the bad field. It keeps the first 38 and last 39 columns, so
  the 78-column limit and the 4-line fit still hold. A daemon error is still cut at its end. (FR-533, SC-004)

### PR review 2026-11-03 (plan re-review run `run-c275c8f2-1611`, resolved in pr-response run `run-c275c8f2-1613`)

Found while re-reviewing the plan, and changed here because they change what the spec says:

- **An older error isn't joined to a held failure.** `t` can open a Replace dialog with no attach, so an old
  `disconnected` or `terminal: …` message can still be showing. It has nothing to do with a failure held behind that
  dialog, so the failure replaces it. A daemon drop while the dialog is open still joins. (FR-533, SC-008)
- **FR-520's lookup error is cut to 78 columns, not 120.** With the unknown-editor notice, a longer error can reach
  5 lines, depending on where its spaces fall, and the end of the notice is then lost on the sandbox list. At 78,
  `(<err>)` fits on one line, so the message stays at 4. The next review moved the cut to the middle. (FR-533, SC-004)
- **SC-008 names the daemon drop.** Its sentence about what shows in one message now covers it, matching FR-533.
  (SC-008)

### PR review 2026-11-03 (plan re-review run `run-c275c8f2-1605`, resolved in pr-response run `run-c275c8f2-1608`)

Found while re-reviewing the plan, and changed here because they change what the spec says:

- **A held failure joins a daemon drop's message.** When a daemon drop closes a Replace dialog, `<name> disconnected —
  orchestrators closed` is the only message explaining why the screen changed, so a failure held behind the dialog joins
  it instead of replacing it. (FR-533, SC-008)
- **SC-002 and SC-005 use FR-517's meaning of an override.** The remote path limit needs the variable set to something
  other than its default, so those two now say so. (FR-517, SC-002, SC-005)

### PR review 2026-11-03 (plan review run `run-c275c8f2-1599`, resolved in pr-response run `run-c275c8f2-1602`)

Found while reviewing the plan, and changed here because they change what the spec says:

- **A variable set to its default isn't an override.** Sourcing `.env.example` as it stands exports every variable at its
  default. The command is the same either way, so the remote path limit stays off, and SC-002 holds for a developer who
  did that. (FR-517, SC-002)
- **A `v` completion clears `m.err`.** It no longer goes through `statusMsg`, which clears it today, so the plan has to
  do it, and the note saying an `errMsg` stays set now says so. (FR-529, SC-002)

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Pick which editor `v` opens (Priority: P1)

A developer uses WebStorm (or Cursor, Zed, IntelliJ IDEA, …) rather than VS Code. They open the settings
screen, pick their editor from a list of popular editors, and from then on `v` on any sandbox opens that
sandbox's workspace folder in the chosen editor.

**Why this priority**: This is the core of the ticket. Without it, developers who don't use VS Code cannot
use `v` at all.

**Independent Test**: On the settings screen, choose WebStorm. Press `v` on a local sandbox and check that
WebStorm opens the sandbox's workspace folder. Restart the TUI and check that WebStorm is still selected.

**Acceptance Scenarios**:

1. **Given** a fresh install with no saved settings, **When** the developer opens the settings screen,
   **Then** the editor setting shows **VS Code**, and `v` opens the sandbox folder in VS Code exactly as
   before this feature.
2. **Given** the settings screen is open, **When** the developer selects the editor setting, **Then** they
   see every supported editor by display name, with the current one marked, and they can move through the
   list and confirm one.
3. **Given** the developer confirmed WebStorm, **When** they press `v` on a local sandbox, **Then** WebStorm
   opens that sandbox's workspace folder, and the TUI stays responsive while the editor starts.
4. **Given** the developer chose an editor and quit, **When** they start the TUI again, **Then** the same
   editor is still selected, and `v` uses it.
5. **Given** an editor other than VS Code is selected, **When** the developer looks at the sandbox list's
   help footer, **Then** the `v` entry names the selected editor (for example "webstorm") instead of
   always saying "vscode".

---

### User Story 2 - Point an editor at a different command (Priority: P2)

A developer's editor command is not the usual one. Maybe WebStorm was installed by JetBrains Toolbox
under a different name, the editor is not on the PATH, or they use a wrapper script. They set an optional
environment variable for that editor, and the TUI starts that command when that editor is selected. This
works the same way `SWITCHBOARD_CODE_BIN` works for VS Code today.

**Why this priority**: Without it, the editor choice fails for anyone whose install doesn't match the
default command name. That is common with JetBrains IDEs. It builds on Story 1.

**Independent Test**: Set the WebStorm command variable to the full path of a stand-in script that logs its
arguments. Select WebStorm and press `v`. Check that the script ran with the sandbox's folder.

**Acceptance Scenarios**:

1. **Given** no editor command variables are set, **When** an editor is opened, **Then** its documented
   default command is used (for example `code` for VS Code, `webstorm` for WebStorm).
2. **Given** the variable for the selected editor is set to a path or command name, **When** `v` is
   pressed, **Then** that one executable is started instead of the default (FR-502).
3. **Given** `SWITCHBOARD_CODE_BIN` is already set from before this feature, **When** VS Code is the
   selected editor, **Then** it is still honoured with no change to the variable's name or meaning.
4. **Given** the developer asks for the client's list of environment variables (the existing env check
   and docs), **When** they read it, **Then** every editor's command variable is listed with its default
   and the editor it belongs to.

---

### User Story 3 - Use a custom editor command (Priority: P3)

A developer's editor is not in the list (for example a terminal editor such as Neovim opened in a new
terminal window, or a niche IDE). On the settings screen they choose **Custom** and type the full command to run.
They mark where the sandbox's folder goes in the command. `v` then runs that command.

**Why this priority**: This is the escape hatch that covers every editor not on the list. Few developers
will need it, so it comes after the built-in editors.

**Independent Test**: Choose Custom with a command such as `kitty -e nvim {path}`. Press `v` on a local
sandbox and check that the command ran with the sandbox's folder in place of the marker.

**Acceptance Scenarios**:

1. **Given** the developer selects **Custom** in the editor list, **When** they confirm, **Then** they are
   asked for a command, with the current custom command (if any) filled in, and a short note beside the
   input gives the rules in FR-521.
2. **Given** a custom command containing the folder marker, **When** `v` is pressed, **Then** the command
   runs with the marker replaced by the sandbox's workspace folder, as one argument even if the folder
   path contains spaces.
3. **Given** a custom command with no folder marker, **When** `v` is pressed, **Then** the folder is added
   as the last argument.
4. **Given** the developer submits an empty custom command, **When** they confirm, **Then** the setting is
   not saved, and they are told a command is required. The previous editor choice stays in force.
5. **Given** Custom is selected, **When** the settings screen is shown, **Then** the editor row shows
   "Custom" and the command's executable, without its other arguments (FR-532).
6. **Given** the command input holds typed changes, **When** the developer presses `esc`, **Then** the
   unsaved-changes dialog appears. Save acts like `enter`, and Discard returns to the editor list with
   nothing saved (FR-528).
7. **Given** the developer submits `cd {path} && nvim .`, **When** they confirm, **Then** it is rejected
   with a message saying the command doesn't run in a shell and that `sh -c` or a wrapper script is the way
   to do this, with the note beside the input giving the form to use (FR-506, FR-521).
8. **Given** a hand-edited command `API_TOKEN=abc123 myeditor {path}`, **When** the settings screen is shown
   or `v` is pressed, **Then** the Editor row shows `Custom (invalid command)`, `v` refuses, and no part of the
   command appears in either (FR-532, FR-522).

---

### User Story 4 - Open a remote sandbox in the chosen editor (Priority: P2)

A developer works with sandboxes on a remote daemon host. With an editor that can open remote folders
selected, `v` opens the sandbox's folder on that host the way that editor does it. With an editor that
cannot open remote folders, `v` tells them so and does nothing else.

**Why this priority**: FR-027 promises that remote sandboxes are openable today, so this feature must not
quietly break remote use when someone picks a different editor.

**Independent Test**: With a remote host connected, select Cursor and press `v` on a remote sandbox. Check
that Cursor is asked to open the folder over SSH to that host. Select WebStorm and press `v` on the same
sandbox. Check that a clear message appears and nothing is started.

**Acceptance Scenarios**:

1. **Given** VS Code, VS Code Insiders, Cursor or Windsurf is selected, **When** `v` is pressed on a remote
   sandbox, **Then** the editor opens the folder over SSH to the sandbox's host, exactly as VS Code does
   today.
2. **Given** Zed is selected, **When** `v` is pressed on a remote sandbox, **Then** Zed is asked to open
   the folder at its SSH address on the sandbox's host (`ssh://<target><absolute-path>`). This holds
   when the path contains spaces too.
3. **Given** an editor with no remote-folder support is selected (the JetBrains IDEs, Sublime Text),
   **When** `v` is pressed on a remote sandbox, **Then** no editor is started, and the status line says
   that this editor can't open folders on remote hosts and names the settings screen as where to switch
   editors.
4. **Given** Custom is selected, **When** `v` is pressed on a remote sandbox, **Then** the custom command
   gets the folder path and the host's SSH target through their markers, and it runs on the developer's
   machine. A custom command with no host marker gets the folder path only, and the completion message
   warns that the path is on the remote host (FR-529).

---

### Edge Cases

- **Command not found.** The selected editor's command (default or from its variable) is not installed or
  not on the PATH. `v` reports an error in the status line that names the executable that could not
  be started and the environment variable that sets it (for Custom, the settings screen instead). The TUI
  does not crash and does not hang.
- **Unknown saved editor.** The settings file names an editor this version doesn't know (for example a
  file written by a newer version, or edited by hand). The TUI uses VS Code for this session and shows a
  notice of its own: "unknown editor '<value>' — using VS Code; other settings loaded". Every other saved
  setting loads as usual. The unknown value stays in the file until the developer confirms an entry in the
  **Editor** list, and that includes confirming VS Code (FR-505). Toggling another setting leaves it in the
  file (FR-530).
- **Downgrade.** An older TUI that saves the settings file drops the editor entries it doesn't know.
  This loss is accepted.
- **Older settings file.** A settings file written before this feature has no editor entry. The editor
  defaults to VS Code, and automatic host sign-in keeps its saved value.
- **Custom selected but the saved command is missing or invalid.** Custom is selected but the saved command
  is empty, missing, or fails FR-506's checks (it can be hand-edited in the settings file). `v` refuses with
  the reason and points to the settings screen (FR-522). It does not fall back to another editor.
- **The settings file is hand-edited while the TUI runs.** The edit stays in the file when the TUI saves
  another setting, and it reaches `v` and the settings screen at the next start (FR-530).
- **Quoting in the custom command.** The custom command contains quoted arguments (for example
  `"/Applications/My Editor.app/bin/edit" {path}`). It is split into arguments the way a shell would
  split it, honouring quotes and backslashes (FR-531). The TUI does **not** run it through a shell, so the
  TUI itself never reads a folder path as shell syntax. A command that starts its own shell with the marker
  in its script (for example `sh -c '…{path}…'`) is the exception, and FR-516 and SC-005 cover it.
- **`~`, `$VAR` and shell syntax in the custom command.** There is no shell to interpret them. A leading
  `~/` is expanded at launch (FR-531). `$VAR`, backticks, unquoted `|`, `;`, `&`, `<`, `>` and a leading
  `NAME=value` are rejected with a message (FR-506). Globs such as `*.txt` are passed as literal text.
- **One Custom command for local and remote sandboxes.** This is a known limitation. There is one command
  for every host. A `{host}` embedded in a larger argument (`--remote ssh-remote+{host}`) or used as an
  option's value (`--host {host}`) produces a broken command on a local sandbox. Developers with both kinds
  of host need to edit the command when they switch, or use a wrapper script. The note beside the input
  (FR-521) and the docs (FR-524) say so.
- **Settings file can't be parsed.** The file has broken TOML or a wrong type (for example `editor = 5`).
  Defaults apply until the developer changes a setting for the session, and the file is never overwritten, so
  a saved editor and custom command aren't lost when another setting is toggled (FR-525). A file that breaks
  after startup has its own, shorter path: the file just needs fixing, with no restart (FR-525).
- **Empty variable.** An editor's command variable is set but empty or only spaces. That counts as unset,
  and the default command is used.
- **Settings store unavailable.** There's no settings store, or the file can't be written. A toggle or an
  Editor list entry applies to this session only, its row shows `(not saved)` (FR-530) and the status line
  says so, the same as the existing sign-in setting does. The command input is different: a failed write
  keeps it open with the error, and only having no store closes it with the command applied to the session
  (FR-528).
- **A session-only change after the file is fixed.** The developer toggles a setting while the file is broken,
  fixes the file, then saves something else. The save writes only that other setting, so the toggle's row keeps
  showing `(not saved)` and it warns that the value won't be there at the next start. Keeping the toggle's value
  takes two presses, because the first saves the opposite value (FR-530).
- **A session-only custom command, then a built-in editor.** The developer confirms Custom while the file is
  broken, fixes the file, then confirms Zed. Zed is saved and the row's mark clears. The command stays in the session
  and fills the input if Custom is chosen again, but it's gone at the next start unless it's confirmed (FR-530).
- **A message while the embedded terminal is open or opening.** For example, the developer presses `v`, then `t`, and a
  late failure report (FR-523) arrives. The status is cut at 2 lines, and the PTY isn't resized. Because it's a `v`
  failure, it's also held, and it's shown in full in place of `detached from …` when the terminal closes (FR-533). A `v`
  refusal is held the same way, such as FR-517's refusal of a path, which comes after the daemon replies. Other
  messages aren't held, so they can lose their end or be replaced.
- **A `v` failure while the attach ends in the Replace dialog.** The developer presses `v`, then `t`, a failure arrives,
  and the attach is refused because of a detached agent. The failure stays held behind the dialog. Declining shows it.
  Accepting starts the replacing attach, and the failure shows once that attach has ended and its terminal has closed.
  If the daemon behind the screen under the dialog disconnects, the dialog closes unanswered and the status shows
  `<name> disconnected — orchestrators closed; <failure>` (FR-533).
- **A failed `t` after a `v` refusal.** A remote sandbox has WebStorm selected. The developer presses `v`, which
  refuses, then `t`, and the attach fails because the daemon can't be reached. The status shows
  `terminal: <reason>; <the refusal>`, so the terminal's failure isn't hidden or blamed on the editor. An accepted
  Replace whose attach fails, an open terminal whose session dies, and an attach the developer moved on from all show
  the same way. If the developer presses `t` again straight away, the refusal isn't held again, so a second failure
  shows only its own error (FR-533). A `v` failure or refusal that arrives once one of these errors is already showing
  isn't joined, because nothing is held by then, so it replaces the error (FR-523). That's accepted, because pressing
  `t` again shows the error if the attach still fails, and a `v` message under 3 seconds old is held, so both show.
- **A screen with less room.** A view that can't shrink, such as Daemons with many rows, can leave fewer lines than a
  message needs, even at 80×24. For example, the developer presses `v` and moves to Daemons, and a late report arrives
  with 7 daemons listed. The message is cut with `…` to fit, and the whole of it shows on a screen with room, unless
  something has replaced it (FR-533).
- **A short terminal.** A message is cut to the room that's left, and never below 1 line. A terminal too short for
  even that loses its top lines, as it does today. At 80 columns the sandbox list needs 20 rows for a one-line
  message (FR-533).
- **The editor exits at once.** Some editor commands hand the work to an already running window and exit
  straight away. An exit with status 0 counts as success. A non-zero exit soon after starting counts as a
  failure (FR-523). The TUI does not wait for the editor to close.

## Requirements *(mandatory)*

### Functional Requirements

**Editor catalogue**

- **FR-501**: The TUI MUST support the following editors for the open-in-editor action, each with a
  display name, a default command, and an environment variable that overrides the command:

  | Identifier      | Editor            | Default command  | Override variable                 | Opens remote folders |
  |-----------------|-------------------|------------------|-----------------------------------|----------------------|
  | `vscode`        | VS Code (default) | `code`           | `SWITCHBOARD_CODE_BIN` (existing) | Yes (Remote-SSH)     |
  | `code-insiders` | VS Code Insiders  | `code-insiders`  | `SWITCHBOARD_CODE_INSIDERS_BIN`   | Yes (Remote-SSH)     |
  | `cursor`        | Cursor            | `cursor`         | `SWITCHBOARD_CURSOR_BIN`          | Yes (Remote-SSH)     |
  | `windsurf`      | Windsurf          | `windsurf`       | `SWITCHBOARD_WINDSURF_BIN`        | Yes (Remote-SSH)     |
  | `zed`           | Zed               | `zed`            | `SWITCHBOARD_ZED_BIN`             | Yes (`ssh://` address) |
  | `webstorm`      | WebStorm          | `webstorm`       | `SWITCHBOARD_WEBSTORM_BIN`        | No                   |
  | `idea`          | IntelliJ IDEA     | `idea`           | `SWITCHBOARD_IDEA_BIN`            | No                   |
  | `pycharm`       | PyCharm           | `pycharm`        | `SWITCHBOARD_PYCHARM_BIN`         | No                   |
  | `goland`        | GoLand            | `goland`         | `SWITCHBOARD_GOLAND_BIN`          | No                   |
  | `sublime`       | Sublime Text      | `subl`           | `SWITCHBOARD_SUBLIME_BIN`         | No                   |
  | `custom`        | Custom            | (developer-set)  | (set on the settings screen)      | Via `{host}` marker  |

  The identifier is what the settings file stores. Planning sets the exact settings keys (for example
  `editor` and `editor_custom_command`).

- **FR-502**: Every override variable MUST be optional. When it is unset, empty or only spaces, the
  default command MUST be used. `SWITCHBOARD_CODE_BIN` MUST keep its current name, default and meaning.
  A set value MUST be used as **one executable**, exactly as given. It is never split into words, because
  macOS paths contain spaces, and `~` and `$VAR` are not expanded, as with `SWITCHBOARD_CODE_BIN` today. A
  command with arguments (for example `flatpak run dev.zed.Zed`) needs a wrapper script or Custom.
- **FR-503**: Every override variable MUST appear in the client's declared configuration (the same list
  the env check and documentation read from), each with its default and the editor it belongs to.

**Choosing the editor**

- **FR-504**: The settings screen MUST have an **Editor** setting that shows the selected editor's display
  name. For Custom it shows `Custom` and the command's executable only (FR-532). It MUST default to VS Code
  when nothing is saved. It shows `(not saved)` when the choice applied to the session only (FR-530).
- **FR-505**: Activating the Editor setting MUST let the developer choose one entry from FR-501's list,
  with the current choice marked. `space` or `enter` activates the row, like the existing rows. In the list,
  `↑`/`↓` and `j`/`k` move, `enter` confirms, and `esc` cancels and leaves the setting unchanged. `space`,
  `q` and `,` do nothing in the list. `enter` MUST always save the chosen entry, even when it's already
  marked, so confirming VS Code over an unknown saved value replaces that value and clears its notice
  (FR-508). `esc` leaves the saved value in place, unknown or not. Choosing Custom saves only when the
  command input is confirmed (FR-506). Confirming a built-in editor returns to the settings row, and so does
  `esc`.
- **FR-506**: Choosing Custom MUST ask for a command line, with the session's custom command filled in when
  one exists. The text is split by FR-531's rules. The command MUST be rejected with a message that gives
  the reason, naming only the rule it broke and quoting no part of the command (FR-532), when it:
  - is empty, contains only spaces, has unbalanced quotes, ends in an unquoted backslash, contains a control
    character other than a tab (only saved text can, FR-528), or splits into no arguments;
  - has an empty first argument (`"" {path}`), because there would be no program to start;
  - has a `{path}` or `{host}` marker anywhere in its first argument (the executable), because that would
    run the folder path or SSH target as a program, or leave no program at all;
  - has a first argument that starts with `NAME=`, quoted or not (`FOO=1 myeditor {path}`), because nothing
    sets the variable and `FOO=1` would be run as the program. A program in such a folder is reached as
    `./FOO=1/edit`;
  - contains an unquoted or double-quoted `$` (`$VAR`, `${VAR}`) or backtick, or an unquoted `|`, `;`, `&`,
    `<` or `>`, because nothing interprets them. The message MUST repeat that the command doesn't run in a
    shell, and that `sh -c` or a wrapper script is the way to do this, using the form in the note beside the
    input (FR-521). Inside single quotes, or after a backslash that FR-531 honours, these are literal
    characters.

  Other characters a shell treats specially, including the globs `*`, `?` and `[`, are passed as literal
  text. The text MUST be saved as the input holds it, with only surrounding spaces trimmed, and nothing else
  rewritten after the input's own cleaning (FR-528). When a command is rejected, the input stays open with the
  reason and the previous editor choice MUST stay in force. FR-528 sets the keys while the input is open.
- **FR-507**: The chosen editor (and the custom command, if any) MUST be saved per user, client-side,
  through the same settings store and file as the existing settings. It MUST take effect for the next `v`
  press with no restart, and it MUST survive a restart.
- **FR-508**: A settings file with no editor entry MUST load as VS Code without changing any other saved
  setting. A settings file naming an unknown editor MUST load as VS Code for the session, MUST load every
  other setting as saved, and MUST show a notice naming the unknown value. The notice MUST be separate from
  the "settings could not be read" notice, and FR-529 sets where it appears. On the settings screen it MUST
  be derived from the session's editor value, so a save of another setting doesn't clear it, and it stays
  until the developer confirms an entry in the Editor list (FR-505). The saved editor value and custom
  command MUST NOT be changed by a save of any other setting (FR-530). For example, toggling automatic host
  sign-in MUST NOT replace an unknown editor value.
- **FR-509**: The existing on/off setting (automatic host sign-in) MUST keep its current behaviour and
  key bindings on the settings screen, except that FR-525 can stop a toggle from being saved, and then its
  row shows `(not saved)` (FR-530). The help footer MUST describe the keys for both kinds of rows.

**Opening a sandbox**

- **FR-510**: `v` on a sandbox MUST open that sandbox's controlled workspace folder in the selected editor
  by starting the editor's command (FR-501/FR-502) on the developer's machine. The TUI MUST NOT block
  waiting for the editor to exit.
- **FR-511**: Every editor command (built-in or Custom) MUST start **detached** from the TUI. It runs in
  its own session or process group, with no terminal and with stdin, stdout and stderr connected to
  nothing. It is reaped in the background so it never becomes a zombie, and it keeps running after the
  TUI quits or its terminal closes. An editor that needs a terminal (for example Neovim) MUST be wrapped
  in a terminal emulator in the Custom command (for example `kitty -e nvim {path}`). The TUI's own
  terminal is never handed over.
- **FR-512**: For a local sandbox, the folder path MUST be passed to the editor as one argument.
- **FR-513**: For a remote sandbox and an editor with Remote-SSH support, the editor MUST be told to open
  the folder over SSH to the host's SSH target, the same way VS Code is told today. For Zed, the editor
  MUST be given one `ssh://<target><absolute-path>` address. There is exactly one `/` between the target
  and the path, because the absolute path starts with one, and the path is encoded the way Zed's address
  parser expects, so spaces and special characters reach the right folder. The exact form and encoding
  MUST be confirmed against Zed's documentation or source during planning. Host entries are free text, so
  the target MUST also be normalised for this address: a leading `ssh://` is removed and an IPv6 address
  gets brackets. A target that still can't be a URL authority (for example one containing `/`, `?` or `#`)
  MUST make `v` refuse with a message, and FR-517's `-` check applies to the normalised target as well.
  Planning confirms the exact rules alongside the address form.
- **FR-514**: For a remote sandbox and an editor without remote-folder support, `v` MUST NOT start
  anything, and the status line MUST say that the selected editor cannot open folders on remote hosts and
  that the editor can be changed on the settings screen. FR-526 sets when this check runs.
- **FR-515**: A Custom command MUST support two markers: `{path}` for the workspace folder and `{host}`
  for the SSH target (empty for a local sandbox). When `{path}` is missing, the folder MUST be added as the
  last argument. When the sandbox is remote and the command has no `{host}` marker, the command MUST
  still run, and its completion message MUST warn that the path is on the remote host (FR-529).
- **FR-516**: A Custom command MUST be split into arguments with shell-style quoting rules and run
  **without** a shell. Each marker MUST be replaced inside its argument, so a substituted value can never
  add or split arguments. Replacement MUST be a **single pass** over the original arguments, so a marker
  that appears inside a substituted value (for example a path containing a literal `{host}`) is not expanded
  again. For a local sandbox, an argument that is exactly `{host}` and nothing else is dropped. Any other
  argument containing `{host}` gets an empty string in its place. Markers cannot be escaped, so a literal
  `{path}` or `{host}` in the command is not supported. These guarantees cover the TUI's own launch only. A
  program in the command that re-parses or executes its arguments (for example `sh -c '…{path}…'` or
  `kitty -e {path}/bin/edit`) can still treat the value as code. The TUI can't detect this reliably, so
  FR-517 limits the risk and FR-521 warns about it.
- **FR-517**: Before any editor is started, the workspace folder MUST be an absolute path and MUST NOT
  contain control characters, including newlines. The SSH target MUST NOT start with `-`, checked on the
  target as given and again after any normalisation (FR-513). For a **remote sandbox with Custom, or with the
  selected editor's override variable set** (FR-502's meaning of set, so an empty or spaces-only value
  doesn't count, and neither does a value equal to the editor's default command, which starts the same program), the
  path comes from the remote daemon and the program may re-parse it (FR-516; a wrapper
  script recommended by FR-502 is that kind of program), so the path MUST also be valid UTF-8 and contain only
  Unicode letters, marks and digits, the ASCII space, and the characters `/ . _ - + = @ , :`. None of those
  letters, marks or digits is shell syntax, so a home such as `/home/josé` works. Anything else is refused,
  including every shell metacharacter, `~`, `(` and `#`. A default command isn't limited, even when another
  editor's variable is set, so SC-002 holds. Otherwise `v` MUST refuse with a message, so neither value is read as an option
  and a remote daemon can't supply shell syntax. A refusal under the path limit MUST name the first rejected
  character, giving its code point when it can't be shown safely, so the developer can tell what to change.
  Spaces stay allowed because real workspace paths can contain them. A space can still split words when a
  marker sits inside a developer's own `sh -c` script, so FR-521 gives the form that avoids this.
- **FR-518**: When the editor's command can't be started (not found, not executable), the status line
  MUST name the executable (FR-532), the reason the OS gave (cut as FR-533 says) and, for a built-in editor, the
  environment variable that overrides it,
  with a reminder that the variable holds a single executable (a wrapper script or Custom covers a command
  with arguments). For Custom, it MUST point to the settings screen.
- **FR-519**: The sandbox list's help footer MUST name the selected editor instead of a fixed "vscode",
  using the editor's identifier (FR-501), and "custom" for Custom. The status label shown while opening
  stays `opening`, as it is today, and names no editor. The completion message (`opened <sandbox> in <name>`)
  and the "opener not configured" message MUST use the editor's display name instead of a fixed "VS Code".
  With VS Code selected, all of these MUST read exactly as they do today, apart from FR-529's notices.
- **FR-520**: A sandbox is **remote** when its host has an SSH target. This is the same rule that decides
  today whether VS Code opens the folder over Remote-SSH. Every "remote" in this spec means this. If the
  host's entry can't be looked up (the hosts file can't be read, or the host is gone), `v` MUST refuse with
  an error for every editor. The sandbox MUST NOT be treated as local, because that would bypass FR-514.
- **FR-521**: The note beside the custom command input MUST state, briefly:
  - what the `{path}` and `{host}` markers do, and that the folder is added last when `{path}` is missing;
  - that the command doesn't run in a shell, so a leading `~/` is expanded, `$VAR` and shell syntax such as
    `&&` or `|` are not allowed, and globs are not expanded;
  - that the command runs with no terminal (FR-511), so an editor that needs one must be wrapped in a terminal
    emulator;
  - that a program which re-parses its arguments, such as `sh -c`, can run a folder path as code when the
    marker is inside its script, so the path must be passed as a separate argument:
    `kitty -e sh -c 'cd "$1" && nvim .' sh {path}`;
  - that a `{host}` embedded in a larger argument only works on remote sandboxes.
- **FR-522**: When Custom is selected, FR-506's checks MUST run again on the session's custom command on
  every `v` press. That is the command loaded at startup or confirmed on the settings screen, so a hand edit
  reaches `v` at the next start (FR-530). If the checks fail, `v` MUST refuse with the reason, naming only the
  rule the command broke (FR-532), and point to the settings screen.
- **FR-523**: If an editor command exits with a **non-zero status** (or is killed by a signal) within
  3 seconds of starting, the status line MUST report a failure that names the sandbox, the executable
  (FR-532), the exit status and where to fix it: the override variable for a built-in editor, the settings
  screen for Custom. stderr stays discarded (FR-511). The report replaces whatever the status line shows at
  that moment, and the same `v`'s completion message MUST NOT overwrite it, even when the command exits at
  once. The report MUST NOT clear another row's in-progress indicator or trigger a reload. The exit status
  comes from the background reaper in FR-511, and `v` MUST NOT wait for it before reporting that the editor
  started. An exit with status 0, or a command still running after 3 seconds, counts as success.
- **FR-524**: The README MUST cover the configurable editor: the editor list and requirements, the
  environment variables (FR-503), and the `v` row in the key table. It MUST also document:
  - the Custom command's rules, including FR-506, FR-511, FR-516, FR-521 and FR-531, with the positional
    `kitty -e sh -c 'cd "$1" && nvim .' sh {path}` form as the example;
  - beside the override variables, the same re-parsing warning and FR-517's remote path limit;
  - that the command is stored in plain text, and that arguments are visible in the process list (`ps`,
    `/proc/<pid>/cmdline`) while the editor runs, so a secret belongs in a wrapper script or the environment;
  - that the settings screen is the recommended way to set a command with quotes, because the TUI handles the
    escaping. A hand-edited command is best written as a TOML literal string
    (`editor_custom_command = '"/My Editor/edit" {path}'`), or as a multi-line literal when it contains a
    single quote (`editor_custom_command = '''kitty -e sh -c 'cd "$1" && nvim .' sh {path}'''`);
  - that hand edits take effect at the next start, and that a save doesn't keep comments in the file (FR-530).
- **FR-525**: A settings file that can't be read or parsed MUST NOT be overwritten. There are two states:
  - **Unreadable at startup.** The file exists but can't be read or parsed when the TUI starts. The TUI MUST NOT
    write it for the rest of that session, even if the file is fixed in the meantime. Defaults apply until the
    developer changes a setting, and any change on the settings screen, including toggling automatic host
    sign-in, applies to this session only. So `v` opens VS Code in this state until the Editor setting is
    changed, even when the file names another editor. The status line MUST say the change wasn't saved and that
    the file needs fixing **and a restart**. The settings screen MUST show the "settings could not be read"
    notice, saying the same, for the session, and FR-529 adds it to `v`. A fixed file takes effect at the next
    start.
  - **Broken mid-session.** The file parsed at startup but can't be read or parsed when a save re-reads it
    (FR-530). That save MUST be refused, and the change applies to this session only. The status line MUST say
    the change wasn't saved and that the file needs fixing, with **no** restart, because the next save re-reads
    it and can succeed. The settings screen MUST show a notice that the file couldn't be read at the last save.
    The notice clears at the next save that re-reads the file successfully. Nothing is added to `v`, because `v`
    doesn't read the file and uses the session's values as before.
- **FR-526**: Every check that doesn't need the workspace path MUST run **before** the daemon is asked for
  it, so `v` refuses at once even when a remote host is unreachable. These are: the host lookup (FR-520), no
  remote support (FR-514), a missing or invalid custom command (FR-522), an SSH target starting with `-`
  (FR-517), a target Zed can't use (FR-513), a home directory that can't be determined when the command needs
  it (FR-531), and an executable that can't be found (FR-518). The launch error stays the final authority,
  because the executable can change between the check and the start. The path checks in FR-517 run after the
  daemon replies. The search for the executable MUST NOT block the TUI (see Notes for Planning). The host lookup
  and FR-530's settings re-read and write stay on the update loop, as the host lookup and the settings write do
  today, because they're small local reads and writes that only happen on a key press. So a config directory on a
  stale mount can still freeze the TUI, as it can today.
- **FR-527**: The settings file MUST be written with owner-only permissions (`0600`), including any
  temporary file used to write it, because a custom command can hold a token or a licence key. The temporary
  file MUST be created fresh with a unique name, so a leftover from a crash can't pass on wider permissions
  and two running TUIs never share one. An existing settings file with wider permissions is corrected on the
  next save.
- **FR-528**: The command input MUST be a guarded inline input under SWI-9 (its FR-010 to FR-017), like the
  rename input:
  - If its trimmed value differs from its starting value (SWI-9 FR-011a), including a cleared value, `esc`
    MUST raise the unsaved-changes dialog. Save acts exactly like `enter`, so an FR-506 rejection keeps the
    input open with the reason. Cancel returns to the input unchanged. Discard returns to the list with
    nothing saved.
  - `enter` writes before it closes. On success the input closes to the **settings row** and the command
    applies. If the write itself fails, the input MUST stay open with its text and the error (SWI-9 FR-017),
    and the session's editor and command don't change, so `v` never starts using a command that failed to save
    this way.
  - **Stated exception to SWI-9 FR-017:** when FR-525 refuses the save, or there's no settings store, no write
    was tried, so the input closes to the settings row, the command applies to this session only, and the row
    shows `(not saved)` (FR-530). This is how a developer keeps working with an unreadable file.
  - The input cleans its text, as every single-line input does: tabs and newlines become spaces and other
    control characters are dropped, both when it opens and on paste. So FR-506's control-character check can't
    fire from the input. It applies to saved text, which only a hand edit can make hold one, and `v` enforces
    it (FR-522). A pasted newline becomes a space, and FR-506's other checks run on the result.
  - The starting value is the cleaned, trimmed text. When cleaning changes a saved command as the input opens,
    the input MUST show SWI-9's notice that the text will be rewritten when saved. `enter` then saves the cleaned
    text. `esc` with no further edit raises no dialog and leaves the saved text as it was.
  - If the value is unchanged, `esc` returns to the list with no dialog.
  - `esc` backs out **one level** at a time (input → list → settings row) and never closes the settings
    screen from inside the list or the input. List → settings row drops no typed text, so it raises no
    dialog, and moving the list cursor isn't an edit.
  - Printable keys and pasted text are text, including `q`, `,`, `j`, `k` and space. Single-line editing keys
    keep their usual meaning (`ctrl+e` moves to the end of the line), and `ctrl+c` keeps its current behaviour
    on inline inputs (SWI-9 FR-012). `enter` confirms.
- **FR-529**: The status line holds one message, so a notice the developer must read MUST be part of the
  message for the action it concerns, rather than a message of its own that the next one replaces:
  - While the unknown-editor state (FR-508) or the unreadable-at-startup state (FR-525) holds, the outcome
    message of **every** `v` MUST include the notice. That covers completion, refusal and failure, including
    FR-523. For example `opened abc in VS Code (unknown editor 'foo' in settings)` and
    `opened abc in Zed (settings couldn't be read at startup, changes aren't saved)`. That wording stays true
    whichever editor the session is using. The broken-mid-session state adds nothing to `v` (FR-525).
  - FR-515's remote warning MUST be part of the completion message, for example
    `opened abc in Custom (the path is on the remote host)`.
  - A message MUST carry **every** notice that applies, in one pair of parentheses separated by `; `, with settings
    notices first and the remote warning last. The case that arises is Custom chosen for the session while the
    file was unreadable at startup, on a remote sandbox with no `{host}`:
    `opened abc in Custom (settings couldn't be read at startup, changes aren't saved; the path is on the remote host)`.
  - The two settings notices MUST also be set in the status line at startup, as best effort. A later message,
    such as the sign-in summary, may replace them, and that's accepted because `v` and the settings screen
    always show them, and FR-533 keeps them from being cut off.
  - Text from the settings file MUST be shown with control characters removed.
  - A `v` failure or refusal (FR-533) MUST show in the error style whenever it's shown, both when it arrives and when
    it's released from a hold. A completion message MUST NOT, even with a notice. A failure or refusal MUST NOT change
    the style of any later message.
- **FR-530**: Every save MUST re-read the settings file, apply only the setting that changed to the values
  just read, and write the result. A toggle changes its own key, confirming a built-in editor changes the
  editor key, and confirming Custom changes the editor key and the custom command. So a hand edit or another
  running TUI's change to any other key survives. A missing file reads as defaults. Confirming Custom writes
  the command shown in the input, because the developer has just confirmed it.
  - The session's values stay as loaded at startup plus its own changes, so a hand edit reaches `v` and the
    settings screen at the next start.
  - A change that applied to the session only (FR-525, no settings store, or a failed write from a toggle or
    the Editor list) isn't written by a later save, because a hand fix may have changed that key. Its row MUST
    show `(not saved)` on the settings screen until a save writes what the row shows: the toggle's key, or for the
    Editor row the keys listed above for the entry confirmed. So the value a row shows can't silently disappear at
    the next start. In the unreadable-at-startup state nothing is written, so the marks stay for the session.
  - To keep the Editor row's value, the developer confirms the same entry again, which writes it. A toggle can't
    re-save its value, because pressing it flips the value first (FR-509). The first press saves the opposite value
    and clears the mark, and a second press saves the original value, so keeping the value takes two presses.
  - Confirming a built-in editor writes only the editor key, so it clears the Editor row's mark even when the session
    holds a custom command that was never saved. That command stays in the session and fills the input if Custom is
    chosen again (FR-506), but it's gone at the next start unless it's confirmed. This loss is accepted, because the
    developer has moved away from it and the row no longer shows it.
  - Comments in the file aren't kept, nor are keys this version doesn't know (see Downgrade).
  - There is no lock. If two TUIs save at the same instant the last write wins, which is accepted.
- **FR-531**: A Custom command MUST be split by these rules, which follow POSIX shell word splitting with no
  expansion except `~`:
  - Spaces and tabs outside quotes separate arguments. Quoted and unquoted pieces next to each other join
    into one argument, and `""` or `''` is an empty argument (rejected only as the first, FR-506).
  - Inside single quotes every character is literal, and there are no escapes.
  - Inside double quotes, a backslash before `"`, `\`, `$` or a backtick gives that character. Any other
    backslash stays as a literal character.
  - Outside quotes, a backslash makes the next character literal, so `/My\ Editor/edit` is one argument, and
    `\$` and `\&` aren't rejected.
  - An unquoted, unescaped `~/` at the start of an argument, or an argument that is exactly `~`, expands to
    the developer's home directory. Nothing else expands: not `~user`, not `--dir=~/x`, and not a quoted or
    escaped `~`.
  - Markers are found in the split arguments, whatever quoting produced them (FR-516).
  - Expansion happens at launch, on each `v`, never at save. The saved text is never expanded, and a hand-edited
    command is treated the same way. If the home directory can't be determined and the command needs it, `v`
    MUST refuse with a message. The order on `v` is: split, FR-506's checks, `~`, then markers, so a
    substituted value is never expanded.
- **FR-532**: A custom command can hold a secret, so outside the command input the TUI MUST show only its
  executable (the first argument after splitting), never its other arguments, and only for a command that
  passes FR-506's checks. This applies to status messages (FR-518, FR-523) and logs. A rejection (FR-506) or
  refusal (FR-522) MUST NOT quote any part of the command, and names only the rule it broke. The Editor row
  shows `Custom: <executable>`, followed by `…` when there are more arguments, or `Custom (invalid command)`
  for any command that fails FR-506's checks, including an empty one or one that can't be split. The input
  shows the full text, because the developer has to edit it.
- **FR-533**: The status line MUST wrap to the terminal's width instead of being cut off, at spaces where it
  can, so the end of a message stays readable, including FR-529's notices and FR-523's "where to fix it". The
  lines it adds MUST come out of the body, so the header and help footer stay on screen. That MUST hold however
  the body came to be the wrong size, including when the developer returns to a surface while a long message is
  showing, and when the update banner or the help footer changes height during one. To keep one message from filling
  the screen:
  - An unknown editor value MUST be cut to 60 columns, with `…` at its end.
  - An executable MUST be cut to at most 60 columns in the middle, keeping its first 20 and last 39 columns (one fewer
    if a wide character falls on the cut), so its name stays readable:
    `/Users/developer/Lib…port/JetBrains/Toolbox/scripts/webstorm`.
  - In a `v` outcome message, text from outside this spec MUST be cut with `…` before the rest of the message is added:
    a daemon error to 120 columns and FR-520's lookup error to 78. FR-518's reason MUST be cut to 40 the same way. So the text after it,
    FR-529's notices and where to fix it, is never what's lost. FR-520's lookup error MUST be cut in the middle, keeping its first 38 and
    last 39 columns (one fewer if a wide character falls on the cut), because it names the path first and the reason last; one that
    already fits MUST NOT be changed.
  - A message MUST be cut with `…` at 6 lines, or sooner if that's what's left after the header, the help footer and
    the screen's smallest usable body, and never below 1 line.
  - Every value MUST be sanitised before it's cut, and the status MUST be sanitised before it's wrapped, so these limits
    count the columns that are actually drawn. Sanitising can make text wider, because a tab has no width until it's
    expanded.

  On a terminal of 80×24 or larger, every message this spec defines, with outside text at these limits, MUST fit in
  full on a screen whose body can shrink to make room, such as the sandbox list, so nothing it requires is lost. A
  screen whose own content can't shrink, such as Daemons with many rows, can leave less room. The message is then cut
  with `…` to fit, and the status keeps the whole of it, so it shows in full on a screen with room unless something has
  replaced it. A held message joined to an error (below) is also outside this promise, because the two can be longer
  than the room at 80×24, and it's cut the same way. These views don't gain scrolling for this feature. A view that's
  still taller than the terminal loses its top lines, as it does today. `chrome` MUST NOT drop lines from the bottom
  of the body. On a very short terminal the selected row can still scroll off the top, as it does today. FR-534 has
  its own rule for its note.
  - **The embedded terminal screen is the exception.** Its body is the agent's PTY grid, so the size of the PTY
    MUST NOT depend on the status line, and a status change MUST NOT resize it or signal the agent. Only a window
    resize does, as today. The status there MUST be cut with `…` at the 2 lines that fit below the grid, so the
    header and its `ctrl+q` hint stay on screen. A message that arrives while the terminal is open can lose its end
    there, and one that arrives while it's opening is replaced when it opens. That's accepted, except for a `v`
    failure or refusal, meaning any `v` outcome message that isn't a completion, including FR-523's report and FR-517's
    path refusals. One that arrives while an in-place terminal is open or opening, or while a Replace dialog for an
    in-place attach is open, MUST be held. The hold ends when the terminal closes, or when its attach ends without
    opening and no attach can follow. So a Replace dialog keeps the hold until it's declined or dropped because its daemon
    disconnected, and accepting it starts an attach that continues the hold. Only the latest attach can end the hold. A superseded one can't. When the hold ends,
    the held message MUST replace whatever the status line shows on the screen the developer lands on, such as
    `detached from <name>`, and MUST show in the error style (FR-529). The exception is an attach or terminal error,
    meaning `terminal: <reason>`, `an external terminal is already attached to <name>` or
    `terminal for <name> not opened — you moved on; press t again`, and a daemon drop's
    `<name> disconnected — orchestrators closed`, which is the only message explaining why the screen changed. The status MUST then show both in one message, with
    the error first: `<error>; <held message>`. The error MUST be cut to 120 columns with `…` first, and the whole
    message MUST show in the error style. An error or daemon-drop message that was already showing when that attach
    started or that Replace dialog opened isn't joined: the held message replaces it. A later `v` failure or refusal replaces a held one. A `v` failure or refusal
    that's already showing when an in-place attach starts, or when a Replace dialog for one opens, MUST be held in the
    same way if it arrived in the last 3 seconds. An older one isn't held. A message that's been released MUST NOT be
    held again, even within its 3 seconds, so the next attach replaces it like any other message.
- **FR-534**: The settings screen MUST wrap everything it shows to the terminal's width, at spaces where it can, so
  nothing is cut at the edge. That covers the rows and their descriptions, FR-508's and FR-525's notices, and
  FR-521's note.
  - The Editor row's executable MUST be cut in the middle (FR-533). A trailing `…` still means only that there are
    more arguments (FR-532), so a row can show both. `(not saved)` follows it and wraps with the row, so the mark is
    never what gets cut.
  - A notice MUST say what to do before it gives any error detail, so a long parse error can't push the instruction
    out of view.
  - While the Editor list or the command input is open, it replaces everything below the header, including the
    screen's title lines, the rows and the notices, leaving one title line (`Editor` or `Custom command`). They come
    back when it closes. FR-506's rejection reason appears in the status line only.
  - The list MUST scroll to keep its cursor visible when it doesn't fit. A long command MUST scroll sideways within
    the input instead of running off the edge.
  - With the input open and a rejection showing, the whole note MUST be visible at 80×24 or larger. On a shorter
    terminal the note gives way first: its last lines are replaced with `…`, so the input stays on screen.

### Key Entities *(include if feature involves data)*

- **Editor**: one entry in the catalogue. It has a stable identifier (saved in settings), a display name,
  a default command, an override variable, and how it opens remote folders (Remote-SSH option, SSH
  address, or not supported).
- **Editor preference**: part of the saved settings. It holds the selected editor's identifier, plus the
  custom command line as saved. The custom command is kept even when another editor is
  selected, so switching back to Custom fills it in again.
- **Open request**: what one `v` press resolves to. It holds the sandbox's workspace folder, the host's
  SSH target (empty for local), the selected editor, and the resolved command. From these it produces
  the exact command and arguments to start, or a reason why the editor cannot be opened.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can switch the `v` editor from VS Code to any other listed editor in under 15
  seconds from the sandbox list, without editing any file or environment variable.
- **SC-002**: With no new settings and no new environment variables, `v` opens the same folder with the same
  command and arguments as before this feature for 100% of local and remote sandboxes, and its footer text
  and success messages are unchanged. The only differences are detached launch (FR-511), the new input
  checks (FR-517, FR-526), the error texts and refusals in FR-518 and FR-520, the failure report in FR-523,
  FR-529's notices when the settings file names an unknown editor or can't be parsed, and long status messages
  wrapping or being cut (FR-533). FR-517's remote path limit can apply only when the selected editor's override
  variable is set to something other than its default, and that includes an existing `SWITCHBOARD_CODE_BIN` with VS Code selected.
- **SC-003**: For every editor in the catalogue, the command and arguments started for a local sandbox,
  and for a remote sandbox where supported, match the editor's documented form. This is checked by an
  automated test for each editor.
- **SC-004**: Every failure the TUI can detect on a `v` press shows a status message that names the cause
  and where to fix it, and the TUI stays usable. The causes are: command missing, a command that exits
  non-zero soon after starting (FR-523), remote not supported, no custom command or an invalid saved one, a
  workspace path that fails FR-517, an SSH target that fails FR-513 or FR-517, a home directory that can't be
  determined (FR-531), and a failed host lookup. For the path and target causes, "where to fix it" is the
  daemon or the host entry. A rejected custom command on
  the settings screen (FR-506) also says why it was rejected. Failures inside the editor after it has started
  can't be detected and aren't covered. An automated test at 80×24 on the sandbox list, with the update banner
  showing, checks that the header and the whole message are on screen, including every notice and where to fix it
  (FR-533). The cases include a long daemon error with a notice, a long daemon error containing tabs, the list opened
  after a 3-line status was set on the settings screen, and a banner arriving during a 4-line message. The embedded
  terminal screen is excluded (SC-008), and so is a held message joined to an attach or terminal error (SC-008). A view
  that can't shrink is also excluded, and a separate test checks that it cuts the message to fit with the header on
  screen, and that the whole message shows on returning to the list.
- **SC-005**: For a local sandbox, or a remote path that FR-517 accepts, a custom command whose programs
  don't re-parse or execute their arguments opens the right folder, and no part of the path is ever run as a
  command. Locally this holds for a path containing spaces or shell characters, and it also holds for the
  positional `sh -c` form in FR-521. For a command with a marker inside a script it re-parses, the TUI can't
  give this guarantee. On a remote sandbox with Custom, or with the selected editor's override variable set to something other than its default, a
  path outside FR-517's limit is refused before anything starts, and the note beside the input and the README
  warn about the rest.
- **SC-006**: After a settings file becomes unparseable, no action in the TUI changes its contents. The saved
  editor and custom command are still there once the developer fixes the file.
- **SC-007**: A hand edit made to any setting while the TUI runs is still in the file after the TUI saves a
  different setting.
- **SC-008**: Automated tests at 80×24 check that the header stays on screen and no line is cut at the edge on the
  settings screen in three states: the rows with two notices and a `(not saved)` mark on a long executable, the open
  Editor list, and the open command input with a rejection and the whole note (FR-534). On the embedded terminal
  screen, tests check that a long status sends no resize and leaves the header on screen, that a terminal opened
  while a wrapped message is showing gets the same PTY size as one opened with no message, and that a `v` failure or
  refusal arriving while the terminal is open or opening is shown in full, in the error style, once it closes. That
  includes a first attach superseded by a second, and an attach that ends in the Replace dialog, both declined and
  accepted. Tests also check that a `v` refused at the key press and followed by `t` is shown the same way, including
  when `t` opens the Replace dialog and it's accepted more than 3 seconds later, and that a message over 3 seconds old
  isn't held. When a `v` is refused and the `t` that follows fails to attach, or a daemon drop closes the Replace dialog, tests check that the status shows both in
  one message, with the error first and in the error style, and that a second `t` within 3 seconds doesn't hold the
  refusal again, so a second failure shows only its own error. The same checks cover an accepted Replace whose attach
  fails. An old error or `disconnected` message still showing when `t` opens the Replace dialog isn't joined to a failure
  held behind it. Tests also check that both show when a terminal session dies or the developer moves on during the attach, and
  that `detached from <name>` is replaced. A joined message too long for the sandbox list at 80×24
  with the update banner is cut with `…`, with the header and the whole error on screen, and the status keeps all of it
  (FR-533). A `v` failure or refusal shows in the error style when it arrives and when it's released, and the next plain
  message isn't in that style (FR-529).

## Notes for Planning

These follow from the requirements above and from how the code works today. They aren't requirements.

- **Tokenizer (FR-531):** the `$` and `~` rules depend on whether each character was quoted, and common
  splitting libraries drop that information. `go.mod` has no such library, and `SWITCHBOARD_TERMINAL` just
  uses `strings.Fields`, so expect a small custom tokenizer.
- **Late failure report (FR-523):** `statusMsg` and `errMsg` both reset `m.busy` for every row, and `statusMsg`
  also triggers a reload, so the report needs its own message type. It must be chained after the "opened"
  message, because `tea.Batch` doesn't guarantee order.
- **Saving (FR-527, FR-530):** `SettingsStore.Save` writes the whole file from `m.prefs`, which is only loaded
  at startup. Merging must keep the raw saved values, so an unknown editor isn't replaced by its VS Code
  fallback. `SaveTOML` uses a fixed `settings.toml.tmp` name and `os.WriteFile`, which applies the mode only
  when it creates the file. The review confirmed that a leftover `0644` temporary file gives a final mode of
  `644`, so add a test for that case. `SaveTOML` is also used for hosts, groups and configs, so choose how the
  owner-only mode reaches settings alone.
- **Guard (FR-528):** add a surface to `guardSurface` in `formguard.go`. `activeGuard` must choose it from a
  sub-state of `screenSettings`, like the `screenHosts` case and `gHostAdd`, because the input shares that screen
  with the row and the list. `gRename` is chosen from the screen alone, so it doesn't fit. Neither `submitRename`
  nor `submitHostAdd` is a model for the submit, since both close the input before acting. This one writes first
  and closes after.
- **Filesystem checks (FR-526):** today `openVSCodeCmd` does its host lookup on the update loop, and only the daemon
  call runs inside the returned command. `exec.LookPath` belongs inside that command, before the daemon call,
  because it stats a file in each `PATH` directory and a stale network mount can block it for a long time. The host
  lookup stays where it is, although `hostStore.Get` re-reads the hosts file through `LoadTOML` on every call. The
  other new work on `v` is small parsing and character checks, and the settings re-read happens only on a save.
- **Status line (FR-533):** `footer` calls `sanitizeLine` with width 0, and Bubble Tea cuts wider lines with no
  ellipsis. `renderHelp` already wraps at `m.width`. Bubble Tea drops the **top** lines of a view taller than the
  terminal, and `bodyHeight` assumes one status line. The change reaches every screen using `chrome`, not only `v`.
  - **Where to resize:** 14 call sites read `bodyHeight` once as a surface is built (7 `newItemList`, 7 huh
    `WithHeight`), and only `m.list` is resized after startup, on a window resize. There are 107 non-test
    `m.status =` assignments and no setter, so resizing where the status is set isn't practical. The `Update` wrapper
    already reconciles guard layers after each message, so it can also compare the active surface's height with its
    target and resize if they differ (for the list, `m.list.Height() != m.mainListHeight()`). Comparing the status
    line count would miss a surface the developer returns to, because `m.list` keeps its old height while other screens
    are open. It would also miss the banner, because `applyUpdateAvailable` sets `m.updateBanner` with no `SetSize` and
    `mainListHeight` counts it. Views that read `bodyHeight` as they render, like Jobs, need nothing. A surface with no
    height to set has its whole height as its smallest usable body, so the message is cut to fit around it. For example,
    `viewDaemons` has no scrolling, and at 80×24 with no status the screen is 7 + 2N lines for N daemons (measured), so 6
    daemons leave 5 lines, 7 leave 3 and 8 leave 1.
  - **Cost of the check:** the wrapper runs on every message, including spinner ticks and mouse wheel and drag events.
    `tea.WithMouseCellMotion` reports motion only while a button is held. Skip the check on the terminal screen, whose
    size never depends on the status, which also covers most PTY updates. Elsewhere the expensive step is the target:
    `mainListHeight()` measured about 265–320 µs and 680–720 KB a call at 80×24 with 8 sandboxes, mostly from `listHelp()`
    and its value-receiver calls copying `Model`. Wrapping a 280-column status is about 7 µs. So:
    - Don't cache the wrap. A cached cut would also go stale, because the cut depends on the room left, and a banner
      arriving changes that with the same status and width. Wrap and cut on each use.
    - Compute the target only when something it reads has changed, by comparing a small key: the status, width, height,
      banner, active surface, number of tabs, and whether `listHelp` offers `MarkFree`. It has to be the active surface
      and not `m.screen`, because a list or form can open and close within one screen, and the one it returns to can
      have an old height. It has to be `MarkFree` and not the selected row, because a reload can change the row's review
      state with the same row selected. A test should change each of these and check the size.
    - huh's `Form` v1.0.0 has `WithHeight` but no way to read the height back, and `Group.WithHeight` renders each field
      to measure it. So store the last height applied beside each form, and call `WithHeight` only when the target
      differs.
    - A field shrunk during a long message doesn't grow back on huh v1.0.0. `Group.WithHeight` calls `field.WithHeight`
      only for a field that's too tall. Measured with the form at 18, shrunk to 13 and restored: a 30-option `Select` or
      `MultiSelect` stays at 13 lines once the form has handled a message, and a `Note` returns to 18. A check that
      doesn't call `Update` after restoring sees all the lines again, which is misleading. The cursor stays on screen,
      and `down` still scrolls, so the field is only shorter than it needs to be. Accept this until the form closes,
      because rebuilding an open form would risk the developer's input.
    - A shrink can also leave the focused field undrawn. With 8 `Input`s in one group and focus on the last, shrinking
      from 18 to 16 or less doesn't draw that field until the form handles a message. `spinner.TickMsg` returns before
      `forward` (`app.go:720`), so ticks don't help. After each `WithHeight`, pass a no-op message through the form's
      `Update` in the same pass, and keep any command it returns. That brought the field back with its typed text in the
      same measurement. Test that the last field stays drawn after a shrink.
  - **No fallback in `chrome`:** the review rendered today's list at 80×15. The view is 20 lines with the selected row at
    lines 8–10, so at that size Bubble Tea's top-line drop keeps that row, and dropping from the bottom of the body would
    remove it. This isn't a guarantee: with a one-line status and the first row selected, the row's name is gone at 80×11
    or shorter (measured), so `v`, `d` or `s` can already act on a row whose name can't be seen. FR-533 doesn't make that
    worse, and this feature doesn't fix it.
  - **Outside text (FR-533):** `errMsg` builds `error: <err.Error()>` with no room for a notice (`groups.go:242`), so
    `v` needs its own message assembly: sanitise and cut the error, then add the notices. FR-518's reason is the innermost
    error, such as `permission denied`, because `Opener.Open` wraps it in `vscode: launch <bin>: …`. These errors name the
    executable and not its arguments, so FR-532 holds.
  - **Sanitising first (FR-533):** `sanitizeLine(text, n)` already sanitises and then cuts, so use it for the end cuts.
    Text with 10 tabs cut by `ansi.Truncate` first came to 156 columns in the review's test and 130 in another, against
    120 with `sanitizeLine`. The middle cut has no helper, and slicing raw text can split an escape sequence. Start from
    `sanitizeLine(text, 0)`, then join `ansi.Truncate(s, 20, "")`, `…` and `ansi.TruncateLeft(s, width-39, "")`. That
    came to 61 columns when a wide character fell on the cut, so test this and drop a column when it does. FR-520's lookup error uses the same cut with a head of 38. `footer`
    keeps calling `sanitizeLine` at render, and the wrap has to come after it, because FR-529 asks for sanitising only
    for text from the settings file, and other callers set `m.status` with raw text.
  - **Room at 80 columns (measured on today's code):** the sandbox list's help footer is 5 lines, and its bubbles
    list draws at least 9 rows even when given fewer. So the screen needs 19 rows plus the status lines, or 20 plus
    them with the update banner. That leaves 4 lines for a message at 80×24 with the banner, so the longest `v`
    messages have to stay within 4 lines at 80 columns, even with two values cut to 60 columns. A wordy draft of FR-518's
    longest case came to 5 lines and a tighter one to 4, so its wording matters. With outside text at FR-533's limits
    and a 60-column unknown value, text-wrap estimates of draft wording give 3 lines for a daemon error, 4 for FR-520,
    4 for FR-518 and 3 for FR-523. The cap of 6 lines is reached only on a taller terminal.
- **Terminal screen (FR-533):** `viewTerminal`'s grid is `termRows()`, which is `m.height - 8`, and `resizeTerminal`
  runs only on a window resize. Measured with a one-line help footer, the view is `m.height - 2` lines plus the status
  lines, so 2 lines fit and a third pushes out the top line. Below 11 rows the grid is at its floor of 3 rows and the
  screen can already overflow. `termRows` has to keep its fixed one-line assumption if `bodyHeight` becomes aware of the
  status line, because `attachTerminal` reads it before it replaces the status, so a terminal opened during a wrapped
  message would start too short and stay that way until the next resize. `footer` needs a per-screen line limit.
  `escHoldHint` is 101 columns, so it takes 2 lines at 80 columns.
- **Holding a `v` failure or refusal (FR-533):** `attachTerminal` sets `opening terminal…`, `handleTermOpened` sets
  `m.status = ""`, and both `ctrl+q` and `handleTermClosed` set `detached from <name>`. So the message needs a field of
  its own, which the `Update` wrapper can move into the status once the hold ends. An attach ends in `handleTermOpened`
  or `handleTermClosed`, including when the developer has moved on. These details are easy to get wrong:
  - **Error state:** `footer` shows red only when `m.err != nil` or the status starts with `error` (`app.go:1454`), and
    `statusMsg` sets `m.err = nil` (`app.go:763`), so a background action finishing while the terminal is open clears it.
    FR-518's and FR-523's messages don't start with `error`, and today's refusals only set `m.status`, such as
    `start the sandbox before opening its terminal` (`terminal.go:98`). A `v` outcome can't use `m.err` for its style
    either, because `attachTerminal` and `handleTermOpened` don't clear it, so `opening terminal…` would show in red. Keep
    the failing text in its own field, such as `m.vFailStatus`, with the time it arrived, and have `footer` use red when
    the status is non-empty and equals it. The style then ends when the message does. A release sets both the status and
    this field, doesn't touch `m.err`, and clears the arrival time, so a later `t` finds nothing recent and can't hold
    the message again. A zero time counts as not recent. Use `nowFunc` (`unsaved.go:16`) so tests can step past the 3
    seconds. An `errMsg` can still leave `m.err` set and turn later messages red, as it can today, but a `v` completion
    must clear it as `statusMsg` does today, or a success after an `errMsg` would show in red.
  - **A message already showing:** the refused case returns before the daemon call, so it's on the screen when `t` arrives,
    and `attachTerminal` replaces it. So `attachTerminal` and `confirmReplaceOrphan` (not for `T`) both check for a
    recent one, and copy it into the held field if the status still equals it. It stays on the status line behind the
    dialog. The check belongs in those two rather than in `enterTerminalFrom`, because `t`'s own refusals, such as
    `start the sandbox…` and the wait notice, start no attach. They replace the message on a screen that's still showing,
    like any other message. The 3 seconds can't be checked only in `attachTerminal`, because an accepted Replace dialog
    reaches it after the developer has read the dialog. Skip the copy when the held field already has a message, which
    is the same one or a newer one, so an older status can't overwrite a failure that arrived while the dialog was open.
  - **Superseded attach:** an attach in flight is only a `termSeq` value today, and `t` can be pressed again during one.
    So the marker has to hold the pending attach's number, set in `attachTerminal`, and only a message with that number
    clears it. With a plain flag, the older attach would clear it while the newer one runs, and the newer
    `handleTermOpened` would wipe the released message.
  - **Replace dialog:** `handleTermClosed` opens it when an attach is refused because of a detached agent
    (`terminal.go:191`), and `orphanGate` opens it on `t`. It needs a field saying it's for an in-place attach, which the
    `T` case isn't, and the hold stays while it's open. Declining needs nothing more, because the wrapper then finds no
    attach pending and releases. Accepting is the trap: `updateConfirmKey` closes the dialog and `replaceOrphanMsg` only
    arrives in a later `Update`, so the wrapper would release in the gap, and `attachTerminal` and `handleTermOpened`
    would then replace the message. So the accept must set the marker in the same message, through `onAccept`.
  - **Joining an error:** the wrapper can't tell an attach or terminal error from `detached from <name>` or a background
    message by its text. So the assignments that set one (`terminal.go:150`, `:190` and `:199`, which `SendData`'s
    failure at `:306` also reaches) also copy it into a field, such as `m.termFailStatus`, and `attachTerminal` clears
    that field. So does `confirmReplaceOrphan` (not for `T`), because `t` can open that dialog with no attach. `closeOrchAdminOnDrop`'s `disconnected` message (`hosts.go:489`) is copied the same way. `:190` can't fire for `t` today, because the daemon only refuses a second external attach
    (`broadcaster.go:156`) and `t` attaches in-TUI, but it's treated the same in case that changes. At a release, if the
    status is non-empty and equals the field, the message is `sanitizeLine(status, 120)`, `; ` and the held text.
    Otherwise it's the held text alone. Both `m.status` and `m.vFailStatus` get the result, so `footer` shows all of it
    in red. With nothing held, the error stays as it is today, uncut and in today's style, and that
    includes a retried `t` that fails after a release. Text-wrap estimates of draft wording at 80 columns, with a
    120-column error, give 3 lines for a one-line refusal, 4 for FR-523's report and 5 for FR-518's longest case. The
    sandbox list has 5 lines at 80×24, or 4 with the banner, so only the longest cases lose the end of the held message.
    Two full-length parts can reach the 6-line cap on a taller terminal.
  - **Order in the wrapper:** `reconcileGuardLayers`, `openParkedOverride`, `openDeferredMcpFollowups`, the release, then
    the size check. The two `open…` calls can set the status (`sources.go:421`, `mcp.go:845`), so a release before them
    could be overwritten, and the size check has to see the final status or the surface stays the wrong size until the
    next message.
- **Settings screen (FR-534):** `viewSettings` joins its rows with no width limit. Today's sign-in description is
  already over 100 columns, and the notice embeds `err.Error()` before its instruction. It also draws three lines above
  the rows (`Settings`, the `persisted by…` line and a blank), and the list and input replace those with their one title
  line. The budget at 80×24 with the input open is 2 header + 1 title + 1 input + 1 blank + 2 rewrite notice + 10 note
  + 1 blank + 3 status + 2 help = 23 lines, so FR-521's note has about 10 lines. A draft of its five points came to 10. The input needs `Width` set,
  because `m.rename` leaves it at 0 and doesn't scroll sideways. `windowRange` in `jobs.go` suits the Editor list on a
  short terminal.
- **Input cleaning (FR-528):** `textinput.SetValue` and paste both sanitise (`runeutil` in Bubbles v1.0.0). Take the
  starting value after `SetValue`, as `enterRename` does, and compare with the saved text to decide on the notice
  (`normalizeSuffix`).

## Assumptions

- "WebStore" in the ticket means JetBrains **WebStorm**.
- Adding a new built-in editor later only means adding a catalogue row (FR-501). Editors not on the list
  are reached through Custom.
- A terminal-based editor (for example `kitty -e nvim {path}`) is one use of the Custom entry. The
  existing popout-terminal setting (`SWITCHBOARD_TERMINAL`) is not reused or changed.
- Opening JetBrains projects on a remote host (JetBrains Gateway) is out of scope. It can be a
  follow-up.
- The editor choice applies to every host. There is no per-host or per-sandbox editor setting.
- The editor runs on the developer's machine for every sandbox, as it does today. The daemon and its RPCs
  are unchanged. The daemon already returns the workspace folder, and the client already knows each
  host's SSH target.
- The default commands are the ones each vendor's installer or "install shell command" step provides.
  Developers whose installs differ use the override variables (FR-502).
- The default commands match the vendors' macOS launchers and JetBrains Toolbox scripts. Linux packages
  may differ: tarball `webstorm.sh`, snap names like `intellij-idea-ultimate`, and Zed packaged as
  `zeditor`. The override variables cover these cases, and the "command not found" message names the
  variable.
- VS Code forks (Cursor, Windsurf) need their own remote-SSH extension installed to open remote folders.
  The TUI can't detect a failure inside the editor once the editor has started, unless the command itself
  exits non-zero (FR-523). Windsurf's `--remote` support is to be confirmed during planning.
- Editors reach the remote host through the developer's own SSH configuration, as VS Code does today. A
  host's switchboard-specific SSH options (for example `-i <key>`) are not passed to editors.
- The key stays `v`. Its help text changes to name the selected editor.
