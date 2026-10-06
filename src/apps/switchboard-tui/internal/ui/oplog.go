package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// opLogMaxLines bounds one sandbox's retained output; sbx image pulls are chatty,
// and only the tail is ever useful for diagnosing a failed install command.
const opLogMaxLines = 2000

// opLog is the output of the most recent seed-mutating operation on one sandbox
// — launch, kit attach, refresh or source add — as the daemon streams it
// (LaunchProgress.log_line is sbx's combined stdout/stderr, which is where a
// kit's install commands print). Neither sbx nor the daemon keeps this anywhere,
// so this buffer is the only place that output can be read after the fact.
// Entries are pointers so progress frames mutate them in place across Model
// copies, like launchInFlight.
type opLog struct {
	name    string   // sandbox display name when the op started ("" => resolve on render)
	verb    string   // "launch", "kit add", "refresh", "add sources"
	lines   []string // streamed log lines, oldest first (capped at opLogMaxLines)
	latest  string   // most recent progress text (a log line or a copy percentage)
	dropped bool     // lines were discarded to stay within opLogMaxLines
	done    bool
	err     error
	// ch is the frame channel of a streamOpCmd-driven op (nil for launches and
	// source adds, which own their channels elsewhere).
	ch chan tea.Msg
}

// opLogState backs the `l` overlay: which log is open and how far the user has
// scrolled up from the tail (0 = follow new output).
type opLogState struct {
	id     string
	scroll int
}

// opStartedMsg announces that a streaming operation began and carries the
// channel its frames arrive on; opProgressMsg is one frame and opResultMsg the
// terminal outcome. They are the generic counterparts of the launch-specific
// messages, for operations (kit attach, refresh) that stream LaunchProgress
// against an existing row rather than a placeholder.
type opStartedMsg struct {
	id, verb string
	ch       chan tea.Msg
}

type opProgressMsg struct {
	id     string
	update client.LaunchUpdate
}

type opResultMsg struct {
	id       string
	sb       *pb.Sandbox
	err      error
	okStatus func(*pb.Sandbox) string // status-line text on success
}

// streamOpCmd runs call in its own goroutine, forwarding every streamed frame as
// an opProgressMsg under id and the outcome as an opResultMsg. The command itself
// only announces the start; the Update loop then re-arms waitForMsg on the
// channel until the result lands — the pattern startLaunch uses.
func streamOpCmd(id, verb string, timeout time.Duration,
	call func(ctx context.Context, onLog func(client.LaunchUpdate)) (*pb.Sandbox, error),
	okStatus func(*pb.Sandbox) string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, 32)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			sb, err := call(ctx, func(u client.LaunchUpdate) {
				ch <- opProgressMsg{id: id, update: u}
			})
			ch <- opResultMsg{id: id, sb: sb, err: err, okStatus: okStatus}
			close(ch)
		}()
		return opStartedMsg{id: id, verb: verb, ch: ch}
	}
}

// --- buffer maintenance (shared with the launch and sources flows) ---

// beginOpLog starts a fresh log for id, replacing any earlier operation's.
func (m *Model) beginOpLog(id, name, verb string) {
	if m.opLogs == nil {
		m.opLogs = map[string]*opLog{}
	}
	m.opLogs[id] = &opLog{name: name, verb: verb}
}

func (m *Model) appendOpLog(id, line string) {
	l, ok := m.opLogs[id]
	if !ok {
		return
	}
	l.lines = append(l.lines, line)
	l.latest = line
	if len(l.lines) > opLogMaxLines {
		l.lines = l.lines[len(l.lines)-opLogMaxLines:]
		l.dropped = true
	}
}

// setOpLatest records non-line progress (a copy percentage) for the row badge
// without adding it to the retained output.
func (m *Model) setOpLatest(id, text string) {
	if l, ok := m.opLogs[id]; ok {
		l.latest = text
	}
}

// finishOpLog closes the log and makes it the fallback the `l` key opens when the
// highlighted row has none (a failed launch's row is gone by then).
func (m *Model) finishOpLog(id string, err error) {
	l, ok := m.opLogs[id]
	if !ok {
		return
	}
	l.done, l.err = true, err
	m.lastOpLogID = id
}

// rekeyOpLog moves a log recorded under a launch placeholder id to the daemon's
// real sandbox id once the launch resolves.
func (m *Model) rekeyOpLog(from, to string) {
	l, ok := m.opLogs[from]
	if !ok || from == to {
		return
	}
	delete(m.opLogs, from)
	m.opLogs[to] = l
	if m.opLogView.id == from {
		m.opLogView.id = to
	}
}

// opLogHint is appended to a failure status when captured output exists, so the
// user knows where the rest of the explanation went.
func (m Model) opLogHint(id string) string {
	if l, ok := m.opLogs[id]; ok && len(l.lines) > 0 {
		return " — press l for the sbx output"
	}
	return ""
}

// opLatest is the live progress text of an operation in flight on id, or "" —
// rendered next to the row's busy spinner.
func (m Model) opLatest(id string) string {
	if l, ok := m.opLogs[id]; ok && !l.done {
		return l.latest
	}
	return ""
}

// --- message handlers ---

func (m Model) handleOpStarted(msg opStartedMsg) (tea.Model, tea.Cmd) {
	m.beginOpLog(msg.id, m.sandboxName(msg.id), msg.verb)
	m.opLogs[msg.id].ch = msg.ch
	return m, waitForMsg(msg.ch)
}

func (m Model) handleOpProgress(msg opProgressMsg) (tea.Model, tea.Cmd) {
	l, ok := m.opLogs[msg.id]
	if !ok || l.done || l.ch == nil {
		return m, nil // already resolved; drop a late frame
	}
	u := msg.update
	switch {
	case u.Copy != nil:
		total := u.Copy.GetBytesTotal()
		pct := 0
		if total > 0 {
			pct = int(100 * u.Copy.GetBytesCopied() / total)
		}
		m.setOpLatest(msg.id, fmt.Sprintf("copying %d%%", pct))
	case u.LogLine != "":
		m.appendOpLog(msg.id, u.LogLine)
	}
	m.refreshListItems()
	return m, waitForMsg(l.ch)
}

// handleOpResult closes the log, then hands the outcome to the ordinary
// statusMsg/errMsg paths (clear the row spinner, reload the list) so a streamed
// operation ends exactly like a unary one did.
func (m Model) handleOpResult(msg opResultMsg) (tea.Model, tea.Cmd) {
	m.finishOpLog(msg.id, msg.err)
	if msg.err != nil {
		next, cmd := m.Update(errMsg{msg.err})
		out := next.(Model)
		out.status += out.opLogHint(msg.id)
		return out, cmd
	}
	status := ""
	if msg.okStatus != nil {
		status = msg.okStatus(msg.sb)
	}
	return m.Update(statusMsg(status))
}

// --- the `l` overlay ---

// enterOpLog opens the output overlay for sb (the highlighted row, which may be a
// still-creating placeholder). With no log for that row it falls back to the most
// recently finished operation, so a failed launch whose row has vanished can
// still be inspected.
func (m Model) enterOpLog(sb *pb.Sandbox) (tea.Model, tea.Cmd) {
	id := ""
	if sb != nil {
		if _, ok := m.opLogs[sb.GetId()]; ok {
			id = sb.GetId()
		}
	}
	if id == "" {
		if _, ok := m.opLogs[m.lastOpLogID]; ok {
			id = m.lastOpLogID
		}
	}
	if id == "" {
		m.status = "no sbx output recorded yet — it is kept for launches, kit attaches, refreshes and source adds made in this session"
		return m, nil
	}
	m.opLogView = opLogState{id: id}
	m.screen = screenOpLog
	return m, nil
}

func (m Model) opLogHelp() helpBindings {
	return helpBindings{
		hkey("↑/↓", "scroll"),
		hkey("pgup/pgdn", "page"),
		hkey("g/G", "top/tail"),
		hkey("esc", "close"),
	}
}

func (m Model) updateOpLogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	_, _, total, avail := m.opLogWindow()
	maxScroll := max(total-avail, 0)
	switch msg.String() {
	case "esc", "q", "l":
		m.opLogView = opLogState{}
		m.screen = screenList
	case "up", "k":
		m.opLogView.scroll = min(m.opLogView.scroll+1, maxScroll)
	case "down", "j":
		m.opLogView.scroll = max(m.opLogView.scroll-1, 0)
	case "pgup":
		m.opLogView.scroll = min(m.opLogView.scroll+avail, maxScroll)
	case "pgdown":
		m.opLogView.scroll = max(m.opLogView.scroll-avail, 0)
	case "g", "home":
		m.opLogView.scroll = maxScroll
	case "G", "end":
		m.opLogView.scroll = 0
	}
	return m, nil
}

// opLogWindow computes the visible slice [start, end) of the open log given the
// modal height and the scroll offset (lines up from the tail).
func (m Model) opLogWindow() (start, end, total, avail int) {
	avail = max(m.height-12, 5)
	l, ok := m.opLogs[m.opLogView.id]
	if !ok {
		return 0, 0, 0, avail
	}
	total = len(l.lines)
	scroll := min(m.opLogView.scroll, max(total-avail, 0))
	end = total - scroll
	start = max(end-avail, 0)
	return start, end, total, avail
}

func (m Model) opLogModal() string {
	l, ok := m.opLogs[m.opLogView.id]
	if !ok {
		return modalStyle.Width(m.modalInnerWidth()).Render(dimStyle.Render("(no output)"))
	}
	name := l.name
	if name == "" {
		name = m.sandboxName(m.opLogView.id)
	}
	state := dimStyle.Render("done")
	switch {
	case !l.done:
		state = m.spinner.View() + " " + selectedStyle.Render("running")
	case l.err != nil:
		state = statusErrStyle.Render("failed")
	}
	title := sectionStyle.Render("sbx output · "+name) + dimStyle.Render("  ·  "+l.verb+"  ·  ") + state

	start, end, total, avail := m.opLogWindow()
	width := max(m.modalInnerWidth()-2, 10)
	rows := make([]string, 0, avail+2)
	if start == 0 && l.dropped {
		rows = append(rows, dimStyle.Render("… earlier output dropped"))
	}
	for _, line := range l.lines[start:end] {
		rows = append(rows, ansi.Truncate(line, width, "…"))
	}
	if total == 0 {
		rows = append(rows, dimStyle.Render("(no output yet)"))
	}
	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", lipgloss.JoinVertical(lipgloss.Left, rows...))
	if total > avail {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", dimStyle.Render(fmt.Sprintf("lines %d–%d of %d", start+1, end, total)))
	}
	if l.err != nil {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", statusErrStyle.Render(l.err.Error()))
	}
	inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", helpStyle.Render("↑/↓ scroll · pgup/pgdn page · g/G top/tail · esc close"))
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
