package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SWI-21 form tests (T026–T030, T032, T037–T039, T043, T046).

// runQuick runs a (possibly batched) command, dropping any that block (cursor
// blinks), and flattens the messages.
func runQuick(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, runQuick(c)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

// drainEdit feeds back the gateway's own messages a command produces, until
// none are left (huh's internal messages are dropped).
func drainEdit(m Model, cmd tea.Cmd) Model {
	queue := runQuick(cmd)
	for i := 0; i < 50 && len(queue) > 0; i++ {
		msg := queue[0]
		queue = queue[1:]
		switch msg.(type) {
		case mcpDefMsg, mcpSaveMsg, mcpLoadedMsg, mcpDoneMsg, mcpFollowupsReadyMsg:
			var c tea.Cmd
			m, c = update(m, msg)
			queue = append(queue, runQuick(c)...)
		}
	}
	return m
}

// key feeds one key and drains.
func keyIn(m Model, k tea.KeyMsg) Model {
	m, cmd := update(m, k)
	return drainEdit(m, cmd)
}

func refused(reason pb.McpSaveRefusal_Reason, msg string) error {
	return &client.SaveRefusal{Reason: reason, Message: msg, Code: codes.FailedPrecondition}
}

func linearView() *pb.McpDefinitionView {
	return &pb.McpDefinitionView{
		Name: "linear", Fingerprint: "fp-1", HasRecord: true, SecretsAvailable: true,
		AuthState: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED,
		Definition: &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://mcp.linear.app/mcp",
			Headers: []string{"Authorization: Bearer ${api-key}"}, Scopes: []string{"read"}},
		StoredSecrets: []string{"mcp:linear:api-key"},
	}
}

func linearServer() *pb.McpServer {
	return &pb.McpServer{Name: "linear", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Target: "https://mcp.linear.app/mcp", AuthState: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED}
}

// editModel opens the gateway view on an "mcp-edit" daemon.
func editModel(t *testing.T, d *fakeDaemon) Model {
	t.Helper()
	d.mcpEdit = true
	m := mcpModel(t, d)
	if !m.mcpView.hasEdit {
		t.Fatal("the view should record the mcp-edit capability")
	}
	return m
}

// openEdit presses e on the first row and drains the definition read.
func openEdit(t *testing.T, m Model) Model {
	t.Helper()
	m = keyIn(m, press("e"))
	if m.mcpView.form == nil {
		t.Fatalf("e should open the edit form; status %q", m.mcpView.status)
	}
	return m
}

// leaveGroup1 moves focus past group 1's last field the way tab does, so the
// form's own group change reaches advanceMcpForm.
func leaveGroup1(t *testing.T, m Model) Model {
	t.Helper()
	// Rebuild from the vals first: a test sets them directly, and a blurring
	// field would otherwise write its stale widget value back.
	_ = m.reopenForm(gMcp)
	f := m.mcpView.form
	for i := 0; i < 20 && f.GetFocusedField().GetKey() != "attach"; i++ {
		f.NextField()
	}
	for _, msg := range runQuick(f.NextField()) {
		if fmt.Sprintf("%T", msg) == "huh.nextGroupMsg" {
			var cmd tea.Cmd
			m, cmd = update(m, msg)
			return drainEdit(m, cmd)
		}
	}
	t.Fatal("no group change from the last field")
	return m
}

// accept answers the open confirm with y.
func accept(t *testing.T, m Model) Model {
	t.Helper()
	if m.screen != screenConfirm {
		t.Fatalf("expected a confirm; screen %v status %q", m.screen, m.mcpView.status)
	}
	return keyIn(m, press("y"))
}

func TestMcpCapabilityGateKeepsOldForm(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}}
	m := mcpModel(t, d)
	if m.mcpView.hasEdit {
		t.Fatal("no capability, no edit")
	}
	m = keyIn(m, press("e"))
	if m.mcpView.form != nil || len(d.mcpDefReads) != 0 {
		t.Error("e is inert without mcp-edit")
	}
	m = keyIn(m, press("S"))
	if m.mcpView.scopes != nil {
		t.Error("S is inert without mcp-edit")
	}
	if strings.Contains(helpLine(m.mcpHelp()), "edit") {
		t.Error("help should not offer edit")
	}
	m = pressCmd(m, "a")
	if strings.Contains(m.View(), "Scopes") || strings.Contains(m.View(), "Register an MCP server @") {
		t.Errorf("the feature-008 form has no scopes; got:\n%s", m.View())
	}

	d2 := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}}
	m2 := editModel(t, d2)
	if !strings.Contains(helpLine(m2.mcpHelp()), "edit") || !strings.Contains(helpLine(m2.mcpHelp()), "scopes") {
		t.Errorf("help = %s", helpLine(m2.mcpHelp()))
	}
}

func TestMcpEditOpensPrefilledForm(t *testing.T) {
	view := linearView()
	view.Flags = []*pb.McpFieldFlag{
		{Field: "url", Kind: pb.McpFieldFlag_CHANGED_ON_HOST, HostValue: "https://mcp.linear.app/mcp", RecordedValue: "https://old/mcp"},
		{Field: "scopes", Kind: pb.McpFieldFlag_NOT_RECOVERABLE},
		{Field: "headers/X-Thing", Kind: pb.McpFieldFlag_RESOLVED_UNCHECKABLE},
		{Field: "oauth_authorization_server", Kind: pb.McpFieldFlag_CHANGED_ON_HOST},
	}
	view.AttachByDefault = true
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := openEdit(t, editModel(t, d))
	v := m.mcpView
	if v.vals.mode != mcpModeEdit || v.vals.url != "https://mcp.linear.app/mcp" || v.vals.headers != "Authorization: Bearer ${api-key}" ||
		v.vals.scopes != "read" || !v.vals.attach || v.fingerprint != "fp-1" {
		t.Fatalf("vals = %+v fp %q", *v.vals, v.fingerprint)
	}
	out := m.View()
	for _, want := range []string{"Edit linear @", "changed on the host", "not recoverable from the host"} {
		if !strings.Contains(out, want) {
			t.Errorf("view should show %q:\n%s", want, out)
		}
	}
	if got := v.fieldFlags("headers"); !strings.Contains(got, "X-Thing: host value cannot be checked against the template") {
		t.Errorf("headers flags = %q", got)
	}
	if got := v.fieldFlags("auth_server"); got != "\n⚑ changed on the host" {
		t.Errorf("a change with no values to show = %q", got)
	}
	if f := v.form.GetFocusedField(); f.GetKey() == "name" {
		t.Error("the name is not a field in edit mode")
	}
	if m.guardDirty(gMcp) {
		t.Error("an untouched edit form is clean")
	}
}

func TestMcpMarkOnlySaveDoesNotReregister(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{out: &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_UNCHANGED_REGISTRATION}}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m = keyIn(m, ctrlS())
	if len(d.mcpSaves) != 1 {
		t.Fatalf("a mark-only save needs no confirm; screen %v status %q", m.screen, m.mcpView.status)
	}
	req := d.mcpSaves[0]
	if req.GetIsNew() || req.GetFingerprint() != "fp-1" || !req.GetAttachByDefault() || req.GetConfirmDiscardAuthorization() || len(req.GetSecretValues()) != 0 {
		t.Errorf("req = %+v", req)
	}
	if m.mcpView.form != nil || m.mcpView.outcome == nil || !strings.Contains(m.View(), "registration unchanged") {
		t.Errorf("the outcome panel should show; view:\n%s", m.View())
	}
	m = keyIn(m, press("x"))
	if m.mcpView.outcome != nil || m.screen != screenMcp {
		t.Errorf("any key closes; no follow-up for an unchanged registration (screen %v)", m.screen)
	}
}

func TestMcpURLEditConfirmsAndBacksOut(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.url = "https://mcp.linear.app/v2?api_key=abc"
	m = keyIn(m, ctrlS())
	// 1. the URL credential warning; back out keeps every value.
	if m.screen != screenConfirm || !strings.Contains(m.View(), "api_key") {
		t.Fatalf("expected the credential warning:\n%s", m.View())
	}
	m = keyIn(m, press("n"))
	if m.screen != screenMcp || m.mcpView.form == nil || m.mcpView.vals.url != "https://mcp.linear.app/v2?api_key=abc" || len(d.mcpSaves) != 0 {
		t.Fatal("back-out returns to the form with its values")
	}
	m.mcpView.vals.url = "https://mcp.linear.app/v2"
	m = keyIn(m, ctrlS())
	// 2. authorization will be discarded.
	if !strings.Contains(m.View(), "authorization is discarded") {
		t.Fatalf("expected the discard warning:\n%s", m.View())
	}
	if m.confirm.yOnly != true {
		t.Error("save-sequence confirms are y-only")
	}
	m = keyIn(m, press("enter"))
	if m.screen != screenConfirm {
		t.Fatal("enter must not accept a y-only confirm")
	}
	m = accept(t, m)
	// 3. the header secret must be re-entered after a URL change.
	if !strings.Contains(m.View(), "will not be sent until set again") || !strings.Contains(m.View(), "mcp:linear:api-key") {
		t.Fatalf("expected the secrets prompt:\n%s", m.View())
	}
	m = accept(t, m)
	if len(d.mcpSaves) != 1 {
		t.Fatalf("saves = %d", len(d.mcpSaves))
	}
	req := d.mcpSaves[0]
	if !req.GetConfirmDiscardAuthorization() || req.GetDefinition().GetUrl() != "https://mcp.linear.app/v2" || req.GetIsNew() {
		t.Errorf("req = %+v", req)
	}
	// Replaced remote: outcome, then the FR-405 authorize offer.
	m = keyIn(m, press("x"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Authorize linear now?") {
		t.Fatalf("expected the authorize offer:\n%s", m.View())
	}
	d.mcpAuthState = pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED
	m, _ = update(m, press("y"))
	if m.mcpView.auth == nil || m.mcpView.auth.name != "linear" {
		t.Error("y starts the existing authorization modal")
	}
}

// A save's relist that lands while its FR-405 authorize offer is open is
// applied, not dropped: otherwise the view keeps the pre-save rows (a Retry's
// pending row stays and e does nothing).
func TestMcpRelistBehindFollowupConfirmIsApplied(t *testing.T) {
	out := &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_REGISTERED,
		Server: &pb.McpServer{Name: "linear", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, AuthState: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED}}
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{out: out}}}
	m := openEdit(t, editModel(t, d))
	m = keyIn(m, ctrlS())
	m = keyIn(m, press("x"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Authorize linear now?") {
		t.Fatalf("expected the authorize offer:\n%s", m.View())
	}
	fresh := []*pb.McpServer{{Name: "notion", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE}}
	m, _ = update(m, mcpLoadedMsg{host: m.mcpView.host, servers: fresh})
	m = keyIn(m, press("n"))
	if m.screen != screenMcp || len(m.mcpView.servers) != 1 || m.mcpView.servers[0].GetName() != "notion" {
		t.Fatalf("the relist behind the offer was dropped: screen %v servers %v", m.screen, m.mcpView.servers)
	}
}

func TestMcpAddHostCommandAcknowledges(t *testing.T) {
	d := &fakeDaemon{}
	m := editModel(t, d)
	m = pressCmd(m, "a")
	if !strings.Contains(m.View(), "Register an MCP server @") || !strings.Contains(m.View(), "Scopes") {
		t.Fatalf("add form:\n%s", m.View())
	}
	v := &m.mcpView
	v.vals.name, v.vals.kind, v.vals.command, v.vals.args = "pw", "command", "npx", "@playwright/mcp, --token=x"
	m = keyIn(m, ctrlS())
	m = accept(t, m) // argument credential warning (--token)
	if !strings.Contains(m.View(), "OUTSIDE sandbox isolation") {
		t.Fatalf("expected the local-execution warning:\n%s", m.View())
	}
	m = keyIn(m, press("esc"))
	if m.mcpView.form == nil || m.mcpView.vals.command != "npx" {
		t.Fatal("back-out keeps the form")
	}
	m = keyIn(m, ctrlS())
	m = accept(t, m)
	m = accept(t, m)
	if len(d.mcpSaves) != 1 || !d.mcpSaves[0].GetAcknowledgeLocalExecution() || !d.mcpSaves[0].GetIsNew() || d.mcpSaves[0].GetFingerprint() != "" ||
		d.mcpSaves[0].GetDefinition().GetCommand().GetArgs()[1] != "--token=x" {
		t.Fatalf("saves = %+v", d.mcpSaves)
	}
	// The add mode read the secret status for the typed name (for_add).
	found := false
	for _, r := range d.mcpDefReads {
		found = found || r == "pw/true"
	}
	if found {
		t.Log("for_add read on name change")
	}
}

func TestMcpAddReadsSecretStatusOnNameChange(t *testing.T) {
	d := &fakeDaemon{mcpViews: map[string]*pb.McpDefinitionView{"gh": {Name: "gh", SecretsAvailable: true, StoredSecrets: []string{"mcp:gh:tok"}}}}
	m := editModel(t, d)
	m = pressCmd(m, "a")
	m.mcpView.form.NextField() // focus is on name; type into it
	m = pressCmd(m, "shift+tab")
	for _, r := range "gh" {
		m = keyIn(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.mcpView.vals.name != "gh" || m.mcpView.statusFor != "gh" {
		t.Fatalf("name %q statusFor %q reads %v", m.mcpView.vals.name, m.mcpView.statusFor, d.mcpDefReads)
	}
	m.mcpView.vals.headers = "X-Api-Key: ${tok}\nX-Org: ${org}"
	if got := m.mcpView.secretStatus(mcpSecretField{name: "mcp:gh:tok"}); got != mcpStatusSet {
		t.Errorf("tok = %q", got)
	}
	if got := m.mcpView.secretStatus(mcpSecretField{name: "mcp:gh:org"}); got != mcpStatusMissing {
		t.Errorf("org = %q", got)
	}
}

func TestMcpSecretsGroupFollowsHeaders(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Authorization: Bearer ${api-key}\nX-Org: ${org}"
	m.mcpView.vals.clientID = "cid"
	m = leaveGroup1(t, m)
	if m.mcpView.focusInDefinition() {
		t.Fatal("focus should be in the secrets group")
	}
	out := m.View()
	for _, want := range []string{"mcp:linear:api-key", "set (blank keeps it)", "mcp:linear:org", "missing", "mcp:linear:client_secret", "Needed only for a confidential client"} {
		if !strings.Contains(out, want) {
			t.Errorf("secrets group should show %q:\n%s", want, out)
		}
	}
	// Typing a secret makes the form dirty (T032).
	*m.mcpView.secretPtr("mcp:linear:org") = "o-1"
	if !m.guardDirty(gMcp) {
		t.Error("a non-empty masked field is an edit")
	}
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm { // the new client ID re-registers: auth discard
		m = accept(t, m)
	}
	if len(d.mcpSaves) != 1 {
		t.Fatalf("saves = %d; screen %v %q", len(d.mcpSaves), m.screen, m.mcpView.status)
	}
	if got := d.mcpSaves[0].GetSecretValues(); len(got) != 1 || got["mcp:linear:org"] != "o-1" {
		t.Errorf("secret values = %v (blank fields are left out)", got)
	}
}

func TestMcpSecretsGroupWithNoSecretsDoesNotPanic(t *testing.T) {
	view := linearView()
	view.Definition.Headers = nil
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := openEdit(t, editModel(t, d))
	m = leaveGroup1(t, m)
	if m.mcpView.focusInDefinition() || !strings.Contains(m.View(), "No secrets needed") {
		t.Errorf("group 2 with only its note:\n%s", m.View())
	}
	// Unavailable storage: the note carries the reason, no inputs.
	view.SecretsAvailable, view.SecretsUnavailableReason = false, "not signed in"
	view.Definition.Headers = []string{"X-Api-Key: ${k}"}
	m2 := openEdit(t, editModel(t, &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}))
	m2 = leaveGroup1(t, m2)
	if !strings.Contains(m2.View(), "not signed in") || strings.Contains(m2.View(), "mcp:linear:k") && strings.Contains(m2.View(), "missing") {
		t.Errorf("unavailable storage:\n%s", m2.View())
	}
	*m2.mcpView.secretPtr("mcp:linear:k") = "v"
	m2 = keyIn(m2, ctrlS())
	if !strings.Contains(m2.mcpView.status, "unavailable") {
		t.Errorf("a save needing a new secret is refused; status %q", m2.mcpView.status)
	}
}

func TestMcpPlaceholderRulesNameThePlaceholder(t *testing.T) {
	d := &fakeDaemon{}
	m := editModel(t, d)
	for _, c := range []struct{ headers, scopes, want string }{
		{"X-Api-Key: ${client_secret}", "", "reserved"},
		{"X-Api-Key: ${a:identity}", "", "${a:identity}"},
		{"X-Mode: on", "read,write", "comma"},
		{"X-Mode: on", "-x", "must not start"},
		{"bad header", "", "Name: value"},
	} {
		m = pressCmd(m, "a")
		v := &m.mcpView
		v.vals.name, v.vals.url, v.vals.headers, v.vals.scopes = "x", "https://x/mcp", c.headers, c.scopes
		m = keyIn(m, ctrlS())
		if m.mcpView.form == nil || !strings.Contains(m.mcpView.status, c.want) || len(d.mcpSaves) != 0 {
			t.Errorf("%+v: status %q", c, m.mcpView.status)
		}
		m = escDiscard(t, m)
	}
	m = pressCmd(m, "a")
	v := &m.mcpView
	v.vals.name, v.vals.kind, v.vals.command, v.vals.scopes = "pw", "command", "npx", "read"
	m = keyIn(m, ctrlS())
	if !strings.Contains(m.mcpView.status, "scopes apply to remote") {
		t.Errorf("scopes on a host command: %q", m.mcpView.status)
	}
}

func TestMcpKindChangeClearsAndRestoresRemoteFields(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	// Focus is on kind (the first field in edit mode); down selects "command"
	// (huh's Select sets the value as the highlight moves).
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.mcpView.vals.kind != "command" || m.mcpView.vals.url != "" || m.mcpView.vals.headers != "" || m.mcpView.vals.scopes != "" ||
		!strings.Contains(m.mcpView.status, "cleared") || !strings.Contains(m.mcpView.status, "restore") {
		t.Errorf("vals %+v status %q", *m.mcpView.vals, m.mcpView.status)
	}
	// Moving back to remote restores every cleared field: passing over the
	// option loses nothing, and the form is clean again.
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyUp})
	v := m.mcpView
	if v.vals.kind != "remote" || v.vals.url != "https://mcp.linear.app/mcp" || v.vals.headers != "Authorization: Bearer ${api-key}" ||
		v.vals.scopes != "read" || !strings.Contains(v.status, "restored") || v.remoteStash != nil {
		t.Errorf("restore: vals %+v status %q", *v.vals, v.status)
	}
	if m.guardDirty(gMcp) {
		t.Error("down then up is no edit")
	}
	if f := v.form.GetFocusedField(); f == nil || f.GetKey() != "kind" {
		t.Error("focus stays on kind across the rebuild")
	}
	// Remote values typed while "host command" was selected win over the stash.
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyDown})
	m.mcpView.vals.url, m.mcpView.vals.kind = "https://typed/mcp", "remote"
	if m.mcpView.switchMcpKind() || m.mcpView.vals.url != "https://typed/mcp" || m.mcpView.remoteStash != nil {
		t.Errorf("typed remote values are kept: %+v", *m.mcpView.vals)
	}
}

// A Cookie header is offered once per cookie, each naming its cookie, and each
// secret is named after its cookie (review round 3).
func TestMcpCookieConversionNamesEachCookie(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Cookie: session=abc; csrf=def"
	m = leaveGroup1(t, m)
	for _, c := range []string{"session", "csrf"} {
		if !strings.Contains(m.View(), "Convert the "+c+" cookie to a secret?") {
			t.Fatalf("expected the %s offer:\n%s", c, m.View())
		}
		m = accept(t, m)
	}
	v := m.mcpView
	if v.vals.headers != "Cookie: session=${cookie-session}; csrf=${cookie-csrf}" ||
		v.secretValue("mcp:linear:cookie-session") != "abc" || v.secretValue("mcp:linear:cookie-csrf") != "def" {
		t.Errorf("headers %q secrets %v", v.vals.headers, v.secretVals)
	}
}

// Review round 4: an empty cookie value is not offered for conversion (it
// used to panic in mcprules.Convert and kill the TUI); the cookie after it is.
func TestMcpCookieConversionSkipsAnEmptyCookie(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Cookie: theme=; session=abc"
	m = leaveGroup1(t, m)
	if strings.Contains(m.View(), "Convert the theme cookie") || !strings.Contains(m.View(), "Convert the session cookie to a secret?") {
		t.Fatalf("expected only the session offer:\n%s", m.View())
	}
	m = accept(t, m)
	v := m.mcpView
	if v.vals.headers != "Cookie: theme=; session=${cookie-session}" || v.secretValue("mcp:linear:cookie-session") != "abc" {
		t.Errorf("headers %q secrets %v", v.vals.headers, v.secretVals)
	}
	if v.focusInDefinition() {
		t.Errorf("the converted header should pass: status %q", v.status)
	}
}

// Review round 5: a cookie of blanks then ${…} does not hide the literal
// cookie after it; the offer used to loop and add an empty secret.
func TestMcpCookieConversionAfterBlankPlaceholderCookie(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Cookie: a= ${x}; session=abc"
	m = leaveGroup1(t, m)
	if !strings.Contains(m.View(), "Convert the session cookie to a secret?") {
		t.Fatalf("expected the session offer:\n%s", m.View())
	}
	m = accept(t, m)
	if m.screen == screenConfirm {
		t.Fatalf("the offer came back:\n%s", m.View())
	}
	v := m.mcpView
	if v.vals.headers != "Cookie: a= ${x}; session=${cookie-session}" || v.secretValue("mcp:linear:cookie-session") != "abc" {
		t.Errorf("headers %q secrets %v", v.vals.headers, v.secretVals)
	}
	if _, ok := v.secretVals["mcp:linear:cookie"]; ok {
		t.Errorf("an empty cookie secret was added: %v", v.secretVals)
	}
}

// Review round 5: a conversion that changes nothing stops the chain with the
// refusal instead of offering the same header again.
func TestMcpConversionThatFindsNothingStops(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Authorization: Bearer ${k}"
	done, before := false, len(m.mcpView.secretVals)
	m.mcpOfferConversions([]mcpConversion{{header: "Authorization", line: 0}}, func(*Model) tea.Cmd { done = true; return nil })
	m = accept(t, m)
	v := m.mcpView
	if done || m.screen == screenConfirm || !strings.Contains(v.status, "cannot be converted") {
		t.Fatalf("done %v screen %v status %q", done, m.screen, v.status)
	}
	if v.vals.headers != "Authorization: Bearer ${k}" || len(v.secretVals) != before {
		t.Errorf("headers %q secrets %v", v.vals.headers, v.secretVals)
	}
}

// Review round 6: an empty or separators-only credential header holds nothing
// to convert, so no offer opens; the inline rule error explains it instead.
func TestMcpNoConversionOfferForEmptyCredential(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	for _, h := range []string{"Authorization:", "Authorization: ;", "X-Api-Key: ,", "Cookie: ;", "Authorization: Bearer ;"} {
		m := openEdit(t, editModel(t, d))
		m.mcpView.vals.headers = h
		if convs := m.mcpView.mcpConversions(); len(convs) != 0 {
			t.Errorf("%q: offers %+v", h, convs)
		}
		// Tabbing out refuses the header: no offer, focus stays at the headers.
		name, _ := mcprules.SplitHeader(h)
		m = leaveGroup1(t, m)
		if m.screen != screenMcp || !m.mcpView.focusInDefinition() || !strings.Contains(m.mcpView.status, "error: header \""+name+"\"") {
			t.Errorf("%q: leave: screen %v focus-in-def %v status %q", h, m.screen, m.mcpView.focusInDefinition(), m.mcpView.status)
		}
		if f := m.mcpView.form.GetFocusedField(); f == nil || f.GetKey() != "headers" {
			t.Errorf("%q: focus not on headers", h)
		}
		if strings.Contains(m.mcpView.status, "invalid argument") {
			t.Errorf("%q: status keeps the rule prefix: %q", h, m.mcpView.status)
		}
		// Fixing the header and tabbing out clears the refusal.
		m.mcpView.vals.headers = name + ": ${k}"
		m = leaveGroup1(t, m)
		if m.screen != screenMcp || m.mcpView.focusInDefinition() || strings.Contains(m.mcpView.status, "error:") {
			t.Errorf("%q: fix+leave: screen %v focus-in-def %v status %q", h, m.screen, m.mcpView.focusInDefinition(), m.mcpView.status)
		}
	}
}

func TestMcpLiteralConversionOnLeave(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Authorization: Bearer sk-live-1"
	m = leaveGroup1(t, m)
	if !strings.Contains(m.View(), "Convert Authorization to a secret?") {
		t.Fatalf("expected the conversion offer:\n%s", m.View())
	}
	// Declining a typed literal keeps the form at the headers, refused.
	m = keyIn(m, press("n"))
	if m.screen != screenMcp || !m.mcpView.focusInDefinition() || !strings.Contains(m.mcpView.status, "literal credential") {
		t.Fatalf("decline: screen %v focus-in-def %v status %q", m.screen, m.mcpView.focusInDefinition(), m.mcpView.status)
	}
	m = leaveGroup1(t, m)
	m = accept(t, m)
	if m.mcpView.vals.headers != "Authorization: Bearer ${authorization}" || m.mcpView.secretValue("mcp:linear:authorization") != "sk-live-1" {
		t.Fatalf("headers %q secret %q", m.mcpView.vals.headers, m.mcpView.secretValue("mcp:linear:authorization"))
	}
	if m.mcpView.focusInDefinition() {
		t.Error("after converting, focus lands in the secrets group")
	}
	// On save the converted header and its secret are sent.
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	if len(d.mcpSaves) != 1 || d.mcpSaves[0].GetSecretValues()["mcp:linear:authorization"] != "sk-live-1" {
		t.Errorf("saves = %+v", d.mcpSaves)
	}
}

func TestMcpLiteralOnSaveIsOfferedAndRefused(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "X-Auth-Mode: oauth"
	m = keyIn(m, ctrlS())
	if !strings.Contains(m.View(), "Convert X-Auth-Mode") {
		t.Fatalf("save offers the conversion again:\n%s", m.View())
	}
	m = keyIn(m, press("n"))
	if len(d.mcpSaves) != 0 || m.mcpView.form == nil || !strings.Contains(m.mcpView.status, "literal credential") {
		t.Errorf("declining refuses the save; status %q", m.mcpView.status)
	}
}

func literalOnHostView() *pb.McpDefinitionView {
	v := linearView()
	v.Definition.Headers = []string{"Authorization: Bearer ••••"}
	v.Definition.LiteralOnHost = []string{"Authorization"}
	v.Flags = []*pb.McpFieldFlag{{Field: "headers/Authorization", Kind: pb.McpFieldFlag_LITERAL_ON_HOST, HostValue: "Bearer ••••"}}
	v.StoredSecrets = nil
	return v
}

func TestMcpLiteralOnHostConversionLeavesSecretBlank(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": literalOnHostView()}}
	m := openEdit(t, editModel(t, d))
	if !strings.Contains(m.mcpView.fieldFlags("headers"), "literal credential on host") {
		t.Errorf("the flag shows: %q", m.mcpView.fieldFlags("headers"))
	}
	m = leaveGroup1(t, m)
	if !strings.Contains(m.View(), "cannot be read back") {
		t.Fatalf("on-host offer:\n%s", m.View())
	}
	m = accept(t, m)
	if m.mcpView.vals.headers != "Authorization: Bearer ${authorization}" || m.mcpView.secretValue("mcp:linear:authorization") != "" {
		t.Fatalf("headers %q, the redacted text must not move: %q", m.mcpView.vals.headers, m.mcpView.secretValue("mcp:linear:authorization"))
	}
	if !strings.Contains(m.View(), mcpStatusHeldOnHost) {
		t.Errorf("the field says to re-enter:\n%s", m.View())
	}
}

func TestMcpLiteralOnHostDeclineAllowsMarkOnlySave(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": literalOnHostView()}}
	m := openEdit(t, editModel(t, d))
	m = leaveGroup1(t, m)
	m = keyIn(m, press("n"))
	if m.screen != screenMcp || m.mcpView.focusInDefinition() || !strings.Contains(m.mcpView.status, "literal credential on host") {
		t.Fatalf("declining continues into group 2; status %q", m.mcpView.status)
	}
	// A save that would re-register is refused while unconverted.
	m.mcpView.vals.url = "https://other/mcp"
	m = keyIn(m, ctrlS())
	if len(d.mcpSaves) != 0 || !strings.Contains(m.mcpView.status, "before re-registering") {
		t.Fatalf("re-registering save refused; status %q", m.mcpView.status)
	}
	// A mark-only save goes through.
	m.mcpView.vals.url = "https://mcp.linear.app/mcp"
	m.mcpView.vals.attach = true
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	if len(d.mcpSaves) != 1 || d.mcpSaves[0].GetDefinition().GetHeaders()[0] != "Authorization: Bearer ••••" {
		t.Errorf("saves = %+v", d.mcpSaves)
	}
}

func TestMcpLiteralOnHostDeclineWarningSurvivesRefusal(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": literalOnHostView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Authorization: Bearer ••••\nX-Api-Key: ,"
	m = leaveGroup1(t, m)
	m = keyIn(m, press("n"))
	// The empty header is refused, and the decline warning stays with it.
	if !m.mcpView.focusInDefinition() || !strings.Contains(m.mcpView.status, `error: header "X-Api-Key"`) ||
		!strings.Contains(m.mcpView.status, "Authorization: literal credential on host") {
		t.Fatalf("decline+refusal: focus-in-def %v status %q", m.mcpView.focusInDefinition(), m.mcpView.status)
	}
	// Fixing the header and tabbing out drops the refusal but keeps the warning.
	m.mcpView.vals.headers = "Authorization: Bearer ••••\nX-Api-Key: ${k}"
	m = leaveGroup1(t, m)
	if m.screen != screenMcp || m.mcpView.focusInDefinition() || strings.Contains(m.mcpView.status, "error:") ||
		!strings.Contains(m.mcpView.status, "Authorization: literal credential on host") {
		t.Fatalf("fix+leave: screen %v focus-in-def %v status %q", m.screen, m.mcpView.focusInDefinition(), m.mcpView.status)
	}
	// A refusal not set by leaving (here "converted X to Y") is kept.
	m.mcpView.status = "converted X to Y"
	m.mcpView.leaveRefusal = "error: stale"
	m = leaveGroup1(t, m)
	if m.mcpView.status != "converted X to Y" {
		t.Errorf("a later status is kept: %q", m.mcpView.status)
	}
}

func TestMcpSecretConfirmCombinesMissingAndRemovalFailed(t *testing.T) {
	view := linearView()
	view.Definition.Headers = []string{"X-Api-Key: ${k}", "X-Org: ${org}"}
	view.StoredSecrets = []string{"mcp:linear:k"}
	view.Secrets = []*pb.McpSecretStatus{{Name: "mcp:linear:k", Status: pb.McpSecretStatus_REMOVAL_FAILED}}
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m = keyIn(m, ctrlS())
	out := m.View()
	if !strings.Contains(out, "Missing") || !strings.Contains(out, "mcp:linear:org") || !strings.Contains(out, "This save will remove") || !strings.Contains(out, "mcp:linear:k") {
		t.Fatalf("combined prompt:\n%s", out)
	}
	m = keyIn(m, press("n"))
	if len(d.mcpSaves) != 0 || m.mcpView.form == nil {
		t.Fatal("back-out keeps the form")
	}
	m = keyIn(m, ctrlS())
	m = accept(t, m)
	if len(d.mcpSaves) != 1 {
		t.Fatal("register anyway saves")
	}
}

func TestMcpClientSecretFieldAndRebindWarning(t *testing.T) {
	view := linearView()
	view.Definition.Headers = nil
	view.Definition.ClientId = "old-client"
	view.Definition.RebindPending = true
	view.StoredSecrets = []string{"mcp:linear:client_secret"}
	view.AuthState = pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED
	d := &fakeDaemon{mcpServers: []*pb.McpServer{{Name: "linear", RebindPending: true}}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := editModel(t, d)
	if !strings.Contains(m.View(), "rebind pending") {
		t.Errorf("row shows rebind pending:\n%s", m.View())
	}
	m = openEdit(t, m)
	m = leaveGroup1(t, m)
	out := m.View()
	for _, want := range []string{"mcp:linear:client_secret", "Needed only for a confidential client", "old client binding may still be in place"} {
		if !strings.Contains(out, want) {
			t.Errorf("client secret field should show %q:\n%s", want, out)
		}
	}
	m.mcpView.vals.clientID = "new-client"
	m = keyIn(m, ctrlS())
	if !strings.Contains(m.View(), "The old client binding stays in place") || !strings.Contains(m.View(), "client re-bind pending") {
		t.Fatalf("FR-415 warning:\n%s", m.View())
	}
	m = accept(t, m)
	if len(d.mcpSaves) != 1 {
		t.Fatalf("saves = %d", len(d.mcpSaves))
	}
	// Entering the new client's secret skips the warning.
	m = keyIn(m, press("x"))
	m = keyIn(m, press("n")) // decline the authorize offer
	m = openEdit(t, m)
	m.mcpView.vals.clientID = "new-client"
	*m.mcpView.secretPtr("mcp:linear:client_secret") = "s3"
	m = keyIn(m, ctrlS())
	if len(d.mcpSaves) != 2 || d.mcpSaves[1].GetSecretValues()["mcp:linear:client_secret"] != "s3" {
		t.Errorf("saves = %+v screen %v", d.mcpSaves, m.screen)
	}
}

func TestMcpUnverifiedKeepsFormAndShowsMessage(t *testing.T) {
	for _, msg := range []string{"couldn't read the server from the host; try again", "recovering an interrupted save; try again"} {
		d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
			mcpSaveResults: []mcpSaveResult{{err: refused(pb.McpSaveRefusal_UNVERIFIED, msg)}}}
		m := openEdit(t, editModel(t, d))
		m.mcpView.vals.attach = true
		*m.mcpView.secretPtr("mcp:linear:api-key") = "typed"
		m = keyIn(m, ctrlS())
		if m.mcpView.form == nil || m.mcpView.status != msg || !m.mcpView.vals.attach || m.mcpView.secretValue("mcp:linear:api-key") != "typed" {
			t.Errorf("%q: form %v status %q", msg, m.mcpView.form != nil, m.mcpView.status)
		}
		if len(d.mcpDefReads) != 1 {
			t.Errorf("UNVERIFIED does not reload; reads %v", d.mcpDefReads)
		}
	}
}

func TestMcpBeingReplacedThenStaleReloads(t *testing.T) {
	view := linearView()
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view},
		mcpSaveResults: []mcpSaveResult{
			{err: refused(pb.McpSaveRefusal_BEING_REPLACED, mcpSavingMsg)},
			{err: refused(pb.McpSaveRefusal_STALE_CHANGED, "linear changed on the host since the form opened")},
		}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.scopes = "read write"
	*m.mcpView.secretPtr("mcp:linear:api-key") = "typed"
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	if m.mcpView.form == nil || m.mcpView.status != mcpSavingMsg || m.mcpView.vals.scopes != "read write" || m.mcpView.secretValue("mcp:linear:api-key") != "typed" {
		t.Fatalf("BEING_REPLACED keeps everything; status %q", m.mcpView.status)
	}
	// The other save re-registered with a new URL: the re-save is STALE_CHANGED
	// and reloads in place.
	reread := linearView()
	reread.Fingerprint = "fp-2"
	reread.Definition.Url = "https://mcp.linear.app/v3"
	d.mcpViews["linear"] = reread
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	v := m.mcpView
	if v.form == nil || v.fingerprint != "fp-2" || v.vals.url != "https://mcp.linear.app/v3" || v.vals.scopes != "read write" {
		t.Fatalf("reload: vals %+v fp %q", *v.vals, v.fingerprint)
	}
	if v.secretValue("mcp:linear:api-key") != "" || !strings.Contains(v.status, "dropped") || !strings.Contains(v.status, "mcp:linear:api-key") {
		t.Errorf("the URL was replaced: the header secret is dropped; status %q", v.status)
	}
	if !strings.Contains(m.View(), "changed on the host") {
		t.Errorf("the URL is flagged:\n%s", m.View())
	}
}

// notRecorded is the daemon's own "save ran, result not recorded yet" error:
// UNAVAILABLE with the MCP_SAVE_NOT_RECORDED ErrorInfo.
func notRecorded(t *testing.T, msg string) error {
	t.Helper()
	st, err := status.New(codes.Unavailable, msg).WithDetails(&errdetails.ErrorInfo{Reason: client.SaveNotRecordedReason, Domain: client.SaveNotRecordedDomain})
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestMcpUnavailableClosesFormRowSaving(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{err: notRecorded(t, "the host was changed but the result could not be recorded yet; retrying")}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m = keyIn(m, ctrlS())
	if m.mcpView.form != nil || m.mcpView.outcome != nil || !strings.Contains(m.mcpView.status, "could not be recorded yet") {
		t.Fatalf("UNAVAILABLE closes the form; status %q", m.mcpView.status)
	}
	if !strings.Contains(m.View(), "saving…") {
		t.Errorf("the row shows saving…:\n%s", m.View())
	}
	m = keyIn(m, press(" "))
	if len(d.mcpDefaults) != 0 || m.mcpView.status != mcpSavingMsg {
		t.Errorf("space on a saving row: defaults %v status %q", d.mcpDefaults, m.mcpView.status)
	}
	m = keyIn(m, press("e"))
	if m.mcpView.form != nil || len(d.mcpDefReads) != 1 {
		t.Errorf("e on a saving row sends no read; reads %v", d.mcpDefReads)
	}
	// The recorded result's event clears it.
	m, cmd := update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_McpGatewayChanged_{McpGatewayChanged: &pb.Event_McpGatewayChanged{HostId: d.HostID()}}}})
	m = drainEdit(m, cmd)
	if strings.Contains(m.View(), "saving…") {
		t.Errorf("saving… should clear after the event:\n%s", m.View())
	}
}

func TestMcpRowStatesRefuseLocally(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{
		{Name: "a", SaveInProgress: true, Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE},
		{Name: "b", Recovering: true, Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE},
	}}
	m := editModel(t, d)
	if !strings.Contains(m.View(), "saving…") || !strings.Contains(m.View(), "recovering interrupted save") {
		t.Fatalf("rows:\n%s", m.View())
	}
	for _, k := range []string{" ", "e", "A", "S"} {
		m = keyIn(m, press(k))
		if m.mcpView.status != mcpSavingMsg || m.mcpView.auth != nil || m.mcpView.scopes != nil {
			t.Errorf("%q on saving row: status %q", k, m.mcpView.status)
		}
	}
	m = keyIn(m, press("j"))
	for _, k := range []string{" ", "e"} {
		m = keyIn(m, press(k))
		if m.mcpView.status != mcpRecoveringMsg {
			t.Errorf("%q on recovering row: status %q", k, m.mcpView.status)
		}
	}
	if len(d.mcpDefaults) != 0 || len(d.mcpDefReads) != 0 {
		t.Errorf("no RPC: defaults %v reads %v", d.mcpDefaults, d.mcpDefReads)
	}
	// d on a saving row reaches the daemon, which refuses.
	m = keyIn(m, press("k"))
	d.mcpErr = refused(pb.McpSaveRefusal_BEING_REPLACED, mcpSavingMsg)
	m = keyIn(m, press("d"))
	m = accept(t, m)
	if len(d.removedMcp) != 0 || !strings.Contains(m.mcpView.status, mcpSavingMsg) {
		t.Errorf("d: status %q", m.mcpView.status)
	}
}

func TestMcpEditStaleRowSaveInProgress(t *testing.T) {
	view := linearView()
	view.SaveInProgress = true
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := editModel(t, d)
	m = keyIn(m, press("e"))
	if m.mcpView.form != nil || m.mcpView.status != mcpSavingMsg {
		t.Errorf("a read reporting save_in_progress opens no form; status %q", m.mcpView.status)
	}
	view.SaveInProgress, view.Recovering = false, true
	m = keyIn(m, press("e"))
	if m.mcpView.form != nil || m.mcpView.status != mcpRecoveringMsg {
		t.Errorf("recovering: status %q", m.mcpView.status)
	}
	d.mcpDefErr = errBoom{}
	m = keyIn(m, press("e"))
	if !strings.Contains(m.mcpView.status, "boom") {
		t.Errorf("read error: %q", m.mcpView.status)
	}
}

func TestMcpEditGoneOffersAddAsNew(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()},
		mcpDefErr: &client.SaveRefusal{Reason: pb.McpSaveRefusal_GONE, Message: "linear is no longer registered", Code: codes.NotFound}}
	m := editModel(t, d)
	m = keyIn(m, press("e"))
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Add linear as new?") || !strings.Contains(m.View(), "The add form opens with the name filled in.") {
		t.Fatalf("GONE offers to add as new:\n%s", m.View())
	}
	m = accept(t, m)
	if m.mcpView.form == nil || m.mcpView.vals.mode != mcpModeAdd || m.mcpView.vals.name != "linear" {
		t.Errorf("add form with the name: %+v", m.mcpView.vals)
	}
}

func TestMcpSaveGoneAndPendingEnded(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{
			{err: &client.SaveRefusal{Reason: pb.McpSaveRefusal_GONE, Message: "gone", Code: codes.NotFound}},
			{err: refused(pb.McpSaveRefusal_AUTH_IN_PROGRESS, "finish or cancel the authorization first")},
			{err: errBoom{}},
		}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m = keyIn(m, ctrlS())
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Add linear as new?") {
		t.Fatalf("save GONE:\n%s", m.View())
	}
	m = keyIn(m, press("n"))
	if m.mcpView.form == nil || m.mcpView.vals.mode != mcpModeEdit {
		t.Fatal("declining keeps the form")
	}
	m = keyIn(m, ctrlS())
	if m.mcpView.form == nil || m.mcpView.status != "finish or cancel the authorization first" {
		t.Errorf("auth in progress: %q", m.mcpView.status)
	}
	m = keyIn(m, ctrlS())
	if m.mcpView.form == nil || m.mcpView.status != "error: boom" {
		t.Errorf("plain error: %q", m.mcpView.status)
	}
}

func pendingLinear() *pb.McpPendingReplace {
	return &pb.McpPendingReplace{
		Name: "linear", Revision: 7, AttachByDefault: true, Error: "add failed: 500", FailedAt: timestamppb.New(time.Now().Add(-5 * time.Minute)),
		Definition:           &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://new/mcp", Headers: []string{"X-Api-Key: ${k}"}},
		Previous:             &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: "https://old/mcp", Headers: []string{"X-Api-Key: ${k}"}},
		RemovedSecrets:       []string{"mcp:linear:k"},
		RemovalFailedSecrets: []string{"mcp:linear:old"},
	}
}

func TestMcpNotRegisteredOutcomeKeepsFormAndLeaveCancels(t *testing.T) {
	p := pendingLinear()
	view := linearView()
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view},
		mcpSaveResults: []mcpSaveResult{{out: &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_NOT_REGISTERED, AddStarted: true,
			HostErrors: []string{"sbx: add failed: 500"}, RolledBack: []string{"mcp:linear:api-key"}, PendingReplace: p}}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.url = "https://new/mcp"
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	out := m.View()
	for _, want := range []string{"The save of linear did not complete", "not registered", "the add may still complete on the host", "sbx: add failed: 500", "rolled back: mcp:linear:api-key"} {
		if !strings.Contains(out, want) {
			t.Errorf("outcome should show %q:\n%s", want, out)
		}
	}
	m = keyIn(m, press("x"))
	if m.mcpView.form == nil || m.mcpView.vals.url != "https://new/mcp" || m.mcpView.retry.GetRevision() != 7 {
		t.Fatalf("the form stays with the edits and holds the pending replace")
	}
	// Leaving asks the y-only Cancel; declining keeps the form.
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Cancel the replace of linear?") {
		t.Fatalf("leave opens the cancel confirm:\n%s", m.View())
	}
	m = keyIn(m, press("n"))
	if m.mcpView.form == nil || len(d.mcpCancels) != 0 {
		t.Fatal("declining keeps the form")
	}
	// A re-save is a retry of the pending revision.
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	if len(d.mcpSaves) != 2 || d.mcpSaves[1].GetRetryOfRevision() != 7 {
		t.Fatalf("retry save = %+v", d.mcpSaves)
	}
	m = keyIn(m, press("x")) // close the REGISTERED outcome
	_ = m
}

func TestMcpLeaveRetryFormCancels(t *testing.T) {
	p := pendingLinear()
	view := &pb.McpDefinitionView{Name: "linear", SecretsAvailable: true, PendingReplace: p, PendingRemovalFailedListed: []string{"mcp:linear:old"}}
	d := &fakeDaemon{mcpPendings: []*pb.McpPendingReplace{p}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view},
		mcpCancelResp: &pb.CancelMcpPendingReplaceResponse{LeftOnHost: []string{"mcp:linear:old"}}}
	m := editModel(t, d)
	if !strings.Contains(m.View(), "linear — not registered (replace failed 5m ago)") {
		t.Fatalf("pending row:\n%s", m.View())
	}
	m = keyIn(m, press("R"))
	if !strings.Contains(m.View(), "h  the held definition") {
		t.Fatalf("retry chooser:\n%s", m.View())
	}
	m = keyIn(m, press("p"))
	v := m.mcpView
	if v.form == nil || v.vals.mode != mcpModeRetry || v.vals.url != "https://old/mcp" || !v.vals.attach || !strings.Contains(m.View(), "old values are gone") {
		t.Fatalf("retry form (previous):\n%s", m.View())
	}
	m = leaveGroup1(t, m)
	if !strings.Contains(m.View(), "Removed by the failed save") || !strings.Contains(m.View(), "mcp:linear:old") {
		t.Errorf("secrets note names the pending's secrets:\n%s", m.View())
	}
	// Leaving the retry form, clean, opens the cancel confirm.
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEsc})
	if !strings.Contains(m.View(), "It will remove: mcp:linear:old") {
		t.Fatalf("cancel confirm:\n%s", m.View())
	}
	m = accept(t, m)
	if m.mcpView.form != nil || len(d.mcpCancels) != 1 || d.mcpCancels[0] != "linear@7" || !strings.Contains(m.mcpView.status, "left on the host") {
		t.Errorf("cancel: %v status %q", d.mcpCancels, m.mcpView.status)
	}
}

func TestMcpPendingRowCancelAndHeld(t *testing.T) {
	p := pendingLinear()
	d := &fakeDaemon{mcpPendings: []*pb.McpPendingReplace{p}, mcpDefErr: status.Error(codes.NotFound, "not listed")}
	m := editModel(t, d)
	if !strings.Contains(helpLine(m.mcpHelp()), "retry") {
		t.Errorf("pending help: %s", helpLine(m.mcpHelp()))
	}
	m = keyIn(m, press("A"))
	if !strings.Contains(m.mcpView.status, "not registered") || m.mcpView.auth != nil {
		t.Errorf("A on a pending row: %q", m.mcpView.status)
	}
	m = keyIn(m, press("X"))
	if !strings.Contains(m.View(), "May be left on the host: mcp:linear:old") {
		t.Fatalf("cancel with unknown list:\n%s", m.View())
	}
	m = keyIn(m, press("n"))
	m = keyIn(m, press("R"))
	m = keyIn(m, press("h"))
	if m.mcpView.form == nil || m.mcpView.vals.url != "https://new/mcp" || m.mcpView.retry == nil {
		t.Fatalf("retry (held) from the row's pending: %+v", m.mcpView.vals)
	}
	d.mcpCancelErr = refused(pb.McpSaveRefusal_BEING_REPLACED, mcpSavingMsg)
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = accept(t, m)
	if !strings.Contains(m.mcpView.status, mcpSavingMsg) {
		t.Errorf("cancel refusal: %q", m.mcpView.status)
	}
}

func TestMcpRetryReloadPendingEnded(t *testing.T) {
	p := pendingLinear()
	view := &pb.McpDefinitionView{Name: "linear", SecretsAvailable: true, PendingReplace: p}
	d := &fakeDaemon{mcpPendings: []*pb.McpPendingReplace{p}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view},
		mcpSaveResults: []mcpSaveResult{{err: refused(pb.McpSaveRefusal_STALE_CHANGED, "the pending replace changed")}, {err: refused(pb.McpSaveRefusal_PENDING_ENDED, "the pending replace was cancelled")}}}
	m := editModel(t, d)
	m = keyIn(m, press("R"))
	m = keyIn(m, press("h"))
	*m.mcpView.secretPtr("mcp:linear:k") = "v"
	// The pending replace ends between the save and the reload's re-read.
	d.mcpViews["linear"] = &pb.McpDefinitionView{Name: "linear", SecretsAvailable: true}
	m = keyIn(m, ctrlS())
	if m.mcpView.form != nil || !strings.Contains(m.mcpView.status, "has ended") {
		t.Fatalf("an ended pending replace closes the form; status %q", m.mcpView.status)
	}
	d.mcpViews["linear"] = view
	m = keyIn(m, press("R"))
	m = keyIn(m, press("h"))
	*m.mcpView.secretPtr("mcp:linear:k") = "v"
	m = keyIn(m, ctrlS())
	if m.mcpView.form != nil || m.mcpView.status != "the pending replace was cancelled" {
		t.Errorf("PENDING_ENDED closes and says how: %q", m.mcpView.status)
	}
}

func TestMcpOutcomePanelAndRemovalOffer(t *testing.T) {
	out := &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_REGISTERED, Replaced: true,
		Created: []string{"mcp:linear:a"}, Overwritten: []string{"mcp:linear:b"}, RollbackFailed: []string{"mcp:linear:c"},
		Identity:      pb.McpSaveOutcome_CLEARED,
		UnusedSecrets: []string{"mcp:linear:client_secret", "mcp:linear:client_secret:identity"},
		Server:        &pb.McpServer{Name: "linear", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE},
		AttachedSandboxes: []*pb.McpAttachedSandbox{
			{SandboxId: "s1", DisplayName: "alpha"},
			{SandboxId: "s2", DisplayName: "beta", SkippedAtLaunch: true},
			{SandboxId: "s3", DisplayName: "gamma", SkippedAtLaunch: true, Exclusive: true},
		}}
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{out: out}}, mcpRmResp: &pb.RemoveMcpServerSecretsResponse{Removed: []string{"mcp:linear:client_secret"}, Failed: []string{"mcp:linear:client_secret:identity"}, RebindPending: true}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m = keyIn(m, ctrlS())
	v := m.View()
	for _, want := range []string{"registered", "secrets created: mcp:linear:a", "overwritten: mcp:linear:b", "rollback failed", "Cleared the old client binding",
		"alpha picks up the new definition", "beta: not attached at launch", "gamma: missing from this sandbox's fixed set"} {
		if !strings.Contains(v, want) {
			t.Errorf("outcome should show %q:\n%s", want, v)
		}
	}
	m = keyIn(m, press("q"))
	if !strings.Contains(m.View(), "Remove unused secrets?") || !strings.Contains(m.View(), "client is public") {
		t.Fatalf("FR-413 offer:\n%s", m.View())
	}
	m = accept(t, m)
	if len(d.mcpRmSecrets) != 1 || d.mcpRmSecrets[0][0] != "linear" || len(d.mcpRmSecrets[0]) != 3 {
		t.Errorf("removal = %v", d.mcpRmSecrets)
	}
	if !strings.Contains(m.View(), "Authorize linear now?") {
		t.Fatalf("FR-405 after FR-413:\n%s", m.View())
	}
	m = keyIn(m, press("n"))
	if m.screen != screenMcp || !strings.Contains(m.mcpView.status, "could not remove") || !strings.Contains(m.mcpView.status, "re-bind pending") {
		t.Errorf("status %q", m.mcpView.status)
	}
}

func TestMcpIdentityTexts(t *testing.T) {
	for id, want := range map[pb.McpSaveOutcome_Identity]string{
		pb.McpSaveOutcome_NOT_TOUCHED:   "",
		pb.McpSaveOutcome_LEFT_IN_PLACE: "marked client re-bind pending",
		pb.McpSaveOutcome_CLEARED:       "Cleared the old client binding",
		pb.McpSaveOutcome_NOT_CLEARED:   "was not cleared. Re-enter the client secret and save to retry the clear",
		pb.McpSaveOutcome_UNCONFIRMED:   "may still be in place). Re-enter the client secret and save to retry the clear",
	} {
		got := mcpIdentityText("s", id)
		if want == "" && got != "" || !strings.Contains(got, want) {
			t.Errorf("%v: %q", id, got)
		}
		if id == pb.McpSaveOutcome_NOT_CLEARED && (!strings.Contains(got, "mcp:s:client_secret:identity") || !strings.Contains(got, "clears only on the next save that stores the client secret")) {
			t.Errorf("not cleared names the record: %q", got)
		}
	}
	m := sized(New(&fakeDaemon{}, "/w"))
	lines := strings.Join(m.mcpOutcomeLines(&pb.McpSaveOutcome{Name: "s", State: pb.McpSaveOutcome_STILL_REGISTERED}), "\n")
	if !strings.Contains(lines, "still registered, authorization may be lost") {
		t.Errorf("still registered: %s", lines)
	}
}

func TestMergeMcpReloadRules(t *testing.T) {
	opened := mcpFormVals{kind: "remote", url: "https://a/mcp", headers: "X-K: ${k}", clientID: "c1", scopes: "read"}
	cases := []struct {
		name          string
		cur, reread   mcpFormVals
		secrets       map[string]string
		wantURL       string
		wantDropped   []string
		wantFlag      string
		wantScopes    string
		wantClientID  string
		wantReenterOK bool
	}{
		{name: "url replaced drops header and client secrets",
			cur: opened, reread: mcpFormVals{kind: "remote", url: "https://b/mcp", headers: "X-K: ${k}", clientID: "c1", scopes: "read"},
			secrets: map[string]string{"mcp:s:k": "v", "mcp:s:client_secret": "cs"}, wantURL: "https://b/mcp",
			wantDropped: []string{"mcp:s:client_secret", "mcp:s:k"}, wantFlag: "url", wantScopes: "read", wantClientID: "c1", wantReenterOK: true},
		{name: "edited url keeps value, flagged, secrets kept",
			cur:     mcpFormVals{kind: "remote", url: "https://mine/mcp", headers: "X-K: ${k}", clientID: "c1", scopes: "read"},
			reread:  mcpFormVals{kind: "remote", url: "https://b/mcp", headers: "X-K: ${k}", clientID: "c1", scopes: "read"},
			secrets: map[string]string{"mcp:s:k": "v"}, wantURL: "https://mine/mcp", wantFlag: "url", wantScopes: "read", wantClientID: "c1"},
		{name: "client id replaced drops client secret only",
			cur: opened, reread: mcpFormVals{kind: "remote", url: "https://a/mcp", headers: "X-K: ${k}", clientID: "c2", scopes: "read"},
			secrets: map[string]string{"mcp:s:k": "v", "mcp:s:client_secret": "cs"}, wantURL: "https://a/mcp",
			wantDropped: []string{"mcp:s:client_secret"}, wantFlag: "client_id", wantScopes: "read", wantClientID: "c2"},
		{name: "orphaned placeholder dropped",
			cur: opened, reread: mcpFormVals{kind: "remote", url: "https://a/mcp", headers: "X-J: ${j}", clientID: "c1", scopes: "read"},
			secrets: map[string]string{"mcp:s:k": "v"}, wantURL: "https://a/mcp", wantDropped: []string{"mcp:s:k"}, wantFlag: "headers", wantScopes: "read", wantClientID: "c1"},
		{name: "client id cleared drops client secret",
			cur: opened, reread: mcpFormVals{kind: "remote", url: "https://a/mcp", headers: "X-K: ${k}", scopes: "read"},
			secrets: map[string]string{"mcp:s:client_secret": "cs"}, wantURL: "https://a/mcp", wantDropped: []string{"mcp:s:client_secret"}, wantFlag: "client_id", wantScopes: "read"},
		{name: "presentation-only difference is not a change",
			cur: opened, reread: mcpFormVals{kind: "remote", url: " https://a/mcp ", headers: "x-k:${k}", clientID: "c1", scopes: "read read"},
			secrets: map[string]string{"mcp:s:k": "v"}, wantURL: "https://a/mcp", wantScopes: "read read", wantClientID: "c1"},
		{name: "edited scopes kept while the host changed them",
			cur:     mcpFormVals{kind: "remote", url: "https://a/mcp", headers: "X-K: ${k}", clientID: "c1", scopes: "read write"},
			reread:  mcpFormVals{kind: "remote", url: "https://a/mcp", headers: "X-K: ${k}", clientID: "c1", scopes: "admin"},
			wantURL: "https://a/mcp", wantFlag: "scopes", wantScopes: "read write", wantClientID: "c1"},
	}
	for _, c := range cases {
		r := mergeMcpReload(c.cur, opened, c.reread, "s", c.secrets)
		if strings.TrimSpace(r.vals.url) != c.wantURL || r.vals.scopes != c.wantScopes || r.vals.clientID != c.wantClientID {
			t.Errorf("%s: vals %+v", c.name, r.vals)
		}
		if strings.Join(r.dropped, ",") != strings.Join(c.wantDropped, ",") {
			t.Errorf("%s: dropped %v want %v", c.name, r.dropped, c.wantDropped)
		}
		if c.wantFlag != "" && !strings.Contains(r.flags[c.wantFlag], "changed on the host: was") {
			t.Errorf("%s: flags %v", c.name, r.flags)
		}
		if c.wantFlag == "" && len(r.flags) != 0 {
			t.Errorf("%s: unexpected flags %v", c.name, r.flags)
		}
		if c.wantReenterOK && !r.reenter["mcp:s:k"] {
			t.Errorf("%s: the header secret must be re-entered", c.name)
		}
	}
}

func TestMcpGuardSnapshotDeepCopies(t *testing.T) {
	v := mcpState{vals: &mcpFormVals{name: "x"}}
	*v.secretPtr("a") = ""
	base := v.mcpSnapshot()
	*v.secretPtr("a") = "typed"
	if v.mcpSnapshot().equal(base) {
		t.Error("a typed secret differs from the baseline")
	}
	*v.secretPtr("a") = ""
	if !v.mcpSnapshot().equal(base) {
		t.Error("typed back to blank is clean")
	}
}

func TestMcpSavingFormIgnoresKeysAndLeaves(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m, cmd := update(m, ctrlS())
	if !m.mcpView.saving || m.guardDirty(gMcp) {
		t.Fatal("a save in flight is not dirty")
	}
	m, _ = update(m, ctrlS())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mcpView.form != nil || !strings.Contains(m.mcpView.status, "in the background") {
		t.Fatalf("esc leaves while saving; status %q", m.mcpView.status)
	}
	m = drainEdit(m, cmd)
	if m.mcpView.outcome == nil {
		t.Error("the late outcome still shows")
	}
}

func TestMcpScopesPrompt(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer(), {Name: "pw", Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL}}, mcpAuthState: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED}
	m := editModel(t, d)
	m = keyIn(m, press("S"))
	if m.mcpView.scopes == nil || !strings.Contains(m.View(), "no refresh-token scope is added") {
		t.Fatalf("scope prompt:\n%s", m.View())
	}
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.View(), "enter at least one scope") {
		t.Errorf("empty: %s", m.mcpView.scopes.err)
	}
	for _, r := range "a,b" {
		m = keyIn(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpView.scopes == nil || !strings.Contains(m.mcpView.scopes.err, "comma") {
		t.Fatalf("invalid scope kept in the prompt: %+v", m.mcpView.scopes)
	}
	m.mcpView.scopes.input.SetValue("read write")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mcpView.auth == nil {
		t.Fatal("enter starts the authorization modal")
	}
	waitAuthEnd(t, m)
	if len(d.mcpAuthScopes) != 1 || strings.Join(d.mcpAuthScopes[0], " ") != "read write" || d.mcpAuthNoScope[0] {
		t.Errorf("scopes %v no %v", d.mcpAuthScopes, d.mcpAuthNoScope)
	}
	m.mcpView.auth = nil
	m = keyIn(m, press("S"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyCtrlN})
	waitAuthEnd(t, m)
	if len(d.mcpAuthNoScope) != 2 || !d.mcpAuthNoScope[1] {
		t.Errorf("ctrl+n = no scopes: %v", d.mcpAuthNoScope)
	}
	m.mcpView.auth = nil
	m = keyIn(m, press("S"))
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mcpView.scopes != nil {
		t.Error("esc closes the prompt")
	}
	m = keyIn(m, press("j"))
	m = keyIn(m, press("S"))
	if m.mcpView.scopes != nil || !strings.Contains(m.mcpView.status, "no authorization step") {
		t.Errorf("S on a host command: %q", m.mcpView.status)
	}
}

// waitAuthEnd drains the authorization goroutine's channel to its end.
func waitAuthEnd(t *testing.T, m Model) {
	t.Helper()
	ch := m.mcpView.auth.ch
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, end := msg.(mcpAuthEndMsg); end {
				return
			}
		case <-deadline:
			t.Fatal("authorization did not end")
		}
	}
}

func TestMcpGatewayChangedRefreshesSecondView(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}}
	m := editModel(t, d)
	// Another client saved: the event carries its outcome and pending replace.
	d.mcpServers = nil
	d.mcpPendings = []*pb.McpPendingReplace{pendingLinear()}
	ev := &pb.Event{Event: &pb.Event_McpGatewayChanged_{McpGatewayChanged: &pb.Event_McpGatewayChanged{HostId: d.HostID(),
		Outcome:         &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_NOT_REGISTERED},
		PendingReplaces: d.mcpPendings}}}
	m, cmd := update(m, eventMsg{ev: ev})
	m = drainEdit(m, cmd)
	if !strings.Contains(m.View(), "linear — not registered (replace failed") {
		t.Errorf("the second view re-lists:\n%s", m.View())
	}
	if m.mcpView.outcome != nil {
		t.Error("another client's outcome does not open this view's panel")
	}
}

func TestMcpFormDescriptionFlagsAndEditFormKeymap(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/w"))
	m.mcpView.hasEdit = true
	m.mcpView.vals = &mcpFormVals{kind: "remote"}
	spec := newFormSpec()
	f := m.mcpEditForm(spec, m.mcpView.vals)
	if f == nil || len(spec.texts) != 1 {
		t.Fatal("the edit form registers its headers Text")
	}
	if f.State != huh.StateNormal {
		t.Error("fresh form")
	}
}

// A replaced host command whose outcome row is missing (the daemon's re-list
// failed) is never offered authorization; a remote one still is.
func TestMcpAuthorizeOfferWithoutOutcomeRow(t *testing.T) {
	m := editModel(t, &fakeDaemon{})
	out := &pb.McpSaveOutcome{Name: "pw", State: pb.McpSaveOutcome_REGISTERED, Replaced: true}
	for kind, want := range map[string]bool{"command": false, "remote": true} {
		offered := false
		for _, st := range m.mcpFollowups(out, kind == "remote") {
			offered = offered || strings.HasPrefix(st.title, "Authorize")
		}
		if offered != want {
			t.Errorf("%s: offered = %v", kind, offered)
		}
	}
}

// A secret typed before the add form's name is set follows the name.
func TestMcpAddRenameKeepsTypedSecrets(t *testing.T) {
	v := &mcpState{}
	*v.secretPtr("mcp:<name>:authorization") = "sk-123"
	*v.secretPtr("mcp:keep:x") = "other"
	v.renameSecrets("", "notion")
	if v.secretValue("mcp:notion:authorization") != "sk-123" || v.secretValue("mcp:<name>:authorization") != "" || v.secretValue("mcp:keep:x") != "other" {
		t.Errorf("secrets = %v", v.secretVals)
	}
	*v.secretPtr("mcp:linear:authorization") = "typed"
	v.renameSecrets("notion", "linear")
	if v.secretValue("mcp:linear:authorization") != "typed" || v.secretVals["mcp:notion:authorization"] != nil {
		t.Errorf("an already-typed value under the new name is kept, the old one dropped: %v", v.secretVals)
	}
}

// A bare UNAVAILABLE is a transport failure (the save may never have run): the
// form stays with its edits and typed secrets, and the row is not saving….
func TestMcpBareUnavailableKeepsFormAndValues(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{err: status.Error(codes.Unavailable, "connection refused")}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	*m.mcpView.secretPtr("mcp:linear:api-key") = "typed"
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	v := m.mcpView
	if v.form == nil || v.status != "error: connection refused" || !v.vals.attach || v.secretValue("mcp:linear:api-key") != "typed" || v.saving {
		t.Fatalf("form %v status %q", v.form != nil, v.status)
	}
	if v.localSaving["linear"] {
		t.Error("a transport failure does not mark the row saving…")
	}
	m = keyIn(m, ctrlS())
	if len(d.mcpSaves) != 2 {
		t.Errorf("the form can save again; saves %d", len(d.mcpSaves))
	}
}

// A definition read that lands after another form or modal opened is dropped.
func TestMcpLateDefinitionReadDoesNotReplaceForm(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := editModel(t, d)
	m, readCmd := update(m, press("e"))
	if m.mcpView.loadingDef != "linear" {
		t.Fatalf("loading: %q", m.mcpView.loadingDef)
	}
	m = keyIn(m, press("a"))
	if strings.Contains(m.mcpView.status, "loading linear") {
		t.Errorf("opening the add form leaves %q", m.mcpView.status)
	}
	m.mcpView.vals.name = "fresh"
	*m.mcpView.secretPtr("mcp:fresh:k") = "typed"
	m = drainEdit(m, readCmd)
	v := m.mcpView
	if v.vals == nil || v.vals.mode != mcpModeAdd || v.vals.name != "fresh" || v.secretValue("mcp:fresh:k") != "typed" {
		t.Fatalf("the add form was replaced: %+v", v.vals)
	}
	m = escDiscard(t, m)
	m, readCmd = update(m, press("e"))
	m.mcpView.outcome = &pb.McpSaveOutcome{Name: "other"}
	m = drainEdit(m, readCmd)
	if m.mcpView.form != nil || m.mcpView.loadingDef != "" || m.mcpView.outcome == nil {
		t.Errorf("a read under a modal opens nothing: form %v", m.mcpView.form != nil)
	}
	if m.mcpView.status == "loading linear…" {
		t.Errorf("a dropped read leaves %q on the status line", m.mcpView.status)
	}
}

// The reply of a save left with esc never touches a later form of the same
// name: matched by the form's generation, and no panel opens over it.
func TestMcpEscapedSaveReplyLeavesNewFormAlone(t *testing.T) {
	cases := map[string]mcpSaveResult{
		"registered":     {out: &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_REGISTERED}},
		"not registered": {out: &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_NOT_REGISTERED, PendingReplace: pendingLinear()}},
		"not recorded":   {err: notRecorded(t, "not recorded yet")},
		"stale":          {err: refused(pb.McpSaveRefusal_STALE_CHANGED, "linear changed on the host")},
	}
	for name, res := range cases {
		d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
			mcpSaveResults: []mcpSaveResult{res}}
		m := openEdit(t, editModel(t, d))
		m.mcpView.vals.attach = true
		m, saveCmd := update(m, ctrlS())
		if !m.mcpView.saving {
			t.Fatalf("%s: the save should be in flight", name)
		}
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
		m = keyIn(m, press("a"))
		m.mcpView.vals.name, m.mcpView.vals.url = "linear", "https://typed/mcp"
		*m.mcpView.secretPtr("mcp:linear:k") = "typed"
		reads := len(d.mcpDefReads)
		m = drainEdit(m, saveCmd)
		v := m.mcpView
		if v.vals == nil || v.vals.mode != mcpModeAdd || v.vals.url != "https://typed/mcp" || v.secretValue("mcp:linear:k") != "typed" {
			t.Errorf("%s: the new form was touched: %+v", name, v.vals)
			continue
		}
		if v.outcome != nil || v.retry != nil || v.holding != nil || len(d.mcpDefReads) != reads || m.screen != screenMcp {
			t.Errorf("%s: outcome %v retry %v reads %d screen %v", name, v.outcome != nil, v.retry != nil, len(d.mcpDefReads)-reads, m.screen)
		}
		switch name {
		case "registered":
			if v.status != "saved linear in the background: registered" {
				t.Errorf("status %q", v.status)
			}
		case "not registered":
			if !strings.HasPrefix(v.status, "error: the background save of linear did not complete: not registered") {
				t.Errorf("status %q", v.status)
			}
		}
		// A late reload or refresh for the old form is dropped too.
		mm, _ := m.applyMcpDef(mcpDefMsg{host: v.host, name: "linear", purpose: mcpDefReload, gen: v.formGen - 1, view: linearView()})
		m = mm.(Model)
		mm, _ = m.applyMcpDef(mcpDefMsg{host: v.host, name: "linear", purpose: mcpDefRefresh, gen: v.formGen - 1, view: linearView()})
		m = mm.(Model)
		if m.mcpView.view != nil || m.mcpView.vals.url != "https://typed/mcp" {
			t.Errorf("%s: a stale reload/refresh reached the new form", name)
		}
	}
}

// A background save's follow-ups (FR-413 removal) are not lost: the status
// names the unused secrets, and the offer opens once the other form closes.
func TestMcpBackgroundSaveFollowupsWaitForTheForm(t *testing.T) {
	noFollowupDelay(t)
	out := &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_REGISTERED, UnusedSecrets: []string{"mcp:linear:k"},
		Server: &pb.McpServer{Name: "linear", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, AuthState: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED}}
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{out: out}}, mcpRmResp: &pb.RemoveMcpServerSecretsResponse{Removed: []string{"mcp:linear:k"}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m, saveCmd := update(m, ctrlS())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = keyIn(m, press("a"))
	m = drainEdit(m, saveCmd)
	if m.mcpView.vals == nil || m.screen != screenMcp {
		t.Fatalf("the add form should stay open: screen %v", m.screen)
	}
	if !strings.Contains(m.mcpView.status, "no longer used: mcp:linear:k") {
		t.Errorf("status %q", m.mcpView.status)
	}
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEsc}) // the untouched add form closes
	if m.mcpView.vals != nil || !strings.Contains(m.View(), "Remove unused secrets?") {
		t.Fatalf("FR-413 offer after the form closed:\n%s", m.View())
	}
	m = accept(t, m)
	if len(d.mcpRmSecrets) != 1 || d.mcpRmSecrets[0][0] != "linear" || m.mcpView.deferred != nil {
		t.Errorf("removal = %v, deferred %v", d.mcpRmSecrets, m.mcpView.deferred)
	}
}

// Review round 5: leaving the screen before a background save's follow-ups
// open drops them, and the status says what was skipped, the FR-405 authorize
// offer included.
func TestMcpBackgroundSaveFollowupsSkippedOnLeave(t *testing.T) {
	out := &pb.McpSaveOutcome{Name: "linear", State: pb.McpSaveOutcome_REGISTERED, UnusedSecrets: []string{"mcp:linear:k"},
		Server: &pb.McpServer{Name: "linear", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, AuthState: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED}}
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{out: out}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.attach = true
	m, saveCmd := update(m, ctrlS())
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m = keyIn(m, press("a"))
	m = drainEdit(m, saveCmd)
	if !strings.Contains(m.mcpView.status, "(offered for removal once the list is idle)") || len(m.mcpView.deferred) != 2 {
		t.Fatalf("status %q deferred %d", m.mcpView.status, len(m.mcpView.deferred))
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // the add form closes
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // a quick second esc leaves
	if m.screen != screenDaemons || m.mcpView.deferred != nil {
		t.Fatalf("screen %v deferred %d", m.screen, len(m.mcpView.deferred))
	}
	for _, want := range []string{"skipped the follow-ups", "mcp:linear:k", "Secrets view", "press A on linear to authorize"} {
		if !strings.Contains(m.status, want) {
			t.Errorf("status %q lacks %q", m.status, want)
		}
	}
}

// Accepting a conversion on ctrl+s rebuilds the form, so a refused save does not
// let the Headers widget write the literal back.
func TestMcpConvertOnSaveDoesNotBringTheLiteralBack(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{err: refused(pb.McpSaveRefusal_UNVERIFIED, "couldn't read the server from the host; try again")}}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Authorization: Bearer sk-live-9"
	_ = m.reopenForm(gMcp) // the widget holds the literal, as if typed
	m.focusMcpField("headers")
	m = keyIn(m, ctrlS())
	if !strings.Contains(m.View(), "Convert Authorization to a secret?") {
		t.Fatalf("expected the offer:\n%s", m.View())
	}
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	if len(d.mcpSaves) != 1 || m.mcpView.form == nil {
		t.Fatalf("saves %d", len(d.mcpSaves))
	}
	// Any message reaching the Headers widget writes its value back.
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.mcpView.vals.headers != "Authorization: Bearer ${authorization}" || m.mcpView.secretValue("mcp:linear:authorization") != "sk-live-9" {
		t.Fatalf("headers %q", m.mcpView.vals.headers)
	}
	m = keyIn(m, ctrlS())
	if strings.Contains(m.View(), "Convert Authorization") {
		t.Error("the next save does not offer the conversion again")
	}
}

// With a blank name every secret is keyed under the same placeholder server.
func TestMcpBlankNameSecretsShareOneKey(t *testing.T) {
	d := &fakeDaemon{}
	m := editModel(t, d)
	m = pressCmd(m, "a")
	v := &m.mcpView
	v.vals.url, v.vals.headers = "https://x/mcp", "Authorization: Bearer sk-1"
	m = leaveGroup1(t, m)
	if !strings.Contains(m.View(), "mcp:<name>:<placeholder>") {
		t.Errorf("the offer names the placeholder server:\n%s", m.View())
	}
	m = accept(t, m)
	fields := m.mcpView.secretFields()
	if len(fields) != 1 || fields[0].name != "mcp:<name>:authorization" || m.mcpView.secretValue(fields[0].name) != "sk-1" {
		t.Fatalf("fields %+v secrets %v", fields, m.mcpView.secretVals)
	}
	*m.mcpView.secretPtr("mcp:<name>:authorization") = "typed"
	m.mcpView.renameSecrets("", "foo")
	m.mcpView.vals.name = "foo"
	if m.mcpView.secretValue("mcp:foo:authorization") != "typed" || m.mcpView.secretFields()[0].name != "mcp:foo:authorization" {
		t.Errorf("setting the name moves the value: %v", m.mcpView.secretVals)
	}
}

// pumpForm feeds a message to a huh form and its resulting messages back in.
func pumpForm(f *huh.Form, msg tea.Msg) {
	queue := []tea.Msg{msg}
	for i := 0; i < 30 && len(queue) > 0; i++ {
		next := queue[0]
		queue = queue[1:]
		_, cmd := f.Update(next)
		queue = append(queue, runQuick(cmd)...)
	}
}

// The Headers description lists the derived secret names as they are typed.
func TestMcpHeadersSecretListFollowsTyping(t *testing.T) {
	view := linearView()
	view.Definition.Headers = nil
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := openEdit(t, editModel(t, d))
	m.focusMcpField("headers")
	f := m.mcpView.form
	pumpForm(f, struct{}{})
	if strings.Contains(f.View(), "mcp:linear:tok") {
		t.Fatal("no placeholder yet")
	}
	// Only the last keystroke's description update can match the final hash.
	var cmd tea.Cmd
	for _, r := range "X-Key: ${tok}" {
		_, cmd = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	for _, msg := range runQuick(cmd) {
		pumpForm(f, msg)
	}
	if m.mcpView.vals.headers != "X-Key: ${tok}" || !strings.Contains(f.View(), "Secrets: mcp:linear:tok") {
		t.Errorf("headers %q; view:\n%s", m.mcpView.vals.headers, f.View())
	}
}

// A STALE reload of a Retry form counts edits against the definition it was
// filled from (held), and keeps comparing secrets against pending.previous.
func TestMcpRetryStaleReloadFlagsNothingUnchanged(t *testing.T) {
	p := pendingLinear()
	view := &pb.McpDefinitionView{Name: "linear", SecretsAvailable: true, PendingReplace: p, Fingerprint: "fp-1"}
	d := &fakeDaemon{mcpPendings: []*pb.McpPendingReplace{p}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view},
		mcpSaveResults: []mcpSaveResult{{err: refused(pb.McpSaveRefusal_STALE_CHANGED, "the pending replace changed")}}}
	m := editModel(t, d)
	m = keyIn(m, press("R"))
	m = keyIn(m, press("h"))
	*m.mcpView.secretPtr("mcp:linear:k") = "v"
	m = keyIn(m, ctrlS())
	for m.screen == screenConfirm {
		m = accept(t, m)
	}
	v := m.mcpView
	if v.form == nil || !strings.Contains(v.status, "reloaded") {
		t.Fatalf("expected a reload; status %q", v.status)
	}
	if len(v.flags) != 0 || v.fieldFlags("url") != "" || strings.Contains(v.status, "dropped") || v.secretValue("mcp:linear:k") != "v" {
		t.Errorf("nothing changed on the host: flags %v status %q", v.flags, v.status)
	}
	if v.vals.url != "https://new/mcp" || v.opened.GetUrl() != "https://old/mcp" || !v.urlChanged() {
		t.Errorf("a retry compares against pending.previous: vals %q opened %q", v.vals.url, v.opened.GetUrl())
	}
}

// No conversion is offered for a typed literal while secret storage is
// unavailable: the secret field it would move into does not exist.
func TestMcpNoConversionWithoutSecretStorage(t *testing.T) {
	view := linearView()
	view.SecretsAvailable, view.SecretsUnavailableReason = false, "keychain locked"
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": view}}
	m := openEdit(t, editModel(t, d))
	m.mcpView.vals.headers = "Authorization: Bearer sk-live-1"
	m = leaveGroup1(t, m)
	v := m.mcpView
	if m.screen == screenConfirm || !strings.Contains(v.status, "cannot be converted") || !strings.Contains(v.status, "keychain locked") ||
		!v.focusInDefinition() || v.secretValue("mcp:linear:authorization") != "" {
		t.Fatalf("screen %v status %q", m.screen, v.status)
	}
	m = keyIn(m, ctrlS())
	if m.screen == screenConfirm || len(d.mcpSaves) != 0 || !strings.Contains(m.mcpView.status, "cannot be converted") {
		t.Errorf("save: screen %v status %q", m.screen, m.mcpView.status)
	}
}

// The authorize offer follows the saved server's auth state.
func TestMcpAuthorizeOfferFollowsAuthState(t *testing.T) {
	m := editModel(t, &fakeDaemon{})
	for st, want := range map[pb.McpAuthState]bool{
		pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED:   true,
		pb.McpAuthState_MCP_AUTH_STATE_UNSPECIFIED:    true,
		pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED:     false,
		pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE: false,
	} {
		out := &pb.McpSaveOutcome{Name: "n", State: pb.McpSaveOutcome_REGISTERED,
			Server: &pb.McpServer{Name: "n", Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, AuthState: st}}
		offered := false
		for _, s := range m.mcpFollowups(out, true) {
			offered = offered || strings.HasPrefix(s.title, "Authorize")
		}
		if offered != want {
			t.Errorf("%v: offered = %v", st, offered)
		}
	}
}

// A pending row whose replace has ended elsewhere opens no dead form: the list
// is re-read and the status says so.
func TestMcpPendingRowWhoseReplaceEndedRelists(t *testing.T) {
	p := pendingLinear()
	d := &fakeDaemon{mcpPendings: []*pb.McpPendingReplace{p},
		mcpDefErr: &client.SaveRefusal{Reason: pb.McpSaveRefusal_GONE, Message: "linear is no longer registered", Code: codes.NotFound}}
	m := editModel(t, d)
	if !strings.Contains(m.View(), "only the failed replaces below") {
		t.Errorf("empty-list line with pending rows:\n%s", m.View())
	}
	d.mcpPendings = nil
	m = keyIn(m, press("R"))
	m = keyIn(m, press("h"))
	if m.mcpView.form != nil || !strings.Contains(m.mcpView.status, "has ended") || strings.Contains(m.View(), "replace failed") {
		t.Fatalf("GONE: form %v status %q\n%s", m.mcpView.form != nil, m.mcpView.status, m.View())
	}
	// Registered again by another path: the read succeeds with no pending.
	d.mcpDefErr, d.mcpPendings = nil, []*pb.McpPendingReplace{p}
	d.mcpViews = map[string]*pb.McpDefinitionView{"linear": linearView()}
	m = keyIn(m, press("r"))
	m = keyIn(m, press("X"))
	if m.screen != screenMcp || !strings.Contains(m.mcpView.status, "has ended") {
		t.Errorf("X: screen %v status %q", m.screen, m.mcpView.status)
	}
	m = keyIn(m, press("r"))
	m = keyIn(m, press("R"))
	m = keyIn(m, press("h"))
	if m.mcpView.form != nil || !strings.Contains(m.mcpView.status, "has ended") {
		t.Errorf("R: form %v status %q", m.mcpView.form != nil, m.mcpView.status)
	}
}

// A save whose reply outlasts the client's bound closes the form (saving again
// is unsafe), but the row is left to the daemon's own save state: the call may
// never have reached the daemon (a half-open tunnel), so nothing pins it to
// saving… and e/R/X stay usable (review round 4).
func TestMcpSaveClientTimeoutClosesTheForm(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	v := m.mcpView
	m, cmd := update(m, mcpSaveMsg{host: v.host, name: "linear", gen: v.formGen, err: status.Error(codes.DeadlineExceeded, "context deadline exceeded"), timedOut: true})
	m = drainEdit(m, cmd)
	if m.mcpView.vals != nil || m.mcpView.localSaving["linear"] || !strings.Contains(m.mcpView.status, "may still be running on the daemon") {
		t.Errorf("form open %v, saving %v, status %q", m.mcpView.vals != nil, m.mcpView.localSaving, m.mcpView.status)
	}
	if strings.Contains(m.View(), "saving…") {
		t.Errorf("the row is pinned to saving… although the daemon reports no save:\n%s", m.View())
	}
	m = keyIn(m, press("e"))
	if m.mcpView.vals == nil {
		t.Errorf("e is refused after a client timeout: status %q", m.mcpView.status)
	}
	// A save the daemon is still running shows from its own state.
	srv := linearServer()
	srv.SaveInProgress = true
	d.mcpServers = []*pb.McpServer{srv}
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, cmd = update(m, mcpSaveMsg{host: v.host, name: "linear", gen: m.mcpView.formGen + 1, err: status.Error(codes.DeadlineExceeded, "x"), timedOut: true})
	m = drainEdit(m, cmd)
	if !strings.Contains(m.View(), "saving…") {
		t.Errorf("the re-list's save_in_progress should show saving…:\n%s", m.View())
	}
}

// sendMcpSave flags timedOut only when its own deadline ended the call, not
// when the daemon itself answered DEADLINE_EXCEEDED.
func TestMcpSendSaveTimedOutOnlyOnTheClientDeadline(t *testing.T) {
	old := mcpSaveTimeout
	mcpSaveTimeout = 20 * time.Millisecond
	t.Cleanup(func() { mcpSaveTimeout = old })
	d := &fakeDaemon{mcpSaveBlock: true}
	m := editModel(t, d)
	if msg := m.sendMcpSave(&pb.SaveMcpServerRequest{Name: "linear"})().(mcpSaveMsg); !msg.timedOut || msg.err == nil {
		t.Errorf("client deadline: timedOut %v err %v", msg.timedOut, msg.err)
	}
	d.mcpSaveBlock = false
	d.mcpSaveResults = []mcpSaveResult{{err: status.Error(codes.DeadlineExceeded, "the daemon's own deadline")}}
	if msg := m.sendMcpSave(&pb.SaveMcpServerRequest{Name: "linear"})().(mcpSaveMsg); msg.timedOut || msg.err == nil {
		t.Errorf("daemon's reply: timedOut %v err %v", msg.timedOut, msg.err)
	}
}

// noFollowupDelay turns off the pause a key puts on deferred follow-ups, so a
// story sees them open right after the key.
func noFollowupDelay(t *testing.T) {
	t.Helper()
	old := mcpFollowupDelay
	mcpFollowupDelay = 0
	t.Cleanup(func() { mcpFollowupDelay = old })
}

// Review round 4: a key on the idle list really holds a deferred offer back,
// so a quick second esc leaves the screen instead of answering it, and a
// message that arrives inside the pause (a spinner tick, a re-list) does not
// open it either; the wake-up after the pause does.
func TestMcpDeferredFollowupsWaitOutAQuickSecondKey(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}}
	m := editModel(t, d)
	m.mcpView.deferred = []confirmState{{title: "Remove unused secrets?"}}
	m, cmd := update(m, press("j"))
	if cmd == nil || m.screen != screenMcp || len(m.mcpView.deferred) != 1 {
		t.Fatalf("a key should schedule the wake-up, not open: screen %v", m.screen)
	}
	m, _ = update(m, mcpFollowupsReadyMsg{}) // early: inside the pause
	if m.screen != screenMcp || len(m.mcpView.deferred) != 1 {
		t.Fatalf("the offer opened inside the pause: screen %v", m.screen)
	}
	m.mcpView.followAfter = time.Now().Add(-time.Millisecond) // the pause has passed
	m, _ = update(m, mcpFollowupsReadyMsg{})
	if m.screen != screenConfirm || m.mcpView.deferred != nil {
		t.Errorf("the offer should open after the pause: screen %v", m.screen)
	}

	m = editModel(t, d)
	m.mcpView.deferred = []confirmState{{title: "Remove unused secrets?"}}
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen == screenMcp || m.screen == screenConfirm {
		t.Errorf("a quick esc should leave the list: screen %v", m.screen)
	}
}

// A background save's follow-ups never open under a credential prompt; they
// wait until it is gone (SWI-9 R7).
func TestMcpDeferredFollowupsWaitForACredentialPrompt(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}}
	m := editModel(t, d)
	m.mcpView.deferred = []confirmState{{title: "Remove unused secrets?"}}
	m.hosts.connecting, m.hosts.pwMode = true, pwDaemonsScreen
	m, _ = update(m, mcpFollowupsReadyMsg{})
	if m.screen != screenMcp || len(m.mcpView.deferred) != 1 {
		t.Fatalf("the offer opened under the prompt: screen %v", m.screen)
	}
	m.hosts.connecting = false
	m, _ = update(m, mcpFollowupsReadyMsg{})
	if m.screen != screenConfirm || m.mcpView.deferred != nil {
		t.Errorf("the offer should open once the prompt is gone: screen %v", m.screen)
	}
}
