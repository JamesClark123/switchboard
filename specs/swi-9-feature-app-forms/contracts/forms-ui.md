# Contract: Forms UI (keys, enlarged view, unsaved-changes dialog)

This is the TUI's user-facing contract for SWI-9. Tests assert against it, and the README key tables follow it.

## 1. Multi-line fields (all forms)

| Key (focus on a multi-line field) | Behaviour |
|---|---|
| `ctrl+e` | Open the enlarged view for this field. The footer shows `ctrl+e enlarge` (FR-001, FR-002). |
| `end` | Move to end of line. ctrl+e no longer does this in multi-line fields (FR-009). |
| every other key | Unchanged: text editing, `tab`/`shift+tab` between fields, `ctrl+s` save/apply. |

On single-line inputs, ctrl+e keeps moving to end of line, and no enlarge hint is shown. On selects and toggles,
ctrl+e does nothing. No key launches an external editor anywhere in the app.

## 2. Enlarged view

The view is a full-terminal modal over the whole screen, covering the header and footer:

```
╭─ Agent instructions ───────────────────────────────────────────────╮
│ <textarea: the field's full text, scrolls to keep the cursor visible>│
│ …                                                                    │
╰──────────────────────────────────────────────────────────────────────╯
 esc close · ctrl+s save · ctrl+r revert · pgup/pgdn page · ctrl+c leave form
```

| Key | Behaviour |
|---|---|
| `esc`, `ctrl+e` | Write the text back to the field and close. Focus returns to that field; no dialog (FR-005). |
| `ctrl+s` | Write back, close, then save the form exactly as the form's ctrl+s (FR-006). |
| `ctrl+c` | Write back, then act as ctrl+c in the form. If the form is changed, the dialog opens over the view and Cancel returns here (FR-005a). |
| `ctrl+r` | Revert to the text the field held when this view opened (FR-005b). |
| `pgup` / `pgdown` | Move the cursor by one view height. |
| `tab`, `shift+tab` | Do nothing (FR-004). |
| `end` | Move to end of line. |
| other keys | Textarea editing and navigation as inline. enter inserts a newline, with no line cap below 10,000 (FR-009a). |

Size: the whole terminal. The title is set into the top border, and there are no line numbers. It opens with the
cursor at the top of the text, and the cursor is always scrolled into view (also after ctrl+r and resize). The hint
line is 76 cells. At 80×24 it shows
≥ 16 text lines (21 by design); at 200×50 it uses ≥ 80% of width and height. Smaller terminals show fewer lines, down
to one, rather than overflowing. It re-fits on resize and keeps the text, cursor and scroll.

## 3. Unsaved-changes dialog

```
╭─ Unsaved changes ───────────────────────────────╮
│ This form has changes that haven't been saved.  │
│                                                 │
│    [ Save ]    [› Cancel ‹]    [ Discard ]      │
│                                                 │
│ s save · d discard · esc cancel · ←/→ · enter   │
╰─────────────────────────────────────────────────╯
```

| Key | Action |
|---|---|
| `s` | Save: the surface's own save or confirm (FR-017). If a background update has left the surface unchanged, it just leaves. |
| `r` (reasoning setup only, labelled **Rebuild**; `s` is ignored there) | Opens a destructive rebuild confirm built from the **edited** setup; its body lists the new sources, mode and kits and warns "Refused while a run is on it.". y rebuilds with that setup and goes to the orchestrator detail; n returns to the setup with its state intact. enter on the highlighted Rebuild does the same as `r`. (FR-017 as amended in this PR.) |
| `c`, `esc`, `ctrl+c` | Cancel: back to the surface exactly as it was (FR-015) |
| `d` | Discard: close the surface without applying, completing the exit (FR-016) |
| `←`/`→`, `h`/`l`, `tab`/`shift+tab` | Move the highlight |
| `enter` | Activate the highlighted choice. Cancel is highlighted on open, so enter discards only when Discard is highlighted (FR-019). |
| anything else | Ignored |

**Typing grace**: for 150 ms after the dialog opens, only `esc` and `ctrl+c` (Cancel) act. Every other key is ignored,
including letters, `enter`, `←`/`→`/`h`/`l` and `tab`/`shift+tab`. This way keys typed just after a stray esc can
never move the highlight to Discard and activate it.

The dialog is identical on every surface (FR-020), apart from the Rebuild label. The body line names the surface kind:
"This form…", "This input…", "This launch…", "These marked folders…". The dialog is `modalInnerWidth()` wide (53 at 80
columns, 49 cells of content). The hint is `s save · d discard · esc cancel · ←/→ · enter` (45 cells), or
`r rebuild · d discard · esc cancel · ←/→ · enter` (48 cells). The body is ≤ 2 lines and the box ≤ 11 rows. No line
wraps at 80×24.

## 4. Per-surface exits, Save and Discard

"Exit keys" are the user-initiated exits that the guard intercepts when the surface is changed. An unchanged surface
exits immediately, as today (FR-014).

| Surface | Exit keys (guarded) | Not exits (unchanged) | Save = | Discard = |
|---|---|---|---|---|
| Config editor | esc, ctrl+c | — | `saveConfig` (validation keeps it open) | back to the list |
| MCP registration | esc, ctrl+c | — | `applyMcpForm`. A host command leads to "register anyway?": n/esc/q return to the form with its values (FR-012a; hint "esc back to form"); y registers. | close the form, stay on the MCP view |
| Secrets | esc, ctrl+c | — | `applySecretsForm` (async; closes first) | close the form |
| Orchestrator create/rename | esc, ctrl+c | — | `applyOrchForm` (async; closes first) | close the form |
| Action / timed event / call | esc, ctrl+c | — | `submitOrchOpForm` (stays open until the result: error keeps it open, success closes) | close the form |
| Mark free | esc, ctrl+c (both return to the list when opened from it) | — | `submitOrchOpForm` | close the form (+ return to the list when opened from it) |
| Kit section / item form | esc, ctrl+c | — | `applyKitForm` (apply to the kit in memory) | close the form, kit untouched |
| Kit overview | esc, q | esc/q on a section's item list (back to the overview) | `saveKit` | `enterKitPicker` (kit unchanged on disk) |
| Launch wizard | esc | esc in the name field, host/orchestrator/kit pickers | `startLaunch` (as enter) | back to the list |
| Reasoning setup | esc | esc in the kit picker | **Rebuild** (`r`, or enter on it) → a confirm built from the edited setup (`confirmRebuildFromSetup`), never a direct rebuild; y runs the same rebuild as `startReasoningRebuildFromSetup` | back to the orchestrator detail |
| Sources overlay | esc, q | — | `confirmRemoveSources`. Declining the confirm returns to the overlay with marks kept; confirming removes and closes the overlay (the confirm's `onAccept` hook). | back to the list |
| Sources add browser | esc | — | `dispatchAddSources` (as enter) | back to the overlay |
| Rename | esc | — | as enter. Empty name: stays open with "name can't be empty" (FR-011a). | close, nothing renamed |
| Tag | esc | — | as enter (an empty value clears the tag) | close |
| Add host / add group | esc | — | as enter | close |

ctrl+c on the non-form surfaces keeps its current behaviour (FR-012): it is a no-op there.

When a save fails on a form that enter had already completed, the form is rebuilt from the same values with the error
shown, and focus starts at the first field. It is never left blank.

## 5. Forced close

These close the surface without the guard. Any open dialog or enlarged view closes with it, and the status reads
`unsaved edits lost: <reason>` when the surface was changed:

| Trigger | Surfaces | Reason text |
|---|---|---|
| Host drop / disconnect | MCP form; orchestrator admin and detail forms; reasoning setup | `daemon disconnected` |
| Sandbox removed (a `Removed` event, or a successful reload from its *connected* host without it; a `List` error closes nothing) | rename, tag, sources overlay / add browser of that sandbox | `sandbox removed` (or `daemon disconnected` if its host dropped) |
| Orchestrator removed | orchestrator rename form, reasoning setup, and op forms targeting it | `orchestrator removed` |

When the closing code already sets a status (e.g. `<host> disconnected — orchestrators closed`), `; unsaved edits lost`
is appended instead.

A background update that closes the surface while the dialog or enlarged view is open (e.g. a forced close) closes them
too. An op form whose save is in flight (`saving…`) ignores ctrl+s and edits until the reply, so the call is never
sent twice; esc/ctrl+c leave it at once, without the dialog, and the save finishes in the background with its result on
the footer. Only the reply to that call, matched by a per-call token, clears or closes the form.

An async low-resource add-override confirm never replaces anything. Every override queues in arrival order and
opens one at a time, on a non-key message, only where the add belongs:
- on the plain sandbox list, while you are not typing into its filter;
- or on that sandbox's own sources overlay with no dialog or browser open, where the marks are kept.

Anywhere else, including confirms, approval, the terminal, any form or editor, the MCP "register anyway?" step and
the password or sign-in prompts, it waits, and the status reads `add to <name> needs confirmation — back on the
list`. When nothing is in the way it opens at once. If the sandbox is removed or its host drops while it waits, it
is dropped with the status `add of <folders> to <name> cancelled: <reason>`.

**y to accept** on the three confirms that open without the user pressing their key (every add-override, since all of them come through the queue, plus the guard's
sources removal Save, Rebuild): only `y` accepts, and enter is ignored. A double, repeated or held enter, or the enter
that submits a password prompt over the confirm, can never accept it. The modal's hint and the footer read
`y confirm · n/esc cancel`. n/esc decline at once. Every other confirm still accepts y or enter.

If a forced close hits a surface while one of its own follow-up confirms is showing (the sources removal confirm or
the Rebuild confirm), the confirm closes with the surface and does not act.

A terminal that finishes opening while the developer is no longer on the sandbox list (where `t` was pressed) does
not take over the screen. The session is detached, and the status reads
`terminal for <name> ready — closed because you left the list; press t again`.
