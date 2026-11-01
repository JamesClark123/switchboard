package mcp

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"google.golang.org/protobuf/proto"
)

// ErrGone: an edit of a name the host no longer lists (and that has no pending
// replace or running save) → NOT_FOUND with the GONE detail.
var ErrGone = &Refusal{Reason: pb.McpSaveRefusal_GONE, Msg: "the server is no longer registered on this host"}

// hostRead is one fresh read of a server from the host (research R11): `mcp
// ls`, `mcp inspect --json` and `mcp auth status --json`, run in parallel. The
// raw reported values stay inside the daemon; they feed the fingerprint.
type hostRead struct {
	lsErr      error
	row        *listRow // nil: not listed
	inspectErr error
	info       inspectInfo
	authErr    error
	auth       pb.McpAuthState
	scopes     []string
	scopesRep  bool
}

// remote reports whether the listed row is a remote server.
func (h *hostRead) remote() bool {
	return h.row != nil && kindOf(h.row.transport) == pb.McpServerKind_MCP_SERVER_KIND_REMOTE
}

// failed reports whether a read the fingerprint depends on failed.
func (h *hostRead) failed() bool {
	return h.lsErr != nil || h.row != nil && (h.inspectErr != nil || h.remote() && h.authErr != nil)
}

// readHost runs the three reads in parallel under the list bound; listOnly
// (an add, which needs only the ls row) skips inspect and auth status.
func (m *Manager) readHost(ctx context.Context, name string, listOnly bool) *hostRead {
	rctx, cancel := context.WithTimeout(ctx, time.Duration(listTimeout))
	defer cancel()
	hr := &hostRead{auth: pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN}
	var inspectOut, authOut string
	var wg sync.WaitGroup
	list := func() {
		rows, err := m.listRows(rctx)
		hr.lsErr = err
		for i := range rows {
			if rows[i].name == name {
				hr.row = &rows[i]
			}
		}
	}
	if listOnly {
		list()
		return hr
	}
	wg.Add(3)
	go func() {
		defer wg.Done()
		list()
	}()
	go func() {
		defer wg.Done()
		inspectOut, hr.inspectErr = m.cli.runJSON(rctx, argvInspect(name)...)
	}()
	go func() {
		defer wg.Done()
		authOut, hr.authErr = m.cli.runJSON(rctx, argvAuthStatus(name)...)
	}()
	wg.Wait()
	if hr.inspectErr == nil {
		hr.info = parseInspect(inspectOut, name)
	}
	if hr.authErr == nil {
		hr.auth = parseAuthStatus(authOut, name)
		hr.scopes, hr.scopesRep = parseAuthStatusScopes(authOut, name)
	}
	if hr.auth == pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN && hr.row != nil {
		hr.auth = authFromStatus(hr.row.status)
	}
	if hr.row != nil && !hr.remote() {
		hr.auth = pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE
	}
	return hr
}

// secretStatus reads the global secret list (FR-409). stored are the server's
// own names, sorted; ok is false (with a reason) when the list is unavailable.
func (m *Manager) secretStatus(ctx context.Context, name string) (stored []string, global map[string]bool, ok bool, reason string) {
	if m.secrets == nil {
		return nil, nil, false, "secret storage is not available on this daemon"
	}
	global, err := m.secrets.ListGlobal(ctx)
	if err != nil {
		return nil, nil, false, "couldn't read the host's secrets: " + err.Error()
	}
	prefix := mcprules.SecretPrefix(name)
	for n := range global {
		if strings.HasPrefix(n, prefix) {
			stored = append(stored, n)
		}
	}
	sort.Strings(stored)
	return stored, global, true, ""
}

// Definition is the edit form's pre-fill (SWI-21 FR-402, research R11): the
// record merged with what the host reports, credential literals redacted, the
// secret status and the fingerprint to echo back. It takes no lock. forAdd
// returns only the secret status. The name must be one a save accepts
// (checkName): any other could never be saved, and a `:` in it would make the
// `mcp:<name>:` secret prefix match another server's secrets.
func (m *Manager) Definition(ctx context.Context, name string, forAdd bool) (*pb.McpDefinitionView, error) {
	name = strings.TrimSpace(name)
	if err := checkName(name); err != nil {
		return nil, err
	}
	settings, err := m.reg.GetMcpSettings()
	if err != nil {
		return nil, err
	}
	st := m.stateSnapshot(name)
	stored, global, ok, reason := m.secretStatus(ctx, name)
	base := &pb.McpDefinitionView{
		Name: name, StoredSecrets: stored, SecretsAvailable: ok, SecretsUnavailableReason: reason,
		SaveInProgress: st.saving || st.recording, Recovering: st.recovering,
	}
	if forAdd {
		return base, nil
	}
	hr := m.readHost(ctx, name, false)
	if hr.lsErr != nil {
		return nil, hr.lsErr
	}
	pending := settings.GetPendingReplaces()[name]
	if hr.row == nil && pending == nil {
		if base.SaveInProgress || base.Recovering {
			return base, nil
		}
		return nil, ErrGone
	}
	v := m.buildView(name, settings, hr)
	v.StoredSecrets, v.SecretsAvailable, v.SecretsUnavailableReason = stored, ok, reason
	v.SaveInProgress, v.Recovering = base.SaveInProgress, base.Recovering
	fillSecretStatus(v, global, ok)
	v.Fingerprint = m.fingerprint(settings.GetDefinitions()[name], hr, pending.GetRevision())
	return v, nil
}

// buildView merges the record (or a pending replace's held definition) with
// the host read per FR-402 and FR-423. Shared by Definition and Save.
func (m *Manager) buildView(name string, settings *pb.McpGatewaySettings, hr *hostRead) *pb.McpDefinitionView {
	rec := settings.GetDefinitions()[name]
	pending := settings.GetPendingReplaces()[name]
	v := &pb.McpDefinitionView{Name: name, HasRecord: rec != nil, AttachByDefault: settings.GetMarks()[name]}
	if pending != nil {
		v.PendingReplace = proto.Clone(pending).(*pb.McpPendingReplace)
		v.AttachByDefault = pending.GetAttachByDefault()
		v.Definition = proto.Clone(pending.GetDefinition()).(*pb.McpServerDefinition)
		v.AuthState = pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED
		if hr.row == nil {
			return v
		}
	}
	def := &pb.McpServerDefinition{}
	if rec != nil {
		def = proto.Clone(rec).(*pb.McpServerDefinition)
	}
	def.LiteralOnHost = nil
	flag := func(field string, kind pb.McpFieldFlag_Kind, host, recorded string) {
		v.Flags = append(v.Flags, &pb.McpFieldFlag{Field: field, Kind: kind, HostValue: host, RecordedValue: recorded})
	}
	if k := kindOf(hr.row.transport); k != pb.McpServerKind_MCP_SERVER_KIND_UNSPECIFIED {
		if rec != nil && rec.GetKind() != pb.McpServerKind_MCP_SERVER_KIND_UNSPECIFIED && rec.GetKind() != k {
			// The host re-registered the name as the other kind: none of the
			// record's fields apply, so the view is built as for an unrecorded
			// server (only what the host reports) and the change is flagged.
			flag("kind", pb.McpFieldFlag_CHANGED_ON_HOST, kindName(k), kindName(rec.GetKind()))
			def = &pb.McpServerDefinition{RebindPending: rec.GetRebindPending()}
			rec = nil
		}
		def.Kind = k
	}
	if def.Kind == pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
		mergeString := func(field string, reported *string, dst *string, recorded string) {
			switch {
			case reported != nil:
				if rec != nil && strings.TrimSpace(recorded) != strings.TrimSpace(*reported) {
					flag(field, pb.McpFieldFlag_CHANGED_ON_HOST, *reported, recorded)
				}
				*dst = strings.TrimSpace(*reported)
			case rec == nil:
				flag(field, pb.McpFieldFlag_NOT_RECOVERABLE, "", "")
			}
		}
		mergeString("url", hr.info.url, &def.Url, rec.GetUrl())
		mergeString("client_id", hr.info.clientID, &def.ClientId, rec.GetClientId())
		mergeString("oauth_authorization_server", hr.info.authServer, &def.OauthAuthorizationServer, rec.GetOauthAuthorizationServer())
		switch {
		case hr.scopesRep:
			// A runtime-added offline_access is told apart only against a record;
			// with none, the host's scopes are kept as reported.
			recorded := rec
			if rec == nil {
				recorded = &pb.McpServerDefinition{Scopes: hr.scopes}
			}
			sc := mcprules.Normalize(&pb.McpServerDefinition{Scopes: hr.scopes}, recorded).GetScopes()
			if rec != nil && !mcprules.Equal(&pb.McpServerDefinition{Scopes: sc}, &pb.McpServerDefinition{Scopes: rec.GetScopes()}) {
				flag("scopes", pb.McpFieldFlag_CHANGED_ON_HOST, strings.Join(sc, " "), strings.Join(rec.GetScopes(), " "))
			}
			// Keep the record's order when the sets agree.
			if rec == nil || !mcprules.Equal(&pb.McpServerDefinition{Scopes: sc}, &pb.McpServerDefinition{Scopes: rec.GetScopes()}) {
				def.Scopes = sc
			}
		case rec == nil:
			flag("scopes", pb.McpFieldFlag_NOT_RECOVERABLE, "", "")
		}
		if mergeHeaders(def, rec, hr, flag) {
			v.UnsupportedSettings = append(v.UnsupportedSettings, "malformed header")
		}
	} else if rec.GetCommand() == nil {
		flag("command", pb.McpFieldFlag_NOT_RECOVERABLE, "", "")
	}
	if hr.info.resource != "" {
		v.UnsupportedSettings = append(v.UnsupportedSettings, "resource")
	}
	if hr.info.skipSSRF {
		v.UnsupportedSettings = append(v.UnsupportedSettings, "skip-ssrf-check")
	}
	if hr.info.image != "" || strings.Contains(strings.ToLower(hr.info.kind), "image") || strings.Contains(strings.ToLower(hr.info.kind), "registry") {
		v.UnsupportedSettings = append(v.UnsupportedSettings, "registry image")
	}
	if pending == nil {
		v.Definition = def
		v.AuthState = hr.auth
	}
	return v
}

// kindName is a server kind as the edit form's flag shows it.
func kindName(k pb.McpServerKind) string {
	if k == pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
		return "remote"
	}
	return "host command"
}

// mergeHeaders applies FR-402 and the FR-423 redaction to the reported headers.
// A reported credential literal is redacted and flagged LITERAL_ON_HOST; the
// literal never reaches the view. A reported entry that is not a field name
// and a colon is left out whole (its "name" may hold a credential) and reported
// as malformed, which the view lists as an unsupported setting.
func mergeHeaders(def, rec *pb.McpServerDefinition, hr *hostRead, flag func(string, pb.McpFieldFlag_Kind, string, string)) (malformed bool) {
	type hv struct{ name, value string }
	recBy := map[string]hv{}
	var recOrder []string
	for _, h := range rec.GetHeaders() {
		n, val := mcprules.SplitHeader(h)
		recBy[strings.ToLower(n)] = hv{n, val}
		recOrder = append(recOrder, strings.ToLower(n))
	}
	if !hr.info.headersReported {
		if rec == nil {
			flag("headers", pb.McpFieldFlag_NOT_RECOVERABLE, "", "")
		}
		for _, n := range rec.GetLiteralOnHost() {
			def.LiteralOnHost = append(def.LiteralOnHost, n)
			flag("headers/"+n, pb.McpFieldFlag_LITERAL_ON_HOST, recBy[strings.ToLower(n)].value, "")
		}
		return false
	}
	var out []string
	seen := map[string]bool{}
	for _, h := range hr.info.headers {
		if !mcprules.WellFormedHeader(h) {
			malformed = true
			continue
		}
		n, val := mcprules.SplitHeader(h)
		key := strings.ToLower(n)
		seen[key] = true
		r, hasRec := recBy[key]
		if hasRec && strings.Contains(r.value, "${") && !strings.Contains(val, "${") && !inspectReturnsTemplates {
			// Inspect returns resolved values: the record's template stands and
			// the host value cannot be checked (FR-423).
			out = append(out, r.name+": "+r.value)
			flag("headers/"+n, pb.McpFieldFlag_RESOLVED_UNCHECKABLE, "", r.value)
			continue
		}
		if mcprules.CheckCredentialValue(n, val) != nil {
			red := mcprules.Redact(val)
			out = append(out, n+": "+red)
			def.LiteralOnHost = append(def.LiteralOnHost, n)
			flag("headers/"+n, pb.McpFieldFlag_LITERAL_ON_HOST, red, r.value)
			continue
		}
		out = append(out, n+": "+val)
		if rec != nil && (!hasRec || r.value != val) {
			flag("headers/"+n, pb.McpFieldFlag_CHANGED_ON_HOST, val, r.value)
		}
	}
	for _, key := range recOrder {
		if !seen[key] {
			r := recBy[key]
			flag("headers/"+r.name, pb.McpFieldFlag_CHANGED_ON_HOST, "", r.value)
		}
	}
	def.Headers = out
	return malformed
}

// fillSecretStatus sets each placeholder's and the client secret's status
// (FR-409, FR-406), plus the Cancel confirmation's removal-failed listing.
func fillSecretStatus(v *pb.McpDefinitionView, global map[string]bool, ok bool) {
	failed := map[string]bool{}
	for _, n := range v.GetPendingReplace().GetRemovalFailedSecrets() {
		failed[n] = true
		switch {
		case !ok:
			v.PendingRemovalFailedListUnknown = true
		case global[n]:
			v.PendingRemovalFailedListed = append(v.PendingRemovalFailedListed, n)
		}
	}
	status := func(n string) {
		st := pb.McpSecretStatus_MISSING
		if global[n] {
			st = pb.McpSecretStatus_SET
			if failed[n] {
				st = pb.McpSecretStatus_REMOVAL_FAILED
			}
		}
		v.Secrets = append(v.Secrets, &pb.McpSecretStatus{Name: n, Status: st})
	}
	def := v.GetDefinition()
	for _, p := range mcprules.Placeholders(def.GetHeaders()) {
		status(mcprules.SecretName(v.GetName(), p))
	}
	if def.GetClientId() != "" {
		status(mcprules.ClientSecretName(v.GetName()))
	}
}

// newFingerprintKey draws the per-start key and its 8-byte id (research R5).
func newFingerprintKey() ([]byte, string) {
	b := make([]byte, 40)
	if _, err := rand.Read(b); err != nil {
		panic("mcp: crypto/rand: " + err.Error())
	}
	return b[:32], hex.EncodeToString(b[32:])
}

// fingerprint is keyID ‖ HMAC-SHA-256 over a length-prefixed canonical encoding
// of the record and everything the host read reported, raw (research R5). A
// failed read is encoded as such, so a read that fails the same way at open
// and at save still matches.
func (m *Manager) fingerprint(rec *pb.McpServerDefinition, hr *hostRead, pendingRev uint64) string {
	mac := hmac.New(sha256.New, m.fpKey)
	field := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		mac.Write(n[:])
		mac.Write([]byte(s))
	}
	flagStr := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	opt := func(p *string) {
		if p == nil {
			field("-")
			return
		}
		field("+" + *p)
	}
	recBytes, _ := proto.MarshalOptions{Deterministic: true}.Marshal(rec)
	field(string(recBytes))
	field(flagStr(hr.lsErr != nil))
	field(flagStr(hr.row != nil))
	if hr.row != nil {
		field(hr.row.transport)
		// Whether the server is authorized: a form opened before an
		// authorization elsewhere is reloaded as stale instead of failing
		// confirm-required on every save (UNKNOWN is left out, so a transient
		// status read never makes a save stale).
		field(flagStr(hr.auth == pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED))
		field(flagStr(hr.inspectErr != nil))
		headers := append([]string(nil), hr.info.headers...)
		sort.Strings(headers)
		field(flagStr(hr.info.headersReported))
		field(strings.Join(headers, "\n"))
		opt(hr.info.url)
		opt(hr.info.clientID)
		opt(hr.info.authServer)
		field(hr.info.resource)
		field(hr.info.image)
		field(hr.info.kind)
		field(flagStr(hr.info.skipSSRF))
		if hr.remote() {
			field(flagStr(hr.authErr != nil))
			field(flagStr(hr.scopesRep))
			field(strings.Join(hr.scopes, " "))
		}
	}
	var rev [8]byte
	binary.BigEndian.PutUint64(rev[:], pendingRev)
	field(string(rev[:]))
	return m.fpKeyID + hex.EncodeToString(mac.Sum(nil))
}
