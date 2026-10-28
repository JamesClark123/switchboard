package ui

import (
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The lane picker (SWI-18, contracts/lanes-ui.md "Lane picker overlay"): one
// overlay with four uses — a worker's lanes, a lane's members, and the lane
// steps of attach and launch. Rows toggle with space, enter saves, esc closes,
// up/down/k/j move. It remembers what it opened with, sent as the expected
// value of a membership save (FR-307a). It is a plain value its host drives;
// saving is the host's.

// pickMode is the picker's use.
type pickMode int

const (
	pickWorkerLanes pickMode = iota // a worker's lanes (SetWorkerLanes)
	pickLaneMembers                 // a lane's members (SetLaneMembers)
	pickAttach                      // lanes for an attach (AttachWorker.lane_ids)
	pickLaunch                      // lanes for a launch (GetWorkerBundle.lane_ids)
)

// pickRow is one toggleable row: a lane or a worker.
type pickRow struct {
	id, label string
}

// lanePicker is the overlay's state.
type lanePicker struct {
	mode    pickMode
	title   string
	header  string // dim line under the title (e.g. "none ticked = open lane")
	rows    []pickRow
	ticked  map[string]bool
	cursor  int
	offset  int // first row shown (the list scrolls)
	subject string
	// opened is the value the picker opened with (expected ids; for a worker
	// also its lanes-pending flag).
	opened        []string
	openedPending bool
	status        string
	busy          bool // a save or fetch is out
}

// pickAction is what a key asks the host to do.
type pickAction int

const (
	pickNone  pickAction = iota
	pickSave             // enter: save the ticked ids
	pickClose            // esc: close without saving
	pickAdd              // a, on an empty worker picker: open the lane form
)

// newLanePicker opens a picker on rows with ticked pre-set.
func newLanePicker(mode pickMode, title, subject string, rows []pickRow, ticked []string) *lanePicker {
	p := &lanePicker{mode: mode, title: title, subject: subject, rows: rows, ticked: map[string]bool{}, opened: slices.Clone(ticked)}
	for _, id := range ticked {
		p.ticked[id] = true
	}
	if mode == pickWorkerLanes {
		p.header = "none ticked = open lane"
	}
	return p
}

// laneRows are an orchestrator's lanes as picker rows.
func laneRows(rec *pb.Orchestrator) []pickRow {
	rows := make([]pickRow, 0, len(rec.GetLanes()))
	for _, l := range rec.GetLanes() {
		rows = append(rows, pickRow{id: l.GetId(), label: l.GetName()})
	}
	return rows
}

// memberRows are an orchestrator's non-reasoning workers, `name @ host`.
func memberRows(det *pb.OrchestratorDetail) []pickRow {
	var rows []pickRow
	for _, w := range det.GetWorkers() {
		if w.GetReasoning() {
			continue
		}
		rows = append(rows, pickRow{id: w.GetSandboxId(), label: workerName(w) + " @ " + w.GetHostId()})
	}
	return rows
}

// workerName is a worker view's display name, else its short id.
func workerName(w *pb.WorkerView) string {
	if w.GetDisplayName() != "" {
		return w.GetDisplayName()
	}
	return short(w.GetSandboxId())
}

// selected is the ticked ids in row order.
func (p *lanePicker) selected() []string {
	out := []string{}
	for _, r := range p.rows {
		if p.ticked[r.id] {
			out = append(out, r.id)
		}
	}
	return out
}

// reload replaces the rows and ticks (a stale save, FR-307a), keeping the
// cursor on the same row id when it is still there.
func (p *lanePicker) reload(rows []pickRow, ticked []string, pending bool) {
	at := ""
	if p.cursor < len(p.rows) {
		at = p.rows[p.cursor].id
	}
	p.rows, p.opened, p.openedPending = rows, slices.Clone(ticked), pending
	p.ticked = map[string]bool{}
	for _, id := range ticked {
		p.ticked[id] = true
	}
	p.cursor = 0
	for i, r := range rows {
		if r.id == at {
			p.cursor = i
		}
	}
	p.busy = false
}

// update handles one key.
func (p *lanePicker) update(msg tea.KeyMsg) pickAction {
	if p.busy {
		if msg.String() == "esc" {
			return pickClose
		}
		return pickNone
	}
	switch msg.String() {
	case "esc":
		return pickClose
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(p.rows)-1 {
			p.cursor++
		}
	case " ", "space":
		if len(p.rows) > 0 {
			id := p.rows[p.cursor].id
			p.ticked[id] = !p.ticked[id]
		}
	case "enter":
		if p.mode == pickLaneMembers || len(p.rows) > 0 || p.mode == pickWorkerLanes {
			return pickSave
		}
	case "a":
		if len(p.rows) == 0 && p.mode == pickWorkerLanes {
			return pickAdd
		}
	}
	return pickNone
}

// view renders the overlay at most height rows tall; the list scrolls.
func (p *lanePicker) view(width, height int) string {
	lines := []string{sectionStyle.Render(sanitizeLine(p.title, 0))}
	if p.header != "" {
		lines = append(lines, dimStyle.Render(p.header))
	}
	lines = append(lines, "")
	if len(p.rows) == 0 {
		switch p.mode {
		case pickWorkerLanes:
			lines = append(lines, dimStyle.Render("no lanes yet — a to add one"))
		case pickLaneMembers:
			lines = append(lines, dimStyle.Render("(no workers)"))
		default:
			lines = append(lines, dimStyle.Render("(no lanes)"))
		}
	}
	room := max(height-8, 3)
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+room {
		p.offset = p.cursor - room + 1
	}
	end := min(p.offset+room, len(p.rows))
	if p.offset > 0 {
		lines = append(lines, dimStyle.Render("  ↑ more"))
	}
	for i := p.offset; i < end; i++ {
		r := p.rows[i]
		box := "[ ] "
		if p.ticked[r.id] {
			box = "[x] "
		}
		label := box + fitCol(r.label, max(width-8, 8))
		if i == p.cursor {
			lines = append(lines, cursorBarStyle.Render("> ")+selectedStyle.Render(label))
		} else {
			lines = append(lines, "  "+label)
		}
	}
	if end < len(p.rows) {
		lines = append(lines, dimStyle.Render("  ↓ more"))
	}
	if p.busy {
		lines = append(lines, "", dimStyle.Render("saving…"))
	}
	if p.status != "" {
		lines = append(lines, "", statusErrStyle.Render(sanitizeTerminalText(p.status)))
	}
	help := "space toggle · enter save · esc cancel"
	switch p.mode {
	case pickAttach:
		help = "space toggle · enter attach · esc cancel"
	case pickLaunch:
		help = "space toggle · enter continue · esc back"
	}
	if len(p.rows) == 0 && p.mode == pickWorkerLanes {
		help = "a add a lane · esc cancel"
	}
	lines = append(lines, "", helpStyle.Render(help))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// capNames joins names, sanitised, at most five then ", and N more".
func capNames(names []string) string {
	const shown = 5
	out := make([]string, 0, min(len(names), shown))
	for i, n := range names {
		if i == shown {
			break
		}
		out = append(out, sanitizeLine(n, 0))
	}
	s := strings.Join(out, ", ")
	if more := len(names) - shown; more > 0 {
		s += ", and " + itoa(more) + " more"
	}
	return s
}
