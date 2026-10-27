package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestQueueDerivation(t *testing.T) {
	orchs := []jobsOrch{
		jo("alpha", "a-bot", nil, &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "q"}}, InFlight: []*pb.Run{{Id: "i"}}, History: finished("a", 30, 0)}),
		jo("beta", "b-bot", nil, &pb.ListRunsResponse{History: finished("b", 30, 30*time.Second)}),
	}
	all := deriveJobs(orchs, 1).queue
	if len(all) != 62 || all[0].run.GetId() != "q" || all[1].run.GetId() != "i" || all[2].run.GetId() != "a00" || all[3].run.GetId() != "b00" {
		t.Fatalf("queued ++ in progress ++ all history newest first: %d %s", len(all), runIDs(all[:4]))
	}
	if got := filterRuns(all, orchs[1].target.key()); len(got) != 30 || got[0].target.name != "b-bot" {
		t.Errorf("filter keeps one orchestrator: %d", len(got))
	}
	if len(filterRuns(all, "")) != 62 {
		t.Error("no filter keeps all")
	}
	if pageCount(0) != 1 || pageCount(25) != 1 || pageCount(26) != 2 || pageCount(62) != 3 {
		t.Error("pageCount")
	}
	if rows, p := pageOf(all, 2); len(rows) != 12 || p != 2 {
		t.Errorf("last page = %d rows (page %d)", len(rows), p)
	}
	if rows, p := pageOf(all, 9); p != 2 || len(rows) != 12 {
		t.Error("a page beyond the end clamps to the last page")
	}
	if _, p := pageOf(all, -1); p != 0 {
		t.Error("a negative page clamps to the first")
	}
	if rows, p := pageOf(nil, 3); p != 0 || len(rows) != 0 {
		t.Error("an empty list has one empty page")
	}
}

// queueHosts wires 30 + 20 finished runs on two hosts, plus a queued run on beta.
func queueHosts(t *testing.T) (Model, *fakeDaemon, *fakeDaemon) {
	t.Helper()
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	a.orchRuns = &pb.ListRunsResponse{History: finished("a", 30, 0)}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	b.orchRuns = &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-qb", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}, History: finished("b", 20, 30*time.Second)}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)
	m.jobs.tab = jobsQueue
	return m, a, b
}

// US3: page through every run, filter to one orchestrator and clear it, and act
// on a run from the Queue tab.
func TestQueueTabPagingFilterAndActions(t *testing.T) {
	m, _, b := queueHosts(t)
	v := m.View()
	if !strings.Contains(v, "filter: all · page 1 of 3 (51 runs)") || !strings.Contains(v, "run-qb") && !strings.Contains(v, "triage") {
		t.Errorf("header and first page:\n%s", v)
	}
	if h := m.renderHelp(m.jobsHelp()); !strings.Contains(h, "filter") || !strings.Contains(h, "page") || !strings.Contains(h, "cancel") {
		t.Errorf("queue help = %q", h)
	}
	if n := m.jobsRowCount(jobsQueue); n != jobsQueuePageSize {
		t.Errorf("a page holds %d runs, got %d", jobsQueuePageSize, n)
	}
	// Next / previous page with ] [ and pgdown / pgup; the end does not wrap.
	m, _ = update(m, press("j"))
	m, _ = update(m, press("]"))
	if m.jobs.queuePage != 1 || m.jobs.cursors[jobsQueue] != 0 || !strings.Contains(m.View(), "page 2 of 3") {
		t.Errorf("] goes to page 2 with the cursor at the top; page %d cursor %d", m.jobs.queuePage, m.jobs.cursors[jobsQueue])
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgDown})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.jobs.queuePage != 2 || m.jobsRowCount(jobsQueue) != 1 {
		t.Errorf("last page: %d with %d rows", m.jobs.queuePage, m.jobsRowCount(jobsQueue))
	}
	m, _ = update(m, press("["))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgUp})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.jobs.queuePage != 0 {
		t.Errorf("[ / pgup back to page 1, got %d", m.jobs.queuePage)
	}
	// f cycles all → alpha → beta → all; the filter is named with its host.
	m, _ = update(m, press("f"))
	if !strings.Contains(m.View(), "filter: alpha-bot @ alpha · page 1 of 2 (30 runs)") {
		t.Errorf("filter alpha:\n%s", m.View())
	}
	m, _ = update(m, press("f"))
	if !strings.Contains(m.View(), "filter: beta-bot @ beta · page 1 of 1 (21 runs)") || strings.Contains(m.View(), "alpha-bot @ alpha") {
		t.Errorf("filter beta:\n%s", m.View())
	}
	// x on beta's queued run cancels it on beta.
	m, _ = update(m, press("x"))
	m, cmd := update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "CancelRun o-b run-qb") || m.screen != screenJobs {
		t.Errorf("cancel from the queue: %q", b.orchCallsJoined())
	}
	// o on a finished run opens its results.
	m, _ = update(m, press("j"))
	m, _ = update(m, press("o"))
	if m.jobs.results == nil {
		t.Error("o on a finished run opens the results")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("f"))
	if !strings.Contains(m.View(), "filter: all") {
		t.Error("f wraps back to all")
	}
	// F clears a filter.
	m, _ = update(m, press("f"))
	m, _ = update(m, press("F"))
	if m.jobs.queueFilter != "" || !strings.Contains(m.View(), "filter: all · page 1 of 3") {
		t.Error("F clears the filter")
	}
	// A key with no run under the cursor is a no-op.
	m.jobs.cursors[jobsQueue] = 99
	if _, c := update(m, press("x")); c != nil {
		t.Error("no run ⇒ no action")
	}
}

// The filtered orchestrator disappearing resets the filter (saying so) and a page
// beyond the end moves to the last page (spec edge cases).
func TestQueueTabVanishedFilterAndClamp(t *testing.T) {
	m, _, b := queueHosts(t)
	m, _ = update(m, press("f"))
	m, _ = update(m, press("f")) // beta
	m.jobs.queuePage = 5
	m.jobsChanged()
	if m.jobs.queuePage != 0 {
		t.Errorf("a page beyond the end clamps, got %d", m.jobs.queuePage)
	}
	b.orchestrators = nil
	m.sub = nil
	m, cmd := update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "hostb", OrchestratorId: "o-b"}}}})
	m = feed(m, cmd)
	if m.jobs.queueFilter != "" || !strings.Contains(m.jobs.status, "filter reset to all orchestrators — beta-bot @ beta is no longer available") {
		t.Errorf("filter %q status %q", m.jobs.queueFilter, m.jobs.status)
	}
	if !strings.Contains(m.View(), "filter: all · page 1 of 2 (30 runs)") {
		t.Errorf("all runs again:\n%s", m.View())
	}
	// A filter on an orchestrator with no runs says so.
	m, _ = update(m, press("f"))
	m.jobs.orchs[0].runs = &pb.ListRunsResponse{}
	m.jobsChanged()
	if !strings.Contains(m.View(), "(no runs)") || !strings.Contains(m.View(), fmt.Sprintf("page 1 of 1 (%s)", plural(0, "run", "runs"))) {
		t.Errorf("empty filtered list:\n%s", m.View())
	}
}
