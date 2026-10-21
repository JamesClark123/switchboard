package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The three top-level screens cycle with shift+←/→ (no wrap) and every screen
// keeps its own cursor across the round trip (FR-142/FR-143).
func TestScreenCyclePreservesCursors(t *testing.T) {
	d := &fakeDaemon{daemonVer: "v1"}
	d.orchestrators = []*pb.OrchestratorSummary{
		{Orchestrator: &pb.Orchestrator{Id: "o1", Name: "alpha-bot"}},
		{Orchestrator: &pb.Orchestrator{Id: "o2", Name: "beta-bot"}, WorkerCount: 2, Queued: 1},
	}
	m := twoHostModel(t, d)
	m.list.Select(1)

	m, _ = update(m, shiftRight()) // daemons
	m, _ = update(m, press("j"))
	m, cmd := update(m, shiftRight()) // orchestrators
	if m.screen != screenOrchestrators {
		t.Fatalf("shift+→ twice should reach the Orchestrators screen, got %v", m.screen)
	}
	m = feed(m, cmd) // orchRowsMsg
	if len(m.orch.rows) != 2 {
		t.Fatalf("rows = %d, want 2 (%v)", len(m.orch.rows), d.orchCallsJoined())
	}
	m, _ = update(m, press("j"))
	if m.orch.cursor != 1 {
		t.Errorf("orchestrator cursor = %d, want 1", m.orch.cursor)
	}
	m, _ = update(m, press("j")) // clamped
	if m.orch.cursor != 1 {
		t.Errorf("cursor must clamp at the last row, got %d", m.orch.cursor)
	}
	v := m.View()
	for _, want := range []string{"Orchestrators", "alpha-bot", "beta-bot", "2 workers", "queued 1", "reachable"} {
		if !strings.Contains(v, want) {
			t.Errorf("orchestrators screen should show %q; got:\n%s", want, v)
		}
	}
	// No wrap past the last screen.
	m, _ = update(m, shiftRight())
	if m.screen != screenOrchestrators {
		t.Error("shift+→ on the last screen must not wrap")
	}
	// Back: daemons keeps its cursor, then the list keeps its selection.
	m, _ = update(m, shiftLeft())
	if m.screen != screenDaemons || m.daemons.cursor != 1 {
		t.Errorf("shift+← should return to daemons with cursor 1; screen %v cursor %d", m.screen, m.daemons.cursor)
	}
	m, _ = update(m, shiftLeft())
	if m.screen != screenList || m.list.Index() != 1 {
		t.Errorf("shift+← should return to the list with row 1; screen %v index %d", m.screen, m.list.Index())
	}
	// Forward again: the orchestrator cursor survived.
	m, _ = update(m, shiftRight())
	m, cmd = update(m, shiftRight())
	m = feed(m, cmd)
	if m.orch.cursor != 1 {
		t.Errorf("orchestrator cursor should survive the round trip, got %d", m.orch.cursor)
	}
	// esc goes home to the list; q quits.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should return to the sandbox list")
	}
}

// The header strip names all three screens on every top-level screen with the
// current one highlighted, and the shift-arrow help labels change per screen.
func TestHeaderStripAndDynamicLabels(t *testing.T) {
	d := &fakeDaemon{}
	m := twoHostModel(t, d)

	strip := func(m Model) string { return m.header() }
	for _, want := range []string{"Sandboxes", "Daemons", "Orchestrators", "shift+←/→"} {
		if !strings.Contains(strip(m), want) {
			t.Errorf("list header missing %q", want)
		}
	}
	if m.topLevel() != screenList || m.screenNextLabel() != "daemons" || m.screenPrevLabel() != "" {
		t.Errorf("list labels: next %q prev %q", m.screenNextLabel(), m.screenPrevLabel())
	}
	if help := m.renderHelp(m.listHelp()); !strings.Contains(help, "daemons") {
		t.Errorf("list help should name the next screen: %q", help)
	}

	m, _ = update(m, shiftRight())
	if m.topLevel() != screenDaemons || m.screenNextLabel() != "orchestrators" || m.screenPrevLabel() != "sandboxes" {
		t.Errorf("daemons labels: next %q prev %q", m.screenNextLabel(), m.screenPrevLabel())
	}
	help := m.renderHelp(m.daemonsHelp())
	if !strings.Contains(help, "orchestrators") || !strings.Contains(help, "sandboxes") {
		t.Errorf("daemons help should name both neighbours: %q", help)
	}
	if !strings.Contains(strip(m), "Orchestrators") {
		t.Error("daemon screen header should keep the strip")
	}

	m, _ = update(m, shiftRight())
	if m.topLevel() != screenOrchestrators || m.screenPrevLabel() != "daemons" || m.screenNextLabel() != "" {
		t.Errorf("orchestrators labels: next %q prev %q", m.screenNextLabel(), m.screenPrevLabel())
	}
	if help := m.renderHelp(m.orchestratorsHelp()); !strings.Contains(help, "daemons") {
		t.Errorf("orchestrators help should name the previous screen: %q", help)
	}
	// The strip highlights the current screen through selectedStyle; without a
	// colour terminal the styles render as plain text, so the choice is pinned
	// via topLevel (above) and the strip's content here.
	if !strings.Contains(strip(m), "Orchestrators") || !strings.Contains(strip(m), "Daemons") {
		t.Error("the strip should name every screen on the orchestrators screen")
	}
	// Settings opened from the orchestrators screen keeps the strip on it.
	m, _ = update(m, press(","))
	if m.screen != screenSettings || m.topLevel() != screenOrchestrators {
		t.Errorf("settings from orchestrators: screen %v top %v", m.screen, m.topLevel())
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenOrchestrators {
		t.Error("esc from settings should return to the orchestrators screen")
	}
	// A narrow terminal drops the strip rather than overflowing.
	m.width = 30
	if strings.Contains(m.header(), "Orchestrators") {
		t.Error("a narrow header should drop the strip")
	}
}

// Without a host manager the screen fans out over the lone active daemon, and
// a failing daemon simply contributes no rows.
func TestOrchestratorsScreenSingleHostAndErrors(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o1", Name: "solo"}}}}
	m := sized(New(d, "/work"))
	mm, cmd := m.enterOrchestrators()
	m = feed(mm.(Model), cmd)
	if len(m.orch.rows) != 1 || m.orch.rows[0].summary.GetOrchestrator().GetName() != "solo" {
		t.Fatalf("rows = %+v", m.orch.rows)
	}
	if !strings.Contains(m.View(), "solo") {
		t.Error("the row should render")
	}
	// `r` reloads; a failing daemon keeps its last-known rows marked
	// unreachable with the error, never an error screen.
	d.orchErr = errBoom{}
	m, cmd = update(m, press("r"))
	m = feed(m, cmd)
	if len(m.orch.rows) != 1 || m.orch.rows[0].reachable || m.orch.rows[0].reason != "boom" {
		t.Errorf("failing daemon should keep rows unreachable: %+v", m.orch.rows)
	}
	if !strings.Contains(m.View(), "unreachable") {
		t.Error("the row should render unreachable")
	}
	// An orchestrator_changed event while the screen is open reloads it (no
	// stream is open, so nothing is re-armed and feed runs only the reload).
	d.orchErr = nil
	m.sub = nil
	m, cmd = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "x", OrchestratorId: "o1"}}}})
	m = feed(m, cmd)
	if len(m.orch.rows) != 1 {
		t.Errorf("orchestrator_changed should reload the rows, got %d", len(m.orch.rows))
	}
	// Up/down on an empty screen are safe.
	m.orch.rows = nil
	m, _ = update(m, press("k"))
	m, _ = update(m, press("j"))
	if _, ok := m.orchCurrent(); ok {
		t.Error("no current row on an empty screen")
	}
	m, _ = update(m, press("q"))
	if !m.quitting {
		t.Error("q should quit from the orchestrators screen")
	}
}
