package ui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// subscribeCmd opens the live event stream of the active daemon (replaying
// undelivered notifications missed while disconnected — FR-026b).
func (m Model) subscribeCmd() tea.Cmd {
	return subscribeHostCmd(m.activeHost, m.daemon)
}

// subscribeHostCmd opens one host's event stream (feature 009, research R13:
// every connected host is subscribed, so remote sandbox changes, orchestrator
// changes and notifications arrive live).
func subscribeHostCmd(hostID string, d Daemon) tea.Cmd {
	return func() tea.Msg {
		stream, err := d.Subscribe(context.Background(), true)
		if err != nil {
			return eventErrMsg{hostID: hostID, err: err}
		}
		return subOpenedMsg{hostID: hostID, stream: stream}
	}
}

// subHostKey is the key a host's stream is stored under ("" means the active
// daemon, which has no entry id in single-host use).
func (m Model) subHostKey(hostID string) string {
	if hostID == "" {
		return m.activeHost
	}
	return hostID
}

// subscribeHostsCmd subscribes every connected host that has no subscription
// yet and forgets the subscriptions of hosts that are no longer connected, so a
// reconnect (daemon screen, startup sign-in) re-subscribes.
func (m *Model) subscribeHostsCmd(hosts []client.HostSandboxes) tea.Cmd {
	var cmds []tea.Cmd
	seen := map[string]bool{}
	for _, hs := range hosts {
		id := hs.Host.Entry.ID
		seen[id] = true
		if hs.Host.State != client.HostConnected || hs.Host.Conn == nil {
			if id != m.activeHost {
				delete(m.subs, id)
				delete(m.subscribed, id)
			}
			continue
		}
		if m.subscribed[id] {
			continue
		}
		m.subscribed[id] = true
		cmds = append(cmds, subscribeHostCmd(id, hs.Host.Conn))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// recvCmd blocks for the next event on the stream, tagging it with the host the
// stream belongs to.
func (m Model) recvCmd(stream client.EventStream) tea.Cmd {
	hostID := ""
	for id, s := range m.subs {
		if s == stream {
			hostID = id
			break
		}
	}
	return func() tea.Msg {
		ev, err := stream.Recv()
		if err != nil {
			return eventErrMsg{hostID: hostID, err: err}
		}
		return eventMsg{hostID: hostID, ev: ev}
	}
}

// rearmCmd re-arms the receiver of a host's stream (the active stream when the
// host is unknown, as in tests that set m.sub directly).
func (m Model) rearmCmd(hostID string) tea.Cmd {
	if s, ok := m.subs[m.subHostKey(hostID)]; ok && s != nil {
		return m.recvCmd(s)
	}
	if m.sub != nil {
		return m.recvCmd(m.sub)
	}
	return nil
}

// handleEvent processes one streamed event and re-arms the receiver of the
// stream it came from. hostID is the host entry id the event arrived from.
func (m Model) handleEvent(hostID string, ev *pb.Event) (tea.Model, tea.Cmd) {
	rearm := m.rearmCmd(hostID)
	var out tea.Model = m
	var cmd tea.Cmd
	switch e := ev.GetEvent().(type) {
	case *pb.Event_Notification:
		n := e.Notification
		m.inbox = append([]*pb.NotificationEvent{n}, m.inbox...) // newest first
		m.inboxHosts[n.GetId()] = m.subHostKey(hostID)
		m.unread++
		// OS desktop notification in addition to the in-TUI list (FR-026a).
		m.notifier.Notify("Switchboard: "+notifTitle(n.GetKind()), n.GetMessage())
		out = m
	case *pb.Event_SandboxChanged:
		// A live state/agent-status change (e.g. the agent started working, needs a
		// prompt, or went idle). Merge it in place and re-render the row so the
		// agent badge updates live — no network reload needed. A sandbox this
		// client has never listed (launched elsewhere, a reasoning sandbox) is
		// inserted under its host once it is past CREATING.
		if m.mergeSandboxUpdate(e.SandboxChanged) {
			m.refreshListItems()
		} else if !m.knowsSandbox(e.SandboxChanged.GetId()) && e.SandboxChanged.GetState() != pb.SandboxState_SANDBOX_STATE_CREATING && e.SandboxChanged.GetState() != pb.SandboxState_SANDBOX_STATE_DESTROYING {
			m.insertSandbox(e.SandboxChanged, m.subHostKey(hostID))
			m.rebuildTabs()
			m.refreshListItems()
		}
		// feature 007: an edit made elsewhere (another client, a completed add on
		// this one) reaches an open sources overlay without a manual reload.
		m.syncSourcesOverlay(e.SandboxChanged)
		// feature 008 (FR-102): an attach made elsewhere reaches an open MCP overlay.
		m.syncSandboxMcpOverlay(e.SandboxChanged)
		out = m
	case *pb.Event_Removed:
		if m.removeSandbox(e.Removed.GetSandboxId()) {
			m.refreshListItems()
		}
		out = m
	case *pb.Event_EscapeHatchRun:
		out, cmd = m.handleEscapeHatchRun(e.EscapeHatchRun)
	case *pb.Event_ServiceInstance:
		out, cmd = m.handleServiceInstance(e.ServiceInstance)
	case *pb.Event_SecretsChanged_:
		// feature 008 (FR-104): a change made on the host or by another client
		// reaches an open secrets view.
		out, cmd = m.syncSecretsView(e.SecretsChanged.GetHostId())
	case *pb.Event_McpGatewayChanged_:
		// feature 008: a daemon's registrations/marks/mode changed (here or in
		// another client). An open gateway view for that host re-lists.
		if m.screen == screenMcp && m.mcpView.host == e.McpGatewayChanged.GetHostId() {
			cmd = m.mcpLoadCmd(m.mcpView.daemon, m.mcpView.host)
		}
		out = m
	case *pb.Event_OrchestratorChanged_:
		// feature 009 (R14): an orchestrator on that host changed; the screen and
		// any open detail re-fetch (filled by the orchestrator views).
		out, cmd = m.syncOrchestratorChanged(m.subHostKey(hostID), e.OrchestratorChanged.GetOrchestratorId())
	}
	return out, tea.Batch(cmd, rearm)
}

// knowsSandbox reports whether a sandbox id is in any in-memory list.
func (m Model) knowsSandbox(id string) bool {
	for _, sb := range m.sandboxes {
		if sb.GetId() == id {
			return true
		}
	}
	for _, hs := range m.hostAgg {
		for _, sb := range hs.Sandboxes {
			if sb.GetId() == id {
				return true
			}
		}
	}
	return false
}

// handleEscapeHatchRun tracks a run's live state for the list badge and, on a
// pending-approval run, raises the approval modal if one is not already up (feature
// 005). Re-arms the event receiver on every path.
func (m Model) handleEscapeHatchRun(run *pb.EscapeHatchRun) (tea.Model, tea.Cmd) {
	if m.pendingRuns == nil {
		m.pendingRuns = map[string]*pb.EscapeHatchRun{}
	}
	// handleEvent re-arms the originating stream; nothing to re-arm here.
	rearm := func() tea.Cmd { return nil }
	if run.GetStatus() == pb.EscapeHatchRunStatus_ESCAPE_HATCH_RUN_STATUS_PENDING_APPROVAL {
		m.pendingRuns[run.GetId()] = run
		m.refreshListItems() // show the awaiting-approval badge
		// Only auto-open the modal when the user is on the list and not already
		// answering another prompt; otherwise the badge + notification suffice.
		if m.screen == screenList {
			out, _ := m.enterApproval(run)
			return out, rearm()
		}
		return m, rearm()
	}
	// A resolved run is no longer pending.
	delete(m.pendingRuns, run.GetId())
	m.refreshListItems()
	return m, rearm()
}

func notifTitle(k pb.NotificationKind) string {
	switch k {
	case pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING:
		return "needs prompting"
	case pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_NEEDS_APPROVAL:
		return "escape-hatch approval"
	case pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_RUN_COMPLETE:
		return "escape-hatch run"
	case pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED:
		// feature 006: the only service transition that notifies, because it is the
		// only one the developer did not just perform themselves (FR-052).
		return "service failed"
	// feature 009 (FR-133): raised by an orchestrator's daemon against the worker.
	case pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED:
		return "run failed"
	case pb.NotificationKind_NOTIFICATION_KIND_RUN_AWAITING_REVIEW:
		return "run awaiting review"
	case pb.NotificationKind_NOTIFICATION_KIND_RUN_NEEDS_INPUT:
		return "worker needs input"
	}
	return "task complete"
}

// enterNotifications shows the in-TUI notification list and marks it read.
func (m Model) enterNotifications() (tea.Model, tea.Cmd) {
	m.screen = screenNotifications
	m.unread = 0
	l := newItemList("Notifications", "notification", "notifications", m.bodyWidth(), m.bodyHeight())
	items := make([]list.Item, 0, len(m.inbox))
	for _, n := range m.inbox {
		items = append(items, listItem{
			id:      n.GetSandboxId(),
			title:   notifIcon(n.GetKind()) + " " + notifTitle(n.GetKind()) + dimStyle.Render("  "+short(n.GetSandboxId())),
			desc:    n.GetMessage(),
			filter:  n.GetMessage(),
			payload: n,
		})
	}
	l.SetItems(items)
	m.notifyList = l
	// Ack everything currently shown so it is not replayed next reconnect.
	return m, m.ackVisibleCmd()
}

func notifIcon(k pb.NotificationKind) string {
	switch k {
	case pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING,
		pb.NotificationKind_NOTIFICATION_KIND_ESCAPE_HATCH_NEEDS_APPROVAL:
		return lipgloss.NewStyle().Foreground(colWarn).Render("◆")
	case pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED:
		return lipgloss.NewStyle().Foreground(colError).Render("✗")
	case pb.NotificationKind_NOTIFICATION_KIND_RUN_AWAITING_REVIEW:
		return lipgloss.NewStyle().Foreground(colWarn).Render("⚑")
	case pb.NotificationKind_NOTIFICATION_KIND_RUN_NEEDS_INPUT:
		return lipgloss.NewStyle().Foreground(colWarn).Render("?")
	}
	return lipgloss.NewStyle().Foreground(colRunning).Render("✓")
}

// ackVisibleCmd acks every shown notification on the daemon it came from
// (feature 009: notifications arrive from every connected host).
func (m Model) ackVisibleCmd() tea.Cmd {
	if len(m.inbox) == 0 {
		return nil
	}
	byHost := map[string][]string{}
	var order []string
	for _, n := range m.inbox {
		host := m.inboxHosts[n.GetId()]
		if _, ok := byHost[host]; !ok {
			order = append(order, host)
		}
		byHost[host] = append(byHost[host], n.GetId())
	}
	type target struct {
		d   Daemon
		ids []string
	}
	targets := make([]target, 0, len(order))
	total := 0
	for _, host := range order {
		targets = append(targets, target{d: m.daemonForHost(host), ids: byHost[host]})
		total += len(byHost[host])
	}
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		for _, t := range targets {
			_ = t.d.AckNotifications(ctx, t.ids)
		}
		return statusMsg(fmt.Sprintf("acknowledged %d notifications", total))
	}
}

func (m Model) notificationsHelp() helpBindings {
	return helpBindings{hkey("enter", "go to sandbox"), m.keys.Up, m.keys.Down, m.keys.Back}
}

func (m Model) updateNotificationsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.notifyList.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.notifyList, cmd = m.notifyList.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenList
		return m, nil
	case "enter":
		// Navigate to the notification's sandbox (FR-026).
		if it, ok := m.notifyList.SelectedItem().(listItem); ok {
			if n, ok := it.payload.(*pb.NotificationEvent); ok {
				selectByID(&m.list, n.GetSandboxId())
			}
		}
		m.screen = screenList
		return m, nil
	}
	var cmd tea.Cmd
	m.notifyList, cmd = m.notifyList.Update(msg)
	return m, cmd
}

func (m Model) viewNotifications() string {
	return m.notifyList.View()
}
