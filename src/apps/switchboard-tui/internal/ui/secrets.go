package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Feature 008 (FR-104). "Secrets" is the second management option on a daemon
// row: the host runtime's secret store (`sbx secret`) for that daemon. The view
// lists every stored secret with its scope, type and the runtime's masked value;
// `a` stores a service secret (the value is typed masked and travels only to
// that daemon, where it is piped to the CLI), `d` removes the highlighted one
// behind a confirmation. Values are never shown or kept client-side.

// secretsOpTimeout bounds one list/set/remove round trip. A package constant.
const secretsOpTimeout = 90 * time.Second

type secretsState struct {
	host, hostName string
	daemon         Daemon
	secrets        []*pb.Secret
	cursor         int
	loading        bool
	unavailable    string // why the host cannot manage secrets
	status         string
	form           *huh.Form
	vals           *secretFormVals
	baseline       secretFormVals // vals as the form opened (SWI-9)
}

type secretFormVals struct {
	name, value, sandbox string
}

type secretsLoadedMsg struct {
	host    string
	secrets []*pb.Secret
	err     error
}

// secretsDoneMsg is the outcome of a set/remove; the view re-lists after it.
type secretsDoneMsg struct {
	host   string
	status string
	err    error
}

// enterSecrets opens the view for a connected daemon row and loads its list.
func (m Model) enterSecrets(row daemonRow) (tea.Model, tea.Cmd) {
	m.secretsView = secretsState{host: row.id, hostName: row.name, daemon: row.daemon, loading: true}
	m.screen = screenSecrets
	if ok, reason := row.daemon.SecretsAvailable(); !ok {
		m.secretsView.loading = false
		if reason == "" {
			reason = "the host's sandbox CLI is missing or below the runtime baseline"
		}
		m.secretsView.unavailable = reason
		return m, nil
	}
	return m, m.secretsLoadCmd(row.daemon, row.id)
}

func (m Model) secretsLoadCmd(d Daemon, host string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), secretsOpTimeout)
		defer cancel()
		secrets, err := d.ListSecrets(ctx)
		return secretsLoadedMsg{host: host, secrets: secrets, err: err}
	}
}

func (m Model) applySecretsLoaded(msg secretsLoadedMsg) (tea.Model, tea.Cmd) {
	v := &m.secretsView
	if m.screen != screenSecrets || v.host != msg.host {
		return m, nil
	}
	v.loading = false
	if msg.err != nil {
		v.status = "could not list secrets: " + msg.err.Error()
		return m, nil
	}
	v.secrets = msg.secrets
	if n := len(v.secrets); v.cursor >= n {
		v.cursor = max(n-1, 0)
	}
	return m, nil
}

func (m Model) applySecretsDone(msg secretsDoneMsg) (tea.Model, tea.Cmd) {
	v := &m.secretsView
	if m.screen != screenSecrets || v.host != msg.host {
		return m, nil
	}
	if msg.err != nil {
		v.status = msg.status + " failed: " + msg.err.Error()
		return m, nil
	}
	v.status = msg.status
	v.loading = true
	return m, m.secretsLoadCmd(v.daemon, v.host)
}

// syncSecretsView reloads an open view when its daemon reports a change made
// elsewhere (Event.secrets_changed), unless a form is being filled.
func (m Model) syncSecretsView(host string) (tea.Model, tea.Cmd) {
	v := &m.secretsView
	if m.screen != screenSecrets || v.host != host || v.form != nil || v.unavailable != "" {
		return m, nil
	}
	v.loading = true
	return m, m.secretsLoadCmd(v.daemon, v.host)
}

func (m Model) secretsHelp() helpBindings {
	v := m.secretsView
	if v.form != nil {
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "store"), hkey("esc", "cancel")}
	}
	if v.unavailable != "" {
		return helpBindings{hkey("r", "retry"), hkey("esc", "back")}
	}
	return helpBindings{hkey("a", "store secret"), hkey("d", "remove"), hkey("r", "reload"), hkey("↑/↓", "move"), hkey("esc", "back")}
}

func (m Model) highlightedSecret() (*pb.Secret, bool) {
	v := m.secretsView
	if v.cursor < 0 || v.cursor >= len(v.secrets) {
		return nil, false
	}
	return v.secrets[v.cursor], true
}

func (m Model) updateSecretsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := &m.secretsView
	if v.form != nil {
		// Like the register form: esc aborts and ctrl+s submits from any field,
		// before huh sees the key (its own submit is enter on the last field).
		switch msg.String() {
		case "esc", "ctrl+c":
			return m.requestExit(gSecrets)
		case "ctrl+s":
			return m.applySecretsForm()
		}
		return m.advanceSecretsForm(msg)
	}
	switch msg.String() {
	case "esc", "q":
		m.screen = screenDaemons
		return m, nil
	case "r":
		if v.unavailable != "" {
			// Re-probe the daemon's availability (it may have been updated).
			if ok, _ := v.daemon.SecretsAvailable(); !ok {
				return m, nil
			}
			v.unavailable = ""
		}
		v.loading, v.status = true, ""
		return m, m.secretsLoadCmd(v.daemon, v.host)
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
		if v.cursor < len(v.secrets)-1 {
			v.cursor++
		}
	case "a":
		v.vals = &secretFormVals{}
		v.form = m.secretsForm(v.vals)
		v.baseline = *v.vals
		v.status = ""
		return m, v.form.Init()
	case "d":
		if s, ok := m.highlightedSecret(); ok {
			if s.GetType() == "custom" {
				v.status = "custom secrets (sbx secret set-custom) are managed on the host"
				return m, nil
			}
			where := "globally"
			if s.GetScope() != "global" {
				where = "for sandbox " + s.GetScope()
			}
			return m.enterConfirm(confirmState{
				title: "Remove secret?",
				body: []string{
					"Remove " + selectedStyle.Render(s.GetName()) + " (" + s.GetType() + ") stored " + where + " on " + selectedStyle.Render(v.hostName) + ".",
					"",
					"Sandboxes and MCP servers that use it lose it on their next request.",
				},
				returnTo:  screenSecrets,
				onConfirm: m.secretRemoveCmd(v.daemon, v.host, s),
			})
		}
	}
	return m, nil
}

// --- the store form ---

func (m Model) secretsForm(v *secretFormVals) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").
			Description("A service id the runtime or a kit expects (anthropic, github, my-service), a gateway header placeholder (mcp:<server>:<placeholder>), or a registry host.").Value(&v.name),
		huh.NewInput().Key("value").Title("Value").EchoMode(huh.EchoModePassword).
			Description("Typed masked; sent only to this daemon and piped to sbx. Single line.").Value(&v.value),
		huh.NewInput().Key("sandbox").Title("Sandbox (optional)").
			Description("Leave empty for a global secret, or name one sandbox to scope it there.").Value(&v.sandbox),
	)).WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(formKeyMap())
}

func (m Model) advanceSecretsForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	v := &m.secretsView
	if v.form == nil {
		return m, nil
	}
	f, cmd := v.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		v.form = ff
	}
	switch v.form.State {
	case huh.StateCompleted:
		return reviveCompleted(gSecrets)(m.applySecretsForm())
	}
	return m, cmd
}

// applySecretsForm validates and sends the store request; the value leaves the
// model the moment the command is built.
func (m Model) applySecretsForm() (tea.Model, tea.Cmd) {
	v := &m.secretsView
	vals := v.vals
	if v.form == nil || vals == nil {
		return m, nil
	}
	name := strings.TrimSpace(vals.name)
	if name == "" || strings.ContainsAny(name, " \t") {
		v.status = "a name without spaces is required"
		return m, nil
	}
	if strings.TrimSpace(vals.value) == "" {
		v.status = "a value is required"
		return m, nil
	}
	req := &pb.SetSecretRequest{Name: name, Value: vals.value, Sandbox: strings.TrimSpace(vals.sandbox)}
	v.form, v.vals = nil, nil
	v.status = "storing " + name + "…"
	return m, m.secretSetCmd(v.daemon, v.host, req)
}

func (m Model) secretSetCmd(d Daemon, host string, req *pb.SetSecretRequest) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), secretsOpTimeout)
		defer cancel()
		s, err := d.SetSecret(ctx, req)
		if err != nil {
			return secretsDoneMsg{host: host, status: "store " + req.GetName(), err: err}
		}
		where := "globally"
		if s.GetScope() != "" && s.GetScope() != "global" {
			where = "for " + s.GetScope()
		}
		return secretsDoneMsg{host: host, status: "stored " + req.GetName() + " " + where}
	}
}

func (m Model) secretRemoveCmd(d Daemon, host string, s *pb.Secret) tea.Cmd {
	req := &pb.RemoveSecretRequest{Name: s.GetName(), Type: s.GetType()}
	if s.GetScope() != "global" {
		req.Sandbox = s.GetScope()
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), secretsOpTimeout)
		defer cancel()
		if err := d.RemoveSecret(ctx, req); err != nil {
			return secretsDoneMsg{host: host, status: "remove " + s.GetName(), err: err}
		}
		return secretsDoneMsg{host: host, status: "removed " + s.GetName()}
	}
}

// --- rendering ---

func (m Model) viewSecrets() string {
	v := m.secretsView
	title := sectionStyle.Render("Secrets · " + v.hostName)
	if v.form != nil {
		return lipgloss.JoinVertical(lipgloss.Left, title, "", v.form.View(), m.secretsStatusLine())
	}
	if v.unavailable != "" {
		return lipgloss.JoinVertical(lipgloss.Left, title, "",
			statusErrStyle.Render("unavailable on this host: "+v.unavailable),
			dimStyle.Render("fix it there, then press r"))
	}
	rows := []string{title, dimStyle.Render("the host runtime's secret store (sbx secret) — values are injected by the host, never shown"), ""}
	if v.loading {
		rows = append(rows, m.spinner.View()+dimStyle.Render(" reading the host's secrets…"))
	} else if len(v.secrets) == 0 {
		rows = append(rows, dimStyle.Render("(no secrets stored on this host — press a to store one)"))
	}
	for i, s := range v.secrets {
		cursor := "  "
		if i == v.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		scope := s.GetScope()
		if scope == "" {
			scope = "global"
		}
		rows = append(rows, cursor+pad(s.GetName(), 28)+"  "+dimStyle.Render(pad(s.GetType(), 9))+"  "+pad(scope, 16)+"  "+dimStyle.Render(s.GetMaskedValue()))
	}
	rows = append(rows, "", dimStyle.Render("gateway header placeholders are secrets named mcp:<server>:<placeholder>"))
	return lipgloss.JoinVertical(lipgloss.Left, rows...) + m.secretsStatusLine()
}

func (m Model) secretsStatusLine() string {
	v := m.secretsView
	if v.status == "" {
		return ""
	}
	style := dimStyle
	if strings.Contains(v.status, "failed") || strings.HasPrefix(v.status, "could not") {
		style = statusErrStyle
	}
	return "\n\n" + style.Render(v.status)
}
