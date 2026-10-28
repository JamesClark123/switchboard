package ui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The unsaved-changes dialog (SWI-9 research R5, contracts/forms-ui.md §3): a
// layer above a changed surface, raised by requestExit. It is not a screen, so
// the surface keeps m.screen and every bit of its state and Cancel is exact.

// nowFunc is the dialog's clock; tests replace it to step through the grace.
var nowFunc = time.Now

// unsavedKeyGrace is how long after opening only esc/ctrl+c act, so keys typed
// just after a stray esc never reach Discard. A var so story tests can zero it.
var unsavedKeyGrace = 150 * time.Millisecond

// The dialog's choices, in button order; highlight indexes them.
const (
	unsavedSave = iota
	unsavedCancel
	unsavedDiscard
)

type unsavedState struct {
	open    bool
	surface guardSurface
	// fromEnlarge: raised by ctrl+c in the enlarged view, which stays open under
	// the dialog so Cancel returns to it (FR-015).
	fromEnlarge bool
	highlight   int
	// saveLabel is "Save", or "Rebuild" on the reasoning setup.
	saveLabel string
	openedAt  time.Time
}

// openUnsaved raises the dialog for g with Cancel highlighted.
func (m *Model) openUnsaved(g guardSurface, fromEnlarge bool) {
	label := "Save"
	if g == gReasoning {
		label = "Rebuild"
	}
	m.unsaved = unsavedState{
		open:        true,
		surface:     g,
		fromEnlarge: fromEnlarge,
		highlight:   unsavedCancel,
		saveLabel:   label,
		openedAt:    nowFunc(),
	}
}

// updateUnsavedKey answers the dialog. Within the typing grace only the Cancel
// keys act; afterwards s (r on the reasoning setup) saves, d discards, c/esc/
// ctrl+c cancel, arrows/h/l/tab move the highlight and enter activates it.
// Anything else is ignored.
func (m Model) updateUnsavedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "esc" || k == "ctrl+c" {
		return m.unsavedChoose(unsavedCancel)
	}
	if nowFunc().Sub(m.unsaved.openedAt) < unsavedKeyGrace {
		return m, nil
	}
	rebuild := m.unsaved.surface == gReasoning
	switch k {
	case "c":
		return m.unsavedChoose(unsavedCancel)
	case "d":
		return m.unsavedChoose(unsavedDiscard)
	case "s":
		if !rebuild {
			return m.unsavedChoose(unsavedSave)
		}
	case "r":
		if rebuild {
			return m.unsavedChoose(unsavedSave)
		}
	case "left", "h", "shift+tab":
		m.unsaved.highlight = (m.unsaved.highlight + 2) % 3
	case "right", "l", "tab":
		m.unsaved.highlight = (m.unsaved.highlight + 1) % 3
	case "enter":
		return m.unsavedChoose(m.unsaved.highlight)
	}
	return m, nil
}

// unsavedChoose closes the dialog and acts: Save is the surface's own save,
// Discard its esc path, Cancel nothing at all.
func (m Model) unsavedChoose(choice int) (tea.Model, tea.Cmd) {
	g := m.unsaved.surface
	var ecmd tea.Cmd
	if m.unsaved.fromEnlarge && m.enlarge.open && choice != unsavedCancel {
		ecmd = m.closeEnlarge() // Save may be refused and keep the form: refresh it
	}
	m.unsaved = unsavedState{}
	var next Model
	var cmd tea.Cmd
	switch choice {
	case unsavedSave:
		next, cmd = m.guardSave(g)
	case unsavedDiscard:
		next, cmd = m.guardLeave(g)
	default:
		return m, nil
	}
	return next, tea.Batch(ecmd, cmd)
}

// unsavedBody names what is unsaved, by surface kind (≤ 49 cells, ≤ 2 lines).
func unsavedBody(g guardSurface) string {
	switch g {
	case gRename, gTag, gHostAdd, gGroupAdd:
		return "This input has changes that haven't been saved."
	case gKitOverview:
		return "This kit has changes that haven't been saved."
	case gLaunch:
		return "This launch has choices that haven't been used."
	case gReasoning:
		return "The reasoning setup has changes not rebuilt yet."
	case gSources:
		return "These marked folders haven't been removed."
	case gSourcesBrowse:
		return "These selected folders haven't been added."
	}
	return "This form has changes that haven't been saved."
}

// unsavedModal renders the dialog, composited by View over the surface.
func (m Model) unsavedModal() string {
	u := m.unsaved
	labels := [3]string{u.saveLabel, "Cancel", "Discard"}
	buttons := make([]string, len(labels))
	for i, l := range labels {
		if i == u.highlight {
			buttons[i] = selectedStyle.Render("[› " + l + " ‹]")
		} else {
			buttons[i] = dimStyle.Render("[ " + l + " ]")
		}
	}
	rows := []string{
		sectionStyle.Render("Unsaved changes"),
		"",
		unsavedBody(u.surface),
		"",
		"   " + strings.Join(buttons, "   "),
		"",
		dimStyle.Render(helpLine(m.unsavedHelp())),
	}
	inner := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
