package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SWI-17 (research R14, contracts/jobs-ui.md): `t`/`T` on a sandbox whose agent
// was detached by a daemon restart, on a worker or a plain sandbox.

// detachedSandbox is a running sandbox whose detached agent was last seen in
// state last; worker adds a controller holding an ACTION run.
func detachedSandbox(id string, last pb.AgentStatus, worker bool) *pb.Sandbox {
	sb := &pb.Sandbox{Id: id, DisplayName: "sb-" + id, State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: last,
			LastSeenAt: timestamppb.New(time.Date(2026, 10, 28, 10, 42, 0, 0, time.Local))}}}
	if worker {
		sb.Controller = &pb.SandboxController{OrchestratorId: "o-a", Name: "alpha-bot", Run: &pb.WorkerRun{Id: "run-9", Action: "review-pr", StepRole: pb.StepRole_STEP_ROLE_ACTION}}
	}
	return sb
}

// orphanList is the sandbox list on one fake daemon showing sb.
func orphanList(t *testing.T, sb *pb.Sandbox) (Model, *fakeDaemon) {
	t.Helper()
	d := &fakeDaemon{sandboxes: []*pb.Sandbox{sb}}
	m := withSandboxes(sized(New(d, "/work")), []*pb.Sandbox{sb})
	if m.actionable() == nil {
		// A worker is listed on the Orchestrator Sandboxes tab.
		m, _ = update(m, press("O"))
	}
	if m.actionable().GetId() != sb.GetId() {
		t.Fatalf("selected %q", m.actionable().GetId())
	}
	return m, d
}

func TestDetachedAgentReplaceOnPlainSandbox(t *testing.T) {
	m, d := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
	m, cmd := update(m, press("t"))
	if cmd != nil || m.screen != screenConfirm {
		t.Fatalf("t should open the dialog: %v", m.screen)
	}
	v := ansi.Strip(m.View())
	for _, want := range []string{"sb-p1's agent was detached by a daemon restart", "Last seen idle 10:42.", "It can't be shown or typed into. Replace it?", "Replacing stops it.", "replace", "wait"} {
		if !strings.Contains(v, want) {
			t.Errorf("dialog missing %q:\n%s", want, v)
		}
	}
	// Idle: Replace is the default, so enter replaces.
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd = update(m, runCmd(cmd))
	if m.status != replacingText {
		t.Errorf("status %q", m.status)
	}
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenTerminal || d.replacedID != "p1" {
		t.Errorf("screen %v replaced %q", m.screen, d.replacedID)
	}
}

func TestDetachedAgentWaitIsDefaultWhenBusy(t *testing.T) {
	for _, last := range []pb.AgentStatus{pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT} {
		m, d := orphanList(t, detachedSandbox("w1", last, true))
		m, _ = update(m, press("t"))
		v := ansi.Strip(m.View())
		if !strings.Contains(v, "on run-9") || !strings.Contains(v, "Replacing stops it and fails run-9.") {
			t.Errorf("%v: dialog:\n%s", last, v)
		}
		// enter is Wait: nothing is attached.
		mm, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || mm.screen != screenList || d.replacedID != "" || d.attachedID != "" {
			t.Errorf("%v: enter should wait (screen %v)", last, mm.screen)
		}
		// n is Wait too; y replaces.
		if mm, _ := update(m, press("n")); mm.screen != screenList {
			t.Error("n waits")
		}
		mm, cmd = update(m, press("y"))
		mm, cmd = update(mm, runCmd(cmd))
		mm, _ = update(mm, runCmd(cmd))
		if mm.screen != screenTerminal || d.replacedID != "w1" {
			t.Errorf("%v: y should replace", last)
		}
	}
	if s := lastSeenText(&pb.AgentCheck{}); s != "in an unknown state" {
		t.Errorf("unknown last state = %q", s)
	}
	// A setup or cleanup run is not failed by a Replace.
	if failsOnReplace(&pb.WorkerRun{StepRole: pb.StepRole_STEP_ROLE_SETUP}) || !failsOnReplace(&pb.WorkerRun{}) {
		t.Error("failsOnReplace")
	}
}

func TestDetachedAgentReplaceRefusedAndExternal(t *testing.T) {
	m, d := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
	d.replaceErr = status.Error(codes.FailedPrecondition, "sandbox is busy; try again")
	m, _ = update(m, press("t"))
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenList || m.status != "terminal: sandbox is busy; try again" {
		t.Errorf("busy replace: screen %v status %q", m.screen, m.status)
	}
	// T: the dialog too; Replace stops the orphan, then opens the window.
	d.replaceErr = nil
	m, _ = update(m, press("T"))
	if m.screen != screenConfirm {
		t.Fatalf("T should open the dialog: %v", m.screen)
	}
	m, cmd = update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	if m.status != replacingText {
		t.Errorf("status %q", m.status)
	}
	m, _ = update(m, runCmd(cmd))
	if d.replacedID != "p1" || !d.termClosed || !strings.Contains(m.status, "no terminal configured") {
		t.Errorf("external replace: replaced %q closed %v status %q", d.replacedID, d.termClosed, m.status)
	}
	m, _ = update(m, orphanReplacedMsg{sb: m.actionable(), err: errors.New("boom")})
	if m.status != "replace: boom" {
		t.Errorf("failed external replace: %q", m.status)
	}
}

// The record was stale: the daemon refuses beside an orphan and the dialog opens.
func TestDetachedAgentRefusalOpensDialog(t *testing.T) {
	sb := &pb.Sandbox{Id: "p2", DisplayName: "sb-p2", State: pb.SandboxState_SANDBOX_STATE_RUNNING}
	m, d := orphanList(t, sb)
	d.attachErr = status.Error(codes.FailedPrecondition, "agent detached by a daemon restart")
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenConfirm || !strings.Contains(ansi.Strip(m.View()), "in an unknown state") || !m.confirm.enterCancels || !m.confirm.yOnly {
		t.Errorf("refusal: screen %v\n%s", m.screen, ansi.Strip(m.View()))
	}
	// Any other refusal shows the daemon's text.
	m, _ = update(m, press("n"))
	d.attachErr = status.Error(codes.FailedPrecondition, "sandbox is busy; try again")
	m, cmd = update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if m.status != "terminal: sandbox is busy; try again" {
		t.Errorf("busy: %q", m.status)
	}
	if !isDetachedRefusal(status.Error(codes.FailedPrecondition, detachedRefusal+"; last seen working 10:42")) || isDetachedRefusal(errors.New(detachedRefusal)) {
		t.Error("isDetachedRefusal")
	}
	if c := refusalCheck(detachedSandbox("x", pb.AgentStatus_AGENT_STATUS_IDLE, false)); c.GetLastStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Error("refusalCheck keeps the record's check")
	}
}

func TestDetachedAgentCheckingAndCheckFailed(t *testing.T) {
	checking := &pb.Sandbox{Id: "c1", DisplayName: "sb-c1", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_UNKNOWN}}}
	m, d := orphanList(t, checking)
	m, cmd := update(m, press("t"))
	if cmd != nil || m.screen != screenList || !strings.Contains(m.status, "checking for a detached agent") || d.attachedID != "" {
		t.Errorf("checking: %v %q", m.screen, m.status)
	}
	failed := detachedSandbox("f1", pb.AgentStatus_AGENT_STATUS_IDLE, true)
	failed.Agent.AgentCheck = &pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED, Error: "exec timed out"}
	m, d = orphanList(t, failed)
	m, _ = update(m, press("t"))
	v := ansi.Strip(m.View())
	for _, want := range []string{"Can't check sb-f1 for a detached agent", "can't check for a detached agent: exec timed out", "fails run-9", "restart sandbox", "wait"} {
		if !strings.Contains(v, want) {
			t.Errorf("dialog missing %q:\n%s", want, v)
		}
	}
	// Wait is the default; there is no Replace.
	if mm, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || mm.screen != screenList || strings.Contains(v, "replace") {
		t.Error("enter waits")
	}
	m, cmd = update(m, press("y"))
	if m.busy["f1"] != "restarting" {
		t.Errorf("restart spinner: %v", m.busy)
	}
	m, _ = update(m, runCmd(cmd))
	if strings.Join(d.lifecycle, ",") != "Stop f1,Restart f1" || !strings.Contains(m.status, "restarted sb-f1") {
		t.Errorf("restart: %v status %q", d.lifecycle, m.status)
	}
	// A plain sandbox names no run.
	plain := &pb.Sandbox{Id: "f2", DisplayName: "sb-f2", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED, Error: "x"}}}
	m, _ = orphanList(t, plain)
	m, _ = update(m, press("T"))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Restarting ends every process in it.") {
		t.Errorf("plain restart dialog:\n%s", v)
	}
	// A run held for cleanup is not failed by a restart.
	cleaning := detachedSandbox("f3", pb.AgentStatus_AGENT_STATUS_IDLE, true)
	cleaning.Controller.Run.StepRole = pb.StepRole_STEP_ROLE_CLEANUP
	cleaning.Agent.AgentCheck = &pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED, Error: "x"}
	m, _ = orphanList(t, cleaning)
	m, _ = update(m, press("t"))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Restarting ends every process in it.") || strings.Contains(v, "fails run-9") {
		t.Errorf("cleanup restart dialog:\n%s", v)
	}
}

// An idle agent whose run a Replace fails still defaults to Wait: enter never
// fails a finished run.
func TestDetachedAgentWaitIsDefaultWhenReplaceFailsRun(t *testing.T) {
	m, d := orphanList(t, detachedSandbox("w2", pb.AgentStatus_AGENT_STATUS_IDLE, true))
	m, _ = update(m, press("t"))
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Replacing stops it and fails run-9.") {
		t.Fatalf("dialog:\n%s", v)
	}
	if mm, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || mm.screen != screenList || d.replacedID != "" {
		t.Errorf("enter should wait (screen %v)", mm.screen)
	}
}

// A failed stop or start is reported, and nothing more is done.
func TestRestartSandboxErrors(t *testing.T) {
	failed := &pb.Sandbox{Id: "f3", DisplayName: "sb-f3", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED}}}
	m, _ := orphanList(t, failed)
	stopErr := &erroringDaemon{fakeDaemon: &fakeDaemon{}, stopErr: errors.New("stop failed")}
	mm, _ := m.confirmRestartUnchecked(failed, stopErr, screenList, failed.GetAgent().GetAgentCheck())
	if msg, ok := mm.(Model).confirm.onConfirm().(errMsg); !ok || !strings.Contains(msg.err.Error(), "stop failed") {
		t.Errorf("stop error: %v", msg)
	}
	startErr := &erroringDaemon{fakeDaemon: &fakeDaemon{}, restartErr: errors.New("start failed")}
	mm, _ = m.confirmRestartUnchecked(failed, startErr, screenList, failed.GetAgent().GetAgentCheck())
	if msg, ok := mm.(Model).confirm.onConfirm().(errMsg); !ok || !strings.Contains(msg.err.Error(), "start failed") {
		t.Errorf("restart error: %v", msg)
	}
}

// erroringDaemon fails Stop or Restart.
type erroringDaemon struct {
	*fakeDaemon
	stopErr, restartErr error
}

func (e *erroringDaemon) Stop(ctx context.Context, id string) (*pb.Sandbox, error) {
	if e.stopErr != nil {
		return nil, e.stopErr
	}
	return e.fakeDaemon.Stop(ctx, id)
}

func (e *erroringDaemon) Restart(ctx context.Context, id string) (*pb.Sandbox, error) {
	if e.restartErr != nil {
		return nil, e.restartErr
	}
	return e.fakeDaemon.Restart(ctx, id)
}

// t on a Jobs run whose worker has a detached agent opens the same dialog and
// returns to Jobs.
func TestDetachedAgentFromJobs(t *testing.T) {
	m, d := stepJobs(t, seqRun("run-x", pb.RunState_RUN_STATE_RUNNING, 3, pb.StepStatus_STEP_STATUS_TURN))
	m.sandboxes[0].Agent = &pb.AgentSession{AgentCheck: &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: pb.AgentStatus_AGENT_STATUS_WORKING}}
	m = overviewAt(t, m, "run-x")
	m, _ = update(m, press("t"))
	if m.screen != screenConfirm || m.confirm.returnTo != screenJobs {
		t.Fatalf("screen %v", m.screen)
	}
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runCmd(cmd))
	m, _ = update(m, runCmd(cmd))
	if m.screen != screenTerminal || d.replacedID != "w-run" || m.term.returnTo != screenJobs {
		t.Errorf("screen %v replaced %q", m.screen, d.replacedID)
	}
}
