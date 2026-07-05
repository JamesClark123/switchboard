package client_test

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// TestMain doubles this test binary as a stdio<->socket bridge when
// SWB_BRIDGE_SOCK is set, emulating the daemon's `dial-stdio` subcommand so the
// SSH transport can be exercised end-to-end without a real ssh/daemon.
func TestMain(m *testing.M) {
	if sock := os.Getenv("SWB_BRIDGE_SOCK"); sock != "" {
		c, err := net.Dial("unix", sock)
		if err != nil {
			os.Exit(1)
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(c, os.Stdin); close(done) }()
		_, _ = io.Copy(os.Stdout, c)
		<-done
		_ = c.Close()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startFakeOnSocket(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, &fakeServer{sandboxes: map[string]*pb.Sandbox{}})
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

func bridgeCmd(ctx context.Context, sock string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), "SWB_BRIDGE_SOCK="+sock)
	return cmd
}

func TestDialCommandTransportEndToEnd(t *testing.T) {
	sock := startFakeOnSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := client.DialCommand(ctx, bridgeCmd(ctx, sock))
	if err != nil {
		t.Fatalf("DialCommand: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.HostID() != "host-1" {
		t.Errorf("host id over stdio = %q", conn.HostID())
	}
	// A real RPC flows over the bridged stdio transport.
	if _, err := conn.ListSources(ctx, "/work", false); err != nil {
		t.Errorf("ListSources over stdio: %v", err)
	}
}

func TestDialCommandFailsWhenBridgeCannotReachSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Bridge points at a non-existent socket -> child exits -> handshake fails.
	cmd := bridgeCmd(ctx, filepath.Join(t.TempDir(), "nope.sock"))
	if _, err := client.DialCommand(ctx, cmd); err == nil {
		t.Error("expected DialCommand to fail when the bridge cannot connect")
	}
}

func TestSSHCommandArgs(t *testing.T) {
	cmd := client.SSHCommand(context.Background(), "user@build-box", []string{"-i", "/key", "-p", "2222"})
	want := []string{"ssh", "-i", "/key", "-p", "2222", "user@build-box", "sxbd", "dial-stdio"}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
}
