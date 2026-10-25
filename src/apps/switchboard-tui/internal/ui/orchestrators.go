package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The Orchestrators screen (feature 009, FR-142/FR-143) is the third top-level
// screen in the shift+←/→ cycle: every orchestrator hosted by any known daemon,
// with its machine, reachability, worker count and queue depths. Rows are
// fanned out from ListOrchestrators on every connected host (data-model
// "Client-side view models"); a host that is disconnected, too old or failing
// keeps its last-known rows marked unreachable with the reason, and only
// reading is offered on those (US1 scenario 4). Only the cursor is state, so a
// round trip through the other screens preserves it.

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
}

// orchRowsMsg carries one fan-out result.
type orchRowsMsg struct{ rows []orchRow }

// orchDoneMsg reports a completed mutation (create/rename/remove/hosts) with
// its status text, or the daemon's error verbatim.
type orchDoneMsg struct {
	status string
	err    error
	// reload, when set, reloads the screen rows / the open detail afterwards.
	reload bool
	// closeForm closes the detail's open form (a successful submission).
	closeForm bool
	// fire marks the reply to an Enqueue now, the only reply that releases its
	// in-flight guard (an unrelated mutation finishing must not).
	fire bool
	// fireEvent is the timed event a successful Enqueue now was for; it starts the
	// repeat window (a refused or failed fire must not, so an immediate retry works).
	fireEvent string
	// fireOrch is the orchestrator the Enqueue now was for: a reply that lands
	// after the developer opened another orchestrator's detail leaves that one alone.
	fireOrch     string
	fireOrchName string
}

// enterOrchestrators switches to the screen and (re)loads its rows.
func (m Model) enterOrchestrators() (tea.Model, tea.Cmd) {
	m.screen = screenOrchestrators
	m.orch.loading = true
	return m, m.orchLoadCmd()
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
		if hc.State == client.HostConnected {
			row.d = m.daemonForHost(hc.Entry.ID)
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

// applyOrchRows installs a fan-out result, keeping the cursor on the same
// orchestrator when it still exists.
func (m Model) applyOrchRows(msg orchRowsMsg) (tea.Model, tea.Cmd) {
	var keep string
	if r, ok := m.orchCurrent(); ok {
		keep = r.summary.GetOrchestrator().GetId()
	}
	m.orch.rows = msg.rows
	m.orch.loading = false
	m.orch.loaded = true
	m.orch.cursor = 0
	for i, r := range msg.rows {
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
	if msg.fire && msg.fireOrch == m.orchDetail.id {
		m.orchDetail.firing = false
		if msg.err == nil {
			m.orchDetail.lastFireAt, m.orchDetail.lastFireEv = time.Now(), msg.fireEvent
			// The success is on the footer; a double tap's in-progress refusal is stale now.
			if m.orchDetail.status == enqueueInProgressText {
				m.orchDetail.status = ""
			}
		}
	}
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		m.err = msg.err
		if msg.fire && msg.fireOrch != m.orchDetail.id {
			// A late refusal for an orchestrator no longer open: footer only, named,
			// so it is not read as a refusal of the open detail's event.
			m.status = "error: enqueue on " + msg.fireOrchName + ": " + msg.err.Error()
			return m, nil
		}
		if m.screen == screenOrchDetail {
			m.orchDetail.status = msg.err.Error()
		}
		return m, nil
	}
	m.status = msg.status
	m.err = nil
	if msg.closeForm {
		m.orchDetail.form, m.orchDetail.formVals, m.orchDetail.formKind = nil, nil, ""
		m.orchDetail.status = ""
		// A dialog opened from the sandbox list (mark free) returns there.
		if m.markFreeFromList && m.screen == screenOrchDetail {
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
	return m, tea.Batch(cmds...)
}

// applyOrchFormErr shows a form refusal verbatim and keeps the form open.
func (m Model) applyOrchFormErr(msg orchFormErrMsg) (tea.Model, tea.Cmd) {
	if m.screen == screenOrchDetail {
		m.orchDetail.status = msg.err.Error()
	} else {
		m.status = "error: " + msg.err.Error()
	}
	return m, nil
}

// applyRunPrompt opens the read-only prompt overlay for a run.
func (m Model) applyRunPrompt(msg runPromptMsg) (tea.Model, tea.Cmd) {
	m.orchDetail.prompt = msg.run
	return m, nil
}

// orchCurrent returns the row under the cursor.
func (m Model) orchCurrent() (orchRow, bool) {
	if len(m.orch.rows) == 0 || m.orch.cursor >= len(m.orch.rows) {
		return orchRow{}, false
	}
	return m.orch.rows[m.orch.cursor], true
}

// syncOrchestratorChanged reacts to Event.orchestrator_changed: the screen's
// rows reload when it is showing, and an open detail of that orchestrator
// re-fetches.
func (m Model) syncOrchestratorChanged(host, orchID string) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	if m.screen == screenOrchestrators || m.screen == screenOrchDetail {
		cmds = append(cmds, m.orchLoadCmd())
	}
	if m.screen == screenOrchDetail && m.orchDetail.id == orchID {
		cmds = append(cmds, m.orchDetailLoadCmd())
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

// forwardOrchestrators routes non-key messages to an open form.
func (m Model) forwardOrchestrators(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch {
	case m.screen == screenOrchestrators && m.orch.form != nil:
		return m.advanceOrchForm(msg)
	case m.screen == screenOrchDetail && m.orchDetail.form != nil:
		return m.advanceOrchDetailForm(msg)
	}
	return m, nil
}

func (m Model) orchestratorsHelp() helpBindings {
	if m.orch.form != nil {
		verb := "create"
		if m.orch.formKind == orchFormRename {
			verb = "rename"
		}
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", verb), hkey("esc", "cancel")}
	}
	return helpBindings{
		hkey("n", "new"),
		hkey("R", "rename"),
		hkey("d", "remove"),
		hkey("enter", "open"),
		hkey("r", "reload"),
		m.screenPrevKey(),
		m.keys.Settings,
		hkey("esc", "sandboxes"),
		m.keys.Quit,
	}
}

func (m Model) updateOrchestratorsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
	switch {
	case keyIs(msg, m.keys.ScreenPrev):
		m.screen = screenDaemons
		m.clampDaemonCursor()
		return m, nil
	case keyIs(msg, m.keys.ScreenNext):
		return m, nil // no wrap: orchestrators is the last screen
	case keyIs(msg, m.keys.Settings):
		return m.enterSettings(screenOrchestrators)
	}
	switch msg.String() {
	case "esc":
		m.screen = screenList
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
		if m.orch.cursor < len(m.orch.rows)-1 {
			m.orch.cursor++
		}
	case "n":
		return m.openOrchCreateForm()
	case "R":
		row, ok := m.orchCurrent()
		if !ok {
			return m, nil
		}
		if !row.reachable {
			m.status = row.summary.GetOrchestrator().GetName() + " is unreachable — " + row.reason
			return m, nil
		}
		return m.openOrchRenameForm(row)
	case "d":
		row, ok := m.orchCurrent()
		if !ok {
			return m, nil
		}
		if !row.reachable {
			m.status = row.summary.GetOrchestrator().GetName() + " is unreachable — " + row.reason
			return m, nil
		}
		return m.confirmOrchRemove(row)
	case "enter":
		if row, ok := m.orchCurrent(); ok {
			return m.enterOrchDetail(row)
		}
	}
	return m, nil
}

// --- create / rename forms (the 008 huh pattern: heap values, ctrl+s saves) ---

// orchMachineOptions lists the connected hosts an orchestrator can be placed on.
func (m Model) orchMachineOptions() []huh.Option[string] {
	var opts []huh.Option[string]
	for _, h := range m.orchHosts() {
		if h.d != nil {
			opts = append(opts, huh.NewOption(h.name, h.id))
		}
	}
	return opts
}

func (m Model) openOrchCreateForm() (tea.Model, tea.Cmd) {
	opts := m.orchMachineOptions()
	if len(opts) == 0 {
		m.status = "no connected machine to place an orchestrator on"
		return m, nil
	}
	v := &orchFormVals{host: opts[0].Value}
	m.orch.vals = v
	m.orch.formKind = orchFormCreate
	m.orch.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").Description("1–64 characters, unique across every orchestrator you can see.").Value(&v.name),
		huh.NewSelect[string]().Key("host").Title("Machine").Description("The daemon that hosts it; workers can live anywhere it can reach over SSH.").Options(opts...).Value(&v.host),
	)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
	return m, m.orch.form.Init()
}

func (m Model) openOrchRenameForm(row orchRow) (tea.Model, tea.Cmd) {
	v := &orchFormVals{name: row.summary.GetOrchestrator().GetName(), host: row.host}
	m.orch.vals = v
	m.orch.formKind = orchFormRename
	m.orch.formID = row.summary.GetOrchestrator().GetId()
	m.orch.formHost = row.host
	m.orch.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Rename " + row.summary.GetOrchestrator().GetName()).Description("The label only; workers and briefings follow as they are reachable.").Value(&v.name),
	)).WithTheme(huhTheme()).WithShowHelp(false).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
	return m, m.orch.form.Init()
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
		return m.applyOrchForm()
	case huh.StateAborted:
		m.orch.form, m.orch.vals, m.orch.formKind = nil, nil, orchFormNone
		return m, nil
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
	}
	if other, taken := m.orchNameTaken(name, exceptID); taken {
		m.status = fmt.Sprintf("an orchestrator named %s already exists on %s", name, other.hostName)
		return m, nil
	}
	kind, host, id := m.orch.formKind, v.host, m.orch.formID
	if kind == orchFormRename {
		host = m.orch.formHost
	}
	m.orch.form, m.orch.vals, m.orch.formKind = nil, nil, orchFormNone
	d := m.daemonForHost(host)
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
	d := m.daemonForHost(row.host)
	id, name := o.GetId(), o.GetName()
	return m.enterConfirm(confirmState{
		title: "Remove orchestrator " + name + "?",
		body: []string{
			"Every reachable worker is detached (their sandboxes keep running).",
			"Its reasoning sandbox is destroyed.",
			"Queued runs are cancelled and in-flight runs are failed.",
			"Its actions, events and run history are discarded.",
		},
		returnTo: screenOrchestrators,
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
			reach += dimStyle.Render(" (" + r.reason + ")")
		}
	}
	return fmt.Sprintf("%s  %s  %s  %s  %s",
		selectedStyle.Render(o.GetName()),
		dimStyle.Render("@"+r.hostName),
		reach,
		plural(int(r.summary.GetWorkerCount()), "worker", "workers"),
		dimStyle.Render(fmt.Sprintf("queued %d / in flight %d / review %d", r.summary.GetQueued(), r.summary.GetInFlight(), r.summary.GetAwaitingReview())),
	)
}

func (m Model) viewOrchestrators() string {
	if m.orch.form != nil {
		title := "New orchestrator"
		if m.orch.formKind == orchFormRename {
			title = "Rename orchestrator"
		}
		return lipgloss.JoinVertical(lipgloss.Left, sectionStyle.Render(title), "", m.orch.form.View())
	}
	lines := []string{sectionStyle.Render("Orchestrators"), ""}
	switch {
	case m.orch.loading && !m.orch.loaded:
		lines = append(lines, m.spinner.View()+dimStyle.Render(" loading…"))
	case len(m.orch.rows) == 0:
		lines = append(lines, dimStyle.Render("(no orchestrators)"))
	}
	for i, r := range m.orch.rows {
		cursor := "  "
		if i == m.orch.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		lines = append(lines, cursor+orchRowLine(r))
	}
	if m.orch.loading && m.orch.loaded {
		lines = append(lines, "", m.spinner.View()+dimStyle.Render(" refreshing…"))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
