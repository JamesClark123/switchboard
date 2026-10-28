package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The orchestrator list (feature 009, FR-142/FR-143): every orchestrator hosted
// by any known daemon, with its machine, reachability, worker count and queue
// depths. Rows are fanned out from ListOrchestrators on every connected host
// (data-model "Client-side view models"); a host that is disconnected, too old or
// failing keeps its last-known rows marked unreachable with the reason, and only
// reading is offered on those (US1 scenario 4). Since feature 010 the rows back
// Daemons › <daemon> › Orchestrators (daemon_orchestrators.go, one daemon's rows),
// the `o` attach picker and the launch wizard's orchestrator choice; the old
// top-level Orchestrators screen is gone (FR-001).

// orchRow is one orchestrator as the screen shows it.
type orchRow struct {
	host      string // host entry id of the daemon hosting it
	hostName  string
	summary   *pb.OrchestratorSummary
	reachable bool   // the host is connected and answered
	reason    string // why not, when it is not
}

// orchFormKind says what an open form does.
type orchFormKind int

const (
	orchFormNone orchFormKind = iota
	orchFormCreate
	orchFormRename
)

// orchFormVals are the heap-allocated values a huh form binds to (the 008 pattern).
type orchFormVals struct {
	name string
	host string
}

// orchState backs the screen.
type orchState struct {
	rows    []orchRow
	cursor  int
	loading bool
	loaded  bool
	// an open create/rename form
	form     *huh.Form
	formKind orchFormKind
	vals     *orchFormVals
	formID   string // the row being renamed
	formHost string
	// formPrefill is the rename input's starting value: submitting it unedited
	// renames nothing.
	formPrefill string
	// formBuild rebuilds the open form from vals (open and reopenForm); it
	// captures only vals and plain strings. baseline is vals as it opened (SWI-9).
	formBuild func(m Model) *huh.Form
	baseline  orchFormVals
}

// orchRowsMsg carries one fan-out result.
type orchRowsMsg struct{ rows []orchRow }

// orchDoneMsg reports a completed mutation (create/rename/remove/hosts, and the
// operations of orchestrator_forms.go / run_actions.go) with its status text, or
// the daemon's error verbatim.
type orchDoneMsg struct {
	status string
	err    error
	// reload, when set, reloads the orchestrator rows / the open detail / the
	// Jobs data afterwards.
	reload bool
	// closeForm closes the open form of the sink surface (a successful submission).
	closeForm bool
	// sink is the surface the operation was started from (its line shows a
	// refusal); target, when set, is the orchestrator it acted on, so Jobs reloads
	// just that one.
	sink   orchSurface
	target orchTarget
	// fire marks the reply to an Enqueue now, the only reply that releases its
	// in-flight record (an unrelated mutation finishing must not).
	fire bool
	// fireEvent is the timed event a successful Enqueue now was for; it starts the
	// repeat window (a refused or failed fire must not, so an immediate retry works).
	fireEvent string
	// fireToken identifies the Enqueue now this reply belongs to; only the reply
	// whose token is the model's in-flight record for fireKey releases it.
	fireToken uint64
	// fireKey / fireLabel say which orchestrator the fire was for: the key releases
	// the model's in-flight record, the label names it in a refusal.
	fireKey   string
	fireLabel string
}

// orchHost is one host the screen fans out over.
type orchHost struct {
	id, name string
	d        Daemon // nil when not connected
	state    client.HostState
	err      error
}

// orchHosts lists the hosts the screen fans out over: every host of the
// manager, or the lone active daemon without one.
func (m Model) orchHosts() []orchHost {
	if m.manager == nil {
		return []orchHost{{id: m.activeHost, name: m.daemon.HostID(), d: m.daemon, state: client.HostConnected}}
	}
	var out []orchHost
	for _, hc := range m.manager.List() {
		row := orchHost{id: hc.Entry.ID, name: hc.Entry.DisplayName, state: hc.State, err: hc.Err}
		if row.name == "" {
			row.name = row.id
		}
		if d := m.connOf(hc); d != nil {
			row.d = d
		}
		out = append(out, row)
	}
	return out
}

// orchLoadCmd fans ListOrchestrators out over every connected host. Hosts that
// are disconnected or fail keep their previous rows, marked unreachable.
func (m Model) orchLoadCmd() tea.Cmd {
	hosts := m.orchHosts()
	prev := m.orch.rows
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		var rows []orchRow
		for _, h := range hosts {
			reason := ""
			var list []*pb.OrchestratorSummary
			var err error
			if h.d == nil {
				reason = strings.ToLower(h.state.String())
				if h.err != nil {
					reason = h.err.Error()
				}
			} else if list, err = h.d.ListOrchestrators(ctx); err != nil {
				reason = err.Error()
			}
			if reason != "" {
				for _, r := range prev {
					if r.host == h.id {
						r.reachable, r.reason = false, reason
						rows = append(rows, r)
					}
				}
				continue
			}
			for _, s := range list {
				rows = append(rows, orchRow{host: h.id, hostName: h.name, summary: s, reachable: true})
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			return rows[i].summary.GetOrchestrator().GetName() < rows[j].summary.GetOrchestrator().GetName()
		})
		return orchRowsMsg{rows: rows}
	}
}

// applyOrchRows installs a fan-out result, keeping the Daemons › Orchestrators
// cursor on the same orchestrator when it still exists.
func (m Model) applyOrchRows(msg orchRowsMsg) (tea.Model, tea.Cmd) {
	var keep string
	if r, ok := m.orchCurrent(); ok {
		keep = r.summary.GetOrchestrator().GetId()
	}
	m.orch.rows = msg.rows
	m.orch.loading = false
	m.orch.loaded = true
	m.orch.cursor = 0
	for i, r := range m.daemonOrchRows() {
		if r.summary.GetOrchestrator().GetId() == keep {
			m.orch.cursor = i
		}
	}
	if m.screen == screenAttachPick {
		m.attach.choices = m.reachableOrchestrators()
		if m.attach.cursor >= len(m.attach.choices) {
			m.attach.cursor = max(len(m.attach.choices)-1, 0)
		}
	}
	return m, nil
}

// applyOrchDone shows a mutation's outcome and reloads what is open.
func (m Model) applyOrchDone(msg orchDoneMsg) (tea.Model, tea.Cmd) {
	op := m.opState(msg.sink)
	if msg.fire {
		// The call is over: release the model's in-flight record (only its own).
		if m.orchFires[msg.fireKey] == msg.fireToken {
			delete(m.orchFires, msg.fireKey)
		}
		if msg.err == nil {
			op.lastFireAt, op.lastFireKey = time.Now(), msg.fireKey+"/"+msg.fireEvent
			// The success is on the footer; a double tap's in-progress refusal is stale now.
			if op.status == enqueueInProgressText {
				op.status = ""
			}
		}
	}
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		m.err = msg.err
		if msg.fire {
			// Named, so a refusal is not read as one for whatever row is highlighted
			// now; and only on the footer while a form is open, so it is not read as
			// that form's refusal.
			text := "enqueue on " + msg.fireLabel + ": " + msg.err.Error()
			m.status = "error: " + text
			if op.form == nil {
				op.status = text
			}
			return m, nil
		}
		if m.onSurface(msg.sink) {
			op.status = msg.err.Error()
		}
		return m, nil
	}
	m.status = msg.status
	m.err = nil
	if msg.closeForm {
		m.closeOpForm(msg.sink)
		op.status = ""
		// A dialog opened from the sandbox list (mark free) returns there.
		if msg.sink == surfaceDetail && m.markFreeFromList && m.screen == screenOrchDetail {
			m.markFreeFromList = false
			m.screen = screenList
			m.listLoading = true
			return m, m.reloadCmd()
		}
	}
	if !msg.reload {
		return m, nil
	}
	var cmds []tea.Cmd
	m.orch.loading = true
	cmds = append(cmds, m.orchLoadCmd())
	if m.screen == screenOrchDetail {
		cmds = append(cmds, m.orchDetailLoadCmd())
	}
	if m.jobs.started {
		cmds = append(cmds, m.jobsAfterChange(msg.target))
	}
	return m, tea.Batch(cmds...)
}

// applyOrchFormErr shows a form refusal verbatim and keeps the form open.
func (m Model) applyOrchFormErr(msg orchFormErrMsg) (tea.Model, tea.Cmd) {
	if m.onSurface(msg.sink) {
		m.opState(msg.sink).status = msg.err.Error()
	} else {
		m.status = "error: " + msg.err.Error()
	}
	// A form enter completed waited dead for this reply: bring it back, values kept.
	return reviveCompleted(opGuard(msg.sink))(m, nil)
}

// applyRunPrompt opens the read-only prompt overlay for a run on its surface.
func (m Model) applyRunPrompt(msg runPromptMsg) (tea.Model, tea.Cmd) {
	m.opState(msg.sink).prompt = msg.run
	return m, nil
}

// orchCurrent returns the Daemons › Orchestrators row under the cursor.
func (m Model) orchCurrent() (orchRow, bool) {
	rows := m.daemonOrchRows()
	if len(rows) == 0 || m.orch.cursor >= len(rows) {
		return orchRow{}, false
	}
	return rows[m.orch.cursor], true
}

// syncOrchestratorChanged reacts to Event.orchestrator_changed: the
// orchestrator rows reload while Daemons › Orchestrators or a detail shows, an
// open detail of that orchestrator re-fetches, and loaded Jobs data reloads just
// that orchestrator (research R3).
func (m Model) syncOrchestratorChanged(host, orchID string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.screen == screenDaemonOrchs || m.screen == screenOrchDetail {
		cmds = append(cmds, m.orchLoadCmd())
	}
	if m.screen == screenOrchDetail && m.orchDetail.id == orchID {
		cmds = append(cmds, m.orchDetailLoadCmd())
	}
	if m.jobs.started {
		cmds = append(cmds, m.jobsReloadOrch(host, orchID))
	}
	if _, gh, gid, ok := m.guardOrch(); ok && gh == host && gid == orchID {
		cmds = append(cmds, m.orchGoneCheckCmd(host, orchID))
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// orchGoneMsg reports that an orchestrator a guarded surface acts on answered
// NotFound (SWI-9 R7).
type orchGoneMsg struct{ host, id string }

// orchGoneCheckCmd reads the orchestrator and reports orchGoneMsg on NotFound
// only; any other outcome leaves the surface as it is.
func (m Model) orchGoneCheckCmd(host, id string) tea.Cmd {
	d, err := m.connectedDaemon(host)
	if err != nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.GetOrchestrator(ctx, id); status.Code(err) == codes.NotFound {
			return orchGoneMsg{host: host, id: id}
		}
		return nil
	}
}

// forwardOrchestrators routes non-key messages to an open form.
func (m Model) forwardOrchestrators(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch {
	case m.screen == screenDaemonOrchs && m.orch.form != nil:
		return m.advanceOrchForm(msg)
	case m.screen == screenOrchDetail && m.orchDetail.form != nil:
		return m.advanceOrchOpForm(surfaceDetail, msg)
	case m.screen == screenJobs && m.jobs.form != nil:
		return m.advanceOrchOpForm(surfaceJobs, msg)
	}
	return m, nil
}

// --- create / rename forms (the 008 huh pattern: heap values, ctrl+s saves) ---

// openOrchCreateForm opens the create form for an orchestrator on host (the
// daemon whose Orchestrators section is open).
func (m Model) openOrchCreateForm(host, hostName string) (tea.Model, tea.Cmd) {
	if _, err := m.connectedDaemon(host); err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	v := &orchFormVals{host: host}
	desc := "1–64 characters, unique across every orchestrator you can see. Hosted by " + sanitizeLine(hostName, 0) + "; workers can live anywhere it can reach over SSH."
	m.orch.formKind = orchFormCreate
	return m, m.setOrchForm(v, func(m Model) *huh.Form {
		return m.newOrchForm(huh.NewInput().Key("name").Title("Name").Description(desc).Value(&v.name))
	})
}

// setOrchForm opens a create/rename form bound to v and records its baseline.
func (m *Model) setOrchForm(v *orchFormVals, build func(Model) *huh.Form) tea.Cmd {
	m.orch.vals, m.orch.formBuild = v, build
	m.orch.form = build(*m)
	m.orch.baseline = *v
	return m.orch.form.Init()
}

// closeOrchForm drops the open create/rename form.
func (m *Model) closeOrchForm() {
	m.orch.form, m.orch.vals, m.orch.formKind, m.orch.formBuild = nil, nil, orchFormNone, nil
}

func (m Model) openOrchRenameForm(row orchRow) (tea.Model, tea.Cmd) {
	// The editable value starts sanitised too: the input draws it as typed. A
	// name that loses characters that way says so, and an unedited submit is a
	// no-op rather than a silent rename (review run-242 finding 6).
	raw := row.summary.GetOrchestrator().GetName()
	v := &orchFormVals{name: sanitizeLine(raw, 0), host: row.host}
	desc := "The label only; workers and briefings follow as they are reachable."
	if v.name != raw {
		desc += " The current name has control or invisible characters; saving an edit drops them."
	}
	title := "Rename " + sanitizeLine(raw, 0)
	m.orch.formKind = orchFormRename
	m.orch.formID = row.summary.GetOrchestrator().GetId()
	m.orch.formHost = row.host
	m.orch.formPrefill = v.name
	return m, m.setOrchForm(v, func(m Model) *huh.Form {
		return m.newOrchForm(huh.NewInput().Key("name").Title(title).Description(desc).Value(&v.name))
	})
}

func (m Model) advanceOrchForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.orch.form == nil {
		return m, nil
	}
	f, cmd := m.orch.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		m.orch.form = ff
	}
	switch m.orch.form.State {
	case huh.StateCompleted:
		return reviveCompleted(gOrchAdmin)(m.applyOrchForm())
	}
	return m, cmd
}

// orchNameTaken reports which host already has an orchestrator with the name
// (unique across every row the TUI knows, FR-105), excluding exceptID.
func (m Model) orchNameTaken(name, exceptID string) (orchRow, bool) {
	for _, r := range m.orch.rows {
		if r.summary.GetOrchestrator().GetName() == name && r.summary.GetOrchestrator().GetId() != exceptID {
			return r, true
		}
	}
	return orchRow{}, false
}

// applyOrchForm validates and submits an open form.
func (m Model) applyOrchForm() (tea.Model, tea.Cmd) {
	v := m.orch.vals
	if m.orch.form == nil || v == nil {
		return m, nil
	}
	name := strings.TrimSpace(v.name)
	switch {
	case name == "":
		m.status = "a name is required"
		return m, nil
	case len(name) > 64:
		m.status = "the name is longer than 64 characters"
		return m, nil
	}
	exceptID := ""
	if m.orch.formKind == orchFormRename {
		exceptID = m.orch.formID
		if name == strings.TrimSpace(m.orch.formPrefill) {
			m.closeOrchForm()
			m.status = "name unchanged"
			return m, nil
		}
	}
	if other, taken := m.orchNameTaken(name, exceptID); taken {
		m.status = fmt.Sprintf("an orchestrator named %s already exists on %s", name, other.hostName)
		return m, nil
	}
	kind, host, id := m.orch.formKind, v.host, m.orch.formID
	if kind == orchFormRename {
		host = m.orch.formHost
	}
	// Only the daemon the form was opened for, and only while it is connected:
	// never the active daemon in its place (FR-030).
	d, err := m.connectedDaemon(host)
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	m.closeOrchForm()
	hostName := m.hostDisplayName(host)
	if kind == orchFormCreate {
		return m, func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			o, err := d.CreateOrchestrator(ctx, name)
			if err != nil {
				return orchDoneMsg{err: err}
			}
			return orchDoneMsg{status: "created " + o.GetName() + " on " + hostName, reload: true}
		}
	}
	return m, func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		o, err := d.RenameOrchestrator(ctx, id, name)
		if err != nil {
			return orchDoneMsg{err: err}
		}
		return orchDoneMsg{status: "renamed to " + o.GetName(), reload: true}
	}
}

// confirmOrchRemove gates removal behind the FR-153 consequences.
func (m Model) confirmOrchRemove(row orchRow) (tea.Model, tea.Cmd) {
	o := row.summary.GetOrchestrator()
	d, err := m.connectedDaemon(row.host)
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	id, name := o.GetId(), o.GetName()
	return m.enterConfirm(confirmState{
		title: "Remove orchestrator " + name + "?",
		body: []string{
			"Every reachable worker is detached (their sandboxes keep running).",
			"Its reasoning sandbox is destroyed.",
			"Queued runs are cancelled and in-flight runs are failed.",
			"Its actions, events and run history are discarded.",
		},
		returnTo: screenDaemonOrchs,
		onConfirm: func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			resp, err := d.RemoveOrchestrator(ctx, id)
			if err != nil {
				return orchDoneMsg{err: err}
			}
			return orchDoneMsg{status: fmt.Sprintf("removed %s (%d workers detached, %d runs failed, %d cancelled)", name, resp.GetWorkersDetached(), resp.GetRunsFailed(), resp.GetRunsCancelled()), reload: true}
		},
	})
}

// --- rendering ---

// orchRowLine renders one row: name · @machine · reachable|unreachable (reason)
// · N workers · queued Q / in flight F / review R.
func orchRowLine(r orchRow) string {
	o := r.summary.GetOrchestrator()
	reach := statusOKStyle.Render("reachable")
	if !r.reachable {
		reach = statusErrStyle.Render("unreachable")
		if r.reason != "" {
			reach += dimStyle.Render(" (" + sanitizeLine(r.reason, 60) + ")")
		}
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s",
		selectedStyle.Render(sanitizeLine(o.GetName(), 40)),
		dimStyle.Render("@"+sanitizeLine(r.hostName, 30)),
		reach,
		plural(int(r.summary.GetWorkerCount()), "worker", "workers"),
		dimStyle.Render(fmt.Sprintf("queued %d / in flight %d / review %d", r.summary.GetQueued(), r.summary.GetInFlight(), r.summary.GetAwaitingReview())),
	)
}
