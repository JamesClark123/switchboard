package client_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// TestMain doubles this test binary as a stdio<->socket bridge when
// SWB_BRIDGE_SOCK is set, emulating the daemon's `dial-stdio` subcommand so the
// Manager's ssh path can be exercised end-to-end without a real ssh/daemon.
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

// The transport itself (stdio bridge, classification, password/askpass wiring)
// is tested in libs/switchboard-dial. These tests pin this package's forwards.

func TestSSHCommandForwardsToDial(t *testing.T) {
	cmd := client.SSHCommand("user@build-box", []string{"-i", "/key", "-p", "2222"}, "")
	want := []string{
		"ssh", "-i", "/key", "-p", "2222",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"user@build-box", "sxbd", "dial-stdio",
	}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("ssh command should start a new session (Setsid)")
	}
	if cmd.Cancel != nil {
		t.Error("ssh child must not be bound to a context")
	}
}

func TestRunAskpassIfRequested(t *testing.T) {
	t.Setenv(dial.AskpassSentinelEnv, "")
	if client.RunAskpassIfRequested() {
		t.Error("should not be in askpass mode without the sentinel")
	}
	t.Setenv(dial.AskpassSentinelEnv, "1")
	if !client.RunAskpassIfRequested() {
		t.Error("should be in askpass mode when the sentinel is set")
	}
}

// A failed dial through this package's forward is still a *dial.DialError, so
// the sign-in flow can classify it.
func TestDialCommandFailureIsClassified(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'me@box: Permission denied (publickey).' >&2; exit 255")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.DialCommand(ctx, cmd)
	var de *dial.DialError
	if !errors.As(err, &de) {
		t.Fatalf("err = %T %v, want *dial.DialError", err, err)
	}
	if de.Kind != dial.DialAuthFailed {
		t.Errorf("kind = %v", de.Kind)
	}
}

func TestDialCommandSuccessWraps(t *testing.T) {
	// The transport test server lives in client_test.go (fakeServer); a bridge
	// is not needed here — a plain socket dial exercises wrap() the same way.
	conn := startFake(t)
	if conn.Conn == nil || conn.HostID() != "host-1" {
		t.Fatalf("wrapped conn = %+v", conn)
	}
}
