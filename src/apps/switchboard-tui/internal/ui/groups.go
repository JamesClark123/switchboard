package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// groupsState backs the group-management screen (US5: FR-018/019).
type groupsState struct {
	list      list.Model
	rows      []store.Group
	adding    bool
	input     textinput.Model
	targetID  string // the list-selected sandbox to add/remove
	targetSbx string
	status    string
	ready     bool
}

// enterGroups opens the group manager, capturing the list-selected sandbox as
// the membership target for space-toggle.
func (m Model) enterGroups() (tea.Model, tea.Cmd) {
	if m.groupStore == nil {
		m.status = "groups not available"
		return m, nil
	}
	m.screen = screenGroups
	m.groups = groupsState{
		list:  newItemList("Groups", "group", "groups", m.bodyWidth(), m.bodyHeight()),
		ready: true,
	}
	if sb := m.current(); sb != nil {
		m.groups.targetSbx = sb.GetId()
		m.groups.targetID = m.activeHost
	}
	return m, m.groupsCmd()
}

func (m Model) groupsCmd() tea.Cmd {
	gs := m.groupStore
	return func() tea.Msg {
		list, err := gs.List()
		if err != nil {
			return errMsg{err}
		}
		return groupsMsg(list)
	}
}

func (m Model) applyGroups(msg groupsMsg) (tea.Model, tea.Cmd) {
	m.groups.rows = []store.Group(msg)

	// Keep the sandbox-list group tabs in sync so a group added/deleted here
	// shows up immediately (no TUI restart needed).
	m.userGroups = []store.Group(msg)
	m.rebuildTabs()
	m.refreshListItems()

	if !m.groups.ready {
		return m, nil
	}
	items := make([]list.Item, 0, len(m.groups.rows))
	for _, g := range m.groups.rows {
		mark := "  "
		for _, mm := range g.Members {
			if mm.SandboxID == m.groups.targetSbx && m.groups.targetSbx != "" {
				mark = statusOKStyle.Render("✓ ")
			}
		}
		items = append(items, listItem{
			id:      g.ID,
			title:   mark + g.Name,
			desc:    plural(len(g.Members), "member", "members"),
			filter:  g.Name,
			payload: g,
		})
	}
	m.groups.list.SetItems(items)
	return m, nil
}

func (m Model) groupsHelp() helpBindings {
	if m.groups.adding {
		return helpBindings{m.keys.Confirm, m.keys.Cancel}
	}
	return helpBindings{m.keys.Toggle, hkey("enter", "go to"), m.keys.Add, m.keys.Delete, m.keys.Back}
}

func (m Model) updateGroupsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.groups.adding {
		return m.updateGroupAddKey(msg)
	}
	if m.groups.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.groups.list, cmd = m.groups.list.Update(msg)
		return m, cmd
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenList
		return m, nil
	case "a":
		m.groups.adding = true
		ti := textinput.New()
		ti.Prompt = "› "
		ti.Placeholder = "group name"
		ti.PromptStyle = selectedStyle
		m.groups.input = ti
		return m, m.groups.input.Focus()
	case "d":
		if g := m.groupsCurrent(); g != nil {
			_ = m.groupStore.Delete(g.ID)
			return m, m.groupsCmd()
		}
		return m, nil
	case " ":
		// Toggle the list-selected sandbox's membership in the cursored group.
		return m.toggleMembership()
	case "enter":
		// Navigate to the group's first member sandbox (FR-019).
		if g := m.groupsCurrent(); g != nil && len(g.Members) > 0 {
			selectByID(&m.list, g.Members[0].SandboxID)
		}
		m.screen = screenList
		return m, nil
	}
	var cmd tea.Cmd
	m.groups.list, cmd = m.groups.list.Update(msg)
	return m, cmd
}

func (m Model) toggleMembership() (tea.Model, tea.Cmd) {
	g := m.groupsCurrent()
	if g == nil {
		return m, nil
	}
	if m.groups.targetSbx == "" {
		m.groups.status = "no sandbox selected to assign"
		return m, nil
	}
	member := store.GroupMember{HostID: m.groups.targetID, SandboxID: m.groups.targetSbx}
	present := false
	for _, mm := range g.Members {
		if mm == member {
			present = true
			break
		}
	}
	if present {
		_ = m.groupStore.RemoveMember(g.ID, member)
		m.groups.status = "removed from " + g.Name
	} else {
		_ = m.groupStore.AddMember(g.ID, member)
		m.groups.status = "added to " + g.Name
	}
	return m, m.groupsCmd()
}

func (m Model) updateGroupAddKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.groups.adding = false
		m.groups.input.Blur()
		return m, nil
	case tea.KeyEnter:
		name := strings.TrimSpace(m.groups.input.Value())
		m.groups.adding = false
		m.groups.input.Blur()
		if name == "" {
			return m, nil
		}
		if _, err := m.groupStore.Save(store.Group{Name: name}); err != nil {
			m.groups.status = "add failed: " + err.Error()
			return m, nil
		}
		return m, m.groupsCmd()
	}
	var cmd tea.Cmd
	m.groups.input, cmd = m.groups.input.Update(msg)
	return m, cmd
}

func (m Model) groupsCurrent() *store.Group {
	if it, ok := m.groups.list.SelectedItem().(listItem); ok {
		if g, ok := it.payload.(store.Group); ok {
			return &g
		}
	}
	return nil
}

func (m Model) viewGroups() string {
	if m.groups.adding {
		return lipgloss.JoinVertical(lipgloss.Left,
			sectionStyle.Render("New group"),
			"",
			panelStyle.Width(m.bodyWidth()-2).Render(m.groups.input.View()),
		)
	}
	head := ""
	if m.groups.targetSbx != "" {
		head = dimStyle.Render("assigning sandbox "+short(m.groups.targetSbx)+" — space toggles membership") + "\n"
	}
	out := m.groups.list.View()
	if head != "" {
		out = lipgloss.JoinVertical(lipgloss.Left, head, out)
	}
	if m.groups.status != "" {
		out = lipgloss.JoinVertical(lipgloss.Left, out, dimStyle.Render(m.groups.status))
	}
	return out
}

// --- Open in VS Code (FR-027) ---

func (m Model) openVSCodeCmd(hostID, sandboxID string) tea.Cmd {
	if m.opener == nil {
		return func() tea.Msg { return statusMsg("VS Code opener not configured") }
	}
	d := m.daemonForHost(hostID)
	opener := m.opener
	sshTarget := m.sshTargetForHost(hostID)
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		tgt, err := d.VSCodeTarget(ctx, sandboxID)
		if err != nil {
			return errMsg{err}
		}
		if err := opener.Open(tgt, sshTarget); err != nil {
			return errMsg{err}
		}
		return statusMsg("opened " + short(sandboxID) + " in VS Code")
	}
}

// sshTargetForHost returns a host's SSH target (empty for local), so a remote
// sandbox opens via DOCKER_HOST=ssh://… (FR-027, research R3). An empty hostID
// falls back to the active host.
func (m Model) sshTargetForHost(hostID string) string {
	if hostID == "" {
		hostID = m.activeHost
	}
	if m.hostStore == nil || hostID == "" {
		return ""
	}
	h, err := m.hostStore.Get(hostID)
	if err != nil || h.Kind != "ssh" {
		return ""
	}
	return h.SSHTarget
}
