package portforward

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func (r *fakeRunner) execed(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, argv := range r.execArgv {
		if strings.Contains(strings.Join(argv, " "), sub) {
			return true
		}
	}
	return false
}

// failedNotifications counts the SERVICE_FAILED notifications s has emitted.
func failedNotifications(s *Supervisor) int {
	failed := 0
	for _, n := range s.emitter.(*fakeEmitter).notifications() {
		if n.kind == pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED {
			failed++
		}
	}
	return failed
}

// freePort is a port nothing listens on.
func freePort(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	p := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	return p
}

// SWI-17 (research R14): a sandbox stop's drain fails every still-starting
// service (may still be running), TERMs then KILLs its in-sandbox group, and
// leaves RUNNING services alone. The cancelled bringUp writes nothing more.
func TestInterruptStartingDrainsOnlyStartingServices(t *testing.T) {
	ln, port := listenAllInterfaces(t)
	defer func() { _ = ln.Close() }()
	slowPort := freePort(t)
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox), listeningService("slow", slowPort, inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	s.SetReadinessTimeout(30 * time.Second)
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	if _, err := s.Start("sb1", "slow"); err != nil {
		t.Fatal(err)
	}
	slowID := instanceIDOf(t, s, "sb1", "slow")
	var pgid int
	waitFor(t, "the slow service's pgid", 5*time.Second, func() bool {
		s.mu.Lock()
		if h := s.handles[slowID]; h != nil && h.pgid > 0 {
			pgid = h.pgid
		}
		s.mu.Unlock()
		return pgid > 0
	})
	t.Cleanup(func() { killPGID(pgid, true) })
	webProc := s.procFor(instanceIDOf(t, s, "sb1", "web"))
	t.Cleanup(func() { killPGID(webProc.pgid, true) })

	groups := s.InterruptStarting("sb1")
	if len(groups) != 1 || groups[0].PGID != pgid || groups[0].Ref != "sb1-ref" {
		t.Fatalf("groups = %+v, want the slow service's", groups)
	}
	inst, _ := s.insts.Get(slowID)
	if inst.GetState() != pb.ServiceState_SERVICE_STATE_FAILED || inst.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED ||
		inst.GetFailureDetail() != startInterruptedDetail {
		t.Errorf("interrupted = %v", inst)
	}
	if !runner.execed("kill -TERM -" + strconv.Itoa(pgid)) {
		t.Error("no TERM to the group")
	}
	s.KillGroups(groups)
	if !runner.execed("kill -KILL -" + strconv.Itoa(pgid)) {
		t.Error("no KILL to the group")
	}
	if w, _ := s.insts.GetByService("sb1", "web"); w.GetState() != pb.ServiceState_SERVICE_STATE_RUNNING {
		t.Error("a RUNNING service was touched")
	}
	// The cancelled bringUp writes nothing over the drain's outcome.
	time.Sleep(200 * time.Millisecond)
	if inst, _ := s.insts.Get(slowID); inst.GetFailureDetail() != startInterruptedDetail {
		t.Errorf("bringUp overwrote the drain: %v", inst)
	}
	if failed := failedNotifications(s); failed != 1 {
		t.Errorf("%d SERVICE_FAILED notifications, want 1", failed)
	}
	if s.InterruptStarting("sb1") != nil {
		t.Error("nothing left starting")
	}
}

// While a lifecycle operation holds the runtime lock, a developer's
// in-sandbox start fails busy keeping no port, and a stop is refused.
func TestBusyRuntimeRefusesServiceStartAndStop(t *testing.T) {
	ln, port := listenAllInterfaces(t)
	defer func() { _ = ln.Close() }()
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox), listeningService("other", freePort(t), inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	var busy atomic.Bool
	s.SetRuntimeLock(func(string) (func(), bool) {
		if busy.Load() {
			return nil, false
		}
		return func() {}, true
	})
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	t.Cleanup(func() {
		if p := s.procFor(instanceIDOf(t, s, "sb1", "web")); p != nil {
			killPGID(p.pgid, true)
		}
	})
	busy.Store(true)
	if _, err := s.Stop("sb1", "web"); !errors.Is(err, ErrSandboxBusy) {
		t.Errorf("stop while busy = %v", err)
	}
	launches := runner.launchCount()
	if _, err := s.Start("sb1", "other"); err != nil {
		t.Fatal(err)
	}
	inst := waitForState(t, s, "sb1", "other", pb.ServiceState_SERVICE_STATE_FAILED, 5*time.Second)
	if inst.GetFailureDetail() != ErrSandboxBusy.Error() || inst.GetLocalPort() != 0 || runner.launchCount() != launches {
		t.Errorf("busy start = %v (launches %d -> %d)", inst, launches, runner.launchCount())
	}
	busy.Store(false)
	if _, err := s.Stop("sb1", "web"); err != nil {
		t.Errorf("stop once free = %v", err)
	}
}

// rwRuntimeLock wires the supervisor to a real read/write lock, as
// sandbox.Manager.TryRLockRuntime does (SWI-26).
func rwRuntimeLock(s *Supervisor) *sync.RWMutex {
	var rw sync.RWMutex
	s.SetRuntimeLock(func(string) (func(), bool) {
		if !rw.TryRLock() {
			return nil, false
		}
		return rw.RUnlock, true
	})
	return &rw
}

// SWI-26 B1: a probe or reap in flight holds the runtime lock only for
// reading, so a developer's service start and stop go ahead beside it.
func TestServiceStartAndStopBesideAProbe(t *testing.T) {
	ln, port := listenAllInterfaces(t)
	defer func() { _ = ln.Close() }()
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)
	rw := rwRuntimeLock(s)
	rw.RLock() // the probe
	defer rw.RUnlock()
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	if _, err := s.Stop("sb1", "web"); err != nil {
		t.Errorf("stop beside a probe = %v", err)
	}
}

// startUnderOperation starts an in-sandbox service whose readiness never
// succeeds, and takes the runtime lock for writing (a lifecycle operation)
// right after its launch. It returns the lock and the instance id once the
// readiness window has passed.
func startUnderOperation(t *testing.T) (*Supervisor, *fakeRunner, *sync.RWMutex, string) {
	t.Helper()
	sb := runningSandbox("sb1", listeningService("web", freePort(t), inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	s.SetReadinessTimeout(200 * time.Millisecond)
	var rw sync.RWMutex
	var calls atomic.Int32
	s.SetRuntimeLock(func(string) (func(), bool) {
		if calls.Add(1) == 2 {
			rw.Lock() // the operation takes the lock right after the launch
		}
		if !rw.TryRLock() {
			return nil, false
		}
		return rw.RUnlock, true
	})
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	id := instanceIDOf(t, s, "sb1", "web")
	var pgid int
	waitFor(t, "the service's pgid", 5*time.Second, func() bool {
		s.mu.Lock()
		if h := s.handles[id]; h != nil && h.pgid > 0 {
			pgid = h.pgid
		}
		s.mu.Unlock()
		return pgid > 0
	})
	t.Cleanup(func() { killPGID(pgid, true) })
	time.Sleep(500 * time.Millisecond) // the readiness window passes
	return s, runner, &rw, id
}

// SWI-26 B4/B5: once a lifecycle operation holds the runtime lock, a start
// already past its launch runs no further `sbx exec` (readiness check, kill)
// and no `sbx ports --publish`; it stays STARTING, so the operation's drain
// settles it.
func TestStartRunsNoExecUnderALifecycleOperation(t *testing.T) {
	s, runner, rw, id := startUnderOperation(t)
	if runner.execed("/proc/net/tcp") || runner.execed("kill -") || len(runner.publishes()) != 0 {
		t.Fatalf("an exec or publish ran under the lifecycle operation: %v", runner.publishes())
	}
	if inst, _ := s.insts.Get(id); inst.GetState() != pb.ServiceState_SERVICE_STATE_STARTING {
		t.Fatalf("the start must wait for the operation, got %v", inst)
	}
	// The stop's drain, under the operation's lock.
	if groups := s.InterruptStarting("sb1"); len(groups) != 1 {
		t.Errorf("the drain finds the start: %+v", groups)
	}
	rw.Unlock()
	time.Sleep(200 * time.Millisecond)
	if inst, _ := s.insts.Get(id); inst.GetFailureDetail() != startInterruptedDetail {
		t.Errorf("the start overwrote the drain: %v", inst)
	}
	if runner.execed("/proc/net/tcp") {
		t.Error("the cancelled start ran a readiness check")
	}
}

// slowCancel stretches the gap between an instance's context being cancelled
// and whatever its canceller does next, so a bringUp woken by the cancel always
// gets to act inside it.
func slowCancel(s *Supervisor, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.handles[id]
	cancel := h.cancel
	h.cancel = func() {
		cancel()
		time.Sleep(200 * time.Millisecond)
	}
}

// SWI-26: the drain claims the instance before it cancels the attempt. A start
// waiting on the lock to clean up wakes on that cancel and must find the outcome
// decided, not record "never listened" over the drain's. Only the drain's
// failure is published.
func TestDrainClaimsTheStartBeforeCancellingIt(t *testing.T) {
	s, _, rw, id := startUnderOperation(t)
	defer rw.Unlock()
	slowCancel(s, id)
	if groups := s.InterruptStarting("sb1"); len(groups) != 1 {
		t.Errorf("the drain finds the start: %+v", groups)
	}
	if inst, _ := s.insts.Get(id); inst.GetFailureDetail() != startInterruptedDetail {
		t.Errorf("the start overwrote the drain: %v", inst)
	}
	if failed := failedNotifications(s); failed != 1 {
		t.Errorf("%d SERVICE_FAILED notifications, want 1", failed)
	}
}

// SWI-26: a teardown cancels a start waiting on the lock to clean up. The start
// reports no failure of its own on the way to the teardown's STOPPED.
func TestTeardownOfAWaitingStartReportsNoFailure(t *testing.T) {
	s, _, rw, id := startUnderOperation(t)
	defer rw.Unlock()
	s.StopAll("sb1", true)
	if inst, _ := s.insts.Get(id); inst.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("after the teardown: %v", inst)
	}
	if failed := failedNotifications(s); failed != 0 {
		t.Errorf("%d SERVICE_FAILED notifications, want 0", failed)
	}
}

// An operation that ends without touching the start (a kit attach refused
// before its recreate) leaves it to finish: the failed start's kill and
// diagnosis run once the lock is free.
func TestStartFinishesAfterAnOperationThatLeftItAlone(t *testing.T) {
	s, runner, rw, id := startUnderOperation(t)
	rw.Unlock()
	inst := waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_FAILED, 5*time.Second)
	if inst.GetId() != id || inst.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING {
		t.Errorf("failed = %v", inst)
	}
	if !runner.execed("kill -TERM") {
		t.Error("the failed start's group was never signalled")
	}
}
