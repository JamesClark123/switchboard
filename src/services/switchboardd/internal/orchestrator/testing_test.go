package orchestrator

import (
	"context"
	"net"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- fake clock (research R16) ---

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.stopped = true
	return !t.fired
}

// Advance moves the clock and fires every due timer in deadline order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*fakeTimer
	var rest []*fakeTimer
	for _, t := range c.timers {
		if !t.stopped && !t.at.After(c.now) {
			due = append(due, t)
		} else if !t.stopped {
			rest = append(rest, t)
		}
	}
	c.timers = rest
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
		t.fired = true
		t.f()
	}
}

// pending counts armed timers.
func (c *fakeClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped {
			n++
		}
	}
	return n
}

// --- fake hub ---

type fakeHub struct {
	mu     sync.Mutex
	events []*pb.Event
	notifs []*pb.NotificationEvent
}

func (h *fakeHub) Publish(ev *pb.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *fakeHub) EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := &pb.NotificationEvent{Id: "n", SandboxId: sandboxID, Kind: kind, Message: message}
	h.notifs = append(h.notifs, n)
	return n
}

// changed counts orchestrator_changed events for an id ("" = any).
func (h *fakeHub) changed(id string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, ev := range h.events {
		if c := ev.GetOrchestratorChanged(); c != nil && (id == "" || c.GetOrchestratorId() == id) {
			n++
		}
	}
	return n
}

func (h *fakeHub) notifications() []*pb.NotificationEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*pb.NotificationEvent(nil), h.notifs...)
}

// --- test service ---

type testEnv struct {
	svc   *Service
	reg   *registry.Registry
	hub   *fakeHub
	clock *fakeClock
	dials *dialRecorder
}

// dialRecorder is a dial.Func that counts calls and dials a scripted socket (or
// fails with a scripted error).
type dialRecorder struct {
	mu    sync.Mutex
	calls int
	socks map[string]string // ssh target -> unix socket
	err   error
}

func (d *dialRecorder) dial(ctx context.Context, target string, opts []string) (*dial.Conn, error) {
	d.mu.Lock()
	d.calls++
	sock, ok := d.socks[target]
	err := d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &dial.DialError{Kind: dial.DialUnreachable, Stderr: "ssh: connect to host " + target + " port 22: Connection refused"}
	}
	return dial.DialLocal(ctx, sock)
}

func (d *dialRecorder) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	env := &testEnv{reg: reg, hub: &fakeHub{}, clock: newFakeClock(), dials: &dialRecorder{socks: map[string]string{}}}
	env.svc = NewService(Deps{Store: reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test"})
	if err := env.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.svc.Stop)
	return env
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// --- fake worker daemon (an in-process gRPC server for a remote host) ---

// fakeWorker is a daemon on a "remote" host: scripted sandboxes, recorded link
// frames and worker-side calls. tooOld makes every feature-009 RPC answer
// UNIMPLEMENTED (an older daemon).
type fakeWorker struct {
	pb.UnimplementedSwitchboardServer
	hostID string
	mu     sync.Mutex
	sbs    map[string]*pb.Sandbox
	frames []*pb.OrchestratorLinkFrame
	calls  []string
	tooOld bool
	events chan *pb.Event
	links  int
	// linkIn lets a test push frames (call) to the orchestrator over the link.
	linkOut chan *pb.OrchestratorLinkFrame
}

func newFakeWorker(hostID string) *fakeWorker {
	return &fakeWorker{hostID: hostID, sbs: map[string]*pb.Sandbox{}, events: make(chan *pb.Event, 16), linkOut: make(chan *pb.OrchestratorLinkFrame, 16)}
}

func (w *fakeWorker) record(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, sprintf(format, args...))
}

func (w *fakeWorker) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: w.hostID}, nil
}

func (w *fakeWorker) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := &pb.ListSandboxesResponse{}
	for _, sb := range w.sbs {
		out.Sandboxes = append(out.Sandboxes, sb)
	}
	return out, nil
}

func (w *fakeWorker) Subscribe(_ *pb.SubscribeRequest, stream pb.Switchboard_SubscribeServer) error {
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case ev := <-w.events:
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

func (w *fakeWorker) OrchestratorLink(stream pb.Switchboard_OrchestratorLinkServer) error {
	if w.tooOld {
		return status.Error(codes.Unimplemented, "unknown method OrchestratorLink")
	}
	w.mu.Lock()
	w.links++
	w.mu.Unlock()
	errc := make(chan error, 1)
	go func() {
		for {
			fr, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			w.mu.Lock()
			w.frames = append(w.frames, fr)
			w.mu.Unlock()
		}
	}()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case err := <-errc:
			return err
		case fr := <-w.linkOut:
			if err := stream.Send(fr); err != nil {
				return err
			}
		}
	}
}

func (w *fakeWorker) AttachController(_ context.Context, req *pb.AttachControllerRequest) (*pb.Sandbox, error) {
	if w.tooOld {
		return nil, status.Error(codes.Unimplemented, "unknown method AttachController")
	}
	w.record("attach %s %s", req.GetSandboxId(), req.GetController().GetOrchestratorId())
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	if c := sb.GetController(); c != nil && c.GetOrchestratorId() != req.GetController().GetOrchestratorId() {
		return nil, status.Errorf(codes.AlreadyExists, "sandbox is controlled by %s", c.GetName())
	}
	sb.Controller = req.GetController()
	return sb, nil
}

func (w *fakeWorker) DetachController(_ context.Context, req *pb.DetachControllerRequest) (*pb.Sandbox, error) {
	w.record("detach %s force=%v", req.GetSandboxId(), req.GetForce())
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	sb.Controller = nil
	return sb, nil
}

func (w *fakeWorker) UpdateWorker(_ context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error) {
	w.record("update %s name=%q briefing=%v", req.GetSandboxId(), req.GetName(), req.Briefing != nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	if sb.Controller == nil {
		return nil, status.Error(codes.PermissionDenied, "not a worker")
	}
	if req.Name != nil {
		sb.Controller.Name = req.GetName()
	}
	if req.GetSetRun() {
		sb.Controller.Run = req.GetRun()
	}
	return sb, nil
}

func (w *fakeWorker) DeliverWorkerPrompt(_ context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	w.record("deliver %s %s", req.GetSandboxId(), req.GetRunId())
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE || sb.GetController().GetRun() != nil {
		return nil, status.Error(codes.FailedPrecondition, "not free")
	}
	sb.Controller.Run = &pb.WorkerRun{Id: req.GetRunId(), Action: req.GetAction()}
	return &pb.DeliverWorkerPromptResponse{Accepted: true}, nil
}

func (w *fakeWorker) frameKinds() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, f := range w.frames {
		switch f.GetFrame().(type) {
		case *pb.OrchestratorLinkFrame_Hello_:
			out = append(out, "hello")
		case *pb.OrchestratorLinkFrame_Ping_:
			out = append(out, "ping")
		case *pb.OrchestratorLinkFrame_Result_:
			out = append(out, "result")
		default:
			out = append(out, "other")
		}
	}
	return out
}

func (w *fakeWorker) recorded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func (w *fakeWorker) put(sb *pb.Sandbox) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sbs[sb.GetId()] = sb
}

func (w *fakeWorker) get(id string) *pb.Sandbox {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sbs[id]
}

// serve starts the fake on a unix socket and returns its path.
func (w *fakeWorker) serve(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "w.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, w)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

func sprintf(format string, args ...any) string { return fmtSprintf(format, args...) }
