package orchestrator

import (
	"errors"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// SWI-12: an agent whose turn ended to wait on background work it started is
// still on its run. WAITING never arms the review grace, however long it lasts,
// and the worker shows busy; the real end of the task (IDLE, no report) still
// goes to review after the grace, unchanged (FR-156).
func TestWaitingAgentIsNeverHandedToReview(t *testing.T) {
	env, w, o, rt, run := startOne(t)
	// A grace armed by a brief IDLE is disarmed by WAITING, and WAITING does not
	// arm one of its own: the timer count, not the run state, is what is waited
	// on, so a wrongly armed grace cannot slip past the check.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WAITING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 0 })
	// Another WAITING event (the idle nudge re-publishes the record) is folded
	// like the first; then a WORKING one, whose fold proves the WAITING ones
	// before it were processed.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WAITING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	env.clock.Advance(10 * reviewGrace)
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("waiting run state = %v, want RUNNING", st)
	}
	if n := env.hub.notifications(); len(n) != 0 {
		t.Errorf("no notification while waiting, got %+v", n)
	}
	det, _ := env.svc.Get(o.GetId())
	if got := det.GetWorkers()[0].GetAvailability(); got != pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY {
		t.Errorf("waiting worker availability = %v, want BUSY", got)
	}

	// The background work finishes, the agent resumes and stops for good
	// without reporting: review after the grace, as before.
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING, pb.SandboxState_SANDBOX_STATE_RUNNING)
	push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	env.clock.Advance(reviewGrace)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_AWAITING_REVIEW })
}

// A run handed to review whose agent then resumes (old hooks, the backstop, a
// race) was not finished: it goes back to RUNNING, the worker's needs_review
// flag clears, and its later report closes it normally. Mark free then finds
// nothing to free and says so.
func TestResumedAgentLeavesReview(t *testing.T) {
	for _, resumed := range []pb.AgentStatus{pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_WAITING} {
		t.Run(resumed.String(), func(t *testing.T) {
			env, w, o, rt, run := startOne(t)
			push(w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING, pb.SandboxState_SANDBOX_STATE_RUNNING)
			push(w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE, pb.SandboxState_SANDBOX_STATE_RUNNING)
			waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
			env.clock.Advance(reviewGrace)
			waitFor(t, func() bool { return w.get("wa").GetController().GetRun().GetNeedsReview() })

			push(w, "wa", resumed, pb.SandboxState_SANDBOX_STATE_RUNNING)
			waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_RUNNING })
			waitFor(t, func() bool {
				r := w.get("wa").GetController().GetRun()
				return r.GetId() == run.GetId() && !r.GetNeedsReview()
			})
			waitFor(t, func() bool {
				det, _ := env.svc.Get(o.GetId())
				return det.GetWorkers()[0].GetAvailability() == pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY && det.GetSummary().GetAwaitingReview() == 0
			})
			if _, err := env.svc.MarkWorkerFree(o.GetId(), "wa", false, ""); !errors.Is(err, ErrPrecondition) {
				t.Errorf("mark free on a resumed run = %v, want ErrPrecondition", err)
			}

			live, _ := env.svc.GetRun(o.GetId(), run.GetId())
			if _, err := rt.completeRun(live, "done after all", false, false); err != nil {
				t.Fatal(err)
			}
			if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_COMPLETED {
				t.Errorf("state after report = %v", st)
			}
			if w.get("wa").GetController().GetRun() != nil {
				t.Error("the report frees the worker")
			}
		})
	}
}

// Reconcile after a link comes up: a waiting agent keeps its run; a run in
// review whose agent resumed while the link was down comes back to RUNNING.
func TestReconcileWithWaitingAndResumedAgents(t *testing.T) {
	env, w, o, rt, run := startOne(t)
	sb := w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WAITING })
	rt.mu.Lock()
	rt.sbs["wa"] = sb
	rt.mu.Unlock()
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("waiting + unreported stays RUNNING, got %v", st)
	}

	sb = w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE })
	rt.mu.Lock()
	rt.sbs["wa"] = sb
	rt.mu.Unlock()
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_AWAITING_REVIEW {
		t.Fatalf("idle + unreported ⇒ review, got %v", st)
	}

	for _, st := range []pb.AgentStatus{pb.AgentStatus_AGENT_STATUS_WAITING, pb.AgentStatus_AGENT_STATUS_WORKING} {
		live, _ := env.svc.GetRun(o.GetId(), run.GetId())
		live.State = pb.RunState_RUN_STATE_AWAITING_REVIEW
		_ = env.reg.PutRun(live)
		sb = w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = st })
		rt.mu.Lock()
		rt.sbs["wa"] = sb
		rt.mu.Unlock()
		rt.reconcileInFlight("box")
		if got := stateOf(env, o.GetId(), run.GetId()); got != pb.RunState_RUN_STATE_RUNNING {
			t.Errorf("review + %v ⇒ RUNNING, got %v", st, got)
		}
	}

	// A run in review on a sandbox that is not running is left for the
	// developer (unchanged FR-156 edge case).
	live, _ := env.svc.GetRun(o.GetId(), run.GetId())
	live.State = pb.RunState_RUN_STATE_AWAITING_REVIEW
	_ = env.reg.PutRun(live)
	sb = w.update("wa", func(sb *pb.Sandbox) {
		sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING
		sb.State = pb.SandboxState_SANDBOX_STATE_STOPPED
	})
	rt.mu.Lock()
	rt.sbs["wa"] = sb
	rt.mu.Unlock()
	rt.reconcileInFlight("box")
	if got := stateOf(env, o.GetId(), run.GetId()); got != pb.RunState_RUN_STATE_AWAITING_REVIEW {
		t.Errorf("review on a stopped sandbox stays, got %v", got)
	}
}

// A worker waiting on background work is never handed a new run.
func TestWaitingWorkerIsNotFree(t *testing.T) {
	sb := &pb.Sandbox{State: pb.SandboxState_SANDBOX_STATE_RUNNING, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_WAITING}, Controller: &pb.SandboxController{}}
	if free(sb, true) {
		t.Error("a WAITING worker must not be free")
	}
	sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE
	if !free(sb, true) {
		t.Error("an IDLE worker with no run is free")
	}
}

// A worker record whose needs_review flag disagrees with its run (a push lost
// while the link was down, or review and resume pushes landing out of order) is
// repaired by the next status fold and by reconcile.
func TestReviewFlagRepaired(t *testing.T) {
	env, w, o, rt, run := startOne(t)
	_ = o
	// The run is RUNNING but the worker still says needs_review.
	sb := w.update("wa", func(sb *pb.Sandbox) {
		sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING
		sb.Controller.Run = &pb.WorkerRun{Id: run.GetId(), Action: run.GetAction(), NeedsReview: true}
	})
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}}
	waitFor(t, func() bool { return !w.get("wa").GetController().GetRun().GetNeedsReview() })

	// Reconcile does the same when the record arrives through a listing.
	sb = w.update("wa", func(sb *pb.Sandbox) {
		sb.Controller.Run = &pb.WorkerRun{Id: run.GetId(), Action: run.GetAction(), NeedsReview: true}
	})
	rt.mu.Lock()
	rt.sbs["wa"] = sb
	rt.mu.Unlock()
	rt.reconcileInFlight("box")
	if wr := w.get("wa").GetController().GetRun(); wr.GetNeedsReview() || wr.GetId() != run.GetId() {
		t.Errorf("reconcile should clear the stale flag, worker run = %+v", w.get("wa").GetController().GetRun())
	}
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Errorf("run state = %v", st)
	}
}

// A run that is closed while a resume is being folded stays closed: the resume
// reads the live record, not the caller's stale copy.
func TestResumeDoesNotReopenAClosedRun(t *testing.T) {
	env, _, o, rt, run := startOne(t)
	stale, _ := env.svc.GetRun(o.GetId(), run.GetId())
	stale.State = pb.RunState_RUN_STATE_AWAITING_REVIEW // what the folder last saw
	live, _ := env.svc.GetRun(o.GetId(), run.GetId())
	if _, err := rt.completeRun(live, "done", false, false); err != nil {
		t.Fatal(err)
	}
	rt.runResumed(stale)
	got, _ := env.svc.GetRun(o.GetId(), run.GetId())
	if got.GetState() != pb.RunState_RUN_STATE_COMPLETED || got.GetSummary() != "done" {
		t.Errorf("a closed run was reopened: %+v", got)
	}
	// Likewise a review decided on a stale copy is not applied.
	rt.runAwaitingReview(stale)
	if got, _ := env.svc.GetRun(o.GetId(), run.GetId()); got.GetState() != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("a closed run was sent to review: %v", got.GetState())
	}
}
