package ui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/termview"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// termState holds the live in-place terminal view (US2). The daemon keeps the
// underlying session alive across detach (FR-002/004), so entering/leaving this
// view is cheap and never restarts the TUI (unlike the old `sbx run` suspend).
type termState struct {
	screen     *termview.Screen
	session    client.TermSession
	cancel     context.CancelFunc
	updates    chan struct{} // signalled by the sink when new PTY output arrives
	done       chan struct{} // closed on detach to release the update waiter
	sandboxID  string
	host       string
	name       string
	cols, rows int
}

// termSink feeds daemon PTY bytes into the screen and wakes the Bubble Tea loop
// so it re-renders. The signal is best-effort (buffered, non-blocking): a full
// buffer already means a render is pending.
type termSink struct {
	screen  *termview.Screen
	updates chan struct{}
}

func (s termSink) Write(p []byte) (int, error) {
	n, err := s.screen.Write(p)
	select {
	case s.updates <- struct{}{}:
	default:
	}
	return n, err
}

// --- messages ---

type termOpenedMsg struct {
	screen     *termview.Screen
	session    client.TermSession
	cancel     context.CancelFunc
	updates    chan struct{}
	done       chan struct{}
	id, host   string
	name       string
	cols, rows int
}
type termUpdateMsg struct{}
type termClosedMsg struct {
	err  error
	name string
}

// enterTerminal (lowercase `t`) opens the sandbox's persistent session in-place
// (US2). It builds the client screen and attaches on a command so the UI never
// blocks; the session shows prior output immediately via the daemon snapshot
// (FR-003).
func (m Model) enterTerminal(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		m.status = "start the sandbox before opening its terminal"
		return m, nil
	}
	cols, rows := m.bodyWidth(), m.termRows()
	d := m.daemonForHost(host)
	id, name := sb.GetId(), sb.GetDisplayName()
	m.status = "opening terminal…"
	return m, func() tea.Msg {
		screen := termview.New(cols, rows)
		updates := make(chan struct{}, 1)
		done := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		sink := termSink{screen: screen, updates: updates}
		sess, err := d.AttachTerminal(ctx, id, client.AttachInTUI, uint32(cols), uint32(rows), sink)
		if err != nil {
			cancel()
			return termClosedMsg{err: err, name: name}
		}
		return termOpenedMsg{
			screen: screen, session: sess, cancel: cancel, updates: updates, done: done,
			id: id, host: host, name: name, cols: cols, rows: rows,
		}
	}
}

func (m Model) handleTermOpened(msg termOpenedMsg) (tea.Model, tea.Cmd) {
	m.term = termState{
		screen: msg.screen, session: msg.session, cancel: msg.cancel,
		updates: msg.updates, done: msg.done,
		sandboxID: msg.id, host: msg.host, name: msg.name, cols: msg.cols, rows: msg.rows,
	}
	m.screen = screenTerminal
	m.status = ""
	return m, m.waitTermUpdateCmd()
}

func (m Model) handleTermClosed(msg termClosedMsg) (tea.Model, tea.Cmd) {
	m.closeTerm()
	m.screen = screenList
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		if errors.Is(msg.err, client.ErrExternalAlreadyOpen) {
			m.status = "an external terminal is already attached to " + msg.name
		} else {
			m.status = "terminal: " + msg.err.Error()
		}
		return m, nil
	}
	m.status = "detached from " + msg.name
	m.listLoading = true
	return m, m.reloadCmd()
}

// waitTermUpdateCmd blocks until the sink signals new output (or the view is
// detached), then asks the loop to re-render. It returns after `done` closes so
// no goroutine leaks past detach.
func (m Model) waitTermUpdateCmd() tea.Cmd {
	ch, done := m.term.updates, m.term.done
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-ch:
		case <-done:
		}
		return termUpdateMsg{}
	}
}

// closeTerm detaches from the session (the daemon keeps it running) and releases
// the update waiter. Idempotent.
func (m *Model) closeTerm() {
	if m.term.session != nil {
		_ = m.term.session.Close()
	}
	if m.term.cancel != nil {
		m.term.cancel()
	}
	if m.term.done != nil {
		close(m.term.done)
	}
	m.term = termState{}
}

func (m *Model) resizeTerminal() {
	if m.term.screen == nil {
		return
	}
	cols, rows := m.bodyWidth(), m.termRows()
	m.term.screen.Resize(cols, rows)
	m.term.cols, m.term.rows = cols, rows
	if m.term.session != nil {
		_ = m.term.session.SendResize(uint32(cols), uint32(rows))
	}
}

func (m Model) termRows() int {
	if r := m.bodyHeight() - 2; r >= 3 {
		return r
	}
	return 3
}

// updateTerminalKey forwards keystrokes to the sandbox PTY. Ctrl+Q detaches back
// to the list (leaving the session running); everything else is translated to
// raw bytes and sent to the daemon.
func (m Model) updateTerminalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlQ {
		name := m.term.name
		m.closeTerm()
		m.screen = screenList
		m.status = "detached from " + name
		return m, nil
	}
	if m.term.session == nil {
		return m, nil
	}
	if b := keyToBytes(msg); len(b) > 0 {
		if err := m.term.session.SendData(b); err != nil {
			return m.handleTermClosed(termClosedMsg{err: err, name: m.term.name})
		}
	}
	return m, nil
}

func (m Model) terminalHelp() helpBindings {
	return helpBindings{
		hkey("ctrl+q", "detach"),
	}
}

func (m Model) viewTerminal() string {
	if m.term.screen == nil {
		return ""
	}
	header := sectionStyle.Render("Terminal ") + dimStyle.Render(m.term.name) +
		dimStyle.Render("   ctrl+q to detach (session keeps running)")
	return lipgloss.JoinVertical(lipgloss.Left, header, "", m.term.screen.Render())
}

// keyToBytes translates a Bubble Tea key event into the raw bytes a PTY expects.
// It covers the common set: printable runes, the C0 controls (enter/tab/esc/
// backspace), ctrl-letter combos, and the cursor/navigation escape sequences.
func keyToBytes(msg tea.KeyMsg) []byte {
	var out []byte
	switch msg.Type {
	case tea.KeyRunes:
		out = []byte(string(msg.Runes))
	case tea.KeySpace:
		out = []byte{' '}
	case tea.KeyEnter:
		out = []byte{'\r'}
	case tea.KeyTab:
		out = []byte{'\t'}
	case tea.KeyBackspace:
		out = []byte{0x7f}
	case tea.KeyEsc:
		out = []byte{0x1b}
	case tea.KeyUp:
		return []byte("\x1b[A")
	case tea.KeyDown:
		return []byte("\x1b[B")
	case tea.KeyRight:
		return []byte("\x1b[C")
	case tea.KeyLeft:
		return []byte("\x1b[D")
	case tea.KeyHome:
		return []byte("\x1b[H")
	case tea.KeyEnd:
		return []byte("\x1b[F")
	case tea.KeyDelete:
		return []byte("\x1b[3~")
	case tea.KeyPgUp:
		return []byte("\x1b[5~")
	case tea.KeyPgDown:
		return []byte("\x1b[6~")
	default:
		// Bubble Tea numbers the C0 control keys (KeyCtrlA..KeyCtrlZ, etc.) by
		// their control-byte value, so a small positive Type IS the byte to send.
		if t := int(msg.Type); t > 0 && t < 0x20 {
			out = []byte{byte(t)}
		}
	}
	if len(out) > 0 && msg.Alt {
		return append([]byte{0x1b}, out...)
	}
	return out
}
