package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The call relay, worker side (research R6): the orchestrator's daemon opens
// one OrchestratorLink stream per host; the first frame is `hello`, which
// indexes the stream by orchestrator id (a newer hello for the same id replaces
// the older stream). A worker's gateway tool call becomes a `call` frame to the
// orchestrator and blocks for the matching `result` up to linkCallTimeout.

// linkCallTimeout bounds one relayed tool call (research R15).
const linkCallTimeout = 10 * time.Second

// ErrLinkDown is returned when an orchestrator has no live link or a call
// timed out; the endpoint renders it as the documented MCP error text.
var ErrLinkDown = errors.New("orchestrator link down")

// LinkDownError names the orchestrator (contracts/orchestrator-mcp.md §Endpoint).
type LinkDownError struct{ Name string }

func (e *LinkDownError) Error() string {
	return fmt.Sprintf("orchestrator %q is unreachable right now; retry in a moment — a failed report does not end the run", e.Name)
}

func (e *LinkDownError) Is(target error) bool { return target == ErrLinkDown }

// ServerStream is one accepted OrchestratorLink stream: the generated server
// stream, or the in-process half Local opens.
type ServerStream interface {
	Send(*pb.OrchestratorLinkFrame) error
	Recv() (*pb.OrchestratorLinkFrame, error)
	Context() context.Context
}

// link is one live stream to an orchestrator.
type link struct {
	orchID string
	name   string
	stream ServerStream
	sendMu sync.Mutex
	mu     sync.Mutex
	calls  map[string]chan *pb.OrchestratorLinkFrame_Result
	closed chan struct{}
	seq    uint64
}

func (l *link) send(fr *pb.OrchestratorLinkFrame) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return l.stream.Send(fr)
}

// linkIndex holds the live link per orchestrator id.
type linkIndex struct {
	mu    sync.Mutex
	links map[string]*link
	clock func() time.Time
}

func newLinkIndex() *linkIndex { return &linkIndex{links: map[string]*link{}, clock: time.Now} }

// AcceptLink serves one OrchestratorLink stream until it ends. The first frame
// MUST be hello (ErrInvalid otherwise); pings are answered; results are routed
// to the waiting call by id. Returns nil when the orchestrator closes its half.
func (ix *linkIndex) AcceptLink(stream ServerStream) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.GetOrchestratorId() == "" {
		return fmt.Errorf("%w: the first OrchestratorLink frame must be hello", ErrInvalid)
	}
	l := &link{orchID: hello.GetOrchestratorId(), name: hello.GetName(), stream: stream, calls: map[string]chan *pb.OrchestratorLinkFrame_Result{}, closed: make(chan struct{})}
	ix.mu.Lock()
	if old, ok := ix.links[l.orchID]; ok {
		old.close()
	}
	ix.links[l.orchID] = l
	ix.mu.Unlock()
	defer func() {
		ix.mu.Lock()
		if ix.links[l.orchID] == l {
			delete(ix.links, l.orchID)
		}
		ix.mu.Unlock()
		l.close()
	}()
	for {
		select {
		case <-l.closed:
			return nil
		default:
		}
		fr, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch f := fr.GetFrame().(type) {
		case *pb.OrchestratorLinkFrame_Ping_:
			_ = l.send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Ping_{Ping: &pb.OrchestratorLinkFrame_Ping{}}})
		case *pb.OrchestratorLinkFrame_Result_:
			l.mu.Lock()
			ch, ok := l.calls[f.Result.GetId()]
			if ok {
				delete(l.calls, f.Result.GetId())
			}
			l.mu.Unlock()
			if ok {
				ch <- f.Result
			}
		case *pb.OrchestratorLinkFrame_Hello_:
			// A second hello on the same stream is ignored.
		}
	}
}

func (l *link) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
		return
	default:
		close(l.closed)
	}
}

// Call relays one tool call to an orchestrator and waits for its result.
func (ix *linkIndex) Call(ctx context.Context, orchID, orchName, sandboxID, tool, argsJSON string) (string, error) {
	ix.mu.Lock()
	l, ok := ix.links[orchID]
	ix.mu.Unlock()
	if !ok {
		return "", &LinkDownError{Name: orchName}
	}
	l.mu.Lock()
	l.seq++
	id := strconv.FormatUint(l.seq, 10)
	ch := make(chan *pb.OrchestratorLinkFrame_Result, 1)
	l.calls[id] = ch
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.calls, id)
		l.mu.Unlock()
	}()
	if err := l.send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Call_{Call: &pb.OrchestratorLinkFrame_Call{Id: id, SandboxId: sandboxID, Tool: tool, ArgsJson: argsJSON}}}); err != nil {
		return "", &LinkDownError{Name: orchName}
	}
	timer := time.NewTimer(linkCallTimeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		if !res.GetOk() {
			return "", errors.New(res.GetError())
		}
		return res.GetBodyJson(), nil
	case <-l.closed:
		return "", &LinkDownError{Name: orchName}
	case <-timer.C:
		return "", &LinkDownError{Name: orchName}
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Linked reports whether an orchestrator has a live link.
func (ix *linkIndex) Linked(orchID string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	_, ok := ix.links[orchID]
	return ok
}

// --- in-process stream pair for Local (the orchestrator's own host) ---

// pipeStream is one half of an in-process OrchestratorLink: frames the client
// sends arrive on the server's Recv and vice versa.
type pipeStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	out    chan *pb.OrchestratorLinkFrame
	in     chan *pb.OrchestratorLinkFrame
}

func newPipe(ctx context.Context) (client, server *pipeStream) {
	cctx, cancel := context.WithCancel(ctx)
	c2s := make(chan *pb.OrchestratorLinkFrame, 16)
	s2c := make(chan *pb.OrchestratorLinkFrame, 16)
	return &pipeStream{ctx: cctx, cancel: cancel, out: c2s, in: s2c}, &pipeStream{ctx: cctx, cancel: cancel, out: s2c, in: c2s}
}

func (p *pipeStream) Send(fr *pb.OrchestratorLinkFrame) error {
	select {
	case p.out <- fr:
		return nil
	case <-p.ctx.Done():
		return io.EOF
	}
}

func (p *pipeStream) Recv() (*pb.OrchestratorLinkFrame, error) {
	select {
	case fr := <-p.in:
		return fr, nil
	case <-p.ctx.Done():
		return nil, io.EOF
	}
}

func (p *pipeStream) CloseSend() error         { p.cancel(); return nil }
func (p *pipeStream) Context() context.Context { return p.ctx }
