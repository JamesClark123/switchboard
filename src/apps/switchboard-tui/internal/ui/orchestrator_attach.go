package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// `o` on the sandbox list (feature 009, FR-107/FR-111/FR-112): on an
// unattached sandbox a picker over every reachable orchestrator the TUI knows;
// choosing one whose host entries lack the sandbox's machine first offers to
// copy the TUI's own host entry onto the orchestrator (FR-106), then attaches
// through the orchestrator's daemon. On a worker, a detach confirm that goes
// through the orchestrator's daemon — or, when that daemon is not connected,
// straight to the sandbox's own daemon (DetachController). The reasoning
// sandbox refuses. Every daemon refusal is shown verbatim.

// attachState backs the picker.
type attachState struct {
	sandbox *pb.Sandbox
	host    string // TUI host entry id of the sandbox's daemon
	choices []orchRow
	cursor  int
}

// orchestratorKey dispatches `o` on a sandbox row.
func (m Model) orchestratorKey(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	if c := sb.GetController(); c != nil {
		if c.GetReasoning() {
			m.status = "reasoning sandbox — not detachable"
			return m, nil
		}
		return m.confirmDetach(sb, host)
	}
	m.attach = attachState{sandbox: sb, host: host, choices: m.reachableOrchestrators()}
	m.screen = screenAttachPick
	// Refresh the rows underneath so a just-created orchestrator shows up.
	m.orch.loading = true
	return m, m.orchLoadCmd()
}

// reachableOrchestrators lists the rows a sandbox can be attached to.
func (m Model) reachableOrchestrators() []orchRow {
	var out []orchRow
	for _, r := range m.orch.rows {
		if r.reachable {
			out = append(out, r)
		}
	}
	return out
}

// daemonHostID is the advertised host id of a TUI host entry's daemon (the id
// orchestrators key hosts and workers by), falling back to the entry id.
func (m Model) daemonHostID(entryID string) string {
	if entryID == "" {
		return m.daemon.HostID()
	}
	if m.manager != nil {
		if hc, ok := m.manager.Get(entryID); ok && hc.State == client.HostConnected && hc.Conn != nil {
			return hc.Conn.HostID()
		}
	}
	return m.daemonForHost(entryID).HostID()
}

// entryForDaemonHost finds the connected TUI host entry whose daemon advertises
// hostID; ok is false when no connected daemon has it. Without a host manager
// the lone active daemon is the only candidate.
func (m Model) entryForDaemonHost(hostID string) (string, bool) {
	if m.manager == nil {
		return m.activeHost, m.daemon.HostID() == hostID
	}
	for _, hc := range m.manager.List() {
		if hc.State == client.HostConnected && hc.Conn != nil && hc.Conn.HostID() == hostID {
			return hc.Entry.ID, true
		}
	}
	return "", false
}

func (m Model) attachPickHelp() helpBindings {
	return helpBindings{hkey("↑/↓", "move"), hkey("enter", "attach"), hkey("esc", "cancel")}
}

func (m Model) updateAttachPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := &m.attach
	switch msg.String() {
	case "esc", "q":
		m.screen = screenList
		return m, nil
	case "up", "k":
		if a.cursor > 0 {
			a.cursor--
		}
	case "down", "j":
		if a.cursor < len(a.choices)-1 {
			a.cursor++
		}
	case "enter":
		if len(a.choices) == 0 {
			return m, nil
		}
		m.screen = screenList
		return m.attachTo(a.choices[a.cursor], a.sandbox, a.host)
	}
	return m, nil
}

// attachTo attaches a sandbox to an orchestrator, offering to add the
// sandbox's machine to the orchestrator first when it has no entry for it.
func (m Model) attachTo(row orchRow, sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	o := row.summary.GetOrchestrator()
	sbHost := m.daemonHostID(host)
	od, err := m.connectedDaemon(row.host)
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	attachCmd := m.attachWorkerCmd(od, o.GetId(), o.GetName(), sbHost, sb)
	if sbHost == o.GetHostId() {
		return m.startBusy(sb.GetId(), "attaching"), attachCmd
	}
	for _, h := range o.GetHosts() {
		if h.GetHostId() == sbHost {
			return m.startBusy(sb.GetId(), "attaching"), attachCmd
		}
	}
	// The orchestrator cannot reach the sandbox's machine yet: offer to copy the
	// TUI's own entry (never a password).
	known, ok := m.knownHostEntry(host)
	if !ok {
		m.status = fmt.Sprintf("%s has no host entry for %s and the TUI has no saved ssh host to copy", o.GetName(), m.hostDisplayName(host))
		return m, nil
	}
	hosts := append([]*pb.OrchestratorHost(nil), o.GetHosts()...)
	entry := knownHostEntry(known)
	entry.HostId = sbHost
	hosts = append(hosts, entry)
	id, name := o.GetId(), o.GetName()
	return m.enterConfirm(confirmState{
		title:     fmt.Sprintf("Add host %s to %s?", known.DisplayName, name),
		body:      []string{sanitizeLine(fmt.Sprintf("%s has no entry for %s.", name, known.DisplayName), 0), "The orchestrator's machine will dial it over SSH (" + sanitizeLine(known.SSHTarget, 0) + ")", "with keys or an agent, then attach the sandbox."},
		warning:   "The orchestrator's machine must be able to ssh there non-interactively.",
		sandboxID: sb.GetId(),
		verb:      "attaching",
		onConfirm: func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := od.SetOrchestratorHosts(ctx, id, hosts); err != nil {
				return errMsg{err}
			}
			return attachCmd()
		},
	})
}

// knownHostEntry finds the TUI's saved ssh host for an entry id.
func (m Model) knownHostEntry(entryID string) (store.KnownHost, bool) {
	if m.hostStore == nil {
		return store.KnownHost{}, false
	}
	h, err := m.hostStore.Get(entryID)
	if err != nil || h.Kind != "ssh" {
		return store.KnownHost{}, false
	}
	return *h, true
}

// attachWorkerCmd sends AttachWorker to the orchestrator's daemon.
func (m Model) attachWorkerCmd(od Daemon, orchID, orchName, sbHost string, sb *pb.Sandbox) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := od.AttachWorker(ctx, orchID, sbHost, sb.GetId()); err != nil {
			return errMsg{err}
		}
		return statusMsg(fmt.Sprintf("attached %s to %s", sb.GetDisplayName(), orchName))
	}
}

// confirmDetach gates a worker's detach; the orchestrator's daemon detaches when
// connected, the sandbox's own daemon otherwise (FR-112).
func (m Model) confirmDetach(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	c := sb.GetController()
	orchEntry, connected := m.entryForDaemonHost(c.GetHostId())
	var cmd tea.Cmd
	body := []string{"The sandbox keeps running; only the controller is cleared.", "Its gateway registration and briefing are removed."}
	if connected {
		od := m.daemonForHost(orchEntry)
		cmd = func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if err := od.DetachWorker(ctx, c.GetOrchestratorId(), sb.GetId()); err != nil {
				return errMsg{err}
			}
			return statusMsg(fmt.Sprintf("detached %s from %s", sb.GetDisplayName(), c.GetName()))
		}
	} else {
		body = append(body, sanitizeLine(fmt.Sprintf("%s's machine is not connected: the sandbox's own daemon clears it;", c.GetName()), 0), "the orchestrator notices when it next reaches this machine.")
		sd := m.daemonForHost(host)
		cmd = func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := sd.DetachController(ctx, sb.GetId(), c.GetOrchestratorId(), false); err != nil {
				return errMsg{err}
			}
			return statusMsg(fmt.Sprintf("detached %s from %s (directly)", sb.GetDisplayName(), c.GetName()))
		}
	}
	return m.enterConfirm(confirmState{
		title:     fmt.Sprintf("Detach %s from %s?", sb.GetDisplayName(), c.GetName()),
		body:      body,
		warning:   "A run in flight on it blocks the detach.",
		sandboxID: sb.GetId(),
		verb:      "detaching",
		onConfirm: cmd,
	})
}

func (m Model) attachModal() string {
	a := m.attach
	rows := []string{sectionStyle.Render("Attach " + sanitizeLine(a.sandbox.GetDisplayName(), 32) + " to an orchestrator"), ""}
	if len(a.choices) == 0 {
		rows = append(rows, dimStyle.Render("(no reachable orchestrator)"), dimStyle.Render("create one under Daemons › <daemon> › Orchestrators"))
	}
	sbHost := m.daemonHostID(a.host)
	for i, r := range a.choices {
		o := r.summary.GetOrchestrator()
		label := fitCol(o.GetName(), 24) + dimStyle.Render("  @"+sanitizeLine(r.hostName, 24))
		if o.GetHostId() != sbHost && !orchHasHost(o, sbHost) {
			label += dimStyle.Render("  (will add this host)")
		}
		if i == a.cursor {
			rows = append(rows, cursorBarStyle.Render("> ")+selectedStyle.Render(label))
		} else {
			rows = append(rows, "  "+label)
		}
	}
	rows = append(rows, "", helpStyle.Render("↑/↓ move · enter attach · esc cancel"))
	return modalStyle.Width(m.modalInnerWidth()).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

func orchHasHost(o *pb.Orchestrator, hostID string) bool {
	for _, h := range o.GetHosts() {
		if h.GetHostId() == hostID {
			return true
		}
	}
	return false
}

// --- the Workers view's detach (`x`, FR-132) ---

func (m Model) updateWorkersViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	ws := d.detail.GetWorkers()
	if len(ws) == 0 {
		return m, nil
	}
	w := ws[d.cursors[viewWorkers]]
	switch msg.String() {
	case "x":
		if w.GetReasoning() {
			d.status = "reasoning sandbox — not detachable"
			return m, nil
		}
		od, err := m.connectedDaemon(d.host)
		if err != nil {
			d.status = err.Error()
			return m, nil
		}
		id, name, sbID, sbName := d.id, d.name, w.GetSandboxId(), w.GetDisplayName()
		return m.enterConfirm(confirmState{
			title:    fmt.Sprintf("Detach %s from %s?", sbName, name),
			body:     []string{"The sandbox keeps running; only the controller is cleared."},
			warning:  "A run in flight on it blocks the detach.",
			returnTo: screenOrchDetail,
			onConfirm: func() tea.Msg {
				ctx, cancel := ctxTimeout()
				defer cancel()
				if err := od.DetachWorker(ctx, id, sbID); err != nil {
					return orchDoneMsg{err: err}
				}
				return orchDoneMsg{status: "detached " + sbName, reload: true}
			},
		})
	case "m":
		return m.markFreeFromView(w)
	}
	return m, nil
}

// renderWorkersView lists the roster: name · @host · state · availability
// (run action); reasoning rows are marked (FR-132).
func (m Model) renderWorkersView(cursor func(int) string) []string {
	ws := m.orchDetail.detail.GetWorkers()
	if len(ws) == 0 {
		return orchEmpty("workers")
	}
	var lines []string
	for i, w := range ws {
		name := w.GetDisplayName()
		if name == "" {
			name = short(w.GetSandboxId())
		}
		// Remote text is sanitised and fitted like the Jobs rows (FR-029; review
		// run-242 finding 4).
		line := cursor(i) + fitCol(name, 24) + dimStyle.Render("  @"+fitCol(w.GetHostId(), 16)) + "  " + stateBadge(w.GetState()) + " " + availabilityBadge(w.GetAvailability(), w.GetAgentStatus())
		if w.GetRunAction() != "" {
			line += dimStyle.Render("  (" + sanitizeLine(w.GetRunAction(), 40) + ")")
		}
		if w.GetReasoning() {
			line += "  " + tagBadgeStyle.Render("reasoning")
		}
		if !w.GetHostReachable() {
			line += "  " + statusErrStyle.Render("host unreachable")
		}
		lines = append(lines, m.fitLine(line))
	}
	return lines
}

// availabilityBadge renders a worker's derived availability. A worker busy
// only because its agent is still starting (SWI-15) says so, as the sandbox
// list's badge does, rather than reading as taken by something else.
func availabilityBadge(a pb.WorkerAvailability, agent pb.AgentStatus) string {
	switch a {
	case pb.WorkerAvailability_WORKER_AVAILABILITY_FREE:
		return agentIdleStyle.Render("free")
	case pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY:
		if agent == pb.AgentStatus_AGENT_STATUS_STARTING {
			return agentWorkStyle.Render("starting…")
		}
		return agentWorkStyle.Render("busy")
	case pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT:
		return agentWaitStyle.Render("needs input")
	case pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW:
		return agentWaitStyle.Render("awaiting review")
	case pb.WorkerAvailability_WORKER_AVAILABILITY_NOT_RUNNING:
		return dimStyle.Render("not running")
	case pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE:
		return statusErrStyle.Render("unreachable")
	}
	return dimStyle.Render("?")
}
