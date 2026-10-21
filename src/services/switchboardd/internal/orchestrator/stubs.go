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

// --- user story 6 ---

// pushBriefingsTo sends UpdateWorker{name, briefing} to this orchestrator's
// workers on one host.
func (rt *runtime) pushBriefingsTo(ctx context.Context, hostID string, api WorkerAPI) {
	rec := rt.record()
	for _, w := range rec.GetWorkers() {
		if w.GetHostId() != hostID {
			continue
		}
		name := rec.GetName()
		briefing := renderBriefing(name, w.GetReasoning(), rec.GetActions())
		cctx, cancel := context.WithTimeout(ctx, unaryTimeout)
		sb, err := api.UpdateWorker(cctx, &pb.UpdateWorkerRequest{SandboxId: w.GetSandboxId(), OrchestratorId: rt.id, Name: &name, Briefing: &briefing})
		cancel()
		if err != nil {
			rt.svc.logf("[orchestrator %s] update worker %s on %s: %v", name, w.GetSandboxId(), hostID, err)
			continue
		}
		rt.mu.Lock()
		rt.sbs[w.GetSandboxId()] = sb
		rt.mu.Unlock()
	}
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
