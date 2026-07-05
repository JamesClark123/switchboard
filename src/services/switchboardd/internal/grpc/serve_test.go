package grpc_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func newBareManager(t *testing.T, dir string) *sandbox.Manager {
	t.Helper()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, filepath.Join(dir, "ws"), "h")
}

// TestServeOnSocketBindsAndStops exercises Server.Serve binding a real Unix
// socket, dialing it, and shutting down on context cancel (covers the socket
// bind/cleanup path, not just ServeListener).
func TestServeOnSocketBindsAndStops(t *testing.T) {
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	mgr := sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, filepath.Join(dir, "ws"), "host-1")
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "host-1", WorkspaceRoot: filepath.Join(dir, "ws")})

	sock := filepath.Join(dir, "sub", "d.sock") // parent dir created by Serve
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ctx, sock) }()

	// Wait for the socket to accept a handshake.
	var conn *grpc.ClientConn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if derr == nil {
			if _, herr := pb.NewSwitchboardClient(c).GetDaemonInfo(context.Background(), &pb.GetDaemonInfoRequest{}); herr == nil {
				conn = c
				break
			}
			_ = c.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if conn == nil {
		t.Fatal("daemon never became reachable on socket")
	}
	_ = conn.Close()

	cancel()
	select {
	case <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}

func TestServeErrorsOnBadSocketPath(t *testing.T) {
	dir := t.TempDir()
	mgr := newBareManager(t, dir)
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "h", WorkspaceRoot: dir})
	// A regular file in the socket path makes MkdirAll(parent) fail.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badSock := filepath.Join(blocker, "nested", "d.sock")
	if err := srv.Serve(context.Background(), badSock); err == nil {
		t.Fatal("expected Serve error for an uncreatable socket path")
	}
}

func TestCheckResourcesRPC(t *testing.T) {
	client, dir := startServer(t)
	if err := os.MkdirAll(filepath.Join(dir, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := client.CheckResources(context.Background(), &pb.CheckResourcesRequest{
		Sources: []*pb.SourceRef{{Path: filepath.Join(dir, "proj")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.GetAvailableBytes() == 0 {
		t.Error("expected a non-zero available-bytes report")
	}
}
