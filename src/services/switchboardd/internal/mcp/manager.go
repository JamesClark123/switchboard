package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
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

// Manager is one host's gateway manager: it reads registrations from the host
// CLI on every query and owns the per-daemon marks + attach mode in the
// registry's `mcp` bucket.
type Manager struct {
	cli    *CLI
	reg    *registry.Registry
	hostID string
	emit   func(*pb.Event) // nil-safe; Event.mcp_gateway_changed after every change

	mu               sync.Mutex
	warnedNoCallback bool              // warnNoCallback has logged
	inflight         map[string]uint32 // names with an authorization wait running -> its OAuth callback port (0 until announced)
}

// NewManager wires a Manager; emit may be nil.
func NewManager(cli *CLI, reg *registry.Registry, hostID string, emit func(*pb.Event)) *Manager {
	return &Manager{cli: cli, reg: reg, hostID: hostID, emit: emit, inflight: map[string]uint32{}}
}

// Attach is what a launch applies (data-model "Launch resolution"): the mode,
// the marked servers the host still knows, and the stale marks that were
// dropped — reported in the launch output, never a failed launch (FR-083).
type Attach struct {
	Mode    pb.McpAttachMode
	Servers []string
	Skipped []string
}

// --- queries ---

// List returns the host's registrations decorated with marks and auth state,
// plus the daemon's (normalized) settings. Stale marks are dropped on read.
func (m *Manager) List(ctx context.Context) ([]*pb.McpServer, *pb.McpGatewaySettings, error) {
	rows, err := m.listRows(ctx)
	if err != nil {
		return nil, nil, err
	}
	settings, _, err := m.reconcile(rows)
	if err != nil {
		return nil, nil, err
	}
	return m.decorate(ctx, rows, settings), settings, nil
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

// reconcile drops marks whose server the host no longer lists (invariant I-M2),
// persisting the drop, and returns the settings with the mode normalized
// (UNSPECIFIED reads as ADDITIVE) plus the dropped names.
func (m *Manager) reconcile(rows []listRow) (*pb.McpGatewaySettings, []string, error) {
	present := map[string]bool{}
	for _, r := range rows {
		present[r.name] = true
	}
	settings, err := m.reg.GetMcpSettings()
	if err != nil {
		return nil, nil, err
	}
	var dropped []string
	for name := range settings.GetMarks() {
		if !present[name] {
			dropped = append(dropped, name)
		}
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		settings, err = m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
			for _, name := range dropped {
				delete(s.Marks, name)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
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
		}
		if out[i].Kind != pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
			continue
		}
		wg.Add(1)
		go func(i int, r listRow) {
			defer wg.Done()
			// The dedicated query decides; the STATUS column ("needs auth") is the
			// fallback when it fails or says nothing recognisable.
			st := pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
			if res, err := m.cli.run(sctx, argvAuthStatus(r.name)...); err == nil {
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
// research R2), optionally marks it, and returns its decorated row. Host
// diagnostics travel verbatim in the error (FR-078).
func (m *Manager) Add(ctx context.Context, req *pb.AddMcpServerRequest) (*pb.McpServer, error) {
	name := strings.TrimSpace(req.GetName())
	if !ValidName(name) {
		return nil, fmt.Errorf("%w: server name %q must contain only letters, digits, dots, hyphens and underscores, and not start with a hyphen", ErrInvalidArgs, name)
	}
	if strings.HasPrefix(name, WorkerServerPrefix) {
		return nil, fmt.Errorf("%w: server name %q uses the reserved prefix %q", ErrInvalidArgs, name, WorkerServerPrefix)
	}
	var argv []string
	switch d := req.GetDefinition().(type) {
	case *pb.AddMcpServerRequest_Url:
		url := strings.TrimSpace(d.Url)
		if url == "" {
			return nil, fmt.Errorf("%w: a remote server needs a URL", ErrInvalidArgs)
		}
		headers, err := normalizeHeaders(req.GetHeaders())
		if err != nil {
			return nil, err
		}
		clientID, authServer, err := normalizeOAuthClient(req.GetClientId(), req.GetOauthAuthorizationServer())
		if err != nil {
			return nil, err
		}
		argv = argvAddRemote(name, url, headers, clientID, authServer)
	case *pb.AddMcpServerRequest_Command:
		if len(req.GetHeaders()) > 0 {
			return nil, fmt.Errorf("%w: custom headers apply to remote endpoints only", ErrInvalidArgs)
		}
		if strings.TrimSpace(req.GetClientId()) != "" || strings.TrimSpace(req.GetOauthAuthorizationServer()) != "" {
			return nil, fmt.Errorf("%w: OAuth client settings apply to remote endpoints only", ErrInvalidArgs)
		}
		if !req.GetAcknowledgeLocalExecution() {
			return nil, ErrLocalNotAcknowledged
		}
		c := d.Command
		if strings.TrimSpace(c.GetCommand()) == "" {
			return nil, fmt.Errorf("%w: a host-run server needs a command", ErrInvalidArgs)
		}
		for _, a := range c.GetArgs() {
			// The documented form joins args with commas and documents no escaping.
			if strings.Contains(a, ",") {
				return nil, fmt.Errorf("%w: argument %q contains a comma, which the host CLI cannot carry", ErrInvalidArgs, a)
			}
		}
		argv = argvAddLocal(name, c.GetCommand(), c.GetArgs(), c.GetDir())
	default:
		return nil, fmt.Errorf("%w: a URL or a command is required", ErrInvalidArgs)
	}
	mctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	if _, err := m.cli.run(mctx, argv...); err != nil {
		return nil, err
	}
	// Re-list first (it reconciles marks against the host), then mark — so a
	// list that momentarily misses the new name cannot drop the fresh mark.
	servers, _, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetAttachByDefault() {
		if err := m.setMark(name, true); err != nil {
			return nil, err
		}
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

// Remove unregisters a server, clears its mark, and relays the runtime's notes
// about credential material left behind (lines mentioning `sbx secret rm`).
func (m *Manager) Remove(ctx context.Context, name string) ([]string, error) {
	mctx, cancel := context.WithTimeout(ctx, time.Duration(mutateTimeout))
	defer cancel()
	out, err := m.cli.run(mctx, argvRemove(name)...)
	if err != nil {
		if LooksNotRegistered(out) {
			return nil, fmt.Errorf("%w: %s", ErrNotRegistered, name)
		}
		return nil, err
	}
	if err := m.setMark(name, false); err != nil {
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
func (m *Manager) Authorize(ctx context.Context, name string, onFrame func(*pb.McpAuthProgress)) error {
	m.mu.Lock()
	if _, busy := m.inflight[name]; busy {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrAuthInProgress, name)
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
	}, argvAuthorize(name)...)

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
func (m *Manager) SetDefault(ctx context.Context, name string, on bool) (*pb.McpServer, error) {
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
	if err := m.setMark(name, on); err != nil {
		return nil, err
	}
	settings, _, err := m.reconcile(rows)
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
	return normalize(settings), nil
}

// setMark updates one mark in the registry.
func (m *Manager) setMark(name string, on bool) error {
	_, err := m.reg.UpdateMcpSettings(func(s *pb.McpGatewaySettings) error {
		if on {
			if s.Marks == nil {
				s.Marks = map[string]bool{}
			}
			s.Marks[name] = true
		} else {
			delete(s.Marks, name)
		}
		return nil
	})
	return err
}

// --- launch integration ---

// ResolveAttach intersects the marks with the host's registrations: Servers are
// the marked names the host still knows (sorted), Skipped the stale marks (now
// dropped), Mode the daemon's normalized default. A list failure is returned so
// the caller launches without MCP rather than guessing.
func (m *Manager) ResolveAttach(ctx context.Context) (Attach, error) {
	rows, err := m.listRows(ctx)
	if err != nil {
		return Attach{}, err
	}
	settings, dropped, err := m.reconcile(rows)
	if err != nil {
		return Attach{}, err
	}
	present := map[string]bool{}
	for _, r := range rows {
		present[r.name] = true
	}
	var servers []string
	for name, on := range settings.GetMarks() {
		if on && present[name] {
			servers = append(servers, name)
		}
	}
	sort.Strings(servers)
	return Attach{Mode: settings.GetDefaultAttachMode(), Servers: servers, Skipped: dropped}, nil
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

func findServer(servers []*pb.McpServer, name string) *pb.McpServer {
	for _, s := range servers {
		if s.GetName() == name {
			return s
		}
	}
	return nil
}

// maxClientID bounds a pre-registered OAuth client ID (FR-105).
const maxClientID = 256

// normalizeOAuthClient trims and validates a remote server's OAuth client
// settings (FR-105): the client ID is one token (no spaces or control
// characters, not flag-like); the authorization server is an https URL (http
// only to localhost or a loopback IP) or an absolute path, which the daemon's `sbx` reads
// on this host. Either may be empty.
func normalizeOAuthClient(clientID, authServer string) (string, string, error) {
	clientID, authServer = strings.TrimSpace(clientID), strings.TrimSpace(authServer)
	if clientID != "" && (utf8.RuneCountInString(clientID) > maxClientID || !utf8.ValidString(clientID) || clientID[0] == '-' ||
		strings.IndexFunc(clientID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
		return "", "", fmt.Errorf("%w: client ID must be a single token of at most %d characters", ErrInvalidArgs, maxClientID)
	}
	if authServer != "" && (!authServerOK(authServer) || strings.IndexFunc(authServer, unicode.IsControl) >= 0) {
		return "", "", fmt.Errorf("%w: authorization server must be an https URL (http only to localhost or a loopback IP) or an absolute path on the daemon's host", ErrInvalidArgs)
	}
	return clientID, authServer, nil
}

// authServerOK accepts an absolute path, an https URL, or an http URL to a
// loopback IP or exactly localhost: the metadata names the endpoints the
// client secret is sent to, so it is never fetched in cleartext across a
// network. Unlike the callback rule (dial.IsLoopbackHost), *.localhost is
// refused: `sbx` dials this host, and the host's resolver may ask the
// network's DNS for such a name.
func authServerOK(s string) bool {
	if filepath.IsAbs(s) {
		return true
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return u.Scheme == "http" && (strings.EqualFold(h, "localhost") || ip != nil && ip.IsLoopback())
}

// headerNameRe is an HTTP field name (RFC 9110 token).
var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// normalizeHeaders validates "Name: value" entries for `--header` (FR-103):
// a token name, a colon, a non-empty single-line value; blank entries are
// dropped, duplicates of a name are refused (the runtime takes each name once).
func normalizeHeaders(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, h := range in {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		name, value, ok := strings.Cut(h, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		switch {
		case !ok || name == "" || value == "":
			return nil, fmt.Errorf("%w: header %q must be \"Name: value\"", ErrInvalidArgs, h)
		case !headerNameRe.MatchString(name):
			return nil, fmt.Errorf("%w: header name %q is not a valid HTTP field name", ErrInvalidArgs, name)
		case strings.ContainsAny(value, "\r\n"):
			return nil, fmt.Errorf("%w: header %q must be a single line", ErrInvalidArgs, name)
		case seen[strings.ToLower(name)]:
			return nil, fmt.Errorf("%w: header %q is given more than once", ErrInvalidArgs, name)
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name+": "+value)
	}
	return out, nil
}
