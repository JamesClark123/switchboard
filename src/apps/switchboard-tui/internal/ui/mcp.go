package ui

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/forward"
	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
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
	target   string   // the ssh target of a remote daemon (for the port-forward hint)
	sshOpts  []string // that host's saved ssh options, so the hint's command reaches it the same way
	canSSH   bool     // an ssh host with a target: only then can the hint offer a port forward
	daemon   Daemon
	remote   bool // the daemon is on another machine, so its OAuth callback needs relaying

	servers     []*pb.McpServer
	settings    *pb.McpGatewaySettings
	cursor      int
	loading     bool
	unavailable string // the daemon's reason when the gateway cannot be managed (FR-079)
	status      string
	notes       []string // runtime notes from the last removal (credential material left behind)

	// add form; spec registers its Text field and baseline is vals as the form
	// opened (SWI-9)
	form     *huh.Form
	vals     *mcpFormVals
	spec     *formSpec
	baseline mcpFormVals
	// pending is a host-command registration awaiting the outside-isolation
	// acknowledgement (FR-076); shown as a modal until confirmed or dismissed.
	pending *pb.AddMcpServerRequest

	// auth is the in-flight authorization wait, shown as a modal (FR-075).
	auth *mcpAuthWait
}

type mcpFormVals struct {
	name, kind, url, command, args, dir string
	headers                             string // one "Name: value" per line (remote only, FR-103)
	attach                              bool
}

type mcpAuthWait struct {
	name     string
	url      string
	deadline time.Time
	messages []string
	ch       chan tea.Msg
	cancel   context.CancelFunc

	// Remote daemons only: the provider redirects the browser to
	// http://localhost:<callbackPort>, which is relayed to the host (tunnel) or, if
	// that port is unavailable here, pasted back by the developer.
	callbackPort uint32
	tunnel       bool   // the local relay is listening
	tunnelErr    string // why it could not be, shown with the paste prompt
	noCallback   bool   // remote daemon, but the link carried no loopback callback port
	sshTarget    string // the ssh target for the hint's port-forward command
	sshOpts      []string
	canSSH       bool
	pasting      bool
	paste        textinput.Model
	pasteErr     string
}

type mcpPasteMsg struct {
	host, name string
	err        error
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
	m.mcpView = mcpState{host: row.id, hostName: row.name, target: row.target, sshOpts: row.sshOptions, canSSH: row.kind == "ssh" && row.target != "", daemon: row.daemon, remote: row.kind != "local", loading: true}
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
		w := v.auth
		if w.pasting {
			switch msg.String() {
			case "esc":
				w.pasting = false
			case "enter":
				if url := strings.TrimSpace(w.paste.Value()); url != "" {
					norm, err := checkPastedCallback(url, w.callbackPort)
					if err != nil {
						w.pasteErr = err.Error() // fast feedback before a round trip
						return m, nil
					}
					w.pasteErr = ""
					return m, m.mcpPasteCmd(v.daemon, v.host, w.name, norm)
				}
			default:
				var cmd tea.Cmd
				w.paste, cmd = w.paste.Update(msg)
				return m, cmd
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			w.cancel() // the daemon kills the wait; the end message follows
			v.status = "cancelling authorization of " + w.name + "…"
		case "p":
			if v.remote && w.callbackPort != 0 {
				w.pasting = true
				w.paste.Focus()
			}
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
			return m.requestExit(gMcp)
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
		cmd := m.buildMcpForm()
		if notice := normalizeNotice(v.spec.normalize()); notice != "" {
			v.status = notice
		}
		v.baseline = *v.vals
		return m, cmd
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

// buildMcpForm builds the add form from the open vals with a fresh registry; it
// never fills the vals, so reopenForm keeps what the user typed. The baseline is
// only taken when the form first opens.
func (m *Model) buildMcpForm() tea.Cmd {
	v := &m.mcpView
	v.spec = newFormSpec()
	v.form = m.mcpForm(v.spec, v.vals)
	return v.form.Init()
}

func (m Model) mcpForm(spec *formSpec, v *mcpFormVals) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").
			Description("Letters, digits, dots, hyphens, underscores. Unique on this host.").Value(&v.name),
		huh.NewSelect[string]().Key("kind").Title("Kind").
			Options(huh.NewOption("Remote endpoint (URL)", "remote"), huh.NewOption("Host command (runs on the daemon's host)", "command")).Value(&v.kind),
		huh.NewInput().Key("url").Title("URL").Description("Remote endpoint, e.g. https://mcp.notion.com/mcp").Value(&v.url),
		spec.text(&v.headers, "Headers (remote only)").Key("headers").Lines(3).
			Description("One per line, Name: value. For credentials use a ${placeholder} and store its value as the secret mcp:<name>:<placeholder> (daemon screen › Secrets). Tool-name collisions between servers (e.g. list_issues in both GitHub and Linear) are avoided with the server's own tool-filter headers, e.g. GitHub X-MCP-Toolsets / X-MCP-Tools."),
		huh.NewInput().Key("command").Title("Command").Description("Host command, e.g. npx").Value(&v.command),
		huh.NewInput().Key("args").Title("Arguments").Description("Comma-separated, e.g. @playwright/mcp@latest").Value(&v.args),
		huh.NewInput().Key("dir").Title("Working directory").Description("Optional, host command only.").Value(&v.dir),
		huh.NewConfirm().Key("attach").Title("Attach to every sandbox").
			Description("On = every sandbox launched on this daemon gets this server (FR-080).").Value(&v.attach),
	)).WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(formKeyMap())
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
		return reviveCompleted(gMcp)(m.applyMcpForm())
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
		if strings.TrimSpace(vals.headers) != "" {
			v.status = "headers apply to remote endpoints only"
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
		for _, line := range strings.Split(vals.headers, "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			name, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
				v.status = "header " + strconv.Quote(line) + " must be Name: value"
				return m, nil
			}
			req.Headers = append(req.Headers, strings.TrimSpace(name)+": "+strings.TrimSpace(value))
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
	if u.URL != "" && u.CallbackPort == 0 && m.mcpView.remote {
		// The relay and paste-back both need that port; say so instead of leaving the
		// developer to meet a "localhost refused" page unexplained.
		w.noCallback = true
		w.sshTarget = m.mcpView.target
		w.sshOpts = m.mcpView.sshOpts
		w.canSSH = m.mcpView.canSSH
	}
	if u.CallbackPort != 0 && m.mcpView.remote && w.callbackPort == 0 {
		w.callbackPort = u.CallbackPort
		w.paste = textinput.New()
		w.paste.Placeholder = fmt.Sprintf("http://localhost:%d/…", u.CallbackPort)
		w.paste.CharLimit = 4096
		w.paste.Width = 60
		opener, _ := m.mcpView.daemon.(forward.Opener)
		var err error
		if opener == nil {
			err = errors.New("no relay available for this daemon")
		} else {
			err = m.forwards.OpenAuth(opener, m.mcpView.host, w.name, u.CallbackPort)
		}
		if err != nil {
			// Only an occupied port gets the friendly explanation; any other failure
			// (no such address, permission, sandboxed TUI…) shows the real cause.
			w.tunnelErr = err.Error()
			if isAddrInUse(err) {
				w.tunnelErr = fmt.Sprintf("port %d is in use on this machine (another program, or another sxb waiting on the same authorization)", u.CallbackPort)
			}
			w.pasting = true // fall back straight to paste-back
			w.paste.Focus()
		} else {
			w.tunnel = true
		}
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
	// Release the callback relay first, even if the view has moved on: the wait is
	// over, and a leaked listener would keep the port bound for the next attempt.
	// The key includes the host, so this can only ever close this wait's own relay.
	m.forwards.Close(forward.AuthKey(msg.host, msg.name))
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

// sshForwardArgs is the hint's port-forward command for a host as its already
// shell-quoted words, built from the same saved options its daemon connection uses
// (-p/-i/-J…), so a copy-paste reaches the same machine. -N: forward only, no remote
// shell (and a busy local port fails loudly instead of leaving an unrelated login
// open). PORT is left for the developer to fill from the browser's address bar.
func sshForwardArgs(opts []string, target string) []string {
	args := []string{"ssh", "-N"}
	for _, o := range opts {
		args = append(args, shellQuote(o))
	}
	return append(args, "-L", "PORT:localhost:PORT", shellQuote(target))
}

// sshForwardCommand is sshForwardArgs as one line.
func sshForwardCommand(opts []string, target string) string {
	return strings.Join(sshForwardArgs(opts, target), " ")
}

// wrapArgs packs shell-quoted words into lines of about width characters, each but
// the last ending in a shell line continuation, so a long command still pastes as
// one command (the modal would otherwise hard-wrap it in the middle of a word).
// Each word is atomic — it is never split, whatever quoting it contains — which is
// why this takes the word list rather than re-parsing a command string.
func wrapArgs(args []string, width int) string {
	var lines []string
	line := ""
	for _, a := range args {
		switch {
		case line == "":
			line = a
		case len(line)+1+len(a) > width:
			lines = append(lines, line+" \\")
			line = "  " + a
		default:
			line += " " + a
		}
	}
	return strings.Join(append(lines, line), "\n")
}

// indentLines prefixes every line of s.
func indentLines(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// shellQuote leaves plain words alone and single-quotes anything a shell would
// split or interpret, so options with spaces ("-o ProxyCommand=...") stay one word.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./_~-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// isAddrInUse reports whether a listen failure is "the port is already taken",
// across platforms (Windows reports it with different codes and wording).
func isAddrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	l := strings.ToLower(err.Error())
	return strings.Contains(l, "address already in use") || strings.Contains(l, "only one usage of each socket address")
}

// checkPastedCallback validates a pasted address client-side and returns the form
// to send: it must be the http(s)://localhost:<port>/… address the browser failed
// to load. A missing scheme is added (Safari and address-bar copies drop it). The
// daemon validates again; this only saves a round trip and gives a clearer message.
func checkPastedCallback(raw string, port uint32) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := neturl.Parse(raw)
	// The same loopback rule the daemon applies (libs/switchboard-dial), so the two
	// can never disagree about what counts as a local callback address.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !dial.IsLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("paste the address the browser failed to load: http://localhost:%d/…", port)
	}
	switch p := u.Port(); {
	case p == "":
		return "", fmt.Errorf("that address has no port; expected http://localhost:%d/…", port)
	case p != strconv.Itoa(int(port)):
		return "", fmt.Errorf("that address is for port %s, expected %d", p, port)
	}
	return raw, nil
}

func (m Model) mcpPasteCmd(d Daemon, host, name, url string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		return mcpPasteMsg{host: host, name: name, err: d.CompleteMcpAuthCallback(ctx, name, url)}
	}
}

// applyMcpPaste reports the paste-back outcome; on success the daemon's wait ends
// on its own and the end message follows.
func (m Model) applyMcpPaste(msg mcpPasteMsg) (tea.Model, tea.Cmd) {
	w := m.mcpView.auth
	if w == nil || w.name != msg.name || m.mcpView.host != msg.host {
		return m, nil
	}
	if msg.err != nil {
		w.pasteErr = msg.err.Error()
		return m, nil
	}
	w.pasting = false
	w.messages = append(w.messages, "callback delivered — waiting for the provider to confirm…")
	return m, nil
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
		// The runtime's own TRANSPORT column when present ("remote http"), else
		// the kind; then the auth state, then whatever target/detail it printed.
		kind := srv.GetTransport()
		if kind == "" {
			kind = mcpKindLabel(srv.GetKind())
		}
		tail := srv.GetTarget()
		if tail == "" {
			tail = srv.GetDetail()
		}
		rows = append(rows, fmt.Sprintf("%s%s %s  %s  %s  %s", cursor, mark, pad(srv.GetName(), 18),
			dimStyle.Render(pad(kind, 12)), mcpAuthLabel(srv.GetAuthState()), dimStyle.Render(truncate(tail, 48))))
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
		if w.noCallback {
			// Two ways out. Forwarding the port needs no cancel: this wait keeps running
			// the host's own `sbx mcp auth`, so it is the one that can still finish.
			// The host-side run is the fallback for someone with a browser on the host,
			// and ONLY after cancelling — a second `sbx mcp auth` alongside the running
			// one would race it for the credential.
			lines = append(lines, "",
				dimStyle.Render("no callback port was reported for this link."),
				dimStyle.Render("If the browser ends on a ‘localhost:PORT’ error page"),
				dimStyle.Render("(PORT is in its address bar):"))
			if w.canSSH {
				ssh := wrapArgs(sshForwardArgs(w.sshOpts, w.sshTarget), 44)
				lines = append(lines,
					dimStyle.Render("  • keep this wait running, forward the port:"),
					dimStyle.Render(indentLines(ssh, "      ")),
					dimStyle.Render("    then reload that browser page"),
					dimStyle.Render("  • or, with a browser on the host:"))
			} else {
				lines = append(lines, dimStyle.Render("  • with a browser on the host:"))
			}
			lines = append(lines,
				dimStyle.Render("      esc (cancel this wait), run  sbx mcp auth "+w.name),
				dimStyle.Render("      there, then r to reload the list"))
		}
		switch {
		case w.pasting:
			lines = append(lines, "")
			if w.tunnelErr != "" {
				lines = append(lines, statusErrStyle.Render("could not relay the callback here: "+w.tunnelErr))
			}
			lines = append(lines,
				"After approving, your browser will fail to load http://localhost:"+fmt.Sprint(w.callbackPort)+"/…",
				"Paste that full address here:",
				w.paste.View())
			if w.pasteErr != "" {
				lines = append(lines, statusErrStyle.Render(w.pasteErr))
			}
			lines = append(lines, helpStyle.Render("enter submit · esc back"))
		case w.tunnel:
			lines = append(lines, dimStyle.Render(fmt.Sprintf("callback relayed from localhost:%d to the host · p paste the address instead", w.callbackPort)))
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
