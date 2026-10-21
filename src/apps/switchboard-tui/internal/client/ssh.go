package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
)

// RunAskpassIfRequested reports whether this process was invoked by ssh as its
// SSH_ASKPASS helper (dial.SSHCommand wires this binary in when a password is
// supplied, so the TUI can collect an SSH password in a masked field and hand it
// to ssh non-interactively instead of ssh prompting on the shared controlling
// terminal the Bubble Tea TUI already owns in raw mode). When it returns true
// the caller MUST exit immediately without doing anything else — main() calls
// this before any other startup.
func RunAskpassIfRequested() bool {
	if os.Getenv(dial.AskpassSentinelEnv) != "1" {
		return false
	}
	fmt.Println(os.Getenv(dial.AskpassSecretEnv))
	return true
}

// SSHCommand builds `ssh [opts] [hardening] <target> sxbd dial-stdio`; see
// dial.SSHCommand. Kept as a forward so callers in this module read naturally.
func SSHCommand(target string, opts []string, password string) *exec.Cmd {
	return dial.SSHCommand(target, opts, password)
}

// DialCommand dials a daemon over the stdio of cmd (see dial.DialCommand).
func DialCommand(ctx context.Context, cmd *exec.Cmd) (*Conn, error) {
	return wrap(dial.DialCommand(ctx, cmd))
}

// DialSSH connects to a remote daemon over SSH using the dial-stdio bridge
// (FR-004). password is supplied non-interactively when non-empty; empty means
// key/agent auth only. A failure is a *dial.DialError carrying ssh's last words.
func DialSSH(ctx context.Context, target string, opts []string, password string) (*Conn, error) {
	return wrap(dial.DialSSH(ctx, target, opts, password))
}
