package orchestrator

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/status"
)

// Small helpers shared by the runtime's host-facing paths.

// statusOf reports whether err already carries a gRPC status.
func statusOf(err error) (error, bool) {
	if _, ok := status.FromError(err); ok {
		return err, true
	}
	return nil, false
}

// pushWorkerStateTo sends UpdateWorker{name, briefing, lanes} to this
// orchestrator's workers on one host (a reconnect, or a briefing refresh).
// Each push holds the worker's pushMu and reads the record while holding it,
// so of racing pushes the last to land carries the latest value (R12).
func (rt *runtime) pushWorkerStateTo(ctx context.Context, hostID string, api WorkerAPI) {
	for _, w := range rt.record().GetWorkers() {
		if w.GetHostId() != hostID {
			continue
		}
		// A failure is logged by pushWorker; the next reconnect retries.
		_ = rt.pushWorker(ctx, api, w.GetSandboxId(), true)
	}
}

// pushWorker pushes one worker's display state: its lanes, and with full also
// its orchestrator name and briefing. It reports whether the push failed.
func (rt *runtime) pushWorker(ctx context.Context, api WorkerAPI, sandboxID string, full bool) error {
	l := rt.workerPushLock(sandboxID)
	l.Lock()
	defer l.Unlock()
	rec := rt.record()
	var w *pb.Worker
	for _, x := range rec.GetWorkers() {
		if x.GetSandboxId() == sandboxID {
			w = x
		}
	}
	if w == nil {
		return nil // left the roster meanwhile
	}
	req := &pb.UpdateWorkerRequest{SandboxId: sandboxID, OrchestratorId: rt.id}
	if full {
		name := rec.GetName()
		briefing := renderBriefing(rec, w.GetReasoning())
		req.Name, req.Briefing = &name, &briefing
	}
	if !w.GetReasoning() {
		req.Lanes = &pb.WorkerLaneDisplay{Names: laneNames(rec, w.GetLaneIds()), Pending: w.GetLanesPending()}
	} else if !full {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, unaryTimeout)
	sb, err := api.UpdateWorker(cctx, req)
	cancel()
	if err != nil {
		rt.svc.logf("[orchestrator %s] update worker %s on %s: %v", rec.GetName(), sandboxID, w.GetHostId(), err)
		return err
	}
	rt.mu.Lock()
	rt.cacheLocked(sb)
	rt.mu.Unlock()
	return nil
}

// hostIDs lists the hosts the runtime has links for.
func (rt *runtime) hostIDs() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out := make([]string, 0, len(rt.links))
	for id := range rt.links {
		out = append(out, id)
	}
	return out
}
