package ui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// tabKind identifies the three flavors of group tab shown above the list.
type tabKind int

const (
	tabAll   tabKind = iota // every sandbox across every connected daemon
	tabHost                 // one connected remote daemon
	tabGroup                // a user-defined group
)

// sandboxTab is one entry in the group tab bar.
type sandboxTab struct {
	kind    tabKind
	label   string
	hostID  string // for tabHost
	groupID string // for tabGroup
}

// sandboxRow is a sandbox paired with the daemon it lives on.
type sandboxRow struct {
	host     string // host id (used to route actions)
	hostName string // host display name (used in the UI)
	sb       *pb.Sandbox
}

// newSandboxList builds the primary sandbox list.
func newSandboxList(w, h int) list.Model {
	return newItemList("Sandboxes", "sandbox", "sandboxes", w, h)
}

// rebuildTabs recomputes the tab bar: "All Sandboxes", one tab per connected
// remote daemon (only when more than one host is connected — otherwise the lone
// host is identical to "All"), and one tab per user-defined group.
func (m *Model) rebuildTabs() {
	tabs := []sandboxTab{{kind: tabAll, label: "All Sandboxes"}}
	if connected := m.connectedHosts(); len(connected) > 1 {
		for _, hs := range connected {
			tabs = append(tabs, sandboxTab{kind: tabHost, label: hs.Host.Entry.DisplayName, hostID: hs.Host.Entry.ID})
		}
	}
	for _, g := range m.userGroups {
		tabs = append(tabs, sandboxTab{kind: tabGroup, label: g.Name, groupID: g.ID})
	}
	m.tabs = tabs
	if m.tabIndex >= len(tabs) {
		m.tabIndex = len(tabs) - 1
	}
	if m.tabIndex < 0 {
		m.tabIndex = 0
	}
}

func (m Model) connectedHosts() []client.HostSandboxes {
	var out []client.HostSandboxes
	for _, hs := range m.hostAgg {
		if hs.Host.State == client.HostConnected {
			out = append(out, hs)
		}
	}
	return out
}

// allRows flattens every known sandbox with its owning host. It prefers the
// cross-host aggregate; without a manager it falls back to the active daemon's
// list so the single-daemon case keeps working.
func (m Model) allRows() []sandboxRow {
	if len(m.hostAgg) > 0 {
		var rows []sandboxRow
		for _, hs := range m.hostAgg {
			for _, sb := range hs.Sandboxes {
				rows = append(rows, sandboxRow{host: hs.Host.Entry.ID, hostName: hs.Host.Entry.DisplayName, sb: sb})
			}
		}
		return rows
	}
	rows := make([]sandboxRow, 0, len(m.sandboxes))
	for _, sb := range m.sandboxes {
		rows = append(rows, sandboxRow{host: m.activeHost, hostName: m.activeHost, sb: sb})
	}
	return rows
}

// currentTabRows filters allRows down to the selected tab.
func (m Model) currentTabRows() []sandboxRow {
	all := m.allRows()
	if len(m.tabs) == 0 {
		return all
	}
	switch tab := m.tabs[m.tabIndex]; tab.kind {
	case tabHost:
		var r []sandboxRow
		for _, row := range all {
			if row.host == tab.hostID {
				r = append(r, row)
			}
		}
		return r
	case tabGroup:
		members := map[string]bool{}
		for _, g := range m.userGroups {
			if g.ID == tab.groupID {
				for _, mm := range g.Members {
					members[mm.HostID+"\x00"+mm.SandboxID] = true
				}
			}
		}
		var r []sandboxRow
		for _, row := range all {
			if members[row.host+"\x00"+row.sb.GetId()] {
				r = append(r, row)
			}
		}
		return r
	default:
		return all
	}
}

// refreshListItems repopulates the list from the current tab.
func (m *Model) refreshListItems() {
	rows := m.currentTabRows()
	showHost := len(m.connectedHosts()) > 1 && (len(m.tabs) == 0 || m.tabs[m.tabIndex].kind != tabHost)
	items := make([]list.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, m.sandboxItem(row, showHost))
	}
	m.list.SetItems(items)
}

// startBusy marks a sandbox's row as awaiting a daemon response (so it renders a
// spinner) and returns the updated model to pair with the action command.
func (m Model) startBusy(id, verb string) Model {
	m.busy[id] = verb
	m.refreshListItems()
	return m
}

// switchTab moves the active tab by delta (wrapping) and reloads the list.
func (m Model) switchTab(delta int) (tea.Model, tea.Cmd) {
	if len(m.tabs) <= 1 {
		return m, nil
	}
	m.tabIndex = (m.tabIndex + delta + len(m.tabs)) % len(m.tabs)
	m.refreshListItems()
	m.list.Select(0)
	return m, nil
}

func (m Model) sandboxItem(row sandboxRow, showHost bool) listItem {
	desc := sandboxDesc(row.sb)
	if showHost && row.hostName != "" {
		desc += dimStyle.Render("  ·  @" + row.hostName)
	}
	title := sandboxTitle(row.sb)
	// A daemon action is in flight for this sandbox: show a spinner + verb in
	// place of the state badge until the response lands.
	if verb, ok := m.busy[row.sb.GetId()]; ok {
		title = m.spinner.View() + " " + selectedStyle.Render(pad(verb+"…", 9)) + " " + row.sb.GetDisplayName()
	}
	return listItem{
		id:      row.sb.GetId(),
		host:    row.host,
		title:   title,
		desc:    desc,
		filter:  row.sb.GetDisplayName(),
		payload: row.sb,
	}
}

func sandboxTitle(sb *pb.Sandbox) string {
	return stateBadge(sb.GetState()) + "  " + sb.GetDisplayName()
}

func sandboxDesc(sb *pb.Sandbox) string {
	var srcs []string
	for _, s := range sb.GetSources() {
		srcs = append(srcs, filepath.Base(s.GetPath()))
	}
	sources := strings.Join(srcs, ", ")
	if sources == "" {
		sources = "no sources"
	}
	return lipgloss.NewStyle().Foreground(colMuted).Render(swb.SeedingModeString(sb.GetSeedingMode())+" · ") + sources
}

func (m Model) current() *pb.Sandbox {
	if it, ok := m.list.SelectedItem().(listItem); ok {
		if sb, ok := it.payload.(*pb.Sandbox); ok {
			return sb
		}
	}
	return nil
}

func (m Model) updateListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While the fuzzy filter input is active, all keys belong to the list.
	if m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "left":
		return m.switchTab(-1)
	case "right":
		return m.switchTab(1)
	}

	switch {
	case keyIs(msg, m.keys.Quit):
		m.quitting = true
		return m, tea.Quit
	case keyIs(msg, m.keys.Refresh):
		m.listLoading = true
		return m, m.reloadCmd()
	case keyIs(msg, m.keys.Launch):
		return m.enterLaunch()
	case keyIs(msg, m.keys.NewConfig):
		return m.enterConfigEditor()
	case keyIs(msg, m.keys.FromConfig):
		return m.enterConfigPicker()
	case keyIs(msg, m.keys.Hosts):
		return m.enterHosts()
	case keyIs(msg, m.keys.Groups):
		return m.enterGroups()
	case keyIs(msg, m.keys.Inbox):
		return m.enterNotifications()
	case keyIs(msg, m.keys.Update):
		return m.enterUpdate()
	case keyIs(msg, m.keys.Terminal):
		if sb := m.current(); sb != nil {
			return m.openAgentTerminal(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.Popout):
		if sb := m.current(); sb != nil {
			return m.openPopoutTerminal(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.VSCode):
		if sb := m.current(); sb != nil {
			return m.startBusy(sb.GetId(), "opening"), m.openVSCodeCmd(m.currentHostID(), sb.GetId())
		}
		return m, nil
	case keyIs(msg, m.keys.StartStop):
		if sb := m.current(); sb != nil {
			d := m.daemonForHost(m.currentHostID())
			// One key toggles by state: stop a running sandbox, start any other.
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
				return m.startBusy(sb.GetId(), "stopping"), m.stopCmd(d, sb.GetId())
			}
			return m.startBusy(sb.GetId(), "starting"), m.restartCmd(d, sb.GetId())
		}
		return m, nil
	case keyIs(msg, m.keys.Destroy):
		if sb := m.current(); sb != nil {
			return m.startBusy(sb.GetId(), "destroying"), m.destroyCmd(m.daemonForHost(m.currentHostID()), sb.GetId())
		}
		return m, nil
	case keyIs(msg, m.keys.Rename):
		if sb := m.current(); sb != nil {
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
				m.status = "stop the sandbox before renaming it"
				return m, nil
			}
			return m.enterRename(sb, m.currentHostID())
		}
		return m, nil
	}

	// Everything else (navigation, filter start, pagination) goes to the list.
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// tabBar renders the group tabs with the active one highlighted.
func (m Model) tabBar() string {
	if len(m.tabs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.tabs))
	for i, t := range m.tabs {
		style := inactiveTabStyle
		if i == m.tabIndex {
			style = activeTabStyle
		}
		parts = append(parts, style.Render(t.label))
	}
	row := strings.Join(parts, tabGapStyle.Render(" "))
	if len(m.tabs) > 1 {
		row += dimStyle.Render("   ←/→ switch group")
	}
	return row
}

func (m Model) viewList() string {
	header := m.tabBar()
	if m.listLoading {
		indicator := m.spinner.View() + dimStyle.Render(" refreshing…")
		if header == "" {
			header = indicator
		} else {
			header += "   " + indicator
		}
	}
	parts := make([]string, 0, 4)
	if m.updateBanner != "" {
		parts = append(parts, updateBannerStyle.Render(m.updateBanner))
	}
	parts = append(parts, header, "", m.list.View())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
