package portforward

import (
	"net"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// waitForState polls until the instance reaches want, or the deadline passes.
func waitForState(t *testing.T, s *Supervisor, sandboxID, name string, want pb.ServiceState, within time.Duration) *pb.ServiceInstance {
	t.Helper()
	deadline := time.Now().Add(within)
	var last pb.ServiceState
	for time.Now().Before(deadline) {
		if inst, ok := s.insts.GetByService(sandboxID, name); ok {
			last = inst.GetState()
			if last == want {
				return inst
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service %q never reached %v (last %v)", name, want, last)
	return nil
}

// listeningService is a command that actually binds the port it is given, so the
// readiness dial can succeed the way a real dev server's would.
func listeningService(name string, port uint32, loc pb.ServiceLocation) *pb.KitService {
	// nc is not guaranteed present; a tiny shell loop over /dev/tcp is not portable
	// either. Go's own toolchain is, so the fixture listens via a here-doc'd program
	// only when needed — for the common case a plain `sleep` plus an external
	// listener started by the test is enough. See startWithListener.
	return &pb.KitService{Name: name, Command: "sleep 30", ListenPort: port, Location: loc}
}

// listenAllInterfaces binds 0.0.0.0 so the host's /proc/net/tcp shows a wildcard
// LISTEN row — which is what the in-sandbox readiness probe reads, since the fake
// Runner executes "in-sandbox" commands on the host.
func listenAllInterfaces(t *testing.T) (net.Listener, uint32) {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, uint32(ln.Addr().(*net.TCPAddr).Port)
}

// US2: the full happy path — STARTING, then RUNNING only once the address works.
func TestStartReachesRunningOnlyWhenTheAddressWorks(t *testing.T) {
	// Something IS listening on all interfaces, which is exactly what readiness
	// requires: a loopback-only bind is unreachable from outside the sandbox.
	ln, port := listenAllInterfaces(t)
	defer func() { _ = ln.Close() }()

	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
	s, _, _, emitter := newTestSupervisor(t, sb)

	inst, err := s.Start("sb1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_STARTING {
		t.Errorf("Start must return STARTING, got %v", inst.GetState())
	}

	running := waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	if running.GetHostEndpointPort() == 0 {
		t.Error("a RUNNING instance must carry the endpoint the relay dials")
	}
	if running.GetStartedAt() == nil {
		t.Error("a started instance must be stamped")
	}
	// Start → running is a developer action; it must not raise a notification.
	for _, n := range emitter.notifications() {
		t.Errorf("unexpected notification on a successful start: %+v", n)
	}
	_, _ = s.Stop("sb1", "web")
}

// FR-046 / US2-6: no port may be allocated when the sandbox is not running.
func TestStartRefusedWhenSandboxNotRunning(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "pnpm dev", 3000, inSandbox))
	sb.State = pb.SandboxState_SANDBOX_STATE_STOPPED
	s, _, runner, _ := newTestSupervisor(t, sb)

	inst, err := s.Start("sb1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_FAILED {
		t.Fatalf("state = %v, want FAILED", inst.GetState())
	}
	if inst.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_SANDBOX_NOT_RUNNING {
		t.Errorf("reason = %v, want SANDBOX_NOT_RUNNING", inst.GetFailureReason())
	}
	if len(runner.publishes()) != 0 {
		t.Error("no port may be published when the start is refused")
	}
	if inst.GetHostEndpointPort() != 0 || inst.GetLocalPort() != 0 {
		t.Error("no port may be allocated when the start is refused")
	}
}

func TestStartRejectsUnknownSandboxAndUndeclaredService(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "pnpm dev", 3000, inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("nope", "web"); err == nil {
		t.Error("an unknown sandbox must be rejected")
	}
	// The allowlist boundary: a name that is not declared starts nothing.
	if _, err := s.Start("sb1", "rm-rf-everything"); err == nil {
		t.Error("an undeclared service name must be rejected")
	}
	if len(s.insts.ListBySandbox("sb1")) != 0 {
		t.Error("a rejected start must not create an instance")
	}
}

// US2: a command that exits immediately is reported, not silently listed as stopped.
func TestStartFailsWhenTheCommandExitsEarly(t *testing.T) {
	ln, port := listenLoopback(t)
	_ = ln.Close() // nothing listening

	sb := runningSandbox("sb1", &pb.KitService{
		Name: "web", Command: "echo boom >&2; exit 1", ListenPort: port, Location: inSandbox,
	})
	s, _, _, emitter := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_FAILED, 5*time.Second)
	if failed.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_EARLY {
		t.Errorf("reason = %v, want EXITED_EARLY", failed.GetFailureReason())
	}
	if failed.GetFailureDetail() == "" {
		t.Error("a failure must carry a reason the developer can act on (FR-051)")
	}
	if len(emitter.notifications()) != 1 {
		t.Errorf("a failure must raise exactly one notification, got %d", len(emitter.notifications()))
	}
}

// SC-007: a process that runs but never listens is FAILED, never RUNNING.
func TestStartFailsWhenNothingEverListens(t *testing.T) {
	ln, port := listenLoopback(t)
	_ = ln.Close()

	sb := runningSandbox("sb1", &pb.KitService{
		Name: "web", Command: "sleep 30", ListenPort: port, Location: inSandbox,
		ReadinessTimeoutSeconds: 1,
	})
	s, _, _, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_FAILED, 6*time.Second)
	switch failed.GetFailureReason() {
	case pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING_LOOPBACK:
		// Either is a correct diagnosis; which one depends on what /proc says.
	default:
		t.Errorf("reason = %v, want a not-listening diagnosis", failed.GetFailureReason())
	}
}

// FR-048 / US4-6: starting a live service returns the same instance, no second run.
func TestStartIsIdempotent(t *testing.T) {
	sb := runningSandbox("sb1", &pb.KitService{
		Name: "web", Command: "sleep 30", ListenPort: 3000, Location: inSandbox, ReadinessTimeoutSeconds: 1,
	})
	s, _, runner, _ := newTestSupervisor(t, sb)

	first, err := s.Start("sb1", "web")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Start("sb1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if second.GetId() != first.GetId() {
		t.Errorf("a second start must return the same instance, got %q vs %q", second.GetId(), first.GetId())
	}
	// Give the first attempt a moment; there must still be only one launch.
	time.Sleep(200 * time.Millisecond)
	if launches := runner.launchCount(); launches != 1 {
		t.Errorf("idempotent start launched %d processes, want 1", launches)
	}
	_, _ = s.Stop("sb1", "web")
}

func TestListReflectsAStartedService(t *testing.T) {
	sb := runningSandbox("sb1", &pb.KitService{
		Name: "web", Command: "sleep 30", ListenPort: 3000, Location: inSandbox, ReadinessTimeoutSeconds: 1,
	})
	s, _, _, _ := newTestSupervisor(t, sb)

	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List("sb1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].GetInstance() == nil {
		t.Fatalf("the started service must join onto its declaration: %+v", rows)
	}
	_, _ = s.Stop("sb1", "web")
}
