package orchestrator

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// Active windows (SWI-10): a timed event may be limited to a daily HH:MM–HH:MM
// window in an IANA timezone, optionally on some weekdays only. It then fires at
// each opening and every interval after it until the close, so its phase restarts
// every day. Everything here is pure (no clock, no state) and works in the
// window's own location. Across DST (wallTime): a wall time that does not exist
// that day (spring forward) moves forward past the gap, and one that occurs
// twice (fall back) is its first occurrence.

// window is a parsed, valid ActiveWindow.
type window struct {
	startMin, endMin int     // minutes after local midnight; end is exclusive
	days             [7]bool // by time.Weekday; all true when no weekday was given
	loc              *time.Location
}

// normalizeWindow validates a window and returns its canonical form (trimmed
// fields, weekdays sorted and de-duplicated), or nil for no window. A window
// message with every field empty is treated as no window.
func normalizeWindow(w *pb.ActiveWindow) (*pb.ActiveWindow, error) {
	if w == nil {
		return nil, nil
	}
	n := &pb.ActiveWindow{Start: strings.TrimSpace(w.GetStart()), End: strings.TrimSpace(w.GetEnd()), Timezone: strings.TrimSpace(w.GetTimezone())}
	var seen [7]bool
	for _, d := range w.GetWeekdays() {
		// Range-checked here, before parseWindow, so a long client-supplied
		// list is refused at its first bad value.
		if d > 6 {
			return nil, fmt.Errorf("%w: window weekday %d is out of range (0=Sunday … 6=Saturday)", ErrInvalid, d)
		}
		if !seen[d] {
			seen[d] = true
			n.Weekdays = append(n.Weekdays, d)
		}
	}
	slices.Sort(n.Weekdays)
	if n.Start == "" && n.End == "" && n.Timezone == "" && len(n.Weekdays) == 0 {
		return nil, nil
	}
	if _, err := parseWindow(n); err != nil {
		return nil, err
	}
	return n, nil
}

// parseWindow validates an ActiveWindow and returns its parsed form.
func parseWindow(w *pb.ActiveWindow) (window, error) {
	var out window
	var err error
	if out.startMin, err = parseClock("start", w.GetStart()); err != nil {
		return out, err
	}
	if out.endMin, err = parseClock("end", w.GetEnd()); err != nil {
		return out, err
	}
	if out.startMin == out.endMin {
		return out, fmt.Errorf("%w: the window's start and end are both %s; leave the window empty to fire around the clock", ErrInvalid, w.GetStart())
	}
	for _, d := range w.GetWeekdays() {
		if d > 6 {
			return out, fmt.Errorf("%w: window weekday %d is out of range (0=Sunday … 6=Saturday)", ErrInvalid, d)
		}
		out.days[d] = true
	}
	if len(w.GetWeekdays()) == 0 {
		out.days = [7]bool{true, true, true, true, true, true, true}
	}
	tz := w.GetTimezone()
	// "Local" would mean the daemon's own zone, which the developer cannot see.
	if tz == "" || tz == "Local" {
		return out, fmt.Errorf("%w: the window needs a timezone, an IANA name like America/Los_Angeles", ErrInvalid)
	}
	if out.loc, err = loadLocation(tz); err != nil {
		return out, fmt.Errorf("%w: unknown timezone %q; use an IANA name like America/Los_Angeles", ErrInvalid, tz)
	}
	return out, nil
}

// locations caches loaded zones: windows are parsed on every scheduler step,
// and time.LoadLocation reads the zone from disk each time.
var locations sync.Map // name -> *time.Location

func loadLocation(name string) (*time.Location, error) {
	if loc, ok := locations.Load(name); ok {
		return loc.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	locations.Store(name, loc)
	return loc, nil
}

// parseClock parses a 24-hour "HH:MM" into minutes after midnight.
func parseClock(field, s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != len("15:04") {
		return 0, fmt.Errorf("%w: window %s %q must be HH:MM (24-hour, e.g. 08:00)", ErrInvalid, field, s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// wallTime is minute-of-day min on the local day y-m-d (d may overflow; it is
// normalised). time.Date may resolve a wall time inside a spring-forward gap to
// the earlier offset (02:30 → 01:30 PST); moving it on by the shortfall gives
// the same wall time after the gap (03:30 PDT), so a window never opens early.
func wallTime(y int, m time.Month, d, min int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, min/60, min%60, 0, 0, loc)
	// Compare wall clocks as naive times so a gap at midnight (which resolves to
	// the previous day) is handled like any other.
	want := time.Date(y, m, d, min/60, min%60, 0, 0, time.UTC)
	gy, gm, gd := t.Date()
	if got := time.Date(gy, gm, gd, t.Hour(), t.Minute(), 0, 0, time.UTC); got.Before(want) {
		t = t.Add(want.Sub(got))
	}
	return t
}

// openOn is the window's opening on the local calendar day of t shifted by
// offset days.
func (w window) openOn(t time.Time, offset int) time.Time {
	y, m, d := t.In(w.loc).Date()
	return wallTime(y, m, d+offset, w.startMin, w.loc)
}

// closeAfter is the close of the window that opens at open: later that day, or
// the next day for an overnight window.
func (w window) closeAfter(open time.Time) time.Time {
	y, m, d := open.In(w.loc).Date()
	if w.endMin < w.startMin {
		d++
	}
	return wallTime(y, m, d, w.endMin, w.loc)
}

// opensOn reports whether a window opens on open's weekday.
func (w window) opensOn(open time.Time) bool { return w.days[open.In(w.loc).Weekday()] }

// openAt returns the opening of the window containing t, if t is inside one. Only
// today's and yesterday's openings can contain t (an overnight window opened
// yesterday may still be open).
func (w window) openAt(t time.Time) (time.Time, bool) {
	for _, off := range []int{0, -1} {
		o := w.openOn(t, off)
		if w.opensOn(o) && !t.Before(o) && t.Before(w.closeAfter(o)) {
			return o, true
		}
	}
	return time.Time{}, false
}

// contains reports whether t falls inside a window.
func (w window) contains(t time.Time) bool {
	_, ok := w.openAt(t)
	return ok
}

// nextOpen is the first window opening strictly after t (weekdays considered).
// Every day of the week is checked, so a window that opens on any day is found.
func (w window) nextOpen(t time.Time) time.Time {
	for off := 0; off <= windowSearchDays; off++ {
		if o := w.openOn(t, off); o.After(t) && w.opensOn(o) {
			return o
		}
	}
	// Unreachable for a parsed window (at least one weekday is set); fall back to
	// a day ahead so a caller never spins.
	return t.Add(24 * time.Hour)
}

// nextFire is the first firing strictly after t: the next slot o + k·interval of
// the window that is open at t, while before its close, else the next opening.
func (w window) nextFire(t time.Time, interval time.Duration) time.Time {
	if o, ok := w.openAt(t); ok {
		k := t.Sub(o)/interval + 1
		if next := o.Add(k * interval); next.Before(w.closeAfter(o)) {
			return next
		}
	}
	return w.nextOpen(t)
}

// eventWindow returns an event's parsed window, if it has a valid one. Windows
// are validated on save, so an invalid stored one (none in practice) is treated
// as no window rather than stopping the event.
func eventWindow(ev *pb.TimedEvent) (window, bool) {
	if ev.GetWindow() == nil {
		return window{}, false
	}
	w, err := parseWindow(ev.GetWindow())
	return w, err == nil
}

// sameWindow reports whether two (normalised) windows are the same.
func sameWindow(a, b *pb.ActiveWindow) bool { return proto.Equal(a, b) }
