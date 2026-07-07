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

// subscribeCmd opens the live event stream (replaying undelivered notifications
// missed while disconnected — FR-026b).
func (m Model) subscribeCmd() tea.Cmd {
	d := m.daemon
	return func() tea.Msg {
		stream, err := d.Subscribe(context.Background(), true)
		if err != nil {
			return eventErrMsg{err}
		}
		return subOpenedMsg{stream: stream}
	}
}

// recvCmd blocks for the next event on the stream.
func (m Model) recvCmd(stream client.EventStream) tea.Cmd {
	return func() tea.Msg {
		ev, err := stream.Recv()
		if err != nil {
			return eventErrMsg{err}
		}
		return eventMsg{ev: ev}
	}
}

// handleEvent processes one streamed event and re-arms the receiver.
func (m Model) handleEvent(ev *pb.Event) (tea.Model, tea.Cmd) {
	switch e := ev.GetEvent().(type) {
	case *pb.Event_Notification:
		n := e.Notification
		m.inbox = append([]*pb.NotificationEvent{n}, m.inbox...) // newest first
		m.unread++
		// OS desktop notification in addition to the in-TUI list (FR-026a).
		m.notifier.Notify("Switchboard: "+notifTitle(n.GetKind()), n.GetMessage())
	case *pb.Event_SandboxChanged:
		// A live state/agent-status change (e.g. the agent started working, needs a
		// prompt, or went idle). Merge it in place and re-render the row so the
		// agent badge updates live — no network reload needed.
		if m.mergeSandboxUpdate(e.SandboxChanged) {
			m.refreshListItems()
		}
	case *pb.Event_Removed:
		if m.removeSandbox(e.Removed.GetSandboxId()) {
			m.refreshListItems()
		}
	}
	var cmd tea.Cmd
	if m.sub != nil {
		cmd = m.recvCmd(m.sub)
	}
	return m, cmd
}

func notifTitle(k pb.NotificationKind) string {
	if k == pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING {
		return "needs prompting"
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
	if k == pb.NotificationKind_NOTIFICATION_KIND_NEEDS_PROMPTING {
		return lipgloss.NewStyle().Foreground(colWarn).Render("◆")
	}
	return lipgloss.NewStyle().Foreground(colRunning).Render("✓")
}

func (m Model) ackVisibleCmd() tea.Cmd {
	if len(m.inbox) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m.inbox))
	for _, n := range m.inbox {
		ids = append(ids, n.GetId())
	}
	d := m.daemon
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		_ = d.AckNotifications(ctx, ids)
		return statusMsg(fmt.Sprintf("acknowledged %d notifications", len(ids)))
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
