package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// openCustom opens the settings screen and the Custom command input.
func openCustom(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	if m.screen != screenSettings {
		m, _ = update(m, press(","))
	}
	m.settingsView.cursor = 1
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m.settingsView.listCursor = 10 // Custom
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.mode != settingsInput || !m.settingsView.input.Focused() {
		t.Fatalf("the input did not open focused: mode %v", m.settingsView.mode)
	}
	return m, cmd
}

// TestSettingsCommandInput (T036, US3, FR-506, FR-528).
func TestSettingsCommandInput(t *testing.T) {
	noKeyGrace(t)
	m, st := settingsModel(t)
	m.prefs.EditorCustomCommand = "old {path}"
	m, cmd := openCustom(t, m)
	if m.settingsView.input.Value() != "old {path}" || !strings.Contains(ansi.Strip(m.View()), "{path} is the workspace folder") {
		t.Fatalf("pre-filled with the session command: %q", m.settingsView.input.Value())
	}
	// The blink message from Focus's command reaches the input (the non-key
	// route a paste takes).
	if cmd == nil {
		t.Fatal("opening returns the focus command")
	}
	blinkBefore := m.settingsView.input.Cursor.Blink
	m, _ = update(m, runCmd(cmd))
	if m.settingsView.input.Cursor.Blink == blinkBefore {
		t.Error("the blink message should toggle the cursor")
	}
	// An empty submit: rejected, open, the previous editor still in force.
	m.settingsView.input.SetValue("   ")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.status != "a command is required" || !m.statusIsErr() || m.settingsView.mode != settingsInput || m.prefs.Editor != "" || m.prefs.EditorCustomCommand != "old {path}" {
		t.Errorf("empty: status %q mode %v prefs %+v", m.status, m.settingsView.mode, m.prefs)
	}
	// Shell syntax: the reason names sh -c and a wrapper script, never the text.
	m.settingsView.input.SetValue("cd {path} && nvim .")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.status, "sh -c") || !strings.Contains(m.status, "wrapper script") || strings.Contains(m.status, "nvim") || m.settingsView.mode != settingsInput {
		t.Errorf("shell-syntax: %q", m.status)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "doesn't run in a shell") {
		t.Errorf("the rejection is in the status line:\n%s", v)
	}
	// esc with a change opens the dialog: s acts like enter (a rejection
	// keeps it open), c returns to the input, d returns to the list.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !m.unsaved.open || m.unsaved.surface != gEditorCommand {
		t.Fatal("esc on a changed input opens the dialog")
	}
	m, _ = update(m, press("s"))
	if m.unsaved.open || m.settingsView.mode != settingsInput || !strings.Contains(m.status, "doesn't run in a shell") {
		t.Errorf("Save acts like enter: open %v mode %v status %q", m.unsaved.open, m.settingsView.mode, m.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("c"))
	if m.unsaved.open || m.settingsView.mode != settingsInput || m.settingsView.input.Value() != "cd {path} && nvim ." {
		t.Error("Cancel returns to the input unchanged")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("d"))
	if m.unsaved.open || m.settingsView.mode != settingsList || m.prefs.EditorCustomCommand != "old {path}" || settingsFile(t, st) != "" {
		t.Errorf("Discard returns to the list with nothing saved: mode %v", m.settingsView.mode)
	}
	// esc unchanged → the list; esc in the list → rows; never closing the screen.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.settingsView.mode != settingsList {
		t.Error("esc unchanged returns to the list")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.settingsView.mode != settingsRows || m.screen != screenSettings {
		t.Error("esc in the list returns to the rows")
	}
	// q , j k and space are text in the input.
	m, _ = openCustom(t, m)
	m.settingsView.input.SetValue("")
	m = typeIn(m, "q,jk ")
	if m.settingsView.input.Value() != "q,jk " || m.screen != screenSettings || m.settingsView.mode != settingsInput {
		t.Errorf("printable keys are text: %q", m.settingsView.input.Value())
	}
	// A successful save: both keys, the row, the status.
	m.settingsView.input.SetValue(`"/My Editor/edit" --open {path}`)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.mode != settingsRows || m.status != "saved settings" || m.prefs.Editor != "custom" || m.prefs.EditorCustomCommand != `"/My Editor/edit" --open {path}` {
		t.Errorf("save: mode %v status %q prefs %+v", m.settingsView.mode, m.status, m.prefs)
	}
	f := settingsFile(t, st)
	if !strings.Contains(f, "editor = 'custom'") || !strings.Contains(f, `editor_custom_command = '"/My Editor/edit" --open {path}'`) {
		t.Errorf("file = %q", f)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Editor: Custom: /My Editor/edit…") || strings.Contains(v, "--open") {
		t.Errorf("row shows only the executable:\n%s", v)
	}
	if fi, err := os.Stat(filepath.Join(st.Dir(), "settings.toml")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", fi.Mode(), err)
	}
}

// A saved command with a tab opens with the rewrite notice and enter saves the
// cleaned text; esc with no edit raises no dialog.
func TestSettingsCommandRewriteNotice(t *testing.T) {
	noKeyGrace(t)
	m, st := settingsModel(t)
	m.prefs.Editor = "custom"
	m.prefs.EditorCustomCommand = "edit\t{path}"
	m, _ = openCustom(t, m)
	if !m.settingsView.inputRewrite || m.settingsView.input.Value() != "edit {path}" {
		t.Fatalf("rewrite %v value %q", m.settingsView.inputRewrite, m.settingsView.input.Value())
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Custom command: line endings/tabs/control characters will be rewritten when saved") {
		t.Errorf("notice missing:\n%s", v)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.unsaved.open || m.settingsView.mode != settingsList {
		t.Error("esc with no further edit raises no dialog")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.prefs.EditorCustomCommand != "edit {path}" || !strings.Contains(settingsFile(t, st), "editor_custom_command = 'edit {path}'") || m.settingsView.inputRewrite {
		t.Errorf("enter saves the cleaned text: %q", settingsFile(t, st))
	}
}

// A rejection goes with the input: leaving by Discard or by a clean esc clears
// it, unless something has replaced it (and a save replaces it anyway).
func TestSettingsCommandRejectionLeavesWithTheInput(t *testing.T) {
	noKeyGrace(t)
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	// Discard, then back out to the sandbox list.
	m, _ := settingsModel(t)
	m, _ = openCustom(t, m)
	m.settingsView.input.SetValue("foo 'bar")
	m, _ = update(m, enter)
	if m.status != "the command has an unbalanced quote" || !m.statusIsErr() {
		t.Fatalf("rejection: %q red %v", m.status, m.statusIsErr())
	}
	m, _ = update(m, esc)
	m, _ = update(m, press("d"))
	if m.settingsView.mode != settingsList || m.status != "" {
		t.Errorf("after Discard: mode %v status %q", m.settingsView.mode, m.status)
	}
	m, _ = update(m, esc)
	m, _ = update(m, esc)
	if m.screen != screenList || m.status != "" || m.statusIsErr() {
		t.Errorf("back on the list: screen %v status %q", m.screen, m.status)
	}
	// A clean esc: an empty command rejected, nothing typed.
	m, _ = settingsModel(t)
	m, _ = openCustom(t, m)
	m, _ = update(m, enter)
	if m.status != "a command is required" {
		t.Fatalf("rejection: %q", m.status)
	}
	m, _ = update(m, esc)
	if m.settingsView.mode != settingsList || m.status != "" {
		t.Errorf("after a clean esc: mode %v status %q", m.settingsView.mode, m.status)
	}
	// A late v failure replaced the rejection: leaving doesn't clear that.
	m, _ = settingsModel(t)
	m, _ = openCustom(t, m)
	m, _ = update(m, enter)
	m, _ = update(m, editorExitMsg{sandboxID: "sb1", text: "the editor exited"})
	m, _ = update(m, esc)
	if m.status != "the editor exited" || !m.statusIsErr() {
		t.Errorf("a newer failure stays: %q red %v", m.status, m.statusIsErr())
	}
	// A rejection, then a valid command saved.
	m, _ = settingsModel(t)
	m, _ = openCustom(t, m)
	m, _ = update(m, enter)
	m.settingsView.input.SetValue("edit {path}")
	m, _ = update(m, enter)
	if m.settingsView.mode != settingsRows || m.status != "saved settings" || m.statusIsErr() {
		t.Errorf("after a save: mode %v status %q red %v", m.settingsView.mode, m.status, m.statusIsErr())
	}
}

// A write error keeps the input open and v keeps the old editor; a nil store
// closes it with (not saved); a broken file closes it with the mark; a
// session-only Custom then Zed confirmed saves Zed, clears the mark, and
// Custom again pre-fills the command (FR-528, FR-530).
func TestSettingsCommandWriteOutcomes(t *testing.T) {
	noKeyGrace(t)
	m, st := settingsModel(t)
	// A write error: settings.toml is a directory, so the re-read fails as
	// unreadable; a rename failure needs the temp dir unwritable instead.
	if err := os.Chmod(st.Dir(), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.Dir(), 0o755) })
	m, _ = openCustom(t, m)
	m.settingsView.input.SetValue("edit {path}")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if os.Geteuid() != 0 {
		if m.settingsView.mode != settingsInput || !strings.HasPrefix(m.status, "error: could not save settings") || m.prefs.Editor != "" {
			t.Errorf("write error: mode %v status %q editor %q", m.settingsView.mode, m.status, m.prefs.Editor)
		}
	}
	_ = os.Chmod(st.Dir(), 0o755)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("d"))

	// No store.
	ns := twoHostModel(t, &fakeDaemon{})
	ns, _ = openCustom(t, ns)
	ns.settingsView.input.SetValue("edit {path}")
	ns, _ = update(ns, tea.KeyMsg{Type: tea.KeyEnter})
	if ns.settingsView.mode != settingsRows || ns.prefs.Editor != "custom" || !strings.Contains(ns.status, "session only") || !strings.Contains(ansi.Strip(ns.View()), "Custom: edit… (not saved)") {
		t.Errorf("no store: mode %v status %q view:\n%s", ns.settingsView.mode, ns.status, ansi.Strip(ns.View()))
	}

	// Broken file: closes with the mark; fixed, Zed saves and clears the mark;
	// Custom again pre-fills the session command.
	writeSettingsFile(t, st, "auto_connect_hosts = [broken")
	m, _ = openCustom(t, m)
	m.settingsView.input.SetValue("edit {path}")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.mode != settingsRows || m.prefs.Editor != "custom" || m.settingsView.lastSaveFailed == nil || !m.settingsView.notSaved[keyEditor] {
		t.Errorf("broken file: mode %v prefs %+v failed %v", m.settingsView.mode, m.prefs, m.settingsView.lastSaveFailed)
	}
	writeSettingsFile(t, st, "auto_connect_hosts = true\n")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m.settingsView.listCursor = 4 // Zed
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.prefs.Editor != "zed" || m.settingsView.notSaved[keyEditor] || !strings.Contains(settingsFile(t, st), "editor = 'zed'") || strings.Contains(settingsFile(t, st), "custom_command") {
		t.Errorf("Zed: prefs %+v file %q", m.prefs, settingsFile(t, st))
	}
	m, _ = openCustom(t, m)
	if m.settingsView.input.Value() != "edit {path}" {
		t.Errorf("Custom again pre-fills the session command: %q", m.settingsView.input.Value())
	}
	// Every rejection has a wording of its own.
	for _, rule := range []string{"empty-executable", "marker-in-executable", "assignment", "unbalanced-quotes", "trailing-backslash", "control-character", "other"} {
		if inputRejectionText(rule) == "" {
			t.Errorf("%s has no text", rule)
		}
	}
	_ = store.SettingsChange{}
}
