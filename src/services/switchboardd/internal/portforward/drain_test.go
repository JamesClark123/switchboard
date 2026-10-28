package portforward

import (
	"errors"
	"net"
	"strconv"
	"strings"
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
	s, _, runner, emitter := newTestSupervisor(t, sb)
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
	failed := 0
	for _, n := range emitter.notifications() {
		if n.kind == pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED {
			failed++
		}
	}
	if failed != 1 {
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
