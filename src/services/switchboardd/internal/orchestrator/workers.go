package orchestrator

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// reconcile re-reads a host's sandboxes on (re)connect (research R6): records
// that name this orchestrator are adopted into the roster, roster entries whose
// record no longer carries it are dropped (failing any run on them), the latest
// name + briefing are pushed, and in-flight runs are re-checked (FR-134).
func (rt *runtime) reconcile(ctx context.Context, hostID string, api WorkerAPI) error {
	rt.reconcileMu.Lock()
	defer rt.reconcileMu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
	sbs, err := api.ListSandboxes(rctx)
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}
	mine := map[string]*pb.Sandbox{}
	for _, sb := range sbs {
		if sb.GetController().GetOrchestratorId() == rt.id {
			mine[sb.GetId()] = sb
		}
	}
	// Roster: adopt + drop.
	var dropped []string
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		kept := o.Workers[:0]
		for _, w := range o.Workers {
			if w.GetHostId() != hostID {
				kept = append(kept, w)
				continue
			}
			if _, ok := mine[w.GetSandboxId()]; ok {
				kept = append(kept, w)
			} else {
				dropped = append(dropped, w.GetSandboxId())
			}
		}
		o.Workers = kept
		for id, sb := range mine {
			if !rosterHas(o, id) {
				o.Workers = append(o.Workers, &pb.Worker{SandboxId: id, HostId: hostID, AttachedAt: sb.GetController().GetAttachedAt(), Reasoning: sb.GetController().GetReasoning()})
			}
			if sb.GetController().GetReasoning() {
				o.ReasoningSandboxId = id
			}
		}
		return nil
	}); err != nil {
		return err
	}
	rt.mu.Lock()
	for id, sb := range rt.sbs {
		if sb.GetHostId() == hostID || hostOf(rt.rec, id) == hostID {
			if _, ok := mine[id]; !ok {
				delete(rt.sbs, id)
			}
		}
	}
	for id, sb := range mine {
		rt.sbs[id] = proto.Clone(sb).(*pb.Sandbox)
	}
	rt.mu.Unlock()
	for _, id := range dropped {
		rt.failRunsOn(id, "worker detached")
	}
	if hostID == rt.svc.hostID {
		if rid := rt.record().GetReasoningSandboxId(); rid != "" {
			if _, ok := mine[rid]; !ok {
				rt.reasoningLost()
			}
		}
	}
	rt.pushBriefingsTo(ctx, hostID, api)
	rt.reconcileInFlight(hostID)
	if len(mine) > 0 || len(dropped) > 0 {
		rt.svc.emitChanged(rt.id)
	}
	return nil
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
		sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_WAITING, // on background work (SWI-12)
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
		Briefing:   renderBriefing(rec.GetName(), false, rec.GetActions()),
	}
}

// attach records this orchestrator as the controller of an existing sandbox
// through its host's link (FR-107/FR-108).
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
		Briefing:   renderBriefing(rec.GetName(), false, rec.GetActions()),
	})
	if err != nil {
		return nil, remoteErr(err, hostID)
	}
	// Committed under reconcileMu, like a reasoning launch: a reconcile whose
	// listing predates the attach would otherwise drop the new worker.
	rt.reconcileMu.Lock()
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		if !rosterHas(o, sandboxID) {
			o.Workers = append(o.Workers, &pb.Worker{SandboxId: sandboxID, HostId: hostID, AttachedAt: sb.GetController().GetAttachedAt()})
		}
		return nil
	}); err != nil {
		rt.reconcileMu.Unlock()
		return nil, err
	}
	rt.mu.Lock()
	rt.sbs[sandboxID] = proto.Clone(sb).(*pb.Sandbox)
	rt.mu.Unlock()
	rt.reconcileMu.Unlock()
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
	up, _ := rt.reachability(hostID)
	return &pb.WorkerView{
		SandboxId: sandboxID, HostId: hostID, DisplayName: sb.GetDisplayName(), Tag: sb.GetTag(),
		State: sb.GetState(), AgentStatus: sb.GetAgent().GetStatus(), Availability: availability(sb, up), HostReachable: up,
	}, nil
}

// detach clears the controller on a worker through its link (FR-111): refused
// while a run is recorded on it and for the reasoning sandbox.
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
	if _, err := api.DetachController(cctx, &pb.DetachControllerRequest{SandboxId: sandboxID, OrchestratorId: rt.id}); err != nil {
		return remoteErr(err, hostID)
	}
	// Under reconcileMu: a reconcile whose listing predates the detach would
	// otherwise re-adopt the worker it still shows as controlled.
	rt.reconcileMu.Lock()
	rt.dropWorker(sandboxID)
	rt.reconcileMu.Unlock()
	rt.svc.emitChanged(rt.id)
	return nil
}

// dropWorker removes a sandbox from the roster and the record cache.
func (rt *runtime) dropWorker(sandboxID string) {
	_, _ = rt.update(func(o *pb.Orchestrator) error {
		kept := o.Workers[:0]
		for _, w := range o.Workers {
			if w.GetSandboxId() != sandboxID {
				kept = append(kept, w)
			}
		}
		o.Workers = kept
		return nil
	})
	rt.mu.Lock()
	delete(rt.sbs, sandboxID)
	rt.mu.Unlock()
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
