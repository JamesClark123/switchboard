//go:build unix

package portforward

import (
	"os/exec"
	"syscall"
)

// setPgid puts the command in its own process group so stopping can signal the
// whole child tree — the shell AND everything it spawned — rather than just `sh`.
func setPgid(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup signals the command's process group: SIGTERM for a graceful shutdown,
// SIGKILL to force it. Best-effort — the process may already be gone.
//
// The negative pid is what makes this a GROUP signal. Signalling the pid alone
// would satisfy "the process was terminated" while leaving the real listener (the
// wrapper's child) alive and the port held — the exact failure FR-048's "and every
// process it spawned" exists to prevent.
func killGroup(c *exec.Cmd, force bool) {
	if c == nil || c.Process == nil {
		return
	}
	killPGID(c.Process.Pid, force)
}

// killPGID signals a process group by id.
func killPGID(pgid int, force bool) {
	if pgid <= 0 {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-pgid, sig)
}
