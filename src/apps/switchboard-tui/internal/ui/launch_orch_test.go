package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Launch wizard `O` (feature 009, FR-110): pick an orchestrator; the launch
// request carries the bundle's controller fetched from that orchestrator's daemon.
func TestLaunchWizardOrchestratorPick(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{hostID: "hosta", orchestrators: []*pb.OrchestratorSummary{
		{Orchestrator: &pb.Orchestrator{Id: "o-here", Name: "here-bot", HostId: "hosta"}},
		{Orchestrator: &pb.Orchestrator{Id: "o-far", Name: "far-bot", HostId: "elsewhere"}},
	}}
	m := sized(New(d, root))
	m = feed(m, m.orchLoadCmd()) // the TUI knows the orchestrator rows

	m, _ = update(m, press("n"))
	m, _ = update(m, press("O"))
	if !m.launch.orchPick || len(m.launch.orchChoices) != 1 || m.launch.orchChoices[0].summary.GetOrchestrator().GetName() != "here-bot" {
		t.Fatalf("O should list only orchestrators that can reach the target: pick %v choices %+v", m.launch.orchPick, m.launch.orchChoices)
	}
	if !strings.Contains(m.View(), "Attach the new sandbox to which orchestrator?") || !strings.Contains(m.View(), "here-bot") {
		t.Error("the orchestrator picker should render")
	}
	m, _ = update(m, press("j")) // onto here-bot (row 0 is "none")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.launch.orchPick || m.launch.orchestrator == nil || !strings.Contains(m.View(), "orchestrator: here-bot") {
		t.Fatal("enter should select the orchestrator and show it in the modal title")
	}
	// Re-opening keeps the cursor on the selection; esc leaves it.
	m, _ = update(m, press("O"))
	if m.launch.orchCursor != 1 {
		t.Errorf("cursor should sit on the selected orchestrator, got %d", m.launch.orchCursor)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.launch.orchestrator == nil {
		t.Error("esc keeps the selection")
	}
	// Launch: the request carries the bundle's controller.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		got := d.lastController
		d.mu.Unlock()
		if got != nil {
			if got.GetController().GetOrchestratorId() != "o-here" || got.GetController().GetName() != "here-bot" || got.GetBriefing() == "" {
				t.Errorf("launch controller = %+v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("launch request never carried the controller; calls %q", d.orchCallsJoined())
}

func TestLaunchWizardOrchestratorPickEmptyAndNone(t *testing.T) {
	root := t.TempDir()
	d := &fakeDaemon{hostID: "hosta"}
	m := sized(New(d, root))
	m, _ = update(m, press("n"))
	m, cmd := update(m, press("O"))
	if m.launch.orchPick || !strings.Contains(m.launch.progress, "no reachable orchestrator") {
		t.Errorf("no orchestrators: pick %v progress %q", m.launch.orchPick, m.launch.progress)
	}
	runCmd(cmd)
	// With one available, choosing "none" clears a previous choice.
	d.orchestrators = []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o", Name: "bot", HostId: "hosta"}}}
	m = feed(m, m.orchLoadCmd())
	m, _ = update(m, press("O"))
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, press("O"))
	m, _ = update(m, press("k"))
	m, _ = update(m, press("k")) // clamped at none
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.launch.orchestrator != nil {
		t.Error("choosing none clears the orchestrator")
	}
	if !strings.Contains(m.renderHelp(m.launchHelp()), "orchestrator") {
		t.Error("launch help should list O")
	}
	m.launch.orchPick = true
	if !strings.Contains(m.renderHelp(m.launchHelp()), "select orchestrator") {
		t.Error("pick help")
	}
}
