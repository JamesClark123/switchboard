package portforward

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// launchOnHost starts a service's command on the daemon's own host, in the
// sandbox's workspace (or a declared subdirectory of it).
//
// The process gets its own process group (Setpgid) so stopping can signal the whole
// tree rather than just the launched shell — the realistic command is a wrapper
// like `pnpm dev` whose child is the real listener (research R6).
//
// PORT and SWITCHBOARD_SERVICE_PORT are exported with the EFFECTIVE port. Many
// frameworks honour PORT without any command change, which gives an author a second
// way to get the {{port}} behaviour.
func (s *Supervisor) launchOnHost(ctx context.Context, workspacePath, relDir, command string, effPort uint32) (*runningProc, error) {
	dir, err := resolveHostWorkdir(workspacePath, relDir)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", effPort),
		fmt.Sprintf("SWITCHBOARD_SERVICE_PORT=%d", effPort),
	)
	setPgid(cmd)
	// Cancelling the context kills the whole group, not just the shell; otherwise a
	// grandchild keeps the output pipes open and Wait blocks forever.
	cmd.Cancel = func() error { killGroup(cmd, false); return nil }

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	proc := &runningProc{cmd: cmd, out: newTailBuffer(maxCapturedOutput), exited: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	proc.pgid = cmd.Process.Pid // Setpgid makes the child its own group leader

	var wg sync.WaitGroup
	wg.Add(2)
	go streamInto(&wg, proc.out, stdout)
	go streamInto(&wg, proc.out, stderr)
	proc.reapAfter(&wg)
	return proc, nil
}

// resolveHostWorkdir joins the service's workspace-relative directory onto the
// sandbox's workspace and re-validates containment on the host, independently of
// the authoring and attach checks (defence in depth — the same belt-and-braces
// escapehatch.resolveWorkdir applies).
func resolveHostWorkdir(workspacePath, relDir string) (string, error) {
	if relDir == "" {
		return workspacePath, nil
	}
	if filepath.IsAbs(relDir) {
		return "", fmt.Errorf("working_dir %q must be relative to the workspace", relDir)
	}
	joined := filepath.Join(workspacePath, relDir)
	rel, err := filepath.Rel(workspacePath, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working_dir %q escapes the workspace", relDir)
	}
	if info, err := os.Stat(joined); err != nil || !info.IsDir() {
		return "", fmt.Errorf("working_dir %q is not a directory in this sandbox's workspace", relDir)
	}
	return joined, nil
}
