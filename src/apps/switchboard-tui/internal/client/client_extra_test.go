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

// bareServer answers only GetDaemonInfo; every other RPC returns Unimplemented,
// exercising the client's error branches.
type bareServer struct {
	pb.UnimplementedSwitchboardServer
}

func (bareServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: "h"}, nil
}

// blockServer's LaunchSandbox returns a low-resource block instead of done.
type blockServer struct{ bareServer }

func (blockServer) LaunchSandbox(_ *pb.LaunchSandboxRequest, stream pb.Switchboard_LaunchSandboxServer) error {
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Blocked{Blocked: &pb.ResourceReport{Warnings: []string{"low"}}}})
}

func dialServer(t *testing.T, srv pb.SwitchboardServer) *client.Conn {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialLocal(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestClientErrorBranches(t *testing.T) {
	conn := dialServer(t, bareServer{})
	if conn.API() == nil {
		t.Error("API() should expose the stub")
	}
	ctx := context.Background()
	if _, err := conn.List(ctx); err == nil {
		t.Error("List should error")
	}
	if _, err := conn.OptionManifest(ctx); err == nil {
		t.Error("OptionManifest should error")
	}
	if err := conn.PromptAgent(ctx, "x", "p"); err == nil {
		t.Error("PromptAgent should error")
	}
	if err := conn.AckNotifications(ctx, []string{"a"}); err == nil {
		t.Error("AckNotifications should error")
	}
	if _, err := conn.VSCodeTarget(ctx, "x"); err == nil {
		t.Error("VSCodeTarget should error")
	}
	// Subscribe opens the stream lazily; the error surfaces on first Recv.
	if stream, err := conn.Subscribe(ctx, false); err == nil {
		if _, rerr := stream.Recv(); rerr == nil {
			t.Error("Subscribe Recv should error against the bare server")
		}
	}
	if _, err := conn.ListSources(ctx, "/x", false); err == nil {
		t.Error("ListSources should error")
	}
	if _, _, err := conn.Launch(ctx, &pb.LaunchSandboxRequest{}, nil); err == nil {
		t.Error("Launch should error")
	}
	if _, err := conn.Stop(ctx, "x"); err == nil {
		t.Error("Stop should error")
	}
	if _, err := conn.Restart(ctx, "x"); err == nil {
		t.Error("Restart should error")
	}
	if _, err := conn.Destroy(ctx, "x"); err == nil {
		t.Error("Destroy should error")
	}
	if _, err := conn.Rename(ctx, "x", "y"); err == nil {
		t.Error("Rename should error")
	}
}

func TestClientLaunchBlocked(t *testing.T) {
	conn := dialServer(t, blockServer{})
	sb, blocked, err := conn.Launch(context.Background(), &pb.LaunchSandboxRequest{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sb != nil || blocked == nil {
		t.Fatalf("expected a blocked report, got sb=%v blocked=%v", sb, blocked)
	}
	if len(blocked.GetWarnings()) == 0 {
		t.Error("expected block warnings")
	}
}
