package ui

import (
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// The settings screen (feature 008, FR-101; SWI-24 FR-504..FR-509, FR-525,
// FR-530, FR-534) lists every TUI-persisted setting with its value and
// changes it in place, saving immediately. Reachable with `,` from either
// top-level screen; esc returns to the screen it was opened from. Rows: the
// automatic host sign-in toggle and the Editor row, whose list and command
// input replace everything below the header while open
// (contracts/settings-ui.md §1).

type settingsMode int

const (
	settingsRows settingsMode = iota
	settingsList
	settingsInput
)

type settingsState struct {
	cursor   int
	returnTo screen
	mode     settingsMode
	// listCursor is the cursor in the Editor list; it starts on the session's editor.
	listCursor int
	// The Custom command input (settings_command.go): its cleaned, trimmed
	// starting value is the SWI-9 baseline; inputRewrite draws the rewrite
	// notice while cleaning changed the saved command.
	input        textinput.Model
	inputInitial string
	inputRewrite bool
	// inputError is the rejection or failed write showing for the input;
	// leaving the input clears it if nothing has replaced it.
	inputError string
	// fileUnreadable: settings.toml couldn't be read at startup (FR-525 state
	// 1): nothing is written this session. lastSaveFailed: a save's re-read
	// failed (state 2): cleared by the next successful save. notSaved marks
	// the rows whose shown value was never written (FR-530).
	fileUnreadable error
	lastSaveFailed error
	notSaved       map[string]bool
}

type rowKind int

const (
	rowToggle rowKind = iota
	rowEditor
)

// settingRow is one row; a toggle writes back through set.
type settingRow struct {
	key, label, desc string
	kind             rowKind
	get              func(store.Settings) bool
	set              func(*store.Settings, bool)
}

const (
	keyAutoConnect = "auto_connect_hosts"
	keyEditor      = "editor"
)

var settingRows = []settingRow{{
	key:   keyAutoConnect,
	label: "Automatic host sign-in at startup",
	desc:  "Try every saved remote host when the TUI starts; prompt for a password where one is needed (next start)",
	get:   func(s store.Settings) bool { return s.AutoConnectHosts },
	set:   func(s *store.Settings, v bool) { s.AutoConnectHosts = v },
}, {
	key:   keyEditor,
	label: "Editor",
	desc:  "Which editor v opens the sandbox's workspace folder in",
	kind:  rowEditor,
}}

// WithSettings attaches the settings store and loads the persisted preferences.
// An unreadable file leaves the defaults in force for the session and is never
// written (FR-525); the startup notices go to the status line, best effort
// (FR-529).
func (m Model) WithSettings(ss *store.SettingsStore) Model {
	m.settingsStore = ss
	m.prefs = store.DefaultSettings()
	if ss == nil {
		return m
	}
	prefs, err := ss.Load()
	m.prefs = prefs
	m.settingsView.fileUnreadable = err
	if n := m.vNotices(); len(n) > 0 {
		m.status = strings.Join(n, "; ")
	}
	return m
}

// sessionEditor is the editor the session uses: the saved identifier, or VS
// Code for an absent or unknown value, with the unknown value (cut to 60
// columns, control characters removed) for the notices (FR-508, FR-533).
func (m Model) sessionEditor() (editor.Editor, string) {
	vscode, _ := editor.Lookup(editor.Default)
	if m.prefs.Editor == "" {
		return vscode, ""
	}
	if e, ok := editor.Lookup(m.prefs.Editor); ok {
		return e, ""
	}
	return vscode, cutEnd(m.prefs.Editor, valueCols)
}

// settingsNotices are the screen's notices: the unknown editor, the
// unreadable-at-startup file, the mid-session failure, each with the
// instruction before any error detail (FR-534).
func (m Model) settingsNotices() []string {
	var out []string
	if _, unknown := m.sessionEditor(); unknown != "" {
		out = append(out, "unknown editor '"+unknown+"' — using VS Code; other settings loaded")
	}
	if err := m.settingsView.fileUnreadable; err != nil {
		out = append(out, "settings couldn't be read at startup, changes aren't saved — fix the file and restart ("+cutEnd(err.Error(), outsideTextCols)+")")
	}
	if err := m.settingsView.lastSaveFailed; err != nil {
		out = append(out, "the last save couldn't read settings.toml — fix the file; the next save retries ("+cutEnd(err.Error(), outsideTextCols)+")")
	}
	return out
}

// vNotices are the notices every `v` outcome carries (FR-529): the first two
// conditions only; a mid-session failure adds nothing to `v`.
func (m Model) vNotices() []string {
	var out []string
	if _, unknown := m.sessionEditor(); unknown != "" {
		out = append(out, "unknown editor '"+unknown+"' in settings")
	}
	if m.settingsView.fileUnreadable != nil {
		out = append(out, "settings couldn't be read at startup, changes aren't saved")
	}
	return out
}

// editorRowValue is the Editor row's value (FR-504, FR-532, FR-534): the
// display name, or `Custom: <executable>` (cut in the middle, `…` when there
// are more arguments) or `Custom (invalid command)`, then `(not saved)`.
func (m Model) editorRowValue() string {
	e, _ := m.sessionEditor()
	v := e.Name
	if e.ID == editor.Custom {
		words, err := editor.Split(m.prefs.EditorCustomCommand)
		if err == nil {
			err = editor.Check(words)
		}
		if err != nil {
			v = "Custom (invalid command)"
		} else {
			v = "Custom: " + cutMiddle(words[0].Text, valueCols)
			if len(words) > 1 {
				v += "…"
			}
		}
	}
	if m.settingsView.notSaved[keyEditor] {
		v += " (not saved)"
	}
	return v
}

func (m Model) enterSettings(returnTo screen) (tea.Model, tea.Cmd) {
	m.settingsView.returnTo = returnTo
	m.settingsView.cursor = 0
	m.settingsView.mode = settingsRows
	m.screen = screenSettings
	return m, nil
}

func (m Model) settingsHelp() helpBindings {
	switch m.settingsView.mode {
	case settingsList:
		return helpBindings{hkey("enter", "select"), hkey("↑/↓", "move"), hkey("esc", "back")}
	case settingsInput:
		return helpBindings{hkey("enter", "save"), hkey("esc", "back")}
	}
	return helpBindings{hkey("space/enter", "toggle/select"), hkey("↑/↓", "move"), hkey("esc", "back")}
}

// saveSetting applies a change to the session and writes it (FR-530): c is
// what's written, apply what the session takes on. With no store, or a file
// unreadable at startup, nothing is written and the row is marked (not
// saved); a save whose re-read fails marks it too and records the failure;
// any other write error keeps the session's value and says so; success
// clears the mark and the failure.
func (m Model) saveSetting(key string, c store.SettingsChange, apply func(*store.Settings)) Model {
	tried, err := m.writeSetting(c)
	return m.applySaveOutcome(key, apply, tried, err)
}

// writeSetting writes c through the store, unless there is none or the file
// was unreadable at startup (nothing is written that session).
func (m Model) writeSetting(c store.SettingsChange) (tried bool, err error) {
	if m.settingsStore == nil || m.settingsView.fileUnreadable != nil {
		return false, nil
	}
	return true, m.settingsStore.Update(c)
}

// applySaveOutcome applies the change to the session and reports the write's
// outcome on the row and the status line.
func (m Model) applySaveOutcome(key string, apply func(*store.Settings), tried bool, err error) Model {
	apply(&m.prefs)
	m.markNotSaved(key, true)
	var ue *store.SettingsUnreadableError
	switch {
	case m.settingsStore == nil:
		m.status = "settings store unavailable — change applies to this session only"
	case !tried:
		m.status = "change not saved — settings couldn't be read at startup; fix settings.toml and restart"
	case errors.As(err, &ue):
		m.settingsView.lastSaveFailed = err
		m.status = "change not saved: fix settings.toml (" + cutEnd(ue.Err.Error(), outsideTextCols) + ")"
	case err != nil:
		m.status = "error: could not save settings: " + err.Error()
	default:
		m.markNotSaved(key, false)
		m.settingsView.lastSaveFailed = nil
		m.status = "saved settings"
	}
	return m
}

func (m *Model) markNotSaved(key string, v bool) {
	if m.settingsView.notSaved == nil {
		m.settingsView.notSaved = map[string]bool{}
	}
	m.settingsView.notSaved[key] = v
}

func (m Model) updateSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.settingsView.mode {
	case settingsList:
		return m.updateSettingsListKey(msg)
	case settingsInput:
		return m.updateSettingsInputKey(msg)
	}
	switch msg.String() {
	case "esc", "q", ",":
		m.screen = m.settingsView.returnTo
		return m, nil
	case "up", "k":
		if m.settingsView.cursor > 0 {
			m.settingsView.cursor--
		}
	case "down", "j":
		if m.settingsView.cursor < len(settingRows)-1 {
			m.settingsView.cursor++
		}
	case " ", "enter":
		it := settingRows[m.settingsView.cursor]
		if it.kind == rowEditor {
			return m.enterEditorList(), nil
		}
		v := !it.get(m.prefs)
		m = m.saveSetting(it.key, store.SettingsChange{AutoConnectHosts: &v}, func(s *store.Settings) { it.set(s, v) })
	}
	return m, nil
}

// enterEditorList opens the Editor list with the cursor on the session's editor.
func (m Model) enterEditorList() Model {
	e, _ := m.sessionEditor()
	m.settingsView.listCursor = 0
	for i, c := range editor.Catalogue() {
		if c.ID == e.ID {
			m.settingsView.listCursor = i
		}
	}
	m.settingsView.mode = settingsList
	return m
}

// updateSettingsListKey is the Editor list (FR-505): enter always saves the
// entry under the cursor, even when marked, and returns to the rows; Custom
// opens the command input instead; esc returns to the rows with nothing
// saved; space, q and `,` do nothing.
func (m Model) updateSettingsListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cat := editor.Catalogue()
	switch msg.String() {
	case "esc":
		m.settingsView.mode = settingsRows
	case "up", "k":
		if m.settingsView.listCursor > 0 {
			m.settingsView.listCursor--
		}
	case "down", "j":
		if m.settingsView.listCursor < len(cat)-1 {
			m.settingsView.listCursor++
		}
	case "enter":
		e := cat[m.settingsView.listCursor]
		if e.ID == editor.Custom {
			return m.enterEditorCommand()
		}
		id := e.ID
		m = m.saveSetting(keyEditor, store.SettingsChange{Editor: &id}, func(s *store.Settings) { s.Editor = id })
		m.settingsView.mode = settingsRows
	}
	return m, nil
}

func (m Model) viewSettings() string {
	switch m.settingsView.mode {
	case settingsList:
		return m.viewEditorList()
	case settingsInput:
		return m.viewEditorCommand()
	}
	wrap := func(s string) string { return ansi.Wrap(s, m.bodyWidth(), "") }
	rows := []string{sectionStyle.Render("Settings"), wrap(dimStyle.Render("persisted by the TUI in settings.toml under the config dir")), ""}
	if notices := m.settingsNotices(); len(notices) > 0 {
		for _, n := range notices {
			rows = append(rows, wrap(statusErrStyle.Render("⚠ "+n)))
		}
		rows = append(rows, "")
	}
	for i, it := range settingRows {
		cursor := "  "
		if i == m.settingsView.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		var line string
		switch it.kind {
		case rowEditor:
			line = cursor + it.label + ": " + m.editorRowValue()
		default:
			state := dimStyle.Render("[off]")
			if it.get(m.prefs) {
				state = selectedStyle.Render("[on] ")
			}
			if m.settingsView.notSaved[it.key] {
				state += " (not saved)"
			}
			line = cursor + state + " " + it.label
		}
		rows = append(rows, wrap(line), wrap("       "+dimStyle.Render(it.desc)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// viewEditorList replaces everything below the header with the title line
// and the entries, windowed around the cursor (FR-534); the view reads
// bodyHeight as it renders, like Jobs.
func (m Model) viewEditorList() string {
	cat := editor.Catalogue()
	cur, _ := m.sessionEditor()
	lines := make([]string, 0, len(cat))
	for i, e := range cat {
		cursor := "  "
		if i == m.settingsView.listCursor {
			cursor = cursorBarStyle.Render("> ")
		}
		mark := "  "
		if e.ID == cur.ID {
			mark = selectedStyle.Render("● ")
		}
		lines = append(lines, cursor+mark+e.Name)
	}
	start, end := windowRange(len(lines), m.settingsView.listCursor, m.bodyHeight()-1)
	rows := append([]string{sectionStyle.Render("Editor")}, lines[start:end]...)
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}
