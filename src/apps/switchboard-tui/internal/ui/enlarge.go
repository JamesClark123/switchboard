package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The enlarged view (SWI-9 research R2, contracts/forms-ui.md §2): ctrl+e on a
// focused multi-line field opens its text in a full-terminal textarea drawn
// instead of the screen chrome. It edits a copy and writes it back to the
// field's bound pointer on close; the form's focus never moves meanwhile.

// enlargeHint is the view's key line (76 cells; fits 80 columns).
const enlargeHint = "esc close · ctrl+s save · ctrl+r revert · pgup/pgdn page · ctrl+c leave form"

// enlargeChromeRows are the rows around the text: the top and bottom border and
// the hint line. 80×24 leaves 21 text lines.
const enlargeChromeRows = 3

type enlargeState struct {
	open bool
	// surface is the form the field belongs to; its edits count toward that
	// surface's changes (FR-007).
	surface guardSurface
	field   *huh.Text
	ptr     *string
	title   string
	ta      textarea.Model
	// baseline is the text at ctrl+e; ctrl+r restores it (FR-005b).
	baseline string
}

// enlargeKeyMap is the textarea's default with end-of-line on end only (ctrl+e
// closes the view) and newline on the forms' Text.NewLine keys.
func enlargeKeyMap() textarea.KeyMap {
	km := textarea.DefaultKeyMap
	km.LineEnd = key.NewBinding(key.WithKeys("end"))
	km.InsertNewline = key.NewBinding(key.WithKeys("enter", "alt+enter", "ctrl+j"))
	return km
}

// enlargeStyles styles the textarea as huh draws a focused Text (huh
// field_text.go Text.View maps the theme's TextInput styles the same way).
func enlargeStyles() (textarea.Style, lipgloss.Style, lipgloss.Style) {
	ti := huhTheme().Focused.TextInput
	st := textarea.Style{
		Placeholder: ti.Placeholder,
		Text:        ti.Text,
		Prompt:      ti.Prompt,
		CursorLine:  ti.Text,
	}
	return st, ti.Cursor, ti.CursorText
}

// enlargeSize is the textarea's size inside the border, at least one cell.
func enlargeSize(w, h int) (int, int) {
	return max(w-2, 1), max(h-enlargeChromeRows, 1)
}

// openEnlarge opens the view on field, the focused Text of g's form.
func (m *Model) openEnlarge(g guardSurface, field *huh.Text, e textEntry) tea.Cmd {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.KeyMap = enlargeKeyMap()
	st, cur, curText := enlargeStyles()
	ta.FocusedStyle, ta.BlurredStyle = st, st
	ta.Cursor.Style, ta.Cursor.TextStyle = cur, curText
	w, h := enlargeSize(m.width, m.height)
	ta.SetWidth(w)
	ta.SetHeight(h)
	ta.SetValue(*e.ptr)
	// Focus first: the textarea ignores keys while blurred. Then ctrl+home
	// (InputBegin) puts the cursor at the top and scrolls there.
	cmd := ta.Focus()
	ta, _ = ta.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	m.enlarge = enlargeState{open: true, surface: g, field: field, ptr: e.ptr, title: e.title, ta: ta, baseline: *e.ptr}
	return cmd
}

// writeBack puts the view's text into the field: the bound pointer, then the
// field's own textarea from it, scrolled so its cursor is visible.
func (e *enlargeState) writeBack() {
	*e.ptr = e.ta.Value()
	e.field.Value(e.ptr)
	e.field.Update(nil)
}

// resize refits the view, keeping text, cursor and scroll.
func (e *enlargeState) resize(w, h int) {
	tw, th := enlargeSize(w, h)
	e.ta.SetWidth(tw)
	e.ta.SetHeight(th)
	e.ta, _ = e.ta.Update(nil) // SetWidth/SetHeight do not re-scroll to the cursor
}

// updateEnlargeKey handles a key while the view is open.
func (m Model) updateEnlargeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := &m.enlarge
	switch msg.String() {
	case "esc", "ctrl+e":
		e.writeBack()
		m.enlarge = enlargeState{}
		return m, nil
	case "ctrl+s":
		g := e.surface
		e.writeBack()
		m.enlarge = enlargeState{}
		return m.surfaceSave(g)
	case "ctrl+c":
		// As ctrl+c in the form: the text counts toward the form's changes, and a
		// changed form asks over the view (Cancel returns here, FR-005a).
		e.writeBack()
		next, cmd := m.requestExit(e.surface)
		nm := next.(Model)
		if nm.unsaved.open {
			nm.unsaved.fromEnlarge = true
		} else {
			nm.enlarge = enlargeState{}
		}
		return nm, cmd
	case "ctrl+r":
		e.ta.SetValue(e.baseline)
		e.ta, _ = e.ta.Update(nil)
		return m, nil
	case "pgup", "pgdown":
		// Batched cursor moves and one reposition: an Update per line costs
		// ~8 ms on a 5,000-line text (research R9).
		for range e.ta.Height() {
			if msg.String() == "pgup" {
				e.ta.CursorUp()
			} else {
				e.ta.CursorDown()
			}
		}
		e.ta, _ = e.ta.Update(nil)
		return m, nil
	case "tab", "shift+tab":
		return m, nil // FR-004: no field navigation from the view
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return m, cmd
}

// enlargeView draws the view over the whole terminal: a rounded border with
// the field's title set into its top edge, the text, and the hint line.
func (m Model) enlargeView() string {
	e := m.enlarge
	w := max(m.width, 4)
	border := lipgloss.NewStyle().Foreground(colAccent)
	title := sanitizeLine(e.title, max(w-6, 1))
	fill := max(w-5-lipgloss.Width(title), 0)
	top := border.Render("╭─ ") + sectionStyle.Render(title) + border.Render(" "+strings.Repeat("─", fill)+"╮")
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderTop(false).BorderForeground(colAccent).
		Render(e.ta.View())
	hint := dimStyle.Render(ansi.Truncate(enlargeHint, w, "…"))
	return lipgloss.JoinVertical(lipgloss.Left, top, box, hint)
}
