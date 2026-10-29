package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
	"google.golang.org/protobuf/proto"
)

// SWI-17 T048: one orphan gate for every spawn (research R14). Each spawn path
// runs through the real wiring: the worker service, the agent and terminal
// registries, and a sandbox.Manager as the gate and the reaper, whose probes
// and reaps go to a stub `sbx` (exec answers from a file the test rewrites).
// The factory counts spawns.

const (
	gateID   = "99999999-g"
	gateName = "w"
	// aliveAnswer is a probe finding the agent.pid process of generation 1.
	aliveAnswer = "agent\t/c\nref\t0\t\nfile\t1\t42\t999\nproc\t42\t999\t0\t/c\t\tclaude\x1f--dangerously-skip-permissions\n"
	// garbled is no decision at all: the probe fails and the check stays unknown.
	garbled = "agent\t/c\n"
)

type gateEnv struct {
	*env
	mgr     *sandbox.Manager
	agents  *agent.Registry
	terms   *terminal.Registry
	answer  string // the file the stub's exec prints
	mu      sync.Mutex
	created int
	block   chan struct{} // when set, the factory waits on it
	entered chan struct{}
}

func newGateEnv(t *testing.T) *gateEnv {
	t.Helper()
	dir := t.TempDir()
	answer := filepath.Join(dir, "answer")
	if err := os.WriteFile(answer, []byte(garbled), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(answer+".reap", []byte("agent\t/c\nnofile\nreaped\t1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := `  "ls --json") printf '{"sandboxes":[{"name":"` + gateName + `","id":"` + gateID + `","status":"running"}]}' ;;
  "exec ` + gateName + ` -- bash -lc "*"kill -KILL"*) cat "` + answer + `.reap" ;;
  "exec ` + gateName + ` -- bash -lc "*) cat "` + answer + `" ;;`
	g := &gateEnv{env: newEnv(t, cases), answer: answer}
	g.agents = agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) {
		g.mu.Lock()
		g.created++
		block, entered := g.block, g.entered
		g.mu.Unlock()
		if block != nil {
			close(entered)
			<-block
		}
		return newRecSession(), nil
	})
	g.terms = terminal.NewRegistry(func(id string) (terminal.PTY, error) { return g.agents.Session(id, &pb.AgentSpec{}) }, 0, nil)
	g.svc.agents = g.agents
	g.svc.SetTerminals(g.terms)
	bin := strings.TrimSuffix(g.log, "argv.log") + "sbx"
	g.mgr = sandbox.NewManager(g.reg, &sandbox.SbxRunner{Bin: bin}, filepath.Join(dir, "ws"), "host-n")
	g.agents.SetOrphanGate(g.mgr)
	g.agents.SetOnClientExit(g.mgr.AgentClientExited)
	g.mgr.SetOrphanDeps(sandbox.OrphanDeps{Agents: g.agents})
	g.svc.SetOrphanReaper(g.mgr)
	return g
}

func (g *gateEnv) spawns() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.created
}

// reaps counts the reap scripts the stub ran.
func (g *gateEnv) reaps(t *testing.T) int {
	return strings.Count(readFile(t, g.log), "kill -KILL")
}

func (g *gateEnv) answerWith(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(g.answer, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (g *gateEnv) check() pb.AgentCheck_State {
	sb, _ := g.reg.Get(gateID)
	return sb.GetAgent().GetAgentCheck().GetState()
}

func gateWait(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The run states the table covers.
var gateRuns = map[string]func(sb *pb.Sandbox){
	"action": func(sb *pb.Sandbox) {
		sb.Controller.Run = &pb.WorkerRun{Id: "run-1", StepRole: pb.StepRole_STEP_ROLE_ACTION}
	},
	"setup": func(sb *pb.Sandbox) {
		sb.Controller.Run = &pb.WorkerRun{Id: "run-1", StepRole: pb.StepRole_STEP_ROLE_SETUP}
	},
	"setup-halted": func(sb *pb.Sandbox) {
		sb.Controller.Run = &pb.WorkerRun{Id: "run-1", StepRole: pb.StepRole_STEP_ROLE_SETUP, Halted: true, StepNote: orphanNote}
	},
	"hold": func(sb *pb.Sandbox) {
		sb.Controller.Run = &pb.WorkerRun{Id: "run-1", StepRole: pb.StepRole_STEP_ROLE_CLEANUP, Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP}
	},
	"no-run":       func(*pb.Sandbox) {},
	"uncontrolled": func(sb *pb.Sandbox) { sb.Controller = nil },
}

// The spawn paths: each returns the error it reports (nil for a path that
// only logs).
var gatePaths = map[string]func(g *gateEnv) error{
	"warm-start": func(g *gateEnv) error {
		sb, _ := g.reg.Get(gateID)
		g.svc.WarmStart(sb)
		return nil
	},
	"deliver": func(g *gateEnv) error {
		_, err := g.svc.Deliver(bg, deliverReq(gateID))
		return err
	},
	"attach-agent": func(g *gateEnv) error {
		_, err := g.terms.Broadcaster(gateID)
		return err
	},
	"step-first": func(g *gateEnv) error {
		req := &pb.DeliverWorkerStepRequest{SandboxId: gateID, OrchestratorId: "orch-1", RunId: "run-1", Action: "a",
			Step: &pb.RunStep{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_CUSTOM, Text: "x"}, StepIndex: 1, First: true}
		_, err := g.svc.DeliverStep(bg, req)
		return err
	},
	"step-restart": func(g *gateEnv) error {
		req := &pb.DeliverWorkerStepRequest{SandboxId: gateID, OrchestratorId: "orch-1", RunId: "run-1", Action: "a",
			Step: &pb.RunStep{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_CUSTOM, Text: "x"}, StepIndex: 1, Restart: true}
		_, err := g.svc.DeliverStep(bg, req)
		return err
	},
	"prompt": func(g *gateEnv) error { return g.agents.Prompt(gateID, &pb.AgentSpec{}, "escape-hatch result") },
	"ensure-drained": func(g *gateEnv) error {
		return EnsureDrained(func() *terminal.Registry { return g.terms }, g.agents)(gateID, &pb.AgentSpec{}, "escape-hatch result")
	},
}

// setGate puts the sandbox in one of the gate's refusing states, returning
// the release for a held lock.
func (g *gateEnv) setGate(t *testing.T, state string) func() {
	t.Helper()
	switch state {
	case "record":
		g.answerWith(t, aliveAnswer)
		g.mgr.AgentClientExited(gateID, 1)
		gateWait(t, func() bool { return g.check() == pb.AgentCheck_STATE_DETACHED })
	case "unknown":
		g.mgr.MarkAgentUnknown(gateID)
		gateWait(t, func() bool { return g.check() == pb.AgentCheck_STATE_UNKNOWN })
	case "busy":
		unlock, ok := g.mgr.TryLockRuntime(gateID)
		if !ok {
			t.Fatal("lock")
		}
		return unlock
	}
	return func() {}
}

func TestOrphanGateEverySpawnPath(t *testing.T) {
	for _, gate := range []string{"record", "unknown", "busy"} {
		for path, call := range gatePaths {
			for runName, run := range gateRuns {
				t.Run(gate+"/"+path+"/"+runName, func(t *testing.T) {
					g := newGateEnv(t)
					sb := g.sandbox(t, gateID, gateName, pb.SandboxState_SANDBOX_STATE_RUNNING)
					sb.Controller = controller("orch-1", "b")
					run(sb)
					if err := g.reg.Put(sb); err != nil {
						t.Fatal(err)
					}
					release := g.setGate(t, gate)
					before, _ := g.reg.Get(gateID)
					err := call(g)
					release()

					// Only the restart of a restart halt stops an orphan (or whatever
					// agent the reap finds while still unknown), and then the fresh
					// agent starts. A held lock refuses it busy.
					reaper := gate != "busy" && path == "step-restart" && runName == "setup-halted"
					if reaper {
						if g.reaps(t) != 1 || g.spawns() != 1 || err == nil || !strings.Contains(err.Error(), "agent is starting") {
							t.Errorf("restart: %d reaps, %d spawns, %v", g.reaps(t), g.spawns(), err)
						}
						return
					}
					if g.spawns() != 0 {
						t.Errorf("spawned beside a %s gate (err %v)", gate, err)
					}
					if n := g.reaps(t); n != 0 {
						t.Errorf("%d reaps", n)
					}
					// Whatever is refused, the run's record is left as it was: an
					// escape-hatch result on a SETUP run that hasn't halted stops
					// nothing and fails nothing.
					after, _ := g.reg.Get(gateID)
					if !proto.Equal(before.GetController().GetRun(), after.GetController().GetRun()) {
						t.Errorf("run changed: %v -> %v", before.GetController().GetRun(), after.GetController().GetRun())
					}
					// A refusal the path reports names the gate's reason.
					if err != nil && (path == "attach-agent" || path == "prompt" || path == "ensure-drained") &&
						!errors.Is(err, agent.ErrOrphan) && !errors.Is(err, agent.ErrBusy) && !errors.Is(err, agent.ErrHeld) {
						t.Errorf("refusal %v", err)
					}
				})
			}
		}
	}
}

// With no orphan and the lock free, the same paths spawn (the table above
// isn't vacuous).
func TestOrphanGateClearSpawns(t *testing.T) {
	cases := map[string]string{
		"warm-start": "no-run", "deliver": "no-run", "attach-agent": "action", "step-first": "no-run",
		"prompt": "uncontrolled", "ensure-drained": "uncontrolled",
	}
	for path, runName := range cases {
		t.Run(path, func(t *testing.T) {
			g := newGateEnv(t)
			sb := g.sandbox(t, gateID, gateName, pb.SandboxState_SANDBOX_STATE_RUNNING)
			sb.Controller = controller("orch-1", "b")
			gateRuns[runName](sb)
			if err := g.reg.Put(sb); err != nil {
				t.Fatal(err)
			}
			_ = gatePaths[path](g)
			if g.spawns() != 1 {
				t.Errorf("%s spawned %d", path, g.spawns())
			}
		})
	}
}

// A stop that starts between the gate check and the factory's cmd.Start
// waits for the spawn: the gate holds the runtime lock until the session is
// registered.
func TestOrphanGateStopWaitsForTheFactory(t *testing.T) {
	g := newGateEnv(t)
	g.sandbox(t, gateID, gateName, pb.SandboxState_SANDBOX_STATE_RUNNING)
	g.block, g.entered = make(chan struct{}), make(chan struct{})
	spawned := make(chan error, 1)
	go func() { _, err := g.agents.Session(gateID, &pb.AgentSpec{}); spawned <- err }()
	<-g.entered
	stopped := make(chan error, 1)
	go func() { _, err := g.mgr.Stop(context.Background(), gateID); stopped <- err }()
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(readFile(t, g.log), "stop "+gateName) {
		t.Fatal("sbx stop ran while the factory was starting the agent")
	}
	close(g.block)
	if err := <-spawned; err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, g.log), "stop "+gateName) {
		t.Error("the stop never ran")
	}
}

// A restart whose reap fails starts nothing.
func TestRestartHaltFailedReapStartsNothing(t *testing.T) {
	g := newGateEnv(t)
	sb := g.sandbox(t, gateID, gateName, pb.SandboxState_SANDBOX_STATE_RUNNING)
	sb.Controller = controller("orch-1", "b")
	gateRuns["setup-halted"](sb)
	if err := g.reg.Put(sb); err != nil {
		t.Fatal(err)
	}
	g.setGate(t, "unknown")
	if err := os.WriteFile(g.answer+".reap", []byte(garbled), 0o644); err != nil {
		t.Fatal(err)
	}
	err := gatePaths["step-restart"](g)
	if err == nil || !errors.Is(err, ErrPrecondition) || g.spawns() != 0 {
		t.Errorf("failed reap: %v, %d spawns", err, g.spawns())
	}
	g.svc.SetOrphanReaper(nil)
	if err := gatePaths["step-restart"](g); err == nil || !strings.Contains(err.Error(), "not handled") {
		t.Errorf("no reaper: %v", err)
	}
}
