package grpc

import (
	"context"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DecideEscapeHatchRun applies a developer's approve/deny decision to a pending
// escape-hatch run (feature 005, FR-039). The daemon owns the gate; this RPC is the
// client's input to it. Idempotent on an already-resolved run.
func (s *Server) DecideEscapeHatchRun(ctx context.Context, req *pb.DecideEscapeHatchRunRequest) (*pb.DecideEscapeHatchRunResponse, error) {
	if s.escapeHatch == nil {
		return nil, status.Error(codes.Unimplemented, "escape hatch not enabled")
	}
	resolved := s.escapeHatch.Decide(req.GetRunId(), req.GetApproved())
	return &pb.DecideEscapeHatchRunResponse{Status: resolved}, nil
}

// ListEscapeHatchRuns returns the escape-hatch runs for a sandbox in the current
// daemon session (feature 005, FR-042). An empty sandbox_id returns every run on
// this host. Runs are in-memory only (session = daemon uptime).
func (s *Server) ListEscapeHatchRuns(ctx context.Context, req *pb.ListEscapeHatchRunsRequest) (*pb.ListEscapeHatchRunsResponse, error) {
	if s.escapeHatch == nil {
		return &pb.ListEscapeHatchRunsResponse{}, nil
	}
	return &pb.ListEscapeHatchRunsResponse{Runs: s.escapeHatch.ListRuns(req.GetSandboxId())}, nil
}
