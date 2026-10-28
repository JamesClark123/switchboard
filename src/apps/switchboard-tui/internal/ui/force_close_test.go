package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Forced closes and the add-override queue (SWI-9 research R7, T032–T034).

// nudgeMsg is a non-key message with no handler of its own.
type nudgeMsg struct{}

func dropped(host string) hostsMsg {
	return hostsMsg{{Host: client.HostConn{Entry: client.HostEntry{ID: host, DisplayName: host, Kind: "ssh"}, State: client.HostDisconnected}}}
}

func removedEvent(id string) eventMsg {
	return eventMsg{ev: &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: id}}}}
}

func listData(host string, state client.HostState, err error, sbs ...*pb.Sandbox) listDataMsg {
	return listDataMsg{hosts: []client.HostSandboxes{{
		Host: client.HostConn{Entry: client.HostEntry{ID: host, DisplayName: host}, State: state, Err: err}, Sandboxes: sbs,
	}}}
}

func blockedAdd(id, host string) sourcesAddResultMsg {
	return sourcesAddResultMsg{id: id, host: host, sources: []*pb.SourceRef{{Path: "/w/lib"}},
		blocked: &pb.ResourceReport{Warnings: []string{"low disk"}}}
}

// renaming opens a changed rename of the list's sandbox on host h1.
func renaming(t *testing.T) (Model, *pb.Sandbox) {
	t.Helper()
	d := &fakeDaemon{}
	m := listModel(t, d)
	sb := d.sandboxes[0]
	mm, _ := m.enterRename(sb, "h1")
	m = typeIn(mm.(Model), "x")
	return m, sb
}

func TestForceCloseOnHostDrop(t *testing.T) {
	noKeyGrace(t)
	// MCP form changed, dialog open.
	m := pressCmd(mcpModel(t, &fakeDaemon{}), "a")
	m = typeIn(m, "x")
	m, _ = update(m, keyEsc)
	if !m.unsaved.open {
		t.Fatal("dialog should be open")
	}
	m, _ = update(m, dropped(m.mcpView.host))
	if m.unsaved.open || m.screen != screenDaemons || !strings.Contains(m.status, "disconnected — gateway view closed; unsaved edits lost") {
		t.Errorf("MCP drop: screen %v dialog %v status %q", m.screen, m.unsaved.open, m.status)
	}

	// A changed rename on the dropped host closes; an unchanged one says nothing lost.
	r, _ := renaming(t)
	r, _ = update(r, dropped("h1"))
	if r.screen != screenList || r.status != "unsaved edits lost: daemon disconnected" {
		t.Errorf("rename drop: screen %v status %q", r.screen, r.status)
	}
	r, _ = renaming(t)
	r.rename.SetValue(r.rename.Value()[:len(r.rename.Value())-1])
	r.status = ""
	r, _ = update(r, dropped("h1"))
	if r.screen != screenList || strings.Contains(r.status, "lost") {
		t.Errorf("clean rename drop: screen %v status %q", r.screen, r.status)
	}
}

func TestForceCloseOrchRenameOnHostDrop(t *testing.T) {
	noKeyGrace(t)
	for _, changed := range []bool{true, false} {
		a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
		m, _, _ := twoFakeHosts(t, a, &fakeDaemon{})
		m, _ = update(openDaemonOrchs(t, m, "hosta"), press("R"))
		if changed {
			m = typeIn(m, "x")
		}
		host := m.daemonOrchs.host
		m, _ = update(m, dropped(host))
		lost := strings.HasSuffix(m.status, "disconnected — orchestrators closed; unsaved edits lost")
		if m.screen != screenDaemons || m.orch.form != nil || lost != changed {
			t.Errorf("changed=%v: screen %v status %q", changed, m.screen, m.status)
		}
	}
}

func TestForceCloseOnSandboxRemoved(t *testing.T) {
	// By event.
	r, sb := renaming(t)
	r, _ = update(r, removedEvent(sb.GetId()))
	if r.screen != screenList || r.status != "unsaved edits lost: sandbox removed" {
		t.Errorf("Removed: screen %v status %q", r.screen, r.status)
	}
	// By a connected, error-free reload without it.
	r, _ = renaming(t)
	r, _ = update(r, listData("h1", client.HostConnected, nil))
	if r.screen != screenList {
		t.Error("a connected reload without the sandbox should close rename")
	}
	// A disconnected host or a failed List proves nothing.
	for _, msg := range []listDataMsg{listData("h1", client.HostDisconnected, nil), listData("h1", client.HostConnected, errBoom{})} {
		r, _ = renaming(t)
		r, _ = update(r, msg)
		if r.screen != screenRename {
			t.Error("rename should stay open")
		}
	}
	// A reload that still has it leaves rename open.
	r, sb = renaming(t)
	r, _ = update(r, listData("h1", client.HostConnected, nil, sb))
	if r.screen != screenRename {
		t.Error("rename closed though its sandbox is listed")
	}

	// The sources overlay with marks, and suspended under its removal confirm.
	m, _ := openSourcesOverlay(t)
	id := m.sourcesView.sandboxID
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, removedEvent(id))
	if m.screen != screenList || m.sourcesView.sandboxID != "" || !strings.Contains(m.status, "unsaved edits lost") {
		t.Errorf("sources: screen %v status %q", m.screen, m.status)
	}
	m, _ = openSourcesOverlay(t)
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm {
		t.Fatal("d should open the removal confirm")
	}
	m, _ = update(m, removedEvent(m.sourcesView.sandboxID))
	if m.screen != screenList || m.confirm.onConfirm != nil {
		t.Errorf("suspended sources: screen %v confirm kept %v", m.screen, m.confirm.onConfirm != nil)
	}
	if m, _ = update(m, press("n")); m.screen == screenSources || m.screen == screenConfirm {
		t.Error("n should no longer reach the dropped confirm")
	}
}

func TestForceCloseOnOrchestratorRemoved(t *testing.T) {
	noKeyGrace(t)
	// The reasoning setup, changed.
	m, _ := editedReasoningSetup(t)
	m, _ = update(m, orchGoneMsg{host: m.launch.reasoning.host, id: m.launch.reasoning.id})
	if m.screen != screenOrchDetail || m.status != "unsaved edits lost: orchestrator removed" {
		t.Errorf("reasoning: screen %v status %q", m.screen, m.status)
	}

	// Under its Rebuild confirm, a host drop closes the confirm and the setup.
	m, _ = editedReasoningSetup(t)
	m = openDialog(t, m, gReasoning)
	m, _ = update(m, press("r"))
	if m.screen != screenConfirm {
		t.Fatal("Rebuild should open its confirm")
	}
	m.launch.reasoning.host = "hx" // the single-host fixture has no host id
	m, cmd := update(m, dropped("hx"))
	if m.screen != screenDaemons || m.confirm.onConfirm != nil || !strings.HasSuffix(m.status, "orchestrators closed; unsaved edits lost") {
		t.Errorf("rebuild drop: screen %v status %q", m.screen, m.status)
	}
	if _, ok := runCmd(cmd).(reasoningRebuildMsg); ok {
		t.Error("the dropped confirm must never rebuild")
	}

	// A Jobs op form whose orchestrator a reload finds gone.
	j, _ := jobsWith(t, jobsActions, nil, nil)
	j, _ = update(j, press("a"))
	j = typeIn(j, "x")
	tgt := j.jobs.formTarget
	j, _ = update(j, jobsOrchMsg{host: tgt.host, id: tgt.id, recSeq: jobsSeq.Add(1), gone: true})
	if j.jobs.form != nil || !strings.Contains(j.status+j.jobs.status, "unsaved edits lost") {
		t.Errorf("op form: open %v status %q / %q", j.jobs.form != nil, j.status, j.jobs.status)
	}
}

func TestOverrideOpensOnThePlainList(t *testing.T) {
	d := &fakeDaemon{}
	m := listModel(t, d)
	m, _ = update(m, blockedAdd(d.sandboxes[0].GetId(), ""))
	if m.screen != screenConfirm || !m.confirm.yOnly || len(m.pendingOverrides) != 0 {
		t.Fatalf("override should open at once, y-only: screen %v", m.screen)
	}
}

func TestOverrideParksDuringRename(t *testing.T) {
	r, sb := renaming(t)
	id := sb.GetId()
	r, _ = update(r, blockedAdd(id, "h1"))
	r, _ = update(r, blockedAdd(id, "h1"))
	if r.screen != screenRename || len(r.pendingOverrides) != 2 || !strings.Contains(r.status, "needs confirmation — back on the list") {
		t.Fatalf("parked: screen %v queue %d status %q", r.screen, len(r.pendingOverrides), r.status)
	}
	// The enter that closes rename does not open (or accept) it.
	r, _ = update(r, tea.KeyMsg{Type: tea.KeyEnter})
	if r.screen != screenList {
		t.Fatalf("enter should submit rename, screen %v", r.screen)
	}
	r, _ = update(r, nudgeMsg{})
	if r.screen != screenConfirm {
		t.Fatal("the next non-key message should open the override")
	}
	for range 5 {
		r, _ = update(r, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if r.screen != screenConfirm {
		t.Fatal("enter must never accept a queued override")
	}
	r, _ = update(r, press("n"))
	r, _ = update(r, nudgeMsg{})
	if r.screen != screenConfirm || len(r.pendingOverrides) != 0 {
		t.Fatal("the second parked override should open next")
	}
	_, cmd := update(r, press("y"))
	if _, ok := runCmd(cmd).(sourcesOverrideMsg); !ok {
		t.Error("y should accept")
	}
}

func TestOverrideStaysQueuedOffTheList(t *testing.T) {
	// The sign-in failure card is up.
	d := &fakeDaemon{}
	m := listModel(t, d)
	m.signIn.active, m.signIn.phase = true, signInFailed
	m, _ = update(m, blockedAdd(d.sandboxes[0].GetId(), ""))
	if m.screen == screenConfirm || len(m.pendingOverrides) != 1 {
		t.Error("an override must not open under the sign-in card")
	}
	m.signIn.active = false
	if m, _ = update(m, nudgeMsg{}); m.screen != screenConfirm {
		t.Error("it should open once back on the plain list")
	}

	// Another sandbox's overlay, with marks: parked, marks kept.
	o, _ := openSourcesOverlay(t)
	o, _ = update(o, tea.KeyMsg{Type: tea.KeySpace})
	marks := len(o.sourcesView.marked)
	o, _ = update(o, blockedAdd("other", ""))
	if o.screen != screenSources || len(o.sourcesView.marked) != marks || len(o.pendingOverrides) != 1 {
		t.Error("an override for another sandbox should park and keep the marks")
	}
	// Its own overlay: it opens there and returns there.
	o, _ = update(o, blockedAdd(o.sourcesView.sandboxID, ""))
	if len(o.pendingOverrides) != 2 {
		t.Error("the head (another sandbox) blocks the queue")
	}
}

func TestOverridePurgedWhenItsSandboxGoes(t *testing.T) {
	r, sb := renaming(t)
	r, _ = update(r, blockedAdd(sb.GetId(), "h1"))
	r, _ = update(r, removedEvent(sb.GetId()))
	if len(r.pendingOverrides) != 0 || !strings.Contains(r.status, "add of lib to") || !strings.Contains(r.status, "cancelled: sandbox removed") {
		t.Errorf("purge: queue %d status %q", len(r.pendingOverrides), r.status)
	}
	if r, _ = update(r, nudgeMsg{}); r.screen == screenConfirm {
		t.Error("a purged override must never open")
	}
}

func TestTerminalOpeningElsewhereIsClosed(t *testing.T) {
	r, _ := renaming(t)
	sess := &fakeTermSession{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r, _ = update(r, termOpenedMsg{session: sess, cancel: cancel, done: done, name: "feature-work", returnTo: screenList, seq: r.termSeq})
	if r.screen != screenRename || !strings.Contains(r.status, "terminal for feature-work not opened — you moved on") {
		t.Errorf("screen %v status %q", r.screen, r.status)
	}
	if ctx.Err() == nil {
		t.Error("the session's context should be cancelled")
	}
	select {
	case <-done:
	default:
		t.Error("done should be closed")
	}
	if r, _ = update(r, termClosedMsg{name: "feature-work", seq: r.termSeq}); r.screen != screenRename {
		t.Error("termClosed must not move off rename")
	}
}
