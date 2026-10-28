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

// claimResult is the outcome of re-checking a planned candidate.
type claimResult int

const (
	claimOK       claimResult = iota // the worker joined rt.delivering
	claimOut                         // the worker is not free (or not a worker) any more
	claimLaneFail                    // the worker is fine, but not allowed this run now
)

// deliverResult is the outcome of one delivery.
type deliverResult int

const (
	delivered deliverResult = iota // the worker took the run
	refused                        // precondition: the run stays queued, the worker leaves the pass
	failed                         // the run was failed
)

// dispatch runs one pass (research R8, contracts/dispatch.md).
func (rt *runtime) dispatch() {
	rt.mu.Lock()
	gen0 := rt.recGen
	detaching := make(map[string]struct{}, len(rt.detaching))
	for id := range rt.detaching {
		detaching[id] = struct{}{}
	}
	rt.mu.Unlock()
	var rec *pb.Orchestrator
	var queued []*pb.Run
	if err := rt.svc.reg.ViewOrchestratorRuns(rt.id, func(o *pb.Orchestrator, q []*pb.Run) error {
		rec, queued = o, q
		return nil
	}); err != nil {
		return
	}
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
	plan := buildPlan(rec, func(w *pb.Worker) bool {
		_, det := detaching[w.GetSandboxId()]
		return !det && free(sbs[w.GetSandboxId()], ups[w.GetHostId()])
	})
	runs := make([]planRun, len(queued))
	for i, run := range queued {
		ids, mode, ok := effectiveLanes(rec, run)
		runs[i] = planRun{run: run, ids: ids, mode: mode, ok: ok, mask: plan.maskOf(ids)}
	}
	plan.reserve(runs)
	reasoningClaimed := false
	for i, r := range runs {
		run := r.run
		if run.GetType() == pb.ActionType_ACTION_TYPE_REASON {
			if !reasoningClaimed {
				reasoningClaimed = rt.dispatchReason(run, rec, sbs, ups)
			}
			continue
		}
		if !r.ok {
			rt.failIfMissing(run)
			continue
		}
		skip := map[*planWorker]bool{}
		for {
			pw := plan.next(i, r, skip)
			if pw == nil {
				plan.release(i)
				break
			}
			switch rt.recheckAndClaim(pw.w, r, gen0) {
			case claimOut:
				plan.drop(pw)
				continue
			case claimLaneFail:
				skip[pw] = true
				continue
			}
			res := rt.deliver(run, pw.w)
			if res == refused {
				// The worker was not free after all (e.g. its agent is still
				// starting): the run tries its next candidate.
				plan.drop(pw)
				continue
			}
			if res == delivered {
				plan.claim(i, pw)
			} else {
				plan.drop(pw)
				plan.release(i)
			}
			break
		}
	}
}

// dispatchReason hands a reason run to the reasoning sandbox when it is free,
// reporting whether it took one. A reason run has exactly one target; while it
// is gone (not merely coming up) the run fails with the reason (FR-145 edge
// case).
func (rt *runtime) dispatchReason(run *pb.Run, rec *pb.Orchestrator, sbs map[string]*pb.Sandbox, ups map[string]bool) bool {
	if ready, reason := rt.reasoningStatus(); !ready && reason != "" {
		rt.failRun(run, reason)
		return false
	}
	for _, w := range rec.GetWorkers() {
		if !w.GetReasoning() || !free(sbs[w.GetSandboxId()], ups[w.GetHostId()]) {
			continue
		}
		// …but the latest record must still agree before delivering: a
		// sandbox removed or busied since the view was taken is out of this
		// pass (the change triggers the next one).
		liveUp, _ := rt.reachability(w.GetHostId())
		if !free(rt.sandboxRecord(w.GetSandboxId()), liveUp) {
			return true
		}
		rt.deliver(run, w)
		return true
	}
	return false
}

// failIfMissing fails a run whose effective lane setting is missing (its
// action was deleted with no kept copy) once a re-read inside the
// record-and-runs transaction agrees: a delete committed after the pass's
// snapshot has given it a kept copy by then. The run is failed after the
// commit, never inside the callback.
func (rt *runtime) failIfMissing(run *pb.Run) {
	missing := false
	if _, err := rt.updateRuns(func(o *pb.Orchestrator, runs RunTx) error {
		cur, err := runs.Get(run.GetId())
		if err != nil || cur.GetState() != pb.RunState_RUN_STATE_QUEUED {
			return errNoChange
		}
		_, _, ok := effectiveLanes(o, cur)
		missing = !ok
		return errNoChange
	}); err != nil || !missing {
		return
	}
	rt.failRun(run, msgActionDeleted)
}

// recheckAndClaim validates a planned candidate against the live state just
// before delivering (research R8): always the sandbox is still free and the
// worker is not detaching; when a record write committed since the snapshot
// (recGen moved), also that the worker is still a lane-capable worker and its
// current lanes allow the run's setting. On success the worker joins
// rt.delivering in the same critical section, so a detach through the
// orchestrator waits for the delivery.
func (rt *runtime) recheckAndClaim(w *pb.Worker, r planRun, gen0 uint64) claimResult {
	id := w.GetSandboxId()
	up, _ := rt.reachability(w.GetHostId())
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !free(rt.sbs[id], up) {
		return claimOut
	}
	if _, det := rt.detaching[id]; det {
		return claimOut
	}
	if _, busy := rt.delivering[id]; busy {
		return claimOut
	}
	if rt.recGen != gen0 {
		cur := onRosterEntry(rt.rec, id)
		if cur == nil || cur.GetReasoning() || cur.GetLanesPending() {
			return claimOut
		}
		// The run's kept copy is read from the snapshot run: claims never read
		// bolt. A missing setting, or a kept lane deleted since, is a skip; the
		// committing write's own pass re-plans the run.
		ids, mode, ok := effectiveLanes(rt.rec, r.run)
		if !ok || !laneIDsExist(rt.rec, ids) || !allowed(cur.GetLaneIds(), ids, mode) {
			return claimLaneFail
		}
	}
	rt.delivering[id] = struct{}{}
	return claimOK
}

// deliver hands one run to one worker through its host link (R7). refused
// leaves the run queued for another worker. The worker leaves rt.delivering
// once the outcome is recorded.
func (rt *runtime) deliver(run *pb.Run, w *pb.Worker) deliverResult {
	defer rt.endDelivery(w.GetSandboxId())
	api, err := rt.apiFor(w.GetHostId())
	if err != nil {
		return refused // the link went down between the check and the call
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
			// The worker took the run: it is busy until its record says
			// otherwise. The cache takes the delivery write's revision (when
			// the host reports it), so a reply to a call made before it (a
			// background push) can no longer read the worker free, while the
			// host's next write still reads as newer.
			if rev := resp.GetRevision(); rev == 0 || rev > sb.GetRevision() {
				sb.Controller.Run = &pb.WorkerRun{Id: run.GetId(), Action: run.GetAction()}
				sb.Revision = max(sb.GetRevision(), rev)
			}
		}
		rt.mu.Unlock()
		if err := rt.startRun(run, w.GetSandboxId(), w.GetHostId()); err != nil {
			rt.svc.logf("[orchestrator %s] record start of %s: %v", rt.name(), run.GetId(), err)
		}
	case status.Code(err) == codes.FailedPrecondition, errors.Is(err, worker.ErrPrecondition):
		// A developer got there first (or the record moved): the run stays
		// queued and the sandbox is not free for this pass. The own host's
		// in-process API returns the worker sentinel, not a gRPC status.
		return refused
	case err != nil:
		rt.failRun(run, "delivery failed: "+describe(err, w.GetHostId()))
		return failed
	default:
		rt.failRun(run, "delivery refused")
		return failed
	}
	return delivered
}
