package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Daemons › <daemon> › Orchestrators (feature 010, user story 5, FR-022..FR-024):
// orchestrator administration moved under the daemon that hosts it. The list is
// that daemon's rows of the orchestrator loader (orchestrators.go): `n` creates
// one on this daemon, `R` renames, `d` removes (confirmed), `enter` opens the
// detail with its Workers, Reasoning and Hosts views, `esc` returns to Daemons.

// daemonOrchsState is the daemon whose Orchestrators section is open; the cursor
// is the orchestrator list's own (orchState.cursor over this daemon's rows).
type daemonOrchsState struct {
	host, hostName string
}

// enterDaemonOrchs opens a connected daemon's Orchestrators section.
func (m Model) enterDaemonOrchs(row daemonRow) (tea.Model, tea.Cmd) {
	host := row.id
	if m.manager == nil {
		// Without a host manager the rows are keyed by the active host entry.
		host = m.activeHost
	}
	if m.daemonOrchs.host != host {
		m.orch.cursor = 0
	}
	m.daemonOrchs = daemonOrchsState{host: host, hostName: row.name}
	m.screen = screenDaemonOrchs
	m.orch.loading = true
	return m, m.orchLoadCmd()
}

// backToDaemonOrchs leaves the detail for its daemon's Orchestrators list.
func (m Model) backToDaemonOrchs() (tea.Model, tea.Cmd) {
	m.screen = screenDaemonOrchs
	m.orch.loading = true
	return m, m.orchLoadCmd()
}

// daemonOrchRows are the orchestrator rows of the open daemon.
func (m Model) daemonOrchRows() []orchRow {
	var out []orchRow
	for _, r := range m.orch.rows {
		if r.host == m.daemonOrchs.host {
			out = append(out, r)
		}
	}
	return out
}

func (m Model) daemonOrchsHelp() helpBindings {
	if m.orch.form != nil {
		verb := "create"
		if m.orch.formKind == orchFormRename {
			verb = "rename"
		}
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", verb), hkey("esc", "cancel")}
	}
	return helpBindings{
		hkey("↑/↓", "move"),
		hkey("n", "new"),
		hkey("R", "rename"),
		hkey("d", "remove"),
		hkey("enter", "open"),
		hkey("r", "reload"),
		hkey("esc", "daemons"),
		m.keys.Quit,
	}
}

func (m Model) updateDaemonOrchsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.orch.form != nil {
		switch msg.String() {
		case "esc":
			m.orch.form, m.orch.vals, m.orch.formKind = nil, nil, orchFormNone
			return m, nil
		case "ctrl+s":
			return m.applyOrchForm()
		}
		return m.advanceOrchForm(msg)
	}
	switch msg.String() {
	case "esc":
		return m.enterDaemons()
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "r":
		m.orch.loading = true
		return m, m.orchLoadCmd()
	case "up", "k":
		if m.orch.cursor > 0 {
			m.orch.cursor--
		}
	case "down", "j":
		if m.orch.cursor < len(m.daemonOrchRows())-1 {
			m.orch.cursor++
		}
	case "n":
		return m.openOrchCreateForm(m.daemonOrchs.host, m.daemonOrchs.hostName)
	case "R", "d":
		row, ok := m.orchCurrent()
		if !ok {
			return m, nil
		}
		if !row.reachable {
			m.status = row.summary.GetOrchestrator().GetName() + " is unreachable — " + row.reason
			return m, nil
		}
		if msg.String() == "R" {
			return m.openOrchRenameForm(row)
		}
		return m.confirmOrchRemove(row)
	case "enter":
		if row, ok := m.orchCurrent(); ok {
			return m.enterOrchDetail(row)
		}
	}
	return m, nil
}

func (m Model) viewDaemonOrchs() string {
	if m.orch.form != nil {
		title := "New orchestrator on " + m.daemonOrchs.hostName
		if m.orch.formKind == orchFormRename {
			title = "Rename orchestrator"
		}
		return lipgloss.JoinVertical(lipgloss.Left, sectionStyle.Render(title), "", m.orch.form.View())
	}
	rows := m.daemonOrchRows()
	lines := []string{sectionStyle.Render("Orchestrators") + dimStyle.Render("  on "+m.daemonOrchs.hostName), ""}
	switch {
	case m.orch.loading && !m.orch.loaded:
		lines = append(lines, m.spinner.View()+dimStyle.Render(" loading…"))
	case len(rows) == 0:
		lines = append(lines, dimStyle.Render("(no orchestrators on "+m.daemonOrchs.hostName+" — n creates one)"))
	}
	for i, r := range rows {
		lines = append(lines, cursorMark(i == m.orch.cursor)+orchRowLine(r))
	}
	if m.orch.loading && m.orch.loaded {
		lines = append(lines, "", m.spinner.View()+dimStyle.Render(" refreshing…"))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
