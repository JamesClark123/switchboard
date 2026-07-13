package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ErrExternalAlreadyOpen is returned by AttachAgent when the daemon rejects a
// second CLIENT_KIND_EXTERNAL attach for the same sandbox (FR-014/015). The TUI
// maps it to "bring the existing terminal window to the front" (extterm.go);
// `sxb attach` prints a message and exits non-zero (cli-attach.md).
var ErrExternalAlreadyOpen = errors.New("external terminal already attached to this sandbox")

// AttachKind selects the CLIENT_KIND used on the daemon side. IN_TUI is the
// in-place Bubble Tea view (US2); External is a full-screen `sxb attach`
// process (US3).
type AttachKind int

const (
	AttachInTUI AttachKind = iota
	AttachExternal
)

// AttachSink receives snapshot + live PTY bytes from the daemon. The termview
// Screen satisfies this (its Write feeds the parser); `sxb attach` uses os.Stdout
// directly so the OS terminal interprets the ANSI stream natively.
type AttachSink interface {
	io.Writer
}

// AttachStream is the live handle returned by AttachAgent. The caller writes
// keystrokes via SendData, publishes resize events via SendResize, and calls
// Close when it wants to detach (the daemon keeps the session running, FR-002).
// Errors from the receive loop surface on Err(). Wait blocks until the stream
// terminates and returns the terminal error.
type AttachStream struct {
	stream   pb.Switchboard_AttachAgentClient
	cancel   context.CancelFunc
	done     chan struct{}
	errOnce  chan error
	Snapshot []byte // the daemon's snapshot frame (empty when there was none)
	Cols     uint32 // snapshot rows/cols the daemon reports (may be 0)
	Rows     uint32
}

// AttachOptions controls how the client attaches to a sandbox's persistent
// session. Kind, SandboxID, and Sink are required; Cols/Rows are the initial
// window size the daemon uses as the smallest-of-attached seed (research.md R3);
// Label is an optional diagnostic string surfaced in daemon logs.
type AttachOptions struct {
	SandboxID string
	Kind      AttachKind
	Cols      uint32
	Rows      uint32
	Label     string
	Sink      AttachSink
}

// AttachAgent opens the daemon's persistent session for the given sandbox and
// starts pumping snapshot + live PTY bytes into opts.Sink. It sends the initial
// AttachInfo frame first, blocks briefly for the snapshot (or the first live
// frame, if the daemon has nothing buffered), then returns an AttachStream the
// caller drives with SendData / SendResize / Close.
//
// The returned stream's receive loop runs on a background goroutine; the caller
// is expected to call Wait (or Close then Wait) to release resources.
//
// A daemon FailedPrecondition (second EXTERNAL) surfaces as ErrExternalAlreadyOpen.
func (c *Conn) AttachAgent(ctx context.Context, opts AttachOptions) (*AttachStream, error) {
	if opts.SandboxID == "" {
		return nil, errors.New("AttachAgent: SandboxID is required")
	}
	if opts.Sink == nil {
		return nil, errors.New("AttachAgent: Sink is required")
	}

	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.api.AttachAgent(streamCtx)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open AttachAgent: %w", err)
	}

	initial := &pb.AgentInput{
		SandboxId: opts.SandboxID,
		Attach: &pb.AgentInput_AttachInfo{
			Kind:        pbClientKind(opts.Kind),
			ClientLabel: opts.Label,
			InitialSize: &pb.AgentInput_Resize{Cols: opts.Cols, Rows: opts.Rows},
		},
	}
	if err := stream.Send(initial); err != nil {
		cancel()
		return nil, mapAttachErr(err)
	}

	// Read the first frame synchronously so callers see the snapshot (or the
	// daemon's rejection) before returning control.
	first, err := stream.Recv()
	if err != nil {
		cancel()
		return nil, mapAttachErr(err)
	}

	a := &AttachStream{
		stream:  stream,
		cancel:  cancel,
		done:    make(chan struct{}),
		errOnce: make(chan error, 1),
	}

	if snap := first.GetSnapshot(); snap != nil {
		a.Snapshot = snap.GetData()
		a.Cols = snap.GetCols()
		a.Rows = snap.GetRows()
		if len(a.Snapshot) > 0 {
			if _, werr := opts.Sink.Write(a.Snapshot); werr != nil {
				cancel()
				_ = stream.CloseSend()
				return nil, werr
			}
		}
	} else if d := first.GetData(); len(d) > 0 {
		if _, werr := opts.Sink.Write(d); werr != nil {
			cancel()
			_ = stream.CloseSend()
			return nil, werr
		}
	}

	go a.recvLoop(opts.Sink)
	return a, nil
}

// SendData forwards a keystroke chunk to the sandbox PTY.
func (a *AttachStream) SendData(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if err := a.stream.Send(&pb.AgentInput{Data: p}); err != nil {
		return mapAttachErr(err)
	}
	return nil
}

// SendResize publishes a new client window size. The daemon reconciles the PTY
// to the smallest cols/rows across attached clients (research.md R3).
func (a *AttachStream) SendResize(cols, rows uint32) error {
	if err := a.stream.Send(&pb.AgentInput{Resize: &pb.AgentInput_Resize{Cols: cols, Rows: rows}}); err != nil {
		return mapAttachErr(err)
	}
	return nil
}

// Close detaches this client. The daemon keeps the session running for other
// attachments and future reconnects (FR-002/004).
func (a *AttachStream) Close() error {
	a.cancel()
	_ = a.stream.CloseSend()
	<-a.done
	return a.err()
}

// Wait blocks until the receive loop terminates (server EOF, transport error,
// or Close) and returns the terminal error. io.EOF and context cancellation
// are folded into a nil return — those are clean detach paths.
func (a *AttachStream) Wait() error {
	<-a.done
	return a.err()
}

func (a *AttachStream) recvLoop(sink AttachSink) {
	defer close(a.done)
	for {
		msg, err := a.stream.Recv()
		if err != nil {
			a.finish(err)
			return
		}
		if d := msg.GetData(); len(d) > 0 {
			if _, werr := sink.Write(d); werr != nil {
				a.finish(werr)
				return
			}
		}
	}
}

func (a *AttachStream) finish(err error) {
	select {
	case a.errOnce <- err:
	default:
	}
}

func (a *AttachStream) err() error {
	select {
	case err := <-a.errOnce:
		if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
			return nil
		}
		if s, ok := status.FromError(err); ok && s.Code() == codes.Canceled {
			return nil
		}
		return err
	default:
		return nil
	}
}

func pbClientKind(k AttachKind) pb.ClientKind {
	if k == AttachExternal {
		return pb.ClientKind_CLIENT_KIND_EXTERNAL
	}
	return pb.ClientKind_CLIENT_KIND_IN_TUI
}

// mapAttachErr translates gRPC status codes to typed client errors so callers
// can branch on ErrExternalAlreadyOpen without reaching into grpc/status. Any
// other error is returned as-is.
func mapAttachErr(err error) error {
	if err == nil {
		return nil
	}
	if s, ok := status.FromError(err); ok && s.Code() == codes.FailedPrecondition {
		return ErrExternalAlreadyOpen
	}
	return err
}
