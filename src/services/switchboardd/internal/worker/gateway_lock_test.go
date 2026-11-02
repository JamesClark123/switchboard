package worker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

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
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), bool) {
		if busy.Load() {
			return nil, nil, false
		}
		return ctx, func() {}, true
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
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), bool) {
		if busy.Load() {
			return nil, nil, false
		}
		return ctx, func() {}, true
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
	e.svc.SetRuntimeLock(func(ctx context.Context, _ string) (context.Context, func(), bool) {
		if calls.Add(1) == 1 {
			held, cancel := context.WithCancel(ctx)
			cancelled <- cancel
			return held, cancel, true
		}
		return ctx, func() {}, true
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
		e.svc.SetRuntimeLock(func(ctx context.Context, id string) (context.Context, func(), bool) {
			if _, err := e.reg.Update(id, func(s *pb.Sandbox) error {
				s.State = to
				return nil
			}); err != nil {
				panic(err)
			}
			return ctx, func() {}, true
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
