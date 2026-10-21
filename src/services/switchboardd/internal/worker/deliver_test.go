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
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/terminal"
)

// recSession records what is typed into the agent and blocks reads until closed.
type recSession struct {
	mu     sync.Mutex
	writes []string
	closed chan struct{}
}

func newRecSession() *recSession { return &recSession{closed: make(chan struct{})} }

func (s *recSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	created := 0
	agents := agent.NewRegistry(func(string, *pb.AgentSpec) (agent.Session, error) {
		created++
		if failPrompt {
			return nil, errors.New("no pty")
		}
		return sess, nil
	})
	e.svc.agents = agents
	e.svc.SetTerminals(terminal.NewRegistry(func(id string) (terminal.PTY, error) { return agents.Session(id, &pb.AgentSpec{}) }, 0, nil))
	return e, sess, &created
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
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot")}); err != nil {
		t.Fatal(err)
	}
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
		"agent needs input": {func(x *pb.Sandbox) { x.Agent.Status = pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT }, "agent is needs_input"},
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
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot")}); err != nil {
		t.Fatal(err)
	}
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
	// frame was typed through the developer's prompt path (body, then Enter).
	if n, _ := e.svc.terms.Counts(sb.GetId()); n != 0 || *created != 1 {
		t.Errorf("terminal session should exist with no attachment: counts %d created %d", n, *created)
	}
	want := "[switchboard orchestrator \"review-bot\" · run run-1a2b3c4d-7 · action \"review-pr\"]\n\nReview pull request 412 in acme/web.\n\nfocus: tests\n\n" + runFrameTail + "\r"
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
	if _, err := e.svc.Attach(bg, &pb.AttachControllerRequest{SandboxId: sb.GetId(), Controller: controller("orch-1", "review-bot")}); err != nil {
		t.Fatal(err)
	}
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
	prompt := EnsureDrained(func() *terminal.Registry { return terms }, agents.Prompt)
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
	for !strings.Contains(sess.typed(), "y\r") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(sess.typed(), "x\r") || !strings.Contains(sess.typed(), "y\r") {
		t.Errorf("typed = %q", sess.typed())
	}
}
