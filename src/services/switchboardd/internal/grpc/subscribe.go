package grpc

import (
	"context"
	"errors"
	"io"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Subscribe streams live sandbox-change + notification events. On (re)subscribe
// with replay_undelivered, the daemon first replays buffered notifications a
// disconnected client missed (FR-026b, SC-008).
func (s *Server) Subscribe(req *pb.SubscribeRequest, stream pb.Switchboard_SubscribeServer) error {
	if s.hub == nil {
		return errors.New("event hub not configured")
	}
	id, ch, replay := s.hub.Subscribe(req.GetReplayUndelivered())
	defer s.hub.Unsubscribe(id)

	for _, ev := range replay {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// AckNotification marks notifications delivered so they are not replayed
// (FR-026b).
func (s *Server) AckNotification(_ context.Context, req *pb.AckNotificationRequest) (*pb.AckNotificationResponse, error) {
	if s.hub == nil {
		return &pb.AckNotificationResponse{}, nil
	}
	return &pb.AckNotificationResponse{Acked: uint32(s.hub.Ack(req.GetNotificationIds()))}, nil
}

// PromptAgent delivers a prompt to a sandbox's agent (FR-022).
func (s *Server) PromptAgent(_ context.Context, req *pb.PromptAgentRequest) (*pb.PromptAgentResponse, error) {
	if s.agents == nil {
		return &pb.PromptAgentResponse{Accepted: false}, errors.New("agent registry not configured")
	}
	spec := s.agentSpecFor(req.GetSandboxId())
	if err := s.agents.Prompt(req.GetSandboxId(), spec, req.GetPrompt()); err != nil {
		return &pb.PromptAgentResponse{Accepted: false}, err
	}
	return &pb.PromptAgentResponse{Accepted: true}, nil
}

// AttachAgent attaches a client to a sandbox's persistent terminal session
// (feature 003). The first AgentInput selects the sandbox and declares the
// client kind + initial size. The server sends a one-shot Snapshot (recent
// output for an immediate redraw) then streams live output; the session survives
// client detach so work keeps running (FR-001..005). A second EXTERNAL attach is
// refused with FailedPrecondition (FR-014/015).
func (s *Server) AttachAgent(stream pb.Switchboard_AttachAgentServer) error {
	if s.terms == nil {
		return errors.New("terminal registry not configured")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	sandboxID := first.GetSandboxId()
	if sandboxID == "" {
		return errors.New("first AgentInput must set sandbox_id")
	}

	bc, err := s.terms.Broadcaster(sandboxID)
	if err != nil {
		return err
	}

	kind := terminal.KindInTUI
	var cols, rows uint16
	if ai := first.GetAttach(); ai != nil {
		if ai.GetKind() == pb.ClientKind_CLIENT_KIND_EXTERNAL {
			kind = terminal.KindExternal
		}
		if is := ai.GetInitialSize(); is != nil {
			cols, rows = uint16(is.GetCols()), uint16(is.GetRows())
		}
	}

	conn, err := bc.Attach(kind, cols, rows)
	if err != nil {
		if errors.Is(err, terminal.ErrExternalBusy) {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
		return err
	}
	defer conn.Close() // detach on stream end; the session keeps running

	// Send the snapshot first so a reconnecting client redraws immediately (FR-003).
	if len(conn.Snapshot) > 0 {
		if err := stream.Send(&pb.AgentOutput{Snapshot: &pb.AgentOutput_Snapshot{
			Data: conn.Snapshot,
			Rows: uint32(rows),
			Cols: uint32(cols),
		}}); err != nil {
			return err
		}
	}

	errc := make(chan error, 2)
	// Live output -> client.
	go func() {
		for chunk := range conn.Out {
			if err := stream.Send(&pb.AgentOutput{Data: chunk}); err != nil {
				errc <- err
				return
			}
		}
		errc <- io.EOF // session ended
	}()
	// Client input/resize -> session (first frame may already carry data/resize).
	go func() {
		errc <- pumpInput(stream, conn, first)
	}()

	select {
	case <-stream.Context().Done():
		return stream.Context().Err()
	case err := <-errc:
		if err == io.EOF {
			return nil
		}
		return err
	}
}

// pumpInput applies the initial message then copies subsequent client input to
// the session's shared PTY.
func pumpInput(stream pb.Switchboard_AttachAgentServer, conn *terminal.Conn, first *pb.AgentInput) error {
	apply := func(in *pb.AgentInput) error {
		if r := in.GetResize(); r != nil {
			conn.Resize(uint16(r.GetCols()), uint16(r.GetRows()))
		}
		if len(in.GetData()) > 0 {
			if _, err := conn.Write(in.GetData()); err != nil {
				return err
			}
		}
		return nil
	}
	if err := apply(first); err != nil {
		return err
	}
	for {
		in, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := apply(in); err != nil {
			return err
		}
	}
}

// agentSpecFor looks up a sandbox's agent spec from the registry (best-effort).
func (s *Server) agentSpecFor(sandboxID string) *pb.AgentSpec {
	sb, err := s.mgr.Get(sandboxID)
	if err != nil || sb.GetAgent() == nil {
		return &pb.AgentSpec{}
	}
	return sb.GetAgent().GetSpec()
}
