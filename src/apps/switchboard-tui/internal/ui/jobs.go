package ui

import (
	"context"
	"sort"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The Jobs screen (feature 010, FR-001..FR-005) is the third top-level screen in
// the shift+←/→ cycle (Sandboxes → Daemons → Jobs, replacing Orchestrators): the
// day-to-day work of every orchestrator on every connected daemon, in four tabs
// switched with ←/→ like the Sandboxes group tabs — Overview, Actions, Events,
// Queue (jobs_overview.go, jobs_groups.go, jobs_queue.go). The data is fanned out
// client-side (research R2): one ListOrchestrators per connected host, then one
// ListRuns per orchestrator, hosts in parallel. Event.orchestrator_changed
// reloads just that orchestrator, and a host connecting or disconnecting reloads
// just that host (R3). Every row names its orchestrator and host (`name @ host`).

// jobsOverviewHistory caps the Overview's History section: the most recently
// finished runs across every orchestrator, combined (FR-009, research R7).
const jobsOverviewHistory = 15

// jobsQueuePageSize is how many runs one page of the Queue tab shows (FR-014,
// research R7). A constant, so paging is predictable at any terminal height.
const jobsQueuePageSize = 25

// jobsTab is one of the four tabs, in their fixed order (FR-002, FR-026).
type jobsTab int

const (
	jobsOverview jobsTab = iota
	jobsActions
	jobsEvents
	jobsQueue
	jobsTabCount
)

var jobsTabNames = [jobsTabCount]string{"Overview", "Actions", "Events", "Queue"}

// jobsOrch is one loaded orchestrator (data-model "jobsOrch").
type jobsOrch struct {
	target  orchTarget
	summary *pb.OrchestratorSummary
	runs    *pb.ListRunsResponse // never nil; empty when err is set
	err     error                // its runs could not be read
}

// jobsState backs the screen (data-model "jobsState"). It survives leaving the
// screen, so the last tab, cursors and filter are remembered for the session.
type jobsState struct {
	// orchOpState carries the open form, the Jobs status line, the prompt overlay
	// and the Enqueue now repeat window (run_actions.go).
	orchOpState
	tab     jobsTab
	orchs   []jobsOrch // ordered by host display name, then orchestrator name
	cursors [jobsTabCount]int
	loading bool
	loaded  bool
	// down maps the host entry ids that could not be read to their display names
	// (FR-004); connected records which hosts were connected at their last load,
	// so a connect or disconnect reloads that host only.
	down      map[string]string
	connected map[string]bool
	// queueFilter is "" (all orchestrators) or a target key; its label is kept so
	// a vanished filter can be named when it resets (jobs_queue.go).
	queueFilter      string
	queueFilterLabel string
	queuePage        int
	// results is the open Open results overlay (run_results.go).
	results *runResultsState
	// returnTo is the top-level screen esc/q go back to.
	returnTo screen
}

// jobsHostResult is one host's share of a load.
type jobsHostResult struct {
	host  orchHost
	orchs []jobsOrch
	down  bool
}

// jobsLoadedMsg carries a full load; jobsHostMsg one host's reload; jobsOrchMsg
// one orchestrator's reload (gone when it no longer exists).
type jobsLoadedMsg struct{ hosts []jobsHostResult }

type jobsHostMsg struct{ res jobsHostResult }

type jobsOrchMsg struct {
	host, id string
	orch     *jobsOrch
	gone     bool
	err      error
}

// enterJobs switches to the screen from a top-level screen and (re)loads it.
func (m Model) enterJobs(from screen) (tea.Model, tea.Cmd) {
	m.screen = screenJobs
	m.jobs.returnTo = from
	m.jobs.loading = true
	return m, m.jobsLoadCmd()
}

// loadJobsHost lists one host's orchestrators and their runs. A disconnected or
// failing host contributes nothing and is named as down; an orchestrator whose
// runs cannot be read is still listed (its actions and events are known).
func loadJobsHost(ctx context.Context, h orchHost) jobsHostResult {
	res := jobsHostResult{host: h}
	if h.d == nil {
		res.down = true
		return res
	}
	list, err := h.d.ListOrchestrators(ctx)
	if err != nil {
		res.down = true
		return res
	}
	for _, s := range list {
		o := s.GetOrchestrator()
		runs, err := h.d.ListRuns(ctx, o.GetId())
		if runs == nil {
			runs = &pb.ListRunsResponse{}
		}
		res.orchs = append(res.orchs, jobsOrch{target: targetOf(h.id, h.name, o), summary: s, runs: runs, err: err})
	}
	return res
}

// jobsLoadCmd loads every host the TUI knows, concurrently per host.
func (m Model) jobsLoadCmd() tea.Cmd {
	hosts := m.orchHosts()
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		res := make([]jobsHostResult, len(hosts))
		var wg sync.WaitGroup
		for i, h := range hosts {
			wg.Add(1)
			go func(i int, h orchHost) {
				defer wg.Done()
				res[i] = loadJobsHost(ctx, h)
			}(i, h)
		}
		wg.Wait()
		return jobsLoadedMsg{hosts: res}
	}
}

// jobsHostLoadCmd reloads one host (it connected or disconnected).
func (m Model) jobsHostLoadCmd(h orchHost) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		return jobsHostMsg{res: loadJobsHost(ctx, h)}
	}
}

// jobsReloadOrchCmd reloads one orchestrator's record and runs (research R3).
// It is nil when the host is not connected (its host reload covers it).
func (m Model) jobsReloadOrchCmd(host, id string) tea.Cmd {
	if id == "" {
		return nil
	}
	var h orchHost
	found := false
	for _, x := range m.orchHosts() {
		if x.id == host && x.d != nil {
			h, found = x, true
		}
	}
	if !found {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		det, err := h.d.GetOrchestrator(ctx, id)
		if err != nil {
			return jobsOrchMsg{host: host, id: id, gone: status.Code(err) == codes.NotFound, err: err}
		}
		runs, rerr := h.d.ListRuns(ctx, id)
		if runs == nil {
			runs = &pb.ListRunsResponse{}
		}
		s := det.GetSummary()
		return jobsOrchMsg{host: host, id: id, orch: &jobsOrch{target: targetOf(h.id, h.name, s.GetOrchestrator()), summary: s, runs: runs, err: rerr}}
	}
}

// jobsSyncHostsCmd reloads each host whose connection changed since Jobs last
// read it (a host snapshot arrived: a reconnect, a disconnect, a reload). Nothing
// happens until Jobs has loaded once.
func (m Model) jobsSyncHostsCmd() tea.Cmd {
	if !m.jobs.loaded {
		return nil
	}
	var cmds []tea.Cmd
	for _, h := range m.orchHosts() {
		conn := h.d != nil
		if was, ok := m.jobs.connected[h.id]; ok && was == conn {
			continue
		}
		m.jobs.connected[h.id] = conn // the map is shared: a second snapshot does not re-issue
		cmds = append(cmds, m.jobsHostLoadCmd(h))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// putHost replaces one host's orchestrators and reachability in the state.
func (j *jobsState) putHost(res jobsHostResult) {
	kept := j.orchs[:0:0]
	for _, o := range j.orchs {
		if o.target.host != res.host.id {
			kept = append(kept, o)
		}
	}
	j.orchs = append(kept, res.orchs...)
	if res.down {
		j.down[res.host.id] = res.host.name
	} else {
		delete(j.down, res.host.id)
	}
	j.connected[res.host.id] = res.host.d != nil
}

// sortJobs orders the orchestrators by host display name, then name (a stable
// group order, FR-019).
func (j *jobsState) sortJobs() {
	sort.SliceStable(j.orchs, func(a, b int) bool {
		x, y := j.orchs[a].target, j.orchs[b].target
		if x.hostName != y.hostName {
			return x.hostName < y.hostName
		}
		if x.name != y.name {
			return x.name < y.name
		}
		return x.key() < y.key()
	})
}

func (m Model) applyJobsLoaded(msg jobsLoadedMsg) (tea.Model, tea.Cmd) {
	j := &m.jobs
	j.orchs, j.down, j.connected = nil, map[string]string{}, map[string]bool{}
	for _, res := range msg.hosts {
		j.putHost(res)
	}
	j.loading, j.loaded = false, true
	j.sortJobs()
	m.clampJobs()
	return m, nil
}

func (m Model) applyJobsHost(msg jobsHostMsg) (tea.Model, tea.Cmd) {
	if !m.jobs.loaded {
		return m, nil
	}
	m.jobs.putHost(msg.res)
	m.jobs.sortJobs()
	m.clampJobs()
	return m, nil
}

func (m Model) applyJobsOrch(msg jobsOrchMsg) (tea.Model, tea.Cmd) {
	j := &m.jobs
	if !j.loaded || (msg.err != nil && !msg.gone) {
		// A transient failure keeps what was last read.
		return m, nil
	}
	key := msg.host + "/" + msg.id
	kept := j.orchs[:0:0]
	for _, o := range j.orchs {
		if o.target.key() != key {
			kept = append(kept, o)
		}
	}
	if msg.orch != nil {
		kept = append(kept, *msg.orch)
	}
	j.orchs = kept
	j.sortJobs()
	m.clampJobs()
	return m, nil
}

// jobsOrchByKey finds a loaded orchestrator by its target key.
func (m Model) jobsOrchByKey(key string) (jobsOrch, bool) {
	for _, o := range m.jobs.orchs {
		if o.target.key() == key {
			return o, true
		}
	}
	return jobsOrch{}, false
}

// jobsRowCount is how many cursor positions a tab has.
func (m Model) jobsRowCount(t jobsTab) int {
	switch t {
	case jobsOverview:
		return len(m.overviewItems())
	case jobsActions, jobsEvents:
		return len(m.groupPositions(t))
	case jobsQueue:
		rows, _, _, _ := m.queuePageRows()
		return len(rows)
	}
	return 0
}

// clampJobs keeps the filter, the page and every cursor valid after the data
// changed (a removed orchestrator, trimmed history).
func (m *Model) clampJobs() {
	m.clampQueue()
	for t := jobsTab(0); t < jobsTabCount; t++ {
		if n := m.jobsRowCount(t); m.jobs.cursors[t] >= n {
			m.jobs.cursors[t] = max(n-1, 0)
		}
	}
}

// unreachableHosts names the hosts that could not be read, sorted.
func (m Model) unreachableHosts() []string {
	names := make([]string, 0, len(m.jobs.down))
	for _, n := range m.jobs.down {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// --- keys ---

func (m Model) updateJobsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	j := &m.jobs
	if j.results != nil {
		return m.updateRunResultsKey(msg)
	}
	if j.prompt != nil {
		switch msg.String() {
		case "esc", "q", "enter":
			j.prompt = nil
		}
		return m, nil
	}
	if j.form != nil {
		switch msg.String() {
		case "esc":
			m.closeOpForm(surfaceJobs)
			j.status = ""
			return m, nil
		case "ctrl+s":
			return m.submitOrchOpForm(surfaceJobs)
		}
		return m.advanceOrchOpForm(surfaceJobs, msg)
	}
	switch {
	case keyIs(msg, m.keys.ScreenPrev):
		return m.enterDaemons()
	case keyIs(msg, m.keys.ScreenNext):
		m.screen = screenList // the cycle wraps
		return m, nil
	case keyIs(msg, m.keys.Settings):
		return m.enterSettings(screenJobs)
	}
	switch msg.String() {
	case "esc", "q":
		if j.returnTo == screenDaemons {
			return m.enterDaemons()
		}
		m.screen = screenList
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "left":
		j.tab = (j.tab + jobsTabCount - 1) % jobsTabCount
		j.status = ""
		return m, nil
	case "right":
		j.tab = (j.tab + 1) % jobsTabCount
		j.status = ""
		return m, nil
	case "r":
		j.loading = true
		return m, m.jobsLoadCmd()
	case "up", "k":
		if j.cursors[j.tab] > 0 {
			j.cursors[j.tab]--
		}
		return m, nil
	case "down", "j":
		if j.cursors[j.tab] < m.jobsRowCount(j.tab)-1 {
			j.cursors[j.tab]++
		}
		return m, nil
	}
	switch j.tab {
	case jobsOverview:
		return m.updateOverviewKey(msg)
	case jobsActions:
		return m.updateJobsActionsKey(msg)
	case jobsEvents:
		return m.updateJobsEventsKey(msg)
	case jobsQueue:
		return m.updateJobsQueueKey(msg)
	}
	return m, nil
}

// jobsHelp lists the keys of the current tab, sensitive to the highlighted row
// (FR-028).
func (m Model) jobsHelp() helpBindings {
	j := m.jobs
	switch {
	case j.results != nil:
		return m.runResultsHelp()
	case j.prompt != nil:
		return helpBindings{hkey("esc", "close")}
	case j.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "save"), hkey("esc", "cancel")}
	}
	hb := helpBindings{hkey("←/→", "tab"), hkey("↑/↓", "move")}
	switch j.tab {
	case jobsOverview:
		hb = append(hb, m.overviewRowHelp()...)
	case jobsActions:
		hb = append(hb, m.groupsRowHelp(jobsActions)...)
	case jobsEvents:
		hb = append(hb, m.groupsRowHelp(jobsEvents)...)
	case jobsQueue:
		hb = append(hb, m.queueRowHelp()...)
	}
	return append(hb, hkey("r", "reload"), m.screenPrevKey(), m.screenNextKey(), m.keys.Settings, hkey("esc", "back"))
}

// runRowHelp is the per-run keys offered for a run in its state.
func runRowHelp(r *pb.Run) helpBindings {
	hb := helpBindings{hkey("enter", "prompt")}
	switch {
	case r.GetState() == pb.RunState_RUN_STATE_QUEUED:
		hb = append(hb, hkey("x", "cancel"))
	case r.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW:
		hb = append(hb, hkey("m", "mark free"))
	case runFinished(r):
		hb = append(hb, hkey("o", "open results"))
	}
	return hb
}

// --- rendering ---

// jobsTabBar renders the four tabs in the Sandboxes tab style.
func (m Model) jobsTabBar() string {
	parts := make([]string, 0, int(jobsTabCount))
	for t := jobsTab(0); t < jobsTabCount; t++ {
		style := inactiveTabStyle
		if t == m.jobs.tab {
			style = activeTabStyle
		}
		parts = append(parts, style.Render(jobsTabNames[t]))
	}
	return strings.Join(parts, tabGapStyle.Render(" ")) + dimStyle.Render("   ←/→ switch tab")
}

// jobsEmptyText is every tab's empty state (no orchestrators anywhere).
const jobsEmptyText = "No orchestrators yet — create one under Daemons › <daemon> › Orchestrators."

func (m Model) viewJobs() string {
	j := m.jobs
	if j.prompt != nil {
		return m.promptView(j.prompt)
	}
	if j.form != nil {
		parts := []string{sectionStyle.Render(j.formTarget.label() + " › " + j.formKind), "", j.form.View()}
		if j.status != "" {
			// A daemon refusal stays visible under the open form (verbatim).
			parts = append(parts, "", statusErrStyle.Render(j.status))
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	header := m.jobsTabBar()
	if j.loading && j.loaded {
		header += "   " + m.spinner.View() + dimStyle.Render(" refreshing…")
	}
	top := []string{header}
	if names := m.unreachableHosts(); len(names) > 0 {
		top = append(top, dimStyle.Render("could not read: "+strings.Join(names, ", ")))
	}
	top = append(top, "")
	var body []string
	focus := 0
	switch {
	case j.loading && !j.loaded:
		body = []string{m.spinner.View() + dimStyle.Render(" loading…")}
	case len(j.orchs) == 0:
		body = []string{dimStyle.Render(jobsEmptyText)}
	default:
		switch j.tab {
		case jobsOverview:
			body, focus = m.renderOverview()
		case jobsActions, jobsEvents:
			body, focus = m.renderGroups(j.tab)
		case jobsQueue:
			body, focus = m.renderJobsQueue()
		}
	}
	var bottom []string
	if j.status != "" {
		bottom = []string{"", statusErrStyle.Render(j.status)}
	}
	avail := m.bodyHeight() - len(top) - len(bottom) - (m.helpLineCount(m.jobsHelp()) - 1)
	lines := append(top, windowLines(body, focus, avail)...)
	return lipgloss.JoinVertical(lipgloss.Left, append(lines, bottom...)...)
}

// windowLines keeps a long body within avail lines, scrolled so the focus line
// (the cursor's) stays visible.
func windowLines(lines []string, focus, avail int) []string {
	if avail < 3 {
		avail = 3
	}
	if len(lines) <= avail {
		return lines
	}
	start := focus - avail/2
	if start < 0 {
		start = 0
	}
	if start > len(lines)-avail {
		start = len(lines) - avail
	}
	return lines[start : start+avail]
}

// cursorMark is the cursor column of a row.
func cursorMark(on bool) string {
	if on {
		return cursorBarStyle.Render("> ")
	}
	return "  "
}
