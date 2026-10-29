package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// lanes is the lane step (SWI-18, FR-306a) for the chosen row, once an
	// orchestrator with lanes is chosen.
	lanes *lanePicker
	row   orchRow
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
	if m.attach.lanes != nil {
		return helpBindings{hkey("space", "toggle"), hkey("enter", "attach"), hkey("esc", "cancel")}
	}
	return helpBindings{hkey("↑/↓", "move"), hkey("enter", "attach"), hkey("esc", "cancel")}
}

func (m Model) updateAttachPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a := &m.attach
	if a.lanes != nil {
		switch a.lanes.update(msg) {
		case pickClose:
			m.screen = screenList
			a.lanes = nil
		case pickSave:
			ids := a.lanes.selected()
			m.screen = screenList
			a.lanes = nil
			return m.attachTo(a.row, a.sandbox, a.host, ids)
		}
		return m, nil
	}
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
		return m.chooseAttachRow(a.choices[a.cursor])
	}
	return m, nil
}

// chooseAttachRow follows choosing an orchestrator: with lanes, the lane step
// (both daemons must serve lanes, else a confirm offers the open lane); without,
// the attach.
func (m Model) chooseAttachRow(row orchRow) (tea.Model, tea.Cmd) {
	a := &m.attach
	rec := row.summary.GetOrchestrator()
	if len(rec.GetLanes()) == 0 {
		m.screen = screenList
		return m.attachTo(row, a.sandbox, a.host, nil)
	}
	old, predates := "", true
	switch {
	case !m.hostHasLanes(row.host):
		old = row.host
	case !m.hostHasLanes(a.host):
		old = a.host
	default:
		predates = false
	}
	if predates {
		// y attaches with no lanes; n leaves things as they were.
		sb, host := a.sandbox, a.host
		return m.enterConfirm(confirmState{
			title:     "The daemon on " + sanitizeLine(m.hostLabel(old), 0) + " predates lanes: attach to the open lane?",
			body:      []string{sanitizeLine(rec.GetName(), 0) + " has lanes, but lanes picked now could not reach the worker."},
			notice:    "It can be put in lanes later, once both daemons are updated (u).",
			onConfirm: func() tea.Msg { return attachOpenLaneMsg{row: row, sb: sb, host: host} },
		})
	}
	a.row = row
	a.lanes = newLanePicker(pickAttach, "Lanes for "+a.sandbox.GetDisplayName()+" on "+rec.GetName(), "", laneRows(rec), nil)
	a.lanes.header = "none ticked = open lane"
	return m, nil
}

// attachOpenLaneMsg is the y of the predates-lanes confirm.
type attachOpenLaneMsg struct {
	row  orchRow
	sb   *pb.Sandbox
	host string
}

func (m Model) applyAttachOpenLane(msg attachOpenLaneMsg) (tea.Model, tea.Cmd) {
	return m.attachTo(msg.row, msg.sb, msg.host, nil)
}

// directDetachWarning is the direct detach's open-lane line (FR-308b), from the
// client's last-known record of the worker's orchestrator: a warning when that
// shows no open-lane worker would be left for un-laned actions, a can't-check
// line when the client has no record, else "".
func (m Model) directDetachWarning(sb *pb.Sandbox) string {
	c := sb.GetController()
	var rec *pb.Orchestrator
	for _, r := range m.orch.rows {
		if o := r.summary.GetOrchestrator(); o.GetId() == c.GetOrchestratorId() {
			rec = o
		}
	}
	if rec == nil {
		return sanitizeLine("Cannot check open-lane workers: "+c.GetName()+"'s daemon is not connected", 0)
	}
	for _, w := range rec.GetWorkers() {
		if w.GetSandboxId() != sb.GetId() && !w.GetReasoning() && len(w.GetLaneIds()) == 0 && !w.GetLanesPending() {
			return ""
		}
	}
	var unlaned []string
	for _, a := range rec.GetActions() {
		if a.GetType() == pb.ActionType_ACTION_TYPE_PROMPT && len(a.GetLaneIds()) == 0 {
			unlaned = append(unlaned, a.GetName())
		}
	}
	if len(unlaned) == 0 {
		return ""
	}
	return "Warning: this may leave no open-lane worker for: " + capNames(unlaned)
}

// attachLaneErrMsg is an attach refused for a lane that no longer exists
// (FR-306a): the lane step reopens on the fresh record with the message.
type attachLaneErrMsg struct {
	err     error
	row     orchRow
	sb      *pb.Sandbox
	host    string
	laneIDs []string
}

func (m Model) applyAttachLaneErr(msg attachLaneErrMsg) (tea.Model, tea.Cmd) {
	delete(m.busy, msg.sb.GetId())
	m.refreshListItems()
	m.status = "error: " + msg.err.Error()
	if m.screen != screenList {
		// The user moved on: the refusal is on the footer only; the picker
		// reopens only where the attach was started.
		return m, nil
	}
	rec := msg.row.summary.GetOrchestrator()
	m.attach = attachState{sandbox: msg.sb, host: msg.host, choices: m.reachableOrchestrators(), row: msg.row}
	var kept []string
	for _, id := range msg.laneIDs {
		if laneByID(rec, id) != nil {
			kept = append(kept, id)
		}
	}
	m.attach.lanes = newLanePicker(pickAttach, "Lanes for "+msg.sb.GetDisplayName()+" on "+rec.GetName(), "", laneRows(rec), kept)
	m.attach.lanes.header = "none ticked = open lane"
	m.attach.lanes.status = msg.err.Error()
	m.screen = screenAttachPick
	return m, nil
}

// attachTo attaches a sandbox to an orchestrator in laneIDs, offering to add
// the sandbox's machine to the orchestrator first when it has no entry for it.
func (m Model) attachTo(row orchRow, sb *pb.Sandbox, host string, laneIDs []string) (tea.Model, tea.Cmd) {
	o := row.summary.GetOrchestrator()
	sbHost := m.daemonHostID(host)
	od, err := m.connectedDaemon(row.host)
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	attachCmd := m.attachWorkerCmd(od, row, sbHost, sb, host, laneIDs)
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

// attachWorkerCmd sends AttachWorker to the orchestrator's daemon. A lane
// gone meanwhile (NotFound) re-reads the orchestrator for the lane step.
func (m Model) attachWorkerCmd(od Daemon, row orchRow, sbHost string, sb *pb.Sandbox, host string, laneIDs []string) tea.Cmd {
	o := row.summary.GetOrchestrator()
	orchID, orchName := o.GetId(), o.GetName()
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := od.AttachWorker(ctx, orchID, sbHost, sb.GetId(), laneIDs); err != nil {
			if len(laneIDs) > 0 && status.Code(err) == codes.NotFound {
				if det, gerr := od.GetOrchestrator(ctx, orchID); gerr == nil {
					fresh := row
					fresh.summary = det.GetSummary()
					return attachLaneErrMsg{err: err, row: fresh, sb: sb, host: host, laneIDs: laneIDs}
				}
			}
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
	if connected && m.hostHasLanes(orchEntry) {
		// Guarded like x on the Workers view (FR-308a).
		od := m.daemonForHost(orchEntry)
		t := orchTarget{host: orchEntry, hostName: m.hostDisplayName(orchEntry), id: c.GetOrchestratorId(), name: c.GetName()}
		w := laneWrite{sink: surfaceDetail, target: t, list: true, busyID: sb.GetId(), returnTo: screenList, returnSet: true,
			done: fmt.Sprintf("detached %s from %s", sb.GetDisplayName(), c.GetName()),
			check: func(ctx context.Context) (*pb.LaneChangeImpact, error) {
				return od.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: t.id, Change: &pb.CheckLaneChangeRequest_DetachWorker{DetachWorker: &pb.DetachWorkerRequest{SandboxId: sb.GetId()}}})
			},
			save: func(ctx context.Context, g *pb.LaneGuardConfirm) error {
				return od.DetachWorker(ctx, t.id, sb.GetId(), g)
			},
			confirm: func(imp *pb.LaneChangeImpact) confirmState {
				st := detachConfirm(sb.GetDisplayName(), c.GetName(), imp)
				st.body = append(st.body[:1], append([]string{"Its gateway registration and briefing are removed."}, st.body[1:]...)...)
				return st
			},
		}
		return m, laneWriteCmd(w, nil, true)
	}
	if connected {
		od := m.daemonForHost(orchEntry)
		cmd = func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if err := od.DetachWorker(ctx, c.GetOrchestratorId(), sb.GetId(), nil); err != nil {
				return errMsg{err}
			}
			return statusMsg(fmt.Sprintf("detached %s from %s", sb.GetDisplayName(), c.GetName()))
		}
	} else {
		body = append(body, sanitizeLine(fmt.Sprintf("%s's machine is not connected: the sandbox's own daemon clears it;", c.GetName()), 0), "the orchestrator notices when it next reaches this machine.")
		if w := m.directDetachWarning(sb); w != "" {
			body = append(body, w)
		}
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
	if a.lanes != nil {
		// The modal's border and padding take 4 rows.
		return modalStyle.Width(m.modalInnerWidth()).Render(a.lanes.view(m.modalInnerWidth(), m.height-4))
	}
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
		return m.detachThroughOrch(surfaceDetail, m.detailTarget(), od, w.GetSandboxId(), w.GetDisplayName(), screenOrchDetail)
	case "m":
		return m.markFreeFromView(w)
	case "l":
		if !m.hostHasLanes(d.host) {
			d.status = m.tooOldForLanes(d.host)
			return m, nil
		}
		if w.GetReasoning() {
			d.status = "reasoning sandbox — no lanes"
			return m, nil
		}
		m.openWorkerPicker(surfaceDetail, w)
		return m, nil
	case "t":
		return m.workerTerminal(w)
	}
	return m, nil
}

// detachConfirm is the detach confirm (FR-132), with the open-lane guard's
// line when it fires (FR-308a).
func detachConfirm(sbName, orchName string, imp *pb.LaneChangeImpact) confirmState {
	body := []string{"The sandbox keeps running; only the controller is cleared."}
	if g := guardText(imp); g != "" {
		body = append(body, sanitizeLine("No open-lane worker will be left to run: "+g, 0))
	}
	return confirmState{
		title:   fmt.Sprintf("Detach %s from %s?", sbName, orchName),
		body:    body,
		warning: "A run in flight on it blocks the detach.",
	}
}

// detachThroughOrch detaches a worker through its orchestrator: against a
// daemon with lanes CheckLaneChange runs first and its list is folded into the
// confirm (FR-308a); otherwise the confirm opens as before. back is the screen
// the confirm returns to.
func (m Model) detachThroughOrch(s orchSurface, t orchTarget, od Daemon, sbID, sbName string, back screen) (tea.Model, tea.Cmd) {
	if sbName == "" {
		sbName = short(sbID)
	}
	w := laneWrite{sink: s, target: t, done: "detached " + sbName, returnTo: back, returnSet: true,
		save: func(ctx context.Context, g *pb.LaneGuardConfirm) error {
			return od.DetachWorker(ctx, t.id, sbID, g)
		},
		confirm: func(imp *pb.LaneChangeImpact) confirmState { return detachConfirm(sbName, t.name, imp) },
	}
	if !od.HasCapability("lanes") {
		st := detachConfirm(sbName, t.name, nil)
		st.returnTo, st.onConfirm = back, laneWriteCmd(w, nil, false)
		return m.enterConfirm(st)
	}
	w.check = func(ctx context.Context) (*pb.LaneChangeImpact, error) {
		return od.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{OrchestratorId: t.id, Change: &pb.CheckLaneChangeRequest_DetachWorker{DetachWorker: &pb.DetachWorkerRequest{SandboxId: sbID}}})
	}
	return m, laneWriteCmd(w, nil, true)
}

// laneCell is a worker's lane cell (FR-309): its lanes, the open lane, lanes
// pending, or detaching.
func laneCell(w *pb.WorkerView, rec *pb.Orchestrator) string {
	switch {
	case w.GetDetaching():
		return "detaching…"
	case w.GetLanesPending():
		return "⚠ lanes pending"
	case len(w.GetLaneIds()) > 0:
		return "lanes: " + strings.Join(laneNames(rec, w.GetLaneIds()), ", ")
	}
	return "open lane"
}

// workerTerminal is `t` in the Workers view (SWI-17): the worker's terminal,
// the way out of a hold, through the same detached-agent gate as everywhere
// (Replace / Wait, or Restart sandbox / Wait). Detaching returns to the view.
func (m Model) workerTerminal(w *pb.WorkerView) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	name := w.GetDisplayName()
	if name == "" {
		name = short(w.GetSandboxId())
	}
	sb, host, why := m.runSandbox(&pb.Run{SandboxId: w.GetSandboxId(), SandboxHostId: w.GetHostId()})
	if why != "" {
		d.status = "worker sandbox " + sanitizeLine(name, 0) + " " + why
		return m, nil
	}
	od, err := m.connectedDaemon(host)
	if err != nil {
		d.status = "worker sandbox " + sanitizeLine(name, 0) + ": " + err.Error()
		return m, nil
	}
	d.status = ""
	return m.enterTerminalFrom(sb, host, od, screenOrchDetail)
}

// renderWorkersView lists the roster: name · @host · state · availability
// (run action); reasoning rows are marked (FR-132).
func (m Model) renderWorkersView(cursor func(int) string) []string {
	ws := m.orchDetail.detail.GetWorkers()
	if len(ws) == 0 {
		return orchEmpty("workers")
	}
	rec, lanes := m.orchRec(), m.hostHasLanes(m.orchDetail.host)
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
		// SWI-17 (FR-213): the hold or halt, as the sandbox list words it.
		if note, surfaced := holdNote(w.GetHold(), w.GetHalted(), w.GetStepNote(), w.GetRunId()); note != "" {
			style := dimStyle
			if surfaced {
				style = agentWaitStyle
			}
			line += "  " + style.Render("⚑ "+note)
		}
		// SWI-17 (R14): a detached agent, or a check for one, as the list shows it.
		if sb, _, why := m.runSandbox(&pb.Run{SandboxId: w.GetSandboxId(), SandboxHostId: w.GetHostId()}); why == "" {
			if note, surfaced := orphanNote(sb); note != "" {
				style := dimStyle
				if surfaced {
					style = statusErrStyle
				}
				line += "  " + style.Render(note)
			}
		}
		if w.GetReasoning() {
			line += "  " + tagBadgeStyle.Render("reasoning")
		}
		if !w.GetHostReachable() {
			line += "  " + statusErrStyle.Render("host unreachable")
		}
		if lanes && !w.GetReasoning() {
			if room := m.bodyWidth() - ansi.StringWidth(line) - 2; room > 0 {
				line += "  " + dimStyle.Render(fitCol(laneCell(w, rec), room))
			}
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
