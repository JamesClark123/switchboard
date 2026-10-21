package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// drainChain runs cmd and keeps feeding the model until the message chain ends
// (a streaming operation's channel closes).
func drainChain(m Model, cmd tea.Cmd) Model {
	for cmd != nil {
		msgs := runAll(cmd)
		if len(msgs) == 0 {
			return m
		}
		cmd = nil
		for _, msg := range msgs {
			var next tea.Cmd
			m, next = update(m, msg)
			if next != nil {
				cmd = next
			}
		}
	}
	return m
}

func reasoningFake(ready bool, errText string) *fakeDaemon {
	sum := &pb.OrchestratorSummary{
		Orchestrator:   &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", ReasoningSandboxId: "r1"},
		ReasoningReady: ready, ReasoningError: errText,
	}
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{sum}, streamLines: []string{"sbx: rm reasoning-o-a", "sbx: create reasoning-o-a"}}
	if ready {
		a.orchDetail = &pb.OrchestratorDetail{Summary: sum, Workers: []*pb.WorkerView{
			{SandboxId: "w1", DisplayName: "worker-1", HostId: "test-host", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_FREE},
			{SandboxId: "r1", DisplayName: "reasoning-o-a", HostId: "test-host", Reasoning: true, State: pb.SandboxState_SANDBOX_STATE_RUNNING, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY, RunId: "run-7", RunAction: "think"},
		}}
	}
	return a
}

func openReasoning(t *testing.T, a *fakeDaemon, root string) Model {
	t.Helper()
	m := sized(New(a, root))
	m = openDetail(t, m)
	m.orchDetail.view = viewReasoning
	return m
}

func TestReasoningViewRendersStates(t *testing.T) {
	m := openReasoning(t, reasoningFake(true, ""), t.TempDir())
	view := m.View()
	for _, want := range []string{"reasoning-o-a", "RUNNING", "busy", "think", "run-7", "default: empty context", "e edit the setup", "b rebuild"} {
		if !strings.Contains(view, want) {
			t.Errorf("ready view missing %q", want)
		}
	}
	if !strings.Contains(m.renderHelp(m.orchDetailHelp()), "rebuild") {
		t.Error("help should list rebuild")
	}
	// Coming up: not ready, no error.
	m = openReasoning(t, reasoningFake(false, ""), t.TempDir())
	if v := m.View(); !strings.Contains(v, "coming up…") || !strings.Contains(v, "reasoning-o-a") {
		t.Errorf("coming-up view = %s", v)
	}
	// Lost: the error verbatim, with the rebuild hint.
	m = openReasoning(t, reasoningFake(false, "destroyed outside switchboard — rebuild"), t.TempDir())
	if v := m.View(); !strings.Contains(v, "error: destroyed outside switchboard — rebuild") || !strings.Contains(v, "b rebuilds it") {
		t.Errorf("lost view = %s", v)
	}
	// A saved setup is described.
	a := reasoningFake(true, "")
	a.orchestrators[0].Orchestrator.ReasoningSetup = &pb.ReasoningSetup{
		Sources: []*pb.SourceRef{{Path: "/srv/api"}, {Path: "/srv/web"}}, Config: &pb.ConfigSnapshot{Name: "big", SeedingMode: pb.SeedingMode_SEEDING_MODE_CLONE},
		Kits: []*pb.KitRef{{Ref: &pb.KitRef_Source{Source: "k"}}}, McpServers: []string{"notion"},
	}
	a.orchDetail.Summary = a.orchestrators[0]
	m = openReasoning(t, a, t.TempDir())
	for _, want := range []string{"folders: api, web (clone)", "configuration: big", "kits: 1", "MCP servers: notion"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("setup lines missing %q", want)
		}
	}
}

func TestReasoningRebuildIsConfirmGatedAndStreamsIntoTheLog(t *testing.T) {
	a := reasoningFake(true, "")
	m := openReasoning(t, a, t.TempDir())
	// b asks first; n cancels without an RPC.
	m, _ = update(m, press("b"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Rebuild the reasoning sandbox of alpha-bot?") || !strings.Contains(m.View(), "default: empty context") {
		t.Fatalf("screen = %v view = %s", m.screen, m.View())
	}
	m, _ = update(m, press("n"))
	if m.screen != screenOrchDetail || strings.Contains(a.orchCallsJoined(), "Rebuild") {
		t.Fatal("cancel must not rebuild")
	}
	// y rebuilds with the saved setup; the lines stream into the sbx log; the
	// detail reloads on success.
	m, _ = update(m, press("b"))
	m, cmd := update(m, press("y"))
	m = drainChain(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "RebuildReasoningSandbox o-a sources= kits=0") {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	if len(m.rebuilds) != 0 || !strings.Contains(m.status, "reasoning sandbox rebuilt") {
		t.Errorf("after rebuild: rebuilds=%d status=%q", len(m.rebuilds), m.status)
	}
	l := m.opLogs[reasoningLogKey("o-a")]
	if l == nil || len(l.lines) != 2 || l.lines[1] != "sbx: create reasoning-o-a" {
		t.Fatalf("op log = %+v", l)
	}
	// l opens that log from the view.
	m, _ = update(m, press("l"))
	if m.screen != screenOpLog || m.opLogView.id != reasoningLogKey("o-a") {
		t.Errorf("l should open the rebuild log: screen=%v id=%q", m.screen, m.opLogView.id)
	}
	m, _ = update(m, press("esc"))

	// A refusal (FAILED_PRECONDITION) shows verbatim; a second rebuild is
	// refused while one is in flight.
	a.orchErr = status.Error(codes.FailedPrecondition, "run run-7 is on the reasoning sandbox")
	m.screen = screenOrchDetail
	m, _ = update(m, press("b"))
	m, cmd = update(m, press("y"))
	m = drainChain(m, cmd)
	if !strings.Contains(m.orchDetail.status, "run run-7 is on the reasoning sandbox") {
		t.Errorf("refusal status = %q", m.orchDetail.status)
	}
	a.orchErr = nil
	m.rebuilds = map[string]*reasoningRebuild{"o-a": {id: "o-a", latest: "sbx: create"}}
	if v := m.View(); !strings.Contains(v, "rebuilding…") || !strings.Contains(v, "sbx: create") {
		t.Errorf("busy badge missing: %s", v)
	}
	m, _ = update(m, press("b"))
	if m.screen != screenOrchDetail || !strings.Contains(m.orchDetail.status, "already in progress") {
		t.Errorf("concurrent rebuild should be refused: %q", m.orchDetail.status)
	}
	m, _ = update(m, press("e"))
	if m.screen != screenOrchDetail {
		t.Error("e is refused while a rebuild runs")
	}
}

func TestReasoningSetupBrowserRebuildsWithTheSelection(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := reasoningFake(true, "")
	a.orchestrators[0].Orchestrator.ReasoningSetup = &pb.ReasoningSetup{Sources: []*pb.SourceRef{{Path: "/old/folder"}}}
	a.orchDetail.Summary = a.orchestrators[0]
	m := openReasoning(t, a, root)
	// e opens the launch browser in setup mode on the orchestrator's host with
	// the saved folder pre-selected; H/O/N are inert; esc returns to the detail.
	m, _ = update(m, press("e"))
	if m.screen != screenLaunch || m.launch.reasoning == nil || m.launch.reasoning.id != "o-a" || !m.launch.selected["/old/folder"] {
		t.Fatalf("screen=%v launch=%+v", m.screen, m.launch.reasoning)
	}
	for _, want := range []string{"Reasoning sandbox setup", "alpha-bot", "reasoning-o-a", "(fixed)", "none = empty context", "proj"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("setup browser missing %q", want)
		}
	}
	for _, k := range []string{"H", "O", "N"} {
		m, _ = update(m, press(k))
		if m.launch.hostPick || m.launch.orchPick || m.launch.naming {
			t.Errorf("%s must be inert in setup mode", k)
		}
	}
	m, _ = update(m, press("esc"))
	if m.screen != screenOrchDetail {
		t.Fatal("esc should return to the detail")
	}
	// Select proj (after the ".." row), drop the old folder, enter rebuilds
	// with exactly the selection and returns to the detail.
	m, _ = update(m, press("e"))
	m.launch.toggle() // ".." is a no-op toggle target; move to proj
	m, _ = update(m, press("j"))
	m, _ = update(m, press(" "))
	delete(m.launch.selected, "/old/folder")
	m.launch.order = []string{filepath.Join(root, "proj")}
	m, cmd := update(m, press("enter"))
	if m.screen != screenOrchDetail {
		t.Fatalf("enter should return to the detail, screen = %v", m.screen)
	}
	m = drainChain(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "RebuildReasoningSandbox o-a sources="+filepath.Join(root, "proj")+" kits=0") {
		t.Errorf("calls = %q", a.orchCallsJoined())
	}
	if len(m.rebuilds) != 0 {
		t.Error("the rebuild should have finished")
	}
}
