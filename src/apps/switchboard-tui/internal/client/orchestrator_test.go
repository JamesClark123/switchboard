package client_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// orchServer scripts the feature-009 RPCs so every wrapper's shape is pinned.
type orchServer struct {
	bareServer
	calls []string
}

func (s *orchServer) ListOrchestrators(context.Context, *pb.ListOrchestratorsRequest) (*pb.ListOrchestratorsResponse, error) {
	return &pb.ListOrchestratorsResponse{Orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o1", Name: "review-bot"}}}}, nil
}
func (s *orchServer) GetOrchestrator(_ context.Context, r *pb.OrchestratorIdRequest) (*pb.OrchestratorDetail, error) {
	return &pb.OrchestratorDetail{Summary: &pb.OrchestratorSummary{Orchestrator: &pb.Orchestrator{Id: r.GetOrchestratorId()}}}, nil
}
func (s *orchServer) CreateOrchestrator(_ context.Context, r *pb.CreateOrchestratorRequest) (*pb.Orchestrator, error) {
	if r.GetName() == "taken" {
		return nil, status.Error(codes.AlreadyExists, "an orchestrator named taken already exists")
	}
	return &pb.Orchestrator{Id: "new", Name: r.GetName()}, nil
}
func (s *orchServer) RenameOrchestrator(_ context.Context, r *pb.RenameOrchestratorRequest) (*pb.Orchestrator, error) {
	return &pb.Orchestrator{Id: r.GetOrchestratorId(), Name: r.GetName()}, nil
}
func (s *orchServer) RemoveOrchestrator(context.Context, *pb.OrchestratorIdRequest) (*pb.RemoveOrchestratorResponse, error) {
	return &pb.RemoveOrchestratorResponse{WorkersDetached: 2}, nil
}
func (s *orchServer) SetOrchestratorHosts(_ context.Context, r *pb.SetOrchestratorHostsRequest) (*pb.Orchestrator, error) {
	return &pb.Orchestrator{Id: r.GetOrchestratorId(), Hosts: r.GetHosts()}, nil
}
func (s *orchServer) GetWorkerBundle(_ context.Context, r *pb.GetWorkerBundleRequest) (*pb.WorkerBundle, error) {
	s.calls = append(s.calls, "bundle "+r.GetHostId()+" "+strings.Join(r.GetLaneIds(), ","))
	return &pb.WorkerBundle{Controller: &pb.SandboxController{OrchestratorId: r.GetOrchestratorId()}, Briefing: "b"}, nil
}
func (s *orchServer) AttachWorker(_ context.Context, r *pb.AttachWorkerRequest) (*pb.WorkerView, error) {
	s.calls = append(s.calls, "attach "+r.GetHostId()+" "+r.GetSandboxId()+" "+strings.Join(r.GetLaneIds(), ","))
	return &pb.WorkerView{SandboxId: r.GetSandboxId(), HostId: r.GetHostId()}, nil
}
func (s *orchServer) DetachWorker(_ context.Context, r *pb.DetachWorkerRequest) (*pb.DetachWorkerResponse, error) {
	s.calls = append(s.calls, "detach "+r.GetSandboxId()+" "+strings.Join(r.GetGuard().GetUnlanedActions(), ","))
	return &pb.DetachWorkerResponse{}, nil
}
func (s *orchServer) DetachController(_ context.Context, r *pb.DetachControllerRequest) (*pb.Sandbox, error) {
	s.calls = append(s.calls, "detach-controller "+r.GetSandboxId())
	return &pb.Sandbox{Id: r.GetSandboxId()}, nil
}
func (s *orchServer) MarkWorkerFree(_ context.Context, r *pb.MarkWorkerFreeRequest) (*pb.Run, error) {
	st := pb.RunState_RUN_STATE_COMPLETED
	if r.GetFailed() {
		st = pb.RunState_RUN_STATE_FAILED
	}
	return &pb.Run{Id: "run-1", State: st, Summary: r.GetNote(), ClosedByDeveloper: true}, nil
}
func (s *orchServer) PutAction(_ context.Context, r *pb.PutActionRequest) (*pb.Action, error) {
	if !r.GetSetsLanes() {
		return nil, status.Error(codes.FailedPrecondition, "this client predates lanes")
	}
	return r.GetAction(), nil
}
func (s *orchServer) DeleteAction(_ context.Context, r *pb.DeleteActionRequest) (*pb.DeleteActionResponse, error) {
	return &pb.DeleteActionResponse{DisabledEvents: []string{"poll"}}, nil
}
func (s *orchServer) PutTimedEvent(_ context.Context, r *pb.PutTimedEventRequest) (*pb.TimedEvent, error) {
	return r.GetEvent(), nil
}
func (s *orchServer) DeleteTimedEvent(context.Context, *pb.DeleteTimedEventRequest) (*pb.DeleteTimedEventResponse, error) {
	return &pb.DeleteTimedEventResponse{}, nil
}
func (s *orchServer) FireTimedEvent(_ context.Context, r *pb.FireTimedEventRequest) (*pb.Run, error) {
	return &pb.Run{Id: "run-8", Origin: r.GetEventId(), EventKind: pb.EventKind_EVENT_KIND_TIMED, Position: 1}, nil
}
func (s *orchServer) CallAction(_ context.Context, r *pb.CallActionRequest) (*pb.Run, error) {
	return &pb.Run{Id: "run-7", Action: r.GetAction(), Arguments: r.GetArguments(), Position: 3}, nil
}
func (s *orchServer) ListRuns(context.Context, *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	return &pb.ListRunsResponse{Queued: []*pb.Run{{Id: "run-1"}}}, nil
}
func (s *orchServer) GetRun(_ context.Context, r *pb.GetRunRequest) (*pb.Run, error) {
	return &pb.Run{Id: r.GetRunId(), Prompt: "composed"}, nil
}
func (s *orchServer) CancelRun(_ context.Context, r *pb.CancelRunRequest) (*pb.Run, error) {
	return &pb.Run{Id: r.GetRunId(), State: pb.RunState_RUN_STATE_CANCELLED}, nil
}
func (s *orchServer) RebuildReasoningSandbox(r *pb.RebuildReasoningSandboxRequest, stream pb.Switchboard_RebuildReasoningSandboxServer) error {
	_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: "creating"}})
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: &pb.Sandbox{Id: "reasoning", DisplayName: "reasoning-" + r.GetOrchestratorId()}}})
}

func TestOrchestratorWrappers(t *testing.T) {
	srv := &orchServer{}
	conn := dialServer(t, srv)
	ctx := context.Background()

	if list, err := conn.ListOrchestrators(ctx); err != nil || len(list) != 1 || list[0].GetOrchestrator().GetName() != "review-bot" {
		t.Errorf("ListOrchestrators = %v / %v", list, err)
	}
	if d, err := conn.GetOrchestrator(ctx, "o1"); err != nil || d.GetSummary().GetOrchestrator().GetId() != "o1" {
		t.Errorf("GetOrchestrator = %v / %v", d, err)
	}
	if o, err := conn.CreateOrchestrator(ctx, "x"); err != nil || o.GetName() != "x" {
		t.Errorf("CreateOrchestrator = %v / %v", o, err)
	}
	if _, err := conn.CreateOrchestrator(ctx, "taken"); status.Code(err) != codes.AlreadyExists {
		t.Errorf("duplicate create should keep ALREADY_EXISTS, got %v", err)
	}
	if o, err := conn.RenameOrchestrator(ctx, "o1", "y"); err != nil || o.GetName() != "y" {
		t.Errorf("Rename = %v / %v", o, err)
	}
	if r, err := conn.RemoveOrchestrator(ctx, "o1"); err != nil || r.GetWorkersDetached() != 2 {
		t.Errorf("Remove = %v / %v", r, err)
	}
	if o, err := conn.SetOrchestratorHosts(ctx, "o1", []*pb.OrchestratorHost{{HostId: "h"}}); err != nil || len(o.GetHosts()) != 1 {
		t.Errorf("SetHosts = %v / %v", o, err)
	}
	if b, err := conn.GetWorkerBundle(ctx, "o1", "h", []string{"ln-a"}); err != nil || b.GetBriefing() != "b" {
		t.Errorf("GetWorkerBundle = %v / %v", b, err)
	}
	if w, err := conn.AttachWorker(ctx, "o1", "h", "sb", []string{"ln-a"}); err != nil || w.GetSandboxId() != "sb" {
		t.Errorf("AttachWorker = %v / %v", w, err)
	}
	if err := conn.DetachWorker(ctx, "o1", "sb", &pb.LaneGuardConfirm{UnlanedActions: []string{"a"}}); err != nil {
		t.Errorf("DetachWorker = %v", err)
	}
	if sb, err := conn.DetachController(ctx, "sb", "o1", true); err != nil || sb.GetId() != "sb" {
		t.Errorf("DetachController = %v / %v", sb, err)
	}
	if run, err := conn.MarkWorkerFree(ctx, "o1", "sb", true, "note"); err != nil || run.GetState() != pb.RunState_RUN_STATE_FAILED || run.GetSummary() != "note" {
		t.Errorf("MarkWorkerFree = %v / %v", run, err)
	}
	if a, err := conn.PutAction(ctx, "o1", &pb.Action{Name: "review-pr"}, nil); err != nil || a.GetName() != "review-pr" {
		t.Errorf("PutAction = %v / %v", a, err)
	}
	if ev, err := conn.DeleteAction(ctx, "o1", "review-pr"); err != nil || len(ev) != 1 || ev[0] != "poll" {
		t.Errorf("DeleteAction = %v / %v", ev, err)
	}
	if ev, err := conn.PutTimedEvent(ctx, "o1", &pb.TimedEvent{Name: "poll"}); err != nil || ev.GetName() != "poll" {
		t.Errorf("PutTimedEvent = %v / %v", ev, err)
	}
	if err := conn.DeleteTimedEvent(ctx, "o1", "tev-1"); err != nil {
		t.Errorf("DeleteTimedEvent = %v", err)
	}
	if run, err := conn.FireTimedEvent(ctx, "o1", "tev-1"); err != nil || run.GetOrigin() != "tev-1" || run.GetEventKind() != pb.EventKind_EVENT_KIND_TIMED {
		t.Errorf("FireTimedEvent = %v / %v", run, err)
	}
	if run, err := conn.CallAction(ctx, "o1", "review-pr", map[string]string{"number": "412"}); err != nil || run.GetPosition() != 3 || run.GetArguments()["number"] != "412" {
		t.Errorf("CallAction = %v / %v", run, err)
	}
	if runs, err := conn.ListRuns(ctx, "o1"); err != nil || len(runs.GetQueued()) != 1 {
		t.Errorf("ListRuns = %v / %v", runs, err)
	}
	if run, err := conn.GetRun(ctx, "o1", "run-1"); err != nil || run.GetPrompt() != "composed" {
		t.Errorf("GetRun = %v / %v", run, err)
	}
	if run, err := conn.CancelRun(ctx, "o1", "run-1"); err != nil || run.GetState() != pb.RunState_RUN_STATE_CANCELLED {
		t.Errorf("CancelRun = %v / %v", run, err)
	}
	var logs []string
	sb, err := conn.RebuildReasoningSandbox(ctx, "o1", &pb.ReasoningSetup{}, func(u client.LaunchUpdate) {
		if u.LogLine != "" {
			logs = append(logs, u.LogLine)
		}
	})
	if err != nil || sb.GetDisplayName() != "reasoning-o1" || len(logs) != 1 {
		t.Errorf("Rebuild = %v / %v / %v", sb, err, logs)
	}
	if len(srv.calls) != 4 || srv.calls[0] != "bundle h ln-a" || srv.calls[1] != "attach h sb ln-a" || srv.calls[2] != "detach sb a" || srv.calls[3] != "detach-controller sb" {
		t.Errorf("server calls = %v", srv.calls)
	}
}

// An older daemon answers every orchestrator RPC with UNIMPLEMENTED; the
// wrapper turns that into DaemonTooOldError naming the host.
func TestOrchestratorTooOld(t *testing.T) {
	conn := dialServer(t, bareServer{})
	ctx := context.Background()
	_, err := conn.ListOrchestrators(ctx)
	if !errors.Is(err, client.ErrDaemonTooOld) {
		t.Fatalf("err = %v, want ErrDaemonTooOld", err)
	}
	var too *client.DaemonTooOldError
	if !errors.As(err, &too) || too.Host != "h" || too.Error() != "daemon on h is too old — update with u" {
		t.Errorf("DaemonTooOldError = %v", err)
	}
	for name, call := range map[string]func() error{
		"Get":       func() error { _, e := conn.GetOrchestrator(ctx, "o"); return e },
		"Create":    func() error { _, e := conn.CreateOrchestrator(ctx, "n"); return e },
		"Rename":    func() error { _, e := conn.RenameOrchestrator(ctx, "o", "n"); return e },
		"Remove":    func() error { _, e := conn.RemoveOrchestrator(ctx, "o"); return e },
		"SetHosts":  func() error { _, e := conn.SetOrchestratorHosts(ctx, "o", nil); return e },
		"Bundle":    func() error { _, e := conn.GetWorkerBundle(ctx, "o", "", nil); return e },
		"Attach":    func() error { _, e := conn.AttachWorker(ctx, "o", "h", "s", nil); return e },
		"Detach":    func() error { return conn.DetachWorker(ctx, "o", "s", nil) },
		"DetachCtl": func() error { _, e := conn.DetachController(ctx, "s", "o", false); return e },
		"MarkFree":  func() error { _, e := conn.MarkWorkerFree(ctx, "o", "s", false, ""); return e },
		"PutAction": func() error { _, e := conn.PutAction(ctx, "o", nil, nil); return e },
		"DelAction": func() error { _, e := conn.DeleteAction(ctx, "o", "a"); return e },
		"PutEvent":  func() error { _, e := conn.PutTimedEvent(ctx, "o", nil); return e },
		"DelEvent":  func() error { return conn.DeleteTimedEvent(ctx, "o", "e") },
		"FireEvent": func() error { _, err := conn.FireTimedEvent(ctx, "o", "e"); return err },
		"Call":      func() error { _, e := conn.CallAction(ctx, "o", "a", nil); return e },
		"ListRuns":  func() error { _, e := conn.ListRuns(ctx, "o"); return e },
		"GetRun":    func() error { _, e := conn.GetRun(ctx, "o", "r"); return e },
		"Cancel":    func() error { _, e := conn.CancelRun(ctx, "o", "r"); return e },
		"Rebuild":   func() error { _, e := conn.RebuildReasoningSandbox(ctx, "o", nil, nil); return e },
		"PutLane":   func() error { _, e := conn.PutLane(ctx, "o", "", "a", ""); return e },
		"DelLane":   func() error { _, e := conn.DeleteLane(ctx, "o", "l", nil); return e },
		"WLanes":    func() error { _, e := conn.SetWorkerLanes(ctx, &pb.SetWorkerLanesRequest{}); return e },
		"Members":   func() error { _, e := conn.SetLaneMembers(ctx, &pb.SetLaneMembersRequest{}); return e },
		"Check":     func() error { _, e := conn.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{}); return e },
		"Dismiss":   func() error { _, e := conn.DismissDroppedWorkerTags(ctx, "o"); return e },
	} {
		if err := call(); !errors.Is(err, client.ErrDaemonTooOld) {
			t.Errorf("%s: err = %v, want ErrDaemonTooOld", name, err)
		}
	}
}

// laneServer scripts the SWI-18 lane RPCs.
type laneServer struct {
	bareServer
}

func (laneServer) PutLane(_ context.Context, r *pb.PutLaneRequest) (*pb.Lane, error) {
	if r.GetName() == "taken" {
		return nil, status.Error(codes.AlreadyExists, `a lane named "taken" already exists`)
	}
	return &pb.Lane{Id: "ln-1", Name: r.GetName(), Description: r.GetDescription()}, nil
}
func (laneServer) DeleteLane(_ context.Context, r *pb.DeleteLaneRequest) (*pb.DeleteLaneResponse, error) {
	if r.GetGuard() == nil {
		st, _ := status.New(codes.FailedPrecondition, "no open-lane worker would be left for actions build — confirm to continue").
			WithDetails(&pb.LaneChangeImpact{UnlanedActions: []string{"build"}})
		return nil, st.Err()
	}
	return &pb.DeleteLaneResponse{Applied: &pb.LaneChangeImpact{ReroutedRuns: 2}}, nil
}
func (laneServer) SetWorkerLanes(_ context.Context, r *pb.SetWorkerLanesRequest) (*pb.WorkerView, error) {
	if r.GetExpectedLanesPending() {
		return nil, status.Error(codes.Aborted, "w's lanes changed since the picker opened — reloaded")
	}
	return &pb.WorkerView{SandboxId: r.GetSandboxId(), LaneIds: r.GetLaneIds()}, nil
}
func (laneServer) SetLaneMembers(_ context.Context, r *pb.SetLaneMembersRequest) (*pb.Lane, error) {
	return &pb.Lane{Id: r.GetLaneId()}, nil
}
func (laneServer) CheckLaneChange(context.Context, *pb.CheckLaneChangeRequest) (*pb.LaneChangeImpact, error) {
	return &pb.LaneChangeImpact{UnlanedActions: []string{"build"}}, nil
}
func (laneServer) DismissDroppedWorkerTags(_ context.Context, r *pb.OrchestratorIdRequest) (*pb.Orchestrator, error) {
	return &pb.Orchestrator{Id: r.GetOrchestratorId()}, nil
}

func TestLaneWrappers(t *testing.T) {
	conn := dialServer(t, laneServer{})
	ctx := context.Background()
	if l, err := conn.PutLane(ctx, "o", "", "frontend", "UI"); err != nil || l.GetName() != "frontend" || l.GetDescription() != "UI" {
		t.Errorf("PutLane = %v / %v", l, err)
	}
	if _, err := conn.PutLane(ctx, "o", "", "taken", ""); status.Code(err) != codes.AlreadyExists {
		t.Errorf("PutLane taken = %v", err)
	}
	_, err := conn.DeleteLane(ctx, "o", "ln-1", nil)
	impact, ok := client.ImpactFromError(err)
	if !ok || len(impact.GetUnlanedActions()) != 1 || impact.GetUnlanedActions()[0] != "build" {
		t.Errorf("ImpactFromError = %v %v (%v)", impact, ok, err)
	}
	if applied, err := conn.DeleteLane(ctx, "o", "ln-1", &pb.LaneGuardConfirm{UnlanedActions: []string{"build"}}); err != nil || applied.GetReroutedRuns() != 2 {
		t.Errorf("DeleteLane = %v / %v", applied, err)
	}
	if v, err := conn.SetWorkerLanes(ctx, &pb.SetWorkerLanesRequest{SandboxId: "w", LaneIds: []string{"ln-1"}}); err != nil || v.GetLaneIds()[0] != "ln-1" {
		t.Errorf("SetWorkerLanes = %v / %v", v, err)
	}
	_, err = conn.SetWorkerLanes(ctx, &pb.SetWorkerLanesRequest{SandboxId: "w", ExpectedLanesPending: true})
	if !errors.Is(err, client.ErrLaneStale) || err.Error() != "w's lanes changed since the picker opened — reloaded" || status.Code(err) != codes.Aborted {
		t.Errorf("stale = %v (%v)", err, status.Code(err))
	}
	if _, ok := client.ImpactFromError(err); ok {
		t.Error("an Aborted error carries no impact")
	}
	if _, ok := client.ImpactFromError(errors.New("plain")); ok {
		t.Error("a plain error carries no impact")
	}
	if _, ok := client.ImpactFromError(status.Error(codes.FailedPrecondition, "no detail")); ok {
		t.Error("a precondition with no detail carries no impact")
	}
	if l, err := conn.SetLaneMembers(ctx, &pb.SetLaneMembersRequest{LaneId: "ln-1"}); err != nil || l.GetId() != "ln-1" {
		t.Errorf("SetLaneMembers = %v / %v", l, err)
	}
	if imp, err := conn.CheckLaneChange(ctx, &pb.CheckLaneChangeRequest{}); err != nil || len(imp.GetUnlanedActions()) != 1 {
		t.Errorf("CheckLaneChange = %v / %v", imp, err)
	}
	if o, err := conn.DismissDroppedWorkerTags(ctx, "o"); err != nil || o.GetId() != "o" {
		t.Errorf("Dismiss = %v / %v", o, err)
	}
}
