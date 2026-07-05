package grpc

import (
	"context"
	"errors"
	"io"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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

// AttachAgent bridges a bidirectional raw-byte PTY stream so the client can
// render an inline prompt pane or "open in another terminal" (FR-022/023, R8).
// The first AgentInput selects the sandbox.
func (s *Server) AttachAgent(stream pb.Switchboard_AttachAgentServer) error {
	if s.agents == nil {
		return errors.New("agent registry not configured")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	sandboxID := first.GetSandboxId()
	if sandboxID == "" {
		return errors.New("first AgentInput must set sandbox_id")
	}
	sess, err := s.agents.Session(sandboxID, s.agentSpecFor(sandboxID))
	if err != nil {
		return err
	}

	// Pump session output to the client.
	errc := make(chan error, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := sess.Read(buf)
			if n > 0 {
				if serr := stream.Send(&pb.AgentOutput{Data: buf[:n]}); serr != nil {
					errc <- serr
					return
				}
			}
			if rerr != nil {
				errc <- rerr
				return
			}
		}
	}()

	// Apply the first message, then pump client input to the session.
	go func() {
		errc <- pumpInput(stream, sess, first)
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

// pumpInput applies the initial message then copies subsequent client input.
func pumpInput(stream pb.Switchboard_AttachAgentServer, sess interface {
	Write([]byte) (int, error)
	Resize(uint16, uint16) error
}, first *pb.AgentInput) error {
	apply := func(in *pb.AgentInput) error {
		if r := in.GetResize(); r != nil {
			_ = sess.Resize(uint16(r.GetCols()), uint16(r.GetRows()))
		}
		if len(in.GetData()) > 0 {
			if _, err := sess.Write(in.GetData()); err != nil {
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
