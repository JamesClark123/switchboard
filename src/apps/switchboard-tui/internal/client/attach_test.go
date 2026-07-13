package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// attachServer implements just enough of Switchboard.AttachAgent for the client
// helper tests. Each test wires its own behavior in via the fields.
type attachServer struct {
	pb.UnimplementedSwitchboardServer
	// snapshot is sent as the first frame (skipped when nil).
	snapshot *pb.AgentOutput_Snapshot
	// live frames are streamed after the snapshot, in order.
	live [][]byte
	// rejectExternal returns FailedPrecondition on any EXTERNAL attach.
	rejectExternal bool
	// captured records everything the client sends, so tests can assert the
	// AttachInfo, keystrokes, and resize frames arrived intact.
	mu       sync.Mutex
	captured []*pb.AgentInput
	// firstReady is closed once the server has written the initial frame.
	firstReady chan struct{}
	// hold blocks the server after streaming live frames so tests that need to
	// observe an open session can inspect state before EOF.
	hold chan struct{}
}

func newAttachServer() *attachServer {
	return &attachServer{firstReady: make(chan struct{}), hold: make(chan struct{})}
}

func (s *attachServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: "h"}, nil
}

func (s *attachServer) AttachAgent(stream pb.Switchboard_AttachAgentServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.captured = append(s.captured, first)
	s.mu.Unlock()

	if s.rejectExternal && first.GetAttach().GetKind() == pb.ClientKind_CLIENT_KIND_EXTERNAL {
		return status.Error(codes.FailedPrecondition, "external terminal already attached")
	}
	if s.snapshot != nil {
		if err := stream.Send(&pb.AgentOutput{Snapshot: s.snapshot}); err != nil {
			return err
		}
	} else if len(s.live) > 0 {
		// Emit the first live frame as the first server frame when there's no
		// snapshot to send — mirrors the daemon path where the ring is empty
		// but new PTY output is already flowing.
		if err := stream.Send(&pb.AgentOutput{Data: s.live[0]}); err != nil {
			return err
		}
		s.live = s.live[1:]
	}
	close(s.firstReady)

	// Stream remaining live frames.
	for _, d := range s.live {
		if err := stream.Send(&pb.AgentOutput{Data: d}); err != nil {
			return err
		}
	}

	// Concurrently drain client-sent frames.
	go func() {
		for {
			in, err := stream.Recv()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.captured = append(s.captured, in)
			s.mu.Unlock()
		}
	}()

	select {
	case <-s.hold:
	case <-stream.Context().Done():
	}
	return nil
}

func (s *attachServer) capturedFrames() []*pb.AgentInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pb.AgentInput, len(s.captured))
	copy(out, s.captured)
	return out
}

func dialAttach(t *testing.T, srv *attachServer) *client.Conn {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialLocal(ctx, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// syncBuf is a concurrency-safe bytes.Buffer for the receive loop.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor polls cond every 5ms up to 2s.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", msg)
}

func TestAttachSnapshotThenLiveFrames(t *testing.T) {
	srv := newAttachServer()
	srv.snapshot = &pb.AgentOutput_Snapshot{Data: []byte("SNAP"), Rows: 24, Cols: 80}
	srv.live = [][]byte{[]byte("more"), []byte("data")}
	conn := dialAttach(t, srv)

	var sink syncBuf
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb1", Kind: client.AttachInTUI, Cols: 80, Rows: 24, Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()

	if string(as.Snapshot) != "SNAP" || as.Rows != 24 || as.Cols != 80 {
		t.Fatalf("snapshot fields = (%q,%d,%d)", as.Snapshot, as.Rows, as.Cols)
	}
	waitFor(t, func() bool { return sink.String() == "SNAPmoredata" }, "snapshot + live in sink")
}

func TestAttachSendsInitialAttachInfo(t *testing.T) {
	srv := newAttachServer()
	srv.live = [][]byte{[]byte("x")}
	conn := dialAttach(t, srv)

	var sink syncBuf
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb2", Kind: client.AttachExternal, Cols: 120, Rows: 40, Label: "tui-external", Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()

	// Wait for the first frame to arrive to guarantee the server captured
	// the initial input.
	<-srv.firstReady
	frames := srv.capturedFrames()
	if len(frames) == 0 {
		t.Fatal("server captured no client frames")
	}
	first := frames[0]
	if first.GetSandboxId() != "sb2" {
		t.Fatalf("sandbox_id = %q", first.GetSandboxId())
	}
	ai := first.GetAttach()
	if ai == nil {
		t.Fatal("expected AttachInfo on first frame")
	}
	if ai.GetKind() != pb.ClientKind_CLIENT_KIND_EXTERNAL {
		t.Fatalf("kind = %v, want EXTERNAL", ai.GetKind())
	}
	if ai.GetClientLabel() != "tui-external" {
		t.Fatalf("label = %q", ai.GetClientLabel())
	}
	is := ai.GetInitialSize()
	if is.GetCols() != 120 || is.GetRows() != 40 {
		t.Fatalf("initial size = (%d,%d)", is.GetCols(), is.GetRows())
	}
}

func TestAttachSendDataAndResize(t *testing.T) {
	srv := newAttachServer()
	srv.snapshot = &pb.AgentOutput_Snapshot{Data: []byte("s")}
	conn := dialAttach(t, srv)

	var sink syncBuf
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Cols: 80, Rows: 24, Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()

	if err := as.SendData([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	// Zero-length SendData is a no-op and must not surface an error.
	if err := as.SendData(nil); err != nil {
		t.Fatalf("zero-length SendData: %v", err)
	}
	if err := as.SendResize(100, 30); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		frames := srv.capturedFrames()
		var sawData, sawResize bool
		for _, f := range frames {
			if bytes.Equal(f.GetData(), []byte("hi\n")) {
				sawData = true
			}
			if r := f.GetResize(); r != nil && r.GetCols() == 100 && r.GetRows() == 30 {
				sawResize = true
			}
		}
		return sawData && sawResize
	}, "server saw data + resize")
}

func TestAttachExternalRejectedMapsToTypedError(t *testing.T) {
	srv := newAttachServer()
	srv.rejectExternal = true
	conn := dialAttach(t, srv)

	var sink syncBuf
	_, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachExternal, Cols: 80, Rows: 24, Sink: &sink,
	})
	if !errors.Is(err, client.ErrExternalAlreadyOpen) {
		t.Fatalf("AttachAgent err = %v, want ErrExternalAlreadyOpen", err)
	}
}

func TestAttachRequiresSandboxIDAndSink(t *testing.T) {
	conn := dialAttach(t, newAttachServer())
	if _, err := conn.AttachAgent(context.Background(), client.AttachOptions{Sink: &syncBuf{}}); err == nil {
		t.Error("empty SandboxID should error")
	}
	if _, err := conn.AttachAgent(context.Background(), client.AttachOptions{SandboxID: "sb"}); err == nil {
		t.Error("nil Sink should error")
	}
}

func TestAttachCloseIsIdempotent(t *testing.T) {
	srv := newAttachServer()
	srv.snapshot = &pb.AgentOutput_Snapshot{Data: []byte("s")}
	conn := dialAttach(t, srv)

	var sink syncBuf
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := as.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// The stream is already closed; Wait must return nil (canceled is a clean
	// detach path).
	if err := as.Wait(); err != nil {
		t.Fatalf("Wait after Close: %v", err)
	}
}

func TestAttachSinkWriteErrorPropagates(t *testing.T) {
	srv := newAttachServer()
	srv.snapshot = &pb.AgentOutput_Snapshot{Data: []byte("boom")}
	conn := dialAttach(t, srv)

	// A Sink that rejects everything — mirrors an in-place viewport that was
	// closed before the snapshot arrived. AttachAgent returns nil, err so the
	// caller has nothing to Close.
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Sink: errorSink{},
	})
	if err == nil {
		t.Fatal("expected AttachAgent to surface sink error")
	}
	if as != nil {
		t.Fatal("expected nil AttachStream when initial sink write fails")
	}
}

func TestAttachServerEOFClosesCleanly(t *testing.T) {
	srv := newAttachServer()
	srv.live = [][]byte{[]byte("hello")}
	conn := dialAttach(t, srv)

	var sink syncBuf
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	close(srv.hold) // server returns → EOF on the client
	if err := as.Wait(); err != nil {
		t.Fatalf("Wait after EOF = %v, want nil", err)
	}
}

// errorSink always fails Write, exercising the "sink rejected snapshot" branch.
type errorSink struct{}

func (errorSink) Write([]byte) (int, error) { return 0, io.ErrShortWrite }

// countingErrorSink fails only after n successful writes so tests can simulate
// a mid-stream sink failure inside the receive loop (e.g. the viewport was
// disposed while live output was arriving).
type countingErrorSink struct {
	mu sync.Mutex
	n  int
}

func (c *countingErrorSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n <= 0 {
		return 0, io.ErrClosedPipe
	}
	c.n--
	return len(p), nil
}

func TestAttachSinkFailureMidStreamTerminates(t *testing.T) {
	srv := newAttachServer()
	srv.snapshot = &pb.AgentOutput_Snapshot{Data: []byte("s")}
	srv.live = [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	conn := dialAttach(t, srv)

	sink := &countingErrorSink{n: 2} // snapshot + one live frame, then fail
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Sink: sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The receive loop should terminate on the sink error; Wait returns it.
	werr := as.Wait()
	if werr == nil {
		t.Fatal("expected Wait to surface sink error")
	}
	if !errors.Is(werr, io.ErrClosedPipe) {
		t.Fatalf("Wait err = %v, want ErrClosedPipe", werr)
	}
	_ = as.Close()
}

func TestAttachFirstFrameIsPureData(t *testing.T) {
	// When the daemon has no ring content, the very first frame carries live
	// data instead of a Snapshot — the client must accept both first-frame shapes.
	srv := newAttachServer()
	srv.live = [][]byte{[]byte("only-live")}
	conn := dialAttach(t, srv)

	var sink syncBuf
	as, err := conn.AttachAgent(context.Background(), client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	waitFor(t, func() bool { return sink.String() == "only-live" }, "sink received live-only data")
	if len(as.Snapshot) != 0 {
		t.Fatalf("snapshot should be empty, got %q", as.Snapshot)
	}
}

func TestAttachContextCancelledIsCleanDetach(t *testing.T) {
	srv := newAttachServer()
	srv.snapshot = &pb.AgentOutput_Snapshot{Data: []byte("s")}
	ctx, cancel := context.WithCancel(context.Background())
	conn := dialAttach(t, srv)
	var sink syncBuf
	as, err := conn.AttachAgent(ctx, client.AttachOptions{
		SandboxID: "sb", Kind: client.AttachInTUI, Sink: &sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := as.Wait(); err != nil {
		t.Fatalf("cancelled context should be treated as clean detach, got %v", err)
	}
}
