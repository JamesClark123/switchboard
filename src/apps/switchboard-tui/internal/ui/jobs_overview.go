package ui

import (
	"fmt"
	"sort"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Jobs › Overview (feature 010, user stories 1–2, FR-006..FR-012): four sections
// across every orchestrator — Upcoming (each enabled timed event once, by next
// firing time; `n` Enqueue now), Queued (by receipt), In progress (by start) and
// History (the latest jobsOverviewHistory finished runs, newest first). Runs keep
// today's Queue actions (`enter` prompt, `x` cancel, `m` mark free) and finished
// ones add `o` Open results (research R4, R5, R9).

// jobsRunRow is one run with the orchestrator it belongs to.
type jobsRunRow struct {
	target orchTarget
	run    *pb.Run
}

// jobsEventRow is one timed event with the orchestrator it belongs to; willSkip
// says dedupe would skip its next firing because a run of it is open (FR-007).
type jobsEventRow struct {
	target   orchTarget
	event    *pb.TimedEvent
	willSkip bool
}

// tsTime is a timestamp as a time (the zero time when unset), for ordering.
func tsTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// ago renders a timestamp relative to now, or "—" when unset.
func ago(ts *timestamppb.Timestamp) string {
	if ts == nil {
		return "—"
	}
	return relTime(ts)
}

// jobsUpcoming is every enabled timed event with a next firing time, soonest
// first (FR-007); disabled events and events without one are left out.
func jobsUpcoming(orchs []jobsOrch) []jobsEventRow {
	var out []jobsEventRow
	for _, o := range orchs {
		for _, ev := range o.target.rec.GetTimedEvents() {
			if ev.GetEnabled() && ev.GetNextFireAt() != nil {
				skip := !ev.GetDedupeDisabled() && openRunOfEvent(o.runs, ev.GetId()) != nil
				out = append(out, jobsEventRow{target: o.target, event: ev, willSkip: skip})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return tsTime(out[i].event.GetNextFireAt()).Before(tsTime(out[j].event.GetNextFireAt()))
	})
	return out
}

// collectRuns gathers one run group of every orchestrator, ordered by less.
func collectRuns(orchs []jobsOrch, group func(*pb.ListRunsResponse) []*pb.Run, less func(a, b *pb.Run) bool) []jobsRunRow {
	var out []jobsRunRow
	for _, o := range orchs {
		for _, r := range group(o.runs) {
			out = append(out, jobsRunRow{target: o.target, run: r})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return less(out[i].run, out[j].run) })
	return out
}

// jobsQueued is every queued run, oldest receipt first (dispatch order).
func jobsQueued(orchs []jobsOrch) []jobsRunRow {
	return collectRuns(orchs, (*pb.ListRunsResponse).GetQueued, func(a, b *pb.Run) bool {
		return tsTime(a.GetReceivedAt()).Before(tsTime(b.GetReceivedAt()))
	})
}

// jobsInProgress is every in-flight run (running, awaiting input or review),
// earliest start first.
func jobsInProgress(orchs []jobsOrch) []jobsRunRow {
	return collectRuns(orchs, (*pb.ListRunsResponse).GetInFlight, func(a, b *pb.Run) bool {
		return tsTime(a.GetStartedAt()).Before(tsTime(b.GetStartedAt()))
	})
}

// jobsHistory is every finished run, most recently ended first; limit > 0 keeps
// only the first limit (the Overview's combined cap, FR-009).
func jobsHistory(orchs []jobsOrch, limit int) []jobsRunRow {
	out := collectRuns(orchs, (*pb.ListRunsResponse).GetHistory, func(a, b *pb.Run) bool {
		return tsTime(a.GetEndedAt()).After(tsTime(b.GetEndedAt()))
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Overview sections, in order (FR-006).
const (
	sectionUpcoming = iota
	sectionQueued
	sectionInProgress
	sectionHistory
)

// overviewItem is one cursor position of the Overview: an upcoming event or a run.
type overviewItem struct {
	section int
	ev      *jobsEventRow
	run     *jobsRunRow
}

// itemTarget is the orchestrator an Overview item belongs to.
func itemTarget(it overviewItem) orchTarget {
	if it.ev != nil {
		return it.ev.target
	}
	return it.run.target
}

// key is the item's identity (R11).
func (it overviewItem) key() string {
	if it.ev != nil {
		return eventKey(it.ev.target, it.ev.event)
	}
	return runKey(it.run.target, it.run.run)
}

// Item identities (R11): a run, an event, an action, and a group heading or
// empty line, each qualified by host and orchestrator id.
func runKey(t orchTarget, r *pb.Run) string           { return "r|" + t.key() + "|" + r.GetId() }
func eventKey(t orchTarget, ev *pb.TimedEvent) string { return "e|" + t.key() + "|" + ev.GetId() }
func actionKey(t orchTarget, a *pb.Action) string     { return "a|" + t.key() + "|" + a.GetName() }
func groupKey(t orchTarget) string                    { return "g|" + t.key() }

// jobsDerived is the rows computed from the loaded data, once per data
// revision rather than per frame (R4).
type jobsDerived struct {
	rev      uint64
	overview []overviewItem
	queue    []jobsRunRow // queued ++ in progress ++ all history (the Queue tab)
}

// deriveJobs computes the Overview items and the Queue tab's runs.
func deriveJobs(orchs []jobsOrch, rev uint64) *jobsDerived {
	d := &jobsDerived{rev: rev}
	for _, e := range jobsUpcoming(orchs) {
		e := e
		d.overview = append(d.overview, overviewItem{section: sectionUpcoming, ev: &e})
	}
	queued, inProgress, history := jobsQueued(orchs), jobsInProgress(orchs), jobsHistory(orchs, 0)
	add := func(section int, rows []jobsRunRow) {
		for _, r := range rows {
			r := r
			d.overview = append(d.overview, overviewItem{section: section, run: &r})
		}
	}
	add(sectionQueued, queued)
	add(sectionInProgress, inProgress)
	add(sectionHistory, history[:min(len(history), jobsOverviewHistory)])
	d.queue = append(append(append(d.queue, queued...), inProgress...), history...)
	return d
}

// jobsRows is the derived rows of the current data.
func (m Model) jobsRows() *jobsDerived {
	if d := m.jobs.derived; d != nil && d.rev == m.jobs.rev {
		return d
	}
	return deriveJobs(m.jobs.orchs, m.jobs.rev)
}

// overviewItems lists the Overview's rows in display order.
func (m Model) overviewItems() []overviewItem { return m.jobsRows().overview }

// overviewCurrent is the Overview row under the cursor.
func (m Model) overviewCurrent() (overviewItem, bool) {
	items := m.overviewItems()
	c := m.jobs.cursors[jobsOverview]
	if c >= len(items) {
		return overviewItem{}, false
	}
	return items[c], true
}

// jobsRunsOf is the loaded runs of an orchestrator (empty when unknown).
func (m Model) jobsRunsOf(t orchTarget) *pb.ListRunsResponse {
	if o, ok := m.jobsOrchByKey(t.key()); ok {
		return o.runs
	}
	return &pb.ListRunsResponse{}
}

func (m Model) updateOverviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	it, ok := m.overviewCurrent()
	if !ok {
		return m, nil
	}
	k := msg.String()
	if it.ev != nil {
		if k == "n" {
			return m.enqueueNowKey(surfaceJobs, it.ev.target, it.ev.event, m.jobsRunsOf(it.ev.target))
		}
		return m, nil
	}
	switch k {
	case "enter", "x", "m", "o", "t", "T":
		return m.runKeyAction(surfaceJobs, it.run.target, it.run.run, k)
	}
	return m, nil
}

// overviewRowHelp is the keys for the highlighted Overview row.
func (m Model) overviewRowHelp() helpBindings {
	it, ok := m.overviewCurrent()
	switch {
	case !ok:
		return nil
	case it.ev != nil:
		return helpBindings{hkey("n", "enqueue now")}
	}
	return runRowHelp(it.run.run)
}

// --- rendering ---

// upcomingLine renders an Upcoming row: name → action · orchestrator @ host ·
// next in … (· next firing skipped — run open) (· window). Columns are cut and
// padded to fixed display widths (contracts/jobs-ui.md). The skip note comes
// first: it is the row's one actionable signal, and the row is cut at the
// screen's width.
func upcomingLine(e jobsEventRow) string {
	line := selectedStyle.Render(fitCol(e.event.GetName(), 14)) + " " + dimStyle.Render(fitCol("→ "+e.event.GetAction(), 16)) + " " +
		fitCol(e.target.label(), 24) + "  " + dimStyle.Render("next "+nextFire(e.event))
	if e.willSkip {
		line += dimStyle.Render(" · next firing skipped — run open")
	}
	// A windowed event can be days from its next firing; the window says why.
	// Compact here (no zone when it is local's); the Events tab has it in full.
	if w := compactWindowLabel(e.event.GetWindow(), localZone()); w != "" {
		line += dimStyle.Render(" · " + sanitizeLine(w, 48))
	}
	return line
}

// jobsRunLine renders a run row of the Overview and the Queue tab, labelled with
// its orchestrator and host (FR-003; contracts/jobs-ui.md row formats). Every
// daemon- or agent-supplied text is a sanitised single line (FR-029); a finished
// run's result snippet takes the rest of the width.
func (m Model) jobsRunLine(r jobsRunRow) string {
	run := r.run
	label := fitCol(r.target.label(), 24)
	switch run.GetState() {
	case pb.RunState_RUN_STATE_QUEUED:
		line := dimStyle.Render(fitCol(fmt.Sprintf("#%d", run.GetPosition()), 4)) + selectedStyle.Render(fitCol(run.GetAction(), 14)) + " " +
			dimStyle.Render(fitCol(runOrigin(run), 16)) + " " + label + "  " + dimStyle.Render("received "+ago(run.GetReceivedAt()))
		if why := run.GetWaitingReason(); why != "" {
			// SWI-18 (FR-320): the rest of the row; + marks a cut, enter shows it whole.
			if room := m.bodyWidth() - 2 - lipgloss.Width(line) - 4; room > 0 {
				line += dimStyle.Render("  · " + ansi.Truncate(sanitizeLine(why, 0), room, "+"))
			}
		}
		return line
	case pb.RunState_RUN_STATE_RUNNING, pb.RunState_RUN_STATE_AWAITING_INPUT, pb.RunState_RUN_STATE_AWAITING_REVIEW:
		style := agentWorkStyle
		if run.GetState() != pb.RunState_RUN_STATE_RUNNING {
			style = agentWaitStyle
		}
		line := selectedStyle.Render(fitCol(run.GetAction(), 16)) + " " + style.Render(fitCol(runState(run), 15)) + " " + label + "  " +
			dimStyle.Render("on "+sanitizeLine(m.workerName(run), 16)+" · "+ago(run.GetStartedAt()))
		if run.GetEventKind() != pb.EventKind_EVENT_KIND_USER {
			// The origin takes what is left of the row, like the History snippet.
			if room := m.bodyWidth() - 2 - lipgloss.Width(line) - 2; room > 0 {
				line += "  " + dimStyle.Render(sanitizeLine(runOrigin(run), room))
			}
		}
		return line
	}
	style := agentIdleStyle
	if run.GetState() != pb.RunState_RUN_STATE_COMPLETED {
		style = statusErrStyle
	}
	line := selectedStyle.Render(fitCol(run.GetAction(), 16)) + " " + style.Render(fitCol(runState(run), 15)) + " " + label + "  " +
		dimStyle.Render("ended "+ago(run.GetEndedAt()))
	if detail := runResultLine(run); detail != "" {
		// The snippet takes what is left of the row (the cursor column is 2 wide).
		if room := m.bodyWidth() - 2 - lipgloss.Width(line) - 3; room > 0 {
			line += dimStyle.Render(" — " + sanitizeLine(detail, room))
		}
	}
	return line
}

// workerName is the display name of the sandbox a run is on, from the TUI's
// aggregated sandbox list (the orchestrator record carries none, R4); the short
// sandbox id when it is not listed.
func (m Model) workerName(r *pb.Run) string {
	f := m.jobs.frame
	key := r.GetSandboxHostId() + "/" + r.GetSandboxId()
	if f != nil {
		if n, ok := f.names[key]; ok {
			return n
		}
	}
	n := m.lookupWorkerName(r)
	if f != nil {
		f.names[key] = n
	}
	return n
}

func (m Model) lookupWorkerName(r *pb.Run) string {
	entry, _ := m.entryForDaemonHost(r.GetSandboxHostId())
	for _, hs := range m.hostAgg {
		if entry != "" && hs.Host.Entry.ID != entry {
			continue
		}
		for _, sb := range hs.Sandboxes {
			if sb.GetId() == r.GetSandboxId() && sb.GetDisplayName() != "" {
				return sb.GetDisplayName()
			}
		}
	}
	for _, sb := range m.sandboxes {
		if sb.GetId() == r.GetSandboxId() && sb.GetDisplayName() != "" {
			return sb.GetDisplayName()
		}
	}
	return short(r.GetSandboxId())
}

// runSandbox finds the record of a run's worker sandbox and the TUI host entry
// it is on, to open its terminal (SWI-14). Unlike lookupWorkerName, which only
// names the worker, it never guesses across hosts: the run's sandbox_host_id
// must be a connected daemon's and only that host's sandboxes match; a run
// without one matches only when exactly one host has the sandbox. When there is
// no match, the last result says why, as the end of a "worker sandbox X …" line.
func (m Model) runSandbox(r *pb.Run) (*pb.Sandbox, string, string) {
	id := r.GetSandboxId()
	if id == "" {
		return nil, "", "has no worker sandbox"
	}
	entry, scoped := "", false
	if hid := r.GetSandboxHostId(); hid != "" {
		var ok bool
		if entry, ok = m.entryForDaemonHost(hid); !ok {
			// entryForDaemonHost skips a host without a connection object, which
			// is how a manager can hold the adopted active host (connOf).
			if m.daemon.HostID() != hid {
				return nil, "", "is not on a connected daemon"
			}
			entry = m.activeHost
		}
		scoped = true
	}
	var found *pb.Sandbox
	var host string
	for _, row := range m.allRows() {
		if row.sb.GetId() != id || (scoped && row.host != entry) {
			continue
		}
		if found != nil {
			return nil, "", "is on more than one host — open it from Sandboxes"
		}
		found, host = row.sb, row.host
	}
	if found == nil {
		if scoped {
			on := m.hostDisplayName(entry)
			if on == "" { // the lone local daemon has no host entry
				on = r.GetSandboxHostId()
			}
			return nil, "", "was not found on " + on + " (still loading, or destroyed)"
		}
		return nil, "", "was not found on a connected daemon"
	}
	return found, host, ""
}

// renderOverview renders the four sections; focus is the cursor's line.
func (m Model) renderOverview() (lines []string, focus int) {
	items := m.overviewItems()
	cur := m.jobs.cursors[jobsOverview]
	titles := [...]string{"Upcoming", "Queued", "In progress", fmt.Sprintf("History (latest %d)", jobsOverviewHistory)}
	i := 0
	for section, title := range titles {
		if section > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, sectionStyle.Render(title))
		n := 0
		for ; i < len(items) && items[i].section == section; i++ {
			if i == cur {
				focus = len(lines)
			}
			line := ""
			if items[i].ev != nil {
				line = upcomingLine(*items[i].ev)
			} else {
				line = m.jobsRunLine(*items[i].run)
			}
			lines = append(lines, cursorMark(i == cur)+m.jobsRowStyle(itemTarget(items[i]).host, line))
			n++
		}
		if n == 0 {
			lines = append(lines, "  "+dimStyle.Render("(none)"))
		}
	}
	return lines, focus
}
