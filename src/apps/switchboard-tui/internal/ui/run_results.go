package ui

import (
	"fmt"
	"strings"
	"unicode"

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
// scrolled with ↑/↓/pgup/pgdown. The text comes from an agent, so terminal
// control sequences are stripped before it is drawn.

// runResultsState backs the open overlay.
type runResultsState struct {
	title string
	vp    viewport.Model
}

// noResultText is shown when a finished run reported nothing.
const noResultText = "no result was reported"

// sanitizeTerminalText strips escape sequences and control characters from text
// that came from an agent, keeping newlines; tabs become spaces.
func sanitizeTerminalText(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case unicode.IsControl(r):
			// dropped (\r, bell, a stray ESC …)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// runResultsBody is the overlay's text for a run, wrapped to width.
func runResultsBody(t orchTarget, r *pb.Run, width int) string {
	head := []string{
		dimStyle.Render("action        ") + r.GetAction(),
		dimStyle.Render("orchestrator  ") + t.label(),
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
	return strings.Join(head, "\n") + wrapped
}

// openRunResults opens the overlay for a finished run.
func (m Model) openRunResults(t orchTarget, r *pb.Run) (tea.Model, tea.Cmd) {
	w := m.modalInnerWidth()
	h := max(m.height-14, 5)
	vp := viewport.New(w, h)
	vp.SetContent(runResultsBody(t, r, w))
	m.jobs.results = &runResultsState{title: "Results · run " + r.GetId(), vp: vp}
	return m, nil
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
