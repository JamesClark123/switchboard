package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func orchSummary(id, name string, hosts ...*pb.OrchestratorHost) *pb.OrchestratorSummary {
	return &pb.OrchestratorSummary{Orchestrator: &pb.Orchestrator{Id: id, Name: name, Hosts: hosts}}
}

// openOrchScreen enters the Orchestrators screen and loads its rows.
func openOrchScreen(m Model) Model {
	mm, cmd := m.enterOrchestrators()
	return feed(mm.(Model), cmd)
}

func TestOrchestratorRowsAggregateAcrossHosts(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	m = openOrchScreen(m)
	if len(m.orch.rows) != 2 {
		t.Fatalf("rows = %+v", m.orch.rows)
	}
	if m.orch.rows[0].hostName != "alpha" || m.orch.rows[1].hostName != "beta" || !m.orch.rows[1].reachable {
		t.Errorf("rows should carry machine names and reachability: %+v", m.orch.rows)
	}
	v := m.View()
	for _, want := range []string{"alpha-bot", "@alpha", "beta-bot", "@beta", "reachable"} {
		if !strings.Contains(v, want) {
			t.Errorf("screen missing %q:\n%s", want, v)
		}
	}
	// Beta disconnects: its row stays, marked unreachable with the reason, and
	// R / d are refused on it.
	mgr.Disconnect("hostb")
	m, _ = update(m, press("r"))
	m = feed(m, m.orchLoadCmd())
	if len(m.orch.rows) != 2 || m.orch.rows[1].reachable || !strings.Contains(m.orch.rows[1].reason, "disconnected") {
		t.Fatalf("after disconnect: %+v", m.orch.rows)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("R"))
	if m.orch.form != nil || !strings.Contains(m.status, "unreachable") {
		t.Errorf("R on an unreachable row must be refused: form %v status %q", m.orch.form != nil, m.status)
	}
	m, _ = update(m, press("d"))
	if m.screen != screenOrchestrators {
		t.Error("d on an unreachable row must not open the confirm")
	}
	if !strings.Contains(m.View(), "unreachable") {
		t.Error("the row should say unreachable")
	}
}

func TestOrchestratorCreateRenameRemove(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openOrchScreen(m)

	// n opens the create form; a duplicate across hosts is refused naming the
	// existing one's host, and nothing is sent.
	m, _ = update(m, press("n"))
	if m.orch.form == nil || m.orch.formKind != orchFormCreate {
		t.Fatal("n should open the create form")
	}
	if !strings.Contains(m.View(), "New orchestrator") {
		t.Error("the form should render")
	}
	m.orch.vals.name = "alpha-bot"
	m, _ = update(m, ctrlS())
	if m.orch.form == nil || !strings.Contains(m.status, "already exists on alpha") {
		t.Fatalf("duplicate should be refused in place: form %v status %q", m.orch.form != nil, m.status)
	}
	m.orch.vals.name = " "
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.status, "name is required") {
		t.Errorf("empty name status = %q", m.status)
	}
	m.orch.vals.name = strings.Repeat("x", 65)
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.status, "64") {
		t.Errorf("long name status = %q", m.status)
	}
	// Create on beta.
	m.orch.vals.name = "review-bot"
	m.orch.vals.host = "hostb"
	m, cmd := update(m, ctrlS())
	if m.orch.form != nil {
		t.Fatal("submitting should close the form")
	}
	m = feed2(m, cmd) // orchDoneMsg → reload
	if !strings.Contains(b.orchCallsJoined(), "CreateOrchestrator review-bot") || strings.Contains(a.orchCallsJoined(), "CreateOrchestrator") {
		t.Errorf("create should reach beta only: a=%q b=%q", a.orchCallsJoined(), b.orchCallsJoined())
	}
	if !strings.Contains(m.status, "created review-bot on beta") {
		t.Errorf("status = %q", m.status)
	}
	if len(m.orch.rows) != 2 || m.orch.rows[1].summary.GetOrchestrator().GetName() != "review-bot" {
		t.Fatalf("rows after create = %+v", m.orch.rows)
	}
	// esc cancels a form.
	m, _ = update(m, press("n"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.orch.form != nil {
		t.Error("esc should cancel the form")
	}

	// R renames the highlighted row on its host.
	m.orch.cursor = 1
	m, _ = update(m, press("R"))
	if m.orch.form == nil || m.orch.formKind != orchFormRename || m.orch.vals.name != "review-bot" {
		t.Fatal("R should open the rename form pre-filled")
	}
	m.orch.vals.name = "alpha-bot"
	m, _ = update(m, ctrlS())
	if m.orch.form == nil || !strings.Contains(m.status, "already exists") {
		t.Error("renaming onto another row's name is refused")
	}
	m.orch.vals.name = "rb2"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "RenameOrchestrator orch-review-bot rb2") {
		t.Errorf("rename calls = %q", b.orchCallsJoined())
	}
	if m.orch.rows[1].summary.GetOrchestrator().GetName() != "rb2" || m.orch.cursor != 1 {
		t.Errorf("after rename: %+v cursor %d", m.orch.rows, m.orch.cursor)
	}

	// d opens the confirm naming the consequences; n cancels, y removes.
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || m.confirm.returnTo != screenOrchestrators {
		t.Fatalf("d should open the confirm over the orchestrators screen; screen %v", m.screen)
	}
	v := m.View()
	for _, want := range []string{"Remove orchestrator rb2?", "detached", "reasoning sandbox is destroyed", "cancelled", "failed", "history", "Orchestrators"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm missing %q", want)
		}
	}
	m, _ = update(m, press("n"))
	if m.screen != screenOrchestrators || strings.Contains(b.orchCallsJoined(), "RemoveOrchestrator") {
		t.Fatal("cancel must not remove")
	}
	m, _ = update(m, press("d"))
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "RemoveOrchestrator orch-review-bot") {
		t.Errorf("remove calls = %q", b.orchCallsJoined())
	}
	if len(m.orch.rows) != 1 || !strings.Contains(m.status, "removed rb2") {
		t.Errorf("after remove: %+v status %q", m.orch.rows, m.status)
	}
	// A daemon error is shown verbatim.
	a.orchErr = errBoom{}
	m, _ = update(m, press("n"))
	m.orch.vals.name = "fails"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.HasPrefix(m.status, "error: ") {
		t.Errorf("error status = %q", m.status)
	}
}

func TestOrchestratorChangedReloadsOnlyThatHost(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openOrchScreen(m)
	a.orchCalls, b.orchCalls = nil, nil
	m.sub = nil
	m, cmd := update(m, eventMsg{hostID: "hostb", ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "hostb", OrchestratorId: "o-b"}}}})
	m = feed(m, cmd)
	// The screen fans out over every host (the rows are one list), so both
	// daemons answer; what matters is that the rows refresh without a key press.
	if !strings.Contains(a.orchCallsJoined(), "ListOrchestrators") {
		t.Error("orchestrator_changed should reload the screen")
	}
	// enter opens the detail; esc returns with the cursor kept.
	m.orch.cursor = 0
	m, cmd = update(m, press("enter"))
	if m.screen != screenOrchDetail || m.orchDetail.id != "o-a" || m.orchDetail.hostName != "alpha" {
		t.Fatalf("enter should open the detail: screen %v detail %+v", m.screen, m.orchDetail)
	}
	m = feed(m, cmd)
	if m.orchDetail.detail == nil || !strings.Contains(m.View(), "Orchestrator alpha-bot") {
		t.Errorf("detail should load: %+v", m.orchDetail.detail)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenOrchestrators || m.orch.cursor != 0 {
		t.Error("esc should return to the list with the cursor kept")
	}
}

// With a single daemon (no manager) the create form still offers the active
// machine and no form opens without a connected one.
func TestOrchestratorCreateSingleHost(t *testing.T) {
	d := &fakeDaemon{}
	m := sized(New(d, "/work"))
	m = openOrchScreen(m)
	m, _ = update(m, press("n"))
	if m.orch.form == nil || m.orch.vals.host != "" {
		t.Fatalf("form should target the active daemon: %+v", m.orch.vals)
	}
	m.orch.vals.name = "solo"
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(d.orchCallsJoined(), "CreateOrchestrator solo") || len(m.orch.rows) != 1 {
		t.Errorf("single-host create: calls %q rows %d", d.orchCallsJoined(), len(m.orch.rows))
	}
	// The help lists the screen's keys.
	help := m.renderHelp(m.orchestratorsHelp())
	for _, want := range []string{"new", "rename", "remove", "open", "reload", "daemons", "settings"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q: %q", want, help)
		}
	}
	m, _ = update(m, press("n"))
	if !strings.Contains(m.renderHelp(m.orchestratorsHelp()), "create") {
		t.Error("form help should name the submit key")
	}
	// Form navigation messages are forwarded to the form.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.orch.form == nil {
		t.Error("tab must not close the form")
	}
}
