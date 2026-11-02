package worker

import (
	"strings"
	"sync/atomic"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// SWI-26 B4: the gateway's `sbx mcp load` takes the sandbox's runtime read
// lock. While a lifecycle operation holds it the attach still registers, and
// leaves the load to that operation's bring-up.
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
	log := readLog(t, e.log)
	if len(log) != 1 || !strings.HasPrefix(log[0], "mcp add sxb-orch-77777777 ") {
		t.Fatalf("registered but not loaded while busy: %q", log)
	}
	busy.Store(false)
	stored, _ := e.reg.Get(sb.GetId())
	e.svc.OnBringUp(stored)
	log = readLog(t, e.log)
	if len(log) != 2 || log[1] != "mcp load sxb-orch-77777777 --sandbox busy-one" {
		t.Errorf("the bring-up loads it: %q", log)
	}
}
