package mcp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

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

	mu       sync.Mutex
	inflight map[string]struct{} // names with an authorization wait running
}

// NewManager wires a Manager; emit may be nil.
func NewManager(cli *CLI, reg *registry.Registry, hostID string, emit func(*pb.Event)) *Manager {
	return &Manager{cli: cli, reg: reg, hostID: hostID, emit: emit, inflight: map[string]struct{}{}}
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
	return parseList(out)
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
			Kind:            kindOf(r.typ),
			Target:          r.target,
			AuthState:       pb.McpAuthState_MCP_AUTH_STATE_NOT_APPLICABLE,
			AttachByDefault: settings.GetMarks()[r.name],
		}
		if out[i].Kind != pb.McpServerKind_MCP_SERVER_KIND_REMOTE {
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			res, err := m.cli.run(sctx, argvAuthStatus(name)...)
			if err != nil {
				out[i].AuthState = pb.McpAuthState_MCP_AUTH_STATE_UNKNOWN
				return
			}
			out[i].AuthState = parseAuthStatus(res)
		}(i, r.name)
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
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: server name %q must contain only letters, digits, dots, hyphens and underscores", ErrInvalidArgs, name)
	}
	var argv []string
	switch d := req.GetDefinition().(type) {
	case *pb.AddMcpServerRequest_Url:
		url := strings.TrimSpace(d.Url)
		if url == "" {
			return nil, fmt.Errorf("%w: a remote server needs a URL", ErrInvalidArgs)
		}
		argv = argvAddRemote(name, url)
	case *pb.AddMcpServerRequest_Command:
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
		if looksNotRegistered(out) {
			return nil, fmt.Errorf("%w: %s", ErrNotRegistered, name)
		}
		return nil, err
	}
	if err := m.setMark(name, false); err != nil {
		return nil, err
	}
	var notes []string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "sbx secret rm") {
			notes = append(notes, strings.TrimSpace(l))
		}
	}
	m.emitChanged()
	return notes, nil
}

func looksNotRegistered(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not registered") || strings.Contains(l, "not found") || strings.Contains(l, "unknown")
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
	m.inflight[name] = struct{}{}
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
				frame(&pb.McpAuthProgress{Event: &pb.McpAuthProgress_Url{Url: u}})
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
