package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SC-008 (FR-533, research R10): a `v` failure or refusal arriving while an
// in-place terminal is open or opening, or while a Replace dialog for one is
// open, is held and shown in full, in the error style, once the terminal
// closes or the dialog is declined; joined to an attach or terminal error or a
// daemon drop; never held again once released.

const vRefusal = "WebStorm can't open folders on remote hosts — switch editors on the settings screen (,)"

// clock steps nowFunc.
func clock(t *testing.T) func(d time.Duration) {
	t.Helper()
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })
	return func(d time.Duration) { now = now.Add(d) }
}

func vFail(m Model, text string) Model {
	m, _ = update(m, editorOutcomeMsg{sandboxID: "sb1", text: text, failed: true})
	return m
}

func assertRed(t *testing.T, m Model, want string) {
	t.Helper()
	if m.status != want {
		t.Errorf("status = %q, want %q", m.status, want)
	}
	if !m.statusIsErr() {
		t.Errorf("status %q should be in the error style", m.status)
	}
}

// runTermAttach runs an attach's command. With a message held it's batched with
// that message's limit timer, attach first; the timer isn't run.
func runTermAttach(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch[0]()
	}
	return msg
}

// openTerm presses t and opens the terminal.
func openTerm(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(m, press("t"))
	m, _ = update(m, runTermAttach(cmd))
	if m.screen != screenTerminal {
		t.Fatalf("terminal not open: %v %q", m.screen, m.status)
	}
	return m
}

func ctrlQ(m Model) Model {
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	return m
}

func TestHoldWhileTerminalOpen(t *testing.T) {
	clock(t)
	m := openTerm(t, runningSbx(&fakeDaemon{}))
	m = vFail(m, vRefusal)
	if m.vHold.text != vRefusal || m.status != vRefusal {
		t.Fatalf("held %q shown %q", m.vHold.text, m.status)
	}
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
	if m.vHold.text != "" || !m.vFail.at.IsZero() {
		t.Error("released: nothing held, never held again")
	}
	// The next plain status isn't red.
	m, _ = update(m, statusMsg("stopped sb1"))
	if m.statusIsErr() {
		t.Error("a later plain message is not red")
	}
}

func TestHoldWhileOpening(t *testing.T) {
	clock(t)
	m := runningSbx(&fakeDaemon{})
	m, cmd := update(m, press("t"))
	m = vFail(m, vRefusal)
	if m.vHold.text != vRefusal || m.status != "opening terminal…" {
		t.Fatalf("held %q shown %q", m.vHold.text, m.status)
	}
	m, _ = update(m, runTermAttach(cmd))
	if m.screen != screenTerminal || m.vHold.text != vRefusal {
		t.Fatal("the terminal opens with the message still held")
	}
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
}

// A first attach superseded by a second: only the second's message ends the hold.
func TestHoldSupersededAttach(t *testing.T) {
	clock(t)
	m := runningSbx(&fakeDaemon{})
	m, cmd1 := update(m, press("t"))
	m, cmd2 := update(m, press("t"))
	m = vFail(m, vRefusal)
	m, _ = update(m, runTermAttach(cmd1))
	if m.vHold.seq == 0 || m.screen == screenTerminal || m.status != "opening terminal…" {
		t.Fatalf("the superseded attach must not end the hold: seq %d screen %v status %q", m.vHold.seq, m.screen, m.status)
	}
	m, _ = update(m, runTermAttach(cmd2))
	if m.screen != screenTerminal || m.vHold.seq != 0 {
		t.Fatal("the second attach opens")
	}
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
}

// The Replace dialog: declined, and accepted more than 3 s after the refusal.
func TestHoldReplaceDialog(t *testing.T) {
	step := clock(t)
	for _, accept := range []bool{false, true} {
		m, d := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
		m = vFail(m, vRefusal) // showing, recent
		m, _ = update(m, press("t"))
		if m.screen != screenConfirm || !m.confirm.holdsV || m.vHold.text != vRefusal || !m.vHoldDialog() {
			t.Fatalf("dialog: screen %v holdsV %v held %q", m.screen, m.confirm.holdsV, m.vHold.text)
		}
		// A failure arriving while the dialog is open replaces the held one.
		m = vFail(m, "later failure")
		if m.vHold.text != "later failure" || m.status == "later failure" {
			t.Fatalf("held %q status %q", m.vHold.text, m.status)
		}
		step(5 * time.Second)
		if !accept {
			m, _ = update(m, press("n"))
			assertRed(t, m, "later failure")
			continue
		}
		m, cmd := update(m, press("y"))
		if m.vHold.seq != m.termSeq+1 || m.vHold.text != "later failure" {
			t.Fatalf("accept passes the hold to the attach: seq %d termSeq %d", m.vHold.seq, m.termSeq)
		}
		m, cmd = update(m, runTermAttach(cmd)) // replaceOrphanMsg → attach
		if m.vHold.seq != m.termSeq || m.status != replacingText {
			t.Fatalf("attach: seq %d termSeq %d status %q", m.vHold.seq, m.termSeq, m.status)
		}
		m, _ = update(m, runTermAttach(cmd))
		if m.screen != screenTerminal || d.replacedID != "p1" {
			t.Fatal("the replacing terminal opens")
		}
		m = ctrlQ(m)
		assertRed(t, m, "later failure")
	}
}

// An old disconnected or terminal error still showing when t opens the Replace
// dialog isn't joined to a failure held behind it.
func TestHoldDialogDoesNotJoinOldError(t *testing.T) {
	clock(t)
	for _, old := range []string{"beta disconnected — orchestrators closed", "terminal: old reason"} {
		m, _ := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
		m.status, m.termFail = old, old
		m, _ = update(m, press("t"))
		m = vFail(m, vRefusal)
		m, _ = update(m, press("n"))
		assertRed(t, m, vRefusal)
	}
}

// A Replace dialog opened from the orchestrator screens and dropped unanswered
// by a daemon drop: the held failure joins the drop's message; a later failure
// shows alone; with nothing held the message stays as today; a failure just
// after the drop replaces it.
func TestHoldDialogDroppedByDaemonDrop(t *testing.T) {
	clock(t)
	a := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-a", "alpha-bot")}}
	b := &fakeDaemon{orchestrators: []*pb.OrchestratorSummary{orchSummary("o-b", "beta-bot")}}
	sb := detachedSandbox("w1", pb.AgentStatus_AGENT_STATUS_IDLE, false)
	open := func() (Model, *client.Manager) {
		t.Helper()
		m, mgr, _ := twoFakeHosts(t, a, b)
		mm, cmd := m.enterDaemonOrchs(daemonRow{id: "hostb", name: "beta"})
		m = feed(mm.(Model), cmd)
		mm, _ = m.confirmReplaceOrphan(sb, "hostb", b, false, screenDaemonOrchs, sb.GetAgent().GetAgentCheck())
		m = mm.(Model)
		if m.screen != screenConfirm || !m.vHoldDialog() {
			t.Fatalf("dialog: %v", m.screen)
		}
		return m, mgr
	}
	m, mgr := open()
	m = vFail(m, vRefusal)
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if m.screen != screenDaemons {
		t.Fatalf("screen %v", m.screen)
	}
	assertRed(t, m, "beta disconnected — orchestrators closed; "+vRefusal)
	m = vFail(m, "later failure")
	assertRed(t, m, "later failure")
	// Nothing held: the message stays as today, not red.
	m, mgr = open()
	mgr.Disconnect("hostb")
	m = feed2(m, m.hostsCmd())
	if m.status != "beta disconnected — orchestrators closed" || m.statusIsErr() {
		t.Errorf("nothing held: %q red %v", m.status, m.statusIsErr())
	}
	m = vFail(m, vRefusal)
	assertRed(t, m, vRefusal)
}

// A refusal then t is held; one older than 3 s isn't.
func TestHoldRecentRefusalOnly(t *testing.T) {
	step := clock(t)
	m := runningSbx(&fakeDaemon{})
	m = vFail(m, vRefusal)
	m = openTerm(t, m)
	m = ctrlQ(m)
	assertRed(t, m, vRefusal)
	m = vFail(m, vRefusal)
	step(4 * time.Second)
	m = openTerm(t, m)
	m = ctrlQ(m)
	if !strings.HasPrefix(m.status, "detached from") || m.statusIsErr() {
		t.Errorf("an old refusal is not held: %q", m.status)
	}
	// A released message can't be held again within its 3 seconds.
	m = vFail(m, vRefusal)
	m = openTerm(t, m)
	m = ctrlQ(m) // released
	m = openTerm(t, m)
	m = ctrlQ(m)
	if !strings.HasPrefix(m.status, "detached from") {
		t.Errorf("a released message is not held again: %q", m.status)
	}
}

// `terminal: <reason>; <refusal>` joined, red, error first; a second t within
// 3 s shows only its own error; moved on and external already attached join
// the same way; an accepted Replace whose attach fails too.
func TestHoldJoinsTerminalErrors(t *testing.T) {
	clock(t)
	d := &fakeDaemon{attachErr: errors.New("daemon unreachable")}
	m := runningSbx(d)
	m = vFail(m, vRefusal)
	m, cmd := update(m, press("t"))
	m, _ = update(m, runTermAttach(cmd))
	assertRed(t, m, "terminal: daemon unreachable; "+vRefusal)
	m, cmd = update(m, press("t"))
	m, _ = update(m, runTermAttach(cmd))
	if m.status != "terminal: daemon unreachable" || m.statusIsErr() {
		t.Errorf("a retried t shows only its own error: %q red %v", m.status, m.statusIsErr())
	}
	// Moved on: t, then the settings screen before the attach lands.
	d.attachErr = nil
	m = runningSbx(d)
	m, cmd = update(m, press("t"))
	m = vFail(m, vRefusal)
	m, _ = update(m, press(","))
	m, _ = update(m, runTermAttach(cmd))
	assertRed(t, m, "terminal for demo not opened — you moved on; press t again; "+vRefusal)
	// External already attached.
	d.attachErr = client.ErrExternalAlreadyOpen
	m = runningSbx(d)
	m = vFail(m, vRefusal)
	m, cmd = update(m, press("t"))
	m, _ = update(m, runTermAttach(cmd))
	assertRed(t, m, "an external terminal is already attached to demo; "+vRefusal)
	// An accepted Replace whose attach fails.
	d.attachErr = nil
	rm, rd := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
	rd.replaceErr = errors.New("replace failed")
	rm = vFail(rm, vRefusal)
	rm, _ = update(rm, press("t"))
	rm, cmd = update(rm, press("y"))
	rm, cmd = update(rm, runTermAttach(cmd))
	rm, _ = update(rm, runTermAttach(cmd))
	assertRed(t, rm, "terminal: replace failed; "+vRefusal)
	// A refused attach that opens the dialog from the daemon's refusal.
	d.attachErr = status.Error(codes.FailedPrecondition, detachedRefusal)
	m = runningSbx(d)
	m = vFail(m, vRefusal)
	m, cmd = update(m, press("t"))
	m, _ = update(m, runTermAttach(cmd))
	if m.screen != screenConfirm || !m.vHoldDialog() || m.vHold.text != vRefusal {
		t.Fatalf("the refusal's dialog holds: %v %q", m.screen, m.vHold.text)
	}
	m, _ = update(m, press("n"))
	assertRed(t, m, vRefusal)
}

// An open terminal whose session dies joins the held failure; `detached from`
// is replaced; a v failure arriving once an error shows replaces it.
func TestHoldSessionDiesAndReplace(t *testing.T) {
	clock(t)
	m := openTerm(t, runningSbx(&fakeDaemon{}))
	m = vFail(m, vRefusal)
	m, _ = update(m, termClosedMsg{err: errors.New("session died"), name: "demo", seq: m.term.seq})
	assertRed(t, m, "terminal: session died; "+vRefusal)
	// A failure arriving after the error is showing replaces it.
	m = vFail(m, "another")
	assertRed(t, m, "another")
	// detached from is replaced.
	m = openTerm(t, m)
	m = vFail(m, vRefusal)
	m, _ = update(m, termClosedMsg{name: "demo", seq: m.term.seq})
	assertRed(t, m, vRefusal)
}

// An attach that never returns: a failure held behind it shows at the limit,
// one arriving after the limit shows at once, and both stay held in case the
// attach does open after all, which clears the status.
func TestHoldAttachLimit(t *testing.T) {
	step := clock(t)
	m := runningSbx(&fakeDaemon{})
	m, attach := update(m, press("t"))
	// The timer is for what's left of the limit, here 1 ms, not all of it.
	step(vHoldAttachLimit - time.Millisecond)
	m, limit := update(m, editorOutcomeMsg{sandboxID: "sb1", text: vRefusal, failed: true})
	if m.status != "opening terminal…" || m.vHold.text != vRefusal || limit == nil {
		t.Fatalf("within the limit: shown %q held %q timer %v", m.status, m.vHold.text, limit != nil)
	}
	m, _ = update(m, limit())
	assertRed(t, m, vRefusal)
	if m.vHold.text != vRefusal || m.vHold.seq == 0 {
		t.Fatalf("shown at the limit, still held: %+v", m.vHold)
	}
	// A later failure is past the limit: shown at once, no timer.
	step(time.Millisecond)
	m, limit = update(m, editorExitMsg{sandboxID: "sb1", text: "later failure"})
	assertRed(t, m, "later failure")
	if m.vHold.text != "later failure" || limit != nil {
		t.Fatalf("past the limit: held %q timer %v", m.vHold.text, limit != nil)
	}
	// The attach opens after all: the status clears, and the failure comes back.
	m, _ = update(m, runTermAttach(attach))
	if m.screen != screenTerminal || m.status != "" {
		t.Fatalf("the late attach opens: screen %v status %q", m.screen, m.status)
	}
	m = ctrlQ(m)
	assertRed(t, m, "later failure")
	if m.vHold.text != "" {
		t.Errorf("released, nothing held: %q", m.vHold.text)
	}
}

// A refusal already showing when t is pressed is held, and comes back at the
// limit: the attach's command carries the timer.
func TestHoldAttachLimitCoversARecentRefusal(t *testing.T) {
	clock(t)
	m := vFail(runningSbx(&fakeDaemon{}), vRefusal)
	m, cmd := update(m, press("t"))
	if m.status != "opening terminal…" || m.vHold.text != vRefusal {
		t.Fatalf("shown %q held %q", m.status, m.vHold.text)
	}
	if batch, ok := cmd().(tea.BatchMsg); !ok || len(batch) != 2 {
		t.Fatalf("the attach's command should carry the timer: %#v", batch)
	}
	m, _ = update(m, vHoldLimitMsg{seq: m.vHold.seq})
	assertRed(t, m, vRefusal)
}

// A timer only speaks for its own attach: a newer attach gets the whole limit,
// and one that has ended leaves the status alone.
func TestHoldAttachLimitOnlyForThePendingAttach(t *testing.T) {
	step := clock(t)
	m := runningSbx(&fakeDaemon{})
	m, _ = update(m, press("t"))
	first := m.vHold.seq
	m = vFail(m, vRefusal)
	step(vHoldAttachLimit)
	m, second := update(m, press("t"))
	m, _ = update(m, vHoldLimitMsg{seq: first})
	if m.status != "opening terminal…" || m.vHold.seq == first {
		t.Fatalf("a superseded attach's limit showed %q (seq %d)", m.status, m.vHold.seq)
	}
	if m.vHoldAttachOverdue() {
		t.Error("the newer attach starts its own limit")
	}
	m, _ = update(m, runTermAttach(second))
	if m.screen != screenTerminal {
		t.Fatalf("the newer attach opens: %v", m.screen)
	}
	seq := m.term.seq
	m, _ = update(m, vHoldLimitMsg{seq: seq})
	if m.status != "" {
		t.Errorf("an attach that has ended has no limit: status %q", m.status)
	}
}

// The limit doesn't lose what arrived while the message was held: an error, or
// a message a release would join, stays in front of the held message, whose
// text alone is what comes back if the attach opens after all.
func TestHoldAttachLimitKeepsANewerError(t *testing.T) {
	clock(t)
	stalled := func() (Model, tea.Cmd) {
		m, attach := update(runningSbx(&fakeDaemon{}), press("t"))
		return vFail(m, vRefusal), attach
	}
	limit := func(m Model) Model {
		m, _ = update(m, vHoldLimitMsg{seq: m.vHold.seq})
		return m
	}
	destroy := "error: destroy sb2: permission denied"

	m, attach := stalled()
	m, _ = update(m, errMsg{err: errors.New("destroy sb2: permission denied")})
	m = limit(m)
	assertRed(t, m, destroy+"; "+vRefusal)
	if m.vHold.text != vRefusal {
		t.Errorf("held %q, want the failure alone", m.vHold.text)
	}
	// A second timer for this attach (one per failure held) changes nothing.
	m = limit(m)
	assertRed(t, m, destroy+"; "+vRefusal)
	m, _ = update(m, runTermAttach(attach))
	assertRed(t, ctrlQ(m), vRefusal)

	// An error with no "error" prefix, which only m.err marks.
	m, _ = stalled()
	m.err, m.status = errors.New("no such image"), "launch failed: no such image"
	assertRed(t, limit(m), "launch failed: no such image; "+vRefusal)

	// A daemon drop's message isn't in the error style, but a release joins it.
	m, _ = stalled()
	m.status = "beta disconnected — orchestrators closed"
	m.termFail = m.status
	assertRed(t, limit(m), "beta disconnected — orchestrators closed; "+vRefusal)

	// A status that isn't an error is replaced.
	m, _ = stalled()
	m.status = "copied"
	assertRed(t, limit(m), vRefusal)

	// A stale m.err styles the attach's own text, which is still replaced.
	m = runningSbx(&fakeDaemon{})
	m, _ = update(m, errMsg{err: errors.New("old")})
	m, _ = update(m, press("t"))
	m = vFail(m, vRefusal)
	if m.status != openingText || !m.statusIsErr() {
		t.Fatalf("the attach's text in the error style: %q red %v", m.status, m.statusIsErr())
	}
	assertRed(t, limit(m), vRefusal)
	// ... as is the text of a Replace attach, ...
	m = runningSbx(&fakeDaemon{})
	m, _ = update(m, errMsg{err: errors.New("old")})
	m, _ = update(m, press("t"))
	m = vFail(m, vRefusal)
	m.status = replacingText
	assertRed(t, limit(m), vRefusal)
	// ... and the held message itself, which a dialog leaves showing.
	m, _ = stalled()
	m.status = m.vHold.text
	assertRed(t, limit(m), vRefusal)
}

// An error about the input leaves with the input after the limit has joined the
// held message to it, and the held message stays on screen.
func TestHoldAttachLimitJoinsAnInputError(t *testing.T) {
	clock(t)
	noKeyGrace(t)
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	reject := func() Model {
		m, _ := settingsModel(t)
		m, _ = openCustom(t, m)
		m.vHold = vHoldState{text: vRefusal, seq: 7, since: nowFunc()}
		m.settingsView.input.SetValue("foo 'bar")
		m, _ = update(m, enter)
		return m
	}
	rejection := "the command has an unbalanced quote"
	// The rejection arrives first, then the limit.
	m := reject()
	if m.status != rejection {
		t.Fatalf("rejection: %q", m.status)
	}
	m, _ = update(m, vHoldLimitMsg{seq: 7})
	assertRed(t, m, rejection+"; "+vRefusal)
	m, _ = update(m, esc)
	m, _ = update(m, press("d"))
	if m.settingsView.mode != settingsList {
		t.Fatalf("mode %v", m.settingsView.mode)
	}
	assertRed(t, m, vRefusal)
	// The limit first, then the rejection, which replaces the shown message.
	m, _ = settingsModel(t)
	m, _ = openCustom(t, m)
	m.vHold = vHoldState{text: vRefusal, seq: 7, since: nowFunc()}
	m, _ = update(m, vHoldLimitMsg{seq: 7})
	m.settingsView.input.SetValue("foo 'bar")
	m, _ = update(m, enter)
	if m.status != rejection {
		t.Fatalf("rejection: %q", m.status)
	}
	m, _ = update(m, esc)
	m, _ = update(m, press("d"))
	assertRed(t, m, vRefusal)
	// A completion ended the hold: the status clears.
	m, _ = settingsModel(t)
	m, _ = openCustom(t, m)
	m.vHold = vHoldState{text: vRefusal, seq: 7, since: nowFunc()}
	m, _ = update(m, vHoldLimitMsg{seq: 7})
	m, _ = update(m, editorOutcomeMsg{sandboxID: "sb1", text: "opened demo in VS Code"})
	if m.vHold.text != "" || !m.vHold.shown.IsZero() {
		t.Errorf("a completion ends the hold: %+v", m.vHold)
	}
	m, _ = update(m, enter)
	m, _ = update(m, esc)
	if m.status != "" || m.statusIsErr() {
		t.Errorf("after a completion: %q red %v", m.status, m.statusIsErr())
	}
	// Nothing held: the status clears, as before.
	m, _ = settingsModel(t)
	m, _ = openCustom(t, m)
	m, _ = update(m, enter)
	m, _ = update(m, esc)
	if m.status != "" {
		t.Errorf("nothing held: %q", m.status)
	}
}

// A message shown at the limit, or past it, was seen: a retried t keeps it only
// if it was shown in the last 3 s, as it does a refusal showing with no attach.
func TestHoldShownMessageAcrossARetry(t *testing.T) {
	step := clock(t)
	shownAtLimit := func() Model {
		m, _ := update(runningSbx(&fakeDaemon{}), press("t"))
		m = vFail(m, vRefusal)
		step(vHoldAttachLimit)
		m, _ = update(m, vHoldLimitMsg{seq: m.vHold.seq})
		assertRed(t, m, vRefusal)
		return m
	}
	shownPastLimit := func() Model {
		m, _ := update(runningSbx(&fakeDaemon{}), press("t"))
		step(vHoldAttachLimit)
		m = vFail(m, vRefusal)
		assertRed(t, m, vRefusal)
		return m
	}
	for name, shown := range map[string]func() Model{"at the limit": shownAtLimit, "past the limit": shownPastLimit} {
		// Long since shown: dropped. The retry opens and closes clean.
		m := shown()
		step(vHoldRecent)
		m, retry := update(m, press("t"))
		if m.status != openingText || m.vHold.text != "" {
			t.Errorf("%s: an old message is dropped: status %q held %q", name, m.status, m.vHold.text)
		}
		m, _ = update(m, runTermAttach(retry))
		m = ctrlQ(m)
		if m.status != "detached from demo" || m.statusIsErr() {
			t.Errorf("%s: after the retry closes: %q red %v", name, m.status, m.statusIsErr())
		}
		// Long since shown: a retry that stalls too shows nothing at its limit.
		m = shown()
		step(time.Hour)
		m, _ = update(m, press("t"))
		step(vHoldAttachLimit)
		m, _ = update(m, vHoldLimitMsg{seq: m.vHold.seq})
		if m.status != openingText {
			t.Errorf("%s: a stalled retry's limit showed %q", name, m.status)
		}
		// Shown just now: kept, out of sight again until the retry's own limit.
		m = shown()
		step(vHoldRecent - time.Millisecond)
		m, _ = update(m, press("t"))
		if m.status != openingText || m.vHold.text != vRefusal || !m.vHold.shown.IsZero() {
			t.Errorf("%s: a recent message is kept: status %q hold %+v", name, m.status, m.vHold)
		}
		step(vHoldAttachLimit)
		m, _ = update(m, vHoldLimitMsg{seq: m.vHold.seq})
		assertRed(t, m, vRefusal)
		// A failure after the retry starts is held as usual.
		m = shown()
		step(time.Hour)
		m, retry = update(m, press("t"))
		m = vFail(m, "later failure")
		if m.status != openingText || m.vHold.text != "later failure" {
			t.Errorf("%s: a new failure: status %q held %q", name, m.status, m.vHold.text)
		}
		m, _ = update(m, runTermAttach(retry))
		assertRed(t, ctrlQ(m), "later failure")
	}
}

// The same rule when the retry opens a Replace dialog, which holds nothing old
// and keeps what it kept even if it's answered more than 3 s later.
func TestHoldShownMessageAcrossAReplaceDialog(t *testing.T) {
	step := clock(t)
	shown := func() Model {
		m, _ := orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
		m.vHold = vHoldState{seq: 7, since: nowFunc()}
		m = vFail(m, vRefusal)
		step(vHoldAttachLimit)
		m, _ = update(m, vHoldLimitMsg{seq: 7})
		assertRed(t, m, vRefusal)
		return m
	}
	m := shown()
	step(vHoldRecent)
	m, _ = update(m, press("t"))
	if m.screen != screenConfirm || m.vHold.text != "" {
		t.Fatalf("an old message is dropped: screen %v held %q", m.screen, m.vHold.text)
	}
	m = shown()
	m, _ = update(m, press("t"))
	if m.screen != screenConfirm || m.vHold.text != vRefusal {
		t.Fatalf("a recent message is kept: screen %v held %q", m.screen, m.vHold.text)
	}
	step(time.Minute)
	m, cmd := update(m, press("y"))
	m, cmd = update(m, runTermAttach(cmd)) // replaceOrphanMsg → attach
	if m.vHold.text != vRefusal || m.status != replacingText {
		t.Fatalf("the replacing attach keeps it: held %q status %q", m.vHold.text, m.status)
	}
	m, _ = update(m, runTermAttach(cmd))
	assertRed(t, ctrlQ(m), vRefusal)
	// Shown behind a dialog, its attach then ends: a failure held after that
	// hasn't been seen, however long the dialog stays open.
	m, _ = orphanList(t, detachedSandbox("p1", pb.AgentStatus_AGENT_STATUS_IDLE, false))
	m.vHold = vHoldState{seq: 7, since: nowFunc()}
	m, _ = update(m, press("t"))
	m = vFail(m, vRefusal)
	step(vHoldAttachLimit)
	m, _ = update(m, vHoldLimitMsg{seq: 7})
	m, _ = update(m, termClosedMsg{seq: 7})
	m = vFail(m, "later failure")
	if m.status == "later failure" || m.vHold.text != "later failure" {
		t.Fatalf("held behind the dialog: status %q held %q", m.status, m.vHold.text)
	}
	step(time.Minute)
	m, cmd = update(m, press("y"))
	m, _ = update(m, runTermAttach(cmd))
	if m.vHold.text != "later failure" {
		t.Errorf("a failure never shown stays held: %q", m.vHold.text)
	}
}

// A successful v after a failed one ends the hold: the old error doesn't come
// back when the terminal closes, whether the success came while it was opening
// or while it was open.
func TestHoldEndsOnACompletion(t *testing.T) {
	clock(t)
	done := editorOutcomeMsg{sandboxID: "sb1", text: "opened demo in VS Code"}
	check := func(m Model) {
		t.Helper()
		if m.status != "detached from demo" || m.statusIsErr() {
			t.Errorf("after the terminal closes: %q red %v", m.status, m.statusIsErr())
		}
	}
	// While it opens.
	m := runningSbx(&fakeDaemon{})
	m, cmd := update(m, press("t"))
	m = vFail(m, vRefusal)
	m, _ = update(m, done)
	if m.status != done.text || m.statusIsErr() || m.vHold.text != "" {
		t.Fatalf("after the success: %q red %v held %q", m.status, m.statusIsErr(), m.vHold.text)
	}
	m, _ = update(m, runTermAttach(cmd))
	check(ctrlQ(m))
	// While it's open.
	m = openTerm(t, runningSbx(&fakeDaemon{}))
	m = vFail(m, vRefusal)
	m, _ = update(m, done)
	check(ctrlQ(m))
	// A failure after the success is held as usual.
	m = openTerm(t, runningSbx(&fakeDaemon{}))
	m, _ = update(m, done)
	m = vFail(m, vRefusal)
	assertRed(t, ctrlQ(m), vRefusal)
}
