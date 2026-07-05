package client_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// fakeServer is a minimal in-test daemon implementing just the RPCs the client
// transport exercises. It depends only on the generated contract (no daemon
// internals, which live in another module and are not importable here).
type fakeServer struct {
	pb.UnimplementedSwitchboardServer
	sandboxes map[string]*pb.Sandbox
}

func (s *fakeServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: "host-1", Hostname: "test", WorkspaceRoot: "/ws"}, nil
}

func (s *fakeServer) GetOptionManifest(context.Context, *pb.GetOptionManifestRequest) (*pb.OptionManifest, error) {
	return &pb.OptionManifest{SbxVersion: "1.0", Options: []*pb.OptionManifest_Option{{Key: "network"}}}, nil
}

func (s *fakeServer) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	var out []*pb.Sandbox
	for _, sb := range s.sandboxes {
		out = append(out, sb)
	}
	return &pb.ListSandboxesResponse{Sandboxes: out}, nil
}

func (s *fakeServer) ListSourceCandidates(_ context.Context, req *pb.ListSourceCandidatesRequest) (*pb.ListSourceCandidatesResponse, error) {
	return &pb.ListSourceCandidatesResponse{Candidates: []*pb.SourceRef{{Path: filepath.Join(req.GetRoot(), "proj"), IsRepo: true}}}, nil
}

func (s *fakeServer) LaunchSandbox(req *pb.LaunchSandboxRequest, stream pb.Switchboard_LaunchSandboxServer) error {
	if err := stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Copy{Copy: &pb.LaunchProgress_CopyProgress{BytesCopied: 5, BytesTotal: 10, CurrentPath: "proj/x"}}}); err != nil {
		return err
	}
	sb := &pb.Sandbox{Id: "id-1", DisplayName: req.GetConfig().GetName(), State: pb.SandboxState_SANDBOX_STATE_RUNNING, Sources: req.GetSources()}
	s.sandboxes[sb.Id] = sb
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

func (s *fakeServer) StopSandbox(_ context.Context, req *pb.SandboxIdRequest) (*pb.Sandbox, error) {
	sb := s.sandboxes[req.GetSandboxId()]
	sb.State = pb.SandboxState_SANDBOX_STATE_STOPPED
	return sb, nil
}

func (s *fakeServer) RestartSandbox(req *pb.SandboxIdRequest, stream pb.Switchboard_RestartSandboxServer) error {
	sb := s.sandboxes[req.GetSandboxId()]
	sb.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: sb}})
}

func (s *fakeServer) DestroySandbox(_ context.Context, req *pb.SandboxIdRequest) (*pb.DestroyResponse, error) {
	delete(s.sandboxes, req.GetSandboxId())
	return &pb.DestroyResponse{DeletedWorkspace: true}, nil
}

func (s *fakeServer) RenameSandbox(_ context.Context, req *pb.RenameSandboxRequest) (*pb.Sandbox, error) {
	sb := s.sandboxes[req.GetSandboxId()]
	sb.DisplayName = req.GetDisplayName()
	return sb, nil
}

func (s *fakeServer) Subscribe(_ *pb.SubscribeRequest, stream pb.Switchboard_SubscribeServer) error {
	return stream.Send(&pb.Event{Event: &pb.Event_Notification{Notification: &pb.NotificationEvent{Id: "n1", SandboxId: "id-1"}}})
}
func (s *fakeServer) PromptAgent(_ context.Context, req *pb.PromptAgentRequest) (*pb.PromptAgentResponse, error) {
	return &pb.PromptAgentResponse{Accepted: req.GetPrompt() != ""}, nil
}
func (s *fakeServer) AckNotification(_ context.Context, req *pb.AckNotificationRequest) (*pb.AckNotificationResponse, error) {
	return &pb.AckNotificationResponse{Acked: uint32(len(req.GetNotificationIds()))}, nil
}
func (s *fakeServer) GetVSCodeTarget(_ context.Context, req *pb.SandboxIdRequest) (*pb.VSCodeTarget, error) {
	return &pb.VSCodeTarget{ContainerName: "/" + req.GetSandboxId(), WorkspacePath: "/workspace"}, nil
}

func startFake(t *testing.T) *client.Conn {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterSwitchboardServer(srv, &fakeServer{sandboxes: map[string]*pb.Sandbox{}})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialLocal(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestClientLifecycleOverSocket(t *testing.T) {
	conn := startFake(t)
	if conn.HostID() != "host-1" {
		t.Fatalf("host id = %q", conn.HostID())
	}
	ctx := context.Background()

	cands, err := conn.ListSources(ctx, "/work", false)
	if err != nil || len(cands) == 0 {
		t.Fatalf("ListSources: %v / %d", err, len(cands))
	}

	man, err := conn.OptionManifest(ctx)
	if err != nil || len(man.GetOptions()) == 0 {
		t.Fatalf("OptionManifest: %v / %d options", err, len(man.GetOptions()))
	}

	// US4: subscribe stream, prompt, ack.
	stream, err := conn.Subscribe(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := stream.Recv(); err != nil || ev.GetNotification().GetId() != "n1" {
		t.Fatalf("Subscribe Recv: %v / %+v", err, ev)
	}
	if err := conn.PromptAgent(ctx, "id-1", "do it"); err != nil {
		t.Errorf("PromptAgent: %v", err)
	}
	if err := conn.PromptAgent(ctx, "id-1", ""); err == nil {
		t.Error("empty prompt should be rejected by the agent")
	}
	if err := conn.AckNotifications(ctx, []string{"n1"}); err != nil {
		t.Errorf("AckNotifications: %v", err)
	}
	if tgt, err := conn.VSCodeTarget(ctx, "id-1"); err != nil || tgt.GetContainerName() != "/id-1" {
		t.Errorf("VSCodeTarget: %v / %q", err, tgt.GetContainerName())
	}

	var sawCopy bool
	sb, blocked, err := conn.Launch(ctx, &pb.LaunchSandboxRequest{
		Config:  &pb.ConfigSnapshot{Name: "feature-work"},
		Sources: []*pb.SourceRef{{Path: "/work/proj"}},
	}, func(u client.LaunchUpdate) {
		if u.Copy != nil {
			sawCopy = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatal("unexpected block")
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || !sawCopy {
		t.Fatalf("launch result: state=%v sawCopy=%v", sb.GetState(), sawCopy)
	}

	list, err := conn.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v / %d", err, len(list))
	}

	if _, err := conn.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}
	if r, err := conn.Restart(ctx, sb.GetId()); err != nil || r.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("Restart: %v", err)
	}
	if rn, err := conn.Rename(ctx, sb.GetId(), "renamed"); err != nil || rn.GetDisplayName() != "renamed" {
		t.Fatalf("Rename: %v", err)
	}
	if deleted, err := conn.Destroy(ctx, sb.GetId()); err != nil || !deleted {
		t.Fatalf("Destroy: %v / %v", err, deleted)
	}
}

func TestDialLocalUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.DialLocal(ctx, filepath.Join(t.TempDir(), "nope.sock")); err == nil {
		t.Fatal("expected error dialing a missing socket")
	}
}
