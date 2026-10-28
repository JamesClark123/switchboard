package grpc_test

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	sbgrpc "github.com/jamesclark123/switchboard/services/switchboardd/internal/grpc"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/sandbox"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// SWI-17 (research R14): the daemon wiring around detached agents.

const (
	orphanAlive   = "agent\t/c\nref\t0\t\nfile\t1\t42\t999\nproc\t42\t999\t0\t/c\t\tclaude\x1f--dangerously-skip-permissions\n"
	orphanGarbled = "agent\t/c\n"
)

// orphanRunner never runs a probe or reap on the host: its `sbx exec`
// answers from answer, and a reap is counted. stopped records whether the
// agent session had been drained when `sbx stop` ran.
type orphanRunner struct {
	*testRunner
	mu       sync.Mutex
	answer   string
	reaps    int
	sess     *echoSession
	drained  atomic.Bool
	stopRuns atomic.Int32
}

func (r *orphanRunner) Exec(ctx context.Context, _ string, argv []string) *exec.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(argv[len(argv)-1], "kill -KILL") {
		r.reaps++
		return exec.CommandContext(ctx, "printf", "%s", "nofile\nreaped\t1\n")
	}
	return exec.CommandContext(ctx, "printf", "%s", r.answer)
}

func (r *orphanRunner) Stop(ctx context.Context, ref string) error {
	r.stopRuns.Add(1)
	r.mu.Lock()
	if r.sess != nil {
		r.sess.mu.Lock()
		r.drained.Store(r.sess.closed)
		r.sess.mu.Unlock()
	}
	r.mu.Unlock()
	return r.testRunner.Stop(ctx, ref)
}

func (r *orphanRunner) setAnswer(s string) {
	r.mu.Lock()
	r.answer = s
	r.mu.Unlock()
}

type orphanServer struct {
	client  pb.SwitchboardClient
	mgr     *sandbox.Manager
	reg     *registry.Registry
	agents  *agent.Registry
	run     *orphanRunner
	spawned atomic.Int32
	id      string
}

func newOrphanServer(t *testing.T) *orphanServer {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	o := &orphanServer{reg: reg, id: "sb-orphan", run: &orphanRunner{testRunner: &testRunner{running: map[string]bool{"w": true}}, answer: orphanGarbled}}
	ws := filepath.Join(dir, "ws")
	o.mgr = sandbox.NewManager(reg, o.run, ws, "host-1")
	o.agents = agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) {
		o.spawned.Add(1)
		s := newEchoSession()
		o.run.mu.Lock()
		o.run.sess = s
		o.run.mu.Unlock()
		return s, nil
	})
	o.agents.SetOrphanGate(o.mgr)
	o.agents.SetOnClientExit(o.mgr.AgentClientExited)
	o.mgr.SetOrphanDeps(sandbox.OrphanDeps{Agents: o.agents})
	srv := sbgrpc.NewServer(sbgrpc.Config{Manager: o.mgr, HostID: "host-1", WorkspaceRoot: ws, Hub: agent.NewHub("host-1"), Agents: o.agents})
	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	o.client = pb.NewSwitchboardClient(conn)
	if err := reg.Put(&pb.Sandbox{Id: o.id, DisplayName: "w", ContainerRef: "w", State: pb.SandboxState_SANDBOX_STATE_RUNNING,
		Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}}); err != nil {
		t.Fatal(err)
	}
	return o
}

func (o *orphanServer) check() pb.AgentCheck_State {
	sb, _ := o.reg.Get(o.id)
	return sb.GetAgent().GetAgentCheck().GetState()
}

func waitCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// attach opens AgentInput and returns the first reply's error (nil when the
// terminal attached).
func (o *orphanServer) attach(t *testing.T, replace bool) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := o.client.AttachAgent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	in := &pb.AgentInput{SandboxId: o.id, Data: []byte("x")}
	if replace {
		in.Attach = &pb.AgentInput_AttachInfo{ReplaceOrphan: true}
	}
	if err := stream.Send(in); err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	return err
}

func refused(err error, text string) bool {
	return status.Code(err) == codes.FailedPrecondition && strings.Contains(status.Convert(err).Message(), text)
}

// AttachAgent: the gate's refusals are FAILED_PRECONDITION with the texts the
// TUI matches; replace_orphan stops the orphan first, then spawns.
func TestAttachAgentBesideAnOrphan(t *testing.T) {
	o := newOrphanServer(t)
	unlock, _ := o.mgr.TryLockRuntime(o.id)
	if err := o.attach(t, false); !refused(err, "sandbox is busy; try again") {
		t.Errorf("busy: %v", err)
	}
	if err := o.attach(t, true); !refused(err, "sandbox is busy; try again") {
		t.Errorf("replace while busy: %v", err)
	}
	unlock()

	o.mgr.MarkAgentUnknown(o.id)
	if err := o.attach(t, false); !refused(err, "checking for a detached agent") {
		t.Errorf("unknown: %v", err)
	}
	o.run.setAnswer(orphanAlive)
	o.mgr.AgentClientExited(o.id, 1)
	waitCond(t, func() bool { return o.check() == pb.AgentCheck_STATE_DETACHED })
	if err := o.attach(t, false); !refused(err, "agent detached by a daemon restart") {
		t.Errorf("detached: %v", err)
	}
	if o.spawned.Load() != 0 || o.run.reaps != 0 {
		t.Fatalf("refusals spawned %d, reaped %d", o.spawned.Load(), o.run.reaps)
	}
	if err := o.attach(t, true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if o.run.reaps != 1 || o.spawned.Load() != 1 || o.check() != pb.AgentCheck_STATE_CLEAR {
		t.Errorf("replace: %d reaps, %d spawns, check %v", o.run.reaps, o.spawned.Load(), o.check())
	}
}

// A stop drains the live agent's client before `sbx stop`, without a probe or
// an orphan afterwards.
func TestStopDrainsTheAgentFirst(t *testing.T) {
	o := newOrphanServer(t)
	if _, err := o.agents.Session(o.id, &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := o.client.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: o.id}); err != nil {
		t.Fatal(err)
	}
	if o.run.stopRuns.Load() != 1 || !o.run.drained.Load() {
		t.Error("`sbx stop` ran before the agent client was drained")
	}
	if o.agents.Live(o.id) || o.check() != pb.AgentCheck_STATE_CLEAR || o.run.reaps != 0 {
		t.Errorf("after the stop: live %v, check %v", o.agents.Live(o.id), o.check())
	}
}

// The on-change hook's teardown of a live agent on CREATING or ERROR leaves
// the sandbox unknown; on STOPPED it does not.
func TestTeardownOnCreatingMarksUnknown(t *testing.T) {
	o := newOrphanServer(t)
	if _, err := o.agents.Session(o.id, &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	o.mgr.Emit(&pb.Sandbox{Id: o.id, State: pb.SandboxState_SANDBOX_STATE_CREATING})
	if _, err := o.mgr.Admit(o.id); !errors.Is(err, agent.ErrChecking) {
		t.Errorf("Admit after a CREATING teardown = %v", err)
	}
	o2 := newOrphanServer(t)
	if _, err := o2.agents.Session(o2.id, &pb.AgentSpec{}); err != nil {
		t.Fatal(err)
	}
	o2.mgr.Emit(&pb.Sandbox{Id: o2.id, State: pb.SandboxState_SANDBOX_STATE_STOPPED})
	if rel, err := o2.mgr.Admit(o2.id); err != nil {
		t.Errorf("Admit after a STOPPED teardown = %v", err)
	} else {
		rel()
	}
}
