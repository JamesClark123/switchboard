package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHostsViewAddRemoveAndCycle(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot", &pb.OrchestratorHost{HostId: "hostb", DisplayName: "beta", SshTarget: "me@beta", Reachable: true})}}
	b := &fakeDaemon{}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openDetail(t, m)

	// left/right cycle the three views (wrapping) and keep per-view cursors.
	if m.orchDetail.view != viewWorkers {
		t.Fatalf("initial view = %v", m.orchDetail.view)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.orchDetail.view != viewLanes {
		t.Errorf("left from Workers should wrap to Lanes, got %v", m.orchDetail.view)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.orchDetail.view != viewHosts {
		t.Errorf("left from Lanes should reach Hosts, got %v", m.orchDetail.view)
	}
	for _, want := range []string{"Workers", "Reasoning", "Hosts", "beta", "me@beta", "reachable"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("hosts view missing %q", want)
		}
	}
	m.orchDetail.cursors[viewHosts] = 0
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.orchDetail.view != viewHosts {
		t.Error("right then left should return to Hosts")
	}

	// a lists only saved ssh hosts not yet on the orchestrator and not its own
	// machine: gamma (hostc) only — alpha is the own host, beta is present.
	m, _ = update(m, press("a"))
	if !m.orchDetail.hostPick || len(m.orchDetail.hostChoices) != 1 || m.orchDetail.hostChoices[0].ID != "hostc" {
		t.Fatalf("host choices = %+v", m.orchDetail.hostChoices)
	}
	if !strings.Contains(m.View(), "Add a host from my hosts") || !strings.Contains(m.View(), "gamma") {
		t.Error("the picker should render the candidate")
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = feed2(m, cmd)
	calls := a.orchCallsJoined()
	if !strings.Contains(calls, "SetOrchestratorHosts o-a hostb,hostc") {
		t.Errorf("add host calls = %q", calls)
	}
	// The copied entry carries the ssh target and options, never a password.
	hosts := a.orchestrators[0].GetOrchestrator().GetHosts()
	if len(hosts) != 2 || hosts[1].GetSshTarget() != "me@gamma" || hosts[1].GetDisplayName() != "gamma" {
		t.Errorf("copied entry = %+v", hosts)
	}
	if !strings.Contains(m.status, "added host gamma") {
		t.Errorf("status = %q", m.status)
	}
	// Nothing left to add.
	m, _ = update(m, press("a"))
	if m.orchDetail.hostPick || !strings.Contains(m.orchDetail.status, "already on this orchestrator") {
		t.Errorf("empty picker: pick %v status %q", m.orchDetail.hostPick, m.orchDetail.status)
	}
	// esc cancels a picker.
	m.orchDetail.hostPick = true
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.orchDetail.hostPick {
		t.Error("esc should close the picker")
	}

	// d is confirm-gated and sends the list without that host; a
	// FAILED_PRECONDITION reply is shown verbatim.
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || m.confirm.returnTo != screenOrchDetail || !strings.Contains(m.View(), "Remove host beta") {
		t.Fatalf("d should confirm over the detail: %v", m.screen)
	}
	a.orchErr = status.Error(codes.FailedPrecondition, "host still has workers sb-1 — detach them first")
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(m.orchDetail.status, "host still has workers sb-1") || !strings.Contains(m.View(), "detach them first") {
		t.Errorf("refusal should be verbatim: %q", m.orchDetail.status)
	}
	a.orchErr = nil
	m, _ = update(m, press("d"))
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "SetOrchestratorHosts o-a hostc") {
		t.Errorf("remove host calls = %q", a.orchCallsJoined())
	}
	// Empty hosts view.
	a.orchestrators[0].Orchestrator.Hosts = nil
	m, _ = update(m, press("r"))
	m = feed(m, m.orchDetailLoadCmd())
	if !strings.Contains(m.View(), "no hosts") {
		t.Error("empty hosts view should say so")
	}
	// Per-view cursors clamp and move.
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight}) // Lanes
	if !strings.Contains(m.View(), "no lanes yet — a to add one") {
		t.Error("empty lanes view should say so")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight}) // Workers
	for _, want := range []string{"no workers"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("empty view missing %q", want)
		}
	}
	// A load error lands in the status line; q quits.
	a.orchErr = errBoom{}
	m = feed(m, m.orchDetailLoadCmd())
	if m.orchDetail.status == "" {
		t.Error("a failed load should be reported")
	}
	m, _ = update(m, press("q"))
	if !m.quitting {
		t.Error("q should quit from the detail")
	}
}

func TestDetailHelpAndEmptyViews(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	m := sized(New(a, "/work"))
	m = openDetail(t, m)
	// Per-view keys that act on a highlighted row are no-ops on an empty view
	// and never open a form.
	noOps := map[orchView][]string{viewWorkers: {"x", "m"}, viewHosts: {"d"}}
	// (The Reasoning view's e/b always act: there is always a reasoning sandbox
	// to rebuild, present or lost — see orchestrator_reasoning_test.go.)
	for v := orchView(0); v < orchViewCount; v++ {
		m.orchDetail.view = v
		if !strings.Contains(m.View(), orchViewNames[v]) {
			t.Errorf("view %s should render its tab", orchViewNames[v])
		}
		if m.renderHelp(m.orchDetailHelp()) == "" {
			t.Errorf("view %s should have help", orchViewNames[v])
		}
		for _, k := range noOps[v] {
			mm, _ := update(m, press(k))
			m = mm
			if m.orchDetail.form != nil || m.screen != screenOrchDetail {
				t.Errorf("view %s key %q should be a no-op on an empty view", orchViewNames[v], k)
			}
		}
	}
	m.orchDetail.hostPick = true
	if !strings.Contains(m.renderHelp(m.orchDetailHelp()), "add host") {
		t.Error("picker help")
	}
	m.orchDetail.hostPick = false
	// A stale detail message (another orchestrator) is ignored.
	m, _ = update(m, orchDetailMsg{id: "other", detail: &pb.OrchestratorDetail{}})
	if m.orchDetail.id != "o-a" {
		t.Error("a stale detail must not replace the open one")
	}
	// orchestrator_changed for the open detail re-fetches it.
	m.sub = nil
	a.orchCalls = nil
	m, cmd := update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{OrchestratorId: "o-a"}}}})
	m = feed(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "GetOrchestrator o-a") {
		t.Errorf("detail should re-fetch on orchestrator_changed: %q", a.orchCallsJoined())
	}
	// The detail belongs to the Daemons top level (Daemons › Orchestrators).
	if m.topLevel() != screenDaemons {
		t.Error("detail belongs to the Daemons top level")
	}
	if !strings.Contains(m.renderHelp(m.orchDetailHelp()), "orchestrators") {
		t.Error("esc's hint names where it returns")
	}
}

// SWI-17 (FR-213): the Workers view shows a worker's hold, halt and waiting
// note, and t opens its terminal (through the detached-agent gate).
func TestWorkersViewHoldsAndTerminal(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	a.orchDetail = &pb.OrchestratorDetail{Summary: a.orchestrators[0], Workers: []*pb.WorkerView{
		{SandboxId: "w-run", HostId: "test-host", DisplayName: "worker-w-run", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY, RunId: "run-d", Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, HostReachable: true},
		{SandboxId: "w2", HostId: "test-host", DisplayName: "w-two", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY, RunId: "run-h", Halted: true, HostReachable: true},
		{SandboxId: "w3", HostId: "test-host", DisplayName: "w-three", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY, RunId: "run-c", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP, StepNote: "waiting: agent busy", HostReachable: true},
		{SandboxId: "w4", HostId: "test-host", DisplayName: "w-four", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY, RunId: "run-p", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP, HostReachable: true},
		{SandboxId: "w5", HostId: "test-host", DisplayName: "w-five", Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_FREE, HostReachable: true},
	}}
	m := withSandboxes(sized(New(a, "/work")), []*pb.Sandbox{runningWorker("w-run"), detachedSandbox("w5", pb.AgentStatus_AGENT_STATUS_IDLE, false)})
	m, _ = update(m, tea.WindowSizeMsg{Width: 200, Height: 50})
	m = openDetail(t, m)
	v := m.View()
	for _, want := range []string{"⚑ needing the developer — open its terminal and run /clear run-d", "⚑ run-h halted: step not accepted",
		"⚑ cleaning up run-c · waiting: agent busy", "⚑ cleaning up run-p", "agent detached by a daemon restart — t to Replace or Wait", "t terminal"} {
		if !strings.Contains(v, want) {
			t.Errorf("workers view missing %q:\n%s", want, v)
		}
	}
	m, cmd := update(m, press("t"))
	opened, ok := runCmd(cmd).(termOpenedMsg)
	if !ok {
		t.Fatalf("t should attach: %q", m.orchDetail.status)
	}
	m, _ = update(m, opened)
	if m.screen != screenTerminal || a.attachedID != "w-run" {
		t.Fatalf("screen %v attached %q", m.screen, a.attachedID)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.screen != screenOrchDetail {
		t.Errorf("ctrl+q returns to the Workers view: %v", m.screen)
	}
	// A worker the TUI doesn't list is refused on the view's line.
	m, _ = update(m, press("j"))
	m, cmd = update(m, press("t"))
	if cmd != nil || !strings.Contains(m.orchDetail.status, "worker sandbox w-two was not found") {
		t.Errorf("unknown worker: %q", m.orchDetail.status)
	}
}
