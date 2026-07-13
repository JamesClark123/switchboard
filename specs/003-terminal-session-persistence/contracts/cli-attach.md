# CLI Contract: `sxb attach` and workspace auto-open

Feature 003 adds one client-facing invocation mode and one auto-open behavior to the `sxb` binary
(`src/apps/switchboard-tui/cmd/sxb/main.go`). This is the user-facing contract for US3 (external
terminal) and US4 (VSCode workspace path). Wire details are in `switchboard-terminal.proto`; policy
rationale is in `research.md` R5/R6.

## `sxb attach` — attach mode (full-screen single session)

```
sxb attach --host <host-id|known-host> --sandbox <sandbox-id>
```

- Opens a full-screen terminal attached to the sandbox's persistent session: renders the
  snapshot immediately, then streams live output; forwards keystrokes and window-resize.
- Sends `AttachInfo{kind = CLIENT_KIND_EXTERNAL, initial_size}` as the first frame.
- On `FAILED_PRECONDITION` ("external already open"), prints a clear message and exits non-zero
  (the spawning TUI interprets this as "focus the existing window instead").
- Detaching (a documented key, e.g. the configured detach chord, or closing the window) leaves the
  session **running** on the daemon (FR-002); it does not stop the sandbox.
- Exit code `0` on clean detach; non-zero on connect/precondition error.

**Who calls it**: the TUI spawns this inside a platform terminal window when the user presses **T**
(US3). It is also the mode entered by the auto-open path below.

## Bare `sxb` — workspace auto-open (US4)

```
sxb            # run with no subcommand
```

Resolution order:
1. Walk up from `cwd` looking for `.switchboard/session.json` (the workspace marker, R6).
2. **Marker found + sandbox running** → behave as `sxb attach --host <marker.host> --sandbox
   <marker.sandbox>` (attach in-place; FR-017). Nested subdirectories resolve to the same sandbox
   (FR-018). The daemon `ResolveWorkspace` RPC MAY be used to confirm/repair a stale marker.
3. **Marker found + sandbox stopped/unknown** → print an actionable message (e.g. "sandbox <id> is
   stopped; run `sxb` to manage it") and either exit or open the general TUI (FR-020).
4. **No marker** → open the general TUI, unchanged from today (FR-019).

## External-terminal spawn & bring-to-front (TUI-side, US3)

Pressing **T** on a sandbox in the TUI:
- If **no external terminal** is tracked for that sandbox and the daemon reports
  `external_attached == false`: spawn the platform terminal emulator running `sxb attach … EXTERNAL`,
  and record the spawned window/process against the sandbox id.
- If an external terminal **is** open (tracked locally, or `external_attached == true`): do **not**
  spawn a second one — bring the tracked window to the foreground. If it can't be located (closed
  out-of-band), spawn a fresh one (FR-014/015; spec edge case).

Platform behavior (best-effort, degrade to a message where unavailable):
- **macOS**: launch via `open -a <terminal>`; focus via `open`/AppleScript `activate`.
- **Linux**: launch via `$TERMINAL` (or a detected emulator); focus via the emulator or
  `wmctrl`/`xdotool` when present.

## List-page surfaces (US3, US5)

- Each sandbox row shows its **connected-terminal count** (`Sandbox.attached_terminals`) and its
  **tag** (`Sandbox.tag`); both update live via the existing `Event.sandbox_changed` stream.
- A tag-edit key opens an inline editor → `SetSandboxTag`. Lowercase **t** opens the in-place
  terminal view (US2); uppercase **T** is the external-terminal action above.
