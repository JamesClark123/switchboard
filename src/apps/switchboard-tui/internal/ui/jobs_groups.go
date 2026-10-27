package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Jobs › Actions and Jobs › Events (feature 010, user story 4, FR-019..FR-021):
// today's per-orchestrator Actions and Events views made cross-orchestrator. One
// group per orchestrator under a `name @ host` heading, in the Jobs order; an
// empty group shows a line that is still a cursor position, so `a` can add the
// first action or event to it. `a` acts on the orchestrator of the cursor's
// group, the other keys on the highlighted item's — through the same target-based
// forms (orchestrator_forms.go), so every rule is today's.

// groupPos is one cursor position: an item of a group, or (item -1) the empty
// line of a group with no items.
type groupPos struct {
	orch int // index into jobs.orchs
	item int
}

// groupItemCount is how many items an orchestrator has on a tab.
func groupItemCount(o jobsOrch, t jobsTab) int {
	if t == jobsEvents {
		return len(o.target.rec.GetTimedEvents())
	}
	return len(o.target.rec.GetActions())
}

// groupPositions lists a grouped tab's cursor positions in display order.
func (m Model) groupPositions(t jobsTab) []groupPos {
	var out []groupPos
	for i, o := range m.jobs.orchs {
		n := groupItemCount(o, t)
		if n == 0 {
			out = append(out, groupPos{orch: i, item: -1})
		}
		for k := 0; k < n; k++ {
			out = append(out, groupPos{orch: i, item: k})
		}
	}
	return out
}

// groupCurrent resolves the cursor of a grouped tab: the orchestrator of its
// group and the highlighted item index (-1 on an empty group's line).
func (m Model) groupCurrent(t jobsTab) (jobsOrch, int, bool) {
	pos := m.groupPositions(t)
	c := m.jobs.cursors[t]
	if c >= len(pos) {
		return jobsOrch{}, -1, false
	}
	return m.jobs.orchs[pos[c].orch], pos[c].item, true
}

func (m Model) updateJobsActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	o, item, ok := m.groupCurrent(jobsActions)
	if !ok {
		return m, nil
	}
	t := o.target
	var cur *pb.Action
	if item >= 0 {
		cur = t.rec.GetActions()[item]
	}
	switch msg.String() {
	case "a":
		return m.openActionForm(surfaceJobs, t, nil)
	case "e":
		if cur != nil {
			return m.openActionForm(surfaceJobs, t, cur)
		}
	case "d":
		if cur != nil {
			return m.confirmDeleteAction(surfaceJobs, t, cur)
		}
	case "enter":
		if cur != nil {
			return m.openCallForm(surfaceJobs, t, cur)
		}
	}
	return m, nil
}

func (m Model) updateJobsEventsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	o, item, ok := m.groupCurrent(jobsEvents)
	if !ok {
		return m, nil
	}
	t := o.target
	var cur *pb.TimedEvent
	if item >= 0 {
		cur = t.rec.GetTimedEvents()[item]
	}
	switch msg.String() {
	case "a":
		return m.openEventForm(surfaceJobs, t, nil)
	case "e":
		if cur != nil {
			return m.openEventForm(surfaceJobs, t, cur)
		}
	case "d":
		if cur != nil {
			return m.confirmDeleteEvent(surfaceJobs, t, cur)
		}
	case " ":
		if cur != nil {
			return m, m.toggleEventCmd(surfaceJobs, t, cur)
		}
	case "n":
		if cur != nil {
			return m.enqueueNowKey(surfaceJobs, t, cur, o.runs)
		}
	}
	return m, nil
}

// groupsRowHelp is a grouped tab's keys for the highlighted position.
func (m Model) groupsRowHelp(t jobsTab) helpBindings {
	_, item, ok := m.groupCurrent(t)
	if !ok {
		return nil
	}
	hb := helpBindings{hkey("a", "add")}
	if item < 0 {
		return hb
	}
	if t == jobsEvents {
		return append(hb, hkey("e", "edit"), hkey("d", "delete"), hkey("space", "on/off"), hkey("n", "enqueue now"))
	}
	return append(hb, hkey("e", "edit"), hkey("d", "delete"), hkey("enter", "call"))
}

// renderGroups renders a grouped tab; focus is the cursor's line.
func (m Model) renderGroups(t jobsTab) (lines []string, focus int) {
	pos := m.groupPositions(t)
	cur := m.jobs.cursors[t]
	p := 0
	for i, o := range m.jobs.orchs {
		if i > 0 {
			lines = append(lines, "")
		}
		heading := sectionStyle.Render(sanitizeLine(o.target.label(), 0))
		if !m.jobsReachable(o.target.host) {
			note := "  (not connected)"
			if hs := m.jobs.hosts[o.target.host]; hs != nil && hs.lost != nil {
				note = "  (connection lost)"
			}
			heading = dimStyle.Render(sanitizeLine(o.target.label(), 0) + note)
		}
		lines = append(lines, heading)
		waiting := map[string]int{}
		if t == jobsEvents {
			for _, run := range o.runs.GetQueued() {
				if run.GetEventKind() == pb.EventKind_EVENT_KIND_TIMED {
					waiting[run.GetOrigin()]++
				}
			}
		}
		for ; p < len(pos) && pos[p].orch == i; p++ {
			if p == cur {
				focus = len(lines)
			}
			var line string
			switch {
			case pos[p].item < 0 && t == jobsEvents:
				line = dimStyle.Render("no events — press a to add one")
			case pos[p].item < 0:
				line = dimStyle.Render("no actions — press a to add one")
			case t == jobsEvents:
				ev := o.target.rec.GetTimedEvents()[pos[p].item]
				line = eventLine(ev, waiting[ev.GetId()])
			default:
				line = actionLine(o.target.rec.GetActions()[pos[p].item])
			}
			lines = append(lines, cursorMark(p == cur)+m.jobsRowStyle(o.target.host, line))
		}
	}
	return lines, focus
}
