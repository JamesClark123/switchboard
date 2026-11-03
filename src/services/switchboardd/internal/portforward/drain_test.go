package portforward

import (
	"context"
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
	return startUnderOperationOn(t, freePort(t))
}

// startUnderOperationOn is startUnderOperation for a service on port, which the
// caller may have something listening on.
func startUnderOperationOn(t *testing.T, port uint32) (*Supervisor, *fakeRunner, *sync.RWMutex, string) {
	t.Helper()
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
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

// SWI-26: an operation that leaves a start alone but spans its readiness deadline
// doesn't cost a healthy service its start. The deadline's check waits for the lock
// rather than reading a refusal as not listening.
func TestAHealthyServiceSurvivesAnOperationAcrossItsDeadline(t *testing.T) {
	_, port := listenAllInterfaces(t)
	s, runner, rw, _ := startUnderOperationOn(t, port)
	rw.Unlock()
	waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	if runner.execed("kill -") {
		t.Error("a healthy service was signalled")
	}
	if failed := failedNotifications(s); failed != 0 {
		t.Errorf("%d SERVICE_FAILED notifications, want 0", failed)
	}
}

// SWI-26 (regression from the read/write split): a developer's stop during an
// in-sandbox launch waits for its PGID and signals the tree. Stopping before the
// marker arrives would read STOPPED with the tree still running in the sandbox.
func TestStopDuringALaunchSignalsTheGroup(t *testing.T) {
	sb := runningSandbox("sb1", listeningService("web", freePort(t), inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	runner.markerDelay = "0.6"
	s.SetReadinessTimeout(30 * time.Second)
	rwRuntimeLock(s)
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the launch", 5*time.Second, func() bool { return runner.launchCount() == 1 })
	stopped, err := s.Stop("sb1", "web")
	if err != nil {
		t.Fatalf("stop during the launch = %v", err)
	}
	if stopped.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("after the stop: %v", stopped)
	}
	if !runner.execed("kill -TERM -") {
		t.Error("the tree was never signalled")
	}
	if failed := failedNotifications(s); failed != 0 {
		t.Errorf("%d SERVICE_FAILED notifications, want 0", failed)
	}
	time.Sleep(100 * time.Millisecond)
	if inst, _ := s.insts.GetByService("sb1", "web"); inst.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("the start overwrote the stop: %v", inst)
	}
}

// SWI-26: that wait never stands in the way of a lifecycle operation. The launch
// finishes, and the stop and the operation follow it.
func TestStopDuringALaunchLetsALifecycleOperationThrough(t *testing.T) {
	sb := runningSandbox("sb1", listeningService("web", freePort(t), inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	runner.markerDelay = "0.6"
	s.SetReadinessTimeout(30 * time.Second)
	rw := rwRuntimeLock(s)
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the launch", 5*time.Second, func() bool { return runner.launchCount() == 1 })
	stopped := make(chan error, 1)
	go func() {
		_, err := s.Stop("sb1", "web")
		stopped <- err
	}()
	time.Sleep(100 * time.Millisecond) // the stop is waiting for the launch
	locked := make(chan struct{})
	go func() {
		rw.Lock()
		close(locked)
	}()
	select {
	case <-locked:
		rw.Unlock()
	case <-time.After(10 * time.Second):
		t.Fatal("the operation never got the lock")
	}
	if err := <-stopped; err != nil {
		t.Errorf("stop = %v", err)
	}
}

// bareStart records a STARTING instance with its handle, without a bringUp.
func bareStart(t *testing.T, s *Supervisor, sandboxID, name string) (string, *procHandle) {
	t.Helper()
	inst, created := s.insts.Create(sandboxID, name)
	if !created {
		t.Fatal("instance exists")
	}
	h := &procHandle{cancel: func() {}, inSbx: true, ref: sandboxID + "-ref"}
	s.mu.Lock()
	s.handles[inst.GetId()] = h
	s.mu.Unlock()
	return inst.GetId(), h
}

// SWI-26: a launch that fails while a stop waits for it keeps its failure.
func TestStopKeepsAFailureRecordedWhileItWaited(t *testing.T) {
	sb := runningSandbox("sb1", listeningService("web", freePort(t), inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)
	id, h := bareStart(t, s, "sb1", "web")
	h.launch.Lock()
	got := make(chan *pb.ServiceInstance, 1)
	go func() {
		inst, _ := s.Stop("sb1", "web")
		got <- inst
	}()
	select {
	case inst := <-got:
		t.Fatalf("the stop didn't wait for the launch: %v", inst)
	case <-time.After(100 * time.Millisecond):
	}
	s.failStarting(id, pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED, "boom")
	h.launch.Unlock()
	if inst := <-got; inst.GetState() != pb.ServiceState_SERVICE_STATE_FAILED || inst.GetFailureDetail() != "boom" {
		t.Errorf("after the stop: %v", inst)
	}
}

// SWI-26: a stop that gets in before the launch leaves nothing to launch.
func TestALaunchAfterAStopNeverRuns(t *testing.T) {
	declared := listeningService("web", freePort(t), inSandbox)
	sb := runningSandbox("sb1", declared)
	s, _, runner, _ := newTestSupervisor(t, sb)
	id, h := bareStart(t, s, "sb1", "web")
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	cancel()
	if proc, _ := s.launchExclusive(ctx, sb, declared, declared.GetCommand(), id, true, declared.GetListenPort()); proc != nil || runner.launchCount() != 0 {
		t.Errorf("launched after a stop: %d launches", runner.launchCount())
	}
}

// SWI-26: a sandbox that left RUNNING between Start's read and the runtime lock
// gets no `sbx exec`, which would start it again.
func TestStartIsRefusedIfTheSandboxStoppedSinceItsRead(t *testing.T) {
	sb := runningSandbox("sb1", listeningService("web", freePort(t), inSandbox))
	s, lookup, runner, _ := newTestSupervisor(t, sb)
	s.SetRuntimeLock(func(string) (func(), bool) {
		stopped := runningSandbox("sb1", sb.GetServices()...)
		stopped.State = pb.SandboxState_SANDBOX_STATE_STOPPED
		lookup.set(stopped)
		return func() {}, true
	})
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	inst := waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_FAILED, 5*time.Second)
	if inst.GetFailureReason() != pb.ServiceFailureReason_SERVICE_FAILURE_REASON_SANDBOX_NOT_RUNNING || runner.launchCount() != 0 {
		t.Errorf("started into a stopped sandbox: %v (%d launches)", inst, runner.launchCount())
	}
}

// shortPublishBound shortens publishAttemptTimeout for one test.
func shortPublishBound(t *testing.T, d time.Duration) {
	t.Helper()
	saved := publishAttemptTimeout
	publishAttemptTimeout = d
	t.Cleanup(func() { publishAttemptTimeout = saved })
}

// SWI-26: the bound is per attempt, so a slow attempt that fails (a port lost to
// the allocation race) doesn't use up the next one's.
func TestPublishRetriesEachGetTheirOwnBound(t *testing.T) {
	shortPublishBound(t, 300*time.Millisecond)
	s, _, runner, _ := newTestSupervisor(t, runningSandbox("sb1"))
	attempts := 0
	runner.publishHook = func(context.Context, uint32) error {
		if attempts++; attempts < 3 {
			time.Sleep(200 * time.Millisecond)
			return errors.New("port already allocated")
		}
		return nil
	}
	if _, err := s.publishSandboxPort(context.Background(), "sb1-ref", freePort(t)); err != nil {
		t.Fatalf("publish = %v", err)
	}
	if attempts != 3 {
		t.Errorf("%d attempts, want 3", attempts)
	}
}

// SWI-26: a publish its bound ended isn't retried, and its port is given back in
// case the mapping was applied before the call was killed.
func TestPublishKilledByItsBoundGivesThePortBack(t *testing.T) {
	shortPublishBound(t, 50*time.Millisecond)
	s, _, runner, _ := newTestSupervisor(t, runningSandbox("sb1"))
	listen := freePort(t)
	var tried []uint32
	runner.publishHook = func(ctx context.Context, hostPort uint32) error {
		tried = append(tried, hostPort)
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := s.publishSandboxPort(context.Background(), "sb1-ref", listen); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("publish = %v", err)
	}
	un := runner.unpublishes()
	if len(tried) != 1 || len(un) != 1 || un[0] != [2]uint32{tried[0], listen} {
		t.Errorf("tried %v, unpublished %v", tried, un)
	}
}

// SWI-26: a developer's stop that lands during the publish finds no port on the
// handle yet; the publish gives it back once it knows the handle is gone.
func TestStopDuringAPublishGetsItsPortBack(t *testing.T) {
	_, port := listenAllInterfaces(t)
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	var published uint32
	runner.publishHook = func(_ context.Context, hostPort uint32) error {
		published = hostPort
		_, err := s.Stop("sb1", "web")
		return err
	}
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the unpublish", 5*time.Second, func() bool { return len(runner.unpublishes()) == 1 })
	if un := runner.unpublishes(); un[0] != [2]uint32{published, port} {
		t.Errorf("unpublished %v, want %d:%d", un, published, port)
	}
	if inst, _ := s.insts.GetByService("sb1", "web"); inst.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("after the stop: %v", inst)
	}
}

// SWI-26 (review run-1528): a publish that lands while the stop is still waiting
// for the port records it beside the stop's read, and the port still goes back.
func TestStopOverlappingAPublishThatLands(t *testing.T) {
	ln, port := listenAllInterfaces(t)
	defer func() { _ = ln.Close() }() // held open: the stop waits on the port
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	s.SetStopGrace(500 * time.Millisecond)
	var published atomic.Uint32
	stopped := make(chan error, 1)
	runner.publishHook = func(_ context.Context, hostPort uint32) error {
		published.Store(hostPort)
		go func() {
			_, err := s.Stop("sb1", "web")
			stopped <- err
		}()
		id := instanceIDOf(t, s, "sb1", "web")
		for deadline := time.Now().Add(5 * time.Second); !s.isStopping(id) && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		return nil
	}
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the unpublish", 5*time.Second, func() bool { return len(runner.unpublishes()) == 1 })
	if un := runner.unpublishes(); un[0] != [2]uint32{published.Load(), port} {
		t.Errorf("unpublished %v, want %d:%d", un, published.Load(), port)
	}
	if inst, _ := s.insts.GetByService("sb1", "web"); inst.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("after the stop: %v", inst)
	}
}

// SWI-26 (review run-1528): the handle follows the record the launch ran against,
// so a stop signals and unpublishes on that ref, not the one Start read.
func TestHandleKeepsTheRefTheLaunchRanAgainst(t *testing.T) {
	_, port := listenAllInterfaces(t)
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
	s, lookup, runner, _ := newTestSupervisor(t, sb)
	s.SetRuntimeLock(func(string) (func(), bool) {
		moved := runningSandbox("sb1", sb.GetServices()...)
		moved.ContainerRef = "sb1-new-ref"
		lookup.set(moved)
		return func() {}, true
	})
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	id := instanceIDOf(t, s, "sb1", "web")
	waitFor(t, "the launch", 5*time.Second, func() bool { return s.procFor(id) != nil })
	s.mu.Lock()
	ref := s.handles[id].ref
	s.mu.Unlock()
	if ref != "sb1-new-ref" {
		t.Errorf("handle ref = %q", ref)
	}
	waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	if _, err := s.Stop("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	if refs := runner.unpublishRefs(); len(refs) != 1 || refs[0] != "sb1-new-ref" {
		t.Errorf("unpublished on %q", refs)
	}
}

// A developer's stop of a RUNNING service signals it, and its exit is not a crash.
func TestStoppingARunningServiceReportsNoFailure(t *testing.T) {
	_, port := listenAllInterfaces(t)
	sb := runningSandbox("sb1", listeningService("web", port, inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)
	if _, err := s.Start("sb1", "web"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "sb1", "web", pb.ServiceState_SERVICE_STATE_RUNNING, 5*time.Second)
	stopped, err := s.Stop("sb1", "web")
	if err != nil || stopped.GetState() != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Fatalf("stop = %v, %v", stopped, err)
	}
	if failed := failedNotifications(s); failed != 0 {
		t.Errorf("%d SERVICE_FAILED notifications, want 0", failed)
	}
}
