package orchestrator

import (
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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

// free reports whether a worker can take a run right now (R7).
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
			sb := rt.sandboxRecord(w.GetSandboxId())
			up, _ := rt.reachability(w.GetHostId())
			if !free(sb, up) || !eligible(run, w, sb, tag) {
				continue
			}
			claimed[w.GetSandboxId()] = true
			rt.deliver(run, w)
			break
		}
	}
}

// deliver hands one run to one worker through its host link (R7).
func (rt *runtime) deliver(run *pb.Run, w *pb.Worker) {
	api, err := rt.apiFor(w.GetHostId())
	if err != nil {
		return // the link went down between the check and the call: the run waits
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
	case status.Code(err) == codes.FailedPrecondition:
		// A developer got there first (or the record moved): the run stays
		// queued and the sandbox is not free for this pass.
	case err != nil:
		rt.failRun(run, "delivery failed: "+describe(err, w.GetHostId()))
	default:
		rt.failRun(run, "delivery refused")
	}
}
