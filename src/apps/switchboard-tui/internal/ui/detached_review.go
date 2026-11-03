package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The start-up "agents detached" notice (SWI-25 A2). After a daemon restart
// that finds several detached agents, their daemon raises one host-level
// AGENTS_DETACHED_AT_START notification listing them with their orchestrators,
// instead of one AGENT_DETACHED each. It has no sandbox row of its own: enter
// on it opens the orchestrator's Workers view, where each worker's row carries
// its note and `R` replaces every idle detached one; when the workers span
// several orchestrators a picker comes first; plain sandboxes alone go to the
// list.

// detachedPickState is the orchestrator picker enter opens on such a notice.
type detachedPickState struct {
	n       *pb.NotificationEvent
	choices []detachedOrch
	cursor  int
}

// detachedOrch is one orchestrator a notice's workers belong to.
type detachedOrch struct {
	id, name, hostID string
	count            int // its affected workers
}

// isHostNotice reports a notification with no sandbox row of its own.
func isHostNotice(n *pb.NotificationEvent) bool {
	return n.GetKind() == pb.NotificationKind_NOTIFICATION_KIND_AGENTS_DETACHED_AT_START
}

// detachedOrchestrators lists the notice's orchestrators, in order of first
// appearance, each with how many of the affected workers it controls.
func detachedOrchestrators(n *pb.NotificationEvent) []detachedOrch {
	var out []detachedOrch
	for _, a := range n.GetAffected() {
		if a.GetOrchestratorId() == "" {
			continue
		}
		found := false
		for i := range out {
			if out[i].id == a.GetOrchestratorId() && out[i].hostID == a.GetOrchestratorHostId() {
				out[i].count++
				found = true
				break
			}
		}
		if !found {
			out = append(out, detachedOrch{id: a.GetOrchestratorId(), name: a.GetOrchestratorName(), hostID: a.GetOrchestratorHostId(), count: 1})
		}
	}
	return out
}

// reviewDetached is enter on the notice: the Workers view of the one
// orchestrator, a picker over several, or the list on the first sandbox when
// none of the affected is a worker.
func (m Model) reviewDetached(n *pb.NotificationEvent) (tea.Model, tea.Cmd) {
	orchs := detachedOrchestrators(n)
	switch len(orchs) {
	case 0:
		if a := n.GetAffected(); len(a) > 0 {
			selectByID(&m.list, a[0].GetSandboxId())
		}
		m.screen = screenList
		return m, nil
	case 1:
		return m.openDetachedWorkers(orchs[0])
	}
	m.detachedPick = detachedPickState{n: n, choices: orchs}
	m.screen = screenDetachedPick
	return m, nil
}

// openDetachedWorkers opens an orchestrator's detail on its Workers view. The
// notice names the orchestrator's daemon by host id; the detail needs the TUI
// host entry that is connected to it.
func (m Model) openDetachedWorkers(o detachedOrch) (tea.Model, tea.Cmd) {
	entry, ok := m.entryForDaemonHost(o.hostID)
	if !ok && m.daemon.HostID() == o.hostID {
		entry, ok = m.activeHost, true // the adopted active host has no connection object (runSandbox does the same)
	}
	if !ok {
		m.status = "orchestrator " + sanitizeLine(o.name, 0) + "'s daemon is not connected"
		m.screen = screenNotifications
		return m, nil
	}
	m.orchDetail.view = viewWorkers
	row := orchRow{host: entry, hostName: m.hostDisplayName(entry), reachable: true,
		summary: &pb.OrchestratorSummary{Orchestrator: &pb.Orchestrator{Id: o.id, Name: o.name, HostId: o.hostID}}}
	return m.enterOrchDetail(row)
}

func (m Model) detachedPickHelp() helpBindings {
	return helpBindings{hkey("↑/↓", "move"), hkey("enter", "open workers"), hkey("esc", "back")}
}

func (m Model) updateDetachedPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.detachedPick
	switch msg.String() {
	case "esc", "q":
		m.screen = screenNotifications
		return m, nil
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(p.choices)-1 {
			p.cursor++
		}
	case "enter":
		if len(p.choices) == 0 {
			return m, nil
		}
		return m.openDetachedWorkers(p.choices[p.cursor])
	}
	return m, nil
}

// detachedPickModal lists the notice's orchestrators with their worker counts.
func (m Model) detachedPickModal() string {
	p := m.detachedPick
	rows := []string{sectionStyle.Render(sanitizeLine(p.n.GetMessage(), 60)), dimStyle.Render("Their workers span several orchestrators. Review which?"), ""}
	for i, o := range p.choices {
		label := fitCol(sanitizeLine(o.name, 24), 24) + dimStyle.Render("  "+plural(o.count, "worker", "workers"))
		if i == p.cursor {
			rows = append(rows, cursorBarStyle.Render("> ")+selectedStyle.Render(label))
		} else {
			rows = append(rows, "  "+label)
		}
	}
	rows = append(rows, "", helpStyle.Render("↑/↓ move · enter open workers · esc back"))
	return modalStyle.Width(m.modalInnerWidth()).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
