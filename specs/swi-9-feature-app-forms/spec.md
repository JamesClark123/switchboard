# Feature Specification: App Forms — Enlargeable Text Fields & Unsaved-Changes Guard

**Feature Branch**: `jamesloganclark/swi-9-feature-app-forms`

**Created**: 2026-10-27

**Status**: Draft

**Input**: User description (Linear SWI-9, "Feature - App forms"), quoted verbatim:

> To improve UX we should formalize how large input areas & forms are handled in the app. Here are the following expectation:
>
> 1. Large text blocks should have the option to 'enlarge'. Enlarging a text block should open it as a large dialog with many lines visible. This will allow a user to see much more of the text at once.
> 2. Editing a form or input should never be exit-able without confirmation. If users have started a new form or editing a form (and have made changes - simply opening and doing nothing doesn't trigger this) then they should get a warning dialog before exiting without saving. No changes should not trigger the dialog. From the dialog they can choose to save, cancel the exit, or choose to exit destructively

## Clarifications

### Session 2026-10-27

- Q: Which text blocks get the "enlarge" option? → A: **Every editable multi-line text field** in a form. The enlarged view is fully editable and its edits flow back into the form. Read-only text is out of scope because it already has scrollable viewers (e.g. Jobs › Open results). (FR-001–FR-008)
- Q: Which editing surfaces get the unsaved-changes guard? → A: **All forms and inline inputs**: every multi-field form (including the nested item editors of a larger form), every multi-step editing flow, and every single-line inline input that holds a value until confirmed. (FR-010, In-scope surfaces)
- Q: Which exit paths trigger the warning? → A: **Every user-initiated exit** from a surface that holds unsaved edits. The exits that exist today are listed under FR-012. Keys that edit text inside a field are never exits, and this feature adds no new ways to leave a form. (FR-012)
- Q: Inside a form, ctrl+c currently aborts the form silently and does not quit the app. With unsaved changes, what should it do? → A: **The same as esc.** It leaves the form behind the unsaved-changes dialog, and the app never quits from inside a form. (FR-012)
- Q: Text fields already offer ctrl+e, "open in external editor". How should that relate to enlarge? → A: **ctrl+e becomes the enlarge key.** It opens the in-app enlarged view, and the external-editor action is removed. (FR-001, FR-009) On single-line inputs ctrl+e keeps its existing meaning, move to end of line.

### PR review 2026-10-27 (pr-review run `run-c275c8f2-276`, resolved in pr-response run `run-c275c8f2-362`)

- The enlarged view accepts text up to the existing 10,000-line load limit, because without the external editor nothing else could grow a field past 99 lines. (FR-009a; round 2 below keeps the inline fields' 99-line cap)
- A forced close (daemon disconnect, or the edited object removed) takes precedence over an open unsaved-changes dialog. The dialog goes with the surface, and the status line says why the edits were lost. (Edge Cases)
- In the enlarged view, ctrl+c writes the text back and then acts as ctrl+c in the form, so this is how the dialog opens over it. Cancel returns to the enlarged view. tab and shift+tab do nothing there. (FR-004, FR-005a, FR-015)
- Inline inputs count as changed only after the normalisation enter applies (trim). A value enter would reject (an empty sandbox name) is a validation failure for Save. (FR-011a, US3 #1 and #7)
- Back keys that step within a surface without dropping anything are not exits. (FR-012, US3 #6)
- Declining the follow-up removal confirm after Save in the sources overlay behaves like Cancel. Confirming completes the exit. (US3 #3)
- The enlarged view gets a revert key that restores the text from when it opened. (FR-005b)
- Large text must stay responsive in the enlarged view. (SC-006)

### PR review round 2 (2026-10-27, re-review run `run-c275c8f2-377`, resolved in pr-response run `run-c275c8f2-386`)

- **Inline multi-line fields keep their 99-line typing cap.** All 17 inline multi-line fields are `huh.Text`. In huh v1.0.0, `huh.Text` doesn't expose its textarea's `MaxHeight`, so removing the cap inline would mean replacing `huh.Text` with a custom field or forking huh. Instead, the enlarged view is how a field grows past 99 lines. A longer value is kept intact inline, because the cap only stops enter from adding a line. I checked this against huh v1.0.0 during review: a 150-line value loaded into `huh.Text` is kept and can be edited, and enter at its end adds no line. (FR-009a, US2 #8)
- Responsiveness also covers a form whose inline field holds very long text (written back from the enlarged view), since every field re-renders on each keystroke. (SC-006)
- The revert baseline is set when ctrl+e opens the enlarged view, and it survives a ctrl+c → Cancel round trip. (FR-005b)

### PR review round 3 (2026-10-27, re-review run `run-c275c8f2-400`, resolved in pr-response run `run-c275c8f2-408`)

- Responsiveness also covers typing, cursor movement and scrolling **in** the long inline field itself, which is where focus lands after the enlarged view closes (FR-005). The textarea's wrap cache is only cut to 99 entries once the field is focused, so that is the worst case. (SC-006)

### Plan review (2026-10-27, re-review run `run-c275c8f2-435`, resolved in pr-response run `run-c275c8f2-437`)

- Rebuild in the unsaved-changes dialog always goes through a confirm built from the edited setup, whichever key chooses it. A dialog that interrupts typing must never destroy a sandbox on one key. (FR-017 exception)
- Confirms that open without the developer pressing that confirm's own key accept only `y`; enter does not confirm them. These are the guard's follow-up confirms (the sources removal after Save, and the Rebuild confirm) and every low-disk add-override confirm (they all open from one queue, including one that opens at once on the sources overlay). A repeated or held enter must never carry through to a destructive or disk-filling action. Every other confirm keeps y/enter. (FR-017, FR-019; resolved in pr-response runs `run-c275c8f2-446` and `run-c275c8f2-450`)

## Background (current behaviour this feature changes)

Switchboard's terminal UI edits its data through these surfaces:

- **Multi-field forms**:
  - the sandbox configuration editor;
  - MCP server registration;
  - secrets;
  - orchestrator create / rename;
  - orchestrator actions and timed events, opened from the Jobs screen and from Daemons › Orchestrators;
  - the call-an-action (arguments) form;
  - the mark-free form (outcome + note), reached with `m` from the sandbox list, Jobs or an orchestrator's detail view.
- **The kit editor**: a section overview that drills into per-section item forms (agent instructions, files, commands, services, …). Applying an item form updates the kit in memory; the kit itself is written only by an explicit save on the overview.
- **Multi-step and selection flows**:
  - the launch wizard (selected sources, name, kits, host, orchestrator, clone mode);
  - the reasoning-sandbox setup, which reuses the launch browser;
  - the sources overlay (folders marked for removal; the add-browser selection).
- **Inline single-line inputs**: renaming a sandbox, editing its tag, adding a host, adding a group.

Several forms contain **multi-line text fields**, such as kit agent instructions and file content, an action's body and arguments, and an MCP server's headers. Inline, these show only 3–6 lines, so reading or editing anything longer means scrolling inside a small box. The fields also offer **ctrl+e**, which suspends the app and opens the text in an external editor.

Today every way out of these surfaces **discards their edits with no warning**:

- **esc** in any form, inline input, wizard or overlay.
- **ctrl+c** inside a form aborts it. It does not quit the app.
- **q** on the kit editor's overview drops item changes that were applied but not saved.
- **Backing out of a follow-up confirmation step.** MCP registration of a local command shows a "register anyway?" step after the form is saved. Backing out of that step drops the whole registration.

While a form has focus, every other key, including `q`, shift+←/→ and `,`, is typed into the focused field. Switching screens or quitting is therefore only possible once the form has closed.

The UI already has a modal confirm dialog for destructive actions (y/enter to confirm, n/esc to cancel; the confirms this feature opens on the developer's behalf accept only `y`, see FR-017). It is a two-choice gate and is not used when leaving a form.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Never lose edits by accident (Priority: P1)

A developer is part-way through editing a kit's agent instructions, or registering an MCP server, and presses esc out of habit. Instead of losing the work silently, they see a warning dialog saying there are unsaved changes. It offers three choices: **Save** them, **Cancel** (back to the form exactly as it was), or **Discard** them and leave.

**Why this priority**: Silent loss of typed work is the most damaging problem the ticket describes. A guard on every form is valuable on its own, even without the enlarge feature.

**Independent Test**:
- Open any form, change one field, press esc, and confirm the dialog appears and each of its three choices behaves as specified.
- Then open the same form, change nothing, press esc, and confirm it closes immediately with no dialog.

**Acceptance Scenarios**:

1. **Given** a form opened for editing with no fields changed, **When** the developer presses esc, **Then** the form closes immediately with no dialog.
2. **Given** a form with at least one field changed, **When** the developer presses esc, **Then** an unsaved-changes dialog opens over the form with three choices: Save, Cancel and Discard.
3. **Given** the dialog is open, **When** the developer chooses **Cancel**, **Then** the dialog closes and the form is exactly as it was, with the same values, focused field and cursor position.
4. **Given** the dialog is open, **When** the developer chooses **Discard**, **Then** the form closes and none of its edits are applied.
5. **Given** the dialog is open and the form's values are valid, **When** the developer chooses **Save**, **Then** the form saves exactly as its own save key would, and on success it closes.
6. **Given** the dialog is open and the form has a validation error, **When** the developer chooses **Save**, **Then** nothing is saved, the dialog closes, and the form stays open showing the validation error.
7. **Given** a field changed and then changed back to its original value, **When** the developer presses esc, **Then** no dialog is shown.
8. **Given** a brand-new (create) form, **When** the developer has typed into any field and presses esc, **Then** the dialog is shown. If they have typed nothing, the form closes without it.

---

### User Story 2 - Guard every way out, not just esc (Priority: P1)

A developer with unsaved edits leaves by a route other than esc. Examples: pressing ctrl+c in a form, pressing q on the kit editor's overview, or backing out of a follow-up confirmation step. They get the same protection: the unsaved-changes dialog. The app never quits or loses edits from inside a form.

**Why this priority**: A guard that covers only esc still loses work through the other exits. The ticket says a form must *never* be exit-able without confirmation.

**Independent Test**: With unsaved changes, try each exit listed in FR-012 (a)–(c) in turn. Confirm that each one raises the dialog, and that Discard performs that exit without applying the edits.

**Acceptance Scenarios**:

1. **Given** unsaved changes in a form, **When** the developer presses ctrl+c, **Then** the unsaved-changes dialog appears, exactly as for esc. Discard and a successful Save return to the screen the form was opened from, and the app keeps running.
2. **Given** item changes applied in the kit editor but not yet saved, **When** the developer presses esc or q on the kit editor's overview, **Then** the dialog appears. Save writes the kit; Discard returns to the kit picker with the kit unchanged.
3. **Given** a form whose save leads to a follow-up confirmation step (e.g. MCP "register anyway?"), **When** the developer backs out of that step, **Then** they return to the form with every value intact, rather than losing the registration (FR-012a).
4. **Given** a form with no unsaved changes, **When** the developer uses any exit, **Then** that exit happens immediately, exactly as it does today.
5. **Given** focus in a text field, **When** the developer types `q`, `,` or shift+←/→, **Then** the key edits the field as it does today and is never treated as an exit.

---

### User Story 3 - Guard inline inputs, wizards and nested editors (Priority: P2)

Some edits live outside a classic form:
- a sandbox rename, a tag edit, or adding a host or group;
- a half-built launch;
- the sources overlay with folders marked for removal;
- an item open inside the kit editor.

When the developer presses esc in any of these with something entered or selected, they get the same three-choice dialog. In a nested editor the guard applies at each level.

**Why this priority**: These surfaces usually hold less work, so losing it costs less. But the ticket covers "a form **or input**", and the developer chose one consistent behaviour everywhere.

**Independent Test**:
- Type into the rename input and press esc: the dialog appears.
- Pick two sources in the launch wizard and press esc: the dialog appears.
- In the kit editor, change an item and apply it, then press esc on the overview: the dialog appears even though the item form itself has closed.

**Acceptance Scenarios**:

1. **Given** an inline single-line input whose value, trimmed as enter trims it, differs from its starting value, **When** the developer presses esc, **Then** the dialog appears. Save commits the value just as enter does; Discard closes the input with nothing committed. A value that only gains surrounding spaces is unchanged and closes with no dialog (FR-011a).
2. **Given** the launch wizard or the reasoning-sandbox setup with any selection or value differing from how it opened, **When** the developer presses esc, **Then** the dialog appears. Save performs the flow's own confirm action, exactly as enter / ctrl+s would: in the launch wizard it launches; in the reasoning setup it starts the rebuild, and the choice is labelled **Rebuild** rather than Save so the destructive effect is explicit. Discard leaves without acting.
3. **Given** the sources overlay with folders marked for removal, or the add-browser with folders selected, **When** the developer presses esc, **Then** the dialog appears. Save performs the pending removal, which shows its usual confirm dialog (accepting only `y` when reached this way, FR-017), or the add, which is sent immediately as enter does.
   - If the developer declines that removal confirm (n/esc), it behaves like Cancel: they return to the overlay with the folders still marked, and the requested exit is dropped.
   - If they confirm it, the removal is sent and the overlay closes, completing the requested exit.
4. **Given** a nested item editor with changes, **When** the developer presses esc in it, **Then** Save applies the item to the parent (the item editor's normal apply) and Discard drops the item's changes. Either way, the developer returns to the parent.
5. **Given** a parent holding applied-but-unsaved changes from its nested editors, **When** the developer leaves the parent, **Then** the dialog appears for the parent, and Save saves the whole parent.
6. **Given** an esc or q that only steps back within the same surface without dropping anything, **When** the developer presses it, **Then** the behaviour is unchanged and no dialog appears. This is not an exit. These keys are:
   - esc in the launch wizard's optional name field (the field loses focus and keeps its value);
   - esc in the launch wizard's or reasoning setup's host picker, orchestrator picker or kit picker (back to the wizard);
   - esc or q on a kit section's item list (back to the overview; applied items stay on the kit).

   In contrast, the sources add-browser *is* guarded, because leaving it drops its selection.
7. **Given** the rename input cleared to an empty name, **When** the developer presses esc and chooses **Save**, **Then** nothing is renamed and the input stays open with a message that the name can't be empty. Discard closes it and the sandbox keeps its name. (A cleared tag is a valid value: Save clears the tag, as enter does.)

---

### User Story 4 - Enlarge a multi-line text field (Priority: P2)

A developer is editing a long text field, such as a kit's agent instructions, an action's prompt body or an MCP server's headers. They press **ctrl+e** on that field to enlarge it. The field opens in a large dialog covering most of the terminal, showing many more lines at once, where they can read and edit the full text. When they close the enlarged view, the form shows the updated text, with focus back on the same field.

**Why this priority**: This makes long text much more comfortable to work with, but nobody loses work without it. It is independent of the guard and can ship separately.

**Independent Test**: Open the kit editor's agent-instructions field containing 50 lines and press ctrl+e. Confirm far more lines are visible than inline. Edit a line, close the view, and confirm the inline field contains the edit.

**Acceptance Scenarios**:

1. **Given** focus on a multi-line text field, **When** the developer reads the help line, **Then** it shows `ctrl+e` as "enlarge" (no longer "open editor").
2. **Given** focus on a multi-line text field, **When** the developer presses ctrl+e, **Then** a large modal view opens over the screen. It shows the field's title and full text and uses most of the terminal's height and width. No external editor is launched.
3. **Given** the enlarged view is open, **When** the developer types, deletes, moves the cursor or scrolls, **Then** the text edits and navigates as the inline field does, and the cursor stays visible.
4. **Given** the enlarged view has edits, **When** the developer closes it (esc or ctrl+e), **Then** the field holds the edited text, focus is back on that field, the form counts as changed for the guard, and no unsaved-changes dialog is shown.
5. **Given** the enlarged view is open, **When** the developer presses the form's save key (ctrl+s), **Then** the view closes with its edits kept and the form saves, exactly as if ctrl+s had been pressed in the form.
6. **Given** the enlarged view is open, **When** the terminal is resized, **Then** the view re-fits without losing text, cursor or scroll position.
7. **Given** focus on a single-line input, **When** the developer presses ctrl+e, **Then** it moves the cursor to the end of the line as it does today, and no enlarge hint is shown. On a non-text field (toggle, select) ctrl+e does nothing.
8. **Given** a multi-line field holding 150 lines in the enlarged view, **When** the developer presses enter at the end, **Then** a 151st line is added. Closing the view leaves all 151 lines in the inline field, and the form saves them in full. Pressing enter at the end inline adds no line past 99, and ctrl+e is how the developer grows the field further (FR-009a).
9. **Given** the enlarged view with edits, **When** the developer presses the revert key, **Then** the view shows the text exactly as it was when the view opened. Edits made in the form before enlarging are kept (FR-005b).
10. **Given** the enlarged view over a changed form, **When** the developer presses ctrl+c, **Then** the view's text is written back and the unsaved-changes dialog opens. Cancel returns to the enlarged view with its text, cursor and scroll position intact (FR-005a).

---

### Edge Cases

- **A save started from the dialog fails** (e.g. the daemon rejects it). On a form that stays open while saving, the form stays open with its values intact and shows the error, as a failed save from the form does today. On an inline input whose own save closes it before the result arrives (rename, tag, add host, add group), Save behaves exactly like enter. A failure then shows in the status line as it does today.
- **A background update arrives while the dialog is open** (e.g. the edited sandbox's state or another field changes). The dialog stays up and the developer's choice is honoured. A Save that then fails follows the rule above.
- **A forced close while the dialog is open.** A forced close takes precedence over the dialog. It is either a daemon disconnect closing an orchestrator form, or the edited object being removed (e.g. the sandbox being renamed or tagged is destroyed, or the orchestrator being edited is removed). The surface closes and the dialog closes with it, so no Save is attempted against a dead link or a missing object. The status line says the edits were lost and why (e.g. "unsaved edits lost: daemon disconnected" or "unsaved edits lost: sandbox removed"). The same happens when there is no dialog open.
- **Closing the enlarged view** is not leaving the form, so it never raises the unsaved-changes dialog.
- **Enter in the last field** of a form submits it today; this is a save, not an exit, and is unaffected.
- **A small terminal**: at 80×24 the enlarged view and the dialog fit fully on screen with their key hints. Smaller sizes degrade by showing fewer lines rather than overflowing.
- **Whitespace edits**: only a value that differs from the original counts as a change. An edit typed and then deleted does not. Inline inputs compare trimmed values (FR-011a), so surrounding spaces alone never raise the dialog. In multi-line fields whitespace is content and counts.
- **Exits the developer did not start** close a surface without the guard: a forced close (above), a closed terminal window, a killed process, a crash. These are not protected and edits are not kept.
- **Out of scope**:
  - Credential prompts (host password, startup sign-in, the MCP authorization paste box) hold a secret for a single attempt. They are not guarded.
  - Settings toggles save immediately, so nothing is ever unsaved there.
- **The destructive-action confirm dialog** (destroy, refresh, …) keeps its current two-choice behaviour.

## Requirements *(mandatory)*

### Functional Requirements

**Enlargeable multi-line text fields**

- **FR-001**: Every editable multi-line text field in every form MUST offer an **enlarge** action on **ctrl+e** while that field has focus.
- **FR-002**: The help line MUST show `ctrl+e enlarge` when, and only when, a multi-line text field has focus.
- **FR-003**: Enlarging MUST open a modal view showing the field's title and full text, using most of the terminal's height and width (see SC-003). The view covers the screen's header and footer.
- **FR-004**: The enlarged view MUST be editable with the same editing and navigation behaviour as the inline field, and MUST scroll to keep the cursor visible. tab and shift+tab, which move between fields inline, MUST do nothing in the enlarged view; they neither insert text nor close it.
- **FR-005**: Closing the enlarged view (esc or ctrl+e) MUST write its text back into the field, return focus to that field, and keep the form open. Closing it MUST NOT raise the unsaved-changes dialog.
- **FR-005a**: ctrl+c in the enlarged view MUST write its text back and then act exactly as ctrl+c in the form (FR-012 (b)). If the form is changed, the unsaved-changes dialog opens and the enlarged view stays underneath it: Cancel returns to the enlarged view unchanged, while Save and Discard act on the whole form. If the form is unchanged, the form closes as it does today.
- **FR-005b**: The enlarged view MUST offer a **revert** key, shown in its key hints, that restores the text the field held when the view opened. It undoes only the edits made in this view, never the form's other edits. The baseline is set when ctrl+e opens the view, and it survives a ctrl+c → Cancel round trip (FR-005a): the write-back at ctrl+c doesn't move it, so after Cancel, revert still restores the text from before ctrl+e. The planning phase chooses the key, under the same no-clash rule as the dialog keys.
- **FR-006**: The form's save key pressed in the enlarged view MUST close the view with its edits kept, then save the form exactly as the form's save key does.
- **FR-007**: Edits made in the enlarged view MUST count towards the form's changed state exactly as inline edits do.
- **FR-008**: The enlarged view MUST re-fit when the terminal is resized, keeping text, cursor and scroll position.
- **FR-009**: ctrl+e MUST NOT launch an external editor anywhere in the app. On single-line inputs it MUST keep its existing move-to-end-of-line behaviour; on non-text fields it MUST do nothing. In multi-line fields and in the enlarged view, move-to-end-of-line stays available on `end`.
- **FR-009a**: The enlarged view MUST accept text up to at least the existing 10,000-line load limit, with no lower line cap. Without the external editor it is the only way to grow a field past 99 lines. (Today the editor's result already reaches fields with only the 10,000-line limit, so kit **Content** and **Agent instructions** can exceed 99 lines.)
  - Inline multi-line fields (`huh.Text`) keep huh's textarea default, a 99-line `MaxHeight` that stops enter from adding a line past 99. huh v1.0.0 offers no setter for it, and lifting it would mean replacing `huh.Text` with a custom field.
  - An inline field MUST still hold, show (scrolling) and save in full any value up to the 10,000-line limit, whether written back from the enlarged view or loaded. The cap blocks only enter: setting a value is bounded only by the 10,000-line limit.
  - The planning phase verifies that a value over 99 lines round-trips through `huh.Text` unchanged. If it doesn't, the plan replaces `huh.Text` for the affected fields rather than truncating the value.
  - The inline field's `ctrl+e enlarge` hint is how a developer at the cap adds lines. Detecting the blocked enter is not required.

**Unsaved-changes guard**

- **FR-010**: The guard MUST apply to every surface listed under Background that holds edits until the developer saves or confirms them: multi-field forms (including the mark-free form), the kit editor (overview and item forms), the launch wizard, the reasoning-sandbox setup, the sources overlay, and the inline single-line inputs. Credential prompts and immediately-saving toggles are excluded.
- **FR-011**: A surface MUST count as **changed** when, and only when, its current values or selections differ from those it had when it opened (for a create form: its initial or default values). Opening it, moving focus or scrolling MUST NOT count as a change, and an edit undone back to the original value MUST NOT count either. For the kit editor, the baseline is the kit as opened, after any automatic format migration. A pending migration alone does not count as a change; the existing "unchanged on disk until you save" notice covers it.
- **FR-011a**: An inline single-line input MUST compare its value after the same normalisation enter applies (trimming). A value that enter would reject MUST still count as changed when it differs from the starting value, and Save MUST then fail validation: the input stays open with a message naming the problem. Today the only such value is an empty sandbox name, which enter silently ignores. Clearing a tag is valid, and the add-host and add-group inputs start empty, so an empty value there is unchanged.
- **FR-012**: The guard MUST intercept every user-initiated exit from a changed surface:
  - (a) esc;
  - (b) ctrl+c inside a form, which MUST act exactly like esc and MUST NOT quit the app;
  - (c) any surface-specific back or close key that leaves the surface or drops part of its state (e.g. `q` on the kit editor's overview, esc in the sources add-browser).

  A key that only steps back within the same surface without dropping anything is not an exit, and MUST raise no dialog (User Story 3, scenario 6). On guarded surfaces that are not forms (inline inputs, the launch wizard, the kit overview, the sources overlay), ctrl+c keeps its current behaviour. Keys that edit text inside a field MUST NOT be treated as exits. This feature MUST NOT add new ways to leave a form.
- **FR-012a**: Backing out of a confirmation step that follows a form's save (e.g. MCP "register anyway?") MUST return to the form with every value intact, instead of discarding the form. This raises no dialog.
- **FR-013**: When the developer tries to leave a changed surface, the app MUST show an **unsaved-changes dialog** instead of leaving. The dialog states that there are unsaved changes and offers three choices: **Save**, **Cancel** and **Discard**.
- **FR-014**: Leaving an unchanged surface MUST work exactly as it does today, immediately and with no dialog.
- **FR-015**: **Cancel** MUST close the dialog and return to the surface unchanged, with the same values, selections, focus and cursor position. When the dialog was raised from the enlarged view (FR-005a), Cancel MUST return to the enlarged view.
- **FR-016**: **Discard** MUST close the surface without applying any of its edits, then complete the exit the developer asked for.
- **FR-017**: **Save** MUST do exactly what the surface's own save or confirm action does, including any validation and follow-up confirmation step:
  - If validation fails, the surface MUST stay open showing the error.
  - If the save fails on a surface that stays open while saving, it MUST stay open with its values and show the error.
  - On success, the surface closes as it does after a normal save.
  - Where the surface's confirm action is destructive (the reasoning-sandbox setup, whose confirm destroys and relaunches the reasoning sandbox), the Save choice MUST be labelled with that action ("Rebuild").
  - Exception: choosing **Rebuild** in the dialog MUST NOT rebuild at once. It MUST open a destructive confirm that names the **edited** setup, even though the setup's own rebuild key has no confirm. Declining returns to the setup unchanged; confirming rebuilds with the edited setup.
  - A follow-up confirm reached through the dialog's Save or Rebuild (the sources removal and the Rebuild confirm) MUST accept only `y`, never enter, so the enter that chose Save or Rebuild cannot also confirm it, however it is repeated or held. Every low-disk add-override confirm follows the same rule, whether it opens at once or after being held back while the developer was elsewhere.
- **FR-018**: In nested editors the guard MUST apply at each level:
  - Leaving a changed item editor raises the dialog for that item; there, Save means "apply to the parent".
  - Leaving a parent that holds applied-but-unsaved item changes raises the dialog for the parent; there, Save means "save the parent".
- **FR-019**: The dialog MUST be fully keyboard-operable, with a visible key for each choice. The highlighted choice when it opens MUST be Cancel, and pressing esc in the dialog MUST choose Cancel. Only Discard's key, or enter while Discard is highlighted, MAY discard edits. (This intentionally differs from the destructive-action confirm dialog, where enter confirms, except on the y-only confirms of FR-017: the guard's follow-up confirms and every low-disk add-override.)
- **FR-020**: The dialog MUST look and behave the same on every guarded surface.
- **FR-021**: The unsaved-changes dialog and the enlarged view MUST be fully visible, key hints included, on an 80×24 terminal.

### Key Entities

- **Guarded surface**: one open form, inline input, wizard or overlay that holds edits. It keeps the values or selections it opened with, its current values, and whether it is changed. A nested editor is its own guarded surface inside its parent.
- **Requested exit**: the exit the developer asked for while a surface was changed. It is held while the dialog is open. It is completed after Discard or a successful Save, and dropped after Cancel, a failed Save, or a declined follow-up confirm. A forced close supersedes it.
- **Enlarged view**: a temporary large editor bound to one multi-line field. Its edits belong to that field's form.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On every guarded surface, 100% of the exits listed in FR-012 (a)–(c) raise the unsaved-changes dialog when the surface is changed. No exit loses edits unless the developer explicitly chooses Discard.
- **SC-002**: On every guarded surface, leaving without changes raises the dialog 0% of the time, and opening then closing a surface costs no more keypresses than it does today.
- **SC-003**: On an 80×24 terminal, the enlarged view shows at least 16 lines of text. On a 200×50 terminal, it uses at least 80% of the terminal's height and width.
- **SC-004**: A developer can enlarge a field, edit it, close the view and see the edit in the form using no more than two keypresses beyond the edit itself.
- **SC-005**: The three dialog choices produce the specified result for every exit in User Stories 1–3. Cancel always restores the exact previous state of the surface.
- **SC-006**: On a 200×50 terminal, each keystroke's result MUST show within 100 ms in all of these cases:
  - with 5,000 lines of text in the enlarged view, for typing, cursor movement and scrolling;
  - in the inline multi-line field holding those 5,000 lines once the view closes (FR-005 returns focus to it), for typing, cursor movement and scrolling in that field;
  - in a form whose inline multi-line field holds 5,000 lines, for typing in *another* field of that form.

  The planning phase includes a check of each. Rendering walks every line of a value, and a form re-renders every field on each update. The textarea's wrap cache is cut to its `MaxHeight` (99 inline) on its first focused update, so the focused long field is where the cache thrashes worst. If the check fails, the plan's fallback (a custom field instead of `huh.Text`, FR-009a) also owns the cache sizing.

## Assumptions

- The enlarge key is ctrl+e (clarified). The planning phase chooses the dialog's Save, Cancel and Discard keys. They must not clash with the existing keys of the surfaces they guard, and must follow the app's key-hint conventions.
- One new three-choice dialog is shared by every guarded surface. The existing two-choice destructive-action confirm dialog is unchanged.
- Save in the dialog uses each surface's existing save or confirm behaviour and validation. No surface gains a new way to save. The one behaviour change is FR-012a: backing out of a follow-up confirmation now returns to the form.
- The launch wizard's name field keeps its current behaviour (esc only leaves the field). The wizard as a whole is the guarded surface.
- Read-only long text (run results, prompts, notifications) is out of scope for enlarge.
- Exits the developer did not start (disconnect, killed process, closed terminal, crash) are not protected, and edits are not kept across app restarts.
- This is a terminal-UI change only. No daemon or protocol changes are expected.
