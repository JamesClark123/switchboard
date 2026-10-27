# Tasks: App Forms — Enlargeable Text Fields & Unsaved-Changes Guard

**Input**: Design documents from `/specs/swi-9-feature-app-forms/` (plan.md, spec.md, research.md, data-model.md, contracts/forms-ui.md, quickstart.md)

**Tests**: Included. The constitution (Rule VI) requires automated tests and ≥90% coverage per module, so each story
carries its TUI tests, using the `update`/`press`/`runCmd`/`sized` and per-surface helpers already in `internal/ui`.

**Organization**: Tasks are grouped by user story (spec.md US1–US4) so each story can be implemented and tested on its
own.

All paths are relative to the repository root; `UI/` = `src/apps/switchboard-tui/internal/ui/`.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: can run in parallel (different files, no dependency on an incomplete task)
- **[Story]**: US1–US4 from spec.md

---

## Phase 1: Setup

**Purpose**: Remove the external editor and give every form one keymap and one Text constructor (R1).

- [ ] T001 Rename `kitFormKeyMap()` to `formKeyMap()` and move it to the new file `UI/formguard.go`. Keep its
  NewLine/Next/Prev/Submit bindings and add a keyless `Text.Editor = key.NewBinding()`. Doc comment: why it is keyless
  (`Text.Update` matches `Editor` unconditionally; `ExternalEditor(false)` only disables it when help renders). `Quit`
  stays ctrl+c until T017 routes ctrl+c through the guard, so ctrl+c keeps working in between.
  - Update every caller: `newKitForm` in `UI/kit_editor.go`, `mcpForm` in `UI/mcp.go`, `secretsForm` in
    `UI/secrets.go`, the orchestrator create/rename forms in `UI/orchestrators.go`, `newOrchForm` in
    `UI/run_actions.go`.
  - Apply it to the config editor form in `UI/config_editor.go`, which used huh's default keymap.
  - Rename the keymap test in `UI/kit_editor_keymap_test.go`.
- [ ] T002 Add `formSpec{texts map[*huh.Text]textEntry{ptr *string; title string}}` in `UI/formguard.go`, with:
  - `newFormSpec()`;
  - `text(ptr *string, title string) *huh.Text`: `huh.NewText().ExternalEditor(false).Title(title).Value(ptr)`,
    recorded;
  - `lookup(field huh.Field) (*huh.Text, textEntry, bool)`;
  - `normalize() []string`: `Update(nil)` on each recorded field, so its pointer holds the textarea's cleaned value
    (research R3). It returns the titles of fields whose final value differs from the **original** recorded by
    `text`, so CRLF conversion counts too. The surface shows the status
    `<title>: line endings/tabs/control characters will be rewritten when saved`.
  - `text` records the original value in its `textEntry`, then converts `\r\n` to `\n` in `*ptr` before building,
    so CRLF content is not doubled by the sanitizer (which maps each `\r` and `\n` to a newline).
- [ ] T003 Build every multi-line field through `formSpec.text`, storing the spec next to the form and dropping it
  with the form:
  - the 13 kit fields in `UI/kit_editor.go`, on `kitEditorState`;
  - MCP Headers in `UI/mcp.go`, on `mcpState`;
  - action Body/Arguments and event Fixed arguments in `UI/orchestrator_forms.go`, on `orchOpState` (set by
    `setOpForm` in `UI/run_actions.go`).

  Each `.Title(...)` moves into the `text(ptr, title)` call. No `huh.NewText()` call may remain outside
  `UI/formguard.go`.
- [ ] T004 Tests in `UI/formguard_test.go`:
  - `formKeyMap()` has a keyless Editor.
  - For every form with a Text field (kit agent instructions, MCP, action, event), sending ctrl+e straight to
    `form.Update` returns no `tea.ExecProcess` command, with `EDITOR` set and `WithShowHelp(false)`.
  - `normalize()` turns a stored tab into the textarea's spaces and reports that field; CRLF content keeps its line
    count **and** is reported (the notice mentions line endings).
  - No `form.KeyBinds()` help reads "open editor".
  - A source scan finds no `huh.NewText(` outside `formguard.go`.

---

## Phase 2: Foundational (blocking prerequisites)

**Purpose**: The guard machinery every story uses: surface identity and dispatch, the dialog layer, and change
snapshots (R3–R5).

**⚠️ CRITICAL**: No user-story work can begin until this phase is complete.

- [ ] T005 Add the `guardSurface` enum (data-model.md table) and these `Model` methods, each a `switch` over the enum,
  in `UI/formguard.go`:
  - `activeGuard() (guardSurface, bool)`, with nested-before-parent precedence. Every case also checks `m.screen`
    (data-model.md), so a form stranded on a screen the user has left is never active;
  - `guardDirty(g) bool`;
  - `guardSave(g) (Model, tea.Cmd)`: re-check `guardDirty(g)` first and just leave when clean (research R4);
    otherwise call the surface's save. The outcome is read from the resulting state;
  - `guardLeave(g) (Model, tea.Cmd)`;
  - `requestExit(g) (tea.Model, tea.Cmd)`: leave immediately when clean, otherwise open the dialog;
  - `reopenForm(g)`: rebuild a completed form with the surface's `build…Form(spec, vals)` from the **same** vals and a
    **fresh** `formSpec` (no `normalize()`), keeping the surface baseline (research R4; the builders are split in
    T039);
  - `reconcileGuardLayers()`: the lifetime invariant (data-model.md). Close the dialog and the enlarged view when
    their surface is no longer active. The parked-override part is added by T033. It must stay O(1): no sandbox-list
    walks.

  Unimplemented surfaces default to "never dirty" and are filled in by the stories. Every story task that adds a
  surface also adds its cases to these switches, so those tasks share `UI/formguard.go` and are not `[P]`.
- [ ] T006 Add `unsavedState` to `UI/unsaved.go` (`open`, `surface`, `fromEnlarge`, `highlight`, `saveLabel`,
  `openedAt`), plus a package `nowFunc = time.Now` and `unsavedKeyGrace = 150 * time.Millisecond`. Add a
  `pastGrace()` test helper in `UI/unsaved_test.go` that moves `nowFunc` past the window. Every story test that
  presses a dialog choice (T016, T020, T025, T030, T034) calls it. The suite does not use `t.Parallel`, so a package
  global is safe; restore it with `t.Cleanup`.
  - `openUnsaved(g, fromEnlarge)`, with Cancel highlighted, `openedAt = nowFunc()`, and the label "Rebuild" for
    `gReasoning`.
  - `updateUnsavedKey` per contracts/forms-ui.md §3:
    - within `unsavedKeyGrace` of `openedAt`, only esc/ctrl+c (Cancel) act and every other key is ignored, including
      enter, arrows, h/l and tab/shift+tab;
    - `s` Save (not on `gReasoning`), or on `gReasoning` `r` Rebuild → `confirmRebuildFromSetup()` in
      `UI/orchestrator_reasoning.go` (research R5):
      - build the `ReasoningSetup` as `startReasoningRebuildFromSetup` does, with `selectedKitRefs` first (a kit
        error shows on the setup);
      - the confirm body lists the edited sources, mode and kits;
      - `onConfirm` sends `reasoningRebuildMsg{…, setup}`, `returnTo = screenLaunch` and
        `acceptTo = screenOrchDetail`;
      - add a `screenLaunch` case to `View`'s confirm-background switch in `UI/app.go`.

      Do **not** reuse the `b` confirm: it rebuilds the saved setup;
    - `c`/esc/ctrl+c Cancel; `d` Discard; ←/→/h/l/tab/shift+tab move the highlight; enter activates the highlight;
      anything else is ignored.

    Save calls `guardSave`, Discard calls `guardLeave` (discarding an open enlarged view first), and Cancel only
    closes.
  - `unsavedModal()`: `modalInnerWidth()` wide in `modalStyle`. A ≤ 2-line body per surface kind, three buttons with
    the highlighted one marked, and the canonical hint `s save · d discard · esc cancel · ←/→ · enter` (or
    `r rebuild · …`). ≤ 11 rows; no line wraps at 80 columns.
- [ ] T007 Wire the dialog layer into `UI/app.go`:
  - `handleKey` routes to `updateUnsavedKey` when `m.unsaved.open`, after the host-password modal and sign-in card
    and before the screen switch.
  - `Update` calls `reconcileGuardLayers()` after handling every message.
  - `View` draws `overlayCenter(<current screen render>, unsavedModal)` when it is open.
  - **Overlay order (security, research R5)**: restructure `View` so that every return path (chrome, the screens
    that return early today such as launch, confirm and sources, and later the enlarged view and the dialog) goes
    through one final pass. That pass applies the host-password prompt and the sign-in failure card **last**. The
    prompt must never capture keys while hidden.
  - Add the dialog bindings to `UI/keys.go`.
- [ ] T008 Add snapshot types and helpers in `UI/formguard.go`:
  - `launchSnapshot` with `snapshotLaunch(launchState)` and an `equal` method (`slices.Equal` for the slices): sources
    in selection order (`l.order`; **not** sorted, since order changes what is sent), name trimmed, kits in order,
    host, orchestrator id;
  - `flattenCallVals(callFormVals) map[string]string`;
  - `kitFingerprint(store.Kit) string`: `yaml.Marshal` of a deep clone (yaml round trip), after `Normalize()`, plus
    the escape-hatch and services encodings (an empty list gives `""`).
    - Do not go through `SpecYAML()`: it errors on an unnamed kit.
    - Do not normalise the live kit: `Normalize()` mutates `Permissions.Network` through a shared pointer.
    - See `src/apps/switchboard-tui/internal/store/kit.go`.
- [ ] T009 Unit tests in `UI/unsaved_test.go` and `UI/formguard_test.go`:
  - the dialog opens on Cancel, and enter there cancels;
  - ←/→ wrap; `d` and enter@Discard discard; `s` and enter@Save save; esc and ctrl+c cancel; `y`/`n`/`q` are ignored;
  - the label reads Rebuild for `gReasoning`; `r` opens a confirm whose body lists the **edited** setup; n there
    returns to the setup with `m.launch` intact (setup still drawn behind the confirm); `s` is ignored;
  - within the grace window (via `nowFunc`) letters, enter, →/← and tab are ignored, so "→, enter" can't discard; esc
    and ctrl+c cancel at once;
  - no line of the modal is wider than 80 cells at 80×24, for both hint variants;
  - with the dialog open and `hosts.connecting = true`, `hosts.pwMode = pwSignIn` (startup sign-in), `View()` shows
    the password prompt on top;
    the same holds on the launch and sources screens (T007 overlay order);
  - `requestExit` on a clean surface leaves without opening the dialog;
  - snapshot helpers: the kit fingerprint is stable across a nil vs empty slice, differs for an unnamed kit with an
    added item, and leaves the input kit unmutated; the launch name is trimmed; re-ordering sources changes the
    launch snapshot;
  - `reconcileGuardLayers` closes a dialog whose surface is gone.

**Checkpoint**: the dialog layer exists and is unit-tested; no surface uses it yet, so behaviour is unchanged.

---

## Phase 3: User Story 1 — Never lose edits by accident (Priority: P1) 🎯 MVP

**Goal**: Every multi-field form raises the Save/Cancel/Discard dialog on esc when changed, and closes immediately
when not.

**Independent Test**: For each form, change a field → esc → dialog; Cancel leaves the form identical; Discard closes
with nothing applied; Save behaves as ctrl+s; an unchanged form or an edit undone back closes with no dialog.

**Baseline rule for T010–T015** (research R3): take each baseline **after** the `huh.Form` is built and after
`spec.normalize()`. A form rebuilt by `reopenForm` keeps its baseline. A save that fails on a form completed by enter
calls `reopenForm`.

- [ ] T039 [US1] Split every form builder into `fill…Vals(source)` (open only) and `build…Form(spec, vals)` (open and
  `reopenForm`; **never fills `vals` from the source**. huh `Select` and `spec.text` still write the bound pointer
  while building, which is expected) (research R4):
  - `UI/kit_editor.go`: the section forms (`identityForm`, `networkForm`, `environmentForm`,
    `agentInstructionsForm`, …) and `openKitItemForm`;
  - `UI/orchestrator_forms.go`: `openActionForm`, `openEventForm`, `openCallForm`;
  - `UI/run_actions.go`: `openMarkFreeForm`;
  - `UI/orchestrators.go`: `openOrchCreateForm`/`openOrchRenameForm`.

  `mcpForm(v)` and `secretsForm(v)` already build from vals, so they only need registering. The config editor's
  split is part of T010.

  Register the per-surface builder in the `reopenForm` switch.
- [ ] T010 [US1] Config editor (`gConfig`) in `UI/config_editor.go`:
  - Add a heap-allocated `*configFormVals{name, mode string; strs map[string]*string; bools map[string]*bool}`, held
    as a pointer on `editorState`. The option maps follow the manifest's options that `buildEditorForm` creates.
  - Add `snapshot()` flattening it to a `configSnapshot`, compared with `maps.Equal`. The baseline is taken after
    `applyManifest` builds the form.
  - `saveConfig` reads the vals instead of `GetString`/`GetBool` (fixing the pre-blur read).
  - esc → `requestExit(gConfig)`; Save = `saveConfig`; Leave = today's esc.
- [ ] T011 [US1] MCP registration (`gMcp`) in `UI/mcp.go`: baseline = a copy of `mcpFormVals` once the form is built; esc →
  `requestExit`; Save = `applyMcpForm`; Leave = close the form.
- [ ] T012 [US1] Secrets (`gSecrets`) in `UI/secrets.go`: baseline = a copy of `secretFormVals`; esc →
  `requestExit`; Save = `applySecretsForm`; Leave = close the form.
- [ ] T013 [US1] Orchestrator create/rename (`gOrchAdmin`) in `UI/orchestrators.go` and
  `UI/daemon_orchestrators.go`: baseline = a copy of `orchFormVals` (the prefill for rename); esc → `requestExit`;
  Save = `applyOrchForm`; Leave = reset the form.
- [ ] T014 [US1] Op forms (`gOpForm` for each `orchSurface`), routed from `UI/jobs.go` and `UI/orchestrator_views.go`:
  - Add a `baseline` on `orchOpState` set by `setOpForm` in `UI/run_actions.go`: a value copy of
    `actionFormVals`/`eventFormVals`/`markFreeVals`, or `flattenCallVals` for the call form.
  - Dirty compares the same form of the current vals.
  - esc → `requestExit`; Save = `submitOrchOpForm`; Leave = `closeOpForm` plus today's status reset and the
    mark-free return to the list.
- [ ] T015 [US1] Kit section and item forms (`gKitItem`) in `UI/kit_editor.go`: baseline = a copy of `kitFormVals`
  after the form fills it; esc → `requestExit`; Save = `applyKitForm`; Leave = close the form with the kit untouched.
- [ ] T016 [US1] Guard tests in `UI/guard_surfaces_test.go`: one table-driven walk over config, MCP, secrets,
  orchestrator create, orchestrator rename, action, event, call, mark-free and a kit item form. Each case covers:
  - an unchanged esc closes with no dialog;
  - a changed esc opens the dialog;
  - Cancel leaves the vals, focused field and `form.View()` identical;
  - Discard closes with no RPC or store write;
  - Save performs the surface's save, and a validation error keeps it open with the error (US1 #6);
  - enter on the last field with an invalid value leaves a visible, editable form with the error and **the values
    the user typed**, not a blank one or one reset to the originals (`reopenForm`); ctrl+e still enlarges a Text
    field after the reopen (fresh registry);
  - Save from the dialog after a background update made the surface clean just leaves, with no save attempted;
  - a stored value containing a tab, tabbed past without editing → esc closes with no dialog (FR-011);
  - type-and-delete back to the original → no dialog (US1 #7);
  - a create form with nothing typed → no dialog (US1 #8);
  - an op form whose save fails keeps the form open with the error (spec Edge Cases);
  - ctrl+s on an op form, esc while the RPC is in flight (dialog open), then the success reply → the form, and with it
    the dialog, are closed (spec Edge Cases, background update).

**Checkpoint**: esc on every form is guarded. This is the MVP.

---

## Phase 4: User Story 2 — Guard every way out, not just esc (Priority: P1)

**Goal**: ctrl+c in forms, `q` on the kit overview and backing out of "register anyway?" are guarded or non-lossy too,
and the app never quits from inside a form.

**Independent Test**: With changes, ctrl+c in each form and esc/q on the kit overview raise the dialog. "register
anyway?" n returns to the form intact. Text keys like `q`, `,` and shift+→ are typed.

- [ ] T017 [US2] Make `formKeyMap()`'s `Quit` keyless (doc comment: ctrl+c is routed by the guard) and route ctrl+c
  through `requestExit` exactly like esc on every form surface. huh then never aborts, so delete the `StateAborted`
  branches. Extend T004's test: ctrl+c sent straight to `form.Update` does not abort.
  - Files: `UI/config_editor.go`, `UI/mcp.go`, `UI/secrets.go`, `UI/orchestrators.go`, `UI/daemon_orchestrators.go`,
    `UI/orchestrator_forms.go` (`advanceOrchOpForm`), `UI/jobs.go`, `UI/orchestrator_views.go`, `UI/kit_editor.go`.
  - Mark-free ctrl+c now returns to the sandbox list when opened from it, as esc does.
- [ ] T018 [US2] Kit overview (`gKitOverview`) in `UI/kit_editor.go`:
  - Set `kitEditorState.baseline = kitFingerprint(kit)` when the editor opens, after migration. `saveKit` returns to
    the picker, so no reset is needed.
  - Overview esc and q → `requestExit(gKitOverview)`; Save = `saveKit`; Leave = `enterKitPicker`.
  - The item list's esc/q stay as they are (not exits).
- [ ] T019 [US2] FR-012a in `UI/mcp.go`:
  - `applyMcpForm` keeps `vals` and the baseline while `pending` is set.
  - Backing out of "register anyway?" (n/esc/q) calls `reopenForm(gMcp)` and shows no "registration cancelled"
    status.
  - y registers and clears the vals as today.
  - Change "esc cancel" to "esc back to form" in both `mcpModal` and `mcpHelp` (mcp.go:197).
- [ ] T020 [US2] Tests in `UI/guard_surfaces_test.go` and `UI/mcp_test.go`:
  - ctrl+c on each form: dialog when changed, closes when not, never `tea.Quit`;
  - Discard after ctrl+c returns to the opening screen;
  - kit: apply an item then esc/q on the overview → dialog; `d` returns to the picker and the kit on disk is
    unchanged; `s` writes it;
  - a migrated v1 kit opened and left with q → no dialog;
  - a new unnamed kit with an added install command → q → dialog, and `s` stays open with "name required (Identity)";
  - esc/q on an item list → overview, no dialog;
  - applying an unchanged item form leaves the overview clean;
  - MCP host command → "register anyway?" → n → form back with every value, and the next esc raises the dialog;
  - in focused Text and Input fields, `q` and `,` are typed, and shift+→ is a no-op; none of them exits (US2 #5).

**Checkpoint**: every FR-012 exit on forms is guarded.

---

## Phase 5: User Story 3 — Guard inline inputs, wizards and nested editors (Priority: P2)

**Goal**: Inline inputs, the launch wizard, the reasoning setup, the sources overlay and the add browser get the same
dialog, with the step-back keys untouched.

**Independent Test**: rename with a typed char → esc → dialog; two sources picked in the launch wizard → esc →
dialog; the reasoning setup's Save is labelled Rebuild; sources Save → removal confirm, decline returns, confirm
closes.

- [ ] T021 [US3] Rename (`gRename`) in `UI/app.go`:
  - Record `renameInitial = TrimSpace(display name)` in `enterRename`.
  - Dirty = `TrimSpace(value) != renameInitial`; esc → `requestExit`.
  - Save = the enter path, except an empty trimmed value keeps the input open with status "name can't be empty"
    (FR-011a). enter's own behaviour is unchanged.
  - Leave = today's esc.
- [ ] T022 [US3] Tag (`gTag`) in `UI/tageditor.go`, add host (`gHostAdd`) in `UI/hosts.go` and add group
  (`gGroupAdd`) in `UI/groups.go`:
  - trimmed-initial dirty checks: the tag; `""` for host and group;
  - esc → `requestExit`; Save = the enter path (an empty tag clears it); Leave = today's esc.
- [ ] T023 [US3] Launch wizard (`gLaunch`) and reasoning setup (`gReasoning`) in `UI/launch.go` and
  `UI/orchestrator_reasoning.go`:
  - Add `launchState.baseline = snapshotLaunch(...)` set in `enterLaunch`, `enterLaunchWithConfig` and
    `enterReasoningSetup` (after the saved-setup prefill).
  - The main esc → `requestExit`. The name field's esc and the host, orchestrator and kit pickers' esc are unchanged.
  - Save = `startLaunch` for the wizard and `startReasoningRebuildFromSetup` for the reasoning setup.
  - Leave = today's esc (list or orchestrator detail).
  - Align `launchHelp` in reasoning mode with the modal help: no N/H/O, and "enter rebuild" (R8).
- [ ] T024 [US3] Sources overlay (`gSources`) and add browser (`gSourcesBrowse`) in `UI/sources.go` and
  `UI/confirm.go`:
  - Add `confirmState.acceptTo screen`: used on y/enter when set, otherwise `returnTo`.
  - Overlay dirty = `len(marked) > 0`; esc/q → `requestExit`. Save = `confirmRemoveSources(exitAfter=true)`, which sets
    `acceptTo = screenList`. Confirming sends the RPC and closes the overlay; n/esc returns to `screenSources` with
    marks kept.
  - Browser dirty = `len(launch.selected) > 0`; esc → `requestExit`. Save = `dispatchAddSources`; Leave = back to the
    overlay.
- [ ] T025 [US3] Tests in `UI/guard_surfaces_test.go`, `UI/sources_test.go` and `UI/launch_test.go`, covering US3
  #1–#7:
  - rename: whitespace-only change → no dialog; empty Save → stays open with the message, Discard keeps the name;
  - tag: cleared → Save clears it;
  - host and group add;
  - launch: two sources → dialog, Save launches; name field and picker esc are not exits;
  - reasoning: the dialog shows Rebuild/`r`; `r` opens the confirm, and y sends a `reasoningRebuildMsg` whose `setup`
    carries the **edited** `Sources`/kits (not nil) and lands on the orchestrator detail; n returns to the setup with
    its edits; Discard returns to the detail; un-marking and re-marking a pre-filled folder (order change) raises the
    dialog;
  - sources: Save → confirm → n keeps marks and the overlay, y removes and closes (`sourcesView` is reset only on y);
  - browser: Discard returns to the overlay;
  - ctrl+c on these surfaces behaves as before.

**Checkpoint**: every guarded surface in FR-010 is covered.

---

## Phase 6: User Story 4 — Enlarge a multi-line text field (Priority: P2)

**Goal**: ctrl+e opens an editable full-terminal view of the focused multi-line field and writes back on close; no
external editor anywhere.

**Independent Test**: Kit agent instructions with 50 lines → ctrl+e shows far more lines; edit, esc → the inline
field holds the edit and focus stays on it, with no dialog.

- [ ] T026 [US4] Add `enlargeState` (data-model.md) with these methods in `UI/enlarge.go`:
  - `openEnlarge(g, field, entry)`: a textarea with `MaxHeight 0`, `CharLimit 0`, `ShowLineNumbers false`,
    `Prompt ""`, the default keymap with `LineEnd = end` and `InsertNewline = enter/alt+enter/ctrl+j`, value `*ptr`,
    cursor at the top (via `ta.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})`, i.e. `KeyMap.InputBegin`; bubbles v1.0.0
    has no exported move-to-start method, and the keymap must keep `InputBegin`), focused, `baseline = *ptr`.
  - `enlargeStyles()`: map huh's focused `FieldStyles` to a `textarea.Style` the way `Text.View` does (huh
    field_text.go:436–443).
  - After every `SetValue` (open, ctrl+r), `SetWidth` and `SetHeight`, call `ta.Update(nil)` so the cursor is
    scrolled into view (research R2).
  - `writeBack()`: `*ptr = ta.Value(); field.Value(ptr); field.Update(nil)`.
  - `resize(w, h)`.
  - `enlargeView(w, h)`: a rounded border at full size with the title in the top border, the textarea, and the hint
    line from contracts/forms-ui.md §2 (76 cells).
  - Size constants that give 21 text lines at 80×24 and at least one line on smaller terminals.
- [ ] T027 [US4] Add `updateEnlargeKey` in `UI/enlarge.go`:
  - esc/ctrl+e → write back and close;
  - ctrl+s → write back, close, then `guardSave(surface)`;
  - ctrl+c → write back, then `requestExit(surface)`; if that opens the dialog, set `fromEnlarge` and keep the view
    open;
  - ctrl+r → `SetValue(baseline)`;
  - pgup/pgdown → `CursorUp`/`CursorDown` × the view height, then a single `ta.Update(nil)` (R9);
  - tab/shift+tab → nothing;
  - everything else → `ta.Update`.

  Discard or Save from a dialog with `fromEnlarge` closes the view first (T006).
- [ ] T028 [US4] Add the form key pre-handler `formKeys(g, form, spec, msg)` in `UI/formguard.go`, used by every form
  surface's key branch from US1/US2:
  - ctrl+e on a focused registered Text → `openEnlarge`;
  - esc/ctrl+c → `requestExit`;
  - ctrl+s → the surface's save;
  - otherwise → the form.

  This consolidates the per-surface esc/ctrl+s branches added in T010–T018 into one path.
- [ ] T029 [US4] Wire the enlarged view into `UI/app.go`:
  - `handleKey` routes to `updateEnlargeKey` when `m.enlarge.open`, after the dialog layer.
  - `View` returns `enlargeView` instead of the screen chrome (the dialog overlays it).
  - `tea.WindowSizeMsg` calls `resize`.
  - `forward()` passes non-key messages (cursor blink) to the enlarged textarea while it is open.
  - The footer help for form screens adds `ctrl+e enlarge` only while the focused field is a registered Text (FR-002).
- [ ] T030 [US4] Tests in `UI/enlarge_test.go`:
  - ctrl+e opens on each of the 17 fields (table over kit/MCP/action/event forms) and the footer hint is present;
  - no hint on an Input; ctrl+e on an Input moves to end of line; on a Select it is a no-op;
  - edit + esc writes back, focus stays on the same field, the form is dirty, and there is no dialog;
  - ctrl+e closes too;
  - ctrl+s closes and saves (kit item applies, op form submits);
  - ctrl+c over a changed form → dialog over the view → `c` back with text, cursor row/col and scroll intact →
    ctrl+r restores the pre-ctrl+e text (FR-005b);
  - ctrl+c over an unchanged form closes it;
  - tab/shift+tab are no-ops;
  - resize keeps text and cursor;
  - 80×24 shows ≥ 16 text lines and the hint line, and every rendered line is ≤ 80 cells; 200×50 uses ≥ 80%
    (SC-003); 60×12 renders within the terminal without overflow;
  - the cursor is inside the visible window after open, after ctrl+r at the end of a long text, and after a resize
    that shrinks the height;
  - CRLF content opens and writes back with its original line count;
  - with the view open and a sign-in password prompt raised, `View()` shows the prompt;
  - a value with a tab: open and close with no edit → the form stays clean;
  - 150 lines + enter at end → 151, close, the kit saves 151 lines; inline enter at line 99+ adds none (FR-009a,
    US4 #8).
- [ ] T031 [US4] (after T029) Add `BenchmarkSC006Enlarged{Type,Cursor,Page}`, `BenchmarkSC006InlineFocused{Type,Cursor}` and
  `BenchmarkSC006OtherField` in `UI/enlarge_perf_test.go`:
  - 200×50, 5,000 lines of ~85 chars, `Update` + `View` per op, driven through the real `Model` key path;
  - a doc comment cites SC-006 and R9's reference numbers;
  - not a timed assertion in CI.

**Checkpoint**: all four stories are complete.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [ ] T032 Forced close (R7). Add `forceClose(g, reason)` in `UI/formguard.go`: close the surface and drop the
  dialog and enlarged view. When the surface was dirty, set `unsaved edits lost: <reason>`, or append
  `; unsaved edits lost` to a status the caller already set. Call it from:
  - the MCP view drop (`UI/hosts.go`, `UI/daemons.go`);
  - `closeOrchAdminOnDrop` (`UI/hosts.go`), which gains a `screenLaunch` case for a reasoning setup on the dropped
    host;
  - Sandbox removal, in the message handlers rather than `reconcileGuardLayers`, for rename, tag, sources overlay or
    add browser:
    - `Event_Removed` for the id (`removeSandbox`, `UI/notifications.go`) → "sandbox removed";
    - a list reload, in the `sandboxesMsg`/`listDataMsg` handlers (`UI/app.go:667/673`), where the host is
      `HostConnected` with `Err == nil` and lacks the id → "sandbox removed". The local `sandboxesMsg` (`refreshCmd`,
      `UI/app.go:523–532`) carries no per-host status; it is authoritative only because its errors arrive separately
      as `errMsg`;
    - the host dropped (`applyHosts`, `UI/hosts.go`) → "daemon disconnected";
    - a host `List` error → no close.
  - Orchestrator removal (`UI/orchestrators.go`, `UI/orchestrator_views.go`, `UI/jobs.go`):
    - `syncOrchestratorChanged` issues a `GetOrchestrator` check whenever an active guarded surface targets that
      orchestrator (reasoning setup `launch.reasoning.id`, orchestrator rename form, `opState(s).formTarget`);
    - a NotFound reply, `applyOrchDetail` receiving NotFound (a new branch), or a Jobs reload dropping the
      orchestrator calls `forceClose(…, "orchestrator removed")`.
- [ ] T033 Park async add-overrides and guard terminal screen switches (R7):
  - The `sourcesAddResultMsg` handler in `UI/sources.go` appends `parkedOverride{msg, name}` to a FIFO,
    `m.pendingOverrides`, instead of calling `confirmAddOverride`, while any guarded surface is active (the whole kit
    editor included). The one exception is the sources overlay of the **same** sandbox with no dialog or browser open
    (`overlayFor(id)`).
  - `reconcileGuardLayers` in `UI/formguard.go` dequeues the head and calls `confirmAddOverride(msg, name)`, building
    the confirm then so `returnTo` is current, but only:
    - on a **non-key** message;
    - when neither `screenConfirm` nor `screenApproval` is showing;
    - when no guarded surface is active, or the only active one is that sandbox's own clean overlay.
  - `UI/terminal.go`: `termOpenedMsg` switches to the terminal only when `m.screen == screenList`. Otherwise it
    detaches the new session itself (`msg.session.Close()`, `msg.cancel()`, `close(msg.done)`) and sets the status
    `terminal for <name> ready — closed because you left the list; press t again`. `termClosedMsg` changes the screen
    only when `m.screen == screenTerminal`.
- [ ] T034 Tests for T032/T033 in `UI/guard_surfaces_test.go`:
  - host drop with the MCP form dirty and the dialog open → closed, with the status naming "daemon disconnected";
  - orchestrator admin drop with a dirty rename → "<host> disconnected — orchestrators closed; unsaved edits lost";
  - clean → no "lost" status;
  - sandbox removed during rename and sources, both by `Removed` event and by a connected, error-free list reload
    without it; a reload where the host is disconnected or its `List` errors leaves rename open;
  - orchestrator removed during the reasoning setup, the orchestrator rename form and an op form;
  - an override arriving during rename is parked and shown after rename closes by enter;
  - an override for sandbox A arriving while B's overlay has marks is parked, and B's marks survive;
  - two overrides parked during a rename open one after the other; an override never replaces the sources removal
    confirm the guard's Save opened;
  - an override during unsaved kit edits on a section item list is parked;
  - a terminal finishing opening during a rename, or during the guard's sources removal confirm, leaves that screen
    as it was, with the "terminal … ready" status, and the session is closed;
  - the enter that closes a rename does not accept a parked override; the override opens on the next non-key
    message;
  - an override parked while the same sandbox's add browser was open, dequeued after the overlay closed, has
    `returnTo = screenList`.
- [ ] T035 [P] Update the key documentation in the repo-root `README.md` ("Using the TUI" section): ctrl+e enlarge (no external
  editor), the enlarged-view keys, the unsaved-changes dialog keys, and ctrl+c in forms ≡ esc.
- [ ] T036 Run quickstart.md scenarios 1–7 at 80×24 and 200×50, and record the `BenchmarkSC006*` results in the PR
  description.
- [ ] T037 Gates: `PATH=$HOME/go/bin:$PATH make fmt-check vet lint cover` (≥ 90% for `switchboard-tui`) and
  `go clean -testcache && make e2e`. Fix any regressions in existing esc/ctrl+c tests whose expectations change by
  design (list the changed expectations in the PR).
- [ ] T038 Mark tasks complete and sync `CLAUDE.md`'s SPECKIT block if the implementation deviates from the plan.

---

## Dependencies & Execution Order

### Phase dependencies

- **Setup (Phase 1)**: none; it starts immediately.
- **Foundational (Phase 2)**: depends on Setup. It blocks all stories.
- **US1 (Phase 3)** depends on Phase 2.
- **US2 (Phase 4)** depends on US1: it reuses each form's baseline and `requestExit` wiring.
- **US3 (Phase 5)** depends only on Phase 2. It can be developed alongside US1/US2, but it shares the
  `UI/formguard.go` dispatch switches and `guard_surfaces_test.go` with them, so merge sequentially.
- **US4 (Phase 6)** depends on Phase 2 and on US1/US2 for T028 (it consolidates their key branches). T026 and T027
  can start after Phase 2. T031's benchmarks drive the real `Model` key path, so they need T029.
- **Polish (Phase 7)** depends on all stories.

### User story dependencies

- US1 → US2 (the same form surfaces). US3 is independent. US4's view (T026/T027) is independent; its routing (T028)
  follows US1/US2.

### Within each story

- Surface wiring before its tests. Each surface task is self-contained, with baseline, dirty, Save and Leave together.

### Parallel opportunities

The `guardSurface` dispatch lives in one file (`UI/formguard.go`), and every surface task adds cases to it. Surface
tasks are therefore sequential even though their main edits are in different files. The genuinely parallel ones are:

- T031 (benchmarks) alongside T030, once T029 is done.
- T035 (README) at any time after the contract is final.

## Parallel Example: User Story 4

```text
T026–T030 [US4] enlarged view + routing + tests   (UI/enlarge.go, UI/formguard.go, UI/app.go, UI/enlarge_test.go)
T031      [US4] SC-006 benchmarks (after T029)     (UI/enlarge_perf_test.go)
```

## Implementation Strategy

### MVP first (US1)

Phases 1–3 make esc on every form safe, the highest-value part of the ticket. Ship-ready on their own.

### Incremental delivery

1. Phases 1–2: no behaviour change except that the external editor is gone. ctrl+e on a multi-line field moves to
   end of line until US4.
2. Add US1 to guard esc on forms.
3. Add US2 to cover ctrl+c, the kit overview and FR-012a.
4. Add US3 to cover inline inputs, wizards and sources.
5. Add US4 for enlarge.
6. Add Polish for forced closes, docs and gates.

## Notes

- `[P]` tasks touch different files and have no dependency on an incomplete task.
- Save always reuses the surface's existing save or confirm. Do not add a second save path (spec Assumptions).
- The external editor must not come back through a new form: T004's source scan enforces `formSpec.text`.
