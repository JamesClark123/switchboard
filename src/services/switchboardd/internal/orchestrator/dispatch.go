package orchestrator

import (
	"errors"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The dispatch pass (research R9, data-model "Dispatch pass"): walk the queued
// runs in receipt order and hand each to the first free, eligible sandbox not
// yet claimed in this pass; a run with no free eligible sandbox keeps its place
// and never blocks later ones. Passes are serialized and coalesced: a trigger
// while a pass runs schedules exactly one more.

// triggerDispatch asks for a pass.
func (rt *runtime) triggerDispatch() {
	rt.dispatchMu.Lock()
	rt.dispatchPending = true
	if rt.dispatching {
		rt.dispatchMu.Unlock()
		return
	}
	rt.dispatching = true
	rt.dispatchMu.Unlock()
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		for {
			rt.dispatchMu.Lock()
			if !rt.dispatchPending || rt.ctx.Err() != nil {
				rt.dispatching = false
				rt.dispatchMu.Unlock()
				return
			}
			rt.dispatchPending = false
			rt.dispatchMu.Unlock()
			rt.dispatch()
		}
	}()
}

// free reports whether a worker can take a run right now (R7). Only an IDLE
// agent is free: one WAITING on background work is still on its run (SWI-12).
func free(sb *pb.Sandbox, hostUp bool) bool {
	return hostUp && sb != nil &&
		sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING &&
		sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_IDLE &&
		sb.GetController().GetRun() == nil
}

// eligible reports whether a run may go to a roster entry (type + tag rules).
func eligible(run *pb.Run, w *pb.Worker, sb *pb.Sandbox, workerTag string) bool {
	if run.GetType() == pb.ActionType_ACTION_TYPE_REASON {
		return w.GetReasoning()
	}
	if w.GetReasoning() {
		return false
	}
	return workerTag == "" || sb.GetTag() == workerTag
}

// dispatch runs one pass.
func (rt *runtime) dispatch() {
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return
	}
	rec := rt.record()
	claimed := map[string]bool{}
	// Candidates come from one view of the roster per pass: were a record
	// re-read per run, a worker freed mid-pass could take a later run while an
	// earlier one it could have taken stays queued (SC-003). The change that
	// freed it triggers another pass.
	sbs := map[string]*pb.Sandbox{}
	ups := map[string]bool{}
	for _, w := range rec.GetWorkers() {
		sbs[w.GetSandboxId()] = rt.sandboxRecord(w.GetSandboxId())
		if _, ok := ups[w.GetHostId()]; !ok {
			up, _ := rt.reachability(w.GetHostId())
			ups[w.GetHostId()] = up
		}
	}
	for _, run := range runs {
		if run.GetState() != pb.RunState_RUN_STATE_QUEUED {
			continue
		}
		action := findAction(rec, run.GetAction())
		tag := ""
		if action != nil {
			tag = action.GetWorkerTag()
		}
		// A reason run has exactly one target; while it is gone (not merely
		// coming up) the run fails with the reason (FR-145 edge case).
		if run.GetType() == pb.ActionType_ACTION_TYPE_REASON {
			if ready, reason := rt.reasoningStatus(); !ready && reason != "" {
				rt.failRun(run, reason)
				continue
			}
		}
		for _, w := range rec.GetWorkers() {
			if claimed[w.GetSandboxId()] {
				continue
			}
			sb, up := sbs[w.GetSandboxId()], ups[w.GetHostId()]
			if !free(sb, up) || !eligible(run, w, sb, tag) {
				continue
			}
			// …but the latest record must still agree before delivering: a
			// worker removed, detached or busied since the view was taken is
			// out of this pass (the change triggers the next one), rather than
			// failing the run with a delivery to a sandbox that is gone.
			claimed[w.GetSandboxId()] = true
			liveUp, _ := rt.reachability(w.GetHostId())
			if live := rt.sandboxRecord(w.GetSandboxId()); !free(live, liveUp) || !eligible(run, w, live, tag) {
				continue
			}
			// A refused delivery (the worker was not free after all, e.g. its
			// agent is still starting) leaves the run for the next worker.
			if rt.deliver(run, w) {
				break
			}
		}
	}
}

// deliver hands one run to one worker through its host link (R7), reporting
// whether that settled the run (delivered, or failed); false leaves it queued
// for another worker.
func (rt *runtime) deliver(run *pb.Run, w *pb.Worker) bool {
	api, err := rt.apiFor(w.GetHostId())
	if err != nil {
		return false // the link went down between the check and the call
	}
	ctx, cancel := rt.callCtx(deliverTimeout)
	defer cancel()
	resp, err := api.DeliverWorkerPrompt(ctx, &pb.DeliverWorkerPromptRequest{
		SandboxId: w.GetSandboxId(), OrchestratorId: rt.id, RunId: run.GetId(), Action: run.GetAction(), Prompt: run.GetPrompt(),
	})
	switch {
	case err == nil && resp.GetAccepted():
		rt.mu.Lock()
		if sb, ok := rt.sbs[w.GetSandboxId()]; ok && sb.GetController() != nil {
			sb.Controller.Run = &pb.WorkerRun{Id: run.GetId(), Action: run.GetAction()}
		}
		rt.mu.Unlock()
		if err := rt.startRun(run, w.GetSandboxId(), w.GetHostId()); err != nil {
			rt.svc.logf("[orchestrator %s] record start of %s: %v", rt.name(), run.GetId(), err)
		}
	case status.Code(err) == codes.FailedPrecondition, errors.Is(err, worker.ErrPrecondition):
		// A developer got there first (or the record moved): the run stays
		// queued and the sandbox is not free for this pass. The own host's
		// in-process API returns the worker sentinel, not a gRPC status.
		return false
	case err != nil:
		rt.failRun(run, "delivery failed: "+describe(err, w.GetHostId()))
	default:
		rt.failRun(run, "delivery refused")
	}
	return true
}
