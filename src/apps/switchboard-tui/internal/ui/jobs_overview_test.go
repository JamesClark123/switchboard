package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// at is a timestamp d from now.
func at(d time.Duration) *timestamppb.Timestamp { return timestamppb.New(time.Now().Add(d)) }

// jo builds a loaded orchestrator for the derivation helpers.
func jo(host, name string, events []*pb.TimedEvent, runs *pb.ListRunsResponse) jobsOrch {
	o := &pb.Orchestrator{Id: "id-" + name, Name: name, TimedEvents: events}
	if runs == nil {
		runs = &pb.ListRunsResponse{}
	}
	return jobsOrch{target: targetOf(host, host, o), summary: &pb.OrchestratorSummary{Orchestrator: o}, runs: runs}
}

func runIDs(rows []jobsRunRow) string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.run.GetId())
	}
	return strings.Join(ids, ",")
}

// finished makes n finished runs ending one minute apart, the newest offset first.
func finished(prefix string, n int, offset time.Duration) []*pb.Run {
	var out []*pb.Run
	for i := 0; i < n; i++ {
		out = append(out, &pb.Run{Id: fmt.Sprintf("%s%02d", prefix, i), Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, EndedAt: at(-offset - time.Duration(i)*time.Minute), Summary: "done " + prefix})
	}
	return out
}

func TestOverviewDerivationHelpers(t *testing.T) {
	orchs := []jobsOrch{
		jo("alpha", "a-bot", []*pb.TimedEvent{
			{Id: "e1", Name: "later", Enabled: true, NextFireAt: at(10 * time.Minute)},
			{Id: "e2", Name: "off", Enabled: false, NextFireAt: at(time.Minute)},
			{Id: "e3", Name: "unscheduled", Enabled: true},
		}, &pb.ListRunsResponse{
			Queued:   []*pb.Run{{Id: "q2", ReceivedAt: at(-time.Minute)}, {Id: "q-unset"}},
			InFlight: []*pb.Run{{Id: "i2", StartedAt: at(-time.Minute)}},
			History:  finished("a", 10, 0),
		}),
		jo("beta", "b-bot", []*pb.TimedEvent{{Id: "e4", Name: "soon", Enabled: true, NextFireAt: at(time.Minute)}}, &pb.ListRunsResponse{
			Queued:   []*pb.Run{{Id: "q1", ReceivedAt: at(-time.Hour)}},
			InFlight: []*pb.Run{{Id: "i1", StartedAt: at(-time.Hour)}},
			History:  finished("b", 10, 30*time.Second),
		}),
	}
	up := jobsUpcoming(orchs)
	if len(up) != 2 || up[0].event.GetName() != "soon" || up[1].event.GetName() != "later" || up[0].target.name != "b-bot" {
		t.Errorf("upcoming = %+v (enabled with a next fire only, soonest first)", up)
	}
	if got := runIDs(jobsQueued(orchs)); got != "q-unset,q1,q2" {
		t.Errorf("queued by receipt = %q", got)
	}
	if got := runIDs(jobsInProgress(orchs)); got != "i1,i2" {
		t.Errorf("in progress by start = %q", got)
	}
	h := jobsHistory(orchs, jobsOverviewHistory)
	if len(h) != 15 {
		t.Fatalf("history is capped at 15 across orchestrators, got %d", len(h))
	}
	if got := runIDs(h[:4]); got != "a00,b00,a01,b01" {
		t.Errorf("history newest first, merged = %q", got)
	}
	if len(jobsHistory(orchs, 0)) != 20 {
		t.Error("no limit keeps every finished run")
	}
	if tsTime(nil) != (time.Time{}) || ago(nil) != "—" || ago(at(-2*time.Hour)) != "2h ago" {
		t.Error("timestamp helpers")
	}
}

// overviewHosts wires two fake hosts with runs in every state and timed events.
func overviewHosts(t *testing.T) (Model, *fakeDaemon, *fakeDaemon) {
	t.Helper()
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", Actions: []*pb.Action{reviewAction()},
		TimedEvents: []*pb.TimedEvent{
			{Id: "tev-a", Name: "poll", Action: "review-pr", Enabled: true, NextFireAt: at(3 * time.Minute)},
			{Id: "tev-off", Name: "paused", Action: "review-pr", Enabled: false, NextFireAt: at(time.Minute)},
		}}}}}
	a.orchRuns = &pb.ListRunsResponse{
		Queued:  []*pb.Run{{Id: "run-qa", Action: "review-pr", State: pb.RunState_RUN_STATE_QUEUED, Position: 1, ReceivedAt: at(-time.Minute)}},
		History: finished("a", 12, 0),
	}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-b", Name: "beta-bot", Actions: []*pb.Action{reviewAction()},
		TimedEvents: []*pb.TimedEvent{{Id: "tev-b", Name: "nightly", Action: "review-pr", Enabled: true, DedupeDisabled: true, NextFireAt: at(time.Hour)}}}}}}
	b.orchRuns = &pb.ListRunsResponse{
		Queued: []*pb.Run{{Id: "run-qb", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1, ReceivedAt: at(-time.Hour), EventKind: pb.EventKind_EVENT_KIND_TIMED, OriginLabel: "nightly", Origin: "tev-b"}},
		InFlight: []*pb.Run{
			{Id: "run-rb", Action: "review-pr", State: pb.RunState_RUN_STATE_RUNNING, SandboxId: "worker-wa", StartedAt: at(-2 * time.Minute)},
			{Id: "run-wb", Action: "review-pr", State: pb.RunState_RUN_STATE_AWAITING_REVIEW, SandboxId: "worker-wb", StartedAt: at(-time.Minute), EventKind: pb.EventKind_EVENT_KIND_TRIGGERED, OriginLabel: "worker-two"},
		},
		History: append(finished("b", 8, 30*time.Second),
			&pb.Run{Id: "run-long", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, EndedAt: at(time.Second), Summary: strings.Repeat("Reviewed PR 412 and left comments. ", 20)}),
	}
	m, _, _ := twoFakeHosts(t, a, b)
	return openJobs(m), a, b
}

// US1: the Overview shows the four sections across both hosts, every row
// labelled, history capped at 15, disabled events absent, the unreachable host
// named, and it follows a state change live.
func TestOverviewAcrossTwoHosts(t *testing.T) {
	m, _, b := overviewHosts(t)
	v := m.View()
	for _, want := range []string{"Upcoming", "Queued", "In progress", "History (latest 15)",
		"poll", "→ review-pr", "alpha-bot @ alpha", "next in", "nightly", "beta-bot @ beta",
		"#1", "timed nightly", "received", "running", "on worker-", "awaiting review", "triggered by worker-two",
		"completed", "ended", "Reviewed PR 412", "could not read: gamma"} {
		if !strings.Contains(v, want) {
			t.Errorf("overview missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "paused") {
		t.Error("a disabled event is not upcoming")
	}
	items := m.overviewItems()
	count := map[int]int{}
	for _, it := range items {
		count[it.section]++
	}
	if count[sectionUpcoming] != 2 || count[sectionQueued] != 2 || count[sectionInProgress] != 2 || count[sectionHistory] != 15 {
		t.Errorf("section sizes = %v", count)
	}
	if items[0].ev.event.GetName() != "poll" || items[2].run.run.GetId() != "run-qb" || items[len(items)-15].run.run.GetId() != "run-long" {
		t.Error("section ordering: soonest event, oldest receipt, newest history first")
	}
	// The cursor walks every row and the view keeps it visible.
	for i := 0; i < len(items)+3; i++ {
		m, _ = update(m, press("j"))
	}
	if m.jobs.cursors[jobsOverview] != len(items)-1 || !strings.Contains(m.View(), "> ") {
		t.Errorf("cursor = %d of %d", m.jobs.cursors[jobsOverview], len(items))
	}
	// Live: beta's running run completes; an orchestrator_changed event reloads it.
	b.orchRuns = &pb.ListRunsResponse{History: []*pb.Run{{Id: "run-rb", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, EndedAt: at(2 * time.Second), Summary: "all green"}}}
	m.sub = nil
	m, cmd := update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "hostb", OrchestratorId: "o-b"}}}})
	m = feed(m, cmd)
	if got := m.overviewItems(); len(got) < 4 || got[2].section != sectionQueued || got[3].section != sectionHistory {
		t.Fatalf("after the change: %+v", got)
	}
	if v := m.View(); !strings.Contains(v, "all green") || strings.Contains(v, "on worker-") {
		t.Errorf("the overview should follow the change:\n%s", v)
	}
	if m.jobs.cursors[jobsOverview] >= len(m.overviewItems()) {
		t.Error("the cursor is clamped after rows disappear")
	}
}

// overviewAt moves the Overview cursor onto the row with that run id (or event name).
func overviewAt(t *testing.T, m Model, id string) Model {
	t.Helper()
	for i, it := range m.overviewItems() {
		if (it.run != nil && it.run.run.GetId() == id) || (it.ev != nil && it.ev.event.GetName() == id) {
			m.jobs.cursors[jobsOverview] = i
			return m
		}
	}
	t.Fatalf("no overview row %q", id)
	return m
}

// US2: each Overview action reaches the right host and orchestrator.
func TestOverviewActionsReachTheRightOrchestrator(t *testing.T) {
	m, a, b := overviewHosts(t)
	a.orchCalls, b.orchCalls = nil, nil

	// x on beta's queued run cancels it on beta.
	m = overviewAt(t, m, "run-qb")
	m, _ = update(m, press("x"))
	if !strings.Contains(m.View(), "beta-bot @ beta") {
		t.Error("the confirm names the orchestrator")
	}
	m, cmd := update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "CancelRun o-b run-qb") || strings.Contains(a.orchCallsJoined(), "CancelRun") {
		t.Errorf("cancel should reach beta only: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	// enter on a run fetches its prompt from its own daemon.
	m, cmd = update(m, press("enter"))
	m = feed(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "GetRun o-b run-qb") || m.jobs.prompt == nil {
		t.Errorf("prompt: %q", b.orchCallsJoined())
	}
	m, _ = update(m, press("q"))
	// m on beta's awaiting-review run opens the dialog for beta.
	m = overviewAt(t, m, "run-wb")
	m, _ = update(m, press("m"))
	if m.jobs.form == nil || m.jobs.formTarget.host != "hostb" {
		t.Fatalf("m should open the dialog for beta: %+v", m.jobs.formTarget)
	}
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), `MarkWorkerFree o-b worker-wb false ""`) {
		t.Errorf("mark free calls = %q", b.orchCallsJoined())
	}
	// o is not offered on an in-progress run; pressing it says so.
	m = overviewAt(t, m, "run-rb")
	if strings.Contains(m.renderHelp(m.jobsHelp()), "open results") {
		t.Error("o must not be offered on an in-progress run")
	}
	m, _ = update(m, press("o"))
	if m.jobs.results != nil || !strings.Contains(m.jobs.status, "finished runs") {
		t.Errorf("o on a running run: %q", m.jobs.status)
	}
	// n does nothing on a run row; o on the long finished run opens the full report.
	if _, c := update(m, press("n")); c != nil {
		t.Error("n on a run row is a no-op")
	}
	m = overviewAt(t, m, "run-long")
	m, _ = update(m, press("o"))
	if m.jobs.results == nil || !strings.Contains(m.View(), "Results · run run-long") {
		t.Fatal("o should open the results overlay")
	}
	m, _ = update(m, press("q"))
	if m.jobs.results != nil {
		t.Error("q closes the results overlay")
	}
}

// US2: Enqueue now from an Upcoming row, with the SWI-4 rules.
func TestOverviewEnqueueNow(t *testing.T) {
	m, a, b := overviewHosts(t)
	m = overviewAt(t, m, "poll")
	if !strings.Contains(m.renderHelp(m.jobsHelp()), "enqueue now") {
		t.Error("an upcoming row offers n enqueue now")
	}
	for _, k := range []string{"enter", "x", "m", "o"} {
		if _, c := update(m, press(k)); c != nil {
			t.Errorf("%q on an upcoming row is a no-op", k)
		}
	}
	m, cmd := update(m, press("n"))
	if m.screen == screenConfirm || cmd == nil {
		t.Fatal("dedupe on: no confirmation")
	}
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "FireTimedEvent o-a tev-a") || strings.Contains(b.orchCallsJoined(), "FireTimedEvent") {
		t.Errorf("fire should reach alpha only: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	if !strings.Contains(m.status, "enqueued poll on alpha-bot @ alpha") {
		t.Errorf("status = %q", m.status)
	}
	// An immediate repeat is ignored with a hint.
	m = overviewAt(t, m, "poll")
	if m, cmd = update(m, press("n")); cmd == nil || !strings.Contains(m.jobs.status, "repeat ignored") {
		t.Errorf("a repeat within the window: %q", m.jobs.status)
	}
	// beta's event has dedupe off and an open run of it: confirm first.
	m = overviewAt(t, m, "nightly")
	m, cmd = update(m, press("n"))
	if m.screen != screenConfirm || cmd != nil || !strings.Contains(m.View(), "run-qb") {
		t.Fatalf("dedupe off with an open run should confirm; screen %v", m.screen)
	}
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "FireTimedEvent o-b tev-b") || m.screen != screenJobs {
		t.Errorf("confirmed fire: %q screen %v", b.orchCallsJoined(), m.screen)
	}
	// The event's runs are found by orchestrator; an unknown one has none.
	if got := m.jobsRunsOf(orchTarget{host: "nowhere", id: "x"}); len(got.GetQueued()) != 0 {
		t.Error("unknown orchestrator ⇒ no runs")
	}
}

// The Overview's empty sections say so.
func TestOverviewEmptySections(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "solo")}}
	m := openJobs(sized(New(d, "/work")))
	if got := strings.Count(m.View(), "(none)"); got != 4 {
		t.Errorf("four empty sections, got %d:\n%s", got, m.View())
	}
	if m.renderHelp(m.jobsHelp()) == "" || m.overviewRowHelp() != nil {
		t.Error("no row ⇒ no row keys")
	}
	if _, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Error("enter on an empty overview is a no-op")
	}
}
