# Data Model: App Forms — Enlargeable Text Fields & Unsaved-Changes Guard

Client-side, in-memory view model only. There are no persisted, proto or daemon entities. All types live in
`src/apps/switchboard-tui/internal/ui`.

## guardSurface (enum)

This enum identifies one guarded surface (spec: **Guarded surface**). At most one is active at a time, and
`m.activeGuard()` derives it from `m.screen` plus that screen's sub-state.

| Value | Surface | Active when |
|---|---|---|
| `gConfig` | Config editor form | `screenConfigEditor` with a form |
| `gMcp` | MCP registration form | `mcpView.form != nil` |
| `gSecrets` | Secrets form | `secretsView.form != nil` |
| `gOrchAdmin` | Orchestrator create/rename | `m.orch.form != nil` |
| `gOpForm` (+ `orchSurface`) | Action / timed-event / call / mark-free form | `opState(s).form != nil` |
| `gKitItem` | Kit section or item form | `kitEditor.form != nil` |
| `gKitOverview` | Kit editor overview and section item lists | `screenKitEditor`, no form (dirty is the kit fingerprint, so applied-but-unsaved edits are guarded for parking while on an item list too; esc/q on the item list remain non-exits) |
| `gLaunch` | Launch wizard | `screenLaunch`, `launch.reasoning == nil` |
| `gReasoning` | Reasoning-sandbox setup | `screenLaunch`, `launch.reasoning != nil` |
| `gSources` | Sources overlay | `screenSources`, `!browsing` |
| `gSourcesBrowse` | Sources add browser | `screenSources`, `browsing` |
| `gRename` / `gTag` | Inline rename / tag | `screenRename` / `screenTag` |
| `gHostAdd` / `gGroupAdd` | Inline add host / group | `hosts.adding` / `groups.adding` |

Precedence inside a surface: a nested form (`gKitItem`) wins over its parent (`gKitOverview`). Each pair of
sub-states is mutually exclusive by construction (`gSources` vs `gSourcesBrowse`, `gLaunch` vs `gReasoning`), so
neither needs a precedence rule.

**Not guarded** (spec Edge Cases): credential prompts (host password, sign-in, the MCP auth paste box), settings
toggles, the destructive confirm and approval modals, and the launch wizard's own pickers and name field (sub-steps
of `gLaunch`).

## Baseline and snapshot (per surface)

- `baseline`: an immutable snapshot taken when the surface opens.
  - For forms it is taken *after* the `huh.Form` is built (Select writes its option into the pointer during
    construction) and after `spec.normalize()` has written each Text's cleaned textarea value back to its pointer
    (research R3).
  - For create forms that is after defaults are filled; for the kit editor, after migration.
  - A form rebuilt by `reopenForm` keeps its original baseline. It is built by the surface's
    `build…Form(spec, vals)` from the **same** vals, never by a filling builder, and gets a fresh `formSpec`.
- `snapshot()`: the same shape, computed from current values.
- `dirty() = snapshot() != baseline`. This is a pure function: no "touched" flags, so an edit undone back to the
  original value is clean.

| Surface | Snapshot type | Notes |
|---|---|---|
| Vals-bound huh forms | value copy of `mcpFormVals`, `secretFormVals`, `orchFormVals`, `actionFormVals`, `eventFormVals`, `markFreeVals`, `kitFormVals` | Every field is a scalar, so `==` works. |
| Config editor | `configSnapshot{name, mode string; strs map[string]string; bools map[string]bool}` | Flattened from the new heap-allocated `*configFormVals` (manifest-driven option maps of pointers); compared with `maps.Equal`. The config editor moves from `GetString` by key to pointer binding. |
| Call form | `map[string]string` | Flattened from `callFormVals.values map[string]*string`; `maps.Equal`. |
| Kit overview | `string` = `kitFingerprint(kit)` | `yaml.Marshal` of a deep clone, after `Normalize()`, plus the escape-hatch and services encodings. There is no name check, so an unnamed new kit still fingerprints. |
| Launch / reasoning | `launchSnapshot{sources []string; cloneMode bool; name string; kits []string; host string; orch string}` | Sources and kits in selection order (order changes what is sent); name trimmed; slices compared with `slices.Equal`. |
| Sources overlay | `bool` = `len(marked) > 0` | Opens with no marks. |
| Add browser | `bool` = `len(launch.selected) > 0` | Opens with no selection. |
| Inline inputs | `string` = trimmed value | Compared against the trimmed initial value: display name, tag, or `""` (FR-011a). |

Multi-line text is compared exactly, so whitespace counts. Inline single-line inputs are compared trimmed.

## Requested exit

This is the spec's **Requested exit**. In this design, every guarded exit of a surface (esc; ctrl+c in a form; `q` on
the kit overview) leads to the same destination as that surface's esc today. The requested exit is therefore fully
identified by the `guardSurface` held in `unsavedState.surface`, and no separate exit kind is stored.

| Event | Effect on the requested exit |
|---|---|
| Discard | `guardLeave(g)`: completed |
| Save succeeds (surface closes) | Completed by the surface's own save path |
| Cancel | Dropped |
| Save fails validation or synchronously | Dropped (the surface stays open showing the error) |
| Follow-up confirm declined (sources removal) | Dropped |
| Forced close | Superseded: `forceClose` drops the dialog |

## unsavedState (dialog)

| Field | Type | Meaning |
|---|---|---|
| `open` | `bool` | The dialog layer is showing and receives every key first. |
| `surface` | `guardSurface` | The surface (and so the requested exit) it guards. |
| `fromEnlarge` | `bool` | Raised by ctrl+c in the enlarged view; Cancel returns to it (FR-015). |
| `highlight` | `int` | 0 Save, 1 Cancel, 2 Discard. Starts at 1 (Cancel). |
| `saveLabel` | `string` | "Save", or "Rebuild" for `gReasoning` (opens the destructive rebuild confirm). |
| `openedAt` | `time.Time` | From `nowFunc()`; letter keys are ignored for `unsavedKeyGrace` (150 ms) after it (research R5). |

State transitions:

```
closed --requestExit(g) && dirty(g)--> open(g, highlight=Cancel)
open --c / esc / ctrl+c / enter@Cancel--> closed                      (surface untouched; enlarged view kept)
open --d / enter@Discard--> closed + guardLeave(g)                    (enlarged view discarded first)
open --s / enter@Save--> closed + guardSave(g)                        (re-checks dirty; enlarged view already written back)
open --r / enter@Rebuild--> closed + destructive rebuild confirm      (gReasoning only)
open --letter key within 150 ms of opening--> ignored                 (typing grace)
open --forceClose(g)--> closed                                        (status "unsaved edits lost: …" if dirty)
```

## enlargeState (enlarged view)

| Field | Type | Meaning |
|---|---|---|
| `open` | `bool` | The view is drawn instead of the screen chrome and receives keys before the form. |
| `surface` | `guardSurface` | The form it belongs to (edits count toward that surface's dirty state, FR-007). |
| `field` | `*huh.Text` | The focused field at ctrl+e. |
| `ptr` | `*string` | The field's bound value, looked up in `formSpec.texts`. |
| `title` | `string` | The field's title, recorded by `spec.text(ptr, title)`; shown in the view's top border. |
| `ta` | `textarea.Model` | `MaxHeight 0`, `CharLimit 0`, sized to the terminal. |
| `baseline` | `string` | The text at ctrl+e; `ctrl+r` restores it (FR-005b); untouched by the ctrl+c write-back. |

Transitions:

```
closed --ctrl+e on registered Text--> open(baseline = *ptr)
open --esc / ctrl+e--> write back → closed (focus stays on field; no dialog)
open --ctrl+s--> write back → closed → surface save (as the form's ctrl+s)
open --ctrl+c--> write back → requestExit(surface)
                   ├─ clean → closed + guardLeave
                   └─ dirty → dialog(fromEnlarge) over the open view
open --ctrl+r--> ta.SetValue(baseline)
open --resize--> ta.SetWidth/SetHeight (text, cursor kept)
```

Write-back is `*ptr = ta.Value(); field.Value(ptr)`.

## formSpec (Text-field registry)

`formSpec{texts map[*huh.Text]textEntry{ptr *string; title string}}` is created next to each form.
- `spec.text(ptr, title)` returns `huh.NewText().ExternalEditor(false).Title(title).Value(ptr)` and records it.
- `spec.normalize()` sends each recorded field `Update(nil)`, so its pointer holds the textarea's cleaned value.

It is stored next to the form on each surface (`mcpState`, `orchOpState`, `kitEditorState`) and dropped with the
form.

## Lifetime invariant

After every message, `reconcileGuardLayers()` does O(1) work:
- it closes the dialog and the enlarged view if their `surface` is no longer `activeGuard()`;
- it opens the head of `pendingOverrides` once no guarded surface is active and no confirm or approval is showing
  (research R4, R7).

Sandbox-removal forced closes are done in the `Removed` event and list-reload handlers, not here.

`activeGuard()` checks `m.screen` for every row above. For example, `gMcp` requires `screenMcp` and
`mcpView.form != nil`, so a form stranded on a screen the user has left is never active.
