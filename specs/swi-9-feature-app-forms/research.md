# Research: App Forms — Enlargeable Text Fields & Unsaved-Changes Guard

**Feature**: `specs/swi-9-feature-app-forms/spec.md` | **Date**: 2026-10-27

All file references are under `src/apps/switchboard-tui/internal/ui/` unless noted. huh refs are to
`github.com/charmbracelet/huh@v1.0.0`; textarea refs are to `github.com/charmbracelet/bubbles@v1.0.0/textarea/textarea.go`.

## Current state (inventory that drove the decisions)

- **Forms** are embedded `*huh.Form`s, never `Run()`, so `CancelCmd`/`SubmitCmd` are nil. Each screen's `update*Key`
  checks esc and ctrl+s **before** the form sees a key. ctrl+c is not intercepted anywhere: it reaches huh's
  `KeyMap.Quit` (keymap.go:109), which sets `StateAborted` (form.go:558–562). Each surface's `advance*` then closes the
  form exactly as esc does. Mark-free is the exception: there, ctrl+c closes the form but does not return to the
  sandbox list, as esc does when it was opened from there (run_actions.go / orchestrator_forms.go:442–444).
- **Seven form surfaces**:
  - config editor (Input/Select/Confirm, read back by key with `GetString` — no pointers);
  - MCP registration (1 Text: Headers);
  - secrets (Inputs);
  - orchestrator create/rename;
  - action / timed-event / call forms (3 Texts: Body, Arguments, Fixed arguments);
  - mark-free;
  - the kit editor (13 Texts across section and item forms).

  All except the config editor bind values by pointer into heap-allocated `*…Vals` structs. All except the config
  editor use `kitFormKeyMap()` (kit_editor.go:1123); the config editor uses huh's default keymap.
- **Non-form surfaces**: the launch wizard and reasoning setup (`launchState`, launch.go), the sources overlay and add
  browser (`sourcesState`, sources.go), and four `textinput` inline inputs (rename `app.go:1386`, tag
  `tageditor.go:28`, add host `hosts.go:274`, add group `groups.go:164`). None has a dirty check.
- **Modal pattern**: the destructive confirm is a *screen* (`screenConfirm`, confirm.go) with `returnTo`. Rendering
  uses `overlayCenter` (overlay.go). There is no global key layer: `handleKey` (app.go:938) checks the host-password
  modal and the sign-in card, then switches on `m.screen`.
- **External editor**: no code calls `.Editor(`/`ExternalEditor`, so huh's defaults apply everywhere
  (`externalEditor: true`, `$EDITOR` or `nano`). The editor's `updateValueMsg` reaches the form through `forward()`.

## R1 — Remove the external editor reliably

**Decision**:
- Every form uses one shared keymap, `formKeyMap()`. This is `kitFormKeyMap()` renamed and extended, and it is now
  also applied to the config editor. It sets `Text.Editor = key.NewBinding()` (no keys) and `Quit = key.NewBinding()`
  (no keys).
- Every multi-line field is built through one constructor, `formSpec.text(ptr, title)`, which calls `ExternalEditor(false)`.

**Rationale**:
- In huh v1.0.0, `Text.Update` matches `keymap.Editor` *unconditionally* (field_text.go:321). `ExternalEditor(false)`
  only takes effect through `KeyBinds()` → `SetEnabled(false)`, and `KeyBinds()` is only called when help is rendered.
  The orchestrator forms use `WithShowHelp(false)`, so on them `ExternalEditor(false)` alone would still launch
  `$EDITOR`.
- A binding with no keys never matches whatever its enabled state, and `ExternalEditor(false)` hides it from huh's own
  help line. Using both is belt and braces.
- A keyless `Quit` means huh can never abort a form. ctrl+c is routed by the guard instead (R4), and the surfaces'
  `StateAborted` branches become dead code and are removed.

**Alternatives rejected**:
- Keeping huh's editor binding relabelled "enlarge" and intercepting ctrl+e first: any missed interception would
  silently launch an editor.
- Forking huh: unnecessary given the above.

**Verification**: a test opens every form that has a Text field and presses ctrl+e with the enlarge interception
bypassed. It asserts the returned command is not a `tea.ExecProcess`. A test also asserts that huh's
`form.KeyBinds()` never contains a binding whose help reads "open editor".

## R2 — Enlarge: interception, the enlarged textarea and write-back

**Decision**:
- **Registry**: `formSpec` records `map[*huh.Text]textEntry{ptr *string; title string}`. huh's chainable setters
  return the same pointer, so `spec.text(&v.body, "Body").Key("body").Lines(5)` registers the field under the pointer
  the form holds. `text` sets the title itself, because `huh.Text` has no title getter and the enlarged view needs the
  title.
- **Interception**: the guard sees each key before the form does. ctrl+e opens the enlarged view only when
  `form.GetFocusedField()` is a registered `*huh.Text`. On an `Input`, ctrl+e passes through to the textinput's own
  `LineEnd` (move to end of line, as today). On Select/Confirm it falls through and does nothing.
- **Enlarged view**: a separate `textarea.Model`.
  - `MaxHeight = 0` (no line cap; the textarea's own `maxLines = 10000` still applies) and `CharLimit = 0`.
  - The keymap is the textarea default with `LineEnd` reduced to `end`, and `InsertNewline` bound to
    `enter`/`alt+enter`/`ctrl+j`, matching the forms' `Text.NewLine`.
  - `ShowLineNumbers = false` and `Prompt = ""`, as `huh.NewText` sets them. `textarea.New()` defaults to line numbers
    and a `┃ ` prompt, and with `MaxHeight 0` its line-number gutter is sized for one digit.
  - Non-key messages (cursor blink) reach the enlarged textarea through `forward()` while it is open.
  - It is styled like huh's focused Text: `huhTheme()` returns huh `FieldStyles`, not a textarea `Style`, so
    `enlargeStyles()` copies the mapping `Text.View` applies (huh field_text.go:436–443: `TextInput.Text`,
    `.Placeholder`, `.Cursor`, `.CursorLine` from the focused theme).
  - It opens with the field's current value (`*ptr`; huh keeps it live, see R3) and the cursor **at the top**,
    because the view is mostly for reading a long value from its start. bubbles v1.0.0 exports no move-to-start
    method (`moveToBegin` is unexported, textarea.go:874), so the view sends
    `ta.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})` through `KeyMap.InputBegin` (:90, :1051). This also re-scrolls, and
    the custom keymap keeps `InputBegin`.
  - **Cursor visibility (FR-004, FR-008)**: bubbles' textarea re-scrolls to the cursor only inside `Update`
    (textarea.go:1087). `SetValue` resets the viewport to the top without following the cursor (:596–600), and
    `SetWidth`/`SetHeight` don't re-scroll at all (:947). So every `SetValue` (open, ctrl+r), `SetWidth` and
    `SetHeight` (resize) is followed by `ta.Update(nil)`, and the write-back by `field.Update(nil)`, so that the
    cursor is in view before the next frame.
  - tab and shift+tab are swallowed (FR-004).
  - pgup/pgdown move the cursor by one view height with `CursorUp`/`CursorDown`, then reposition the view with a
    single `Update` (R9).
- **Write-back** (close with esc/ctrl+e, ctrl+s or ctrl+c): `*ptr = ta.Value()`, then `field.Value(ptr)` to reload the
  field's textarea (`SetValue`) from the same pointer.
  - Focus is still on that field: the form's focus never moved while the view was open.
  - The inline cursor lands at the end of the text (`SetValue` behaviour), which the spec does not constrain.
    `field.Update(nil)` then scrolls the inline field so that it is visible.
- **Revert** (FR-005b): `ctrl+r` "revert" restores `baseline`, the text captured when ctrl+e opened the view.
  - The ctrl+c write-back does not move the baseline, so revert after a ctrl+c → Cancel round trip still restores the
    pre-ctrl+e text.
  - `ctrl+r` is unbound in the textarea keymap and on every form surface.
- **Size**: the enlarged view is drawn instead of the screen chrome, covering the header and footer (FR-003): a
  rounded border at full terminal size with the field's title set into the top border, the textarea, and one hint
  line below. At 80×24 that leaves 24 − 2 border − 1 hint = **21 text lines** (SC-003 ≥ 16). At 200×50 it uses 100%
  of the width and 47/50 of the height for text. Below 80×24 the textarea shrinks, down to a minimum of one line, so
  the view shows fewer lines rather than overflowing. On `tea.WindowSizeMsg` the view calls `SetWidth`/`SetHeight`, which keeps text, cursor row and column (FR-008).

**FR-009a check (round trip > 99 lines)**: verified on 2026-10-27 with a throwaway test against huh v1.0.0.
- A 150-line value bound to `huh.Text` stays 150 lines through focus, ctrl+end and enter at the end. MaxHeight blocks
  the new line, and the value is kept.
- Writing back 151 lines via `field.Value(ptr)` gives a 151-line field value.
- **`huh.Text` is therefore kept**; no replacement field is needed. An automated test (T-level, see tasks) pins this:
  write back 151 lines, then save, and the saved value has 151 lines.

## R3 — Change detection (dirty state)

**Decision**: each guarded surface has a `baseline` captured when it opens and a pure `dirty()` comparing the current
values against it.

**Baseline rule** (applies to every form):
1. Take the baseline **after** the `huh.Form` is built. huh's `Select.Value`/`Options` write the selected option into
   the bound pointer during construction, so a baseline taken before would be dirty on open.
2. First call `spec.normalize()`: send each registered Text a no-op `Update(nil)`. `Text.Update` ends with
   `accessor.Set(textarea.Value())` (field_text.go:350), so this writes the textarea's *cleaned* value back to the
   pointer. The textarea's input cleaning (bubbles `runeutil` sanitizer) turns tabs into 4 spaces, turns **each**
   `\r` and `\n` into a newline (so CRLF text gets doubled line breaks; runeutil.go:70), and strips other control
   characters. Without this step, merely tabbing past a field whose stored text holds a tab would mark it changed
   (FR-011), and so would the enlarged view's write-back.
3. **Values the textarea would rewrite.** huh already applies this rewrite today on any edit or blur of such a field,
   so saving rewrites the stored text. The plan keeps that behaviour but makes it safe and visible:
   - `spec.text` records the **original** value in its `textEntry`, then converts `\r\n` to `\n` in the bound value
     **before** the field is built, so CRLF content keeps its line structure and is not doubled.
   - `normalize()` reports every field whose final (cleaned) value differs from that recorded original, so CRLF
     conversion is reported as well as tabs, a lone `\r` and control bytes. The surface shows a status notice:
     `<field title>: line endings/tabs/control characters will be rewritten when saved`. The developer learns about
     the rewrite before saving, and the baseline (the normalised value) does not raise a spurious dialog.

The values are snapshots compared with `==` where every field is a scalar, and with `maps.Equal`/`slices.Equal`
otherwise:

| Surface | Snapshot |
|---|---|
| huh forms bound to `*…Vals` (MCP, secrets, orchestrator admin, action, event, mark-free, kit item) | a value copy of the vals struct (every field is a string, bool or int); compared with `==` |
| Call form (`map[string]*string`) | flattened to `map[string]string`, compared with `maps.Equal` |
| Config editor | the option fields come from the host's manifest, so they cannot be fixed struct fields. A heap-allocated `*configFormVals{name, mode string; strs map[string]*string; bools map[string]*bool}` is bound by pointer, held as a pointer on `editorState` (config_editor.go:18–20 warns that pointers into the value-type model dangle), and flattened to `map[string]string`/`map[string]bool` for `maps.Equal`. Reading these vals instead of `GetString` also fixes the latent "ctrl+s reads a stale value before the focused field blurs" bug. |
| Kit editor overview | `kitFingerprint(kit)`: `yaml.Marshal` of a deep clone (made by a yaml round trip; `store.Kit` has no `Clone`, and `Normalize()` mutates `Permissions.Network` through a shared pointer) after `Normalize()`, plus the escape-hatch and services encodings. It does **not** go through `SpecYAML()`, which returns an error for an unnamed kit and would make every edit to a new, unnamed kit look clean. The baseline is taken after migration, at open (FR-011). |
| Launch wizard / reasoning setup | `launchSnapshot{sources []string (in selection order), cloneMode bool, name string (trimmed), kits []string (in order), host string, orch string}`, compared field by field with `slices.Equal` for the slices. Sources are **not** sorted: `selectedSources()` sends them in `l.order` (launch.go:701–710) and `toggle()` re-appends, so a different order is a different launch or rebuild. The reasoning baseline is the pre-filled saved setup. |
| Sources overlay | `len(marked) > 0` (it opens empty) |
| Add browser | `len(selected) > 0` |
| Inline inputs | `strings.TrimSpace(input.Value()) != initial`. `initial` is the trimmed starting value (display name, tag, or `""` for add host/group) (FR-011a). |

**Rationale**:
- huh writes bound values live: `Input.Update` (field_input.go:362), `Text.Update` (field_text.go:350), Select cursor
  moves (field_select.go:520) and Confirm toggles (field_confirm.go:207). So the snapshot is current on every key with
  no blur needed.
- Comparing values instead of tracking "touched" satisfies "changed back to the original is not a change" (US1 #7)
  and "whitespace is content in multi-line fields" (the text is compared exactly).
- Fingerprinting the kit through its save encoding makes an applied-but-identical item form (for example a nil slice
  becoming an empty one) count as unchanged whenever it would write the same file. Tasks include a test for exactly
  that case.

## R4 — Exit interception and the guard dispatch

**Decision**:
- **Identity**: `guardSurface` is an enum naming every guarded surface: `gConfig`, `gMcp`, `gSecrets`, `gOrchAdmin`,
  `gOpForm` (with its `orchSurface`), `gKitItem`, `gKitOverview`, `gLaunch`, `gReasoning`, `gSources`,
  `gSourcesBrowse`, `gRename`, `gTag`, `gHostAdd`, `gGroupAdd`.
- **Model methods**, one per need, each a switch over the enum:
  - `m.activeGuard() (guardSurface, bool)`: which surface is open now;
  - `m.guardDirty(g) bool`;
  - `m.guardSave(g) (Model, tea.Cmd)`: first re-checks `guardDirty(g)`. A background update can make the surface
    clean while the dialog is open: for example, `syncSourcesOverlay` drops marks on folders that vanished
    (sources.go:113–130), and `confirmRemoveSources` would then fall back to the *highlighted* folder (:404–408). When
    the surface is clean, Save just leaves. Otherwise it calls the surface's existing save/confirm function unchanged.
    The outcome (closed, still open with an error, or waiting on a follow-up confirm) is read from the resulting
    state, not returned;
  - `m.guardLeave(g) (Model, tea.Cmd)`: today's esc path.
  These are switches, not stored closures, because Bubble Tea models are values and a captured `Model` would be
  stale.
- **Routing**:
  - Each surface's existing exit branches call `m.requestExit(g)`. These are esc, ctrl+c in forms, `q` on the kit
    overview, `q` on the sources overlay, and esc in the add browser.
  - `requestExit` either leaves immediately (not dirty, FR-014) or opens the unsaved-changes dialog (R5) for `g`.
  - In forms, the guard's key pre-handler (R2) owns esc/ctrl+c/ctrl+e/ctrl+s before the form. The surfaces' own
    esc/ctrl+s branches move into it.
- **One exit per surface**: in the current code every guarded exit of a surface leads to the same place as its esc.
  - Kit overview q = esc → kit picker.
  - Form ctrl+c = esc.
  - Mark-free ctrl+c is aligned with esc, which fixes its don't-return-to-list inconsistency.

  So the "requested exit" (spec Key Entities) is fully described by the surface, and Discard runs `guardLeave(g)`.
- **A form completed by enter**: enter on the last field sets huh's `StateCompleted` and its private `quitting`
  flag. From then on `Update` ignores input (form.go:525) and `View()` returns `""` (form.go:649). If the save that
  follows fails validation (or, for the op forms, the RPC fails) the form must not stay as a blank, dead form. The
  surface rebuilds it from the same vals with `reopenForm(g)`, keeping the baseline, and the error stays visible.
  Focus restarts at the first field. The MCP back-out in R6 uses the same helper.
  - **Builders must be split for this to work.** Today most builders *fill* `vals` from the source object as they
    build: `identityForm`/`networkForm` from `m.kitEditor.kit` (kit_editor.go:362–391), `openKitItemForm` allocating
    new vals (:421), `openActionForm`/`openEventForm` from the action or event (orchestrator_forms.go:68–80), and
    `openOrchRenameForm` from the row (orchestrators.go:342). Reopening through them would silently replace the
    developer's edits with the original values.
  - Each surface is therefore split into `fill…Vals(source) *…Vals` (called on open only) and
    `build…Form(spec, vals) *huh.Form` (called on open and on reopen, never writing `vals`).
  - `reopenForm` builds a **fresh `formSpec`**, because the rebuilt form has new `*huh.Text` pointers and the old
    registry would make ctrl+e silently stop enlarging. It skips `normalize()` (vals are already normalised) and
    keeps the surface's baseline.
- **Lifetime invariant**: the dialog and the enlarged view are layers on `Model` bound to a `guardSurface`. After
  every message, `Update` runs `m.reconcileGuardLayers()`, which closes both layers when `activeGuard()` no longer
  matches their surface. This covers a background update while the dialog is open (spec Edge Cases). For example:
  ctrl+s on an op form, then esc while the RPC is in flight, then the success `orchDoneMsg{closeForm}` closes the form
  under the dialog. The same pass opens a parked add-override (R7).
  - **Cost**: the pass runs on every message, including terminal output, spinner and blink ticks, so it must be O(1).
    It does three things: an `activeGuard()` evaluation (a few field checks), two surface comparisons, and a
    queue-length check. It never walks the sandbox list. Removal detection lives in the sandbox `Removed` event and
    list-reload handlers instead (R7).
  - **`activeGuard()` always checks `m.screen`** as well as the sub-state (data-model.md). A form left behind on a
    screen the user is no longer on is not active, so it cannot hold layers open or block a parked override forever.
- **Not exits** (US3 #6): the launch name field's esc, the host/orchestrator/kit pickers' esc, and the kit item list's
  esc/q keep their current branches and never call `requestExit`.
- **ctrl+c on non-form surfaces** keeps its current behaviour (FR-012). It is a no-op on the launch wizard, sources and
  inline inputs, so nothing changes.

## R5 — The unsaved-changes dialog

**Decision**:
- **State**: one `unsavedState` on `Model`: `open`, `surface guardSurface`, `fromEnlarge bool`, `highlight`
  (0 Save, 1 Cancel, 2 Discard), `saveLabel`.
- **Routing**: `handleKey` routes to it **first**, after the host-password modal and sign-in card. It is a layer, not
  a screen, so the surface underneath keeps `m.screen` and all of its state; that is what makes Cancel exact (FR-015).
- **Rendering**: `overlayCenter` over whatever the screen (or the enlarged view) renders.
- **Overlay order (security)**: `View` is restructured so that a final pass always applies the host-password prompt
  and the sign-in failure card **last**, over every return path: chrome, the screens that return early today (launch,
  confirm, sources overlay, …), the enlarged view and the dialog.
  - Today `handleKey` routes keys to the password modal first (app.go:941), but `View` overlays it only after
    `chrome(...)` (app.go:1135–1140), and the async startup sign-in can raise it at any time (signin.go:115). On a
    screen that returns early, typing therefore goes into an **invisible** password field, and the next enter sends it
    as an SSH password.
  - Without this change, replacing the chrome with the enlarged view would extend that hazard to every form. The
    final overlay pass also fixes the existing early-return screens.
- **Keys** (FR-019; contracts/forms-ui.md):

| Key | Action |
|---|---|
| `s` | **Save**. On the reasoning setup it is `r` **Rebuild**, which opens a destructive rebuild confirm built from the **edited** setup (see "Rebuild confirm" below) instead of rebuilding directly. |
| `c`, `esc`, `ctrl+c` | **Cancel** |
| `d` | **Discard** |
| `←`/`→`, `h`/`l`, `tab`/`shift+tab` | Move the highlight |
| `enter` | Activate the highlighted choice |

  Cancel is highlighted when the dialog opens. Every other key is ignored.

  **Typing grace**: the dialog exists to catch a habitual esc pressed mid-typing, so the developer's next keystrokes
  land on it. For `unsavedKeyGrace` (a package `var`, 150 ms) after it opens, **only the Cancel keys** (`esc`,
  `ctrl+c`) act; every other key is ignored. That includes the letters, enter, ←/→/h/l and tab/shift+tab. Letting
  the arrows and enter through would allow the common in-text sequence "→, enter" to move the highlight from Cancel
  to Discard and discard everything.
  - A timed window is enough here, unlike for confirms (R7 C3), because it targets a typed *sequence*, not a held
    key. A held enter after the window activates the default highlight, Cancel, which is harmless. On the two
    surfaces whose Save opens a destructive confirm (sources, reasoning setup), that confirm is `yOnly` (R7 C3), so
    an enter that reaches Save still cannot carry through to the destructive action.
  - The time comes from a package `nowFunc = time.Now` seam. `openedAt` is also read from `nowFunc`, so shifting
    the clock by a fixed amount before opening would not leave the window. Tests therefore use two mechanisms:
    - Story tests call `noKeyGrace(t)`, which sets `unsavedKeyGrace` to 0 with
      `t.Cleanup` restoring it. With a zero grace, `elapsed < grace` is never true, so every key acts.
    - The grace tests (T009) install a fake clock (`nowFunc` returns a variable) and advance it **after** the dialog
      opens.

  **Rebuild confirm** (reasoning setup only): the `b` confirm cannot be reused. It sends `reasoningRebuildMsg`
  without a setup, and a nil setup means the *saved* one (orchestrator_reasoning.go:27–28, :97–108), so it would
  drop the edits. Instead, `guardSave(gReasoning)` **is** `confirmRebuildFromSetup()`. Every dialog path to Rebuild
  goes through `guardSave`: `r`, and enter while Rebuild is highlighted. Neither path can rebuild directly. The
  setup's own enter/ctrl+s keep calling `startReasoningRebuildFromSetup`, unchanged. Like every `guardSave`, it first
  re-checks `guardDirty` and just leaves if the setup is clean. Then:
  1. It builds the `ReasoningSetup` exactly as `startReasoningRebuildFromSetup` does (orchestrator_reasoning.go:152–167),
     running `selectedKitRefs` first. A kit error closes the dialog and shows on the setup, as enter would.
  2. It opens a confirm whose body lists the **new** selection (sources, seeding mode, kits) and says the
     conversation and workspace are lost. It keeps the `b` confirm's `warning: "Refused while a run is on it."`
     (orchestrator_reasoning.go:106), so the user knows the daemon can still refuse after y. The `onConfirm` is
     `reasoningRebuildMsg{…, setup: <edited setup>}`.
  3. It sets `returnTo = screenLaunch`, so n returns to the setup with its state intact (`m.launch` is untouched by
     the confirm). It sets `onAccept` (R6) to `func(m *Model) { m.screen = screenOrchDetail }`, so y lands where the
     setup's own rebuild lands today. `m.orchDetail` was untouched while on the setup, so it is the same
     orchestrator. It also sets `yOnly` (R7 C3), so only `y` accepts. The enter that chose Rebuild, repeated or
     held for any length of time, cannot also accept the confirm.
  4. `View`'s confirm-background switch gains a `screenLaunch` case that draws the launch modal, so the setup stays
     visible behind the confirm.

  The setup's own enter/ctrl+s rebuilds without a confirm, so this extra confirm departs from FR-017's "exactly what
  the surface's confirm action does". It exists only on the dialog path, because a dialog that interrupts typing
  should never destroy a sandbox on one key. **FR-017 is amended in this PR** (spec.md) with this exception and a
  clarification entry, so the spec and the plan agree.
- **Rationale for the keys**:
  - `y`/`n` are deliberately **not** used. The destructive confirm uses y = proceed, and muscle memory must not turn
    into Discard.
  - The dialog is modal, so its letters cannot clash with the surface underneath. Even so, `s`, `c` and `d` read as
    the action names, and enter only discards when Discard is highlighted.
  - The reasoning setup gets `r` because its choice is labelled Rebuild (FR-017). There, `s` is ignored, so the
    destructive rebuild is never under a key labelled "save". The dialog is otherwise identical (FR-020).
- **Save outcome**:
  - On **validation failure or a synchronous save error**, the surface's own save function leaves it open with its
    status/error. The dialog closes and the exit is dropped (US1 #6).
  - On **success**, the surface closes exactly as after a normal save (FR-017).
  - Where a save is async and the form **stays open** while it runs (op forms, mark-free), success closes it via the
    existing `orchDoneMsg{closeForm:true}`. Failure keeps it open via `applyOrchFormErr`.
  - Where a save is async and closes first (secrets, orchestrator create/rename, MCP remote, the inline inputs),
    Save behaves exactly like the surface's own save. A failure lands in the status line as today (spec Edge Cases).
- **80×24 (FR-021)**: the dialog uses `modalInnerWidth()` (53 at 80 columns; launch.go:1074) inside `modalStyle`'s
  padding, which leaves 49 cells of content. The canonical hints are `s save · d discard · esc cancel · ←/→ · enter`
  (45 cells) and, on the reasoning setup, `r rebuild · d discard · esc cancel · ←/→ · enter` (48 cells). The body is
  ≤ 2 lines, so the box is ≤ 11 rows. A test asserts no line wraps at 80×24.

## R6 — Nested editors (kit editor) and follow-up confirmations

**Decision**:
- **Kit item and section forms** (`gKitItem`): Save = `applyKitForm`, which validates and writes into
  `kitEditor.kit`. Discard closes the form with the kit untouched. Both return to the item list or the overview, as
  today.
- **Kit overview** (`gKitOverview`): dirty = `kitFingerprint(kit) != baseline`. Save = `saveKit`, which validates and
  writes, then returns to the kit picker. Discard = `enterKitPicker`. A new, unnamed kit with an added item is dirty,
  and Save then fails "name required (Identity)" and stays open.
- **The `d` delete on an item list** stays immediate (in memory) and simply makes the overview dirty.
- **FR-012a, MCP "register anyway?"**:
  - `applyMcpForm` keeps `vals` (and the baseline) while `pending` is set, instead of discarding them.
  - Backing out with n/esc/q calls `reopenForm(gMcp)` (R4), which rebuilds `mcpForm(vals)` with every value intact.
    This raises no dialog. huh cannot reopen a completed form, so focus starts at the first field.
  - The "esc cancel" wording in both `mcpModal` and `mcpHelp` (mcp.go:197) becomes "esc back to form".
  - `y` registers and closes as today.
- **Sources removal confirm** (US3 #3):
  - A dialog Save on `gSources` calls `confirmRemoveSources` with `exitAfter = true`.
  - `updateConfirmKey` (confirm.go:54) today sets `screen = returnTo` on both y and n, so the confirm needs a
    per-answer effect. A destination field (`acceptTo screen`) cannot work: `screenList` is `iota` 0 (app.go:141),
    so "go to the list" would read as "not set". `confirmState` gains an **`onAccept func(m *Model)`** hook instead.
    - On y/enter, `updateConfirmKey` sets `screen = returnTo` as today, then calls `onAccept(&m)` when it is non-nil,
      then returns `startBusy` + `onConfirm` as today. n/esc never call it.
    - The hook is called with the **live** model pointer, and it must capture only plain values (ids), never a
      `Model`, so it cannot act on a stale copy.
    - A forced close drops the confirm (R7), so its hook never runs.
    - Every existing confirm leaves `onAccept` nil and behaves exactly as today.
  - `confirmRemoveSources(exitAfter=true)` sets
    `onAccept = func(m *Model) { m.screen = screenList; m.sourcesView = sourcesState{} }` and `yOnly` (R7 C3).
    The overlay's own removal key keeps `exitAfter=false`, so it has no hook and enter still accepts. The reset belongs to this
    one confirm, and declining keeps the marks. A later `handleSourcesRemoved` for that sandbox then finds no overlay
    and only sets its status, as it does today when the overlay is closed.
  - Declining (n/esc) returns to `returnTo = screenSources` with the marks kept, and the exit is dropped.
  - Confirming sends the removal and closes the overlay.
- **Add browser** (`gSourcesBrowse`): Save = `dispatchAddSources`. Its refusals (an empty selection cannot be dirty;
  an add in flight) keep the browser open.

## R7 — Forced close (spec Edge Cases)

**Decision**:
- **One helper**: `m.forceClose(g, reason)` closes the surface and drops any open unsaved dialog and enlarged view
  bound to it.
  - When the surface was clean it sets no status of its own.
  - When it was dirty, it sets `unsaved edits lost: <reason>`. If the caller already set a status (for example
    "<host> disconnected — orchestrators closed"), it appends `; unsaved edits lost` instead, so neither message is
    lost.
- **Existing closes** that gain the teardown and status:
  - the MCP view on a host drop (hosts.go:127–135). The Daemons-screen disconnect path at daemons.go:195–197 cannot
    have an MCP form open, because the developer is on the Daemons screen there, so it needs no change;
  - `closeOrchAdminOnDrop` (hosts.go:408–452).
- **New closes**, which today leave the editor open on a dead object:
  - Rename, tag and the sources overlay (and add browser) when their sandbox goes away. Absence from the aggregated
    list alone is **not** evidence of removal: `AggregateSandboxes` returns an empty list for a disconnected host and
    on a `List` error (internal/client/manager.go:232–241). So:
    - an `Event_Removed` for the id (`removeSandbox`) closes with "sandbox removed";
    - a list reload closes with "sandbox removed" only when that host's entry is `HostConnected` with `Err == nil`
      and lacks the id;
    - a host that dropped closes with "daemon disconnected";
    - a host with a `List` error is left alone.

    These checks run in those message handlers, not in `reconcileGuardLayers`.
  - The reasoning setup when its host drops, with reason "daemon disconnected" (`closeOrchAdminOnDrop` gains a
    `screenLaunch` case for `launch.reasoning.host`).
  - **Suspended surfaces.** While one of a guarded surface's own follow-up confirms is showing (the sources removal
    confirm, the Rebuild confirm, or any confirm whose `returnTo` is that surface's screen), `m.screen ==
    screenConfirm`, so `activeGuard()` reports nothing. The surface still exists underneath, and n returns to it.
    - `m.suspendedGuard() (guardSurface, bool)` reports it when `m.screen == screenConfirm`, `confirm.returnTo` is a
      guarded surface's screen, and that surface's state is still set: `launch.reasoning != nil` for `screenLaunch`,
      `sourcesView.sandboxID != ""` for `screenSources`.
    - Every forced-close trigger (host drop, sandbox removal, orchestrator removal, including the
      `syncOrchestratorChanged` target check) matches on `activeOrSuspendedGuard()`. `forceClose` on a suspended
      surface also clears `m.confirm` (so neither its `onConfirm` nor its `onAccept` runs) and moves to the surface's
      leave destination with the usual status. Without this, a host drop during the Rebuild confirm would leave n
      returning to a setup on a dead host, and y failing in `connectedDaemon` (orchestrator_reasoning.go:180).
    - `activeGuard()` itself is unchanged. The layer and terminal rules keep treating a confirm screen as "not on the
      surface". The override rule (C2 below) does not need `suspendedGuard`, because it refuses to open while
      **any** confirm is showing.
  - The reasoning setup, the orchestrator rename form, and op forms targeting the orchestrator when it is removed,
    with reason "orchestrator removed". Today nothing notices this:
    - `syncOrchestratorChanged` issues no reload while the screen is `screenLaunch`;
    - `applyOrchDetail` has no NotFound branch;
    - Jobs drops a NotFound orchestrator from its rows but leaves an open form.

    So `syncOrchestratorChanged` issues a `GetOrchestrator` check whenever an active guarded surface targets that
    orchestrator (`reasoning.id`, `orch.formKind == rename` id, `opState(s).formTarget.id`). A NotFound reply, or a
    detail/Jobs reload reporting NotFound for it, calls `forceClose`.
- **Confirm lifecycle (one design).** Four review rounds found hijacks in the confirm and override flows, each
  patched in a different place. They are replaced by a single set of rules covering every path that can open,
  replace or close a confirm. The paths in today's code:

  | Path | Opens from | Rule below |
  |---|---|---|
  | 16 `enterConfirm` callers (`confirmOrchRemove`, `confirmCancelRun`, `confirmDelete*`, `attachTo`, `confirmDetach`, `confirmRefresh`, `confirmAttachKit`, MCP / sandbox-MCP / secrets removal, `confirmRemoveSources`, the `b` rebuild, …) | the user's own key on that screen | C1 |
  | The guard's follow-up confirms: sources removal Save, the Rebuild confirm | the unsaved dialog (`s`/`r`/enter) | C1, C3 |
  | `confirmAddOverride` (sources.go:367) | an async `sourcesAddResultMsg` | C2, C3 |
  | `enterApproval` (approval.go:21) | an async escape-hatch event, only when `m.screen == screenList` (notifications.go:195) | unchanged by this feature: it opens only on the list, so never over a confirm or a guarded surface. Known, pre-existing risk, out of scope: it can open while the list filter is being typed into, where a typed `y` answers it |
  | y/enter, n/esc (`updateConfirmKey`) | the user | C3, C4 |
  | Forced close (host drop, sandbox or orchestrator removed) | async | C4 |

  (`termOpenedMsg` is covered separately below. The MCP "register anyway?" step is an in-view state answered with
  `y` only, not a `confirmState`.)
  - **C1, the one-owner rule.** `m.confirm` is only ever written by `enterConfirm`, and `enterConfirm` is only called
    either (a) synchronously from a key handler, as the direct answer to the user's key on the current screen, or
    (b) from the C2 dequeue. No message handler other than C2 opens a confirm. Under (a) a confirm cannot be
    replaced: while one is showing, keys go to `updateConfirmKey`, or before it to a credential prompt (the
    host-password prompt or the sign-in failure card, app.go:941–946), and neither prompt writes `m.confirm`.
    Under (b) the dequeue requires that no confirm is showing. **So a showing confirm is never replaced**, whichever
    surface owns it.
  - **C2, async overrides always go through the queue.** The `sourcesAddResultMsg` handler **always** appends
    `parkedOverride{msg, name}` to `m.pendingOverrides` and never calls `confirmAddOverride` itself. There is no
    separate arrival test that could disagree with the dequeue test, and FIFO order holds. `reconcileGuardLayers`
    opens the head (building the confirm then, so `returnTo` is current) only when `overrideMayOpen(head)` holds:
    - the message is **not** a key message;
    - the user is on one of the two screens the add belongs to, and is not typing there. Either:
      - `m.screen == screenList` and the list filter is not being typed into (`m.list.FilterState() !=
        list.Filtering`, as sandbox_list.go:618 checks), so the confirm's `returnTo = screenList` is where the user
        already is; or
      - `m.screen == screenSources` and `activeGuard()` is `gSources` for **that** sandbox (with the browser open it
        would be `gSourcesBrowse`), with no unsaved dialog open. The confirm gets `returnTo = screenSources`
        (`overlayFor`), and the marks survive both answers.

      Every other screen keeps it queued. That covers confirms (including the guard's removal and Rebuild
      confirms), approval, the terminal, every guarded surface, and the unguarded screens that hold state a return
      to the list would drop: the MCP "register anyway?" step and auth wait (`enterMcp` resets `mcpView`,
      mcp.go:132), the pickers, orchestrator detail, Jobs and settings. When an arriving override cannot open in
      the same `Update`, the status is set **once** to `add to <name> needs confirmation — back on the list`. It is
      not re-set on every pass, so it never overwrites later statuses;
    - no credential prompt is capturing keys: not `hosts.connecting && hosts.pwMode != pwHostsScreen` (app.go:921),
      and not `signIn.active && signIn.phase == signInFailed` (app.go:944). Either one would take the keys while
      the confirm sat hidden underneath (app.go:1137–1140).

    When an override arrives while nothing blocks it, the arrival message is itself non-key, so the same `Update`
    opens it. There is no added delay. The **queue purge is unconditional**, whether or not any surface is open:
    - the `Removed` event handler and the connected, error-free list reload drop queued entries for a missing
      sandbox id;
    - `applyHosts` drops entries for a dropped host;
    - each purge sets the status `add of <folders> to <name> cancelled: sandbox removed` (or `: daemon
      disconnected`), so the add doesn't vanish silently.

    An override already **showing** on the list for a sandbox that is then removed stays up. y then fails with the
    daemon's error in the status, as any action on a removed sandbox does today. This is accepted.
  - **C3, explicit accept on confirms the user didn't open with that key.** `confirmState.yOnly bool` is set on
    exactly three kinds of confirm: a dequeued override, the removal confirm opened by the guard's Save, and the
    Rebuild confirm. Each opens on a key or message the user meant for something else, usually enter (enter@Save,
    enter@Rebuild, or the enter that closed a rename before a queued override opened).
    - On a `yOnly` confirm, `updateConfirmKey` accepts **only `y`/`Y`**. enter is ignored, with no other effect.
      n/N/esc/q/ctrl+c decline at once, because declining is safe.
    - The footer (`confirmHelp`) and the modal's hint line (`confirmModal`, which hardcodes
      `y/enter confirm · n/esc cancel` at confirm.go:93) read `y confirm · n/esc cancel` on a `yOnly` confirm.
    - Why not a time window: an earlier draft ignored y/enter for 150 ms. OS key auto-repeat starts 225–1000 ms
      after the first press, so a held enter still accepted after the window. The enter that submitted a password
      over the confirm also closed the prompt before any re-arm could run. A key rule has neither problem. Every
      enter, however late and from whichever prompt, is ignored. There is no clock, timer or re-arm, and no
      per-message work.
    - `y` is not bound on the two screens where a queued override can open (the sandbox list and the sources
      overlay), nor on the reasoning setup or the unsaved dialog that lead to the other two. Keys typed into a
      credential prompt over a confirm go to the prompt (app.go:941–946), never to the confirm.
    - **Known residual risk (accepted):** a queued override can open on the list just before the developer starts a
      filter search without noticing it. The confirm ignores `/` (confirm.go:56–74), so the first `y` in the query
      would accept the add. The risk is low: the override only opens while the filter is *not* being typed into
      (C2), the modal is drawn over the list, and the action is a low-disk add, not a removal. Closing this fully
      would need a confirm that, once it has ignored a printable key, ignores `y` too, and then the developer could
      never accept it. That is not worth the cost. The same filter caveat already applies to `enterApproval` (table
      above).
    - Confirms opened by their own dedicated key (C1a, all 16 existing callers) keep `yOnly` false and behave
      exactly as today. `confirmAddOverride` is the one existing confirm whose keys change: it now always has
      `yOnly`, so enter no longer accepts it.
  - **C4, the close rule.** A confirm closes only (a) through `updateConfirmKey`, or (b) with its owning surface,
    guarded or not, in a forced close. On y/enter the `onAccept` hook (R6) and `onConfirm` run. On n/esc neither
    runs. For (b), `forceClose` uses `suspendedGuard()` (above) to find the guarded surface under the confirm. The
    existing `closeOrchAdminOnDrop` path that moves off a confirm over the daemon orchestrators or orchestrator
    detail (hosts.go:410–415, :444–447) does the same. Every path clears `m.confirm` without running either
    callback, so no stale confirm state is left behind. A dequeued override over its own sandbox's overlay counts as suspended `gSources`, so it closes
    with the overlay when that sandbox is removed.
- **Terminal open/close** also switches screens asynchronously:
  - `termOpenedMsg` sets `screen = screenTerminal` unconditionally (terminal.go:96–104), and `termClosedMsg` returns
    to the list.
  - `termOpenedMsg` now switches screens **only when `m.screen == screenList`**, where `t` was pressed; the approval
    auto-open uses the same rule (notifications.go:195). "No guarded surface is active" is not enough: during the
    guard's own follow-up confirms (sources removal, Rebuild) `m.screen == screenConfirm` and no surface counts as
    active.
  - On any other screen it **detaches** the new session itself: `msg.session.Close()`, `msg.cancel()` and
    `close(msg.done)`. `closeTerm()` only acts on `m.term`, which this session never reached. The status reads
    `terminal for <name> ready — closed because you left the list; press t again`.
  - `termClosedMsg` changes the screen only when `m.screen == screenTerminal`.

## R8 — Help and key hints

**Decision**:
- **Footer**: the app adds `ctrl+e enlarge` when, and only when, the focused field of the open form is a registered
  Text (FR-002). huh's own help line no longer shows "open editor" (R1).
- **Enlarged view hint line**:
  `esc close · ctrl+s save · ctrl+r revert · pgup/pgdn page · ctrl+c leave form`.
- **Dialog hint line**: `s save · d discard · esc cancel · ←/→ · enter`, or on the reasoning setup
  `r rebuild · d discard · esc cancel · ←/→ · enter` (R5). The hint lists `esc` for Cancel; `c` and `ctrl+c` also
  cancel but are not listed, to keep the hint inside 49 cells.
- **Enlarged view hint width**: the line above is 76 cells, inside the 78 available at 80 columns. A test asserts
  that every line of the enlarged view and the dialog is ≤ 80 cells at 80×24 (FR-021).
- **Launch footer drift**: `launchHelp` still shows N/H/O and "enter launch" in reasoning mode. It is aligned with the
  modal help while this footer is touched (FR-017's Rebuild wording).

## R9 — Responsiveness (SC-006)

**Measured** on 2026-10-27 with a throwaway `go test` timing harness (200×50 form, 5,000 lines of ~85 chars,
`Update` + `View` per keystroke, mean of 20–50 ops, 24-core dev container):

| Case | ms / keystroke |
|---|---|
| Enlarged view (MaxHeight 0): type | 34.8 |
| Enlarged view: cursor up/down | 34.7 |
| Enlarged view: page (40× `CursorUp` + one `Update` + `View`) | 36.7 |
| Inline focused `huh.Text` holding 5,000 lines: type | 54.6 |
| Inline focused field: cursor down | 53.3 |
| *Another* field of that form: type | 52.9 |

- All cases are within the 100 ms budget, with ~45% headroom on the worst one.
- Paging by sending 40 `KeyUp` messages through `Update` costs ~377 ms (each `Update` repositions at ~8 ms), so
  pgup/pgdown use the batched cursor moves above.
- `View` dominates at ~25 ms, because the textarea wraps every line on each render (textarea.go:1108).

**Decision**:
- Keep `huh.Text` inline (R2) and a plain textarea in the enlarged view.
- Ship the measurement as Go benchmarks, `BenchmarkSC006*` in `enlarge_perf_test.go`, run by quickstart Scenario 7.
  They are not a CI timing gate, because shared CI runners make wall-clock assertions flaky.
- If a slower machine exceeds 100 ms, the fallback the spec names (a custom field owning its wrap cache) is the
  remedy, recorded as a follow-up.

## R10 — Testing approach

**Decision**: use the package's existing patterns: `update`/`press`/`runCmd`/`sized` (model_test.go) and the surface
helpers (`kitModel`, `mcpModel`, `secretsModel`, `openDaemonOrchs`, `jobsWith`, `sourcesModel`, `fakeDaemon`).

- One table-driven test walks every guarded surface. For each, it covers: open → esc with no change (closes, no
  dialog); change → esc (dialog); Cancel (state identical); Discard (closed, nothing applied); Save (the surface's
  save effect). Forms also cover ctrl+c, and the kit overview also covers `q`.
- Focused tests cover the enlarged view: open, edit, close, write-back; ctrl+s; ctrl+c → Cancel → revert; resize; 151
  lines; tab is a no-op; ctrl+e on Input/Select.
- Further focused tests cover FR-012a, the sources confirm decline/confirm, the forced closes, and the parked
  override.
- Coverage stays ≥ 90% for `switchboard-tui` (`make cover`).
