package client

import (
	"context"
	"fmt"
	"io"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// LaunchUpdate is a single progress event emitted during a launch.
type LaunchUpdate struct {
	Copy    *pb.LaunchProgress_CopyProgress
	LogLine string
	Done    *pb.Sandbox
	Blocked *pb.ResourceReport
}

// OptionManifest returns the host's full sbx option surface (FR-014).
func (c *Conn) OptionManifest(ctx context.Context) (*pb.OptionManifest, error) {
	return c.api.GetOptionManifest(ctx, &pb.GetOptionManifestRequest{})
}

// List returns all sandboxes on the connected daemon.
func (c *Conn) List(ctx context.Context) ([]*pb.Sandbox, error) {
	resp, err := c.api.ListSandboxes(ctx, &pb.ListSandboxesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetSandboxes(), nil
}

// Launch starts a sandbox and invokes onUpdate for each progress event. It
// returns the terminal Sandbox, or a non-nil blocked report if a low-resource
// gate stopped the launch without override (FR-012f).
func (c *Conn) Launch(ctx context.Context, req *pb.LaunchSandboxRequest, onUpdate func(LaunchUpdate)) (*pb.Sandbox, *pb.ResourceReport, error) {
	stream, err := c.api.LaunchSandbox(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch ev := msg.GetEvent().(type) {
		case *pb.LaunchProgress_Copy:
			if onUpdate != nil {
				onUpdate(LaunchUpdate{Copy: ev.Copy})
			}
		case *pb.LaunchProgress_LogLine:
			if onUpdate != nil {
				onUpdate(LaunchUpdate{LogLine: ev.LogLine})
			}
		case *pb.LaunchProgress_Blocked:
			return nil, ev.Blocked, nil
		case *pb.LaunchProgress_Done:
			return ev.Done, nil, nil
		}
	}
	return nil, nil, fmt.Errorf("launch ended without a terminal result")
}

// Stop stops a sandbox, retaining its copy (FR-012a).
func (c *Conn) Stop(ctx context.Context, id string) (*pb.Sandbox, error) {
	return c.api.StopSandbox(ctx, &pb.SandboxIdRequest{SandboxId: id})
}

// Restart restarts a stopped sandbox from its retained copy (FR-012b).
func (c *Conn) Restart(ctx context.Context, id string) (*pb.Sandbox, error) {
	stream, err := c.api.RestartSandbox(ctx, &pb.SandboxIdRequest{SandboxId: id})
	if err != nil {
		return nil, err
	}
	var done *pb.Sandbox
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if d := msg.GetDone(); d != nil {
			done = d
		}
	}
	return done, nil
}

// Destroy removes a sandbox and deletes its copy (FR-012c).
func (c *Conn) Destroy(ctx context.Context, id string) (bool, error) {
	resp, err := c.api.DestroySandbox(ctx, &pb.SandboxIdRequest{SandboxId: id})
	if err != nil {
		return false, err
	}
	return resp.GetDeletedWorkspace(), nil
}

// Rename sets a custom display name (FR-012e).
func (c *Conn) Rename(ctx context.Context, id, name string) (*pb.Sandbox, error) {
	return c.api.RenameSandbox(ctx, &pb.RenameSandboxRequest{SandboxId: id, DisplayName: name})
}

// SetTag sets or clears a sandbox's mutable purpose tag (feature 003, FR-021..024).
func (c *Conn) SetTag(ctx context.Context, id, tag string) (*pb.Sandbox, error) {
	return c.api.SetSandboxTag(ctx, &pb.SetSandboxTagRequest{SandboxId: id, Tag: tag})
}

// ResolveWorkspace asks the daemon which sandbox owns a filesystem path, so `sxb`
// run inside a workspace can attach to that sandbox's session (feature 003, FR-017/018).
func (c *Conn) ResolveWorkspace(ctx context.Context, path string) (*pb.ResolveWorkspaceResponse, error) {
	return c.api.ResolveWorkspace(ctx, &pb.ResolveWorkspaceRequest{Path: path})
}

// TermSession is the caller's handle to a live persistent terminal attachment.
// *AttachStream satisfies it; the TUI depends on this interface so it can be faked.
type TermSession interface {
	SendData(p []byte) error
	SendResize(cols, rows uint32) error
	Close() error
}

// AttachTerminal opens the sandbox's persistent session and streams snapshot +
// live PTY bytes into sink (feature 003). It is a thin convenience over
// AttachAgent that returns the TermSession interface.
func (c *Conn) AttachTerminal(ctx context.Context, sandboxID string, kind AttachKind, cols, rows uint32, sink io.Writer) (TermSession, error) {
	return c.AttachAgent(ctx, AttachOptions{
		SandboxID: sandboxID,
		Kind:      kind,
		Cols:      cols,
		Rows:      rows,
		Label:     "sxb-tui",
		Sink:      sink,
	})
}

// EventStream is the receive side of a Subscribe stream.
type EventStream interface {
	Recv() (*pb.Event, error)
}

// Subscribe opens the live event stream (sandbox changes + notifications). When
// replay is set the daemon first replays undelivered notifications (FR-026b).
func (c *Conn) Subscribe(ctx context.Context, replay bool) (EventStream, error) {
	return c.api.Subscribe(ctx, &pb.SubscribeRequest{ReplayUndelivered: replay})
}

// VSCodeTarget returns the sandbox's controlled workspace folder on its host, so
// the client opens that folder in VS Code (locally or over Remote-SSH) (FR-027).
func (c *Conn) VSCodeTarget(ctx context.Context, sandboxID string) (*pb.VSCodeTarget, error) {
	return c.api.GetVSCodeTarget(ctx, &pb.SandboxIdRequest{SandboxId: sandboxID})
}

// PromptAgent delivers a prompt to a sandbox's agent (FR-022).
func (c *Conn) PromptAgent(ctx context.Context, sandboxID, prompt string) error {
	resp, err := c.api.PromptAgent(ctx, &pb.PromptAgentRequest{SandboxId: sandboxID, Prompt: prompt})
	if err != nil {
		return err
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("agent did not accept the prompt")
	}
	return nil
}

// AckNotifications marks notifications delivered so they are not replayed.
func (c *Conn) AckNotifications(ctx context.Context, ids []string) error {
	_, err := c.api.AckNotification(ctx, &pb.AckNotificationRequest{NotificationIds: ids})
	return err
}

// ListSources enumerates launch candidates under root (FR-007).
func (c *Conn) ListSources(ctx context.Context, root string, reposOnly bool) ([]*pb.SourceRef, error) {
	resp, err := c.api.ListSourceCandidates(ctx, &pb.ListSourceCandidatesRequest{Root: root, ReposOnly: reposOnly})
	if err != nil {
		return nil, err
	}
	return resp.GetCandidates(), nil
}
