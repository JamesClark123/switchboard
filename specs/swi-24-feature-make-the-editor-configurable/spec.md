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
sandboxes on remote hosts) and `specs/008-mcp-gateway-manager` (FR-101: the TUI settings screen).
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
   pressed, **Then** that command is started instead of the default.
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
   asked for a command, with the current custom command (if any) filled in, and a short note explains the
   folder marker.
2. **Given** a custom command containing the folder marker, **When** `v` is pressed, **Then** the command
   runs with the marker replaced by the sandbox's workspace folder, as one argument even if the folder
   path contains spaces.
3. **Given** a custom command with no folder marker, **When** `v` is pressed, **Then** the folder is added
   as the last argument.
4. **Given** the developer submits an empty custom command, **When** they confirm, **Then** the setting is
   not saved, and they are told a command is required. The previous editor choice stays in force.
5. **Given** Custom is selected, **When** the settings screen is shown, **Then** the editor row shows
   "Custom" and the saved command.

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
   machine. A custom command with no host marker gets the folder path only, and the status line warns that
   the path is on the remote host.

---

### Edge Cases

- **Command not found.** The selected editor's command (default or from its variable) is not installed or
  not on the PATH. `v` reports an error in the status line that names the command that could not be
  started and the environment variable that sets it (for Custom, the settings screen instead). The TUI
  does not crash and does not hang.
- **Unknown saved editor.** The settings file names an editor this version doesn't know (for example a
  file written by a newer version, or edited by hand). The TUI uses VS Code for this session and shows a
  notice of its own: "unknown editor '<value>' — using VS Code; other settings loaded". Every other saved
  setting loads as usual. The unknown value stays in the file until the developer changes the **Editor**
  row. Toggling another setting writes the unknown value back unchanged.
- **Downgrade.** An older TUI that saves the settings file drops the editor entries it doesn't know.
  This loss is accepted.
- **Older settings file.** A settings file written before this feature has no editor entry. The editor
  defaults to VS Code, and automatic host sign-in keeps its saved value.
- **Custom selected but the command is missing.** Custom is selected but the saved command is empty or
  missing. `v` reports that no custom command is set and points to the settings screen. It does not fall
  back to another editor without saying so.
- **Quoting in the custom command.** The custom command contains quoted arguments (for example
  `"/Applications/My Editor.app/bin/edit" {path}`). It is split into arguments the way a shell would
  split it, honouring quotes. It is **not** run through a shell, so a folder path with special characters
  can never be read as shell syntax.
- **Empty variable.** An editor's command variable is set but empty or only spaces. That counts as unset,
  and the default command is used.
- **Settings store unavailable.** The settings file can't be written. The choice applies to this session
  only and the status line says so, the same as the existing sign-in setting does.
- **The editor exits at once.** Some editor commands hand the work to an already running window and exit
  straight away. That counts as success. The TUI does not wait for the editor to close.

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
- **FR-503**: Every override variable MUST appear in the client's declared configuration (the same list
  the env check and documentation read from), each with its default and the editor it belongs to.

**Choosing the editor**

- **FR-504**: The settings screen MUST have an **Editor** setting that shows the selected editor's display
  name (and, for Custom, the saved command). It MUST default to VS Code when nothing is saved.
- **FR-505**: Activating the Editor setting MUST let the developer choose one entry from FR-501's list,
  with the current choice marked. They confirm with one key and cancel with `esc`, which leaves the
  setting unchanged.
- **FR-506**: Choosing Custom MUST ask for a command line, with the saved custom command filled in when one
  exists. The command MUST be rejected with a message when it is empty, contains only spaces, has
  unbalanced quotes, or splits into no arguments. When it is rejected, the previous editor choice MUST
  stay in force. `enter` confirms and `esc` cancels, both in the editor list and in the command input.
- **FR-507**: The chosen editor (and the custom command, if any) MUST be saved per user, client-side,
  through the same settings store and file as the existing settings. It MUST take effect for the next `v`
  press with no restart, and it MUST survive a restart.
- **FR-508**: A settings file with no editor entry MUST load as VS Code without changing any other saved
  setting. A settings file naming an unknown editor MUST load as VS Code for the session, MUST load every
  other setting as saved, and MUST show a notice naming the unknown value. The notice MUST be separate from
  the "settings could not be read" notice. The saved editor value and custom command MUST be written back
  unchanged on every save until the developer changes the Editor row itself. For example, toggling
  automatic host sign-in MUST NOT replace an unknown editor value.
- **FR-509**: The existing on/off setting (automatic host sign-in) MUST keep its current behaviour and
  key bindings on the settings screen. The help footer MUST describe the keys for both kinds of rows.

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
  MUST be confirmed against Zed's documentation or source during planning.
- **FR-514**: For a remote sandbox and an editor without remote-folder support, `v` MUST NOT start
  anything, and the status line MUST say that the selected editor cannot open folders on remote hosts and
  that the editor can be changed on the settings screen.
- **FR-515**: A Custom command MUST support two markers: `{path}` for the workspace folder and `{host}`
  for the SSH target (empty for a local sandbox). When `{path}` is missing, the folder MUST be added as the
  last argument. When the sandbox is remote and the command has no `{host}` marker, the command MUST
  still run, and the status line MUST warn that the path is on the remote host.
- **FR-516**: A Custom command MUST be split into arguments with shell-style quoting rules and run
  **without** a shell. Each marker MUST be replaced inside its argument, so a substituted value can never
  add or split arguments. For a local sandbox, an argument that is exactly `{host}` and nothing else is
  dropped. Any other argument containing `{host}` gets an empty string in its place. Markers cannot be
  escaped, so a literal `{path}` or `{host}` in the command is not supported.
- **FR-517**: Before any editor is started, the workspace folder MUST be an absolute path. The SSH
  target MUST NOT start with `-`. Otherwise `v` MUST refuse with a message, so a value supplied by a
  remote daemon is never read as an option.
- **FR-518**: When the editor's command can't be started (not found, not executable), the status line
  MUST name the command and, for a built-in editor, the environment variable that overrides it. For
  Custom, it MUST point to the settings screen.
- **FR-519**: The sandbox list's help footer and the status message shown while opening MUST name the
  selected editor instead of a fixed "vscode". The footer uses the editor's identifier (FR-501), and
  "custom" for Custom. With VS Code selected, both MUST read exactly as they do today.
- **FR-520**: A sandbox is **remote** when its host has an SSH target. This is the same rule that decides
  today whether VS Code opens the folder over Remote-SSH. Every "remote" in this spec means this.

### Key Entities *(include if feature involves data)*

- **Editor**: one entry in the catalogue. It has a stable identifier (saved in settings), a display name,
  a default command, an override variable, and how it opens remote folders (Remote-SSH option, SSH
  address, or not supported).
- **Editor preference**: part of the saved settings. It holds the selected editor's identifier, plus the
  custom command line when Custom is selected. The custom command is kept even when another editor is
  selected, so switching back to Custom fills it in again.
- **Open request**: what one `v` press resolves to. It holds the sandbox's workspace folder, the host's
  SSH target (empty for local), the selected editor, and the resolved command. From these it produces
  the exact command and arguments to start, or a reason why the editor cannot be opened.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A developer can switch the `v` editor from VS Code to any other listed editor in under 15
  seconds from the sandbox list, without editing any file or environment variable.
- **SC-002**: With no new settings and no new environment variables, `v` behaves identically to before
  this feature for 100% of local and remote sandboxes.
- **SC-003**: For every editor in the catalogue, the command and arguments started for a local sandbox,
  and for a remote sandbox where supported, match the editor's documented form. This is checked by an
  automated test for each editor.
- **SC-004**: Every failed `v` press shows a status message that names the cause and where to fix it,
  and the TUI stays usable. The causes are: command missing, remote not supported, no custom command, a
  workspace path that isn't absolute, and an SSH target starting with `-`. For the last two, "where to fix
  it" is the daemon or the host entry. A rejected custom command on the settings screen (FR-506) also
  says why it was rejected.
- **SC-005**: A custom command with a folder path containing spaces or shell characters opens the right
  folder, and no part of the path is ever run as a command.

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
  The TUI can't detect a failure inside the editor once the editor has started. Windsurf's `--remote`
  support is to be confirmed during planning.
- Editors reach the remote host through the developer's own SSH configuration, as VS Code does today. A
  host's switchboard-specific SSH options (for example `-i <key>`) are not passed to editors.
- The key stays `v`. Its help text changes to name the selected editor.
