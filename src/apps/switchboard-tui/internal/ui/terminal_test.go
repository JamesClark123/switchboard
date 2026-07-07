package ui

import (
	"context"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestAttachCmd(t *testing.T) {
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("args = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("args = %v, want %v", got, want)
			}
		}
	}
	// Local host: run sbx directly.
	eq(attachCmd("sbx", "", "id1").Args, "sbx", "run", "id1")
	// A custom sbx binary is honored locally.
	eq(attachCmd("/opt/sbx", "", "id1").Args, "/opt/sbx", "run", "id1")
	// Remote host: run over an SSH PTY.
	eq(attachCmd("sbx", "user@box", "id1").Args, "ssh", "-t", "user@box", "sbx", "run", "id1")
	// Empty sbx bin falls back to "sbx".
	eq(attachCmd("", "", "id1").Args, "sbx", "run", "id1")
}

func TestTerminalOpensForRunningSandbox(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithSbx("sbx"),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})

	// 't' on a running sandbox suspends the TUI into the agent session.
	mm, cmd := update(m, press("t"))
	if cmd == nil {
		t.Fatal("t on a running sandbox should open the agent terminal")
	}
	if mm.screen != screenList {
		t.Errorf("screen should stay list (Bubble Tea suspends around Exec), got %v", mm.screen)
	}
}

func TestTerminalHintForStoppedSandbox(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work"),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_STOPPED}})

	mm, cmd := update(m, press("t"))
	if cmd != nil {
		t.Error("t on a stopped sandbox should not open a terminal")
	}
	if !strings.Contains(mm.status, "start the sandbox") {
		t.Errorf("status = %q, want a start-first hint", mm.status)
	}
}

func TestPopoutTerminalLaunchesConfiguredTerminal(t *testing.T) {
	d := &fakeDaemon{}
	// "true" is a harmless binary; the popout Starts it detached and reports.
	m := withSandboxes(New(d, "/work").WithSbx("sbx").WithTerminal("true"),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})

	mm, cmd := update(m, press("T"))
	if cmd == nil {
		t.Fatal("T on a running sandbox should open a popout terminal")
	}
	if mm.screen != screenList {
		t.Errorf("popout should not leave the list, got %v", mm.screen)
	}
	if s, ok := runCmd(cmd).(statusMsg); !ok || !strings.Contains(string(s), "terminal window") {
		t.Errorf("popout should report success, got %v", runCmd(cmd))
	}
}

func TestPopoutTerminalHintsWhenUnconfigured(t *testing.T) {
	d := &fakeDaemon{}
	// No terminal configured (WithTerminal("")) -> a hint, no launch.
	m := withSandboxes(New(d, "/work").WithSbx("sbx").WithTerminal(""),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})

	mm, cmd := update(m, press("T"))
	if cmd != nil {
		t.Error("popout with no terminal configured should not launch anything")
	}
	if !strings.Contains(mm.status, "no terminal configured") {
		t.Errorf("status = %q, want a configure hint", mm.status)
	}
}

func TestAgentExitReturnsAndRefreshes(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))

	// Clean exit: refresh the list and note the return.
	m, cmd := update(m, agentExitMsg{name: "demo"})
	if !strings.Contains(m.status, "returned from demo") {
		t.Errorf("status = %q", m.status)
	}
	if !m.listLoading {
		t.Error("returning from the terminal should refresh the list")
	}
	if runCmd(cmd) == nil {
		t.Error("exit should trigger a reload command")
	}

	// Failed session: surface the error, no refresh.
	m2, cmd := update(sized(New(&fakeDaemon{}, "/work")), agentExitMsg{name: "demo", err: context.DeadlineExceeded})
	if m2.err == nil || !strings.Contains(m2.status, "failed") {
		t.Errorf("error exit should surface the failure: status=%q err=%v", m2.status, m2.err)
	}
	if cmd != nil {
		t.Error("a failed session should not trigger a reload")
	}
}
