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
	m = m.WithGroups(gs, nil)
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
	m, _ = update(m, press("o"))
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
