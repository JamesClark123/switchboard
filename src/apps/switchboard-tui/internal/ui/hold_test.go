package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SC-008 (FR-533, research R10): a `v` failure or refusal arriving while an
// in-place terminal is open or opening, or while a Replace dialog for one is
// open, is held and shown in full, in the error style, once the terminal
// closes or the dialog is declined; joined to an attach or terminal error or a
// daemon drop; never held again once released.

const vRefusal = "WebStorm can't open folders on remote hosts — switch editors on the settings screen (,)"

// clock steps nowFunc.
func clock(t *testing.T) func(d time.Duration) {
	t.Helper()
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
	return func(d time.Duration) { now = now.Add(d) }
}

func vFail(m Model, text string) Model {
	m, _ = update(m, editorOutcomeMsg{sandboxID: "sb1", text: text, failed: true})
	return m
}

func assertRed(t *testing.T, m Model, want string) {
	t.Helper()
	if m.status != want {
		t.Errorf("status = %q, want %q", m.status, want)
	}
	if !m.statusIsErr() {
		t.Errorf("status %q should be in the error style", m.status)
	}
}

// openTerm presses t and opens the terminal.
func openTerm(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenTerminal {
		t.Fatalf("terminal not open: %v %q", m.screen, m.status)
	}
	return m
}

func ctrlQ(m Model) Model {
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	return m
}

func TestHoldWhileTerminalOpen(t *testing.T) {
	clock(t)
	m := openTerm(t, runningSbx(&fakeDaemon{}))
	m = vFail(m, vRefusal)
	if m.vHold.text != vRefusal || m.status != vRefusal {
		t.Fatalf("held %q shown %q", m.vHold.text, m.status)
	}
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
	if m.vHold.text != "" || !m.vFail.at.IsZero() {
		t.Error("released: nothing held, never held again")
	}
	// The next plain status isn't red.
	m, _ = update(m, statusMsg("stopped sb1"))
	if m.statusIsErr() {
		t.Error("a later plain message is not red")
	}
}

func TestHoldWhileOpening(t *testing.T) {
	clock(t)
	m := runningSbx(&fakeDaemon{})
	m, cmd := update(m, press("t"))
	m = vFail(m, vRefusal)
	if m.vHold.text != vRefusal || m.status != "opening terminal…" {
		t.Fatalf("held %q shown %q", m.vHold.text, m.status)
	}
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenTerminal || m.vHold.text != vRefusal {
		t.Fatal("the terminal opens with the message still held")
	}
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
}

// A first attach superseded by a second: only the second's message ends the hold.
func TestHoldSupersededAttach(t *testing.T) {
	clock(t)
	m := runningSbx(&fakeDaemon{})
	m, cmd1 := update(m, press("t"))
	m, cmd2 := update(m, press("t"))
	m = vFail(m, vRefusal)
	m, _ = update(m, runCmd(cmd1))
	if m.vHold.seq == 0 || m.screen == screenTerminal || m.status != "opening terminal…" {
		t.Fatalf("the superseded attach must not end the hold: seq %d screen %v status %q", m.vHold.seq, m.screen, m.status)
	}
	m, _ = update(m, runCmd(cmd2))
	if m.screen != screenTerminal || m.vHold.seq != 0 {
		t.Fatal("the second attach opens")
	}
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
}

// The Replace dialog: declined, and accepted more than 3 s after the refusal.
func TestHoldReplaceDialog(t *testing.T) {
	step := clock(t)
	for _, accept := range []bool{false, true} {
		m, d := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
		m = vFail(m, vRefusal) // showing, recent
		m, _ = update(m, press("t"))
		if m.screen != screenConfirm || !m.confirm.holdsV || m.vHold.text != vRefusal || !m.vHoldDialog() {
			t.Fatalf("dialog: screen %v holdsV %v held %q", m.screen, m.confirm.holdsV, m.vHold.text)
		}
		// A failure arriving while the dialog is open replaces the held one.
		m = vFail(m, "later failure")
		if m.vHold.text != "later failure" || m.status == "later failure" {
			t.Fatalf("held %q status %q", m.vHold.text, m.status)
		}
		step(5 * time.Second)
		if !accept {
			m, _ = update(m, press("n"))
			assertRed(t, m, "later failure")
			continue
		}
		m, cmd := update(m, press("y"))
		if m.vHold.seq != m.termSeq+1 || m.vHold.text != "later failure" {
			t.Fatalf("accept passes the hold to the attach: seq %d termSeq %d", m.vHold.seq, m.termSeq)
		}
		m, cmd = update(m, runCmd(cmd)) // replaceOrphanMsg → attach
		if m.vHold.seq != m.termSeq || m.status != replacingText {
			t.Fatalf("attach: seq %d termSeq %d status %q", m.vHold.seq, m.termSeq, m.status)
		}
		m, _ = update(m, runCmd(cmd))
		if m.screen != screenTerminal || d.replacedID != "p1" {
			t.Fatal("the replacing terminal opens")
		}
		m = ctrlQ(m)
		assertRed(t, m, "later failure")
	}
}

// An old disconnected or terminal error still showing when t opens the Replace
// dialog isn't joined to a failure held behind it.
func TestHoldDialogDoesNotJoinOldError(t *testing.T) {
	clock(t)
	for _, old := range []string{"beta disconnected — orchestrators closed", "terminal: old reason"} {
		m, _ := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
		m.status, m.termFail = old, old
		m, _ = update(m, press("t"))
		m = vFail(m, vRefusal)
		m, _ = update(m, press("n"))
		assertRed(t, m, vRefusal)
	}
}

// A Replace dialog opened from the orchestrator screens and dropped unanswered
// by a daemon drop: the held failure joins the drop's message; a later failure
// shows alone; with nothing held the message stays as today; a failure just
// after the drop replaces it.
func TestHoldDialogDroppedByDaemonDrop(t *testing.T) {
	clock(t)
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	sb := detachedSandbox("w1", pb.AgentStatus_AGENT_STATUS_IDLE, false)
	open := func() (Model, *client.Manager) {
		t.Helper()
		m, mgr, _ := twoFakeHosts(t, a, b)
		mm, cmd := m.enterDaemonOrchs(daemonRow{id: "hostb", name: "beta"})
		m = feed(mm.(Model), cmd)
		mm, _ = m.confirmReplaceOrphan(sb, "hostb", b, false, screenDaemonOrchs, sb.GetAgent().GetAgentCheck())
		m = mm.(Model)
		if m.screen != screenConfirm || !m.vHoldDialog() {
			t.Fatalf("dialog: %v", m.screen)
		}
		return m, mgr
	}
	m, mgr := open()
	m = vFail(m, vRefusal)
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if m.screen != screenDaemons {
		t.Fatalf("screen %v", m.screen)
	}
	assertRed(t, m, "beta disconnected — orchestrators closed; "+vRefusal)
	m = vFail(m, "later failure")
	assertRed(t, m, "later failure")
	// Nothing held: the message stays as today, not red.
	m, mgr = open()
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if m.status != "beta disconnected — orchestrators closed" || m.statusIsErr() {
		t.Errorf("nothing held: %q red %v", m.status, m.statusIsErr())
	}
	m = vFail(m, vRefusal)
	assertRed(t, m, vRefusal)
}

// A refusal then t is held; one older than 3 s isn't.
func TestHoldRecentRefusalOnly(t *testing.T) {
	step := clock(t)
	m := runningSbx(&fakeDaemon{})
	m = vFail(m, vRefusal)
	m = openTerm(t, m)
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
	m = vFail(m, vRefusal)
	step(4 * time.Second)
	m = openTerm(t, m)
	m = ctrlQ(m)
	if !strings.HasPrefix(m.status, "detached from") || m.statusIsErr() {
		t.Errorf("an old refusal is not held: %q", m.status)
	}
	// A released message can't be held again within its 3 seconds.
	m = vFail(m, vRefusal)
	m = openTerm(t, m)
	m = ctrlQ(m) // released
	m = openTerm(t, m)
	m = ctrlQ(m)
	if !strings.HasPrefix(m.status, "detached from") {
		t.Errorf("a released message is not held again: %q", m.status)
	}
}

// `terminal: <reason>; <refusal>` joined, red, error first; a second t within
// 3 s shows only its own error; moved on and external already attached join
// the same way; an accepted Replace whose attach fails too.
func TestHoldJoinsTerminalErrors(t *testing.T) {
	clock(t)
	d := &fakeDaemon{attachErr: errors.New("daemon unreachable")}
	m := runningSbx(d)
	m = vFail(m, vRefusal)
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	assertRed(t, m, "terminal: daemon unreachable; "+vRefusal)
	m, cmd = update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if m.status != "terminal: daemon unreachable" || m.statusIsErr() {
		t.Errorf("a retried t shows only its own error: %q red %v", m.status, m.statusIsErr())
	}
	// Moved on: t, then the settings screen before the attach lands.
	d.attachErr = nil
	m = runningSbx(d)
	m, cmd = update(m, press("t"))
	m = vFail(m, vRefusal)
	m, _ = update(m, press(","))
	m, _ = update(m, runCmd(cmd))
	assertRed(t, m, "terminal for demo not opened — you moved on; press t again; "+vRefusal)
	// External already attached.
	d.attachErr = client.ErrExternalAlreadyOpen
	m = runningSbx(d)
	m = vFail(m, vRefusal)
	m, cmd = update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	assertRed(t, m, "an external terminal is already attached to demo; "+vRefusal)
	// An accepted Replace whose attach fails.
	d.attachErr = nil
	rm, rd := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
	rd.replaceErr = errors.New("replace failed")
	rm = vFail(rm, vRefusal)
	rm, _ = update(rm, press("t"))
	rm, cmd = update(rm, press("y"))
	rm, cmd = update(rm, runCmd(cmd))
	rm, _ = update(rm, runCmd(cmd))
	assertRed(t, rm, "terminal: replace failed; "+vRefusal)
	// A refused attach that opens the dialog from the daemon's refusal.
	d.attachErr = status.Error(codes.FailedPrecondition, detachedRefusal)
	m = runningSbx(d)
	m = vFail(m, vRefusal)
	m, cmd = update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenConfirm || !m.vHoldDialog() || m.vHold.text != vRefusal {
		t.Fatalf("the refusal's dialog holds: %v %q", m.screen, m.vHold.text)
	}
	m, _ = update(m, press("n"))
	assertRed(t, m, vRefusal)
}

// An open terminal whose session dies joins the held failure; `detached from`
// is replaced; a v failure arriving once an error shows replaces it.
func TestHoldSessionDiesAndReplace(t *testing.T) {
	clock(t)
	m := openTerm(t, runningSbx(&fakeDaemon{}))
	m = vFail(m, vRefusal)
	m, _ = update(m, termClosedMsg{err: errors.New("session died"), name: "demo", seq: m.term.seq})
	assertRed(t, m, "terminal: session died; "+vRefusal)
	// A failure arriving after the error is showing replaces it.
	m = vFail(m, "another")
	assertRed(t, m, "another")
	// detached from is replaced.
	m = openTerm(t, m)
	m = vFail(m, vRefusal)
	m, _ = update(m, termClosedMsg{name: "demo", seq: m.term.seq})
	assertRed(t, m, vRefusal)
}
