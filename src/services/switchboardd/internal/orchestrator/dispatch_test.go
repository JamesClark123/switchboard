package orchestrator

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// dispatchEnv is an orchestrator with a reachable fake host carrying n
// workers (and optionally a reasoning sandbox), plus the review-pr action.
func dispatchEnv(t *testing.T, n int, reasoning bool) (*testEnv, *fakeWorker, *pb.Orchestrator, *runtime) {
	t.Helper()
	env, w, o, rt := remoteEnv(t)
	for i := 0; i < n; i++ {
		id := "w" + string(rune('a'+i))
		w.put(running(id, "worker-"+id))
		if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", id); err != nil {
			t.Fatal(err)
		}
	}
	if reasoning {
		r := running("reasoning-1", "reasoning-1")
		r.Controller = &pb.SandboxController{OrchestratorId: o.GetId(), Name: "review-bot", HostId: "host-m", Reasoning: true}
		w.put(proto.Clone(r).(*pb.Sandbox)) // the fake and the runtime each own a copy
		if _, err := rt.update(func(x *pb.Orchestrator) error {
			x.Workers = append(x.Workers, &pb.Worker{SandboxId: "reasoning-1", HostId: "box", Reasoning: true})
			x.ReasoningSandboxId = "reasoning-1"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		rt.mu.Lock()
		rt.sbs["reasoning-1"] = r
		rt.mu.Unlock()
	}
	if _, err := env.svc.PutAction(o.GetId(), reviewPR()); err != nil {
		t.Fatal(err)
	}
	return env, w, o, rt
}

// settle waits until no dispatch pass is pending, failing the test if it never
// settles (rather than letting a later waitFor fail with a misleading message).
func settle(t *testing.T, rt *runtime) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		rt.dispatchMu.Lock()
		idle := !rt.dispatching && !rt.dispatchPending
		rt.dispatchMu.Unlock()
		if idle {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("dispatch did not settle in time")
}

func call(t *testing.T, env *testEnv, orchID, number string) *pb.Run {
	t.Helper()
	run, err := env.svc.CallAction(orchID, "review-pr", map[string]string{"number": number, "repo": "acme/web"}, pb.EventKind_EVENT_KIND_USER, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestDispatchOrderAndCapacity(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	r1 := call(t, env, o.GetId(), "1")
	r2 := call(t, env, o.GetId(), "2")
	r3 := call(t, env, o.GetId(), "3")
	settle(t, rt)
	waitFor(t, func() bool {
		runs, _ := env.svc.ListRuns(o.GetId())
		return len(runs.GetInFlight()) == 2 && len(runs.GetQueued()) == 1
	})
	runs, _ := env.svc.ListRuns(o.GetId())
	if runs.GetQueued()[0].GetId() != r3.GetId() || runs.GetQueued()[0].GetPosition() != 1 {
		t.Errorf("the third call waits at position 1: %+v", runs.GetQueued())
	}
	ids := map[string]bool{}
	for _, r := range runs.GetInFlight() {
		ids[r.GetId()] = true
		if r.GetState() != pb.RunState_RUN_STATE_RUNNING || r.GetSandboxId() == "" || r.GetStartedAt() == nil || r.GetSandboxHostId() != "box" {
			t.Errorf("started run = %+v", r)
		}
	}
	if !ids[r1.GetId()] || !ids[r2.GetId()] {
		t.Errorf("the first two runs start: %v", ids)
	}
	// Each worker carries exactly one run (I-R2), and the delivery went through
	// the link with the composed prompt.
	if w.get("wa").GetController().GetRun() == nil || w.get("wb").GetController().GetRun() == nil {
		t.Error("both workers should carry a run")
	}
	delivered := 0
	for _, c := range w.recorded() {
		if strings.HasPrefix(c, "deliver ") {
			delivered++
		}
	}
	if delivered != 2 {
		t.Errorf("deliveries = %d, want 2", delivered)
	}
	// A report on wa frees it; the third run starts there.
	run := rt.runOn("wa")
	if _, err := rt.completeRun(run, "done", false, false); err != nil {
		t.Fatal(err)
	}
	// The fake's record clears the run on UpdateWorker; the status event says IDLE.
	sb := w.get("wa")
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: sb}}
	waitFor(t, func() bool {
		runs, _ := env.svc.ListRuns(o.GetId())
		return len(runs.GetQueued()) == 0 && len(runs.GetHistory()) == 1
	})
	runs, _ = env.svc.ListRuns(o.GetId())
	if runs.GetHistory()[0].GetId() != r1.GetId() || runs.GetHistory()[0].GetSummary() != "done" || runs.GetHistory()[0].GetState() != pb.RunState_RUN_STATE_COMPLETED {
		t.Errorf("history = %+v", runs.GetHistory())
	}
	if env.hub.notifications() != nil {
		t.Errorf("completions are silent (SC-014), got %+v", env.hub.notifications())
	}
	// Position and GetRun.
	r4 := call(t, env, o.GetId(), "4")
	got, _ := env.svc.GetRun(o.GetId(), r4.GetId())
	if got.GetPrompt() != "Review pull request 4 in acme/web and leave review comments." {
		t.Errorf("GetRun prompt = %q", got.GetPrompt())
	}
}

func TestDispatchTagsReasonAndRefusals(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, true)
	// worker_tag restricts prompt runs; a reason run goes only to the reasoning sandbox.
	tagged := reviewPR()
	tagged.WorkerTag = "ci"
	if _, err := env.svc.PutAction(o.GetId(), tagged); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.PutAction(o.GetId(), &pb.Action{Name: "triage", Type: pb.ActionType_ACTION_TYPE_REASON, Body: "Think about {issues}", Arguments: []*pb.ActionArgument{{Name: "issues", Required: true}}}); err != nil {
		t.Fatal(err)
	}
	wb := w.update("wb", func(sb *pb.Sandbox) { sb.Tag = "ci" })
	rt.mu.Lock()
	rt.sbs["wb"] = wb
	rt.mu.Unlock()
	r1 := call(t, env, o.GetId(), "1")
	reason, _ := env.svc.CallAction(o.GetId(), "triage", map[string]string{"issues": "a, b"}, pb.EventKind_EVENT_KIND_USER, "", "")
	settle(t, rt)
	waitFor(t, func() bool {
		runs, _ := env.svc.ListRuns(o.GetId())
		return len(runs.GetInFlight()) == 2
	})
	runs, _ := env.svc.ListRuns(o.GetId())
	for _, r := range runs.GetInFlight() {
		switch r.GetId() {
		case r1.GetId():
			if r.GetSandboxId() != "wb" {
				t.Errorf("tagged run went to %s, want the #ci worker", r.GetSandboxId())
			}
		case reason.GetId():
			if r.GetSandboxId() != "reasoning-1" {
				t.Errorf("reason run went to %s, want the reasoning sandbox", r.GetSandboxId())
			}
		}
	}
	// A second tagged call waits (wa is free but not #ci) and never blocks later
	// untagged runs.
	r2 := call(t, env, o.GetId(), "2")
	untagged := reviewPR()
	untagged.Name = "any"
	if _, err := env.svc.PutAction(o.GetId(), untagged); err != nil {
		t.Fatal(err)
	}
	r3, _ := env.svc.CallAction(o.GetId(), "any", map[string]string{"number": "3", "repo": "r"}, pb.EventKind_EVENT_KIND_USER, "", "")
	settle(t, rt)
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(o.GetId(), r3.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING && got.GetSandboxId() == "wa"
	})
	if got, _ := env.svc.GetRun(o.GetId(), r2.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED || got.GetPosition() != 1 {
		t.Errorf("tagged run should still be queued at 1: %+v", got)
	}
	// Cancel only when queued.
	if _, err := env.svc.CancelRun(o.GetId(), r2.GetId()); err != nil {
		t.Errorf("cancel queued = %v", err)
	}
	if _, err := env.svc.CancelRun(o.GetId(), r1.GetId()); err == nil {
		t.Error("cancel of a running run must be refused")
	}
	if _, err := env.svc.CancelRun(o.GetId(), "run-nope-1"); err == nil {
		t.Error("cancel unknown")
	}
	// Call refusals queue nothing (SC-005).
	before, _ := env.svc.ListRuns(o.GetId())
	if _, err := env.svc.CallAction(o.GetId(), "review-pr", map[string]string{"number": "9"}, pb.EventKind_EVENT_KIND_USER, "", ""); err == nil {
		t.Error("missing required should be refused")
	}
	if _, err := env.svc.CallAction(o.GetId(), "nope", nil, pb.EventKind_EVENT_KIND_USER, "", ""); err == nil {
		t.Error("unknown action should be refused")
	}
	after, _ := env.svc.ListRuns(o.GetId())
	if len(after.GetQueued()) != len(before.GetQueued()) {
		t.Error("refused calls must queue nothing")
	}
}

// Delivery outcomes: FAILED_PRECONDITION keeps the run queued and the sandbox
// unclaimed for the pass; any other error fails the run (FR-124).
// setAgent sets a worker's agent status on both the fake host and the
// orchestrator's cached record, without a status event (no dispatch pass).
func setAgent(rt *runtime, w *fakeWorker, id string, st pb.AgentStatus) {
	sb := w.update(id, func(sb *pb.Sandbox) { sb.Agent.Status = st })
	rt.mu.Lock()
	rt.sbs[id] = sb
	rt.mu.Unlock()
}

// A worker removed while a pass is delivering to another one is out of that
// pass: the later run stays queued instead of failing on a delivery to a
// sandbox that is gone.
func TestWorkerRemovedMidPassKeepsRunQueued(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_WORKING)
	setAgent(rt, w, "wb", pb.AgentStatus_AGENT_STATUS_WORKING)
	r1 := call(t, env, o.GetId(), "1")
	r2 := call(t, env, o.GetId(), "2")
	settle(t, rt)
	// While r1 is delivered to wa, wb is removed and the orchestrator has seen it.
	removed := make(chan struct{})
	w.mu.Lock()
	w.onDeliver = func(req *pb.DeliverWorkerPromptRequest) {
		if req.GetRunId() != r1.GetId() {
			return
		}
		w.mu.Lock()
		delete(w.sbs, "wb")
		w.mu.Unlock()
		w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "wb"}}}
		for deadline := time.Now().Add(testWaitTimeout); rt.sandboxRecord("wb") != nil && time.Now().Before(deadline); {
			time.Sleep(2 * time.Millisecond)
		}
		close(removed)
	}
	w.mu.Unlock()
	// Both free in one pass: r1 → wa, then r2 would go to wb.
	setAgent(rt, w, "wa", pb.AgentStatus_AGENT_STATUS_IDLE)
	setAgent(rt, w, "wb", pb.AgentStatus_AGENT_STATUS_IDLE)
	rt.triggerDispatch()
	<-removed
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), r1.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	if got, _ := env.svc.GetRun(o.GetId(), r2.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED {
		t.Errorf("r2 = %v (%q), want QUEUED", got.GetState(), got.GetReason())
	}
}

// ownHostAPI is the own host's worker side as the daemon serves it in-process
// (worker.Local): refusals come back as the worker sentinel, not a gRPC status.
type ownHostAPI struct{ worker.API }

func (a ownHostAPI) DeliverWorkerPrompt(ctx context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	resp, err := a.API.DeliverWorkerPrompt(ctx, req)
	if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
		return &pb.DeliverWorkerPromptResponse{}, fmt.Errorf("%w: %s", worker.ErrPrecondition, st.Message())
	}
	return resp, err
}

// A refusal by the own host's worker side keeps the run queued, as one over a
// remote link does.
func TestOwnHostRefusalKeepsRunQueued(t *testing.T) {
	w := newFakeWorker("host-m")
	conn, err := dial.DialLocal(context.Background(), w.serve(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	env := &testEnv{reg: reg, hub: &fakeHub{}, clock: newFakeClock(), dials: &dialRecorder{socks: map[string]string{}}}
	env.svc = NewService(Deps{Store: reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test",
		Local: ownHostAPI{worker.Remote(conn.API())}})
	if err := env.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.svc.Stop)
	o, err := env.svc.Create("review-bot")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := env.svc.runtimeFor(o.GetId())
	if err != nil {
		t.Fatal(err)
	}
	sb := running("wa", "worker-wa")
	sb.HostId = "host-m"
	w.put(sb)
	waitFor(t, func() bool { ok, _ := rt.reachability("host-m"); return ok })
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "host-m", "wa"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.PutAction(o.GetId(), reviewPR()); err != nil {
		t.Fatal(err)
	}
	// The agent turns busy as the delivery arrives (the record moved after the
	// orchestrator's check), so the host refuses it.
	delivered := make(chan struct{}, 1)
	w.mu.Lock()
	w.onDeliver = func(*pb.DeliverWorkerPromptRequest) {
		w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING })
		select {
		case delivered <- struct{}{}:
		default:
		}
	}
	w.mu.Unlock()
	r1 := call(t, env, o.GetId(), "1")
	select {
	case <-delivered:
	case <-time.After(testWaitTimeout):
		t.Fatal("no delivery attempt")
	}
	settle(t, rt)
	if got, _ := env.svc.GetRun(o.GetId(), r1.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED {
		t.Errorf("own-host refusal: run = %v (%q), want QUEUED", got.GetState(), got.GetReason())
	}
}

func TestDeliveryRefusedAndFailed(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 1, false)
	// The fake refuses delivery when the agent is not idle.
	w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_WORKING })
	// …but the orchestrator's cached record still says idle (a race).
	r1 := call(t, env, o.GetId(), "1")
	settle(t, rt)
	time.Sleep(20 * time.Millisecond)
	if got, _ := env.svc.GetRun(o.GetId(), r1.GetId()); got.GetState() != pb.RunState_RUN_STATE_QUEUED {
		t.Errorf("a refused delivery keeps the run queued: %v", got.GetState())
	}
	// The sandbox vanishes on the far side: delivery fails the run.
	w.mu.Lock()
	delete(w.sbs, "wa")
	w.mu.Unlock()
	rt.triggerDispatch()
	settle(t, rt)
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(o.GetId(), r1.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_FAILED
	})
	got, _ := env.svc.GetRun(o.GetId(), r1.GetId())
	if !strings.Contains(got.GetReason(), "delivery failed") {
		t.Errorf("reason = %q", got.GetReason())
	}
	// RUN_FAILED is raised after the run is stored as FAILED.
	waitFor(t, func() bool { return len(env.hub.notifications()) >= 1 })
	n := env.hub.notifications()
	if len(n) != 1 || n[0].GetKind() != pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED || !strings.Contains(n[0].GetMessage(), "review-bot") {
		t.Errorf("notifications = %+v", n)
	}
}

// SC-003/SC-004 property: over random call sequences with random free/busy
// flips, no run starts before an earlier run the same sandbox could have taken,
// and no sandbox ever carries two runs.
func TestDispatchOrderProperty(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 3, false)
	rng := rand.New(rand.NewSource(7))
	workers := []string{"wa", "wb", "wc"}
	var order []string
	for i := 0; i < 40; i++ {
		switch rng.Intn(3) {
		case 0, 1:
			run := call(t, env, o.GetId(), "x")
			order = append(order, run.GetId())
		case 2:
			// Flip a worker: a running run completes (frees it) and the status
			// event carries the fresh record.
			id := workers[rng.Intn(len(workers))]
			if run := rt.runOn(id); run != nil {
				_, _ = rt.completeRun(run, "ok", false, false)
				w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: w.get(id)}}
			}
		}
		settle(t, rt)
		time.Sleep(3 * time.Millisecond)
		// At most one in-flight run per sandbox at every step (SC-004).
		runs, _ := env.svc.ListRuns(o.GetId())
		seen := map[string]bool{}
		for _, r := range runs.GetInFlight() {
			if seen[r.GetSandboxId()] {
				t.Fatalf("sandbox %s carries two runs", r.GetSandboxId())
			}
			seen[r.GetSandboxId()] = true
		}
	}
	// Order of receipt: started_at never decreases along the queue order for
	// runs that started, and every started run precedes every still-queued one
	// in receipt order (no run overtakes an earlier eligible one).
	runs, _ := env.svc.ListRuns(o.GetId())
	started := map[string]time.Time{}
	for _, r := range append(runs.GetInFlight(), runs.GetHistory()...) {
		if r.GetStartedAt() != nil {
			started[r.GetId()] = r.GetStartedAt().AsTime()
		}
	}
	lastQueued := -1
	for i, id := range order {
		if _, ok := started[id]; !ok {
			lastQueued = i
		}
	}
	for i, id := range order {
		if _, ok := started[id]; ok && i > lastQueued && lastQueued >= 0 {
			// started after a still-queued one: only legal if it was the same pass
			// where the earlier one had no eligible free sandbox — with identical
			// untagged runs that cannot happen.
			t.Fatalf("run %s (receipt %d) started while run %s (receipt %d) is still queued", id, i, order[lastQueued], lastQueued)
		}
	}
	if len(started) == 0 {
		t.Fatal("nothing ran")
	}
}

func bg() context.Context { return context.Background() }

// SWI-15: a worker that looks free but refuses the delivery (its agent is
// still starting) does not hold the run: the same pass offers it to the next
// free worker.
func TestRefusedDeliveryFallsThroughToTheNextWorker(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 2, false)
	// The worker side refuses wa (as for an agent not ready yet), while the
	// orchestrator's cached record still reads it free.
	w.update("wa", func(sb *pb.Sandbox) { sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_STARTING })
	r1 := call(t, env, o.GetId(), "1")
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), r1.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	if got, _ := env.svc.GetRun(o.GetId(), r1.GetId()); got.GetSandboxId() != "wb" {
		t.Errorf("run went to %q, want wb", got.GetSandboxId())
	}
	if calls := strings.Join(w.recorded(), "\n"); !strings.Contains(calls, "deliver wa "+r1.GetId()) {
		t.Errorf("wa was not offered the run first: %s", calls)
	}
}
