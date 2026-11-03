# Contract: `settings.toml` (SWI-24)

The TUI's per-user settings file under `SWITCHBOARD_CONFIG_DIR` (`$XDG_CONFIG_HOME/switchboard/settings.toml`).
Written by the TUI (`store.SettingsStore`), readable and editable by hand.

## Keys

| Key                     | Type   | Default (absent) | Values |
|-------------------------|--------|------------------|--------|
| `auto_connect_hosts`    | bool   | `true`           | existing (feature 008) |
| `editor`                | string | `vscode`         | an FR-501 identifier: `vscode`, `code-insiders`, `cursor`, `windsurf`, `zed`, `webstorm`, `idea`, `pycharm`, `goland`, `sublime`, `custom` |
| `editor_custom_command` | string | (none)           | the Custom command line, raw, as typed (markers `{path}`, `{host}`) |

Example:

```toml
auto_connect_hosts = true
editor = 'custom'
editor_custom_command = '''kitty -e sh -c 'cd "$1" && nvim .' sh {path}'''
```

## Reading (startup)

- Missing file → defaults.
- Unreadable or unparseable (bad TOML, or a wrong type such as `editor = 5`) → defaults for the whole session,
  the file is **never written** this session, the settings screen shows
  `⚠ settings couldn't be read at startup, changes aren't saved — fix the file and restart (<error>)`, and
  every `v` outcome carries `settings couldn't be read at startup, changes aren't saved` (FR-525, FR-529).
- `editor` unknown → VS Code for the session; the value stays in the file until an Editor list entry is
  confirmed; notice `unknown editor '<value>' — using VS Code; other settings loaded` (value cut to 60 columns,
  control characters removed) on the settings screen and in every `v` outcome (FR-508, FR-529).
- `editor = 'custom'` with a missing, empty or invalid `editor_custom_command` → Custom stays selected; `v`
  refuses (FR-522); the Editor row shows `Custom (invalid command)` (FR-532).
- Hand edits made while the TUI runs reach the session at the **next start** (FR-530).

## Writing (every save)

1. Re-read the file (missing = empty).
2. Apply only the changed key(s): a toggle → `auto_connect_hosts`; a built-in editor → `editor`; Custom →
   `editor` and `editor_custom_command`. Other keys are written as just read; an absent key stays absent.
3. Encode and write to a fresh `os.CreateTemp` file in the same directory (mode `0600`, unique name), rename over
   `settings.toml`, then `chmod 0600` the result (FR-527).

Failure modes:

| Condition | Effect |
|-----------|--------|
| Re-read fails (unreadable/unparseable now) | nothing written; the change applies to this session only; the row shows `(not saved)`; status: `… — change not saved: fix settings.toml (<error>)` (no restart); settings screen notice until the next successful save (FR-525 state 2). |
| Write fails (permissions, disk) | toggle / Editor list: session only, `(not saved)`, `error: could not save settings: <error>`; command input: stays open with the error, session unchanged (FR-528). |
| No settings store | session only, `(not saved)`, `settings store unavailable — change applies to this session only`. |

Guarantees: comments and unknown keys are **not** preserved (FR-530); two TUIs saving at once is last-write-wins
(no lock); a value this version doesn't understand (an unknown `editor`) is never replaced by a save of another
key (SC-006, SC-007).

## Hand-editing a command

Prefer the settings screen for a command with quotes. By hand, use a TOML literal string, or a multi-line literal
when the command contains a single quote:

```toml
editor_custom_command = '"/Applications/My Editor.app/bin/edit" {path}'
editor_custom_command = '''kitty -e sh -c 'cd "$1" && nvim .' sh {path}'''
```

The command is stored in plain text and its arguments are visible in `ps` while the editor runs; a secret belongs
in a wrapper script or the environment (FR-524).
