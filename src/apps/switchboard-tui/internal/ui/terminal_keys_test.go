package ui

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

func TestKeyToBytes(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want []byte
	}{
		{"runes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")}, []byte("ab")},
		{"space", tea.KeyMsg{Type: tea.KeySpace}, []byte{' '}},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, []byte{'\r'}},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, []byte{'\t'}},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, []byte{0x7f}},
		{"esc", tea.KeyMsg{Type: tea.KeyEsc}, []byte{0x1b}},
		{"up", tea.KeyMsg{Type: tea.KeyUp}, []byte("\x1b[A")},
		{"down", tea.KeyMsg{Type: tea.KeyDown}, []byte("\x1b[B")},
		{"right", tea.KeyMsg{Type: tea.KeyRight}, []byte("\x1b[C")},
		{"left", tea.KeyMsg{Type: tea.KeyLeft}, []byte("\x1b[D")},
		{"home", tea.KeyMsg{Type: tea.KeyHome}, []byte("\x1b[H")},
		{"end", tea.KeyMsg{Type: tea.KeyEnd}, []byte("\x1b[F")},
		{"delete", tea.KeyMsg{Type: tea.KeyDelete}, []byte("\x1b[3~")},
		{"pgup", tea.KeyMsg{Type: tea.KeyPgUp}, []byte("\x1b[5~")},
		{"pgdown", tea.KeyMsg{Type: tea.KeyPgDown}, []byte("\x1b[6~")},
		{"ctrl-c", tea.KeyMsg{Type: tea.KeyCtrlC}, []byte{0x03}},
		{"ctrl-d", tea.KeyMsg{Type: tea.KeyCtrlD}, []byte{0x04}},
		{"alt-rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x"), Alt: true}, []byte{0x1b, 'x'}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyToBytes(tc.msg); !bytes.Equal(got, tc.want) {
				t.Fatalf("keyToBytes(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestTerminalHelpersAndResize exercises the terminal view helpers and the
// window-resize path (US2).
func TestTerminalHelpersAndResize(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd)) // termOpenedMsg
	if m.screen != screenTerminal {
		t.Fatal("expected terminal screen")
	}

	// Helpers render without panicking and reflect the session.
	if m.viewTerminal() == "" {
		t.Error("viewTerminal should render the session")
	}
	if len(m.terminalHelp()) == 0 {
		t.Error("terminalHelp should list the detach binding")
	}
	if m.termRows() < 3 {
		t.Error("termRows should never be below 3")
	}

	// A window resize resizes the screen and forwards to the daemon (no panic).
	before, _ := m.term.screen.Size()
	m2, _ := update(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	after, _ := m2.term.screen.Size()
	if after == before {
		t.Errorf("resize should change the terminal screen size (before=%d after=%d)", before, after)
	}
}

func TestTerminalHelperEmptyState(t *testing.T) {
	var m Model
	if m.viewTerminal() != "" {
		t.Error("viewTerminal with no session should render empty")
	}
	if m.termRows() != 3 {
		t.Errorf("termRows on a zero model = %d, want the floor of 3", m.termRows())
	}
	// resizeTerminal with no screen must be a no-op (no panic).
	m.resizeTerminal()
}

func TestTagHelperViews(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(sized(New(d, "/work")),
		[]*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})
	m, _ = update(m, press("#"))
	if m.viewTag() == "" {
		t.Error("viewTag should render the editor")
	}
	if len(m.tagHelp()) == 0 {
		t.Error("tagHelp should list confirm/cancel")
	}
}

// termRecorder records, in order, what the in-TUI terminal sends on ctrl+l,
// and whether the local view was already blank when the redraw was requested
// (SWI-32).
type termRecorder struct {
	client.TermSession
	screen *termview.Screen
	events []string
}

func (r *termRecorder) SendRepaint() error {
	r.events = append(r.events, fmt.Sprintf("repaint(blank=%v)", !strings.Contains(termText(r.screen), "SNAPSHOT")))
	return r.TermSession.SendRepaint()
}

func (r *termRecorder) SendData(p []byte) error {
	r.events = append(r.events, fmt.Sprintf("data:%q", p))
	return r.TermSession.SendData(p)
}

// termText joins the screen's live rows.
func termText(s *termview.Screen) string {
	var sb strings.Builder
	for _, l := range s.Lines() {
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func ctrlL(m Model) Model {
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlL})
	return m
}

// SWI-32: ctrl+l clears the local view, asks for a redraw and then forwards
// the key, in that order; a repeat press inside termResetEvery does nothing;
// the key is discoverable.
func TestCtrlLResetsTheTerminalView(t *testing.T) {
	tick := clock(t)
	d := &fakeDaemon{}
	m := openTerm(t, runningSbx(d))
	if !strings.Contains(termText(m.term.screen), "SNAPSHOT:sb1") {
		t.Fatalf("the snapshot should be on the view:\n%s", termText(m.term.screen))
	}
	rec := &termRecorder{TermSession: m.term.session, screen: m.term.screen}
	m.term.session = rec

	m = ctrlL(m)
	want := []string{"repaint(blank=true)", `data:"\f"`}
	if fmt.Sprint(rec.events) != fmt.Sprint(want) {
		t.Fatalf("ctrl+l events = %q, want %q", rec.events, want)
	}
	if m.screen != screenTerminal || m.status != termResetText || d.repaints != 1 {
		t.Errorf("after ctrl+l: screen %v, status %q, repaints %d", m.screen, m.status, d.repaints)
	}
	if strings.Contains(termText(m.term.screen), "SNAPSHOT") {
		t.Error("the view should be cleared")
	}

	// A second press inside termResetEvery clears, requests and forwards nothing.
	tick(termResetEvery / 2)
	m = ctrlL(m)
	if len(rec.events) != 2 || d.repaints != 1 {
		t.Errorf("a repeat press inside %v did something: %q", termResetEvery, rec.events)
	}
	// After it, the reset works again.
	tick(termResetEvery)
	m = ctrlL(m)
	if len(rec.events) != 4 || d.repaints != 2 {
		t.Errorf("a press after %v should reset again: %q", termResetEvery, rec.events)
	}

	if !strings.Contains(m.viewTerminal(), "ctrl+l reset view") {
		t.Errorf("the header should mention ctrl+l:\n%s", m.viewTerminal())
	}
	var listed bool
	for _, b := range m.terminalHelp() {
		listed = listed || strings.Contains(fmt.Sprint(b), "ctrl+l")
	}
	if !listed {
		t.Errorf("the terminal help should list ctrl+l: %v", m.terminalHelp())
	}
}

// SWI-32: in a terminal opened from Jobs the first ctrl+l ends the esc hold
// like any other key, and a later esc goes straight through.
func TestCtrlLClearsAHeldEsc(t *testing.T) {
	clock(t)
	m, d := terminalJobs(t)
	m = overviewAt(t, m, "run-run")
	m, cmd := update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	if !m.term.escHeld {
		t.Fatal("a Jobs terminal should start with the esc held")
	}
	rec := &termRecorder{TermSession: m.term.session, screen: m.term.screen}
	m.term.session = rec
	m = ctrlL(m)
	if m.term.escHeld || m.status != termResetText || d.repaints != 1 {
		t.Errorf("after ctrl+l: escHeld %v, status %q, repaints %d", m.term.escHeld, m.status, d.repaints)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := rec.events[len(rec.events)-1]; got != `data:"\x1b"` {
		t.Errorf("esc after ctrl+l: last event %q, want it sent at once", got)
	}

	// With the hint showing (a first esc held), ctrl+l replaces it.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlQ})
	m, cmd = update(m, press("t"))
	m, _ = update(m, runCmd(cmd))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.status != escHoldHint {
		t.Fatalf("status = %q, want the esc hint", m.status)
	}
	m = ctrlL(m)
	if m.status != termResetText || m.term.escHeld {
		t.Errorf("ctrl+l over the hint: status %q, escHeld %v", m.status, m.term.escHeld)
	}
}

// SWI-32: a failed redraw request closes the terminal as a failed SendData does.
func TestCtrlLRepaintErrorClosesTheTerminal(t *testing.T) {
	clock(t)
	d := &fakeDaemon{repaintErr: errors.New("stream broken")}
	m := openTerm(t, runningSbx(d))
	m = ctrlL(m)
	if m.screen != screenList || !d.termClosed || !strings.Contains(m.status, "terminal: ") {
		t.Errorf("after a failed repaint: screen %v, closed %v, status %q", m.screen, d.termClosed, m.status)
	}
}
