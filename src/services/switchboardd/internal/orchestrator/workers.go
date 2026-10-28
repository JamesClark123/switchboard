package orchestrator

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// reconcile re-reads a host's sandboxes (research R4/R5): roster_seq is read
// before the listing, the listing is made with no lock held, and the result is
// applied through applyRecords in one update — records naming this
// orchestrator are adopted, roster entries whose record no longer names it (or
// that the listing lacks) are dropped, failing any run on them. A full
// reconcile (the link (re)connected) also pushes the host's workers their
// name, briefing and lanes; one triggered by an events_dropped marker lists
// and applies only. In-flight runs are re-checked either way (FR-134).
func (rt *runtime) reconcile(ctx context.Context, hostID string, api WorkerAPI, full bool) error {
	requested := rt.record().GetRosterSeq()
	rctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	sbs, err := api.ListSandboxes(rctx)
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}
	if _, err := rt.applyRoster(rosterInput{source: srcListing, hostID: hostID, requestedSeq: requested, records: sbs}); err != nil {
		return err
	}
	if full {
		rt.pushWorkerStateTo(ctx, hostID, api)
	}
	rt.reconcileInFlight(hostID)
	rt.svc.emitChanged(rt.id)
	return nil
}

// applyRoster applies observed records through applyRecords in one update:
// the records are cached first (the cache keeps the newest), the rules read a
// snapshot of the cache, and removals evict their entries afterwards. Nothing
// is written when nothing changed. The follow-ups run after the commit.
func (rt *runtime) applyRoster(in rosterInput) (rosterResult, error) {
	var res rosterResult
	now := rt.svc.clock.Now()
	_, err := rt.update(func(o *pb.Orchestrator) error {
		rt.mu.Lock()
		for _, sb := range in.records {
			if sb.GetState() != pb.SandboxState_SANDBOX_STATE_DESTROYING && rt.relevantLocked(o, sb) {
				rt.cacheLocked(sb)
			}
		}
		in.cache = rt.snapshotCacheLocked(in.rosterIDs())
		rt.mu.Unlock()
		res = applyRecords(o, in, now)
		if !res.changed {
			return errNoChange
		}
		return nil
	})
	if len(res.evict) > 0 {
		rt.mu.Lock()
		for _, id := range res.evict {
			delete(rt.sbs, id)
		}
		rt.mu.Unlock()
	}
	if err != nil {
		return rosterResult{}, err
	}
	rt.afterRoster(res)
	return res, nil
}

// relevantLocked reports whether a record concerns this orchestrator, so it is
// worth caching: it names this orchestrator, is on the roster, is cached
// already or has a (non-removed) high-water mark. Caller holds mu.
func (rt *runtime) relevantLocked(o *pb.Orchestrator, sb *pb.Sandbox) bool {
	id := sb.GetId()
	if m := o.GetHighWaterMarks()[id]; m.GetRemoved() {
		return false
	} else if m != nil {
		return true
	}
	if _, ok := rt.sbs[id]; ok {
		return true
	}
	return sb.GetController().GetOrchestratorId() == rt.id || rosterHas(o, id)
}

// afterRoster runs a committed roster change's follow-ups: runs on dropped
// workers fail, a lost reasoning sandbox is recorded, adopted workers get
// their lanes shown and the briefing, and a dispatch pass is asked for.
func (rt *runtime) afterRoster(res rosterResult) {
	if !res.changed {
		return
	}
	for _, d := range res.dropped {
		reason := "worker detached"
		if d.removedEvent {
			reason = "sandbox removed"
		}
		rt.failRunsOn(d.id, reason)
	}
	if res.reasoningLost {
		rt.setReasoningErr(reasoningLostReason)
	}
	if len(res.adopted) > 0 {
		rt.markLanes(res.adopted...)
		rt.requestBriefing()
	}
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
}

func rosterHas(o *pb.Orchestrator, sandboxID string) bool {
	for _, w := range o.GetWorkers() {
		if w.GetSandboxId() == sandboxID {
			return true
		}
	}
	return false
}

// hostOf returns the roster host of a sandbox ("" when not on the roster).
func hostOf(o *pb.Orchestrator, sandboxID string) string {
	for _, w := range o.GetWorkers() {
		if w.GetSandboxId() == sandboxID {
			return w.GetHostId()
		}
	}
	return ""
}

// availability derives a worker's availability from its record and its host
// link (data-model "Worker availability", first match wins).
func availability(sb *pb.Sandbox, hostUp bool) pb.WorkerAvailability {
	switch {
	case !hostUp:
		return pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE
	case sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
		return pb.WorkerAvailability_WORKER_AVAILABILITY_NOT_RUNNING
	case sb.GetController().GetRun().GetNeedsReview():
		return pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW
	case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
		return pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT
	case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_WORKING,
		sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_WAITING,  // on background work (SWI-12)
		sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_STARTING, // SWI-15: not yet at its prompt
		sb.GetController().GetRun() != nil,
		sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_EXITED:
		return pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY
	}
	return pb.WorkerAvailability_WORKER_AVAILABILITY_FREE
}

// workerViews renders the roster for the Workers view (FR-132).
func (rt *runtime) workerViews() []*pb.WorkerView {
	rec := rt.record()
	out := make([]*pb.WorkerView, 0, len(rec.GetWorkers()))
	for _, w := range rec.GetWorkers() {
		up, _ := rt.reachability(w.GetHostId())
		v := &pb.WorkerView{SandboxId: w.GetSandboxId(), HostId: w.GetHostId(), Reasoning: w.GetReasoning(), HostReachable: up}
		if sb := rt.sandboxRecord(w.GetSandboxId()); sb != nil {
			v.DisplayName = sb.GetDisplayName()
			v.Tag = sb.GetTag()
			v.State = sb.GetState()
			v.AgentStatus = sb.GetAgent().GetStatus()
			v.Availability = availability(sb, up)
			v.RunId = sb.GetController().GetRun().GetId()
			v.RunAction = sb.GetController().GetRun().GetAction()
		} else {
			v.Availability = pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE
			if up {
				v.Availability = pb.WorkerAvailability_WORKER_AVAILABILITY_NOT_RUNNING
			}
		}
		out = append(out, v)
	}
	// Workers first in roster order, the reasoning sandbox last (it is not a
	// worker the developer attached, and the Workers view lists it marked).
	sort.SliceStable(out, func(i, j int) bool { return !out[i].GetReasoning() && out[j].GetReasoning() })
	return out
}

// bundle is what a launching sandbox carries to become a worker before its
// agent starts (FR-110).
func (rt *runtime) bundle() *pb.WorkerBundle {
	rec := rt.record()
	return &pb.WorkerBundle{
		Controller: &pb.SandboxController{OrchestratorId: rt.id, Name: rec.GetName(), HostId: rt.svc.hostID},
		Briefing:   renderBriefing(rec, false),
	}
}

// attach records this orchestrator as the controller of an existing sandbox
// through its host's link (FR-107/FR-108); the reply is applied through
// applyRecords, so a newer record already seen (a direct detach) wins.
func (rt *runtime) attach(ctx context.Context, hostID, sandboxID string) (*pb.WorkerView, error) {
	api, err := rt.awaitAPI(ctx, hostID)
	if err != nil {
		return nil, err
	}
	rec := rt.record()
	cctx, cancel := context.WithTimeout(ctx, unaryTimeout)
	defer cancel()
	sb, err := api.AttachController(cctx, &pb.AttachControllerRequest{
		SandboxId:  sandboxID,
		Controller: &pb.SandboxController{OrchestratorId: rt.id, Name: rec.GetName(), HostId: rt.svc.hostID},
		Briefing:   renderBriefing(rec, false),
	})
	if err != nil {
		return nil, remoteErr(err, hostID)
	}
	if _, err := rt.applyRoster(rosterInput{source: srcAttachReply, hostID: hostID, records: []*pb.Sandbox{sb}}); err != nil {
		return nil, err
	}
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
	return rt.workerView(sandboxID), nil
}

// workerView renders one roster entry (nil-safe: an entry that left the roster
// renders from its id alone).
func (rt *runtime) workerView(sandboxID string) *pb.WorkerView {
	for _, v := range rt.workerViews() {
		if v.GetSandboxId() == sandboxID {
			return v
		}
	}
	return &pb.WorkerView{SandboxId: sandboxID}
}

// detach clears the controller on a worker through its link (FR-111): refused
// while a run is recorded on it and for the reasoning sandbox. The reply is
// applied through applyRecords.
func (rt *runtime) detach(ctx context.Context, sandboxID string) error {
	rec := rt.record()
	hostID := hostOf(rec, sandboxID)
	if hostID == "" {
		return fmt.Errorf("%w: sandbox %s is not a worker of %s", ErrNotFound, sandboxID, rec.GetName())
	}
	if sb := rt.sandboxRecord(sandboxID); sb != nil {
		if sb.GetController().GetReasoning() || sandboxID == rec.GetReasoningSandboxId() {
			return fmt.Errorf("%w: reasoning sandbox is not detachable", ErrPrecondition)
		}
		if run := sb.GetController().GetRun(); run != nil {
			return fmt.Errorf("%w: run %s (%s) is in flight on this worker — wait for it or mark the worker free", ErrPrecondition, run.GetId(), run.GetAction())
		}
	}
	api, err := rt.apiFor(hostID)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, unaryTimeout)
	defer cancel()
	sb, err := api.DetachController(cctx, &pb.DetachControllerRequest{SandboxId: sandboxID, OrchestratorId: rt.id})
	if err != nil {
		return remoteErr(err, hostID)
	}
	if _, err := rt.applyRoster(rosterInput{source: srcDetachReply, hostID: hostID, records: []*pb.Sandbox{sb}}); err != nil {
		return err
	}
	rt.svc.emitChanged(rt.id)
	return nil
}

// detachAll detaches every reachable worker with force (orchestrator removal,
// FR-153) and returns how many were detached.
func (rt *runtime) detachAll() uint32 {
	rec := rt.record()
	var n uint32
	for _, w := range rec.GetWorkers() {
		if w.GetReasoning() {
			continue // destroyed with the orchestrator instead
		}
		api, err := rt.apiFor(w.GetHostId())
		if err != nil {
			continue
		}
		cctx, cancel := rt.callCtx(unaryTimeout)
		_, err = api.DetachController(cctx, &pb.DetachControllerRequest{SandboxId: w.GetSandboxId(), OrchestratorId: rt.id, Force: true})
		cancel()
		if err == nil {
			n++
		}
	}
	return n
}

// remoteErr passes a worker daemon's status through (ALREADY_EXISTS naming the
// controller, FAILED_PRECONDITION, …) and names old daemons and dead links.
func remoteErr(err error, hostID string) error {
	if err == nil {
		return nil
	}
	if isTooOld(err) {
		return &UnreachableError{Host: hostID, Err: errTooOld(hostID)}
	}
	if st, ok := statusOf(err); ok {
		return st
	}
	return err
}
