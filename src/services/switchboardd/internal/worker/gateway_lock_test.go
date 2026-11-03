package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// errTestBusy is the fake runtime lock's refusal (SWI-25 A5: the lock names
// the operation holding it).
var errTestBusy = errors.New("sandbox is stopping; try again")

// SWI-26 B4: the gateway's `sbx mcp load` takes the sandbox's runtime read
// lock. While a lifecycle operation holds it the attach still registers, and
// the load waits it out in the background; one the operation left stopped
// is left to its next bring-up.
func TestGatewayLoadWaitsOutALifecycleOperation(t *testing.T) {
	e := newEnv(t, `
  "mcp add "*) echo "MCP server registered" ;;
  "mcp load "*) echo "loaded (live)" ;;`)
	var busy atomic.Bool
	busy.Store(true)
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), error) {
		if busy.Load() {
			return nil, nil, errTestBusy
		}
		return ctx, func() {}, nil
	})
	sb := e.sandbox(t, "77777777-aaaa", "busy-one", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * loadRetryStep)
	log := readLog(t, e.log)
	if len(log) != 1 || !strings.HasPrefix(log[0], "mcp add sxb-orch-77777777 ") {
		t.Fatalf("registered but not loaded while busy: %q", log)
	}
	busy.Store(false)
	deadline := time.Now().Add(3 * time.Second)
	for len(readLog(t, e.log)) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if log = readLog(t, e.log); len(log) != 2 || log[1] != "mcp load sxb-orch-77777777 --sandbox busy-one" {
		t.Errorf("the load once the operation ends: %q", log)
	}

	// The operation leaves the sandbox stopped: no load.
	busy.Store(true)
	sb2 := e.sandbox(t, "88888888-bbbb", "stops", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb2.GetId(), Controller: controller("orch-1", "b")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reg.Update(sb2.GetId(), func(s *pb.Sandbox) error {
		s.State = pb.SandboxState_SANDBOX_STATE_STOPPED
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	busy.Store(false)
	time.Sleep(5 * loadRetryStep)
	for _, l := range readLog(t, e.log) {
		if strings.HasPrefix(l, "mcp load sxb-orch-88888888") {
			t.Errorf("loaded into a stopped sandbox: %q", l)
		}
	}
}

// setState writes the sandbox's record state.
func (e *env) setState(t *testing.T, id string, state pb.SandboxState) *pb.Sandbox {
	t.Helper()
	sb, err := e.reg.Update(id, func(s *pb.Sandbox) error {
		s.State = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb
}

// busyLock wires a runtime lock that is refused while busy is set.
func (e *env) busyLock() *atomic.Bool {
	var busy atomic.Bool
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), error) {
		if busy.Load() {
			return nil, nil, errTestBusy
		}
		return ctx, func() {}, nil
	})
	return &busy
}

func (e *env) loads(t *testing.T) int {
	t.Helper()
	n := 0
	for _, l := range readLog(t, e.log) {
		if strings.HasPrefix(l, "mcp load ") {
			n++
		}
	}
	return n
}

func (e *env) waitBroughtUp(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for e.svc.bringingUp(id) {
		if time.Now().After(deadline) {
			t.Fatal("the bring-up never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// SWI-26: a lifecycle operation cancels a gateway load in flight rather than
// waiting it out. The attach treats that like a refusal, and the load is retried
// as a bring-up once the operation has ended.
func TestGatewayLoadCancelledByAnOperationIsRetried(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "free")
	e := newEnv(t, `
  "mcp load "*) [ -f "`+flag+`" ] || exec sleep 30 ;;`)
	sb := e.sandbox(t, "77777777-aaaa", "cancelled", pb.SandboxState_SANDBOX_STATE_RUNNING)
	var calls atomic.Int32
	cancelled := make(chan context.CancelFunc, 1)
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), error) {
		if calls.Add(1) == 1 {
			held, cancel := context.WithCancel(ctx)
			cancelled <- cancel
			return held, cancel, nil
		}
		return ctx, func() {}, nil
	})
	attached := make(chan error, 1)
	go func() {
		_, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")})
		attached <- err
	}()
	cancel := <-cancelled
	deadline := time.Now().Add(3 * time.Second)
	for e.loads(t) < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(flag, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-attached; err != nil {
		t.Fatalf("attach = %v", err)
	}
	e.waitBroughtUp(t, sb.GetId())
	if n := e.loads(t); n != 2 {
		t.Errorf("%d loads, want the cancelled one and its retry", n)
	}
}

// SWI-26: an attach that overlaps the start of a kit attach defers its load to the
// bring-up the kit attach ends in, which starts the agent too.
func TestAttachOverlappingAKitAttachIsBroughtUpAfterIt(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	busy := e.busyLock()
	busy.Store(true)
	sb := e.sandbox(t, "77777777-aaaa", "kit", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	e.setState(t, sb.GetId(), pb.SandboxState_SANDBOX_STATE_CREATING)
	e.waitBroughtUp(t, sb.GetId())
	if e.loads(t) != 0 || *created != 0 {
		t.Fatalf("during the kit attach: %d loads, %d agents", e.loads(t), *created)
	}
	// AddKit writes RUNNING, releases the lock and runs the bring-up observer.
	done := e.setState(t, sb.GetId(), pb.SandboxState_SANDBOX_STATE_RUNNING)
	busy.Store(false)
	e.svc.OnBringUp(done)
	if e.loads(t) != 1 || *created != 1 {
		t.Errorf("after the kit attach: %d loads, %d agents", e.loads(t), *created)
	}
}

// SWI-26: a sandbox destroyed while its load waits gets no warm start, which would
// mint a hook token for it after the destroy purged them.
func TestBringUpOfADestroyedSandboxStartsNothing(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	busy := e.busyLock()
	busy.Store(true)
	sb := e.sandbox(t, "77777777-aaaa", "gone", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	if err := e.reg.Delete(sb.GetId()); err != nil {
		t.Fatal(err)
	}
	e.waitBroughtUp(t, sb.GetId())
	busy.Store(false)
	if *created != 0 {
		t.Errorf("%d agents started for a destroyed sandbox", *created)
	}
	// Directly too, as after a bring-up gives up waiting.
	e.svc.warmStartCurrent(sb.GetId())
	if *created != 0 {
		t.Errorf("%d agents started for a destroyed sandbox", *created)
	}
}

// SWI-26: the record is read under the runtime lock, not before. Registering can
// take minutes, and the sandbox may have stopped, or come back, meanwhile.
func TestAttachReadsTheStateUnderTheRuntimeLock(t *testing.T) {
	flip := func(e *env, to pb.SandboxState) {
		e.svc.SetRuntimeLock(func(ctx context.Context, id string) (context.Context, func(), error) {
			if _, err := e.reg.Update(id, func(s *pb.Sandbox) error {
				s.State = to
				return nil
			}); err != nil {
				panic(err)
			}
			return ctx, func() {}, nil
		})
	}
	t.Run("stopped since", func(t *testing.T) {
		e, _, created := deliverEnv(t, false)
		flip(e, pb.SandboxState_SANDBOX_STATE_STOPPED)
		sb := e.sandbox(t, "77777777-aaaa", "stopped", pb.SandboxState_SANDBOX_STATE_RUNNING)
		if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
			t.Fatal(err)
		}
		if e.loads(t) != 0 || *created != 0 {
			t.Errorf("into a stopped sandbox: %d loads, %d agents", e.loads(t), *created)
		}
	})
	t.Run("running since", func(t *testing.T) {
		e, _, created := deliverEnv(t, false)
		flip(e, pb.SandboxState_SANDBOX_STATE_RUNNING)
		sb := e.sandbox(t, "88888888-aaaa", "back", pb.SandboxState_SANDBOX_STATE_STOPPED)
		if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "a")}); err != nil {
			t.Fatal(err)
		}
		log := readLog(t, e.log)
		if !slices.Contains(log, "mcp load sxb-orch-88888888 --sandbox back") || *created != 1 {
			t.Errorf("a sandbox back up: %q, %d agents", log, *created)
		}
	})
}

// SWI-26 (review run-1528): a launch-attach whose load is refused owes it to the
// end of the launch. The record is CREATING, so a bring-up then would give up on
// it, and the launch would warm-start an agent without the orchestrator's tools.
func TestLaunchAttachBusyLoadRunsAtTheEndOfTheLaunch(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	busy := e.busyLock()
	busy.Store(true)
	stored := e.sandbox(t, "77777777-aaaa", "launching", pb.SandboxState_SANDBOX_STATE_CREATING)
	view := proto.Clone(stored).(*pb.Sandbox)
	view.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	if err := e.svc.Apply(bg, view, controller("orch-1", "a"), ""); err != nil {
		t.Fatal(err)
	}
	// Longer than a bring-up's retry step: one started now would have found the
	// record CREATING and given up by the time the launch writes RUNNING.
	time.Sleep(3 * loadRetryStep)
	if !e.svc.bringingUp(stored.GetId()) {
		t.Error("the owed load should hold the agent's start")
	}
	// Launch writes RUNNING and publishes it, which can wake a dispatch.
	out := e.setState(t, stored.GetId(), pb.SandboxState_SANDBOX_STATE_RUNNING)
	e.svc.WarmStart(out)
	if e.loads(t) != 0 || *created != 0 {
		t.Fatalf("before the end of the launch: %d loads, %d agents", e.loads(t), *created)
	}
	// The end of the launch doesn't wait for an operation still holding the sandbox.
	e.svc.OnLaunched(out)
	if !e.svc.bringingUp(stored.GetId()) || e.loads(t) != 0 || *created != 0 {
		t.Fatalf("while the operation holds it: %d loads, %d agents", e.loads(t), *created)
	}
	busy.Store(false)
	// The bring-up's warm start follows its mark, on the bring-up's goroutine.
	deadline := time.Now().Add(3 * time.Second)
	for !e.svc.agents.Live(stored.GetId()) {
		if time.Now().After(deadline) {
			t.Fatal("no agent after the launch")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.loads(t) != 1 || *created != 1 {
		t.Errorf("after the launch: %d loads, %d agents", e.loads(t), *created)
	}
	if e.svc.loadOwed(stored.GetId()) {
		t.Error("the bring-up should settle the owed load")
	}
}

// A launch whose load went through only warm-starts.
func TestOnLaunchedWithNoOwedLoad(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	sb := e.sandbox(t, "77777777-aaaa", "launched", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if err := e.svc.Apply(bg, sb, controller("orch-1", "a"), ""); err != nil {
		t.Fatal(err)
	}
	if e.loads(t) != 1 || *created != 0 {
		t.Fatalf("after the attach: %d loads, %d agents", e.loads(t), *created)
	}
	stored, _ := e.reg.Get(sb.GetId())
	e.svc.OnLaunched(stored)
	if e.loads(t) != 1 || *created != 1 {
		t.Errorf("after the launch: %d loads, %d agents", e.loads(t), *created)
	}
}

// launchAttachBusy runs a launch-attach whose load a lifecycle operation refuses,
// leaving the load owed and the operation still holding the sandbox.
func (e *env) launchAttachBusy(t *testing.T, id, name string) (*pb.Sandbox, *atomic.Bool) {
	t.Helper()
	busy := e.busyLock()
	busy.Store(true)
	stored := e.sandbox(t, id, name, pb.SandboxState_SANDBOX_STATE_CREATING)
	view := proto.Clone(stored).(*pb.Sandbox)
	view.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	if err := e.svc.Apply(bg, view, controller("orch-1", "a"), ""); err != nil {
		t.Fatal(err)
	}
	if !e.svc.loadOwed(id) {
		t.Fatal("the refused load should be owed")
	}
	return stored, busy
}

// waitBringUpsEnded waits out the bring-ups in progress, which an owed load may
// outlive (waitBroughtUp waits for both).
func (e *env) waitBringUpsEnded(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		e.svc.bringMu.Lock()
		n := e.svc.bringing[id]
		e.svc.bringMu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the bring-up never ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// SWI-26 (review run-1545): a sandbox stopped while the end of its launch's
// bring-up waits gets no load and no agent, and the load stays owed. The next
// bring-up does it, and the agent starts with the orchestrator's tools.
func TestOwedLoadOutlivesAStopAndRunsAtTheNextBringUp(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	stored, busy := e.launchAttachBusy(t, "88888888-bbbb", "stopped")
	id := stored.GetId()
	e.svc.OnLaunched(e.setState(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING))
	e.setState(t, id, pb.SandboxState_SANDBOX_STATE_STOPPED)
	busy.Store(false)
	e.waitBringUpsEnded(t, id)
	if e.loads(t) != 0 || *created != 0 {
		t.Errorf("for a stopped sandbox: %d loads, %d agents", e.loads(t), *created)
	}
	if !e.svc.loadOwed(id) {
		t.Error("a bring-up that gave up should leave the load owed")
	}
	e.svc.OnBringUp(e.setState(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING))
	if e.loads(t) != 1 || *created != 1 {
		t.Errorf("after the restart: %d loads, %d agents", e.loads(t), *created)
	}
	if e.svc.bringingUp(id) {
		t.Error("the load has run, so nothing should still be held")
	}
}

// SWI-26 (review run-1545): an attach overlapping the launch starts a bring-up,
// which gives up on the CREATING record. The launch's owed load survives it, or
// the end of the launch would warm-start an agent without the orchestrator's tools.
func TestBringUpOnACreatingRecordLeavesTheLoadOwed(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	stored, busy := e.launchAttachBusy(t, "77777777-aaaa", "overlap")
	id := stored.GetId()
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: id, Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	e.waitBringUpsEnded(t, id)
	if !e.svc.loadOwed(id) {
		t.Fatal("a bring-up that gave up on the CREATING record should leave the load owed")
	}
	out := e.setState(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING)
	busy.Store(false)
	e.svc.OnLaunched(out)
	e.waitBroughtUp(t, id)
	deadline := time.Now().Add(3 * time.Second)
	for !e.svc.agents.Live(id) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.loads(t) != 1 || *created != 1 {
		t.Errorf("after the launch: %d loads, %d agents", e.loads(t), *created)
	}
}

// reattach attaches the detached sandbox with the lock free and asserts it gets
// its load and its agent, rather than being refused as starting for good.
func (e *env) reattach(t *testing.T, id string, created *int) {
	t.Helper()
	if e.svc.bringingUp(id) {
		t.Fatal("a detached sandbox should hold nothing")
	}
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: id, Controller: controller("orch-1", "a")}); err != nil {
		t.Fatal(err)
	}
	if e.loads(t) != 1 || *created != 1 {
		t.Errorf("after the re-attach: %d loads, %d agents", e.loads(t), *created)
	}
}

// SWI-26 (review run-1545): a Detach settles a load already owed.
func TestDetachSettlesAnOwedLoad(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	stored, busy := e.launchAttachBusy(t, "77777777-aaaa", "detached")
	id := stored.GetId()
	if _, err := e.svc.Detach(bg, id, "orch-1", false); err != nil {
		t.Fatal(err)
	}
	e.setState(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING)
	busy.Store(false)
	e.reattach(t, id, created)
}

// SWI-26 (review run-1545): a Detach during the launch-attach's `mcp add` runs
// before its load is refused, so it can't settle that debt. The end of the launch
// does, with the controller already gone.
func TestDetachBeforeTheLoadIsRefusedIsSettledAtTheEndOfTheLaunch(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	stored := e.sandbox(t, "77777777-aaaa", "detached", pb.SandboxState_SANDBOX_STATE_CREATING)
	id := stored.GetId()
	var free atomic.Bool
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), error) {
		if free.Load() {
			return ctx, func() {}, nil
		}
		if _, err := e.svc.Detach(bg, id, "orch-1", false); err != nil {
			t.Error(err)
		}
		return nil, nil, errTestBusy
	})
	view := proto.Clone(stored).(*pb.Sandbox)
	view.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	if err := e.svc.Apply(bg, view, controller("orch-1", "a"), ""); err != nil {
		t.Fatal(err)
	}
	free.Store(true)
	out := e.setState(t, id, pb.SandboxState_SANDBOX_STATE_RUNNING)
	if out.GetController() != nil {
		t.Fatal("the Detach should have cleared the controller")
	}
	e.svc.OnLaunched(out)
	e.reattach(t, id, created)
}

// A destroy settles the owed load, so nothing is held for an id that's gone.
func TestDestroySettlesAnOwedLoad(t *testing.T) {
	e := newEnv(t, "")
	busy := e.busyLock()
	busy.Store(true)
	stored := e.sandbox(t, "77777777-aaaa", "destroyed", pb.SandboxState_SANDBOX_STATE_CREATING)
	view := proto.Clone(stored).(*pb.Sandbox)
	view.State = pb.SandboxState_SANDBOX_STATE_RUNNING
	if err := e.svc.Apply(bg, view, controller("orch-1", "a"), ""); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.reg.Get(stored.GetId())
	e.svc.OnDestroy(cur)
	if e.svc.bringingUp(stored.GetId()) {
		t.Error("the destroy should settle the owed load")
	}
}
