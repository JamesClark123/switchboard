package portforward

import (
	"bufio"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// runningProc is the plumbing shared by both launchers: the command, its bounded
// output, the process-group id to signal on stop, and a channel closed when it
// exits.
//
// It lives here rather than in either launcher because in-sandbox and on-host
// services differ only in how they are STARTED and SIGNALLED; everything after
// that — output capture, exit detection, readiness — is identical, and the
// supervisor should not care which kind it is holding.
type runningProc struct {
	cmd  *exec.Cmd
	out  *tailBuffer
	pgid int
	// exited closes when Wait returns. waitErr is safe to read only after that.
	exited  chan struct{}
	waitErr error

	mu sync.Mutex
}

// reapAfter waits for the output pumps to finish, then reaps the process and
// closes exited.
//
// Draining the pipes BEFORE Wait is not optional: Wait closes them, so reaping
// first would truncate the last output — exactly the bytes that explain a crash.
func (p *runningProc) reapAfter(wg *sync.WaitGroup) {
	go func() {
		wg.Wait()
		err := p.cmd.Wait()
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		close(p.exited)
	}()
}

// err returns the wait error; only meaningful once exited is closed.
func (p *runningProc) err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// hasExited reports whether the process is already gone.
func (p *runningProc) hasExited() bool {
	select {
	case <-p.exited:
		return true
	default:
		return false
	}
}

// output returns the captured tail and whether anything was dropped.
func (p *runningProc) output() (string, bool) { return p.out.result() }

// shellQuote wraps s for safe inclusion inside a single-quoted shell word.
//
// Service commands are author-written and arbitrary (`pnpm dev --host 0.0.0.0`,
// pipes, quotes and all). The in-sandbox launcher has to nest one shell inside
// another, so getting this wrong would not just break exotic commands — it would
// silently change what a perfectly ordinary one means.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// streamInto copies src into the bounded buffer until EOF.
func streamInto(wg *sync.WaitGroup, dst io.Writer, src io.Reader) {
	defer wg.Done()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_, _ = dst.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// pgidMarker is the prefix the in-sandbox wrapper prints on stderr to announce its
// process-group id (research R3).
const pgidMarker = "swb-pgid:"

// streamStderrWithMarker reads the first stderr line looking for the PGID marker,
// reports it on pgidCh, and copies everything else into the bounded buffer.
//
// The marker line is deliberately kept OUT of the captured output: it is switchboard
// plumbing, and a developer reading a crash log should not have to wonder what
// "swb-pgid:214" was.
func streamStderrWithMarker(wg *sync.WaitGroup, dst io.Writer, src io.Reader, pgidCh chan<- int) {
	defer wg.Done()
	defer close(pgidCh)

	r := bufio.NewReader(src)
	line, err := r.ReadString('\n')
	if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, pgidMarker) {
		if pgid, convErr := strconv.Atoi(strings.TrimPrefix(trimmed, pgidMarker)); convErr == nil {
			pgidCh <- pgid
		}
	} else if line != "" {
		// No marker (an older image, or setsid unavailable): the line is real output.
		_, _ = io.WriteString(dst, line)
	}
	if err != nil {
		return
	}
	_, _ = io.Copy(dst, r)
}
