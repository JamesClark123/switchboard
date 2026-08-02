package portforward

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// pidAlive reports whether a pid is still running.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(nil) == nil
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The realistic declared command is a WRAPPER whose child is the real listener.
// Killing only the launched shell would satisfy "the process was terminated" while
// leaving the listener alive and the port held — the exact failure FR-048's "and
// every process it spawned" exists to prevent.
func TestStopKillsTheWholeProcessTree(t *testing.T) {
	sb := hostSandbox(t, "sb1", &pb.KitService{
		Name: "worker", Command: "sleep 300 & echo $! > child.pid; wait", ListenPort: 61234,
		Location: onHost, ReadinessTimeoutSeconds: 1,
	})
	s, _, _, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "worker"); err != nil {
		t.Fatal(err)
	}
	// It never listens, so it lands in FAILED — but the tree still had to die.
	waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_FAILED, 6*time.Second)

	body, err := os.ReadFile(sb.GetWorkspacePath() + "/child.pid")
	if err != nil {
		t.Skipf("fixture did not record a child pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || childPID == 0 {
		t.Skipf("unparseable child pid %q", body)
	}
	waitFor(t, "the spawned child to be killed with its group", 3*time.Second, func() bool {
		return !pidAlive(childPID)
	})
}

// A process that ignores SIGTERM must still die once the grace period elapses.
func TestStopForceKillsAProcessThatIgnoresSIGTERM(t *testing.T) {
	sb := hostSandbox(t, "sb1", &pb.KitService{
		Name: "stubborn", Command: "trap '' TERM; sleep 300", ListenPort: 61235,
		Location: onHost, ReadinessTimeoutSeconds: 30,
	})
	s, _, _, _ := newTestSupervisor(t, sb)
	s.SetStopGrace(300 * time.Millisecond)

	if _, err := s.Start("sb1", "stubborn"); err != nil {
		t.Fatal(err)
	}
	// Wait until the process is actually up before stopping it.
	waitFor(t, "the process to launch", 3*time.Second, func() bool {
		return s.procFor(instanceIDOf(t, s, "sb1", "stubborn")) != nil
	})
	proc := s.procFor(instanceIDOf(t, s, "sb1", "stubborn"))

	start := time.Now()
	if _, err := s.Stop("sb1", "stubborn"); err != nil {
		t.Fatal(err)
	}
	if !proc.hasExited() {
		t.Error("a process that ignores SIGTERM must be force-killed by the time Stop returns")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("Stop took %v — it must give the process its grace period before SIGKILL", elapsed)
	}

	inst, _ := s.insts.GetByService("sb1", "stubborn")
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("state = %v, want STOPPED", inst.GetState())
	}
}

func instanceIDOf(t *testing.T, s *Supervisor, sandboxID, name string) string {
	t.Helper()
	inst, ok := s.insts.GetByService(sandboxID, name)
	if !ok {
		t.Fatalf("no instance for %s/%s", sandboxID, name)
	}
	return inst.GetId()
}

// FR-048: the port is released, and the sandbox port unpublished, on stop.
func TestStopReleasesPortsAndUnpublishes(t *testing.T) {
	ln, port := listenAllInterfaces(t)
	defer func() { _ = ln.Close() }()

	sb := runningSandbox("sb1", &pb.KitService{
		Name: "web", Command: "sleep 30", ListenPort: port, Location: inSandbox,
	})
	s, _, runner, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	running := waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 6*time.Second)
	hostPort := running.GetHostEndpointPort()

	stopped, err := s.Stop("sb1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("state = %v, want STOPPED", stopped.GetState())
	}
	if stopped.GetLocalPort() != 0 {
		t.Error("a stopped service must release its local port")
	}
	un := runner.unpublishes()
	if len(un) != 1 || un[0][0] != hostPort || un[0][1] != port {
		t.Errorf("unpublish = %v, want the exact published triple %d:%d", un, hostPort, port)
	}
}

func TestStopIsIdempotentAndValidatesNames(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "sleep 30", 3000, inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)

	// Never started: stopping is a no-op that reports STOPPED.
	inst, err := s.Stop("sb1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("state = %v, want STOPPED", inst.GetState())
	}
	if _, err := s.Stop("sb1", "nope"); err == nil {
		t.Error("an undeclared service must be rejected")
	}
	if _, err := s.Stop("nope", "web"); err == nil {
		t.Error("an unknown sandbox must be rejected")
	}
}

// US4-3: an out-of-band death becomes FAILED with output retained.
func TestUnexpectedExitBecomesFailedWithOutput(t *testing.T) {
	requirePython(t)
	free, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	// Listens (so it reaches RUNNING), prints, then dies on its own.
	cmd := "python3 -c \"import socket,sys,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0'," + itoa(free) + "));s.listen(8);print('serving',flush=True);time.sleep(1.5);print('CRASH',flush=True);sys.exit(2)\""

	sb := hostSandbox(t, "sb1", &pb.KitService{
		Name: "worker", Command: cmd, ListenPort: free, Location: onHost, ReadinessTimeoutSeconds: 8,
	})
	s, _, _, emitter := newTestSupervisor(t, sb)
	s.SetReadinessTimeout(8 * time.Second)

	if _, err := s.Start("sb1", "worker"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_RUNNING, 15*time.Second)
	failed := waitForState(t, s, "sb1", "worker", pb.ServiceState_SERVICE_STATE_FAILED, 15*time.Second)

	if failed.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_UNEXPECTEDLY {
		t.Errorf("reason = %v, want EXITED_UNEXPECTEDLY", failed.GetFailureReason())
	}
	if !strings.Contains(failed.GetOutput(), "CRASH") {
		t.Errorf("output = %q, want the last bytes before the crash retained", failed.GetOutput())
	}
	if failed.GetLocalPort() != 0 {
		t.Error("a crashed service must release its local port")
	}
	// The developer is by definition elsewhere; the crash must be announced.
	notifs := emitter.notifications()
	if len(notifs) != 1 || notifs[0].kind != pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED {
		t.Errorf("notifications = %+v, want exactly one SERVICE_FAILED", notifs)
	}
}

// FR-048 / US4-5: sandbox teardown stops everything and leaves no orphan.
func TestStopAllCascadesAcrossASandbox(t *testing.T) {
	sb := hostSandbox(t, "sb1",
		&pb.KitService{Name: "a", Command: "sleep 300", ListenPort: 62001, Location: onHost, ReadinessTimeoutSeconds: 30},
		&pb.KitService{Name: "b", Command: "sleep 300", ListenPort: 62002, Location: onHost, ReadinessTimeoutSeconds: 30},
	)
	s, _, _, _ := newTestSupervisor(t, sb)
	s.SetStopGrace(300 * time.Millisecond)

	for _, name := range []string{"a", "b"} {
		if _, err := s.Start("sb1", name); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "both processes to launch", 3*time.Second, func() bool {
		return s.procFor(instanceIDOf(t, s, "sb1", "a")) != nil &&
			s.procFor(instanceIDOf(t, s, "sb1", "b")) != nil
	})
	pidA := s.procFor(instanceIDOf(t, s, "sb1", "a")).cmd.Process.Pid
	pidB := s.procFor(instanceIDOf(t, s, "sb1", "b")).cmd.Process.Pid

	s.StopAll("sb1")

	if got := len(s.insts.ActiveBySandbox("sb1")); got != 0 {
		t.Errorf("active instances after teardown = %d, want 0", got)
	}
	waitFor(t, "no orphaned process to remain", 3*time.Second, func() bool {
		return !pidAlive(pidA) && !pidAlive(pidB)
	})
}

func TestStopAllOnASandboxWithNothingRunning(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "sleep 30", 3000, inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)
	s.StopAll("sb1") // must not panic or block
}
