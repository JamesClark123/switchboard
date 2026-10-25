package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/forward"
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

// The real `sbx mcp ls` columns are shown verbatim when the daemon carries them.
func TestMcpViewShowsRealColumns(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{{Name: "notion", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Transport: "remote http", StatusText: "needs auth", Detail: "sbx mcp auth notion", AuthState: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED}}}
	m := mcpModel(t, d)
	v := m.View()
	for _, want := range []string{"remote http", "unauthorized", "sbx mcp auth notion"} {
		if !strings.Contains(v, want) {
			t.Errorf("view should show %q; got:\n%s", want, v)
		}
	}
}

// Custom headers (FR-103): one "Name: value" per line reaches the request; a
// malformed line keeps the form open; a host command refuses them.
func TestMcpRegisterRemoteWithHeaders(t *testing.T) {
	d := &fakeDaemon{}
	m := mcpModel(t, d)
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "gh"
	m.mcpView.vals.url = "https://api.githubcopilot.com/mcp/"
	m.mcpView.vals.headers = "Authorization: Bearer ${token}\n\nAccept: application/json\n"
	m, cmd := update(m, ctrlS())
	m = drainMcp(m, cmd)
	if len(d.addedMcp) != 1 || strings.Join(d.addedMcp[0].GetHeaders(), "|") != "Authorization: Bearer ${token}|Accept: application/json" {
		t.Fatalf("added = %+v", d.addedMcp)
	}
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "bad"
	m.mcpView.vals.url = "https://x/mcp"
	m.mcpView.vals.headers = "NoColon"
	m, _ = update(m, ctrlS())
	if m.mcpView.form == nil || !strings.Contains(m.mcpView.status, "must be Name: value") {
		t.Errorf("malformed header should keep the form open; status %q", m.mcpView.status)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = pressCmd(m, "a")
	m.mcpView.vals.name = "pw"
	m.mcpView.vals.kind = "command"
	m.mcpView.vals.command = "npx"
	m.mcpView.vals.headers = "A: b"
	m, _ = update(m, ctrlS())
	if m.mcpView.form == nil || !strings.Contains(m.mcpView.status, "remote endpoints only") {
		t.Errorf("headers on a host command should be refused; status %q", m.mcpView.status)
	}
}

// openerDaemon lets a fakeDaemon satisfy forward.Opener; its streams never carry
// traffic here, the tests only observe whether the callback listener exists.
type openerDaemon struct{ *fakeDaemon }

func (openerDaemon) ForwardPort(context.Context) (pb.Switchboard_ForwardPortClient, error) {
	return nil, errors.New("not connected")
}

func authFrameModel(t *testing.T, remote bool, port uint32, d Daemon) (Model, *fakeDaemon) {
	t.Helper()
	fd := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}, mcpAuthGate: make(chan struct{}),
		mcpAuthFrames: []client.McpAuthUpdate{{URL: "https://auth.example/authorize", CallbackPort: port}}}
	m := mcpModel(t, fd)
	m.mcpView.remote = remote
	if d == nil {
		d = fd
	} else if od, ok := d.(openerDaemon); ok {
		od.fakeDaemon = fd
		d = od
	}
	m.mcpView.daemon = d
	m, cmd := update(m, press("A"))
	m, _ = update(m, runCmd(cmd))
	return m, fd
}

func TestMcpAuthRemoteRelaysTheCallbackPort(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	m, fd := authFrameModel(t, true, port, openerDaemon{})
	defer close(fd.mcpAuthGate)
	if m.forwards.Port(forward.AuthKey(m.mcpView.host, "notion")) != port || !m.mcpView.auth.tunnel {
		t.Fatalf("a remote authorization should listen on the callback port; port %d", m.forwards.Port(forward.AuthKey(m.mcpView.host, "notion")))
	}
	if !strings.Contains(m.View(), "relayed") {
		t.Errorf("modal should say the callback is relayed:\n%s", m.View())
	}
	m, _ = update(m, press("esc")) // cancel; the end message tears the relay down
	m = drainMcp(m, func() tea.Msg { return mcpAuthEndMsg{host: m.mcpView.host, name: "notion"} })
	if m.forwards.Count() != 0 {
		t.Error("the relay must close when the wait ends")
	}
}

func TestMcpAuthLocalDaemonOpensNoRelay(t *testing.T) {
	m, fd := authFrameModel(t, false, 45678, openerDaemon{})
	defer close(fd.mcpAuthGate)
	if m.forwards.Count() != 0 || m.mcpView.auth.callbackPort != 0 {
		t.Error("a local daemon's callback is already on this machine; no relay")
	}
}

func TestMcpAuthBusyPortFallsBackToPasteBack(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = ln.Close() }()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	m, fd := authFrameModel(t, true, port, openerDaemon{})
	defer close(fd.mcpAuthGate)
	w := m.mcpView.auth
	if w.tunnel || !w.pasting || w.tunnelErr == "" {
		t.Fatalf("a busy port should open the paste prompt; tunnel %v pasting %v err %q", w.tunnel, w.pasting, w.tunnelErr)
	}
	// A wrong paste is rejected client-side with a clear message, before any RPC.
	for _, r := range "http://localhost:1/cb?code=abc" {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, cmd := update(m, press("enter"))
	if cmd != nil || len(fd.mcpPasted) != 0 || !strings.Contains(m.mcpView.auth.pasteErr, "expected") {
		t.Fatalf("a wrong-port paste must be refused locally; pasteErr %q pasted %v", m.mcpView.auth.pasteErr, fd.mcpPasted)
	}
	good := fmt.Sprintf("http://localhost:%d/cb?code=abc", port)
	m.mcpView.auth.paste.SetValue(good)
	m, cmd = update(m, press("enter"))
	m, _ = update(m, runCmd(cmd))
	if len(fd.mcpPasted) != 1 || fd.mcpPasted[0] != good {
		t.Errorf("pasted = %v", fd.mcpPasted)
	}
	if m.mcpView.auth.pasting {
		t.Error("a delivered callback should leave the paste prompt")
	}
}

func TestMcpAuthPKeyOffersPasteBackOnlyWhenRemote(t *testing.T) {
	m, fd := authFrameModel(t, false, 0, nil)
	defer close(fd.mcpAuthGate)
	m, _ = update(m, press("p"))
	if m.mcpView.auth.pasting {
		t.Error("p must do nothing for a local daemon")
	}
}

func TestCheckPastedCallback(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:4000/cb?code=x": "http://localhost:4000/cb?code=x",
		"https://127.0.0.1:4000/":         "https://127.0.0.1:4000/",
		"http://[::1]:4000/cb":            "http://[::1]:4000/cb",
		"localhost:4000/cb?code=x":        "http://localhost:4000/cb?code=x", // browsers that drop the scheme
		"  127.0.0.1:4000/cb  ":           "http://127.0.0.1:4000/cb",
		"HTTP://LOCALHOST:4000/cb":        "HTTP://LOCALHOST:4000/cb",     // hosts are case-insensitive
		"http://localhost.:4000/cb":       "http://localhost.:4000/cb",    // trailing-dot form
		"http://app.localhost:4000/cb":    "http://app.localhost:4000/cb", // RFC 6761 *.localhost
		"http://127.0.0.2:4000/cb":        "http://127.0.0.2:4000/cb",     // any loopback IP, as on the daemon (shared rule)
	} {
		got, err := checkPastedCallback(in, 4000)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "nonsense", "ftp://localhost:4000/", "http://evil.example.com:4000/", "http://localhost:4001/cb"} {
		if _, err := checkPastedCallback(bad, 4000); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	// No port: say so instead of printing 'port , expected N'.
	if _, err := checkPastedCallback("http://localhost/cb", 4000); err == nil || strings.Contains(err.Error(), "port ,") || !strings.Contains(err.Error(), "no port") {
		t.Errorf("empty port message = %v", err)
	}
}

// The relay is released when the wait ends even if the view no longer shows it.
func TestMcpAuthEndClosesRelayEvenWhenViewMovedOn(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	m, fd := authFrameModel(t, true, port, openerDaemon{})
	defer close(fd.mcpAuthGate)
	if m.forwards.Count() != 1 {
		t.Fatal("precondition: relay open")
	}
	m.mcpView.auth = nil // the view was reset under the running wait
	m, _ = update(m, mcpAuthEndMsg{host: m.mcpView.host, name: "notion"})
	if m.forwards.Count() != 0 {
		t.Error("the relay must close when the wait ends")
	}
}

func TestIsAddrInUse(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer func() { _ = ln.Close() }()
	_, err := net.Listen("tcp", ln.Addr().String())
	if !isAddrInUse(err) {
		t.Errorf("a real EADDRINUSE must match: %v", err)
	}
	for _, e := range []error{errors.New("listen tcp 127.0.0.1:80: bind: permission denied"), errors.New("cannot assign requested address")} {
		if isAddrInUse(e) {
			t.Errorf("%v must not match", e)
		}
	}
	if !isAddrInUse(errors.New("Only one usage of each socket address (protocol/network address/port) is normally permitted")) {
		t.Error("the Windows wording must match")
	}
}

// The busy-port text is only for a port that is actually taken; any other failure
// shows its real cause. Port 70000 is out of range, so the bind fails the same way
// everywhere (never EADDRINUSE, whoever runs the test).
func TestMcpAuthOtherListenFailuresShowTheRealCause(t *testing.T) {
	m, fd := authFrameModel(t, true, 70000, openerDaemon{})
	defer close(fd.mcpAuthGate)
	w := m.mcpView.auth
	if w.tunnel || !w.pasting {
		t.Fatalf("a failed bind must fall back to paste-back; tunnel %v pasting %v", w.tunnel, w.pasting)
	}
	if strings.Contains(w.tunnelErr, "is in use") || !strings.Contains(w.tunnelErr, "70000") {
		t.Errorf("a non-EADDRINUSE failure must show the real error; tunnelErr = %q", w.tunnelErr)
	}
}

// A remote daemon whose link carries no loopback callback port tells the developer
// what to do; a local daemon does not.
func TestMcpAuthRemoteWithoutACallbackPortShowsAHint(t *testing.T) {
	m, fd := authFrameModel(t, true, 0, openerDaemon{})
	defer close(fd.mcpAuthGate)
	if !m.mcpView.auth.noCallback {
		t.Fatal("remote + url without a port should set noCallback")
	}
	// The ssh target comes from the daemon row; fall back to its name.
	if got := m.mcpView.auth.sshTarget; got == "" {
		t.Fatalf("the hint needs something to ssh to; got %q", got)
	}
	v := m.View()
	for _, want := range []string{
		"no callback port was reported for this link",
		"ssh -L PORT:localhost:PORT " + m.mcpView.auth.sshTarget,
		"keep this wait running",
		"esc (cancel this wait)",
		"sbx mcp auth notion",
		"then r to reload the list",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("hint missing %q in:\n%s", want, v)
		}
	}
	// The forward needs no cancel, so it comes first; esc belongs to the host-side fallback.
	if strings.Index(v, "ssh -L") > strings.Index(v, "esc (cancel") || strings.Index(v, "esc (cancel") > strings.Index(v, "sbx mcp auth notion") {
		t.Error("order must be: forward the port, then (fallback) esc before the host command")
	}

	ml, fdl := authFrameModel(t, false, 0, openerDaemon{})
	defer close(fdl.mcpAuthGate)
	if ml.mcpView.auth.noCallback || strings.Contains(ml.View(), "no callback port was reported") {
		t.Error("a local daemon needs no relay, so no hint")
	}
}

// With an ssh target on the view, the hint names it.
func TestMcpAuthHintUsesTheDaemonsSSHTarget(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{notionServer()}, mcpAuthGate: make(chan struct{}),
		mcpAuthFrames: []client.McpAuthUpdate{{URL: "https://auth.example/authorize"}}}
	defer close(d.mcpAuthGate)
	m := mcpModel(t, d)
	m.mcpView.remote, m.mcpView.target = true, "dev@build-box"
	m, cmd := update(m, press("A"))
	m, _ = update(m, runCmd(cmd))
	if v := m.View(); !strings.Contains(v, "ssh -L PORT:localhost:PORT dev@build-box") {
		t.Errorf("the hint should name the ssh target; got:\n%s", v)
	}
}
