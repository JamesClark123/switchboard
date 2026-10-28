package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the daemon's zone database, so the form's check matches it

	"github.com/charmbracelet/huh"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Timed-event active windows (SWI-10): the editor's "Active from/until", "Days"
// and "Timezone" fields, and the window shown on an event's row. The daemon
// validates the window again and is the authority on the timezone; both embed
// the same zone database, so a zone this host's older zoneinfo lacks is still
// known here.

// weekdayOrder is the Days field's order (Monday first); values are the proto's
// 0=Sunday … 6=Saturday.
var weekdayOrder = []int{1, 2, 3, 4, 5, 6, 0}

// dayOptions are the Days multi-select's options.
func dayOptions() []huh.Option[int] {
	out := make([]huh.Option[int], 0, len(weekdayOrder))
	for _, d := range weekdayOrder {
		out = append(out, huh.NewOption(time.Weekday(d).String()[:3], d))
	}
	return out
}

// zoneinfoPath and timezonePath locate the local zone (vars so tests can point
// them elsewhere).
var (
	zoneinfoPath = "/etc/localtime"
	timezonePath = "/etc/timezone"
)

// localZoneName is this machine's IANA zone, the Timezone field's default: TZ,
// else the zoneinfo file /etc/localtime links to, else /etc/timezone (where
// /etc/localtime is a copy, not a link), else time.Local's name, else UTC.
func localZoneName() string {
	if tz, ok := os.LookupEnv("TZ"); ok {
		if tz = strings.TrimPrefix(tz, ":"); tz != "" && !filepath.IsAbs(tz) {
			return tz
		}
	}
	if target, err := filepath.EvalSymlinks(zoneinfoPath); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok && name != "" {
			return name
		}
	}
	if b, err := os.ReadFile(timezonePath); err == nil {
		if name := strings.TrimSpace(string(b)); name != "" {
			return name
		}
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	return "UTC"
}

// normClock accepts "H:MM" or "HH:MM" (24-hour) and returns "HH:MM".
func normClock(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) == len("1:04") {
		s = "0" + s
	}
	if _, err := time.Parse("15:04", s); err != nil || len(s) != len("15:04") {
		return "", false
	}
	return s, true
}

// windowFromForm turns the editor's window fields into an ActiveWindow: nil when
// both times are blank (around the clock), else a checked window.
func windowFromForm(v *eventFormVals) (*pb.ActiveWindow, error) {
	from, until := strings.TrimSpace(v.activeFrom), strings.TrimSpace(v.activeUntil)
	if from == "" && until == "" {
		if len(v.days) > 0 {
			return nil, fmt.Errorf("days limit an active window: set Active from and Active until too")
		}
		return nil, nil
	}
	if from == "" || until == "" {
		return nil, fmt.Errorf("set both Active from and Active until, or leave both blank to fire around the clock")
	}
	start, ok := normClock(from)
	if !ok {
		return nil, fmt.Errorf("active from %q must be HH:MM (24-hour, e.g. 08:00)", from)
	}
	end, ok := normClock(until)
	if strings.TrimSpace(until) == "24:00" {
		end, ok = "00:00", true // the end is exclusive, so midnight is the next day's 00:00
	}
	if !ok {
		return nil, fmt.Errorf("active until %q must be HH:MM (24-hour, e.g. 17:00)", until)
	}
	if start == end {
		return nil, fmt.Errorf("active from and until are both %s; leave both blank to fire around the clock", start)
	}
	tz := strings.TrimSpace(v.timezone)
	if tz == "" {
		return nil, fmt.Errorf("the window needs a timezone, an IANA name like America/Los_Angeles")
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return nil, fmt.Errorf("unknown timezone %q; use an IANA name like America/Los_Angeles", tz)
	}
	w := &pb.ActiveWindow{Start: start, End: end, Timezone: tz}
	for _, d := range v.days {
		w.Weekdays = append(w.Weekdays, uint32(d))
	}
	slices.Sort(w.Weekdays)
	return w, nil
}

// windowToForm pre-fills the editor's window fields from a saved window.
func windowToForm(v *eventFormVals, w *pb.ActiveWindow) {
	if w == nil {
		return
	}
	v.activeFrom, v.activeUntil, v.timezone = w.GetStart(), windowEnd(w), w.GetTimezone()
	for _, d := range w.GetWeekdays() {
		v.days = append(v.days, int(d))
	}
}

// windowEnd is a window's end as the form spells it: a midnight end is stored
// as 00:00 (the end is exclusive) and shown as the 24:00 the form accepts,
// unless the window starts at midnight too.
func windowEnd(w *pb.ActiveWindow) string {
	if w.GetEnd() == "00:00" && w.GetStart() != "00:00" {
		return "24:00"
	}
	return w.GetEnd()
}

// windowLabel renders a window for an event's row, e.g. "08:00–17:00 Mon–Fri
// America/Los_Angeles"; "" for none.
func windowLabel(w *pb.ActiveWindow) string {
	if w == nil {
		return ""
	}
	return strings.Join(append(windowSpan(w), w.GetTimezone()), " ")
}

// compactWindowLabel is windowLabel for the Jobs Upcoming row, where width is
// short: the zone only when it is not local's, e.g. "08:00–17:00 Mon–Fri".
func compactWindowLabel(w *pb.ActiveWindow, local string) string {
	if w == nil {
		return ""
	}
	parts := windowSpan(w)
	if w.GetTimezone() != local {
		parts = append(parts, w.GetTimezone())
	}
	return strings.Join(parts, " ")
}

// windowSpan is a window's times and, unless every day, its days.
func windowSpan(w *pb.ActiveWindow) []string {
	parts := []string{w.GetStart() + "–" + windowEnd(w)}
	if days := daysLabel(w.GetWeekdays()); days != "" {
		parts = append(parts, days)
	}
	return parts
}

// localZone is localZoneName read once, for rows rendered every frame.
var localZone = sync.OnceValue(localZoneName)

// daysLabel renders weekdays Monday first: "" for every day (none or all seven),
// "Mon–Fri" for a run of three or more, else "Sat,Sun".
func daysLabel(days []uint32) string {
	var set [7]bool
	for _, d := range days {
		if d < 7 {
			set[d] = true
		}
	}
	var in []int // positions in weekdayOrder
	for i, d := range weekdayOrder {
		if set[d] {
			in = append(in, i)
		}
	}
	if len(in) == 0 || len(in) == 7 {
		return ""
	}
	name := func(i int) string { return time.Weekday(weekdayOrder[i]).String()[:3] }
	if len(in) >= 3 && in[len(in)-1]-in[0] == len(in)-1 {
		return name(in[0]) + "–" + name(in[len(in)-1])
	}
	names := make([]string, 0, len(in))
	for _, i := range in {
		names = append(names, name(i))
	}
	return strings.Join(names, ",")
}
