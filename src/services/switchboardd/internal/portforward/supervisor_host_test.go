package portforward

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// hostSandbox is a sandbox whose workspace exists on this host, since an on-host
// service actually runs there.
func hostSandbox(t *testing.T, id string, services ...*pb.KitService) *pb.Sandbox {
	t.Helper()
	sb := runningSandbox(id, services...)
	sb.WorkspacePath = t.TempDir()
	return sb
}

// pyListener is a command that binds a port and holds it, standing in for a dev
// server. Python 3 is present in this toolchain; the test skips if it is not.
func pyListener(port string) string {
	return "python3 -c \"import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0'," + port + "));s.listen(8);time.sleep(30)\""
}

func requirePython(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		if _, err := os.Stat("/usr/local/bin/python3"); err != nil {
			t.Skip("python3 not available for the listening-service fixture")
		}
	}
}

// US3-1: the command runs on the supervising host, in that sandbox's workspace.
func TestOnHostServiceRunsInTheSandboxWorkspace(t *testing.T) {
	sb := hostSandbox(t, "sb1")
	marker := filepath.Join(sb.GetWorkspacePath(), "ran-here")
	sb.Services = []*pb.KitService{{
		Name: "worker", Command: "pwd > ran-here; sleep 5", ListenPort: 61999,
		Location: onHost, ReadinessTimeoutSeconds: 1,
	}}
	s, _, runner, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "worker"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_FAILED, 6*time.Second)

	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the command must run in the sandbox's workspace: %v", err)
	}
	if strings.TrimSpace(string(body)) != sb.GetWorkspacePath() {
		t.Errorf("ran in %q, want the workspace %q", strings.TrimSpace(string(body)), sb.GetWorkspacePath())
	}
	// It must NOT have gone through the sandbox runner.
	if runner.launchCount() != 0 {
		t.Error("an on-host service must not be launched via sbx exec")
	}
}

// US3-3: two sandboxes running the same {{port}} service coexist on distinct ports.
func TestTwoSandboxesRunTheSamePortTokenServiceConcurrently(t *testing.T) {
	requirePython(t)

	decl := func() *pb.KitService {
		return &pb.KitService{
			Name: "worker", Command: pyListener("{{port}}"), ListenPort: 7000,
			Location: onHost, ReadinessTimeoutSeconds: 8,
		}
	}
	sb1 := hostSandbox(t, "sb1", decl())
	sb2 := hostSandbox(t, "sb2", decl())

	s, lookup, _, _ := newTestSupervisor(t, sb1)
	lookup.set(sb2)
	s.SetReadinessTimeout(8 * time.Second)

	if _, err := s.Start("sb1", "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("sb2", "worker"); err != nil {
		t.Fatal(err)
	}
	defer func() { s.StopAll("sb1"); s.StopAll("sb2") }()

	a := waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_RUNNING, 15*time.Second)
	b := waitForState(t, s, "sb2", "worker", pb.ServiceState_SERVICE_STATE_RUNNING, 15*time.Second)

	if a.GetEffectivePort() == b.GetEffectivePort() {
		t.Fatalf("both instances bound port %d; {{port}} must give each its own", a.GetEffectivePort())
	}
	if a.GetEffectivePort() == 7000 || b.GetEffectivePort() == 7000 {
		t.Error("a {{port}} service must not use the declared port")
	}
	// Both must actually be reachable, which is the point of US3-3.
	for _, inst := range []*pb.ServiceInstance{a, b} {
		if !dialOK(inst.GetHostEndpointPort()) {
			t.Errorf("instance %s is not reachable at :%d", inst.GetId(), inst.GetHostEndpointPort())
		}
	}
}

// US3-4: without {{port}}, a second instance on an occupied port fails loudly
// rather than letting the developer talk to someone else's process.
func TestOnHostServiceWithoutPortTokenFailsWhenThePortIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	occupied := uint32(ln.Addr().(*net.TCPAddr).Port)

	sb := hostSandbox(t, "sb1", &pb.KitService{
		Name: "worker", Command: "sleep 5", ListenPort: occupied,
		Location: onHost, ReadinessTimeoutSeconds: 1,
	})
	s, _, _, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "worker"); err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_FAILED, 5*time.Second)
	if failed.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_PORT_IN_USE {
		t.Fatalf("reason = %v, want PORT_IN_USE", failed.GetFailureReason())
	}
	// The message must point at the fix, not just state the problem.
	if !strings.Contains(failed.GetFailureDetail(), PortPlaceholder) {
		t.Errorf("detail = %q, want it to suggest %s", failed.GetFailureDetail(), PortPlaceholder)
	}
}

// An on-host service is reached over its own host's loopback, so a loopback-only
// bind is fine there — the bind-address requirement only bites at the sandbox
// boundary (research R5).
func TestOnHostLoopbackBindIsReachable(t *testing.T) {
	requirePython(t)

	free, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Replace(pyListener(itoa(free)), "'0.0.0.0'", "'127.0.0.1'", 1)
	sb := hostSandbox(t, "sb1", &pb.KitService{
		Name: "worker", Command: cmd, ListenPort: free, Location: onHost, ReadinessTimeoutSeconds: 8,
	})
	s, _, _, _ := newTestSupervisor(t, sb)
	s.SetReadinessTimeout(8 * time.Second)

	if _, err := s.Start("sb1", "worker"); err != nil {
		t.Fatal(err)
	}
	defer s.StopAll("sb1")
	waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_RUNNING, 15*time.Second)
}
