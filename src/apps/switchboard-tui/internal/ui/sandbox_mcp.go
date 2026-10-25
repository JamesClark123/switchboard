package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Feature 008 (FR-102). `M` on a sandbox row opens the per-sandbox MCP overlay:
// the servers switchboard attached to that sandbox (at launch or later), each
// with the kind and authorization state its daemon reports, and `a` attaches
// one more registered server from that daemon's gateway list. The runtime
// delivers the new tools to the agent live, so nothing here stops or restarts
// anything; servers the agent attached for itself are the runtime's affair and
// are not listed. `d` detaches the highlighted server (SWI-3) behind a confirm;
// the server stays registered on the daemon and can be attached again.

// attachMcpTimeout is the client-side bound on one attach; the daemon bounds the
// `mcp load` itself at 60 s. A package constant, never an env var.
const attachMcpTimeout = 2 * time.Minute

// sandboxMcpState backs the overlay for one sandbox.
type sandboxMcpState struct {
	sandboxID  string
	sandbox    string // display name
	host       string // owning host id — every RPC targets THIS host
	mode       pb.McpAttachMode
	attached   []string        // the record as last seen
	registered []*pb.McpServer // the daemon's gateway list, nil until loaded
	gatewayErr string          // why the gateway cannot be managed on the host
	listErr    string          // the last list failure, shown inline
	loading    bool
	cursor     int
	picking    bool // the add sub-mode (pick a registered server)
	pickCursor int
	busy       string // name being attached, "" when idle
	status     string
}

// sandboxMcpListMsg carries the daemon's gateway list for the overlay.
type sandboxMcpListMsg struct {
	id      string
	servers []*pb.McpServer
	err     error
}

// sandboxMcpAttachedMsg is the outcome of one attach.
type sandboxMcpAttachedMsg struct {
	id   string
	name string
	sb   *pb.Sandbox
	err  error
}

// sandboxMcpDetachedMsg is the outcome of one detach.
type sandboxMcpDetachedMsg struct {
	id   string
	name string
	sb   *pb.Sandbox
	err  error
}

// enterSandboxMcp opens the overlay and asks the owning daemon for its gateway
// list (to decorate the attached names and to offer candidates).
func (m Model) enterSandboxMcp(sb *pb.Sandbox, host string) (tea.Model, tea.Cmd) {
	v := sandboxMcpState{
		sandboxID: sb.GetId(),
		sandbox:   sb.GetDisplayName(),
		host:      host,
		mode:      sb.GetMcpAttachMode(),
		attached:  append([]string(nil), sb.GetMcpServers()...),
	}
	m.screen = screenSandboxMcp
	d := m.daemonForHost(host)
	if ok, reason := d.McpGateway(); !ok {
		if reason == "" {
			reason = "the MCP gateway cannot be managed on this host"
		}
		v.gatewayErr = reason
		m.sandboxMcpView = v
		return m, nil
	}
	v.loading = true
	m.sandboxMcpView = v
	return m, m.sandboxMcpListCmd(host, sb.GetId())
}

// sandboxMcpListCmd lists the daemon's registered servers.
func (m Model) sandboxMcpListCmd(host, id string) tea.Cmd {
	d := m.daemonForHost(host)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		servers, _, err := d.ListMcpServers(ctx)
		return sandboxMcpListMsg{id: id, servers: servers, err: err}
	}
}

func (m Model) handleSandboxMcpList(msg sandboxMcpListMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenSandboxMcp || m.sandboxMcpView.sandboxID != msg.id {
		return m, nil // overlay closed or moved on; drop a late list
	}
	m.sandboxMcpView.loading = false
	if msg.err != nil {
		m.sandboxMcpView.listErr = "could not list the daemon's servers: " + msg.err.Error()
		return m, nil
	}
	m.sandboxMcpView.listErr = ""
	m.sandboxMcpView.registered = msg.servers
	return m, nil
}

// mcpOverlayFor reports whether the overlay is open for sandbox id.
func (m Model) mcpOverlayFor(id string) bool {
	return m.screen == screenSandboxMcp && m.sandboxMcpView.sandboxID == id
}

// syncSandboxMcpOverlay refreshes an open overlay from a live sandbox update
// (Event.sandbox_changed or a completed attach), so an attach made anywhere
// reaches it without a manual reload.
func (m *Model) syncSandboxMcpOverlay(sb *pb.Sandbox) {
	if sb == nil || !m.mcpOverlayFor(sb.GetId()) {
		return
	}
	m.sandboxMcpView.attached = append([]string(nil), sb.GetMcpServers()...)
	m.sandboxMcpView.mode = sb.GetMcpAttachMode()
	if n := len(m.sandboxMcpView.attached); m.sandboxMcpView.cursor >= n {
		m.sandboxMcpView.cursor = max(n-1, 0)
	}
}

// mcpCandidates are the daemon's registered servers not yet on the record.
func (v sandboxMcpState) mcpCandidates() []*pb.McpServer {
	have := map[string]bool{}
	for _, n := range v.attached {
		have[n] = true
	}
	var out []*pb.McpServer
	for _, s := range v.registered {
		if !have[s.GetName()] {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

func (m Model) sandboxMcpHelp() helpBindings {
	if m.sandboxMcpView.picking {
		return helpBindings{hkey("enter", "attach"), hkey("↑/↓", "move"), hkey("esc", "back")}
	}
	return helpBindings{hkey("a", "attach server"), hkey("d", "detach"), hkey("r", "reload"), hkey("↑/↓", "move"), hkey("esc", "close")}
}

// updateSandboxMcpKey drives the overlay. An attach only ever adds a server; a
// detach (`d`) removes one from the sandbox only (the registration is untouched)
// and is confirmed first.
func (m Model) updateSandboxMcpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := &m.sandboxMcpView
	if v.picking {
		cands := v.mcpCandidates()
		switch msg.String() {
		case "esc", "q":
			v.picking = false
		case "up", "k":
			if v.pickCursor > 0 {
				v.pickCursor--
			}
		case "down", "j":
			if v.pickCursor < len(cands)-1 {
				v.pickCursor++
			}
		case "enter":
			if v.busy != "" {
				v.status = "an attach is already in progress"
				return m, nil
			}
			if v.pickCursor < 0 || v.pickCursor >= len(cands) {
				return m, nil
			}
			name := cands[v.pickCursor].GetName()
			v.busy = name
			v.status = "attaching " + name + "…"
			return m, tea.Batch(m.attachMcpCmd(v.host, v.sandboxID, name), m.spinner.Tick)
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "q":
		m.sandboxMcpView = sandboxMcpState{}
		m.screen = screenList
		return m, nil
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
		}
	case "down", "j":
		if v.cursor < len(v.attached)-1 {
			v.cursor++
		}
	case "r":
		if v.gatewayErr == "" && !v.loading {
			v.loading = true
			return m, m.sandboxMcpListCmd(v.host, v.sandboxID)
		}
	case "d":
		switch {
		case v.gatewayErr != "":
			v.status = "cannot detach: " + v.gatewayErr
		case v.busy != "":
			v.status = "an attach is already in progress"
		case v.cursor < 0 || v.cursor >= len(v.attached):
			v.status = "nothing to detach"
		default:
			name, id, host := v.attached[v.cursor], v.sandboxID, v.host
			v.status = ""
			return m.enterConfirm(confirmState{
				title: "Detach " + name + "?",
				body: []string{
					"Removes " + selectedStyle.Render(name) + " from sandbox " + selectedStyle.Render(v.sandbox) + ".",
					"The agent loses its tools at once; the server stays registered",
					"on the daemon and can be attached again.",
				},
				warning:   "The sandbox keeps running.",
				onConfirm: m.detachMcpCmd(host, id, name),
				returnTo:  screenSandboxMcp,
			})
		}
	case "a":
		switch {
		case v.gatewayErr != "":
			v.status = "cannot attach: " + v.gatewayErr
		case v.loading:
			v.status = "still loading the daemon's servers…"
		case v.listErr != "":
			v.status = "cannot attach: " + v.listErr + " (r to retry)"
		case v.busy != "":
			v.status = "an attach is already in progress"
		case len(v.mcpCandidates()) == 0:
			v.status = "every server registered on this daemon is already attached — register more on the daemon screen (shift+→)"
		default:
			v.picking = true
			v.pickCursor = 0
			v.status = ""
		}
	}
	return m, nil
}

// attachMcpCmd runs the attach against the owning daemon.
func (m Model) attachMcpCmd(host, id, name string) tea.Cmd {
	d := m.daemonForHost(host)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), attachMcpTimeout)
		defer cancel()
		sb, err := d.AttachMcpServer(ctx, id, name)
		return sandboxMcpAttachedMsg{id: id, name: name, sb: sb, err: err}
	}
}

// detachMcpCmd runs the detach against the owning daemon.
func (m Model) detachMcpCmd(host, id, name string) tea.Cmd {
	d := m.daemonForHost(host)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), attachMcpTimeout)
		defer cancel()
		sb, err := d.DetachMcpServer(ctx, id, name)
		return sandboxMcpDetachedMsg{id: id, name: name, sb: sb, err: err}
	}
}

// handleSandboxMcpDetached merges the updated record into the row and the
// overlay, or surfaces the daemon's diagnostic.
func (m Model) handleSandboxMcpDetached(msg sandboxMcpDetachedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if m.mcpOverlayFor(msg.id) {
			m.sandboxMcpView.status = "detach " + msg.name + " failed: " + msg.err.Error()
		} else {
			m.status = "detach " + msg.name + " from " + m.sandboxName(msg.id) + " failed: " + msg.err.Error()
		}
		return m, nil
	}
	if msg.sb != nil {
		m.mergeSandboxUpdate(msg.sb)
		m.refreshListItems()
		m.syncSandboxMcpOverlay(msg.sb)
	}
	if m.mcpOverlayFor(msg.id) {
		m.sandboxMcpView.status = "detached " + msg.name + " — still registered on the daemon (a re-attaches it)"
	} else {
		m.status = "detached " + msg.name + " from " + m.sandboxName(msg.id)
	}
	return m, nil
}

// handleSandboxMcpAttached merges the updated record into the row and the
// overlay, or surfaces the daemon's diagnostic.
func (m Model) handleSandboxMcpAttached(msg sandboxMcpAttachedMsg) (tea.Model, tea.Cmd) {
	if m.mcpOverlayFor(msg.id) {
		m.sandboxMcpView.busy = ""
		m.sandboxMcpView.picking = false
	}
	if msg.err != nil {
		if m.mcpOverlayFor(msg.id) {
			m.sandboxMcpView.status = "attach " + msg.name + " failed: " + msg.err.Error()
		} else {
			m.status = "attach " + msg.name + " to " + m.sandboxName(msg.id) + " failed: " + msg.err.Error()
		}
		return m, nil
	}
	if msg.sb != nil {
		m.mergeSandboxUpdate(msg.sb)
		m.refreshListItems()
		m.syncSandboxMcpOverlay(msg.sb)
	}
	if m.mcpOverlayFor(msg.id) {
		m.sandboxMcpView.status = "attached " + msg.name + " — the agent sees its tools now"
	} else {
		m.status = "attached " + msg.name + " to " + m.sandboxName(msg.id)
	}
	return m, nil
}

// sandboxMcpModal renders the overlay: the attached servers (decorated from the
// daemon's list when loaded), then the picker in the add sub-mode.
func (m Model) sandboxMcpModal() string {
	v := m.sandboxMcpView
	hostLabel := m.hostDisplayName(v.host)
	if hostLabel == "" {
		hostLabel = m.daemonForHost(v.host).HostID()
	}
	mode := "additive"
	if v.mode == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		mode = "exclusive"
	}
	title := sectionStyle.Render("MCP servers · "+v.sandbox) + dimStyle.Render("  ·  "+mode+"  ·  host "+hostLabel)

	byName := map[string]*pb.McpServer{}
	for _, s := range v.registered {
		byName[s.GetName()] = s
	}
	rows := make([]string, 0, len(v.attached)+2)
	for i, name := range v.attached {
		cursor := "  "
		if i == v.cursor && !v.picking {
			cursor = cursorBarStyle.Render("> ")
		}
		row := cursor + pad(name, 18)
		switch srv := byName[name]; {
		case srv != nil:
			kind := srv.GetTransport()
			if kind == "" {
				kind = mcpKindLabel(srv.GetKind())
			}
			row += "  " + dimStyle.Render(pad(kind, 12)) + "  " + mcpAuthLabel(srv.GetAuthState())
			if srv.GetAuthState() == pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED {
				// Its own short line: the modal wraps long rows.
				row += "\n    " + dimStyle.Render("needs authorization: shift+→ › MCP gateway › A")
			}
		case v.registered != nil:
			row += "  " + statusErrStyle.Render("no longer registered on this host")
		default:
			row += "  " + dimStyle.Render("…")
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		rows = append(rows, dimStyle.Render("(none attached — press a to attach a registered server)"))
	}
	rows = append(rows, "", dimStyle.Render("servers the agent attached for itself are not listed here"))
	content := lipgloss.JoinVertical(lipgloss.Left, rows...)

	if v.picking {
		cands := v.mcpCandidates()
		pick := []string{"", sectionStyle.Render("Attach a registered server")}
		for i, s := range cands {
			cursor := "  "
			if i == v.pickCursor {
				cursor = cursorBarStyle.Render("> ")
			}
			kind := s.GetTransport()
			if kind == "" {
				kind = mcpKindLabel(s.GetKind())
			}
			pick = append(pick, cursor+pad(s.GetName(), 18)+"  "+dimStyle.Render(pad(kind, 12))+"  "+mcpAuthLabel(s.GetAuthState()))
		}
		content = lipgloss.JoinVertical(lipgloss.Left, append([]string{content}, pick...)...)
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, title, "", content)
	switch {
	case v.gatewayErr != "":
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", statusErrStyle.Render("MCP gateway unavailable on this host: "+v.gatewayErr))
	case v.loading:
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", m.spinner.View()+dimStyle.Render(" loading the daemon's servers…"))
	case v.listErr != "":
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", statusErrStyle.Render(v.listErr))
	}
	if v.busy != "" {
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", m.spinner.View()+" "+selectedStyle.Render("attaching "+v.busy+"…"))
	} else if v.status != "" {
		style := dimStyle
		if strings.Contains(v.status, "failed") || strings.HasPrefix(v.status, "cannot") {
			style = statusErrStyle
		}
		inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", style.Render(v.status))
	}
	help := "a attach server · d detach · r reload · ↑/↓ move · esc close"
	if v.picking {
		help = "enter attach · ↑/↓ move · esc back"
	}
	inner = lipgloss.JoinVertical(lipgloss.Left, inner, "", helpStyle.Render(help))
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
