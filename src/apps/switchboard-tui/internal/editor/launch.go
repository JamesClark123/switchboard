package editor

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// Launcher starts an editor command detached from the TUI (FR-511). The TUI's
// model holds one; tests inject a recording fake.
type Launcher interface {
	Launch(argv []string) (*Started, error)
}

// Started is a launched editor. Report receives at most one value: a non-zero
// exit or a signal within exitGrace of the start (FR-523). It is closed with
// nothing sent for a zero exit or a command still running after the grace.
type Started struct {
	Report <-chan ExitReport
	pid    int
}

// ExitReport is an early failure: the exit status, or -1 with the signal's
// name when the editor was killed.
type ExitReport struct {
	Status int
	Signal string
}

// exitGrace is how long after the start a non-zero exit is reported. A
// variable so tests can shorten it.
var exitGrace = 3 * time.Second

type launcher struct{}

// NewLauncher returns the real launcher.
func NewLauncher() Launcher { return launcher{} }

// Launch starts argv in its own session (Setsid: own process group, no
// controlling terminal) with stdin, stdout and stderr connected to /dev/null
// and the TUI's environment. One goroutine waits for the editor however long
// it runs, so it is always reaped and never a zombie; a second decides the
// report within exitGrace. A start failure returns the error with the OS
// reason reachable through errors.Unwrap (FR-518).
func (launcher) Launch(argv []string) (*Started, error) {
	if len(argv) == 0 {
		return nil, errors.New("editor: empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	report := make(chan ExitReport, 1)
	go func() {
		defer close(report)
		select {
		case err := <-waited:
			if r, failed := exitReport(err); failed {
				report <- r
			}
		case <-time.After(exitGrace):
		}
	}()
	return &Started{Report: report, pid: cmd.Process.Pid}, nil
}

// exitReport turns Wait's error into a report; a nil error (exit 0) is no failure.
func exitReport(err error) (ExitReport, bool) {
	if err == nil {
		return ExitReport{}, false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return ExitReport{Status: -1}, true
	}
	r := ExitReport{Status: ee.ExitCode()}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		r.Signal = ws.Signal().String()
	}
	return r, true
}
