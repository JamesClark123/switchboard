package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func eventOf(t *testing.T, env *testEnv, orchID, evID string) *pb.TimedEvent {
	t.Helper()
	rec, err := env.reg.GetOrchestrator(orchID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range rec.GetTimedEvents() {
		if ev.GetId() == evID {
			return ev
		}
	}
	return nil
}

func pollEvent() *pb.TimedEvent {
	return &pb.TimedEvent{Name: "poll", Action: "review-pr", Arguments: map[string]string{"number": "1", "repo": "acme/web"}, IntervalSeconds: 60, Enabled: true}
}

func TestTimedEventFiresDedupesAndKeepsPhase(t *testing.T) {
	env, w, o, rt := dispatchEnv(t, 1, false)
	start := env.clock.Now()
	ev, err := env.svc.PutTimedEvent(o.GetId(), pollEvent())
	if err != nil {
		t.Fatal(err)
	}
	if ev.GetId() != "tev-1" || !ev.GetEnabled() || ev.GetDedupeDisabled() || !ev.GetNextFireAt().AsTime().Equal(start.Add(time.Minute)) {
		t.Fatalf("created = %+v", ev)
	}
	// Refusals: short interval, unknown action, missing required argument,
	// blank name, unknown event id, unknown orchestrator.
	for name, bad := range map[string]*pb.TimedEvent{
		"interval 59":    {Name: "x", Action: "review-pr", IntervalSeconds: 59, Arguments: map[string]string{"number": "1", "repo": "r"}, Enabled: true},
		"unknown action": {Name: "x", Action: "nope", IntervalSeconds: 60, Enabled: true},
		"missing arg":    {Name: "x", Action: "review-pr", IntervalSeconds: 60, Enabled: true},
		"blank name":     {Name: " ", Action: "review-pr", IntervalSeconds: 60, Arguments: map[string]string{"number": "1", "repo": "r"}},
	} {
		if _, err := env.svc.PutTimedEvent(o.GetId(), bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := env.svc.PutTimedEvent(o.GetId(), &pb.TimedEvent{Id: "tev-9", Name: "x", Action: "review-pr", IntervalSeconds: 60, Arguments: map[string]string{"number": "1", "repo": "r"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown event id = %v", err)
	}
	if _, err := env.svc.PutTimedEvent("o-none", pollEvent()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}

	// Not before next_fire_at.
	env.clock.Advance(59 * time.Second)
	if n := runCount(t, env, o.GetId()); n != 0 {
		t.Fatalf("fired early: %d runs", n)
	}
	// At next_fire_at: one TIMED run with the event as origin and its fixed
	// arguments; the phase advances by one interval.
	env.clock.Advance(time.Second)
	runs, _ := env.reg.ListRuns(o.GetId())
	if len(runs) != 1 || runs[0].GetEventKind() != pb.EventKind_EVENT_KIND_TIMED || runs[0].GetOrigin() != "tev-1" || runs[0].GetOriginLabel() != "poll" || runs[0].GetArguments()["number"] != "1" {
		t.Fatalf("runs after first fire = %+v", runs)
	}
	ev = eventOf(t, env, o.GetId(), "tev-1")
	if !ev.GetLastFiredAt().AsTime().Equal(start.Add(time.Minute)) || !ev.GetNextFireAt().AsTime().Equal(start.Add(2*time.Minute)) {
		t.Errorf("after fire = %+v", ev)
	}
	settle(rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), runs[0].GetId()) == pb.RunState_RUN_STATE_RUNNING })

	// Dedupe (on by default): while that run is in flight the next firing is
	// skipped and recorded; the phase still advances.
	env.clock.Advance(time.Minute)
	if n := runCount(t, env, o.GetId()); n != 1 {
		t.Errorf("dedupe should skip: %d runs", n)
	}
	ev = eventOf(t, env, o.GetId(), "tev-1")
	if ev.GetSkippedCount() != 1 || !ev.GetLastSkippedAt().AsTime().Equal(start.Add(2*time.Minute)) || !ev.GetNextFireAt().AsTime().Equal(start.Add(3*time.Minute)) {
		t.Errorf("after skip = %+v", ev)
	}
	// Once the run closes, the next firing queues again (SC-012).
	if _, err := rt.runTool("box", "wa", toolReportCompletion, `{"summary":"done"}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return w.get("wa").GetController().GetRun() == nil })
	env.clock.Advance(time.Minute)
	if n := runCount(t, env, o.GetId()); n != 2 {
		t.Errorf("after close: %d runs, want 2", n)
	}
	settle(rt)

	// dedupe_disabled queues every firing even with a run in flight; an
	// update that keeps the interval keeps the phase.
	upd := pollEvent()
	upd.Id, upd.DedupeDisabled = "tev-1", true
	ev, err = env.svc.PutTimedEvent(o.GetId(), upd)
	if err != nil || !ev.GetDedupeDisabled() || !ev.GetNextFireAt().AsTime().Equal(start.Add(4*time.Minute)) {
		t.Fatalf("dedupe off = %+v / %v", ev, err)
	}
	env.clock.Advance(time.Minute)
	if n := runCount(t, env, o.GetId()); n != 3 {
		t.Errorf("dedupe off: %d runs, want 3", n)
	}
	if ev = eventOf(t, env, o.GetId(), "tev-1"); ev.GetSkippedCount() != 1 {
		t.Errorf("skips with dedupe off = %d", ev.GetSkippedCount())
	}

	// Disable stops firing; enable resumes on a fresh phase from now.
	upd.Enabled = false
	if _, err := env.svc.PutTimedEvent(o.GetId(), upd); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(3 * time.Minute)
	if n := runCount(t, env, o.GetId()); n != 3 {
		t.Errorf("disabled event fired: %d runs", n)
	}
	upd.Enabled = true
	ev, err = env.svc.PutTimedEvent(o.GetId(), upd)
	if err != nil || !ev.GetNextFireAt().AsTime().Equal(env.clock.Now().Add(time.Minute)) {
		t.Fatalf("re-enabled = %+v / %v", ev, err)
	}
	env.clock.Advance(time.Minute)
	if n := runCount(t, env, o.GetId()); n != 4 {
		t.Errorf("re-enabled event: %d runs, want 4", n)
	}
	// A shorter interval restarts the phase.
	upd.IntervalSeconds = 120
	ev, _ = env.svc.PutTimedEvent(o.GetId(), upd)
	if !ev.GetNextFireAt().AsTime().Equal(env.clock.Now().Add(2 * time.Minute)) {
		t.Errorf("interval change = %+v", ev)
	}

	// Deleting the action disables the event with the reason and reports it.
	names, err := env.svc.DeleteAction(o.GetId(), "review-pr")
	if err != nil || len(names) != 1 || names[0] != "poll" {
		t.Fatalf("DeleteAction = %v / %v", names, err)
	}
	ev = eventOf(t, env, o.GetId(), "tev-1")
	if ev.GetEnabled() || ev.GetDisabledReason() != "action review-pr was deleted" {
		t.Errorf("after action delete = %+v", ev)
	}
	env.clock.Advance(5 * time.Minute)
	if n := runCount(t, env, o.GetId()); n != 4 {
		t.Errorf("a disabled event must not fire: %d runs", n)
	}
	// Delete the event.
	if err := env.svc.DeleteTimedEvent(o.GetId(), "tev-1"); err != nil {
		t.Fatal(err)
	}
	if err := env.svc.DeleteTimedEvent(o.GetId(), "tev-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete = %v", err)
	}
	if eventOf(t, env, o.GetId(), "tev-1") != nil {
		t.Error("event should be gone")
	}
}

func TestSchedulerRestartResumesOnPhaseWithoutReplay(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 1, false)
	start := env.clock.Now()
	// Dedupe off on both, so the one worker's backlog does not mask the slots.
	first := pollEvent()
	first.DedupeDisabled = true
	if _, err := env.svc.PutTimedEvent(o.GetId(), first); err != nil {
		t.Fatal(err)
	}
	second := pollEvent()
	second.Name, second.DedupeDisabled = "later", true
	if _, err := env.svc.PutTimedEvent(o.GetId(), second); err != nil {
		t.Fatal(err)
	}
	env.clock.Advance(time.Minute) // both fire once
	if n := runCount(t, env, o.GetId()); n != 2 {
		t.Fatalf("runs before restart = %d", n)
	}
	// The daemon stops; 3.5 intervals pass; a new service on the same store
	// resumes each event on its phase — the missed slots are not replayed.
	env.svc.Stop()
	env.clock.Advance(3*time.Minute + 30*time.Second)
	svc2 := NewService(Deps{Store: env.reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test"})
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc2.Stop)
	if n := runCount(t, env, o.GetId()); n != 2 {
		t.Errorf("restart replayed firings: %d runs", n)
	}
	ev := eventOf(t, env, o.GetId(), "tev-1")
	if !ev.GetNextFireAt().AsTime().Equal(start.Add(5 * time.Minute)) {
		t.Errorf("next fire after restart = %v, want %v", ev.GetNextFireAt().AsTime(), start.Add(5*time.Minute))
	}
	env.clock.Advance(29 * time.Second)
	if n := runCount(t, env, o.GetId()); n != 2 {
		t.Errorf("fired before the slot: %d runs", n)
	}
	env.clock.Advance(time.Second)
	if n := runCount(t, env, o.GetId()); n != 4 {
		t.Errorf("both events should fire on the slot: %d runs", n)
	}
	// The second event kept its own phase too, and nothing was counted as skipped.
	ev = eventOf(t, env, o.GetId(), "tev-2")
	if !ev.GetEnabled() || ev.GetSkippedCount() != 0 || !ev.GetLastFiredAt().AsTime().Equal(start.Add(5*time.Minute)) {
		t.Errorf("second event = %+v", ev)
	}
}
