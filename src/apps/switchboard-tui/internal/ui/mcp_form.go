package ui

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/grpc/status"
)

// The MCP add/edit form (feature 008, SWI-21). Without the daemon's "mcp-edit"
// capability it is the feature-008 add form (one group, AddMcpServer). With it,
// it is the add/edit/retry form of SWI-21 (research R14): group 1 is the
// definition, group 2 the secrets, rebuilt from group 1 each time it is
// entered, and every save is one SaveMcpServer call behind the contract's
// y-only confirmations (contracts/mcp-form-ui.md). Every client-side rule goes
// through mcprules (FR-420), so a daemon refusal never costs the form.

type mcpFormMode int

const (
	mcpModeAdd mcpFormMode = iota
	mcpModeEdit
	mcpModeRetry
)

// mcpFormVals holds the form's comparable fields only (SWI-9 guard). Secret
// values, the opened definition and the view live beside it in mcpState.
type mcpFormVals struct {
	name, kind, url, command, args, dir string
	headers                             string // one "Name: value" per line (remote only, FR-103)
	clientID, authServer                string // pre-registered OAuth client (remote only, FR-105)
	scopes                              string // space-separated default scopes (remote only, FR-416)
	attach                              bool
	mode                                mcpFormMode
}

// mcpSnapshot is what the guard compares: the vals plus every non-empty entered
// secret, so a typed masked value counts as an edit (T032).
type mcpSnapshot struct {
	vals    mcpFormVals
	secrets map[string]string
}

func (s mcpSnapshot) equal(o mcpSnapshot) bool {
	return s.vals == o.vals && maps.Equal(s.secrets, o.secrets)
}

// mcpSnapshot copies the open form's values; the secret map is a fresh copy, so
// a baseline never aliases the live values.
func (v mcpState) mcpSnapshot() mcpSnapshot {
	s := mcpSnapshot{secrets: map[string]string{}}
	if v.vals != nil {
		s.vals = *v.vals
	}
	for name, p := range v.secretVals {
		if p != nil && *p != "" {
			s.secrets[name] = *p
		}
	}
	return s
}

// mcpDefinitionKeys are the field keys of group 1; any other focused field
// (a secret input, or group 2's note) is in the secrets group.
var mcpDefinitionKeys = map[string]bool{
	"name": true, "kind": true, "url": true, "headers": true, "client_id": true,
	"auth_server": true, "scopes": true, "command": true, "args": true, "dir": true, "attach": true,
}

// --- vals <-> definitions ---

func headerLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func argList(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// mcpDefFromVals is the definition the vals describe, unvalidated.
func mcpDefFromVals(vals mcpFormVals) *pb.McpServerDefinition {
	if vals.kind == "command" {
		return &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: &pb.AddMcpServerRequest_LocalCommand{
			Command: strings.TrimSpace(vals.command), Args: argList(vals.args), Dir: strings.TrimSpace(vals.dir)}}
	}
	return &pb.McpServerDefinition{
		Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: strings.TrimSpace(vals.url), Headers: headerLines(vals.headers),
		ClientId: strings.TrimSpace(vals.clientID), OauthAuthorizationServer: strings.TrimSpace(vals.authServer),
		Scopes: mcprules.ParseScopes(vals.scopes),
	}
}

// mcpValsFromDef pre-fills the vals from a definition (FR-402).
func mcpValsFromDef(def *pb.McpServerDefinition, name string, attach bool, mode mcpFormMode) mcpFormVals {
	vals := mcpFormVals{name: name, kind: "remote", attach: attach, mode: mode}
	if def.GetKind() == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
		vals.kind = "command"
	}
	vals.url = def.GetUrl()
	vals.headers = strings.Join(def.GetHeaders(), "\n")
	vals.clientID = def.GetClientId()
	vals.authServer = def.GetOauthAuthorizationServer()
	vals.scopes = strings.Join(def.GetScopes(), " ")
	vals.command = def.GetCommand().GetCommand()
	vals.args = strings.Join(def.GetCommand().GetArgs(), ", ")
	vals.dir = def.GetCommand().GetDir()
	return vals
}

// fillMcpVals is the edit form's vals from a definition read (SWI-9 pattern:
// fill, then build from the vals).
func fillMcpVals(view *pb.McpDefinitionView) mcpFormVals {
	return mcpValsFromDef(view.GetDefinition(), view.GetName(), view.GetAttachByDefault(), mcpModeEdit)
}

// mcpField is one comparable field of the form, for the FR-404 reload.
type mcpField struct {
	key   string                                      // the form field key (and flag field name)
	get   func(v mcpFormVals) string                  // the field's text as the form shows it
	set   func(v *mcpFormVals, s string)              // write it back
	def   func(v mcpFormVals) *pb.McpServerDefinition // a definition holding only this field
	label string
}

var mcpFields = []mcpField{
	{key: "kind", label: "kind", get: func(v mcpFormVals) string { return v.kind }, set: func(v *mcpFormVals, s string) { v.kind = s }},
	{key: "url", label: "URL", get: func(v mcpFormVals) string { return v.url }, set: func(v *mcpFormVals, s string) { v.url = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition { return &pb.McpServerDefinition{Url: v.url} }},
	{key: "headers", label: "headers", get: func(v mcpFormVals) string { return v.headers }, set: func(v *mcpFormVals, s string) { v.headers = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition {
			return &pb.McpServerDefinition{Headers: headerLines(v.headers)}
		}},
	{key: "client_id", label: "client ID", get: func(v mcpFormVals) string { return v.clientID }, set: func(v *mcpFormVals, s string) { v.clientID = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition { return &pb.McpServerDefinition{ClientId: v.clientID} }},
	{key: "auth_server", label: "authorization server", get: func(v mcpFormVals) string { return v.authServer }, set: func(v *mcpFormVals, s string) { v.authServer = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition {
			return &pb.McpServerDefinition{OauthAuthorizationServer: v.authServer}
		}},
	{key: "scopes", label: "scopes", get: func(v mcpFormVals) string { return v.scopes }, set: func(v *mcpFormVals, s string) { v.scopes = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition {
			return &pb.McpServerDefinition{Scopes: mcprules.ParseScopes(v.scopes)}
		}},
	{key: "command", label: "command", get: func(v mcpFormVals) string { return v.command }, set: func(v *mcpFormVals, s string) { v.command = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition {
			return &pb.McpServerDefinition{Command: &pb.AddMcpServerRequest_LocalCommand{Command: v.command}}
		}},
	{key: "args", label: "arguments", get: func(v mcpFormVals) string { return v.args }, set: func(v *mcpFormVals, s string) { v.args = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition {
			return &pb.McpServerDefinition{Command: &pb.AddMcpServerRequest_LocalCommand{Command: "-", Args: argList(v.args)}}
		}},
	{key: "dir", label: "working directory", get: func(v mcpFormVals) string { return v.dir }, set: func(v *mcpFormVals, s string) { v.dir = s },
		def: func(v mcpFormVals) *pb.McpServerDefinition {
			return &pb.McpServerDefinition{Command: &pb.AddMcpServerRequest_LocalCommand{Command: "-", Dir: v.dir}}
		}},
	{key: "attach", label: "attach to every sandbox", get: func(v mcpFormVals) string { return strconv.FormatBool(v.attach) },
		set: func(v *mcpFormVals, s string) { v.attach = s == "true" }},
}

// same compares one field of two vals in normalised form (mcprules.Equal), so a
// difference only in presentation is not an edit.
func (f mcpField) same(a, b mcpFormVals) bool {
	if f.def == nil {
		return f.get(a) == f.get(b)
	}
	return mcprules.Equal(f.def(a), f.def(b))
}

// mcpReload is the outcome of merging a re-read definition into an open form
// (FR-404, T030): the merged vals, the per-field flags, and the entered
// secrets the reload drops.
type mcpReload struct {
	vals    mcpFormVals
	flags   map[string]string
	dropped []string        // full secret names dropped, in order
	reenter map[string]bool // dropped header secrets that must be re-entered
}

// mergeMcpReload keeps every field the developer edited (form value != opened)
// and takes the re-read value for the others; a field the host changed is
// flagged either way. Entered secrets are dropped by the FR-404 rules.
func mergeMcpReload(cur, opened, reread mcpFormVals, server string, secrets map[string]string) mcpReload {
	out := mcpReload{vals: cur, flags: map[string]string{}, reenter: map[string]bool{}}
	replaced := map[string]bool{}
	for _, f := range mcpFields {
		edited := !f.same(cur, opened)
		hostChanged := !f.same(opened, reread)
		if hostChanged {
			out.flags[f.key] = "changed on the host: was " + quoteOrNone(f.get(opened)) + ", host now " + quoteOrNone(f.get(reread))
		}
		if !edited {
			f.set(&out.vals, f.get(reread))
			replaced[f.key] = hostChanged
		}
	}
	cs := mcprules.ClientSecretName(server)
	placeholders := map[string]bool{}
	if out.vals.kind != "command" {
		for _, p := range mcprules.Placeholders(headerLines(out.vals.headers)) {
			placeholders[mcprules.SecretName(server, p)] = true
		}
	}
	clientGone := out.vals.kind == "command" || strings.TrimSpace(out.vals.clientID) == ""
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		if secrets[name] == "" {
			continue
		}
		switch {
		case name == cs:
			if replaced["url"] || replaced["client_id"] || replaced["auth_server"] || clientGone {
				out.dropped = append(out.dropped, name)
			}
		case replaced["url"]:
			out.dropped = append(out.dropped, name)
			out.reenter[name] = placeholders[name]
		case !placeholders[name]:
			out.dropped = append(out.dropped, name)
		}
	}
	return out
}

func quoteOrNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return strconv.Quote(strings.ReplaceAll(s, "\n", "; "))
}

// --- form state helpers ---

// mcpServerName is the name the form saves (the read-only name in edit and
// retry mode).
func (v mcpState) mcpServerName() string {
	if v.vals == nil {
		return ""
	}
	return strings.TrimSpace(v.vals.name)
}

// mcpNamePlaceholder stands in for the server name in secret names while the
// add form's Name is blank, so fields, conversions and renames agree on one key.
const mcpNamePlaceholder = "<name>"

// secretServerName is the server part of a secret name for a form name.
func secretServerName(name string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return mcpNamePlaceholder
}

// secretServer is the server part of the open form's secret names.
func (v mcpState) secretServer() string { return secretServerName(v.mcpServerName()) }

// mcpSecretField is one masked input of the secrets group.
type mcpSecretField struct {
	name   string // full secret name
	header string // the placeholder (empty for the client secret)
	client bool
}

// secretFields are the secret inputs the current definition needs: one per
// distinct valid placeholder, then the client secret when a client ID is set.
func (v mcpState) secretFields() []mcpSecretField {
	if v.vals == nil || v.vals.kind == "command" {
		return nil
	}
	server := v.secretServer()
	var out []mcpSecretField
	for _, p := range mcprules.Placeholders(headerLines(v.vals.headers)) {
		if mcprules.ValidPlaceholder(p) == nil {
			out = append(out, mcpSecretField{name: mcprules.SecretName(server, p), header: p})
		}
	}
	if strings.TrimSpace(v.vals.clientID) != "" {
		out = append(out, mcpSecretField{name: mcprules.ClientSecretName(server), client: true})
	}
	return out
}

// secretPtr is the stable value pointer of a secret field, kept by name across
// rebuilds (R14).
func (v *mcpState) secretPtr(name string) *string {
	if v.secretVals == nil {
		v.secretVals = map[string]*string{}
	}
	p, ok := v.secretVals[name]
	if !ok {
		p = new(string)
		v.secretVals[name] = p
	}
	return p
}

func (v mcpState) secretValue(name string) string {
	if p := v.secretVals[name]; p != nil {
		return *p
	}
	return ""
}

func (v mcpState) secretsAvailable() (bool, string) {
	if v.view == nil || v.view.GetSecretsAvailable() {
		return true, ""
	}
	reason := v.view.GetSecretsUnavailableReason()
	if reason == "" {
		reason = "secret storage is unavailable on this host"
	}
	return false, reason
}

// stored reports a global-scope secret the host lists (FR-409).
func (v mcpState) stored(name string) bool {
	if slices.Contains(v.view.GetStoredSecrets(), name) {
		return true
	}
	for _, s := range v.view.GetSecrets() {
		if s.GetName() == name && s.GetStatus() != pb.McpSecretStatus_MISSING && s.GetStatus() != pb.McpSecretStatus_STATUS_UNSPECIFIED {
			return true
		}
	}
	return false
}

// removalFailed reports a name a pending replace holds as removal-failed that
// is still listed (FR-406, FR-409).
func (v mcpState) removalFailed(name string) bool {
	for _, s := range v.view.GetSecrets() {
		if s.GetName() == name && s.GetStatus() == pb.McpSecretStatus_REMOVAL_FAILED {
			return true
		}
	}
	p := v.pendingHeld()
	return p != nil && slices.Contains(p.GetRemovalFailedSecrets(), name) && v.stored(name)
}

// pendingHeld is the pending replace the form works on: the Retry form's, or
// the one a NOT_REGISTERED outcome left.
func (v mcpState) pendingHeld() *pb.McpPendingReplace {
	if v.holding != nil {
		return v.holding
	}
	return v.retry
}

// urlChanged reports an edit of the URL from the definition the form opened
// with: the runtime binds header secrets to the endpoint (FR-409).
func (v mcpState) urlChanged() bool {
	return v.opened != nil && v.vals != nil && v.opened.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_LOCAL &&
		strings.TrimSpace(v.vals.url) != strings.TrimSpace(v.opened.GetUrl())
}

const (
	mcpStatusSet           = "set (blank keeps it)"
	mcpStatusMissing       = "missing"
	mcpStatusReenterURL    = "must be re-entered (URL changed)"
	mcpStatusRemovalFailed = "removal failed; will be removed unless re-entered"
	mcpStatusHeldOnHost    = "re-enter the credential (held on the host)"
)

// secretStatus words one field's status from stored_secrets and the view.
func (v mcpState) secretStatus(f mcpSecretField) string {
	switch {
	case v.reenter[f.name] != "":
		return v.reenter[f.name]
	case v.removalFailed(f.name):
		return mcpStatusRemovalFailed
	case !f.client && v.urlChanged():
		return mcpStatusReenterURL
	case v.stored(f.name):
		return mcpStatusSet
	}
	return mcpStatusMissing
}

// fieldFlags renders the view's flags (FR-402, FR-423) and the reload's for one
// field key, one per line.
func (v mcpState) fieldFlags(key string) string {
	var lines []string
	for _, f := range v.view.GetFlags() {
		field := f.GetField()
		if strings.HasPrefix(field, "headers/") {
			field = "headers"
		}
		if field == "oauth_authorization_server" {
			field = "auth_server"
		}
		if field != key {
			continue
		}
		which := ""
		if h, ok := strings.CutPrefix(f.GetField(), "headers/"); ok {
			which = h + ": "
		}
		switch f.GetKind() {
		case pb.McpFieldFlag_CHANGED_ON_HOST:
			if f.GetRecordedValue() == "" && f.GetHostValue() == "" {
				lines = append(lines, which+"changed on the host")
				continue
			}
			lines = append(lines, which+"changed on the host: was "+quoteOrNone(f.GetRecordedValue())+", host now "+quoteOrNone(f.GetHostValue()))
		case pb.McpFieldFlag_NOT_RECOVERABLE:
			lines = append(lines, which+"not recoverable from the host — re-enter")
		case pb.McpFieldFlag_LITERAL_ON_HOST:
			lines = append(lines, which+"literal credential on host — convert before re-registering")
		case pb.McpFieldFlag_RESOLVED_UNCHECKABLE:
			lines = append(lines, which+"host value cannot be checked against the template; a re-registering save registers the template")
		}
	}
	if s := v.flags[key]; s != "" {
		lines = append(lines, s)
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n⚑ " + strings.Join(lines, "\n⚑ ")
}

// literalOnHost maps a lower-cased header name to the redacted value the view
// returned for it (FR-423): such a header holds no real literal.
func (v mcpState) literalOnHost() map[string]string {
	out := map[string]string{}
	names := map[string]bool{}
	for _, n := range v.view.GetDefinition().GetLiteralOnHost() {
		names[strings.ToLower(n)] = true
	}
	for _, f := range v.view.GetFlags() {
		if h, ok := strings.CutPrefix(f.GetField(), "headers/"); ok && f.GetKind() == pb.McpFieldFlag_LITERAL_ON_HOST {
			names[strings.ToLower(h)] = true
		}
	}
	for _, h := range v.view.GetDefinition().GetHeaders() {
		n, val := mcprules.SplitHeader(h)
		if names[strings.ToLower(n)] {
			out[strings.ToLower(n)] = val
		}
	}
	return out
}

// --- building ---

// buildMcpForm builds the open form from the vals with a fresh registry; it
// never fills the vals, so reopenForm keeps what the user typed. The baseline is
// only taken when the form first opens.
func (m *Model) buildMcpForm() tea.Cmd {
	v := &m.mcpView
	v.spec = newFormSpec()
	if v.hasEdit {
		v.form = m.mcpEditForm(v.spec, v.vals)
	} else {
		v.form = m.mcpForm(v.spec, v.vals)
	}
	return v.form.Init()
}

// mcpForm is the feature-008 add form (no "mcp-edit" capability).
func (m Model) mcpForm(spec *formSpec, v *mcpFormVals) *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("name").Title("Name").
			Description("Letters, digits, dots, hyphens, underscores. Unique on this host.").Value(&v.name),
		huh.NewSelect[string]().Key("kind").Title("Kind").
			Options(huh.NewOption("Remote endpoint (URL)", "remote"), huh.NewOption("Host command (runs on the daemon's host)", "command")).Value(&v.kind),
		huh.NewInput().Key("url").Title("URL").Description("Remote endpoint, e.g. https://mcp.notion.com/mcp").Value(&v.url),
		spec.text(&v.headers, "Headers (remote only)").Key("headers").Lines(3).
			Description("One per line, Name: value. For credentials use a ${placeholder} and store its value as the secret mcp:<name>:<placeholder> (daemon screen › Secrets). Tool-name collisions between servers (e.g. list_issues in both GitHub and Linear) are avoided with the server's own tool-filter headers, e.g. GitHub X-MCP-Toolsets / X-MCP-Tools."),
		huh.NewInput().Key("client_id").Title("OAuth client ID (remote only)").
			Description("For servers without Dynamic Client Registration (e.g. Slack): the client ID you registered with the provider. A confidential client also needs its secret stored as mcp:<name>:client_secret (daemon screen › Secrets) before adding — without it, authorization is skipped.").Value(&v.clientID),
		huh.NewInput().Key("auth_server").Title("OAuth authorization server (remote only)").
			Description("Only if the server publishes no OAuth metadata: an https URL (http only to localhost) or an absolute path on the daemon's host to RFC 8414 metadata.").Value(&v.authServer),
		huh.NewInput().Key("command").Title("Command").Description("Host command, e.g. npx").Value(&v.command),
		huh.NewInput().Key("args").Title("Arguments").Description("Comma-separated, e.g. @playwright/mcp@latest").Value(&v.args),
		huh.NewInput().Key("dir").Title("Working directory").Description("Optional, host command only.").Value(&v.dir),
		huh.NewConfirm().Key("attach").Title("Attach to every sandbox").
			Description("On = every sandbox launched on this daemon gets this server (FR-080).").Value(&v.attach),
	)).WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(formKeyMap())
}

// mcpFormTitle is group 1's title: the name is shown, not editable, in edit and
// retry mode (FR-401).
func (m Model) mcpFormTitle() string {
	v := m.mcpView
	switch v.vals.mode {
	case mcpModeEdit:
		return "Edit " + v.mcpServerName() + " @ " + v.hostName
	case mcpModeRetry:
		return "Retry " + v.mcpServerName() + " @ " + v.hostName
	}
	return "Register an MCP server @ " + v.hostName
}

// mcpEditForm is the SWI-21 add/edit/retry form: Definition, then Secrets.
func (m Model) mcpEditForm(spec *formSpec, vals *mcpFormVals) *huh.Form {
	v := &m.mcpView
	var def []huh.Field
	if vals.mode == mcpModeAdd {
		def = append(def, huh.NewInput().Key("name").Title("Name").
			Description("Letters, digits, dots, hyphens, underscores. Unique on this host.").Value(&vals.name))
	}
	headersDesc := func() string {
		d := "One per line, Name: value. A credential header (Authorization, *-Key, *-Token, …) holds only ${placeholder}s; each gets a masked field in the next step. Other header values are kept in switchboard's record and shown when the form opens."
		var names []string
		for _, f := range v.secretFields() {
			if !f.client {
				names = append(names, f.name)
			}
		}
		if len(names) > 0 {
			d += "\nSecrets: " + strings.Join(names, ", ")
		}
		return d + v.fieldFlags("headers")
	}
	// huh re-runs a DescriptionFunc when the hash of its bindings changes, and
	// hashstructure skips unexported fields: bind exported pointers to what the
	// list depends on, so it follows typing (the vals struct would hash constant).
	headersDeps := &struct{ Name, Kind, Headers *string }{&vals.name, &vals.kind, &vals.headers}
	def = append(def,
		huh.NewSelect[string]().Key("kind").Title("Kind").
			Options(huh.NewOption("Remote endpoint (URL)", "remote"), huh.NewOption("Host command (runs on the daemon's host)", "command")).Value(&vals.kind),
		huh.NewInput().Key("url").Title("URL").Description("Remote endpoint, e.g. https://mcp.notion.com/mcp"+v.fieldFlags("url")).Value(&vals.url),
		spec.text(&vals.headers, "Headers (remote only)").Key("headers").Lines(3).DescriptionFunc(headersDesc, headersDeps),
		huh.NewInput().Key("client_id").Title("OAuth client ID (remote only)").
			Description("For servers without Dynamic Client Registration (e.g. Slack): the client ID you registered with the provider. Its client secret, if any, is entered in the next step."+v.fieldFlags("client_id")).Value(&vals.clientID),
		huh.NewInput().Key("auth_server").Title("OAuth authorization server (remote only)").
			Description("Only if the server publishes no OAuth metadata: an https URL (http only to localhost) or an absolute path on the daemon's host to RFC 8414 metadata."+v.fieldFlags("auth_server")).Value(&vals.authServer),
		huh.NewInput().Key("scopes").Title("Scopes (remote only)").
			Description("Space-separated default OAuth scopes, requested at every authorization."+v.fieldFlags("scopes")).Value(&vals.scopes),
		huh.NewInput().Key("command").Title("Command").Description("Host command, e.g. npx"+v.fieldFlags("command")).Value(&vals.command),
		huh.NewInput().Key("args").Title("Arguments").Description("Comma-separated, e.g. @playwright/mcp@latest").Value(&vals.args),
		huh.NewInput().Key("dir").Title("Working directory").Description("Optional, host command only.").Value(&vals.dir),
		huh.NewConfirm().Key("attach").Title("Attach to every sandbox").
			Description("On = every sandbox launched on this daemon gets this server (FR-080).").Value(&vals.attach),
	)
	desc := "Definition"
	if v.formNote != "" {
		desc += " — " + v.formNote
	}
	g1 := huh.NewGroup(def...).Title(m.mcpFormTitle()).Description(desc)

	// Group 2 always starts with a note, so it is never empty (huh v1.0.0
	// panics on an empty group); a lone note is focusable and submits.
	secrets := []huh.Field{huh.NewNote().Title("Secrets").Description(m.mcpSecretsNote())}
	if ok, _ := v.secretsAvailable(); ok {
		for _, f := range v.secretFields() {
			d := v.secretStatus(f)
			if f.client {
				d = "Needed only for a confidential client. " + d
				if v.view.GetDefinition().GetRebindPending() {
					d += "\nThe old client binding may still be in place. Saving with the client secret entered clears it and the mark (for a public client, remove the stored secret and its binding record instead)."
				}
			}
			secrets = append(secrets, huh.NewInput().Key("secret:"+f.name).Title(f.name).Description(d).
				EchoMode(huh.EchoModePassword).Value(v.secretPtr(f.name)))
		}
	}
	return huh.NewForm(g1, huh.NewGroup(secrets...).Title("Secrets")).
		WithTheme(huhTheme()).WithShowHelp(true).WithShowErrors(false).
		WithWidth(m.bodyWidth()).WithHeight(m.bodyHeight()).WithKeyMap(formKeyMap())
}

// mcpSecretsNote is group 2's leading note.
func (m Model) mcpSecretsNote() string {
	v := m.mcpView
	if ok, reason := v.secretsAvailable(); !ok {
		return "Secret storage is unavailable on this host: " + reason + ". A save that needs a new secret is refused."
	}
	fields := v.secretFields()
	if len(fields) == 0 {
		return "No secrets needed: the headers have no ${placeholder} and there is no OAuth client ID."
	}
	s := "Values are stored on " + v.hostName + "'s host when you save; a blank field keeps what is stored."
	if p := v.pendingHeld(); p != nil {
		if r := p.GetRemovedSecrets(); len(r) > 0 {
			s += "\nRemoved by the failed save (enter them again): " + strings.Join(r, ", ")
		}
		if r := p.GetRemovalFailedSecrets(); len(r) > 0 {
			s += "\nRemoval failed (removed unless re-entered): " + strings.Join(r, ", ")
		}
	}
	return s
}

// --- opening ---

// openMcpAdd opens the form in add mode (name prefilled for "add as new").
func (m Model) openMcpAdd(vals mcpFormVals) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	m.resetMcpForm()
	v.formGen++
	// A definition read still in flight (e, R, X) is superseded: its reply
	// must not replace this form (it is dropped while a form is open anyway),
	// and its loading line goes.
	v.clearMcpLoading(v.loadingDef)
	v.loadingDef = ""
	vals.mode = mcpModeAdd
	if vals.kind == "" {
		vals.kind = "remote"
	}
	v.vals = &vals
	cmd := m.buildMcpForm()
	if notice := normalizeNotice(v.spec.normalize()); notice != "" {
		v.status = notice
	}
	v.baseline = v.mcpSnapshot()
	if v.hasEdit {
		return m, tea.Batch(cmd, m.mcpForAddCmd())
	}
	return m, cmd
}

// openMcpFromView opens the edit (or retry) form from a definition read;
// opened is what FR-409/FR-415 compare with (the previous definition for a
// Retry), vals what a reload counts edits against, and setup sets the mode's
// extra state before the form is built.
func (m Model) openMcpFromView(view *pb.McpDefinitionView, vals mcpFormVals, opened *pb.McpServerDefinition, setup func(v *mcpState)) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	m.resetMcpForm()
	v.formGen++
	v.view = view
	v.fingerprint = view.GetFingerprint()
	v.opened, v.filled = opened, vals
	v.vals = &vals
	if setup != nil {
		setup(v)
	}
	cmd := m.buildMcpForm()
	if notice := normalizeNotice(v.spec.normalize()); notice != "" {
		v.status = notice
	}
	v.baseline = v.mcpSnapshot()
	return m, cmd
}

// resetMcpForm clears every per-form field (a fresh form, or leaving one).
func (m *Model) resetMcpForm() {
	v := &m.mcpView
	v.form, v.vals, v.spec, v.pending = nil, nil, nil, nil
	v.secretVals, v.opened, v.view, v.fingerprint = nil, nil, nil, ""
	v.retry, v.holding, v.flags, v.reenter = nil, nil, nil, nil
	v.declinedHost, v.formNote, v.saving, v.statusFor, v.retryPrevious = nil, "", false, "", false
	v.leaveRefusal = ""
	v.baseline, v.filled, v.remoteStash = mcpSnapshot{}, mcpFormVals{}, nil
}

// mcpForAddCmd reads the secret status for the name typed in add mode (R11).
func (m Model) mcpForAddCmd() tea.Cmd {
	v := m.mcpView
	name := v.mcpServerName()
	if !v.hasEdit || v.vals == nil || v.vals.mode != mcpModeAdd || !mcpNameRe.MatchString(name) {
		return nil
	}
	d, host, gen := v.daemon, v.host, v.formGen
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		view, err := d.GetMcpServerDefinition(ctx, name, true)
		return mcpDefMsg{host: host, name: name, purpose: mcpDefForAdd, view: view, err: err, gen: gen}
	}
}

// --- advancing ---

// focusInDefinition reports whether the focused field is in group 1.
func (v mcpState) focusInDefinition() bool {
	if v.form == nil {
		return false
	}
	f := v.form.GetFocusedField()
	return f != nil && mcpDefinitionKeys[f.GetKey()]
}

func (m Model) advanceMcpForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.form == nil {
		return m, nil
	}
	before := v.focusInDefinition()
	prevKind, prevName := v.vals.kind, strings.TrimSpace(v.vals.name)
	f, cmd := v.form.Update(msg)
	if ff, ok := f.(*huh.Form); ok {
		v.form = ff
	}
	if v.form.State == huh.StateCompleted {
		return reviveCompleted(gMcp)(m.applyMcpForm())
	}
	if !v.hasEdit {
		return m, cmd
	}
	cmds := []tea.Cmd{cmd}
	if name := strings.TrimSpace(v.vals.name); v.vals.mode == mcpModeAdd && name != prevName {
		v.renameSecrets(prevName, name)
		cmds = append(cmds, m.mcpForAddCmd())
	}
	if v.vals.kind != prevKind && v.switchMcpKind() {
		cmds = append(cmds, m.reopenForm(gMcp))
		m.focusMcpField("kind")
		return m, tea.Batch(cmds...)
	}
	if before && !v.focusInDefinition() {
		mm, c := m.leaveMcpDefinition()
		return mm, tea.Batch(append(cmds, c)...)
	}
	return m, tea.Batch(cmds...)
}

// switchMcpKind applies a Kind change: to "host command" it stashes and clears
// the remote-only fields; back to remote it restores them, unless the developer
// has typed remote values since. It reports whether the vals changed (the form
// must then be rebuilt so its widgets show them).
func (v *mcpState) switchMcpKind() bool {
	vals := v.vals
	remoteEmpty := vals.url == "" && vals.headers == "" && vals.clientID == "" && vals.authServer == "" && vals.scopes == ""
	switch {
	case vals.kind == "command" && !remoteEmpty:
		stash := *vals
		v.remoteStash = &stash
		vals.url, vals.headers, vals.clientID, vals.authServer, vals.scopes = "", "", "", "", ""
		v.status = "host command: cleared the URL, headers, OAuth client and scopes (remote only); switch back to remote to restore them"
		return true
	case vals.kind != "command" && v.remoteStash != nil:
		st := v.remoteStash
		v.remoteStash = nil
		if !remoteEmpty {
			return false
		}
		vals.url, vals.headers, vals.clientID, vals.authServer, vals.scopes = st.url, st.headers, st.clientID, st.authServer, st.scopes
		v.status = "remote endpoint: restored the URL, headers, OAuth client and scopes"
		return true
	}
	return false
}

// focusMcpField moves the rebuilt form's focus to key within group 1.
func (m *Model) focusMcpField(key string) {
	f := m.mcpView.form
	for i := 0; i < len(mcpDefinitionKeys) && f.GetFocusedField() != nil && f.GetFocusedField().GetKey() != key; i++ {
		f.NextField()
	}
}

// mcpConversion is one literal-credential conversion offer (FR-423).
type mcpConversion struct {
	header string // the header's name as typed
	cookie string // in a Cookie header, the cookie whose value converts next
	line   int    // its index in headerLines
	onHost bool   // the view's redacted LITERAL_ON_HOST value: no literal to move
}

// mcpConversions lists the credential headers holding literal text, skipping
// on-host literals the developer already declined.
func (v mcpState) mcpConversions() []mcpConversion {
	if v.vals == nil || v.vals.kind == "command" {
		return nil
	}
	host := v.literalOnHost()
	var out []mcpConversion
	for i, h := range headerLines(v.vals.headers) {
		n, val := mcprules.SplitHeader(h)
		if mcprules.CheckCredentialValue(n, val) == nil {
			continue
		}
		onHost := host[strings.ToLower(n)] != "" && host[strings.ToLower(n)] == val
		if !onHost && !mcprules.HasLiteral(n, val) {
			// Empty or separators only: Convert has nothing to move, so no
			// offer opens; leaveMcpDefinition refuses the header instead.
			continue
		}
		if onHost && v.declinedHost[strings.ToLower(n)] {
			continue
		}
		out = append(out, mcpConversion{header: n, cookie: mcprules.LiteralCookie(n, val), line: i, onHost: onHost})
	}
	return out
}

// convertMcpHeader applies mcprules.Convert to one header: the value becomes the
// scheme plus a ${placeholder}, and a typed literal moves into that secret's
// field (an on-host literal does not: its field stays blank, R14). It returns
// "" and changes nothing when Convert finds no literal to move.
func (m *Model) convertMcpHeader(c mcpConversion) string {
	v := &m.mcpView
	lines := headerLines(v.vals.headers)
	if c.line >= len(lines) {
		return ""
	}
	used := map[string]bool{}
	for _, p := range mcprules.Placeholders(lines) {
		used[p] = true
	}
	n, val := mcprules.SplitHeader(lines[c.line])
	newVal, placeholder, secret := mcprules.Convert(n, val, used)
	if newVal == val {
		return ""
	}
	lines[c.line] = n + ": " + newVal
	v.vals.headers = strings.Join(lines, "\n")
	name := mcprules.SecretName(v.secretServer(), placeholder)
	if c.onHost {
		if v.reenter == nil {
			v.reenter = map[string]string{}
		}
		v.reenter[name] = mcpStatusHeldOnHost
		*v.secretPtr(name) = ""
	} else {
		*v.secretPtr(name) = secret
	}
	return name
}

// mcpConversionConfirm is the y-only offer for one header.
func (m Model) mcpConversionConfirm(c mcpConversion) confirmState {
	v := m.mcpView
	body := []string{"The " + selectedStyle.Render(c.header) + " header is a credential header and holds literal text."}
	title := "Convert " + c.header + " to a secret?"
	if c.cookie != "" {
		// A Cookie header is offered once per cookie: say which one.
		body = []string{"The " + selectedStyle.Render(c.cookie) + " cookie in the " + c.header + " header holds literal text."}
		title = "Convert the " + c.cookie + " cookie to a secret?"
	}
	if c.onHost {
		body = append(body,
			"The host holds that credential; its value cannot be read back.",
			"Converting rewrites the header to a ${placeholder}; enter the credential",
			"again in its secret field (it stays blank and missing until you do).")
	} else {
		body = append(body,
			"Converting rewrites it to a ${placeholder} and moves the typed value",
			"into a masked secret field ("+mcprules.SecretName(v.secretServer(), "<placeholder>")+").")
	}
	return confirmState{
		title:       title,
		body:        body,
		notice:      "Saving a credential header with literal text is refused.",
		acceptLabel: "convert", cancelLabel: "keep as typed",
	}
}

// mcpChain opens steps one after another, each y-only over the gateway view:
// accepting moves on and the last runs done; backing out runs back (R7: each
// opens from the key that answered the one before).
func (m *Model) mcpChain(steps []confirmState, done, back func(m *Model) tea.Cmd) tea.Cmd {
	if len(steps) == 0 {
		return done(m)
	}
	st, rest := steps[0], steps[1:]
	st.yOnly, st.returnTo = true, screenMcp
	st.next = func(m *Model) tea.Cmd { return m.mcpChain(rest, done, back) }
	st.onCancel = back
	m.confirm = st
	m.screen = screenConfirm
	return nil
}

// leaveMcpDefinition runs when focus leaves group 1 (R14): each literal
// credential header gets a conversion offer, then the form is rebuilt so the
// secrets group matches the headers, and focus lands in it. A credential
// header the rule still refuses (one with nothing to convert, such as an empty
// or separators-only value) returns to the form at the headers with the
// refusal instead.
func (m Model) leaveMcpDefinition() (tea.Model, tea.Cmd) {
	return m, m.mcpOfferConversions(m.mcpView.mcpConversions(), func(m *Model) tea.Cmd {
		if err := m.mcpView.mcpCredentialRefusal(); err != nil {
			return m.refuseMcpLeave(ruleMsg(err))
		}
		// A refusal from an earlier leave no longer holds: clear it (a status
		// set since, such as "converted X to Y", stays), keeping the warning
		// for any on-host literal still declined.
		v := &m.mcpView
		if v.leaveRefusal != "" && v.status == v.leaveRefusal {
			v.status = v.declinedHostWarning()
		}
		v.leaveRefusal = ""
		return m.enterMcpSecrets()
	})
}

// refuseMcpLeave returns to the form at the headers with a refusal, keeping
// the warning for any declined on-host literal (it shows only once otherwise).
func (m *Model) refuseMcpLeave(msg string) tea.Cmd {
	v := &m.mcpView
	if w := v.declinedHostWarning(); w != "" {
		msg += "; " + w
	}
	v.status, v.leaveRefusal = msg, msg
	cmd := m.reopenForm(gMcp)
	m.focusMcpField("headers")
	return cmd
}

// declinedHostWarning words the declined on-host literals the headers still
// hold, or "" when there are none.
func (v mcpState) declinedHostWarning() string {
	if v.vals == nil || v.vals.kind == "command" || len(v.declinedHost) == 0 {
		return ""
	}
	host := v.literalOnHost()
	var out []string
	for _, h := range headerLines(v.vals.headers) {
		n, val := mcprules.SplitHeader(h)
		if r := host[strings.ToLower(n)]; r != "" && r == val && v.declinedHost[strings.ToLower(n)] {
			out = append(out, n+": literal credential on host — a save that re-registers is refused until it is converted")
		}
	}
	return strings.Join(out, "; ")
}

// mcpCredentialRefusal applies the credential header rule to the typed
// headers, skipping on-host literals as continueMcpSave does (their refusal
// depends on whether the save re-registers).
func (v mcpState) mcpCredentialRefusal() error {
	if v.vals == nil || v.vals.kind == "command" {
		return nil
	}
	lines := headerLines(v.vals.headers)
	skip := map[string]bool{}
	host := v.literalOnHost()
	for _, h := range lines {
		n, val := mcprules.SplitHeader(h)
		if r := host[strings.ToLower(n)]; r != "" && r == val {
			skip[strings.ToLower(n)] = true
		}
	}
	return mcprules.CheckCredentialHeaders(lines, skip)
}

// mcpOfferConversions offers each conversion in turn, then runs done. Declining
// a typed literal returns to the form at the headers with the refusal;
// declining an on-host literal goes on with the warning (secrets- and
// mark-only saves stay reachable).
func (m *Model) mcpOfferConversions(convs []mcpConversion, done func(m *Model) tea.Cmd) tea.Cmd {
	if len(convs) == 0 {
		return done(m)
	}
	c := convs[0]
	if ok, reason := m.mcpView.secretsAvailable(); !ok && !c.onHost {
		// A typed literal would move into a secret field that does not exist,
		// and every save storing it would be refused: no offer, the refusal.
		return m.refuseMcpLeave("error: " + c.header + " holds a literal credential and cannot be converted: secret storage is unavailable on this host (" +
			reason + "); remove the header, or write a ${placeholder} by hand and store its value once storage is back")
	}
	st := m.mcpConversionConfirm(c)
	st.yOnly, st.returnTo = true, screenMcp
	st.next = func(m *Model) tea.Cmd {
		name := m.convertMcpHeader(c)
		if name == "" {
			// The rule refuses the header but Convert finds nothing to move:
			// offering it again would loop, so stop with the refusal.
			why := c.header + " holds literal text"
			if lines := headerLines(m.mcpView.vals.headers); c.line < len(lines) {
				if err := mcprules.CheckCredentialValue(mcprules.SplitHeader(lines[c.line])); err != nil {
					why = ruleText(err)
				}
			}
			return m.refuseMcpLeave("error: " + why + " — it cannot be converted; edit it to hold only ${placeholder}s (saving it as typed is refused)")
		}
		m.mcpView.status = "converted " + c.header + " to " + name
		return m.mcpOfferConversions(m.mcpView.mcpConversions(), done)
	}
	st.onCancel = func(m *Model) tea.Cmd {
		v := &m.mcpView
		if c.onHost {
			if v.declinedHost == nil {
				v.declinedHost = map[string]bool{}
			}
			v.declinedHost[strings.ToLower(c.header)] = true
			v.status = c.header + ": literal credential on host — a save that re-registers is refused until it is converted"
			return m.mcpOfferConversions(m.mcpView.mcpConversions(), done)
		}
		return m.refuseMcpLeave("error: " + c.header + " holds a literal credential; convert it to a ${placeholder} (saving it as typed is refused)")
	}
	m.confirm = st
	m.screen = screenConfirm
	return nil
}

// enterMcpSecrets rebuilds the form from the vals and moves into group 2.
func (m *Model) enterMcpSecrets() tea.Cmd {
	cmd := m.reopenForm(gMcp)
	if m.mcpView.form == nil {
		return cmd
	}
	return tea.Batch(cmd, m.mcpView.form.NextGroup())
}

// reviveMcpForm returns to the form after a confirm was declined or a save
// refused: a completed (dead) form is rebuilt from the same vals.
func (m *Model) reviveMcpForm() tea.Cmd {
	if f := m.mcpView.form; f == nil || f.State != huh.StateNormal {
		return m.reopenForm(gMcp)
	}
	return nil
}

// --- saving ---

// applyMcpForm validates the form and saves it: the feature-008 add without the
// capability, the SWI-21 save sequence with it.
func (m Model) applyMcpForm() (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.form == nil || v.vals == nil {
		return m, nil
	}
	if v.hasEdit {
		return m.applyMcpEditForm()
	}
	vals := v.vals
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
		if strings.TrimSpace(vals.clientID) != "" || strings.TrimSpace(vals.authServer) != "" {
			v.status = "OAuth client settings apply to remote endpoints only"
			return m, nil
		}
		req.Definition = &pb.AddMcpServerRequest_Command{Command: &pb.AddMcpServerRequest_LocalCommand{Command: cmd, Args: argList(vals.args), Dir: strings.TrimSpace(vals.dir)}}
		// The warning modal takes it from here; vals and the baseline stay so
		// backing out reopens the form as it was (FR-012a).
		v.form = nil
		v.pending = req
		return m, nil
	default:
		url := strings.TrimSpace(vals.url)
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			v.status = "a remote endpoint URL (http:// or https://) is required"
			return m, nil
		}
		for _, line := range headerLines(vals.headers) {
			name, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
				v.status = "header " + strconv.Quote(line) + " must be Name: value"
				return m, nil
			}
			req.Headers = append(req.Headers, strings.TrimSpace(name)+": "+strings.TrimSpace(value))
		}
		// The daemon's rules (mcprules), so a refusal never costs the form.
		req.ClientId = strings.TrimSpace(vals.clientID)
		if err := mcprules.ValidClientID(req.ClientId); err != nil {
			v.status = "the OAuth client ID must be a single token of at most " + strconv.Itoa(mcprules.MaxClientID) + " characters"
			return m, nil
		}
		req.OauthAuthorizationServer = strings.TrimSpace(vals.authServer)
		if err := mcprules.CheckAuthServer(req.OauthAuthorizationServer); err != nil {
			v.status = "the OAuth authorization server must be an https URL (http only to localhost or a loopback IP) or an absolute path"
			return m, nil
		}
		req.Definition = &pb.AddMcpServerRequest_Url{Url: url}
	}
	v.form, v.vals = nil, nil
	return m, m.mcpAddCmd(v.daemon, v.host, req)
}

// ruleMsg is the error status for an mcprules error (see ruleText).
func ruleMsg(err error) string {
	return "error: " + ruleText(err)
}

// ruleText is a rule error's message without the "invalid argument: " prefix.
func ruleText(err error) string {
	return strings.TrimPrefix(err.Error(), mcprules.ErrInvalid.Error()+": ")
}

// mcpCheckRules applies the client-side rules (FR-420) that need no answer
// from the developer; it returns the problem or "".
func (v mcpState) mcpCheckRules() string {
	vals := v.vals
	if vals.mode == mcpModeAdd && !mcpNameRe.MatchString(strings.TrimSpace(vals.name)) {
		return "name must use letters, digits, dots, hyphens or underscores"
	}
	if vals.kind == "command" {
		switch {
		case strings.TrimSpace(vals.command) == "":
			return "a host command is required"
		case strings.TrimSpace(vals.headers) != "":
			return "headers apply to remote endpoints only"
		case strings.TrimSpace(vals.clientID) != "" || strings.TrimSpace(vals.authServer) != "":
			return "OAuth client settings apply to remote endpoints only"
		case strings.TrimSpace(vals.scopes) != "":
			return "scopes apply to remote endpoints only"
		}
		return ""
	}
	url := strings.TrimSpace(vals.url)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "a remote endpoint URL (http:// or https://) is required"
	}
	headers := headerLines(vals.headers)
	if _, err := mcprules.NormalizeHeaders(headers); err != nil {
		return ruleMsg(err)
	}
	for _, check := range []error{
		mcprules.CheckPlaceholders(headers),
		mcprules.ValidClientID(strings.TrimSpace(vals.clientID)),
		mcprules.CheckAuthServer(strings.TrimSpace(vals.authServer)),
		mcprules.ValidScopes(mcprules.ParseScopes(vals.scopes)),
	} {
		if check != nil {
			return ruleMsg(check)
		}
	}
	return ""
}

// mcpReregisters reports whether saving def would re-register the server.
func (v mcpState) mcpReregisters(def *pb.McpServerDefinition) bool {
	if v.vals.mode == mcpModeAdd || v.retry != nil {
		return true
	}
	return mcprules.RegistrationChanged(v.opened, def)
}

func (m Model) applyMcpEditForm() (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.saving {
		return m, nil
	}
	if p := v.mcpCheckRules(); p != "" {
		v.status = p
		return m, nil
	}
	// Literal credentials: offered again on save (typed ones only; an on-host
	// literal the developer declined is checked against re-registration below).
	var typed []mcpConversion
	for _, c := range v.mcpConversions() {
		if !c.onHost {
			typed = append(typed, c)
		}
	}
	return m, m.mcpOfferConversions(typed, func(m *Model) tea.Cmd {
		if len(typed) == 0 {
			return m.continueMcpSave()
		}
		// Every offer was accepted and rewrote vals.headers: rebuild the form,
		// or the Headers widget still holds the literal and writes it back on
		// the next message if the save is refused or fails.
		reopen := m.reopenForm(gMcp)
		m.focusMcpField("headers")
		return tea.Batch(reopen, m.continueMcpSave())
	})
}

// continueMcpSave runs the checks that follow the conversions and opens the
// confirmations in the contract's order, then sends the save.
func (m *Model) continueMcpSave() tea.Cmd {
	v := &m.mcpView
	back := func(m *Model) tea.Cmd { return m.reviveMcpForm() }
	if p := v.mcpCheckRules(); p != "" {
		v.status = p
		return back(m)
	}
	def := mcpDefFromVals(*v.vals)
	rereg := v.mcpReregisters(def)
	skip := map[string]bool{}
	host := v.literalOnHost()
	for _, h := range def.GetHeaders() {
		n, val := mcprules.SplitHeader(h)
		if r := host[strings.ToLower(n)]; r != "" && r == val {
			if rereg {
				v.status = "error: " + n + " holds a literal credential on the host — convert it to a ${placeholder} before re-registering"
				return back(m)
			}
			skip[strings.ToLower(n)] = true
		}
	}
	if err := mcprules.CheckCredentialHeaders(def.GetHeaders(), skip); err != nil {
		v.status = ruleMsg(err)
		return back(m)
	}
	secretValues := map[string]string{}
	fields := v.secretFields()
	for _, f := range fields {
		if val := v.secretValue(f.name); val != "" {
			secretValues[f.name] = val
		}
	}
	if ok, reason := v.secretsAvailable(); !ok && len(secretValues) > 0 {
		v.status = "error: secret storage is unavailable on this host (" + reason + "); a save that stores a secret is refused"
		return back(m)
	}
	server := v.mcpServerName()
	req := &pb.SaveMcpServerRequest{
		Name: server, Definition: def, AttachByDefault: v.vals.attach, IsNew: v.vals.mode == mcpModeAdd,
		Fingerprint: v.fingerprint, SecretValues: secretValues,
	}
	if req.IsNew {
		req.Fingerprint = ""
	}
	if p := v.retry; p != nil {
		req.RetryOfRevision = p.GetRevision()
	}

	var steps []confirmState
	// 1. URL / argument credential warning (FR-423, US2 #10).
	if names := mcprules.CredentialLooking(def.GetUrl(), def.GetCommand().GetArgs()); rereg && len(names) > 0 {
		where := "placeholder headers"
		if def.GetKind() == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
			where = "environment-based secrets"
		}
		steps = append(steps, confirmState{
			title: "Credential-looking values in plain text",
			body: []string{
				"These look like credentials: " + selectedStyle.Render(strings.Join(names, ", ")) + ".",
				"Their values are stored in plain text in switchboard's record and shown",
				"to every connected client. Prefer " + where + ".",
			},
			warning: "Saved as typed if you continue.", acceptLabel: "save anyway", cancelLabel: "back to form",
		})
	}
	// 2. Dropped unsupported settings (FR-402).
	if dropped := v.view.GetUnsupportedSettings(); rereg && v.vals.mode == mcpModeEdit && len(dropped) > 0 {
		req.ConfirmDropUnsupported = true
		steps = append(steps, confirmState{
			title:   "Drop settings the form cannot express?",
			body:    []string{"The host registration has " + selectedStyle.Render(strings.Join(dropped, ", ")) + ".", "Re-registering from this form drops each of them."},
			warning: "Those settings are lost.", acceptLabel: "drop and save", cancelLabel: "back to form",
		})
	}
	// 3. Local execution (FR-407).
	if rereg && def.GetKind() == pb.McpServerKind_MCP_SERVER_KIND_LOCAL {
		req.AcknowledgeLocalExecution = true
		c := def.GetCommand()
		steps = append(steps, confirmState{
			title: "Register a host-run server?",
			body: []string{
				selectedStyle.Render(server) + " would run " + selectedStyle.Render(strings.TrimSpace(c.GetCommand()+" "+strings.Join(c.GetArgs(), " "))),
				"on " + selectedStyle.Render(v.hostName) + " itself — OUTSIDE sandbox isolation.",
				"It can reach the host's files, network and any credentials it is given.",
			},
			warning: "Only register commands and images you trust.", acceptLabel: "register anyway", cancelLabel: "back to form",
		})
	}
	// 4. Authorization will be discarded (FR-404).
	if st := v.view.GetAuthState(); rereg && v.vals.mode == mcpModeEdit && v.retry == nil &&
		st != pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED && st != pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE {
		req.ConfirmDiscardAuthorization = true
		steps = append(steps, confirmState{
			title:   "Re-register " + server + "?",
			body:    []string{"The change re-registers the server (remove, then add).", "Its authorization is discarded: authorize it again afterwards."},
			warning: "The current authorization is lost.", acceptLabel: "re-register", cancelLabel: "back to form",
		})
	}
	// 5. Missing / removal-failed / must-re-enter secrets (FR-409, FR-411).
	if st, ok := v.mcpSecretsConfirm(fields, secretValues); ok {
		steps = append(steps, st)
	}
	// 6. FR-415: the old client binding is left in place.
	cs := mcprules.ClientSecretName(server)
	if v.opened != nil && def.GetClientId() != "" && secretValues[cs] == "" && v.stored(cs) && mcprules.ClientChanged(v.opened, def) {
		steps = append(steps, confirmState{
			title: "The old client binding stays in place",
			body: []string{
				"The OAuth client changes but no new client secret is entered while " + cs + " is stored.",
				"The runtime refuses to authorize the new client while the old client's binding",
				"is in place: enter the new client's secret and save to clear it (for a public",
				"client, remove " + cs + " and " + mcprules.IdentityName(server) + " in Secrets).",
				"The server is marked client re-bind pending.",
			},
			notice: "The old secret is never offered to the new client.", acceptLabel: "save", cancelLabel: "back to form",
		})
	}
	return m.mcpChain(steps, func(m *Model) tea.Cmd { return m.sendMcpSave(req) }, back)
}

// mcpSecretsConfirm combines FR-411's missing secrets, FR-409's removal-failed
// blanks and the blanks after a URL change into one prompt.
func (v mcpState) mcpSecretsConfirm(fields []mcpSecretField, entered map[string]string) (confirmState, bool) {
	var missing, remove, unsent []string
	clientNote := false
	for _, f := range fields {
		if entered[f.name] != "" {
			continue
		}
		switch v.secretStatus(f) {
		case mcpStatusRemovalFailed:
			remove = append(remove, f.name)
			clientNote = clientNote || f.client
		case mcpStatusReenterURL:
			unsent = append(unsent, f.name)
		case mcpStatusMissing, mcpStatusHeldOnHost:
			if !f.client {
				missing = append(missing, f.name)
			}
		}
	}
	if len(missing)+len(remove)+len(unsent) == 0 {
		return confirmState{}, false
	}
	var body []string
	if len(missing) > 0 {
		body = append(body, "Missing (the header will not work until it is set): "+selectedStyle.Render(strings.Join(missing, ", ")))
	}
	if len(unsent) > 0 {
		body = append(body, "The URL changed; the header will not be sent until set again: "+selectedStyle.Render(strings.Join(unsent, ", ")))
	}
	if len(remove) > 0 {
		body = append(body, "This save will remove: "+selectedStyle.Render(strings.Join(remove, ", ")))
		if clientNote {
			body = append(body, "The client secret is needed only for a confidential client.")
		}
	}
	return confirmState{
		title: "Save with blank secrets?", body: body,
		notice: "Enter the values in the Secrets step to avoid this.", acceptLabel: "register anyway", cancelLabel: "back to form",
	}, true
}

// sendMcpSave sends the save; the form stays (keys wait) until the reply.
func (m *Model) sendMcpSave(req *pb.SaveMcpServerRequest) tea.Cmd {
	v := &m.mcpView
	v.saving = true
	v.status = "saving " + req.GetName() + "…"
	d, host, gen := v.daemon, v.host, v.formGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), mcpSaveTimeout)
		defer cancel()
		out, err := d.SaveMcpServer(ctx, req)
		remote := req.GetDefinition().GetKind() == pb.McpServerKind_MCP_SERVER_KIND_REMOTE
		timedOut := err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)
		return mcpSaveMsg{host: host, name: req.GetName(), gen: gen, remote: remote, outcome: out, err: err, timedOut: timedOut}
	}
}

// mcpSaveMsg is a save's reply.
type mcpSaveMsg struct {
	host, name string
	gen        uint64 // the form that sent it (mcpState.formGen)
	remote     bool   // the saved definition's kind, for the authorize offer
	outcome    *pb.McpSaveOutcome
	err        error
	timedOut   bool // err is this client's mcpSaveTimeout, not the daemon's reply
}

// mcpFormIs reports whether the open form is the one gen and name identify.
func (v mcpState) mcpFormIs(gen uint64, name string) bool {
	return v.vals != nil && v.formGen == gen && v.mcpServerName() == name
}

// applyMcpSave handles a save's reply (contracts/mcp-form-ui.md).
func (m Model) applyMcpSave(msg mcpSaveMsg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if v.host != msg.host {
		return m, nil
	}
	// Matched by the form's generation, not its name: after esc mid-save the
	// developer may have opened another form, even of the same name.
	formOpen := v.mcpFormIs(msg.gen, msg.name)
	if formOpen {
		v.saving = false
	}
	relist := m.mcpLoadCmd(v.daemon, v.host)
	if msg.err != nil {
		if ref, ok := client.AsSaveRefusal(msg.err); ok {
			return m.applyMcpRefusal(msg.name, ref, formOpen)
		}
		if msg.timedOut {
			// No reply within mcpSaveTimeout. The daemon may still be running
			// the save (a slow rollback can outlast the bound), or the call may
			// never have reached it (a half-open SSH tunnel). Saving again is
			// unsafe either way, so the form closes; the row is left to the
			// daemon's own save state in the re-list (save_in_progress →
			// saving…), never pinned by localSaving, which nothing would clear
			// if no save ran.
			if formOpen {
				m.resetMcpForm()
			}
			v.status = "no reply to the save of " + msg.name + " within " + strconv.Itoa(int(mcpSaveTimeout.Minutes())) + " min" +
				"; it may still be running on the daemon (the list shows its state)"
			return m, relist
		}
		text := status.Convert(msg.err).Message()
		if client.IsSaveNotRecorded(msg.err) {
			// The save ran; its result is not recorded yet. Saving again is
			// pointless: close, and the row shows saving… until it lands. A
			// bare UNAVAILABLE (daemon unreachable) is a plain error below:
			// the save may never have run, so the form stays with its values.
			if formOpen {
				m.resetMcpForm()
			}
			if v.localSaving == nil {
				v.localSaving = map[string]bool{}
			}
			v.localSaving[msg.name] = true
			v.status = text
			return m, relist
		}
		v.status = "error: " + text
		if formOpen {
			return m, tea.Batch(m.reviveMcpForm(), relist)
		}
		return m, relist
	}
	out := msg.outcome
	if !formOpen && v.vals != nil {
		// A save left mid-flight (esc) landed while another form is open: no
		// panel over that form (its "any key closes" would take the next
		// keystroke); the row shows the result, and the follow-up offers
		// (FR-413 removal, FR-405 authorize) wait until that form closes.
		v.status = "saved " + msg.name + " in the background: " + mcpOutcomeState(out)
		if mcpOutcomeFailed(out) {
			v.status = "error: the background save of " + msg.name + " did not complete: " + mcpOutcomeState(out)
		}
		unused := out.GetUnusedSecrets()
		if len(unused) > 0 {
			v.status += "; no longer used: " + strings.Join(unused, ", ") + " (offered for removal once the list is idle)"
		}
		steps := m.mcpFollowups(out, msg.remote)
		if len(unused) > 0 {
			v.deferredSkip = append(v.deferredSkip, "unused secrets "+strings.Join(unused, ", ")+" stay removable from the Secrets view")
		}
		if len(steps) > 0 && len(unused) == 0 || len(steps) > 1 {
			// The FR-405 authorize offer always follows the removal offer.
			v.deferredSkip = append(v.deferredSkip, "press A on "+msg.name+" to authorize it")
		}
		v.deferred = append(v.deferred, steps...)
		return m, relist
	}
	followups := m.mcpFollowups(out, msg.remote)
	switch out.GetState() {
	case pb.McpSaveOutcome_STILL_REGISTERED, pb.McpSaveOutcome_NOT_REGISTERED:
		// The form stays open with the edits (FR-406).
		v.status = "error: the save did not complete — see the outcome"
		var cmds []tea.Cmd
		if formOpen {
			if p := out.GetPendingReplace(); p != nil {
				v.holding, v.retry = p, p
				cmds = append(cmds, m.mcpRefreshViewCmd(msg.name))
			}
			cmds = append(cmds, m.reviveMcpForm())
		}
		v.outcome, v.followups = out, followups
		return m, tea.Batch(append(cmds, relist)...)
	}
	if formOpen {
		m.resetMcpForm()
	}
	v.status = "saved " + msg.name
	v.outcome, v.followups = out, followups
	return m, relist
}

// applyMcpRefusal handles a refused save: the daemon changed nothing.
func (m Model) applyMcpRefusal(name string, ref *client.SaveRefusal, formOpen bool) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	relist := m.mcpLoadCmd(v.daemon, v.host)
	switch ref.Reason {
	case pb.McpSaveRefusal_STALE_CHANGED, pb.McpSaveRefusal_STALE_RESTARTED:
		v.status = ref.Message
		if !formOpen {
			return m, relist
		}
		return m, tea.Batch(m.reloadMcpCmd(name, ref.Message), relist)
	case pb.McpSaveRefusal_PENDING_ENDED:
		if formOpen {
			m.resetMcpForm()
		}
		v.status = ref.Message
		return m, relist
	case pb.McpSaveRefusal_GONE:
		v.status = ref.Message
		if !formOpen {
			return m, relist
		}
		cmd := m.reviveMcpForm()
		vals := *v.vals
		return m, tea.Batch(cmd, relist, m.offerMcpAddAsNew(name, vals, true))
	}
	// UNVERIFIED, BEING_REPLACED and the rest: keep the form and every value
	// (no reload), show the daemon's message, re-list.
	v.status = ref.Message
	if formOpen {
		return m, tea.Batch(m.reviveMcpForm(), relist)
	}
	return m, relist
}

// offerMcpAddAsNew asks whether to add a vanished server as new; accepting
// opens the add form with vals (FR-401 edge case "edit of a server that
// disappeared").
func (m *Model) offerMcpAddAsNew(name string, vals mcpFormVals, fromForm bool) tea.Cmd {
	notice := "The add form opens with the name filled in."
	if fromForm {
		notice = "The form keeps its values."
	}
	st := confirmState{
		title:       "Add " + name + " as new?",
		body:        []string{selectedStyle.Render(name) + " is no longer registered on " + selectedStyle.Render(m.mcpView.hostName) + ".", "Nothing was removed. Add it again as a new server?"},
		notice:      notice,
		acceptLabel: "add as new", cancelLabel: "back",
		yOnly: true, returnTo: screenMcp,
	}
	st.next = func(m *Model) tea.Cmd {
		secrets := m.mcpView.secretVals
		mm, cmd := m.openMcpAdd(vals)
		*m = mm.(Model)
		m.mcpView.secretVals = secrets
		return cmd
	}
	if fromForm {
		st.onCancel = func(m *Model) tea.Cmd { return m.reviveMcpForm() }
	}
	m.confirm = st
	m.screen = screenConfirm
	return nil
}

// mcpRefreshViewCmd re-reads the definition view only (statuses, fingerprint,
// pending replace) after a failed replace, keeping the form's values.
func (m Model) mcpRefreshViewCmd(name string) tea.Cmd {
	d, host, gen := m.mcpView.daemon, m.mcpView.host, m.mcpView.formGen
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		view, err := d.GetMcpServerDefinition(ctx, name, false)
		return mcpDefMsg{host: host, name: name, purpose: mcpDefRefresh, view: view, err: err, gen: gen}
	}
}

// reloadMcpCmd re-reads the definition for an in-place reload (FR-404, T030).
func (m Model) reloadMcpCmd(name, why string) tea.Cmd {
	d, host, gen := m.mcpView.daemon, m.mcpView.host, m.mcpView.formGen
	return func() tea.Msg {
		ctx, cancel := ctxTimeout()
		defer cancel()
		view, err := d.GetMcpServerDefinition(ctx, name, false)
		return mcpDefMsg{host: host, name: name, purpose: mcpDefReload, view: view, err: err, why: why, gen: gen}
	}
}

// reloadMcpForm merges a re-read into the open form (FR-404). A Retry form
// re-reads the pending replace instead; if it has ended, the form closes and
// says so.
func (m Model) reloadMcpForm(msg mcpDefMsg) (tea.Model, tea.Cmd) {
	v := &m.mcpView
	if !v.mcpFormIs(msg.gen, msg.name) {
		return m, nil
	}
	if msg.err != nil {
		v.status = "error: could not re-read " + msg.name + ": " + status.Convert(msg.err).Message()
		return m, m.reviveMcpForm()
	}
	view := msg.view
	reread := view.GetDefinition()
	attach := view.GetAttachByDefault()
	if v.retry != nil {
		p := view.GetPendingReplace()
		if p == nil {
			m.resetMcpForm()
			v.status = "the pending replace of " + msg.name + " has ended (retried, cancelled or registered by another path); open Edit to change the server"
			return m, nil
		}
		reread = p.GetDefinition()
		if v.vals.mode == mcpModeRetry && v.retryPrevious {
			reread = p.GetPrevious()
		}
		attach = p.GetAttachByDefault()
		v.retry, v.holding = p, p
	}
	// Edits count against what the form was filled from: for a Retry that is
	// the held (or previous) definition, not opened (pending.previous), or
	// every field where the two differ would read as edited and host-changed.
	filled := v.filled
	filled.mode = v.vals.mode
	rereadVals := mcpValsFromDef(reread, msg.name, attach, v.vals.mode)
	secrets := map[string]string{}
	for n, p := range v.secretVals {
		secrets[n] = *p
	}
	r := mergeMcpReload(*v.vals, filled, rereadVals, msg.name, secrets)
	*v.vals = r.vals
	v.flags = r.flags
	for _, n := range r.dropped {
		*v.secretPtr(n) = ""
		if r.reenter[n] {
			if v.reenter == nil {
				v.reenter = map[string]string{}
			}
			v.reenter[n] = mcpStatusReenterURL
		}
	}
	v.view, v.fingerprint, v.filled = view, view.GetFingerprint(), rereadVals
	if v.retry != nil {
		v.opened = v.retry.GetPrevious() // a retry compares against pending.previous
	} else {
		v.opened = reread
	}
	v.status = msg.why + " — the form was reloaded"
	if len(r.dropped) > 0 {
		v.status += "; dropped (enter again): " + strings.Join(r.dropped, ", ")
	}
	v.baseline = mcpSnapshot{vals: rereadVals, secrets: map[string]string{}}
	cmd := m.reopenForm(gMcp)
	return m, cmd
}

// renameSecrets moves typed secret values to the new server name in add mode,
// so a value typed (or converted) before the name was set or changed is kept;
// a blank name is the placeholder name the fields use.
func (v *mcpState) renameSecrets(from, to string) {
	oldPrefix, newPrefix := mcprules.SecretPrefix(secretServerName(from)), mcprules.SecretPrefix(secretServerName(to))
	for name, p := range v.secretVals {
		rest, ok := strings.CutPrefix(name, oldPrefix)
		if !ok || p == nil || *p == "" {
			continue
		}
		delete(v.secretVals, name)
		if q := v.secretVals[newPrefix+rest]; q == nil || *q == "" {
			v.secretVals[newPrefix+rest] = p
		}
	}
}
