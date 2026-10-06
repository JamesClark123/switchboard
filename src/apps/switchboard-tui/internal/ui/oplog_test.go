package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// drainOp feeds a streaming operation's messages (opStartedMsg → progress frames
// → opResultMsg) into the model until the result has been applied.
func drainOp(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	for i := 0; i < 10000 && msg != nil; i++ {
		var cmd tea.Cmd
		m, cmd = update(m, msg)
		if _, done := msg.(opResultMsg); done {
			return m
		}
		msg = runCmd(cmd)
	}
	t.Fatal("streamed operation never delivered its result")
	return m
}

// A refresh's sbx output is kept per sandbox, and `l` opens it with the verb and
// outcome; esc closes.
func TestOpLogRecordsRefreshOutputAndOverlayShowsIt(t *testing.T) {
	d := &fakeDaemon{streamLines: []string{"pulling image", "Created sandbox 'sb-1'"}}
	m := listModel(t, d)
	out, _ := update(m, press("F"))
	out, cmd := update(out, press("y"))
	out = drainOp(t, out, runCmd(cmd))

	l, ok := out.opLogs["sb-1"]
	if !ok {
		t.Fatal("no sbx log recorded for sb-1")
	}
	if len(l.lines) != 2 || l.verb != "refresh" || !l.done || l.err != nil {
		t.Errorf("log = %+v, want two lines of a finished refresh", l)
	}
	if out.busy["sb-1"] != "" {
		t.Error("row spinner should clear once the stream ends")
	}

	out, _ = update(out, press("l"))
	if out.screen != screenOpLog {
		t.Fatalf("screen = %v, want screenOpLog", out.screen)
	}
	v := out.View()
	for _, want := range []string{"sbx output", "refresh", "pulling image", "Created sandbox", "done"} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay should show %q; got:\n%s", want, v)
		}
	}
	out, _ = update(out, press("esc"))
	if out.screen != screenList {
		t.Errorf("esc should close the overlay; screen = %v", out.screen)
	}
}

// A failed attach keeps its output, the status line points at it, and the
// overlay shows both the lines and the error.
func TestFailedOpStatusPointsAtTheLog(t *testing.T) {
	d := &fakeDaemon{addKitErr: errBoom{}, streamLines: []string{"npm ERR! code 1"}}
	m := kitModel(t, d, ruffKit())
	out, cmd := update(m, press("A"))
	out, _ = update(out, runCmd(cmd))
	out, _ = update(out, press("enter"))
	out, cmd = update(out, press("y"))
	out = drainOp(t, out, runCmd(cmd))

	if out.err == nil || !strings.Contains(out.status, "press l") {
		t.Errorf("status = %q, err = %v; want the failure to point at the sbx output", out.status, out.err)
	}
	if out.busy["sb-1"] != "" {
		t.Error("row spinner should clear on failure")
	}
	out, _ = update(out, press("l"))
	if out.screen != screenOpLog {
		t.Fatalf("screen = %v, want screenOpLog", out.screen)
	}
	v := out.View()
	for _, want := range []string{"kit add", "failed", "npm ERR! code 1", errBoom{}.Error()} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay should show %q; got:\n%s", want, v)
		}
	}
}

func TestOpLogKeyWithoutOutputExplains(t *testing.T) {
	m := listModel(t, &fakeDaemon{})
	out, _ := update(m, press("l"))
	if out.screen != screenList || !strings.Contains(out.status, "no sbx output") {
		t.Errorf("screen = %v, status = %q; want to stay on the list and explain", out.screen, out.status)
	}
}

func pendingLaunch(m Model) Model {
	m.launchSeq = 1
	m.launching["pending-1"] = &launchInFlight{
		tempID: "pending-1", seq: 1, ch: make(chan tea.Msg, 8), progress: "starting…",
		sb: &pb.Sandbox{Id: "pending-1", State: pb.SandboxState_SANDBOX_STATE_CREATING},
	}
	m.beginOpLog("pending-1", "new sandbox", "launch")
	return m
}

// Launch output is recorded under the placeholder id and follows the sandbox to
// its real id on success; after a failure the vanished row's log is still the
// one `l` opens.
func TestLaunchOpLogLifecycle(t *testing.T) {
	m := pendingLaunch(sized(New(&fakeDaemon{}, "/work")))
	m, _ = update(m, launchProgressMsg{id: "pending-1", update: client.LaunchUpdate{LogLine: "launching sandbox"}})
	m, _ = update(m, launchProgressMsg{id: "pending-1", update: client.LaunchUpdate{Copy: &pb.LaunchProgress_CopyProgress{BytesCopied: 1, BytesTotal: 4}}})
	if l := m.opLogs["pending-1"]; len(l.lines) != 1 || !strings.Contains(l.latest, "copying") {
		t.Fatalf("log = %+v; want one line and a copy badge", l)
	}

	t.Run("failure", func(t *testing.T) {
		mf, _ := update(m, launchResultMsg{id: "pending-1", err: errBoom{}})
		if !strings.Contains(mf.status, "press l") {
			t.Errorf("status = %q, want a pointer to the sbx output", mf.status)
		}
		mf, _ = update(mf, press("l"))
		if mf.screen != screenOpLog || mf.opLogView.id != "pending-1" {
			t.Fatalf("screen = %v, log = %q; want the failed launch's log open", mf.screen, mf.opLogView.id)
		}
		if v := mf.View(); !strings.Contains(v, "launching sandbox") || !strings.Contains(v, "failed") {
			t.Errorf("overlay should show the captured line and the failure; got:\n%s", v)
		}
	})

	t.Run("success rekeys", func(t *testing.T) {
		m := pendingLaunch(sized(New(&fakeDaemon{}, "/work")))
		m, _ = update(m, launchProgressMsg{id: "pending-1", update: client.LaunchUpdate{LogLine: "Created sandbox"}})
		ms, _ := update(m, launchResultMsg{id: "pending-1", sb: &pb.Sandbox{Id: "real-1", DisplayName: "demo"}})
		if _, stale := ms.opLogs["pending-1"]; stale {
			t.Error("placeholder key should be gone after the launch resolves")
		}
		l, ok := ms.opLogs["real-1"]
		if !ok || !l.done || l.err != nil || len(l.lines) != 1 {
			t.Fatalf("log under the real id = %+v; want the finished launch log", l)
		}
		if ms.lastOpLogID != "real-1" {
			t.Errorf("lastOpLogID = %q, want real-1", ms.lastOpLogID)
		}
	})

	t.Run("blocked drops the log", func(t *testing.T) {
		m := pendingLaunch(sized(New(&fakeDaemon{}, "/work")))
		mb, _ := update(m, launchResultMsg{id: "pending-1", blocked: &pb.ResourceReport{Warnings: []string{"low disk"}}})
		if _, ok := mb.opLogs["pending-1"]; ok {
			t.Error("a resource-gated launch never ran sbx; its log should be dropped")
		}
	})
}

// The busy row shows the live tail, and the overlay scrolls through long output
// (tail by default, g/G jump, ↑/↓ and pgup/pgdn move).
func TestOpLogScrollingAndBusyRowBadge(t *testing.T) {
	m := listModel(t, &fakeDaemon{})
	m, _ = update(m, opStartedMsg{id: "sb-1", verb: "kit add", ch: make(chan tea.Msg, 1)})
	m = m.startBusy("sb-1", "kit add")
	for i := 1; i <= 60; i++ {
		m, _ = update(m, opProgressMsg{id: "sb-1", update: client.LaunchUpdate{LogLine: fmt.Sprintf("line %03d", i)}})
	}
	if v := m.View(); !strings.Contains(v, "line 060") {
		t.Errorf("busy row should show the latest sbx line; got:\n%s", v)
	}

	m, _ = update(m, press("l"))
	start, end, total, avail := m.opLogWindow()
	if total != 60 || end != 60 || start != 60-avail {
		t.Fatalf("window = [%d,%d) of %d (avail %d); want the tail", start, end, total, avail)
	}
	if v := m.View(); !strings.Contains(v, "line 060") || strings.Contains(v, "line 001") || !strings.Contains(v, "running") {
		t.Errorf("overlay should show the tail of a running op; got:\n%s", v)
	}
	m, _ = update(m, press("g"))
	if s, _, _, _ := m.opLogWindow(); s != 0 {
		t.Errorf("g should jump to the top; start = %d", s)
	}
	if v := m.View(); !strings.Contains(v, "line 001") {
		t.Errorf("top of the log should show the first line; got:\n%s", v)
	}
	m, _ = update(m, press("G"))
	m, _ = update(m, press("up"))
	m, _ = update(m, press("up"))
	if _, e, _, _ := m.opLogWindow(); e != 58 {
		t.Errorf("two ups from the tail should end the window at 58; got %d", e)
	}
	m, _ = update(m, press("pgdown"))
	if _, e, _, _ := m.opLogWindow(); e != 60 {
		t.Errorf("pgdown should return to the tail; got %d", e)
	}
	m, _ = update(m, press("l"))
	if m.screen != screenList {
		t.Errorf("l should toggle the overlay closed; screen = %v", m.screen)
	}
}

func TestOpLogIsCapped(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.beginOpLog("sb-1", "demo", "launch")
	for i := 0; i < opLogMaxLines+5; i++ {
		m.appendOpLog("sb-1", fmt.Sprintf("line %d", i))
	}
	l := m.opLogs["sb-1"]
	if len(l.lines) != opLogMaxLines || !l.dropped || l.lines[0] != "line 5" {
		t.Errorf("len = %d, dropped = %v, first = %q; want the oldest five dropped", len(l.lines), l.dropped, l.lines[0])
	}
	m.opLogView = opLogState{id: "sb-1"}
	m.screen = screenOpLog
	m, _ = update(m, press("g"))
	if v := m.View(); !strings.Contains(v, "earlier output dropped") {
		t.Errorf("top of a capped log should say output was dropped; got:\n%s", v)
	}
}
