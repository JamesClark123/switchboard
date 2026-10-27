# Data Model: App Forms — Enlargeable Text Fields & Unsaved-Changes Guard

Client-side, in-memory view model only. There are no persisted, proto or daemon entities. All types live in
`src/apps/switchboard-tui/internal/ui`.

## guardSurface (enum)

This enum identifies one guarded surface (spec: **Guarded surface**). At most one is active at a time, and
`m.activeGuard()` derives it from `m.screen` plus that screen's sub-state.

| Value | Surface | Active when |
|---|---|---|
| `gConfig` | Config editor form | `screenConfigEditor` with a form |
| `gMcp` | MCP registration form | `screenMcp` and `mcpView.form != nil` |
| `gSecrets` | Secrets form | `screenSecrets` and `secretsView.form != nil` |
| `gOrchAdmin` | Orchestrator create/rename | `screenDaemonOrchs` and `m.orch.form != nil` |
| `gOpForm` (+ `orchSurface`) | Action / timed-event / call / mark-free form | `m.screen == surfaceScreen(s)` and `opState(s).form != nil` |
| `gKitItem` | Kit section or item form | `screenKitEditor` and `kitEditor.form != nil` |
| `gKitOverview` | Kit editor overview and section item lists | `screenKitEditor`, no form (dirty is the kit fingerprint, so applied-but-unsaved edits are guarded for parking while on an item list too; esc/q on the item list remain non-exits) |
| `gLaunch` | Launch wizard | `screenLaunch`, `launch.reasoning == nil` |
| `gReasoning` | Reasoning-sandbox setup | `screenLaunch`, `launch.reasoning != nil` |
| `gSources` | Sources overlay | `screenSources`, `!browsing` |
| `gSourcesBrowse` | Sources add browser | `screenSources`, `browsing` |
| `gRename` / `gTag` | Inline rename / tag | `screenRename` / `screenTag` |
| `gHostAdd` / `gGroupAdd` | Inline add host / group | `screenHosts` and `hosts.adding` / `screenGroups` and `groups.adding` |

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
| `saveLabel` | `string` | "Save", or "Rebuild" for `gReasoning` (opens a destructive confirm built from the edited setup). |
| `openedAt` | `time.Time` | From `nowFunc()`. For `unsavedKeyGrace` (a `var`, 150 ms; 0 in story tests) after it, only esc/ctrl+c (Cancel) act (research R5). |

State transitions:

```
closed --requestExit(g) && dirty(g)--> open(g, highlight=Cancel)
open --c / esc / ctrl+c / enter@Cancel--> closed                      (surface untouched; enlarged view kept)
open --d / enter@Discard--> closed + guardLeave(g)                    (enlarged view discarded first)
open --s / enter@Save--> closed + guardSave(g)                        (re-checks dirty; enlarged view already written back)
open --r / enter@Rebuild--> closed + guardSave(gReasoning) = confirmRebuildFromSetup()
                                                                      (confirm built from the EDITED setup, keeps the
                                                                      "Refused while a run is on it." warning;
                                                                      n → setup intact, y → onAccept → orchestrator detail)
open --any key but esc/ctrl+c within 150 ms of opening--> ignored     (typing grace)
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
- it opens the head of `pendingOverrides` (every async add-override goes through the queue) when
  `overrideMayOpen` holds. That means a non-key message; either the plain sandbox list (not filtering) or that
  sandbox's own sources overlay with no dialog; and no credential prompt is up. The opened confirm is `yOnly`
  (research R7 "Confirm lifecycle", C2–C3).

**Confirm lifecycle** (research R7, C1–C4): a showing confirm is never replaced, because only key handlers and the
override dequeue open confirms, and the dequeue opens only on the list or the sandbox's own overlay. A confirm closes
only through its own keys or with its owning surface in a forced close, and every forced close clears `m.confirm`.
The queue is purged of a removed sandbox or a dropped host unconditionally, with a "cancelled" status.

`m.suspendedGuard()` reports a guarded surface that sits under one of its own follow-up confirms (`screenConfirm`
with `confirm.returnTo` = that surface's screen, its state still set). Forced-close triggers match active **or**
suspended surfaces. `activeGuard()` itself stays false under a confirm.

Sandbox-removal forced closes are done in the `Removed` event and list-reload handlers, not here.

`activeGuard()` checks `m.screen` for every row above. For example, `gMcp` requires `screenMcp` and
`mcpView.form != nil`, so a form stranded on a screen the user has left is never active.

## confirmState additions (UI/confirm.go)

| Field | Type | Meaning |
|---|---|---|
| `onAccept` | `func(m *Model)` | Optional. Called on y/enter with the live model, after `screen = returnTo` and before `onConfirm`. It captures only plain values. It is used by the sources removal Save (close and reset the overlay) and the Rebuild confirm (go to the orchestrator detail). nil means today's behaviour. n/esc and forced closes never call it. |
| `yOnly` | `bool` | C3. When set, `updateConfirmKey` accepts only `y`/`Y`; enter is ignored. n/esc/q/ctrl+c decline as usual. The modal hint and the footer read `y confirm · n/esc cancel`. It is set on exactly three kinds of confirm: dequeued add-overrides, the sources removal confirm opened by the guard's Save, and the Rebuild confirm. It is false elsewhere. There is no timer. |

`acceptTo screen` is deliberately not used: `screenList` is the zero value of `screen`, so "go to the list" would be
indistinguishable from "not set".
