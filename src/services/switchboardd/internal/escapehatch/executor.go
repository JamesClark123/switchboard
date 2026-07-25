package escapehatch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

const (
	// defaultMaxDuration bounds a run whose command specifies no max (clarification
	// Q3). A hung command is killed and reported TIMED_OUT within this window.
	defaultMaxDuration = 30 * time.Minute
	// maxCapturedOutput caps captured stdout+stderr; beyond it the head is kept and
	// the run is marked truncated (research R4). The full output remains on the host.
	maxCapturedOutput = 1 << 20 // 1 MiB
)

// Outcome is the terminal result of running a command.
type Outcome struct {
	Status    pb.EscapeHatchRunStatus
	ExitCode  int32
	Output    string
	Truncated bool
}

// Executor runs escape-hatch commands on the host. It is stateless: cancellation on
// sandbox stop/destroy/refresh is owned by the Service, which cancels the parent
// context passed to Run (covering both running and pending-approval runs). Safe for
// concurrent use.
type Executor struct{}

// NewExecutor constructs an Executor.
func NewExecutor() *Executor { return &Executor{} }

// Run executes cmd's fixed command string with `sh -c` in the sandbox's workspace
// (joined with the command's optional workspace-relative working_dir), streaming and
// bounding output, bounded by the command's max_duration_seconds or the 30-minute
// default. It blocks until the command finishes, is cancelled (parent ctx), or times
// out.
func (e *Executor) Run(parent context.Context, workspacePath string, cmd *pb.EscapeHatchCommand) Outcome {
	dir, err := resolveWorkdir(workspacePath, cmd.GetWorkingDir())
	if err != nil {
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, Output: err.Error()}
	}

	timeout := defaultMaxDuration
	if secs := cmd.GetMaxDurationSeconds(); secs > 0 {
		timeout = time.Duration(secs) * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	c := exec.CommandContext(ctx, "/bin/sh", "-c", cmd.GetCommand())
	c.Dir = dir
	// Own process group so a timeout/cancel kills the whole child tree, not just sh,
	// leaving no orphaned host process (FR-041, SC-008). The default CommandContext
	// cancel only kills the direct child, which would let a grandchild keep the
	// output pipes open and block Wait; killing the group closes them.
	setPgid(c)
	c.Cancel = func() error { killPgid(c); return nil }
	// Bound any lingering pipe read after the group is killed.
	c.WaitDelay = 5 * time.Second

	cap := &boundedBuffer{limit: maxCapturedOutput}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, Output: err.Error()}
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, Output: err.Error()}
	}
	if err := c.Start(); err != nil {
		// Command could not be launched (not found, not executable, misconfigured) —
		// a FAILED run with diagnostics, never silently dropped (FR-041).
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, Output: launchError(err)}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go streamInto(&wg, cap, stdout)
	go streamInto(&wg, cap, stderr)
	wg.Wait()
	waitErr := c.Wait()

	out, truncated := cap.result()
	// Timeout is reported distinctly from a plain non-zero exit.
	if ctx.Err() == context.DeadlineExceeded {
		killPgid(c)
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT, Output: out, Truncated: truncated}
	}
	if ctx.Err() == context.Canceled {
		killPgid(c)
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED, Output: out, Truncated: truncated}
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, ExitCode: int32(exitErr.ExitCode()), Output: out, Truncated: truncated}
		}
		return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED, Output: appendLine(out, waitErr.Error()), Truncated: truncated}
	}
	return Outcome{Status: pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED, ExitCode: 0, Output: out, Truncated: truncated}
}

// resolveWorkdir joins an optional workspace-relative working_dir onto the
// workspace path and re-validates containment daemon-side (analysis S1,
// defense-in-depth), independent of the client-side check. A dir that escapes the
// workspace is refused.
func resolveWorkdir(workspacePath, workingDir string) (string, error) {
	if workingDir == "" {
		return workspacePath, nil
	}
	if filepath.IsAbs(workingDir) {
		return "", fmt.Errorf("working_dir %q must be relative to the workspace", workingDir)
	}
	joined := filepath.Join(workspacePath, workingDir)
	rel, err := filepath.Rel(workspacePath, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working_dir %q escapes the workspace", workingDir)
	}
	return joined, nil
}

func streamInto(wg *sync.WaitGroup, dst *boundedBuffer, src io.Reader) {
	defer wg.Done()
	r := bufio.NewReader(src)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = dst.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func launchError(err error) string {
	return "failed to launch command: " + err.Error()
}

func appendLine(out, line string) string {
	if out == "" {
		return line
	}
	return out + "\n" + line
}

// boundedBuffer accumulates output up to limit bytes, then drops the rest and
// records that truncation occurred. Safe for concurrent writers (stdout + stderr).
type boundedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) >= b.limit {
		b.truncated = true
		return len(p), nil
	}
	room := b.limit - len(b.buf)
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.truncated = true
	} else {
		b.buf = append(b.buf, p...)
	}
	return len(p), nil
}

func (b *boundedBuffer) result() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf), b.truncated
}
