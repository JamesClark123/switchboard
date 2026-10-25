package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Open results (feature 010, FR-012, research R5): a finished run's full
// reported result in a scrollable overlay over the Jobs screen — the action, the
// orchestrator and host, the state and end time, then the whole Run.summary (the
// developer's note when closed by hand) and, for a failed or cancelled run, the
// reason it ended. Nothing is truncated: the text is wrapped to the overlay and
// scrolled with ↑/↓/pgup/pgdown. The text comes from an agent, so it is drawn
// through sanitizeTerminalText (FR-029). The viewport is sized to the modal's
// content area (inside its padding) and re-fitted when the terminal is resized,
// so the whole modal, scroll position and hint included, stays on screen.

// runResultsState backs the open overlay.
type runResultsState struct {
	title  string
	target orchTarget
	run    *pb.Run
	vp     viewport.Model
}

// noResultText is shown when a finished run reported nothing.
const noResultText = "no result was reported"

// runResultsBody is the overlay's text for a run, wrapped to width.
func runResultsBody(t orchTarget, r *pb.Run, width int) string {
	head := []string{
		dimStyle.Render("action        ") + sanitizeLine(r.GetAction(), 0),
		dimStyle.Render("orchestrator  ") + sanitizeLine(t.label(), 0),
		dimStyle.Render("state         ") + runState(r),
		dimStyle.Render("ended         ") + ago(r.GetEndedAt()),
		"",
	}
	var body []string
	summary := sanitizeTerminalText(r.GetSummary())
	switch {
	case r.GetClosedByDeveloper():
		body = append(body, "closed by developer: "+summary)
	case strings.TrimSpace(summary) != "":
		body = append(body, summary)
	}
	if reason := sanitizeTerminalText(r.GetReason()); reason != "" {
		if len(body) > 0 {
			body = append(body, "")
		}
		body = append(body, "reason: "+reason)
	}
	if len(body) == 0 {
		body = append(body, dimStyle.Render(noResultText))
	}
	wrapped := ansi.Wrap(strings.Join(body, "\n"), max(width, 10), "")
	return strings.Join(head, "\n") + "\n" + wrapped
}

// runResultsChrome is the modal's height beyond the viewport: border (2),
// padding (2), title and gap (2), scroll position and gap (2), hint and gap (2).
const runResultsChrome = 10

// runResultsSize is the viewport's width and height: the modal's content area
// (its width minus modalStyle's horizontal padding), and the terminal height less
// the modal's chrome and a line of margin above and below.
func (m Model) runResultsSize() (int, int) {
	w := m.modalInnerWidth() - modalStyle.GetHorizontalPadding()
	h := max(m.height-runResultsChrome-2, 3)
	return w, h
}

// openRunResults opens the overlay for a finished run.
func (m Model) openRunResults(t orchTarget, r *pb.Run) (tea.Model, tea.Cmd) {
	m.jobs.results = &runResultsState{title: "Results · run " + sanitizeLine(r.GetId(), 0), target: t, run: r}
	m.fitRunResults()
	return m, nil
}

// fitRunResults (re)builds the overlay's viewport for the current terminal size,
// keeping the scroll position (clamped) across a resize.
func (m *Model) fitRunResults() {
	rs := m.jobs.results
	if rs == nil {
		return
	}
	w, h := m.runResultsSize()
	y := rs.vp.YOffset
	vp := viewport.New(w, h)
	vp.SetContent(runResultsBody(rs.target, rs.run, w))
	vp.SetYOffset(y)
	next := *rs
	next.vp = vp
	m.jobs.results = &next
}

func (m Model) updateRunResultsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.jobs.results = nil
		return m, nil
	}
	rs := *m.jobs.results
	var cmd tea.Cmd
	rs.vp, cmd = rs.vp.Update(msg)
	m.jobs.results = &rs
	return m, cmd
}

func (m Model) runResultsHelp() helpBindings {
	return helpBindings{hkey("↑/↓", "scroll"), hkey("pgup/pgdn", "page"), hkey("esc", "close")}
}

// runResultsModal renders the overlay.
func (m Model) runResultsModal() string {
	rs := m.jobs.results
	rows := []string{sectionStyle.Render(rs.title), "", rs.vp.View()}
	if !(rs.vp.AtTop() && rs.vp.AtBottom()) {
		rows = append(rows, "", dimStyle.Render(fmt.Sprintf("%d%% scrolled", int(rs.vp.ScrollPercent()*100))))
	}
	rows = append(rows, "", helpStyle.Render("↑/↓ scroll · pgup/pgdn page · esc close"))
	return modalStyle.Width(m.modalInnerWidth()).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}
