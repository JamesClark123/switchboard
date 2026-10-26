package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// openJobs enters the Jobs screen from the sandbox list and loads it.
func openJobs(m Model) Model {
	mm, cmd := m.enterJobs(screenList)
	return feed(mm.(Model), cmd)
}

// reloadJobs reloads every connected host (as `r` does) and applies the replies.
func reloadJobs(m Model) Model {
	cmd := m.jobsLoadAll()
	return feed(m, cmd)
}

// jobsLabels lists the loaded orchestrators' labels in order.
func jobsLabels(m Model) []string {
	var out []string
	for _, o := range m.jobs.orchs {
		out = append(out, o.target.label())
	}
	return out
}

// The top-level cycle is Sandboxes → Daemons → Jobs and wraps; there is no
// Orchestrators screen (FR-001). Each screen keeps its cursor across the trip.
func TestScreenCycleSandboxesDaemonsJobs(t *testing.T) {
	d := &fakeDaemon{daemonVer: "v1", orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "alpha-bot"), orchSummary("o2", "beta-bot")}}
	m := twoHostModel(t, d)
	m.list.Select(1)

	for _, want := range []string{"Sandboxes", "Daemons", "Jobs", "shift+←/→"} {
		if !strings.Contains(m.header(), want) {
			t.Errorf("list header missing %q", want)
		}
	}
	if strings.Contains(m.header(), "Orchestrators") {
		t.Error("the strip must not name an Orchestrators screen")
	}
	if m.topLevel() != screenList || m.screenNextLabel() != "daemons" || m.screenPrevLabel() != "jobs" {
		t.Errorf("list labels: next %q prev %q", m.screenNextLabel(), m.screenPrevLabel())
	}

	m, _ = update(m, shiftRight()) // daemons
	m, _ = update(m, press("j"))
	if m.screenNextLabel() != "jobs" || m.screenPrevLabel() != "sandboxes" || !strings.Contains(m.renderHelp(m.daemonsHelp()), "jobs") {
		t.Errorf("daemons labels: next %q prev %q", m.screenNextLabel(), m.screenPrevLabel())
	}
	m, cmd := update(m, shiftRight()) // jobs
	if m.screen != screenJobs || m.topLevel() != screenJobs {
		t.Fatalf("shift+→ twice should reach Jobs, got %v", m.screen)
	}
	m = feed(m, cmd)
	if m.jobs.tab != jobsOverview || len(m.jobs.orchs) != 2 {
		t.Fatalf("Jobs opens on Overview with the data loaded: tab %v orchs %v", m.jobs.tab, jobsLabels(m))
	}
	if m.screenNextLabel() != "sandboxes" || m.screenPrevLabel() != "daemons" {
		t.Errorf("jobs labels: next %q prev %q", m.screenNextLabel(), m.screenPrevLabel())
	}
	help := m.renderHelp(m.jobsHelp())
	for _, want := range []string{"tab", "reload", "daemons", "sandboxes", "settings", "back"} {
		if !strings.Contains(help, want) {
			t.Errorf("jobs help missing %q: %q", want, help)
		}
	}
	// The cycle wraps to the list, and back the other way.
	m, _ = update(m, shiftRight())
	if m.screen != screenList || m.list.Index() != 1 {
		t.Errorf("shift+→ on Jobs should wrap to the list with its row kept; screen %v index %d", m.screen, m.list.Index())
	}
	m, cmd = update(m, shiftLeft())
	if m.screen != screenJobs {
		t.Fatalf("shift+← from the list reaches Jobs, got %v", m.screen)
	}
	m = feed(m, cmd)
	m, _ = update(m, shiftLeft())
	if m.screen != screenDaemons || m.daemons.cursor != 1 {
		t.Errorf("shift+← from Jobs returns to daemons with its cursor; screen %v cursor %d", m.screen, m.daemons.cursor)
	}
	// esc goes back to Sandboxes from wherever Jobs was entered; q quits (R8).
	m, cmd = update(m, shiftRight())
	m = feed(m, cmd)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Errorf("esc from Jobs returns to Sandboxes, got %v", m.screen)
	}
	mm, _ := m.enterJobs(screenList)
	if q, c := update(mm.(Model), press("q")); !q.quitting || c == nil {
		t.Error("q quits from Jobs, as on every top-level screen")
	}
	// Settings from Jobs keeps the strip on Jobs and returns there.
	mm, _ = m.enterJobs(screenList)
	m, _ = update(mm.(Model), press(","))
	if m.screen != screenSettings || m.topLevel() != screenJobs {
		t.Errorf("settings from Jobs: screen %v top %v", m.screen, m.topLevel())
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenJobs {
		t.Error("esc from settings returns to Jobs")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quitting {
		t.Error("ctrl+c quits from Jobs")
	}
	// A narrow terminal drops the strip rather than overflowing.
	m.width = 30
	if strings.Contains(m.header(), "Jobs") {
		t.Error("a narrow header should drop the strip")
	}
}

// Tabs are Overview, Actions, Events, Queue, switched with ←/→ (wrapping), and
// the last tab is remembered (FR-002).
func TestJobsTabsOrderAndSwitching(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "solo")}}
	m := openJobs(sized(New(d, "/work")))
	if jobsTabNames != [jobsTabCount]string{"Overview", "Actions", "Events", "Queue"} {
		t.Fatalf("tab order = %v", jobsTabNames)
	}
	bar := m.jobsTabBar()
	if i, j, k, l := strings.Index(bar, "Overview"), strings.Index(bar, "Actions"), strings.Index(bar, "Events"), strings.Index(bar, "Queue"); i >= j || j >= k || k >= l {
		t.Errorf("tab bar order: %q", bar)
	}
	want := []jobsTab{jobsActions, jobsEvents, jobsQueue, jobsOverview}
	for _, w := range want {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
		if m.jobs.tab != w {
			t.Errorf("right: tab %v, want %v", m.jobs.tab, w)
		}
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.jobs.tab != jobsQueue {
		t.Errorf("left from Overview wraps to Queue, got %v", m.jobs.tab)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = openJobs(m)
	if m.jobs.tab != jobsQueue {
		t.Error("the last tab is remembered for the session")
	}
	// Every tab renders with a help line.
	for tab := jobsTab(0); tab < jobsTabCount; tab++ {
		m.jobs.tab = tab
		if !strings.Contains(m.View(), "solo @ test-host") && tab != jobsQueue && tab != jobsOverview {
			t.Errorf("tab %s should name the orchestrator", jobsTabNames[tab])
		}
		if m.renderHelp(m.jobsHelp()) == "" {
			t.Errorf("tab %s should have help", jobsTabNames[tab])
		}
	}
}

// Jobs fans out over every connected host, labels every orchestrator with its
// host, and names the hosts it could not read (FR-003, FR-004).
func TestJobsFanOutAcrossTwoHosts(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot"), orchSummary("o-z", "a-bot")}}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,a-bot @ beta,beta-bot @ beta" {
		t.Errorf("orchestrators by host then name = %q", got)
	}
	for _, c := range []string{"ListOrchestrators", "ListRuns o-a"} {
		if !strings.Contains(a.orchCallsJoined(), c) {
			t.Errorf("alpha calls missing %q: %q", c, a.orchCallsJoined())
		}
	}
	if !strings.Contains(b.orchCallsJoined(), "ListRuns o-b") || !strings.Contains(b.orchCallsJoined(), "ListRuns o-z") {
		t.Errorf("beta calls = %q", b.orchCallsJoined())
	}
	// gamma is known but was never connected: not named (FR-004).
	if v := m.View(); strings.Contains(v, "could not read") || strings.Contains(v, "gamma") {
		t.Errorf("a host never connected is not named:\n%s", v)
	}
	// beta's listing fails: its rows stay as last read and it is named with the error.
	b.orchErr = errBoom{}
	m, cmd := update(m, press("r"))
	if !m.jobsRefreshing() {
		t.Error("r reloads")
	}
	if !strings.Contains(m.View(), "refreshing") {
		t.Error("a reload over loaded data says refreshing")
	}
	m = feed(m, cmd)
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,a-bot @ beta,beta-bot @ beta" || !strings.Contains(m.View(), "could not read: beta (boom)") {
		t.Errorf("after beta fails: %q\n%s", got, m.View())
	}
	// An orchestrator whose runs cannot be read is still listed, and named on
	// every tab.
	b.orchErr = nil
	m = reloadJobs(m)
	m.jobs.orchs[0].err = errBoom{}
	for tab := jobsTab(0); tab < jobsTabCount; tab++ {
		m.jobs.tab = tab
		if !strings.Contains(m.View(), "could not read: alpha-bot @ alpha (runs: boom)") {
			t.Errorf("tab %s: a runs failure is named:\n%s", jobsTabNames[tab], m.View())
		}
	}
}

// Event.orchestrator_changed reloads just that orchestrator (R3): a change shows
// up, a removed orchestrator disappears, a transient failure keeps what was read.
func TestJobsOrchestratorChangedReloadsOne(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)
	a.orchCalls, b.orchCalls = nil, nil
	m.sub = nil
	changed := func(m Model, host, id string) Model {
		m, cmd := update(m, eventMsg{hostID: host, ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: host, OrchestratorId: id}}}})
		return feed(m, cmd)
	}
	b.orchRuns = &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-new", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}}
	m = changed(m, "hostb", "o-b")
	if calls := b.orchCallsJoined(); !strings.Contains(calls, "GetOrchestrator o-b") || !strings.Contains(calls, "ListRuns o-b") || strings.Contains(calls, "ListOrchestrators") {
		t.Errorf("only that orchestrator reloads: %q", calls)
	}
	if a.orchCallsJoined() != "" {
		t.Errorf("the other host is not touched: %q", a.orchCallsJoined())
	}
	if !strings.Contains(m.View(), "run-new") && !strings.Contains(m.View(), "triage") {
		t.Errorf("the new run shows without reopening:\n%s", m.View())
	}
	// A transient failure keeps the orchestrator.
	b.orchErr = errBoom{}
	m = changed(m, "hostb", "o-b")
	if len(m.jobs.orchs) != 2 {
		t.Errorf("a transient failure must keep the orchestrator: %v", jobsLabels(m))
	}
	// A removed orchestrator disappears; a new one appears.
	b.orchErr = nil
	b.orchestrators = []*pb.OrchestratorSummary{orchSummary("o-n", "new-bot")}
	m = changed(m, "hostb", "o-b")
	m = changed(m, "hostb", "o-n")
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,new-bot @ beta" {
		t.Errorf("after remove + create: %q", got)
	}
	// An event for a host that is not connected reloads nothing.
	if c := m.jobsReloadOrch("hostc", "o-x"); c != nil {
		t.Error("no reload for a disconnected host")
	}
	// Jobs not loaded yet: events do nothing for it.
	m2 := sized(New(&fakeDaemon{}, "/work"))
	if _, cmd := m2.syncOrchestratorChanged("", "o-a"); cmd != nil {
		t.Error("unloaded Jobs ignores orchestrator_changed")
	}
	if _, cmd := m2.applyJobsOrch(jobsOrchMsg{host: "", id: "o-a", gone: true}); cmd != nil {
		t.Error("a stray reload is ignored")
	}
}

// A host the developer disconnects drops out unnamed; a host whose connection
// fails keeps its rows, dimmed and refusing operations, and is named with its
// error; a host that (re)connects is loaded without a reload (R3, FR-004).
func TestJobsHostDisconnectReloadsThatHost(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}, orchRuns: &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-b", Action: "triage", State: pb.RunState_RUN_STATE_QUEUED, Position: 1}}}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	m = openJobs(m)
	// A host snapshot with nothing changed reloads nothing.
	if c := m.jobsSyncHosts(); c != nil {
		t.Error("no connection change ⇒ no reload")
	}
	a.orchCalls = nil
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha" {
		t.Errorf("beta's orchestrators drop out: %q", got)
	}
	if strings.Contains(m.View(), "could not read") {
		t.Errorf("a host the developer disconnected is not named:\n%s", m.View())
	}
	if strings.Contains(a.orchCallsJoined(), "ListOrchestrators") {
		t.Errorf("alpha is not reloaded: %q", a.orchCallsJoined())
	}
	// beta reconnects: loaded without r.
	if err := mgr.Connect(context.Background(), "hostb"); err != nil {
		t.Fatal(err)
	}
	m = feed2(m, m.hostsCmd())
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,beta-bot @ beta" {
		t.Errorf("beta's orchestrators come back on reconnect: %q", got)
	}
	// beta's connection fails: rows kept, dimmed, named with the error, and
	// operations on them refused without reaching any daemon.
	mgr.SetDialFunc(func(context.Context, client.HostEntry) (*client.Conn, error) { return nil, errBoom{} })
	mgr.Disconnect("hostb")
	_ = mgr.Connect(context.Background(), "hostb")
	m = feed2(m, m.hostsCmd())
	if got := strings.Join(jobsLabels(m), ","); got != "alpha-bot @ alpha,beta-bot @ beta" {
		t.Errorf("a failed host keeps its rows: %q", got)
	}
	if !strings.Contains(m.View(), "could not read: beta (boom)") {
		t.Errorf("a failed host is named with its error:\n%s", m.View())
	}
	m.jobs.tab = jobsQueue
	m.rememberJobsCursor(jobsQueue)
	b.orchCalls = nil
	m, cmd := update(m, press("x"))
	if cmd != nil || m.screen != screenJobs || !strings.Contains(m.jobs.status, "beta is not connected") {
		t.Errorf("cancel on a dropped host is refused: screen %v status %q", m.screen, m.jobs.status)
	}
	if b.orchCallsJoined() != "" || strings.Contains(a.orchCallsJoined(), "CancelRun") {
		t.Errorf("nothing reaches any daemon: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	// Host snapshots before Jobs first loads do not fetch.
	m2 := sized(New(&fakeDaemon{}, "/work"))
	if _, c := m2.applyJobsHost(jobsHostMsg{}); c != nil || m2.jobsSyncHosts() != nil {
		t.Error("unloaded Jobs fetches nothing on host changes")
	}
}

// No orchestrators anywhere: every tab explains where to create one; the first
// load shows a spinner.
func TestJobsEmptyAndLoadingStates(t *testing.T) {
	d := &fakeDaemon{}
	m := sized(New(d, "/work"))
	mm, cmd := m.enterJobs(screenList)
	m = mm.(Model)
	if !strings.Contains(m.View(), "loading") {
		t.Error("the first load shows loading")
	}
	m = feed(m, cmd)
	for tab := jobsTab(0); tab < jobsTabCount; tab++ {
		m.jobs.tab = tab
		if !strings.Contains(m.View(), "No orchestrators yet — create one under Daemons › <daemon> › Orchestrators.") {
			t.Errorf("tab %s empty state:\n%s", jobsTabNames[tab], m.View())
		}
		for _, k := range []string{"enter", "x", "m", "o", "n", "a", "e", "d", " ", "j", "k"} {
			if mm, cmd := update(m, press(k)); cmd != nil || mm.jobs.form != nil {
				t.Errorf("tab %s key %q on no data must do nothing", jobsTabNames[tab], k)
			}
		}
	}
	if windowLines([]string{"a", "b", "c", "d", "e", "f"}, 5, 3)[2] != "f" || len(windowLines([]string{"a"}, 0, 1)) != 1 {
		t.Error("windowLines keeps the focus visible")
	}
	if got := windowLines([]string{"a", "b", "c", "d", "e", "f"}, 0, 3); got[0] != "a" {
		t.Errorf("windowLines at the top = %v", got)
	}
}

// A NotFound reload removes; other errors keep. Reloads of an orchestrator that
// is unknown still insert it.
func TestJobsApplyOrchEdgeCases(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o1", "solo")}}
	m := openJobs(sized(New(d, "/work")))
	key := m.jobs.orchs[0].target
	seq := jobsSeq.Add(1)
	m, _ = update(m, jobsOrchMsg{host: key.host, id: key.id, seq: seq, err: status.Error(codes.Internal, "boom")})
	if len(m.jobs.orchs) != 1 || m.jobs.orchs[0].err == nil {
		t.Error("a failed reload keeps the orchestrator, with the error")
	}
	m, _ = update(m, jobsOrchMsg{host: key.host, id: key.id, seq: seq - 1, gone: true})
	if len(m.jobs.orchs) != 1 {
		t.Error("an older reply is dropped")
	}
	m, _ = update(m, jobsOrchMsg{host: key.host, id: key.id, seq: jobsSeq.Add(1), gone: true, err: status.Error(codes.NotFound, "gone")})
	if len(m.jobs.orchs) != 0 {
		t.Error("a NotFound reload removes it")
	}
	if !strings.Contains(m.View(), "No orchestrators yet") {
		t.Error("the empty state follows")
	}
}
