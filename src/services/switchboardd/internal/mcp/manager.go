package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/libs/switchboard-proto/mcprules"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/protobuf/proto"
)

// Sentinel errors the gRPC layer maps to status codes.
var (
	// ErrNotRegistered: the name is not in `sbx mcp ls` on this host → NOT_FOUND.
	ErrNotRegistered = errors.New("not registered on this host")
	// ErrLocalNotAcknowledged: a host-run server was requested without the
	// developer acknowledging that it executes outside sandbox isolation
	// (FR-076) → FAILED_PRECONDITION.
	ErrLocalNotAcknowledged = errors.New("a host-run server executes on the daemon's host outside sandbox isolation; acknowledge that to register it")
	// ErrInvalidArgs: a malformed name, an empty definition, or an argument the
	// documented comma-joined `--args` form cannot carry → INVALID_ARGUMENT.
	ErrInvalidArgs = errors.New("invalid argument")
	// ErrAuthInProgress: a second Authorize for a name whose wait is still
	// running → FAILED_PRECONDITION.
	ErrAuthInProgress = errors.New("authorization already in progress")
)

// AuthTimeout bounds one authorization wait (clarified: 10 minutes). A package
// constant, not an env var; authTimeout is the overridable copy tests shrink.
const AuthTimeout = 10 * time.Minute

var authTimeout = AuthTimeout

// nameRe is the runtime's server-name rule (letters, digits, dots, hyphens,
// underscores). Checked before any exec so a bad name never reaches the CLI.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// settingsStore is the registry's `mcp` bucket record (*registry.Registry);
// an interface so tests can inject write failures.
type settingsStore interface {
	GetMcpSettings() (*pb.McpGatewaySettings, error)
	UpdateMcpSettings(mutate func(*pb.McpGatewaySettings) error) (*pb.McpGatewaySettings, error)
}

var _ settingsStore = (*registry.Registry)(nil)

// Secrets is the host secret store as the save uses it (SWI-21, internal/secret):
// global-scope presence, quiet stores/removals, and one change event per save.
type Secrets interface {
	ListGlobal(ctx context.Context) (map[string]bool, error)
	SetQuiet(ctx context.Context, name, value string) error
	RemoveQuiet(ctx context.Context, name string) (string, error)
	EmitChanged()
}

// Sandboxes lists this daemon's sandbox records (FR-419: the save outcome's
// attached sandboxes).
type Sandboxes interface {
	List() ([]*pb.Sandbox, error)
}

// Manager is one host's gateway manager: it reads registrations from the host
// CLI on every query and owns the per-daemon marks + attach mode in the
// registry's `mcp` bucket, plus (SWI-21) switchboard's definition record,
// pending replaces and in-progress save records.
type Manager struct {
	cli    *CLI
	reg    settingsStore
	hostID string
	emit   func(*pb.Event) // nil-safe; Event.mcp_gateway_changed after every change

	secrets        Secrets                            // nil: the form's secrets are unavailable
	sandboxes      Sandboxes                          // nil: no FR-419 list
	onReplaceEnded func(name string, registered bool) // nil-safe (R12 sweep)
	daemonCtx      context.Context                    // persist retries and recovery stop with it

	// Fingerprint key and its id, drawn per start (research R5); never leave memory.
	fpKey   []byte
	fpKeyID string

	mu               sync.Mutex
	warnedNoCallback bool              // warnNoCallback has logged
	inflight         map[string]uint32 // names with an authorization wait running -> its OAuth callback port (0 until announced)
	busy             map[string]*nameLock

	// stateMu is a leaf: nothing is acquired while it is held; it may be taken
	// under mu or inside a registry transaction (research R12 Lock order).
	stateMu    sync.Mutex
	state      map[string]*nameState
	pruneEpoch uint64 // bumped by every prune of an idle state entry
	genClock   uint64 // the last generation handed out (releaseFunc)

	recoverQueue []string // start-up leftovers (research R2 Recovery)
}

// Option configures a Manager.
type Option func(*Manager)

// WithSecrets gives the save access to the host secret store.
func WithSecrets(s Secrets) Option { return func(m *Manager) { m.secrets = s } }

// WithSandboxes lets a save list the sandboxes that have the server (FR-419).
func WithSandboxes(s Sandboxes) Option { return func(m *Manager) { m.sandboxes = s } }

// WithReplaceEnded sets the hook called on every end of a replace (R12).
func WithReplaceEnded(fn func(name string, registered bool)) Option {
	return func(m *Manager) { m.onReplaceEnded = fn }
}

// WithContext bounds the background work (persist retries, recovery) by the
// daemon's lifetime.
func WithContext(ctx context.Context) Option { return func(m *Manager) { m.daemonCtx = ctx } }

// NewManager wires a Manager; emit may be nil. It makes no host call: leftover
// in-progress records are loaded as recovering and resolved by one background
// routine (research R2).
func NewManager(cli *CLI, reg settingsStore, hostID string, emit func(*pb.Event), opts ...Option) *Manager {
	m := &Manager{
		cli: cli, reg: reg, hostID: hostID, emit: emit, daemonCtx: context.Background(),
		inflight: map[string]uint32{}, busy: map[string]*nameLock{}, state: map[string]*nameState{},
	}
	for _, o := range opts {
		o(m)
	}
	m.fpKey, m.fpKeyID = newFingerprintKey()
	if reg == nil {
		return m
	}
	if settings, err := reg.GetMcpSettings(); err != nil {
		log.Printf("[mcp] read settings: %v", err)
	} else {
		for name := range settings.GetPendingReplaces() {
			m.st(name).pending = true
		}
		for name := range settings.GetSavesInProgress() {
			m.st(name).recovering = true
			m.recoverQueue = append(m.recoverQueue, name)
		}
		sort.Strings(m.recoverQueue)
	}
	if len(m.recoverQueue) > 0 {
		go m.recoverLoop(m.daemonCtx, m.recoverQueue)
	}
	return m
}

// Attach is what a launch applies (data-model "Launch resolution"): the mode,
// the marked servers the host still knows, and the stale marks that were
// dropped — reported in the launch output, never a failed launch (FR-083).
type Attach struct {
	Mode    pb.McpAttachMode
	Servers []string
	Skipped []string
	// BeingReplaced are marked servers skipped because a replace of them is
	// running or pending (SWI-21 FR-425); their marks are kept.
	BeingReplaced []string
}

// --- queries ---

// List returns the host's registrations decorated with marks and auth state,
// plus the daemon's (normalized) settings. Stale marks are dropped on read.
func (m *Manager) List(ctx context.Context) ([]*pb.McpServer, *pb.McpGatewaySettings, error) {
	servers, settings, _, err := m.ListAll(ctx)
	return servers, settings, err
}

// ListAll is List plus the pending replaces (FR-426). The returned settings
// never carry definitions, pending replaces or in-progress records.
func (m *Manager) ListAll(ctx context.Context) ([]*pb.McpServer, *pb.McpGatewaySettings, []*pb.McpPendingReplace, error) {
	gens := m.generations()
	rows, err := m.listRows(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	settings, _, err := m.reconcile(rows, gens)
	if err != nil {
		return nil, nil, nil, err
	}
	return m.decorate(ctx, rows, settings), clientSettings(settings), pendingList(settings), nil
}

// clientSettings is the settings as clients see them (proto delta).
func clientSettings(s *pb.McpGatewaySettings) *pb.McpGatewaySettings {
	out := proto.Clone(s).(*pb.McpGatewaySettings)
	out.Definitions, out.PendingReplaces, out.SavesInProgress = nil, nil, nil
	return out
}

// pendingList returns the pending replaces sorted by name.
func pendingList(s *pb.McpGatewaySettings) []*pb.McpPendingReplace {
	var out []*pb.McpPendingReplace
	for _, p := range s.GetPendingReplaces() {
		out = append(out, proto.Clone(p).(*pb.McpPendingReplace))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out
}

// listRows runs `sbx mcp ls` under the list bound and parses the table.
func (m *Manager) listRows(ctx context.Context) ([]listRow, error) {
	lctx, cancel := context.WithTimeout(ctx, time.Duration(listTimeout))
	defer cancel()
	out, err := m.cli.run(lctx, argvList()...)
	if err != nil {
		return nil, err
	}
	rows, err := parseList(out)
	if err != nil {
		return nil, err
	}
	// Per-worker registrations (feature 009) are hidden everywhere: not listed,
	// never reconciled against marks, never attach-by-default candidates.
	kept := rows[:0]
	for _, r := range rows {
		if !strings.HasPrefix(r.name, WorkerServerPrefix) {
			kept = append(kept, r)
		}
	}
	return kept, nil
}

// reconcile drops marks and definitions whose server the host no longer lists
// (invariant I-M2, D2) and ends a pending replace whose name the host lists
// again (*registered by another path*, R8), persisting both, and returns the
// settings with the mode normalized (UNSPECIFIED reads as ADDITIVE) plus the
// dropped mark names. gens is the generation snapshot taken before the list:
// a name that is locked, has an in-progress record, or whose generation moved
// is left alone (research R4). It never holds m.mu, and it calls the replace
// hook only after the registry write returns.
func (m *Manager) reconcile(rows []listRow, gens genSnapshot) (*pb.McpGatewaySettings, []string, error) {
	present := map[string]bool{}
	for _, r := range rows {
		present[r.name] = true
	}
	settings, err := m.reg.GetMcpSettings()
	if err != nil {
		return nil, nil, err
	}
	need := false
	for name := range settings.GetMarks() {
		need = need || !present[name]
	}
	for name := range settings.GetDefinitions() {
		need = need || !present[name] && settings.GetPendingReplaces()[name] == nil
	}
	for name := range settings.GetPendingReplaces() {
		need = need || present[name]
	}
	if !need {
		return normalize(settings), nil, nil
	}
	var dropped []string
	var ended []*pb.McpPendingReplace
	settings, err = m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		dropped, ended = nil, nil
		m.stateMu.Lock()
		defer m.stateMu.Unlock()
		keep := func(name string) bool {
			return m.staleFor(name, gens) || s.GetSavesInProgress()[name] != nil
		}
		for name := range s.Marks {
			if !present[name] && !keep(name) {
				dropped = append(dropped, name)
				delete(s.Marks, name)
			}
		}
		for name, p := range s.PendingReplaces {
			if present[name] && !keep(name) {
				ended = append(ended, p)
				delete(s.PendingReplaces, name)
				delete(s.Definitions, name)
				delete(s.Marks, name)
			}
		}
		for name := range s.Definitions {
			if !present[name] && s.PendingReplaces[name] == nil && !keep(name) {
				delete(s.Definitions, name)
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(dropped)
	for _, p := range ended {
		m.endPending(p.GetName())
		log.Printf("mcp: %s: pending replace ended: registered by another path", p.GetName())
		m.replaceEnded(p.GetName(), false)
		m.emitOutcome(&pb.McpSaveOutcome{
			Name: p.GetName(), State: pb.McpSaveOutcome_REGISTERED,
			HostErrors:     []string{"the pending replace ended: the server was registered by another path"},
			RollbackFailed: p.GetRemovalFailedSecrets(),
		}, settings)
	}
	return normalize(settings), dropped, nil
}

// normalize returns a copy whose UNSPECIFIED mode reads as ADDITIVE.
func normalize(s *pb.McpGatewaySettings) *pb.McpGatewaySettings {
	out := proto.Clone(s).(*pb.McpGatewaySettings)
	if out.GetDefaultAttachMode() == pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED {
		out.DefaultAttachMode = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
	}
	return out
}

// decorate builds the wire rows: kind from the TYPE column, the mark from
// settings, and — for remote servers, concurrently — the auth state from
// `sbx mcp auth status --json`. A status failure is UNKNOWN, never an error.
func (m *Manager) decorate(ctx context.Context, rows []listRow, settings *pb.McpGatewaySettings) []*pb.McpServer {
	out := make([]*pb.McpServer, len(rows))
	sctx, cancel := context.WithTimeout(ctx, time.Duration(listTimeout))
	defer cancel()
	var wg sync.WaitGroup
	for i, r := range rows {
		out[i] = &pb.McpServer{
			Name:            r.name,
			Kind:            kindOf(r.transport),
			Target:          r.target,
			Transport:       r.transport,
			StatusText:      r.status,
			Detail:          r.detail,
			AuthState:       pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE,
			AttachByDefault: settings.GetMarks()[r.name],
			RebindPending:   settings.GetDefinitions()[r.name].GetRebindPending(),
		}
		st := m.stateSnapshot(r.name)
		out[i].SaveInProgress = st.saving || st.recording
		out[i].Recovering = st.recovering
		if out[i].Kind != pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
			continue
		}
		wg.Add(1)
		go func(i int, r listRow) {
			defer wg.Done()
			// The dedicated query decides; the STATUS column ("needs auth") is the
			// fallback when it fails or says nothing recognisable.
			st := pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
			if res, err := m.cli.runJSON(sctx, argvAuthStatus(r.name)...); err == nil {
				st = parseAuthStatus(res, r.name)
			}
			if st == pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN {
				st = authFromStatus(r.status)
			}
			out[i].AuthState = st
		}(i, r)
	}
	wg.Wait()
	return out
}

// --- mutations ---

// Add registers a server without authorizing it (always `--skip-auth`,
// research R2), optionally marks it, records its definition (SWI-21 R4), and
// returns its decorated row. Host diagnostics travel verbatim in the error
// (FR-078). The old-client RPC keeps today's validation: the stricter
// credential-header rule applies only to Save (FR-422); a literal it would
// flag is recorded redacted (D1).
func (m *Manager) Add(ctx context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	name := strings.TrimSpace(req.GetName())
	if err := checkName(name); err != nil {
		return nil, err
	}
	var def *pb.McpServerDefinition
	var argv []string
	switch d := req.GetDefinition().(type) {
	case *pb.AddMcpServerRequest_Url:
		url := strings.TrimSpace(d.Url)
		if url == "" {
			return nil, fmt.Errorf("%w: a remote server needs a URL", ErrInvalidArgs)
		}
		headers, err := mcprules.NormalizeHeaders(req.GetHeaders())
		if err != nil {
			return nil, ruleErr(err)
		}
		// FR-408 holds for old clients too (FR-422 relaxes only the
		// credential-header rule): ${client_secret} would send the OAuth client
		// secret to the resource server.
		if err := mcprules.CheckPlaceholders(headers); err != nil {
			return nil, ruleErr(err)
		}
		clientID, authServer := strings.TrimSpace(req.GetClientId()), strings.TrimSpace(req.GetOauthAuthorizationServer())
		if err := mcprules.ValidClientID(clientID); err != nil {
			return nil, ruleErr(err)
		}
		if err := mcprules.CheckAuthServer(authServer); err != nil {
			return nil, ruleErr(err)
		}
		if err := mcprules.ValidScopes(req.GetScopes()); err != nil {
			return nil, ruleErr(err)
		}
		argv = argvAddRemote(name, url, headers, clientID, authServer, req.GetScopes())
		def = &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_REMOTE, Url: url, Headers: headers,
			ClientId: clientID, OauthAuthorizationServer: authServer, Scopes: req.GetScopes()}
	case *pb.AddMcpServerRequest_Command:
		if len(req.GetHeaders()) > 0 {
			return nil, fmt.Errorf("%w: custom headers apply to remote endpoints only", ErrInvalidArgs)
		}
		if strings.TrimSpace(req.GetClientId()) != "" || strings.TrimSpace(req.GetOauthAuthorizationServer()) != "" {
			return nil, fmt.Errorf("%w: OAuth client settings apply to remote endpoints only", ErrInvalidArgs)
		}
		if len(req.GetScopes()) > 0 {
			return nil, fmt.Errorf("%w: scopes apply to remote endpoints only", ErrInvalidArgs)
		}
		if !req.GetAcknowledgeLocalExecution() {
			return nil, ErrLocalNotAcknowledged
		}
		c := d.Command
		if err := checkCommand(c); err != nil {
			return nil, err
		}
		argv = argvAddLocal(name, c.GetCommand(), c.GetArgs(), c.GetDir())
		def = &pb.McpServerDefinition{Kind: pb.McpServerKind_MCP_SERVER_KIND_LOCAL, Command: proto.Clone(c).(*pb.AddMcpServerRequest_LocalCommand)}
	default:
		return nil, fmt.Errorf("%w: a URL or a command is required", ErrInvalidArgs)
	}
	redactLiterals(def)

	_, release, err := m.lockName(ctx, name, lockOpts{})
	if err != nil {
		return nil, err
	}
	defer release()
	if err := m.refuseRecord(name, true); err != nil {
		return nil, err
	}
	mctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	if _, err := m.cli.run(mctx, argv...); err != nil {
		return nil, err
	}
	// Re-list first (it reconciles marks against the host; this name is locked
	// so it is left alone), then record the definition and mark together.
	servers, _, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		if s.Definitions == nil {
			s.Definitions = map[string]*pb.McpServerDefinition{}
		}
		s.Definitions[name] = def
		if req.GetAttachByDefault() {
			setMarkIn(s, name, true)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	row := findServer(servers, name)
	if row == nil {
		row = &pb.McpServer{Name: name, AuthState: pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN}
		if req.GetUrl() != "" {
			row.Kind, row.Target = pb.McpServerKind_MCP_SERVER_KIND_REMOTE, req.GetUrl()
		} else {
			row.Kind, row.Target = pb.McpServerKind_MCP_SERVER_KIND_LOCAL, req.GetCommand().GetCommand()
			row.AuthState = pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE
		}
	}
	row.AttachByDefault = req.GetAttachByDefault()
	m.emitChanged()
	return row, nil
}

// checkName is the register-form name rule.
func checkName(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: server name %q must contain only letters, digits, dots, hyphens and underscores, and not start with a hyphen", ErrInvalidArgs, name)
	}
	if strings.HasPrefix(name, WorkerServerPrefix) {
		return fmt.Errorf("%w: server name %q uses the reserved prefix %q", ErrInvalidArgs, name, WorkerServerPrefix)
	}
	return nil
}

// checkCommand validates a host-run server's command.
func checkCommand(c *pb.AddMcpServerRequest_LocalCommand) error {
	if strings.TrimSpace(c.GetCommand()) == "" {
		return fmt.Errorf("%w: a host-run server needs a command", ErrInvalidArgs)
	}
	for _, a := range c.GetArgs() {
		// The documented form joins args with commas and documents no escaping.
		if strings.Contains(a, ",") {
			return fmt.Errorf("%w: argument %q contains a comma, which the host CLI cannot carry", ErrInvalidArgs, a)
		}
	}
	// A NUL makes the exec fail before `sbx` starts (after a replace's `mcp rm`
	// has run), and other control characters would reach every client. A tab
	// is allowed, as in a header value (a tab-separated or JSON argument).
	for _, f := range append([]string{c.GetCommand(), c.GetDir()}, c.GetArgs()...) {
		if strings.IndexFunc(f, func(r rune) bool { return r != '\t' && unicode.IsControl(r) }) >= 0 {
			return fmt.Errorf("%w: the command, its arguments and its directory must not hold control characters such as NUL or line breaks (a tab is allowed)", ErrInvalidArgs)
		}
	}
	return nil
}

// ruleErr maps an mcprules violation onto this package's ErrInvalidArgs.
func ruleErr(err error) error {
	if errors.Is(err, mcprules.ErrInvalid) {
		return fmt.Errorf("%w%s", ErrInvalidArgs, strings.TrimPrefix(err.Error(), mcprules.ErrInvalid.Error()))
	}
	return err
}

// redactLiterals replaces every credential literal in def's headers with its
// redacted form and lists those names in literal_on_host (D1, FR-423).
func redactLiterals(def *pb.McpServerDefinition) {
	for i, h := range def.Headers {
		if out, ok := mcprules.RedactHeader(h); ok {
			def.Headers[i] = out
			n, _ := mcprules.SplitHeader(h)
			def.LiteralOnHost = append(def.LiteralOnHost, n)
		}
	}
}

// refuseRecord is the lock holder's check for a leftover in-progress record
// (UNVERIFIED, research R2) and, for an add, for a pending replace
// (PENDING_EXISTS, R8).
func (m *Manager) refuseRecord(name string, add bool) error {
	s, err := m.reg.GetMcpSettings()
	if err != nil {
		return err
	}
	if s.GetSavesInProgress()[name] != nil {
		return ErrRecovering
	}
	if add && s.GetPendingReplaces()[name] != nil {
		return refuse(pb.McpSaveRefusal_PENDING_EXISTS, "%s has a pending replace; retry or cancel it", name)
	}
	return nil
}

// Remove unregisters a server, clears its mark, and relays the runtime's notes
// about credential material left behind (lines mentioning `sbx secret rm`).
func (m *Manager) Remove(ctx context.Context, name string) ([]string, error) {
	_, release, err := m.lockName(ctx, name, lockOpts{authSensitive: true})
	if err != nil {
		return nil, err
	}
	defer release()
	if err := m.refuseRecord(name, false); err != nil {
		return nil, err
	}
	mctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	out, err := m.cli.run(mctx, argvRemove(name)...)
	if err != nil {
		if LooksNotRegistered(out) {
			return nil, fmt.Errorf("%w: %s", ErrNotRegistered, name)
		}
		return nil, err
	}
	if _, err := m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		delete(s.Marks, name)
		delete(s.Definitions, name)
		return nil
	}); err != nil {
		return nil, err
	}
	m.emitChanged()
	return removalNotes(out), nil
}

// removalNotes keeps what the runtime says about credential material (a real
// host prints e.g. `No local OAuth token found for MCP server "notion"`, older
// docs show `sbx secret rm …` hints) and drops the bare removal confirmation.
func removalNotes(out string) []string {
	var notes []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || removedLine.MatchString(l) {
			continue
		}
		notes = append(notes, l)
	}
	return notes
}

var removedLine = regexp.MustCompile(`^MCP server ".*" removed\.?$`)

// LooksNotRegistered reports whether a runtime diagnostic says the named MCP
// server is unknown on this host (`… MCP server "ghost" is not registered …`).
// Exported for the gRPC layer, which maps it to NOT_FOUND for attach as well.
func LooksNotRegistered(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not registered") || strings.Contains(l, "not found") || strings.Contains(l, "unknown")
}

// ValidName is the rule for registering a server: the runtime's name rule, and
// never a leading '-' (which the CLI would parse as a flag).
func ValidName(name string) bool {
	return nameRe.MatchString(name) && !strings.HasPrefix(name, "-")
}

// SafeArg reports whether name can be passed as a single `sbx mcp` argument
// without being misread or mis-rendered: non-empty, valid UTF-8, no leading '-'
// (a flag), and no non-printable characters — C0/C1 controls, line/paragraph
// separators, or format characters such as bidi overrides and zero-width
// joiners, which could break a row or log line or reorder/hide text (bidi and
// zero-width tricks). It does not catch look-alike characters (homoglyphs) or
// combining marks. It is deliberately looser than ValidName — attach/detach act on
// servers the HOST registered (any name the runtime accepted, including ones
// registered with the sbx CLI directly), so only what could change the meaning
// of the argv is refused.
func SafeArg(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || len(name) > 256 {
		return false
	}
	if !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// LooksUnregistered reports whether a diagnostic says the SERVER NAME is "not
// registered" on the host. Stricter than LooksNotRegistered (which also matches
// "not found"/"unknown" and so must not gate record changes): the phrase must
// appear and the diagnostic must mention the server's name, so a "not registered"
// about something else (a sandbox, a project) cannot drop a live server's record.
func LooksUnregistered(out, name string) bool {
	l := strings.ToLower(out)
	if name == "" || !strings.Contains(l, "not registered") {
		return false
	}
	// The name as a whole token (not a fragment of another word, which a short name
	// would otherwise match everywhere).
	re, err := regexp.Compile(`(^|[^a-z0-9._-])` + regexp.QuoteMeta(strings.ToLower(name)) + `($|[^a-z0-9._-])`)
	return err == nil && re.MatchString(l)
}

// LooksNotLoaded reports whether an `sbx mcp unload` diagnostic says the server
// is not loaded in that sandbox, i.e. there is nothing left to unload and the
// sandbox record should simply drop it. The exact runtime wording is unverified
// (SWI-3), so the match is tolerant but limited to "not loaded" phrasings.
func LooksNotLoaded(out string) bool {
	l := strings.ToLower(out)
	for _, s := range []string{"not loaded", "isn't loaded", "not currently loaded"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// Authorize runs the host's authorization flow for a registered server as a
// bounded, killable child (research R2). Frames: the URL as soon as the runtime
// prints it, then progress lines, then a terminal Done (AUTHORIZED, or
// UNAUTHORIZED on timeout/cancel/failure — with an Error frame first for a
// failure). The registration is never touched; the only error returned is a
// failure to start the CLI at all.
//
// SWI-21: scopes are one-time scopes (`--scope` each) and noScope requests none
// (FR-418); neither keeps today's argv. It is refused while a save of the name
// holds or waits for the lock, or a pending replace or in-progress record
// exists, checked in the same m.mu hold that records the authorization (FR-424).
func (m *Manager) Authorize(ctx context.Context, name string, scopes []string, noScope bool, onFrame func(*pb.McpAuthProgress)) error {
	if noScope && len(scopes) > 0 {
		return fmt.Errorf("%w: one-time scopes and no scopes cannot be combined", ErrInvalidArgs)
	}
	if err := mcprules.ValidScopes(scopes); err != nil {
		return ruleErr(err)
	}
	m.mu.Lock()
	if _, busy := m.inflight[name]; busy {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAuthInProgress, name)
	}
	if m.busy[name] != nil {
		m.mu.Unlock()
		return refuse(pb.McpSaveRefusal_BEING_REPLACED, "%s is being saved; try again once the save finishes", name)
	}
	st := m.stateSnapshot(name)
	switch {
	case st.pending:
		m.mu.Unlock()
		return refuse(pb.McpSaveRefusal_BEING_REPLACED, "%s has a pending replace; retry or cancel it first", name)
	case st.recovering:
		m.mu.Unlock()
		return ErrRecovering
	case st.saving || st.recording:
		m.mu.Unlock()
		return ErrBeingReplaced
	}
	m.inflight[name] = 0
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.inflight, name)
		m.mu.Unlock()
	}()

	actx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()
	deadline := time.Now().Add(authTimeout).Unix()
	frame := func(p *pb.McpAuthProgress) {
		p.DeadlineUnix = deadline
		if onFrame != nil {
			onFrame(p)
		}
	}
	urlSent := false
	err := m.cli.runStreaming(actx, func(line string) {
		if !urlSent {
			if u := findURL(line); u != "" {
				urlSent = true
				port := callbackPort(u)
				if port == 0 {
					m.warnNoCallback(name)
				}
				if port != 0 {
					m.mu.Lock()
					m.inflight[name] = port
					m.mu.Unlock()
				}
				frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Url{Url: u}, CallbackPort: port})
				return
			}
		}
		if strings.TrimSpace(line) != "" {
			frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Message{Message: line}})
		}
	}, argvAuthorize(name, scopes, noScope)...)

	switch {
	case err == nil:
		frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Done{Done: pb.McpAuthState_MCP_AUTH_STATE_AUTHORIZED}})
		m.emitChanged()
		return nil
	case actx.Err() != nil:
		// Timeout or client cancel: the child was killed with its group.
		frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Done{Done: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED}})
		return nil
	default:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return err // could not start the CLI at all
		}
		frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Error{Error: err.Error()}})
		frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Done{Done: pb.McpAuthState_MCP_AUTH_STATE_UNAUTHORIZED}})
		return nil
	}
}

// warnNoCallback logs, once per daemon run, that an authorization link carried no
// usable loopback redirect_uri (remote callback relay/paste-back unavailable).
// Local daemons legitimately hit this on every authorization, so it must not
// repeat; the exact redirect_uri shape sbx prints is unverified (research R2).
func (m *Manager) warnNoCallback(name string) {
	m.mu.Lock()
	first := !m.warnedNoCallback
	m.warnedNoCallback = true
	m.mu.Unlock()
	if first {
		log.Printf("[mcp] authorize %s: link has no usable loopback redirect_uri; remote callback relay/paste-back unavailable (logged once)", name)
	}
}

// CallbackPort returns the OAuth callback port of the in-flight authorization for
// name. ok is false when none is running or its URL announced no loopback port, so
// a relay can only ever reach a listener an authorization started.
func (m *Manager) CallbackPort(name string) (uint32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	port := m.inflight[name]
	return port, port != 0
}

// ErrNoCallback: no in-flight authorization is waiting on a callback for the name
// → FAILED_PRECONDITION.
var ErrNoCallback = errors.New("no authorization is waiting for a callback")

// CompleteCallback is the paste-back path: it replays the browser's failed
// redirect (http://localhost:<port>/...) against the host's callback listener.
// The URL must be loopback and on the announced port, so it cannot be used to
// reach anything else.
func (m *Manager) CompleteCallback(ctx context.Context, name, rawURL string) error {
	port, ok := m.CallbackPort(name)
	if !ok {
		return ErrNoCallback
	}
	target, err := callbackTarget(rawURL, port)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgs, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgs, err)
	}
	resp, err := callbackClient.Do(req)
	if err != nil {
		return fmt.Errorf("replay callback: %w", err)
	}
	_ = resp.Body.Close()
	// A redirect to the provider-style error page (…?error=access_denied) is a
	// rejection too, not a delivery.
	if loc, lerr := resp.Location(); lerr == nil {
		if e := loc.Query().Get("error"); e != "" {
			return fmt.Errorf("the callback was answered with an authorization error (%s) — the provider rejected the sign-in", printable(e, 80))
		}
	}
	if resp.StatusCode >= 400 {
		// The listener rejected it (state mismatch, expired code, …): say so rather
		// than let the developer wait out the 10-minute timeout.
		return fmt.Errorf("the host's callback listener answered HTTP %d — the redirect was not accepted (copy the full, latest address from the browser)", resp.StatusCode)
	}
	return nil
}

// printable makes listener-controlled text safe to show in a terminal: control and
// non-printable characters are dropped and the result is capped at limit runes.
func printable(s string, limit int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if !unicode.IsPrint(r) {
			continue
		}
		if n == limit {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// callbackClient replays a pasted redirect on the host's loopback only: it never
// follows a redirect (the listener answers with one to its "success" page, which
// must not become a request to another host) and never uses a proxy.
var callbackClient = &http.Client{
	// No proxy, and no keep-alive: the listener is one-shot, so an idle pooled
	// connection to it would only linger (IdleConnTimeout 0 = forever).
	Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// SetDefault sets or clears the attach-by-default mark for a registered server
// (FR-080) and returns its decorated row.
//
// SWI-21 (research R2 Leftover records): refused with no change while a save of
// the name is saving/recording (BEING_REPLACED) or a leftover is recovering
// (UNVERIFIED): first from the state map, before any host call, then again
// inside the mark write's own transaction against saves_in_progress.
func (m *Manager) SetDefault(ctx context.Context, name string, on bool) (*pb.McpServer, error) {
	if err := m.toggleRefusal(name); err != nil {
		return nil, err
	}
	gens := m.generations()
	rows, err := m.listRows(ctx)
	if err != nil {
		return nil, err
	}
	var row *listRow
	for i := range rows {
		if rows[i].name == name {
			row = &rows[i]
			break
		}
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotRegistered, name)
	}
	if _, err := m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		if s.GetSavesInProgress()[name] != nil {
			if m.stateSnapshot(name).recovering {
				return ErrRecovering
			}
			return ErrBeingReplaced
		}
		setMarkIn(s, name, on)
		return nil
	}); err != nil {
		return nil, err
	}
	settings, _, err := m.reconcile(rows, gens)
	if err != nil {
		return nil, err
	}
	m.emitChanged()
	return m.decorate(ctx, []listRow{*row}, settings)[0], nil
}

// SetMode persists the daemon's default attach mode (FR-100); UNSPECIFIED is
// stored as ADDITIVE. Existing sandboxes are never touched.
func (m *Manager) SetMode(_ context.Context, mode pb.McpAttachMode) (*pb.McpGatewaySettings, error) {
	if mode == pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED {
		mode = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
	}
	settings, err := m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		s.DefaultAttachMode = mode
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emitChanged()
	return clientSettings(normalize(settings)), nil
}

// toggleRefusal is SetDefault's state-map fast path.
func (m *Manager) toggleRefusal(name string) error {
	st := m.stateSnapshot(name)
	switch {
	case st.recovering:
		return ErrRecovering
	case st.saving || st.recording:
		return ErrBeingReplaced
	}
	return nil
}

// setMarkIn sets or clears one mark in a settings record.
func setMarkIn(s *pb.McpGatewaySettings, name string, on bool) {
	if on {
		if s.Marks == nil {
			s.Marks = map[string]bool{}
		}
		s.Marks[name] = true
	} else {
		delete(s.Marks, name)
	}
}

// --- launch integration ---

// ResolveAttach intersects the marks with the host's registrations: Servers are
// the marked names the host still knows (sorted), Skipped the stale marks (now
// dropped), Mode the daemon's normalized default. A list failure is returned so
// the caller launches without MCP rather than guessing.
//
// SWI-21 (FR-425, research R12): a marked name whose lock is replacing or that
// has an in-progress record (live or leftover), and a pending name whose held
// mark is on, is in BeingReplaced instead.
func (m *Manager) ResolveAttach(ctx context.Context) (Attach, error) {
	gens := m.generations()
	rows, err := m.listRows(ctx)
	if err != nil {
		return Attach{}, err
	}
	settings, dropped, err := m.reconcile(rows, gens)
	if err != nil {
		return Attach{}, err
	}
	present := map[string]bool{}
	for _, r := range rows {
		present[r.name] = true
	}
	var servers, replaced []string
	for name, on := range settings.GetMarks() {
		if !on {
			continue
		}
		st := m.stateSnapshot(name)
		if st.replacing || st.saving || st.recovering || settings.GetSavesInProgress()[name] != nil {
			replaced = append(replaced, name)
			continue
		}
		if present[name] {
			servers = append(servers, name)
		}
	}
	for name, p := range settings.GetPendingReplaces() {
		if p.GetAttachByDefault() {
			replaced = append(replaced, name)
		}
	}
	sort.Strings(servers)
	sort.Strings(replaced)
	return Attach{Mode: settings.GetDefaultAttachMode(), Servers: servers, Skipped: dropped, BeingReplaced: replaced}, nil
}

// Load attaches a registered server to a created sandbox (`sbx mcp load`,
// additive mode). The CLI error is returned as-is for the launch log.
func (m *Manager) Load(ctx context.Context, name, sandboxName string) error {
	lctx, cancel := context.WithTimeout(ctx, time.Duration(loadTimeout))
	defer cancel()
	_, err := m.cli.run(lctx, argvLoad(name, sandboxName)...)
	return err
}

// --- helpers ---

func (m *Manager) emitChanged() {
	if m.emit == nil {
		return
	}
	m.emit(&pb.Event{Event: &pb.Event_McpGatewayChanged_{McpGatewayChanged: &pb.Event_McpGatewayChanged{HostId: m.hostID}}})
}

// emitOutcome emits one mcp_gateway_changed carrying a save outcome (nil for
// none) and the pending replaces from settings.
func (m *Manager) emitOutcome(out *pb.McpSaveOutcome, settings *pb.McpGatewaySettings) {
	if m.emit == nil {
		return
	}
	m.emit(&pb.Event{Event: &pb.Event_McpGatewayChanged_{McpGatewayChanged: &pb.Event_McpGatewayChanged{
		HostId: m.hostID, Outcome: out, PendingReplaces: pendingList(settings),
	}}})
}

func findServer(servers []*pb.McpServer, name string) *pb.McpServer {
	for _, s := range servers {
		if s.GetName() == name {
			return s
		}
	}
	return nil
}
