package grpc

import (
	"context"
	"errors"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListSandboxServices returns every service the sandbox's attached kits declare,
// joined with its current instance state (feature 006, FR-045). Nothing is started
// as a side effect of listing.
func (s *Server) ListSandboxServices(_ context.Context, req *pb.ListSandboxServicesRequest) (*pb.ListSandboxServicesResponse, error) {
	if s.services == nil {
		return &pb.ListSandboxServicesResponse{}, nil
	}
	rows, err := s.services.List(req.GetSandboxId())
	if err != nil {
		return nil, serviceStatus(err)
	}
	return &pb.ListSandboxServicesResponse{Services: rows}, nil
}

// StartSandboxService starts one declared service by NAME (FR-045, FR-046).
//
// The name is validated against the sandbox's persisted service set, so an unknown
// name starts nothing and comes back NOT_FOUND. The call returns as soon as the
// instance exists (STARTING); readiness — and therefore the local address — arrives
// on the event stream, because a cold dev server can take a minute to listen and
// blocking the RPC on that would freeze the client.
func (s *Server) StartSandboxService(_ context.Context, req *pb.StartSandboxServiceRequest) (*pb.StartSandboxServiceResponse, error) {
	if s.services == nil {
		return nil, status.Error(codes.Unimplemented, "port forwarding not enabled")
	}
	inst, err := s.services.Start(req.GetSandboxId(), req.GetServiceName())
	if err != nil {
		return nil, serviceStatus(err)
	}
	return &pb.StartSandboxServiceResponse{Instance: inst}, nil
}

// StopSandboxService terminates a running service's whole process tree and releases
// its ports (FR-048). Idempotent on an already-stopped service.
func (s *Server) StopSandboxService(_ context.Context, req *pb.StopSandboxServiceRequest) (*pb.StopSandboxServiceResponse, error) {
	if s.services == nil {
		return nil, status.Error(codes.Unimplemented, "port forwarding not enabled")
	}
	inst, err := s.services.Stop(req.GetSandboxId(), req.GetServiceName())
	if err != nil {
		return nil, serviceStatus(err)
	}
	return &pb.StopSandboxServiceResponse{Instance: inst}, nil
}

// serviceStatus maps the supervisor's sentinels onto gRPC codes. An undeclared name
// is NOT_FOUND rather than a generic error because it is the allowlist boundary
// answering: that service does not exist for this sandbox.
func serviceStatus(err error) error {
	switch {
	case errors.Is(err, portforward.ErrUnknownSandbox):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, portforward.ErrServiceNotDeclared):
		return status.Error(codes.NotFound, err.Error())
	default:
		return err
	}
}
