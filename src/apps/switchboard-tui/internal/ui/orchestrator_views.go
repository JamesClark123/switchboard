package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The orchestrator detail (feature 009, research R13) is one orchestrator's
// management surface: six views switched with ←/→ — Workers, Actions, Queue,
// Events, Reasoning, Hosts — each filled by its user story. The Hosts view
// manages the machines the orchestrator dials (FR-106) with copy-from-my-hosts.

// orchView is one of the six views.
type orchView int

const (
	viewWorkers orchView = iota
	viewActions
	viewQueue
	viewEvents
	viewReasoning
	viewHosts
	orchViewCount
)

var orchViewNames = [orchViewCount]string{"Workers", "Actions", "Queue", "Events", "Reasoning", "Hosts"}

// orchDetailState backs the detail screen.
type orchDetailState struct {
	host, hostName string
	id, name       string
	detail         *pb.OrchestratorDetail
	runs           *pb.ListRunsResponse
	view           orchView
	cursors        [orchViewCount]int
	loading        bool
	status         string
	firing         bool      // an Enqueue now is in flight (refuses key-repeat / double presses)
	fireToken      uint64    // identifies that in-flight fire; its reply echoes it (0 = none)
	lastFireAt     time.Time // when the last Enqueue now started, for the repeat debounce
	lastFireEv     string
	// Hosts view: the copy-from-my-hosts picker.
	hostPick    bool
	hostChoices []store.KnownHost
	hostCursor  int
	// a form opened by one of the views (actions, events, mark free …)
	form     *huh.Form
	formKind string
	formVals any
	// prompt is the run whose composed prompt the read-only overlay shows.
	prompt *pb.Run
}

// orchDetailMsg carries a loaded detail (+ runs) or the error.
type orchDetailMsg struct {
	id     string
	detail *pb.OrchestratorDetail
	runs   *pb.ListRunsResponse
	err    error
}

// enterOrchDetail opens a row's detail, keeping the list cursor for the return.
func (m Model) enterOrchDetail(row orchRow) (tea.Model, tea.Cmd) {
	o := row.summary.GetOrchestrator()
	m.orchDetail = orchDetailState{host: row.host, hostName: row.hostName, id: o.GetId(), name: o.GetName(), loading: true, view: m.orchDetail.view}
	if tok := m.orchFires[o.GetId()]; tok != 0 {
		// An Enqueue now for this orchestrator is still in flight from before the detail
		// was left: keep refusing a second one, and let its reply count as current.
		m.orchDetail.firing, m.orchDetail.fireToken = true, tok
	}
	m.screen = screenOrchDetail
	return m, m.orchDetailLoadCmd()
}

// orchDetailLoadCmd fetches the detail and the run groups.
func (m Model) orchDetailLoadCmd() tea.Cmd {
	d := m.daemonForHost(m.orchDetail.host)
	id := m.orchDetail.id
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		det, err := d.GetOrchestrator(ctx, id)
		if err != nil {
			return orchDetailMsg{id: id, err: err}
		}
		runs, err := d.ListRuns(ctx, id)
		if err != nil {
			runs = &pb.ListRunsResponse{}
		}
		return orchDetailMsg{id: id, detail: det, runs: runs}
	}
}

func (m Model) applyOrchDetail(msg orchDetailMsg) (tea.Model, tea.Cmd) {
	if m.orchDetail.id != msg.id {
		return m, nil
	}
	m.orchDetail.loading = false
	if msg.err != nil {
		m.orchDetail.status = msg.err.Error()
		return m, nil
	}
	m.orchDetail.detail = msg.detail
	m.orchDetail.runs = msg.runs
	m.orchDetail.name = msg.detail.GetSummary().GetOrchestrator().GetName()
	m.clampOrchCursors()
	return m, nil
}

// orchRec is the open orchestrator's record (nil before it loads).
func (m Model) orchRec() *pb.Orchestrator {
	return m.orchDetail.detail.GetSummary().GetOrchestrator()
}

// viewRowCount is how many rows the current view has (for cursor clamping).
func (m Model) viewRowCount(v orchView) int {
	d := m.orchDetail
	switch v {
	case viewWorkers:
		return len(d.detail.GetWorkers())
	case viewActions:
		return len(m.orchRec().GetActions())
	case viewQueue:
		return len(d.runs.GetQueued()) + len(d.runs.GetInFlight()) + len(d.runs.GetHistory())
	case viewEvents:
		return len(m.orchRec().GetTimedEvents())
	case viewHosts:
		return len(d.detail.GetHosts())
	}
	return 0
}

func (m *Model) clampOrchCursors() {
	for v := orchView(0); v < orchViewCount; v++ {
		if n := m.viewRowCount(v); m.orchDetail.cursors[v] >= n {
			m.orchDetail.cursors[v] = max(n-1, 0)
		}
	}
}

func (m Model) orchDetailHelp() helpBindings {
	d := m.orchDetail
	switch {
	case d.prompt != nil:
		return helpBindings{hkey("esc", "close")}
	case d.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "save"), hkey("esc", "cancel")}
	case d.hostPick:
		return helpBindings{hkey("↑/↓", "move"), hkey("enter", "add host"), hkey("esc", "cancel")}
	}
	hb := helpBindings{hkey("←/→", "view"), hkey("↑/↓", "move")}
	switch d.view {
	case viewWorkers:
		hb = append(hb, hkey("x", "detach"), hkey("m", "mark free"))
	case viewActions:
		hb = append(hb, hkey("a", "add"), hkey("e", "edit"), hkey("d", "delete"), hkey("enter", "call"))
	case viewQueue:
		hb = append(hb, hkey("enter", "prompt"), hkey("x", "cancel"), hkey("m", "mark free"))
	case viewEvents:
		hb = append(hb, hkey("a", "add"), hkey("e", "edit"), hkey("d", "delete"), hkey("space", "on/off"), hkey("n", "enqueue now"))
	case viewReasoning:
		hb = append(hb, hkey("e", "edit setup"), hkey("b", "rebuild"), hkey("l", "output"))
	case viewHosts:
		hb = append(hb, hkey("a", "add from my hosts"), hkey("d", "remove"))
	}
	return append(hb, hkey("r", "reload"), hkey("esc", "back"))
}

func (m Model) updateOrchDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	if d.prompt != nil {
		switch msg.String() {
		case "esc", "q", "enter":
			d.prompt = nil
		}
		return m, nil
	}
	if d.form != nil {
		switch msg.String() {
		case "esc":
			d.form, d.formVals, d.formKind = nil, nil, ""
			d.status = ""
			if m.markFreeFromList {
				m.markFreeFromList = false
				m.screen = screenList
			}
			return m, nil
		case "ctrl+s":
			return m.applyOrchDetailForm()
		}
		return m.advanceOrchDetailForm(msg)
	}
	if d.hostPick {
		return m.updateHostPickKey(msg)
	}
	switch msg.String() {
	case "esc":
		m.screen = screenOrchestrators
		d.status = ""
		return m, nil
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "left":
		d.view = (d.view + orchViewCount - 1) % orchViewCount
		return m, nil
	case "right":
		d.view = (d.view + 1) % orchViewCount
		return m, nil
	case "r":
		d.loading = true
		return m, m.orchDetailLoadCmd()
	case "up", "k":
		if d.cursors[d.view] > 0 {
			d.cursors[d.view]--
		}
		return m, nil
	case "down", "j":
		if d.cursors[d.view] < m.viewRowCount(d.view)-1 {
			d.cursors[d.view]++
		}
		return m, nil
	}
	switch d.view {
	case viewHosts:
		return m.updateHostsViewKey(msg)
	case viewWorkers:
		return m.updateWorkersViewKey(msg)
	case viewActions:
		return m.updateActionsViewKey(msg)
	case viewQueue:
		return m.updateQueueViewKey(msg)
	case viewEvents:
		return m.updateEventsViewKey(msg)
	case viewReasoning:
		return m.updateReasoningViewKey(msg)
	}
	return m, nil
}

// --- Hosts view (complete in user story 1) ---

func (m Model) updateHostsViewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	switch msg.String() {
	case "a":
		choices := m.hostChoicesForOrch()
		if len(choices) == 0 {
			d.status = "every saved host is already on this orchestrator (or is its own machine)"
			return m, nil
		}
		d.hostPick, d.hostChoices, d.hostCursor = true, choices, 0
		return m, nil
	case "d":
		hosts := d.detail.GetHosts()
		if len(hosts) == 0 {
			return m, nil
		}
		h := hosts[d.cursors[viewHosts]]
		kept := make([]*pb.OrchestratorHost, 0, len(hosts)-1)
		for _, x := range hosts {
			if x.GetHostId() != h.GetHostId() {
				kept = append(kept, x)
			}
		}
		return m.enterConfirm(confirmState{
			title:     "Remove host " + hostLabel(h) + " from " + d.name + "?",
			body:      []string{"The orchestrator stops dialing it. Workers on it must be detached first."},
			warning:   "Workers still on this host block the removal.",
			returnTo:  screenOrchDetail,
			onConfirm: m.setOrchHostsCmd(kept, "removed host "+hostLabel(h)),
		})
	}
	return m, nil
}

// hostChoicesForOrch lists the TUI's saved ssh hosts not yet on the
// orchestrator, excluding its own machine (which needs no entry).
func (m Model) hostChoicesForOrch() []store.KnownHost {
	if m.hostStore == nil {
		return nil
	}
	known, err := m.hostStore.List()
	if err != nil {
		return nil
	}
	present := map[string]bool{}
	for _, h := range m.orchDetail.detail.GetHosts() {
		present[h.GetHostId()] = true
	}
	ownHost := m.orchRec().GetHostId()
	var out []store.KnownHost
	for _, h := range known {
		if h.Kind != "ssh" || present[h.ID] || h.ID == m.orchDetail.host || h.ID == ownHost {
			continue
		}
		out = append(out, h)
	}
	return out
}

func (m Model) updateHostPickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	switch msg.String() {
	case "esc", "q":
		d.hostPick = false
		return m, nil
	case "up", "k":
		if d.hostCursor > 0 {
			d.hostCursor--
		}
	case "down", "j":
		if d.hostCursor < len(d.hostChoices)-1 {
			d.hostCursor++
		}
	case "enter":
		h := d.hostChoices[d.hostCursor]
		d.hostPick = false
		hosts := append([]*pb.OrchestratorHost(nil), d.detail.GetHosts()...)
		hosts = append(hosts, knownHostEntry(h))
		return m, m.setOrchHostsCmd(hosts, "added host "+h.DisplayName)
	}
	return m, nil
}

// knownHostEntry copies a saved host into an orchestrator entry — never a
// password (the daemon dials with keys or an agent only, FR-106).
func knownHostEntry(h store.KnownHost) *pb.OrchestratorHost {
	return &pb.OrchestratorHost{HostId: h.ID, DisplayName: h.DisplayName, SshTarget: h.SSHTarget, SshOptions: append([]string(nil), h.SSHOptions...)}
}

// setOrchHostsCmd sends the edited host list; the daemon's refusal text is
// shown verbatim.
func (m Model) setOrchHostsCmd(hosts []*pb.OrchestratorHost, status string) tea.Cmd {
	d := m.daemonForHost(m.orchDetail.host)
	id := m.orchDetail.id
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.SetOrchestratorHosts(ctx, id, hosts); err != nil {
			return orchDoneMsg{err: err}
		}
		return orchDoneMsg{status: status, reload: true}
	}
}

func hostLabel(h *pb.OrchestratorHost) string {
	if h.GetDisplayName() != "" {
		return h.GetDisplayName()
	}
	return h.GetHostId()
}

// --- forms shared by the views (filled per story in orchestrator_forms.go) ---

func (m Model) advanceOrchDetailForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	if d.form == nil {
		return m, nil
	}
	f, cmd := d.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		d.form = ff
	}
	switch d.form.State {
	case huh.StateCompleted:
		return m.applyOrchDetailForm()
	case huh.StateAborted:
		d.form, d.formVals, d.formKind = nil, nil, ""
		return m, nil
	}
	return m, cmd
}

// --- rendering ---

func (m Model) orchViewTabs() string {
	parts := make([]string, 0, int(orchViewCount))
	for v := orchView(0); v < orchViewCount; v++ {
		style := inactiveTabStyle
		if v == m.orchDetail.view {
			style = activeTabStyle
		}
		parts = append(parts, style.Render(orchViewNames[v]))
	}
	return strings.Join(parts, tabGapStyle.Render(" ")) + dimStyle.Render("   ←/→ switch view")
}

func (m Model) viewOrchDetail() string {
	d := m.orchDetail
	if d.prompt != nil {
		r := d.prompt
		return lipgloss.JoinVertical(lipgloss.Left,
			sectionStyle.Render("Run "+r.GetId())+dimStyle.Render("  "+r.GetAction()+" · "+strings.ToLower(strings.TrimPrefix(r.GetState().String(), "RUN_STATE_"))),
			"",
			panelStyle.Width(m.bodyWidth()-2).Render(r.GetPrompt()),
		)
	}
	if d.form != nil {
		parts := []string{sectionStyle.Render(d.name + " › " + d.formKind), "", d.form.View()}
		if d.status != "" {
			// A daemon refusal stays visible under the open form (verbatim).
			parts = append(parts, "", statusErrStyle.Render(d.status))
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	header := sectionStyle.Render("Orchestrator "+d.name) + dimStyle.Render("  @"+d.hostName)
	lines := []string{header, m.orchViewTabs(), ""}
	if d.hostPick {
		lines = append(lines, sectionStyle.Render("Add a host from my hosts"), "")
		for i, h := range d.hostChoices {
			cursor := "  "
			if i == d.hostCursor {
				cursor = cursorBarStyle.Render("> ")
			}
			lines = append(lines, cursor+h.DisplayName+dimStyle.Render("  "+h.Summary()))
		}
		return lipgloss.JoinVertical(lipgloss.Left, lines...)
	}
	switch {
	case d.loading && d.detail == nil:
		lines = append(lines, m.spinner.View()+dimStyle.Render(" loading…"))
	case d.detail == nil:
		lines = append(lines, dimStyle.Render("(not loaded)"))
	default:
		lines = append(lines, m.renderOrchView()...)
	}
	if d.status != "" {
		lines = append(lines, "", statusErrStyle.Render(d.status))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderOrchView renders the body of the current view.
func (m Model) renderOrchView() []string {
	d := m.orchDetail
	cursor := func(i int) string {
		if i == d.cursors[d.view] {
			return cursorBarStyle.Render("> ")
		}
		return "  "
	}
	switch d.view {
	case viewHosts:
		hosts := d.detail.GetHosts()
		if len(hosts) == 0 {
			return []string{dimStyle.Render("(no hosts — workers can only live on " + d.hostName + " until you add one)")}
		}
		var lines []string
		for i, h := range hosts {
			reach := statusOKStyle.Render("reachable")
			if !h.GetReachable() {
				reach = statusErrStyle.Render("unreachable")
				if h.GetError() != "" {
					reach += dimStyle.Render(": " + h.GetError())
				}
			}
			lines = append(lines, cursor(i)+hostLabel(h)+dimStyle.Render("  ·  "+h.GetSshTarget())+"  ·  "+reach)
		}
		return lines
	case viewWorkers:
		return m.renderWorkersView(cursor)
	case viewActions:
		return m.renderActionsView(cursor)
	case viewQueue:
		return m.renderQueueView(cursor)
	case viewEvents:
		return m.renderEventsView(cursor)
	case viewReasoning:
		return m.renderReasoningView()
	}
	return nil
}

// orchEmpty is the empty-state line of a view that a later story fills.
func orchEmpty(what string) []string {
	return []string{dimStyle.Render(fmt.Sprintf("(no %s)", what))}
}
