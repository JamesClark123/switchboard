// Package vscode opens a sandbox's controlled workspace folder — the retained
// verbatim copy of the seeded files — in VS Code, launching the `code` CLI
// (FR-027, research R3). For a remote host it opens the folder over Remote-SSH;
// it does NOT attach to the running container.
package vscode

import (
	"fmt"
	"os/exec"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Opener launches the local `code` CLI to open a sandbox's workspace folder.
type Opener struct {
	CodeBin string
	// Run executes the command; overridable in tests. Defaults to Start (so the
	// editor launches detached without blocking the TUI).
	Run func(*exec.Cmd) error
}

// NewOpener constructs an Opener using the given `code` binary.
func NewOpener(codeBin string) *Opener {
	return &Opener{CodeBin: codeBin, Run: func(c *exec.Cmd) error { return c.Start() }}
}

// codeArgs builds the `code` CLI arguments to open the controlled folder. When
// sshTarget is set the folder lives on a remote daemon host, so it is opened
// over Remote-SSH (`code --remote ssh-remote+<target> <path>`); otherwise the
// folder is local and opened directly (`code <path>`).
func codeArgs(path, sshTarget string) []string {
	if sshTarget != "" {
		return []string{"--remote", "ssh-remote+" + sshTarget, path}
	}
	return []string{path}
}

// Open opens the target sandbox's controlled workspace folder in VS Code. `code`
// always runs on the user's local machine; for a remote host it opens the folder
// via Remote-SSH to that host (research R3).
func (o *Opener) Open(t *pb.VSCodeTarget, sshTarget string) error {
	path := t.GetWorkspacePath()
	if path == "" {
		return fmt.Errorf("vscode: empty workspace path")
	}
	cmd := exec.Command(o.CodeBin, codeArgs(path, sshTarget)...)
	if err := o.Run(cmd); err != nil {
		return fmt.Errorf("vscode: launch %s: %w", o.CodeBin, err)
	}
	return nil
}
