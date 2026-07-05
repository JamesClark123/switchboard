package grpc_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startServerWithManifest(t *testing.T, m *pb.OptionManifest) (pb.SwitchboardClient, string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	ws := filepath.Join(dir, "ws")
	mgr := sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, ws, "host-1")
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "host-1", WorkspaceRoot: ws, Manifest: m})

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
	return pb.NewSwitchboardClient(conn), dir
}

func TestGetOptionManifestRPC(t *testing.T) {
	man := &pb.OptionManifest{SbxVersion: "1.0", Options: []*pb.OptionManifest_Option{{Key: "network"}, {Key: "cpus"}}}
	client, _ := startServerWithManifest(t, man)
	got, err := client.GetOptionManifest(context.Background(), &pb.GetOptionManifestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetSbxVersion() != "1.0" || len(got.GetOptions()) != 2 {
		t.Errorf("manifest = %v", got)
	}

	// Empty-manifest server still returns a (versioned) manifest, not an error.
	c2, _ := startServer(t)
	if _, err := c2.GetOptionManifest(context.Background(), &pb.GetOptionManifestRequest{}); err != nil {
		t.Fatalf("empty manifest RPC errored: %v", err)
	}
}

func TestLaunchRejectsUnsupportedOption(t *testing.T) {
	man := &pb.OptionManifest{SbxVersion: "1.0", Options: []*pb.OptionManifest_Option{{Key: "network"}}}
	client, dir := startServerWithManifest(t, man)

	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	stream, err := client.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config: &pb.ConfigSnapshot{
			Name:       "bad",
			KitOptions: map[string]string{"network": `"host"`, "made_up_option": "1"},
		},
		Sources:                 []*pb.SourceRef{{Path: src}},
		OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := recvUntilErrOrDone(stream)
	if rerr == nil {
		t.Fatal("expected launch to fail on unsupported option")
	}
}

func recvUntilErrOrDone(stream pb.Switchboard_LaunchSandboxClient) (*pb.Sandbox, error) {
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if d := msg.GetDone(); d != nil {
			return d, nil
		}
	}
}
