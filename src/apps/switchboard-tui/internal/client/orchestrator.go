package client

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Orchestrator wrappers (feature 009, FR-105..FR-157). The nineteen
// orchestrator-side RPCs act on the daemon behind this Conn (the one hosting the
// orchestrator); DetachController is the one worker-side RPC the TUI calls
// directly, on the sandbox's OWN daemon, when the orchestrator is unreachable
// (FR-112). Errors keep their gRPC codes so the UI can show the daemon's text
// verbatim; UNIMPLEMENTED (an older daemon) becomes a DaemonTooOldError.

// ErrDaemonTooOld is wrapped by every orchestrator call that an older daemon
// answers with UNIMPLEMENTED; the `u` update fan-out is the remedy.
var ErrDaemonTooOld = errors.New("daemon is too old")

// DaemonTooOldError names the host whose daemon predates feature 009.
type DaemonTooOldError struct{ Host string }

func (e *DaemonTooOldError) Error() string {
	return fmt.Sprintf("daemon on %s is too old — update with u", e.Host)
}

func (e *DaemonTooOldError) Is(target error) bool { return target == ErrDaemonTooOld }

// orchErr maps UNIMPLEMENTED to DaemonTooOldError and passes everything else
// through with its code.
func (c *Conn) orchErr(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) == codes.Unimplemented {
		return &DaemonTooOldError{Host: c.HostID()}
	}
	return err
}

func (c *Conn) ListOrchestrators(ctx context.Context) ([]*pb.OrchestratorSummary, error) {
	resp, err := c.API().ListOrchestrators(ctx, &pb.ListOrchestratorsRequest{})
	if err != nil {
		return nil, c.orchErr(err)
	}
	return resp.GetOrchestrators(), nil
}

func (c *Conn) GetOrchestrator(ctx context.Context, id string) (*pb.OrchestratorDetail, error) {
	out, err := c.API().GetOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: id})
	return out, c.orchErr(err)
}

func (c *Conn) CreateOrchestrator(ctx context.Context, name string) (*pb.Orchestrator, error) {
	out, err := c.API().CreateOrchestrator(ctx, &pb.CreateOrchestratorRequest{Name: name})
	return out, c.orchErr(err)
}

func (c *Conn) RenameOrchestrator(ctx context.Context, id, name string) (*pb.Orchestrator, error) {
	out, err := c.API().RenameOrchestrator(ctx, &pb.RenameOrchestratorRequest{OrchestratorId: id, Name: name})
	return out, c.orchErr(err)
}

func (c *Conn) RemoveOrchestrator(ctx context.Context, id string) (*pb.RemoveOrchestratorResponse, error) {
	out, err := c.API().RemoveOrchestrator(ctx, &pb.OrchestratorIdRequest{OrchestratorId: id})
	return out, c.orchErr(err)
}

func (c *Conn) SetOrchestratorHosts(ctx context.Context, id string, hosts []*pb.OrchestratorHost) (*pb.Orchestrator, error) {
	out, err := c.API().SetOrchestratorHosts(ctx, &pb.SetOrchestratorHostsRequest{OrchestratorId: id, Hosts: hosts})
	return out, c.orchErr(err)
}

// GetWorkerBundle fetches a launch's bundle. With laneIDs (SWI-18 FR-306a)
// the daemon issues an attach ticket bound to hostID, the target daemon's
// host id.
func (c *Conn) GetWorkerBundle(ctx context.Context, id, hostID string, laneIDs []string) (*pb.WorkerBundle, error) {
	out, err := c.API().GetWorkerBundle(ctx, &pb.GetWorkerBundleRequest{OrchestratorId: id, HostId: hostID, LaneIds: laneIDs})
	return out, c.orchErr(err)
}

func (c *Conn) AttachWorker(ctx context.Context, id, hostID, sandboxID string, laneIDs []string) (*pb.WorkerView, error) {
	out, err := c.API().AttachWorker(ctx, &pb.AttachWorkerRequest{OrchestratorId: id, HostId: hostID, SandboxId: sandboxID, LaneIds: laneIDs})
	return out, c.orchErr(err)
}

// DetachWorker detaches through the orchestrator; guard confirms the open-lane
// guard's list (SWI-18 FR-308a; nil against a daemon without lanes).
func (c *Conn) DetachWorker(ctx context.Context, id, sandboxID string, guard *pb.LaneGuardConfirm) error {
	_, err := c.API().DetachWorker(ctx, &pb.DetachWorkerRequest{OrchestratorId: id, SandboxId: sandboxID, Guard: guard})
	return c.orchErr(err)
}

// DetachController is the direct worker-side call on the sandbox's own daemon
// (FR-112), for when the orchestrator's daemon cannot be reached.
func (c *Conn) DetachController(ctx context.Context, sandboxID, orchestratorID string, force bool) (*pb.Sandbox, error) {
	out, err := c.API().DetachController(ctx, &pb.DetachControllerRequest{SandboxId: sandboxID, OrchestratorId: orchestratorID, Force: force})
	return out, c.orchErr(err)
}

func (c *Conn) MarkWorkerFree(ctx context.Context, id, sandboxID string, failed bool, note string) (*pb.Run, error) {
	out, err := c.API().MarkWorkerFree(ctx, &pb.MarkWorkerFreeRequest{OrchestratorId: id, SandboxId: sandboxID, Failed: failed, Note: note})
	return out, c.orchErr(err)
}

// PutAction saves an action as a lanes-aware client (sets_lanes, SWI-18
// FR-318a), with guard confirming the open-lane guard's list.
func (c *Conn) PutAction(ctx context.Context, id string, a *pb.Action, guard *pb.LaneGuardConfirm) (*pb.Action, error) {
	out, err := c.API().PutAction(ctx, &pb.PutActionRequest{OrchestratorId: id, Action: a, SetsLanes: true, Guard: guard})
	return out, c.orchErr(err)
}

func (c *Conn) DeleteAction(ctx context.Context, id, name string) ([]string, error) {
	resp, err := c.API().DeleteAction(ctx, &pb.DeleteActionRequest{OrchestratorId: id, Name: name})
	if err != nil {
		return nil, c.orchErr(err)
	}
	return resp.GetDisabledEvents(), nil
}

func (c *Conn) PutTimedEvent(ctx context.Context, id string, ev *pb.TimedEvent) (*pb.TimedEvent, error) {
	out, err := c.API().PutTimedEvent(ctx, &pb.PutTimedEventRequest{OrchestratorId: id, Event: ev})
	return out, c.orchErr(err)
}

func (c *Conn) DeleteTimedEvent(ctx context.Context, id, eventID string) error {
	_, err := c.API().DeleteTimedEvent(ctx, &pb.DeleteTimedEventRequest{OrchestratorId: id, EventId: eventID})
	return c.orchErr(err)
}

// FireTimedEvent enqueues one run of a timed event now, as that event (SWI-4);
// the event's schedule is untouched.
func (c *Conn) FireTimedEvent(ctx context.Context, id, eventID string) (*pb.Run, error) {
	out, err := c.API().FireTimedEvent(ctx, &pb.FireTimedEventRequest{OrchestratorId: id, EventId: eventID})
	return out, c.orchErr(err)
}

func (c *Conn) CallAction(ctx context.Context, id, action string, args map[string]string) (*pb.Run, error) {
	out, err := c.API().CallAction(ctx, &pb.CallActionRequest{OrchestratorId: id, Action: action, Arguments: args})
	return out, c.orchErr(err)
}

func (c *Conn) ListRuns(ctx context.Context, id string) (*pb.ListRunsResponse, error) {
	out, err := c.API().ListRuns(ctx, &pb.ListRunsRequest{OrchestratorId: id})
	return out, c.orchErr(err)
}

func (c *Conn) GetRun(ctx context.Context, id, runID string) (*pb.Run, error) {
	out, err := c.API().GetRun(ctx, &pb.GetRunRequest{OrchestratorId: id, RunId: runID})
	return out, c.orchErr(err)
}

func (c *Conn) CancelRun(ctx context.Context, id, runID string) (*pb.Run, error) {
	out, err := c.API().CancelRun(ctx, &pb.CancelRunRequest{OrchestratorId: id, RunId: runID})
	return out, c.orchErr(err)
}

// ResolveRunStep retries, skips or fails a run's halted step (SWI-17, FR-222).
// step must be the run's halted step; FAILED_PRECONDITION means the run moved on.
func (c *Conn) ResolveRunStep(ctx context.Context, id, runID string, step int32, res pb.StepResolution) (*pb.Run, error) {
	out, err := c.API().ResolveRunStep(ctx, &pb.ResolveRunStepRequest{OrchestratorId: id, RunId: runID, Step: step, Resolution: res})
	return out, c.orchErr(err)
}

// RebuildReasoningSandbox destroys and relaunches the orchestrator's reasoning
// sandbox with setup (saved first when non-nil), draining the LaunchProgress
// stream like Refresh (FR-146).
func (c *Conn) RebuildReasoningSandbox(ctx context.Context, id string, setup *pb.ReasoningSetup, onLog func(LaunchUpdate)) (*pb.Sandbox, error) {
	stream, err := c.API().RebuildReasoningSandbox(ctx, &pb.RebuildReasoningSandboxRequest{OrchestratorId: id, Setup: setup})
	if err != nil {
		return nil, c.orchErr(err)
	}
	out, err := drainLaunchProgress(stream, onLog)
	return out, c.orchErr(err)
}

// --- lanes (SWI-18) ---

// ErrLaneStale matches a lane write refused because what the developer saw
// changed since (ABORTED, FR-307a): reload and show the daemon's message.
var ErrLaneStale = errors.New("lanes changed since they were shown")

// laneErr is orchErr plus ErrLaneStale for ABORTED; the daemon's text and code
// are kept.
func (c *Conn) laneErr(err error) error {
	if status.Code(err) == codes.Aborted {
		return &laneStaleError{err: err}
	}
	return c.orchErr(err)
}

type laneStaleError struct{ err error }

func (e *laneStaleError) Error() string              { return status.Convert(e.err).Message() }
func (e *laneStaleError) Is(target error) bool       { return target == ErrLaneStale }
func (e *laneStaleError) GRPCStatus() *status.Status { return status.Convert(e.err) }

// ImpactFromError reads the open-lane guard's list from a refused guarded
// write (FAILED_PRECONDITION with a LaneChangeImpact detail).
func ImpactFromError(err error) (*pb.LaneChangeImpact, bool) {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return nil, false
	}
	for _, d := range st.Details() {
		if impact, ok := d.(*pb.LaneChangeImpact); ok {
			return impact, true
		}
	}
	return nil, false
}

// PutLane creates (empty laneID) or renames / re-describes a lane.
func (c *Conn) PutLane(ctx context.Context, id, laneID, name, description string) (*pb.Lane, error) {
	out, err := c.API().PutLane(ctx, &pb.PutLaneRequest{OrchestratorId: id, LaneId: laneID, Name: name, Description: description})
	return out, c.orchErr(err)
}

// DeleteLane deletes a lane and returns the impact the daemon applied.
func (c *Conn) DeleteLane(ctx context.Context, id, laneID string, guard *pb.LaneGuardConfirm) (*pb.LaneChangeImpact, error) {
	resp, err := c.API().DeleteLane(ctx, &pb.DeleteLaneRequest{OrchestratorId: id, LaneId: laneID, Guard: guard})
	if err != nil {
		return nil, c.laneErr(err)
	}
	return resp.GetApplied(), nil
}

func (c *Conn) SetWorkerLanes(ctx context.Context, req *pb.SetWorkerLanesRequest) (*pb.WorkerView, error) {
	out, err := c.API().SetWorkerLanes(ctx, req)
	return out, c.laneErr(err)
}

func (c *Conn) SetLaneMembers(ctx context.Context, req *pb.SetLaneMembersRequest) (*pb.Lane, error) {
	out, err := c.API().SetLaneMembers(ctx, req)
	return out, c.laneErr(err)
}

// CheckLaneChange reports what a guarded change would affect; nothing is
// written. Only call it against a daemon with the lanes capability.
func (c *Conn) CheckLaneChange(ctx context.Context, req *pb.CheckLaneChangeRequest) (*pb.LaneChangeImpact, error) {
	out, err := c.API().CheckLaneChange(ctx, req)
	return out, c.laneErr(err)
}

func (c *Conn) DismissDroppedWorkerTags(ctx context.Context, id string) (*pb.Orchestrator, error) {
	out, err := c.API().DismissDroppedWorkerTags(ctx, &pb.OrchestratorIdRequest{OrchestratorId: id})
	return out, c.orchErr(err)
}
