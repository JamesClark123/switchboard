package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func notionServer() *pb.McpServer {
	return &pb.McpServer{Name: "notion", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Target: "https://mcp.notion.com/mcp", AuthState: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED}
}

// mcpModel opens the gateway view for the (single, local) daemon and drains the
// initial load.
func mcpModel(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	d.mcpAvail = true
	m := sized(New(d, "/work"))
	m, _ = update(m, shiftRight())
	m, _ = update(m, press("enter"))
	m, cmd := update(m, press("enter"))
	if m.screen != screenMcp {
		t.Fatalf("expected the gateway view, got %v", m.screen)
	}
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			m, _ = update(m, msg)
		}
	}
	return m
}

// drainMcp runs a returned command and feeds its message, then re-lists.
func drainMcp(m Model, cmd tea.Cmd) Model {
	for i := 0; i < 5 && cmd != nil; i++ {
		msg := runCmd(cmd)
		if msg == nil {
			break
		}
		m, cmd = update(m, msg)
	}
	return m
}

func TestMcpViewListsRegistrations(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer(), {Name: "pw", Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Target: "npx", AuthState: pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE, AttachByDefault: true}}}
	m := mcpModel(t, d)
	v := m.View()
	for _, want := range []string{"MCP gateway", "notion", "remote", "unauthorized", "pw", "host-run", "[x]", "default attach mode", "additive"} {
		if !strings.Contains(v, want) {
			t.Errorf("view should show %q; got:\n%s", want, v)
		}
	}
	empty := mcpModel(t, &fakeDaemon{})
	if !strings.Contains(empty.View(), "no MCP servers registered") {
		t.Errorf("empty state missing; got:\n%s", empty.View())
	}
}

func TestMcpViewShowsUnavailableReason(t *testing.T) {
	d := &fakeDaemon{mcpReason: "this host is not signed in"}
	m := sized(New(d, "/work"))
	m, _ = update(m, shiftRight())
	m, _ = update(m, press("enter"))
	m, _ = update(m, press("enter"))
	if m.screen != screenMcp || !strings.Contains(m.View(), "not signed in") {
		t.Errorf("unavailable gateway should open with the reason; screen %v view:\n%s", m.screen, m.View())
	}
	// Keys that would operate on the list are inert.
	m, cmd := update(m, press("a"))
	if m.mcpView.form != nil || cmd != nil {
		t.Error("register must be inert while the gateway is unavailable")
	}
}

func TestMcpRegisterRemoteSendsRequest(t *testing.T) {
	d := &fakeDaemon{}
	m := mcpModel(t, d)
	m = pressCmd(m, "a")
	if m.mcpView.form == nil {
		t.Fatal("a should open the register form")
	}
	m.mcpView.vals.name = "linear"
	m.mcpView.vals.url = "https://mcp.linear.app/mcp"
	m.mcpView.vals.attach = true
	m, cmd := update(m, ctrlS())
	m = drainMcp(m, cmd)
	if len(d.addedMcp) != 1 || d.addedMcp[0].GetUrl() != "https://mcp.linear.app/mcp" || !d.addedMcp[0].GetAttachByDefault() || d.addedMcp[0].GetAcknowledgeLocalExecution() {
		t.Fatalf("added = %+v", d.addedMcp)
	}
	if !strings.Contains(m.View(), "linear") || !strings.Contains(m.mcpView.status, "registered linear") {
		t.Errorf("after add the list should show linear; status %q", m.mcpView.status)
	}
	// Validation: a bad name or a non-URL keeps the form open with a message.
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "bad name"
	m, _ = update(m, ctrlS())
	if m.mcpView.form == nil || !strings.Contains(m.mcpView.status, "letters, digits") {
		t.Errorf("bad name should be refused in the form; status %q", m.mcpView.status)
	}
}

func TestMcpRegisterHostCommandRequiresAcknowledgement(t *testing.T) {
	d := &fakeDaemon{}
	m := mcpModel(t, d)
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "pw"
	m.mcpView.vals.kind = "command"
	m.mcpView.vals.command = "npx"
	m.mcpView.vals.args = "@playwright/mcp@latest, --headless"
	m, _ = update(m, ctrlS())
	if m.mcpView.pending == nil || len(d.addedMcp) != 0 {
		t.Fatalf("a host command must wait for the warning; pending %v added %d", m.mcpView.pending != nil, len(d.addedMcp))
	}
	if v := m.View(); !strings.Contains(v, "OUTSIDE sandbox isolation") {
		t.Errorf("warning modal missing; got:\n%s", v)
	}
	m, _ = update(m, press("esc"))
	if m.mcpView.pending != nil || len(d.addedMcp) != 0 {
		t.Fatal("esc must cancel without sending")
	}
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "pw"
	m.mcpView.vals.kind = "command"
	m.mcpView.vals.command = "npx"
	m.mcpView.vals.args = "@playwright/mcp@latest"
	m, _ = update(m, ctrlS())
	m, cmd := update(m, press("y"))
	m = drainMcp(m, cmd)
	if len(d.addedMcp) != 1 || !d.addedMcp[0].GetAcknowledgeLocalExecution() || d.addedMcp[0].GetCommand().GetArgs()[0] != "@playwright/mcp@latest" {
		t.Errorf("acknowledged add = %+v", d.addedMcp)
	}
}

func TestMcpRemoveIsConfirmedAndRelaysNotes(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}, mcpNotes: []string{"sbx secret rm mcp:notion:api-key"}}
	m := mcpModel(t, d)
	var cmd tea.Cmd
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm {
		t.Fatal("remove must be confirm-gated")
	}
	m, _ = update(m, press("n"))
	if len(d.removedMcp) != 0 || m.screen != screenMcp {
		t.Fatal("cancelling must not remove and must return to the gateway view")
	}
	m, _ = update(m, press("d"))
	m, cmd = update(m, press("y"))
	m = drainMcp(m, cmd)
	if len(d.removedMcp) != 1 || d.removedMcp[0] != "notion" {
		t.Errorf("removed = %v", d.removedMcp)
	}
	if v := m.View(); strings.Contains(v, "mcp.notion.com") || !strings.Contains(v, "sbx secret rm") {
		t.Errorf("after remove the row should be gone and the note shown; got:\n%s", v)
	}
}

func TestMcpHostErrorIsShownVerbatimAndListUnchanged(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}}
	m := mcpModel(t, d)
	d.mcpErr = errBoom{}
	m, cmd := update(m, press(" "))
	m = drainMcp(m, cmd)
	if !strings.Contains(m.mcpView.status, "boom") || len(m.mcpView.servers) != 1 || m.mcpView.servers[0].GetAttachByDefault() {
		t.Errorf("status %q servers %+v", m.mcpView.status, m.mcpView.servers)
	}
}

func TestMcpMarkAndModeToggle(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}}
	m := mcpModel(t, d)
	m, cmd := update(m, press(" "))
	m = drainMcp(m, cmd)
	if !d.mcpServers[0].GetAttachByDefault() || !strings.Contains(m.View(), "[x]") {
		t.Errorf("space should mark the server; view:\n%s", m.View())
	}
	m, cmd = update(m, press("m"))
	m = drainMcp(m, cmd)
	if d.mcpSettings.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE || !strings.Contains(m.View(), "exclusive") {
		t.Errorf("m should switch to exclusive; settings %+v", d.mcpSettings)
	}
	m, cmd = update(m, press("m"))
	m = drainMcp(m, cmd)
	if d.mcpSettings.GetDefaultAttachMode() != pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE || !strings.Contains(m.View(), "additive") {
		t.Errorf("m again should switch back to additive; settings %+v", d.mcpSettings)
	}
}

func TestMcpAuthorizeShowsURLCountdownAndCancels(t *testing.T) {
	gate := make(chan struct{})
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}, mcpAuthGate: gate,
		mcpAuthFrames: []client.McpAuthUpdate{{URL: "https://api.notion.com/v1/oauth/authorize?x=1", Deadline: time.Now().Add(9 * time.Minute)}}}
	m := mcpModel(t, d)
	m, cmd := update(m, press("A"))
	if m.mcpView.auth == nil || cmd == nil {
		t.Fatal("A should start an authorization wait")
	}
	// The URL frame arrives first; the modal shows it with a countdown.
	m, cmd = update(m, runCmd(cmd))
	v := m.View()
	if !strings.Contains(v, "https://api.notion.com/v1/oauth/authorize?x=1") || !strings.Contains(v, "time remaining") || !strings.Contains(v, "never opened on the") {
		t.Errorf("auth modal should show the URL and countdown; got:\n%s", v)
	}
	// esc cancels: the fake observes ctx.Done and reports UNAUTHORIZED.
	m, _ = update(m, press("esc"))
	m, cmd = update(m, runCmd(cmd))
	m = drainMcp(m, cmd)
	if m.mcpView.auth != nil || !strings.Contains(m.mcpView.status, "not authorized") {
		t.Errorf("cancel should end the wait as unauthorized; auth %v status %q", m.mcpView.auth != nil, m.mcpView.status)
	}
	if len(d.mcpAuthed) != 0 {
		t.Error("a cancelled wait must not count as authorized")
	}
	close(gate)
}

func TestMcpAuthorizeSuccessUpdatesRow(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}, mcpAuthState: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED}
	m := mcpModel(t, d)
	m, cmd := update(m, press("A"))
	m = drainMcp(m, cmd)
	if m.mcpView.auth != nil || !strings.Contains(m.mcpView.status, "authorized notion") || len(d.mcpAuthed) != 1 {
		t.Errorf("auth %v status %q authed %v", m.mcpView.auth != nil, m.mcpView.status, d.mcpAuthed)
	}
	// Local servers have no authorization step.
	d.mcpServers = append(d.mcpServers, &pb.McpServer{Name: "pw", Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL})
	m, cmd = update(m, press("r"))
	m = drainMcp(m, cmd)
	m, _ = update(m, press("j"))
	m, cmd = update(m, press("A"))
	if cmd != nil || !strings.Contains(m.mcpView.status, "no authorization step") {
		t.Errorf("A on a host-run server should explain; status %q", m.mcpView.status)
	}
}

func TestMcpGatewayChangedEventReloadsOpenView(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}}
	m := mcpModel(t, d)
	d.mcpServers = append(d.mcpServers, &pb.McpServer{Name: "linear", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE})
	// Another host's change is ignored; this host's re-lists.
	m, cmd := update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_McpGatewayChanged_{McpGatewayChanged: &pb.Event_McpGatewayChanged{HostId: "elsewhere"}}}})
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			m, _ = update(m, msg)
		}
	}
	if strings.Contains(m.View(), "linear") {
		t.Error("an event for another host must not reload this view")
	}
	m, cmd = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_McpGatewayChanged_{McpGatewayChanged: &pb.Event_McpGatewayChanged{HostId: d.HostID()}}}})
	m = drainMcp(m, cmd)
	if !strings.Contains(m.View(), "linear") {
		t.Errorf("the open view should re-list on its host's event; got:\n%s", m.View())
	}
}

func TestSandboxRowShowsAttachedMcpServers(t *testing.T) {
	d := &fakeDaemon{}
	sbs := []*pb.Sandbox{
		{Id: "a", DisplayName: "with", State: pb.SandboxState_SANDBOX_STATE_RUNNING, McpServers: []string{"notion", "linear"}, McpAttachMode: pb.McpAttachMode_MCP_ATTACH_MODE_EXCLUSIVE},
		{Id: "b", DisplayName: "without", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
	}
	m := withSandboxes(sized(New(d, "/work")), sbs)
	v := m.View()
	if !strings.Contains(v, "mcp: notion, linear (exclusive)") {
		t.Errorf("row should show the attached servers and mode; got:\n%s", v)
	}
	if strings.Count(v, "mcp:") != 1 {
		t.Errorf("a sandbox without servers must show no mcp badge; got:\n%s", v)
	}
}

// Branch coverage for the register form's validation and the view's lesser states.
func TestMcpFormValidationAndLesserStates(t *testing.T) {
	d := &fakeDaemon{}
	m := mcpModel(t, d)
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "pw"
	m.mcpView.vals.kind = "command"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.mcpView.status, "host command is required") {
		t.Errorf("status = %q", m.mcpView.status)
	}
	m.mcpView.vals.kind = "remote"
	m.mcpView.vals.url = "ftp://nope"
	m, _ = update(m, ctrlS())
	if !strings.Contains(m.mcpView.status, "URL") {
		t.Errorf("status = %q", m.mcpView.status)
	}
	m, _ = update(m, press("esc"))
	if m.mcpView.form != nil {
		t.Error("esc should close the form")
	}
	// Messages for another host or a stale wait are ignored.
	before := m.mcpView.status
	m, _ = update(m, mcpDoneMsg{host: "elsewhere", status: "x"})
	m, _ = update(m, mcpAuthEndMsg{host: "elsewhere", name: "n"})
	m, _ = update(m, mcpAuthMsg{host: "elsewhere", name: "n"})
	if m.mcpView.status != before {
		t.Error("foreign-host messages must not touch this view")
	}
	// A list failure shows the reason in place; r retries.
	d.mcpListErr = errBoom{}
	m, cmd := update(m, press("r"))
	m = drainMcp(m, cmd)
	if !strings.Contains(m.View(), "unavailable on this host: boom") {
		t.Errorf("list failure should render as unavailable; got:\n%s", m.View())
	}
	d.mcpListErr = nil
	m, cmd = update(m, press("r"))
	m = drainMcp(m, cmd)
	if m.mcpView.unavailable != "" {
		t.Error("a successful reload clears the unavailable state")
	}
	// The auth modal before any URL arrived shows a waiting line; an auth error ends it.
	d.mcpServers = []*pb.McpServer{notionServer()}
	m, cmd = update(m, press("r"))
	m = drainMcp(m, cmd)
	gate := make(chan struct{})
	d.mcpAuthGate = gate
	m, _ = update(m, press("A"))
	if v := m.View(); !strings.Contains(v, "waiting for the host to start") {
		t.Errorf("pre-URL modal missing; got:\n%s", v)
	}
	w := m.mcpView.auth
	m, _ = update(m, mcpAuthMsg{host: m.mcpView.host, name: "notion", update: client.McpAuthUpdate{Message: "step 1"}})
	if len(w.messages) != 1 {
		t.Errorf("messages = %v", w.messages)
	}
	m, _ = update(m, mcpAuthEndMsg{host: m.mcpView.host, name: "notion", err: errBoom{}})
	if m.mcpView.auth != nil || !strings.HasPrefix(m.mcpView.status, "error") {
		t.Errorf("auth error should end the wait with the error; status %q", m.mcpView.status)
	}
	close(gate)
	// Cursor movement and labels.
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	if m.mcpView.cursor != 0 {
		t.Errorf("cursor = %d", m.mcpView.cursor)
	}
	if mcpAuthLabel(pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN) == "" || mcpKindLabel(pb.McpServerKind_MCP_SERVER_KIND_LOCAL) != "host-run" {
		t.Error("labels")
	}
}
