package ui

import (
	"context"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Detached agents (SWI-17, research R14; contracts/jobs-ui.md). After its
// owning daemon restarts, a sandbox's agent can survive as an orphan the daemon
// can neither show nor type into. The daemon only detects and surfaces it
// (AgentSession.agent_check); only the developer stops it. `t`/`T` on such a
// sandbox, worker or not, opens a Wait/Replace dialog instead of the terminal;
// a sandbox whose check keeps failing offers Restart sandbox/Wait; one still
// being checked asks to wait. Bulk Replace (`R` on the Workers view) is
// deferred to SWI-19 (tasks.md, Deferred items).

// The daemon's refusal texts the TUI recognises (research R14, the gate).
const (
	detachedRefusal = "agent detached by a daemon restart"
	checkingRefusal = "checking for a detached agent"
)

// replacingText is the status while a Replace re-attaches.
const replacingText = "replacing the detached agent…"

// replaceOrphanMsg is an accepted Replace: re-attach with replace_orphan.
type replaceOrphanMsg struct {
	sb       *pb.Sandbox
	host     string
	d        Daemon
	external bool
	returnTo screen
}

// orphanReplacedMsg ends a Replace started from `T`: the external window opens
// once the fresh agent is up.
type orphanReplacedMsg struct {
	sb   *pb.Sandbox
	host string
	err  error
}

// orphanGate decides whether `t`/`T` may attach to sb now. When it may not, it
// returns the dialog (or the wait notice) and true.
func (m Model) orphanGate(sb *pb.Sandbox, host string, d Daemon, external bool, returnTo screen) (tea.Model, tea.Cmd, bool) {
	check := sb.GetAgent().GetAgentCheck()
	switch check.GetState() {
	case pb.AgentCheck_STATE_UNKNOWN:
		m.status = checkingRefusal + "… try again in a moment"
		return m, nil, true
	case pb.AgentCheck_STATE_DETACHED:
		mm, cmd := m.confirmReplaceOrphan(sb, host, d, external, returnTo, check)
		return mm, cmd, true
	case pb.AgentCheck_STATE_CHECK_FAILED:
		mm, cmd := m.confirmRestartUnchecked(sb, d, returnTo, check)
		return mm, cmd, true
	}
	return m, nil, false
}

// lastSeenText is the detached agent's last known state and time ("working
// 10:42"); the time is left out when the daemon recorded none.
func lastSeenText(check *pb.AgentCheck) string {
	var state string
	switch check.GetLastStatus() {
	case pb.AgentStatus_AGENT_STATUS_WORKING:
		state = "working"
	case pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
		state = "waiting for input"
	case pb.AgentStatus_AGENT_STATUS_IDLE:
		state = "idle"
	default:
		state = "in an unknown state"
	}
	if ts := check.GetLastSeenAt(); ts != nil {
		state += " " + ts.AsTime().Local().Format("15:04")
	}
	return state
}

// failsOnReplace reports whether replacing the agent fails the worker's run:
// an ACTION run does (research R14); a run without steps is all action.
func failsOnReplace(r *pb.WorkerRun) bool {
	switch r.GetStepRole() {
	case pb.StepRole_STEP_ROLE_SETUP, pb.StepRole_STEP_ROLE_CLEANUP:
		return false
	}
	return r != nil
}

// confirmReplaceOrphan opens the Wait/Replace dialog. Wait is the default
// (enter) unless the agent was last seen idle, so a developer's own turn in a
// plain sandbox is never killed by a reflexive enter.
func (m Model) confirmReplaceOrphan(sb *pb.Sandbox, host string, d Daemon, external bool, returnTo screen, check *pb.AgentCheck) (tea.Model, tea.Cmd) {
	name := sanitizeLine(sb.GetDisplayName(), 40)
	seen := "Last seen " + lastSeenText(check)
	run := sb.GetController().GetRun()
	if run != nil {
		seen += " on " + sanitizeLine(run.GetId(), 0)
	}
	warning := "Replacing stops it."
	if run != nil && failsOnReplace(run) {
		warning = "Replacing stops it and fails " + sanitizeLine(run.GetId(), 0) + "."
	}
	msg := replaceOrphanMsg{sb: sb, host: host, d: d, external: external, returnTo: returnTo}
	return m.enterConfirm(confirmState{
		title:        name + "'s agent was detached by a daemon restart",
		body:         []string{seen + ".", "It can't be shown or typed into. Replace it?"},
		warning:      warning,
		returnTo:     returnTo,
		acceptLabel:  "replace",
		cancelLabel:  "wait",
		enterCancels: check.GetLastStatus() != pb.AgentStatus_AGENT_STATUS_IDLE,
		onConfirm:    func() tea.Msg { return msg },
	})
}

// confirmRestartUnchecked is `t` on a sandbox whose detached-agent check keeps
// failing: Restart sandbox (which ends every process in it, the only stop that
// needs no `sbx exec`) or Wait, the default. The dialog itself is the confirm
// naming the run a restart fails.
func (m Model) confirmRestartUnchecked(sb *pb.Sandbox, d Daemon, returnTo screen, check *pb.AgentCheck) (tea.Model, tea.Cmd) {
	name := sanitizeLine(sb.GetDisplayName(), 40)
	warning := "Restarting ends every process in it."
	if run := sb.GetController().GetRun(); run != nil {
		warning = "Restarting ends every process in it and fails " + sanitizeLine(run.GetId(), 0) + "."
	}
	id := sb.GetId()
	return m.enterConfirm(confirmState{
		title:        "Can't check " + name + " for a detached agent",
		body:         []string{"can't check for a detached agent: " + sanitizeLine(check.GetError(), 60), "Restart the sandbox, or wait for the check?"},
		warning:      warning,
		returnTo:     returnTo,
		acceptLabel:  "restart sandbox",
		cancelLabel:  "wait",
		enterCancels: true,
		onConfirm: func() tea.Msg {
			ctx, cancel := ctxTimeout()
			defer cancel()
			// A restart of a running sandbox is a stop, then a start: only the
			// stop ends the processes in it.
			if _, err := d.Stop(ctx, id); err != nil {
				return errMsg{err}
			}
			if _, err := d.Restart(ctx, id); err != nil {
				return errMsg{err}
			}
			return statusMsg("restarted " + name)
		},
	})
}

// startReplaceOrphan runs an accepted Replace. In place it is the terminal's
// own attach with replace_orphan; from `T` the replacing attach is made here and
// closed, then the external window opens on the fresh agent.
func (m Model) startReplaceOrphan(msg replaceOrphanMsg) (tea.Model, tea.Cmd) {
	if !msg.external {
		return m.attachTerminal(msg.sb, msg.host, msg.d, msg.returnTo, true)
	}
	m.status = replacingText
	d, sb, host := msg.d, msg.sb, msg.host
	cols, rows := uint32(m.bodyWidth()), uint32(m.termRows())
	return m, func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sess, err := d.AttachTerminalReplacing(ctx, sb.GetId(), cols, rows, io.Discard)
		if err != nil {
			return orphanReplacedMsg{sb: sb, host: host, err: err}
		}
		_ = sess.Close()
		return orphanReplacedMsg{sb: sb, host: host}
	}
}

// handleOrphanReplaced opens the external window after a Replace from `T`.
func (m Model) handleOrphanReplaced(msg orphanReplacedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "replace: " + attachErrText(msg.err)
		return m, nil
	}
	return m.spawnExternalTerminal(msg.sb, msg.host)
}

// isDetachedRefusal reports an attach the daemon refused beside a detached agent.
func isDetachedRefusal(err error) bool {
	return status.Code(err) == codes.FailedPrecondition && strings.Contains(status.Convert(err).Message(), detachedRefusal)
}

// refusalCheck is the check to show for a refusal the sandbox record did not
// yet reflect: the record's own when it has one, otherwise a detached agent in
// an unknown state (so Wait is the default).
func refusalCheck(sb *pb.Sandbox) *pb.AgentCheck {
	if c := sb.GetAgent().GetAgentCheck(); c.GetState() == pb.AgentCheck_STATE_DETACHED {
		return c
	}
	return &pb.AgentCheck{State: pb.AgentCheck_STATE_DETACHED}
}

// attachErrText is a refused attach's reason: the daemon's own text for a
// precondition (busy, still checking, detached), the error otherwise.
func attachErrText(err error) string {
	if s, ok := status.FromError(err); ok && s.Code() == codes.FailedPrecondition {
		return sanitizeLine(s.Message(), 0)
	}
	return err.Error()
}

// orphanNote is the sandbox row's note for its detached-agent check (empty when
// clear), and whether it surfaces the row (contracts/jobs-ui.md).
func orphanNote(sb *pb.Sandbox) (string, bool) {
	check := sb.GetAgent().GetAgentCheck()
	switch check.GetState() {
	case pb.AgentCheck_STATE_UNKNOWN:
		return checkingRefusal + "…", false
	case pb.AgentCheck_STATE_DETACHED:
		return detachedRefusal + " — t to Replace or Wait", true
	case pb.AgentCheck_STATE_CHECK_FAILED:
		return "can't check for a detached agent: " + sanitizeLine(check.GetError(), 60), true
	}
	return "", false
}
