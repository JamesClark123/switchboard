package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Operations on one orchestrator's runs (feature 010, research R1): show a run's
// composed prompt, cancel a queued run, mark a reviewed worker free and open a
// finished run's results. Each takes an explicit orchTarget — the Jobs tabs build
// it from the highlighted row, the orchestrator detail from itself — and reports
// on the surface it was started from, so one implementation serves every screen
// and today's behaviour carries over unchanged (FR-010, FR-027).

// orchTarget is the orchestrator an operation acts on (data-model "orchTarget").
// Identity is (host, id); the label names it to the developer.
type orchTarget struct {
	host     string // host entry id of the daemon hosting it
	hostName string // that daemon's display name
	id, name string
	rec      *pb.Orchestrator // the record as last loaded (actions, timed events)
}

// targetOf builds the target for an orchestrator record hosted on host.
func targetOf(host, hostName string, o *pb.Orchestrator) orchTarget {
	return orchTarget{host: host, hostName: hostName, id: o.GetId(), name: o.GetName(), rec: o}
}

// label is how rows and headings name the orchestrator: `name @ host` (FR-003).
func (t orchTarget) label() string { return t.name + " @ " + t.hostName }

// key is the target's identity as a map key (the same id on two hosts differs).
func (t orchTarget) key() string { return t.host + "/" + t.id }

// connectedDaemon is the daemon an orchestrator operation on host must go to
// (FR-030, research R12): that host's connection, or a refusal naming it when it
// is not connected. Unlike daemonForHost it never falls back to the active
// daemon, which would send the operation to a different machine.
func (m Model) connectedDaemon(host string) (Daemon, error) {
	if m.manager == nil {
		return m.daemon, nil
	}
	hc, ok := m.manager.Get(host)
	d := m.connOf(hc)
	if !ok || d == nil {
		return nil, fmt.Errorf("%s is not connected", m.hostDisplayName(host))
	}
	if hs := m.jobs.hosts[host]; hs != nil && hs.lost != nil {
		// Still "connected" to the client, but its link died (R3).
		return nil, fmt.Errorf("%s: connection lost — r on Jobs retries", m.hostDisplayName(host))
	}
	return d, nil
}

// requireDaemon is connectedDaemon for an operation started on a surface: a
// refusal goes to the surface's line and the footer.
func (m *Model) requireDaemon(s orchSurface, host string) (Daemon, bool) {
	d, err := m.connectedDaemon(host)
	if err != nil {
		m.opState(s).status = err.Error()
		m.status = "error: " + err.Error()
		return nil, false
	}
	return d, true
}

// errCmd reports an error as an operation's outcome on a surface.
func errCmd(s orchSurface, err error) tea.Cmd {
	return func() tea.Msg { return orchDoneMsg{err: err, sink: s} }
}

// orchSurface is the screen an orchestrator operation was started from: where its
// form opens, its refusals land and its confirm dialog returns to.
type orchSurface int

const (
	surfaceDetail orchSurface = iota // the orchestrator detail under Daemons › Orchestrators
	surfaceJobs                      // the Jobs screen
)

// surfaceScreen is the screen a surface lives on.
func surfaceScreen(s orchSurface) screen {
	if s == surfaceJobs {
		return screenJobs
	}
	return screenOrchDetail
}

// orchOpState is what a surface carries for the operations started from it: an
// open form and the target it acts on, the surface's own status line, the
// read-only prompt overlay, and the Enqueue now repeat window and hint.
type orchOpState struct {
	form       *huh.Form
	formKind   string
	formVals   any
	formTarget orchTarget
	// status is the surface's own line (refusals, transient hints); the footer
	// keeps the outcome of the last mutation.
	status string
	// prompt is the run whose composed prompt the read-only overlay shows.
	prompt *pb.Run
	// lastFireAt / lastFireKey are when, and for which orchestrator/event, the last
	// SUCCESSFUL Enqueue now started (the repeat debounce); hintKey is the
	// orchestrator the repeat hint was shown for, so only its timer clears it.
	lastFireAt  time.Time
	lastFireKey string
	hintKey     string
}

// opState is the operation state of a surface.
func (m *Model) opState(s orchSurface) *orchOpState {
	if s == surfaceJobs {
		return &m.jobs.orchOpState
	}
	return &m.orchDetail.orchOpState
}

// onSurface reports whether the surface's screen is the one showing.
func (m Model) onSurface(s orchSurface) bool { return m.screen == surfaceScreen(s) }

// setOpForm installs an open form on a surface, bound to the target it acts on.
func (m *Model) setOpForm(s orchSurface, t orchTarget, kind string, vals any, form *huh.Form) tea.Cmd {
	op := m.opState(s)
	op.form, op.formKind, op.formVals, op.formTarget = form, kind, vals, t
	return form.Init()
}

// closeOpForm drops a surface's open form.
func (m *Model) closeOpForm(s orchSurface) {
	op := m.opState(s)
	op.form, op.formVals, op.formKind, op.formTarget = nil, nil, "", orchTarget{}
}

// newOrchForm builds a huh form in the package's form style (the 008 pattern:
// heap-allocated values, ctrl+s saves, esc cancels).
func (m Model) newOrchForm(fields ...huh.Field) *huh.Form {
	return huh.NewForm(huh.NewGroup(fields...)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
}

// runPromptMsg carries a run fetched for the prompt overlay of a surface.
type runPromptMsg struct {
	run  *pb.Run
	sink orchSurface
}

// runState is a run's state in lower-case words ("awaiting review").
func runState(r *pb.Run) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(r.GetState().String(), "RUN_STATE_"), "_", " "))
}

// runFinished reports whether a run has ended (completed, failed or cancelled).
func runFinished(r *pb.Run) bool {
	switch r.GetState() {
	case pb.RunState_RUN_STATE_COMPLETED, pb.RunState_RUN_STATE_FAILED, pb.RunState_RUN_STATE_CANCELLED:
		return true
	}
	return false
}

// runKeyAction is a per-run key on a surface (FR-010, FR-012, FR-016): enter
// shows the composed prompt, x cancels a queued run, m marks the worker of a run
// awaiting review free, o opens a finished run's results. A key that does not
// apply to the run's state says so on the surface's line.
func (m Model) runKeyAction(s orchSurface, t orchTarget, run *pb.Run, k string) (tea.Model, tea.Cmd) {
	op := m.opState(s)
	switch k {
	case "enter":
		return m, m.runPromptCmd(s, t, run.GetId())
	case "x":
		if run.GetState() != pb.RunState_RUN_STATE_QUEUED {
			op.status = "only a queued run can be cancelled"
			return m, nil
		}
		return m.confirmCancelRun(s, t, run.GetId())
	case "m":
		if run.GetState() != pb.RunState_RUN_STATE_AWAITING_REVIEW {
			op.status = "only a run awaiting review can be marked free"
			return m, nil
		}
		return m.openMarkFreeForm(s, t, run.GetSandboxId())
	case "o":
		if !runFinished(run) {
			op.status = "open results is for finished runs"
			return m, nil
		}
		return m.openRunResults(t, run)
	}
	return m, nil
}

// runPromptCmd fetches a run (its composed prompt) for the read-only overlay.
func (m Model) runPromptCmd(s orchSurface, t orchTarget, runID string) tea.Cmd {
	od, err := m.connectedDaemon(t.host)
	if err != nil {
		return errCmd(s, err)
	}
	id := t.id
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		got, err := od.GetRun(ctx, id, runID)
		if err != nil {
			return orchDoneMsg{err: err, sink: s}
		}
		return runPromptMsg{run: got, sink: s}
	}
}

// confirmCancelRun gates cancelling a queued run on its orchestrator.
func (m Model) confirmCancelRun(s orchSurface, t orchTarget, runID string) (tea.Model, tea.Cmd) {
	od, ok := m.requireDaemon(s, t.host)
	if !ok {
		return m, nil
	}
	id := t.id
	return m.enterConfirm(confirmState{
		title:    "Cancel run " + runID + "?",
		body:     []string{"It leaves the queue of " + t.label() + "; nothing was delivered."},
		warning:  "The call is not retried.",
		returnTo: surfaceScreen(s),
		onConfirm: func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			if _, err := od.CancelRun(ctx, id, runID); err != nil {
				return orchDoneMsg{err: err, sink: s}
			}
			return orchDoneMsg{status: "cancelled " + runID, reload: true, sink: s, target: t}
		},
	})
}

// --- mark free (FR-156) ---

// openMarkFreeForm opens the outcome + note dialog for a worker's reviewed run on
// the target orchestrator.
func (m Model) openMarkFreeForm(s orchSurface, t orchTarget, sandboxID string) (tea.Model, tea.Cmd) {
	if _, ok := m.requireDaemon(s, t.host); !ok {
		return m, nil
	}
	v := &markFreeVals{outcome: "completed", sandboxID: sandboxID, host: t.host, orchID: t.id}
	form := m.newOrchForm(
		huh.NewSelect[string]().Key("outcome").Title("Outcome").Options(huh.NewOption("completed", "completed"), huh.NewOption("failed", "failed")).Value(&v.outcome),
		huh.NewInput().Key("note").Title("Note").Description("optional; recorded as the run's summary").Value(&v.note),
	)
	return m, m.setOpForm(s, t, "mark free", v, form)
}

// markFreeKey is `m` on a sandbox row: while the worker's run awaits review
// the outcome dialog is sent to the controller's daemon; a worker parked
// "needs input" with no run on it is freed outright (its agent set back to
// idle), no dialog. The dialog opens over the orchestrator detail (under
// Daemons › Orchestrators) and returns to the list (research R8).
func (m Model) markFreeKey(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	c := sb.GetController()
	parked := c.GetRun() == nil && sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	if !c.GetRun().GetNeedsReview() && !parked {
		m.status = "no run awaiting review on this sandbox"
		return m, nil
	}
	orchHost, ok := m.entryForDaemonHost(c.GetHostId())
	if !ok {
		m.status = c.GetName() + "'s machine is not connected — mark free needs the orchestrator's daemon"
		return m, nil
	}
	if parked {
		return m.freeParkedWorker(sb.GetId(), sb.GetDisplayName(), orchHost, c.GetOrchestratorId())
	}
	// Refuse before touching the detail state: a refusal leaves the list as it
	// was (review run-224 item 3).
	if _, err := m.connectedDaemon(orchHost); err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	// The dialog lives on the detail state but returns to the list.
	t := orchTarget{host: orchHost, hostName: m.hostDisplayName(orchHost), id: c.GetOrchestratorId(), name: c.GetName()}
	m.orchDetail.host = orchHost
	m.orchDetail.id = c.GetOrchestratorId()
	m.markFreeFromList = true
	m.screen = screenOrchDetail
	return m.openMarkFreeForm(surfaceDetail, t, sb.GetId())
}

// markFreeFromView is `m` in the Workers view.
func (m Model) markFreeFromView(w *pb.WorkerView) (tea.Model, tea.Cmd) {
	switch {
	case w.GetAvailability() == pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW:
		return m.openMarkFreeForm(surfaceDetail, m.detailTarget(), w.GetSandboxId())
	case w.GetAvailability() == pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT && w.GetRunId() == "":
		return m.freeParkedWorker(w.GetSandboxId(), w.GetDisplayName(), m.orchDetail.host, m.orchDetail.id)
	}
	m.orchDetail.status = "that worker has no run awaiting review"
	return m, nil
}

// freeParkedWorker asks the orchestrator's daemon to set a worker that is
// parked needs-input with no run back to idle (MarkWorkerFree with nothing to
// close); the next queued run can then go to it.
func (m Model) freeParkedWorker(sandboxID, name, host, orchID string) (tea.Model, tea.Cmd) {
	md, err := m.connectedDaemon(host)
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	if name == "" {
		name = sandboxID
	}
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := md.MarkWorkerFree(ctx, orchID, sandboxID, false, ""); err != nil {
			return orchDoneMsg{err: err}
		}
		return orchDoneMsg{status: "marked " + name + " free", reload: true}
	}
}

// promptView renders the read-only prompt overlay of a run.
func (m Model) promptView(r *pb.Run) string {
	// The composed prompt carries arguments other sandboxes supplied (FR-029).
	return sectionStyle.Render("Run "+sanitizeLine(r.GetId(), 0)) + dimStyle.Render("  "+sanitizeLine(r.GetAction(), 0)+" · "+runState(r)) + "\n\n" +
		panelStyle.Width(m.bodyWidth()-2).Render(sanitizeTerminalText(r.GetPrompt()))
}
