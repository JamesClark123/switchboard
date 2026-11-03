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

// The Custom command input (SWI-24, FR-506, FR-521, FR-528; research R11): a
// guarded inline input under SWI-9 (gEditorCommand). enter writes first and
// closes after; a rejection or a failed write keeps it open; esc backs out
// one level, through the unsaved-changes dialog when the text changed.

// editorCommandNote is the note beside the input (FR-521).
const editorCommandNote = "{path} is the workspace folder; {host} the SSH target (empty for a local sandbox). " +
	"With no {path} the folder is added last. No shell: a leading ~/ expands; $VAR, && and | aren't allowed; " +
	"globs stay literal. The command runs with no terminal, so wrap a terminal editor: kitty -e nvim {path}. " +
	"A program that re-parses its arguments (sh -c) can run the path as code when a marker sits inside its " +
	"script — pass the path as an argument instead: kitty -e sh -c 'cd \"$1\" && nvim .' sh {path}. " +
	"A {host} inside a larger argument only works on remote sandboxes."

// enterEditorCommand opens the input pre-filled with the session's command.
// The input cleans the text as it's set (tabs and newlines to spaces, other
// control characters dropped); the cleaned, trimmed value is the baseline,
// and the rewrite notice shows when cleaning changed the saved command.
func (m Model) enterEditorCommand() (tea.Model, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.PromptStyle = selectedStyle
	ti.Cursor.Style = cursorBarStyle
	ti.Width = m.editorInputWidth()
	ti.SetValue(m.prefs.EditorCustomCommand)
	ti.CursorEnd()
	m.settingsView.input = ti
	m.settingsView.inputInitial = strings.TrimSpace(ti.Value())
	m.settingsView.inputRewrite = m.settingsView.inputInitial != strings.TrimSpace(m.prefs.EditorCustomCommand)
	m.settingsView.mode = settingsInput
	return m, m.settingsView.input.Focus()
}

// editorInputWidth is the input's visible width: the body less the prompt
// and a margin, so a long command scrolls sideways (FR-534).
func (m Model) editorInputWidth() int {
	if w := m.bodyWidth() - 4; w >= 10 {
		return w
	}
	return 10
}

// resizeEditorInput resets the input's width on a window resize, keeping the
// cursor where it was: Width alone doesn't move the visible window until the
// next key, and SetCursor moves it only when the cursor is outside the old
// window, so the cursor goes to the end first (FR-534).
func (m *Model) resizeEditorInput() {
	if m.screen != screenSettings || m.settingsView.mode != settingsInput {
		return
	}
	pos := m.settingsView.input.Position()
	m.settingsView.input.Width = m.editorInputWidth()
	m.settingsView.input.CursorEnd()
	m.settingsView.input.SetCursor(pos)
}

func (m Model) updateSettingsInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		return m.requestExit(gEditorCommand)
	case tea.KeyEnter:
		return m.submitEditorCommand()
	}
	var cmd tea.Cmd
	m.settingsView.input, cmd = m.settingsView.input.Update(msg)
	return m, cmd
}

// inputRejectionText is the settings screen's wording for an FR-506 rule
// (contract §4): no `(,)` suffix, no command text.
func inputRejectionText(rule string) string {
	switch rule {
	case editor.RuleEmpty:
		return "a command is required"
	case editor.RuleEmptyExecutable:
		return "the command has an empty first argument — there would be no program to start"
	case editor.RuleMarkerInExecutable:
		return "the first argument contains a marker — the program can't be {path} or {host}"
	case editor.RuleAssignment:
		return "the command starts with NAME=value, which nothing sets — use a wrapper script"
	case editor.RuleShellSyntax:
		return "the command doesn't run in a shell: $VAR, backticks and unquoted | ; & < > aren't allowed — use sh -c or a wrapper script (see the note)"
	case editor.RuleUnbalancedQuotes:
		return "the command has an unbalanced quote"
	case editor.RuleTrailingBackslash:
		return "the command ends in a backslash"
	case editor.RuleControlCharacter:
		return "the command contains a control character"
	}
	return "the command was rejected: " + rule
}

// submitEditorCommand is enter (and the dialog's Save): the trimmed text is
// checked, then written first and the input closed after (FR-528). With no
// store or a file unreadable at startup, no write is tried: the command
// applies to the session and the row shows (not saved). A save whose re-read
// fails applies for the session the same way and records the failure. Any
// other write error keeps the input open with the error and the session
// unchanged.
func (m Model) submitEditorCommand() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.settingsView.input.Value())
	words, err := editor.Split(text)
	if err == nil {
		err = editor.Check(words)
	}
	if err != nil {
		var ce *editor.CheckError
		rule := "invalid"
		if errors.As(err, &ce) {
			rule = ce.Rule
		}
		m.showErr(inputRejectionText(rule))
		return m, nil
	}
	custom := editor.Custom
	apply := func(s *store.Settings) {
		s.Editor = custom
		s.EditorCustomCommand = text
	}
	tried, writeErr := m.writeSetting(store.SettingsChange{Editor: &custom, EditorCustomCommand: &text})
	var ue *store.SettingsUnreadableError
	if writeErr != nil && !errors.As(writeErr, &ue) {
		// SWI-9 FR-017: the input stays open with its text and the error, and
		// the session's editor and command don't change.
		m.showErr("error: could not save settings: " + writeErr.Error())
		return m, nil
	}
	m = m.applySaveOutcome(keyEditor, apply, tried, writeErr)
	m = m.leaveEditorCommand(settingsRows)
	return m, nil
}

// leaveEditorCommand closes the input to the given mode.
func (m Model) leaveEditorCommand(to settingsMode) Model {
	m.settingsView.input.Blur()
	m.settingsView.inputRewrite = false
	m.settingsView.mode = to
	return m
}

// settingsInputFixedLines are the body lines above the note: the title, the
// input, a blank, and the rewrite notice when shown.
func (m Model) settingsInputFixedLines() int {
	n := 3
	if m.settingsView.inputRewrite {
		n += strings.Count(m.rewriteNotice(), "\n") + 1
	}
	return n
}

func (m Model) rewriteNotice() string {
	return ansi.Wrap(normalizeNotice([]string{"Custom command"}), m.bodyWidth(), "")
}

// viewEditorCommand replaces everything below the header with the title, the
// input, a blank, the rewrite notice when it applies and the FR-521 note,
// which gives way first on a short terminal: its room is bodyHeight less the
// fixed lines, never below 1, and its last lines are replaced by `…`.
func (m Model) viewEditorCommand() string {
	rows := []string{sectionStyle.Render("Custom command"), m.settingsView.input.View(), ""}
	if m.settingsView.inputRewrite {
		rows = append(rows, dimStyle.Render(m.rewriteNotice()))
	}
	note := strings.Split(ansi.Wrap(editorCommandNote, m.bodyWidth(), ""), "\n")
	room := m.bodyHeight() - m.settingsInputFixedLines()
	if room < 1 {
		room = 1
	}
	if len(note) > room {
		note = note[:room]
		note[room-1] = ansi.Truncate(note[room-1], m.bodyWidth()-1, "") + "…"
	}
	for _, l := range note {
		rows = append(rows, dimStyle.Render(l))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}
