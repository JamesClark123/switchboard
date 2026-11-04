package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// SC-008 (terminal, T041): a long status while the terminal is open sends no
// resize and keeps the header; a terminal opened during a wrapped message gets
// the same PTY size as one opened with none.
func TestStatusTerminalNoResize(t *testing.T) {
	d := &fakeDaemon{}
	m := runningSbx(d)
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = openTerm(t, m)
	rowsClean := d.attachRows
	m = setStatus(m, strings.Repeat("a long status while the terminal is open ", 8))
	if d.resizes != 0 {
		t.Errorf("a status change sent %d resizes", d.resizes)
	}
	lines := assertFits(t, m, 80, 24)
	if !strings.Contains(strings.Join(lines, "\n"), "Terminal") {
		t.Error("the terminal header must stay")
	}
	if m.term.rows != int(rowsClean) {
		t.Errorf("PTY rows changed: %d vs %d", m.term.rows, rowsClean)
	}
	m = ctrlQ(m)
	// Opened during a wrapped message: the same PTY size.
	m = setStatus(m, strings.Repeat("a long status before opening the terminal ", 8))
	if m.bodyHeight() >= 18 {
		t.Fatal("the status should have shrunk the body")
	}
	m = openTerm(t, m)
	if d.attachRows != rowsClean {
		t.Errorf("PTY rows with a wrapped message %d, with none %d", d.attachRows, rowsClean)
	}
	// A window resize still resizes the PTY.
	m, _ = update(m, tea.WindowSizeMsg{Width: 80, Height: 30})
	if d.resizes != 1 || m.term.rows != 22 {
		t.Errorf("window resize: resizes %d rows %d", d.resizes, m.term.rows)
	}
}
