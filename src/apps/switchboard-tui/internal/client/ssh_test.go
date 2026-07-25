package client_test

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestDialCommandSurvivesDialContextCancel is the regression test for the
// "stdio connection already consumed (child exited?)" failure. connectHostCmd
// dials with a short-lived ctxTimeout context and cancels it (defer cancel) the
// moment the connect finishes. The stdio child (and therefore the *Conn's
// transport) MUST outlive that dial context; if it does not, the next RPC on the
// stored connection finds the transport dead and gRPC redials into the
// single-use guard. The bridge child here is built with exec.Command (no context
// binding), mirroring the fixed SSHCommand.
func TestDialCommandSurvivesDialContextCancel(t *testing.T) {
	sock := startFakeOnSocket(t)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SWB_BRIDGE_SOCK="+sock)

	dialCtx, cancel := context.WithCancel(context.Background())
	conn, err := client.DialCommand(dialCtx, cmd)
	if err != nil {
		t.Fatalf("DialCommand: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// Cancel the dial context, exactly as connectHostCmd's deferred cancel does
	// once the connect returns. The established connection must remain usable.
	cancel()

	if _, err := conn.ListSources(context.Background(), "/work", false); err != nil {
		t.Errorf("RPC after dial-context cancel: %v", err)
	}
}

// TestSSHCommandNotBoundToContext guards the fix at the source: the ssh child
// must not be created via exec.CommandContext, which would kill it when the dial
// context is cancelled. exec.Command leaves cmd.Cancel nil; exec.CommandContext
// sets it to a process-killing func.
func TestSSHCommandNotBoundToContext(t *testing.T) {
	cmd := client.SSHCommand("user@build-box", nil, "")
	if cmd.Cancel != nil {
		t.Error("ssh child must not be bound to a context (cmd.Cancel should be nil); " +
			"a context-bound child dies when the dial context is cancelled")
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
	// No password: user opts, then hardening, then key/agent-only (BatchMode).
	cmd := client.SSHCommand("user@build-box", []string{"-i", "/key", "-p", "2222"}, "")
	want := []string{
		"ssh", "-i", "/key", "-p", "2222",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"user@build-box", "sxbd", "dial-stdio",
	}
	if len(cmd.Args) != len(want) {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
	// The child must be detached from the controlling terminal so ssh cannot
	// read the TUI's tty for a prompt.
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("ssh command should start a new session (Setsid) to detach from the tty")
	}
}

func TestSSHCommandPasswordMode(t *testing.T) {
	cmd := client.SSHCommand("user@build-box", nil, "s3cr3t")
	joined := strings.Join(cmd.Args, " ")
	// Password retries are capped, but BatchMode must NOT be set (it would
	// disable password auth entirely).
	if !strings.Contains(joined, "NumberOfPasswordPrompts=1") {
		t.Errorf("password-mode args %q missing NumberOfPasswordPrompts=1", joined)
	}
	if strings.Contains(joined, "BatchMode=yes") {
		t.Error("password mode must not set BatchMode=yes")
	}
	// The auth methods MUST NOT be overridden: the user's keys/agent and
	// ~/.ssh/config must still be honored (the password is only a fallback).
	// Forcing PreferredAuthentications breaks key-authenticated hosts.
	if strings.Contains(joined, "PreferredAuthentications") {
		t.Errorf("password mode must not override PreferredAuthentications: %q", joined)
	}
	// The password is wired through SSH_ASKPASS (this binary), not the tty.
	env := envMap(cmd.Env)
	if env["SWITCHBOARD_SSH_ASKPASS"] != "1" || env["SWITCHBOARD_SSH_PASSWORD"] != "s3cr3t" {
		t.Errorf("askpass env not wired: %v", env)
	}
	if env["SSH_ASKPASS"] == "" || env["SSH_ASKPASS_REQUIRE"] != "force" {
		t.Errorf("SSH_ASKPASS not forced: %v", env)
	}
	if env["DISPLAY"] == "" {
		t.Error("DISPLAY should be set so older ssh consults SSH_ASKPASS")
	}
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}

func TestRunAskpassIfRequested(t *testing.T) {
	// Not in askpass mode by default.
	t.Setenv("SWITCHBOARD_SSH_ASKPASS", "")
	if client.RunAskpassIfRequested() {
		t.Error("should not be in askpass mode without the sentinel")
	}
	t.Setenv("SWITCHBOARD_SSH_ASKPASS", "1")
	if !client.RunAskpassIfRequested() {
		t.Error("should be in askpass mode when the sentinel is set")
	}
}
