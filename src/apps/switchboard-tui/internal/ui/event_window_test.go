package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// 24:00 ends a window at midnight; a non-IANA zone is refused in the form,
// checked against the embedded zone database the daemon also uses.
func TestWindowFromFormMidnightAndZones(t *testing.T) {
	w, err := windowFromForm(&eventFormVals{activeFrom: "18:00", activeUntil: " 24:00 ", timezone: "UTC"})
	if err != nil || w.GetStart() != "18:00" || w.GetEnd() != "00:00" {
		t.Errorf("18:00–24:00 = %+v, %v; want 18:00–00:00", w, err)
	}
	if _, err := windowFromForm(&eventFormVals{activeFrom: "24:00", activeUntil: "08:00", timezone: "UTC"}); err == nil || !strings.Contains(err.Error(), `active from "24:00" must be HH:MM`) {
		t.Errorf("24:00 as a start: %v", err)
	}
	if _, err := windowFromForm(&eventFormVals{activeFrom: "08:00", activeUntil: "17:00", timezone: "PST"}); err == nil || !strings.Contains(err.Error(), `unknown timezone "PST"; use an IANA name`) {
		t.Errorf("PST: %v", err)
	}
}

// The Upcoming row names a windowed event's window, so a far-off next firing
// reads as the window and not as a stuck event.
func TestUpcomingLineShowsTheWindow(t *testing.T) {
	ev := &pb.TimedEvent{Id: "ev-1", Name: "poll", Action: "review-pr", Enabled: true, NextFireAt: at(62 * time.Hour),
		Window: &pb.ActiveWindow{Start: "08:00", End: "17:00", Weekdays: []uint32{1, 2, 3, 4, 5}, Timezone: "America/Los_Angeles"}}
	rows := jobsUpcoming([]jobsOrch{jo("h", "bot", []*pb.TimedEvent{ev}, &pb.ListRunsResponse{})})
	if len(rows) != 1 || !strings.Contains(upcomingLine(rows[0]), " · 08:00–17:00 Mon–Fri") {
		t.Errorf("Upcoming row without the window: %q", upcomingLine(rows[0]))
	}
	// The skip note comes before the window, so a narrow row cuts the window.
	rows[0].willSkip = true
	if line := upcomingLine(rows[0]); strings.Index(line, "next firing skipped") > strings.Index(line, "08:00–17:00") {
		t.Errorf("skip note after the window: %q", line)
	}
	rows[0].willSkip = false
	ev.Window = nil
	if line := upcomingLine(jobsUpcoming([]jobsOrch{jo("h", "bot", []*pb.TimedEvent{ev}, &pb.ListRunsResponse{})})[0]); strings.Contains(line, "·") {
		t.Errorf("an event without a window gets no marker: %q", line)
	}
}

// The Upcoming row's window leaves out a local zone; a midnight end reads
// 24:00 in labels and in the form, as the form accepts it.
func TestCompactWindowLabelAndMidnightEnd(t *testing.T) {
	w := &pb.ActiveWindow{Start: "08:00", End: "17:00", Weekdays: []uint32{1, 2, 3, 4, 5}, Timezone: "America/Los_Angeles"}
	if got := compactWindowLabel(w, "America/Los_Angeles"); got != "08:00–17:00 Mon–Fri" {
		t.Errorf("local zone = %q", got)
	}
	if got := compactWindowLabel(w, "Europe/Berlin"); got != "08:00–17:00 Mon–Fri America/Los_Angeles" {
		t.Errorf("other zone = %q", got)
	}
	if compactWindowLabel(nil, "UTC") != "" {
		t.Error("no window, no label")
	}
	night, err := windowFromForm(&eventFormVals{activeFrom: "18:00", activeUntil: "24:00", timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if got := windowLabel(night); got != "18:00–24:00 UTC" {
		t.Errorf("midnight end label = %q", got)
	}
	v := &eventFormVals{}
	windowToForm(v, night)
	if v.activeUntil != "24:00" {
		t.Errorf("midnight end in the form = %q, want 24:00", v.activeUntil)
	}
	if back, err := windowFromForm(v); err != nil || back.GetEnd() != "00:00" {
		t.Errorf("re-saving = %+v, %v; want 00:00 stored", back, err)
	}
	// A window starting at midnight keeps 00:00 (00:00–24:00 would be all day).
	early := &pb.ActiveWindow{Start: "22:00", End: "00:00", Timezone: "UTC"}
	if got := windowLabel(early); got != "22:00–24:00 UTC" {
		t.Errorf("22:00–00:00 label = %q", got)
	}
	if got := windowEnd(&pb.ActiveWindow{Start: "00:00", End: "00:00"}); got != "00:00" {
		t.Errorf("00:00 start keeps 00:00, got %q", got)
	}
}

// Review run-502: stepping through the Days field of an unedited event whose
// window includes Sunday leaves the form clean, so esc closes it.
func TestUneditedSundayEventClosesOnEsc(t *testing.T) {
	ev := &pb.TimedEvent{Id: "tev-1", Name: "poll", Action: "review-pr", IntervalSeconds: 900, Enabled: true, Arguments: map[string]string{"number": "1", "repo": "r"},
		Window: &pb.ActiveWindow{Start: "08:00", End: "17:00", Weekdays: []uint32{0, 1}, Timezone: "UTC"}}
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{{Orchestrator: &pb.Orchestrator{Id: "o-a", Name: "alpha-bot", HostId: "test-host", Actions: []*pb.Action{reviewAction()}, TimedEvents: []*pb.TimedEvent{ev}}}}}
	m := openJobs(sized(New(a, "/work")))
	m.jobs.tab = jobsEvents
	m, _ = update(m, press("e"))
	v := m.jobs.formVals.(*eventFormVals)
	for i := 0; i < 8; i++ {
		m.jobs.form.NextField()
	}
	if len(v.days) != 2 || v.days[0] != 1 {
		t.Fatalf("the walk should pass the Days field, which huh rewrites Monday first: %v", v.days)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.jobs.form != nil || m.unsaved.open {
		t.Errorf("an unedited form should close on esc: form %v dialog %v", m.jobs.form != nil, m.unsaved.open)
	}
}
