package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// WorkerAPI is the worker side as the runtime calls it (research R3/R6).
type WorkerAPI = worker.API

// hostLink is one machine the orchestrator reaches (research R2/R6): the
// orchestrator's own host, served in-process, or a host entry dialed over SSH
// with backoff while down. Over an up link the runtime holds one Subscribe
// stream (status), one OrchestratorLink stream (the call relay) and makes the
// unary worker-side calls.
type hostLink struct {
	rt     *runtime
	hostID string
	local  bool

	mu      sync.Mutex
	entry   *pb.OrchestratorHost // nil for the own host
	api     WorkerAPI            // nil while down
	up      bool
	lastErr string
	conn    *dial.Conn // the SSH-backed connection, remote only
	link    worker.LinkStream
	cancel  context.CancelFunc // cancels the loops of the current entry
	backoff time.Duration

	first     chan struct{} // closed once the first connect attempt has settled
	firstOnce sync.Once
}

func newHostLink(rt *runtime, hostID string, entry *pb.OrchestratorHost) *hostLink {
	return &hostLink{rt: rt, hostID: hostID, local: entry == nil, entry: entry, first: make(chan struct{})}
}

// settle marks the first connect attempt as finished (up or down).
func (l *hostLink) settle() { l.firstOnce.Do(func() { close(l.first) }) }

// syncLinks reconciles the runtime's links with the record's host entries: the
// own host always exists; new entries start dialing; removed ones stop.
func (rt *runtime) syncLinks() {
	rt.mu.Lock()
	rec := rt.rec
	want := map[string]*pb.OrchestratorHost{}
	for _, h := range rec.GetHosts() {
		want[h.GetHostId()] = h
	}
	if _, ok := rt.links[rt.svc.hostID]; !ok {
		l := newHostLink(rt, rt.svc.hostID, nil)
		rt.links[rt.svc.hostID] = l
		rt.startLink(l)
	}
	for id, l := range rt.links {
		if l.local {
			continue
		}
		if h, ok := want[id]; !ok {
			l.stopLink()
			delete(rt.links, id)
		} else if !proto.Equal(l.entryCopy(), h) {
			l.stopLink()
			delete(rt.links, id)
			want[id] = h
		} else {
			delete(want, id)
		}
	}
	for id, h := range want {
		l := newHostLink(rt, id, proto.Clone(h).(*pb.OrchestratorHost))
		rt.links[id] = l
		rt.startLink(l)
	}
	rt.mu.Unlock()
}

// startLink launches a link's loops (caller holds rt.mu).
func (rt *runtime) startLink(l *hostLink) {
	ctx, cancel := context.WithCancel(rt.ctx)
	l.cancel = cancel
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		l.run(ctx)
	}()
}

func (l *hostLink) stopLink() {
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (l *hostLink) entryCopy() *pb.OrchestratorHost {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entry
}

// link returns a host's link, or nil when the orchestrator has no entry for it.
func (rt *runtime) link(hostID string) *hostLink {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.links[hostID]
}

// reachability reports whether a host's link is up and, when not, why.
func (rt *runtime) reachability(hostID string) (bool, string) {
	l := rt.link(hostID)
	if l == nil {
		return false, "no host entry"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.up, l.lastErr
}

// apiFor returns the worker API of an up link, or ErrUnreachable / a
// precondition error naming the missing entry.
func (rt *runtime) apiFor(hostID string) (WorkerAPI, error) {
	l := rt.link(hostID)
	if l == nil {
		return nil, fmt.Errorf("%w: no host entry for %s", ErrPrecondition, hostID)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.up || l.api == nil {
		return nil, &UnreachableError{Host: hostID, Err: l.lastErr}
	}
	return l.api, nil
}

// awaitAPI is apiFor once the link's first connect attempt has settled, so a
// host entry added a moment ago (the TUI adds the host and attaches in one
// step, FR-105) is dialed before it can be reported unreachable.
func (rt *runtime) awaitAPI(ctx context.Context, hostID string) (WorkerAPI, error) {
	l := rt.link(hostID)
	if l == nil {
		return nil, fmt.Errorf("%w: no host entry for %s", ErrPrecondition, hostID)
	}
	select {
	case <-l.first:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-rt.ctx.Done():
		return nil, rt.ctx.Err()
	}
	return rt.apiFor(hostID)
}

// run is the link's life: for the own host it attaches to the in-process API
// once; for a remote host it dials with backoff, and on every (re)connect
// reconciles the host and holds the status + relay streams until one fails.
func (l *hostLink) run(ctx context.Context) {
	defer l.settle()
	clock := l.rt.svc.clock
	for ctx.Err() == nil {
		api, conn, err := l.connect(ctx)
		if err != nil {
			l.setDown(err.Error())
			l.settle()
			if !wait(ctx, clock, l.nextBackoff()) {
				return
			}
			continue
		}
		l.setUp(api, conn)
		l.settle()
		l.rt.svc.logf("[orchestrator %s] host %s link up", l.rt.name(), l.hostID)
		err = l.serve(ctx, api)
		l.setDown(describe(err, l.hostID))
		if conn != nil {
			_ = conn.Close()
		}
		if ctx.Err() != nil {
			return
		}
		l.rt.svc.logf("[orchestrator %s] host %s link down: %v", l.rt.name(), l.hostID, err)
		if !wait(ctx, clock, l.nextBackoff()) {
			return
		}
	}
}

// connect produces the API for this link: the local worker side, or a fresh SSH
// dial of the entry (BatchMode, FR-106).
func (l *hostLink) connect(ctx context.Context) (WorkerAPI, *dial.Conn, error) {
	if l.local {
		if l.rt.svc.local == nil {
			return nil, nil, errors.New("worker side not configured on this daemon")
		}
		return l.rt.svc.local, nil, nil
	}
	entry := l.entryCopy()
	dctx, cancel := context.WithTimeout(ctx, linkDialTimeout)
	defer cancel()
	conn, err := l.rt.svc.dial(dctx, entry.GetSshTarget(), entry.GetSshOptions())
	if err != nil {
		return nil, nil, err
	}
	return worker.Remote(conn.API()), conn, nil
}

// serve subscribes to the host's events and starts reading them at once, then
// reconciles it (off the reader, research R5), then opens the relay link, and
// runs until a stream ends. An events_dropped marker on the subscription
// re-runs a list-and-apply reconcile, coalesced. Returns the error that ended it.
func (l *hostLink) serve(ctx context.Context, api WorkerAPI) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := api.Subscribe(sctx)
	if err != nil {
		return err
	}
	errc := make(chan error, 4)
	resync := make(chan struct{}, 1)
	go func() { errc <- l.readEvents(sctx, events, resync) }()
	if err := l.rt.reconcile(sctx, l.hostID, api, true); err != nil {
		return err
	}
	go func() { errc <- l.resyncLoop(sctx, api, resync) }()
	link, err := api.OpenLink(sctx)
	if err != nil {
		return err
	}
	if err := link.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Hello_{Hello: &pb.OrchestratorLinkFrame_Hello{OrchestratorId: l.rt.id, Name: l.rt.name()}}}); err != nil {
		return err
	}
	l.mu.Lock()
	l.link = link
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.link = nil
		l.mu.Unlock()
	}()
	go func() { errc <- l.readLink(sctx, link) }()
	go func() { errc <- l.ping(sctx, link) }()
	l.rt.triggerDispatch()
	select {
	case <-sctx.Done():
		return sctx.Err()
	case err := <-errc:
		return err
	}
}

// readEvents folds the host's sandbox events into the runtime; an
// events_dropped marker asks for a resync (coalesced: one pending at most).
func (l *hostLink) readEvents(ctx context.Context, events worker.EventStream, resync chan<- struct{}) error {
	for {
		ev, err := events.Recv()
		if err != nil {
			return fmt.Errorf("status stream: %w", err)
		}
		if ev.GetEventsDropped() != nil {
			select {
			case resync <- struct{}{}:
			default:
			}
			continue
		}
		l.rt.onSandboxEvent(ctx, l.hostID, ev)
	}
}

// resyncLoop re-lists the host after the hub dropped events for this link: it
// lists and applies only, pushing no names or briefings (research R6).
func (l *hostLink) resyncLoop(ctx context.Context, api WorkerAPI, resync <-chan struct{}) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resync:
			if err := l.rt.reconcile(ctx, l.hostID, api, false); err != nil {
				return err
			}
		}
	}
}

// readLink answers the worker daemon's call frames (tools.go) and pings.
func (l *hostLink) readLink(ctx context.Context, link worker.LinkStream) error {
	for {
		fr, err := link.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("link closed by the host")
			}
			return fmt.Errorf("link: %w", err)
		}
		switch f := fr.GetFrame().(type) {
		case *pb.OrchestratorLinkFrame_Call_:
			res := l.rt.handleCall(ctx, l.hostID, f.Call)
			if err := link.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Result_{Result: res}}); err != nil {
				return err
			}
		case *pb.OrchestratorLinkFrame_Ping_:
			// keepalive from the host; nothing to do
		}
	}
}

// ping sends a keepalive every linkPingInterval.
func (l *hostLink) ping(ctx context.Context, link worker.LinkStream) error {
	for {
		if !wait(ctx, l.rt.svc.clock, linkPingInterval) {
			return ctx.Err()
		}
		if err := link.Send(&pb.OrchestratorLinkFrame{Frame: &pb.OrchestratorLinkFrame_Ping_{Ping: &pb.OrchestratorLinkFrame_Ping{}}}); err != nil {
			return fmt.Errorf("ping: %w", err)
		}
	}
}

func (l *hostLink) setUp(api WorkerAPI, conn *dial.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.api, l.conn, l.up, l.lastErr, l.backoff = api, conn, true, "", 0
}

func (l *hostLink) setDown(reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.api, l.conn, l.up, l.link = nil, nil, false, nil
	l.lastErr = reason
}

// nextBackoff doubles from linkRetryMin to linkRetryMax.
func (l *hostLink) nextBackoff() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.backoff == 0 {
		l.backoff = linkRetryMin
	} else if l.backoff < linkRetryMax {
		l.backoff *= 2
		if l.backoff > linkRetryMax {
			l.backoff = linkRetryMax
		}
	}
	return l.backoff
}

// isTooOld reports whether a worker-side call hit a daemon without feature 009.
func isTooOld(err error) bool {
	return status.Code(err) == codes.Unimplemented
}

// describe renders why a link went down: an old daemon by name, otherwise the
// error's last line.
func describe(err error, host string) string {
	if err == nil {
		return ""
	}
	if isTooOld(err) {
		return errTooOld(host)
	}
	var de *dial.DialError
	if errors.As(err, &de) {
		return de.Error()
	}
	msg := err.Error()
	if i := strings.LastIndex(strings.TrimSpace(msg), "\n"); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	return msg
}

// callCtx bounds a unary worker-side call.
func (rt *runtime) callCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(rt.ctx, d)
}
