# Quickstart: Configurable Editor (SWI-24)

Validation scenarios for the implemented feature. Argv forms, rules and messages are in
[contracts/editor-launch.md](./contracts/editor-launch.md); the file in
[contracts/settings-file.md](./contracts/settings-file.md); the screen, status line and hold in
[contracts/settings-ui.md](./contracts/settings-ui.md).

## Prerequisites

- `sxbd` and `sxb` built from the implement branch (`make build`), a local daemon with at least one sandbox.
- For the remote scenarios: a saved SSH host with a sandbox on it.
- A stand-in editor script that logs its argv, e.g. `~/bin/logedit`:
  `#!/bin/sh` / `printf '%s\n' "$@" >> /tmp/logedit.log`. Variants: `exit 3` for Scenario 6, `sleep 10` for a
  long-running editor.
- Automated gates: `make fmt-check vet lint test cover` (≥ 90 % per module), `make env-check`, `make e2e`.

## Scenario 0 — Vendor CLI reconciliation (documentation-derived rows)

With a real install of each, run the remote argv from the contract's §1 by hand and record the result in that
table's *Verified* column with the date:

1. **Windsurf**: `windsurf --remote ssh-remote+<target> <path>` opens the folder on the host. If Windsurf's
   authority prefix differs, change its catalogue row (research R8) and the contract.
2. **Cursor**: `cursor --remote ssh-remote+<target> <path>`.
3. **Zed**: `zed 'ssh://<target>/<path with a space>'` opens the right folder (the address form is from Zed's
   source; this confirms the installed version agrees).

Expected: each opens the workspace folder on the remote host, or the row is corrected.

## Scenario 1 — Pick an editor (US1)

1. Start `sxb` with no `settings.toml`. Press `,`. **Expect** `Editor: VS Code`; `v`'s footer entry reads `vscode`.
2. Select the Editor row, `enter`, move to WebStorm, `enter`. **Expect** back on the rows, `Editor: WebStorm`,
   `saved settings`; `settings.toml` contains `editor = 'webstorm'`; footer reads `webstorm`.
3. With `SWITCHBOARD_WEBSTORM_BIN=~/bin/logedit` (full path), press `v` on a local sandbox. **Expect**
   `opened <n> in WebStorm`, the row's spinner cleared, no list reload, and `/tmp/logedit.log` holding the
   workspace path as one line. The TUI stays responsive.
4. Quit and restart. **Expect** WebStorm still selected. Press `esc` in the list after moving the cursor.
   **Expect** nothing saved.
5. Confirm the already-marked entry with `enter`. **Expect** `saved settings` (always saves).

## Scenario 2 — Override variables (US2)

1. Unset every `SWITCHBOARD_*_BIN`; select Zed; `v`. **Expect** `zed <path>` (or `can't start zed …` naming
   `SWITCHBOARD_ZED_BIN` if Zed isn't installed, with the reason cut to 40 columns).
2. `SWITCHBOARD_ZED_BIN="/path/with space/logedit"`; `v`. **Expect** that one executable started, never split.
3. `SWITCHBOARD_ZED_BIN="  "`; `v`. **Expect** the default `zed`.
4. `SWITCHBOARD_CODE_BIN=~/bin/logedit` with VS Code selected. **Expect** honoured as before this feature.
5. `make env-check` and the README table. **Expect** every variable listed with its default and editor.

## Scenario 3 — Custom command (US3)

1. Editor list → Custom. **Expect** the input with the note; the previous command filled in if any.
2. Type `~/bin/logedit --open {path} extra` and `enter`. **Expect** rows, `Editor: Custom: ~/bin/logedit…`;
   `v` logs `--open`, the path, `extra`. The path is one argument even with spaces.
3. Set `logedit` with no marker. **Expect** the path appended last.
4. Submit an empty command. **Expect** `a command is required` in red; input open; the previous editor still in
   force (`v` uses it).
5. Submit `cd {path} && nvim .`. **Expect** the shell-syntax rejection naming `sh -c` or a wrapper script; the
   command isn't quoted in the message.
6. Type a change, press `esc`. **Expect** the unsaved-changes dialog; `d` returns to the list with nothing
   saved; repeat with `s` and **expect** it to act like `enter`.
7. Hand-edit `editor_custom_command = 'API_TOKEN=abc123 logedit {path}'`, restart. **Expect** the row shows
   `Custom (invalid command)`; `v` refuses naming the `NAME=` rule; `abc123` appears nowhere.
8. `kitty -e sh -c 'cd "$1" && nvim .' sh {path}` with a workspace path containing `$(touch /tmp/pwned)`
   (local). **Expect** no `/tmp/pwned` (SC-005 holds for the positional form).

## Scenario 4 — Remote sandboxes (US4)

1. Cursor selected, `v` on a remote sandbox. **Expect** `cursor --remote ssh-remote+<target> <path>` (log it with
   `SWITCHBOARD_CURSOR_BIN=~/bin/logedit`).
2. Zed selected, path with a space. **Expect** one argument `ssh://<target>/<path>` with `%20`.
3. WebStorm selected. **Expect** the `can't open folders on remote hosts` refusal, in red, nothing started, and
   it arrives before any daemon call (stop the remote daemon first: the refusal is still immediate).
4. Custom `logedit --host {host} {path}`. **Expect** both substituted; with no `{host}` the completion ends with
   `(the path is on the remote host)`. On a local sandbox a bare `{host}` argument is dropped.
5. Rename the host entry's target to `-oProxyCommand=…`. **Expect** the `target-dash` refusal for every editor
   with remote support (VS Code family, Zed, Custom); WebStorm still refuses with `no-remote`, which runs first.
6. Corrupt `hosts.toml` (or remove the entry) while the TUI runs. **Expect** `host entry unavailable` for every
   editor; the sandbox isn't treated as local.
7. `SWITCHBOARD_CODE_BIN=~/bin/logedit`, VS Code, a remote workspace path containing `(`. **Expect** the
   `path-limited` refusal naming `'('` and the variable; unset it, or set it to `code` (the default) → opens as today.

## Scenario 5 — The settings file (FR-525, FR-527, FR-530)

1. `ls -l settings.toml` after any save. **Expect** `-rw-------`; no leftover temp file. Pre-create
   `settings.toml` as `0644` and `.settings.toml-stale.tmp` as `0644`; save. **Expect** `0600`, the leftover
   untouched.
2. While the TUI runs, hand-edit `editor = 'goland'`, then toggle sign-in. **Expect** the file keeps `goland` and
   the new toggle; the session still uses the old editor until restart.
3. Write `editor = 'future-ide'`, start. **Expect** the unknown-editor notice on the settings screen and in every
   `v` message; toggling sign-in keeps `future-ide` in the file; confirming VS Code in the list replaces it and
   clears the notice.
4. Write `editor = 5`, start. **Expect** the unreadable notice with "restart"; toggle sign-in → `(not saved)`;
   fix the file; toggle again → still not written (restart needed); `v` adds the notice to its message.
5. Start with a good file, then break it; toggle. **Expect** "fix the file" with no restart, `(not saved)`; fix
   it; toggle twice. **Expect** the second press saves the original value and clears the mark.
6. Break the file, confirm Custom with a command. **Expect** the input closes, the row shows `(not saved)`; fix
   the file, confirm Zed. **Expect** Zed saved, mark cleared; choose Custom again → the input is pre-filled.

## Scenario 6 — Failures and the status line (FR-518, FR-523, FR-533, FR-534)

1. Select a built-in editor whose command isn't installed; `v`. **Expect** the not-found message naming the
   executable, the reason and the variable; it's red; the TUI is usable.
2. `SWITCHBOARD_GOLAND_BIN=~/bin/exit3`; `v`. **Expect** `opened <n> in GoLand`, then within 3 s the exit report
   with status 3 and the variable, replacing it; the completion never overwrites the report; other rows' spinners
   untouched. With `sleep10` → no report.
3. At 80×24 with the update banner showing, trigger the longest messages (a 120-column daemon error with the
   unknown-editor notice; a 60-column executable in the not-found message). **Expect** the header and the whole
   message on screen, wrapped at spaces; the list shrinks to make room.
4. Go to Daemons with 7+ daemons during a long message. **Expect** the message cut with `…`, header on screen;
   back on the list the whole message shows.
5. Settings screen at 80×24: two notices, a long executable `(not saved)`, the open list, the open input with a
   rejection. **Expect** nothing cut at the edge, header visible, the whole note visible with the input open.

## Scenario 7 — Held `v` messages (FR-533, SC-008)

1. Press `v` on a sandbox whose editor exits non-zero after 2 s, then `t` at once. **Expect** the terminal opens
   at its normal size; the report, if it arrives while open, is cut to 2 lines and the grid isn't resized;
   `ctrl+q` → the report shows in full, red, instead of `detached from …`.
2. WebStorm on a remote sandbox: `v` (refused), then `t`. **Expect** on `ctrl+q` the refusal is back in red; the
   next plain status isn't red.
3. Same, but make `t` fail (daemon unreachable). **Expect** `terminal: <reason>; <refusal>` in red; press `t`
   again at once. **Expect** only the terminal error.
4. A sandbox with a detached agent: `v` (refused), `t` → Replace dialog. Decline → the refusal shows. Repeat and
   accept after 5 s → the refusal shows once the replacing terminal closes.
5. Wait 4 s after a refusal, then `t`. **Expect** the refusal is not held (replaced by `opening terminal…`).
