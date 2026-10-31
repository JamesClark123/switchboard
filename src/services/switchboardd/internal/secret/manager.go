package secret

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// ErrInvalidArgs rejects a request before any CLI call.
var ErrInvalidArgs = errors.New("invalid secret request")

// nameRe accepts service ids (anthropic, my-service), gateway placeholders
// (mcp:<server>:<placeholder>) and registry hosts (ghcr.io, registry:5000).
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]*$`)

// Manager drives `sbx secret` on this host and emits a change event after every
// successful mutation so other clients re-list.
type Manager struct {
	cli    *CLI
	hostID string
	emit   func(*pb.Event)
}

// NewManager wires the CLI runner; emit MAY be nil.
func NewManager(cli *CLI, hostID string, emit func(*pb.Event)) *Manager {
	return &Manager{cli: cli, hostID: hostID, emit: emit}
}

// List returns the host's stored secrets with masked values (FR-104).
func (m *Manager) List(ctx context.Context) ([]*pb.Secret, error) {
	lctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	out, err := m.cli.run(lctx, "", argvList()...)
	if err != nil {
		return nil, err
	}
	return parseList(out), nil
}

// Set stores (or overwrites) a service secret. The value is piped to the CLI's
// stdin followed by a newline, exactly as the documented `echo … | sbx secret
// set` form does; it is never placed in argv. Single-line values only: the
// runtime reads one line, so a multi-line value would be silently truncated.
func (m *Manager) Set(ctx context.Context, req *pb.SetSecretRequest) (*pb.Secret, error) {
	name := strings.TrimSpace(req.GetName())
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: secret name %q must start with a letter or digit and use only letters, digits, dots, colons, slashes, hyphens and underscores", ErrInvalidArgs, name)
	}
	value := req.GetValue()
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%w: a value is required", ErrInvalidArgs)
	}
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("%w: the value must be a single line (set multi-line secrets on the host with sbx secret set)", ErrInvalidArgs)
	}
	sandbox := strings.TrimSpace(req.GetSandbox())
	if sandbox != "" && !nameRe.MatchString(sandbox) {
		return nil, fmt.Errorf("%w: sandbox name %q is not a valid runtime name", ErrInvalidArgs, sandbox)
	}
	mctx, cancel := context.WithTimeout(ctx, mutateTimeout)
	defer cancel()
	if _, err := m.cli.run(mctx, value+"\n", argvSet(name, sandbox)...); err != nil {
		return nil, err
	}
	m.emitChanged()
	scope := "global"
	if sandbox != "" {
		scope = sandbox
	}
	// Report the row as the runtime now prints it when the list can be read;
	// otherwise a synthesized row with a fully masked value.
	if listed, err := m.List(ctx); err == nil {
		for _, s := range listed {
			if s.GetName() == name && s.GetScope() == scope {
				return s, nil
			}
		}
	}
	return &pb.Secret{Scope: scope, Type: "service", Name: name, MaskedValue: "****"}, nil
}

// Remove deletes a stored secret: a service secret by name, or a registry
// credential by host when type is "registry".
func (m *Manager) Remove(ctx context.Context, req *pb.RemoveSecretRequest) error {
	name := strings.TrimSpace(req.GetName())
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w: secret name %q is not valid", ErrInvalidArgs, name)
	}
	sandbox := strings.TrimSpace(req.GetSandbox())
	if sandbox != "" && !nameRe.MatchString(sandbox) {
		return fmt.Errorf("%w: sandbox name %q is not a valid runtime name", ErrInvalidArgs, sandbox)
	}
	typ := strings.ToLower(strings.TrimSpace(req.GetType()))
	if typ == "custom" {
		return fmt.Errorf("%w: custom secrets (sbx secret set-custom) are managed on the host", ErrInvalidArgs)
	}
	mctx, cancel := context.WithTimeout(ctx, mutateTimeout)
	defer cancel()
	if _, err := m.cli.run(mctx, "", argvRemove(name, sandbox, typ)...); err != nil {
		return err
	}
	m.emitChanged()
	return nil
}

// LooksNotFound reports whether a runtime diagnostic says the secret is unknown.
func LooksNotFound(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not found") || strings.Contains(l, "no such") || strings.Contains(l, "does not exist") || strings.Contains(l, "unknown secret")
}

func (m *Manager) emitChanged() {
	if m.emit == nil {
		return
	}
	m.emit(&pb.Event{Event: &pb.Event_SecretsChanged_{SecretsChanged: &pb.Event_SecretsChanged{HostId: m.hostID}}})
}

// --- SWI-21: the MCP form's save (FR-409, FR-410, FR-421) ---
//
// The MCP manager stores and removes a server's secrets inside one save and
// emits secrets_changed once at the end, so these variants skip emitChanged.

// ListGlobal returns the names stored in the global scope only (FR-409): a
// sandbox-scoped secret of the same name does not count.
func (m *Manager) ListGlobal(ctx context.Context) (map[string]bool, error) {
	all, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range all {
		if s.GetScope() == "global" && s.GetType() == "service" {
			out[s.GetName()] = true
		}
	}
	return out, nil
}

// SetQuiet stores a global service secret without emitting. The value goes to
// stdin only; an error never carries it.
func (m *Manager) SetQuiet(ctx context.Context, name, value string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%w: secret name %q is not valid", ErrInvalidArgs, name)
	}
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%w: the value of %s must be a single non-empty line", ErrInvalidArgs, name)
	}
	mctx, cancel := context.WithTimeout(ctx, mutateTimeout)
	defer cancel()
	_, err := m.cli.run(mctx, value+"\n", argvSet(name, "")...)
	return err
}

// RemoveQuiet removes a global service secret without emitting and returns the
// CLI output, so the caller can re-check a failure against ListGlobal.
func (m *Manager) RemoveQuiet(ctx context.Context, name string) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("%w: secret name %q is not valid", ErrInvalidArgs, name)
	}
	mctx, cancel := context.WithTimeout(ctx, mutateTimeout)
	defer cancel()
	return m.cli.run(mctx, "", argvRemove(name, "", "")...)
}

// EmitChanged emits one secrets_changed (after a save's quiet mutations).
func (m *Manager) EmitChanged() { m.emitChanged() }
