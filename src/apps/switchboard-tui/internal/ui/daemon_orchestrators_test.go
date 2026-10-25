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

// openDaemonOrchs opens Daemons › <daemon> › Orchestrators for the daemon row
// with the given id ("" = the first) and loads the rows.
func openDaemonOrchs(t *testing.T, m Model, host string) Model {
	t.Helper()
	for _, r := range m.daemonRows() {
		if host == "" || r.id == host {
			mm, cmd := m.enterDaemonOrchs(r)
			return feed(mm.(Model), cmd)
		}
	}
	t.Fatalf("no daemon %q", host)
	return m
}

// openDetail opens the first orchestrator's detail from its daemon's
// Orchestrators section and loads it.
func openDetail(t *testing.T, m Model) Model {
	t.Helper()
	m = openDaemonOrchs(t, m, "")
	if len(m.orch.rows) == 0 {
		t.Fatal("no rows")
	}
	m.daemonOrchs.host, m.orch.cursor = m.orch.rows[0].host, 0
	m, cmd := update(m, press("enter"))
	return feed(m, cmd)
}

// Daemons offers an Orchestrators option next to MCP gateway and Secrets; it
// lists only that daemon's orchestrators, and esc goes back (FR-022, FR-023).
func TestDaemonsOrchestratorsOptionListsThatDaemon(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	m, mgr, _ := twoFakeHosts(t, a, b)
	if len(daemonOptions) != 3 || daemonOptions[2] != "Orchestrators" {
		t.Fatalf("daemon options = %v", daemonOptions)
	}
	m, _ = update(m, shiftRight()) // Daemons
	for i, r := range m.daemonRows() {
		if r.id == "hostb" {
			m.daemons.cursor = i
		}
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "Orchestrators") || !strings.Contains(m.View(), "Secrets") {
		t.Errorf("the options should include Orchestrators:\n%s", m.View())
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("j"))
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenDaemonOrchs || m.daemonOrchs.host != "hostb" {
		t.Fatalf("enter on Orchestrators should open beta's section: screen %v host %q", m.screen, m.daemonOrchs.host)
	}
	m = feed(m, cmd)
	v := m.View()
	if !strings.Contains(v, "beta-bot") || strings.Contains(v, "alpha-bot") || !strings.Contains(v, "on beta") {
		t.Errorf("only beta's orchestrators are listed:\n%s", v)
	}
	if m.topLevel() != screenDaemons || !strings.Contains(m.header(), "Jobs") {
		t.Error("the section belongs to the Daemons top level")
	}
	help := m.renderHelp(m.daemonOrchsHelp())
	for _, want := range []string{"new", "rename", "remove", "open", "reload", "daemons"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q: %q", want, help)
		}
	}
	// Beta disconnects: its row stays, marked unreachable with the reason, and
	// R / d are refused on it.
	mgr.Disconnect("hostb")
	m, cmd = update(m, press("r"))
	m = feed(m, cmd)
	rows := m.daemonOrchRows()
	if len(rows) != 1 || rows[0].reachable || !strings.Contains(rows[0].reason, "disconnected") {
		t.Fatalf("after disconnect: %+v", rows)
	}
	m, _ = update(m, press("R"))
	if m.orch.form != nil || !strings.Contains(m.status, "unreachable") {
		t.Errorf("R on an unreachable row must be refused: form %v status %q", m.orch.form != nil, m.status)
	}
	m, _ = update(m, press("d"))
	if m.screen != screenDaemonOrchs {
		t.Error("d on an unreachable row must not open the confirm")
	}
	if !strings.Contains(m.View(), "unreachable") {
		t.Error("the row should say unreachable")
	}
	// esc returns to Daemons with the options still open.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenDaemons || !m.daemons.options {
		t.Errorf("esc should return to Daemons; screen %v options %v", m.screen, m.daemons.options)
	}
}

func TestDaemonOrchestratorsCreateRenameRemove(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openDaemonOrchs(t, m, "hostb")
	if !strings.Contains(m.View(), "no orchestrators on beta") {
		t.Errorf("empty section:\n%s", m.View())
	}

	// n opens the create form for this daemon; a duplicate across hosts is refused
	// naming the existing one's host, and nothing is sent.
	m, _ = update(m, press("n"))
	if m.orch.form == nil || m.orch.formKind != orchFormCreate || m.orch.vals.host != "hostb" {
		t.Fatal("n should open the create form for beta")
	}
	if !strings.Contains(m.View(), "New orchestrator on beta") || !strings.Contains(m.renderHelp(m.daemonOrchsHelp()), "create") {
		t.Error("the form should render, naming the daemon")
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
	// Form navigation messages are forwarded to the form.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyTab})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.orch.form == nil {
		t.Error("tab must not close the form")
	}
	m.orch.vals.name = "review-bot"
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
	if rows := m.daemonOrchRows(); len(rows) != 1 || rows[0].summary.GetOrchestrator().GetName() != "review-bot" {
		t.Fatalf("rows after create = %+v", rows)
	}
	// esc cancels a form.
	m, _ = update(m, press("n"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.orch.form != nil {
		t.Error("esc should cancel the form")
	}

	// R renames the highlighted row on its host.
	m, _ = update(m, press("R"))
	if m.orch.form == nil || m.orch.formKind != orchFormRename || m.orch.vals.name != "review-bot" {
		t.Fatal("R should open the rename form pre-filled")
	}
	if !strings.Contains(m.View(), "Rename orchestrator") || !strings.Contains(m.renderHelp(m.daemonOrchsHelp()), "rename") {
		t.Error("the rename form renders")
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
	if rows := m.daemonOrchRows(); rows[0].summary.GetOrchestrator().GetName() != "rb2" || m.orch.cursor != 0 {
		t.Errorf("after rename: %+v cursor %d", rows, m.orch.cursor)
	}

	// d opens the confirm naming the consequences; n cancels, y removes.
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm || m.confirm.returnTo != screenDaemonOrchs {
		t.Fatalf("d should open the confirm over the section; screen %v", m.screen)
	}
	v := m.View()
	for _, want := range []string{"Remove orchestrator rb2?", "detached", "reasoning sandbox is destroyed", "cancelled", "failed", "history", "Orchestrators"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm missing %q", want)
		}
	}
	if m.topLevel() != screenDaemons {
		t.Error("the confirm keeps the Daemons top level")
	}
	m, _ = update(m, press("n"))
	if m.screen != screenDaemonOrchs || strings.Contains(b.orchCallsJoined(), "RemoveOrchestrator") {
		t.Fatal("cancel must not remove")
	}
	m, _ = update(m, press("d"))
	m, cmd = update(m, press("y"))
	m = feed2(m, cmd)
	if !strings.Contains(b.orchCallsJoined(), "RemoveOrchestrator orch-review-bot") {
		t.Errorf("remove calls = %q", b.orchCallsJoined())
	}
	if len(m.daemonOrchRows()) != 0 || !strings.Contains(m.status, "removed rb2") {
		t.Errorf("after remove: status %q", m.status)
	}
	// A daemon error is shown verbatim.
	b.orchErr = errBoom{}
	m, _ = update(m, press("n"))
	m.orch.vals.name = "fails"
	m, cmd = update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.HasPrefix(m.status, "error: ") {
		t.Errorf("error status = %q", m.status)
	}
}

// The detail opened from the section has exactly Workers, Reasoning and Hosts
// (FR-024); esc returns to the section with the cursor kept; an
// orchestrator_changed event refreshes the open section and detail.
func TestDaemonOrchestratorsOpenDetailAndBack(t *testing.T) {
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot"), orchSummary("o-c", "gamma-bot")}}
	b := &fakeDaemon{}
	m, _, _ := twoFakeHosts(t, a, b)
	m = openDaemonOrchs(t, m, "hosta")
	m, _ = update(m, press("j"))
	m, _ = update(m, press("j")) // clamped at the last row
	m, _ = update(m, press("k"))
	m, _ = update(m, press("j"))
	if m.orch.cursor != 1 {
		t.Fatalf("cursor = %d", m.orch.cursor)
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenOrchDetail || m.orchDetail.id != "o-c" || m.orchDetail.hostName != "alpha" {
		t.Fatalf("enter should open the detail: screen %v detail %+v", m.screen, m.orchDetail)
	}
	m = feed(m, cmd)
	v := m.View()
	if !strings.Contains(v, "Orchestrator gamma-bot") {
		t.Errorf("detail should load:\n%s", v)
	}
	if int(orchViewCount) != 3 || orchViewNames != [orchViewCount]string{"Workers", "Reasoning", "Hosts"} {
		t.Errorf("the detail views = %v", orchViewNames)
	}
	for _, gone := range []string{"Actions", "Queue", "Events"} {
		if strings.Contains(m.orchViewTabs(), gone) {
			t.Errorf("the detail must not offer %s", gone)
		}
	}
	// orchestrator_changed reloads the rows and the open detail.
	a.orchCalls = nil
	m.sub = nil
	m, cmd = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "hosta", OrchestratorId: "o-c"}}}})
	m = feed(m, cmd)
	if calls := a.orchCallsJoined(); !strings.Contains(calls, "ListOrchestrators") || !strings.Contains(calls, "GetOrchestrator o-c") {
		t.Errorf("orchestrator_changed should reload the rows and the detail: %q", calls)
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenDaemonOrchs || m.orch.cursor != 1 {
		t.Errorf("esc should return to the section with the cursor kept; screen %v cursor %d", m.screen, m.orch.cursor)
	}
	m = feed(m, cmd)
	if m.orch.cursor != 1 {
		t.Errorf("the reload keeps the cursor on the same orchestrator, got %d", m.orch.cursor)
	}
	// An orchestrator_changed on the section itself reloads its rows.
	a.orchCalls = nil
	m, cmd = update(m, eventMsg{hostID: "hosta", ev: &pb.Event{Event: &pb.Event_OrchestratorChanged_{OrchestratorChanged: &pb.Event_OrchestratorChanged{HostId: "hosta", OrchestratorId: "o-a"}}}})
	m = feed(m, cmd)
	if !strings.Contains(a.orchCallsJoined(), "ListOrchestrators") {
		t.Error("orchestrator_changed should reload the section")
	}
	m, _ = update(m, press("q"))
	if !m.quitting {
		t.Error("q quits from the section")
	}
}

// With a single daemon (no manager) the section lists the active daemon's
// orchestrators and creates there; a failing daemon keeps its rows unreachable.
func TestDaemonOrchestratorsSingleHost(t *testing.T) {
	d := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o1", Name: "solo"}}}}
	m := sized(New(d, "/work"))
	m = openDaemonOrchs(t, m, "")
	if rows := m.daemonOrchRows(); len(rows) != 1 || rows[0].summary.GetOrchestrator().GetName() != "solo" {
		t.Fatalf("rows = %+v", rows)
	}
	m, _ = update(m, press("n"))
	if m.orch.form == nil || m.orch.vals.host != "" {
		t.Fatalf("form should target the active daemon: %+v", m.orch.vals)
	}
	m.orch.vals.name = "duo"
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if !strings.Contains(d.orchCallsJoined(), "CreateOrchestrator duo") || len(m.daemonOrchRows()) != 2 {
		t.Errorf("single-host create: calls %q rows %d", d.orchCallsJoined(), len(m.daemonOrchRows()))
	}
	// `r` reloads; a failing daemon keeps its last-known rows marked unreachable
	// with the error, never an error screen.
	d.orchErr = errBoom{}
	m, cmd = update(m, press("r"))
	m = feed(m, cmd)
	if rows := m.daemonOrchRows(); len(rows) != 2 || rows[0].reachable || rows[0].reason != "boom" {
		t.Errorf("failing daemon should keep rows unreachable: %+v", rows)
	}
	// Up/down/enter/R/d on an empty section are safe.
	m.orch.rows = nil
	for _, k := range []string{"k", "j", "R", "d"} {
		m, _ = update(m, press(k))
	}
	if _, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Error("enter on an empty section is a no-op")
	}
	if _, ok := m.orchCurrent(); ok {
		t.Error("no current row on an empty section")
	}
	// While the first load is in flight the section says so.
	m.orch.loaded, m.orch.loading = false, true
	if !strings.Contains(m.viewDaemonOrchs(), "loading") {
		t.Error("loading state")
	}
	m.orch.loaded = true
	if !strings.Contains(m.viewDaemonOrchs(), "refreshing") {
		t.Error("refreshing state")
	}
}
