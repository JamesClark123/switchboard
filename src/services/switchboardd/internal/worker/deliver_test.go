package worker

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
)

// recSession records what is typed into the agent and blocks reads until closed.
type recSession struct {
	mu     sync.Mutex
	writes []string
	closed chan struct{}
	fail   bool // every write fails
}

func newRecSession() *recSession { return &recSession{closed: make(chan struct{})} }

func (s *recSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return 0, errors.New("no pty")
	}
	s.writes = append(s.writes, string(p))
	return len(p), nil
}
func (s *recSession) Read([]byte) (int, error)    { <-s.closed; return 0, io.EOF }
func (s *recSession) Resize(uint16, uint16) error { return nil }
func (s *recSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}
func (s *recSession) typed() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.writes, "")
}

// deliverEnv wires a worker service with a recording agent session and a real
// terminal registry over it, so the broadcaster-ensure step is observable.
func deliverEnv(t *testing.T, failPrompt bool) (*env, *recSession, *int) {
	t.Helper()
	e := newEnv(t, "")
	sess := newRecSession()
	sess.fail = failPrompt
	created := 0
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) {
		created++
		return sess, nil
	})
	e.svc.agents = agents
	e.svc.SetTerminals(terminal.NewRegistry(func(id string) (terminal.PTY, error) { return agents.Session(id, &pb.AgentSpec{}) }, 0, nil))
	return e, sess, &created
}

// attachReady attaches the sandbox to orch-1 (which warm-starts its agent) and
// records the agent ready, as its SessionStart hook would (SWI-15).
func attachReady(t *testing.T, e *env, sandboxID string) {
	t.Helper()
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sandboxID, Controller: controller("orch-1", "review-bot")}); err != nil {
		t.Fatal(err)
	}
	if !e.svc.agents.Live(sandboxID) {
		t.Fatal("attach should warm-start the agent")
	}
	e.svc.agents.MarkReady(sandboxID)
}

func deliverReq(sandboxID string) *pb.DeliverWorkerPromptRequest {
	return &pb.DeliverWorkerPromptRequest{SandboxId: sandboxID, OrchestratorId: "orch-1", RunId: "run-1a2b3c4d-7", Action: "review-pr", Prompt: "Review pull request 412 in acme/web.\n\nfocus: tests"}
}

func TestDeliverRefusals(t *testing.T) {
	e, sess, _ := deliverEnv(t, false)
	sb := e.sandbox(t, "aaaaaaaa-1", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	// Not a worker.
	if ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId())); ok || !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "not a worker of orchestrator orch-1") {
		t.Errorf("not a worker = %v / %v", ok, err)
	}
	attachReady(t, e, sb.GetId())
	set := func(mut func(*pb.Sandbox)) {
		if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error { mut(x); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]struct {
		mut  func(*pb.Sandbox)
		want string
	}{
		"stopped": {func(x *pb.Sandbox) { x.State = pb.SandboxState_SANDBOX_STATE_STOPPED }, "sandbox is stopped"},
		"agent working": {func(x *pb.Sandbox) {
			x.State = pb.SandboxState_SANDBOX_STATE_RUNNING
			x.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING
		}, "agent is working"},
		"agent needs input":                {func(x *pb.Sandbox) { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT }, "agent is needs_input"},
		"agent waiting on background work": {func(x *pb.Sandbox) { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_WAITING }, "agent is waiting"},
		"wrong orchestrator": {func(x *pb.Sandbox) {
			x.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE
			x.Controller.OrchestratorId = "other"
		}, "not a worker of orchestrator orch-1"},
		"run already recorded": {func(x *pb.Sandbox) {
			x.Controller.OrchestratorId = "orch-1"
			x.Controller.Run = &pb.WorkerRun{Id: "run-old"}
		}, "run run-old is already on this sandbox"},
	}
	for _, name := range []string{"stopped", "agent working", "agent needs input", "wrong orchestrator", "run already recorded"} {
		c := cases[name]
		set(c.mut)
		ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId()))
		if ok || !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v / %v (want %q)", name, ok, err, c.want)
		}
	}
	if sess.typed() != "" {
		t.Error("a refused delivery must type nothing")
	}
	if ok, err := e.svc.Deliver(bg, deliverReq("nope")); ok || !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown sandbox = %v / %v", ok, err)
	}
}

func TestDeliverAcceptsTypesFrameAndDrains(t *testing.T) {
	e, sess, created := deliverEnv(t, false)
	sb := e.sandbox(t, "bbbbbbbb-2", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	attachReady(t, e, sb.GetId())
	emitsBefore := e.mgr.emitted()
	ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId()))
	if err != nil || !ok {
		t.Fatalf("deliver = %v / %v", ok, err)
	}
	// The run is recorded on the sandbox and emitted.
	stored, _ := e.reg.Get(sb.GetId())
	if r := stored.GetController().GetRun(); r.GetId() != "run-1a2b3c4d-7" || r.GetAction() != "review-pr" || r.GetNeedsReview() {
		t.Errorf("recorded run = %+v", r)
	}
	if e.mgr.emitted() != emitsBefore+1 {
		t.Error("delivery should emit sandbox_changed")
	}
	// The broadcaster exists (the PTY has a reader) and exactly the documented
	// frame was typed through the developer's prompt path (bracketed paste, then
	// Enter).
	if n, _ := e.svc.terms.Counts(sb.GetId()); n != 0 || *created != 1 {
		t.Errorf("terminal session should exist with no attachment: counts %d created %d", n, *created)
	}
	want := agent.Typed("[switchboard orchestrator \"review-bot\" · run run-1a2b3c4d-7 · action \"review-pr\"]\n\nReview pull request 412 in acme/web.\n\nfocus: tests\n\n" + runFrameTail)
	deadline := time.Now().Add(2 * time.Second)
	for sess.typed() != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sess.typed(); got != want {
		t.Errorf("typed = %q\nwant  %q", got, want)
	}
	// A second delivery while the run is recorded is refused.
	if ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId())); ok || !errors.Is(err, ErrPrecondition) {
		t.Errorf("second delivery = %v / %v", ok, err)
	}
	// Frame rendering is exact.
	if Frame("n", "r", "a", "p") != "[switchboard orchestrator \"n\" · run r · action \"a\"]\n\np\n\n"+runFrameTail {
		t.Error("Frame")
	}
}

func TestDeliverRollsBackWhenPromptFails(t *testing.T) {
	e, _, _ := deliverEnv(t, true)
	sb := e.sandbox(t, "cccccccc-3", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	attachReady(t, e, sb.GetId())
	ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId()))
	if ok || err == nil || !strings.Contains(err.Error(), "no pty") {
		t.Fatalf("deliver with a failing prompt = %v / %v", ok, err)
	}
	stored, _ := e.reg.Get(sb.GetId())
	if stored.GetController().GetRun() != nil {
		t.Error("a failed prompt must roll the recorded run back")
	}
	// No agent registry at all: refused and rolled back as well.
	e2 := newEnv(t, "")
	sb2 := e2.sandbox(t, "dddddddd-4", "w2", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e2.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb2.GetId(), Controller: controller("orch-1", "b")}); err != nil {
		t.Fatal(err)
	}
	if ok, err := e2.svc.Deliver(bg, deliverReq(sb2.GetId())); ok || err == nil {
		t.Error("no agent registry should refuse")
	}
	if stored, _ := e2.reg.Get(sb2.GetId()); stored.GetController().GetRun() != nil {
		t.Error("rollback without a registry")
	}
}

// EnsureDrained creates the terminal session before the wrapped prompt runs,
// and tolerates a nil registry (the escape hatch's startup order).
func TestEnsureDrained(t *testing.T) {
	sess := newRecSession()
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { return sess, nil })
	var terms *terminal.Registry
	prompt := EnsureDrained(func() *terminal.Registry { return terms }, agents)
	if err := prompt("sb", &pb.AgentSpec{}, "x"); err != nil {
		t.Fatal(err)
	}
	terms = terminal.NewRegistry(func(id string) (terminal.PTY, error) { return agents.Session(id, &pb.AgentSpec{}) }, 0, nil)
	if err := prompt("sb", &pb.AgentSpec{}, "y"); err != nil {
		t.Fatal(err)
	}
	if n, _ := terms.Counts("sb"); n != 0 {
		t.Error("counts")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(sess.typed(), agent.Typed("y")) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(sess.typed(), agent.Typed("x")) || !strings.Contains(sess.typed(), agent.Typed("y")) {
		t.Errorf("typed = %q", sess.typed())
	}
}

// heldStatus is a status side whose one sandbox's run holds it.
type heldStatus struct{ hold pb.WorkerHold }

func (h heldStatus) SetAgentStatus(string, pb.AgentStatus, time.Time) (*pb.Sandbox, error) {
	return &pb.Sandbox{}, nil
}

func (h heldStatus) Get(id string) (*pb.Sandbox, error) {
	return &pb.Sandbox{Id: id, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE},
		Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "r1", Hold: h.hold}}}, nil
}

// SWI-17 (research R13): an escape-hatch result to a held sandbox with no live
// agent spawns nothing; a live agent still gets it.
func TestEnsureDrainedSpawnsNothingWhileHeld(t *testing.T) {
	var created int
	sess := newRecSession()
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { created++; return sess, nil })
	agents.SetStatusUpdater(heldStatus{hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER})
	terms := terminal.NewRegistry(func(id string) (terminal.PTY, error) { return agents.Session(id, &pb.AgentSpec{}) }, 0, nil)
	prompt := EnsureDrained(func() *terminal.Registry { return terms }, agents)
	if err := prompt("sb", &pb.AgentSpec{}, "result"); !errors.Is(err, agent.ErrHeld) || err.Error() != "sandbox is held; open its terminal" {
		t.Errorf("held, no agent = %v", err)
	}
	if err := agents.Prompt("sb", &pb.AgentSpec{}, "result"); !errors.Is(err, agent.ErrHeld) {
		t.Errorf("Prompt, held, no agent = %v", err)
	}
	if created != 0 || agents.Live("sb") {
		t.Fatal("nothing may be spawned while held")
	}
	// The developer's terminal (AttachAgent) still starts one; then results reach it.
	if _, err := terms.Broadcaster("sb"); err != nil {
		t.Fatal(err)
	}
	if err := prompt("sb", &pb.AgentSpec{}, "result"); err != nil || created != 1 {
		t.Errorf("held, live agent = %v (created %d)", err, created)
	}
}

// SWI-15: a prompt is never typed into an agent that has not reported ready.
// With no session the agent is started and the run refused (it stays queued);
// a live agent still starting is refused without a second session.
func TestDeliverWaitsForTheAgentToBeReady(t *testing.T) {
	e, sess, created := deliverEnv(t, false)
	sb := e.sandbox(t, "eeeeeeee-5", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	// A controller recorded without the attach path (e.g. a daemon restart
	// dropped every session): the record reads IDLE but no agent runs.
	if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error {
		x.Controller = controller("orch-1", "review-bot")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId()))
	if ok || !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "agent is starting") {
		t.Fatalf("deliver with no agent = %v / %v", ok, err)
	}
	if !e.svc.agents.Live(sb.GetId()) || *created != 1 {
		t.Errorf("the agent should have been started: live %v created %d", e.svc.agents.Live(sb.GetId()), *created)
	}
	if n, _ := e.svc.terms.Counts(sb.GetId()); n != 0 {
		t.Errorf("started through the terminal registry with no attachment: %d", n)
	}
	// Still starting: refused again, no second session, nothing recorded.
	if ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId())); ok || !strings.Contains(err.Error(), "agent is starting") {
		t.Errorf("deliver while starting = %v / %v", ok, err)
	}
	if *created != 1 {
		t.Errorf("sessions created = %d", *created)
	}
	if stored, _ := e.reg.Get(sb.GetId()); stored.GetController().GetRun() != nil {
		t.Error("a refused delivery must record no run")
	}
	time.Sleep(20 * time.Millisecond)
	if sess.typed() != "" {
		t.Errorf("typed %q into an agent that was not ready", sess.typed())
	}
	// Once it reports in, the run is delivered.
	e.svc.agents.MarkReady(sb.GetId())
	if ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId())); !ok || err != nil {
		t.Errorf("deliver once ready = %v / %v", ok, err)
	}
	// The other guards still come first: a stopped sandbox is not started.
	e2, _, created2 := deliverEnv(t, false)
	sb2 := e2.sandbox(t, "ffffffff-6", "w", pb.SandboxState_SANDBOX_STATE_STOPPED)
	if _, err := e2.reg.Update(sb2.GetId(), func(x *pb.Sandbox) error { x.Controller = controller("orch-1", "b"); return nil }); err != nil {
		t.Fatal(err)
	}
	if ok, err := e2.svc.Deliver(bg, deliverReq(sb2.GetId())); ok || !strings.Contains(err.Error(), "sandbox is stopped") || *created2 != 0 {
		t.Errorf("stopped sandbox = %v / %v, created %d", ok, err, *created2)
	}
}

// WarmStart starts a running worker's agent once; uncontrolled or non-running
// sandboxes, and a service without registries, are left alone.
func TestWarmStart(t *testing.T) {
	e, _, created := deliverEnv(t, false)
	plain := e.sandbox(t, "11111111-a", "p", pb.SandboxState_SANDBOX_STATE_RUNNING)
	e.svc.WarmStart(plain)
	stopped := e.sandbox(t, "22222222-b", "s", pb.SandboxState_SANDBOX_STATE_STOPPED)
	stopped.Controller = controller("orch-1", "b")
	e.svc.WarmStart(stopped)
	if *created != 0 {
		t.Fatalf("uncontrolled / stopped sandboxes started: %d", *created)
	}
	w := e.sandbox(t, "33333333-c", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	w.Controller = controller("orch-1", "b")
	e.svc.WarmStart(w)
	e.svc.WarmStart(w)
	if *created != 1 || !e.svc.agents.Live(w.GetId()) {
		t.Errorf("warm start: created %d", *created)
	}
	// A worker with a run in flight is left alone (its agent may outlive a
	// daemon restart and still be working).
	busy := e.sandbox(t, "88888888-g", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	busy.Controller = controller("orch-1", "b")
	busy.Controller.Run = &pb.WorkerRun{Id: "run-1"}
	e.svc.WarmStart(busy)
	if *created != 1 {
		t.Errorf("a worker with a run in flight was started: created %d", *created)
	}
	// Without a terminal registry the agent registry starts it directly.
	e2 := newEnv(t, "")
	n := 0
	e2.svc.agents = agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { n++; return newRecSession(), nil })
	w2 := e2.sandbox(t, "44444444-d", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	w2.Controller = controller("orch-1", "b")
	e2.svc.WarmStart(w2)
	if n != 1 {
		t.Errorf("direct start: created %d", n)
	}
	// A factory failure is logged, never fatal; no registry at all is a no-op.
	e3 := newEnv(t, "")
	e3.svc.agents = agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) { return nil, errors.New("sbx down") })
	w3 := e3.sandbox(t, "55555555-e", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	w3.Controller = controller("orch-1", "b")
	e3.svc.WarmStart(w3)
	e4 := newEnv(t, "")
	w4 := e4.sandbox(t, "66666666-f", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	w4.Controller = controller("orch-1", "b")
	e4.svc.WarmStart(w4)
}

// Mark-free on an agent the start timeout parked vouches that it is at its
// prompt: it is IDLE and may be typed into (a lost SessionStart callback).
func TestClearNeedsInputVouchesForAnAgentStillStarting(t *testing.T) {
	e, _, _ := deliverEnv(t, false)
	sb := e.sandbox(t, "77777777-1", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error {
		x.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT // the start timeout parked it
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A fresh agent still STARTING (a stale view's mark-free) is not vouched for.
	if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error {
		x.Agent.Status = pb.AgentStatus_AGENT_STATUS_STARTING
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", ClearNeedsInput: true}); err != nil {
		t.Fatal(err)
	}
	if e.svc.agents.Ready(sb.GetId()) {
		t.Fatal("mark-free must not vouch for an agent still STARTING")
	}
	if stored, _ := e.reg.Get(sb.GetId()); stored.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_STARTING {
		t.Fatalf("STARTING changed to %v", stored.GetAgent().GetStatus())
	}
	if _, err := e.reg.Update(sb.GetId(), func(x *pb.Sandbox) error {
		x.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Another orchestrator's mark-free neither vouches nor frees.
	if _, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-2", ClearNeedsInput: true}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign mark-free = %v", err)
	}
	if e.svc.agents.Ready(sb.GetId()) {
		t.Fatal("a foreign mark-free must not mark the agent ready")
	}
	out, err := e.svc.Update(bg, &pb.UpdateWorkerRequest{SandboxId: sb.GetId(), OrchestratorId: "orch-1", ClearNeedsInput: true})
	if err != nil || out.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		t.Fatalf("mark-free = %v / %v", out.GetAgent().GetStatus(), err)
	}
	if !e.svc.agents.Ready(sb.GetId()) {
		t.Error("mark-free should mark the agent ready")
	}
}

// statusToRecord writes agent statuses onto the sandbox record, as the sandbox
// manager does in the daemon.
type statusToRecord struct{ reg *registry.Registry }

func (s statusToRecord) SetAgentStatus(id string, st pb.AgentStatus, _ time.Time) (*pb.Sandbox, error) {
	return s.reg.Update(id, func(x *pb.Sandbox) error {
		if x.Agent == nil {
			x.Agent = &pb.AgentSession{}
		}
		x.Agent.Status = st
		return nil
	})
}

// SWI-15 review: a hook that writes IDLE over an agent that never reported
// ready (its SessionStart lost, then the developer typed into it and its Stop
// fired) does not leave the worker reading free forever: the refused delivery
// puts STARTING back, so dispatch skips the worker until it is ready or the
// developer vouches for it.
func TestDeliverPutsAStrayIdleBackToStarting(t *testing.T) {
	e, sess, created := deliverEnv(t, false)
	e.svc.agents.SetStatusUpdater(statusToRecord{e.reg})
	sb := e.sandbox(t, "99999999-8", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot")}); err != nil {
		t.Fatal(err)
	}
	status := func() pb.AgentStatus {
		got, _ := e.reg.Get(sb.GetId())
		return got.GetAgent().GetStatus()
	}
	waitUntil := func(want pb.AgentStatus) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for status() != want && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := status(); got != want {
			t.Fatalf("agent status = %v, want %v", got, want)
		}
	}
	waitUntil(pb.AgentStatus_AGENT_STATUS_STARTING)
	// The Stop hook's IDLE lands; SessionStart never did.
	if _, err := (statusToRecord{e.reg}).SetAgentStatus(sb.GetId(), pb.AgentStatus_AGENT_STATUS_IDLE, time.Now()); err != nil {
		t.Fatal(err)
	}
	ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId()))
	if ok || !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "agent is starting") {
		t.Fatalf("deliver = %v / %v", ok, err)
	}
	waitUntil(pb.AgentStatus_AGENT_STATUS_STARTING)
	if *created != 1 || sess.typed() != "" {
		t.Errorf("created %d sessions, typed %q", *created, sess.typed())
	}
	// A STARTING record is refused without another write.
	if ok, _ := e.svc.Deliver(bg, deliverReq(sb.GetId())); ok {
		t.Error("an unready agent took a delivery")
	}
	// Once ready, the IDLE the ready hook writes sticks and the run goes in.
	e.svc.agents.MarkReady(sb.GetId())
	if _, err := (statusToRecord{e.reg}).SetAgentStatus(sb.GetId(), pb.AgentStatus_AGENT_STATUS_IDLE, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.svc.Deliver(bg, deliverReq(sb.GetId())); !ok || err != nil {
		t.Errorf("deliver once ready = %v / %v", ok, err)
	}
}

// Attaching gives the worker's broadcaster an agent screen even when it was
// created without one (SWI-17, research R2).
func TestAttachEnablesTheAgentScreen(t *testing.T) {
	e, _, _ := deliverEnv(t, false)
	sb := e.sandbox(t, "aaaaaaaa-5", "w", pb.SandboxState_SANDBOX_STATE_RUNNING)
	attachReady(t, e, sb.GetId())
	if _, _, ok := e.svc.terms.Screen(sb.GetId()); !ok {
		t.Fatal("attach should enable the agent screen on the warm-started broadcaster")
	}
	e.svc.terms.Close(sb.GetId())
}
