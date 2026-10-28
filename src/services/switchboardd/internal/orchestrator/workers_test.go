package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func running(id, name string) *pb.Sandbox {
	return &pb.Sandbox{Id: id, DisplayName: name, HostId: "box", State: pb.SandboxState_SANDBOX_STATE_RUNNING, Agent: &pb.AgentSession{Status: pb.AgentStatus_AGENT_STATUS_IDLE}}
}

// remoteEnv is an orchestrator with one reachable remote host served by a fake
// worker daemon.
func remoteEnv(t *testing.T) (*testEnv, *fakeWorker, *pb.Orchestrator, *runtime) {
	t.Helper()
	env := newEnv(t)
	w := newFakeWorker("box")
	env.dials.socks["me@box"] = w.serve(t)
	o, _ := env.svc.Create("review-bot")
	rt := withHost(t, env, o.GetId(), "box", "me@box")
	waitFor(t, func() bool { ok, _ := rt.reachability("box"); return ok })
	return env, w, o, rt
}

func TestAttachDetachOverLink(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("sb-1", "worker-one"))

	// No entry for an unknown host.
	if _, err := env.svc.AttachWorker(context.Background(), o.GetId(), "nowhere", "sb-1", nil); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "no host entry for nowhere") {
		t.Errorf("attach on a missing entry = %v", err)
	}
	if _, err := env.svc.AttachWorker(context.Background(), o.GetId(), "", "", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("attach without ids = %v", err)
	}
	view, err := env.svc.AttachWorker(context.Background(), o.GetId(), "box", "sb-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if view.GetSandboxId() != "sb-1" || view.GetHostId() != "box" || view.GetAvailability() != pb.WorkerAvailability_WORKER_AVAILABILITY_FREE || !view.GetHostReachable() {
		t.Errorf("view = %+v", view)
	}
	// The remote record carries the controller; the roster has the worker.
	if c := w.get("sb-1").GetController(); c.GetOrchestratorId() != o.GetId() || c.GetName() != "review-bot" || c.GetHostId() != "host-m" {
		t.Errorf("remote controller = %+v", c)
	}
	rec, _ := env.reg.GetOrchestrator(o.GetId())
	if len(rec.GetWorkers()) != 1 || rec.GetWorkers()[0].GetSandboxId() != "sb-1" || rec.GetWorkers()[0].GetHostId() != "box" {
		t.Errorf("roster = %+v", rec.GetWorkers())
	}
	calls := w.recorded()
	if len(calls) == 0 || !strings.HasPrefix(calls[len(calls)-1], "attach sb-1 "+o.GetId()) {
		t.Errorf("remote calls = %v", calls)
	}
	// Bundle + detail.
	b, _ := env.svc.Bundle(o.GetId())
	if b.GetController().GetOrchestratorId() != o.GetId() || b.GetController().GetName() != "review-bot" || b.GetController().GetHostId() != "host-m" || !strings.Contains(b.GetBriefing(), "review-bot") {
		t.Errorf("bundle = %+v", b)
	}
	det, _ := env.svc.Get(o.GetId())
	if len(det.GetWorkers()) != 1 || det.GetWorkers()[0].GetDisplayName() != "worker-one" || det.GetSummary().GetWorkerCount() != 1 {
		t.Errorf("detail workers = %+v", det.GetWorkers())
	}
	// Another orchestrator's refusal passes through as ALREADY_EXISTS naming the controller.
	o2, _ := env.svc.Create("other")
	rt2 := withHost(t, env, o2.GetId(), "box", "me@box")
	waitFor(t, func() bool { ok, _ := rt2.reachability("box"); return ok })
	_, err = env.svc.AttachWorker(context.Background(), o2.GetId(), "box", "sb-1", nil)
	if st := status.Convert(err); st.Code() != codes.AlreadyExists || !strings.Contains(st.Message(), "controlled by review-bot") {
		t.Errorf("second orchestrator = %v", err)
	}
	// Detach refused while a run is recorded, then succeeds.
	rt.mu.Lock()
	rt.sbs["sb-1"].Controller.Run = &pb.WorkerRun{Id: "run-1", Action: "review-pr"}
	rt.mu.Unlock()
	if err := env.svc.DetachWorker(context.Background(), o.GetId(), "sb-1", nil); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "run-1") {
		t.Errorf("detach with a run = %v", err)
	}
	rt.mu.Lock()
	rt.sbs["sb-1"].Controller.Run = nil
	rt.mu.Unlock()
	if err := env.svc.DetachWorker(context.Background(), o.GetId(), "sb-1", nil); err != nil {
		t.Fatal(err)
	}
	if w.get("sb-1").GetController() != nil {
		t.Error("remote controller should be cleared")
	}
	rec, _ = env.reg.GetOrchestrator(o.GetId())
	if len(rec.GetWorkers()) != 0 {
		t.Error("roster should be empty")
	}
	if err := env.svc.DetachWorker(context.Background(), o.GetId(), "sb-1", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("detach a non-worker = %v", err)
	}
	if _, err := env.svc.Bundle("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bundle unknown = %v", err)
	}
}

func TestAttachOnUnreachableHost(t *testing.T) {
	env := newEnv(t)
	env.dials.err = errors.New("ssh: connect to host box port 22: Connection refused")
	o, _ := env.svc.Create("bot")
	withHost(t, env, o.GetId(), "box", "me@box")
	waitFor(t, func() bool { return env.dials.count() >= 1 })
	_, err := env.svc.AttachWorker(context.Background(), o.GetId(), "box", "sb-1", nil)
	var ue *UnreachableError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("attach on a down link = %v", err)
	}
}

func TestReconcileAdoptsDropsAndRenamePushes(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	// A sandbox attached out of band (launch-attach) with our id, and one that
	// is on our roster but whose record no longer names us.
	launched := running("sb-launched", "launched")
	launched.Controller = &pb.SandboxController{OrchestratorId: o.GetId(), Name: "review-bot", HostId: "host-m"}
	w.put(launched)
	w.put(running("sb-gone", "gone"))
	if _, err := rt.update(func(x *pb.Orchestrator) error {
		x.Workers = append(x.Workers, &pb.Worker{SandboxId: "sb-gone", HostId: "box"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	api, err := rt.apiFor("box")
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.reconcile(context.Background(), "box", api, true); err != nil {
		t.Fatal(err)
	}
	rec, _ := env.reg.GetOrchestrator(o.GetId())
	ids := map[string]bool{}
	for _, wk := range rec.GetWorkers() {
		ids[wk.GetSandboxId()] = true
	}
	if !ids["sb-launched"] || ids["sb-gone"] || len(ids) != 1 {
		t.Errorf("roster after reconcile = %v", ids)
	}
	if rt.sandboxRecord("sb-launched") == nil || rt.sandboxRecord("sb-gone") != nil {
		t.Error("record cache should follow the roster")
	}
	// The adopted worker received the current name + briefing.
	found := false
	for _, c := range w.recorded() {
		if strings.HasPrefix(c, `update sb-launched name="review-bot" briefing=true`) {
			found = true
		}
	}
	if !found {
		t.Errorf("reconcile should push name + briefing: %v", w.recorded())
	}
	// Rename pushes the new name into the remote record.
	if _, err := env.svc.Rename(o.GetId(), "rb2"); err != nil {
		t.Fatal(err)
	}
	// The push runs in the background (SWI-18 FR-321).
	waitFor(t, func() bool { return w.get("sb-launched").GetController().GetName() == "rb2" })
	// Status events fold into the record cache; removal drops it.
	w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: &pb.Sandbox{Id: "sb-launched", State: pb.SandboxState_SANDBOX_STATE_STOPPED, Controller: launched.Controller}}}
	waitFor(t, func() bool {
		sb := rt.sandboxRecord("sb-launched")
		return sb != nil && sb.GetState() == pb.SandboxState_SANDBOX_STATE_STOPPED
	})
	views := rt.workerViews()
	if len(views) != 1 || views[0].GetAvailability() != pb.WorkerAvailability_WORKER_AVAILABILITY_NOT_RUNNING {
		t.Errorf("views = %+v", views)
	}
	w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "sb-launched"}}}
	waitFor(t, func() bool { return rt.sandboxRecord("sb-launched") == nil })
	// A removal is terminal (SWI-18 FR-308c): a record for the id is ignored.
	if rec, _ := env.reg.GetOrchestrator(o.GetId()); !rec.GetHighWaterMarks()["sb-launched"].GetRemoved() {
		t.Errorf("removal should leave a removed mark: %v", rec.GetHighWaterMarks())
	}
	// Removing the orchestrator force-detaches reachable workers.
	again := running("sb-again", "again")
	w.put(again)
	if _, err := env.svc.AttachWorker(context.Background(), o.GetId(), "box", "sb-again", nil); err != nil {
		t.Fatal(err)
	}
	quiesce(t, rt)
	resp, err := env.svc.Remove(o.GetId())
	if err != nil || resp.GetWorkersDetached() != 1 {
		t.Errorf("remove = %+v / %v", resp, err)
	}
	if w.get("sb-again").GetController() != nil {
		t.Error("remove should detach the remote worker")
	}
	last := w.recorded()[len(w.recorded())-1]
	if last != "detach sb-again force=true" {
		t.Errorf("last remote call = %q", last)
	}
}

func TestWorkerViewsForUnknownRecords(t *testing.T) {
	env := newEnv(t)
	o, _ := env.svc.Create("bot")
	rt, _ := env.svc.runtimeFor(o.GetId())
	if _, err := rt.update(func(x *pb.Orchestrator) error {
		x.Workers = []*pb.Worker{{SandboxId: "never-seen", HostId: "box"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	views := rt.workerViews()
	if len(views) != 1 || views[0].GetAvailability() != pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE || views[0].GetHostReachable() {
		t.Errorf("unknown host views = %+v", views)
	}
	if hostOf(rt.record(), "nope") != "" || !rosterHas(rt.record(), "never-seen") {
		t.Error("roster helpers")
	}
	if remoteErr(nil, "h") != nil {
		t.Error("remoteErr(nil)")
	}
	if err := remoteErr(errors.New("plain"), "h"); err == nil || err.Error() != "plain" {
		t.Errorf("remoteErr plain = %v", err)
	}
	if err := remoteErr(statusUnimplemented(), "h"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("remoteErr too old = %v", err)
	}
}

// staleListAPI is a host whose ListSandboxes answers with the records as they
// were when it was called, then holds the reply until release is closed: a
// reconcile whose listing predates whatever the test does meanwhile.
type staleListAPI struct {
	WorkerAPI
	listed  chan struct{}
	release chan struct{}
}

func (a staleListAPI) ListSandboxes(ctx context.Context) ([]*pb.Sandbox, error) {
	sbs, err := a.WorkerAPI.ListSandboxes(ctx)
	close(a.listed)
	<-a.release
	return sbs, err
}

// reconcileAround runs a reconcile of "box" whose listing is taken before op
// and applied after it.
func reconcileAround(t *testing.T, w *fakeWorker, rt *runtime, op func()) {
	t.Helper()
	api, err := rt.apiFor("box")
	if err != nil {
		t.Fatal(err)
	}
	stale := staleListAPI{WorkerAPI: api, listed: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- rt.reconcile(bg(), "box", stale, true) }()
	<-stale.listed
	opDone := make(chan struct{})
	go func() { op(); close(opDone) }()
	// Let op reach the host (its record changes there) before the stale
	// listing is applied; op itself may then wait for the reconcile.
	waitFor(t, func() bool { return len(w.recorded()) > 0 })
	close(stale.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-opDone:
	case <-time.After(testWaitTimeout):
		t.Fatal("operation did not finish")
	}
}

// A reconcile whose listing predates an attach does not drop the new worker.
func TestReconcileDuringAttachKeepsWorker(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "worker-wa"))
	settle(t, rt)
	w.mu.Lock()
	w.calls = nil
	w.mu.Unlock()
	reconcileAround(t, w, rt, func() {
		if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
			t.Error(err)
		}
	})
	if !rosterHas(rt.record(), "wa") {
		t.Errorf("worker dropped by a stale reconcile: roster = %v", rt.record().GetWorkers())
	}
	if rt.sandboxRecord("wa") == nil {
		t.Error("worker record dropped by a stale reconcile")
	}
}

// A reconcile whose listing predates a detach does not re-adopt the worker.
func TestReconcileDuringDetachKeepsItDetached(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "worker-wa"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	settle(t, rt)
	w.mu.Lock()
	w.calls = nil
	w.mu.Unlock()
	reconcileAround(t, w, rt, func() {
		if err := env.svc.DetachWorker(bg(), o.GetId(), "wa", nil); err != nil {
			t.Error(err)
		}
	})
	if rosterHas(rt.record(), "wa") {
		t.Errorf("detached worker re-adopted by a stale reconcile: roster = %v", rt.record().GetWorkers())
	}
}

// An attach or detach reply about another sandbox than the one asked for is
// ignored and reported: replies are trusted over marks and the cache, so a
// misbehaving host must not adopt or freeze another sandbox through one
// (PR #24 review).
func TestRepliesForAnotherSandboxAreIgnored(t *testing.T) {
	env, w, o, rt := remoteEnv(t)
	w.put(running("wa", "a"))
	w.put(running("wb", "b"))
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wa", nil); err != nil {
		t.Fatal(err)
	}
	quiesce(t, rt)
	// wb's record is newer than wa's cached one, so a forged reply (wb's
	// record named wa) would replace the cache if it were applied.
	for range 3 {
		w.update("wb", func(*pb.Sandbox) {})
	}
	before := proto.Clone(rt.sandboxRecord("wa")).(*pb.Sandbox)
	w.mu.Lock()
	w.replyAs = "wa"
	w.mu.Unlock()
	if _, err := env.svc.AttachWorker(bg(), o.GetId(), "box", "wb", nil); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "another sandbox") {
		t.Errorf("attach with a foreign reply = %v", err)
	}
	if got := rt.sandboxRecord("wa"); !proto.Equal(got, before) || got.GetDisplayName() != "a" {
		t.Errorf("the reply was applied to wa's cache: %+v, want %+v", got, before)
	}
	if onRoster(rt.record(), "wb") != nil {
		t.Errorf("the reply was applied: roster %+v", rt.record().GetWorkers())
	}
	w.mu.Lock()
	w.replyAs = "wb"
	w.mu.Unlock()
	if err := env.svc.DetachWorker(bg(), o.GetId(), "wa", &pb.LaneGuardConfirm{}); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "another sandbox") {
		t.Errorf("detach with a foreign reply = %v", err)
	}
	if rt.isDetaching("wa") {
		t.Error("a refused reply left the detaching mark")
	}
}
