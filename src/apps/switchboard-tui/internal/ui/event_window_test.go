package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestEventWindowFormRoundTrip(t *testing.T) {
	t.Setenv("TZ", "Europe/Paris")
	ev := &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 900, Enabled: true, Arguments: map[string]string{"number": "1", "repo": "r"},
		Window: &pb.ActiveWindow{Start: "08:00", End: "17:00", Weekdays: []uint32{1, 2, 3, 4, 5}, Timezone: "America/Los_Angeles"}}
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{ev}}}}}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	if view := m.View(); !strings.Contains(view, "every 15m · 08:00–17:00 Mon–Fri America/Los_Angeles") {
		t.Errorf("row should show the window:\n%s", view)
	}

	// e pre-fills the window fields.
	m, _ = update(m, press("e"))
	v := m.jobs.formVals.(*eventFormVals)
	if v.activeFrom != "08:00" || v.activeUntil != "17:00" || v.timezone != "America/Los_Angeles" || len(v.days) != 5 {
		t.Fatalf("edit pre-fill = %+v", v)
	}
	// Local checks refuse a bad window before anything is sent.
	for _, tc := range []struct{ from, until, tz, want string }{
		{"8am", "17:00", "UTC", `active from "8am" must be HH:MM`},
		{"08:00", "25:00", "UTC", `active until "25:00" must be HH:MM`},
		{"08:00", "", "UTC", "set both Active from and Active until"},
		{"08:00", "8:00", "UTC", "both 08:00"},
		{"08:00", "17:00", " ", "needs a timezone"},
	} {
		v.activeFrom, v.activeUntil, v.timezone = tc.from, tc.until, tc.tz
		m, _ = update(m, ctrlS())
		if !strings.Contains(m.jobs.status, tc.want) {
			t.Errorf("%+v: status = %q, want %q", tc, m.jobs.status, tc.want)
		}
	}
	if len(a.putEvents) != 0 {
		t.Fatalf("a refused window was sent: %+v", a.putEvents)
	}
	// A valid overnight window on two days is sent normalised.
	v.activeFrom, v.activeUntil, v.timezone, v.days = "22:00", "6:00", " Asia/Tokyo ", []int{6, 0}
	m, cmd := update(m, ctrlS())
	m = feed2(m, cmd)
	if len(a.putEvents) != 1 {
		t.Fatalf("save calls = %q", a.orchCallsJoined())
	}
	got := a.putEvents[0].GetWindow()
	if got.GetStart() != "22:00" || got.GetEnd() != "06:00" || got.GetTimezone() != "Asia/Tokyo" || len(got.GetWeekdays()) != 2 || got.GetWeekdays()[0] != 0 || got.GetWeekdays()[1] != 6 {
		t.Errorf("sent window = %+v", got)
	}
	if m.jobs.form != nil {
		t.Error("a successful save closes the form")
	}

	// A new event defaults the timezone to the local zone and sends no window
	// when the times are blank; days without times are refused.
	m, _ = update(m, press("a"))
	v = m.jobs.formVals.(*eventFormVals)
	if v.timezone != "Europe/Paris" || v.activeFrom != "" || v.days != nil {
		t.Errorf("new event defaults = %+v", v)
	}
	v.name, v.args = "always", "number = 2\nrepo = r"
	v.days = []int{1}
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.jobs.status, "days limit an active window") {
		t.Errorf("days without times: status = %q", m.jobs.status)
	}
	v.days = nil
	m, cmd = update(m, ctrlS())
	_ = feed2(m, cmd)
	if n := len(a.putEvents); n != 2 || a.putEvents[1].GetWindow() != nil {
		t.Errorf("blank window should be sent as none: %+v", a.putEvents)
	}
}

func TestDaysAndWindowLabels(t *testing.T) {
	for _, tc := range []struct {
		days []uint32
		want string
	}{
		{nil, ""},
		{[]uint32{0, 1, 2, 3, 4, 5, 6}, ""},
		{[]uint32{1, 2, 3, 4, 5}, "Mon–Fri"},
		{[]uint32{6, 0}, "Sat,Sun"},
		{[]uint32{5, 6, 0}, "Fri–Sun"},
		{[]uint32{1, 3, 5}, "Mon,Wed,Fri"},
		{[]uint32{0, 1}, "Mon,Sun"},
		{[]uint32{9}, ""},
	} {
		if got := daysLabel(tc.days); got != tc.want {
			t.Errorf("daysLabel(%v) = %q, want %q", tc.days, got, tc.want)
		}
	}
	if windowLabel(nil) != "" {
		t.Error("no window, no label")
	}
	if got := windowLabel(&pb.ActiveWindow{Start: "22:00", End: "06:00", Timezone: "UTC"}); got != "22:00–06:00 UTC" {
		t.Errorf("every-day window = %q", got)
	}
	// windowToForm with no window leaves the defaults alone.
	v := &eventFormVals{timezone: "UTC"}
	windowToForm(v, nil)
	if v.timezone != "UTC" || v.activeFrom != "" {
		t.Errorf("windowToForm(nil) = %+v", v)
	}
}

func TestLocalZoneName(t *testing.T) {
	old := zoneinfoPath
	t.Cleanup(func() { zoneinfoPath = old })
	dir := t.TempDir()
	target := filepath.Join(dir, "usr", "share", "zoneinfo", "America", "Denver")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "localtime")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	zoneinfoPath = link

	t.Setenv("TZ", ":Asia/Tokyo")
	if got := localZoneName(); got != "Asia/Tokyo" {
		t.Errorf("TZ=:Asia/Tokyo → %q", got)
	}
	os.Unsetenv("TZ") //nolint:errcheck // t.Setenv restores it
	if got := localZoneName(); got != "America/Denver" {
		t.Errorf("/etc/localtime link → %q", got)
	}
	zoneinfoPath = filepath.Join(dir, "missing")
	oldTZFile := timezonePath
	t.Cleanup(func() { timezonePath = oldTZFile })
	timezonePath = filepath.Join(dir, "timezone")
	if err := os.WriteFile(timezonePath, []byte("Europe/Berlin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := localZoneName(); got != "Europe/Berlin" {
		t.Errorf("/etc/timezone → %q", got)
	}
	timezonePath = filepath.Join(dir, "missing-too")
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })
	time.Local = time.FixedZone("Local", 0)
	if got := localZoneName(); got != "UTC" {
		t.Errorf("no zone found → %q, want UTC", got)
	}
	time.Local = time.UTC
	if got := localZoneName(); got != "UTC" {
		t.Errorf("time.Local=UTC → %q", got)
	}
}

func TestNextFireOverADay(t *testing.T) {
	ev := &pb.TimedEvent{Enabled: true, NextFireAt: at(62*time.Hour + 10*time.Minute)}
	if got := nextFire(ev); got != "in 2d14h" {
		t.Errorf("nextFire 62h ahead = %q", got)
	}
}
