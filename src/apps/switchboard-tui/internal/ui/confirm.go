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
	// warning replaces the default "This cannot be undone." line when set — a gate
	// in front of a risky-but-reversible step (the feature-007 low-resource
	// override) should not claim irreversibility.
	warning string
	// notice, when set, replaces the red warning with a plain dim line — for a
	// reassurance ("not restarted") rather than a danger.
	notice string
	// returnTo is the screen shown after the dialog closes (accept or cancel).
	// The zero value is the sandbox list; the sources overlay sets itself so a
	// removal lands the user back on the editor (feature 007).
	returnTo screen
	// onAccept runs on y/enter only, after screen = returnTo and before
	// onConfirm, with the live model: a per-answer effect (the guard's sources
	// Save closes the overlay; its Rebuild lands on the orchestrator detail).
	// It captures plain values only, never a Model. Not a destination screen
	// field: screenList is the zero value, so "go to the list" would read as
	// unset (SWI-9 R6).
	onAccept func(m *Model)
	// yOnly accepts on y alone; enter is ignored (SWI-9 R7 C3). Set on the
	// confirms that open without the user pressing their key, so a repeated or
	// held enter, or one submitting a prompt above, can never accept them.
	yOnly bool
	// enterCancels makes enter answer No (SWI-17): the run-step confirms default
	// to No, and the detached-agent dialog defaults to Wait unless the agent was
	// last seen idle. y still accepts.
	enterCancels bool
	// acceptLabel / cancelLabel name the two answers in the help line when the
	// dialog is a choice rather than a yes/no ("replace" / "wait"); empty keeps
	// "confirm" / "cancel".
	acceptLabel, cancelLabel string
	// next runs on accept after onAccept, with the live model, and its command
	// joins onConfirm's: a chain of y-only confirms (the SWI-21 MCP save
	// sequence) opens its next step from here.
	next func(m *Model) tea.Cmd
	// onCancel runs on every back-out (n/esc/q/ctrl+c, and enter when
	// enterCancels): the MCP form's "back-out returns to the form".
	onCancel func(m *Model) tea.Cmd
}

// enterConfirm opens the dialog over the sandbox list. The caller supplies the
// already-built command so the decision to act is made once, up front.
func (m Model) enterConfirm(st confirmState) (tea.Model, tea.Cmd) {
	m.confirm = st
	m.screen = screenConfirm
	return m, nil
}

func (m Model) confirmHelp() helpBindings {
	accept, cancel := "confirm", "cancel"
	if m.confirm.acceptLabel != "" {
		accept = m.confirm.acceptLabel
	}
	if m.confirm.cancelLabel != "" {
		cancel = m.confirm.cancelLabel
	}
	cancelKeys := "n/esc"
	if m.confirm.enterCancels {
		cancelKeys = "n/enter/esc"
	}
	return helpBindings{hkey(m.confirmAcceptKeys(), accept), hkey(cancelKeys, cancel)}
}

// confirmAcceptKeys names the keys that accept the showing confirm.
func (m Model) confirmAcceptKeys() string {
	if m.confirm.yOnly || m.confirm.enterCancels {
		return "y"
	}
	return "y/enter"
}

// updateConfirmKey answers the dialog. Cancel is the default: esc, n, and q all
// back out, and any unrecognised key is ignored rather than treated as consent.
func (m Model) updateConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		if msg.String() == "enter" && m.confirm.enterCancels {
			return m.cancelConfirm()
		}
		if msg.String() == "enter" && m.confirm.yOnly {
			return m, nil
		}
		cmd, accept, next := m.confirm.onConfirm, m.confirm.onAccept, m.confirm.next
		id, verb := m.confirm.sandboxID, m.confirm.verb
		m.screen = m.confirm.returnTo
		m.confirm = confirmState{}
		if accept != nil {
			accept(&m)
		}
		if next != nil {
			if c := next(&m); c != nil {
				cmd = tea.Batch(cmd, c)
			}
		}
		if cmd == nil {
			return m, nil
		}
		if id != "" && verb != "" {
			return m.startBusy(id, verb), cmd
		}
		return m, cmd
	case "n", "N", "esc", "q", "ctrl+c":
		return m.cancelConfirm()
	}
	return m, nil
}

// cancelConfirm closes the dialog unanswered and runs its onCancel.
func (m Model) cancelConfirm() (tea.Model, tea.Cmd) {
	back := m.confirm.onCancel
	m.screen = m.confirm.returnTo
	m.confirm = confirmState{}
	if back != nil {
		return m, back(&m)
	}
	return m, nil
}

// confirmModal renders the dialog body. It is composited over the list by View via
// overlayCenter, the same path the launch modal uses.
func (m Model) confirmModal() string {
	rows := []string{sectionStyle.Render(sanitizeLine(m.confirm.title, 0)), ""}
	rows = append(rows, m.confirm.body...)
	warning := m.confirm.warning
	if warning == "" {
		warning = "This cannot be undone."
	}
	line := dangerStyle.Render(warning)
	if m.confirm.notice != "" {
		line = dimStyle.Render(m.confirm.notice)
	}
	rows = append(rows,
		"",
		line,
		"",
		dimStyle.Render(helpLine(m.confirmHelp())),
	)
	inner := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
