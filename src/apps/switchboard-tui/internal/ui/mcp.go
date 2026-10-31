package ui

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"regexp"
	"slices"
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
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The MCP gateway view (feature 008, US1/US2) manages ONE daemon's registrations
// from the daemon screen: list, register, remove, authorize, mark attach-by-default
// and set the daemon's default attach mode. Every operation goes to that daemon
// only; the list is always re-read from the daemon (which reads the host) after an
// operation and on an mcp_gateway_changed event.

// Client-side bounds (package constants, never env vars; research R11).
const (
	mcpOpTimeout = 2 * time.Minute // register / remove / marks / mode
	// The daemon's worst case: a 10-minute wait for the name's lock, then its own
	// 10-minute save budget and the persist retries; plus slack. A shorter bound
	// would give the form back while the detached save is still queued.
	mcpSaveTimeout = 21 * time.Minute
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

	// hasEdit: the daemon advertised "mcp-edit" when the view opened (SWI-21,
	// R15). Without it the view and form behave exactly as in feature 008.
	hasEdit bool
	// pendings are the pending replaces the list carries (FR-426), shown as rows
	// after the servers.
	pendings []*pb.McpPendingReplace
	// localSaving names this client's own saves that ended UNAVAILABLE: the
	// row shows saving… until the next list says otherwise.
	localSaving map[string]bool
	loadingDef  string // the name whose definition read is in flight (e, R, X)

	// the add/edit form (mcp_form.go); spec registers its Text field and
	// baseline is the snapshot as the form opened (SWI-9)
	form     *huh.Form
	vals     *mcpFormVals
	spec     *formSpec
	baseline mcpSnapshot
	// SWI-21 form state beside the comparable vals (research R14): the entered
	// secret values by full name, the definition read and the definition the
	// form opened with, the fingerprint to echo, the pending replace a Retry
	// form works on (retry) or a failed replace left (holding).
	secretVals map[string]*string
	view       *pb.McpDefinitionView
	opened     *pb.McpServerDefinition
	// filled is the vals the form was pre-filled from (or last reloaded to):
	// what an FR-404 reload counts edits against. It differs from opened in a
	// Retry form, which is filled from the held or previous definition while
	// opened stays pending.previous.
	filled        mcpFormVals
	fingerprint   string
	retry         *pb.McpPendingReplace
	retryPrevious bool
	holding       *pb.McpPendingReplace
	flags         map[string]string // reload flags per field key (FR-404)
	reenter       map[string]string // secret name -> forced status
	declinedHost  map[string]bool   // on-host literal headers whose conversion was declined
	formNote      string
	statusFor     string // the name the add-mode secret status was read for
	saving        bool   // a SaveMcpServer call is in flight
	// remoteStash holds the remote-only fields a switch to "host command"
	// cleared, restored on a switch back (huh's Select sets the value as the
	// highlight moves, so passing over the option must not lose them).
	remoteStash *mcpFormVals
	// formGen identifies the open form: it grows each time a form opens and
	// is never reset, so a reply carrying it (save, reload, refresh) is matched
	// to the form that sent it, not to a later form of the same name.
	formGen uint64
	// pending is a host-command registration awaiting the outside-isolation
	// acknowledgement (FR-076, feature 008 form only); shown as a modal until
	// confirmed or dismissed.
	pending *pb.AddMcpServerRequest

	// outcome is the save's outcome panel (any key closes it); followups are
	// the FR-413 removal and FR-405 authorize offers that open after it.
	outcome   *pb.McpSaveOutcome
	followups []confirmState
	// deferred are the follow-ups of a background save (esc mid-save) that
	// landed while another form was open; they open once the list is idle.
	deferred []confirmState
	// choose is the pending replace whose Retry asks held / previous (R).
	choose *pb.McpPendingReplace
	// scopes is the one-time scope prompt of Authorize with scopes… (S).
	scopes *mcpScopePrompt

	// auth is the in-flight authorization wait, shown as a modal (FR-075).
	auth *mcpAuthWait
}

// mcpScopePrompt is the one-line prompt for one-time scopes (FR-418).
type mcpScopePrompt struct {
	name  string
	input textinput.Model
	err   string
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
	pendings []*pb.McpPendingReplace
	err      error
}

// mcpDefPurpose says what a definition read is for.
type mcpDefPurpose int

const (
	mcpDefForAdd  mcpDefPurpose = iota // add mode's secret status (for_add)
	mcpDefEdit                         // e: open the edit form
	mcpDefRetry                        // R: open the Retry form
	mcpDefCancel                       // X: the Cancel confirmation's secret list
	mcpDefReload                       // FR-404 in-place reload after a STALE refusal
	mcpDefRefresh                      // the view only, after a failed replace
)

// mcpDefMsg is a GetMcpServerDefinition reply.
type mcpDefMsg struct {
	host, name string
	purpose    mcpDefPurpose
	view       *pb.McpDefinitionView
	err        error
	why        string                // the refusal that triggered a reload
	pending    *pb.McpPendingReplace // the row's pending replace (R, X)
	previous   bool                  // R: retry the previous definition
	gen        uint64                // the form that asked (add status, reload, refresh)
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
	m.mcpView = mcpState{host: row.id, hostName: row.name, target: row.target, sshOpts: row.sshOptions, canSSH: row.kind == "ssh" && row.target != "", daemon: row.daemon, remote: row.kind != "local", loading: true,
		hasEdit: row.daemon.HasCapability("mcp-edit")}
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
		resp, err := d.ListMcpGateway(ctx)
		if err != nil {
			return mcpLoadedMsg{host: host, err: err}
		}
		return mcpLoadedMsg{host: host, servers: resp.GetServers(), settings: resp.GetSettings(), pendings: resp.GetPendingReplaces()}
	}
}

func (m Model) applyMcpLoaded(msg mcpLoadedMsg) (tea.Model, tea.Cmd) {
	// A follow-up confirm over the view (FR-405/FR-413 after a save) still
	// takes the list: a save's relist landing behind it must not be lost.
	onView := m.screen == screenMcp || m.screen == screenConfirm && m.confirm.returnTo == screenMcp
	if !onView || m.mcpView.host != msg.host {
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
	m.mcpView.pendings = nil
	if m.mcpView.hasEdit {
		m.mcpView.pendings = msg.pendings
	}
	if n := m.mcpView.rowCount(); m.mcpView.cursor >= n {
		m.mcpView.cursor = max(n-1, 0)
	}
	return m, nil
}

func (m Model) applyMcpDone(msg mcpDoneMsg) (tea.Model, tea.Cmd) {
	// A follow-up confirm over the view (SWI-21) still lets the result land.
	onView := m.screen == screenMcp || m.screen == screenConfirm && m.confirm.returnTo == screenMcp
	if !onView || m.mcpView.host != msg.host {
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
	v := m.mcpView
	switch {
	case v.auth != nil:
		return helpBindings{hkey("esc", "cancel wait")}
	case v.outcome != nil:
		return helpBindings{hkey("any key", "close")}
	case v.choose != nil:
		return helpBindings{hkey("h", "held definition"), hkey("p", "previous definition"), hkey("esc", "back")}
	case v.scopes != nil:
		return helpBindings{hkey("enter", "authorize"), hkey("ctrl+n", "no scopes"), hkey("esc", "back")}
	case v.pending != nil:
		return helpBindings{hkey("y", "register anyway"), hkey("esc", "back to form")}
	case v.form != nil:
		return helpBindings{hkey("tab", "next"), hkey("ctrl+s", "save"), hkey("esc", "cancel")}
	case v.unavailable != "":
		return helpBindings{hkey("r", "retry"), hkey("esc", "back")}
	}
	if !v.hasEdit {
		return helpBindings{
			hkey("a", "register"), hkey("d", "remove"), hkey("A", "authorize"),
			hkey("space", "attach by default"), hkey("m", "attach mode"), hkey("r", "reload"), hkey("esc", "back"),
		}
	}
	if _, ok := m.highlightedPending(); ok {
		return helpBindings{hkey("R", "retry"), hkey("X", "cancel replace"), hkey("a", "register"), hkey("r", "reload"), hkey("esc", "back")}
	}
	return helpBindings{
		hkey("a", "register"), hkey("e", "edit"), hkey("d", "remove"), hkey("A", "authorize"), hkey("S", "authorize with scopes…"),
		hkey("space", "attach by default"), hkey("m", "attach mode"), hkey("r", "reload"), hkey("esc", "back"),
	}
}

// rowCount is the gateway rows: the servers, then the pending replaces.
func (v mcpState) rowCount() int { return len(v.servers) + len(v.pendings) }

func (m Model) highlightedMcp() (*pb.McpServer, bool) {
	v := m.mcpView
	if len(v.servers) == 0 || v.cursor >= len(v.servers) {
		return nil, false
	}
	return v.servers[v.cursor], true
}

// highlightedPending is the pending-replace row under the cursor (FR-426).
func (m Model) highlightedPending() (*pb.McpPendingReplace, bool) {
	v := m.mcpView
	i := v.cursor - len(v.servers)
	if i < 0 || i >= len(v.pendings) {
		return nil, false
	}
	return v.pendings[i], true
}

// Row-state refusals (contracts/mcp-form-ui.md): neutral on purpose, as the
// running save may be this developer's own after UNAVAILABLE.
const (
	mcpSavingMsg     = "a save of this server is still running; try again later"
	mcpRecoveringMsg = "recovering interrupted save; try again shortly"
)

// mcpRowBusy is the refusal for a saving… or recovering row, or "".
func (v mcpState) mcpRowBusy(name string, srv *pb.McpServer) string {
	switch {
	case srv.GetRecovering():
		return mcpRecoveringMsg
	case srv.GetSaveInProgress() || v.localSaving[name]:
		return mcpSavingMsg
	}
	return ""
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
	case v.outcome != nil:
		// Any key closes the panel; the follow-up offers open after it.
		v.outcome = nil
		steps := v.followups
		v.followups = nil
		return m, m.mcpFollowChain(steps)
	case v.choose != nil:
		p := v.choose
		switch msg.String() {
		case "h", "p":
			v.choose = nil
			v.loadingDef, v.status = p.GetName(), mcpLoadingMsg(p.GetName())
			return m, m.mcpDefCmd(p.GetName(), mcpDefRetry, p, msg.String() == "p")
		case "esc", "q", "n":
			v.choose = nil
		}
		return m, nil
	case v.scopes != nil:
		return m.updateMcpScopes(msg)
	case v.pending != nil:
		switch msg.String() {
		case "y", "Y":
			req := v.pending
			v.pending, v.vals, v.spec = nil, nil, nil
			req.AcknowledgeLocalExecution = true
			return m, m.mcpAddCmd(v.daemon, v.host, req)
		case "esc", "n", "N", "q":
			// Back to the form with every value (FR-012a): backing out of the
			// warning is not a discard.
			v.pending = nil
			return m, m.reopenForm(gMcp)
		}
		return m, nil
	case v.form != nil:
		if v.saving {
			// The save is in flight: edits and ctrl+s wait for the reply; esc and
			// ctrl+c leave at once, the save going on (the daemon finishes it).
			switch msg.String() {
			case "esc", "ctrl+c":
				m.resetMcpForm()
				v.status = "saving… (in the background; the result will show here)"
			}
			return m, nil
		}
		return m.formKey(gMcp, msg)
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
		if v.cursor < v.rowCount()-1 {
			v.cursor++
		}
	case "a":
		return m.openMcpAdd(mcpFormVals{kind: "remote"})
	case "e":
		if srv, ok := m.highlightedMcp(); ok && v.hasEdit {
			if busy := v.mcpRowBusy(srv.GetName(), srv); busy != "" {
				v.status = busy // refused locally: no definition read (R11)
				return m, nil
			}
			v.loadingDef, v.status = srv.GetName(), mcpLoadingMsg(srv.GetName())
			return m, m.mcpDefCmd(srv.GetName(), mcpDefEdit, nil, false)
		}
	case "R":
		if p, ok := m.highlightedPending(); ok {
			if v.localSaving[p.GetName()] {
				v.status = mcpSavingMsg
				return m, nil
			}
			v.choose = p
		}
	case "X":
		if p, ok := m.highlightedPending(); ok {
			v.loadingDef, v.status = p.GetName(), mcpLoadingMsg(p.GetName())
			return m, m.mcpDefCmd(p.GetName(), mcpDefCancel, p, false)
		}
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
	case "A", "S":
		if p, ok := m.highlightedPending(); ok {
			v.status = p.GetName() + " is not registered (its replace failed): Retry (R) or Cancel (X) it first"
			return m, nil
		}
		srv, ok := m.highlightedMcp()
		if !ok || (msg.String() == "S" && !v.hasEdit) {
			return m, nil
		}
		if srv.GetKind() == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
			v.status = srv.GetName() + " runs on the host; it has no authorization step"
			return m, nil
		}
		if busy := v.mcpRowBusy(srv.GetName(), srv); busy != "" && v.hasEdit {
			v.status = busy
			return m, nil
		}
		if msg.String() == "S" {
			in := textinput.New()
			in.Placeholder = "read write"
			in.CharLimit = 4096
			in.Width = 50
			in.Focus()
			v.scopes = &mcpScopePrompt{name: srv.GetName(), input: in}
			return m, nil
		}
		return m.startMcpAuth(srv.GetName(), nil, false)
	case " ":
		if srv, ok := m.highlightedMcp(); ok {
			if busy := v.mcpRowBusy(srv.GetName(), srv); busy != "" && v.hasEdit {
				v.status = busy // never sends SetMcpServerDefault (FR-425)
				return m, nil
			}
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

// updateMcpScopes drives the one-time scope prompt (FR-418).
func (m Model) updateMcpScopes(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	sp := v.scopes
	switch msg.String() {
	case "esc":
		v.scopes = nil
		return m, nil
	case "ctrl+n":
		v.scopes = nil
		return m.startMcpAuth(sp.name, nil, true)
	case "enter":
		scopes := mcprules.ParseScopes(sp.input.Value())
		if len(scopes) == 0 {
			sp.err = "enter at least one scope, or ctrl+n for no scopes"
			return m, nil
		}
		if err := mcprules.ValidScopes(scopes); err != nil {
			sp.err = strings.TrimPrefix(ruleMsg(err), "error: ")
			return m, nil
		}
		v.scopes = nil
		return m.startMcpAuth(sp.name, scopes, false)
	}
	var cmd tea.Cmd
	sp.input, cmd = sp.input.Update(msg)
	sp.err = ""
	return m, cmd
}

// mcpDefCmd reads a server's definition for the edit, Retry or Cancel flow.
func (m Model) mcpDefCmd(name string, purpose mcpDefPurpose, p *pb.McpPendingReplace, previous bool) tea.Cmd {
	d, host := m.mcpView.daemon, m.mcpView.host
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		view, err := d.GetMcpServerDefinition(ctx, name, false)
		return mcpDefMsg{host: host, name: name, purpose: purpose, view: view, err: err, pending: p, previous: previous}
	}
}

// applyMcpDef handles a definition read.
func (m Model) applyMcpDef(msg mcpDefMsg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.host != msg.host {
		return m, nil
	}
	switch msg.purpose {
	case mcpDefForAdd:
		if v.mcpFormIs(msg.gen, msg.name) && v.vals.mode == mcpModeAdd && msg.err == nil {
			v.view, v.statusFor = msg.view, msg.name
		}
		return m, nil
	case mcpDefReload:
		return m.reloadMcpForm(msg)
	case mcpDefRefresh:
		if v.mcpFormIs(msg.gen, msg.name) && msg.err == nil {
			v.view, v.fingerprint = msg.view, msg.view.GetFingerprint()
			if p := msg.view.GetPendingReplace(); p != nil {
				v.retry, v.holding = p, p
			}
		}
		return m, nil
	}
	if m.screen != screenMcp || v.loadingDef != msg.name {
		return m, nil // the view moved on
	}
	v.loadingDef = ""
	if v.vals != nil || v.mcpModalOpen() {
		// A form or modal opened while the read was in flight (a, an outcome
		// panel, R's chooser): the late reply must not replace it, and its
		// loading line goes (unless something newer replaced it).
		v.clearMcpLoading(msg.name)
		return m, nil
	}
	view := msg.view
	if msg.err != nil {
		ref, ok := client.AsSaveRefusal(msg.err)
		switch {
		case msg.pending != nil && ok && ref.Reason == pb.McpSaveRefusal_GONE:
			// Neither listed nor pending: the replace ended elsewhere (retried,
			// cancelled or registered by another path). No dead Retry form.
			v.status = mcpPendingEndedMsg(msg.name)
			return m, m.mcpLoadCmd(v.daemon, v.host)
		case msg.pending != nil && (status.Code(msg.err) == codes.NotFound):
			// A pending replace's name is not listed; fall back to what the row
			// carries (secret statuses unknown).
			view = &pb.McpDefinitionView{Name: msg.name, PendingReplace: msg.pending, SecretsAvailable: true,
				PendingRemovalFailedListUnknown: true}
		case ok && ref.Reason == pb.McpSaveRefusal_GONE:
			v.status = ref.Message
			return m, m.offerMcpAddAsNew(msg.name, mcpFormVals{name: msg.name, kind: "remote"}, false)
		default:
			v.status = "error: " + status.Convert(msg.err).Message()
			return m, nil
		}
	}
	switch {
	case view.GetRecovering():
		v.status = mcpRecoveringMsg
		return m, nil
	case view.GetSaveInProgress() && msg.purpose != mcpDefCancel:
		// The row was stale: a save started since. The read takes no lock, so
		// its definition may be changing; do not pre-fill from it (R11).
		v.status = mcpSavingMsg
		return m, m.mcpLoadCmd(v.daemon, v.host)
	}
	v.status = ""
	switch msg.purpose {
	case mcpDefEdit:
		vals := fillMcpVals(view)
		vals.name = msg.name
		return m.openMcpFromView(view, vals, view.GetDefinition(), nil)
	case mcpDefRetry:
		p := view.GetPendingReplace()
		if p == nil {
			// The server is registered again: its replace ended.
			v.status = mcpPendingEndedMsg(msg.name)
			return m, m.mcpLoadCmd(v.daemon, v.host)
		}
		def := p.GetDefinition()
		note := "retrying the failed replace with the held definition"
		if msg.previous {
			def = p.GetPrevious()
			note = "restoring the previous definition; the removed secrets' old values are gone and must be entered again"
		}
		vals := mcpValsFromDef(def, msg.name, p.GetAttachByDefault(), mcpModeRetry)
		return m.openMcpFromView(view, vals, p.GetPrevious(), func(v *mcpState) {
			v.retry, v.holding, v.retryPrevious, v.formNote = p, p, msg.previous, note
		})
	case mcpDefCancel:
		p := view.GetPendingReplace()
		if p == nil {
			v.status = mcpPendingEndedMsg(msg.name)
			return m, m.mcpLoadCmd(v.daemon, v.host)
		}
		return m, m.mcpCancelConfirm(p, view, false)
	}
	return m, nil
}

// mcpPendingEndedMsg says a pending replace a row showed has ended.
func mcpPendingEndedMsg(name string) string {
	return "the pending replace of " + name + " has ended (retried, cancelled or registered by another path); the list was refreshed"
}

// mcpCancelConfirm is the y-only Cancel of a pending replace (FR-406, FR-426):
// from its gateway row (X) or when leaving a form that holds it.
func (m *Model) mcpCancelConfirm(p *pb.McpPendingReplace, view *pb.McpDefinitionView, fromForm bool) tea.Cmd {
	v := &m.mcpView
	name := p.GetName()
	body := []string{
		selectedStyle.Render(name) + " is not registered on " + selectedStyle.Render(v.hostName) + ".",
		"Cancelling drops its attach-by-default mark and switchboard's record of it.",
	}
	switch {
	case view.GetPendingRemovalFailedListUnknown() && len(p.GetRemovalFailedSecrets()) > 0:
		body = append(body, "May be left on the host: "+strings.Join(p.GetRemovalFailedSecrets(), ", "))
	case len(view.GetPendingRemovalFailedListed()) > 0:
		body = append(body, "It will remove: "+strings.Join(view.GetPendingRemovalFailedListed(), ", "))
	}
	st := confirmState{
		title: "Cancel the replace of " + name + "?", body: body,
		warning: "The server stays unregistered.", acceptLabel: "cancel replace", cancelLabel: "keep",
		yOnly: true, returnTo: screenMcp,
		onConfirm: m.mcpCancelCmd(name, p.GetRevision()),
	}
	if fromForm {
		st.next = func(m *Model) tea.Cmd { m.resetMcpForm(); return nil }
		st.onCancel = func(m *Model) tea.Cmd { return m.reviveMcpForm() }
	}
	m.confirm = st
	m.screen = screenConfirm
	return nil
}

func (m Model) mcpCancelCmd(name string, revision uint64) tea.Cmd {
	d, host := m.mcpView.daemon, m.mcpView.host
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		resp, err := d.CancelMcpPendingReplace(ctx, name, revision)
		if err != nil {
			return mcpDoneMsg{host: host, err: err}
		}
		s := "cancelled the replace of " + name + "; it is not registered"
		if l := resp.GetLeftOnHost(); len(l) > 0 {
			s += " — left on the host (remove in Secrets): " + strings.Join(l, ", ")
		}
		if u := resp.GetUnconfirmed(); len(u) > 0 {
			s += " — may be left on the host: " + strings.Join(u, ", ")
		}
		return mcpDoneMsg{host: host, status: s}
	}
}

// mcpLeavePending reports whether leaving the open form must go through the
// Cancel confirmation: it holds a pending replace (FR-406, T032).
func (m Model) mcpLeavePending() (tea.Model, tea.Cmd, bool) {
	v := &m.mcpView
	p := v.pendingHeld()
	if p == nil || v.vals == nil {
		return m, nil, false
	}
	view := v.view
	if view == nil {
		view = &pb.McpDefinitionView{PendingRemovalFailedListUnknown: true}
	}
	cmd := m.mcpCancelConfirm(p, view, true)
	return m, cmd, true
}

// --- outcome panel and follow-ups (FR-405, FR-413, FR-419) ---

// mcpFollowups are the offers after the outcome panel, in order: FR-413
// removal of unused secrets, then FR-405 authorize after a registration.
func (m Model) mcpFollowups(out *pb.McpSaveOutcome, savedRemote bool) []confirmState {
	name := out.GetName()
	var steps []confirmState
	if unused := out.GetUnusedSecrets(); len(unused) > 0 {
		body := []string{"No longer used by " + selectedStyle.Render(name) + ":"}
		for _, s := range unused {
			body = append(body, "  "+s)
		}
		cs, id := mcprules.ClientSecretName(name), mcprules.IdentityName(name)
		if slices.Contains(unused, cs) || slices.Contains(unused, id) {
			body = append(body, "The client ID was cleared (or the client is public): the client secret and",
				"its binding record are no longer needed.")
		}
		body = append(body, "Remove them from the host? Nothing is removed unless you say so.")
		steps = append(steps, confirmState{
			title: "Remove unused secrets?", body: body,
			warning: "Removed secrets cannot be restored.", acceptLabel: "remove", cancelLabel: "keep",
			onConfirm: m.mcpRemoveSecretsCmd(name, unused),
		})
	}
	// The row is best effort (nil when the daemon's re-list failed); the saved
	// definition's kind decides then, so a host command is never offered
	// authorization.
	remote := out.GetServer().GetKind() == pb.McpServerKind_MCP_SERVER_KIND_REMOTE
	if out.GetServer() == nil {
		remote = savedRemote
	}
	// Only a server that needs authorization is offered it: a header-token
	// server (no auth) or one already authorized is not. An unknown state (no
	// row, or the runtime did not say) still offers, as before.
	auth := out.GetServer().GetAuthState()
	needsAuth := auth != pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED && auth != pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE
	if out.GetState() == pb.McpSaveOutcome_REGISTERED && remote && needsAuth {
		st := confirmState{
			title:  "Authorize " + name + " now?",
			body:   []string{selectedStyle.Render(name) + " was registered without authorization."},
			notice: "You can also authorize it later with A.", acceptLabel: "authorize", cancelLabel: "later",
		}
		st.next = func(m *Model) tea.Cmd {
			mm, cmd := m.startMcpAuth(name, nil, false)
			*m = mm.(Model)
			return cmd
		}
		steps = append(steps, st)
	}
	return steps
}

// openDeferredMcpFollowups offers a background save's follow-ups once the
// list is idle: no form, panel or modal open (Update calls it after every
// message).
func (m Model) openDeferredMcpFollowups() (Model, tea.Cmd) {
	v := &m.mcpView
	if len(v.deferred) == 0 || m.screen != screenMcp || v.vals != nil || v.mcpModalOpen() {
		return m, nil
	}
	steps := v.deferred
	v.deferred = nil
	return m, m.mcpFollowChain(steps)
}

// mcpFollowChain opens the follow-up offers one after another; declining one
// moves on to the next.
func (m *Model) mcpFollowChain(steps []confirmState) tea.Cmd {
	if len(steps) == 0 {
		return nil
	}
	st, rest := steps[0], steps[1:]
	st.yOnly, st.returnTo = true, screenMcp
	own := st.next
	st.next = func(m *Model) tea.Cmd {
		var cmd tea.Cmd
		if own != nil {
			cmd = own(m)
		}
		if m.mcpView.auth != nil {
			return cmd // the authorization modal is up; nothing follows it
		}
		return tea.Batch(cmd, m.mcpFollowChain(rest))
	}
	st.onCancel = func(m *Model) tea.Cmd { return m.mcpFollowChain(rest) }
	m.confirm = st
	m.screen = screenConfirm
	return nil
}

func (m Model) mcpRemoveSecretsCmd(name string, secrets []string) tea.Cmd {
	d, host := m.mcpView.daemon, m.mcpView.host
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpOpTimeout)
		defer cancel()
		resp, err := d.RemoveMcpServerSecrets(ctx, name, secrets)
		if err != nil {
			return mcpDoneMsg{host: host, err: err}
		}
		s := "removed " + strings.Join(resp.GetRemoved(), ", ")
		if len(resp.GetRemoved()) == 0 {
			s = "removed nothing"
		}
		if f := resp.GetFailed(); len(f) > 0 {
			s = "error: " + s + "; could not remove " + strings.Join(f, ", ")
		}
		if resp.GetRebindPending() {
			s += " — " + name + " is still marked client re-bind pending"
		}
		return mcpDoneMsg{host: host, status: s, notes: resp.GetHostErrors()}
	}
}

// mcpOutcomeState is a save outcome's state line.
func mcpOutcomeState(out *pb.McpSaveOutcome) string {
	switch out.GetState() {
	case pb.McpSaveOutcome_UNCHANGED_REGISTRATION:
		return "registration unchanged"
	case pb.McpSaveOutcome_STILL_REGISTERED:
		return "still registered, authorization may be lost"
	case pb.McpSaveOutcome_NOT_REGISTERED:
		if out.GetAddStarted() {
			return "not registered — the add may still complete on the host"
		}
		return "not registered"
	}
	return "registered"
}

// mcpOutcomeFailed reports a save whose replace did not complete (FR-406).
func mcpOutcomeFailed(out *pb.McpSaveOutcome) bool {
	return out.GetState() == pb.McpSaveOutcome_STILL_REGISTERED || out.GetState() == pb.McpSaveOutcome_NOT_REGISTERED
}

// mcpOutcomeLines renders a save's outcome (T029, T044).
func (m Model) mcpOutcomeLines(out *pb.McpSaveOutcome) []string {
	name := out.GetName()
	style, title := statusOKStyle, "Saved "+name
	if mcpOutcomeFailed(out) {
		style, title = statusErrStyle, "The save of "+name+" did not complete"
	}
	lines := []string{sectionStyle.Render(title), "", style.Render(mcpOutcomeState(out))}
	for _, e := range out.GetHostErrors() {
		lines = append(lines, statusErrStyle.Render(sanitizeLine(e, 0)))
	}
	list := func(label string, names []string) {
		if len(names) > 0 {
			lines = append(lines, label+strings.Join(names, ", "))
		}
	}
	list("secrets created: ", out.GetCreated())
	list("secrets overwritten: ", out.GetOverwritten())
	list("secrets rolled back: ", out.GetRolledBack())
	list("rollback failed (remove in Secrets): ", out.GetRollbackFailed())
	if t := mcpIdentityText(name, out.GetIdentity()); t != "" {
		lines = append(lines, "", t)
	}
	if p := out.GetPendingReplace(); p != nil {
		lines = append(lines, "", "The replace is pending: save again to retry, or leave the form to cancel it.")
	}
	if len(out.GetAttachedSandboxes()) > 0 {
		lines = append(lines, "")
	}
	for _, sb := range out.GetAttachedSandboxes() {
		sbName := sb.GetDisplayName()
		if sbName == "" {
			sbName = short(sb.GetSandboxId())
		}
		switch {
		case sb.GetSkippedAtLaunch() && sb.GetExclusive():
			lines = append(lines, sbName+": missing from this sandbox's fixed set — relaunch to include it")
		case sb.GetSkippedAtLaunch():
			lines = append(lines, sbName+": not attached at launch — attach it from its MCP overlay (a)")
		default:
			lines = append(lines, sbName+" picks up the new definition after a restart or a re-attach from its MCP overlay")
		}
	}
	return append(lines, "", helpStyle.Render("any key closes"))
}

// mcpIdentityText words the identity-binding outcome (FR-415, T044).
func mcpIdentityText(name string, id pb.McpSaveOutcome_Identity) string {
	cs, rec := mcprules.ClientSecretName(name), mcprules.IdentityName(name)
	retry := "Re-enter the client secret and save to retry the clear, or remove " + rec +
		" in Secrets or the host CLI; the re-bind pending mark clears only on the next save that stores the client secret."
	switch id {
	case pb.McpSaveOutcome_LEFT_IN_PLACE:
		return "The old client binding was left in place: " + name + " is marked client re-bind pending. Enter the new client's secret and save to clear it (for a public client, remove " + cs + " and " + rec + " in Secrets or the host CLI)."
	case pb.McpSaveOutcome_CLEARED:
		return "Cleared the old client binding (" + rec + "): the next authorization binds the new client."
	case pb.McpSaveOutcome_NOT_CLEARED:
		return "The old client binding was not cleared. " + retry
	case pb.McpSaveOutcome_UNCONFIRMED:
		return "Could not confirm the old client binding was cleared (it may still be in place). " + retry
	}
	return ""
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

func (m Model) startMcpAuth(name string, scopes []string, noScope bool) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	ctx, cancel := context.WithTimeout(context.Background(), mcpAuthTimeout)
	ch := make(chan tea.Msg, 32)
	v.auth = &mcpAuthWait{name: name, deadline: time.Now().Add(10 * time.Minute), ch: ch, cancel: cancel}
	v.status = ""
	d, host := v.daemon, v.host
	go func() {
		defer cancel()
		state, err := d.AuthorizeMcpServer(ctx, name, scopes, noScope, func(u client.McpAuthUpdate) {
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
	} else if len(v.servers) == 0 && len(v.pendings) > 0 {
		rows = append(rows, dimStyle.Render("(no MCP servers registered on this host — only the failed replaces below; press a to register one)"))
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
		row := fmt.Sprintf("%s%s %s  %s  %s  %s", cursor, mark, pad(srv.GetName(), 18),
			dimStyle.Render(pad(kind, 12)), mcpAuthLabel(srv.GetAuthState()), dimStyle.Render(truncate(tail, 48)))
		if st := v.mcpRowState(srv); st != "" {
			row += "  " + selectedStyle.Render(st)
		}
		rows = append(rows, row)
	}
	for i, p := range v.pendings {
		cursor := "  "
		if len(v.servers)+i == v.cursor {
			cursor = cursorBarStyle.Render("> ")
		}
		rows = append(rows, cursor+"    "+statusErrStyle.Render(p.GetName()+" — not registered (replace failed "+mcpAge(p)+" ago)")+
			dimStyle.Render("  R retry · X cancel"))
	}
	rows = append(rows, "", dimStyle.Render("[x] = attached to every new sandbox on this daemon"))
	for _, n := range v.notes {
		rows = append(rows, dimStyle.Render("note: "+n))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...) + m.mcpStatusLine()
}

// mcpRowState is a registered server's status column (SWI-21).
func (v mcpState) mcpRowState(srv *pb.McpServer) string {
	switch {
	case srv.GetRecovering():
		return "recovering interrupted save"
	case srv.GetSaveInProgress() || v.localSaving[srv.GetName()]:
		return "saving…"
	case srv.GetRebindPending():
		return "rebind pending"
	}
	return ""
}

// mcpAge is how long ago a pending replace failed, coarsely.
func mcpAge(p *pb.McpPendingReplace) string {
	if p.GetFailedAt() == nil {
		return "a while"
	}
	d := time.Since(p.GetFailedAt().AsTime())
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(int(d.Seconds()), 0))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// mcpModalOpen reports a modal layer over the gateway view.
// mcpLoadingMsg is the status line while name's definition read is in flight.
func mcpLoadingMsg(name string) string { return "loading " + name + "…" }

// clearMcpLoading clears the status when it is still name's loading line.
func (v *mcpState) clearMcpLoading(name string) {
	if name != "" && v.status == mcpLoadingMsg(name) {
		v.status = ""
	}
}

func (v mcpState) mcpModalOpen() bool {
	return v.auth != nil || v.pending != nil || v.outcome != nil || v.choose != nil || v.scopes != nil
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
	case v.outcome != nil:
		inner = lipgloss.JoinVertical(lipgloss.Left, m.mcpOutcomeLines(v.outcome)...)
	case v.choose != nil:
		p := v.choose
		lines := []string{sectionStyle.Render("Retry " + p.GetName()), "",
			"The replace failed " + mcpAge(p) + " ago: " + sanitizeLine(p.GetError(), 0), "",
			"h  the held definition (the one that failed)",
			"p  the previous definition (the last one that was live)"}
		if r := p.GetRemovedSecrets(); len(r) > 0 {
			lines = append(lines, "", "Removed by the failed save (enter them again): "+strings.Join(r, ", "))
		}
		if r := p.GetRemovalFailedSecrets(); len(r) > 0 {
			lines = append(lines, "Removal failed (removed unless re-entered): "+strings.Join(r, ", "))
		}
		lines = append(lines, "", helpStyle.Render("h held · p previous · esc back"))
		inner = lipgloss.JoinVertical(lipgloss.Left, lines...)
	case v.scopes != nil:
		sp := v.scopes
		lines := []string{sectionStyle.Render("Authorize " + sp.name + " with scopes…"), "",
			"Space-separated scopes for this authorization only; the recorded defaults are unchanged.",
			dimStyle.Render("Requested exactly as given; no refresh-token scope is added."), "", sp.input.View()}
		if sp.err != "" {
			lines = append(lines, statusErrStyle.Render(sp.err))
		}
		lines = append(lines, "", helpStyle.Render("enter authorize · ctrl+n no scopes · esc back"))
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
			helpStyle.Render("y register anyway · esc back to form"),
		)
	}
	return modalStyle.Width(m.modalInnerWidth()).Render(inner)
}
