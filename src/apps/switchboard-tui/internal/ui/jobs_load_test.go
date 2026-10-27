package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// changedOrch delivers Event.orchestrator_changed for t and applies the replies.
func changedOrch(m Model, t orchTarget) Model {
	mm, cmd := m.syncOrchestratorChanged(t.host, t.id)
	return feed(mm.(Model), cmd)
}

// twoOrchs is one daemon hosting alpha-bot and beta-bot, each with one event.
func twoOrchs() *fakeDaemon {
	return &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{
		{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", Actions: []*pb.Action{reviewAction()},
			TimedEvents: []*pb.TimedEvent{{Id: "ev-a", Name: "poll-a", Action: "review-pr", IntervalSeconds: 300, Enabled: true, NextFireAt: at(time.Minute)}}}},
		{Orchestrator: &pb.Orchestrator{Id: "o-b", Name: "beta-bot", Actions: []*pb.Action{reviewAction()},
			TimedEvents: []*pb.TimedEvent{{Id: "ev-b", Name: "poll-b", Action: "review-pr", IntervalSeconds: 300, Enabled: true, NextFireAt: at(2 * time.Minute)}}}},
	}}
}

// The highlight stays on its item when a reload inserts or reorders rows, and an
// operation acts on that item (FR-031, review item 2).
func TestJobsCursorFollowsItsItem(t *testing.T) {
	d := twoOrchs()
	m := openJobs(sized(New(d, "/work")))
	m.jobs.tab = jobsEvents
	m, _ = update(m, press("j")) // beta's poll-b
	alpha := m.jobs.orchs[0].target
	d.orchestrators[0].Orchestrator.TimedEvents = append(d.orchestrators[0].Orchestrator.TimedEvents,
		&pb.TimedEvent{Id: "ev-a2", Name: "poll-a2", Action: "review-pr", IntervalSeconds: 300, Enabled: true})
	m = changedOrch(m, alpha)
	if o, item, _ := m.groupCurrent(jobsEvents); o.target.id != "o-b" || item != 0 || m.jobs.cursors[jobsEvents] != 2 {
		t.Fatalf("the cursor should stay on beta's poll-b: %s item %d index %d", o.target.label(), item, m.jobs.cursors[jobsEvents])
	}
	d.orchCalls = nil
	m, cmd := update(m, press(" "))
	m = feed2(m, cmd) // the toggle, then the reload it asks for
	if calls := d.orchCallsJoined(); !strings.Contains(calls, "PutTimedEvent o-b poll-b") {
		t.Errorf("space toggles the highlighted event: %q", calls)
	}

	// Upcoming re-sorts when a firing time moves; the highlight follows.
	d.orchestrators[1].Orchestrator.TimedEvents[0].Enabled = true
	d.orchestrators[1].Orchestrator.TimedEvents[0].NextFireAt = at(2 * time.Minute)
	m = changedOrch(m, m.jobs.orchs[1].target)
	m.jobs.tab = jobsOverview
	m = overviewAt(t, m, "poll-b")
	m, _ = update(m, press("k"))
	m, _ = update(m, press("j")) // recorded by a key
	d.orchestrators[1].Orchestrator.TimedEvents[0].NextFireAt = at(30 * time.Second)
	m = changedOrch(m, m.jobs.orchs[1].target)
	if it, _ := m.overviewCurrent(); it.ev == nil || it.ev.event.GetId() != "ev-b" || m.jobs.cursors[jobsOverview] != 0 {
		t.Fatalf("the Overview cursor follows poll-b to the top: %+v index %d", it.ev, m.jobs.cursors[jobsOverview])
	}
	// Gone: the nearest neighbour in the same place.
	d.orchestrators[1].Orchestrator.TimedEvents = nil
	m = changedOrch(m, m.jobs.orchs[1].target)
	if it, ok := m.overviewCurrent(); !ok || it.ev == nil || it.ev.event.GetId() != "ev-a" {
		t.Errorf("a removed item's cursor goes to its neighbour: %+v", it.ev)
	}
}

// On the Queue tab the page follows the highlighted run when newer runs push it
// to another page (FR-031).
func TestJobsQueuePageFollowsRun(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}, orchRuns: &pb.ListRunsResponse{History: finished("a", 40, 0)}}
	m := openJobs(sized(New(d, "/work")))
	m.jobs.tab = jobsQueue
	for i := 0; i < 3; i++ {
		m, _ = update(m, press("j"))
	}
	var queued []*pb.Run
	for i := 0; i < 30; i++ {
		queued = append(queued, &pb.Run{Id: fmt.Sprintf("q%02d", i), Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: uint32(i + 1), ReceivedAt: at(time.Duration(i) * time.Second)})
	}
	d.orchRuns = &pb.ListRunsResponse{Queued: queued, History: finished("a", 40, 0)}
	m = changedOrch(m, m.jobs.orchs[0].target)
	rows, page, _, _ := m.queuePageRows()
	if page != 1 || rows[m.jobs.cursors[jobsQueue]].run.GetId() != "a03" {
		t.Errorf("page %d cursor %d: the highlighted run a03 should stay highlighted", page, m.jobs.cursors[jobsQueue])
	}
	// A filter change keeps it too when it is still listed.
	m, _ = update(m, press("f"))
	if rows, _, _, _ := m.queuePageRows(); rows[m.jobs.cursors[jobsQueue]].run.GetId() != "a03" {
		t.Error("the filter keeps the highlighted run")
	}
}

// An older reply never overwrites a newer one: every call is stamped from jobsSeq
// as it is issued, so a host load issued after an orchestrator reload wins even
// if the reload's reply lands last, and a slow host load's older part does not
// revert a newer event reload (FR-005, review item 3, R3).
func TestJobsOlderReplyIsDropped(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	target := m.jobs.orchs[0].target
	queued := &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "r1", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}}
	running := &pb.ListRunsResponse{InFlight: []*pb.Run{{Id: "r1", Action: "triage", State: pb.RunState_RUN_STATE_RUNNING}}}

	d.orchRuns = queued
	mm, older := m.syncOrchestratorChanged(target.host, target.id)
	m = mm.(Model)
	olderMsg := older() // read while the run was queued
	d.orchRuns = running
	newer := m.jobsLoadAll()
	m = feed(m, newer)
	m, _ = update(m, olderMsg)
	if runs := m.jobs.orchs[0].runs; len(runs.GetInFlight()) != 1 || len(runs.GetQueued()) != 0 {
		t.Fatalf("the older reply must not put the run back in the queue: %v", runs)
	}
	// A host reply other than the load in flight is dropped too.
	flight := m.jobs.hosts[target.host].flight
	m, _ = update(m, jobsHostMsg{host: target.host, flight: flight - 1})
	if len(m.jobs.orchs) != 1 {
		t.Error("a superseded host reply is dropped")
	}

	// The other way round: the host load reads first (queued) but lands after an
	// event reload that read later (running). The event reload's state stays.
	d.orchRuns = queued
	slow := runAll(m.jobsLoadAll())
	d.orchRuns = running
	m = changedOrch(m, target)
	for _, msg := range slow {
		m, _ = update(m, msg)
	}
	if runs := m.jobs.orchs[0].runs; len(runs.GetInFlight()) != 1 || len(runs.GetQueued()) != 0 {
		t.Fatalf("a slow host load must not revert a newer event reload: %v", runs)
	}
	// ...and its older list does not drop an orchestrator a newer reload read.
	d.orchestrators = nil
	slow = runAll(m.jobsLoadAll())
	d.orchestrators = []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}
	m = changedOrch(m, target)
	for _, msg := range slow {
		m, _ = update(m, msg)
	}
	if len(m.jobs.orchs) != 1 {
		t.Error("an older list does not drop an orchestrator read since")
	}
	// A newer list without it does.
	d.orchestrators = nil
	if m = reloadJobs(m); len(m.jobs.orchs) != 0 {
		t.Error("a newer list without it drops it")
	}
	// And an older reply cannot bring it back.
	m, _ = update(m, jobsOrchMsg{host: target.host, id: target.id, recSeq: 1, runsSeq: 1, orch: &jobsOrch{target: target, runs: running}})
	if len(m.jobs.orchs) != 0 {
		t.Error("an older reply does not resurrect a removed orchestrator")
	}
}

// Events during a reload are coalesced into one more reload; host loads do not
// take part (R3, review item 6).
func TestJobsReloadsAreCoalesced(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	target := m.jobs.orchs[0].target
	d.orchCalls = nil
	mm, first := m.syncOrchestratorChanged(target.host, target.id)
	m = mm.(Model)
	for i := 0; i < 3; i++ {
		var c tea.Cmd
		mm, c = m.syncOrchestratorChanged(target.host, target.id)
		m = mm.(Model)
		if c != nil {
			t.Fatal("a reload in flight absorbs further events")
		}
	}
	m, again := update(m, first())
	if again == nil {
		t.Fatal("one more reload follows the burst")
	}
	m, last := update(m, again())
	if last != nil || strings.Count(d.orchCallsJoined(), "GetOrchestrator o-a") != 2 {
		t.Errorf("a burst costs two reloads: %q", d.orchCallsJoined())
	}
	// During a host load an event reloads at once; the host's reply adds nothing.
	load := m.jobsLoadAll()
	mm, c := m.syncOrchestratorChanged(target.host, target.id)
	m = mm.(Model)
	if c == nil {
		t.Fatal("an event during a host load reloads its orchestrator")
	}
	if again := m.jobsLoadAll(); again != nil {
		t.Error("r while a host load runs does not stack another")
	}
	m, follow := update(m, runAll(load)[0])
	if follow != nil {
		t.Error("the host's reply asks for no further reload")
	}
}

// While Jobs is not on screen nothing is fetched; it catches up when shown,
// reloading only what changed (R3, SC-007, review item 6).
func TestJobsReloadsOnlyWhileVisible(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot"), orchSummary("o-b", "beta-bot")}}
	m := openJobs(sized(New(d, "/work")))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	d.orchCalls = nil
	target := m.jobs.orchs[0].target
	mm, cmd := m.syncOrchestratorChanged(target.host, target.id)
	m = mm.(Model)
	if cmd != nil || d.orchCallsJoined() != "" {
		t.Fatalf("no fetch while Jobs is hidden: %q", d.orchCallsJoined())
	}
	// An operation's success elsewhere does not fetch either.
	mm, cmd = m.applyOrchDone(orchDoneMsg{status: "ok", reload: true, target: target})
	m = mm.(Model)
	m = feed(m, cmd)
	if strings.Contains(d.orchCallsJoined(), "ListRuns") {
		t.Errorf("no Jobs fetch while hidden: %q", d.orchCallsJoined())
	}
	d.orchCalls = nil
	m = openJobs(m)
	calls := d.orchCallsJoined()
	if !strings.Contains(calls, "GetOrchestrator o-a") || strings.Contains(calls, "GetOrchestrator o-b") {
		t.Errorf("entering reloads the stale orchestrator only: %q", calls)
	}
}

// A NotFound on ListRuns also means the orchestrator was removed (R3).
func TestJobsNotFoundRunsRemoves(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	target := m.jobs.orchs[0].target
	m, _ = update(m, jobsOrchMsg{host: target.host, id: target.id, recSeq: jobsSeq.Add(1), gone: true, err: status.Error(codes.NotFound, "no such orchestrator")})
	if len(m.jobs.orchs) != 0 {
		t.Error("NotFound removes it from every tab")
	}
}

// Each host is loaded by its own command and shown as it answers; one still
// loading is named and does not hold back the others (R2, SC-007).
func TestJobsHostsLoadIndependently(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, _, _ := twoFakeHosts(t, a, b)
	mm, cmd := m.enterJobs(screenList)
	m = mm.(Model)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("one command per connected host, got %T", cmd())
	}
	m.jobs.tab = jobsActions
	m, _ = update(m, batch[0]())
	v := m.View()
	if len(m.jobs.orchs) != 1 || !strings.Contains(v, "loading: beta") || !strings.Contains(v, "alpha-bot @ alpha") {
		t.Errorf("alpha is shown while beta loads:\n%s", v)
	}
	m, _ = update(m, batch[1]())
	if len(m.jobs.orchs) != 2 || strings.Contains(m.View(), "loading:") {
		t.Errorf("beta joins when it answers: %v", jobsLabels(m))
	}
}

// A host that connects while the first load is running is loaded too (review
// item 12).
func TestJobsHostConnectingDuringFirstLoad(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	mgr.Disconnect("hostb")
	mm, first := m.enterJobs(screenList)
	m = mm.(Model)
	if err := mgr.Connect(context.Background(), "hostb"); err != nil {
		t.Fatal(err)
	}
	m = feed2(m, m.hostsCmd())
	m = feed(m, first)
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,beta-bot @ beta" {
		t.Errorf("beta connected during the first load and is shown: %q", got)
	}
}

// Operations never fall back to the active daemon: a create submitted after the
// daemon dropped is refused with the form kept, nothing is created anywhere, and
// the section closes on the host snapshot (FR-030, review item 1).
func TestDaemonOrchsRefuseAfterDisconnect(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "alpha-bot")}}
	m := twoHostModel(t, d)
	m = openDaemonOrchs(t, m, "local")
	m, _ = update(m, press("n"))
	if m.orch.form == nil {
		t.Fatal("n opens the create form")
	}
	m.orch.vals.name = "new-bot"
	m.manager.Disconnect("local")
	m, cmd := update(m, ctrlS())
	m = feed(m, cmd)
	if strings.Contains(d.orchCallsJoined(), "CreateOrchestrator") {
		t.Fatalf("nothing is created on the active daemon: %q", d.orchCallsJoined())
	}
	if m.orch.form == nil || !strings.Contains(m.status, "laptop is not connected") {
		t.Errorf("the form stays open with the refusal: %q", m.status)
	}
	m, _ = update(m, hostsMsg{{Host: client.HostConn{Entry: client.HostEntry{ID: "local", DisplayName: "laptop"}, State: client.HostDisconnected}}})
	if m.screen != screenDaemons || !strings.Contains(m.status, "laptop disconnected — orchestrators closed") || m.orch.form != nil {
		t.Errorf("the section closes on disconnect: screen %v status %q", m.screen, m.status)
	}
	// The other entry points refuse a disconnected daemon outright.
	for name, try := range map[string]func(Model) (tea.Model, tea.Cmd){
		"create": func(m Model) (tea.Model, tea.Cmd) { return m.openOrchCreateForm("box", "build-box") },
		"open":   func(m Model) (tea.Model, tea.Cmd) { return m.enterDaemonOrchs(daemonRow{id: "box", name: "build-box"}) },
		"detail": func(m Model) (tea.Model, tea.Cmd) {
			return m.enterOrchDetail(orchRow{host: "box", hostName: "build-box", summary: orchSummary("o9", "x")})
		},
		"remove": func(m Model) (tea.Model, tea.Cmd) {
			return m.confirmOrchRemove(orchRow{host: "box", hostName: "build-box", summary: orchSummary("o9", "x")})
		},
	} {
		mm, c := try(m)
		if c != nil || !strings.Contains(mm.(Model).status, "build-box is not connected") {
			t.Errorf("%s on a disconnected daemon: status %q", name, mm.(Model).status)
		}
	}
	if _, err := m.connectedDaemon("nope"); err == nil {
		t.Error("an unknown host is not connected")
	}
}

// Daemons › Orchestrators keeps the screen cycle and settings keys (review item 9).
func TestDaemonOrchsCycleAndSettings(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "alpha-bot")}}
	m := openDaemonOrchs(t, twoHostModel(t, d), "local")
	if h := m.renderHelp(m.daemonOrchsHelp()); !strings.Contains(h, "shift+→") || !strings.Contains(h, "settings") {
		t.Errorf("help lists the cycle and settings: %q", h)
	}
	if s, _ := update(m, press(",")); s.screen != screenSettings || s.topLevel() != screenDaemons {
		t.Error(", opens settings")
	}
	if s, _ := update(m, shiftLeft()); s.screen != screenList {
		t.Error("shift+← goes to Sandboxes")
	}
	if s, _ := update(m, shiftRight()); s.screen != screenJobs {
		t.Error("shift+→ goes to Jobs")
	}
}

// An Enqueue now failure while a Jobs form is open goes to the footer, not under
// the form (review item 11).
func TestFireErrorUnderFormGoesToFooter(t *testing.T) {
	d := twoOrchs()
	m := openJobs(sized(New(d, "/work")))
	m.jobs.form = huh.NewForm(huh.NewGroup(huh.NewInput()))
	mm, _ := m.applyOrchDone(orchDoneMsg{fire: true, err: errBoom{}, sink: surfaceJobs, fireLabel: "beta-bot @ test-host"})
	m = mm.(Model)
	if m.jobs.status != "" || !strings.Contains(m.status, "enqueue on beta-bot @ test-host: boom") {
		t.Errorf("form line %q footer %q", m.jobs.status, m.status)
	}
}

// Agent-written text on the main Jobs rows, in the prompt view and in
// notifications cannot reach the terminal (FR-029, review item 5).
func TestJobsRowsAreSanitised(t *testing.T) {
	hostile := "ok\x1b]52;c;Zm9v\x07\x1b[2J\rPWNED\u202etxt.exe\x9b31m"
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}, orchRuns: &pb.ListRunsResponse{
		Queued:  []*pb.Run{{Id: "q1", Action: "triage\x1b[31m", State: pb.RunState_RUN_STATE_QUEUED, EventKind: pb.EventKind_EVENT_KIND_TRIGGERED, OriginLabel: "w\x1b]0;t\x07"}},
		History: []*pb.Run{{Id: "h1", Action: "review-pr", State: pb.RunState_RUN_STATE_FAILED, Summary: hostile, Reason: "tab\there", EndedAt: at(0)}},
	}}
	m := openJobs(sized(New(d, "/work")))
	for tab := jobsTab(0); tab < jobsTabCount; tab++ {
		m.jobs.tab = tab
		v := m.View()
		for _, bad := range []string{"\x1b]", "\x1b[2J", "\r", "\u202e", "\x07", "\x9b", "\t", "\x1b[31mm"} {
			if strings.Contains(v, bad) {
				t.Errorf("tab %s shows %q", jobsTabNames[tab], bad)
			}
		}
	}
	m.jobs.tab = jobsOverview
	if !strings.Contains(m.View(), "ok PWNEDtxt.exe") {
		t.Errorf("the text is kept, minus the controls:\n%s", m.View())
	}
	if v := m.promptView(&pb.Run{Id: "p", Prompt: "do\x1b]52;c;x\x07 it\r\nnow"}); strings.Contains(v, "\x1b]52") || !strings.Contains(v, "do it") {
		t.Errorf("prompt view: %q", v)
	}
	n := &pb.NotificationEvent{Id: "n1", Message: "run failed: \x1b]52;c;x\x07bad\u202e"}
	m.inbox = []*pb.NotificationEvent{n}
	m.inboxHosts = map[string]string{}
	mm, _ := m.enterNotifications()
	if v := mm.(Model).View(); strings.Contains(v, "\x1b]52") || strings.Contains(v, "\u202e") {
		t.Errorf("notification list: %q", v)
	}
}

// The results overlay fits the terminal with a long report, keeps its scroll
// position and hint visible, and re-fits on resize (review item 4).
func TestRunResultsOverlayFitsLongReport(t *testing.T) {
	long := strings.Repeat("Reviewed the change and left comments on several files. ", 40)
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}, orchRuns: &pb.ListRunsResponse{
		History: []*pb.Run{{Id: "h1", Action: "review-pr", State: pb.RunState_RUN_STATE_COMPLETED, Summary: long, EndedAt: at(0)}},
	}}
	m := openJobs(sized(New(d, "/work")))
	m = overviewAt(t, m, "h1")
	m, _ = update(m, press("o"))
	if m.jobs.results == nil {
		t.Fatal("o opens the overlay")
	}
	check := func(m Model, w, h int) {
		t.Helper()
		lines := strings.Split(m.View(), "\n")
		if len(lines) > h {
			t.Errorf("%dx%d: the view has %d lines", w, h, len(lines))
		}
		for _, l := range lines {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("%dx%d: a line is %d wide", w, h, lw)
				break
			}
		}
		v := m.View()
		if !strings.Contains(v, "esc close") || !strings.Contains(v, "% scrolled") {
			t.Errorf("%dx%d: the hint and scroll position stay visible:\n%s", w, h, v)
		}
	}
	check(m, 100, 40)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyPgDown})
	y := m.jobs.results.vp.YOffset
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	check(m, 80, 24)
	if m.jobs.results.vp.YOffset == 0 && y != 0 {
		t.Error("the scroll position survives a resize")
	}
}

// SC-007: with 3 daemons × 10 orchestrators × 200 finished runs, moving the
// cursor or switching tabs (with the redraw) responds within 100 ms.
func TestJobsResponsivenessBudget(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.screen = screenJobs
	m.jobs.started = true
	for h := 0; h < 3; h++ {
		for o := 0; o < 10; o++ {
			var ev []*pb.TimedEvent
			for e := 0; e < 3; e++ {
				ev = append(ev, &pb.TimedEvent{Id: fmt.Sprintf("e%d", e), Name: fmt.Sprintf("poll-%d", e), Action: "review-pr", Enabled: true, NextFireAt: at(time.Duration(h*100+o*3+e) * time.Second)})
			}
			runs := &pb.ListRunsResponse{History: finished(fmt.Sprintf("h%d-o%d-", h, o), 200, time.Duration(h*10+o)*time.Second)}
			o := jo("", fmt.Sprintf("bot-%d-%d", h, o), ev, runs)
			o.target.hostName = fmt.Sprintf("host-%d", h)
			m.jobs.orchs = append(m.jobs.orchs, o)
		}
	}
	m.jobsChanged()
	if n := len(m.jobsRows().queue); n != 6000 {
		t.Fatalf("queue = %d runs", n)
	}
	budget := 100 * time.Millisecond
	time1 := func(name string, f func()) {
		start := time.Now()
		f()
		if el := time.Since(start); el > budget {
			t.Errorf("%s took %v (budget %v)", name, el, budget)
		}
	}
	for _, tab := range []jobsTab{jobsOverview, jobsActions, jobsEvents, jobsQueue} {
		m.jobs.tab = tab
		_ = m.View()
		time1("cursor move on "+jobsTabNames[tab], func() { m, _ = update(m, press("j")); _ = m.View() })
		time1("tab switch from "+jobsTabNames[tab], func() { m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight}); _ = m.View() })
	}
	time1("an orchestrator's reply", func() {
		o := m.jobs.orchs[5]
		s := jobsSeq.Add(1)
		m, _ = update(m, jobsOrchMsg{host: o.target.host, id: o.target.id, recSeq: s, runsSeq: s, orch: &o})
		_ = m.View()
	})
}

// In-progress rows name the worker sandbox from the sandbox list (falling back
// to its short id), and an Upcoming firing dedupe would skip says so (R4).
func TestJobsWorkerNameAndSkipNote(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.sandboxes = []*pb.Sandbox{{Id: "sb-1", DisplayName: "worker-wa"}}
	m.hostAgg = []client.HostSandboxes{{Host: client.HostConn{Entry: client.HostEntry{ID: "h1"}}, Sandboxes: []*pb.Sandbox{{Id: "sb-2", DisplayName: "worker-wb"}}}}
	for id, want := range map[string]string{"sb-1": "worker-wa", "sb-2": "worker-wb", "sb-unknown-123": short("sb-unknown-123")} {
		if got := m.workerName(&pb.Run{SandboxId: id}); got != want {
			t.Errorf("workerName(%s) = %q, want %q", id, got, want)
		}
	}
	ev := &pb.TimedEvent{Id: "ev-1", Name: "nightly", Action: "triage", Enabled: true, NextFireAt: at(time.Hour)}
	open := &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "r", EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "ev-1"}}}
	rows := jobsUpcoming([]jobsOrch{jo("h", "bot", []*pb.TimedEvent{ev}, open)})
	if len(rows) != 1 || !rows[0].willSkip || !strings.Contains(upcomingLine(rows[0]), "next firing skipped — run open") {
		t.Errorf("dedupe on and a run open ⇒ the skip note: %+v", rows)
	}
	ev.DedupeDisabled = true
	if rows := jobsUpcoming([]jobsOrch{jo("h", "bot", []*pb.TimedEvent{ev}, open)}); rows[0].willSkip {
		t.Error("dedupe off ⇒ no skip note")
	}
}

// Operations on a dropped host report the refusal as their outcome, and an
// operation's success reloads what it touched only while Jobs is shown.
func TestJobsRefusalsAndAfterChange(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "alpha-bot")}}
	m := twoHostModel(t, d)
	m = openJobs(m)
	box := orchTarget{host: "box", hostName: "build-box", id: "o9", name: "x", rec: &pb.Orchestrator{}}
	msg := m.toggleEventCmd(surfaceJobs, box, &pb.TimedEvent{Id: "e"})()
	if done, ok := msg.(orchDoneMsg); !ok || done.err == nil || !strings.Contains(done.err.Error(), "build-box is not connected") {
		t.Errorf("toggle on a dropped host: %#v", msg)
	}
	if done, ok := m.runPromptCmd(surfaceJobs, box, "r1")().(orchDoneMsg); !ok || done.err == nil || !strings.Contains(done.err.Error(), "not connected") {
		t.Errorf("prompt on a dropped host: %v", done.err)
	}
	// No target: nothing reloads here; the daemon's orchestrator_changed does
	// (review run-224 item 11).
	d.orchCalls = nil
	if c := m.jobsAfterChange(orchTarget{}); c != nil || d.orchCallsJoined() != "" {
		t.Errorf("an untargeted success re-reads hosts: %q", d.orchCallsJoined())
	}
	m.screen = screenList
	if c := m.jobsAfterChange(orchTarget{}); c != nil || m.jobs.hosts["local"].stale {
		t.Error("hidden ⇒ nothing is marked stale")
	}
	// A host the developer removed drops out with its bookkeeping.
	m.screen = screenJobs
	m.manager.Remove("local")
	if m.jobsSyncHosts(); len(m.jobs.orchs) != 0 || m.jobs.hasHost("local") {
		t.Errorf("a removed host is forgotten: %v", jobsLabels(m))
	}
}

// The client keeps a dead link HostConnected, so Jobs marks a host lost when its
// event stream ends or a Jobs call fails Unavailable: rows stay, dimmed, named
// with the error, and operations are refused without an RPC; the next successful
// load (r, or a re-subscribe) clears it (R3, R12, FR-004, FR-030).
func TestJobsLostHost(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}, orchRuns: &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-b", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)

	// beta's event stream ends while the client still calls it connected.
	m, _ = update(m, eventErrMsg{hostID: "hostb", err: status.Error(codes.Unavailable, "transport is closing\x1b]52;c;x\x07")})
	v := m.View()
	if !strings.Contains(v, "could not read: beta (transport is closing)") || strings.Contains(v, "\x1b]52") {
		t.Errorf("a lost host is named with its sanitised error:\n%s", v)
	}
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,beta-bot @ beta" {
		t.Errorf("a lost host keeps its rows: %q", got)
	}
	if m.jobsReachable("hostb") || !m.jobsReachable("hosta") {
		t.Error("only beta's rows are unreachable")
	}
	m.jobs.tab = jobsQueue
	m.rememberJobsCursor(jobsQueue)
	b.orchCalls = nil
	m, cmd := update(m, press("x"))
	if cmd != nil || !strings.Contains(m.jobs.status, "beta: connection lost") || b.orchCallsJoined() != "" {
		t.Errorf("cancel on a lost host is refused without an RPC: status %q calls %q", m.jobs.status, b.orchCallsJoined())
	}
	// A successful load finds the stream gone: it re-subscribes instead of
	// clearing the mark, and the subscription clears it (review item 6).
	load := m.jobsLoadAll()
	var resub tea.Cmd
	for _, msg := range runAll(load) {
		mm, c := m.Update(msg)
		m, resub = mm.(Model), c
	}
	if m.jobsReachable("hostb") || resub == nil {
		t.Fatal("a load without an event stream keeps the mark and re-subscribes")
	}
	if opened, ok := resub().(subOpenedMsg); !ok || opened.hostID != "hostb" {
		t.Fatalf("the load re-subscribes beta: %#v", opened)
	}
	m = feed(m, m.jobsResubscribed("hostb")) // what subOpenedMsg does, without arming Recv
	if strings.Contains(m.View(), "could not read") || !m.jobsReachable("hostb") || !m.subscribed["hostb"] {
		t.Errorf("the re-subscription clears the lost mark:\n%s", m.View())
	}

	// A Jobs call failing Unavailable marks it lost too; a re-subscribe clears it
	// and reads the host again.
	b.orchErr = status.Error(codes.Unavailable, "connection refused")
	m = reloadJobs(m)
	if _, err := m.connectedDaemon("hostb"); err == nil || err.Error() != "beta: connection lost — r on Jobs retries" {
		t.Errorf("Unavailable ⇒ lost: %v", err)
	}
	b.orchErr = nil
	b.orchCalls = nil
	m = feed(m, m.jobsResubscribed("hostb"))
	if !m.jobsReachable("hostb") || !strings.Contains(b.orchCallsJoined(), "ListOrchestrators") {
		t.Errorf("a re-subscribe clears it and reloads beta: %q", b.orchCallsJoined())
	}
	if m.jobsResubscribed("hostb") != nil {
		t.Error("a re-subscribe of a host that was not lost reads nothing")
	}

	// Hidden: the stream end marks it, a re-subscribe marks the host for loading.
	m.screen = screenList
	m, _ = update(m, eventErrMsg{hostID: "hostb", err: status.Error(codes.Unavailable, "eof")})
	if c := m.jobsResubscribed("hostb"); c != nil || !m.jobs.hosts["hostb"].stale {
		t.Error("hidden ⇒ the re-subscribed host is loaded when Jobs is shown")
	}

	// A host the developer disconnected is not marked when its stream ends.
	m.screen = screenJobs
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	m, _ = update(m, eventErrMsg{hostID: "hostb", err: status.Error(codes.Canceled, "closed")})
	if strings.Contains(m.View(), "could not read") {
		t.Errorf("a disconnected host's stream end is not a failure:\n%s", m.View())
	}
}

// A connecting host is shown on the connecting line, not as unreadable; a failed
// dial is named with its sanitised error until the developer disconnects it
// (Manager.Disconnect clears the error: FR-004, FR-029).
func TestJobsConnectingAndFailedDial(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)

	release := make(chan struct{})
	dialing := make(chan struct{})
	mgr.SetDialFunc(func(context.Context, client.HostEntry) (*client.Conn, error) {
		close(dialing)
		<-release
		return nil, fmt.Errorf("ssh: banner \x1b[2Jhostile\x1b]0;t\x07 rejected")
	})
	mgr.Disconnect("hostb")
	done := make(chan struct{})
	go func() { _ = mgr.Connect(context.Background(), "hostb"); close(done) }()
	<-dialing
	m = feed2(m, m.hostsCmd())
	v := m.View()
	if !strings.Contains(v, "connecting: beta") || strings.Contains(v, "could not read") {
		t.Errorf("a connecting host is on the connecting line only:\n%s", v)
	}
	close(release)
	<-done
	m = feed2(m, m.hostsCmd())
	v = m.View()
	if !strings.Contains(v, "could not read: beta (ssh: banner hostile rejected)") || strings.Contains(v, "\x1b[2J") || strings.Contains(v, "connecting:") {
		t.Errorf("a failed dial is named with its sanitised error:\n%s", v)
	}
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if strings.Contains(m.View(), "could not read") {
		t.Errorf("the developer disconnected it: no longer named:\n%s", m.View())
	}
}

// The Reasoning rebuild output is shown as plain lines: colour dropped, each \r
// progress redraw a line of its own (FR-024, FR-029).
func TestReasoningRebuildLogIsSanitised(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	key := reasoningLogKey("o1")
	ch := make(chan tea.Msg, 1)
	m.rebuilds = map[string]*reasoningRebuild{"o1": {id: "o1", name: "bot", ch: ch}}
	m.beginOpLog(key, "reasoning sandbox of bot", "rebuild")
	mm, _ := m.handleReasoningProgress(reasoningProgressMsg{id: "o1", update: client.LaunchUpdate{LogLine: "\x1b[32mpulling\x1b[0m 10%\r20%\r\n\x1b]52;c;x\x07done\t!"}})
	m = mm.(Model)
	got := m.opLogs[key].lines
	if strings.Join(got, "|") != "pulling 10%|20%|done    !" {
		t.Errorf("rebuild log lines = %q", got)
	}
	if m.rebuilds["o1"].latest != "done    !" {
		t.Errorf("latest = %q", m.rebuilds["o1"].latest)
	}
}

// hostReply is a host load's reply for t's host, as the load in flight (stamps
// are taken by the caller in the order the calls would be issued).
func hostReply(m *Model, t orchTarget, listSeq uint64, parts ...jobsPart) jobsHostMsg {
	m.jobsLoadAll() // starts the load the reply answers (its command is not run)
	return jobsHostMsg{host: t.host, flight: m.jobs.hosts[t.host].flight, listSeq: listSeq, parts: parts}
}

// A host load whose ListRuns for an orchestrator fails keeps that
// orchestrator's last runs, named with the error (review run-224 item 1, R3).
func TestJobsHostLoadKeepsRunsWhenListRunsFails(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}, orchRuns: &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "r1", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}}}
	m := openJobs(sized(New(d, "/work")))
	o := m.jobs.orchs[0]
	listSeq := jobsSeq.Add(1)
	failed := jobsOrch{target: o.target, summary: o.summary, err: status.Error(codes.DeadlineExceeded, "deadline exceeded")}
	m, _ = update(m, hostReply(&m, o.target, listSeq, jobsPart{orch: failed, seq: jobsSeq.Add(1)}))
	if got := m.jobs.orchs[0].runs.GetQueued(); len(got) != 1 {
		t.Errorf("the queued run must not vanish: %v", got)
	}
	if !strings.Contains(m.View(), "alpha-bot @ test-host (runs: deadline exceeded)") {
		t.Errorf("the failure is named:\n%s", m.View())
	}
}

// A removed orchestrator does not come back from a host load that listed it
// before the removal: neither from its later ListRuns (NotFound ⇒ gone) nor from
// a part whose record predates the removal (review run-224 item 2).
func TestJobsRemovedOrchestratorStaysRemoved(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	o := m.jobs.orchs[0]
	listSeq := jobsSeq.Add(1) // the host load lists it
	m, _ = update(m, jobsOrchMsg{host: o.target.host, id: o.target.id, recSeq: jobsSeq.Add(1), gone: true, err: status.Error(codes.NotFound, "orchestrator not found")})
	if len(m.jobs.orchs) != 0 {
		t.Fatal("the event reload's NotFound removes it")
	}
	ghost := jobsOrch{target: o.target, summary: o.summary, err: status.Error(codes.NotFound, "orchestrator not found")}
	m, _ = update(m, hostReply(&m, o.target, listSeq, jobsPart{orch: ghost, seq: jobsSeq.Add(1), gone: true}))
	if len(m.jobs.orchs) != 0 || strings.Contains(m.View(), "could not read") {
		t.Errorf("a later NotFound ListRuns does not bring it back:\n%s", m.View())
	}
	readable := jobsOrch{target: o.target, summary: o.summary, runs: &pb.ListRunsResponse{}}
	m, _ = update(m, hostReply(&m, o.target, listSeq, jobsPart{orch: readable, seq: jobsSeq.Add(1)}))
	if len(m.jobs.orchs) != 0 {
		t.Error("a part whose record was read before the removal does not bring it back")
	}
}

// An event reload's runs are stamped when its ListRuns is issued, after its
// GetOrchestrator: a host load's ListRuns issued in between read an older state
// and loses, whichever lands last (review run-224 item 7).
func TestJobsRunsStampedAtListRuns(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	o := m.jobs.orchs[0]
	queued := &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "r1", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}}
	running := &pb.ListRunsResponse{InFlight: []*pb.Run{{Id: "r1", Action: "triage", State: pb.RunState_RUN_STATE_RUNNING}}}
	listSeq := jobsSeq.Add(1)
	recSeq := jobsSeq.Add(1)    // the event reload's GetOrchestrator
	hostRuns := jobsSeq.Add(1)  // the host load's ListRuns (reads queued)
	eventRuns := jobsSeq.Add(1) // the event reload's ListRuns (reads running)
	newer := o
	newer.runs = running
	m, _ = update(m, jobsOrchMsg{host: o.target.host, id: o.target.id, recSeq: recSeq, runsSeq: eventRuns, orch: &newer})
	older := o
	older.runs = queued
	m, _ = update(m, hostReply(&m, o.target, listSeq, jobsPart{orch: older, seq: hostRuns}))
	if runs := m.jobs.orchs[0].runs; len(runs.GetInFlight()) != 1 || len(runs.GetQueued()) != 0 {
		t.Errorf("the host load's older runs must not win: %v", runs)
	}
}

// reviewWorker is a sandbox whose run on host's orchestrator awaits review.
func reviewWorker(daemonHost string) *pb.Sandbox {
	return &pb.Sandbox{Id: "w1", DisplayName: "worker-one", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Controller: &pb.SandboxController{OrchestratorId: "o-b", Name: "beta-bot", HostId: daemonHost, Run: &pb.WorkerRun{Id: "run-1", Action: "review-pr", NeedsReview: true}}}
}

// m (mark free) on the sandbox list for a daemon that is lost is refused before
// anything changes: no detail, no list-return flag (review run-224 item 3).
func TestMarkFreeFromListRefusedLeavesListAlone(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, _, _ := twoFakeHosts(t, a, b)
	m.orchDetail.host, m.orchDetail.id = "hosta", "o-a"
	m.jobs.hostLoad("hostb").lost = status.Error(codes.Unavailable, "eof")
	mm, cmd := m.markFreeKey(reviewWorker("hostb"), "hostb")
	m = mm.(Model)
	if cmd != nil || m.screen != screenList || m.markFreeFromList || m.orchDetail.host != "hosta" || m.orchDetail.form != nil {
		t.Errorf("refused mark free changed state: screen %v fromList %v detail host %q", m.screen, m.markFreeFromList, m.orchDetail.host)
	}
	if !strings.Contains(m.status, "beta: connection lost") {
		t.Errorf("status = %q", m.status)
	}
}

// The detail closes when its own daemon drops, not the Orchestrators section's:
// a mark-free dialog opened from the list for beta survives alpha dropping and
// closes back to the list when beta drops (review run-224 item 4).
func TestDetailClosesOnItsOwnDaemon(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	m.daemonOrchs = daemonOrchsState{host: "hosta", hostName: "alpha"} // an earlier visit
	mm, _ := m.markFreeKey(reviewWorker("hostb"), "hostb")
	m = mm.(Model)
	if m.screen != screenOrchDetail || m.orchDetail.form == nil {
		t.Fatalf("the dialog opens: screen %v", m.screen)
	}
	mgr.Disconnect("hosta")
	m = feed2(m, m.hostsCmd())
	if m.screen != screenOrchDetail || m.orchDetail.form == nil {
		t.Fatalf("alpha dropping leaves beta's dialog alone: screen %v status %q", m.screen, m.status)
	}
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if m.screen != screenList || m.orchDetail.form != nil || m.orchDetail.formVals != nil || m.markFreeFromList {
		t.Errorf("beta dropping closes the dialog back to the list: screen %v", m.screen)
	}
	if !strings.Contains(m.status, "beta disconnected — orchestrators closed") {
		t.Errorf("status = %q", m.status)
	}
}

// A drop is noticed while settings or a confirm is open over Daemons ›
// Orchestrators (review run-224 item 5).
func TestDaemonOrchsCloseUnderSettingsAndConfirm(t *testing.T) {
	for _, over := range []string{"settings", "confirm"} {
		a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
		b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
		m, mgr, _ := twoFakeHosts(t, a, b)
		mm, cmd := m.enterDaemonOrchs(daemonRow{id: "hostb", name: "beta"})
		m = feed(mm.(Model), cmd)
		if over == "settings" {
			m, _ = update(m, press(","))
		} else {
			m, _ = update(m, press("d"))
		}
		if m.screen != screenSettings && m.screen != screenConfirm {
			t.Fatalf("%s did not open: %v", over, m.screen)
		}
		mgr.Disconnect("hostb")
		m = feed2(m, m.hostsCmd())
		if over == "settings" {
			m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
		}
		if m.screen != screenDaemons || !strings.Contains(m.status, "beta disconnected — orchestrators closed") {
			t.Errorf("%s: screen %v status %q", over, m.screen, m.status)
		}
	}
}

// connOf reads the daemon from one host snapshot and never substitutes another
// host's (review run-224 item 8).
func TestConnOfNeverFallsBack(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.activeHost = "local"
	for _, c := range []struct {
		hc   *client.HostConn
		want bool
	}{
		{nil, false},
		{&client.HostConn{Entry: client.HostEntry{ID: "box"}, State: client.HostConnected}, false},
		{&client.HostConn{Entry: client.HostEntry{ID: "box"}, State: client.HostConnecting}, false},
		{&client.HostConn{Entry: client.HostEntry{ID: "local"}, State: client.HostDisconnected}, false},
		{&client.HostConn{Entry: client.HostEntry{ID: "local"}, State: client.HostConnected}, true},
	} {
		if got := m.connOf(c.hc) != nil; got != c.want {
			t.Errorf("connOf(%+v) = %v, want %v", c.hc, got, c.want)
		}
	}
}

// After a create, only the event's orchestrator is read, and an orchestrator
// marked stale on a host that is fully loaded on entry is read once, by that
// load (review run-224 item 11).
func TestJobsCreateReadsOnlyTheNewOrchestrator(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	d.orchestrators = append(d.orchestrators, orchSummary("o2", "beta-bot"))
	d.orchCalls = nil
	m = feed(m, m.jobsAfterChange(orchTarget{}))
	mm, cmd := m.syncOrchestratorChanged(m.subHostKey(""), "o2")
	m = feed(mm.(Model), cmd)
	if calls := d.orchCallsJoined(); strings.Contains(calls, "ListOrchestrators") || strings.Contains(calls, "o1") || !strings.Contains(calls, "o2") {
		t.Errorf("calls after a create = %q", calls)
	}
	if got := jobsLabels(m); len(got) != 2 {
		t.Errorf("rows = %v", got)
	}
	// Hidden: the event marks o2 stale and the host is marked to reload; entry
	// reads o2 once.
	m.screen = screenList
	mm, _ = m.syncOrchestratorChanged(m.subHostKey(""), "o2")
	m = mm.(Model)
	m.jobs.hostLoad(m.subHostKey("")).stale = true
	d.orchCalls = nil
	mm, cmd = m.enterJobs(screenList)
	m = feed2(mm.(Model), cmd)
	if n := strings.Count(d.orchCallsJoined(), "ListRuns o2"); n != 1 || strings.Contains(d.orchCallsJoined(), "GetOrchestrator o2") {
		t.Errorf("o2 read %d times on entry: %q", n, d.orchCallsJoined())
	}
}

// A frame resolves each host's reachability and each worker's name once
// (review run-224 item 12).
func TestJobsFrameMemo(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "alpha-bot")}}
	m := openJobs(sized(New(d, "/work")))
	m.jobs.frame = &jobsFrame{reach: map[string]bool{"local": false}, names: map[string]string{"h/s": "memo"}}
	if m.jobsReachable("local") {
		t.Error("the frame's answer is used")
	}
	if got := m.workerName(&pb.Run{SandboxHostId: "h", SandboxId: "s"}); got != "memo" {
		t.Errorf("workerName = %q", got)
	}
}

// A lost daemon's groups say so, and opening its Daemons › Orchestrators or r
// there retries it: its Jobs load clears the mark (review run-224 UX notes).
func TestJobsLostHeadingAndRetryFromDaemonOrchs(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-b", Name: "beta-bot", Actions: []*pb.Action{reviewAction()}}}}}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)
	m.jobs.hostLoad("hostb").lost = status.Error(codes.Unavailable, "eof")
	m.jobs.tab = jobsActions
	if v := m.View(); !strings.Contains(v, "beta-bot @ beta  (connection lost)") {
		t.Errorf("the lost heading:\n%s", v)
	}
	m.subscribed[m.subHostKey("hostb")] = true // the stream is open again
	// Daemons › Orchestrators opens for a lost daemon and retries it.
	mm, cmd := m.enterDaemonOrchs(daemonRow{id: "hostb", name: "beta"})
	m = feed(mm.(Model), cmd)
	if m.screen != screenDaemonOrchs || m.jobs.hosts["hostb"].lost != nil {
		t.Errorf("entry: screen %v lost %v", m.screen, m.jobs.hosts["hostb"].lost)
	}
	// So does r there.
	m.jobs.hostLoad("hostb").lost = status.Error(codes.Unavailable, "eof")
	m, cmd = update(m, press("r"))
	m = feed(m, cmd)
	if hs := m.jobs.hosts["hostb"]; hs.lost != nil {
		t.Errorf("r on Daemons › Orchestrators leaves the host lost: %v", hs.lost)
	}
}

// Every Jobs row fits an 80-column terminal, whatever the names (review run-224
// UX notes).
func TestJobsRowsFitTheWidth(t *testing.T) {
	long := strings.Repeat("x", 60)
	ev := &pb.TimedEvent{Id: "ev-1", Name: long, Action: long, Enabled: true, NextFireAt: at(time.Hour)}
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o1", Name: long, TimedEvents: []*pb.TimedEvent{ev}, Actions: []*pb.Action{reviewAction()}}}},
		orchRuns: &pb.ListRunsResponse{
			Queued:   []*pb.Run{{Id: "q1", Action: long, State: pb.RunState_RUN_STATE_QUEUED, EventKind: pb.EventKind_EVENT_KIND_TIMED, Origin: "ev-1", OriginLabel: long}},
			InFlight: []*pb.Run{{Id: "r1", Action: long, State: pb.RunState_RUN_STATE_RUNNING, EventKind: pb.EventKind_EVENT_KIND_TRIGGERED, OriginLabel: long, SandboxId: long, StartedAt: at(0)}},
		}}
	m, _ := update(New(d, "/work"), tea.WindowSizeMsg{Width: 80, Height: 40})
	m = openJobs(m)
	for tab := jobsTab(0); tab < jobsTabCount; tab++ {
		m.jobs.tab = tab
		for _, l := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(l); w > 80 {
				t.Errorf("tab %s: a %d-column line: %q", jobsTabNames[tab], w, l)
			}
		}
	}
}
