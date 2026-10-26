package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
	settle(t, rt)
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
	settle(t, rt)

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

// --- SWI-4: Enqueue now ---

func TestFireTimedEventQueuesAsTheEventAndKeepsTheSchedule(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false) // no worker: the run stays queued
	ev, err := env.svc.PutTimedEvent(o.GetId(), pollEvent())
	if err != nil {
		t.Fatal(err)
	}
	before := eventOf(t, env, o.GetId(), ev.GetId())
	env.clock.Advance(20 * time.Second) // not due yet

	run, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId())
	if err != nil {
		t.Fatalf("FireTimedEvent: %v", err)
	}
	if run.GetEventKind() != pb.EventKind_EVENT_KIND_TIMED || run.GetOrigin() != ev.GetId() || run.GetOriginLabel() != "poll" ||
		run.GetArguments()["number"] != "1" || run.GetState() != pb.RunState_RUN_STATE_QUEUED || run.GetPosition() != 1 {
		t.Fatalf("run = %+v", run)
	}
	after := eventOf(t, env, o.GetId(), ev.GetId())
	if !after.GetNextFireAt().AsTime().Equal(before.GetNextFireAt().AsTime()) || after.GetLastFiredAt() != nil ||
		after.GetLastSkippedAt() != nil || after.GetSkippedCount() != 0 {
		t.Errorf("a manual fire must not touch the schedule: before %+v after %+v", before, after)
	}

	// Dedupe on: the open (queued) run refuses a second one and names it.
	_, err = env.svc.FireTimedEvent(o.GetId(), ev.GetId())
	if !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), run.GetId()) || !strings.Contains(err.Error(), "poll") || !strings.Contains(err.Error(), "queued") {
		t.Errorf("dedupe refusal = %v", err)
	}
	if n := runCount(t, env, o.GetId()); n != 1 {
		t.Errorf("a refused fire must queue nothing: %d runs", n)
	}

	// The next scheduled firing, still with that run open, is skipped and recorded.
	env.clock.Advance(40 * time.Second)
	if n := runCount(t, env, o.GetId()); n != 1 {
		t.Errorf("scheduled firing should be skipped: %d runs", n)
	}
	if got := eventOf(t, env, o.GetId(), ev.GetId()); got.GetSkippedCount() != 1 {
		t.Errorf("skipped_count = %d, want 1", got.GetSkippedCount())
	}
}

func TestFireTimedEventRefusedWhileARunIsInFlightButNotWithDedupeOff(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 1, false)
	ev, _ := env.svc.PutTimedEvent(o.GetId(), pollEvent())
	first, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId())
	if err != nil {
		t.Fatal(err)
	}
	settle(t, rt)
	waitFor(t, func() bool { return stateOf(env, o.GetId(), first.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	if _, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId()); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "running") {
		t.Errorf("in-flight refusal = %v", err)
	}

	upd := pollEvent()
	upd.Id, upd.DedupeDisabled = ev.GetId(), true
	if _, err := env.svc.PutTimedEvent(o.GetId(), upd); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId()); err != nil {
		t.Errorf("dedupe off should always enqueue: %v", err)
	}
	if n := runCount(t, env, o.GetId()); n != 2 {
		t.Errorf("runs = %d, want 2", n)
	}
}

func TestFireTimedEventWorksWhenDisabledAndReportsMissingThings(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false)
	disabled := pollEvent()
	disabled.Enabled, disabled.DedupeDisabled = false, true
	ev, err := env.svc.PutTimedEvent(o.GetId(), disabled)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId()); err != nil {
		t.Errorf("a disabled event can be fired by hand: %v", err)
	}
	if _, err := env.svc.FireTimedEvent(o.GetId(), "tev-404"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown event = %v", err)
	}
	if _, err := env.svc.FireTimedEvent("o-none", ev.GetId()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown orchestrator = %v", err)
	}
	// Deleting the action disables the event; firing it reports the missing action.
	if _, err := env.svc.DeleteAction(o.GetId(), "review-pr"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId()); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "action review-pr") {
		t.Errorf("deleted action = %v", err)
	}
}

// --- PR #4 review fixes ---

// The event is read under the lock: a definition replaced or deleted before the
// fire got the lock is never used. Deterministic: the test holds fireMu, parks the
// fire at the seam just before its Lock(), changes the event, then releases.
func TestFireTimedEventUsesTheCurrentDefinitionAndNotADeletedOne(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 0, false)
	ev, _ := env.svc.PutTimedEvent(o.GetId(), pollEvent())

	type result struct {
		run *pb.Run
		err error
	}
	fireAfter := func(change func()) result {
		reached := make(chan struct{})
		rt.fireMu.Lock()
		rt.beforeFireLock = func() { close(reached) }
		done := make(chan result, 1)
		go func() {
			run, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId())
			done <- result{run, err}
		}()
		<-reached // the fire is at the seam; it cannot proceed past Lock() while we hold it
		change()
		rt.beforeFireLock = nil
		rt.fireMu.Unlock()
		return <-done
	}

	// An edit that lands before the fire gets the lock is what the fire uses.
	r := fireAfter(func() {
		upd := pollEvent()
		upd.Id = ev.GetId()
		upd.Arguments = map[string]string{"number": "99", "repo": "acme/web"}
		if _, err := env.svc.PutTimedEvent(o.GetId(), upd); err != nil {
			t.Fatal(err)
		}
	})
	if r.err != nil || r.run.GetArguments()["number"] != "99" {
		t.Fatalf("the fire must use the definition current at lock time: %+v / %v", r.run, r.err)
	}

	// A delete before the lock makes the fire fail NotFound and queue nothing.
	before := runCount(t, env, o.GetId())
	r = fireAfter(func() {
		if err := env.svc.DeleteTimedEvent(o.GetId(), ev.GetId()); err != nil {
			t.Fatal(err)
		}
	})
	if !errors.Is(r.err, ErrNotFound) {
		t.Errorf("a deleted event must not fire; run %+v err %v", r.run, r.err)
	}
	if n := runCount(t, env, o.GetId()); n != before {
		t.Errorf("a deleted event queued a run: %d -> %d", before, n)
	}
}

// Dedupe holds across concurrent manual fires and a scheduled tick: exactly one
// run is queued.
func TestFireTimedEventConcurrentWithTickQueuesOne(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false)
	ev, _ := env.svc.PutTimedEvent(o.GetId(), pollEvent())
	start := make(chan struct{})
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId()); err == nil {
				ok.Add(1)
			} else if errors.Is(err, ErrPrecondition) {
				refused.Add(1)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; env.clock.Advance(time.Minute) }() // the scheduled slot comes due too
	close(start)
	wg.Wait()
	if n := runCount(t, env, o.GetId()); n != 1 {
		t.Errorf("dedupe must hold under concurrency: %d runs (ok %d, refused %d)", n, ok.Load(), refused.Load())
	}
}

func TestFireTimedEventAwaitingReviewRefusalSaysHowToClearIt(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false)
	ev, _ := env.svc.PutTimedEvent(o.GetId(), pollEvent())
	run, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId())
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := env.reg.GetRun(o.GetId(), run.GetId())
	stored.State = pb.RunState_RUN_STATE_AWAITING_REVIEW
	if err := env.reg.PutRun(stored); err != nil {
		t.Fatal(err)
	}
	_, err = env.svc.FireTimedEvent(o.GetId(), ev.GetId())
	if !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "awaiting review") || !strings.Contains(err.Error(), "mark the worker free") {
		t.Errorf("err = %v", err)
	}
}

// The position is where the run queued (>= 1), even when a free worker takes it
// immediately.
func TestFireTimedEventPositionIsNeverZeroWithAFreeWorker(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 1, false)
	ev, _ := env.svc.PutTimedEvent(o.GetId(), pollEvent())
	run, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId())
	if err != nil || run.GetPosition() != 1 {
		t.Errorf("position = %d, err %v; want 1", run.GetPosition(), err)
	}
}

// Review (PR #4, run 43): a manual fire is logged with the name quoted (a newline
// in an event name cannot forge a second line) and AFTER fireMu is released (a slow
// Logf must not hold up the scheduler).
func TestFireTimedEventLogsQuotedNameOutsideTheLock(t *testing.T) {
	env, _, o, rt := dispatchEnv(t, 0, false)
	ev := pollEvent()
	ev.Name = "poll\nforged line"
	saved, err := env.svc.PutTimedEvent(o.GetId(), ev)
	if err != nil {
		t.Fatal(err)
	}
	// Background goroutines (links, the scheduler) log too, so the hook guards its
	// state and probes fireMu only for the manual-fire line itself (tick() may
	// legitimately log under fireMu).
	var mu sync.Mutex
	var manual []string
	lockedWhileLogging := false
	env.setLogHook(func(line string) {
		if !strings.Contains(line, "manual fire") {
			return
		}
		locked := !rt.fireMu.TryLock()
		if !locked {
			rt.fireMu.Unlock()
		}
		mu.Lock()
		defer mu.Unlock()
		manual = append(manual, line)
		lockedWhileLogging = lockedWhileLogging || locked
	})
	t.Cleanup(func() { env.setLogHook(nil) })
	if _, err := env.svc.FireTimedEvent(o.GetId(), saved.GetId()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(manual) != 1 || strings.Contains(manual[0], "\n") || !strings.Contains(manual[0], `"poll\nforged line"`) {
		t.Errorf("want one single-line log with the quoted name; got %q", manual)
	}
	if lockedWhileLogging {
		t.Error("the log call must happen after fireMu is released")
	}
}
