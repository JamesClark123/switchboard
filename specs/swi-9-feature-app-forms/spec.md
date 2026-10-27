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

The UI already has a modal confirm dialog for destructive actions (y/enter to confirm, n/esc to cancel). It is a two-choice gate and is not used when leaving a form.

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

1. **Given** an inline single-line input whose value differs from its starting value, **When** the developer presses esc, **Then** the dialog appears. Save commits the value just as enter does; Discard closes the input with nothing committed.
2. **Given** the launch wizard or the reasoning-sandbox setup with any selection or value differing from how it opened, **When** the developer presses esc, **Then** the dialog appears. Save performs the flow's own confirm action, exactly as enter / ctrl+s would: in the launch wizard it launches; in the reasoning setup it starts the rebuild, and the choice is labelled **Rebuild** rather than Save so the destructive effect is explicit. Discard leaves without acting.
3. **Given** the sources overlay with folders marked for removal, or the add-browser with folders selected, **When** the developer presses esc, **Then** the dialog appears. Save performs the pending removal, which shows its usual confirm dialog, or the add, which is sent immediately as enter does.
4. **Given** a nested item editor with changes, **When** the developer presses esc in it, **Then** Save applies the item to the parent (the item editor's normal apply) and Discard drops the item's changes. Either way, the developer returns to the parent.
5. **Given** a parent holding applied-but-unsaved changes from its nested editors, **When** the developer leaves the parent, **Then** the dialog appears for the parent, and Save saves the whole parent.
6. **Given** the launch wizard's optional name field, **When** the developer presses esc inside it, **Then** the behaviour is unchanged: the field loses focus, keeps its value, and stays within the wizard. This is not an exit.

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

---

### Edge Cases

- **A save started from the dialog fails** (e.g. the daemon rejects it). On a form that stays open while saving, the form stays open with its values intact and shows the error, as a failed save from the form does today. On an inline input whose own save closes it before the result arrives (rename, tag, add host, add group), Save behaves exactly like enter. A failure then shows in the status line as it does today.
- **A background update arrives while the dialog is open** (e.g. the edited sandbox changes or is removed). The dialog stays up and the developer's choice is honoured. A Save that then fails follows the rule above.
- **Closing the enlarged view** is not leaving the form, so it never raises the unsaved-changes dialog.
- **Enter in the last field** of a form submits it today; this is a save, not an exit, and is unaffected.
- **A small terminal**: at 80×24 the enlarged view and the dialog fit fully on screen with their key hints. Smaller sizes degrade by showing fewer lines rather than overflowing.
- **Whitespace edits**: only a value that differs from the original counts as a change. An edit typed and then deleted does not.
- **Exits the developer did not start** close a surface without the guard: a daemon disconnect closing an orchestrator form, a closed terminal window, a killed process, a crash. These are not protected and edits are not kept.
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
- **FR-004**: The enlarged view MUST be editable with the same editing and navigation behaviour as the inline field, and MUST scroll to keep the cursor visible.
- **FR-005**: Closing the enlarged view (esc or ctrl+e) MUST write its text back into the field, return focus to that field, and keep the form open. Closing it MUST NOT raise the unsaved-changes dialog.
- **FR-006**: The form's save key pressed in the enlarged view MUST close the view with its edits kept, then save the form exactly as the form's save key does.
- **FR-007**: Edits made in the enlarged view MUST count towards the form's changed state exactly as inline edits do.
- **FR-008**: The enlarged view MUST re-fit when the terminal is resized, keeping text, cursor and scroll position.
- **FR-009**: ctrl+e MUST NOT launch an external editor anywhere in the app. On single-line inputs it MUST keep its existing move-to-end-of-line behaviour; on non-text fields it MUST do nothing. In multi-line fields and in the enlarged view, move-to-end-of-line stays available on `end`.

**Unsaved-changes guard**

- **FR-010**: The guard MUST apply to every surface listed under Background that holds edits until the developer saves or confirms them: multi-field forms (including the mark-free form), the kit editor (overview and item forms), the launch wizard, the reasoning-sandbox setup, the sources overlay, and the inline single-line inputs. Credential prompts and immediately-saving toggles are excluded.
- **FR-011**: A surface MUST count as **changed** when, and only when, its current values or selections differ from those it had when it opened (for a create form: its initial or default values). Opening it, moving focus or scrolling MUST NOT count as a change, and an edit undone back to the original value MUST NOT count either. For the kit editor, the baseline is the kit as opened, after any automatic format migration. A pending migration alone does not count as a change; the existing "unchanged on disk until you save" notice covers it.
- **FR-012**: The guard MUST intercept every user-initiated exit from a changed surface:
  - (a) esc;
  - (b) ctrl+c inside a form, which MUST act exactly like esc and MUST NOT quit the app;
  - (c) any surface-specific back or close key (e.g. `q` on the kit editor's overview).

  On guarded surfaces that are not forms (inline inputs, the launch wizard, the kit overview, the sources overlay), ctrl+c keeps its current behaviour. Keys that edit text inside a field MUST NOT be treated as exits. This feature MUST NOT add new ways to leave a form.
- **FR-012a**: Backing out of a confirmation step that follows a form's save (e.g. MCP "register anyway?") MUST return to the form with every value intact, instead of discarding the form. This raises no dialog.
- **FR-013**: When the developer tries to leave a changed surface, the app MUST show an **unsaved-changes dialog** instead of leaving. The dialog states that there are unsaved changes and offers three choices: **Save**, **Cancel** and **Discard**.
- **FR-014**: Leaving an unchanged surface MUST work exactly as it does today, immediately and with no dialog.
- **FR-015**: **Cancel** MUST close the dialog and return to the surface (or the enlarged view) unchanged, with the same values, selections, focus and cursor position.
- **FR-016**: **Discard** MUST close the surface without applying any of its edits, then complete the exit the developer asked for.
- **FR-017**: **Save** MUST do exactly what the surface's own save or confirm action does, including any validation and follow-up confirmation step:
  - If validation fails, the surface MUST stay open showing the error.
  - If the save fails on a surface that stays open while saving, it MUST stay open with its values and show the error.
  - On success, the surface closes as it does after a normal save.
  - Where the surface's confirm action is destructive (the reasoning-sandbox setup, whose confirm destroys and relaunches the reasoning sandbox), the Save choice MUST be labelled with that action ("Rebuild").
- **FR-018**: In nested editors the guard MUST apply at each level:
  - Leaving a changed item editor raises the dialog for that item; there, Save means "apply to the parent".
  - Leaving a parent that holds applied-but-unsaved item changes raises the dialog for the parent; there, Save means "save the parent".
- **FR-019**: The dialog MUST be fully keyboard-operable, with a visible key for each choice. The highlighted choice when it opens MUST be Cancel, and pressing esc in the dialog MUST choose Cancel. Only Discard's key, or enter while Discard is highlighted, MAY discard edits. (This intentionally differs from the destructive-action confirm dialog, where enter confirms.)
- **FR-020**: The dialog MUST look and behave the same on every guarded surface.
- **FR-021**: The unsaved-changes dialog and the enlarged view MUST be fully visible, key hints included, on an 80×24 terminal.

### Key Entities

- **Guarded surface**: one open form, inline input, wizard or overlay that holds edits. It keeps the values or selections it opened with, its current values, and whether it is changed. A nested editor is its own guarded surface inside its parent.
- **Requested exit**: the exit the developer asked for while a surface was changed. It is held while the dialog is open, completed after Discard or a successful Save, and dropped after Cancel or a failed Save.
- **Enlarged view**: a temporary large editor bound to one multi-line field. Its edits belong to that field's form.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On every guarded surface, 100% of the exits listed in FR-012 (a)–(c) raise the unsaved-changes dialog when the surface is changed. No exit loses edits unless the developer explicitly chooses Discard.
- **SC-002**: On every guarded surface, leaving without changes raises the dialog 0% of the time, and opening then closing a surface costs no more keypresses than it does today.
- **SC-003**: On an 80×24 terminal, the enlarged view shows at least 16 lines of text. On a 200×50 terminal, it uses at least 80% of the terminal's height and width.
- **SC-004**: A developer can enlarge a field, edit it, close the view and see the edit in the form using no more than two keypresses beyond the edit itself.
- **SC-005**: The three dialog choices produce the specified result for every exit in User Stories 1–3. Cancel always restores the exact previous state of the surface.

## Assumptions

- The enlarge key is ctrl+e (clarified). The planning phase chooses the dialog's Save, Cancel and Discard keys. They must not clash with the existing keys of the surfaces they guard, and must follow the app's key-hint conventions.
- One new three-choice dialog is shared by every guarded surface. The existing two-choice destructive-action confirm dialog is unchanged.
- Save in the dialog uses each surface's existing save or confirm behaviour and validation. No surface gains a new way to save. The one behaviour change is FR-012a: backing out of a follow-up confirmation now returns to the form.
- The launch wizard's name field keeps its current behaviour (esc only leaves the field). The wizard as a whole is the guarded surface.
- Read-only long text (run results, prompts, notifications) is out of scope for enlarge.
- Exits the developer did not start (disconnect, killed process, closed terminal, crash) are not protected, and edits are not kept across app restarts.
- This is a terminal-UI change only. No daemon or protocol changes are expected.
