package ui

import (
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// attachCmd builds the command that opens a sandbox's interactive agent terminal
// — the view you get from `sbx run --name <id>`. Local hosts run sbx directly;
// remote hosts run it over an SSH PTY (`ssh -t <target> sbx run --name <id>`) so
// the session rides the user's existing SSH, matching the rest of the client.
func attachCmd(sbxBin, sshTarget, name string) *exec.Cmd {
	if sbxBin == "" {
		sbxBin = "sbx"
	}
	if sshTarget != "" {
		// The remote host runs its own sbx from PATH; -t allocates a PTY.
		return exec.Command("ssh", "-t", sshTarget, "sbx", "run", name)
	}
	return exec.Command(sbxBin, "run", name)
}

// openAgentTerminal suspends the TUI and drops the user into the sandbox's
// interactive agent session. Bubble Tea hands the real terminal to the child
// process and restores the TUI when it exits, delivering an agentExitMsg.
func (m Model) openAgentTerminal(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		m.status = "start the sandbox before opening its terminal"
		return m, nil
	}
	name := sb.GetDisplayName()
	// sbx addresses the sandbox by its (human) name, so attach by name.
	cmd := attachCmd(m.sbxBin, m.sshTargetForHost(host), name)
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return agentExitMsg{name: name, err: err}
	})
}

// openPopoutTerminal launches the sandbox's interactive session in a SEPARATE
// terminal window (the configured/system terminal), leaving the TUI running.
// Unlike the inline terminal (t), this does not suspend Switchboard.
func (m Model) openPopoutTerminal(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		m.status = "start the sandbox before opening its terminal"
		return m, nil
	}
	prefix := strings.Fields(m.terminal)
	if len(prefix) == 0 {
		m.status = "no terminal configured (set SWITCHBOARD_TERMINAL)"
		return m, nil
	}
	name := sb.GetDisplayName()
	attach := attachCmd(m.sbxBin, m.sshTargetForHost(host), name)
	return m, popoutCmd(prefix, attach.Args, name)
}

// popoutCmd spawns the configured terminal, detached, running the sandbox's
// session command inside it (e.g. `gnome-terminal -- sbx run --name <name>`).
func popoutCmd(prefix, session []string, name string) tea.Cmd {
	return func() tea.Msg {
		args := append(append([]string{}, prefix[1:]...), session...)
		if err := exec.Command(prefix[0], args...).Start(); err != nil {
			return errMsg{err}
		}
		return statusMsg("opened " + name + " in a terminal window")
	}
}
