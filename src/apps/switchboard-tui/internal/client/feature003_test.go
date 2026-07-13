package client_test

import (
	"context"
	"testing"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// tagServer echoes SetSandboxTag and ResolveWorkspace, and implements AttachAgent
// with a snapshot-then-echo session.
type tagServer struct{ bareServer }

func (tagServer) SetSandboxTag(_ context.Context, req *pb.SetSandboxTagRequest) (*pb.Sandbox, error) {
	return &pb.Sandbox{Id: req.GetSandboxId(), Tag: req.GetTag()}, nil
}

func (tagServer) ResolveWorkspace(_ context.Context, req *pb.ResolveWorkspaceRequest) (*pb.ResolveWorkspaceResponse, error) {
	if req.GetPath() == "/nope" {
		return &pb.ResolveWorkspaceResponse{Found: false}, nil
	}
	return &pb.ResolveWorkspaceResponse{Found: true, SandboxId: "sb-resolved", State: pb.SandboxState_SANDBOX_STATE_RUNNING}, nil
}

func (tagServer) AttachAgent(stream pb.Switchboard_AttachAgentServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	// Snapshot first.
	if err := stream.Send(&pb.AgentOutput{Snapshot: &pb.AgentOutput_Snapshot{Data: []byte("SNAP:" + first.GetSandboxId())}}); err != nil {
		return err
	}
	for {
		in, err := stream.Recv()
		if err != nil {
			return err
		}
		if d := in.GetData(); len(d) > 0 {
			if err := stream.Send(&pb.AgentOutput{Data: d}); err != nil {
				return err
			}
		}
	}
}

func TestConnSetTag(t *testing.T) {
	conn := dialServer(t, tagServer{})
	sb, err := conn.SetTag(context.Background(), "sb1", "billing")
	if err != nil {
		t.Fatal(err)
	}
	if sb.GetTag() != "billing" || sb.GetId() != "sb1" {
		t.Fatalf("SetTag returned %+v", sb)
	}
}

func TestConnResolveWorkspace(t *testing.T) {
	conn := dialServer(t, tagServer{})
	res, err := conn.ResolveWorkspace(context.Background(), "/work/proj")
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetFound() || res.GetSandboxId() != "sb-resolved" {
		t.Fatalf("resolve = %+v", res)
	}
	miss, err := conn.ResolveWorkspace(context.Background(), "/nope")
	if err != nil {
		t.Fatal(err)
	}
	if miss.GetFound() {
		t.Fatal("expected not-found for /nope")
	}
}

// sinkBuf collects the snapshot + echoed bytes.
type sinkBuf struct{ data []byte }

func (s *sinkBuf) Write(p []byte) (int, error) { s.data = append(s.data, p...); return len(p), nil }

func TestConnAttachTerminal(t *testing.T) {
	conn := dialServer(t, tagServer{})
	sink := &sinkBuf{}
	sess, err := conn.AttachTerminal(context.Background(), "sb1", client.AttachInTUI, 80, 24, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// The snapshot was drained into the sink before AttachTerminal returned.
	if string(sink.data) == "" || !contains(sink.data, "SNAP:sb1") {
		t.Fatalf("sink after attach = %q, want the snapshot", string(sink.data))
	}
	// Sending data echoes back into the sink.
	if err := sess.SendData([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := sess.SendResize(100, 30); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return contains(sink.data, "ping") }, "echoed data should reach the sink")
}

func contains(b []byte, sub string) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return true
		}
	}
	return false
}
