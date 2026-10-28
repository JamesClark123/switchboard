# Quickstart: validating App Forms (SWI-9)

Automated coverage comes first:

```bash
cd src/apps/switchboard-tui
go test ./internal/ui/ -run 'Guard|Enlarge|Unsaved'              # feature tests
go test ./internal/ui/ -run '^$' -bench 'SC006' -benchtime 20x    # SC-006 timings (R9)
cd ../../.. && PATH=$HOME/go/bin:$PATH make fmt-check vet lint cover && make e2e
```

The manual scenarios below run against a local daemon (`sxbd`) and the TUI (`sxb`) at 80×24, then at 200×50.

## Scenario 1: dialog basics (US1)
1. shift+→ (Daemons) → a daemon → **MCP** → `a`. Press esc at once: the form closes with no dialog.
2. `a` again and type a name. Press esc: the **Unsaved changes** dialog opens with **Cancel** highlighted.
3. Press enter: the dialog closes and the form shows the same name, focus and cursor.
4. Delete the name you typed and press esc: no dialog (the change was undone).
5. Type a name again, press esc, then `d`: the form closes and nothing is registered.
6. Type an invalid name (`a b`), press esc, then `s`: the form stays open with the validation error.

## Scenario 2: every exit (US2)
1. In an orchestrator **Actions** form, edit the body and press ctrl+c: the dialog appears and the app keeps running.
   `d` returns to the screen the form was opened from.
2. In the kit editor, open a kit, change an item and apply it (ctrl+s). This leaves you on the item list, so press
   esc to return to the overview, then press `q`: the dialog appears. `d` returns to the picker, and reopening the kit shows it unchanged. Repeat, and `s` saves.
3. Register an MCP host command (Kind: Host command). At "register anyway?" press n: the form is back with every
   value.
4. With focus in a text field, type `q` and `,`: they are typed. shift+→ does nothing. None of them is treated as an
   exit.
5. Open a kit whose file **Content** holds a tab character, tab through its fields without editing, and press esc: no
   dialog.
6. Call an action that has a required argument (Jobs › Actions › `enter` on the action), leave the argument empty, and press enter on
   the last field. The call form's fields are all single-line inputs, so enter submits. The form stays visible with
   the error and the values you typed, and is not blank.
7. Kit picker → new kit, add an install command without naming the kit (ctrl+s applies it, leaving you on the item
   list), press esc to return to the overview, then press q: the dialog appears. `s` reports "name required".

## Scenario 3: inputs, wizards, nested (US3)
1. `R` (rename) on a stopped sandbox and type a character, then esc: the dialog appears. Clear the name, esc, `s`:
   "name can't be empty" and the input stays open. Then esc → `d`: nothing is renamed.
2. Type spaces only around the original name (`  name  `) and press esc: no dialog.
3. `n` (launch), mark two folders, esc: the dialog appears; `s` launches.
4. In the launch wizard press `N`, type a name, esc: only the field blurs. Open the host picker with `H`, then esc:
   back to the wizard, no dialog.
5. Orchestrator → Reasoning → `e`, toggle a folder, esc: the dialog shows **Rebuild** (`r`). `r` (or ← to Rebuild,
   then enter) opens a destructive confirm listing the **edited** folders and warning "Refused while a run is on
   it.", with the setup still visible behind it. Nothing is rebuilt before y. n returns to the setup with the
   toggle intact. Repeat and press y: the rebuild uses the edited folders, and you land on the orchestrator
   detail. Toggling a folder off and on again (it moves to the
   end of the order) also raises the dialog.
6. `S` (sources), mark a folder, esc → `s`: the removal confirm appears. Press n: back on the overlay with the mark
   kept. Repeat and press y: the folder is removed, the overlay closes and you are on the sandbox list. Repeat
   once more with enter on Save, then press or hold enter: nothing is removed, and the confirm reads
   `y confirm · n/esc cancel`. Press y to remove.
7. `S` → `a`, select a folder, esc: the dialog appears. `d` returns to the overlay.

## Scenario 4: enlarge (US4)
1. Kit editor → **Agent instructions** with ~50 lines. The footer shows `ctrl+e enlarge`.
2. ctrl+e: the full-terminal view shows ≥ 16 lines at 80×24 (≥ 80% of the terminal at 200×50), with the title in
   the border and no line numbers, and no external editor starts.
3. Edit a line and press esc: the inline field has the edit, and no dialog appears.
4. ctrl+e, edit, ctrl+r: the text is back to how it was at ctrl+e.
5. ctrl+e, edit, ctrl+c: the dialog appears over the view. Press `c`: back in the view with the edit, cursor and
   scroll intact. Press ctrl+r: the pre-ctrl+e text is back.
6. ctrl+e, edit, ctrl+s: the view closes and the form applies.
7. Resize the terminal while enlarged: the view re-fits and keeps the text and cursor.
8. Focus an Input (e.g. a service name) and press ctrl+e: the cursor moves to end of line and no enlarge hint shows.
9. Orchestrator Action **Body** (`WithShowHelp(false)` form) → ctrl+e: the enlarged view opens. With `EDITOR=vim`
   set, nothing launches.

## Scenario 5: past 99 lines (FR-009a)
1. Paste 150 lines into Agent instructions via the enlarged view, press enter at the end (151 lines), and close.
2. The inline field holds 151 lines; inline enter at the end adds no line past 99.
3. Save the kit and reopen it: all 151 lines are present.

## Scenario 6: forced close
1. Open the MCP form with an edit on a remote host, then kill that host's connection: status reads
   `unsaved edits lost: daemon disconnected`.
2. Open rename on a sandbox and type a change; destroy that sandbox from another client: the input closes with
   `unsaved edits lost: sandbox removed`.

## Scenario 7: responsiveness (SC-006)
Run the benchmarks above. Each `BenchmarkSC006*` must report < 100 ms/op: enlarged type/cursor/page, inline-focused
type/cursor, and other-field type. Record the numbers in the PR. Then manually paste 5,000 lines into an enlarged
field at 200×50 and check typing, arrows and pgup/pgdn feel immediate inline and enlarged.
