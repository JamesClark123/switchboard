package grpc

import (
	"errors"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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
	if s.services == nil {
		return status.Error(codes.Unimplemented, "port forwarding not enabled")
	}
	err := s.services.Relay(stream)
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
