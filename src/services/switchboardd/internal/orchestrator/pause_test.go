package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Pausing workers (SWI-23).

func pause(t *testing.T, env *testEnv, orchID, id string, paused bool) *pb.WorkerView {
	t.Helper()
	v, err := env.svc.SetWorkerPaused(orchID, id, paused)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPausedWorkerIsSkippedAndResumeDispatches(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 2, false)
	id := o.GetId()
	if v := pause(t, env, id, "wa", true); !v.GetPaused() {
		t.Errorf("view = %+v", v)
	}
	// wa is first in the roster, yet the run goes to wb.
	r0 := call(t, env, id, "1")
	settle(t, rt)
	waitFor(t, func() bool { return runOf(t, env, id, r0.GetId()).GetSandboxId() == "wb" })
	// wb is busy and wa paused: the next run stays queued, and says nothing
	// while a busy worker could take it.
	r1 := call(t, env, id, "2")
	settle(t, rt)
	if got := runOf(t, env, id, r1.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED || got.GetWaitingReason() != "" {
		t.Errorf("queued run = %v %q", got.GetState(), got.GetWaitingReason())
	}
	// Resuming hands it to wa at once, with no other trigger.
	pause(t, env, id, "wa", false)
	waitFor(t, func() bool { return runOf(t, env, id, r1.GetId()).GetSandboxId() == "wa" })
}

func TestRunOnPausedWorkerFinishes(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	r0 := call(t, env, id, "1")
	waitFor(t, func() bool { return runOf(t, env, id, r0.GetId()).GetSandboxId() == "wa" })
	pause(t, env, id, "wa", true)
	r1 := call(t, env, id, "2")
	settle(t, rt)

	running := runOf(t, env, id, r0.GetId())
	if running.GetState() != pb.RunState_RUN_STATE_RUNNING {
		t.Fatalf("running run = %v", running.GetState())
	}
	// A paused worker mid-run counts as paused, not busy.
	if got := runOf(t, env, id, r1.GetId()).GetWaitingReason(); got != "waiting for paused worker-wa" {
		t.Errorf("queued reason = %q", got)
	}
	if _, err := rt.completeRun(running, "done", false, false); err != nil {
		t.Fatal(err)
	}
	if got := runOf(t, env, id, r0.GetId()); got.GetState() != pb.RunState_RUN_STATE_COMPLETED || got.GetSummary() != "done" {
		t.Errorf("closed run = %v %q", got.GetState(), got.GetSummary())
	}
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun() == nil })
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE)
	rt.triggerDispatch()
	settle(t, rt)
	if got := runOf(t, env, id, r1.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED || got.GetSandboxId() != "" {
		t.Errorf("idle paused worker got a run: %v on %q", got.GetState(), got.GetSandboxId())
	}
	if v := rt.workerView("wa"); !v.GetPaused() || v.GetRunId() != "" {
		t.Errorf("worker after the run = %+v", v)
	}
	pause(t, env, id, "wa", false)
	waitFor(t, func() bool { return runOf(t, env, id, r1.GetId()).GetSandboxId() == "wa" })
}

func TestSetWorkerPausedIdempotentAndRefusals(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 1, true)
	id := o.GetId()
	pause(t, env, id, "wa", true)
	waitFor(t, func() bool { return w.get("wa").GetController().GetPaused() })
	quiesce(t, rt)

	rt.mu.Lock()
	gen := rt.recGen
	rt.mu.Unlock()
	changes := env.hub.changed(id)
	if v := pause(t, env, id, "wa", true); !v.GetPaused() {
		t.Errorf("repeat = %+v", v)
	}
	rt.mu.Lock()
	genAfter := rt.recGen
	rt.mu.Unlock()
	if genAfter != gen || env.hub.changed(id) != changes {
		t.Errorf("a repeat wrote: gen %d -> %d, changed events %d -> %d", gen, genAfter, changes, env.hub.changed(id))
	}

	if _, err := env.svc.SetWorkerPaused(id, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("not a worker = %v", err)
	}
	if _, err := env.svc.SetWorkerPaused(id, "reasoning-1", true); !errors.Is(err, ErrInvalid) {
		t.Errorf("reasoning = %v", err)
	}
	if _, err := env.svc.SetWorkerPaused("nope", "wa", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
	pause(t, env, id, "wa", false)
	waitFor(t, func() bool { return !w.get("wa").GetController().GetPaused() })
}

func TestPausedSurvivesRestart(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 1, false)
	id := o.GetId()
	pause(t, env, id, "wa", true)
	env.svc.Stop()

	svc2 := NewService(Deps{Store: env.reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test"})
	if err := svc2.Start(bg()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc2.Stop)
	rt2, _ := svc2.runtimeFor(id)
	if !onRosterEntry(rt2.record(), "wa").GetPaused() {
		t.Error("paused flag lost across a restart")
	}
	run, err := svc2.CallAction(id, "review-pr", map[string]string{"number": "1", "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	settle(t, rt2)
	if got, _ := svc2.GetRun(id, run.GetId()); got.GetSandboxId() != "" {
		t.Errorf("run went to %s after the restart", got.GetSandboxId())
	}
}

func TestDetachAndReattachResetsPaused(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false) // wb keeps the open-lane guard quiet
	id := o.GetId()
	pause(t, env, id, "wa", true)
	waitFor(t, func() bool { return w.get("wa").GetController().GetPaused() })
	if err := env.svc.DetachWorker(bg(), id, "wa", nil); err != nil {
		t.Fatal(err)
	}
	if onRosterEntry(rt.record(), "wa") != nil {
		t.Fatal("still on the roster after the detach")
	}
	if _, err := env.svc.AttachWorker(bg(), id, "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	if onRosterEntry(rt.record(), "wa").GetPaused() {
		t.Error("a re-attached worker came back paused")
	}
	waitFor(t, func() bool { return !w.get("wa").GetController().GetPaused() })
}

func TestApplyRecordsKeepsPaused(t *testing.T) {
	w := rworker("s", 5, 1)
	w.Paused = true
	rec := rrec(w)
	// A newer record that still names us leaves the entry alone.
	applyRecords(rec, event(rsb("s", 6, rOrch), rsb("s", 5, rOrch)), rNow)
	if !onRoster(rec, "s").GetPaused() {
		t.Fatal("a reconcile cleared paused")
	}
	// Dropped by a detach, then adopted again: a fresh, unpaused entry.
	applyRecords(rec, event(rsb("s", 7, ""), rsb("s", 6, rOrch)), rNow)
	if onRoster(rec, "s") != nil {
		t.Fatal("not dropped")
	}
	applyRecords(rec, event(rsb("s", 8, rOrch), rsb("s", 7, "")), rNow)
	if e := onRoster(rec, "s"); e == nil || e.GetPaused() {
		t.Fatalf("re-adopted entry = %+v", e)
	}
}

func TestWaitingReasonsPaused(t *testing.T) {
	rec := &pb.Orchestrator{Actions: []*pb.Action{{Name: "plain", Type: pb.ActionType_ACTION_TYPE_PROMPT}}}
	names := map[string]string{}
	add := func(prefix string, n int, paused bool) {
		for i := range n {
			id := fmt.Sprintf("%s%d", prefix, i)
			rec.Workers = append(rec.Workers, &pb.Worker{SandboxId: id, Paused: paused})
			names[id] = id
		}
	}
	q := &pb.Run{Action: "plain", State: pb.RunState_RUN_STATE_QUEUED, Type: pb.ActionType_ACTION_TYPE_PROMPT}
	check := func(what, dev, agent string, avail map[string]bool) {
		t.Helper()
		c := reasonCounts(rec, avail, names)
		if got := waitingReason(q, c, false); got != dev {
			t.Errorf("%s: developer = %q, want %q", what, got, dev)
		}
		if got := waitingReason(q, c, true); got != agent {
			t.Errorf("%s: agent = %q, want %q", what, got, agent)
		}
	}
	add("p", 2, true)
	check("all paused", "waiting for paused p0, p1", "waiting: every worker that could take it is paused", nil)
	add("s", 1, false)
	check("paused and stopped", "waiting for paused p0, p1; s0, stopped or unreachable",
		"waiting: every worker that could take it is paused, stopped or unreachable", nil)
	check("another worker available", "", "", map[string]bool{"s0": true})
	add("t", 4, false)
	check("the limit covers both lists", "waiting for paused p0, p1; s0, t0, t1 +2 more, stopped or unreachable",
		"waiting: every worker that could take it is paused, stopped or unreachable", nil)

	rec.Workers, names = nil, map[string]string{}
	add("p", 7, true)
	add("s", 1, false)
	check("paused alone fill the limit", "waiting for paused p0, p1, p2, p3, p4 +2 more; +1 more, stopped or unreachable",
		"waiting: every worker that could take it is paused, stopped or unreachable", nil)

	// A stopped worker sharing a paused worker's display name still shows.
	rec.Workers = []*pb.Worker{{SandboxId: "a", Paused: true}, {SandboxId: "b"}}
	names = map[string]string{"a": "same", "b": "same"}
	check("shared name", "waiting for paused same; same, stopped or unreachable",
		"waiting: every worker that could take it is paused, stopped or unreachable", nil)
}

// refuseFirstStep leaves a run claimed on wa and QUEUED: its first step reached
// the worker, which refused it, so nothing is typed.
func refuseFirstStep(t *testing.T, env *testEnv, w *fakeWorker, o *pb.Orchestrator, d *fakeDaemon) *pb.Run {
	t.Helper()
	d.set(func(d *fakeDaemon) {
		d.refuse = func(req *pb.DeliverWorkerStepRequest) string {
			if req.GetSandboxId() == "wa" {
				return "input box not empty"
			}
			return ""
		}
	})
	run := callSeq(t, env, o, "1")
	waitFor(t, func() bool { return len(stepReqs(w)) > 0 })
	r := waitStep(t, env, o, run.GetId(), 0, pb.StepStatus_STEP_STATUS_PENDING)
	if r.GetState() != pb.RunState_RUN_STATE_QUEUED || r.GetSandboxId() != "wa" {
		t.Fatalf("claimed run = %v on %q", r.GetState(), r.GetSandboxId())
	}
	return run
}

// Pausing a worker releases a claim it hasn't started: the run goes to another
// free worker at once, and nothing is typed on the paused one.
func TestPauseReleasesAClaimToAnotherWorker(t *testing.T) {
	env, w, o, _, d := seqEnv(t, 2, nil)
	run := refuseFirstStep(t, env, w, o, d)
	pause(t, env, o.GetId(), "wa", true)
	waitFor(t, func() bool {
		r := runNow(env, o, run.GetId())
		return r.GetSandboxId() == "wb" && r.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
	for _, typed := range d.typedTexts() {
		if strings.HasPrefix(typed, "wa: ") {
			t.Errorf("typed on the paused worker: %q", typed)
		}
	}
}

// With no other worker the run waits and says why, and the refusal clearing
// does not start it on the paused worker. Resuming does.
func TestPausedClaimDoesNotStartWhenTheRefusalClears(t *testing.T) {
	env, w, o, rt, d := seqEnv(t, 1, nil)
	id := o.GetId()
	run := refuseFirstStep(t, env, w, o, d)
	pause(t, env, id, "wa", true)
	r := runOf(t, env, id, run.GetId())
	if r.GetSandboxId() != "" || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_UNSPECIFIED || r.GetWaitingReason() != "waiting for paused worker-wa" {
		t.Errorf("released run = %q %v %q", r.GetSandboxId(), r.GetStepStatus(), r.GetWaitingReason())
	}
	rt.mu.Lock()
	marks := rt.seq.marks[run.GetId()]
	rt.mu.Unlock()
	if marks != nil {
		t.Errorf("the refusal's timing should not follow the run to its next worker: %+v", marks)
	}

	d.set(func(d *fakeDaemon) { d.refuse = nil })
	env.clock.Advance(stepRetryEvery)
	settle(t, rt)
	if r := runOf(t, env, id, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_QUEUED || r.GetSandboxId() != "" || len(d.typedTexts()) != 0 {
		t.Errorf("after the refusal cleared = %v on %q, typed %q", r.GetState(), r.GetSandboxId(), d.typedTexts())
	}

	pause(t, env, id, "wa", false)
	waitFor(t, func() bool {
		r := runOf(t, env, id, run.GetId())
		return r.GetSandboxId() == "wa" && r.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
}

// A claim halted on a refused first step is released too: its stepper has
// nothing left to do, so only the pause itself can move it.
func TestPauseReleasesAHaltedClaim(t *testing.T) {
	env, w, o, _, _ := seqEnv(t, 1, nil)
	w.mu.Lock()
	w.steps = func(context.Context, *fakeWorker, *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
		return nil, status.Error(codes.InvalidArgument, "invalid")
	}
	w.mu.Unlock()
	run := callSeq(t, env, o, "1")
	r := waitStep(t, env, o, run.GetId(), 0, pb.StepStatus_STEP_STATUS_HALTED)
	if r.GetState() != pb.RunState_RUN_STATE_QUEUED || r.GetSandboxId() != "wa" {
		t.Fatalf("halted claim = %v on %q", r.GetState(), r.GetSandboxId())
	}
	pause(t, env, o.GetId(), "wa", true)
	if r := runNow(env, o, run.GetId()); r.GetSandboxId() != "" || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_UNSPECIFIED {
		t.Errorf("released run = %q %v", r.GetSandboxId(), r.GetStepStatus())
	}
}

// A pause landing between recheckAndClaim and claim: the stepper checks before
// it sends, so the claim goes back to the queue with nothing sent.
func TestClaimLandingAfterThePauseIsReleased(t *testing.T) {
	env, w, o, rt, _ := seqEnv(t, 1, nil)
	pause(t, env, o.GetId(), "wa", true)
	run := callSeq(t, env, o, "1")
	settle(t, rt)
	if !rt.claim(run, onRosterEntry(rt.record(), "wa")) {
		t.Fatal("claim refused")
	}
	waitFor(t, func() bool { return runNow(env, o, run.GetId()).GetSandboxId() == "" })
	if n := len(stepReqs(w)); n != 0 {
		t.Errorf("sent to the paused worker: %d calls", n)
	}
}

// holdFirstStep leaves a run's first step in flight on wa, answered once the
// returned func is called.
func holdFirstStep(t *testing.T, env *testEnv, o *pb.Orchestrator, d *fakeDaemon, answer error) (*pb.Run, func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	d.set(func(d *fakeDaemon) {
		d.hold = func(*pb.DeliverWorkerStepRequest) {
			once.Do(func() { close(entered); <-release })
		}
		if answer != nil {
			d.outcome = func(*pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) { return nil, answer }
		}
	})
	run := callSeq(t, env, o, "1")
	<-entered
	return run, func() { close(release) }
}

// A first step already in flight when the pause lands may be typed: the run then
// started just before the pause, and finishes like any other.
func TestPauseDuringAFirstStepInFlight(t *testing.T) {
	env, _, o, _, d := seqEnv(t, 1, nil)
	run, answer := holdFirstStep(t, env, o, d, nil)
	pause(t, env, o.GetId(), "wa", true)
	if r := runNow(env, o, run.GetId()); r.GetSandboxId() != "wa" || r.GetStepStatus() != pb.StepStatus_STEP_STATUS_SENDING {
		t.Errorf("in flight = %q %v", r.GetSandboxId(), r.GetStepStatus())
	}
	answer()
	waitFor(t, func() bool {
		r := runNow(env, o, run.GetId())
		return r.GetSandboxId() == "wa" && r.GetState() == pb.RunState_RUN_STATE_RUNNING
	})
}

// That step refused instead: the claim is released on the next retry.
func TestPauseDuringAFirstStepThenRefused(t *testing.T) {
	env, _, o, _, d := seqEnv(t, 1, nil)
	run, answer := holdFirstStep(t, env, o, d, fp("input box not empty"))
	pause(t, env, o.GetId(), "wa", true)
	answer()
	waitFor(t, func() bool {
		env.clock.Advance(stepRetryEvery)
		return runNow(env, o, run.GetId()).GetSandboxId() == ""
	})
	if r := runNow(env, o, run.GetId()); r.GetState() != pb.RunState_RUN_STATE_QUEUED {
		t.Errorf("released run = %v", r.GetState())
	}
}

// A stepper still holding a run from its old claim must not act on the new one.
func TestSameStepSeesAClaimThatMoved(t *testing.T) {
	onWorker := func(id string) *pb.Run {
		return &pb.Run{State: pb.RunState_RUN_STATE_QUEUED, SandboxId: id, StepStatus: pb.StepStatus_STEP_STATUS_PENDING}
	}
	if !sameStep(onWorker("wa"), onWorker("wa")) {
		t.Error("the same claim should match")
	}
	if sameStep(onWorker("wa"), onWorker("wb")) {
		t.Error("a claim that moved to another worker should not match")
	}
}

func TestPausedReasonReachesTheAgent(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	id := o.GetId()
	pause(t, env, id, "wa", true)
	run := call(t, env, id, "1")
	settle(t, rt)
	out, err := rt.toolCheckRun(checkRunIn{RunID: run.GetId()})
	if err != nil || out.WaitingReason != "waiting: every worker that could take it is paused" {
		t.Errorf("check run = %+v / %v", out, err)
	}
}
