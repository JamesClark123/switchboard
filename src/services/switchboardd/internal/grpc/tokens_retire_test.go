package grpc_test

import (
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
)

// SWI-17 (research R13/R14): a confirmed stop retires a sandbox's hook tokens
// and a destroy purges them; CREATING and ERROR, which may leave the agent
// running, retire nothing.
func TestStopAndDestroyRetireHookTokens(t *testing.T) {
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	mgr := sandbox.NewManager(reg, &testRunner{running: map[string]bool{}}, t.TempDir(), "h")
	tokens, err := agent.LoadHookTokens(reg)
	if err != nil {
		t.Fatal(err)
	}
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { return nil, nil })
	agents.SetHookTokens(tokens)
	sbgrpc.NewServer(sbgrpc.Config{Manager: mgr, HostID: "h", Agents: agents})
	sp, err := tokens.Issue("sb")
	if err != nil {
		t.Fatal(err)
	}
	live := func() bool { _, ok := tokens.Verify("sb", "1", sp.Token); return ok }
	for _, st := range []pb.SandboxState{pb.SandboxState_SANDBOX_STATE_CREATING, pb.SandboxState_SANDBOX_STATE_ERROR} {
		mgr.Emit(&pb.Sandbox{Id: "sb", State: st})
		if !live() {
			t.Fatalf("%v retired the token", st)
		}
	}
	mgr.Emit(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_STOPPED})
	if live() || tokens.Generation("sb") != 1 {
		t.Error("a stop retires the tokens and keeps the counter")
	}
	mgr.Emit(&pb.Sandbox{Id: "sb", State: pb.SandboxState_SANDBOX_STATE_DESTROYING})
	if tokens.Generation("sb") != 0 {
		t.Error("a destroy purges the counter")
	}
}
