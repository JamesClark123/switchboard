# Contract: editor launch (SWI-24)

What `v` starts for each editor, the Custom command grammar, every check and its message. Owned by
`src/apps/switchboard-tui/internal/editor`; the message texts are assembled in `ui/editor_open.go` from the typed
`Refusal`/`Plan`. SC-003's test asserts §1 row by row.

## 1. Argv per editor

`<bin>` is the override variable's raw value when set (non-blank), else the default command. `<target>` is the
host's SSH target as recorded; `<path>` the workspace folder from `GetVSCodeTarget`.

| ID              | Local argv              | Remote argv                                   | Verified |
|-----------------|-------------------------|-----------------------------------------------|----------|
| `vscode`        | `code <path>`           | `code --remote ssh-remote+<target> <path>`    | today's behaviour |
| `code-insiders` | `code-insiders <path>`  | `code-insiders --remote ssh-remote+<target> <path>` | same CLI as VS Code |
| `cursor`        | `cursor <path>`         | `cursor --remote ssh-remote+<target> <path>`  | **documentation-derived** (research F6); quickstart Scenario 0 |
| `windsurf`      | `windsurf <path>`       | `windsurf --remote ssh-remote+<target> <path>`| **documentation-derived** (research F6); quickstart Scenario 0 |
| `zed`           | `zed <path>`            | `zed ssh://<normalised target><encoded path>` | from Zed's source (research F5) |
| `webstorm`      | `webstorm <path>`       | refused (§4 `no-remote`)                      | — |
| `idea`          | `idea <path>`           | refused                                       | — |
| `pycharm`       | `pycharm <path>`        | refused                                       | — |
| `goland`        | `goland <path>`         | refused                                       | — |
| `sublime`       | `subl <path>`           | refused                                       | — |
| `custom`        | §2                      | §2                                            | — |

With no settings and no new variables, `vscode` is selected and its argv is byte-identical to today's (SC-002).

### Zed address (FR-513)

`ZedAddress(target, path)`:

1. Strip one leading `ssh://` from `target`.
2. Split the user at the **last** `@` (`user@host[:port]` → user, hostport); no `@` → no user.
3. If hostport has more than one `:` and doesn't start with `[`, wrap it in brackets (bare IPv6, no port).
4. Build `url.URL{Scheme: "ssh", User: url.User(user), Host: hostport, Path: path}` and take `String()`.
5. Parse the string back; require the same user, host and path. Otherwise refuse `zed-target` (a target with
   `/`, `?`, `#`, or that can't be an authority).
6. The `-` check (FR-517) runs on `target` as given and on the normalised user and host.

Examples: `user@box` + `/srv/ws/my proj` → `ssh://user@box/srv/ws/my%20proj`; `2001:db8::1` + `/x` →
`ssh://[2001:db8::1]/x`; `u@[2001:db8::1]:22` → `ssh://u@[2001:db8::1]:22/x`; `/a/b#c` → `/a/b%23c`; `/ü` →
`/%C3%BC`; `user@box/evil` → refused.

## 2. Custom command

### Grammar (FR-531)

`Split(text) ([]Word, error)`:

- Spaces and tabs outside quotes separate words. Adjacent quoted/unquoted pieces join. `""` / `''` is an empty
  word.
- Single quotes: every character literal, no escapes.
- Double quotes: `\` before `"`, `\`, `$` or `` ` `` yields that character; any other `\` stays literal.
- Unquoted `\` makes the next character literal (`/My\ Editor/edit` is one word; `\$`, `\&` pass).
- `Word.TildeOK` is true when the word began with an unquoted, unescaped `~` and is exactly `~` or starts `~/`.
- Errors (rule names in §4): unbalanced quote → `unbalanced-quotes`; trailing unquoted `\` → `trailing-backslash`;
  a control character other than tab → `control-character`; an unquoted or double-quoted `$` or `` ` ``, or an
  unquoted `|`, `;`, `&`, `<`, `>` → `shell-syntax`.

### Checks after splitting (FR-506)

`Check(words)`: no words → `empty`; first word `""` → `empty-executable`; first word contains `{path}` or
`{host}` → `marker-in-executable`; first word matches `^[A-Za-z_][A-Za-z0-9_]*=` → `assignment` (the check is on
the split word, so quoting doesn't matter). Globs and every other character are literal.

### Expansion and substitution, on each `v` (FR-515, FR-516, FR-531)

Order: `Split` → `Check` → `Expand(home)` → `Substitute(path, host)`.

- `Expand`: a word with `TildeOK` becomes `home` or `home + rest`; `home == ""` with such a word → refuse
  `no-home`. Nothing else expands.
- `Substitute`: one pass over the original words. In every word, `{path}` → path and `{host}` → target (empty
  when local). A word that is exactly `{host}` on a local sandbox is dropped. If no word contained `{path}`, the
  path is appended as the last argument. A marker inside a substituted value is not expanded again. Markers
  can't be escaped.
- Remote with no `{host}` in any word → `Plan.RemoteNoHost = true` (completion warns).

### Saved vs session (FR-522, FR-530)

The settings screen saves the trimmed input text after `Split` + `Check` pass. `v` re-runs `Split` + `Check` on
the session's command every time; a failure refuses with the rule and `Where = "settings screen"`.

## 3. Checks before and after the daemon call (FR-526)

`Precheck(req)` (no path; on the update loop, then `LookPath` inside the command):

1. `req.HostErr != nil` → `host-lookup`, for every editor.
2. Remote and `Remote == RemoteNone` → `no-remote`.
3. Custom: `Split`/`Check` failure → that rule (`Where = settings screen`); `Expand` needing an unknown home →
   `no-home`.
4. Remote: target starts with `-` → `target-dash`; Zed: `ZedAddress` fails → `zed-target` or `target-dash` on the
   normalised parts.
5. Executable resolved: `Command(editor, bins)` or the custom first word (after `~` expansion). The caller runs
   `exec.LookPath` inside the command → `not-found` (FR-518) with the OS reason.

`Resolve(req)` (with path):

6. Path not absolute, or contains a control character (including newline) → `path-unsafe`.
7. Remote **and** (Custom or `Command(editor, req.Bins)` reports `overridden`, i.e. the selected editor's own
   variable is non-blank) → every rune must be valid UTF-8 and one of:
   Unicode letter, mark, digit, ASCII space, `/ . _ - + = @ , :`. First failure → `path-limited` with
   `Detail = 'x'` or `U+XXXX` when not printable.
8. Build argv per §1/§2.

The launch error (`Launch` → `Start` fails) stays the final authority → `not-found` / `launch-failed` with the OS
reason.

## 4. Rules and messages

`<exe>` is the executable cut in the middle to 60 columns; `<reason>` is the OS error's innermost text cut to 40
columns; `<err>` outside text cut to 120 columns; `<n>` the sandbox's short id; `<var>` the override variable.
Every `v` failure or refusal shows in the error style. Notices (FR-529) are appended as ` (<n1>; <n2>)`.

| Rule                   | Status text | Where |
|------------------------|-------------|-------|
| completion             | `opened <n> in <Name>` (+ notices; Custom remote with no `{host}` adds `the path is on the remote host` last) | — |
| `host-lookup`          | `can't open <n>: host entry unavailable (<err>) — check the hosts file` | hosts |
| `no-remote`            | `<Name> can't open folders on remote hosts — switch editors on the settings screen (,)` | settings |
| `not-found` (built-in) | `<n>: can't start <exe>: <reason> — set <var> to one executable (wrapper script or Custom for arguments)` | env var |
| `not-found` (Custom)   | `<n>: can't start <exe>: <reason> — fix the Custom command on the settings screen (,)` | settings |
| `launch-failed`        | same shape as `not-found` | as above |
| `empty`                | `no Custom command is set — set one on the settings screen (,)` | settings |
| `empty-executable`     | `the Custom command has an empty first argument — fix it on the settings screen (,)` | settings |
| `marker-in-executable` | `the Custom command's first argument contains a marker — the program can't be {path} or {host}; fix it on the settings screen (,)` | settings |
| `assignment`           | `the Custom command starts with NAME=value, which nothing sets — use a wrapper script; fix it on the settings screen (,)` | settings |
| `shell-syntax`         | `the Custom command doesn't run in a shell, so $VAR, backticks and unquoted \| ; & < > aren't allowed — use sh -c or a wrapper script as the note shows; fix it on the settings screen (,)` | settings |
| `unbalanced-quotes`    | `the Custom command has an unbalanced quote — fix it on the settings screen (,)` | settings |
| `trailing-backslash`   | `the Custom command ends in a backslash — fix it on the settings screen (,)` | settings |
| `control-character`    | `the Custom command contains a control character — re-enter it on the settings screen (,)` | settings |
| `no-home`              | `the Custom command uses ~ but the home directory can't be determined — set HOME or use a full path` | env |
| `target-dash`          | `the host's SSH target starts with '-' and can't be passed to an editor — fix the host entry` | host entry |
| `zed-target`           | `the host's SSH target can't be used in an ssh:// address — fix the host entry` | host entry |
| `path-unsafe`          | `the workspace path from the daemon isn't absolute or contains control characters — fix it on the daemon` | daemon |
| `path-limited`         | `the workspace path contains '<c>', which isn't allowed when <Custom \| <var> is set> opens a remote sandbox — move the workspace or unset the override` | daemon / env |
| exit report (FR-523)   | `<exe> exited with status <s> (<signal>) after opening <n> — check <var> \| the Custom command on the settings screen (,)` | env var / settings |

On the settings screen the same rule names map to the input's rejection text (no `(,)` suffix, no `fix it on the
settings screen`): e.g. `shell-syntax` → `the command doesn't run in a shell: $VAR, backticks and unquoted | ; & < >
aren't allowed — use sh -c or a wrapper script (see the note)`; `empty` → `a command is required`. Rejections and
refusals never quote any part of the command (FR-532).

Every message must fit in 4 lines at 80 columns with the values at their limits (the room on the sandbox list at
80×24 with the banner; SC-004's test, T040, is the authority). The tightest case is `not-found` for a built-in
editor with a 60-column executable, a 40-column reason, `SWITCHBOARD_CODE_INSIDERS_BIN` and the 60-column
unknown-editor notice: about 310 characters, and a 60-column executable can't break at a space, so it sits on the
edge. T028 measures it; if it exceeds 4 lines the reminder shortens first (`(or use Custom)`), then the lead
(`<n>: <exe>: <reason>`). The FR-523 report, `host-lookup`, `path-limited` and `shell-syntax` are near 4 lines
too, and the same rule applies. The parts named above (cause, where to fix it, the notices) must stay in every
message.

## 5. Environment variables (FR-502, FR-503)

| Variable                         | Default         | Editor |
|----------------------------------|-----------------|--------|
| `SWITCHBOARD_CODE_BIN`           | `code`          | VS Code (existing) |
| `SWITCHBOARD_CODE_INSIDERS_BIN`  | `code-insiders` | VS Code Insiders |
| `SWITCHBOARD_CURSOR_BIN`         | `cursor`        | Cursor |
| `SWITCHBOARD_WINDSURF_BIN`       | `windsurf`      | Windsurf |
| `SWITCHBOARD_ZED_BIN`            | `zed`           | Zed |
| `SWITCHBOARD_WEBSTORM_BIN`       | `webstorm`      | WebStorm |
| `SWITCHBOARD_IDEA_BIN`           | `idea`          | IntelliJ IDEA |
| `SWITCHBOARD_PYCHARM_BIN`        | `pycharm`       | PyCharm |
| `SWITCHBOARD_GOLAND_BIN`         | `goland`        | GoLand |
| `SWITCHBOARD_SUBLIME_BIN`        | `subl`          | Sublime Text |

A set value is one executable, used as given (no splitting, no `~`/`$VAR` expansion); blank counts as unset.
Declared in `config.schema` from the catalogue, listed in `.env.example` and the README (env-check lockstep).

## 6. Launch (FR-511, FR-523)

`Launch(argv)`: `exec.Command` with `SysProcAttr{Setsid: true}`, nil stdin/stdout/stderr (`/dev/null`), the
TUI's environment, `Start`. A goroutine `Wait`s until the editor exits (so it's always reaped); a second selects
on that result and a 3 s timer: a non-zero exit or signal before the timer is sent on `Report`, otherwise the
channel is closed. The TUI never blocks on the editor. The exit report replaces the status (and is held like
any `v` failure) and doesn't touch other rows or reload.
