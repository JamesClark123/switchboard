package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// attachEnv is two fake hosts with sandboxes on each and one orchestrator on
// alpha whose host entries are scripted per test. The list is loaded from the
// aggregate so rows carry their host.
func attachEnv(t *testing.T, orchHosts ...*pb.OrchestratorHost) (Model, *fakeDaemon, *fakeDaemon) {
	t.Helper()
	a := &fakeDaemon{sandboxes: []*pb.Sandbox{{Id: "sa", DisplayName: "sand-a", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}}
	b := &fakeDaemon{sandboxes: []*pb.Sandbox{{Id: "sb", DisplayName: "sand-b", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}}
	a.orchestrators = []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "review-bot", HostId: "hosta", Hosts: orchHosts}}}
	m, _, _ := twoFakeHosts(t, a, b)
	m = feed2(m, m.listDataCmd())
	m = feed(m, m.orchLoadCmd()) // the TUI knows the orchestrator rows
	m.refreshListItems()
	return m, a, b
}

func selectSandbox(m Model, id string) Model {
	selectByID(&m.list, id)
	return m
}

func TestAttachLocalSandboxSendsAttachWorker(t *testing.T) {
	m, a, _ := attachEnv(t)
	m = selectSandbox(m, "sa")
	m, cmd := update(m, press("o"))
	if m.screen != screenAttachPick || len(m.attach.choices) != 1 {
		t.Fatalf("o should open the picker with the reachable orchestrator: screen %v choices %d", m.screen, len(m.attach.choices))
	}
	m = feed(m, cmd) // the rows reload underneath
	if !strings.Contains(m.View(), "Attach sand-a to an orchestrator") || !strings.Contains(m.View(), "review-bot") {
		t.Error("the picker should render")
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList || m.busy["sa"] != "attaching" {
		t.Fatalf("enter should attach: screen %v busy %v", m.screen, m.busy)
	}
	msg := runCmd(cmd)
	if st, ok := msg.(statusMsg); !ok || !strings.Contains(string(st), "attached sand-a to review-bot") {
		t.Fatalf("attach result = %v", msg)
	}
	// The sandbox is on the orchestrator's own machine: no host entry needed.
	if !strings.Contains(a.orchCallsJoined(), "AttachWorker o-a hosta sa") || strings.Contains(a.orchCallsJoined(), "SetOrchestratorHosts") {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	// esc cancels the picker.
	m, _ = update(m, press("o"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should close the picker")
	}
}

func TestAttachRemoteSandboxOffersHostFirst(t *testing.T) {
	m, a, _ := attachEnv(t) // review-bot has no entry for beta
	m = selectSandbox(m, "sb")
	m, _ = update(m, press("o"))
	if !strings.Contains(m.View(), "will add this host") {
		t.Error("the picker should flag that the host will be added")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Add host beta to review-bot?") || !strings.Contains(m.View(), "me@beta") {
		t.Fatalf("a missing entry should open the add-host confirm: screen %v\n%s", m.screen, m.View())
	}
	m, _ = update(m, press("n"))
	if strings.Contains(a.orchCallsJoined(), "SetOrchestratorHosts") {
		t.Fatal("cancelling must not change the hosts")
	}
	m, _ = update(m, press("o"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := update(m, press("y"))
	msg := runCmd(cmd)
	if st, ok := msg.(statusMsg); !ok || !strings.Contains(string(st), "attached sand-b") {
		t.Fatalf("result = %v", msg)
	}
	calls := a.orchCallsJoined()
	if !strings.Contains(calls, "SetOrchestratorHosts o-a hostb") || strings.Index(calls, "SetOrchestratorHosts") > strings.Index(calls, "AttachWorker o-a hostb sb") {
		t.Errorf("the host is added before the attach: %q", calls)
	}
	// The copied entry carries the TUI's ssh target and options, keyed by the
	// daemon's advertised host id.
	hosts := a.orchestrators[0].GetOrchestrator().GetHosts()
	if len(hosts) != 1 || hosts[0].GetHostId() != "hostb" || hosts[0].GetSshTarget() != "me@beta" || len(hosts[0].GetSshOptions()) != 2 {
		t.Errorf("copied entry = %+v", hosts)
	}
	// With the entry present (the picker reloads the rows), a second attach
	// goes straight through.
	m, cmd = update(m, press("o"))
	m = feed(m, cmd)
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList {
		t.Fatal("a known host attaches without the offer")
	}
	runCmd(cmd)
}

func TestAttachErrorsVerbatim(t *testing.T) {
	m, a, _ := attachEnv(t)
	m = selectSandbox(m, "sa")
	var cmd tea.Cmd
	for _, err := range []error{
		status.Error(codes.AlreadyExists, "sandbox is controlled by other-bot"),
		status.Error(codes.Unavailable, "host box is unreachable: ssh: Connection refused"),
		status.Error(codes.FailedPrecondition, "run run-1 (review-pr) is in flight on this worker"),
	} {
		a.orchErr = err
		m, _ = update(m, press("o"))
		m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
		m, _ = update(m, runCmd(cmd))
		if !strings.Contains(m.status, status.Convert(err).Message()) {
			t.Errorf("status %q should carry %q verbatim", m.status, status.Convert(err).Message())
		}
	}
	// No reachable orchestrator: the picker says so and enter does nothing.
	m.orch.rows = nil
	m, _ = update(m, press("o"))
	if m.screen != screenAttachPick || !strings.Contains(m.View(), "no reachable orchestrator") {
		t.Errorf("empty picker: screen %v", m.screen)
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || m.screen != screenAttachPick {
		t.Error("enter on an empty picker is a no-op")
	}
}

func TestDetachWorkerViaOrchestratorOrDirectly(t *testing.T) {
	m, a, b := attachEnv(t)
	// sand-b is a worker of review-bot (whose daemon is alpha = "hosta"). As a
	// worker it left the everyday tabs (US4): it is found on the O tab.
	b.sandboxes[0].Controller = &pb.SandboxController{OrchestratorId: "o-a", Name: "review-bot", HostId: "hosta"}
	m = feed2(m, m.listDataCmd())
	m, _ = update(m, press("O"))
	m = selectSandbox(m, "sb")
	m, _ = update(m, press("o"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Detach sand-b from review-bot?") {
		t.Fatalf("o on a worker should confirm the detach: %v", m.screen)
	}
	m, cmd := update(m, press("y"))
	msg := runCmd(cmd)
	if st, ok := msg.(statusMsg); !ok || !strings.Contains(string(st), "detached sand-b from review-bot") {
		t.Fatalf("detach result = %v", msg)
	}
	if !strings.Contains(a.orchCallsJoined(), "DetachWorker o-a sb") || strings.Contains(b.orchCallsJoined(), "DetachController") {
		t.Errorf("detach should go through the orchestrator's daemon: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	// The orchestrator's daemon is not connected: DetachController on the
	// sandbox's own daemon instead (FR-112).
	b.sandboxes[0].Controller = &pb.SandboxController{OrchestratorId: "o-x", Name: "far-bot", HostId: "elsewhere"}
	m = feed2(m, m.listDataCmd())
	m.refreshListItems()
	m = selectSandbox(m, "sb")
	m, _ = update(m, press("o"))
	if !strings.Contains(m.View(), "not connected") {
		t.Error("the confirm should explain the direct path")
	}
	m, cmd = update(m, press("y"))
	msg = runCmd(cmd)
	if st, ok := msg.(statusMsg); !ok || !strings.Contains(string(st), "(directly)") {
		t.Fatalf("direct detach result = %v", msg)
	}
	if !strings.Contains(b.orchCallsJoined(), "DetachController sb o-x false") {
		t.Errorf("direct detach calls = %q", b.orchCallsJoined())
	}
	// A reasoning sandbox refuses locally, with no call.
	b.sandboxes[0].Controller.Reasoning = true
	m = feed2(m, m.listDataCmd())
	m.refreshListItems()
	m = selectSandbox(m, "sb")
	before := b.orchCallsJoined()
	m, _ = update(m, press("o"))
	if m.screen != screenList || !strings.Contains(m.status, "not detachable") || b.orchCallsJoined() != before {
		t.Errorf("reasoning sandbox: screen %v status %q", m.screen, m.status)
	}
	// m without a review run is refused locally too.
	m, _ = update(m, press("m"))
	if !strings.Contains(m.status, "no run awaiting review") {
		t.Errorf("m status = %q", m.status)
	}
	// A worker parked "needs input" with no run on it is freed outright by m:
	// MarkWorkerFree goes to the orchestrator's daemon with nothing to close,
	// no dialog.
	b.sandboxes[0].Controller = &pb.SandboxController{OrchestratorId: "o-a", Name: "review-bot", HostId: "hosta"}
	b.sandboxes[0].Agent = &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT}
	m = feed2(m, m.listDataCmd())
	m.refreshListItems()
	m = selectSandbox(m, "sb")
	before = a.orchCallsJoined()
	m, cmd = update(m, press("m"))
	if cmd == nil || m.screen != screenList || m.orchDetail.form != nil {
		t.Fatalf("m on a parked worker should call the daemon directly: cmd %v screen %v status %q", cmd != nil, m.screen, m.status)
	}
	done, ok := runCmd(cmd).(orchDoneMsg)
	if !ok || done.err != nil || !strings.Contains(done.status, "marked sand-b free") {
		t.Errorf("parked worker result = %+v", done)
	}
	if calls := strings.TrimPrefix(a.orchCallsJoined(), before); !strings.Contains(calls, `MarkWorkerFree o-a sb false ""`) {
		t.Errorf("calls = %q", calls)
	}
}

func TestWorkersViewDetach(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	a.orchDetail = &pb.OrchestratorDetail{Summary: a.orchestrators[0], Workers: []*pb.WorkerView{
		{SandboxId: "w1", HostId: "h", DisplayName: "worker-one", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY, RunAction: "review-pr", HostReachable: true},
		{SandboxId: "r1", HostId: "h", DisplayName: "reasoning-abc", Reasoning: true, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE},
	}}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	v := m.View()
	for _, want := range []string{"worker-one", "busy", "(review-pr)", "reasoning-abc", "reasoning", "unreachable", "host unreachable"} {
		if !strings.Contains(v, want) {
			t.Errorf("workers view missing %q", want)
		}
	}
	m, _ = update(m, press("x"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Detach worker-one from alpha-bot?") {
		t.Fatalf("x should confirm: %v", m.screen)
	}
	m, cmd := update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "DetachWorker o-a w1") || !strings.Contains(m.status, "detached worker-one") {
		t.Errorf("calls %q status %q", a.orchCallsJoined(), m.status)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("x"))
	if m.screen != screenOrchDetail || !strings.Contains(m.orchDetail.status, "not detachable") {
		t.Errorf("reasoning row x: %v %q", m.screen, m.orchDetail.status)
	}
	for _, a := range []pb.WorkerAvailability{pb.WorkerAvailability_WORKER_AVAILABILITY_FREE, pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT, pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW, pb.WorkerAvailability_WORKER_AVAILABILITY_NOT_RUNNING, pb.WorkerAvailability_WORKER_AVAILABILITY_UNSPECIFIED} {
		if availabilityBadge(a) == "" {
			t.Errorf("badge for %v", a)
		}
	}
}
