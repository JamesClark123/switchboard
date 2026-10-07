package grpc

import (
	"context"
	"errors"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MCP gateway RPCs (feature 008, FR-072..FR-100). Every handler first checks that
// the gateway can be managed on this host (FR-079); the manager does the work and
// its sentinel errors map to status codes here. Host diagnostics travel verbatim
// (FR-078).

// requireMcp returns FAILED_PRECONDITION with the probed reason when the gateway
// is unavailable here.
func (s *Server) requireMcp() error {
	if s.mcp == nil {
		return status.Error(codes.FailedPrecondition, "MCP gateway manager is not configured on this daemon")
	}
	if !s.mcpAvail.Available {
		reason := s.mcpAvail.Reason
		if reason == "" {
			reason = "the MCP gateway cannot be managed on this host"
		}
		return status.Error(codes.FailedPrecondition, reason)
	}
	return nil
}

// mcpStatus maps the manager's sentinel errors to gRPC codes; anything else is
// the host's own diagnostic, passed through verbatim.
func mcpStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mcp.ErrNotRegistered):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, mcp.ErrInvalidArgs):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, mcp.ErrLocalNotAcknowledged), errors.Is(err, mcp.ErrAuthInProgress):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Unknown, err.Error())
}

func (s *Server) ListMcpServers(ctx context.Context, _ *pb.ListMcpServersRequest) (*pb.ListMcpServersResponse, error) {
	if err := s.requireMcp(); err != nil {
		return nil, err
	}
	servers, settings, err := s.mcp.List(ctx)
	if err != nil {
		return nil, mcpStatus(err)
	}
	return &pb.ListMcpServersResponse{Servers: servers, Settings: settings}, nil
}

func (s *Server) AddMcpServer(ctx context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	if err := s.requireMcp(); err != nil {
		return nil, err
	}
	out, err := s.mcp.Add(ctx, req)
	return out, mcpStatus(err)
}

func (s *Server) RemoveMcpServer(ctx context.Context, req *pb.RemoveMcpServerRequest) (*pb.RemoveMcpServerResponse, error) {
	if err := s.requireMcp(); err != nil {
		return nil, err
	}
	notes, err := s.mcp.Remove(ctx, req.GetName())
	if err != nil {
		return nil, mcpStatus(err)
	}
	return &pb.RemoveMcpServerResponse{Notes: notes}, nil
}

// AuthorizeMcpServer streams the authorization URL, progress and the terminal
// frame; the client cancelling the stream cancels the daemon-side wait (FR-075).
func (s *Server) AuthorizeMcpServer(req *pb.AuthorizeMcpServerRequest, stream pb.Switchboard_AuthorizeMcpServerServer) error {
	if err := s.requireMcp(); err != nil {
		return err
	}
	err := s.mcp.Authorize(stream.Context(), req.GetName(), func(f *pb.McpAuthProgress) {
		_ = stream.Send(f)
	})
	return mcpStatus(err)
}

func (s *Server) SetMcpServerDefault(ctx context.Context, req *pb.SetMcpServerDefaultRequest) (*pb.McpServer, error) {
	if err := s.requireMcp(); err != nil {
		return nil, err
	}
	out, err := s.mcp.SetDefault(ctx, req.GetName(), req.GetAttachByDefault())
	return out, mcpStatus(err)
}

func (s *Server) SetMcpAttachMode(ctx context.Context, req *pb.SetMcpAttachModeRequest) (*pb.McpGatewaySettings, error) {
	if err := s.requireMcp(); err != nil {
		return nil, err
	}
	out, err := s.mcp.SetMode(ctx, req.GetMode())
	return out, mcpStatus(err)
}
