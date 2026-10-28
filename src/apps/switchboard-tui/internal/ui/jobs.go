package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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

// jobsOrch is one loaded orchestrator (data-model "jobsOrch"). Whether its host
// is reachable is not stored: it is the host's connection state now
// (jobsReachable), so rows of a dropped host dim and refuse at once.
type jobsOrch struct {
	target  orchTarget
	summary *pb.OrchestratorSummary
	runs    *pb.ListRunsResponse // never nil
	err     error                // its runs (or record) could not be read
	recErr  bool                 // err is the record's (GetOrchestrator), not the runs'
}

// jobsState backs the screen (data-model "jobsState"). It survives leaving the
// screen, so the last tab, cursors and filter are remembered for the session.
type jobsState struct {
	// orchOpState carries the open form, the Jobs status line, the prompt overlay
	// and the Enqueue now repeat window (run_actions.go).
	orchOpState
	tab   jobsTab
	orchs []jobsOrch // ordered by host display name, then orchestrator name
	// cursors are the tabs' row indexes; cursorKeys the identity of each tab's
	// highlighted item, which the index follows after every data change (R11).
	cursors    [jobsTabCount]int
	cursorKeys [jobsTabCount]string
	// cursorSection is the Overview cursor's section, for its nearest neighbour.
	cursorSection int
	// The load bookkeeping (jobs_load.go): started once Jobs first loads; flights
	// numbers the loads in flight (ordering is jobsSeq's); hosts and meta are per
	// host and per orchestrator (keyed by target key); connected records which
	// hosts were connected at the last host snapshot.
	started   bool
	flights   uint64
	hosts     map[string]*jobsHostLoad
	meta      map[string]*jobsOrchMeta
	connected map[string]bool
	// rev counts applied data changes; derived holds the rows computed from it
	// (computed once per change, not per frame: R4).
	rev     uint64
	derived *jobsDerived
	// frame memoises per-host reachability and worker names while one view
	// renders; viewJobs sets it on its own copy only, so it never outlives the
	// frame (review run-224 item 12).
	frame *jobsFrame
	// queueFilter is "" (all orchestrators) or a target key; its label is kept so
	// a vanished filter can be named when it resets (jobs_queue.go).
	queueFilter      string
	queueFilterLabel string
	queuePage        int
	// results is the open Open results overlay (run_results.go).
	results *runResultsState
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

// --- cursors keyed by identity (R11, FR-031) ---

// jobsTabKeys lists a tab's item identities in display order (the Queue tab:
// every run under the filter, across pages) and, for the Overview, each item's
// section.
func (m Model) jobsTabKeys(t jobsTab) (keys []string, sections []int) {
	switch t {
	case jobsOverview:
		for _, it := range m.overviewItems() {
			keys, sections = append(keys, it.key()), append(sections, it.section)
		}
	case jobsActions, jobsEvents:
		for _, p := range m.groupPositions(t) {
			keys = append(keys, m.groupPosKey(t, p))
		}
	case jobsQueue:
		for _, r := range m.queueFiltered() {
			keys = append(keys, runKey(r.target, r.run))
		}
	}
	return keys, sections
}

// groupPosKey is a grouped tab position's identity.
func (m Model) groupPosKey(t jobsTab, p groupPos) string {
	o := m.jobs.orchs[p.orch]
	switch {
	case p.item < 0:
		return groupKey(o.target)
	case t == jobsEvents:
		return eventKey(o.target, o.target.rec.GetTimedEvents()[p.item])
	}
	return actionKey(o.target, o.target.rec.GetActions()[p.item])
}

// jobsCursorAbs is a tab's cursor as an index into jobsTabKeys.
func (m Model) jobsCursorAbs(t jobsTab) int {
	if t == jobsQueue {
		return m.jobs.queuePage*jobsQueuePageSize + m.jobs.cursors[t]
	}
	return m.jobs.cursors[t]
}

// setJobsCursor moves a tab's cursor to an index into jobsTabKeys (the Queue
// tab turns to that run's page) and, with record, remembers the item there.
func (m *Model) setJobsCursor(t jobsTab, idx int, keys []string, sections []int, record bool) {
	j := &m.jobs
	if idx >= len(keys) {
		idx = len(keys) - 1
	}
	if idx < 0 {
		idx = 0
	}
	if t == jobsQueue {
		j.queuePage, j.cursors[t] = idx/jobsQueuePageSize, idx%jobsQueuePageSize
	} else {
		j.cursors[t] = idx
	}
	if !record {
		return
	}
	j.cursorKeys[t] = ""
	if idx < len(keys) {
		j.cursorKeys[t] = keys[idx]
		if sections != nil {
			j.cursorSection = sections[idx]
		}
	}
}

// anchorJobsCursors moves every tab's cursor back onto its item after the data
// changed. When the item is gone (removed, filtered out, past the History cap)
// it takes the row now at its old position in the same section, else the row
// before it. A tab the developer has not used yet stays at its index (the top),
// so rows arriving from a slower daemon do not carry its cursor away.
func (m *Model) anchorJobsCursors() {
	for t := jobsTab(0); t < jobsTabCount; t++ {
		keys, sections := m.jobsTabKeys(t)
		if m.jobs.cursorKeys[t] == "" {
			m.setJobsCursor(t, m.jobsCursorAbs(t), keys, sections, false)
			continue
		}
		idx := -1
		for i, k := range keys {
			if k == m.jobs.cursorKeys[t] && k != "" {
				idx = i
				break
			}
		}
		if idx < 0 {
			idx = min(m.jobsCursorAbs(t), len(keys)-1)
			if sections != nil && idx > 0 && sections[idx] != m.jobs.cursorSection {
				idx--
			}
		}
		m.setJobsCursor(t, idx, keys, sections, true)
	}
}

// rememberJobsCursor records the item under a tab's cursor after the developer
// moved it.
func (m *Model) rememberJobsCursor(t jobsTab) {
	keys, sections := m.jobsTabKeys(t)
	m.setJobsCursor(t, m.jobsCursorAbs(t), keys, sections, true)
}

// --- keys ---

// updateJobsKey handles a key, then records the item the cursor is on, so the
// next data change keeps it there (R11).
func (m Model) updateJobsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The cursor's item is already recorded: after the previous key and after
	// every data change (anchorJobsCursors), so one pass after the key suffices.
	out, cmd := m.handleJobsKey(msg)
	if mm, ok := out.(Model); ok && mm.screen == screenJobs && mm.jobs.results == nil && mm.jobs.prompt == nil && mm.jobs.form == nil {
		mm.rememberJobsCursor(mm.jobs.tab)
		return mm, cmd
	}
	return out, cmd
}

func (m Model) handleJobsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		case "esc", "ctrl+c":
			return m.requestExit(gOpJobs)
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
	case "esc":
		// Back to Sandboxes, as on every top-level screen (R8).
		m.screen = screenList
		return m, nil
	case "q", "ctrl+c":
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
		return m, m.jobsLoadAll()
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
	if runInFlight(r) {
		hb = append(hb, hkey("t", "terminal"), hkey("T", "external terminal"))
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
	m.jobs.frame = &jobsFrame{reach: map[string]bool{}, names: map[string]string{}}
	j := m.jobs
	if j.prompt != nil {
		return m.promptView(j.prompt)
	}
	if j.form != nil {
		parts := []string{sectionStyle.Render(sanitizeLine(j.formTarget.label()+" › "+j.formKind, 0)), "", j.form.View()}
		if j.status != "" {
			// A daemon refusal stays visible under the open form (verbatim).
			parts = append(parts, "", statusErrStyle.Render(sanitizeTerminalText(j.status)))
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}
	header := m.jobsTabBar()
	if m.jobsRefreshing() && !m.jobsWaiting() {
		header += "   " + m.spinner.View() + dimStyle.Render(" refreshing…")
	}
	top := []string{header}
	if names := m.jobsUnreadable(); len(names) > 0 {
		top = append(top, m.fitLine(dimStyle.Render("could not read: "+joinNames(names))))
	}
	if names := m.jobsConnectingHosts(); len(names) > 0 {
		top = append(top, m.fitLine(dimStyle.Render("connecting: "+joinNames(names))))
	}
	if names := m.jobsLoadingHosts(); len(names) > 0 {
		top = append(top, m.fitLine(dimStyle.Render("loading: "+joinNames(names))))
	}
	top = append(top, "")
	var body []string
	focus := 0
	switch {
	case m.jobsWaiting():
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
		bottom = []string{"", statusErrStyle.Render(sanitizeTerminalText(j.status))}
	}
	avail := m.bodyHeight() - len(top) - len(bottom) - (m.helpLineCount(m.jobsHelp()) - 1)
	lines := append(top, windowLines(body, focus, avail)...)
	return lipgloss.JoinVertical(lipgloss.Left, append(lines, bottom...)...)
}

// fitLine cuts a rendered line to the body width.
func (m Model) fitLine(s string) string { return ansi.Truncate(s, m.bodyWidth(), "…") }

// jobsRowStyle cuts a row to the body width (less the 2-column cursor mark) and
// dims it when its host is not connected now: it stays as last read, and
// operations on it are refused (R3, R12).
func (m Model) jobsRowStyle(host, line string) string {
	line = ansi.Truncate(line, m.bodyWidth()-2, "…")
	if m.jobsReachable(host) {
		return line
	}
	return dimStyle.Render(ansi.Strip(line))
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
