package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// runsState backs the escape-hatch run review screen (feature 005, US5).
type runsState struct {
	sandboxID string
	sandbox   string
	list      list.Model
	detail    string // captured output of the highlighted run
}

// escapeHatchBadge renders a live list-row indicator when a sandbox has an
// escape-hatch run awaiting approval, so a supervising developer sees it at a glance
// (feature 005, FR-042).
func (m Model) escapeHatchBadge(sandboxID string) string {
	for _, run := range m.pendingRuns {
		if run.GetSandboxId() == sandboxID {
			return lipgloss.NewStyle().Foreground(colWarn).Render("⛓ awaiting approval")
		}
	}
	return ""
}

// enterRuns opens the run-review screen for a sandbox, loading its session runs.
func (m Model) enterRuns(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	m.runsView = runsState{
		sandboxID: sb.GetId(),
		sandbox:   sb.GetDisplayName(),
		list:      newItemList("Escape-hatch runs", "run", "runs", m.bodyWidth(), m.bodyHeight()),
	}
	m.screen = screenRuns
	return m, m.listRunsCmd(m.daemonForHost(host), sb.GetId())
}

// listRunsCmd fetches the session's runs for a sandbox.
func (m Model) listRunsCmd(d Daemon, sandboxID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		runs, err := d.ListEscapeHatchRuns(ctx, sandboxID)
		if err != nil {
			return errMsg{err}
		}
		return runsLoadedMsg{sandboxID: sandboxID, runs: runs}
	}
}

// runsLoadedMsg carries the loaded runs back into Update.
type runsLoadedMsg struct {
	sandboxID string
	runs      []*pb.EscapeHatchRun
}

// applyRuns populates the review list from loaded runs (newest first).
func (m Model) applyRuns(msg runsLoadedMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenRuns || m.runsView.sandboxID != msg.sandboxID {
		return m, nil
	}
	items := make([]list.Item, 0, len(msg.runs))
	for i := len(msg.runs) - 1; i >= 0; i-- {
		r := msg.runs[i]
		items = append(items, listItem{
			id:      r.GetId(),
			title:   runStatusIcon(r.GetStatus()) + " " + r.GetCommandName() + dimStyle.Render("  "+runStatusText(r.GetStatus())),
			desc:    truncate(r.GetCommand(), 70),
			filter:  r.GetCommandName(),
			payload: r,
		})
	}
	m.runsView.list.SetItems(items)
	m.runsView.detail = ""
	return m, nil
}

func (m Model) runsHelp() helpBindings {
	return helpBindings{hkey("↑/↓", "select"), hkey("enter", "output"), hkey("esc", "back")}
}

// updateRunsKey drives the review screen: enter shows the highlighted run's output,
// esc returns to the list.
func (m Model) updateRunsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.runsView = runsState{}
		m.screen = screenList
		return m, nil
	case "enter":
		if it, ok := m.runsView.list.SelectedItem().(listItem); ok {
			if run, ok := it.payload.(*pb.EscapeHatchRun); ok {
				m.runsView.detail = runDetail(run)
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.runsView.list, cmd = m.runsView.list.Update(msg)
	return m, cmd
}

// viewRuns renders the run list, with the selected run's captured output below it.
func (m Model) viewRuns() string {
	header := sectionStyle.Render("Escape-hatch runs · "+m.runsView.sandbox) + "\n"
	body := m.runsView.list.View()
	if m.runsView.detail != "" {
		body += "\n\n" + m.runsView.detail
	}
	return header + body
}

// runDetail renders the full outcome (command, status, exit, output) of one run.
func runDetail(run *pb.EscapeHatchRun) string {
	var b strings.Builder
	b.WriteString(dimStyle.Render("command: ") + run.GetCommand() + "\n")
	b.WriteString(dimStyle.Render("status:  ") + runStatusText(run.GetStatus()))
	switch run.GetStatus() {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED:
		b.WriteString(dimStyle.Render("  exit ") + itoa(int(run.GetExitStatus())))
	}
	b.WriteString("\n")
	if out := run.GetOutput(); out != "" {
		label := "output:"
		if run.GetOutputTruncated() {
			label = "output (truncated):"
		}
		b.WriteString(dimStyle.Render(label) + "\n" + out)
	}
	return b.String()
}

func runStatusText(st pb.EscapeHatchRunStatus) string {
	switch st {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL:
		return "pending approval"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING:
		return "running"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED:
		return "succeeded"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_FAILED:
		return "failed"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_TIMED_OUT:
		return "timed out"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_CANCELLED:
		return "cancelled"
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_DENIED:
		return "declined"
	}
	return "unknown"
}

func runStatusIcon(st pb.EscapeHatchRunStatus) string {
	switch st {
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_SUCCEEDED:
		return lipgloss.NewStyle().Foreground(colRunning).Render("✓")
	case pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_RUNNING,
		pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL:
		return lipgloss.NewStyle().Foreground(colWarn).Render("◆")
	default:
		return lipgloss.NewStyle().Foreground(colError).Render("✗")
	}
}
