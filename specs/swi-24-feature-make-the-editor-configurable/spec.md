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

- **The remote path limit also covers set override variables**, including `SWITCHBOARD_CODE_BIN`, because a
  wrapper script can re-parse its arguments. Default commands aren't limited. (FR-517, FR-524)
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
   to do this (FR-506).

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
  Defaults apply for the session, and the file is never overwritten, so a saved editor and custom command
  aren't lost when another setting is toggled (FR-525).
- **Empty variable.** An editor's command variable is set but empty or only spaces. That counts as unset,
  and the default command is used.
- **Settings store unavailable.** The settings file can't be written. The choice applies to this session
  only and the status line says so, the same as the existing sign-in setting does.
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
  when nothing is saved.
- **FR-505**: Activating the Editor setting MUST let the developer choose one entry from FR-501's list,
  with the current choice marked. `space` or `enter` activates the row, like the existing rows. In the list,
  `↑`/`↓` and `j`/`k` move, `enter` confirms, and `esc` cancels and leaves the setting unchanged. `space`,
  `q` and `,` do nothing in the list. `enter` MUST always save the chosen entry, even when it's already
  marked, so confirming VS Code over an unknown saved value replaces that value and clears its notice
  (FR-508). `esc` leaves the saved value in place, unknown or not. Choosing Custom saves only when the
  command input is confirmed (FR-506).
- **FR-506**: Choosing Custom MUST ask for a command line, with the session's custom command filled in when
  one exists. The text is split by FR-531's rules. The command MUST be rejected with a message that gives
  the reason when it:
  - is empty, contains only spaces, has unbalanced quotes, ends in an unquoted backslash, contains a control
    character other than a tab, or splits into no arguments;
  - has an empty first argument (`"" {path}`), because there would be no program to start;
  - has a `{path}` or `{host}` marker anywhere in its first argument (the executable), because that would
    run the folder path or SSH target as a program, or leave no program at all;
  - has a first argument that starts with an unquoted `NAME=` (`FOO=1 myeditor {path}`), because nothing
    sets the variable and `FOO=1` would be run as the program;
  - contains an unquoted or double-quoted `$` (`$VAR`, `${VAR}`) or backtick, or an unquoted `|`, `;`, `&`,
    `<` or `>`, because nothing interprets them. The message MUST repeat that the command doesn't run in a
    shell, and that `sh -c` or a wrapper script is the way to do this. Inside single quotes, or after a
    backslash that FR-531 honours, these are literal characters.

  Other characters a shell treats specially, including the globs `*`, `?` and `[`, are passed as literal
  text. The text MUST be saved as typed, with only surrounding spaces trimmed. When a command is rejected,
  the input stays open with the reason and the previous editor choice MUST stay in force. FR-528 sets the
  keys while the input is open.
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
  key bindings on the settings screen, except that FR-525 can stop a toggle from being saved. The help footer MUST describe the keys for both kinds of rows.

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
  target as given and again after any normalisation (FR-513). For a **remote sandbox with Custom or with a
  set override variable**, the path comes from the remote daemon and the program may re-parse it (FR-516; a
  wrapper script recommended by FR-502 is that kind of program), so the path MUST also be valid UTF-8 and
  contain only Unicode letters, marks and digits, the ASCII space, and the characters `/ . _ - + = @ , :`.
  None of those letters, marks or digits is shell syntax, so a home such as `/home/josé` works. Anything else
  is refused, including every shell metacharacter, `~`, `(` and `#`. A default command with no override isn't
  limited, so SC-002 holds. Otherwise `v` MUST refuse with a message, so neither value is read as an option
  and a remote daemon can't supply shell syntax. A refusal under the path limit MUST name the first rejected
  character, giving its code point when it can't be shown safely, so the developer can tell what to change.
  Spaces stay allowed because real workspace paths can contain them. A space can still split words when a
  marker sits inside a developer's own `sh -c` script, so FR-521 gives the form that avoids this.
- **FR-518**: When the editor's command can't be started (not found, not executable), the status line
  MUST name the executable (FR-532) and, for a built-in editor, the environment variable that overrides it,
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
  - that a program which re-parses its arguments, such as `sh -c`, can run a folder path as code when the
    marker is inside its script, so the path must be passed as a separate argument:
    `sh -c 'cd "$1" && nvim .' sh {path}`;
  - that a `{host}` embedded in a larger argument only works on remote sandboxes.
- **FR-522**: When Custom is selected, FR-506's checks MUST run again on the session's custom command on
  every `v` press. That is the command loaded at startup or confirmed on the settings screen, so a hand edit
  reaches `v` at the next start (FR-530). If the checks fail, `v` MUST refuse with the reason and point to
  the settings screen.
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
  - the Custom command's rules, including FR-506, FR-516, FR-521 and FR-531, with the positional
    `sh -c 'cd "$1" && nvim .' sh {path}` form as the example;
  - beside the override variables, the same re-parsing warning and FR-517's remote path limit;
  - that the command is stored in plain text, and that arguments are visible in the process list (`ps`,
    `/proc/<pid>/cmdline`) while the editor runs, so a secret belongs in a wrapper script or the environment;
  - that the settings screen is the recommended way to set a command with quotes, because the TUI handles the
    escaping. A hand-edited command is best written as a TOML literal string
    (`editor_custom_command = '"/My Editor/edit" {path}'`), or as a multi-line literal when it contains a
    single quote (`editor_custom_command = '''sh -c 'cd "$1" && nvim .' sh {path}'''`);
  - that hand edits take effect at the next start, and that a save doesn't keep comments in the file (FR-530).
- **FR-525**: When the settings file exists but can't be read or parsed at startup, the TUI MUST NOT
  overwrite it for the rest of that session, even if the file is fixed in the meantime. Defaults apply, and
  any change on the settings screen, including toggling automatic host sign-in, applies to this session only.
  The status line MUST say the change wasn't saved and that the file needs fixing **and a restart**. If the
  file parsed at startup but no longer parses when a save re-reads it (FR-530), that save MUST be refused in
  the same way, and a later save re-reads it again. The "settings could not be read" notice MUST say this and
  MUST stay on the settings screen for the session. FR-529 sets where else it appears, because `v` opens
  VS Code in this state even when the file names another editor. A fixed file takes effect at the next start.
- **FR-526**: Every check that doesn't need the workspace path MUST run **before** the daemon is asked for
  it, so `v` refuses at once even when a remote host is unreachable. These are: the host lookup (FR-520), no
  remote support (FR-514), a missing or invalid custom command (FR-522), an SSH target starting with `-`
  (FR-517), a target Zed can't use (FR-513), a home directory that can't be determined when the command needs
  it (FR-531), and an executable that can't be found (FR-518). The launch error stays the final authority,
  because the executable can change between the check and the start. The path checks in FR-517 run after the
  daemon replies.
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
  - If the value is unchanged, `esc` returns to the list with no dialog.
  - `esc` backs out **one level** at a time (input → list → settings row) and never closes the settings
    screen from inside the list or the input. List → settings row drops no typed text, so it raises no
    dialog, and moving the list cursor isn't an edit.
  - Printable keys and pasted text are text, including `q`, `,`, `j`, `k` and space. Single-line editing keys
    keep their usual meaning (`ctrl+e` moves to the end of the line), and `ctrl+c` keeps its current behaviour
    on inline inputs (SWI-9 FR-012). `enter` confirms.
- **FR-529**: The status line holds one message, so a notice the developer must read MUST be part of the
  message for the action it concerns, rather than a message of its own that the next one replaces:
  - While the unknown-editor state (FR-508) or the unreadable-settings state (FR-525) holds, the outcome
    message of **every** `v` MUST include the notice. That covers completion, refusal and failure, including
    FR-523. For example `opened abc in VS Code (unknown editor 'foo' in settings)` and
    `opened abc in VS Code (settings file can't be read, defaults apply)`.
  - FR-515's remote warning MUST be part of the completion message, for example
    `opened abc in Custom (the path is on the remote host)`.
  - One message never carries two notices. Both settings states use VS Code, the remote warning applies only
    to Custom, and an unreadable file can't name an unknown editor.
  - The two settings notices MUST also be set in the status line at startup, as best effort. A later message,
    such as the sign-in summary, may replace them, and that's accepted because `v` and the settings screen
    always show them.
  - Text from the settings file MUST be shown with control characters removed.
- **FR-530**: Every save MUST re-read the settings file, apply only the setting that changed to the values
  just read, and write the result. A toggle changes its own key, confirming a built-in editor changes the
  editor key, and confirming Custom changes the editor key and the custom command. So a hand edit or another
  running TUI's change to any other key survives. A missing file reads as defaults. Confirming Custom writes
  the command shown in the input, because the developer has just confirmed it.
  - The session's values stay as loaded at startup plus its own changes, so a hand edit reaches `v` and the
    settings screen at the next start.
  - A change that applied to the session only (FR-525, or a failed write) isn't written by a later save.
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
  - Expansion happens at launch, on each `v`, never at save. The saved text stays as typed, and a hand-edited
    command is treated the same way. If the home directory can't be determined and the command needs it, `v`
    MUST refuse with a message. The order on `v` is: split, FR-506's checks, `~`, then markers, so a
    substituted value is never expanded.
- **FR-532**: A custom command can hold a secret, so outside the command input the TUI MUST show only its
  executable (the first argument after splitting), never its other arguments. This applies to status messages
  (FR-518, FR-522, FR-523), rejection reasons (FR-506) and logs. The Editor row shows `Custom: <executable>`,
  followed by `…` when there are more arguments, or `Custom (invalid command)` when the command can't be
  split. The input shows the full text, because the developer has to edit it.

### Key Entities *(include if feature involves data)*

- **Editor**: one entry in the catalogue. It has a stable identifier (saved in settings), a display name,
  a default command, an override variable, and how it opens remote folders (Remote-SSH option, SSH
  address, or not supported).
- **Editor preference**: part of the saved settings. It holds the selected editor's identifier, plus the
  custom command line as typed. The custom command is kept even when another editor is
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
  and FR-529's notices when the settings file names an unknown editor or can't be parsed. FR-517's remote
  path limit can apply only when an override is set, and that includes an existing `SWITCHBOARD_CODE_BIN`.
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
  can't be detected and aren't covered.
- **SC-005**: For a local sandbox, or a remote path that FR-517 accepts, a custom command whose programs
  don't re-parse or execute their arguments opens the right folder, and no part of the path is ever run as a
  command. Locally this holds for a path containing spaces or shell characters, and it also holds for the
  positional `sh -c` form in FR-521. For a command with a marker inside a script it re-parses, the TUI can't
  give this guarantee. On a remote sandbox with Custom or a set override variable, a path outside FR-517's
  limit is refused before anything starts, and the note beside the input and the README warn about the rest.
- **SC-006**: After a settings file becomes unparseable, no action in the TUI changes its contents. The saved
  editor and custom command are still there once the developer fixes the file.
- **SC-007**: A hand edit made to any setting while the TUI runs is still in the file after the TUI saves a
  different setting.

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
- **Guard (FR-528):** add a surface to `guardSurface` in `formguard.go`, following `gRename`.

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
