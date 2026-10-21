package dial_test

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

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
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

// fakeServer answers the handshake and one list RPC; everything else is
// Unimplemented. It depends only on the generated contract.
type fakeServer struct {
	pb.UnimplementedSwitchboardServer
}

func (fakeServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{
		HostId: "host-1", Hostname: "test", DaemonVersion: "9.9.9", WorkspaceRoot: "/ws",
		SbxVersion: "0.47.0", SbxMinVersion: "0.36.0", RuntimeBaselineMet: true,
		McpGatewayAvailable: false, McpGatewayReason: "not signed in",
	}, nil
}

func (fakeServer) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	return &pb.ListSandboxesResponse{Sandboxes: []*pb.Sandbox{{Id: "sb-1"}}}, nil
}

func startFakeOnSocket(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, fakeServer{})
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

func bridgeCmd(ctx context.Context, sock string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), "SWB_BRIDGE_SOCK="+sock)
	return cmd
}

func TestDialLocalHandshakeAndAccessors(t *testing.T) {
	sock := startFakeOnSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dial.DialLocal(ctx, sock)
	if err != nil {
		t.Fatalf("DialLocal: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if conn.HostID() != "host-1" || conn.Info.GetHostname() != "test" {
		t.Errorf("handshake identity = %q / %q", conn.HostID(), conn.Info.GetHostname())
	}
	if conn.DaemonVersion() != "9.9.9" || conn.WorkspaceRoot() != "/ws" || conn.SbxVersion() != "0.47.0" {
		t.Errorf("cached accessors: %q %q %q", conn.DaemonVersion(), conn.WorkspaceRoot(), conn.SbxVersion())
	}
	if conn.SbxMinVersion() != "0.36.0" || !conn.RuntimeBaselineMet() {
		t.Errorf("baseline accessors: %q %v", conn.SbxMinVersion(), conn.RuntimeBaselineMet())
	}
	if ok, reason := conn.McpGateway(); ok || reason != "not signed in" {
		t.Errorf("McpGateway = %v %q", ok, reason)
	}
	if conn.API() == nil {
		t.Error("API() should expose the stub")
	}
	resp, err := conn.API().ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil || len(resp.GetSandboxes()) != 1 {
		t.Errorf("ListSandboxes over the socket: %v / %d", err, len(resp.GetSandboxes()))
	}
}

func TestDialLocalUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dial.DialLocal(ctx, filepath.Join(t.TempDir(), "nope.sock")); err == nil {
		t.Fatal("expected error dialing a missing socket")
	}
}

func TestDialCommandTransportEndToEnd(t *testing.T) {
	sock := startFakeOnSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := dial.DialCommand(ctx, bridgeCmd(ctx, sock))
	if err != nil {
		t.Fatalf("DialCommand: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.HostID() != "host-1" {
		t.Errorf("host id over stdio = %q", conn.HostID())
	}
	// A real RPC flows over the bridged stdio transport.
	if _, err := conn.API().ListSandboxes(ctx, &pb.ListSandboxesRequest{}); err != nil {
		t.Errorf("ListSandboxes over stdio: %v", err)
	}
}

// TestDialCommandSurvivesDialContextCancel is the regression test for the
// "stdio connection already consumed (child exited?)" failure: the stdio child
// (and therefore the *Conn's transport) MUST outlive the dial context.
func TestDialCommandSurvivesDialContextCancel(t *testing.T) {
	sock := startFakeOnSocket(t)

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "SWB_BRIDGE_SOCK="+sock)

	dialCtx, cancel := context.WithCancel(context.Background())
	conn, err := dial.DialCommand(dialCtx, cmd)
	if err != nil {
		t.Fatalf("DialCommand: %v", err)
	}
	defer func() { _ = conn.Close() }()
	cancel()

	if _, err := conn.API().ListSandboxes(context.Background(), &pb.ListSandboxesRequest{}); err != nil {
		t.Errorf("RPC after dial-context cancel: %v", err)
	}
}

// TestSSHCommandNotBoundToContext guards the fix at the source: the ssh child
// must not be created via exec.CommandContext.
func TestSSHCommandNotBoundToContext(t *testing.T) {
	cmd := dial.SSHCommand("user@build-box", nil, "")
	if cmd.Cancel != nil {
		t.Error("ssh child must not be bound to a context (cmd.Cancel should be nil)")
	}
}

func TestDialCommandFailsWhenBridgeCannotReachSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := bridgeCmd(ctx, filepath.Join(t.TempDir(), "nope.sock"))
	if _, err := dial.DialCommand(ctx, cmd); err == nil {
		t.Error("expected DialCommand to fail when the bridge cannot connect")
	}
}

func TestDialCommandStartError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := dial.DialCommand(ctx, exec.Command("/no/such/binary/switchboard-test")); err == nil {
		t.Error("expected start error for a missing binary")
	}
}

func TestSSHCommandArgs(t *testing.T) {
	// No password: user opts, then hardening, then key/agent-only (BatchMode).
	cmd := dial.SSHCommand("user@build-box", []string{"-i", "/key", "-p", "2222"}, "")
	want := []string{
		"ssh", "-i", "/key", "-p", "2222",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "BatchMode=yes",
		"user@build-box", "sxbd", "dial-stdio",
	}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", cmd.Args, want)
	}
	// The child must be detached from the controlling terminal so ssh cannot
	// read a client's tty for a prompt.
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Error("ssh command should start a new session (Setsid) to detach from the tty")
	}
	if cmd.Env != nil {
		t.Error("no password => no askpass environment")
	}
}

// SSHCommandBatch is the daemon's form: identical to the passwordless
// SSHCommand (BatchMode, no askpass), so a daemon never blocks on a prompt.
func TestSSHCommandBatch(t *testing.T) {
	a := dial.SSHCommandBatch("worker-box", []string{"-p", "22"})
	b := dial.SSHCommand("worker-box", []string{"-p", "22"}, "")
	if strings.Join(a.Args, " ") != strings.Join(b.Args, " ") {
		t.Errorf("batch args %v != passwordless args %v", a.Args, b.Args)
	}
	if !strings.Contains(strings.Join(a.Args, " "), "BatchMode=yes") || a.Env != nil {
		t.Errorf("batch command must set BatchMode and no askpass env: %v %v", a.Args, a.Env)
	}
}

func TestSSHCommandPasswordMode(t *testing.T) {
	cmd := dial.SSHCommand("user@build-box", nil, "s3cr3t")
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "NumberOfPasswordPrompts=1") {
		t.Errorf("password-mode args %q missing NumberOfPasswordPrompts=1", joined)
	}
	if strings.Contains(joined, "BatchMode=yes") {
		t.Error("password mode must not set BatchMode=yes")
	}
	if strings.Contains(joined, "PreferredAuthentications") {
		t.Errorf("password mode must not override PreferredAuthentications: %q", joined)
	}
	env := envMap(cmd.Env)
	if env[dial.AskpassSentinelEnv] != "1" || env[dial.AskpassSecretEnv] != "s3cr3t" {
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

// DialSSH and Batch against an unresolvable target fail quickly (or fail to
// start ssh at all) — either way an error, never a hang.
func TestDialSSHAndBatchErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	opts := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=1"}
	if _, err := dial.DialSSH(ctx, "switchboard.invalid", opts, ""); err == nil {
		t.Error("expected DialSSH error for an unreachable target")
	}
	if _, err := dial.Batch(ctx, "switchboard.invalid", opts); err == nil {
		t.Error("expected Batch error for an unreachable target")
	}
}

// A real failed dial comes back as a DialError carrying ssh's last words.
func TestDialCommandFailureIsClassified(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'me@box: Permission denied (publickey).' >&2; exit 255")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := dial.DialCommand(ctx, cmd)
	var de *dial.DialError
	if !errors.As(err, &de) {
		t.Fatalf("err = %T %v, want *dial.DialError", err, err)
	}
	if de.Kind != dial.DialAuthFailed || !strings.Contains(de.Stderr, "Permission denied") {
		t.Errorf("DialError = kind %v stderr %q", de.Kind, de.Stderr)
	}
	if !errors.Is(err, de.Err) {
		t.Error("DialError should unwrap to the handshake error")
	}
}
