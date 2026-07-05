package grpc_test

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// echoSession is a fake agent.Session: written bytes become readable.
type echoSession struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newEchoSession() *echoSession {
	s := &echoSession{}
	s.cond = sync.NewCond(&s.mu)
	return s
}
func (s *echoSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	s.cond.Signal()
	return len(p), nil
}
func (s *echoSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && !s.closed {
		s.cond.Wait()
	}
	if len(s.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}
func (s *echoSession) Resize(uint16, uint16) error { return nil }
func (s *echoSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.cond.Broadcast()
	return nil
}

func startServerWithAgents(t *testing.T) (pb.SwitchboardClient, *agent.Hub) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	ws := filepath.Join(dir, "ws")
	mgr := sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, ws, "host-1")
	hub := agent.NewHub("host-1")
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) {
		return newEchoSession(), nil
	})
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "host-1", WorkspaceRoot: ws, Hub: hub, Agents: agents})

	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewSwitchboardClient(conn), hub
}

func TestSubscribeLiveNotification(t *testing.T) {
	client, hub := startServerWithAgents(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Give the server a moment to register the subscriber, then emit.
	time.Sleep(100 * time.Millisecond)
	hub.EmitNotification("sb1", pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE, "done", time.Unix(1, 0))

	ev, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if ev.GetNotification().GetSandboxId() != "sb1" {
		t.Errorf("received %+v", ev)
	}
}

func TestSubscribeReplayUndeliveredAndAck(t *testing.T) {
	client, hub := startServerWithAgents(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Emitted while no client is connected -> buffered.
	ev := hub.EmitNotification("sb9", pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING, "prompt me", time.Unix(1, 0))

	stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{ReplayUndelivered: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.GetNotification().GetId() != ev.GetId() {
		t.Fatalf("expected replayed notification %s, got %+v", ev.GetId(), got)
	}

	// Ack it; a fresh subscribe replays nothing.
	ack, err := client.AckNotification(ctx, &pb.AckNotificationRequest{NotificationIds: []string{ev.GetId()}})
	if err != nil {
		t.Fatal(err)
	}
	if ack.GetAcked() != 1 {
		t.Errorf("acked = %d, want 1", ack.GetAcked())
	}
	if len(hub.Undelivered()) != 0 {
		t.Error("expected no undelivered after ack")
	}
}

func TestPromptAndAttachAgent(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Prompt is accepted.
	resp, err := client.PromptAgent(ctx, &pb.PromptAgentRequest{SandboxId: "sb1", Prompt: "hi there"})
	if err != nil || !resp.GetAccepted() {
		t.Fatalf("PromptAgent: %v / accepted=%v", err, resp.GetAccepted())
	}

	// Attach: send raw bytes, receive the echo back over the bidi stream.
	stream, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.AgentInput{SandboxId: "sb1", Data: []byte("echo me")}); err != nil {
		t.Fatal(err)
	}
	out, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	// The session is shared with the prompt above, so output may include the
	// earlier prompt; assert our bytes are present.
	if !containsBytes(out.GetData(), "hi there") && !containsBytes(out.GetData(), "echo me") {
		t.Errorf("attach output = %q", string(out.GetData()))
	}
}

func TestAttachRequiresSandboxID(t *testing.T) {
	client, _ := startServerWithAgents(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.AgentInput{Data: []byte("no id")}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Error("expected error when first AgentInput lacks sandbox_id")
	}
}

func TestAgentRPCsWithoutHubOrRegistry(t *testing.T) {
	// startServer wires no hub/agents -> the US4 RPCs degrade with errors.
	client, _ := startServer(t)
	ctx := context.Background()

	stream, err := client.Subscribe(ctx, &pb.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Error("Subscribe should error without a hub")
	}
	if _, err := client.PromptAgent(ctx, &pb.PromptAgentRequest{SandboxId: "x"}); err == nil {
		t.Error("PromptAgent should error without an agent registry")
	}
	att, err := client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := att.Send(&pb.AgentInput{SandboxId: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := att.Recv(); err == nil {
		t.Error("AttachAgent should error without an agent registry")
	}
	// Ack without a hub is a no-op success.
	if resp, err := client.AckNotification(ctx, &pb.AckNotificationRequest{NotificationIds: []string{"a"}}); err != nil || resp.GetAcked() != 0 {
		t.Errorf("Ack without hub: %v / %d", err, resp.GetAcked())
	}
}

func containsBytes(b []byte, sub string) bool {
	return len(b) >= len(sub) && indexBytes(b, sub) >= 0
}
func indexBytes(b []byte, sub string) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == sub {
			return i
		}
	}
	return -1
}
