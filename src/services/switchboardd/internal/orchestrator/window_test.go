package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zones below, whatever the test host has installed

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func la(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func mustWindow(t *testing.T, w *pb.ActiveWindow) window {
	t.Helper()
	pw, err := parseWindow(w)
	if err != nil {
		t.Fatalf("parseWindow(%+v): %v", w, err)
	}
	return pw
}

// workdays is 08:00–17:00 Mon–Fri, Pacific.
func workdays() *pb.ActiveWindow {
	return &pb.ActiveWindow{Start: "08:00", End: "17:00", Weekdays: []uint32{1, 2, 3, 4, 5}, Timezone: "America/Los_Angeles"}
}

func TestNormalizeWindow(t *testing.T) {
	for name, tc := range map[string]struct {
		in   *pb.ActiveWindow
		want string // substring of the error; "" = valid
	}{
		"bad start":         {&pb.ActiveWindow{Start: "8am", End: "17:00", Timezone: "UTC"}, `window start "8am" must be HH:MM`},
		"one-digit hour":    {&pb.ActiveWindow{Start: "8:00", End: "17:00", Timezone: "UTC"}, "must be HH:MM"},
		"bad end":           {&pb.ActiveWindow{Start: "08:00", End: "24:00", Timezone: "UTC"}, `window end "24:00"`},
		"missing end":       {&pb.ActiveWindow{Start: "08:00", Timezone: "UTC"}, "window end"},
		"start == end":      {&pb.ActiveWindow{Start: "08:00", End: "08:00", Timezone: "UTC"}, "around the clock"},
		"weekday 7":         {&pb.ActiveWindow{Start: "08:00", End: "17:00", Weekdays: []uint32{7}, Timezone: "UTC"}, "weekday 7 is out of range"},
		"no timezone":       {&pb.ActiveWindow{Start: "08:00", End: "17:00"}, "needs a timezone"},
		"Local":             {&pb.ActiveWindow{Start: "08:00", End: "17:00", Timezone: "Local"}, "needs a timezone"},
		"abbreviation":      {&pb.ActiveWindow{Start: "08:00", End: "17:00", Timezone: "PST"}, `unknown timezone "PST"; use an IANA name like America/Los_Angeles`},
		"valid overnight":   {&pb.ActiveWindow{Start: "22:00", End: "06:00", Timezone: "Europe/London"}, ""},
		"valid with spaces": {&pb.ActiveWindow{Start: " 08:00", End: "17:00 ", Weekdays: []uint32{5, 1, 5}, Timezone: " UTC "}, ""},
	} {
		got, err := normalizeWindow(tc.in)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want ErrInvalid containing %q", name, err, tc.want)
		case tc.want != "" && got != nil:
			t.Errorf("%s: an invalid window was returned: %+v", name, got)
		}
	}
	got, _ := normalizeWindow(&pb.ActiveWindow{Start: " 08:00", End: "17:00 ", Weekdays: []uint32{5, 1, 5}, Timezone: " UTC "})
	if got.GetStart() != "08:00" || got.GetEnd() != "17:00" || got.GetTimezone() != "UTC" || len(got.GetWeekdays()) != 2 || got.GetWeekdays()[0] != 1 || got.GetWeekdays()[1] != 5 {
		t.Errorf("normalised = %+v", got)
	}
	// No window, or a window with nothing set, is "around the clock".
	for _, w := range []*pb.ActiveWindow{nil, {}, {Start: " ", Timezone: " "}} {
		if got, err := normalizeWindow(w); got != nil || err != nil {
			t.Errorf("normalizeWindow(%+v) = %+v, %v; want nil, nil", w, got, err)
		}
	}
}

func TestWindowContainsOpenAndClose(t *testing.T) {
	loc := la(t)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) } // Oct 2026: the 26th is a Monday
	w := mustWindow(t, workdays())
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{
		{at(26, 7, 59), false}, {at(26, 8, 0), true}, {at(26, 16, 59), true}, {at(26, 17, 0), false}, // end is exclusive
		{at(31, 10, 0), false}, {at(25, 10, 0), false}, // Saturday, Sunday
		{at(30, 12, 0), true}, // Friday
	} {
		if got := w.contains(tc.t); got != tc.want {
			t.Errorf("contains(%v) = %v", tc.t, got)
		}
	}
	// Friday evening → Monday morning, skipping the weekend.
	if got := w.nextOpen(at(30, 17, 0)); !got.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, loc)) {
		t.Errorf("nextOpen(Fri 17:00) = %v", got)
	}
	// Strictly after: at an opening, the next one is tomorrow's.
	if got := w.nextOpen(at(27, 8, 0)); !got.Equal(at(28, 8, 0)) {
		t.Errorf("nextOpen(at an opening) = %v", got)
	}
	if got := w.closeAfter(at(27, 8, 0)); !got.Equal(at(27, 17, 0)) {
		t.Errorf("closeAfter = %v", got)
	}

	// Overnight, Fridays only: the window opened Friday 22:00 is open into
	// Saturday morning, but none opens on Saturday night.
	night := mustWindow(t, &pb.ActiveWindow{Start: "22:00", End: "06:00", Weekdays: []uint32{5}, Timezone: "America/Los_Angeles"})
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{
		{at(30, 21, 59), false}, {at(30, 22, 0), true}, {at(31, 3, 0), true}, {at(31, 5, 59), true}, {at(31, 6, 0), false},
		{at(31, 23, 0), false}, {at(30, 3, 0), false}, // Saturday night; Friday early morning belongs to Thursday's window
	} {
		if got := night.contains(tc.t); got != tc.want {
			t.Errorf("overnight contains(%v) = %v", tc.t, got)
		}
	}
	if got := night.closeAfter(at(30, 22, 0)); !got.Equal(at(31, 6, 0)) {
		t.Errorf("overnight closeAfter = %v", got)
	}
	if got := night.nextOpen(at(31, 3, 0)); !got.Equal(time.Date(2026, 11, 6, 22, 0, 0, 0, loc)) {
		t.Errorf("overnight nextOpen = %v", got)
	}
	// A window in another zone is evaluated in that zone, whatever t's location.
	tokyo := mustWindow(t, &pb.ActiveWindow{Start: "09:00", End: "10:00", Timezone: "Asia/Tokyo"})
	if !tokyo.contains(time.Date(2026, 10, 27, 0, 30, 0, 0, time.UTC)) || tokyo.contains(time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)) {
		t.Error("the Tokyo window must be read in Tokyo time")
	}
}

func TestWindowNextFire(t *testing.T) {
	loc := la(t)
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, loc) }
	w := mustWindow(t, workdays())
	const q = 15 * time.Minute
	for _, tc := range []struct {
		name     string
		t, want  time.Time
		interval time.Duration
	}{
		{"before the window", at(27, 6, 0), at(27, 8, 0), q},
		{"at the opening", at(27, 8, 0), at(27, 8, 15), q},
		{"mid window, on the grid", at(27, 10, 7), at(27, 10, 15), q},
		{"exactly on a slot", at(27, 10, 15), at(27, 10, 30), q},
		{"last slot before the close", at(27, 16, 40), at(27, 16, 45), q},
		{"the close is exclusive", at(27, 16, 45), at(28, 8, 0), q},
		{"after the window", at(27, 18, 0), at(28, 8, 0), q},
		{"Friday after the last slot", at(30, 16, 50), time.Date(2026, 11, 2, 8, 0, 0, 0, loc), q},
		{"Saturday", at(31, 9, 0), time.Date(2026, 11, 2, 8, 0, 0, 0, loc), q},
		{"interval longer than the window", at(27, 8, 0), at(28, 8, 0), 10 * time.Hour},
		{"interval that does not divide the window", at(27, 15, 0), at(27, 16, 0), 2 * time.Hour},
	} {
		if got := w.nextFire(tc.t, tc.interval); !got.Equal(tc.want) {
			t.Errorf("%s: nextFire(%v) = %v, want %v", tc.name, tc.t, got, tc.want)
		}
	}
	// Overnight: the grid runs from 22:00 through midnight to the last slot before 06:00.
	night := mustWindow(t, &pb.ActiveWindow{Start: "22:00", End: "06:00", Timezone: "America/Los_Angeles"})
	if got := night.nextFire(at(27, 23, 50), time.Hour); !got.Equal(at(28, 0, 0)) {
		t.Errorf("overnight across midnight = %v", got)
	}
	if got := night.nextFire(at(28, 5, 0), time.Hour); !got.Equal(at(28, 22, 0)) {
		t.Errorf("overnight after the last slot = %v", got)
	}
}

// slots lists every firing of w from `from` (exclusive) up to `until`.
func slots(w window, from, until time.Time, interval time.Duration) []time.Time {
	var out []time.Time
	for t := w.nextFire(from, interval); t.Before(until); t = w.nextFire(t, interval) {
		out = append(out, t)
	}
	return out
}

func TestWindowAcrossDST(t *testing.T) {
	loc := la(t)
	// Spring forward, Sun 8 Mar 2026: 02:00 PST jumps to 03:00 PDT, so the
	// 01:00–04:00 window lasts two real hours.
	w := mustWindow(t, &pb.ActiveWindow{Start: "01:00", End: "04:00", Timezone: "America/Los_Angeles"})
	got := slots(w, time.Date(2026, 3, 8, 0, 0, 0, 0, loc), time.Date(2026, 3, 8, 12, 0, 0, 0, loc), 30*time.Minute)
	if len(got) != 4 {
		t.Fatalf("spring-forward slots = %v, want 4", got)
	}
	for i, s := range got {
		if !w.contains(s) || (i > 0 && s.Sub(got[i-1]) != 30*time.Minute) {
			t.Errorf("spring-forward slot %d = %v (outside the window or off the interval)", i, s)
		}
	}
	// A window opening inside the gap (02:30 does not exist that day) opens at
	// the normalised 03:30 PDT rather than being skipped.
	gap := mustWindow(t, &pb.ActiveWindow{Start: "02:30", End: "05:00", Timezone: "America/Los_Angeles"})
	if got := gap.nextOpen(time.Date(2026, 3, 8, 0, 0, 0, 0, loc)); !got.Equal(time.Date(2026, 3, 8, 3, 30, 0, 0, loc)) {
		t.Errorf("opening in the DST gap = %v", got)
	}

	// Where the gap is at midnight (Chile springs forward from 00:00 to 01:00 on
	// Sun 6 Sep 2026), a 00:00 opening moves to 01:00 the same day, not the
	// evening before.
	scl, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	mid := mustWindow(t, &pb.ActiveWindow{Start: "00:00", End: "02:00", Timezone: "America/Santiago"})
	if got := mid.nextOpen(time.Date(2026, 9, 5, 12, 0, 0, 0, scl)).In(scl); got.Day() != 6 || got.Hour() != 1 || got.Minute() != 0 {
		t.Errorf("opening in a midnight gap = %v, want 6 Sep 01:00", got)
	}

	// Fall back, Sun 1 Nov 2026: 02:00 PDT repeats as 01:00 PST, so the
	// 00:30–03:00 window lasts three and a half real hours, fired every 30m
	// without a double fire at the repeated hour.
	fb := mustWindow(t, &pb.ActiveWindow{Start: "00:30", End: "03:00", Timezone: "America/Los_Angeles"})
	got = slots(fb, time.Date(2026, 11, 1, 0, 0, 0, 0, loc), time.Date(2026, 11, 1, 12, 0, 0, 0, loc), 30*time.Minute)
	if len(got) != 7 {
		t.Fatalf("fall-back slots = %v, want 7", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Sub(got[i-1]) != 30*time.Minute {
			t.Errorf("fall-back slots %v and %v are not one interval apart", got[i-1], got[i])
		}
	}
	// The ordinary daytime window still opens at 08:00 local on both sides of the change.
	day := mustWindow(t, workdays())
	if o := day.nextOpen(time.Date(2026, 10, 30, 18, 0, 0, 0, loc)); o.In(loc).Hour() != 8 || o.In(loc).Weekday() != time.Monday {
		t.Errorf("opening after fall-back = %v", o.In(loc))
	}
}

// setClock moves the fake clock forward to t.
func setClock(t *testing.T, env *testEnv, to time.Time) {
	t.Helper()
	d := to.Sub(env.clock.Now())
	if d < 0 {
		t.Fatalf("setClock(%v) is in the past of %v", to, env.clock.Now())
	}
	env.clock.Advance(d)
}

func windowedEvent() *pb.TimedEvent {
	ev := pollEvent()
	ev.IntervalSeconds, ev.DedupeDisabled, ev.Window = 15*60, true, workdays()
	return ev
}

func TestWindowedEventFiresOnlyInsideItsWindow(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false) // no worker: every run stays queued
	loc := la(t)
	// Friday 30 Oct 2026, 07:00 Pacific; the weekend after it ends DST.
	setClock(t, env, time.Date(2026, 10, 30, 7, 0, 0, 0, loc))
	ev, err := env.svc.PutTimedEvent(o.GetId(), windowedEvent())
	if err != nil {
		t.Fatal(err)
	}
	if got := ev.GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, loc)) {
		t.Fatalf("first fire = %v, want the opening", got.In(loc))
	}
	if ev.GetWindow().GetTimezone() != "America/Los_Angeles" || len(ev.GetWindow().GetWeekdays()) != 5 {
		t.Errorf("window not stored: %+v", ev.GetWindow())
	}
	// Step from firing to firing for the whole day.
	var fired []time.Time
	for range 40 {
		next := eventOf(t, env, o.GetId(), ev.GetId()).GetNextFireAt().AsTime()
		if next.In(loc).Day() != 30 {
			break
		}
		setClock(t, env, next)
		fired = append(fired, next)
	}
	if len(fired) != 36 || !fired[0].Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, loc)) || !fired[35].Equal(time.Date(2026, 10, 30, 16, 45, 0, 0, loc)) {
		t.Fatalf("fired %d times, first %v last %v; want 36 from 08:00 to 16:45", len(fired), fired[0].In(loc), fired[len(fired)-1].In(loc))
	}
	if n := runCount(t, env, o.GetId()); n != 36 {
		t.Errorf("runs = %d, want 36", n)
	}
	// After the last slot the timer sleeps through the weekend (and the DST
	// change) to Monday 08:00 PST.
	if got := eventOf(t, env, o.GetId(), ev.GetId()).GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, loc)) {
		t.Errorf("next after Friday = %v, want Monday 08:00", got.In(loc))
	}
	setClock(t, env, time.Date(2026, 11, 2, 7, 59, 0, 0, loc))
	if n := runCount(t, env, o.GetId()); n != 36 {
		t.Errorf("fired outside the window: %d runs", n)
	}

	// Enqueue now ignores the window.
	if _, err := env.svc.FireTimedEvent(o.GetId(), ev.GetId()); err != nil {
		t.Errorf("FireTimedEvent outside the window: %v", err)
	}
	if n := runCount(t, env, o.GetId()); n != 37 {
		t.Errorf("runs after Enqueue now = %d, want 37", n)
	}
}

func TestWindowedEventEditsAndRestart(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false)
	loc := la(t)
	setClock(t, env, time.Date(2026, 10, 27, 10, 7, 0, 0, loc)) // a Tuesday, mid-window
	ev, err := env.svc.PutTimedEvent(o.GetId(), windowedEvent())
	if err != nil {
		t.Fatal(err)
	}
	// Created mid-window: the next slot on the grid, not now.
	if got := ev.GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 10, 27, 10, 15, 0, 0, loc)) {
		t.Fatalf("created mid-window: next = %v", got.In(loc))
	}
	// An edit that keeps the schedule keeps the slot.
	same := windowedEvent()
	same.Id, same.Name = ev.GetId(), "renamed"
	same.Window.Weekdays = []uint32{5, 4, 3, 2, 1, 1} // the same days, unsorted
	if ev, err = env.svc.PutTimedEvent(o.GetId(), same); err != nil {
		t.Fatal(err)
	}
	if got := ev.GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 10, 27, 10, 15, 0, 0, loc)) {
		t.Errorf("an unchanged window moved the slot: %v", got.In(loc))
	}
	// A new window restarts the phase on its own grid.
	moved := windowedEvent()
	moved.Id, moved.Window.Start = ev.GetId(), "13:00"
	if ev, err = env.svc.PutTimedEvent(o.GetId(), moved); err != nil {
		t.Fatal(err)
	}
	if got := ev.GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 10, 27, 13, 0, 0, 0, loc)) {
		t.Errorf("after moving the window: next = %v, want 13:00", got.In(loc))
	}
	// Removing the window goes back to around the clock: now + interval.
	plain := windowedEvent()
	plain.Id, plain.Window = ev.GetId(), nil
	if ev, err = env.svc.PutTimedEvent(o.GetId(), plain); err != nil {
		t.Fatal(err)
	}
	if ev.GetWindow() != nil || !ev.GetNextFireAt().AsTime().Equal(env.clock.Now().Add(15*time.Minute)) {
		t.Errorf("without a window: %+v", ev)
	}
	// An invalid window is refused and leaves the event as it was.
	bad := windowedEvent()
	bad.Id, bad.Window.Timezone = ev.GetId(), "PST"
	if _, err := env.svc.PutTimedEvent(o.GetId(), bad); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "IANA") {
		t.Errorf("invalid timezone = %v", err)
	}
	if got := eventOf(t, env, o.GetId(), ev.GetId()); got.GetWindow() != nil {
		t.Errorf("a refused edit changed the event: %+v", got)
	}

	// Back to the work-day window; the daemon stops at 16:50 and restarts at
	// 02:00. Nothing missed overnight is replayed and the event resumes at the
	// next opening.
	again := windowedEvent()
	again.Id = ev.GetId()
	if _, err := env.svc.PutTimedEvent(o.GetId(), again); err != nil {
		t.Fatal(err)
	}
	setClock(t, env, time.Date(2026, 10, 27, 16, 50, 0, 0, loc))
	runsBefore := runCount(t, env, o.GetId())
	env.svc.Stop()
	setClock(t, env, time.Date(2026, 10, 28, 2, 0, 0, 0, loc))
	svc2 := NewService(Deps{Store: env.reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test"})
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc2.Stop)
	if n := runCount(t, env, o.GetId()); n != runsBefore {
		t.Errorf("restart replayed firings: %d runs, want %d", n, runsBefore)
	}
	if got := eventOf(t, env, o.GetId(), ev.GetId()).GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 10, 28, 8, 0, 0, 0, loc)) {
		t.Errorf("next after restart = %v, want 08:00", got.In(loc))
	}
	// A restart mid-window resumes on the window's grid.
	svc2.Stop()
	setClock(t, env, time.Date(2026, 10, 28, 9, 22, 0, 0, loc))
	svc3 := NewService(Deps{Store: env.reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test"})
	if err := svc3.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc3.Stop)
	if got := eventOf(t, env, o.GetId(), ev.GetId()).GetNextFireAt().AsTime(); !got.Equal(time.Date(2026, 10, 28, 9, 30, 0, 0, loc)) {
		t.Errorf("next after a mid-window restart = %v, want 09:30", got.In(loc))
	}
}

// A dedupe skip inside the window advances on the window's grid too.
func TestWindowedEventSkipStaysOnTheGrid(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false)
	loc := la(t)
	setClock(t, env, time.Date(2026, 10, 27, 16, 20, 0, 0, loc))
	in := windowedEvent()
	in.DedupeDisabled = false
	ev, err := env.svc.PutTimedEvent(o.GetId(), in)
	if err != nil {
		t.Fatal(err)
	}
	setClock(t, env, time.Date(2026, 10, 27, 16, 30, 0, 0, loc)) // fires; the run stays queued
	setClock(t, env, time.Date(2026, 10, 27, 16, 45, 0, 0, loc)) // skipped (dedupe)
	got := eventOf(t, env, o.GetId(), ev.GetId())
	if got.GetSkippedCount() != 1 || !got.GetNextFireAt().AsTime().Equal(time.Date(2026, 10, 28, 8, 0, 0, 0, loc)) {
		t.Errorf("after the skip: skipped %d, next %v", got.GetSkippedCount(), got.GetNextFireAt().AsTime().In(loc))
	}
}

// A timer that runs late past the window's close (a suspended host) drops the
// slot instead of firing outside the window, and resumes at the next opening.
func TestWindowedEventLateTimerDoesNotFireOutsideTheWindow(t *testing.T) {
	env, _, o, _ := dispatchEnv(t, 0, false)
	loc := la(t)
	setClock(t, env, time.Date(2026, 10, 30, 16, 40, 0, 0, loc)) // Friday
	ev, err := env.svc.PutTimedEvent(o.GetId(), windowedEvent())
	if err != nil {
		t.Fatal(err)
	}
	// The host sleeps from before 16:45 until Saturday 10:00; the timer fires then.
	setClock(t, env, time.Date(2026, 10, 31, 10, 0, 0, 0, loc))
	got := eventOf(t, env, o.GetId(), ev.GetId())
	if n := runCount(t, env, o.GetId()); n != 0 {
		t.Errorf("fired on Saturday: %d runs", n)
	}
	if got.GetLastFiredAt() != nil || got.GetSkippedCount() != 0 || !got.GetNextFireAt().AsTime().Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, loc)) {
		t.Errorf("after a late tick = %+v", got)
	}
	// A slot reached late but still inside its window fires as usual.
	setClock(t, env, time.Date(2026, 11, 2, 8, 5, 0, 0, loc))
	if n := runCount(t, env, o.GetId()); n != 1 {
		t.Errorf("a late slot inside the window should fire: %d runs", n)
	}
	if missedWindow(pollEvent(), env.clock.Now()) {
		t.Error("an event without a window never misses one")
	}
}
