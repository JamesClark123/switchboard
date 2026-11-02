package worker

import (
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
	e.svc.SetRuntimeLock(func(string) (func(), bool) {
		if busy.Load() {
			return nil, false
		}
		return func() {}, true
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
