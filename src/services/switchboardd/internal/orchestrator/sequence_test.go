package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// stepScript answers the fake's DeliverWorkerStep.
type stepScript func(ctx context.Context, w *fakeWorker, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error)

// fakeDaemon is the owning daemon's step side as the orchestrator sees it
// (contracts/worker-step.md): the guard (checks 1–5, committing step_index,
// step_sent_at and halted = false only when every check passes), then a
// scripted outcome. Built-ins default to AGENT_LOCAL, other steps to
// MODEL_TURN.
type fakeDaemon struct {
	clock *fakeClock
	mu    sync.Mutex
	typed []string // "<sandbox>: <text>" for every step typed, in order
	// refuse, when set, refuses a step before the guard commits (FP).
	refuse func(req *pb.DeliverWorkerStepRequest) string
	// outcome, when set, answers a typed step (nil response = default).
	outcome func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error)
	// check answers check_only (default AGENT_LOCAL).
	check func(req *pb.DeliverWorkerStepRequest) *pb.DeliverWorkerStepResponse
	// hold, when set, is waited on by each typed call before it answers.
	hold func(req *pb.DeliverWorkerStepRequest)
}

func (d *fakeDaemon) set(fn func(d *fakeDaemon)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

func (d *fakeDaemon) typedTexts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.typed...)
}

// sendCheck mirrors the worker's guard 3a.
func sendCheck(run *pb.WorkerRun, req *pb.DeliverWorkerStepRequest) error {
	if run == nil {
		return nil
	}
	isClear := req.GetStep().GetKind() == pb.StepKind_STEP_KIND_CLEAR
	held := run.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	if held && (!isClear || !req.GetRetry()) {
		return status.Error(codes.FailedPrecondition, "precondition failed: cleanup dropped")
	}
	if run.GetStepSentAt() == nil {
		return nil
	}
	idx, rec := req.GetStepIndex(), run.GetStepIndex()
	if idx > rec || (idx == rec && req.GetRetry() && (run.GetHalted() || (isClear && held))) {
		return nil
	}
	return status.Error(codes.AlreadyExists, "step already sent")
}

func fp(msg string) error { return status.Error(codes.FailedPrecondition, "precondition failed: "+msg) }

func (d *fakeDaemon) script(ctx context.Context, w *fakeWorker, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	d.mu.Lock()
	refuse, outcome, check, hold := d.refuse, d.outcome, d.check, d.hold
	d.mu.Unlock()
	if req.GetCheckOnly() {
		if check != nil {
			return check(req), nil
		}
		return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL}, nil
	}
	w.mu.Lock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		w.mu.Unlock()
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	c := sb.GetController()
	run := c.GetRun()
	var err error
	switch {
	case sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
		err = fp("sandbox is not running")
	case c == nil || c.GetOrchestratorId() != req.GetOrchestratorId():
		err = fp("not this orchestrator's worker")
	case req.GetRestart():
		// T047: the restart reaps the orphan and starts a fresh agent.
		if sb.GetAgent().GetAgentCheck().GetState() != pb.AgentCheck_STATE_DETACHED {
			err = fp("not a restart halt")
		} else {
			sb.Agent.AgentCheck = nil
			sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_STARTING
			err = fp("agent is starting")
		}
	case req.GetFirst() && run != nil && run.GetId() != req.GetRunId():
		err = fp("run " + run.GetId() + " is already on this sandbox")
	case !req.GetFirst() && (run == nil || run.GetId() != req.GetRunId()):
		err = fp("run moved")
	}
	if err == nil {
		err = sendCheck(run, req)
	}
	if err == nil {
		switch {
		case sb.GetAgent().GetAgentCheck().GetState() != pb.AgentCheck_STATE_CLEAR:
			err = fp("agent detached by a daemon restart")
		case sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE:
			err = fp("agent is " + strings.ToLower(strings.TrimPrefix(sb.GetAgent().GetStatus().String(), "AGENT_STATUS_")))
		case refuse != nil:
			if why := refuse(req); why != "" {
				err = fp(why)
			}
		}
	}
	if err != nil {
		if req.GetRestart() {
			w.stamp(sb, sb.GetRevision()) // a write, as the registry stamps it
		}
		snap := proto.Clone(sb).(*pb.Sandbox)
		w.mu.Unlock()
		if req.GetRestart() {
			w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: snap}}
		}
		return nil, err
	}
	sentAt := timestamppb.New(d.clock.Now())
	if run == nil {
		run = &pb.WorkerRun{Id: req.GetRunId(), Action: req.GetAction()}
		c.Run = run
	}
	run.StepRole, run.StepIndex, run.StepSentAt, run.Halted = req.GetStep().GetRole(), req.GetStepIndex(), sentAt, false
	w.stamp(sb, sb.GetRevision())
	snap := proto.Clone(sb).(*pb.Sandbox)
	w.mu.Unlock()
	d.mu.Lock()
	d.typed = append(d.typed, req.GetSandboxId()+": "+req.GetStep().GetText())
	d.mu.Unlock()
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: snap}}
	if hold != nil {
		hold(req)
	}
	if outcome != nil {
		resp, err := outcome(req)
		if resp != nil && resp.SentAt == nil {
			resp.SentAt = sentAt
		}
		if resp != nil || err != nil {
			return resp, err
		}
	}
	out := pb.StepOutcome_STEP_OUTCOME_MODEL_TURN
	if builtin(req.GetStep().GetKind()) {
		out = pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL
	}
	return &pb.DeliverWorkerStepResponse{Outcome: out, SentAt: sentAt}, nil
}

// --- environment ---

// seqEnv is dispatchEnv with a step-capable fake daemon and the action given
// (review-pr's sequence when nil: /model opus, /effort medium, the action
// prompt, /clear).
func seqEnv(t *testing.T, n int, action *pb.Action) (*testEnv, *fakeWorker, *pb.Orchestrator, *runtime, *fakeDaemon) {
	t.Helper()
	env, w, o, rt := dispatchEnv(t, n, false)
	d := &fakeDaemon{clock: env.clock}
	w.mu.Lock()
	w.steps = d.script
	w.mu.Unlock()
	if action == nil {
		action = reviewPR()
	}
	if _, err := putAction(env.svc, o.GetId(), action); err != nil {
		t.Fatal(err)
	}
	return env, w, o, rt, d
}

func callSeq(t *testing.T, env *testEnv, o *pb.Orchestrator, number string) *pb.Run {
	t.Helper()
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"number": number, "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func runNow(env *testEnv, o *pb.Orchestrator, id string) *pb.Run {
	r, _ := env.svc.GetRun(o.GetId(), id)
	return r
}

// waitStep waits until a run is on step i with status st.
func waitStep(t *testing.T, env *testEnv, o *pb.Orchestrator, id string, i int32, st pb.StepStatus) *pb.Run {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		if r := runNow(env, o, id); r.GetStep() == i && r.GetStepStatus() == st {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	r := runNow(env, o, id)
	t.Fatalf("run %s at step %d %v (note %q, state %v), want step %d %v", id, r.GetStep(), r.GetStepStatus(), r.GetStepNote(), r.GetState(), i, st)
	return nil
}

// emit pushes a worker's record, changed by fn, as a status event.
func emit(w *fakeWorker, id string, fn func(sb *pb.Sandbox)) *pb.Sandbox {
	sb := w.update(id, func(sb *pb.Sandbox) {
		if fn != nil {
			fn(sb)
		}
	})
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}}
	return sb
}

// turnEnds reports the current step's turn as ended: IDLE, with a turn end
// after its recorded send.
func turnEnds(w *fakeWorker, id string) {
	emit(w, id, func(sb *pb.Sandbox) {
		sent := sb.GetController().GetRun().GetStepSentAt().AsTime()
		sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE
		sb.Agent.LastTurnEndAt = timestamppb.New(sent.Add(time.Second))
	})
}

func stepReqs(w *fakeWorker) []*pb.DeliverWorkerStepRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*pb.DeliverWorkerStepRequest(nil), w.stepReqs...)
}

func notifiedKinds(env *testEnv) []pb.NotificationKind {
	var out []pb.NotificationKind
	for _, n := range env.hub.notifications() {
		out = append(out, n.GetKind())
	}
	return out
}

func report(t *testing.T, rt *runtime, sandboxID string) error {
	t.Helper()
	_, err := rt.runTool("box", sandboxID, toolReportCompletion, `{"summary":"done"}`)
	return err
}

// --- order, advance, close and cleanup ---

// The default sequence runs in order, one step at a time, each only once the
// previous one finished; the report closes the run at once and Clear runs
// while the run holds the worker; the worker is free afterwards (FR-205,
// FR-214, FR-215, SC-004).
func TestSequenceDefaultOrderAndCleanup(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	run := callSeq(t, env, o, "1")
	r := waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	if r.GetState() != pb.RunState_RUN_STATE_RUNNING || r.GetSandboxId() != "wa" || r.GetStartedAt() == nil || r.GetStepKindSeen() != pb.StepObserved_STEP_OBSERVED_MODEL_TURN {
		t.Fatalf("run in its action turn = %+v", r)
	}
	want := []string{"wa: /model opus", "wa: /effort medium", "wa: Review pull request 1 in acme/web and leave review comments."}
	if got := d.typedTexts(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("typed = %q, want %q", got, want)
	}
	reqs := stepReqs(w)
	for i, req := range reqs {
		if req.GetFirst() != (i == 0) || req.GetStepIndex() != int32(i+1) || req.GetRetry() || req.GetRestart() {
			t.Errorf("request %d = first %v index %d retry %v", i, req.GetFirst(), req.GetStepIndex(), req.GetRetry())
		}
	}
	// A second run waits: the worker is busy with the first.
	run2 := callSeq(t, env, o, "2")
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	r = runNow(env, o, run.GetId())
	if r.GetState() != pb.RunState_RUN_STATE_COMPLETED || r.GetSummary() != "done" {
		t.Fatalf("the report closes the run at once: %+v", r)
	}
	// Clear runs while the closed run holds the worker; then the next run.
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	waitFor(t, func() bool { return runNow(env, o, run2.GetId()).GetState() == pb.RunState_RUN_STATE_RUNNING })
	got := d.typedTexts()
	if got[3] != "wa: /clear" || got[4] != "wa: /model opus" {
		t.Errorf("Clear precedes the next run's first step: %q", got)
	}
	if r := runNow(env, o, run.GetId()); r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE {
		t.Errorf("hold after cleanup = %v", r.GetHold())
	}
	if k := notifiedKinds(env); len(k) != 0 {
		t.Errorf("a clean sequence notifies nothing: %v", k)
	}
}

// While the closed run cleans up it holds the worker (CLEANING_UP on the run
// and the worker's record), is listed in progress, and the worker takes no run.
func TestCleanupHoldsTheWorker(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	// Clear can't be typed while the agent asks a question.
	d.set(func(d *fakeDaemon) {
		d.refuse = func(req *pb.DeliverWorkerStepRequest) string {
			if req.GetStep().GetKind() == pb.StepKind_STEP_KIND_CLEAR {
				return "input box not empty"
			}
			return ""
		}
	})
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	r := waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_PENDING)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_CLEANING_UP || r.GetHoldSince() == nil {
		t.Fatalf("cleaning run = %+v", r)
	}
	waitFor(t, func() bool {
		return w.get("wa").GetController().GetRun().GetHold() == pb.WorkerHold_WORKER_HOLD_CLEANING_UP
	})
	run2 := callSeq(t, env, o, "2")
	settle(t, rt)
	if got := runNow(env, o, run2.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED || got.GetSandboxId() != "" {
		t.Errorf("a held worker takes no run: %+v", got)
	}
	runs, _ := env.svc.ListRuns(o.GetId())
	if len(runs.GetInFlight()) != 1 || runs.GetInFlight()[0].GetId() != run.GetId() || len(runs.GetHistory()) != 0 {
		t.Errorf("a holding run is listed in progress: %+v", runs)
	}
	if sum := summaryOf(t, env, o.GetId()); sum.GetInFlight() != 1 {
		t.Errorf("summary in flight = %d", sum.GetInFlight())
	}
	if det, _ := env.svc.Get(o.GetId()); det.GetWorkers()[0].GetHold() != pb.WorkerHold_WORKER_HOLD_CLEANING_UP {
		t.Errorf("workers view hold = %v", det.GetWorkers()[0].GetHold())
	}
	// Refusals lasting stepWaitNote show on the run and the worker's record.
	env.clock.Advance(stepRetryEvery)
	env.clock.Advance(stepWaitNote)
	waitFor(t, func() bool { return runNow(env, o, run.GetId()).GetStepNote() == "waiting: input box not empty" })
	waitFor(t, func() bool {
		return w.get("wa").GetController().GetRun().GetStepNote() == "waiting: input box not empty"
	})
	// The box empties: the ticker retries, Clear runs, the worker is free.
	d.set(func(d *fakeDaemon) { d.refuse = nil })
	env.clock.Advance(stepRetryEvery)
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	waitFor(t, func() bool { return runNow(env, o, run2.GetId()).GetState() == pb.RunState_RUN_STATE_RUNNING })
}

// Idle between setup steps never hands the run to review, and a report
// before the action prompt was accepted is refused (FR-209, FR-211).
func TestSetupIsNeverReviewAndReportWaitsForTheAction(t *testing.T) {
	a := withSequence([]string{"/prepare {ticket}"}, nil)
	a.Name = "review-pr"
	env, w, o, rt, d := seqEnv(t, 1, a)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(req *pb.DeliverWorkerStepRequest) string {
			if req.GetStep().GetKind() == pb.StepKind_STEP_KIND_ACTION_PROMPT {
				return "input box not empty"
			}
			return ""
		}
	})
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"ticket": "T-1", "skill": "s", "number": "1", "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN) // the custom pre-prompt, a model turn
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_PENDING)
	env.clock.Advance(10 * reviewGrace)
	rt.reconcileInFlight("box")
	settle(t, rt)
	if r := runNow(env, o, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("idle in setup = %v, want RUNNING", r.GetState())
	}
	if err := report(t, rt, "wa"); err == nil || !strings.Contains(err.Error(), "the action prompt has not been delivered yet") {
		t.Errorf("early report = %v", err)
	}
	if r := runNow(env, o, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_RUNNING || r.GetSummary() != "" {
		t.Errorf("a refused report changes nothing: %+v", r)
	}
	// Once the action is accepted and idle, review follows as before.
	d.set(func(d *fakeDaemon) { d.refuse = nil })
	env.clock.Advance(stepRetryEvery)
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_TURN)
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	waitFor(t, func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.reviewTimers) == 1 })
	env.clock.Advance(reviewGrace)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_AWAITING_REVIEW })
	// Mark free: cleanup after the review (FR-214), the worker held meanwhile.
	if _, err := env.svc.MarkWorkerFree(o.GetId(), "wa", true, "meh"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 4, pb.StepStatus_STEP_STATUS_DONE)
	if r := runNow(env, o, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_FAILED || !r.GetClosedByDeveloper() {
		t.Errorf("marked failed = %+v", r)
	}
	if got := d.typedTexts(); got[len(got)-1] != "wa: /clear" {
		t.Errorf("cleanup after mark free: %q", got)
	}
}

// A step classified only by a needs-input report finishes on the next idle
// report, or on the input box staying empty and ready for inputOnlyIdle.
func TestInputOnlyStepFinishes(t *testing.T) {
	a := withSequence([]string{"/ask {ticket}"}, nil)
	a.Name = "review-pr"
	env, w, o, rt, d := seqEnv(t, 1, a)
	d.set(func(d *fakeDaemon) {
		d.outcome = func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
			if req.GetStep().GetKind() == pb.StepKind_STEP_KIND_CUSTOM {
				return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY}, nil
			}
			return nil, nil
		}
	})
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"ticket": "T-1", "skill": "s", "number": "1", "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	if r.GetStepKindSeen() != pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY {
		t.Fatalf("kind = %v", r.GetStepKindSeen())
	}
	// The box goes empty and ready after the send; 60 s later the step ends.
	emit(w, "wa", func(sb *pb.Sandbox) {
		sb.Agent.InputIdleSince = timestamppb.New(sb.GetController().GetRun().GetStepSentAt().AsTime().Add(time.Second))
	})
	waitFor(t, func() bool {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		m := rt.seq.marks[run.GetId()]
		return m != nil && !m.idleSeen.IsZero() && m.wake != nil
	})
	env.clock.Advance(inputOnlyIdle / 2)
	if r := runNow(env, o, run.GetId()); r.GetStep() != 2 {
		t.Fatalf("finished before the idle period: step %d", r.GetStep())
	}
	env.clock.Advance(inputOnlyIdle / 2)
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_TURN)
}

// --- delivery: refusals, kicks during a call, single flight ---

// A refused first step leaves the run QUEUED and claimed; it is retried on
// the ticker, and the claim keeps a second run off the worker. Cancelling the
// claimed run frees the worker (T019).
func TestClaimRefusalsAndCancel(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(*pb.DeliverWorkerStepRequest) string { return "input box not empty" }
	})
	run := callSeq(t, env, o, "1")
	waitFor(t, func() bool { return len(stepReqs(w)) >= 1 })
	r := waitStep(t, env, o, run.GetId(), 0, pb.StepStatus_STEP_STATUS_PENDING)
	if r.GetState() != pb.RunState_RUN_STATE_QUEUED || r.GetSandboxId() != "wa" {
		t.Fatalf("claimed run = %+v", r)
	}
	run2 := callSeq(t, env, o, "2")
	settle(t, rt)
	if got := runNow(env, o, run2.GetId()); got.GetSandboxId() != "" {
		t.Errorf("a claimed worker takes no second run: %+v", got)
	}
	env.clock.Advance(stepRetryEvery)
	waitFor(t, func() bool { return len(stepReqs(w)) >= 2 })
	env.clock.Advance(stepWaitNote)
	waitFor(t, func() bool { return runNow(env, o, run.GetId()).GetStepNote() == "waiting: input box not empty" })
	// Cancel: the claim is released and the next run claims the worker.
	if _, err := env.svc.CancelRun(o.GetId(), run.GetId()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runNow(env, o, run2.GetId()).GetSandboxId() == "wa" })
	if r := runNow(env, o, run.GetId()); r.GetStepStatus() != pb.StepStatus_STEP_STATUS_DONE || r.GetState() != pb.RunState_RUN_STATE_CANCELLED {
		t.Errorf("cancelled claim = %+v", r)
	}
	d.set(func(d *fakeDaemon) { d.refuse = nil })
	env.clock.Advance(stepRetryEvery)
	waitStep(t, env, o, run2.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	for _, txt := range d.typedTexts() {
		if strings.Contains(txt, "pull request 1 ") {
			t.Errorf("the cancelled run was typed: %q", txt)
		}
	}
}

// A call in flight blocks neither dispatch to another worker nor kicks: a kick
// during the call is coalesced, never lost, so a turn that ended during the
// call finishes; the step is SENDING during the call and typed once.
func TestStepCallSingleFlight(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 2, nil)
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	var once sync.Once
	d.set(func(d *fakeDaemon) {
		d.hold = func(req *pb.DeliverWorkerStepRequest) {
			if req.GetSandboxId() == "wa" && req.GetStep().GetKind() == pb.StepKind_STEP_KIND_ACTION_PROMPT {
				once.Do(func() { entered <- struct{}{}; <-release })
			}
		}
	})
	run := callSeq(t, env, o, "1")
	<-entered
	if r := runNow(env, o, run.GetId()); r.GetStepStatus() != pb.StepStatus_STEP_STATUS_SENDING || r.GetStep() != 2 {
		t.Fatalf("during the call the step is SENDING: %+v", r)
	}
	// Another run is delivered to the other worker meanwhile.
	run2 := callSeq(t, env, o, "2")
	waitStep(t, env, o, run2.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	// The action turn ends during the call, with several kicks.
	turnEnds(w, "wa")
	emit(w, "wa", nil)
	emit(w, "wa", nil)
	time.Sleep(20 * time.Millisecond)
	close(release)
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	n := 0
	for _, txt := range d.typedTexts() {
		if strings.HasPrefix(txt, "wa: Review") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the action prompt was typed %d times", n)
	}
}

// --- lost answers (MVP, T022) ---

// A transport error leaves the step SENDING, never failed nor re-typed; it is
// resolved from a fresh read of the daemon's record: typed → SENT, then
// finished by evidence; not typed → PENDING and sent again.
func TestLostAnswersResolveFromTheRecord(t *testing.T) {
	env, _, o, rt, d := seqEnv(t, 1, nil)
	lost := map[pb.StepKind]bool{pb.StepKind_STEP_KIND_MODEL: true, pb.StepKind_STEP_KIND_ACTION_PROMPT: true}
	var mu sync.Mutex
	d.set(func(d *fakeDaemon) {
		d.outcome = func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			if lost[req.GetStep().GetKind()] {
				delete(lost, req.GetStep().GetKind())
				return nil, status.Error(codes.Unavailable, "link blip")
			}
			return nil, nil
		}
	})
	run := callSeq(t, env, o, "1")
	// Both answers are lost after the steps were typed: each is resolved from
	// a fresh read of the record (typed → SENT), never failed nor re-typed;
	// the built-in is confirmed by check_only, so the run moves on.
	r := waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_SENT)
	if r.GetState() != pb.RunState_RUN_STATE_RUNNING || r.GetStepSentAt() == nil {
		t.Fatalf("resolved lost answers = %+v", r)
	}
	// The action prompt was typed, only its answer was lost: the agent's
	// report is accepted (FR-211).
	if err := report(t, rt, "wa"); err != nil {
		t.Fatalf("report while the action answer is lost = %v", err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	n := 0
	for _, txt := range d.typedTexts() {
		if txt == "wa: /model opus" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("/model typed %d times: %q", n, d.typedTexts())
	}
}

// A lost answer whose step was never typed resolves to PENDING and is sent
// again with a new send number.
func TestLostAnswerNotTypedIsResent(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	first := true
	d.set(func(d *fakeDaemon) {
		d.refuse = func(req *pb.DeliverWorkerStepRequest) string {
			if first {
				first = false
				return "" // passes; the next hook drops the answer
			}
			return ""
		}
	})
	// Fail the very first call at the transport, before the guard.
	w.mu.Lock()
	inner := w.steps
	calls := 0
	w.steps = func(ctx context.Context, w *fakeWorker, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
		calls++
		if calls == 1 {
			return nil, status.Error(codes.Unavailable, "connection reset")
		}
		return inner(ctx, w, req)
	}
	w.mu.Unlock()
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 0, pb.StepStatus_STEP_STATUS_SENDING)
	emit(w, "wa", nil)
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	reqs := stepReqs(w)
	if len(reqs) < 2 || reqs[1].GetStepIndex() <= reqs[0].GetStepIndex() || !reqs[1].GetFirst() {
		t.Errorf("resend = %+v", reqs)
	}
}

// ALREADY_EXISTS: the step was typed by an earlier call; it is SENT with the
// recorded time and resolved by evidence (a submit after it → TURN).
func TestStepSentResolvesByEvidence(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) {
		d.outcome = func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
			if req.GetStep().GetKind() == pb.StepKind_STEP_KIND_ACTION_PROMPT {
				return nil, status.Error(codes.AlreadyExists, "step already sent")
			}
			return nil, nil
		}
	})
	run := callSeq(t, env, o, "1")
	r := waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_SENT)
	if r.GetStepSentAt() == nil {
		t.Fatal("SENT carries the recorded time")
	}
	emit(w, "wa", func(sb *pb.Sandbox) {
		sb.Agent.LastSubmitAt = timestamppb.New(sb.GetController().GetRun().GetStepSentAt().AsTime().Add(time.Second))
	})
	r = waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	if r.GetStepKindSeen() != pb.StepObserved_STEP_OBSERVED_MODEL_TURN {
		t.Errorf("kind = %v", r.GetStepKindSeen())
	}
}

// UNCONFIRMED: a built-in is read back with check_only; a custom step with no
// evidence halts after sentGrace, marked as possibly run.
func TestUnconfirmedResolution(t *testing.T) {
	a := withSequence([]string{"/prepare {ticket}"}, nil)
	a.Name = "review-pr"
	env, _, o, _, d := seqEnv(t, 1, a)
	d.set(func(d *fakeDaemon) {
		d.outcome = func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
			if req.GetStep().GetKind() != pb.StepKind_STEP_KIND_ACTION_PROMPT {
				return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED, Note: "screen resyncing"}, nil
			}
			return nil, nil
		}
	})
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"ticket": "T-1", "skill": "s", "number": "1", "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_SENT)
	if !r.GetStepUnconfirmed() {
		t.Fatalf("unconfirmed custom step = %+v", r)
	}
	waitFor(t, func() bool { return env.clock.pending() > 0 })
	env.clock.Advance(sentGrace)
	r = waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_HALTED)
	if !r.GetStepUnconfirmed() || r.GetStepNote() != unconfirmedNote {
		t.Errorf("halted unconfirmed = %+v", r)
	}
	// Both built-ins were confirmed by check_only (steps 0 and 1 passed).
}

// --- mixed versions ---

// A worker whose daemon predates step delivery hands the run back; it goes to
// another worker, and fails only when no eligible worker supports steps.
func TestUnsupportedWorkerRequeues(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 2, nil)
	inner := d.script
	w.mu.Lock()
	w.steps = func(ctx context.Context, w *fakeWorker, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
		if req.GetSandboxId() == "wa" {
			return nil, status.Error(codes.Unimplemented, "unknown method DeliverWorkerStep")
		}
		return inner(ctx, w, req)
	}
	w.mu.Unlock()
	run := callSeq(t, env, o, "1")
	r := waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	if r.GetSandboxId() != "wb" {
		t.Errorf("run went to %s, want wb", r.GetSandboxId())
	}
	// wb busy, wa unsupported: a run whose only allowed worker is wa fails.
	ids := seedLanes(t, rt, map[string][]string{"old": {"wa"}})
	tagged := reviewPR()
	tagged.Name, tagged.LaneIds, tagged.LaneMode = "tagged", []string{ids["old"]}, pb.LaneMode_LANE_MODE_EXCLUSIVE
	if _, err := putAction(env.svc, o.GetId(), tagged); err != nil {
		t.Fatal(err)
	}
	run2, err := env.svc.CallAction(o.GetId(), "tagged", map[string]string{"number": "2", "repo": "r"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return runNow(env, o, run2.GetId()).GetState() == pb.RunState_RUN_STATE_FAILED })
	if got := runNow(env, o, run2.GetId()).GetReason(); got != unsupportedReason {
		t.Errorf("reason = %q", got)
	}
}

// --- halts and ResolveRunStep ---

func notAccepted(kind pb.StepKind, note string) func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	return func(req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
		if req.GetStep().GetKind() == kind && !req.GetRetry() {
			return &pb.DeliverWorkerStepResponse{Outcome: pb.StepOutcome_STEP_OUTCOME_NOT_ACCEPTED, Note: note}, nil
		}
		return nil, nil
	}
}

// A first step not accepted gives a RUNNING, HALTED run, raises
// RUN_STEP_NOT_ACCEPTED and marks the worker's record halted; R types it once
// more with the same send number.
func TestHaltedFirstStepRetry(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) { d.outcome = notAccepted(pb.StepKind_STEP_KIND_MODEL, "Model 'bogus' not found") })
	run := callSeq(t, env, o, "1")
	r := waitStep(t, env, o, run.GetId(), 0, pb.StepStatus_STEP_STATUS_HALTED)
	if r.GetState() != pb.RunState_RUN_STATE_RUNNING || r.GetStepNote() != "Model 'bogus' not found" {
		t.Fatalf("halted first step = %+v", r)
	}
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun().GetHalted() })
	waitFor(t, func() bool {
		k := notifiedKinds(env)
		return len(k) == 1 && k[0] == pb.NotificationKind_NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED
	})
	runs, _ := env.svc.ListRuns(o.GetId())
	if len(runs.GetInFlight()) != 1 {
		t.Errorf("a halted run is in progress: %+v", runs)
	}
	// No review while halted.
	env.clock.Advance(10 * reviewGrace)
	rt.reconcileInFlight("box")
	if st := stateOf(env, o.GetId(), run.GetId()); st != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("halted run state = %v", st)
	}
	before := len(stepReqs(w))
	got, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 0, pb.StepResolution_STEP_RESOLUTION_RETRY)
	if err != nil || got.GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED {
		t.Fatalf("retry = %+v / %v", got, err)
	}
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	retry := stepReqs(w)[before]
	if !retry.GetRetry() || retry.GetStepIndex() != 1 || retry.GetStep().GetKind() != pb.StepKind_STEP_KIND_MODEL {
		t.Errorf("retry request = %+v", retry)
	}
	// A second retry of the same step is refused: the run moved on.
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 0, pb.StepResolution_STEP_RESOLUTION_RETRY); err == nil {
		t.Error("a stale retry is refused")
	}
}

// S skips a halted setup step; X on a setup step fails the run naming it,
// and Clear (only) follows, since a step reached the agent (FR-212, FR-214).
func TestHaltedSetupSkipAndFail(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) { d.outcome = notAccepted(pb.StepKind_STEP_KIND_EFFORT, "picker opened") })
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 1, pb.StepStatus_STEP_STATUS_HALTED)
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 1, pb.StepResolution_STEP_RESOLUTION_SKIP); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	waitFor(t, func() bool { return !w.get("wa").GetController().GetRun().GetHalted() })

	// A second run, failed at its halted model step.
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	d.set(func(d *fakeDaemon) { d.outcome = notAccepted(pb.StepKind_STEP_KIND_MODEL, "Model 'x' not found") })
	run2 := callSeq(t, env, o, "2")
	if _, err := env.svc.MarkWorkerFree(o.GetId(), "wa", false, ""); err == nil {
		t.Fatal("not awaiting review yet")
	}
	waitFor(t, func() bool { rt := mustRT(t, env, o); return rt != nil })
	if err := report(t, mustRT(t, env, o), "wa"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run2.GetId(), 0, pb.StepStatus_STEP_STATUS_HALTED)
	if _, err := env.svc.ResolveRunStep(o.GetId(), run2.GetId(), 0, pb.StepResolution_STEP_RESOLUTION_FAIL); err != nil {
		t.Fatal(err)
	}
	r := runNow(env, o, run2.GetId())
	if r.GetState() != pb.RunState_RUN_STATE_FAILED || r.GetReason() != "failed by the developer at setup step 1/2 (/model opus)" {
		t.Fatalf("failed run = %+v", r)
	}
	waitStep(t, env, o, run2.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	typed := d.typedTexts()
	if typed[len(typed)-1] != "wa: /clear" || strings.Contains(strings.Join(typed, "|"), "pull request 2") {
		t.Errorf("Clear only follows the failed setup: %q", typed)
	}
}

func mustRT(t *testing.T, env *testEnv, o *pb.Orchestrator) *runtime {
	t.Helper()
	rt, err := env.svc.runtimeFor(o.GetId())
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// A halted action step can't be skipped; X fails the run naming it, and Clear
// follows. On Clear itself: S refused, X holds the sandbox for the developer
// with R still offered, and the daemon's release ends the hold and drops a
// retried Clear still waiting (FR-216, FR-222).
func TestHaltedActionAndClear(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) {
		d.outcome = notAccepted(pb.StepKind_STEP_KIND_ACTION_PROMPT, "text still in the input box")
	})
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_HALTED)
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 2, pb.StepResolution_STEP_RESOLUTION_SKIP); err == nil || !strings.Contains(err.Error(), "the action prompt cannot be skipped") {
		t.Errorf("skip action = %v", err)
	}
	d.set(func(d *fakeDaemon) {
		d.outcome = notAccepted(pb.StepKind_STEP_KIND_CLEAR, "no new session after /clear")
	})
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 2, pb.StepResolution_STEP_RESOLUTION_FAIL); err != nil {
		t.Fatal(err)
	}
	if r := runNow(env, o, run.GetId()); r.GetReason() != "failed by the developer at the action prompt" {
		t.Errorf("reason = %q", r.GetReason())
	}
	r := waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_HALTED)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_CLEANING_UP {
		t.Fatalf("halted Clear = %+v", r)
	}
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 3, pb.StepResolution_STEP_RESOLUTION_SKIP); err == nil || !strings.Contains(err.Error(), "Clear cannot be skipped") {
		t.Errorf("skip Clear = %v", err)
	}
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 3, pb.StepResolution_STEP_RESOLUTION_FAIL); err != nil {
		t.Fatal(err)
	}
	r = runNow(env, o, run.GetId())
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_DONE || r.GetStep() != 3 {
		t.Fatalf("X on Clear = %+v", r)
	}
	waitFor(t, func() bool {
		return w.get("wa").GetController().GetRun().GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	})
	// R stays: a retried Clear waits for the agent (busy here) …
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING })
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 3, pb.StepResolution_STEP_RESOLUTION_RETRY); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_PENDING)
	// … and the developer's fresh session (the daemon clears the record) ends
	// the hold: the waiting Clear is dropped and the run is done.
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Controller.Run = nil; sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE })
	r = waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE {
		t.Errorf("released hold = %v", r.GetHold())
	}
	runs, _ := env.svc.ListRuns(o.GetId())
	if len(runs.GetHistory()) != 1 || len(runs.GetInFlight()) != 0 {
		t.Errorf("a released run is history: %+v", runs)
	}
}

// After X on Clear, R is accepted under the hold: the retried Clear carries
// retry, and its fresh session (the daemon releasing) finishes the run.
func TestRetriedClearUnderHold(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) {
		d.outcome = notAccepted(pb.StepKind_STEP_KIND_CLEAR, "no new session after /clear")
	})
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_HALTED)
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 3, pb.StepResolution_STEP_RESOLUTION_FAIL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return w.get("wa").GetController().GetRun().GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	})
	before := len(stepReqs(w))
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 3, pb.StepResolution_STEP_RESOLUTION_RETRY); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(stepReqs(w)) > before })
	if req := stepReqs(w)[before]; !req.GetRetry() || req.GetStep().GetKind() != pb.StepKind_STEP_KIND_CLEAR {
		t.Errorf("retried Clear = %+v", req)
	}
	// The retried Clear passed; the daemon ended the hold on its fresh session.
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Controller.Run = nil })
	r := waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE {
		t.Errorf("hold = %v", r.GetHold())
	}
}

// A custom post-prompt failed with X drops the remaining custom steps; Clear
// still runs and the worker is freed.
func TestFailCustomCleanup(t *testing.T) {
	a := withSequence(nil, []string{"Summarise {ticket}", "Tidy up"})
	a.Name = "review-pr"
	env, w, o, rt, d := seqEnv(t, 1, a)
	d.set(func(d *fakeDaemon) {
		d.outcome = notAccepted(pb.StepKind_STEP_KIND_CUSTOM, "text still in the input box")
	})
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"ticket": "T-1", "skill": "s", "number": "1", "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_HALTED)
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 3, pb.StepResolution_STEP_RESOLUTION_FAIL); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 5, pb.StepStatus_STEP_STATUS_DONE)
	if r := runNow(env, o, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("X on a post-prompt keeps the outcome: %v", r.GetState())
	}
	typed := strings.Join(d.typedTexts(), "|")
	if strings.Contains(typed, "Tidy up") || !strings.HasSuffix(typed, "wa: /clear") {
		t.Errorf("typed = %q", typed)
	}
}

// --- exits and stops ---

// An exit during setup fails the run naming the step; nothing follows, the
// agent being gone (FR-212). The record is cleared.
func TestExitDuringSetup(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) { d.outcome = notAccepted(pb.StepKind_STEP_KIND_EFFORT, "picker opened") })
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 1, pb.StepStatus_STEP_STATUS_HALTED)
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_EXITED })
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_FAILED })
	r := runNow(env, o, run.GetId())
	if r.GetReason() != "agent exited during setup step 2/2 (/effort medium)" || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_DONE {
		t.Errorf("failed run = %+v", r)
	}
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun() == nil })
}

// A stop during cleanup drops the remaining steps and holds the sandbox for
// the developer (FR-216); a close on a stopped sandbox does the same.
func TestCleanupOnAStoppedSandbox(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(*pb.DeliverWorkerStepRequest) string { return "agent is starting" }
	})
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_PENDING)
	emit(w, "wa", func(sb *pb.Sandbox) { sb.State = pb.SandboxState_SANDBOX_STATE_STOPPED })
	r := waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		t.Fatalf("stop during cleanup = %+v", r)
	}

	// Close on a stopped sandbox (kept awaiting review): no cleanup, held.
	env2, w2, o2, rt2, _ := seqEnv(t, 1, nil)
	run2 := callSeq(t, env2, o2, "1")
	waitStep(t, env2, o2, run2.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	turnEnds(w2, "wa")
	waitFor(t, func() bool { rt2.mu.Lock(); defer rt2.mu.Unlock(); return len(rt2.reviewTimers) == 1 })
	env2.clock.Advance(reviewGrace)
	waitFor(t, func() bool { return stateOf(env2, o2.GetId(), run2.GetId()) == pb.RunState_RUN_STATE_AWAITING_REVIEW })
	emit(w2, "wa", func(sb *pb.Sandbox) { sb.State = pb.SandboxState_SANDBOX_STATE_STOPPED })
	waitFor(t, func() bool { return rt2.sandboxRecord("wa").GetState() == pb.SandboxState_SANDBOX_STATE_STOPPED })
	if _, err := env2.svc.MarkWorkerFree(o2.GetId(), "wa", false, ""); err != nil {
		t.Fatal(err)
	}
	r = runNow(env2, o2, run2.GetId())
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER || r.GetStep() != 3 || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_DONE {
		t.Errorf("close on a stopped sandbox = %+v", r)
	}
}

// An exit while cleanup runs (the daemon raising the hold) is adopted: the
// steps are dropped and the hold is NEEDS_DEVELOPER.
func TestExitDuringCleanupAdoptsTheDaemonsHold(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(*pb.DeliverWorkerStepRequest) string { return "input box not empty" }
	})
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_PENDING)
	waitFor(t, func() bool {
		return w.get("wa").GetController().GetRun().GetHold() == pb.WorkerHold_WORKER_HOLD_CLEANING_UP
	})
	emit(w, "wa", func(sb *pb.Sandbox) {
		sb.Controller.Run.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
		sb.Controller.Run.HoldGen = 7
	})
	r := waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		t.Errorf("adopted hold = %v", r.GetHold())
	}
	if wr := w.get("wa").GetController().GetRun(); wr.GetHoldGen() != 7 || wr.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		t.Errorf("worker record = %+v", wr)
	}
}

// --- holding runs: removal, retention, reasoning ---

// Removing a held sandbox ends its run's cleanup; failRunsOn returns.
func TestRemovalEndsTheHold(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(*pb.DeliverWorkerStepRequest) string { return "input box not empty" }
	})
	turnEnds(w, "wa")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_AWAIT_CLOSE)
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_PENDING)
	rt.failRunsOn("wa", "worker detached") // a holding run is not in flight
	w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "wa"}}}
	r := waitStep(t, env, o, run.GetId(), 3, pb.StepStatus_STEP_STATUS_DONE)
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE || r.GetState() != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("removed hold = %+v", r)
	}
}

// Retention never prunes a run holding its sandbox.
func TestRetentionSkipsHoldingRuns(t *testing.T) {
	env, _, o, _, _ := seqEnv(t, 0, nil)
	put := func(id string, hold pb.WorkerHold) {
		if err := env.reg.PutRun(&pb.Run{Id: id, OrchestratorId: o.GetId(), State: pb.RunState_RUN_STATE_COMPLETED, Steps: []*pb.RunStep{{}},
			SandboxId: "wa", Hold: hold, StepStatus: pb.StepStatus_STEP_STATUS_DONE}); err != nil {
			t.Fatal(err)
		}
	}
	held := runID(o.GetId(), 1)
	put(held, pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER)
	for i := 0; i <= historyKeep; i++ {
		put(runID(o.GetId(), uint64(i+2)), pb.WorkerHold_WORKER_HOLD_NONE)
	}
	if err := env.svc.trimHistory(o.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reg.GetRun(o.GetId(), held); err != nil {
		t.Errorf("the holding run was pruned: %v", err)
	}
	runs, _ := env.reg.ListRuns(o.GetId())
	if len(runs) != historyKeep+1 {
		t.Errorf("kept %d runs, want %d", len(runs), historyKeep+1)
	}
}

// A review-flag push during a hold keeps the hold, halted and the step fields.
func TestWorkerRunForKeepsHoldAndSteps(t *testing.T) {
	sent := timestamppb.New(time.Unix(100, 0))
	live := &pb.WorkerRun{Id: "r", Action: "a", StepRole: pb.StepRole_STEP_ROLE_CLEANUP, StepIndex: 4, StepSentAt: sent,
		Hold: pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER, HoldGen: 3}
	run := &pb.Run{Id: "r", Action: "a", State: pb.RunState_RUN_STATE_COMPLETED, Steps: []*pb.RunStep{{}}, Hold: pb.WorkerHold_WORKER_HOLD_CLEANING_UP,
		StepStatus: pb.StepStatus_STEP_STATUS_HALTED, StepNote: "picker opened"}
	got := workerRunFor(run, live)
	if got.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER || !got.GetHalted() || got.GetStepIndex() != 4 ||
		got.GetStepRole() != pb.StepRole_STEP_ROLE_CLEANUP || got.GetHoldGen() != 3 || got.GetStepNote() != "picker opened" {
		t.Errorf("workerRunFor = %+v", got)
	}
	// A retry pending keeps the record halted, so the worker accepts it.
	run.StepStatus, run.StepSentAt = pb.StepStatus_STEP_STATUS_PENDING, sent
	if !workerRunFor(run, live).GetHalted() {
		t.Error("a pending retry stays halted on the worker")
	}
	run.StepSentAt = nil
	if workerRunFor(run, live).GetHalted() {
		t.Error("a new step is not halted")
	}
}

// A worker record naming a finished run with no step work is cleared.
func TestStaleRecordCleared(t *testing.T) {
	env, w, o, _, _ := seqEnv(t, 1, nil)
	old, gone := runID(o.GetId(), 90), runID(o.GetId(), 91)
	if err := env.reg.PutRun(&pb.Run{Id: old, OrchestratorId: o.GetId(), State: pb.RunState_RUN_STATE_COMPLETED}); err != nil {
		t.Fatal(err)
	}
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Controller.Run = &pb.WorkerRun{Id: old} })
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun() == nil })
	// And an unknown one.
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Controller.Run = &pb.WorkerRun{Id: gone} })
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun() == nil })
}

// --- owning-daemon restart: a detached agent (T047, research R14) ---

// An orphan's completion report closes an ACTION run; its cleanup can't be
// typed, so the hold is NEEDS_DEVELOPER with the detached note, and the fresh
// session of t → Replace ends it.
func TestOrphanActionRunCloses(t *testing.T) {
	a := withSequence(nil, []string{"Summarise {ticket}"})
	a.Name = "review-pr"
	env, w, o, rt, d := seqEnv(t, 1, a)
	run, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"ticket": "T-1", "skill": "s", "number": "1", "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	emit(w, "wa", func(sb *pb.Sandbox) {
		sb.Agent.AgentCheck = &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: pb.AgentStatus_AGENT_STATUS_WORKING}
	})
	waitFor(t, func() bool { return agentCheck(rt.sandboxRecord("wa")) == pb.AgentCheck_STATE_DETACHED })
	if r := runNow(env, o, run.GetId()); r.GetStepStatus() != pb.StepStatus_STEP_STATUS_TURN {
		t.Fatalf("an ACTION run is left in flight: %+v", r)
	}
	if err := report(t, rt, "wa"); err != nil {
		t.Fatal(err)
	}
	r := runNow(env, o, run.GetId())
	if r.GetState() != pb.RunState_RUN_STATE_COMPLETED || r.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER ||
		r.GetStepNote() != orphanCloseNote+"; "+droppedPostNote {
		t.Fatalf("orphan close = %+v", r)
	}
	waitFor(t, func() bool {
		return w.get("wa").GetController().GetRun().GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	})
	// Replace's fresh startup: the daemon ends the hold.
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Controller.Run = nil; sb.Agent.AgentCheck = nil })
	waitStep(t, env, o, run.GetId(), 4, pb.StepStatus_STEP_STATUS_DONE)
	for _, txt := range d.typedTexts() {
		if strings.Contains(txt, "Summarise") || strings.Contains(txt, "/clear") {
			t.Errorf("nothing is typed into an orphan: %q", txt)
		}
	}
}

// Replace on an ACTION orphan fails the run with the daemon's reason and
// sends no post-prompt and no Clear. A check-failed note is never a reason.
func TestOrphanReplaceFailsTheRun(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	const replaced = "agent replaced by the developer after a daemon restart"
	emit(w, "wa", func(sb *pb.Sandbox) {
		sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_EXITED
		sb.Controller.Run.StepNote = replaced
	})
	waitFor(t, func() bool { return stateOf(env, o.GetId(), run.GetId()) == pb.RunState_RUN_STATE_FAILED })
	r := runNow(env, o, run.GetId())
	if r.GetReason() != replaced || r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_DONE {
		t.Errorf("replaced run = %+v", r)
	}
	if typed := d.typedTexts(); len(typed) != 3 {
		t.Errorf("nothing after a replace: %q", typed)
	}
	// A check-failed note on the record is not a reason.
	sb := &pb.Sandbox{Controller: &pb.SandboxController{Run: &pb.WorkerRun{Id: "x", StepNote: "can't confirm the agent is still running: boom"}}}
	if got := exitReason(&pb.Run{Id: "x"}, sb); got != "agent exited" {
		t.Errorf("exit reason = %q", got)
	}
}

// A setup step halts when its agent is detached; R stops the orphan and
// starts a fresh agent (restart), then re-sends every pre-step from step 0
// as new sends, so the action prompt follows /model and /effort on the fresh
// agent (SC-001). S is refused for such a halt; X fails the run and leaves
// the sandbox held for the developer.
func TestOrphanSetupRestart(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(req *pb.DeliverWorkerStepRequest) string { return pickKind(req, pb.StepKind_STEP_KIND_EFFORT) }
	})
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 1, pb.StepStatus_STEP_STATUS_PENDING)
	emit(w, "wa", func(sb *pb.Sandbox) {
		sb.Agent.AgentCheck = &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED, LastStatus: pb.AgentStatus_AGENT_STATUS_IDLE}
	})
	r := waitStep(t, env, o, run.GetId(), 1, pb.StepStatus_STEP_STATUS_HALTED)
	if r.GetStepNote() != orphanSetupNote || r.GetStepSentBeforeRestart() {
		t.Fatalf("restart halt = %+v", r)
	}
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 1, pb.StepResolution_STEP_RESOLUTION_SKIP); err == nil || !strings.Contains(err.Error(), "restart setup with R") {
		t.Errorf("skip on a restart halt = %v", err)
	}
	d.set(func(d *fakeDaemon) { d.refuse = nil })
	before := len(stepReqs(w))
	if _, err := env.svc.ResolveRunStep(o.GetId(), run.GetId(), 1, pb.StepResolution_STEP_RESOLUTION_RETRY); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(stepReqs(w)) > before })
	restart := stepReqs(w)[before]
	if !restart.GetRestart() || restart.GetStep().GetKind() != pb.StepKind_STEP_KIND_MODEL {
		t.Fatalf("restart request = %+v", restart)
	}
	// The fresh agent comes up.
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE })
	waitStep(t, env, o, run.GetId(), 2, pb.StepStatus_STEP_STATUS_TURN)
	typed := d.typedTexts()
	want := []string{"wa: /model opus", "wa: /model opus", "wa: /effort medium", "wa: Review pull request 1 in acme/web and leave review comments."}
	if strings.Join(typed, "|") != strings.Join(want, "|") {
		t.Errorf("typed = %q, want %q", typed, want)
	}
	reqs := stepReqs(w)
	for i := 1; i < len(reqs); i++ {
		if reqs[i].GetStepIndex() < reqs[i-1].GetStepIndex() {
			t.Errorf("send numbers only grow: %d then %d", reqs[i-1].GetStepIndex(), reqs[i].GetStepIndex())
		}
	}

	// X on a restart halt: failed, and the orphan holds the sandbox.
	env2, w2, o2, _, d2 := seqEnv(t, 1, nil)
	d2.set(func(d *fakeDaemon) {
		d.refuse = func(req *pb.DeliverWorkerStepRequest) string { return pickKind(req, pb.StepKind_STEP_KIND_EFFORT) }
	})
	run2 := callSeq(t, env2, o2, "1")
	waitStep(t, env2, o2, run2.GetId(), 1, pb.StepStatus_STEP_STATUS_PENDING)
	emit(w2, "wa", func(sb *pb.Sandbox) { sb.Agent.AgentCheck = &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED} })
	waitStep(t, env2, o2, run2.GetId(), 1, pb.StepStatus_STEP_STATUS_HALTED)
	if _, err := env2.svc.ResolveRunStep(o2.GetId(), run2.GetId(), 1, pb.StepResolution_STEP_RESOLUTION_FAIL); err != nil {
		t.Fatal(err)
	}
	r = runNow(env2, o2, run2.GetId())
	if r.GetState() != pb.RunState_RUN_STATE_FAILED || r.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		t.Errorf("X on a restart halt = %+v", r)
	}
}

func pickKind(req *pb.DeliverWorkerStepRequest, k pb.StepKind) string {
	if req.GetStep().GetKind() == k {
		return "input box not empty"
	}
	return ""
}

// A claimed run whose worker turns out detached goes back to the queue, and
// dispatch skips that worker.
func TestOrphanWorkerIsNotClaimed(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	d.set(func(d *fakeDaemon) {
		d.refuse = func(*pb.DeliverWorkerStepRequest) string { return "agent detached by a daemon restart" }
	})
	run := callSeq(t, env, o, "1")
	waitStep(t, env, o, run.GetId(), 0, pb.StepStatus_STEP_STATUS_PENDING)
	emit(w, "wa", func(sb *pb.Sandbox) { sb.Agent.AgentCheck = &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED} })
	waitFor(t, func() bool { return runNow(env, o, run.GetId()).GetSandboxId() == "" })
	settle(t, rt)
	if r := runNow(env, o, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_QUEUED || r.GetSandboxId() != "" {
		t.Errorf("unclaimed run = %+v", r)
	}
}

// --- small pure helpers ---

func TestStepHelpers(t *testing.T) {
	run := &pb.Run{Steps: []*pb.RunStep{
		{Role: pb.StepRole_STEP_ROLE_SETUP, Kind: pb.StepKind_STEP_KIND_CUSTOM, Text: strings.Repeat("long words ", 10)},
		{Role: pb.StepRole_STEP_ROLE_ACTION, Kind: pb.StepKind_STEP_KIND_ACTION_PROMPT},
		{Role: pb.StepRole_STEP_ROLE_CLEANUP, Kind: pb.StepKind_STEP_KIND_CLEAR, Text: "/clear"},
	}}
	if got := stepLabel(run); !strings.HasPrefix(got, "setup step 1/1 (long words") || !strings.HasSuffix(got, "…)") {
		t.Errorf("label = %q", got)
	}
	run.Step = 2
	if got := stepLabel(run); got != "cleanup step 1/1 (/clear)" {
		t.Errorf("label = %q", got)
	}
	run.Step = 9
	if got := stepLabel(run); got != "the sequence" {
		t.Errorf("label = %q", got)
	}
	cases := map[codes.Code]stepResult{
		codes.AlreadyExists: resSent, codes.Unimplemented: resUnsupported, codes.InvalidArgument: resInvalid,
		codes.FailedPrecondition: resRefused, codes.NotFound: resRefused, codes.Unavailable: resTransport,
	}
	for c, want := range cases {
		if got := classifyStep(status.Error(c, "x")); got != want {
			t.Errorf("classify(%v) = %v, want %v", c, got, want)
		}
	}
	if got := refusalReason(fp("input box not empty")); got != "input box not empty" {
		t.Errorf("reason = %q", got)
	}
}

// INVALID_ARGUMENT (an expect outside the fixed sets) halts with the error,
// never retried.
func TestInvalidStepHalts(t *testing.T) {
	env2, w2, o2, _, _ := seqEnv(t, 1, nil)
	w2.mu.Lock()
	w2.steps = func(ctx context.Context, w *fakeWorker, req *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid: model \"bogus\" is not one of opus, fable, sonnet, haiku")
	}
	w2.mu.Unlock()
	run := callSeq(t, env2, o2, "1")
	waitFor(t, func() bool { return runNow(env2, o2, run.GetId()).GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED })
	if r := runNow(env2, o2, run.GetId()); !strings.Contains(r.GetStepNote(), "is not one of") {
		t.Errorf("invalid halt = %+v", r)
	}
	if n := len(stepReqs(w2)); n != 1 {
		t.Errorf("an invalid step is never retried: %d calls", n)
	}
}
