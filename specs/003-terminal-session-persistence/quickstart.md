# Quickstart & Validation: Terminal Session Persistence & Sandbox Tags

Runnable validation for feature 003, mapped to the spec's user stories and Success Criteria. Assumes
feature 001 is built and working (a `sxbd` daemon and the `sxb` TUI, with at least one launchable
sandbox via `sbx`). Build details and the workspace layout are in `plan.md`; contracts are in
`contracts/`.

## Prerequisites

```bash
# From repo root — build the workspace (feature 001 toolchain)
go build ./...
# Regenerate proto after applying the contract delta (contracts/switchboard-terminal.proto):
bash src/libs/switchboard-proto/gen.sh
go test ./...          # unit + integration, 90% coverage floor (Rule VI)
```

Start a daemon and launch a sandbox with an agent (as in feature 001) so there is a running sandbox
to attach to. Note its `<sandbox-id>` from the TUI list.

## Scenario 1 — Drop out / jump back in with prior output (US1 · FR-001..005 · SC-001/002)

1. In the TUI, press **t** on the running sandbox to open its terminal; run a command that produces
   scrolling output and start a long-running AI prompt.
2. Press back to leave the terminal view, then **close the whole TUI** (`sxb` exits).
3. From the sandbox itself (`sbx exec <sandbox-id> …`), confirm the prompt/command **is still
   running** after the TUI closed. → **SC-001**.
4. Relaunch `sxb`, press **t** on the same sandbox.
   - **Expected**: the terminal shows the **prior output** (what you typed + what was produced),
     reflecting current state — never a blank screen or a fresh shell. → **FR-003, SC-002**.
5. Repeat detach/reattach a few times.
   - **Expected**: every reattach lands on the **same continuous session** (history intact), not a
     new one. → **FR-004**.

## Scenario 2 — In-place terminal view, no TUI restart (US2 · FR-009..012 · SC-003)

1. From the sandbox list, press **t**; interact; press back.
2. Repeat several times, quickly.
   - **Expected**: the terminal opens **inside** the running TUI and returns to the list without the
     TUI process restarting; the list is exactly as it was; each transition is well under 2 seconds.
     → **SC-003**.
   - Contrast with the old behavior (stop TUI → open sandbox → rerun TUI), which must no longer occur.

## Scenario 3 — One external terminal + live count (US3 · FR-007/008/013..016 · SC-004/005)

1. Press **T** on the sandbox → an external terminal window opens attached to the same session
   (snapshot then live). The list row's **connected-terminal count** increments. → **FR-013, SC-005**.
2. Press **T** again on the same sandbox.
   - **Expected**: **no second window**; the existing external terminal is brought to the foreground.
     → **FR-014/015, SC-004**.
3. With the external terminal open, press **t** to also open the in-TUI view.
   - **Expected**: both may attach to the same session (permitted, not required); the count reflects
     both. → **FR-016**.
4. Close the external terminal.
   - **Expected**: the count decrements within one refresh; the session keeps running. → **SC-005**.

## Scenario 4 — Auto-open from a workspace directory (US4 · FR-017..020 · SC-006)

1. `cd` into the sandbox's controlled workspace copy (or a **nested subdirectory** of it) — the same
   path a VSCode window/integrated terminal would sit in.
2. Run `sxb` with no arguments.
   - **Expected**: it opens **directly** into that sandbox's active terminal session, no TUI
     navigation. From a nested subdir it still resolves the owning sandbox. → **FR-017/018, SC-006**.
3. `cd` somewhere outside any workspace and run `sxb`.
   - **Expected**: the general TUI opens as before. → **FR-019**.
4. Stop the sandbox, then run `sxb` from its (now stale) workspace dir.
   - **Expected**: an actionable message / fallback to the TUI, not a hang. → **FR-020**.

## Scenario 5 — Tag a sandbox (US5 · FR-021..024 · SC-007/008)

1. On the list, open the tag editor for a sandbox and set a tag (e.g. `auth-refactor`).
   - **Expected**: the tag shows on the list row; no other attribute changed (state, id,
     display_name untouched). → **FR-021/023, SC-007**.
2. Change the tag, then clear it (empty).
   - **Expected**: list updates each time within one refresh; clearing removes it. → **FR-022**.
3. Give a **second** sandbox the **same** tag.
   - **Expected**: both accept it (tags need not be unique); each remains identifiable by its
     permanent name/id. → **FR-022**.
4. Restart the TUI, and separately stop+restart the tagged sandbox.
   - **Expected**: the tag persists across both. → **FR-024, SC-008**.

## Automated coverage (what CI asserts)

- **Unit** (`services/switchboardd/internal/terminal`): broadcaster fan-out to N fake clients;
  snapshot reproduces current screen; scrollback ring stays within its byte bound; resize arbiter
  returns smallest-of-attached; second `EXTERNAL` attach is rejected. (Uses the existing fake
  `agent.Session`.)
- **Integration** (gRPC over in-process socket): `AttachAgent` sends a Snapshot first then live
  frames; detach leaves the session alive; `SetSandboxTag` persists and republishes; count/`external`
  fields appear on `Event.sandbox_changed`; `ResolveWorkspace` maps a path to its sandbox.
- **TUI** (`teatest`): in-place terminal open/return preserves list state; list renders count + tag;
  tag editor round-trips.
- **E2E** (`switchboard-tui-e2e` / `switchboardd-e2e`, PTY/`vhs`, real `sbx`): detach→reattach shows
  prior output; a long command survives TUI close; one-external-terminal enforced end-to-end.
