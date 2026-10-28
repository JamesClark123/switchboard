package grpc

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Worker-side RPCs (feature 009, research R3): served by EVERY daemon for the
// sandboxes it owns and called by an orchestrator's daemon over its link (or
// in-process for its own host). Filled in by user stories 2, 3 and 6; a daemon
// without the worker service configured answers UNAVAILABLE.

func (s *Server) AttachController(ctx context.Context, req *pb.AttachControllerRequest) (*pb.Sandbox, error) {
	if s.worker == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.worker.Attach(ctx, req)
	return s.withTerminalCounts(out), orchStatus(err)
}

func (s *Server) DetachController(ctx context.Context, req *pb.DetachControllerRequest) (*pb.Sandbox, error) {
	if s.worker == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.worker.Detach(ctx, req.GetSandboxId(), req.GetOrchestratorId(), req.GetForce())
	return s.withTerminalCounts(out), orchStatus(err)
}

func (s *Server) UpdateWorker(ctx context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error) {
	if s.worker == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.worker.Update(ctx, req)
	return s.withTerminalCounts(out), orchStatus(err)
}

func (s *Server) DeliverWorkerPrompt(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	if s.worker == nil {
		return nil, errNoOrchestrator
	}
	resp, err := s.worker.DeliverResponse(ctx, req)
	if err != nil {
		return nil, orchStatus(err)
	}
	return resp, nil
}

// DeliverWorkerStep types one sequence step (SWI-17, contracts/worker-step.md):
// served like DeliverWorkerPrompt, with ErrStepSent as ALREADY_EXISTS.
func (s *Server) DeliverWorkerStep(ctx context.Context, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	if s.worker == nil {
		return nil, errNoOrchestrator
	}
	out, err := s.worker.DeliverStep(ctx, req)
	if err != nil {
		return nil, orchStatus(err)
	}
	return out, nil
}

func (s *Server) OrchestratorLink(stream pb.Switchboard_OrchestratorLinkServer) error {
	if s.worker == nil {
		return errNoOrchestrator
	}
	return orchStatus(s.worker.AcceptLink(stream))
}
