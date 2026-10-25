package sandbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// opAttachMcp labels the per-sandbox operation latch while a server is loaded
// into an existing sandbox, so the load can never interleave with a refresh or a
// source edit that may be recreating the container (feature 008, FR-102).
const opAttachMcp = "attach mcp server"

// opDetachMcp labels the latch while a server is unloaded (SWI-3).
const opDetachMcp = "detach mcp server"

// ErrMcpInvalidName: the name cannot be a server name on the `sbx mcp` command line
// (bad characters, or a leading '-' the CLI would parse as a flag) → INVALID_ARGUMENT.
var ErrMcpInvalidName = errors.New("invalid MCP server name")

// ErrMcpNotAttached: switchboard does not record the server on the sandbox.
var ErrMcpNotAttached = errors.New("MCP server is not attached to this sandbox")

// ErrMcpReserved: the name is a switchboard-internal per-worker orchestrator
// server (feature 009), which is not the developer's to detach.
var ErrMcpReserved = errors.New("orchestrator MCP servers cannot be detached")

// ErrMcpToolCollision: loading the server would put a tool in the gateway's flat
// namespace that another loaded server already provides → FAILED_PRECONDITION.
var ErrMcpToolCollision = errors.New("MCP tool name collision")

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
	if !mcp.SafeArg(name) {
		return nil, fmt.Errorf("%w: %q (names may not start with '-' or contain control or other non-printable characters)", ErrMcpInvalidName, name)
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
	logLine, captured := captureOutput(onLog)
	var collision error
	var hint string
	err = tryRefs(sbxRefs(sb), func(r string) error {
		captured.Reset()
		err := m.runner.LoadMcp(lctx, r, name, logLine)
		if err == nil {
			return nil
		}
		if hint = collisionHint(err, captured); hint != "" {
			// A clash is about the server, not the handle: trying the next ref
			// would only replace this diagnostic with an unrelated one.
			collision = err
			return nil
		}
		return err
	})
	if collision != nil {
		// For a sandbox that already has the clashing server attached there is a
		// simpler remedy than filtering its tools: detach that server first.
		for _, c := range mcp.ParseToolCollisions(captured.String() + "\n" + collision.Error()) {
			if slices.Contains(sb.GetMcpServers(), c.Other) {
				hint += fmt.Sprintf("; or detach %s from this sandbox first (d in the MCP overlay)", c.Other)
				break
			}
		}
		return nil, fmt.Errorf("%w: %s (runtime: %v)", ErrMcpToolCollision, hint, collision)
	}
	if err != nil {
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

// DetachMcp removes an MCP server from an EXISTING sandbox (SWI-3):
// `sbx mcp unload <name> --sandbox <ref>`, then the name is dropped from the
// record and the change emitted. Mirror of AttachMcp: same latch, same
// RUNNING/STOPPED eligibility, never stops or restarts the sandbox, and the
// server stays registered on the host (re-attachable). A name switchboard does
// not record is refused before any CLI call (ErrMcpNotAttached); the per-worker
// orchestrator server is refused outright (ErrMcpReserved). If the runtime says
// the server is not loaded (or no longer registered) the record still drops it —
// the record follows the runtime. Any other failure leaves the record untouched
// and the runtime's diagnostic travels back verbatim.
func (m *Manager) DetachMcp(ctx context.Context, id, name string, onLog func(string)) (*pb.Sandbox, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("MCP server name is required")
	}
	// Case-sensitive prefix on the trimmed name: a server a developer registered
	// under this reserved prefix cannot be detached (the prefix is switchboard's).
	if strings.HasPrefix(name, mcp.WorkerServerPrefix) {
		return nil, fmt.Errorf("%w: %s", ErrMcpReserved, name)
	}
	if err := m.ops.acquire(id, opDetachMcp); err != nil {
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
	// The record gates everything below: a name switchboard never recorded is
	// refused (as invalid if it could not even be an argument, else not-attached).
	if !slices.Contains(sb.GetMcpServers(), name) {
		if !mcp.SafeArg(name) {
			return nil, fmt.Errorf("%w: %q (names may not start with '-' or contain control or other non-printable characters)", ErrMcpInvalidName, name)
		}
		return nil, fmt.Errorf("%w: %s", ErrMcpNotAttached, name)
	}
	logLine, captured := captureOutput(onLog)
	// tolerated is set by the handle whose output says this server is not loaded
	// (or no longer registered): that handle reached the right sandbox, so the
	// search stops there instead of being overwritten by a later handle's "not found".
	var tolerated string
	if !mcp.SafeArg(name) {
		// A legacy record (written before names were checked) whose name could be
		// misread as a flag: it cannot be handed to the CLI, but it must not be
		// stuck in the record either — even on a sandbox with no container.
		logDetach(onLog, "mcp: %q cannot be passed to sbx safely; dropping it from the record only (unload it from the sandbox with the sbx CLI if it is still loaded)", name)
	} else if sb.GetContainerRef() == "" {
		return nil, fmt.Errorf("sandbox %s has no container to detach an MCP server from", id)
	} else if err := tryRefs(sbxRefs(sb), func(r string) error {
		// Each handle gets its own bound, so a hung first attempt cannot starve the retry.
		uctx, cancel := context.WithTimeout(ctx, mcpLoadTimeout)
		defer cancel()
		captured.Reset()
		e := m.runner.UnloadMcp(uctx, r, name, logLine)
		if e == nil {
			return nil
		}
		// Classify on what sbx PRINTED for this handle (its full streamed output),
		// never on the error string: that starts with the command line — which
		// always contains the server's name — and keeps only the last few lines of
		// output, so a phrase printed earlier would be missed (same reason attach
		// builds its collision hint from the captured output).
		switch out := captured.String(); {
		case mcp.LooksNotLoaded(out):
			tolerated = fmt.Sprintf("mcp: %s was not loaded in the sandbox; dropping it from the record", name)
			return nil
		case mcp.LooksUnregistered(out, name):
			// Removed from the host while still recorded: the only way to clean the
			// record. The runtime may still hold the tools, so say so in the log.
			tolerated = fmt.Sprintf("mcp: %s is no longer registered on this host; dropping it from the record (the runtime may still have it loaded in the sandbox)", name)
			return nil
		}
		return e
	}); err != nil {
		return nil, err
	}
	if tolerated != "" {
		logDetach(onLog, "%s", tolerated)
	}

	out, err := m.store.Update(id, func(s *pb.Sandbox) error {
		s.McpServers = slices.DeleteFunc(s.McpServers, func(n string) bool { return n == name })
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	m.emit(out)
	return out, nil
}

func logDetach(onLog func(string), format string, args ...any) {
	if onLog != nil {
		onLog(fmt.Sprintf(format, args...))
	}
}

// captureOutput returns a log callback that forwards each line to next (which
// may be nil) and also keeps it. A runner's error carries only a bounded tail
// of the output (outputTail), so building the collision hint from that alone
// would drop the earliest clashing tools (SWI-2).
func captureOutput(next func(string)) (func(string), *strings.Builder) {
	var buf strings.Builder
	return func(line string) {
		buf.WriteString(line)
		buf.WriteByte('\n')
		if next != nil {
			next(line)
		}
	}, &buf
}

// collisionHint is mcp.CollisionHint over the full captured output plus the
// error text ("" when neither is a tool-name collision).
func collisionHint(err error, captured *strings.Builder) string {
	return mcp.CollisionHint(captured.String() + "\n" + err.Error())
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
