package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

func settingsModel(t *testing.T) (Model, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{}
	m := twoHostModel(t, d).WithSettings(st.Settings())
	return m, st
}

func settingsFile(t *testing.T, st *store.Store) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.Dir(), "settings.toml"))
	if err != nil {
		return ""
	}
	return string(b)
}

func writeSettingsFile(t *testing.T, st *store.Store, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(st.Dir(), "settings.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsScreenOpensFromBothScreensAndReturns(t *testing.T) {
	m, _ := settingsModel(t)
	m, _ = update(m, press(","))
	if m.screen != screenSettings || !strings.Contains(m.View(), "Automatic host sign-in") {
		t.Fatalf("`,` should open the settings screen; screen %v", m.screen)
	}
	m, _ = update(m, press("esc"))
	if m.screen != screenList {
		t.Errorf("esc should return to the list, got %v", m.screen)
	}
	m, _ = update(m, shiftRight())
	m, _ = update(m, press(","))
	m, _ = update(m, press("esc"))
	if m.screen != screenDaemons {
		t.Errorf("esc should return to the daemon screen it was opened from, got %v", m.screen)
	}
}

func TestSettingsToggleSavesImmediately(t *testing.T) {
	m, st := settingsModel(t)
	if !m.prefs.AutoConnectHosts {
		t.Fatal("auto-connect should default to on")
	}
	m, _ = update(m, press(","))
	if !strings.Contains(m.View(), "[on]") {
		t.Errorf("toggle should read on; got:\n%s", m.View())
	}
	m, _ = update(m, press(" "))
	if m.prefs.AutoConnectHosts || m.status != "saved settings" || !strings.Contains(m.View(), "[off]") {
		t.Errorf("space should turn it off and save; prefs %+v status %q", m.prefs, m.status)
	}
	got, err := st.Settings().Load()
	if err != nil || got.AutoConnectHosts {
		t.Errorf("persisted = %+v, %v", got, err)
	}
	if strings.Contains(m.View(), "(not saved)") {
		t.Error("a saved row has no mark")
	}
}

func TestSettingsUnreadableFileShowsNoticeAndDefaults(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSettingsFile(t, st, "auto_connect_hosts = [broken")
	m := twoHostModel(t, &fakeDaemon{}).WithSettings(st.Settings())
	if !m.prefs.AutoConnectHosts || m.settingsView.fileUnreadable == nil {
		t.Error("defaults must apply when the file is unreadable")
	}
	// The startup notice is in the status line (FR-529, best effort).
	if m.status != "settings couldn't be read at startup, changes aren't saved" {
		t.Errorf("startup status = %q", m.status)
	}
	m, _ = update(m, press(","))
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "settings couldn't be read at startup, changes aren't saved — fix the file and restart (") {
		t.Errorf("the screen should say defaults apply and a restart is needed; got:\n%s", v)
	}
	// A toggle applies to the session only, with the restart wording and the mark.
	m, _ = update(m, press(" "))
	if m.prefs.AutoConnectHosts || !strings.Contains(m.status, "restart") || !strings.Contains(ansi.Strip(m.View()), "(not saved)") {
		t.Errorf("status %q view:\n%s", m.status, ansi.Strip(m.View()))
	}
	if b := settingsFile(t, st); b != "auto_connect_hosts = [broken" {
		t.Errorf("the file must never be written: %q", b)
	}
}

func TestSettingsWithoutStoreAppliesForSessionOnly(t *testing.T) {
	m := twoHostModel(t, &fakeDaemon{}) // no WithSettings: store nil, defaults on
	m, _ = update(m, press(","))
	m, _ = update(m, press(" "))
	if m.prefs.AutoConnectHosts || !strings.Contains(m.status, "session only") || !strings.Contains(ansi.Strip(m.View()), "(not saved)") {
		t.Errorf("prefs %+v status %q", m.prefs, m.status)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, _ = update(m, press(","))
	if m.screen != screenList {
		t.Errorf("`,` again should close the screen, got %v", m.screen)
	}
}

// A file that breaks after startup refuses the save with "fix the file" and no
// restart; the next successful save clears both the notice and the mark, and
// two presses restore a toggle's value (FR-525, FR-530).
func TestSettingsBrokenMidSession(t *testing.T) {
	m, st := settingsModel(t)
	m, _ = update(m, press(","))
	writeSettingsFile(t, st, "editor = 'goland'\nauto_connect_hosts = [broken")
	m, _ = update(m, press(" "))
	if m.prefs.AutoConnectHosts || !strings.Contains(m.status, "fix settings.toml") || strings.Contains(m.status, "restart") {
		t.Errorf("status = %q", m.status)
	}
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "the last save couldn't read settings.toml — fix the file; the next save retries (") || !strings.Contains(v, "(not saved)") {
		t.Errorf("view:\n%s", v)
	}
	writeSettingsFile(t, st, "editor = 'goland'\n")
	m, _ = update(m, press(" ")) // saves the opposite value, clears the mark
	if !m.prefs.AutoConnectHosts || m.status != "saved settings" || m.settingsView.lastSaveFailed != nil {
		t.Errorf("after the fix: prefs %+v status %q", m.prefs, m.status)
	}
	v = ansi.Strip(m.View())
	if strings.Contains(v, "(not saved)") || strings.Contains(v, "last save") {
		t.Errorf("the mark and the notice clear:\n%s", v)
	}
	m, _ = update(m, press(" ")) // the second press saves the original value
	if m.prefs.AutoConnectHosts || !strings.Contains(settingsFile(t, st), "auto_connect_hosts = false") || !strings.Contains(settingsFile(t, st), "editor = 'goland'") {
		t.Errorf("file = %q", settingsFile(t, st))
	}
}

// TestSettingsEditorList (T020, US1): open, move, confirm; esc saves nothing;
// confirming VS Code over an unknown value replaces it and clears the notice;
// toggling sign-in leaves the unknown value; the window follows the cursor;
// q in the list does nothing.
func TestSettingsEditorList(t *testing.T) {
	m, st := settingsModel(t)
	m, _ = update(m, press(","))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Editor: VS Code") {
		t.Fatalf("default row:\n%s", v)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.mode != settingsList {
		t.Fatal("enter on the Editor row opens the list")
	}
	v := ansi.Strip(m.View())
	for _, want := range []string{"Editor", "● VS Code", "WebStorm", "Custom"} {
		if !strings.Contains(v, want) {
			t.Errorf("list missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Automatic host sign-in") || strings.Contains(v, "Settings") {
		t.Errorf("the list replaces the rows:\n%s", v)
	}
	// esc saves nothing.
	for i := 0; i < 5; i++ {
		m, _ = update(m, press("j"))
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.settingsView.mode != settingsRows || m.prefs.Editor != "" || settingsFile(t, st) != "" {
		t.Errorf("esc: mode %v editor %q file %q", m.settingsView.mode, m.prefs.Editor, settingsFile(t, st))
	}
	// Space opens it too; q and space do nothing inside; enter confirms.
	m, _ = update(m, press(" "))
	m, _ = update(m, press("q"))
	m, _ = update(m, press(" "))
	if m.screen != screenSettings || m.settingsView.mode != settingsList {
		t.Fatal("q and space do nothing in the list")
	}
	for i := 0; i < 5; i++ {
		m, _ = update(m, press("j"))
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.mode != settingsRows || m.prefs.Editor != "webstorm" || m.status != "saved settings" || !strings.Contains(settingsFile(t, st), "editor = 'webstorm'") {
		t.Errorf("confirm: mode %v editor %q status %q file %q", m.settingsView.mode, m.prefs.Editor, m.status, settingsFile(t, st))
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Editor: WebStorm") {
		t.Errorf("row:\n%s", v)
	}
	// Reopening starts on the marked entry; k moves up; enter always saves.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.listCursor != 5 || !strings.Contains(ansi.Strip(m.View()), "● WebStorm") {
		t.Errorf("cursor %d", m.settingsView.listCursor)
	}
	m.status = ""
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.status != "saved settings" {
		t.Error("enter on the marked entry still saves")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, press("k"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.prefs.Editor != "zed" {
		t.Errorf("k then enter: %q", m.prefs.Editor)
	}

	// An unknown saved value: the notice, the toggle leaves it, VS Code replaces it.
	writeSettingsFile(t, st, "editor = 'future-ide'\n")
	m = m.WithSettings(st.Settings())
	if m.status != "unknown editor 'future-ide' in settings" {
		t.Errorf("startup status = %q", m.status)
	}
	v = ansi.Strip(m.View())
	if !strings.Contains(v, "unknown editor 'future-ide' — using VS Code; other settings loaded") || !strings.Contains(v, "Editor: VS Code") {
		t.Errorf("unknown editor:\n%s", v)
	}
	m, _ = update(m, press("k"))
	m, _ = update(m, press(" "))
	if !strings.Contains(settingsFile(t, st), "editor = 'future-ide'") || !strings.Contains(ansi.Strip(m.View()), "unknown editor") {
		t.Errorf("a toggle keeps the unknown value and the notice: %q", settingsFile(t, st))
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(ansi.Strip(m.View()), "● VS Code") {
		t.Error("the list marks VS Code for an unknown value")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.prefs.Editor != "vscode" || !strings.Contains(settingsFile(t, st), "editor = 'vscode'") || strings.Contains(ansi.Strip(m.View()), "unknown editor") {
		t.Errorf("confirming VS Code replaces the value and clears the notice: %q", settingsFile(t, st))
	}

	// A 12-row terminal windows the list around the cursor.
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 12})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	for i := 0; i < 10; i++ {
		m, _ = update(m, press("j"))
	}
	v = ansi.Strip(m.View())
	if !strings.Contains(v, "> ") || !strings.Contains(v, "Custom") || strings.Contains(v, "VS Code Insiders") {
		t.Errorf("the window keeps the cursor visible:\n%s", v)
	}
	if lines := strings.Split(v, "\n"); len(lines) > 12 {
		t.Errorf("view is %d lines on a 12-row terminal", len(lines))
	}
}

// assertFits checks the SC-004/SC-008 layout rules on the current view: no
// line wider than width, at most height lines, the header on screen.
func assertFits(t *testing.T, m Model, width, height int) []string {
	t.Helper()
	v := ansi.Strip(m.View())
	lines := strings.Split(v, "\n")
	if len(lines) > height {
		t.Errorf("view is %d lines, more than %d:\n%s", len(lines), height, v)
	}
	if !strings.Contains(lines[0], "Switchboard") {
		t.Errorf("the header is off screen:\n%s", v)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Errorf("line wider than %d (%d): %q", width, w, l)
		}
	}
	return lines
}

// TestSettingsLayout80x24 (T021, T037, SC-008): the rows with both notices
// and a (not saved) mark on a 90-column executable, the open list, and the
// open input with a rejection and the whole note; at 80×18 the note gives way.
func TestSettingsLayout80x24(t *testing.T) {
	noKeyGrace(t)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe := "/Users/developer/Library/Application Support/JetBrains/Toolbox/scripts/" + strings.Repeat("w", 20)
	writeSettingsFile(t, st, "editor = 'custom'\neditor_custom_command = '\""+exe+"\" {path}'\n")
	m := twoHostModel(t, &fakeDaemon{}).WithSettings(st.Settings())
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.prefs.Editor = "future-ide"
	m.settingsView.fileUnreadable = os.ErrPermission
	m.markNotSaved(keyEditor, true)
	m.prefs.Editor = "custom"
	m, _ = update(m, press(","))
	v := strings.Join(strings.Fields(strings.Join(assertFits(t, m, 80, 24), " ")), " ")
	for _, want := range []string{"(not saved)", "Custom: /Users/developer/Lib…", "/scripts/" + strings.Repeat("w", 20) + "… (not saved)", "fix the file and restart"} {
		if !strings.Contains(v, want) {
			t.Errorf("rows missing %q:\n%s", want, v)
		}
	}
	// The open list.
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	assertFits(t, m, 80, 24)
	// The open input with a rejection showing: the whole note is on screen.
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.settingsView.mode != settingsInput {
		t.Fatal("Custom opens the input")
	}
	m.settingsView.input.SetValue("cd {path} && nvim .")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.status, "doesn't run in a shell") || m.settingsView.mode != settingsInput {
		t.Fatalf("rejection: %q mode %v", m.status, m.settingsView.mode)
	}
	lines := assertFits(t, m, 80, 24)
	v = strings.Join(lines, "\n")
	for _, l := range strings.Split(ansi.Wrap(editorCommandNote, 80, ""), "\n") {
		if !strings.Contains(v, l) {
			t.Errorf("note line missing: %q\n%s", l, v)
		}
	}
	if !strings.Contains(v, "Custom command") || !strings.Contains(v, "› ") {
		t.Errorf("the title and the input are on screen:\n%s", v)
	}
	// On a shorter terminal the note ends in … and the input stays: at 80×18
	// the note still fits beside the 2-line rejection, at 80×15 it gives way.
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 18})
	assertFits(t, m, 80, 18)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 15})
	lines = assertFits(t, m, 80, 15)
	v = strings.Join(lines, "\n")
	if !strings.Contains(v, "…") || !strings.Contains(v, "› ") || !strings.Contains(v, "Custom command") || !strings.Contains(v, "doesn't run in a shell") {
		t.Errorf("at 80×15 the note gives way first:\n%s", v)
	}
	// Narrowing with a long command keeps the cursor and no line too wide,
	// at the end and in the middle; widening shows more of the command.
	long := strings.Repeat("abcdefghij", 12)
	m.settingsView.input.SetValue(long)
	m.settingsView.input.CursorEnd()
	m, _ = update(m, tea.WindowSizeMsg{Width: 60, Height: 24})
	assertFits(t, m, 60, 24)
	if m.settingsView.input.Position() != len(long) {
		t.Errorf("cursor moved on resize: %d", m.settingsView.input.Position())
	}
	m.settingsView.input.SetCursor(40)
	m, _ = update(m, tea.WindowSizeMsg{Width: 50, Height: 24})
	assertFits(t, m, 50, 24)
	if m.settingsView.input.Position() != 40 {
		t.Errorf("cursor moved on resize: %d", m.settingsView.input.Position())
	}
	narrow := ansi.Strip(m.settingsView.input.View())
	m, _ = update(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	wide := ansi.Strip(m.settingsView.input.View())
	if len(strings.TrimSpace(wide)) <= len(strings.TrimSpace(narrow)) {
		t.Errorf("widening shows more of the command: %q vs %q", narrow, wide)
	}
}

// TestSettingsStartupNotices (T042): both notices are set in the status at
// startup and shown by the screen; an unknown value with control characters
// and 80 columns is shown cut to 60 with the characters removed.
func TestSettingsStartupNotices(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeSettingsFile(t, st, "editor = \""+strings.Repeat("ab", 40)+"\\u001b[2J\\u0007\"\n")
	m := twoHostModel(t, &fakeDaemon{}).WithSettings(st.Settings())
	shown := ansi.Truncate(strings.Repeat("ab", 40), 60, "…") // 60 columns with the marker
	if m.status != "unknown editor '"+shown+"' in settings" {
		t.Errorf("status = %q", m.status)
	}
	m, _ = update(m, press(","))
	v := ansi.Strip(m.View())
	if !strings.Contains(v, "unknown editor '"+shown+"' — using VS Code") || strings.Contains(v, "\x07") {
		t.Errorf("view:\n%s", v)
	}
	// Both notices, in order.
	m.settingsView.fileUnreadable = os.ErrPermission
	n := m.vNotices()
	if len(n) != 2 || !strings.HasPrefix(n[0], "unknown editor") || n[1] != "settings couldn't be read at startup, changes aren't saved" {
		t.Errorf("vNotices = %q", n)
	}
	if s := m.settingsNotices(); len(s) != 2 {
		t.Errorf("settingsNotices = %q", s)
	}
}
