package orchestrator

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// startOne attaches one worker, calls review-pr once and waits for it to run.
func startOne(t *testing.T) (*testEnv, *fakeWorker, *pb.Orchestrator, *runtime, *pb.Run) {
	t.Helper()
	env, w, o, rt := dispatchEnv(t, 1, false)
	run := call(t, env, o.GetId(), "1")
	settle(t, rt)
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(o.GetId(), run.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
	return env, w, o, rt, run
}

// status pushes a worker status event through the fake host's stream.
func push(w *fakeWorker, id string, agent pb.AgentStatus, state pb.SandboxState) {
	sb := w.update(id, func(sb *pb.Sandbox) {
		sb.Agent.Status = agent
		sb.State = state
	})
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}}
}

func stateOf(env *testEnv, orchID, runID string) pb.RunState {
	got, _ := env.svc.GetRun(orchID, runID)
	return got.GetState()
}

func TestStatusFoldingInputWorkingAndReview(t *testing.T) {
	env, w, o, rt, run := startOne(t)
	// NEEDS_INPUT ⇒ AWAITING_INPUT + one notification; WORKING ⇒ RUNNING; a
	// second NEEDS_INPUT entry notifies again, repeats within one entry do not.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_AWAITING_INPUT })
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.SandboxState_SANDBOX_STATE_RUNNING)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_AWAITING_INPUT })
	waitFor(t, func() bool { return len(env.hub.notifications()) == 2 })
	for _, n := range env.hub.notifications() {
		if n.GetKind() != pb.NotificationKind_NOTIFICATION_KIND_RUN_NEEDS_INPUT || n.GetSandboxId() != "wa" || !strings.Contains(n.GetMessage(), "review-pr") {
			t.Errorf("notification = %+v", n)
		}
	}
	// IDLE without a report arms the grace; a report during it completes with
	// no review; the timer is gone.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	run, _ = env.svc.GetRun(o.GetId(), run.GetId()) // the live record, not the queued snapshot
	if _, err := rt.completeRun(run, "all good", false, false); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	timers := len(rt.reviewTimers)
	rt.mu.Unlock()
	if timers != 0 {
		t.Error("a report cancels the review grace")
	}
	env.clock.Advance(reviewGrace)
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("state after report + grace = %v", st)
	}
	if got, _ := env.svc.GetRun(o.GetId(), run.GetId()); got.GetSummary() != "all good" || got.GetClosedByDeveloper() || got.GetEndedAt() == nil {
		t.Errorf("completed run = %+v", got)
	}
	// Second summary refused.
	if _, err := rt.completeRun(run, "again", false, false); !errors.Is(err, ErrPrecondition) {
		t.Errorf("second report = %v", err)
	}
	// The worker's record was cleared through the link.
	if w.get("wa").GetController().GetRun() != nil {
		t.Error("completion should clear controller.run on the worker")
	}
}

func TestReviewGraceThenMarkFree(t *testing.T) {
	env, w, o, rt, run := startOne(t)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	// Not yet.
	env.clock.Advance(reviewGrace - 1)
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("too early: %v", st)
	}
	env.clock.Advance(1)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_AWAITING_REVIEW })
	// The worker's record carries needs_review; the inbox got RUN_AWAITING_REVIEW.
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun().GetNeedsReview() })
	waitFor(t, func() bool { return len(env.hub.notifications()) >= 1 })
	n := env.hub.notifications()
	if len(n) != 1 || n[0].GetKind() != pb.NotificationKind_NOTIFICATION_KIND_RUN_AWAITING_REVIEW {
		t.Errorf("notifications = %+v", n)
	}
	// Workers view shows AWAITING_REVIEW; summary counts it.
	det, _ := env.svc.Get(o.GetId())
	if det.GetWorkers()[0].GetAvailability() != pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW || det.GetSummary().GetAwaitingReview() != 1 || det.GetSummary().GetInFlight() != 1 {
		t.Errorf("detail = %+v", det)
	}
	// Mark free on the wrong sandbox is refused; on the right one closes with
	// the note, closed_by_developer, FAILED when asked, and frees the worker.
	if _, err := env.svc.MarkWorkerFree(o.GetId(), "nope", false, ""); !errors.Is(err, ErrPrecondition) {
		t.Errorf("mark free on a sandbox without review = %v", err)
	}
	closed, err := env.svc.MarkWorkerFree(o.GetId(), "wa", true, "did not finish")
	if err != nil || closed.GetState() != pb.RunState_RUN_STATE_FAILED || closed.GetSummary() != "did not finish" || !closed.GetClosedByDeveloper() {
		t.Fatalf("MarkWorkerFree = %+v / %v", closed, err)
	}
	if w.get("wa").GetController().GetRun() != nil {
		t.Error("the worker should be free")
	}
	// A report that arrives while awaiting review closes it normally (second
	// run, same path).
	run2 := call(t, env, o.GetId(), "2")
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run2.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	env.clock.Advance(reviewGrace)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run2.GetId()) == pb.RunState_RUN_STATE_AWAITING_REVIEW })
	got, _ := env.svc.GetRun(o.GetId(), run2.GetId())
	if _, err := rt.completeRun(got, "late report", false, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = env.svc.GetRun(o.GetId(), run2.GetId()); got.GetState() != pb.RunState_RUN_STATE_COMPLETED || got.GetSummary() != "late report" {
		t.Errorf("late report = %+v", got)
	}
	if _, err := env.svc.MarkWorkerFree(o.GetId(), "wa", false, ""); !errors.Is(err, ErrPrecondition) {
		t.Error("nothing awaits review any more")
	}
	// A worker parked NEEDS_INPUT with no run on it is the developer's call
	// too: mark free sets it back to IDLE on its daemon (an empty run comes
	// back) and the next queued run goes to it.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool {
		return rt.sandboxRecord("wa").GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	})
	freed, err := env.svc.MarkWorkerFree(o.GetId(), "wa", false, "")
	if err != nil || freed.GetId() != "" {
		t.Fatalf("mark free on a parked worker = %+v / %v", freed, err)
	}
	if w.get("wa").GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Error("the parked worker should be IDLE on its daemon")
	}
	run3 := call(t, env, o.GetId(), "3")
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run3.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	// With a run in flight the parked agent belongs to that run (awaiting
	// input), not to mark free.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run3.GetId()) == pb.RunState_RUN_STATE_AWAITING_INPUT })
	if _, err := env.svc.MarkWorkerFree(o.GetId(), "wa", false, ""); !errors.Is(err, ErrPrecondition) {
		t.Errorf("mark free while a run awaits input = %v", err)
	}
}

func TestFailureEdges(t *testing.T) {
	cases := []struct {
		name  string
		agent pb.AgentStatus
		state pb.SandboxState
		want  string
	}{
		{"exited", pb.AgentStatus_AGENT_STATUS_EXITED, pb.SandboxState_SANDBOX_STATE_RUNNING, "agent exited"},
		{"stopped", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_STOPPED, "sandbox stopped"},
		{"error", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_ERROR, "sandbox error"},
		{"creating", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_CREATING, "sandbox restarting"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, w, o, _, run := startOne(t)
			push(w, "wa", c.agent, c.state)
			waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_FAILED })
			got, _ := env.svc.GetRun(o.GetId(), run.GetId())
			if got.GetReason() != c.want {
				t.Errorf("reason = %q, want %q", got.GetReason(), c.want)
			}
			// RUN_FAILED is raised after the run is stored as FAILED.
			waitFor(t, func() bool { return len(env.hub.notifications()) >= 1 })
			n := env.hub.notifications()
			if len(n) != 1 || n[0].GetKind() != pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED {
				t.Errorf("notifications = %+v", n)
			}
		})
	}
	// Removed ⇒ FAILED (even while awaiting review); stopped while awaiting
	// review keeps the run; the worker leaves the roster on removal.
	env, w, o, rt, run := startOne(t)
	run, _ = env.svc.GetRun(o.GetId(), run.GetId())
	rt.runAwaitingReview(run)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_STOPPED)
	waitFor(t, func() bool {
		sb := rt.sandboxRecord("wa")
		return sb != nil && sb.GetState() == pb.SandboxState_SANDBOX_STATE_STOPPED
	})
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_AWAITING_REVIEW {
		t.Errorf("stopped while awaiting review should keep it, got %v", st)
	}
	w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "wa"}}}
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_FAILED })
	if got, _ := env.svc.GetRun(o.GetId(), run.GetId()); got.GetReason() != "sandbox removed" {
		t.Errorf("reason = %q", got.GetReason())
	}
	waitFor(t, func() bool { return !rosterHas(rt.record(), "wa") })
	// Orchestrator removal fails in-flight and cancels queued runs.
	env2, _, o2, rt2, run2 := startOne(t)
	queued := call(t, env2, o2.GetId(), "2")
	settle(t, rt2)
	resp, err := env2.svc.Remove(o2.GetId())
	if err != nil || resp.GetRunsFailed() != 1 || resp.GetRunsCancelled() != 1 || resp.GetWorkersDetached() != 1 {
		t.Errorf("remove = %+v / %v (run %s queued %s)", resp, err, run2.GetId(), queued.GetId())
	}
}

func TestReconcileInFlightTable(t *testing.T) {
	env, w, o, rt, run := startOne(t)
	// Mark the run as RUNNING on a worker whose latest record says IDLE and no
	// report arrived: reconcile ⇒ AWAITING_REVIEW, with no re-delivery.
	sb := w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE })
	rt.mu.Lock()
	rt.sbs["wa"] = sb
	rt.mu.Unlock()
	before := len(w.recorded())
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_AWAITING_REVIEW {
		t.Errorf("idle + unreported ⇒ review, got %v", st)
	}
	for _, c := range w.recorded()[before:] {
		if strings.HasPrefix(c, "deliver ") {
			t.Fatal("reconcile must never re-deliver (I-R3)")
		}
	}
	// Already AWAITING_REVIEW stays; WORKING stays RUNNING; NEEDS_INPUT folds;
	// gone ⇒ FAILED.
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_AWAITING_REVIEW {
		t.Errorf("review stays, got %v", st)
	}
	run2 := &pb.Run{Id: newRunID(o.GetId(), 99), OrchestratorId: o.GetId(), Action: "review-pr", State: pb.RunState_RUN_STATE_RUNNING, SandboxId: "wb", SandboxHostId: "box"}
	_ = env.reg.PutRun(run2)
	wb := running("wb", "wb")
	wb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING
	rt.mu.Lock()
	rt.sbs["wb"] = wb
	rt.mu.Unlock()
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run2.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Errorf("working stays running, got %v", st)
	}
	wb.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run2.GetId()); st != pb.RunState_RUN_STATE_AWAITING_INPUT {
		t.Errorf("needs input folds, got %v", st)
	}
	rt.mu.Lock()
	delete(rt.sbs, "wb")
	rt.mu.Unlock()
	rt.reconcileInFlight("box")
	if got, _ := env.svc.GetRun(o.GetId(), run2.GetId()); got.GetState() != pb.RunState_RUN_STATE_FAILED || got.GetReason() != "sandbox gone" {
		t.Errorf("gone ⇒ failed, got %+v", got)
	}
	// Retention: the newest historyKeep finished runs survive a close.
	for i := 100; i < 100+historyKeep+3; i++ {
		_ = env.reg.PutRun(&pb.Run{Id: newRunID(o.GetId(), uint64(i)), OrchestratorId: o.GetId(), State: pb.RunState_RUN_STATE_COMPLETED})
	}
	rt.closedRun(run2)
	runs, _ := env.svc.ListRuns(o.GetId())
	if len(runs.GetHistory()) != historyKeep {
		t.Errorf("history = %d, want %d", len(runs.GetHistory()), historyKeep)
	}
	if runs.GetHistory()[0].GetId() != newRunID(o.GetId(), uint64(100+historyKeep+2)) {
		t.Errorf("history is newest first: %s", runs.GetHistory()[0].GetId())
	}
	if stateLabel(pb.RunState_RUN_STATE_AWAITING_REVIEW) != "awaiting_review" {
		t.Error("stateLabel")
	}
}
