package grpc

import (
	"context"
	"errors"
	"slices"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/escapehatch"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/orchestrator"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sbxkit"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Orchestrator-side RPCs (feature 009, FR-105..FR-157): served by the daemon
// that hosts the orchestrator. The handlers are filled in per user story; until
// then, and whenever no orchestrator service is configured, they answer
// UNAVAILABLE so an older client sees a clear message rather than a crash.

// errNoOrchestrator is the nil-config answer shared by both sides.
var errNoOrchestrator = status.Error(codes.Unavailable, "orchestrator support not configured")

// orchStatus maps the orchestrator/worker sentinels onto gRPC codes. An error
// that already carries a status passes through; anything else is INTERNAL.
func orchStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, orchestrator.ErrNotFound), errors.Is(err, worker.ErrNotFound),
		errors.Is(err, registry.ErrNotFound),
		errors.Is(err, registry.ErrOrchestratorNotFound),
		errors.Is(err, registry.ErrRunNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, orchestrator.ErrNameTaken), errors.Is(err, orchestrator.ErrAlreadyControlled), errors.Is(err, worker.ErrAlreadyControlled):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, worker.ErrStepSent):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, worker.ErrUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, orchestrator.ErrInvalid), errors.Is(err, worker.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, orchestrator.ErrPrecondition), errors.Is(err, worker.ErrPrecondition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, orchestrator.ErrUnreachable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, orchestrator.ErrForbidden), errors.Is(err, worker.ErrForbidden):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, orchestrator.ErrAborted):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, orchestrator.ErrExhausted):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func (s *Server) ListOrchestrators(context.Context, *pb.ListOrchestratorsRequest) (*pb.ListOrchestratorsResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	list, err := s.orchestrator.List()
	if err != nil {
		return nil, orchStatus(err)
	}
	return &pb.ListOrchestratorsResponse{Orchestrators: list}, nil
}

func (s *Server) GetOrchestrator(_ context.Context, req *pb.OrchestratorIdRequest) (*pb.OrchestratorDetail, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.Get(req.GetOrchestratorId())
	return out, orchStatus(err)
}

func (s *Server) CreateOrchestrator(_ context.Context, req *pb.CreateOrchestratorRequest) (*pb.Orchestrator, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.Create(req.GetName())
	return out, orchStatus(err)
}

func (s *Server) RenameOrchestrator(_ context.Context, req *pb.RenameOrchestratorRequest) (*pb.Orchestrator, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.Rename(req.GetOrchestratorId(), req.GetName())
	return out, orchStatus(err)
}

func (s *Server) RemoveOrchestrator(_ context.Context, req *pb.OrchestratorIdRequest) (*pb.RemoveOrchestratorResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.Remove(req.GetOrchestratorId())
	return out, orchStatus(err)
}

func (s *Server) SetOrchestratorHosts(_ context.Context, req *pb.SetOrchestratorHostsRequest) (*pb.Orchestrator, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.SetHosts(req.GetOrchestratorId(), req.GetHosts())
	return out, orchStatus(err)
}

func (s *Server) GetWorkerBundle(_ context.Context, req *pb.GetWorkerBundleRequest) (*pb.WorkerBundle, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.BundleFor(req.GetOrchestratorId(), req.GetHostId(), req.GetLaneIds())
	return out, orchStatus(err)
}

func (s *Server) AttachWorker(ctx context.Context, req *pb.AttachWorkerRequest) (*pb.WorkerView, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.AttachWorker(ctx, req.GetOrchestratorId(), req.GetHostId(), req.GetSandboxId(), req.GetLaneIds())
	return out, orchStatus(err)
}

func (s *Server) DetachWorker(ctx context.Context, req *pb.DetachWorkerRequest) (*pb.DetachWorkerResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	if err := s.orchestrator.DetachWorker(ctx, req.GetOrchestratorId(), req.GetSandboxId(), req.GetGuard()); err != nil {
		return nil, orchStatus(err)
	}
	return &pb.DetachWorkerResponse{}, nil
}

func (s *Server) MarkWorkerFree(_ context.Context, req *pb.MarkWorkerFreeRequest) (*pb.Run, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.MarkWorkerFree(req.GetOrchestratorId(), req.GetSandboxId(), req.GetFailed(), req.GetNote())
	return out, orchStatus(err)
}

func (s *Server) PutAction(_ context.Context, req *pb.PutActionRequest) (*pb.Action, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.PutAction(req)
	return out, orchStatus(err)
}

func (s *Server) DeleteAction(_ context.Context, req *pb.DeleteActionRequest) (*pb.DeleteActionResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	disabled, err := s.orchestrator.DeleteAction(req.GetOrchestratorId(), req.GetName())
	if err != nil {
		return nil, orchStatus(err)
	}
	return &pb.DeleteActionResponse{DisabledEvents: disabled}, nil
}

func (s *Server) PutTimedEvent(_ context.Context, req *pb.PutTimedEventRequest) (*pb.TimedEvent, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.PutTimedEvent(req.GetOrchestratorId(), req.GetEvent())
	return out, orchStatus(err)
}

func (s *Server) DeleteTimedEvent(_ context.Context, req *pb.DeleteTimedEventRequest) (*pb.DeleteTimedEventResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	if err := s.orchestrator.DeleteTimedEvent(req.GetOrchestratorId(), req.GetEventId()); err != nil {
		return nil, orchStatus(err)
	}
	return &pb.DeleteTimedEventResponse{}, nil
}

// FireTimedEvent enqueues one run of a timed event now (SWI-4).
func (s *Server) FireTimedEvent(_ context.Context, req *pb.FireTimedEventRequest) (*pb.Run, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.FireTimedEvent(req.GetOrchestratorId(), req.GetEventId())
	return out, orchStatus(err)
}

func (s *Server) CallAction(_ context.Context, req *pb.CallActionRequest) (*pb.Run, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.CallAction(req.GetOrchestratorId(), req.GetAction(), req.GetArguments(), pb.EventKind_EVENT_KIND_USER, "", "")
	return out, orchStatus(err)
}

func (s *Server) ListRuns(_ context.Context, req *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.ListRuns(req.GetOrchestratorId())
	return out, orchStatus(err)
}

func (s *Server) GetRun(_ context.Context, req *pb.GetRunRequest) (*pb.Run, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.GetRun(req.GetOrchestratorId(), req.GetRunId())
	return out, orchStatus(err)
}

func (s *Server) CancelRun(_ context.Context, req *pb.CancelRunRequest) (*pb.Run, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.CancelRun(req.GetOrchestratorId(), req.GetRunId())
	return out, orchStatus(err)
}

// ResolveRunStep retries, skips or fails a run's halted step (SWI-17, FR-222).
func (s *Server) ResolveRunStep(_ context.Context, req *pb.ResolveRunStepRequest) (*pb.Run, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.ResolveRunStep(req.GetOrchestratorId(), req.GetRunId(), req.GetStep(), req.GetResolution())
	return out, orchStatus(err)
}

func (s *Server) RebuildReasoningSandbox(req *pb.RebuildReasoningSandboxRequest, stream pb.Switchboard_RebuildReasoningSandboxServer) error {
	if s.orchestrator == nil {
		return errNoOrchestrator
	}
	onLog := func(line string) {
		_ = stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_LogLine{LogLine: line}})
	}
	sb, err := s.orchestrator.RebuildReasoningSandbox(stream.Context(), req.GetOrchestratorId(), req.GetSetup(), onLog)
	if err != nil {
		return orchStatus(err)
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

// LaunchReasoning is the orchestrator service's ReasoningLauncher (research
// R11): the reasoning sandbox is created exactly as LaunchSandbox creates one —
// kits materialized, escape-hatch commands and services resolved, the host's
// attach-by-default MCP servers plus the setup's own — on an empty workspace
// when the setup names no folder, with the controller applied before RUNNING.
func (s *Server) LaunchReasoning(ctx context.Context, req orchestrator.ReasoningLaunch, onLog func(string)) (*pb.Sandbox, error) {
	setup := req.Setup
	config := setup.GetConfig()
	if config == nil {
		config = &pb.ConfigSnapshot{}
	}
	if err := sbxkit.Validate(s.manifest, config.GetKitOptions()); err != nil {
		return nil, err
	}
	if len(setup.GetKits()) > 0 {
		if err := s.requireKitBaseline(); err != nil {
			return nil, err
		}
	}
	kitSources, err := s.kits.ResolveAll(setup.GetKits())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ehCommands, err := escapehatch.Resolve(escapehatch.CommandsFromRefs(setup.GetKits())...)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	services, err := portforward.Resolve(portforward.ServicesFromRefs(setup.GetKits())...)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	var mcpAttach *sandbox.McpAttach
	if s.mcp != nil && s.mcpAvail.Available {
		if att, err := s.mcp.ResolveAttach(ctx); err != nil {
			onLog("mcp: could not resolve attach-by-default servers (launching without): " + err.Error())
		} else {
			mcpAttach = &sandbox.McpAttach{Mode: att.Mode, Servers: att.Servers, Skipped: att.Skipped,
				BeingReplaced: att.BeingReplaced, ReplaceState: s.mcp.ReplaceState}
			// A setup server being replaced is skipped like a marked one (FR-425).
			for _, name := range setup.GetMcpServers() {
				switch {
				case slices.Contains(mcpAttach.Servers, name), slices.Contains(mcpAttach.BeingReplaced, name):
				case s.mcp.ReplaceState(name) == mcp.BeingReplaced:
					mcpAttach.BeingReplaced = append(mcpAttach.BeingReplaced, name)
				default:
					mcpAttach.Servers = append(mcpAttach.Servers, name)
				}
			}
		}
	} else if len(setup.GetMcpServers()) > 0 {
		onLog("mcp: no gateway on this host; the setup's MCP servers are skipped")
	}
	return s.mgr.Launch(ctx, sandbox.LaunchRequest{
		Config:              config,
		Sources:             setup.GetSources(),
		DisplayName:         req.DisplayName,
		KitSources:          kitSources,
		EscapeHatchCommands: ehCommands,
		Services:            services,
		Mcp:                 mcpAttach,
		EmptyWorkspace:      len(setup.GetSources()) == 0,
		Controller:          req.Controller,
		ControllerBriefing:  req.Briefing,
	}, func(duplicate.Progress) {}, onLog)
}

// --- lanes (SWI-18) ---

func (s *Server) PutLane(_ context.Context, req *pb.PutLaneRequest) (*pb.Lane, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.PutLane(req.GetOrchestratorId(), req.GetLaneId(), req.GetName(), req.GetDescription())
	return out, orchStatus(err)
}

func (s *Server) DeleteLane(_ context.Context, req *pb.DeleteLaneRequest) (*pb.DeleteLaneResponse, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	applied, err := s.orchestrator.DeleteLane(req.GetOrchestratorId(), req.GetLaneId(), req.GetGuard())
	if err != nil {
		return nil, orchStatus(err)
	}
	return &pb.DeleteLaneResponse{Applied: applied}, nil
}

func (s *Server) SetWorkerLanes(_ context.Context, req *pb.SetWorkerLanesRequest) (*pb.WorkerView, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.SetWorkerLanes(req)
	return out, orchStatus(err)
}

func (s *Server) SetLaneMembers(_ context.Context, req *pb.SetLaneMembersRequest) (*pb.Lane, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.SetLaneMembers(req)
	return out, orchStatus(err)
}

func (s *Server) CheckLaneChange(_ context.Context, req *pb.CheckLaneChangeRequest) (*pb.LaneChangeImpact, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.CheckLaneChange(req)
	return out, orchStatus(err)
}

func (s *Server) DismissDroppedWorkerTags(_ context.Context, req *pb.OrchestratorIdRequest) (*pb.Orchestrator, error) {
	if s.orchestrator == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.orchestrator.DismissDroppedWorkerTags(req.GetOrchestratorId())
	return out, orchStatus(err)
}
