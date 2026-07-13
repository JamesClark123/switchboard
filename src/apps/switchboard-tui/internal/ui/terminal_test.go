package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// runningSbx builds a model with one running sandbox selected.
func runningSbx(d *fakeDaemon) Model {
	return withSandboxes(sized(New(d, "/work").WithSbx("sbx")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})
}

// TestInPlaceTerminalOpensAndDetaches proves `t` opens the session in-place
// (no TUI restart) showing the snapshot, and ctrl+q detaches back to the list
// (US2, FR-009/011/012, SC-003).
func TestInPlaceTerminalOpensAndDetaches(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)

	// `t` issues the attach command; it must not change screens synchronously
	// (the attach happens on a command) and must not restart the program.
	m, cmd := update(m, press("t"))
	if cmd == nil {
		t.Fatal("t should start attaching to the session")
	}
	// Run the attach command -> termOpenedMsg, feed it back.
	msg := runCmd(cmd)
	opened, ok := msg.(termOpenedMsg)
	if !ok {
		t.Fatalf("expected termOpenedMsg, got %T (%v)", msg, msg)
	}
	m, _ = update(m, opened)
	if m.screen != screenTerminal {
		t.Fatalf("screen = %v, want screenTerminal", m.screen)
	}
	if d.attachedID != "sb1" {
		t.Fatalf("attached to %q, want sb1", d.attachedID)
	}
	// The daemon snapshot is rendered in the view (FR-003).
	if !strings.Contains(m.viewTerminal(), "SNAPSHOT:sb1") {
		t.Fatalf("terminal view missing snapshot: %q", m.viewTerminal())
	}

	// A keystroke is forwarded to the PTY (echoed by the fake into the screen).
	m, _ = update(m, press("x"))
	if !strings.Contains(m.viewTerminal(), "x") {
		t.Error("keystroke should reach the session and echo back")
	}

	// ctrl+q detaches back to the list; the session was closed client-side but
	// the daemon keeps it running (that's the fake's Close, not a stop).
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	if m.screen != screenList {
		t.Fatalf("after detach screen = %v, want screenList", m.screen)
	}
	if !d.termClosed {
		t.Error("detach should Close the client session")
	}
	if !strings.Contains(m.status, "detached") {
		t.Errorf("status = %q, want a detach note", m.status)
	}
}

func TestInPlaceTerminalHintForStoppedSandbox(t *testing.T) {
	m := withSandboxes(sized(New(&fakeDaemon{}, "/work")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_STOPPED}})
	mm, cmd := update(m, press("t"))
	if cmd != nil {
		t.Error("t on a stopped sandbox should not attach")
	}
	if !strings.Contains(mm.status, "start the sandbox") {
		t.Errorf("status = %q, want a start-first hint", mm.status)
	}
}

func TestInPlaceTerminalAttachError(t *testing.T) {
	d := &fakeDaemon{attachErr: errFocusUnsupported} // any non-nil error
	m := runningSbx(d)
	m, cmd := update(m, press("t"))
	msg := runCmd(cmd)
	closed, ok := msg.(termClosedMsg)
	if !ok {
		t.Fatalf("expected termClosedMsg on attach error, got %T", msg)
	}
	m, _ = update(m, closed)
	if m.screen != screenList {
		t.Errorf("a failed attach should stay on the list, got %v", m.screen)
	}
	if !strings.Contains(m.status, "terminal:") {
		t.Errorf("status = %q, want the attach error surfaced", m.status)
	}
}

// TestExternalTerminalSingleInstance proves `T` opens one external terminal and a
// repeat `T` does not spawn a second (US3, FR-014/015).
func TestExternalTerminalSingleInstance(t *testing.T) {
	d := &fakeDaemon{}
	// A long-lived spawn so processAlive stays true between the two presses.
	m := withSandboxes(sized(New(d, "/work").WithSbx("sbx").WithTerminal("sleep 30")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})

	m, _ = update(m, press("T"))
	if len(m.extTerm) != 1 {
		t.Fatalf("first T should spawn one external terminal, have %d", len(m.extTerm))
	}
	if !strings.Contains(m.status, "opened") {
		t.Errorf("status = %q, want an opened note", m.status)
	}

	m, _ = update(m, press("T"))
	if len(m.extTerm) != 1 {
		t.Fatalf("second T must not spawn another, have %d", len(m.extTerm))
	}
	if !strings.Contains(m.status, "already open") && !strings.Contains(m.status, "front") {
		t.Errorf("status = %q, want an already-open / focus note", m.status)
	}
}

func TestExternalTerminalRespectsDaemonExternalFlag(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(sized(New(d, "/work").WithTerminal("sleep 30")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING, ExternalAttached: true}})
	m, _ = update(m, press("T"))
	if len(m.extTerm) != 0 {
		t.Error("should not spawn when the daemon reports an external terminal already attached")
	}
	if !strings.Contains(m.status, "already has an external terminal") {
		t.Errorf("status = %q", m.status)
	}
}

func TestExternalTerminalUnconfigured(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(sized(New(d, "/work").WithTerminal("")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})
	m, cmd := update(m, press("T"))
	if cmd != nil {
		t.Error("no terminal configured should not launch anything")
	}
	if !strings.Contains(m.status, "no terminal configured") {
		t.Errorf("status = %q", m.status)
	}
}
