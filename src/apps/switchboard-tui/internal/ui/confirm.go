package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// confirmState backs a modal yes/no gate in front of a destructive action.
//
// The pending action is held as a tea.Cmd rather than an action enum: a Cmd is a
// closure over values already captured at the call site (exactly what destroyCmd /
// setTagCmd return), so the dialog stays agnostic about what it is guarding and
// nothing has to be re-derived from the list selection after the user answers —
// the selection may well have moved by then.
type confirmState struct {
	title string
	// body lines are rendered above the prompt; keep each short enough for the
	// modal's inner width.
	body []string
	// verb is the busy label shown on the row while the action runs (<= 8 chars,
	// per the list's pad width).
	verb string
	// sandboxID is the row the busy spinner attaches to ("" = no spinner).
	sandboxID string
	// onConfirm runs when the user accepts. Never nil while screen == screenConfirm.
	onConfirm tea.Cmd
}

// enterConfirm opens the dialog over the sandbox list. The caller supplies the
// already-built command so the decision to act is made once, up front.
func (m Model) enterConfirm(st confirmState) (tea.Model, tea.Cmd) {
	m.confirm = st
	m.screen = screenConfirm
	return m, nil
}

func (m Model) confirmHelp() helpBindings {
	return helpBindings{hkey("y/enter", "confirm"), hkey("n/esc", "cancel")}
}

// updateConfirmKey answers the dialog. Cancel is the default: esc, n, and q all
// back out, and any unrecognised key is ignored rather than treated as consent.
func (m Model) updateConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		cmd := m.confirm.onConfirm
		id, verb := m.confirm.sandboxID, m.confirm.verb
		m.confirm = confirmState{}
		m.screen = screenList
		if cmd == nil {
			return m, nil
		}
		if id != "" && verb != "" {
			return m.startBusy(id, verb), cmd
		}
		return m, cmd
	case "n", "N", "esc", "q", "ctrl+c":
		m.confirm = confirmState{}
		m.screen = screenList
		return m, nil
	}
	return m, nil
}

// confirmModal renders the dialog body. It is composited over the list by View via
// overlayCenter, the same path the launch modal uses.
func (m Model) confirmModal() string {
	rows := []string{sectionStyle.Render(m.confirm.title), ""}
	rows = append(rows, m.confirm.body...)
	rows = append(rows,
		"",
		dangerStyle.Render("This cannot be undone."),
		"",
		dimStyle.Render("y/enter confirm · n/esc cancel"),
	)
	inner := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
