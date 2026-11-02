package orchestrator

import (
	"errors"
	"fmt"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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

// A claimed run is on its worker running setup steps: pausing that worker does
// not make the run wait, unless the worker has stopped too.
func TestWaitingReasonClaimedRunOnPausedWorker(t *testing.T) {
	rec := &pb.Orchestrator{
		Actions: []*pb.Action{{Name: "plain", Type: pb.ActionType_ACTION_TYPE_PROMPT}},
		Workers: []*pb.Worker{{SandboxId: "p0", Paused: true}},
	}
	q := &pb.Run{Action: "plain", State: pb.RunState_RUN_STATE_QUEUED, Type: pb.ActionType_ACTION_TYPE_PROMPT, SandboxId: "p0"}
	names := map[string]string{"p0": "p0"}
	if got := waitingReason(q, reasonCounts(rec, map[string]bool{"p0": true}, names), false); got != "" {
		t.Errorf("claimed run = %q", got)
	}
	if got := waitingReason(q, reasonCounts(rec, nil, names), false); got != "waiting for paused p0" {
		t.Errorf("claimed run, worker stopped = %q", got)
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
