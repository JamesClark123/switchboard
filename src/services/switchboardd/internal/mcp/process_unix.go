//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// setPgid puts the command in its own process group so a timeout/cancel can signal
// the whole child tree (the authorize flow may spawn helpers), not just sbx.
func setPgid(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killPgid sends SIGKILL to the command's process group. Best-effort: the process
// may already be gone.
func killPgid(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
