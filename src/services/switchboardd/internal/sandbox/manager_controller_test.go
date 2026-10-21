package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	_ = filepath.Join
}
