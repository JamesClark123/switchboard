package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// approvalState backs the modal that gates a requires-approval escape-hatch run
// (feature 005, FR-039). Like the destructive-action confirm, the pending run is
// captured up front so the decision is unambiguous even if the list moves.
type approvalState struct {
	runID     string
	command   string
	sandboxID string
	sandbox   string // display name, resolved at open time
	host      string
}

// enterApproval opens the approval modal for a pending run.
func (m Model) enterApproval(run *pb.EscapeHatchRun) (tea.Model, tea.Cmd) {
	name, host := m.sandboxLabelAndHost(run.GetSandboxId())
	m.approval = approvalState{
		runID:     run.GetId(),
		command:   run.GetCommand(),
		sandboxID: run.GetSandboxId(),
		sandbox:   name,
		host:      host,
	}
	m.screen = screenApproval
	return m, nil
}

func (m Model) approvalHelp() helpBindings {
	return helpBindings{hkey("y/enter", "approve"), hkey("n/esc", "deny")}
}

// updateApprovalKey answers the modal. Deny is the default: n, esc, and q deny, and
// any unrecognised key is ignored rather than read as approval (deny-by-default,
// SC-003) — the same discipline as the destructive-action confirm.
func (m Model) updateApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		return m.decideApproval(true)
	case "n", "N", "esc", "q", "ctrl+c":
		return m.decideApproval(false)
	}
	return m, nil
}

func (m Model) decideApproval(approved bool) (tea.Model, tea.Cmd) {
	runID, host := m.approval.runID, m.approval.host
	delete(m.pendingRuns, runID)
	m.approval = approvalState{}
	m.screen = screenList
	return m, decideRunCmd(m.daemonForHost(host), runID, approved)
}

// decideRunCmd sends the approve/deny decision. The daemon is captured as a
// parameter, never read off m inside the closure (Model is value-copied each Update).
func decideRunCmd(d Daemon, runID string, approved bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if _, err := d.DecideEscapeHatchRun(ctx, runID, approved); err != nil {
			return errMsg{err}
		}
		verb := "approved"
		if !approved {
			verb = "denied"
		}
		return statusMsg("escape-hatch run " + verb)
	}
}

// approvalModal renders the approval dialog, naming the sandbox, the exact command,
// and that it runs on the host (FR-039).
func (m Model) approvalModal() string {
	rows := []string{
		sectionStyle.Render("Approve escape-hatch command?"),
		"",
		"Sandbox: " + selectedStyle.Render(m.approval.sandbox),
		"Command: " + selectedStyle.Render(m.approval.command),
		"",
		dangerStyle.Render("This runs on the HOST, outside the sandbox."),
		"",
		dimStyle.Render("y/enter approve · n/esc deny"),
	}
	inner := lipgloss.JoinVertical(lipgloss.Left, rows...)
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}

// sandboxLabelAndHost resolves a sandbox id to its display name and owning host for
// UI copy and command routing; falls back to the id and active host when unknown.
func (m Model) sandboxLabelAndHost(sandboxID string) (string, string) {
	for _, sb := range m.sandboxes {
		if sb.GetId() == sandboxID {
			host := sb.GetHostId()
			if host == "" {
				host = m.activeHost
			}
			return sb.GetDisplayName(), host
		}
	}
	return sandboxID, m.activeHost
}
