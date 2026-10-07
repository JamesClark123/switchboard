package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func shiftRight() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyShiftRight} }
func shiftLeft() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyShiftLeft} }

// twoHostModel wires a manager with one connected and one disconnected host.
func twoHostModel(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	d.sandboxes = []*pb.Sandbox{
		{Id: "s1", DisplayName: "one", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
		{Id: "s2", DisplayName: "two", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
		{Id: "s3", DisplayName: "three", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
	}
	m := withSandboxes(sized(New(d, "/work")), d.sandboxes)
	mgr := client.NewManager()
	mgr.Upsert(client.HostEntry{ID: "local", DisplayName: "laptop", Kind: "local", SocketPath: "/tmp/s.sock"})
	mgr.Adopt("local", nil) // connected (the fake stands in for the conn)
	mgr.Upsert(client.HostEntry{ID: "box", DisplayName: "build-box", Kind: "ssh", SSHTarget: "me@box"})
	return m.WithHosts(mgr, nil, "local")
}

func TestShiftArrowsSwitchScreensAndPreserveCursors(t *testing.T) {
	d := &fakeDaemon{daemonVer: "v0.9", sbxVer: "sbx version 0.46.0", baselineMet: true, mcpAvail: true}
	m := twoHostModel(t, d)
	m.list.Select(2)

	m, _ = update(m, shiftRight())
	if m.screen != screenDaemons {
		t.Fatalf("shift+→ should open the daemon screen, got %v", m.screen)
	}
	v := m.View()
	for _, want := range []string{"Daemons", "laptop", "build-box", "sxbd v0.9", "sbx sbx version 0.46.0"} {
		if !strings.Contains(v, want) {
			t.Errorf("daemon screen should show %q; got:\n%s", want, v)
		}
	}
	m, _ = update(m, press("j"))
	if m.daemons.cursor != 1 {
		t.Errorf("daemon cursor = %d, want 1", m.daemons.cursor)
	}
	m, _ = update(m, shiftLeft())
	if m.screen != screenList || m.list.Index() != 2 {
		t.Errorf("shift+← should return to the list with row 2 selected; screen %v index %d", m.screen, m.list.Index())
	}
	m, _ = update(m, shiftRight())
	if m.daemons.cursor != 1 {
		t.Errorf("daemon cursor should survive the round trip; got %d", m.daemons.cursor)
	}
}

func TestDaemonOptionsOnlyForConnectedRows(t *testing.T) {
	d := &fakeDaemon{mcpAvail: true}
	m := twoHostModel(t, d)
	m, _ = update(m, shiftRight())
	m, _ = update(m, press("j")) // build-box: disconnected
	m, _ = update(m, press("enter"))
	if m.daemons.options {
		t.Fatal("a disconnected daemon must not open management options")
	}
	if !strings.Contains(m.status, "no management options") {
		t.Errorf("status = %q, want an explanation", m.status)
	}
	m, _ = update(m, press("k")) // laptop: connected
	m, _ = update(m, press("enter"))
	if !m.daemons.options || !strings.Contains(m.View(), "MCP gateway") {
		t.Fatalf("a connected daemon should list the MCP gateway option; got:\n%s", m.View())
	}
	m, _ = update(m, press("enter"))
	if m.screen != screenMcp || m.mcpView.host != "local" {
		t.Fatalf("choosing the option should open the gateway view for laptop; screen %v host %q", m.screen, m.mcpView.host)
	}
	m, _ = update(m, press("esc"))
	if m.screen != screenDaemons {
		t.Errorf("esc should return to the daemon screen, got %v", m.screen)
	}
	m, _ = update(m, press("esc"))
	if m.daemons.options {
		t.Error("esc should close the option list")
	}
}

func TestDaemonRowShowsBaselineAndGatewayMarkers(t *testing.T) {
	d := &fakeDaemon{sbxVer: "sbx version 0.35.0", sbxMin: "0.36.0", baselineMet: false, mcpReason: "not signed in"}
	m := twoHostModel(t, d)
	m, _ = update(m, shiftRight())
	v := m.View()
	for _, want := range []string{"below baseline (min 0.36.0)", "gateway: not signed in"} {
		if !strings.Contains(v, want) {
			t.Errorf("row should show %q; got:\n%s", want, v)
		}
	}
}

func TestDaemonScreenWithoutManagerShowsLocalDaemon(t *testing.T) {
	d := &fakeDaemon{hostID: "solo", mcpAvail: true}
	m := sized(New(d, "/work"))
	m, _ = update(m, shiftRight())
	if m.screen != screenDaemons || !strings.Contains(m.View(), "solo") {
		t.Errorf("single-host model should still show the local daemon; got:\n%s", m.View())
	}
	m, _ = update(m, press("enter"))
	m, _ = update(m, press("enter"))
	if m.screen != screenMcp {
		t.Errorf("gateway view should open for the local daemon, screen %v", m.screen)
	}
}

// --- feature 008, US4: connect / disconnect from the daemon screen ---

func TestDaemonConnectOpensCenteredPromptAndReportsOutcome(t *testing.T) {
	d := &fakeDaemon{mcpAvail: true}
	m := twoHostModel(t, d)
	m, _ = update(m, shiftRight())
	m, _ = update(m, press("j")) // build-box (ssh, disconnected)
	m, cmd := update(m, press("c"))
	if cmd == nil || !m.hosts.connecting || m.hosts.pwMode != pwDaemonsScreen {
		t.Fatalf("c on a disconnected ssh row should open the password prompt; connecting %v mode %v", m.hosts.connecting, m.hosts.pwMode)
	}
	if v := m.View(); !strings.Contains(v, "Connect to me@box") || !strings.Contains(v, "Daemons") {
		t.Errorf("the prompt should float centered over the daemon screen; got:\n%s", v)
	}
	// Submitting hands the connect to the manager and marks the row for a report.
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.hosts.connecting || m.hosts.lastConnect != "box" {
		t.Fatalf("enter should dispatch the connect; cmd %v connecting %v lastConnect %q", cmd != nil, m.hosts.connecting, m.hosts.lastConnect)
	}
	// Simulate the manager's outcome: the host came up.
	m.manager.Adopt("box", nil)
	m, _ = update(m, hostsMsg{{Host: client.HostConn{Entry: client.HostEntry{ID: "box", DisplayName: "build-box", Kind: "ssh"}, State: client.HostConnected}}})
	if m.status != "connected to build-box" {
		t.Errorf("status = %q", m.status)
	}
	if v := m.View(); !strings.Contains(v, "CONNECTED") {
		t.Errorf("row should now read connected; got:\n%s", v)
	}
	// A failure reports its reason instead.
	m.hosts.lastConnect = "box"
	m, _ = update(m, hostsMsg{{Host: client.HostConn{Entry: client.HostEntry{ID: "box", DisplayName: "build-box", Kind: "ssh"}, State: client.HostDisconnected, Err: errBoom{}}}})
	if !strings.Contains(m.status, "could not connect to build-box") || !strings.Contains(m.status, "boom") {
		t.Errorf("status = %q", m.status)
	}
	// esc on the prompt cancels without connecting.
	m, _ = update(m, press("c"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.hosts.connecting || m.hosts.lastConnect != "" {
		t.Error("esc must close the prompt without a connect")
	}
}

func TestDaemonDisconnectClosesGatewayViewAndSparesLocal(t *testing.T) {
	d := &fakeDaemon{mcpAvail: true}
	m := twoHostModel(t, d)
	m.manager.Adopt("box", nil)
	m, _ = update(m, shiftRight())
	m, _ = update(m, press("j")) // build-box, now connected
	m, _ = update(m, press("enter"))
	m, _ = update(m, press("enter")) // its gateway view
	if m.screen != screenMcp || m.mcpView.host != "box" {
		t.Fatalf("expected build-box's gateway view; screen %v host %q", m.screen, m.mcpView.host)
	}
	// The host drops while the view is open: it closes with a message (FR-071).
	m, _ = update(m, hostsMsg{{Host: client.HostConn{Entry: client.HostEntry{ID: "box", DisplayName: "build-box", Kind: "ssh"}, State: client.HostDisconnected}}})
	if m.screen != screenDaemons || !strings.Contains(m.status, "gateway view closed") {
		t.Errorf("screen %v status %q", m.screen, m.status)
	}
	// x on a connected ssh row disconnects it; x on the local daemon is refused.
	m.manager.Adopt("box", nil)
	m, _ = update(m, press("x"))
	if hc, _ := m.manager.Get("box"); hc.State == client.HostConnected || !strings.Contains(m.status, "disconnected build-box") {
		t.Errorf("x should disconnect build-box; state %v status %q", hc.State, m.status)
	}
	m, _ = update(m, press("k"))
	m, _ = update(m, press("x"))
	if hc, _ := m.manager.Get("local"); hc.State != client.HostConnected || !strings.Contains(m.status, "stays connected") {
		t.Errorf("the local daemon must stay connected; state %v status %q", hc.State, m.status)
	}
	// Sandbox-list keys do nothing here.
	m, cmd := update(m, press("d"))
	if m.screen != screenDaemons || cmd != nil {
		t.Error("sandbox keys must be inert on the daemon screen")
	}
}
