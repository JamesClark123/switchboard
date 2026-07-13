package ui

import (
	"bytes"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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
