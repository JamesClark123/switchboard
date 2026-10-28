package ui

import (
	"context"
	"errors"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
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
	// returnTo is the screen leaving the terminal goes back to (SWI-14): the
	// Sandboxes list (the zero value) or the Jobs screen it was opened from.
	returnTo screen
	seq      uint64 // the attach that opened it (Model.termSeq)
	// escHeld: opened from Jobs and nothing typed yet. A lone esc there is
	// likely meant as "back", but it would interrupt the worker's agent, so
	// the first one is held and explained instead (SWI-14).
	escHeld bool
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
	returnTo   screen
	seq        uint64
}
type termUpdateMsg struct{}
type termClosedMsg struct {
	err      error
	name     string
	returnTo screen
	seq      uint64 // the attach (or open terminal) it is about
}

// enterTerminal (lowercase `t`) opens the sandbox's persistent session in-place
// (US2). It builds the client screen and attaches on a command so the UI never
// blocks; the session shows prior output immediately via the daemon snapshot
// (FR-003).
func (m Model) enterTerminal(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	return m.enterTerminalFrom(sb, host, m.daemonForHost(host), screenList)
}

// enterTerminalFrom attaches to a running sandbox's session on daemon d, which
// the caller resolved: detaching, or the session closing, returns to returnTo
// (SWI-14).
func (m Model) enterTerminalFrom(sb *pb.Sandbox, host string, d Daemon, returnTo screen) (tea.Model, tea.Cmd) {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		m.status = "start the sandbox before opening its terminal"
		return m, nil
	}
	m.termSeq++
	seq := m.termSeq
	cols, rows := m.bodyWidth(), m.termRows()
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
			return termClosedMsg{err: err, name: name, returnTo: returnTo, seq: seq}
		}
		return termOpenedMsg{
			screen: screen, session: sess, cancel: cancel, updates: updates, done: done,
			id: id, host: host, name: name, cols: cols, rows: rows, returnTo: returnTo, seq: seq,
		}
	}
}

func (m Model) handleTermOpened(msg termOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.termSeq || !m.attachAwaited(msg.returnTo) {
		// A newer attach superseded it, or the developer moved on (another
		// screen, an overlay) while it attached: detach rather than pull them in.
		_ = msg.session.Close()
		msg.cancel()
		close(msg.done)
		if msg.seq == m.termSeq {
			m.status = "terminal for " + msg.name + " not opened — you moved on; press t again"
		}
		return m, nil
	}
	m.term = termState{
		screen: msg.screen, session: msg.session, cancel: msg.cancel,
		updates: msg.updates, done: msg.done,
		sandboxID: msg.id, host: msg.host, name: msg.name, cols: msg.cols, rows: msg.rows,
		returnTo: msg.returnTo, seq: msg.seq, escHeld: msg.returnTo == screenJobs,
	}
	m.screen = screenTerminal
	m.status = ""
	return m, m.waitTermUpdateCmd()
}

// attachAwaited reports whether the developer is still where an attach was
// started from, with nothing opened over it since.
func (m Model) attachAwaited(from screen) bool {
	if m.screen != from {
		return false
	}
	return from != screenJobs || (m.jobs.prompt == nil && m.jobs.form == nil && m.jobs.results == nil)
}

// handleTermClosed reports a session that closed, or an attach that failed. It
// changes screen only when it is about the terminal showing: a failed attach
// leaves the developer wherever they are now.
func (m Model) handleTermClosed(msg termClosedMsg) (tea.Model, tea.Cmd) {
	showing := m.screen == screenTerminal && msg.seq == m.term.seq
	if !showing && msg.seq != m.termSeq {
		return m, nil // about an attach a newer one superseded: nothing to report
	}
	var cmd tea.Cmd
	if showing {
		m.closeTerm()
		m, cmd = m.leaveTerminal(msg.returnTo)
	}
	if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
		if errors.Is(msg.err, client.ErrExternalAlreadyOpen) {
			m.status = "an external terminal is already attached to " + msg.name
		} else {
			m.status = "terminal: " + msg.err.Error()
		}
		return m, cmd
	}
	m.status = "detached from " + msg.name
	m.listLoading = true
	return m, tea.Batch(cmd, m.reloadCmd())
}

// leaveTerminal shows the screen a closed terminal returns to. Jobs is entered
// as from the screen cycle, so reloads skipped while the terminal hid it run
// now (SWI-14).
func (m Model) leaveTerminal(back screen) (Model, tea.Cmd) {
	if back == screenJobs {
		mm, cmd := m.enterJobs(screenTerminal)
		return mm.(Model), cmd
	}
	m.screen = back
	return m, nil
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

// escHoldHint explains a held first esc in a terminal opened from Jobs; the
// next key clears it.
const escHoldHint = "esc goes to the agent and interrupts its turn — press esc again to send it, ctrl+q to go back to Jobs"

// updateTerminalKey forwards keystrokes to the sandbox PTY. Ctrl+Q detaches back
// to the screen the terminal was opened from (leaving the session running);
// everything else is translated to raw bytes and sent to the daemon.
func (m Model) updateTerminalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlQ {
		name, back := m.term.name, m.term.returnTo
		m.closeTerm()
		m, cmd := m.leaveTerminal(back)
		m.status = "detached from " + name
		return m, cmd
	}
	if m.term.session == nil {
		return m, nil
	}
	if m.term.escHeld {
		m.term.escHeld = false
		if msg.Type == tea.KeyEsc {
			m.status = escHoldHint
			return m, nil
		}
	}
	if m.status == escHoldHint {
		m.status = "" // the held esc is resolved: this key is sent (or ignored)
	}
	if b := keyToBytes(msg); len(b) > 0 {
		// Typing snaps the view back to the live output so the user sees what
		// they're sending rather than staying stuck in scrollback.
		if m.term.screen != nil {
			m.term.screen.ScrollToBottom()
		}
		if err := m.term.session.SendData(b); err != nil {
			return m.handleTermClosed(termClosedMsg{err: err, name: m.term.name, returnTo: m.term.returnTo, seq: m.term.seq})
		}
	}
	return m, nil
}

// updateTerminalMouse scrolls the in-place terminal's scrollback with the wheel
// (US2). The screen is a pointer, so mutating it here re-renders on the next
// View pass with no extra command.
func (m Model) updateTerminalMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.term.screen == nil {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.term.screen.ScrollUp(3)
	case tea.MouseButtonWheelDown:
		m.term.screen.ScrollDown(3)
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
	detach := "   ctrl+q to detach (session keeps running)"
	if m.term.returnTo == screenJobs {
		// The way back first: a narrow terminal cuts the header's end.
		detach = "   ctrl+q back to Jobs · keys go to the agent (esc interrupts it)"
	}
	header := sectionStyle.Render("Terminal ") + dimStyle.Render(m.term.name) + dimStyle.Render(detach)
	if off := m.term.screen.ScrollOffset(); off > 0 {
		header += dimStyle.Render("   ⇡ scrolled " + strconv.Itoa(off) + " (type to follow)")
	}
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
