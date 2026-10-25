package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestSanitizeTerminalText(t *testing.T) {
	in := "ok\x1b[31m red\x1b[0m\r\n\ttabbed\x07 bell\x1b]0;title\x07 done"
	got := sanitizeTerminalText(in)
	if strings.ContainsAny(got, "\x1b\x07\r\t") {
		t.Errorf("control sequences must be stripped: %q", got)
	}
	if !strings.Contains(got, "ok red\n    tabbed bell") || !strings.Contains(got, "done") {
		t.Errorf("text and newlines kept: %q", got)
	}
}

func TestRunResultsBody(t *testing.T) {
	tg := orchTarget{host: "h", hostName: "alpha", id: "o-a", name: "alpha-bot"}
	long := strings.Repeat("word ", 400) + "THE-END"
	body := runResultsBody(tg, &pb.Run{Id: "r1", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, EndedAt: at(-5 * time.Minute), Summary: long}, 40)
	for _, want := range []string{"review-pr", "alpha-bot @ alpha", "completed", "5m ago", "THE-END"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	if strings.Count(body, "word") != 400 {
		t.Error("nothing is truncated")
	}
	for _, line := range strings.Split(body, "\n") {
		if len(line) > 80 {
			t.Errorf("lines are wrapped to the overlay: %d", len(line))
		}
	}
	cases := []struct {
		run  *pb.Run
		want string
	}{
		{&pb.Run{State: pb.RunState_RUN_STATE_COMPLETED, Summary: "lgtm", ClosedByDeveloper: true}, "closed by developer: lgtm"},
		{&pb.Run{State: pb.RunState_RUN_STATE_FAILED, Summary: "partial", Reason: "agent exited"}, "reason: agent exited"},
		{&pb.Run{State: pb.RunState_RUN_STATE_CANCELLED, Reason: "cancelled by the developer"}, "reason: cancelled by the developer"},
		{&pb.Run{State: pb.RunState_RUN_STATE_COMPLETED, Summary: "  "}, noResultText},
	}
	for _, c := range cases {
		if got := runResultsBody(tg, c.run, 60); !strings.Contains(got, c.want) {
			t.Errorf("body for %v missing %q:\n%s", c.run.GetState(), c.want, got)
		}
	}
	if got := runResultsBody(tg, &pb.Run{State: pb.RunState_RUN_STATE_FAILED, Summary: "partial", Reason: "x"}, 60); !strings.Contains(got, "partial") {
		t.Error("a failed run's summary is kept alongside the reason")
	}
	if got := runResultsBody(tg, &pb.Run{Summary: "\x1b[2Jcleared"}, 60); strings.Contains(got, "\x1b[2J") {
		t.Error("an agent's escape sequences never reach the terminal")
	}
}

// The overlay scrolls through a report longer than it, shows its position, and
// closes with esc.
func TestRunResultsOverlayScrolls(t *testing.T) {
	var lines []string
	for i := 0; i < 80; i++ {
		lines = append(lines, "line "+string(rune('A'+i%26)))
	}
	runs := &pb.ListRunsResponse{History: []*pb.Run{{Id: "run-1", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, Summary: strings.Join(lines, "\n") + "\nLAST-LINE"}}}
	m, _ := jobsWith(t, jobsOverview, nil, runs)
	m, _ = update(m, press("o"))
	if m.jobs.results == nil {
		t.Fatal("o opens the overlay")
	}
	v := m.View()
	if !strings.Contains(v, "Results · run run-1") || !strings.Contains(v, "0% scrolled") || strings.Contains(v, "LAST-LINE") {
		t.Errorf("the overlay starts at the top:\n%s", v)
	}
	if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "scroll") || !strings.Contains(h, "close") {
		t.Errorf("help = %q", h)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.jobs.results.vp.YOffset != 1 {
		t.Errorf("down scrolls one line, offset %d", m.jobs.results.vp.YOffset)
	}
	for i := 0; i < 10; i++ {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !m.jobs.results.vp.AtBottom() || !strings.Contains(m.View(), "LAST-LINE") {
		t.Error("pgdown reaches the end of the report")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.jobs.results.vp.AtBottom() {
		t.Error("pgup scrolls back")
	}
	// Other Jobs keys are swallowed while it is open.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.jobs.tab != jobsOverview {
		t.Error("the overlay owns the keyboard")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.jobs.results != nil || m.screen != screenJobs {
		t.Error("esc closes the overlay, staying on Jobs")
	}
	// A short report shows no scroll position.
	m.jobs.results = nil
	mm, _ := m.openRunResults(m.jobs.orchs[0].target, &pb.Run{Id: "r2", State: pb.RunState_RUN_STATE_COMPLETED, Summary: "short"})
	m = mm.(Model)
	if strings.Contains(m.runResultsModal(), "scrolled") {
		t.Error("a report that fits shows no position")
	}
}
