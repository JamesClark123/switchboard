package orchestrator

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// --- fake clock (research R16) ---

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// fakeTimer's stopped/fired flags are guarded by its clock's mu.
type fakeTimer struct {
	c       *fakeClock
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
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
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
	for _, t := range due {
		t.fired = true
	}
	c.timers = rest
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, t := range due {
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
	// logHook, when set, also receives every service log line. It is installed
	// through the logger given at construction (never by reassigning svc.logf
	// while background goroutines read it) and is read under logMu.
	logMu   sync.Mutex
	logHook func(line string)
}

// setLogHook installs (or, with nil, removes) the per-test log hook.
func (e *testEnv) setLogHook(h func(line string)) {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	e.logHook = h
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
	logf := func(format string, args ...any) {
		env.logMu.Lock()
		h := env.logHook
		env.logMu.Unlock()
		if h != nil {
			h(fmt.Sprintf(format, args...))
		}
		t.Logf(format, args...)
	}
	env.svc = NewService(Deps{Store: reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: logf, DaemonVersion: "test"})
	if err := env.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.svc.Stop)
	return env
}

// testWaitTimeout bounds every polling wait in this package's tests. It only
// costs time when a test is already failing, so it is generous: it leaves
// headroom for -race, atomic coverage (-coverpkg over the whole module) and
// 2-core CI runners, where a 3 s deadline proved flaky.
const testWaitTimeout = 15 * time.Second

// quiesce waits until the runtime's background pushes (briefings, lane
// displays) are done.
func quiesce(t *testing.T, rt *runtime) {
	t.Helper()
	waitFor(t, func() bool {
		rt.brief.mu.Lock()
		busy := rt.brief.running
		rt.brief.mu.Unlock()
		rt.lanes.mu.Lock()
		for _, r := range rt.lanes.running {
			busy = busy || r
		}
		busy = busy || len(rt.lanes.retriers) > 0 // a retry is still pending
		rt.lanes.mu.Unlock()
		return !busy
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
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
	// onDeliver, when set (under mu), runs at the start of each
	// DeliverWorkerPrompt, before the fake decides; it may change records.
	onDeliver func(req *pb.DeliverWorkerPromptRequest)
	// noRevisions makes the fake a host that predates lanes: its records
	// carry no revision (SWI-18 research R16). Otherwise every write stamps
	// one, as the registry does (R3).
	noRevisions bool
	// onUpdate, when set (under mu), runs at the start of each UpdateWorker
	// with no lock held; a non-nil error is returned as the call's.
	onUpdate func(req *pb.UpdateWorkerRequest) error
	// onList, when set (under mu), runs at the start of each ListSandboxes
	// with no lock held.
	onList func()
	lists  int
	// replyAs, when set (under mu), makes attach and detach replies name this
	// sandbox id instead of the asked-for one (a misbehaving host).
	replyAs string
}

// stamp bumps a record's revision as the registry would (caller holds mu).
func (w *fakeWorker) stamp(sb *pb.Sandbox, prev uint64) {
	if w.noRevisions {
		sb.Revision = 0
		return
	}
	sb.Revision = prev + 1
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
	hook := w.onList
	w.lists++
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := &pb.ListSandboxesResponse{}
	for _, sb := range w.sbs {
		out.Sandboxes = append(out.Sandboxes, proto.Clone(sb).(*pb.Sandbox))
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
	sb.Controller = proto.Clone(req.GetController()).(*pb.SandboxController)
	w.stamp(sb, sb.GetRevision())
	return w.reply(sb), nil
}

// reply is an attach or detach reply: sb, renamed to replyAs when set.
func (w *fakeWorker) reply(sb *pb.Sandbox) *pb.Sandbox {
	out := proto.Clone(sb).(*pb.Sandbox)
	if w.replyAs != "" {
		out.Id = w.replyAs
	}
	return out
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
	w.stamp(sb, sb.GetRevision())
	return w.reply(sb), nil
}

func (w *fakeWorker) UpdateWorker(_ context.Context, req *pb.UpdateWorkerRequest) (*pb.Sandbox, error) {
	w.record("update %s name=%q briefing=%v", req.GetSandboxId(), req.GetName(), req.Briefing != nil)
	w.mu.Lock()
	hook := w.onUpdate
	w.mu.Unlock()
	if hook != nil {
		if err := hook(req); err != nil {
			return nil, err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	if sb.Controller == nil {
		return nil, status.Error(codes.PermissionDenied, "not a worker")
	}
	// As the real worker side (R7): expect_run_id is a compare-and-set key.
	if e := req.GetExpectRunId(); e != "" && sb.GetController().GetRun().GetId() != e {
		return nil, status.Error(codes.FailedPrecondition, "run moved")
	}
	if req.Name != nil {
		sb.Controller.Name = req.GetName()
	}
	if req.GetSetRun() {
		sb.Controller.Run = req.GetRun()
	}
	if req.GetClearNeedsInput() {
		if sb.Controller.Run != nil {
			return nil, status.Error(codes.FailedPrecondition, "run still on this sandbox")
		}
		if sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT {
			sb.Agent.Status = pb.AgentStatus_AGENT_STATUS_IDLE
		}
	}
	if req.GetLanes() != nil {
		sb.Controller.LaneNames = req.GetLanes().GetNames()
		sb.Controller.LanesPending = req.GetLanes().GetPending()
		sb.Controller.LanesKnown = true
	}
	w.stamp(sb, sb.GetRevision())
	return proto.Clone(sb).(*pb.Sandbox), nil
}

// DeliverWorkerStep: this fake predates step delivery (SWI-17).
func (w *fakeWorker) DeliverWorkerStep(context.Context, *pb.DeliverWorkerStepRequest) (*pb.DeliverWorkerStepResponse, error) {
	return nil, status.Error(codes.Unimplemented, "DeliverWorkerStep")
}

func (w *fakeWorker) DeliverWorkerPrompt(_ context.Context, req *pb.DeliverWorkerPromptRequest) (*pb.DeliverWorkerPromptResponse, error) {
	w.record("deliver %s %s", req.GetSandboxId(), req.GetRunId())
	w.mu.Lock()
	hook := w.onDeliver
	w.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[req.GetSandboxId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "sandbox not found")
	}
	// As the real worker side (worker.Service.Deliver): only a worker of the
	// calling orchestrator, running, idle and without a run takes a delivery.
	if c := sb.GetController(); c == nil || c.GetOrchestratorId() != req.GetOrchestratorId() {
		return nil, status.Error(codes.FailedPrecondition, "not a worker of this orchestrator")
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE || sb.GetController().GetRun() != nil {
		return nil, status.Error(codes.FailedPrecondition, "not free")
	}
	sb.Controller.Run = &pb.WorkerRun{Id: req.GetRunId(), Action: req.GetAction()}
	w.stamp(sb, sb.GetRevision())
	return &pb.DeliverWorkerPromptResponse{Accepted: true, Revision: sb.GetRevision()}, nil
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

// listCount reports how many ListSandboxes calls the fake served.
func (w *fakeWorker) listCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lists
}

// countPrefix counts recorded calls starting with prefix.
func (w *fakeWorker) countPrefix(prefix string) int {
	n := 0
	for _, c := range w.recorded() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (w *fakeWorker) recorded() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func (w *fakeWorker) put(sb *pb.Sandbox) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stamp(sb, w.sbs[sb.GetId()].GetRevision())
	w.sbs[sb.GetId()] = sb
}

// get returns a snapshot of a stored record (nil when absent); change one
// through update, never through a snapshot.
func (w *fakeWorker) get(id string) *pb.Sandbox {
	w.mu.Lock()
	defer w.mu.Unlock()
	sb, ok := w.sbs[id]
	if !ok {
		return nil
	}
	return proto.Clone(sb).(*pb.Sandbox)
}

// update changes a stored record under the lock and returns a snapshot of it.
func (w *fakeWorker) update(id string, fn func(sb *pb.Sandbox)) *pb.Sandbox {
	w.mu.Lock()
	defer w.mu.Unlock()
	sb := w.sbs[id]
	prev := sb.GetRevision()
	fn(sb)
	w.stamp(sb, prev)
	return proto.Clone(sb).(*pb.Sandbox)
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

// putAction saves an action as a lanes-aware client does: the guard's current
// list is fetched with CheckLaneChange and confirmed.
func putAction(svc *Service, id string, a *pb.Action) (*pb.Action, error) {
	req := &pb.PutActionRequest{OrchestratorId: id, Action: a, SetsLanes: true}
	if impact, err := svc.CheckLaneChange(&pb.CheckLaneChangeRequest{OrchestratorId: id, Change: &pb.CheckLaneChangeRequest_PutAction{PutAction: req}}); err == nil {
		req.Guard = &pb.LaneGuardConfirm{UnlanedActions: impact.GetUnlanedActions(), UnlanedRuns: impact.GetUnlanedRuns()}
	}
	return svc.PutAction(req)
}

// seedLanes creates lanes by name and puts the listed roster workers in them
// (tests seed lanes directly rather than through the lane RPCs). It returns
// the lane ids by name.
func seedLanes(t *testing.T, rt *runtime, members map[string][]string) map[string]string {
	t.Helper()
	ids := map[string]string{}
	if _, err := rt.update(func(o *pb.Orchestrator) error {
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			l := laneByName(o, name)
			if l == nil {
				l = &pb.Lane{Id: newLaneID(), Name: name}
				o.Lanes = append(o.Lanes, l)
			}
			ids[name] = l.GetId()
			for _, id := range members[name] {
				w := onRosterEntry(o, id)
				if w == nil {
					return fmt.Errorf("seedLanes: %s is not on the roster", id)
				}
				if !slices.Contains(w.GetLaneIds(), l.GetId()) {
					w.LaneIds = append(w.LaneIds, l.GetId())
				}
				w.LanesPending = false
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rt.triggerDispatch()
	return ids
}
