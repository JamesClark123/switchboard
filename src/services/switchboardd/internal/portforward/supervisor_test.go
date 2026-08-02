package portforward

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// --- test doubles -----------------------------------------------------------

type fakeLookup struct {
	mu        sync.Mutex
	sandboxes map[string]*pb.Sandbox
}

func newFakeLookup(sbs ...*pb.Sandbox) *fakeLookup {
	m := map[string]*pb.Sandbox{}
	for _, sb := range sbs {
		m[sb.GetId()] = sb
	}
	return &fakeLookup{sandboxes: m}
}

func (f *fakeLookup) Get(id string) (*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sb, ok := f.sandboxes[id]
	if !ok {
		return nil, ErrUnknownSandbox
	}
	return sb, nil
}

func (f *fakeLookup) set(sb *pb.Sandbox) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sandboxes[sb.GetId()] = sb
}

type recordedNotification struct {
	sandboxID string
	kind      pb.NotificationKind
	message   string
}

type fakeEmitter struct {
	mu      sync.Mutex
	events  []*pb.ServiceInstance
	notifs  []recordedNotification
	changed chan struct{}
}

func newFakeEmitter() *fakeEmitter {
	return &fakeEmitter{changed: make(chan struct{}, 128)}
}

func (e *fakeEmitter) PublishServiceInstance(inst *pb.ServiceInstance) {
	e.mu.Lock()
	e.events = append(e.events, inst)
	e.mu.Unlock()
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

func (e *fakeEmitter) EmitNotification(sandboxID string, kind pb.NotificationKind, message string, _ time.Time) *pb.NotificationEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifs = append(e.notifs, recordedNotification{sandboxID: sandboxID, kind: kind, message: message})
	return &pb.NotificationEvent{SandboxId: sandboxID, Kind: kind, Message: message}
}

func (e *fakeEmitter) states() []pb.ServiceState {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]pb.ServiceState, 0, len(e.events))
	for _, ev := range e.events {
		out = append(out, ev.GetState())
	}
	return out
}

func (e *fakeEmitter) eventCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events)
}

func (e *fakeEmitter) notifications() []recordedNotification {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]recordedNotification, len(e.notifs))
	copy(out, e.notifs)
	return out
}

// fakeRunner runs "in-sandbox" commands on the host — there is no sandbox in unit
// tests — and records the port publishes it was asked for.
type fakeRunner struct {
	mu          sync.Mutex
	published   [][2]uint32
	unpublished [][2]uint32
	execArgv    [][]string
	publishErr  error
	forwarders  map[uint32]net.Listener
}

// PublishPort stands in for `sbx ports --publish` by actually forwarding hostPort
// to 127.0.0.1:sandboxPort.
//
// A no-op fake would make every in-sandbox readiness probe fail, which would test
// nothing: the probe's whole job is to dial the PUBLISHED port. Since the fake
// Runner already runs "in-sandbox" commands on the host, a real loopback forwarder
// reproduces the mapping faithfully.
func (r *fakeRunner) PublishPort(_ context.Context, _ string, hostPort, sandboxPort uint32) error {
	r.mu.Lock()
	if r.publishErr != nil {
		err := r.publishErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", hostPort))
	if err != nil {
		return err
	}
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = in.Close() }()
				out, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", sandboxPort))
				if err != nil {
					return
				}
				defer func() { _ = out.Close() }()
				go func() { _, _ = io.Copy(out, in) }()
				_, _ = io.Copy(in, out)
			}()
		}
	}()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forwarders == nil {
		r.forwarders = map[uint32]net.Listener{}
	}
	r.forwarders[hostPort] = ln
	r.published = append(r.published, [2]uint32{hostPort, sandboxPort})
	return nil
}

func (r *fakeRunner) UnpublishPort(_ context.Context, _ string, hostPort, sandboxPort uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ln, ok := r.forwarders[hostPort]; ok {
		_ = ln.Close()
		delete(r.forwarders, hostPort)
	}
	r.unpublished = append(r.unpublished, [2]uint32{hostPort, sandboxPort})
	return nil
}

func (r *fakeRunner) Exec(ctx context.Context, _ string, argv []string) *exec.Cmd {
	r.mu.Lock()
	r.execArgv = append(r.execArgv, argv)
	r.mu.Unlock()
	if len(argv) == 0 {
		argv = []string{"true"}
	}
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

func (r *fakeRunner) publishes() [][2]uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][2]uint32, len(r.published))
	copy(out, r.published)
	return out
}

func (r *fakeRunner) unpublishes() [][2]uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][2]uint32, len(r.unpublished))
	copy(out, r.unpublished)
	return out
}

// launchCount counts only the exec calls that started a service, ignoring the
// /proc readiness probes that also go through Runner.Exec.
func (r *fakeRunner) launchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, argv := range r.execArgv {
		if len(argv) > 2 && strings.Contains(argv[2], pgidMarker) {
			n++
		}
	}
	return n
}

func (r *fakeRunner) lastExec() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.execArgv) == 0 {
		return nil
	}
	return r.execArgv[len(r.execArgv)-1]
}

// newTestSupervisor wires a supervisor over the fakes, with short windows so tests
// do not wait on the 60s/10s production defaults.
func newTestSupervisor(t *testing.T, sbs ...*pb.Sandbox) (*Supervisor, *fakeLookup, *fakeRunner, *fakeEmitter) {
	t.Helper()
	lookup := newFakeLookup(sbs...)
	runner := &fakeRunner{}
	emitter := newFakeEmitter()
	s := NewSupervisor(lookup, runner, emitter)
	s.SetReadinessTimeout(2 * time.Second)
	s.SetStopGrace(500 * time.Millisecond)
	return s, lookup, runner, emitter
}

func runningSandbox(id string, services ...*pb.KitService) *pb.Sandbox {
	return &pb.Sandbox{
		Id:            id,
		State:         pb.SandboxState_SANDBOX_STATE_RUNNING,
		ContainerRef:  id + "-ref",
		WorkspacePath: "/tmp",
		Services:      services,
	}
}

// --- tests ------------------------------------------------------------------

func TestListJoinsDeclaredServicesWithInstances(t *testing.T) {
	sb := runningSandbox("sb1",
		svc("web", "pnpm dev", 3000, inSandbox),
		svc("api", "go run .", 8080, onHost),
	)
	s, _, _, _ := newTestSupervisor(t, sb)

	rows, err := s.List("sb1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.GetInstance() != nil {
			t.Errorf("service %q must have no instance before it is started (FR-045)", row.GetDeclared().GetName())
		}
	}

	// Once an instance exists it joins onto its declaration.
	inst, _ := s.insts.Create("sb1", "web")
	rows, _ = s.List("sb1")
	if rows[0].GetInstance().GetId() != inst.GetId() {
		t.Error("the current instance must join onto its declared row")
	}
	if rows[1].GetInstance() != nil {
		t.Error("an unstarted service must stay instance-less")
	}
}

func TestListRejectsUnknownSandbox(t *testing.T) {
	s, _, _, _ := newTestSupervisor(t)
	if _, err := s.List("nope"); err == nil {
		t.Error("an unknown sandbox must be rejected")
	}
}

func TestEachTransitionPublishesExactlyOneEvent(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "pnpm dev", 3000, inSandbox))
	s, _, _, emitter := newTestSupervisor(t, sb)

	inst, _ := s.insts.Create("sb1", "web")
	if emitter.eventCount() != 0 {
		t.Fatal("creating an instance must not publish; the supervisor's transition does")
	}

	s.transition(inst.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_RUNNING })
	s.transition(inst.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_STOPPED })

	if got := emitter.eventCount(); got != 2 {
		t.Errorf("events = %d, want exactly one per transition", got)
	}
	states := emitter.states()
	if states[0] != pb.ServiceState_SERVICE_STATE_RUNNING || states[1] != pb.ServiceState_SERVICE_STATE_STOPPED {
		t.Errorf("published states = %v", states)
	}
}

func TestOnlyFailureRaisesANotification(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "pnpm dev", 3000, inSandbox))
	s, _, _, emitter := newTestSupervisor(t, sb)
	inst, _ := s.insts.Create("sb1", "web")

	// A successful start and a developer-initiated stop are silent — echoing back an
	// action the developer just took would train them to ignore the inbox.
	s.transition(inst.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_RUNNING })
	s.transition(inst.GetId(), func(i *pb.ServiceInstance) { i.State = pb.ServiceState_SERVICE_STATE_STOPPED })
	if got := len(emitter.notifications()); got != 0 {
		t.Fatalf("notifications after start+stop = %d, want 0", got)
	}

	s.markFailed(inst.GetId(), pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_UNEXPECTEDLY, "exit status 1")

	notifs := emitter.notifications()
	if len(notifs) != 1 {
		t.Fatalf("notifications after failure = %d, want 1", len(notifs))
	}
	if notifs[0].kind != pb.NotificationKind_NOTIFICATION_KIND_SERVICE_FAILED {
		t.Errorf("kind = %v, want SERVICE_FAILED", notifs[0].kind)
	}
	if notifs[0].sandboxID != "sb1" {
		t.Errorf("sandbox id = %q, want sb1", notifs[0].sandboxID)
	}
	// The developer reading this is by definition looking at something else, so the
	// message has to identify the service and say what went wrong.
	if !strings.Contains(notifs[0].message, "web") || !strings.Contains(notifs[0].message, "exited unexpectedly") {
		t.Errorf("message = %q, want it to name the service and the reason", notifs[0].message)
	}
}

func TestMarkFailedClearsTheLocalPort(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "pnpm dev", 3000, inSandbox))
	s, _, _, _ := newTestSupervisor(t, sb)
	inst, _ := s.insts.Create("sb1", "web")
	s.insts.Update(inst.GetId(), func(i *pb.ServiceInstance) { i.LocalPort = 49221 })

	failed := s.markFailed(inst.GetId(), pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING, "no listener")
	if failed.GetLocalPort() != 0 {
		t.Error("a failed service must release its local port (FR-048, SC-004)")
	}
	if failed.GetEndedAt() == nil {
		t.Error("a terminal instance must be stamped")
	}
}

func TestReleaseUnpublishesTheExactTriple(t *testing.T) {
	sb := runningSandbox("sb1", svc("web", "pnpm dev", 3000, inSandbox))
	s, _, runner, _ := newTestSupervisor(t, sb)
	inst, _ := s.insts.Create("sb1", "web")

	s.mu.Lock()
	s.handles[inst.GetId()] = &procHandle{ref: "sb1-ref", hostPort: 51234, sbxPort: 3000, inSbx: true}
	s.mu.Unlock()

	s.release(inst.GetId())

	un := runner.unpublishes()
	if len(un) != 1 || un[0] != [2]uint32{51234, 3000} {
		t.Errorf("unpublish = %v, want the exact published triple 51234:3000", un)
	}
	if _, ok := s.handleFor(inst.GetId()); ok {
		t.Error("release must drop the handle")
	}
	// Idempotent: the crash path and the stop path both reach it.
	s.release(inst.GetId())
	if got := len(runner.unpublishes()); got != 1 {
		t.Errorf("release must be idempotent, got %d unpublishes", got)
	}
}

func TestTransitionOnUnknownInstanceIsANoOp(t *testing.T) {
	s, _, _, emitter := newTestSupervisor(t)
	if got := s.transition("svc-999", func(*pb.ServiceInstance) {}); got != nil {
		t.Error("an unknown instance must not transition")
	}
	if emitter.eventCount() != 0 {
		t.Error("an unknown instance must publish nothing")
	}
}

func TestReasonLabelsCoverEveryFailureReason(t *testing.T) {
	// Every reason must have a human label — an inbox entry saying "failed" with no
	// cause is exactly what FR-051 forbids.
	reasons := []pb.ServiceFailureReason{
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_PORT_IN_USE,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NOT_LISTENING_LOOPBACK,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_EARLY,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_EXITED_UNEXPECTEDLY,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_SANDBOX_NOT_RUNNING,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_NO_LOCAL_PORT,
		pb.ServiceFailureReason_SERVICE_FAILURE_REASON_HOST_UNREACHABLE,
	}
	for _, r := range reasons {
		if label := reasonLabel(r); label == "" || label == "failed" {
			t.Errorf("reason %v has no specific label", r)
		}
	}
	if reasonLabel(pb.ServiceFailureReason_SERVICE_FAILURE_REASON_UNSPECIFIED) != "failed" {
		t.Error("the unspecified reason should fall back to a generic label")
	}
}

func TestFailureMessageUsesOnlyTheFirstDetailLine(t *testing.T) {
	inst := &pb.ServiceInstance{
		ServiceName:   "web",
		FailureReason: pb.ServiceFailureReason_SERVICE_FAILURE_REASON_LAUNCH_FAILED,
		FailureDetail: "exec: \"pnpm\": not found\nstack trace line\nanother line",
	}
	msg := failureMessage(inst)
	if strings.Contains(msg, "stack trace") {
		t.Errorf("a notification must stay one line, got %q", msg)
	}
	if !strings.Contains(msg, "not found") {
		t.Errorf("the first detail line must survive, got %q", msg)
	}
}
