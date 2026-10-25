package grpc

import (
	"errors"
	"fmt"
	"net"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/portforward"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ForwardPort relays one TCP connection from the developer's machine to a running
// service (feature 006, research R1).
//
// The client opens one stream per accepted connection on its local listener. This
// is what makes a remote-host sandbox work with no second SSH session: the bytes
// ride the connection the client already authenticated, whether that is a Unix
// socket or the `ssh … dial-stdio` bridge.
func (s *Server) ForwardPort(stream pb.Switchboard_ForwardPortServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if name := first.GetOpen().GetMcpAuthName(); name != "" {
		return s.relayMcpAuth(stream, first.GetOpen())
	}
	if s.services == nil {
		return status.Error(codes.Unimplemented, "port forwarding not enabled")
	}
	err = s.services.Relay(&replayStream{Switchboard_ForwardPortServer: stream, first: first})
	switch {
	case errors.Is(err, portforward.ErrRelayFirstFrame):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, portforward.ErrRelayNotRunning):
		// A stale client asking to forward a service that has since stopped. Refusing
		// here is what stops a dead address from looking like a working one (FR-050).
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return err
}

// replayStream hands an already-read first frame back to the relay.
type replayStream struct {
	pb.Switchboard_ForwardPortServer
	first *pb.PortForwardFrame
}

func (r *replayStream) Recv() (*pb.PortForwardFrame, error) {
	if f := r.first; f != nil {
		r.first = nil
		return f, nil
	}
	return r.Switchboard_ForwardPortServer.Recv()
}

// relayMcpAuth carries one browser connection to the OAuth callback listener of an
// in-flight MCP authorization on this host. Only the port that authorization
// announced is dialled, and only on loopback, so this is not a general proxy.
func (s *Server) relayMcpAuth(stream pb.Switchboard_ForwardPortServer, open *pb.PortForwardFrame_Open) error {
	if err := s.requireMcp(); err != nil {
		return err
	}
	port, ok := s.mcp.CallbackPort(open.GetMcpAuthName())
	if !ok {
		return status.Error(codes.FailedPrecondition, mcp.ErrNoCallback.Error())
	}
	if open.GetLocalPort() != port {
		return status.Error(codes.InvalidArgument, "port does not match the authorization callback")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("localhost", fmt.Sprint(port)), 2*time.Second)
	if err != nil {
		return status.Error(codes.Unavailable, "dial callback listener: "+err.Error())
	}
	defer func() { _ = conn.Close() }()
	if err := stream.Send(&pb.PortForwardFrame{
		Frame: &pb.PortForwardFrame_Opened_{Opened: &pb.PortForwardFrame_Opened{}},
	}); err != nil {
		return err
	}
	return portforward.Pipe(stream, conn)
}
