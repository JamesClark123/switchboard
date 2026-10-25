package orchestrator

import (
	"fmt"
	"strings"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Timed events (research R10, data-model "TimedEvent"): one timer per
// orchestrator armed for the earliest enabled next_fire_at. A firing calls the
// action as a TIMED event with the event as origin; with dedupe on (the
// default) a firing is skipped while a run of this event is queued or in
// flight. After a restart the next firing is the first slot on the event's
// phase — never a replay.

type scheduler struct {
	mu    sync.Mutex
	timer Timer
}

// PutTimedEvent creates or replaces a timed event (FR-130/FR-157).
func (s *Service) PutTimedEvent(id string, ev *pb.TimedEvent) (*pb.TimedEvent, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(ev.GetName())
	switch {
	case name == "":
		return nil, fmt.Errorf("%w: event name is required", ErrInvalid)
	case ev.GetIntervalSeconds() < minIntervalSeconds:
		return nil, fmt.Errorf("%w: the interval must be at least %d seconds", ErrInvalid, minIntervalSeconds)
	}
	now := s.clock.Now()
	var saved *pb.TimedEvent
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		action := findAction(o, ev.GetAction())
		if action == nil {
			return fmt.Errorf("%w: action %s does not exist", ErrInvalid, ev.GetAction())
		}
		if err := validateCall(action, ev.GetArguments()); err != nil {
			return err
		}
		next := timestamppb.New(now.Add(time.Duration(ev.GetIntervalSeconds()) * time.Second))
		if ev.GetId() == "" {
			seq := 0
			for _, ex := range o.GetTimedEvents() {
				var n int
				if _, err := fmt.Sscanf(ex.GetId(), "tev-%d", &n); err == nil && n > seq {
					seq = n
				}
			}
			created := &pb.TimedEvent{
				Id: fmt.Sprintf("tev-%d", seq+1), Name: name, Action: ev.GetAction(), Arguments: ev.GetArguments(),
				IntervalSeconds: ev.GetIntervalSeconds(), Enabled: ev.GetEnabled(), DedupeDisabled: ev.GetDedupeDisabled(),
				NextFireAt: next,
			}
			o.TimedEvents = append(o.TimedEvents, created)
			saved = created
			return nil
		}
		for i, ex := range o.TimedEvents {
			if ex.GetId() != ev.GetId() {
				continue
			}
			upd := proto.Clone(ex).(*pb.TimedEvent)
			upd.Name, upd.Action, upd.Arguments = name, ev.GetAction(), ev.GetArguments()
			upd.Enabled, upd.DedupeDisabled = ev.GetEnabled(), ev.GetDedupeDisabled()
			if upd.GetEnabled() {
				upd.DisabledReason = ""
			}
			// A new interval, a re-enable or a missing slot restarts the phase
			// from now (a re-enabled event never replays the time it was off).
			if ex.GetIntervalSeconds() != ev.GetIntervalSeconds() || ex.GetNextFireAt() == nil || (!ex.GetEnabled() && ev.GetEnabled()) {
				upd.NextFireAt = next
			}
			upd.IntervalSeconds = ev.GetIntervalSeconds()
			o.TimedEvents[i] = upd
			saved = upd
			return nil
		}
		return fmt.Errorf("%w: event %s", ErrNotFound, ev.GetId())
	}); err != nil {
		return nil, err
	}
	s.emitChanged(id)
	rt.rearmScheduler()
	return proto.Clone(saved).(*pb.TimedEvent), nil
}

// DeleteTimedEvent removes an event.
func (s *Service) DeleteTimedEvent(id, eventID string) error {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return err
	}
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		kept := o.TimedEvents[:0]
		found := false
		for _, ex := range o.TimedEvents {
			if ex.GetId() == eventID {
				found = true
				continue
			}
			kept = append(kept, ex)
		}
		if !found {
			return fmt.Errorf("%w: event %s", ErrNotFound, eventID)
		}
		o.TimedEvents = kept
		return nil
	}); err != nil {
		return err
	}
	s.emitChanged(id)
	rt.rearmScheduler()
	return nil
}

// FireTimedEvent enqueues one run of a timed event now (SWI-4), as that event:
// EVENT_KIND_TIMED with origin = the event id, so it counts in the event's
// waiting column and is attributed to it in the queue and history.
//
// The schedule is untouched — next_fire_at, last_fired_at, last_skipped_at and
// skipped_count do not move (last_fired_at drives the phase math after a restart).
// With dedupe on (the default) a run of this event that is queued or in flight
// refuses the call with ErrPrecondition naming that run, keeping FR-157's "never
// more than one run anywhere at a time"; with dedupe off it always enqueues. A
// disabled event can be fired; one whose action was deleted fails with callAction's
// not-found error. A scheduled firing that comes due while this run is still open
// (dedupe on) is skipped and recorded as usual.
func (s *Service) FireTimedEvent(id, eventID string) (*pb.Run, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	// Take the lock first and read the event under it, as tick() does, so an edit
	// or delete racing this call can never queue a stale or just-deleted definition.
	rt.fireMu.Lock()
	defer rt.fireMu.Unlock()
	var ev *pb.TimedEvent
	for _, e := range rt.record().GetTimedEvents() {
		if e.GetId() == eventID {
			ev = e
			break
		}
	}
	if ev == nil {
		return nil, fmt.Errorf("%w: event %s", ErrNotFound, eventID)
	}
	if !ev.GetDedupeDisabled() {
		if open := rt.openRunFor(ev.GetId()); open != nil {
			hint := ""
			if open.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW {
				hint = " (mark the worker free in the Queue view to clear it)"
			}
			return nil, fmt.Errorf("%w: run %s of %s is already %s%s", ErrPrecondition, open.GetId(), ev.GetName(), runStateLabel(open.GetState()), hint)
		}
	}
	return rt.callAction(ev.GetAction(), ev.GetArguments(), pb.EventKind_EVENT_KIND_TIMED, ev.GetId(), ev.GetName())
}

// runStateLabel is a run state in plain words for an error message.
func runStateLabel(st pb.RunState) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(st.String(), "RUN_STATE_"), "_", " "))
}

// startScheduler restores every event's phase after a (re)start and arms the timer.
func (rt *runtime) startScheduler() {
	rt.sched = &scheduler{}
	now := rt.svc.clock.Now()
	_, _ = rt.update(func(o *pb.Orchestrator) error {
		for _, ev := range o.GetTimedEvents() {
			if !ev.GetEnabled() {
				continue
			}
			ev.NextFireAt = timestamppb.New(nextSlot(ev, now))
		}
		return nil
	})
	rt.rearmScheduler()
}

// nextSlot is the first firing time on the event's phase after now: the
// slot after last_fired_at, else next_fire_at if it is still ahead, else now +
// interval (never a replay).
func nextSlot(ev *pb.TimedEvent, now time.Time) time.Time {
	interval := time.Duration(ev.GetIntervalSeconds()) * time.Second
	if interval <= 0 {
		interval = minIntervalSeconds * time.Second
	}
	if ev.GetLastFiredAt() != nil {
		next := ev.GetLastFiredAt().AsTime().Add(interval)
		for !next.After(now) {
			next = next.Add(interval)
		}
		return next
	}
	if ev.GetNextFireAt() != nil && ev.GetNextFireAt().AsTime().After(now) {
		return ev.GetNextFireAt().AsTime()
	}
	return now.Add(interval)
}

func (rt *runtime) stopScheduler() {
	if rt.sched == nil {
		return
	}
	rt.sched.mu.Lock()
	defer rt.sched.mu.Unlock()
	if rt.sched.timer != nil {
		rt.sched.timer.Stop()
		rt.sched.timer = nil
	}
}

// rearmScheduler arms the timer for the earliest enabled next_fire_at.
func (rt *runtime) rearmScheduler() {
	if rt.sched == nil || rt.ctx.Err() != nil {
		return
	}
	rec := rt.record()
	var earliest time.Time
	for _, ev := range rec.GetTimedEvents() {
		if !ev.GetEnabled() || ev.GetNextFireAt() == nil {
			continue
		}
		t := ev.GetNextFireAt().AsTime()
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	rt.sched.mu.Lock()
	defer rt.sched.mu.Unlock()
	if rt.sched.timer != nil {
		rt.sched.timer.Stop()
		rt.sched.timer = nil
	}
	if earliest.IsZero() {
		return
	}
	delay := earliest.Sub(rt.svc.clock.Now())
	if delay < 0 {
		delay = 0
	}
	rt.sched.timer = rt.svc.clock.AfterFunc(delay, rt.tick)
}

// tick fires every due event, advances its phase and re-arms.
func (rt *runtime) tick() {
	if rt.ctx.Err() != nil {
		return
	}
	rt.fireMu.Lock()
	defer rt.fireMu.Unlock()
	now := rt.svc.clock.Now()
	type firing struct {
		ev   *pb.TimedEvent
		skip bool
	}
	var due []firing
	rec := rt.record()
	for _, ev := range rec.GetTimedEvents() {
		if !ev.GetEnabled() || ev.GetNextFireAt() == nil || ev.GetNextFireAt().AsTime().After(now) {
			continue
		}
		due = append(due, firing{ev: ev, skip: !ev.GetDedupeDisabled() && rt.hasOpenRun(ev.GetId())})
	}
	for _, f := range due {
		if !f.skip {
			if _, err := rt.callAction(f.ev.GetAction(), f.ev.GetArguments(), pb.EventKind_EVENT_KIND_TIMED, f.ev.GetId(), f.ev.GetName()); err != nil {
				rt.svc.logf("[orchestrator %s] timed event %s: %v", rec.GetName(), f.ev.GetName(), err)
			}
		}
	}
	if len(due) > 0 {
		_, _ = rt.update(func(o *pb.Orchestrator) error {
			for _, f := range due {
				for _, ev := range o.GetTimedEvents() {
					if ev.GetId() != f.ev.GetId() {
						continue
					}
					interval := time.Duration(ev.GetIntervalSeconds()) * time.Second
					if f.skip {
						ev.LastSkippedAt = timestamppb.New(now)
						ev.SkippedCount++
					} else {
						ev.LastFiredAt = timestamppb.New(now)
					}
					next := ev.GetNextFireAt().AsTime().Add(interval)
					for !next.After(now) {
						next = next.Add(interval)
					}
					ev.NextFireAt = timestamppb.New(next)
				}
			}
			return nil
		})
		rt.svc.emitChanged(rt.id)
	}
	rt.rearmScheduler()
}

// hasOpenRun reports whether a run with this origin is queued or in flight.
func (rt *runtime) hasOpenRun(origin string) bool { return rt.openRunFor(origin) != nil }

// openRunFor returns a run with this origin that is queued or in flight, or nil.
func (rt *runtime) openRunFor(origin string) *pb.Run {
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return nil
	}
	for _, r := range runs {
		if r.GetOrigin() == origin && (r.GetState() == pb.RunState_RUN_STATE_QUEUED || inFlight(r)) {
			return r
		}
	}
	return nil
}
