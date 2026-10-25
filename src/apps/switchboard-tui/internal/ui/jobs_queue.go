package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// Jobs › Queue (feature 010, user story 3, FR-013..FR-018): every run the daemons
// keep — queued, then in progress, then the whole retained history newest first
// — across every orchestrator, with no cap of its own (the daemons' retention is
// unchanged). `f` cycles a filter through the orchestrators, `F` clears it; pages
// of jobsQueuePageSize runs are turned with `]`/`[` (research R6). Runs keep the
// Overview's per-run keys.

// queueAll flattens every run: queued ++ in progress ++ all history.
func queueAll(orchs []jobsOrch) []jobsRunRow {
	out := jobsQueued(orchs)
	out = append(out, jobsInProgress(orchs)...)
	return append(out, jobsHistory(orchs, 0)...)
}

// filterRuns keeps the runs of one orchestrator (key "" keeps all).
func filterRuns(rows []jobsRunRow, key string) []jobsRunRow {
	if key == "" {
		return rows
	}
	var out []jobsRunRow
	for _, r := range rows {
		if r.target.key() == key {
			out = append(out, r)
		}
	}
	return out
}

// pageCount is how many pages n runs fill (at least one, even when empty).
func pageCount(n int) int {
	if n == 0 {
		return 1
	}
	return (n + jobsQueuePageSize - 1) / jobsQueuePageSize
}

// pageOf is page p (0-based, clamped) of rows.
func pageOf(rows []jobsRunRow, p int) ([]jobsRunRow, int) {
	pages := pageCount(len(rows))
	if p >= pages {
		p = pages - 1
	}
	if p < 0 {
		p = 0
	}
	end := (p + 1) * jobsQueuePageSize
	if end > len(rows) {
		end = len(rows)
	}
	return rows[p*jobsQueuePageSize : end], p
}

// queuePageRows is the current page of the filtered list, its 0-based index, the
// page count and the filtered total.
func (m Model) queuePageRows() (rows []jobsRunRow, page, pages, total int) {
	all := filterRuns(queueAll(m.jobs.orchs), m.jobs.queueFilter)
	rows, page = pageOf(all, m.jobs.queuePage)
	return rows, page, pageCount(len(all)), len(all)
}

// clampQueue resets a filter whose orchestrator is gone (saying so) and moves a
// page beyond the end to the last page (spec edge cases).
func (m *Model) clampQueue() {
	j := &m.jobs
	if j.queueFilter != "" {
		if _, ok := m.jobsOrchByKey(j.queueFilter); !ok {
			j.status = "filter reset to all orchestrators — " + j.queueFilterLabel + " is no longer available"
			j.queueFilter, j.queueFilterLabel = "", ""
		}
	}
	_, page, _, _ := m.queuePageRows()
	j.queuePage = page
}

// queueFilterLabelText names the filter: "all" or `name @ host` (FR-015).
func (m Model) queueFilterLabelText() string {
	if m.jobs.queueFilter == "" {
		return "all"
	}
	return m.jobs.queueFilterLabel
}

// cycleQueueFilter moves the filter: all → each orchestrator in order → all.
func (m *Model) cycleQueueFilter() {
	j := &m.jobs
	next := 0
	if j.queueFilter != "" {
		for i, o := range j.orchs {
			if o.target.key() == j.queueFilter {
				next = i + 1
			}
		}
	}
	if next >= len(j.orchs) {
		j.queueFilter, j.queueFilterLabel = "", ""
	} else {
		j.queueFilter, j.queueFilterLabel = j.orchs[next].target.key(), j.orchs[next].target.label()
	}
	j.queuePage, j.cursors[jobsQueue] = 0, 0
}

func (m Model) updateJobsQueueKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	j := &m.jobs
	k := msg.String()
	switch k {
	case "f":
		m.cycleQueueFilter()
		j.status = ""
		return m, nil
	case "F":
		j.queueFilter, j.queueFilterLabel = "", ""
		j.queuePage, j.cursors[jobsQueue] = 0, 0
		j.status = ""
		return m, nil
	case "]", "pgdown":
		if _, page, pages, _ := m.queuePageRows(); page < pages-1 {
			j.queuePage, j.cursors[jobsQueue] = page+1, 0
		}
		return m, nil
	case "[", "pgup":
		if _, page, _, _ := m.queuePageRows(); page > 0 {
			j.queuePage, j.cursors[jobsQueue] = page-1, 0
		}
		return m, nil
	case "enter", "x", "m", "o":
		rows, _, _, _ := m.queuePageRows()
		if c := j.cursors[jobsQueue]; c < len(rows) {
			return m.runKeyAction(surfaceJobs, rows[c].target, rows[c].run, k)
		}
	}
	return m, nil
}

// queueRowHelp is the Queue tab's keys for the highlighted run.
func (m Model) queueRowHelp() helpBindings {
	hb := helpBindings{hkey("f", "filter"), hkey("F", "all"), hkey("]/[", "page")}
	rows, _, _, _ := m.queuePageRows()
	if c := m.jobs.cursors[jobsQueue]; c < len(rows) {
		hb = append(hb, runRowHelp(rows[c].run)...)
	}
	return hb
}

// renderJobsQueue renders the header and the current page; focus is the
// cursor's line.
func (m Model) renderJobsQueue() (lines []string, focus int) {
	rows, page, pages, total := m.queuePageRows()
	lines = append(lines, dimStyle.Render(fmt.Sprintf("filter: %s · page %d of %d (%s)", m.queueFilterLabelText(), page+1, pages, plural(total, "run", "runs"))), "")
	if len(rows) == 0 {
		return append(lines, "  "+dimStyle.Render("(no runs)")), 0
	}
	cur := m.jobs.cursors[jobsQueue]
	for i, r := range rows {
		if i == cur {
			focus = len(lines)
		}
		lines = append(lines, cursorMark(i == cur)+jobsRunLine(r))
	}
	return lines, focus
}
