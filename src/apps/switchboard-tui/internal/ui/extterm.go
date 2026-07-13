package ui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// extTerminal tracks an external terminal window this TUI spawned for a sandbox,
// so a repeat `T` focuses it instead of opening a second one (US3, FR-014/015).
type extTerminal struct {
	proc      *os.Process
	sandboxID string
}

// openExternalTerminal (uppercase `T`) opens the sandbox's persistent session in
// a separate terminal window running `sxb attach`. Only one external terminal per
// sandbox is allowed: if one is already tracked (or the daemon reports an external
// attachment), it is brought to the front / reported rather than duplicated.
func (m Model) openExternalTerminal(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		m.status = "start the sandbox before opening its terminal"
		return m, nil
	}
	id, name := sb.GetId(), sb.GetDisplayName()

	// Already open from this TUI: focus it (best-effort) rather than spawn a second.
	if et, ok := m.extTerm[id]; ok && processAlive(et.proc) {
		if err := focusWindow(et.proc); err == nil {
			m.status = "brought " + name + "’s terminal to the front"
		} else {
			m.status = name + " is already open in an external terminal"
		}
		return m, nil
	}
	// Open elsewhere (another client / machine): the daemon refuses a second
	// external attach, so don't even spawn one.
	if sb.GetExternalAttached() {
		m.status = name + " already has an external terminal open"
		return m, nil
	}

	prefix := strings.Fields(m.terminal)
	if len(prefix) == 0 {
		m.status = "no terminal configured (set SWITCHBOARD_TERMINAL)"
		return m, nil
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "sxb"
	}
	// e.g. `gnome-terminal -- sxb attach --host <h> --sandbox <id>`
	inner := []string{exe, "attach", "--sandbox", id}
	if host != "" {
		inner = append(inner, "--host", host)
	}
	args := append(append([]string{}, prefix[1:]...), inner...)
	cmd := exec.Command(prefix[0], args...)
	if err := cmd.Start(); err != nil {
		return m, func() tea.Msg { return errMsg{err} }
	}
	m.extTerm[id] = &extTerminal{proc: cmd.Process, sandboxID: id}
	m.status = "opened " + name + " in an external terminal"
	return m, nil
}

// processAlive reports whether p is still running (signal 0 probes existence).
func processAlive(p *os.Process) bool {
	if p == nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// errFocusUnsupported is returned by focusWindow where no reliable, portable way
// to raise another process's window exists; callers fall back to a status hint.
var errFocusUnsupported = errors.New("window focus not supported on this platform")

// focusWindow attempts to raise the terminal window owning p. Portable window
// activation from a child PID is unreliable (it needs window-manager cooperation),
// so this is intentionally a no-op that reports it did nothing — the single-window
// guarantee (no duplicate spawn) is what matters and is enforced above and by the
// daemon. A future enhancement can shell out to `wmctrl`/AppleScript here.
func focusWindow(_ *os.Process) error { return errFocusUnsupported }
