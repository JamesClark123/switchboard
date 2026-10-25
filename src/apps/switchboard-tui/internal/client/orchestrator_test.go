package client_test

import (
	"context"
	"errors"
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
	return &pb.WorkerBundle{Controller: &pb.SandboxController{OrchestratorId: r.GetOrchestratorId()}, Briefing: "b"}, nil
}
func (s *orchServer) AttachWorker(_ context.Context, r *pb.AttachWorkerRequest) (*pb.WorkerView, error) {
	s.calls = append(s.calls, "attach "+r.GetHostId()+" "+r.GetSandboxId())
	return &pb.WorkerView{SandboxId: r.GetSandboxId(), HostId: r.GetHostId()}, nil
}
func (s *orchServer) DetachWorker(_ context.Context, r *pb.DetachWorkerRequest) (*pb.DetachWorkerResponse, error) {
	s.calls = append(s.calls, "detach "+r.GetSandboxId())
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
	if b, err := conn.GetWorkerBundle(ctx, "o1"); err != nil || b.GetBriefing() != "b" {
		t.Errorf("GetWorkerBundle = %v / %v", b, err)
	}
	if w, err := conn.AttachWorker(ctx, "o1", "h", "sb"); err != nil || w.GetSandboxId() != "sb" {
		t.Errorf("AttachWorker = %v / %v", w, err)
	}
	if err := conn.DetachWorker(ctx, "o1", "sb"); err != nil {
		t.Errorf("DetachWorker = %v", err)
	}
	if sb, err := conn.DetachController(ctx, "sb", "o1", true); err != nil || sb.GetId() != "sb" {
		t.Errorf("DetachController = %v / %v", sb, err)
	}
	if run, err := conn.MarkWorkerFree(ctx, "o1", "sb", true, "note"); err != nil || run.GetState() != pb.RunState_RUN_STATE_FAILED || run.GetSummary() != "note" {
		t.Errorf("MarkWorkerFree = %v / %v", run, err)
	}
	if a, err := conn.PutAction(ctx, "o1", &pb.Action{Name: "review-pr"}); err != nil || a.GetName() != "review-pr" {
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
	if len(srv.calls) != 3 || srv.calls[0] != "attach h sb" || srv.calls[2] != "detach-controller sb" {
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
		"Bundle":    func() error { _, e := conn.GetWorkerBundle(ctx, "o"); return e },
		"Attach":    func() error { _, e := conn.AttachWorker(ctx, "o", "h", "s"); return e },
		"Detach":    func() error { return conn.DetachWorker(ctx, "o", "s") },
		"DetachCtl": func() error { _, e := conn.DetachController(ctx, "s", "o", false); return e },
		"MarkFree":  func() error { _, e := conn.MarkWorkerFree(ctx, "o", "s", false, ""); return e },
		"PutAction": func() error { _, e := conn.PutAction(ctx, "o", nil); return e },
		"DelAction": func() error { _, e := conn.DeleteAction(ctx, "o", "a"); return e },
		"PutEvent":  func() error { _, e := conn.PutTimedEvent(ctx, "o", nil); return e },
		"DelEvent":  func() error { return conn.DeleteTimedEvent(ctx, "o", "e") },
		"FireEvent": func() error { _, err := conn.FireTimedEvent(ctx, "o", "e"); return err },
		"Call":      func() error { _, e := conn.CallAction(ctx, "o", "a", nil); return e },
		"ListRuns":  func() error { _, e := conn.ListRuns(ctx, "o"); return e },
		"GetRun":    func() error { _, e := conn.GetRun(ctx, "o", "r"); return e },
		"Cancel":    func() error { _, e := conn.CancelRun(ctx, "o", "r"); return e },
		"Rebuild":   func() error { _, e := conn.RebuildReasoningSandbox(ctx, "o", nil, nil); return e },
	} {
		if err := call(); !errors.Is(err, client.ErrDaemonTooOld) {
			t.Errorf("%s: err = %v, want ErrDaemonTooOld", name, err)
		}
	}
}
