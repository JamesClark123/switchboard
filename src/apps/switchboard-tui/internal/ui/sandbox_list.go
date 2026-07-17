package ui

import (
	"path/filepath"
	"sort"
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

// hostDisplayName resolves a host id to its friendly name via the manager,
// falling back to the id itself when it is unknown or multi-host is disabled.
func (m Model) hostDisplayName(host string) string {
	if m.manager != nil && host != "" {
		if hc, ok := m.manager.Get(host); ok && hc.Entry.DisplayName != "" {
			return hc.Entry.DisplayName
		}
	}
	return host
}

// allRows flattens every known sandbox with its owning host. It prefers the
// cross-host aggregate; without a manager it falls back to the active daemon's
// list so the single-daemon case keeps working.
func (m Model) allRows() []sandboxRow {
	var rows []sandboxRow
	if len(m.hostAgg) > 0 {
		for _, hs := range m.hostAgg {
			for _, sb := range hs.Sandboxes {
				rows = append(rows, sandboxRow{host: hs.Host.Entry.ID, hostName: hs.Host.Entry.DisplayName, sb: sb})
			}
		}
	} else {
		for _, sb := range m.sandboxes {
			rows = append(rows, sandboxRow{host: m.activeHost, hostName: m.activeHost, sb: sb})
		}
	}
	// Optimistic, still-creating launches, appended after the daemon's real rows.
	// Each carries the host it was launched on (which may differ from the active
	// host) so it lands under the correct host tab.
	for _, lf := range m.launchingOrdered() {
		host := lf.host
		if host == "" {
			host = m.activeHost
		}
		rows = append(rows, sandboxRow{host: host, hostName: m.hostDisplayName(host), sb: lf.sb})
	}
	return rows
}

// launchingOrdered returns the in-flight launch placeholders in a stable order
// (by launch sequence), so concurrent "creating…" rows don't reshuffle each tick.
func (m Model) launchingOrdered() []*launchInFlight {
	out := make([]*launchInFlight, 0, len(m.launching))
	for _, lf := range m.launching {
		out = append(out, lf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// insertSandbox appends a freshly-launched sandbox to the in-memory lists if it is
// not already present, so its row survives the frames between a launch completing
// and the follow-up reload landing (avoiding a one-frame blink). A reload replaces
// these slices wholesale, so the insert is transient and never duplicates.
func (m *Model) insertSandbox(sb *pb.Sandbox, host string) {
	if sb.GetId() == "" {
		return
	}
	if host == "" {
		host = m.activeHost
	}
	present := func(list []*pb.Sandbox) bool {
		for _, ex := range list {
			if ex.GetId() == sb.GetId() {
				return true
			}
		}
		return false
	}
	// With a cross-host aggregate, attribute the sandbox to its owning host so it
	// renders under the right tab; mirror into m.sandboxes only when it belongs to
	// the active daemon (whose list that slice represents).
	for i := range m.hostAgg {
		if m.hostAgg[i].Host.Entry.ID != host {
			continue
		}
		if !present(m.hostAgg[i].Sandboxes) {
			m.hostAgg[i].Sandboxes = append(m.hostAgg[i].Sandboxes, sb)
		}
		if host == m.activeHost && !present(m.sandboxes) {
			m.sandboxes = append(m.sandboxes, sb)
		}
		return
	}
	// No aggregate (single-daemon view): the sandbox belongs to the active list.
	if !present(m.sandboxes) {
		m.sandboxes = append(m.sandboxes, sb)
	}
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
	// A still-creating optimistic launch: show a spinner, the "creating" verb, and
	// the live copy/boot progress in place of the usual state badge.
	if lf, ok := m.launching[row.sb.GetId()]; ok {
		name := row.sb.GetDisplayName()
		if name == "" {
			name = "new sandbox"
		}
		title := m.spinner.View() + " " + selectedStyle.Render(pad("creating", 9)) + " " + name
		if lf.progress != "" {
			title += "  " + dimStyle.Render(lf.progress)
		}
		return listItem{id: row.sb.GetId(), host: row.host, title: title, desc: desc, filter: name, payload: row.sb}
	}
	title := sandboxTitle(row.sb)
	// A daemon action is in flight for this sandbox: show a spinner + verb in
	// place of the state badge until the response lands. Otherwise, surface the
	// coding agent's live status (working / needs input / idle) so the user can
	// tell at a glance what each sandbox's agent is doing.
	if verb, ok := m.busy[row.sb.GetId()]; ok {
		title = m.spinner.View() + " " + selectedStyle.Render(pad(verb+"…", 9)) + " " + row.sb.GetDisplayName()
	} else if badge := m.agentBadge(row.sb); badge != "" {
		title += "   " + badge
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
	title := stateBadge(sb.GetState()) + "  " + sb.GetDisplayName()
	if tag := sb.GetTag(); tag != "" {
		title += "  " + tagBadgeStyle.Render("#"+tag)
	}
	if n := sb.GetAttachedTerminals(); n > 0 {
		label := "▣ " + itoa(int(n))
		if sb.GetExternalAttached() {
			label += "⧉" // an external terminal is among them
		}
		title += "  " + termCountStyle.Render(label)
	}
	return title
}

// itoa is a tiny int→string without importing strconv for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// agentBadge renders a live indicator of the coding agent's status for a RUNNING
// sandbox — working, waiting for a prompt, or idle — so users can see at a glance
// what each agent is doing without opening the sandbox. It returns "" for
// non-running sandboxes and for sandboxes launched without an agent (nothing to
// show). The WORKING indicator reuses the model spinner so it animates while the
// agent is active; IDLE is the resting state (initial, or after a task completes).
func (m Model) agentBadge(sb *pb.Sandbox) string {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || sb.GetAgent() == nil {
		return ""
	}
	switch sb.GetAgent().GetStatus() {
	case pb.AgentStatus_AGENT_STATUS_WORKING:
		return m.spinner.View() + " " + agentWorkStyle.Render("working")
	case pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
		return agentWaitStyle.Render("◆ needs input")
	case pb.AgentStatus_AGENT_STATUS_IDLE:
		return agentIdleStyle.Render("✓ idle")
	default: // UNSPECIFIED / EXITED — nothing to surface on the row
		return ""
	}
}

// anyAgentWorking reports whether any sandbox in the current tab has a working
// agent, so the spinner-tick handler knows to keep re-rendering the list (the
// "working" indicator animates via the shared spinner).
func (m Model) anyAgentWorking() bool {
	for _, row := range m.currentTabRows() {
		if row.sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING &&
			row.sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_WORKING {
			return true
		}
	}
	return false
}

// mergeSandboxUpdate applies a live Sandbox update from the event stream into the
// in-memory lists (both the single-daemon list and the multi-host aggregate)
// without a network round-trip. The daemon emits one Event_SandboxChanged per
// agent-status/state change (FR-024/025) — including on every tool-use while the
// agent works — so reloading over the wire each time would be needlessly chatty.
// It reports whether a rendered field (state, agent status, or name) actually
// changed, letting the caller skip a redundant re-render on no-op repeats.
//
// Sandboxes are matched by their unique id, NOT by host: the aggregate is keyed
// by the client's host id (e.g. "local"), while the event's sb.HostId is the
// daemon-assigned host id (the machine hostname), so those two legitimately
// differ — gating on them would drop every live update into the void, leaving the
// rendered list (which reads from m.hostAgg) stale until a manual reload.
func (m *Model) mergeSandboxUpdate(sb *pb.Sandbox) bool {
	if sb.GetId() == "" {
		return false
	}
	changed := false
	apply := func(list []*pb.Sandbox) {
		for i, ex := range list {
			if ex.GetId() == sb.GetId() {
				if renderFieldsDiffer(ex, sb) {
					changed = true
				}
				list[i] = sb
				return
			}
		}
	}
	apply(m.sandboxes)
	for hi := range m.hostAgg {
		apply(m.hostAgg[hi].Sandboxes)
	}
	return changed
}

// removeSandbox drops a sandbox from the in-memory lists on a Removed event,
// reporting whether anything was removed (so the caller can skip a re-render).
func (m *Model) removeSandbox(id string) bool {
	if id == "" {
		return false
	}
	removed := false
	filter := func(list []*pb.Sandbox) []*pb.Sandbox {
		out := list[:0]
		for _, sb := range list {
			if sb.GetId() == id {
				removed = true
				continue
			}
			out = append(out, sb)
		}
		return out
	}
	m.sandboxes = filter(m.sandboxes)
	for hi := range m.hostAgg {
		m.hostAgg[hi].Sandboxes = filter(m.hostAgg[hi].Sandboxes)
	}
	return removed
}

// renderFieldsDiffer reports whether two snapshots of the same sandbox differ in
// any field the row renders, so repeated identical updates don't cause churn.
func renderFieldsDiffer(a, b *pb.Sandbox) bool {
	return a.GetState() != b.GetState() ||
		a.GetAgent().GetStatus() != b.GetAgent().GetStatus() ||
		a.GetDisplayName() != b.GetDisplayName() ||
		a.GetTag() != b.GetTag() ||
		a.GetAttachedTerminals() != b.GetAttachedTerminals() ||
		a.GetExternalAttached() != b.GetExternalAttached()
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

// actionable returns the selected sandbox unless it is a still-creating optimistic
// placeholder — those have no real daemon id yet, so per-sandbox actions must wait
// for the launch to resolve. Returning nil makes such keypresses a harmless no-op.
func (m Model) actionable() *pb.Sandbox {
	sb := m.current()
	if sb == nil {
		return nil
	}
	if _, pending := m.launching[sb.GetId()]; pending {
		return nil
	}
	return sb
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
		if sb := m.actionable(); sb != nil {
			return m.enterTerminal(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.Popout):
		if sb := m.actionable(); sb != nil {
			return m.openExternalTerminal(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.Tag):
		if sb := m.actionable(); sb != nil {
			return m.enterTag(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.VSCode):
		if sb := m.actionable(); sb != nil {
			return m.startBusy(sb.GetId(), "opening"), m.openVSCodeCmd(m.currentHostID(), sb.GetId())
		}
		return m, nil
	case keyIs(msg, m.keys.StartStop):
		if sb := m.actionable(); sb != nil {
			d := m.daemonForHost(m.currentHostID())
			// One key toggles by state: stop a running sandbox, start any other.
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
				return m.startBusy(sb.GetId(), "stopping"), m.stopCmd(d, sb.GetId())
			}
			return m.startBusy(sb.GetId(), "starting"), m.restartCmd(d, sb.GetId())
		}
		return m, nil
	case keyIs(msg, m.keys.Destroy):
		if sb := m.actionable(); sb != nil {
			return m.startBusy(sb.GetId(), "destroying"), m.destroyCmd(m.daemonForHost(m.currentHostID()), sb.GetId())
		}
		return m, nil
	case keyIs(msg, m.keys.Rename):
		if sb := m.actionable(); sb != nil {
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
				m.status = "stop the sandbox before renaming it"
				return m, nil
			}
			return m.enterRename(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.RefreshSandbox):
		if sb := m.actionable(); sb != nil {
			return m.confirmRefresh(sb, m.currentHostID())
		}
		return m, nil
	case keyIs(msg, m.keys.Kits):
		return m.enterKitPicker()
	case keyIs(msg, m.keys.AddKit):
		if sb := m.actionable(); sb != nil {
			return m.enterKitAttach(sb, m.currentHostID())
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
