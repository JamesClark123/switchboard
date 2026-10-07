package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
)

// The daemon screen (feature 008, FR-068..FR-071) is the second top-level screen
// beside the sandbox list: every known daemon (one per saved host, local
// included) with its connection state, and for a connected one a short list of
// management options — the MCP gateway first. Rows are rebuilt from the host
// manager on every render so connection changes land live (FR-071); only the
// cursor is state, so a round-trip through the sandbox list preserves it.

// daemonRow is one known daemon as the screen shows it.
type daemonRow struct {
	id, name, kind string
	target         string // ssh target or socket path
	state          client.HostState
	err            error
	daemon         Daemon // non-nil when connected
}

// daemonsState backs the screen: the highlighted daemon and whether its option
// list is open.
type daemonsState struct {
	cursor    int
	options   bool
	optCursor int
}

// daemonOptions are the per-daemon management surfaces, in menu order. Adding one
// here is the only navigation change a future per-daemon feature needs.
var daemonOptions = []string{"MCP gateway"}

// daemonRows derives the rows from the host manager (or the single local daemon
// when there is no manager, as in tests and single-host use).
func (m Model) daemonRows() []daemonRow {
	if m.manager == nil {
		return []daemonRow{{id: m.daemon.HostID(), name: m.daemon.HostID(), kind: "local", state: client.HostConnected, daemon: m.daemon}}
	}
	hcs := m.manager.List()
	rows := make([]daemonRow, 0, len(hcs))
	for _, hc := range hcs {
		r := daemonRow{id: hc.Entry.ID, name: hc.Entry.DisplayName, kind: hc.Entry.Kind, state: hc.State, err: hc.Err}
		if r.name == "" {
			r.name = r.id
		}
		switch hc.Entry.Kind {
		case "ssh":
			r.target = hc.Entry.SSHTarget
		default:
			r.target = hc.Entry.SocketPath
		}
		if hc.State == client.HostConnected {
			r.daemon = m.daemonForHost(hc.Entry.ID)
		}
		rows = append(rows, r)
	}
	return rows
}

// enterDaemons switches to the daemon screen (shift+→ from the list). Known hosts
// are synced into the manager first, as the hosts screen does, so a saved host
// that was never visited still appears.
func (m Model) enterDaemons() (tea.Model, tea.Cmd) {
	if m.manager != nil && m.hostStore != nil {
		if known, err := m.hostStore.List(); err == nil {
			for _, h := range known {
				m.manager.Upsert(toEntry(h))
			}
		}
	}
	m.screen = screenDaemons
	m.clampDaemonCursor()
	return m, nil
}

func (m *Model) clampDaemonCursor() {
	if n := len(m.daemonRows()); m.daemons.cursor >= n {
		m.daemons.cursor = max(n-1, 0)
	}
}

// highlightedDaemon returns the row under the cursor.
func (m Model) highlightedDaemon() (daemonRow, bool) {
	rows := m.daemonRows()
	if len(rows) == 0 || m.daemons.cursor >= len(rows) {
		return daemonRow{}, false
	}
	return rows[m.daemons.cursor], true
}

func (m Model) daemonsHelp() helpBindings {
	if m.daemons.options {
		return helpBindings{hkey("↑/↓", "move"), hkey("enter", "open"), hkey("esc", "close")}
	}
	return helpBindings{
		hkey("↑/↓", "move"),
		hkey("enter", "options"),
		hkey("c", "connect"),
		hkey("x", "disconnect"),
		m.keys.ScreenPrev,
		m.keys.Settings,
		m.keys.Quit,
	}
}

func (m Model) updateDaemonsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if keyIs(msg, m.keys.ScreenPrev) {
		m.daemons.options = false
		m.screen = screenList
		return m, nil
	}
	if m.daemons.options {
		switch msg.String() {
		case "esc", "q":
			m.daemons.options = false
		case "up", "k":
			if m.daemons.optCursor > 0 {
				m.daemons.optCursor--
			}
		case "down", "j":
			if m.daemons.optCursor < len(daemonOptions)-1 {
				m.daemons.optCursor++
			}
		case "enter":
			if row, ok := m.highlightedDaemon(); ok && row.daemon != nil && m.daemons.optCursor == 0 {
				return m.enterMcp(row)
			}
		}
		return m, nil
	}
	if keyIs(msg, m.keys.Settings) {
		return m.enterSettings(screenDaemons)
	}
	switch msg.String() {
	case "esc":
		m.screen = screenList
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "c":
		row, ok := m.highlightedDaemon()
		if !ok || m.manager == nil {
			return m, nil
		}
		if row.state == client.HostConnected {
			m.status = row.name + " is already connected"
			return m, nil
		}
		// SSH hosts get the shared password prompt (blank = key/agent auth);
		// local hosts have no interactive auth so connect directly (FR-069).
		if row.kind == "ssh" {
			return m.enterHostPasswordFrom(row.id, row.target, pwDaemonsScreen, "")
		}
		m.hosts.lastConnect = row.id
		return m, m.connectHostCmd(row.id, "")
	case "x":
		row, ok := m.highlightedDaemon()
		if !ok || m.manager == nil {
			return m, nil
		}
		if row.state != client.HostConnected {
			m.status = row.name + " is not connected"
			return m, nil
		}
		if row.kind == "local" {
			m.status = "the local daemon stays connected"
			return m, nil
		}
		m.manager.Disconnect(row.id)
		// The access path to that host's services is gone (feature 006, US5-2).
		m = m.hostDisconnected(row.id)
		m.status = "disconnected " + row.name
		if m.mcpView.host == row.id {
			m.mcpView = mcpState{}
		}
		return m, m.hostsCmd()
	case "up", "k":
		if m.daemons.cursor > 0 {
			m.daemons.cursor--
		}
	case "down", "j":
		if m.daemons.cursor < len(m.daemonRows())-1 {
			m.daemons.cursor++
		}
	case "enter":
		if row, ok := m.highlightedDaemon(); ok && row.daemon != nil {
			m.daemons.options = true
			m.daemons.optCursor = 0
		} else if ok {
			m.status = row.name + " is " + row.state.String() + " — no management options until it is connected"
		}
	}
	return m, nil
}

func (m Model) viewDaemons() string {
	rows := m.daemonRows()
	lines := []string{sectionStyle.Render("Daemons"), ""}
	if len(rows) == 0 {
		lines = append(lines, dimStyle.Render("(no daemons known)"))
	}
	for i, r := range rows {
		cursor := "  "
		if i == m.daemons.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		name := r.name
		if r.id == m.activeHost {
			name = selectedStyle.Render("★ " + name)
		}
		line := cursor + hostStateBadge(r.state) + "  " + name + dimStyle.Render("  ("+r.kind+" "+r.target+")")
		lines = append(lines, line)
		lines = append(lines, "    "+dimStyle.Render(daemonDetail(r)))
		if i == m.daemons.cursor && m.daemons.options {
			for j, opt := range daemonOptions {
				mark := "    "
				if j == m.daemons.optCursor {
					mark = "  " + cursorBarStyle.Render("▸ ")
				}
				lines = append(lines, mark+opt)
			}
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// daemonDetail is the second line of a row: versions and capability markers for a
// connected daemon, the failure reason for a disconnected one.
func daemonDetail(r daemonRow) string {
	if r.daemon == nil {
		if r.err != nil {
			return "error: " + r.err.Error()
		}
		return strings.ToLower(r.state.String())
	}
	parts := []string{}
	if v := r.daemon.DaemonVersion(); v != "" {
		parts = append(parts, "sxbd "+v)
	}
	if v := r.daemon.SbxVersion(); v != "" {
		parts = append(parts, "sbx "+strings.TrimSpace(v))
	}
	if !r.daemon.RuntimeBaselineMet() {
		min := r.daemon.SbxMinVersion()
		if min == "" {
			min = "?"
		}
		parts = append(parts, "below baseline (min "+min+")")
	}
	if ok, reason := r.daemon.McpGateway(); !ok {
		if reason == "" {
			reason = "unavailable"
		}
		parts = append(parts, "gateway: "+reason)
	}
	if len(parts) == 0 {
		return "connected"
	}
	return strings.Join(parts, " · ")
}
