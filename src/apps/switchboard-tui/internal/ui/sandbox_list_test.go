package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func worker(id, name string, needsInput, review bool) *pb.Sandbox {
	sb := &pb.Sandbox{Id: id, DisplayName: name, State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent:      &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE},
		Controller: &pb.SandboxController{OrchestratorId: "o-a", Name: "review-bot", HostId: "hosta"}}
	if needsInput {
		sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	}
	if review {
		sb.Controller.Run = &pb.WorkerRun{Id: "run-1", Action: "review-pr", NeedsReview: true}
	}
	return sb
}

func rowIDs(rows []sandboxRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.sb.GetId())
	}
	return out
}

// Hidden workers leave the All, host and group tabs; those that need the
// developer lead every tab; group memberships survive (FR-113/FR-114).
func TestWorkersHiddenSurfacedAndOrdered(t *testing.T) {
	a := &fakeDaemon{sandboxes: []*pb.Sandbox{
		{Id: "plain-a", DisplayName: "plain-a", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
		worker("w1", "worker-one", false, false),
		worker("w2", "worker-two", false, false),
	}}
	b := &fakeDaemon{sandboxes: []*pb.Sandbox{
		{Id: "plain-b", DisplayName: "plain-b", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
		worker("w3", "worker-three", false, false),
	}}
	m, _, hs := twoFakeHosts(t, a, b)
	s, _ := store.New(t.TempDir())
	gs := s.Groups()
	g, _ := gs.Save(store.Group{Name: "mygroup"})
	_ = gs.AddMember(g.ID, store.GroupMember{HostID: "hosta", SandboxID: "w1"})
	_ = gs.AddMember(g.ID, store.GroupMember{HostID: "hosta", SandboxID: "plain-a"})
	m = m.WithGroups(gs)
	_ = hs
	m = feed2(m, m.listDataCmd())

	// All tab: workers absent.
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != "plain-a,plain-b" {
		t.Fatalf("All rows = %v", got)
	}
	// Host tab and group tab: workers absent, membership intact in the store.
	for i, tab := range m.tabs {
		m.tabIndex = i
		for _, r := range m.currentTabRows() {
			if tab.kind != tabOrchestrated && r.sb.GetController() != nil {
				t.Errorf("tab %q should hide worker %s", tab.label, r.sb.GetId())
			}
		}
	}
	m.tabIndex = 0
	groups, _ := gs.List()
	if len(groups[0].Members) != 2 {
		t.Error("group memberships must survive hiding")
	}

	// A worker that needs input surfaces at the top, marked; IDLE hides it again.
	m.sub = nil
	needs := worker("w2", "worker-two", true, false)
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: needs}}})
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != "w2,plain-a,plain-b" {
		t.Errorf("surfaced rows = %v", got)
	}
	if !strings.Contains(m.View(), "worker · needs input") {
		t.Error("the surfaced row should say why it is here")
	}
	idle := worker("w2", "worker-two", false, false)
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: idle}}})
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != "plain-a,plain-b" {
		t.Errorf("hidden again rows = %v", got)
	}
	// A run awaiting review surfaces it marked review; clearing hides it.
	review := worker("w3", "worker-three", false, true)
	m, _ = update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: review}}})
	if got := rowIDs(m.currentTabRows()); got[0] != "w3" {
		t.Errorf("review should surface first: %v", got)
	}
	if !strings.Contains(m.View(), "worker · review") {
		t.Error("the review row should be marked")
	}
	cleared := worker("w3", "worker-three", false, false)
	m, _ = update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: cleared}}})
	if got := rowIDs(m.currentTabRows()); len(got) != 2 {
		t.Errorf("cleared review hides the worker: %v", got)
	}
	// An unattached NEEDS_INPUT sandbox leads its tab too; order within each
	// half is stable.
	plainB := &pb.Sandbox{Id: "plain-b", DisplayName: "plain-b", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT}}
	m, _ = update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: plainB}}})
	m, _ = update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: review}}})
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != "plain-b,w3,plain-a" {
		t.Errorf("stable partition = %v", got)
	}
	if !needsDeveloper(plainB) || hiddenWorker(plainB) || !hiddenWorker(idle) || hiddenWorker(review) {
		t.Error("predicates")
	}
}

func TestOrchestratedTabLifetime(t *testing.T) {
	a := &fakeDaemon{sandboxes: []*pb.Sandbox{{Id: "plain-a", DisplayName: "plain-a", State: pb.SandboxState_SANDBOX_STATE_RUNNING}, worker("w1", "worker-one", false, false)}}
	b := &fakeDaemon{sandboxes: []*pb.Sandbox{worker("w3", "worker-three", false, false)}}
	r := worker("r1", "reasoning-abcd", false, false)
	r.Controller.Reasoning = true
	a.sandboxes = append(a.sandboxes, r)
	a.orchestrators = []*pb.OrchestratorSummary{orchSummary("o-a", "review-bot")}
	m, mgr, _ := twoFakeHosts(t, a, b)
	m = feed2(m, m.listDataCmd())
	tabsBefore := len(m.tabs)

	m, _ = update(m, press("O"))
	if !m.showOrchTab || len(m.tabs) != tabsBefore+1 || m.tabs[m.tabIndex].kind != tabOrchestrated {
		t.Fatalf("O should show and select the tab: show %v tabs %d kind %v", m.showOrchTab, len(m.tabs), m.tabs[m.tabIndex].kind)
	}
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != "w1,r1,w3" {
		t.Errorf("tab rows = %v, want every controlled sandbox across hosts", got)
	}
	v := m.View()
	for _, want := range []string{"Orchestrator Sandboxes", "orchestrator: review-bot", "(reasoning)"} {
		if !strings.Contains(v, want) {
			t.Errorf("tab view missing %q", want)
		}
	}
	// Per-sandbox keys work on the tab's rows (t, d, o, m): t starts the
	// terminal attach (async), the others dispatch as on any row.
	m, cmd := update(m, press("t"))
	if cmd == nil || !strings.Contains(m.status, "opening terminal") {
		t.Errorf("t should open the terminal on a worker: status %q", m.status)
	}
	m, cmd = update(m, press("o"))
	m = feed(m, cmd)
	if m.screen != screenConfirm {
		t.Error("o on a worker should confirm the detach")
	}
	m, _ = update(m, press("n"))
	m, _ = update(m, press("m"))
	if !strings.Contains(m.status, "no run awaiting review") {
		t.Error("m on a worker without review is refused locally")
	}
	_, cmd = update(m, press("d"))
	if cmd == nil {
		t.Error("d should destroy the worker like any sandbox")
	}
	// The tab stays open and empty when the last worker detaches.
	for _, sb := range a.sandboxes {
		sb.Controller = nil
	}
	b.sandboxes[0].Controller = nil
	m = feed2(m, m.listDataCmd())
	if !m.showOrchTab || m.tabs[m.tabIndex].kind != tabOrchestrated || len(m.currentTabRows()) != 0 {
		t.Errorf("the tab stays open and empty: show %v rows %d", m.showOrchTab, len(m.currentTabRows()))
	}
	// Switching to another tab hides it; O again brings it back; O on it hides it.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.showOrchTab || len(m.tabs) != tabsBefore {
		t.Errorf("leaving the tab hides it: show %v tabs %d", m.showOrchTab, len(m.tabs))
	}
	m, _ = update(m, press("O"))
	m, _ = update(m, press("O"))
	if m.showOrchTab || m.tabIndex != 0 {
		t.Error("O on the tab hides it and returns to All")
	}
	// An unreachable orchestrator is named in the tab's status line.
	m.orch.rows = []orchRow{{summary: orchSummary("o-x", "far-bot"), reachable: false, reason: "disconnected"}}
	m, _ = update(m, press("O"))
	if !strings.Contains(m.View(), "unreachable: far-bot") {
		t.Error("the tab should name unreachable orchestrators")
	}
	mgr.Disconnect("hostb")
}

// SWI-17 (FR-213, contracts/jobs-ui.md): holds, halts and detached agents on
// the sandbox list. A hold that needs the developer, a halt, a cleanup that
// waits, a detached agent and a failing check surface on the All, host and
// group tabs; a cleanup that progresses stays hidden.
func TestHoldsSurfaceOnTheList(t *testing.T) {
	held := func(id string, run *pb.WorkerRun) *pb.Sandbox {
		sb := worker(id, "worker-"+id, false, false)
		sb.Controller.Run = run
		return sb
	}
	cleaning := held("wc", &pb.WorkerRun{Id: "run-c", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP})
	a := &fakeDaemon{sandboxes: []*pb.Sandbox{
		{Id: "plain", DisplayName: "plain", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
		cleaning,
		held("wd", &pb.WorkerRun{Id: "run-d", Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER}),
		held("wf", &pb.WorkerRun{Id: "run-f", Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING}),
		held("wh", &pb.WorkerRun{Id: "run-h", Halted: true}),
		detachedSandbox("orph", pb.AgentStatus_AGENT_STATUS_IDLE, false),
	}}
	m, _, _ := twoFakeHosts(t, a, &fakeDaemon{})
	m = feed2(m, m.listDataCmd())
	m, _ = update(m, tea.WindowSizeMsg{Width: 200, Height: 60})
	got := strings.Join(rowIDs(m.currentTabRows()), ",")
	if got != "wd,wf,wh,orph,plain" {
		t.Fatalf("All rows = %v", got)
	}
	v := m.View()
	for _, want := range []string{
		"⚑ worker · needing the developer — open its terminal and run /clear run-d",
		"⚑ worker · needs input (freeing) run-f",
		"⚑ worker · run-h halted: step not accepted",
		"agent detached by a daemon restart (last: idle, Oct 28 10:42) — t to Replace or Wait",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("list missing %q:\n%s", want, v)
		}
	}
	// Host tab: the same rule.
	for i, tab := range m.tabs {
		if tab.kind == tabOrchestrated {
			continue
		}
		m.tabIndex = i
		for _, r := range m.currentTabRows() {
			if r.sb.GetId() == "wc" {
				t.Errorf("tab %q shows a progressing cleanup", tab.label)
			}
		}
	}
	m.tabIndex = 0
	// The cleanup's step waits: it surfaces, with the reason.
	m.sub = nil
	waiting := held("wc", &pb.WorkerRun{Id: "run-c", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP, StepNote: "waiting: input box not empty"})
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: waiting}}})
	if got := rowIDs(m.currentTabRows()); got[0] != "wc" {
		t.Errorf("a waiting cleanup surfaces first: %v", got)
	}
	if !strings.Contains(m.View(), "⚑ worker · cleaning up run-c · waiting: input box not empty") {
		t.Errorf("waiting cleanup:\n%s", m.View())
	}
	// A failing check surfaces with its error; a running check doesn't.
	failed := &pb.Sandbox{Id: "plain", DisplayName: "plain", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED, Error: "exec timed out"}}}
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: failed}}})
	if !strings.Contains(m.View(), "can't check for a detached agent: exec timed out") {
		t.Errorf("check failed:\n%s", m.View())
	}
	checking := &pb.Sandbox{Id: "x", Agent: &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_UNKNOWN}}}
	if note, surfaced := orphanNote(checking); note != "checking for a detached agent…" || surfaced || needsDeveloper(checking) {
		t.Errorf("checking: %q %v", note, surfaced)
	}
	// The Orchestrator Sandboxes tab shows a progressing cleanup, dimmed.
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: cleaning}}})
	m, _ = update(m, press("O"))
	if !strings.Contains(m.View(), "⚑ worker · cleaning up run-c") {
		t.Errorf("orchestrator tab:\n%s", m.View())
	}
}

// plainSB builds an unattached sandbox in the given state.
func plainSB(id string, state pb.SandboxState) *pb.Sandbox {
	return &pb.Sandbox{Id: id, DisplayName: id, State: state}
}

// listItemIDs reads the ids of the items the list currently renders, in order.
func listItemIDs(t *testing.T, m Model) []string {
	t.Helper()
	items := m.list.Items()
	out := make([]string, 0, len(items))
	for _, it := range items {
		li, ok := it.(listItem)
		if !ok {
			t.Fatalf("list item %T is not a listItem", it)
		}
		out = append(out, li.id)
	}
	return out
}

// Stopped sandboxes sink below every non-stopped row on every tab, keeping the
// existing order within each half; stopped wins over "needs the developer";
// live state changes move the row both ways and the cursor keeps its row
// number (SWI-31).
func TestStoppedSandboxesSortLast(t *testing.T) {
	w2 := worker("w2", "worker-two", false, false)
	w2.State = pb.SandboxState_SANDBOX_STATE_STOPPED
	stoppedNeedsInput := plainSB("s-a2", pb.SandboxState_SANDBOX_STATE_STOPPED)
	stoppedNeedsInput.Agent = &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT}
	a := &fakeDaemon{sandboxes: []*pb.Sandbox{
		plainSB("s-a1", pb.SandboxState_SANDBOX_STATE_STOPPED),
		plainSB("r-a1", pb.SandboxState_SANDBOX_STATE_RUNNING),
		stoppedNeedsInput,
		plainSB("e-a", pb.SandboxState_SANDBOX_STATE_ERROR),
		worker("w1", "worker-one", false, false),
		w2,
	}}
	a.orchestrators = []*pb.OrchestratorSummary{orchSummary("o-a", "review-bot")}
	b := &fakeDaemon{sandboxes: []*pb.Sandbox{
		plainSB("s-b", pb.SandboxState_SANDBOX_STATE_STOPPED),
		plainSB("r-b", pb.SandboxState_SANDBOX_STATE_RUNNING),
		plainSB("c-b", pb.SandboxState_SANDBOX_STATE_CREATING),
	}}
	m, _, _ := twoFakeHosts(t, a, b)
	s, _ := store.New(t.TempDir())
	gs := s.Groups()
	g, _ := gs.Save(store.Group{Name: "mygroup"})
	_ = gs.AddMember(g.ID, store.GroupMember{HostID: "hosta", SandboxID: "s-a1"})
	_ = gs.AddMember(g.ID, store.GroupMember{HostID: "hosta", SandboxID: "r-a1"})
	_ = gs.AddMember(g.ID, store.GroupMember{HostID: "hostb", SandboxID: "s-b"})
	m = m.WithGroups(gs)
	m = feed2(m, m.listDataCmd())
	// An in-flight launch placeholder (CREATING) on alpha.
	m.launching["pending-1"] = &launchInFlight{tempID: "pending-1", host: "hosta", seq: 1,
		sb: &pb.Sandbox{Id: "pending-1", State: pb.SandboxState_SANDBOX_STATE_CREATING}}
	m.refreshListItems()

	// All tab: running, error and creating rows (incl. the placeholder) stay
	// above every stopped row; the stopped sandbox that needs input sinks
	// with the other stopped rows (first among them only by stability);
	// creation order is kept within each half.
	const wantAll = "r-a1,e-a,r-b,c-b,pending-1,s-a2,s-a1,s-b"
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != wantAll {
		t.Fatalf("All rows = %v, want %s", got, wantAll)
	}
	if got := listItemIDs(t, m); strings.Join(got, ",") != wantAll {
		t.Fatalf("All list items = %v, want %s", got, wantAll)
	}
	// Stable across renders.
	m.refreshListItems()
	if got := listItemIDs(t, m); strings.Join(got, ",") != wantAll {
		t.Fatalf("All list items after re-render = %v, want %s", got, wantAll)
	}

	// Host, group and Orchestrated tabs apply the same rule.
	want := map[string]string{
		"host:hosta": "r-a1,e-a,pending-1,s-a2,s-a1",
		"host:hostb": "r-b,c-b,s-b",
		"group":      "r-a1,s-a1,s-b",
	}
	seen := 0
	for i, tab := range m.tabs {
		var key string
		switch tab.kind {
		case tabHost:
			key = "host:" + tab.hostID
		case tabGroup:
			key = "group"
		default:
			continue
		}
		seen++
		m.tabIndex = i
		if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != want[key] {
			t.Errorf("%s rows = %v, want %s", key, got, want[key])
		}
	}
	if seen != 3 {
		t.Fatalf("expected two host tabs and one group tab, checked %d", seen)
	}
	m.tabIndex = 0
	m.refreshListItems()
	m, _ = update(m, press("O"))
	if m.tabs[m.tabIndex].kind != tabOrchestrated {
		t.Fatalf("O should select the Orchestrator Sandboxes tab")
	}
	if got := rowIDs(m.currentTabRows()); strings.Join(got, ",") != "w1,w2" {
		t.Errorf("Orchestrated rows = %v, want w1,w2 (stopped worker last)", got)
	}
	m, _ = update(m, press("O"))
	m.tabIndex = 0
	m.refreshListItems()

	// A live stop sinks the selected row without a reload. By the SWI-31
	// decision the cursor keeps its row number (no selection-follow, as with
	// surfaceFirst today), so the next sandbox takes the selection.
	m.sub = nil
	m.list.Select(0)
	if cur := m.current(); cur.GetId() != "r-a1" {
		t.Fatalf("selected = %q, want r-a1", cur.GetId())
	}
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{
		SandboxChanged: plainSB("r-a1", pb.SandboxState_SANDBOX_STATE_STOPPED)}}})
	const wantStopped = "e-a,r-b,c-b,pending-1,s-a2,s-a1,r-a1,s-b"
	if got := listItemIDs(t, m); strings.Join(got, ",") != wantStopped {
		t.Errorf("after stop list items = %v, want %s", got, wantStopped)
	}
	if m.list.Index() != 0 || m.current().GetId() != "e-a" {
		t.Errorf("cursor after stop: index %d selected %q, want row 0 / e-a", m.list.Index(), m.current().GetId())
	}
	// Starting it again restores its place among the running rows. The cursor
	// keeps its row number here too, so the row that moved up leaves the
	// selection on another stopped sandbox.
	m.list.Select(6)
	if cur := m.current(); cur.GetId() != "r-a1" {
		t.Fatalf("selected = %q, want r-a1", cur.GetId())
	}
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{
		SandboxChanged: plainSB("r-a1", pb.SandboxState_SANDBOX_STATE_RUNNING)}}})
	if got := listItemIDs(t, m); strings.Join(got, ",") != wantAll {
		t.Errorf("after start list items = %v, want %s", got, wantAll)
	}
	if m.list.Index() != 6 || m.current().GetId() != "s-a1" {
		t.Errorf("cursor after start: index %d selected %q, want row 6 / s-a1", m.list.Index(), m.current().GetId())
	}

	// A stopped sandbox that needs the developer stays below a running one
	// that does not, even when the need arises live.
	stoppedReview := worker("w2", "worker-two", false, true)
	stoppedReview.State = pb.SandboxState_SANDBOX_STATE_STOPPED
	m, _ = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: stoppedReview}}})
	got := rowIDs(m.currentTabRows())
	if strings.Join(got, ",") != "r-a1,e-a,r-b,c-b,pending-1,s-a2,w2,s-a1,s-b" {
		t.Errorf("stopped review worker rows = %v", got)
	}
	if !strings.Contains(m.View(), "worker · review") {
		t.Error("the stopped review worker should still be visible and marked")
	}
}

// An active "/" filter is a search: it ranks matches best-match-first, so a
// stopped sandbox can rank above a running one (FR-114, SWI-31).
func TestFilterRanksMatchesOverStoppedOrder(t *testing.T) {
	m, _, _ := twoFakeHosts(t, &fakeDaemon{sandboxes: []*pb.Sandbox{
		plainSB("app", pb.SandboxState_SANDBOX_STATE_STOPPED),
		plainSB("zzappzz", pb.SandboxState_SANDBOX_STATE_RUNNING),
		plainSB("other", pb.SandboxState_SANDBOX_STATE_RUNNING),
	}}, &fakeDaemon{})
	m = feed2(m, m.listDataCmd())
	m.refreshListItems()
	if got := listItemIDs(t, m); strings.Join(got, ",") != "zzappzz,other,app" {
		t.Fatalf("unfiltered list items = %v, want zzappzz,other,app", got)
	}

	m.list.SetFilterText("app")
	var got []string
	for _, it := range m.list.VisibleItems() {
		got = append(got, it.(listItem).id)
	}
	if strings.Join(got, ",") != "app,zzappzz" {
		t.Errorf("filtered items = %v, want app,zzappzz", got)
	}
}
