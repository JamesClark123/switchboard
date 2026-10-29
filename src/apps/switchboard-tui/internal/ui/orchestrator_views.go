package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The orchestrator detail (feature 009, research R13) is one orchestrator's
// administration surface, opened from Daemons › <daemon> › Orchestrators
// (feature 010, FR-024): views switched with ←/→ — Workers, Reasoning,
// Hosts, and Lanes when the daemon serves lanes (SWI-18, orchestrator_lanes.go). Its former Actions, Queue and Events views live on the Jobs screen,
// across every orchestrator. The Hosts view manages the machines the
// orchestrator dials (FR-106) with copy-from-my-hosts.

// orchView is one of the views.
type orchView int

const (
	viewWorkers orchView = iota
	viewReasoning
	viewHosts
	viewLanes
	orchViewCount
)

var orchViewNames = [orchViewCount]string{"Workers", "Reasoning", "Hosts", "Lanes"}

// orchViewShown reports whether a view is offered: Lanes only when the
// orchestrator's daemon serves lanes (FR-318a).
func (m Model) orchViewShown(v orchView) bool {
	return v != viewLanes || m.hostHasLanes(m.orchDetail.host)
}

// stepOrchView moves to the next (dir 1) or previous (dir -1) offered view.
func (m Model) stepOrchView(v orchView, dir int) orchView {
	for {
		v = orchView((int(v) + dir + int(orchViewCount)) % int(orchViewCount))
		if m.orchViewShown(v) {
			return v
		}
	}
}

// orchDetailState backs the detail screen.
type orchDetailState struct {
	// orchOpState carries the detail's open form (mark free), status line and
	// prompt overlay (run_actions.go).
	orchOpState
	host, hostName string
	id, name       string
	detail         *pb.OrchestratorDetail
	view           orchView
	cursors        [orchViewCount]int
	loading        bool
	// Hosts view: the copy-from-my-hosts picker.
	hostPick    bool
	hostChoices []store.KnownHost
	hostCursor  int
	// lanes is the Lanes view and the Workers view's lane picker (SWI-18).
	lanes lanesPanel
}

// orchDetailMsg carries a loaded detail or the error.
type orchDetailMsg struct {
	id     string
	detail *pb.OrchestratorDetail
	err    error
}

// enterOrchDetail opens a row's detail; esc returns to that daemon's
// Orchestrators list.
func (m Model) enterOrchDetail(row orchRow) (tea.Model, tea.Cmd) {
	if _, err := m.connectedDaemon(row.host); err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	o := row.summary.GetOrchestrator()
	m.orchDetail = orchDetailState{host: row.host, hostName: row.hostName, id: o.GetId(), name: o.GetName(), loading: true, view: m.orchDetail.view}
	m.daemonOrchs.host, m.daemonOrchs.hostName = row.host, row.hostName
	m.screen = screenOrchDetail
	return m, m.orchDetailLoadCmd()
}

// orchDetailLoadCmd fetches the detail.
func (m Model) orchDetailLoadCmd() tea.Cmd {
	id := m.orchDetail.id
	d, err := m.connectedDaemon(m.orchDetail.host)
	if err != nil {
		return func() tea.Msg { return orchDetailMsg{id: id, err: err} }
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		det, err := d.GetOrchestrator(ctx, id)
		if err != nil {
			return orchDetailMsg{id: id, err: err}
		}
		return orchDetailMsg{id: id, detail: det}
	}
}

func (m Model) applyOrchDetail(msg orchDetailMsg) (tea.Model, tea.Cmd) {
	if m.orchDetail.id != msg.id {
		return m, nil
	}
	m.orchDetail.loading = false
	if msg.err != nil {
		if status.Code(msg.err) == codes.NotFound {
			m.closeOrchGuard(m.orchDetail.host, msg.id)
		}
		m.orchDetail.status = msg.err.Error()
		return m, nil
	}
	m.orchDetail.detail = msg.detail
	m.orchDetail.name = msg.detail.GetSummary().GetOrchestrator().GetName()
	m.clampOrchCursors()
	m.refreshLanes(surfaceDetail)
	if m.orchDetail.view == viewLanes && !m.hostHasLanes(m.orchDetail.host) {
		// The daemon was replaced by one without lanes.
		m.orchDetail.view = viewWorkers
	}
	return m, nil
}

// orchRec is the open orchestrator's record (nil before it loads).
func (m Model) orchRec() *pb.Orchestrator {
	return m.orchDetail.detail.GetSummary().GetOrchestrator()
}

// detailTarget is the open orchestrator as an operation target.
func (m Model) detailTarget() orchTarget {
	d := m.orchDetail
	return orchTarget{host: d.host, hostName: d.hostName, id: d.id, name: d.name, rec: m.orchRec()}
}

// viewRowCount is how many rows the current view has (for cursor clamping).
func (m Model) viewRowCount(v orchView) int {
	d := m.orchDetail
	switch v {
	case viewWorkers:
		return len(d.detail.GetWorkers())
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
	case d.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "save"), hkey("esc", "cancel")}
	case d.hostPick:
		return helpBindings{hkey("↑/↓", "move"), hkey("enter", "add host"), hkey("esc", "cancel")}
	case d.lanes.form != nil || d.lanes.picker != nil:
		return m.lanesHelp(surfaceDetail)
	}
	hb := helpBindings{hkey("←/→", "view"), hkey("↑/↓", "move")}
	switch d.view {
	case viewWorkers:
		hb = append(hb, hkey("x", "detach"), hkey("m", "mark free"), hkey("t", "terminal"))
		if m.hostHasLanes(d.host) {
			hb = append(hb, hkey("l", "lanes"))
		}
	case viewLanes:
		hb = append(hb, m.lanesHelp(surfaceDetail)...)
	case viewReasoning:
		hb = append(hb, hkey("e", "edit setup"), hkey("b", "rebuild"), hkey("l", "output"))
	case viewHosts:
		hb = append(hb, hkey("a", "add from my hosts"), hkey("d", "remove"))
	}
	return append(hb, hkey("r", "reload"), hkey("esc", "orchestrators"))
}

func (m Model) updateOrchDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := &m.orchDetail
	if d.form != nil {
		return m.formKey(gOpDetail, msg)
	}
	if d.hostPick {
		return m.updateHostPickKey(msg)
	}
	if d.lanes.form != nil || d.lanes.picker != nil || (d.view == viewLanes && d.detail != nil) {
		if mm, cmd, handled := m.updateLanesKey(surfaceDetail, msg); handled {
			return mm, cmd
		}
	}
	switch msg.String() {
	case "esc":
		d.status = ""
		return m.backToDaemonOrchs()
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "left":
		d.view = m.stepOrchView(d.view, -1)
		return m, nil
	case "right":
		d.view = m.stepOrchView(d.view, 1)
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
	d, err := m.connectedDaemon(m.orchDetail.host)
	if err != nil {
		return errCmd(surfaceDetail, err)
	}
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

// --- the detail's form (mark free; orchestrator_forms.go submits it) ---

// submitOrchDetailForm sends the detail's open form; the Reasoning view opens no
// huh form of its own, so anything else just closes.
func (m Model) submitOrchDetailForm() (tea.Model, tea.Cmd) {
	if _, ok := m.orchDetail.formVals.(*markFreeVals); ok {
		return m.submitOrchOpForm(surfaceDetail)
	}
	return m.reasoningFormSubmit()
}

// --- rendering ---

func (m Model) orchViewTabs() string {
	parts := make([]string, 0, int(orchViewCount))
	for v := orchView(0); v < orchViewCount; v++ {
		if !m.orchViewShown(v) {
			continue
		}
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
	if d.form != nil {
		parts := []string{sectionStyle.Render(sanitizeLine(d.name+" › "+d.formKind, 0)), "", d.form.View()}
		if d.status != "" {
			// A daemon refusal stays visible under the open form (verbatim).
			parts = append(parts, "", statusErrStyle.Render(sanitizeTerminalText(d.status)))
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	header := sectionStyle.Render("Orchestrator "+sanitizeLine(d.name, 0)) + dimStyle.Render("  @"+sanitizeLine(d.hostName, 0))
	lines := []string{header, m.orchViewTabs(), ""}
	if d.hostPick {
		lines = append(lines, sectionStyle.Render("Add a host from my hosts"), "")
		for i, h := range d.hostChoices {
			cursor := "  "
			if i == d.hostCursor {
				cursor = cursorBarStyle.Render("> ")
			}
			lines = append(lines, m.fitLine(cursor+fitCol(h.DisplayName, 24)+dimStyle.Render("  "+sanitizeLine(h.Summary(), 0))))
		}
		return lipgloss.JoinVertical(lipgloss.Left, lines...)
	}
	switch {
	case d.loading && d.detail == nil:
		lines = append(lines, m.spinner.View()+dimStyle.Render(" loading…"))
	case d.detail == nil:
		lines = append(lines, dimStyle.Render("(not loaded)"))
	case d.lanes.form != nil || d.lanes.picker != nil:
		lines = append(lines, m.lanesView(surfaceDetail)...)
	default:
		lines = append(lines, m.renderOrchView()...)
	}
	if d.status != "" {
		lines = append(lines, "", statusErrStyle.Render(sanitizeTerminalText(d.status)))
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
			return []string{dimStyle.Render("(no hosts — workers can only live on " + sanitizeLine(d.hostName, 0) + " until you add one)")}
		}
		var lines []string
		for i, h := range hosts {
			reach := statusOKStyle.Render("reachable")
			if !h.GetReachable() {
				reach = statusErrStyle.Render("unreachable")
				if h.GetError() != "" {
					reach += dimStyle.Render(": " + sanitizeLine(h.GetError(), 80))
				}
			}
			lines = append(lines, m.fitLine(cursor(i)+fitCol(hostLabel(h), 24)+dimStyle.Render("  ·  "+sanitizeLine(h.GetSshTarget(), 40))+"  ·  "+reach))
		}
		return lines
	case viewWorkers:
		return m.renderWorkersView(cursor)
	case viewReasoning:
		return m.renderReasoningView()
	case viewLanes:
		return m.lanesView(surfaceDetail)
	}
	return nil
}

// orchEmpty is the empty-state line of a view.
func orchEmpty(what string) []string {
	return []string{dimStyle.Render(fmt.Sprintf("(no %s)", what))}
}
