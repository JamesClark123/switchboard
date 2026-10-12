package sandbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// opAttachMcp labels the per-sandbox operation latch while a server is loaded
// into an existing sandbox, so the load can never interleave with a refresh or a
// source edit that may be recreating the container (feature 008, FR-102).
const opAttachMcp = "attach mcp server"

// ErrMcpAlreadyAttached: switchboard already records the server on the sandbox.
var ErrMcpAlreadyAttached = errors.New("MCP server already attached to this sandbox")

// AttachMcp attaches a registered MCP server to an EXISTING sandbox (FR-102):
// `sbx mcp load <name> --sandbox <ref>` against the sandbox's container, then
// the name is appended to the record and the change is emitted. The runtime
// delivers the new tools to a connected agent live (confirmed on a real host,
// research R4), so the sandbox is never stopped or restarted, and it works on a
// dynamic and on a `--static-mcp` sandbox alike.
//
// Eligible states are RUNNING and STOPPED, like a source edit; the runtime
// starts a stopped sandbox on demand. A name switchboard already recorded is
// refused before any CLI call (ErrMcpAlreadyAttached). A failed load leaves the
// record untouched and the runtime's own diagnostic travels back verbatim. An
// unregistered name is the runtime's "is not registered" failure — the gRPC
// layer maps it. The sandbox's attach mode is recorded as additive when the
// launch pre-dates feature 008 and left no mode behind.
func (m *Manager) AttachMcp(ctx context.Context, id, name string, onLog func(string)) (*pb.Sandbox, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("MCP server name is required")
	}
	if err := m.ops.acquire(id, opAttachMcp); err != nil {
		return nil, err
	}
	defer m.ops.release(id)

	sb, err := m.store.Get(id)
	if err != nil {
		return nil, err
	}
	if err := attachableState(sb.GetState()); err != nil {
		return nil, err
	}
	if slices.Contains(sb.GetMcpServers(), name) {
		return nil, fmt.Errorf("%w: %s", ErrMcpAlreadyAttached, name)
	}
	if sb.GetContainerRef() == "" {
		return nil, fmt.Errorf("sandbox %s has no container to attach an MCP server to", id)
	}

	lctx, cancel := context.WithTimeout(ctx, mcpLoadTimeout)
	defer cancel()
	if err := tryRefs(sbxRefs(sb), func(r string) error { return m.runner.LoadMcp(lctx, r, name, onLog) }); err != nil {
		return nil, err
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		if !slices.Contains(s.GetMcpServers(), name) {
			s.McpServers = append(s.McpServers, name)
		}
		if s.GetMcpAttachMode() == pb.McpAttachMode_MCP_ATTACH_MODE_UNSPECIFIED {
			s.McpAttachMode = pb.McpAttachMode_MCP_ATTACH_MODE_ADDITIVE
		}
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

// attachableState mirrors editableState for MCP attach: RUNNING or STOPPED only.
func attachableState(st pb.SandboxState) error {
	switch st {
	case pb.SandboxState_SANDBOX_STATE_RUNNING, pb.SandboxState_SANDBOX_STATE_STOPPED:
		return nil
	}
	label := strings.ToLower(strings.TrimPrefix(st.String(), "SANDBOX_STATE_"))
	return fmt.Errorf("%w: the sandbox is %s (MCP servers can be attached only while running or stopped)", ErrIneligibleState, label)
}
