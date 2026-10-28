package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// --- feature 009: EmptyWorkspace launches (research R11) ---

func TestLaunchEmptyWorkspace(t *testing.T) {
	ctx := context.Background()
	m, _, runner, _ := newTestManager(t)
	// Zero sources without the flag are still refused.
	if _, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "x"}}, nil, nil); err == nil {
		t.Fatal("zero sources without EmptyWorkspace must be refused")
	}
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "reasoning-abcd1234"}, EmptyWorkspace: true}, nil, nil)
	if err != nil {
		t.Fatalf("empty launch: %v", err)
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || len(sb.GetSources()) != 0 {
		t.Errorf("record = state %v sources %v", sb.GetState(), sb.GetSources())
	}
	entries, err := os.ReadDir(sb.GetWorkspacePath())
	if err != nil {
		t.Fatalf("workspace dir should exist: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".switchboard" && e.Name() != ".claude" {
			t.Errorf("unexpected workspace entry %q in an empty launch", e.Name())
		}
	}
	if len(runner.lastSources) != 0 {
		t.Errorf("runner got sources %v for an empty launch", runner.lastSources)
	}
	// Refresh of the empty record works (recreates the empty dir).
	if _, err := m.Refresh(ctx, sb.GetId(), nil, nil); err != nil {
		t.Errorf("refresh of an empty workspace: %v", err)
	}
}

// --- feature 009: controller helpers (research R3/R7) ---

func controlled(t *testing.T, m *Manager, reg *registry.Registry, dir, name string, run *pb.WorkerRun) *pb.Sandbox {
	t.Helper()
	sb, _ := launchOne(t, m, dir, name)
	out, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.Controller = &pb.SandboxController{OrchestratorId: "orch-1", Name: "review-bot", HostId: "host-1", Run: run}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestControllerAccessor(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	sb, _ := launchOne(t, m, dir, "plain")
	if c, err := m.Controller(sb.GetId()); err != nil || c != nil {
		t.Errorf("plain sandbox controller = %v / %v", c, err)
	}
	w := controlled(t, m, reg, dir, "worker", nil)
	if c, err := m.Controller(w.GetId()); err != nil || c.GetName() != "review-bot" {
		t.Errorf("worker controller = %v / %v", c, err)
	}
	if _, err := m.Controller("nope"); !errors.Is(err, registry.ErrNotFound) {
		t.Errorf("unknown id = %v", err)
	}
}

func TestLeavingRunningClearsRunUnlessReview(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	run := &pb.WorkerRun{Id: "run-1", Action: "review-pr"}
	busy := controlled(t, m, reg, dir, "busy", run)
	out, err := m.Stop(ctx, busy.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if out.GetController() == nil || out.GetController().GetRun() != nil {
		t.Errorf("stop must clear controller.run but keep the controller: %+v", out.GetController())
	}

	review := controlled(t, m, reg, dir, "review", &pb.WorkerRun{Id: "run-2", Action: "review-pr", NeedsReview: true})
	out, err = m.Stop(ctx, review.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if out.GetController().GetRun().GetId() != "run-2" {
		t.Errorf("a run awaiting review survives a stop: %+v", out.GetController())
	}
	// Restart keeps it too (no transition out of RUNNING).
	if out, err = m.Restart(ctx, review.GetId(), nil); err != nil || out.GetController().GetRun().GetId() != "run-2" {
		t.Errorf("restart: %v / %+v", err, out.GetController())
	}

	// fail(): ERROR clears a plain run.
	failing := controlled(t, m, reg, dir, "failing", &pb.WorkerRun{Id: "run-3"})
	if _, err := m.fail(failing, errors.New("boom")); err == nil {
		t.Fatal("fail returns the cause")
	}
	got, _ := reg.Get(failing.GetId())
	if got.GetState() != pb.SandboxState_SANDBOX_STATE_ERROR || got.GetController().GetRun() != nil {
		t.Errorf("fail: %v %+v", got.GetState(), got.GetController())
	}

	// Readopt (container gone => STOPPED) clears a plain run as well.
	ro := controlled(t, m, reg, dir, "readopt", &pb.WorkerRun{Id: "run-4"})
	m.runner.(*fakeRunner).running[ro.GetDisplayName()] = false
	if err := m.Readopt(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = reg.Get(ro.GetId())
	if got.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED || got.GetController().GetRun() != nil {
		t.Errorf("readopt: %v %+v", got.GetState(), got.GetController())
	}

	// leaveRunning into RUNNING is a no-op for the run.
	s := &pb.Sandbox{Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "r"}}}
	leaveRunning(s, pb.SandboxState_SANDBOX_STATE_RUNNING)
	if s.GetController().GetRun() == nil {
		t.Error("RUNNING must not clear the run")
	}
	leaveRunning(&pb.Sandbox{}, pb.SandboxState_SANDBOX_STATE_STOPPED) // no controller: fine
}

func TestOnDestroyAndBringUpHooks(t *testing.T) {
	ctx := context.Background()
	m, reg, runner, dir := newTestManager(t)
	var destroyed, broughtUp []string
	m.SetOnDestroy(func(sb *pb.Sandbox) { destroyed = append(destroyed, sb.GetId()) })
	m.SetOnBringUp(func(sb *pb.Sandbox) { broughtUp = append(broughtUp, sb.GetId()) })

	w := controlled(t, m, reg, dir, "worker", nil)
	plain, _ := launchOne(t, m, dir, "plain")

	// Bring-up fires for controlled sandboxes only.
	if _, err := m.Stop(ctx, w.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Restart(ctx, w.GetId(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, plain.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Restart(ctx, plain.GetId(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Refresh(ctx, w.GetId(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(broughtUp) != 2 || broughtUp[0] != w.GetId() || broughtUp[1] != w.GetId() {
		t.Errorf("broughtUp = %v, want the worker twice (restart + refresh)", broughtUp)
	}

	// Destroy fires with the record before deletion.
	if _, err := m.Destroy(ctx, w.GetId()); err != nil {
		t.Fatal(err)
	}
	if len(destroyed) != 1 || destroyed[0] != w.GetId() {
		t.Errorf("destroyed = %v", destroyed)
	}
	// The vanished-workspace prune in List is a destroy too.
	if err := os.RemoveAll(plain.GetWorkspacePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.List(); err != nil {
		t.Fatal(err)
	}
	if len(destroyed) != 2 || destroyed[1] != plain.GetId() {
		t.Errorf("prune should run the destroy hook: %v", destroyed)
	}
	_ = runner
}

func TestLaunchAttachAppliesControllerBeforeRunning(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	ctl := &pb.SandboxController{OrchestratorId: "orch-1", Name: "review-bot", HostId: "host-9"}

	var seenState pb.SandboxState
	var seenRef, seenBriefing string
	m.SetControllerApplier(func(_ context.Context, sb *pb.Sandbox, c *pb.SandboxController, briefing string) error {
		seenRef, seenBriefing = sb.GetContainerRef(), briefing
		stored, _ := reg.Get(sb.GetId())
		seenState = stored.GetState()
		// The applier records the controller on the sandbox (the worker service's CAS).
		_, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error { s.Controller = c; return nil })
		return err
	})
	// SWI-15: a launched worker is reported once RUNNING so its agent can warm-start.
	var launched []pb.SandboxState
	m.SetOnLaunched(func(sb *pb.Sandbox) { launched = append(launched, sb.GetState()) })
	var logs []string
	sb, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "worker"}, Sources: []*pb.SourceRef{src},
		Controller: ctl, ControllerBriefing: "## Orchestrator: review-bot",
	}, nil, func(l string) { logs = append(logs, l) })
	if err != nil {
		t.Fatal(err)
	}
	if seenState != pb.SandboxState_SANDBOX_STATE_CREATING {
		t.Errorf("applier ran with the record in %v, want CREATING (before RUNNING)", seenState)
	}
	if seenRef != "worker" || seenBriefing != "## Orchestrator: review-bot" {
		t.Errorf("applier args: ref %q briefing %q", seenRef, seenBriefing)
	}
	if sb.GetController().GetName() != "review-bot" || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("final record: %+v", sb)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "orchestrator: attached to review-bot") {
		t.Errorf("launch log = %v", logs)
	}

	// A failing applier is logged and never fails the launch.
	m.SetControllerApplier(func(context.Context, *pb.Sandbox, *pb.SandboxController, string) error {
		return errors.New("gateway down")
	})
	logs = nil
	sb2, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "worker2"}, Sources: []*pb.SourceRef{src}, Controller: ctl,
	}, nil, func(l string) { logs = append(logs, l) })
	if err != nil || sb2.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("launch must succeed despite an applier failure: %v / %v", err, sb2.GetState())
	}
	if !strings.Contains(strings.Join(logs, "\n"), "orchestrator: skipped — gateway down") {
		t.Errorf("launch log = %v", logs)
	}
	// No applier registered: the controller is ignored.
	m.SetControllerApplier(nil)
	sb3, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "worker3"}, Sources: []*pb.SourceRef{src}, Controller: ctl}, nil, nil)
	if err != nil || sb3.GetController() != nil {
		t.Errorf("without an applier: %v / %+v", err, sb3.GetController())
	}
	// Only the launch whose controller was recorded is reported, as RUNNING.
	if len(launched) != 1 || launched[0] != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("onLaunched calls = %v, want one RUNNING", launched)
	}
	_ = filepath.Join
}

// SWI-12: a WORKING or WAITING agent does not survive leaving RUNNING (its
// session is gone), so a worker stopped mid-wait and restarted is IDLE — free —
// rather than busy forever with no agent left to report otherwise.
func TestStopResetsABusyAgent(t *testing.T) {
	for _, st := range []pb.AgentStatus{pb.AgentStatus_AGENT_STATUS_WAITING, pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT} {
		t.Run(st.String(), func(t *testing.T) {
			m, _, _, dir := newTestManager(t)
			sb, err := m.Launch(context.Background(), LaunchRequest{
				Config: &pb.ConfigSnapshot{Name: "w"}, Sources: []*pb.SourceRef{makeSource(t, dir, "proj")},
			}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.SetAgentStatus(sb.GetId(), st, time.Now()); err != nil {
				t.Fatal(err)
			}
			stopped, err := m.Stop(context.Background(), sb.GetId())
			if err != nil {
				t.Fatal(err)
			}
			if got := stopped.GetAgent().GetStatus(); got != pb.AgentStatus_AGENT_STATUS_IDLE {
				t.Errorf("agent after stop = %v, want IDLE", got)
			}
			restarted, err := m.Restart(context.Background(), sb.GetId(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if restarted.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || restarted.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
				t.Errorf("after restart: %v / %v, want RUNNING / IDLE", restarted.GetState(), restarted.GetAgent().GetStatus())
			}
		})
	}
	// A NEEDS_INPUT asked by the stopped session is gone with it, but a run the
	// orchestrator handed to the developer for review is kept, so what the
	// developer must still act on stays visible.
	s := &pb.Sandbox{
		Agent:      &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT},
		Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "run-1", NeedsReview: true}},
	}
	leaveRunning(s, pb.SandboxState_SANDBOX_STATE_STOPPED)
	if s.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE || !s.GetController().GetRun().GetNeedsReview() {
		t.Errorf("after leaving RUNNING: agent %v, run %v; want IDLE with the review run kept", s.GetAgent().GetStatus(), s.GetController().GetRun())
	}
}

// --- SWI-17: holds survive a stop, and an agent exit during cleanup ---

func TestStopKeepsARunWithCleanupHeld(t *testing.T) {
	ctx := context.Background()
	m, reg, _, dir := newTestManager(t)
	for _, c := range []struct {
		name string
		run  *pb.WorkerRun
		kept bool
	}{
		{"cleaning", &pb.WorkerRun{Id: "r1", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP, StepRole: pb.StepRole_STEP_ROLE_CLEANUP}, true},
		{"freeing", &pb.WorkerRun{Id: "r2", Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING}, true},
		{"cleanup-step", &pb.WorkerRun{Id: "r3", StepRole: pb.StepRole_STEP_ROLE_CLEANUP}, true},
		{"held", &pb.WorkerRun{Id: "r4", Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, HoldGen: 7}, true},
		{"setup", &pb.WorkerRun{Id: "r5", StepRole: pb.StepRole_STEP_ROLE_SETUP}, false},
		{"action", &pb.WorkerRun{Id: "r6", StepRole: pb.StepRole_STEP_ROLE_ACTION}, false},
	} {
		sb := controlled(t, m, reg, dir, c.name, c.run)
		out, err := m.Stop(ctx, sb.GetId())
		if err != nil {
			t.Fatal(err)
		}
		run := out.GetController().GetRun()
		if !c.kept {
			if run != nil {
				t.Errorf("%s: a run in setup or action is cleared by a stop: %+v", c.name, run)
			}
			continue
		}
		if run.GetId() != c.run.GetId() || run.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
			t.Errorf("%s: kept run = %+v, want held NEEDS_DEVELOPER", c.name, run)
		}
	}
	got, _ := reg.Get(controlled(t, m, reg, dir, "gen", &pb.WorkerRun{Id: "r7", Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, HoldGen: 7}).GetId())
	leaveRunning(got, pb.SandboxState_SANDBOX_STATE_STOPPED)
	if got.GetController().GetRun().GetHoldGen() != 7 {
		t.Error("an existing NEEDS_DEVELOPER hold is left as it is")
	}
}

func TestAgentExitDuringCleanupHolds(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	cleaning := controlled(t, m, reg, dir, "cleaning", &pb.WorkerRun{Id: "r1", Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP, StepRole: pb.StepRole_STEP_ROLE_CLEANUP})
	action := controlled(t, m, reg, dir, "action", &pb.WorkerRun{Id: "r2", StepRole: pb.StepRole_STEP_ROLE_ACTION})
	for _, id := range []string{cleaning.GetId(), action.GetId()} {
		if _, err := m.SetAgentStatus(id, pb.AgentStatus_AGENT_STATUS_EXITED, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := reg.Get(cleaning.GetId())
	if got.GetController().GetRun().GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		t.Errorf("exit during cleanup = %+v, want NEEDS_DEVELOPER", got.GetController().GetRun())
	}
	got, _ = reg.Get(action.GetId())
	if got.GetController().GetRun().GetHold() != pb.WorkerHold_WORKER_HOLD_NONE {
		t.Error("a run with nothing left to clean gets no hold")
	}
	// A fresh session's corroborated startup then ends the hold at once.
	var emits int
	m.SetOnChange(func(*pb.Sandbox) { emits++ })
	if cleared, err := m.ClearHeldRun(cleaning.GetId()); err != nil || !cleared {
		t.Fatalf("ClearHeldRun = %v / %v", cleared, err)
	}
	got, _ = reg.Get(cleaning.GetId())
	if got.GetController().GetRun() != nil || got.GetController() == nil || emits != 1 {
		t.Errorf("hold not ended: %+v (emits %d)", got.GetController(), emits)
	}
	// The CAS: a run not held NEEDS_DEVELOPER is left alone, silently.
	if cleared, err := m.ClearHeldRun(action.GetId()); err != nil || cleared || emits != 1 {
		t.Errorf("unheld = %v / %v (emits %d)", cleared, err, emits)
	}
	if _, err := m.ClearHeldRun("nope"); err == nil {
		t.Error("unknown sandbox must error")
	}
}

func TestSetAgentEvidence(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	sb := controlled(t, m, reg, dir, "ev", nil)
	var emits int
	m.SetOnChange(func(*pb.Sandbox) { emits++ })
	if _, err := reg.Update(sb.GetId(), func(s *pb.Sandbox) error { s.Agent = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	out, err := m.SetAgentEvidence(sb.GetId(), func(a *pb.AgentSession) { a.SessionId = "s-1" })
	if err != nil || out.GetAgent().GetSessionId() != "s-1" || emits != 1 {
		t.Errorf("evidence = %+v / %v (emits %d)", out.GetAgent(), err, emits)
	}
	if _, err := m.SetAgentEvidence("nope", func(*pb.AgentSession) {}); err == nil {
		t.Error("unknown sandbox must error")
	}
}
