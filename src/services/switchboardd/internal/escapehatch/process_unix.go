//go:build unix

package escapehatch

import (
	"os/exec"
	"syscall"
)

// setPgid puts the command in its own process group so a timeout/cancel can signal
// the whole child tree (e.g. a shell that spawned subprocesses), not just `sh`.
func setPgid(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killPgid sends SIGKILL to the command's process group, leaving no orphaned host
// process (FR-041, SC-008). Best-effort: the process may already be gone.
func killPgid(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	// Negative pid targets the whole process group created by setPgid.
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
