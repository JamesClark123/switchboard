//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// requireRuntime skips the suite unless a real sbx + Docker runtime is present.
// The daemon E2E exercises the actual container lifecycle, so it runs only where
// that runtime exists (CI-with-Docker or a developer host).
func requireRuntime(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_TARGET") != "" && os.Getenv("E2E_TARGET") != "local" {
		t.Skipf("E2E_TARGET=%q unsupported by this harness", os.Getenv("E2E_TARGET"))
	}
	if _, err := exec.LookPath("sbx"); err != nil {
		t.Skip("sbx not on PATH; skipping daemon E2E (needs the real sandbox runtime)")
	}
	if err := exec.Command("docker", "version").Run(); err != nil {
		t.Skip("docker not available; skipping daemon E2E")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func buildDaemon(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sxbd")
	cmd := exec.Command("go", "build", "-o", bin, "./src/services/switchboardd/cmd/sxbd")
	cmd.Dir = repoRoot(t)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, b)
	}
	return bin
}

type daemonProc struct {
	cmd  *exec.Cmd
	sock string
}

func startDaemon(t *testing.T, bin, dataDir, wsDir string) *daemonProc {
	t.Helper()
	sock := fmt.Sprintf("/tmp/sbd-e2e-%d.sock", os.Getpid())
	_ = os.Remove(sock)
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(),
		"SWITCHBOARDD_SOCKET="+sock,
		"SWITCHBOARDD_WORKSPACE_ROOT="+wsDir,
		"SWITCHBOARDD_DATA_DIR="+dataDir,
		"SWITCHBOARDD_HOST_ID=e2e",
		// Isolate the PID file per data dir. It defaults to a GLOBAL
		// $XDG_RUNTIME_DIR/switchboard.pid, which would (a) collide with a real
		// daemon on the host and (b) let one leaked test daemon wedge every later
		// run with "daemon already running". Keeping it beside DataDir means the
		// re-adoption test's two daemons share it (as intended) while different
		// tests and any real daemon stay isolated.
		"SWITCHBOARDD_PID_FILE="+filepath.Join(dataDir, "switchboard.pid"),
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Guarantee the process is reaped even if this function t.Fatals before
	// returning a daemonProc (a too-tight readiness wait previously leaked
	// daemons that then blocked subsequent runs).
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	// On a restart the daemon re-adopts still-running sandboxes and introspects
	// the sbx option surface BEFORE it binds the socket (see cmd/sxbd/main.go:
	// mgr.Readopt + sbxkit.Build precede srv.Serve). Against the real runtime that
	// startup work makes several sbx/Docker calls and easily exceeds a few seconds,
	// so the wait is generous — a too-tight deadline only produced a spurious
	// "socket never appeared" while the daemon was still adopting.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return &daemonProc{cmd: cmd, sock: sock}
		}
		// Signal 0 probes liveness without delivering a signal; ESRCH means the
		// daemon died during startup, so fail fast instead of waiting the full 30s.
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatalf("daemon exited during startup: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("daemon socket never appeared within 30s")
	return nil
}

func (d *daemonProc) stop() {
	_ = d.cmd.Process.Kill()
	_, _ = d.cmd.Process.Wait()
}

func dial(t *testing.T, sock string) pb.SwitchboardClient {
	t.Helper()
	conn, err := grpc.NewClient("unix:"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return net.Dial("unix", sock) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewSwitchboardClient(conn)
}

func launch(t *testing.T, c pb.SwitchboardClient, src string) *pb.Sandbox {
	t.Helper()
	stream, err := c.LaunchSandbox(context.Background(), &pb.LaunchSandboxRequest{
		Config:                  &pb.ConfigSnapshot{Name: "e2e"},
		Sources:                 []*pb.SourceRef{{Path: src}},
		OverrideResourceWarning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if d := msg.GetDone(); d != nil {
			return d
		}
	}
	t.Fatal("no sandbox returned")
	return nil
}

// TestDaemonLifecycleE2E drives launch → stop → restart → destroy against the
// real runtime, asserting the workspace copy is retained on stop and removed on
// destroy (SC-002/011).
func TestDaemonLifecycleE2E(t *testing.T) {
	requireRuntime(t)
	bin := buildDaemon(t)
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	d := startDaemon(t, bin, filepath.Join(dir, "data"), ws)
	defer d.stop()
	c := dial(t, d.sock)
	ctx := context.Background()

	src := filepath.Join(dir, "proj")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	sb := launch(t, c, src)
	// Safety net: the test destroys the sandbox explicitly below, but a mid-test
	// failure would otherwise leak the "e2e"-named container into the next run.
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "-f", "e2e").Run() })
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("state = %v", sb.GetState())
	}

	if _, err := c.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.GetWorkspacePath()); err != nil {
		t.Errorf("stop must retain workspace copy: %v", err)
	}

	rs, err := c.RestartSandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := rs.Recv(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := c.DestroySandbox(ctx, &pb.SandboxIdRequest{SandboxId: sb.GetId()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.GetWorkspacePath()); !os.IsNotExist(err) {
		t.Errorf("destroy must delete workspace copy")
	}
}

// TestDaemonReadoptionE2E confirms a still-running sandbox is re-adopted after a
// daemon restart (FR-002a, SC-012).
func TestDaemonReadoptionE2E(t *testing.T) {
	requireRuntime(t)
	bin := buildDaemon(t)
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	data := filepath.Join(dir, "data")

	d := startDaemon(t, bin, data, ws)
	c := dial(t, d.sock)
	src := filepath.Join(dir, "proj")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644)
	sb := launch(t, c, src)
	// This test intentionally never destroys the sandbox through the daemon (it
	// asserts re-adoption of a still-running one), so guarantee host-level cleanup
	// or the "e2e"-named container leaks and collides with the next run's create.
	t.Cleanup(func() { _ = exec.Command("sbx", "rm", "-f", "e2e").Run() })

	// Restart the daemon (the container keeps running).
	d.stop()
	d2 := startDaemon(t, bin, data, ws)
	defer d2.stop()
	c2 := dial(t, d2.sock)

	list, err := c2.ListSandboxes(context.Background(), &pb.ListSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list.GetSandboxes() {
		if s.GetId() == sb.GetId() && s.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
			found = true
		}
	}
	if !found {
		t.Error("re-adopted sandbox should still be RUNNING after daemon restart")
	}
}
