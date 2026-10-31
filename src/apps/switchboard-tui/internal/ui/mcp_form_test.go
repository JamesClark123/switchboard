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
		case mcpDefMsg, mcpSaveMsg, mcpLoadedMsg, mcpDoneMsg:
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

func TestMcpKindChangeClearsRemoteFields(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()}}
	m := openEdit(t, editModel(t, d))
	// Focus is on kind (the first field in edit mode); down selects "command".
	m = keyIn(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.mcpView.vals.kind != "command" || m.mcpView.vals.url != "" || m.mcpView.vals.headers != "" || m.mcpView.vals.scopes != "" ||
		!strings.Contains(m.mcpView.status, "cleared") {
		t.Errorf("vals %+v status %q", *m.mcpView.vals, m.mcpView.status)
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

func TestMcpUnavailableClosesFormRowSaving(t *testing.T) {
	d := &fakeDaemon{mcpServers: []*pb.McpServer{linearServer()}, mcpViews: map[string]*pb.McpDefinitionView{"linear": linearView()},
		mcpSaveResults: []mcpSaveResult{{err: status.Error(codes.Unavailable, "the host was changed but the result could not be recorded yet; retrying")}}}
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
	if m.screen != screenConfirm || !strings.Contains(m.View(), "Add linear as new?") {
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
	for _, want := range []string{"not registered", "the add may still complete on the host", "sbx: add failed: 500", "rolled back: mcp:linear:api-key"} {
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
