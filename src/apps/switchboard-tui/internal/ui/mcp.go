package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The MCP gateway view (feature 008, US1/US2) manages ONE daemon's registrations
// from the daemon screen: list, register, remove, authorize, mark attach-by-default
// and set the daemon's default attach mode. Every operation goes to that daemon
// only; the list is always re-read from the daemon (which reads the host) after an
// operation and on an mcp_gateway_changed event.

// Client-side bounds (package constants, never env vars; research R11).
const (
	mcpOpTimeout   = 2 * time.Minute                 // register / remove / marks / mode
	mcpAuthTimeout = 10*time.Minute + 30*time.Second // the daemon's 10-minute wait plus slack
)

var mcpNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// mcpState backs the view for one daemon.
type mcpState struct {
	host     string
	hostName string
	daemon   Daemon

	servers     []*pb.McpServer
	settings    *pb.McpGatewaySettings
	cursor      int
	loading     bool
	unavailable string // the daemon's reason when the gateway cannot be managed (FR-079)
	status      string
	notes       []string // runtime notes from the last removal (credential material left behind)

	// add form
	form *huh.Form
	vals *mcpFormVals
	// pending is a host-command registration awaiting the outside-isolation
	// acknowledgement (FR-076); shown as a modal until confirmed or dismissed.
	pending *pb.AddMcpServerRequest

	// auth is the in-flight authorization wait, shown as a modal (FR-075).
	auth *mcpAuthWait
}

type mcpFormVals struct {
	name, kind, url, command, args, dir string
	attach                              bool
}

type mcpAuthWait struct {
	name     string
	url      string
	deadline time.Time
	messages []string
	ch       chan tea.Msg
	cancel   context.CancelFunc
}

// --- messages ---

type mcpLoadedMsg struct {
	host     string
	servers  []*pb.McpServer
	settings *pb.McpGatewaySettings
	err      error
}

// mcpDoneMsg is the outcome of a register/remove/mark/mode operation.
type mcpDoneMsg struct {
	host   string
	status string
	notes  []string
	err    error
}

type mcpAuthMsg struct {
	host, name string
	update     client.McpAuthUpdate
}

type mcpAuthEndMsg struct {
	host, name string
	state      pb.McpAuthState
	err        error
}

// --- entry + loading ---

// enterMcp opens the gateway view for a connected daemon row and loads its list.
func (m Model) enterMcp(row daemonRow) (tea.Model, tea.Cmd) {
	m.mcpView = mcpState{host: row.id, hostName: row.name, daemon: row.daemon, loading: true}
	if ok, reason := row.daemon.McpGateway(); !ok {
		m.mcpView.loading = false
		m.mcpView.unavailable = reason
		if reason == "" {
			m.mcpView.unavailable = "the MCP gateway cannot be managed on this host"
		}
		m.screen = screenMcp
		return m, nil
	}
	m.screen = screenMcp
	return m, m.mcpLoadCmd(row.daemon, row.id)
}

func (m Model) mcpLoadCmd(d Daemon, host string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		servers, settings, err := d.ListMcpServers(ctx)
		return mcpLoadedMsg{host: host, servers: servers, settings: settings, err: err}
	}
}

func (m Model) applyMcpLoaded(msg mcpLoadedMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenMcp || m.mcpView.host != msg.host {
		return m, nil
	}
	m.mcpView.loading = false
	if msg.err != nil {
		// A daemon that cannot manage its gateway answers every call with the
		// reason; show it in place rather than as a transient error (FR-079).
		m.mcpView.unavailable = msg.err.Error()
		return m, nil
	}
	m.mcpView.unavailable = ""
	m.mcpView.servers = msg.servers
	m.mcpView.settings = msg.settings
	if n := len(msg.servers); m.mcpView.cursor >= n {
		m.mcpView.cursor = max(n-1, 0)
	}
	return m, nil
}

func (m Model) applyMcpDone(msg mcpDoneMsg) (tea.Model, tea.Cmd) {
	if m.screen != screenMcp || m.mcpView.host != msg.host {
		return m, nil
	}
	if msg.err != nil {
		// The host's own diagnostic, verbatim (FR-078); the list is unchanged.
		m.mcpView.status = "error: " + msg.err.Error()
		return m, nil
	}
	m.mcpView.status = msg.status
	m.mcpView.notes = msg.notes
	return m, m.mcpLoadCmd(m.mcpView.daemon, m.mcpView.host)
}

// --- keys ---

func (m Model) mcpHelp() helpBindings {
	switch {
	case m.mcpView.auth != nil:
		return helpBindings{hkey("esc", "cancel wait")}
	case m.mcpView.pending != nil:
		return helpBindings{hkey("y", "register anyway"), hkey("esc", "cancel")}
	case m.mcpView.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "register"), hkey("esc", "cancel")}
	case m.mcpView.unavailable != "":
		return helpBindings{hkey("r", "retry"), hkey("esc", "back")}
	}
	return helpBindings{
		hkey("a", "register"), hkey("d", "remove"), hkey("A", "authorize"),
		hkey("space", "attach by default"), hkey("m", "attach mode"), hkey("r", "reload"), hkey("esc", "back"),
	}
}

func (m Model) highlightedMcp() (*pb.McpServer, bool) {
	v := m.mcpView
	if len(v.servers) == 0 || v.cursor >= len(v.servers) {
		return nil, false
	}
	return v.servers[v.cursor], true
}

func (m Model) updateMcpKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	switch {
	case v.auth != nil:
		if msg.String() == "esc" {
			v.auth.cancel() // the daemon kills the wait; the end message follows
			v.status = "cancelling authorization of " + v.auth.name + "…"
		}
		return m, nil
	case v.pending != nil:
		switch msg.String() {
		case "y", "Y":
			req := v.pending
			v.pending = nil
			req.AcknowledgeLocalExecution = true
			return m, m.mcpAddCmd(v.daemon, v.host, req)
		case "esc", "n", "N", "q":
			v.pending = nil
			v.status = "registration cancelled"
		}
		return m, nil
	case v.form != nil:
		switch msg.String() {
		case "esc":
			v.form, v.vals = nil, nil
			return m, nil
		case "ctrl+s":
			return m.applyMcpForm()
		}
		return m.advanceMcpForm(msg)
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenDaemons
		return m, nil
	case "r":
		v.loading, v.status = true, ""
		return m, m.mcpLoadCmd(v.daemon, v.host)
	}
	if v.unavailable != "" {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		if v.cursor > 0 {
			v.cursor--
		}
	case "down", "j":
		if v.cursor < len(v.servers)-1 {
			v.cursor++
		}
	case "a":
		v.vals = &mcpFormVals{kind: "remote"}
		v.form = m.mcpForm(v.vals)
		return m, v.form.Init()
	case "d":
		if srv, ok := m.highlightedMcp(); ok {
			return m.enterConfirm(confirmState{
				title: "Remove MCP server?",
				body: []string{
					"Remove " + selectedStyle.Render(srv.GetName()) + " from " + selectedStyle.Render(v.hostName) + ".",
					"",
					"Sandboxes already using it keep it until they are recreated;",
					"its attach-by-default mark is cleared.",
				},
				warning:   "Credential material the runtime keeps is reported, not deleted.",
				returnTo:  screenMcp,
				onConfirm: m.mcpRemoveCmd(v.daemon, v.host, srv.GetName()),
			})
		}
	case "A":
		if srv, ok := m.highlightedMcp(); ok {
			if srv.GetKind() == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
				v.status = srv.GetName() + " runs on the host; it has no authorization step"
				return m, nil
			}
			return m.startMcpAuth(srv.GetName())
		}
	case " ":
		if srv, ok := m.highlightedMcp(); ok {
			return m, m.mcpSetDefaultCmd(v.daemon, v.host, srv.GetName(), !srv.GetAttachByDefault())
		}
	case "m":
		next := pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE
		if v.settings.GetDefaultAttachMode() == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
			next = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
		}
		return m, m.mcpSetModeCmd(v.daemon, v.host, next)
	}
	return m, nil
}

// --- the add form ---

func (m Model) mcpForm(v *mcpFormVals) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").
			Description("Letters, digits, dots, hyphens, underscores. Unique on this host.").Value(&v.name),
		huh.NewSelect[string]().Key("kind").Title("Kind").
			Options(huh.NewOption("Remote endpoint (URL)", "remote"), huh.NewOption("Host command (runs on the daemon's host)", "command")).Value(&v.kind),
		huh.NewInput().Key("url").Title("URL").Description("Remote endpoint, e.g. https://mcp.notion.com/mcp").Value(&v.url),
		huh.NewInput().Key("command").Title("Command").Description("Host command, e.g. npx").Value(&v.command),
		huh.NewInput().Key("args").Title("Arguments").Description("Comma-separated, e.g. @playwright/mcp@latest").Value(&v.args),
		huh.NewInput().Key("dir").Title("Working directory").Description("Optional, host command only.").Value(&v.dir),
		huh.NewConfirm().Key("attach").Title("Attach to every sandbox").
			Description("On = every sandbox launched on this daemon gets this server (FR-080).").Value(&v.attach),
	)).WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(kitFormKeyMap())
}

func (m Model) advanceMcpForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.form == nil {
		return m, nil
	}
	f, cmd := v.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		v.form = ff
	}
	switch v.form.State {
	case huh.StateCompleted:
		return m.applyMcpForm()
	case huh.StateAborted:
		v.form, v.vals = nil, nil
		return m, nil
	}
	return m, cmd
}

// applyMcpForm validates the form and sends the registration — or, for a host
// command, parks it behind the outside-isolation warning first (FR-076).
func (m Model) applyMcpForm() (tea.Model, tea.Cmd) {
	v := &m.mcpView
	vals := v.vals
	if v.form == nil || vals == nil {
		return m, nil
	}
	name := strings.TrimSpace(vals.name)
	if !mcpNameRe.MatchString(name) {
		v.status = "name must use letters, digits, dots, hyphens or underscores"
		return m, nil
	}
	req := &pb.AddMcpServerRequest{Name: name, AttachByDefault: vals.attach}
	switch vals.kind {
	case "command":
		cmd := strings.TrimSpace(vals.command)
		if cmd == "" {
			v.status = "a host command is required"
			return m, nil
		}
		var args []string
		for _, a := range strings.Split(vals.args, ",") {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, a)
			}
		}
		req.Definition = &pb.AddMcpServerRequest_Command{Command: &pb.AddMcpServerRequest_LocalCommand{Command: cmd, Args: args, Dir: strings.TrimSpace(vals.dir)}}
		v.form, v.vals = nil, nil
		v.pending = req // the warning modal takes it from here
		return m, nil
	default:
		url := strings.TrimSpace(vals.url)
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			v.status = "a remote endpoint URL (http:// or https://) is required"
			return m, nil
		}
		req.Definition = &pb.AddMcpServerRequest_Url{Url: url}
	}
	v.form, v.vals = nil, nil
	return m, m.mcpAddCmd(v.daemon, v.host, req)
}

// --- operations ---

func (m Model) mcpAddCmd(d Daemon, host string, req *pb.AddMcpServerRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		srv, err := d.AddMcpServer(ctx, req)
		if err != nil {
			return mcpDoneMsg{host: host, err: err}
		}
		status := "registered " + srv.GetName()
		if srv.GetKind() == pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
			status += " — press A to authorize it"
		}
		return mcpDoneMsg{host: host, status: status}
	}
}

func (m Model) mcpRemoveCmd(d Daemon, host, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		notes, err := d.RemoveMcpServer(ctx, name)
		if err != nil {
			return mcpDoneMsg{host: host, err: err}
		}
		return mcpDoneMsg{host: host, status: "removed " + name, notes: notes}
	}
}

func (m Model) mcpSetDefaultCmd(d Daemon, host, name string, on bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		if _, err := d.SetMcpServerDefault(ctx, name, on); err != nil {
			return mcpDoneMsg{host: host, err: err}
		}
		if on {
			return mcpDoneMsg{host: host, status: name + " will attach to every new sandbox"}
		}
		return mcpDoneMsg{host: host, status: name + " no longer attaches by default"}
	}
}

func (m Model) mcpSetModeCmd(d Daemon, host string, mode pb.McpAttachMode) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		if _, err := d.SetMcpAttachMode(ctx, mode); err != nil {
			return mcpDoneMsg{host: host, err: err}
		}
		return mcpDoneMsg{host: host, status: "default attach mode: " + mcpModeLabel(mode) + " (new launches only)"}
	}
}

// --- authorization wait ---

func (m Model) startMcpAuth(name string) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	ctx, cancel := context.WithTimeout(context.Background(), mcpAuthTimeout)
	ch := make(chan tea.Msg, 32)
	v.auth = &mcpAuthWait{name: name, deadline: time.Now().Add(10 * time.Minute), ch: ch, cancel: cancel}
	v.status = ""
	d, host := v.daemon, v.host
	go func() {
		defer cancel()
		state, err := d.AuthorizeMcpServer(ctx, name, func(u client.McpAuthUpdate) {
			ch <- mcpAuthMsg{host: host, name: name, update: u}
		})
		ch <- mcpAuthEndMsg{host: host, name: name, state: state, err: err}
		close(ch)
	}()
	return m, waitForMsg(ch)
}

func (m Model) applyMcpAuth(msg mcpAuthMsg) (tea.Model, tea.Cmd) {
	w := m.mcpView.auth
	if w == nil || w.name != msg.name || m.mcpView.host != msg.host {
		return m, nil
	}
	u := msg.update
	if u.URL != "" {
		w.url = u.URL
	}
	if !u.Deadline.IsZero() {
		w.deadline = u.Deadline
	}
	if u.Message != "" {
		w.messages = append(w.messages, u.Message)
		if len(w.messages) > 5 {
			w.messages = w.messages[len(w.messages)-5:]
		}
	}
	return m, waitForMsg(w.ch)
}

func (m Model) applyMcpAuthEnd(msg mcpAuthEndMsg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.auth == nil || v.auth.name != msg.name || v.host != msg.host {
		return m, nil
	}
	v.auth = nil
	switch {
	case msg.err != nil:
		v.status = "error: " + msg.err.Error()
	case msg.state == pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED:
		v.status = "authorized " + msg.name
	default:
		v.status = msg.name + " is not authorized (the wait ended) — press A to try again"
	}
	if m.screen != screenMcp {
		return m, nil
	}
	return m, m.mcpLoadCmd(v.daemon, v.host)
}

// --- rendering ---

func mcpModeLabel(mode pb.McpAttachMode) string {
	if mode == pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE {
		return "exclusive"
	}
	return "additive"
}

func mcpKindLabel(k pb.McpServerKind) string {
	if k == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
		return "host-run"
	}
	return "remote"
}

func mcpAuthLabel(s pb.McpAuthState) string {
	switch s {
	case pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED:
		return lipgloss.NewStyle().Foreground(colRunning).Render("authorized")
	case pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED:
		return statusErrStyle.Render("unauthorized")
	case pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE:
		return dimStyle.Render("no auth")
	default:
		return dimStyle.Render("auth unknown")
	}
}

func (m Model) viewMcp() string {
	v := m.mcpView
	title := sectionStyle.Render("MCP gateway · " + v.hostName)
	if v.form != nil {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", v.form.View(), m.mcpStatusLine())
	}
	if v.unavailable != "" {
		return lipgloss.JoinVertical(lipgloss.Left, title, "",
			statusErrStyle.Render("unavailable on this host: "+v.unavailable),
			dimStyle.Render("fix it there, then press r"))
	}
	mode := "default attach mode: " + selectedStyle.Render(mcpModeLabel(v.settings.GetDefaultAttachMode()))
	switch v.settings.GetDefaultAttachMode() {
	case pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE:
		mode += dimStyle.Render("  — marked servers are the agent's whole set (no discovery)")
	default:
		mode += dimStyle.Render("  — marked servers are added; the agent may also discover others")
	}
	rows := []string{title, mode, ""}
	if v.loading {
		rows = append(rows, m.spinner.View()+dimStyle.Render(" reading the host's registrations…"))
	} else if len(v.servers) == 0 {
		rows = append(rows, dimStyle.Render("(no MCP servers registered on this host — press a to register one)"))
	}
	for i, srv := range v.servers {
		cursor := "  "
		if i == v.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		mark := "[ ]"
		if srv.GetAttachByDefault() {
			mark = selectedStyle.Render("[x]")
		}
		rows = append(rows, fmt.Sprintf("%s%s %s  %s  %s  %s", cursor, mark, pad(srv.GetName(), 18),
			dimStyle.Render(pad(mcpKindLabel(srv.GetKind()), 9)), mcpAuthLabel(srv.GetAuthState()), dimStyle.Render(truncate(srv.GetTarget(), 48))))
	}
	rows = append(rows, "", dimStyle.Render("[x] = attached to every new sandbox on this daemon"))
	for _, n := range v.notes {
		rows = append(rows, dimStyle.Render("note: "+n))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...) + m.mcpStatusLine()
}

func (m Model) mcpStatusLine() string {
	if m.mcpView.status == "" {
		return ""
	}
	style := statusOKStyle
	if strings.HasPrefix(m.mcpView.status, "error") {
		style = statusErrStyle
	}
	return "\n\n" + style.Render(m.mcpView.status)
}

// mcpModal renders the authorization wait or the host-command warning.
func (m Model) mcpModal() string {
	v := m.mcpView
	var inner string
	switch {
	case v.auth != nil:
		w := v.auth
		remaining := time.Until(w.deadline).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		lines := []string{sectionStyle.Render("Authorize " + w.name), ""}
		if w.url == "" {
			lines = append(lines, m.spinner.View()+dimStyle.Render(" waiting for the host to start the authorization flow…"))
		} else {
			lines = append(lines,
				"Open this link where you are — it is never opened on the daemon's host:",
				"",
				selectedStyle.Render(w.url),
				"",
				dimStyle.Render("waiting for the provider to confirm…"))
		}
		for _, l := range w.messages {
			lines = append(lines, dimStyle.Render(l))
		}
		lines = append(lines, "", dimStyle.Render(fmt.Sprintf("time remaining %s · esc cancels (the registration is kept)", remaining)))
		inner = lipgloss.JoinVertical(lipgloss.Left, lines...)
	case v.pending != nil:
		req := v.pending
		inner = lipgloss.JoinVertical(lipgloss.Left,
			sectionStyle.Render("Register a host-run server?"),
			"",
			selectedStyle.Render(req.GetName())+" would run "+selectedStyle.Render(req.GetCommand().GetCommand()+" "+strings.Join(req.GetCommand().GetArgs(), " ")),
			"on "+selectedStyle.Render(v.hostName)+" itself — OUTSIDE sandbox isolation.",
			"",
			"It can reach the host's files, network and any credentials it is given.",
			"Only register commands and images you trust.",
			"",
			helpStyle.Render("y register anyway · esc cancel"),
		)
	}
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
