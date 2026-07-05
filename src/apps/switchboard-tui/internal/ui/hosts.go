package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// hostsState backs the multi-host screen (US3): a host-grouped list with
// per-host connection state (FR-020/021) and an inline "add SSH host" input.
type hostsState struct {
	list   list.Model
	rows   []client.HostSandboxes
	adding bool
	input  textinput.Model
	status string
	ready  bool
}

// enterHosts loads known hosts into the manager and shows the hosts screen.
func (m Model) enterHosts() (tea.Model, tea.Cmd) {
	if m.manager == nil || m.hostStore == nil {
		m.status = "multi-host support not available"
		return m, nil
	}
	m.screen = screenHosts
	m.hosts = hostsState{
		list:  newItemList("Hosts", "host", "hosts", m.bodyWidth(), m.bodyHeight()),
		ready: true,
	}
	// Sync known hosts from the store into the manager.
	if known, err := m.hostStore.List(); err == nil {
		for _, h := range known {
			m.manager.Upsert(toEntry(h))
		}
	}
	return m, m.hostsCmd()
}

func toEntry(h store.KnownHost) client.HostEntry {
	return client.HostEntry{
		ID:          h.ID,
		DisplayName: h.DisplayName,
		Kind:        h.Kind,
		SocketPath:  h.SocketPath,
		SSHTarget:   h.SSHTarget,
		SSHOptions:  h.SSHOptions,
	}
}

// hostsCmd snapshots the host-grouped aggregate (FR-020).
func (m Model) hostsCmd() tea.Cmd {
	mgr := m.manager
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		return hostsMsg(mgr.AggregateSandboxes(ctx))
	}
}

// connectHostCmd connects to a host then re-snapshots.
func (m Model) connectHostCmd(id string) tea.Cmd {
	mgr := m.manager
	hs := m.hostStore
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		if err := mgr.Connect(ctx, id); err == nil && hs != nil {
			_ = hs.Touch(id, time.Now())
		}
		return hostsMsg(mgr.AggregateSandboxes(ctx))
	}
}

func (m Model) applyHosts(msg hostsMsg) (tea.Model, tea.Cmd) {
	m.hosts.rows = []client.HostSandboxes(msg)
	if !m.hosts.ready {
		return m, nil
	}
	items := make([]list.Item, 0, len(m.hosts.rows))
	for _, row := range m.hosts.rows {
		items = append(items, listItem{
			id:      row.Host.Entry.ID,
			title:   hostTitle(row, m.activeHost),
			desc:    hostDesc(row),
			filter:  row.Host.Entry.DisplayName,
			payload: row,
		})
	}
	m.hosts.list.SetItems(items)
	return m, nil
}

func hostTitle(row client.HostSandboxes, activeHost string) string {
	name := row.Host.Entry.DisplayName
	if row.Host.Entry.ID == activeHost {
		name = selectedStyle.Render("★ " + name)
	}
	badge := hostStateBadge(row.Host.State)
	return badge + "  " + name
}

func hostStateBadge(st client.HostState) string {
	label := strings.ToUpper(st.String())
	fg := colMuted
	if st == client.HostConnected {
		fg = colRunning
	}
	return lipgloss.NewStyle().Foreground(fg).Bold(true).Render(pad(label, 12))
}

func hostDesc(row client.HostSandboxes) string {
	summary := entrySummary(row.Host.Entry)
	switch {
	case row.Host.State == client.HostConnected:
		return summary + " · " + plural(len(row.Sandboxes), "sandbox", "sandboxes")
	case row.Host.Err != nil:
		return summary + " · " + statusErrStyle.Render("error: "+row.Host.Err.Error())
	default:
		return summary
	}
}

func (m Model) hostsHelp() helpBindings {
	if m.hosts.adding {
		return helpBindings{m.keys.Confirm, m.keys.Cancel}
	}
	return helpBindings{m.keys.Connect, m.keys.Disconnect, m.keys.Enter, m.keys.Add, m.keys.Delete, m.keys.Back}
}

func (m Model) updateHostsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.hosts.adding {
		return m.updateHostAddKey(msg)
	}
	if m.hosts.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.hosts.list, cmd = m.hosts.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenList
		return m, nil
	case "c":
		if row := m.hostsCurrent(); row != nil {
			return m, m.connectHostCmd(row.Host.Entry.ID)
		}
		return m, nil
	case "x":
		if row := m.hostsCurrent(); row != nil {
			m.manager.Disconnect(row.Host.Entry.ID)
			return m, m.hostsCmd()
		}
		return m, nil
	case "d":
		if row := m.hostsCurrent(); row != nil {
			id := row.Host.Entry.ID
			m.manager.Remove(id)
			if m.hostStore != nil {
				_ = m.hostStore.Delete(id)
			}
			return m, m.hostsCmd()
		}
		return m, nil
	case "a":
		m.hosts.adding = true
		ti := textinput.New()
		ti.Prompt = "› "
		ti.Placeholder = "user@host[:port]"
		ti.PromptStyle = selectedStyle
		m.hosts.input = ti
		return m, m.hosts.input.Focus()
	case "enter":
		return m.activateHost()
	}
	var cmd tea.Cmd
	m.hosts.list, cmd = m.hosts.list.Update(msg)
	return m, cmd
}

// activateHost makes the selected (connected) host the active one driving the
// sandbox list (target-host selection, FR-012).
func (m Model) activateHost() (tea.Model, tea.Cmd) {
	row := m.hostsCurrent()
	if row == nil {
		return m, nil
	}
	hc, ok := m.manager.Get(row.Host.Entry.ID)
	if !ok || hc.State != client.HostConnected || hc.Conn == nil {
		m.hosts.status = "connect the host first (press c)"
		return m, nil
	}
	m.activeHost = hc.Entry.ID
	m.daemon = hc.Conn
	m.screen = screenList
	m.status = "active host: " + hc.Entry.DisplayName
	return m, m.reloadCmd()
}

func (m Model) updateHostAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.hosts.adding = false
		m.hosts.input.Blur()
		return m, nil
	case tea.KeyEnter:
		target := strings.TrimSpace(m.hosts.input.Value())
		m.hosts.adding = false
		m.hosts.input.Blur()
		if target == "" {
			return m, nil
		}
		host, err := m.hostStore.Save(store.KnownHost{Kind: "ssh", DisplayName: target, SSHTarget: target})
		if err != nil {
			m.hosts.status = "add failed: " + err.Error()
			return m, nil
		}
		m.manager.Upsert(toEntry(*host))
		return m, m.hostsCmd()
	}
	var cmd tea.Cmd
	m.hosts.input, cmd = m.hosts.input.Update(msg)
	return m, cmd
}

func (m Model) hostsCurrent() *client.HostSandboxes {
	if it, ok := m.hosts.list.SelectedItem().(listItem); ok {
		if row, ok := it.payload.(client.HostSandboxes); ok {
			return &row
		}
	}
	return nil
}

func (m Model) viewHosts() string {
	if m.hosts.adding {
		return lipgloss.JoinVertical(lipgloss.Left,
			sectionStyle.Render("Add SSH host"),
			"",
			panelStyle.Width(m.bodyWidth()-2).Render(m.hosts.input.View()),
		)
	}
	out := m.hosts.list.View()
	if m.hosts.status != "" {
		out = lipgloss.JoinVertical(lipgloss.Left, out, dimStyle.Render(m.hosts.status))
	}
	return out
}

func entrySummary(e client.HostEntry) string {
	if e.Kind == "ssh" {
		return "ssh " + e.SSHTarget
	}
	return "local " + e.SocketPath
}
