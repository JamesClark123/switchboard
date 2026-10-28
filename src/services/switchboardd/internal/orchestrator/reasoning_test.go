package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	dial "github.com/jamesclark123/switchboard/libs/switchboard-dial"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/protobuf/proto"
)

// launchRecorder is the reasoning launcher + sandbox manager of a test: it
// creates records on the own-host fake worker and remembers every call.
type launchRecorder struct {
	w         *fakeWorker
	mu        sync.Mutex
	launches  []ReasoningLaunch
	destroyed []string
	fail      error
	n         int
}

func (r *launchRecorder) launch(_ context.Context, req ReasoningLaunch, onLog func(string)) (*pb.Sandbox, error) {
	r.mu.Lock()
	r.launches = append(r.launches, req)
	r.n++
	n, fail := r.n, r.fail
	r.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	onLog("sbx: create " + req.DisplayName)
	sb := running(sprintf("reasoning-%d", n), req.DisplayName)
	sb.Controller = proto.Clone(req.Controller).(*pb.SandboxController)
	sb.Sources = req.Setup.GetSources()
	r.w.put(proto.Clone(sb).(*pb.Sandbox)) // the fake owns its copy; sb stays ours
	sb = r.w.get(sb.GetId())               // with the revision the put stamped
	// The real manager publishes the new record on the host's stream.
	r.w.events <- &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: proto.Clone(sb).(*pb.Sandbox)}}
	return proto.Clone(sb).(*pb.Sandbox), nil
}

func (r *launchRecorder) Get(id string) (*pb.Sandbox, error) {
	if sb := r.w.get(id); sb != nil {
		return sb, nil
	}
	return nil, errors.New("not found")
}

// Destroy drops the record and emits the removal the real manager would.
func (r *launchRecorder) Destroy(_ context.Context, id string) (bool, error) {
	r.mu.Lock()
	r.destroyed = append(r.destroyed, id)
	r.mu.Unlock()
	r.w.mu.Lock()
	delete(r.w.sbs, id)
	r.w.mu.Unlock()
	r.w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: id}}}
	return true, nil
}

func (r *launchRecorder) launchesSeen() []ReasoningLaunch {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ReasoningLaunch(nil), r.launches...)
}

func (r *launchRecorder) destroyedSeen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.destroyed...)
}

// reasoningEnv is an orchestrator daemon whose own host is a fake worker
// reached in-process, with a recording launcher.
func reasoningEnv(t *testing.T) (*testEnv, *fakeWorker, *launchRecorder) {
	t.Helper()
	w := newFakeWorker("host-m")
	sock := w.serve(t)
	conn, err := dial.DialLocal(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reg, err := registry.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	rec := &launchRecorder{w: w}
	env := &testEnv{reg: reg, hub: &fakeHub{}, clock: newFakeClock(), dials: &dialRecorder{socks: map[string]string{}}}
	env.svc = NewService(Deps{
		Store: reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test",
		Local: worker.Remote(conn.API()), Sandboxes: rec, Launch: rec.launch,
	})
	if err := env.svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.svc.Stop)
	return env, w, rec
}

func summaryOf(t *testing.T, env *testEnv, id string) *pb.OrchestratorSummary {
	t.Helper()
	det, err := env.svc.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return det.GetSummary()
}

func TestReasoningSandboxLifecycle(t *testing.T) {
	env, w, rec := reasoningEnv(t)
	o, err := env.svc.Create("review-bot")
	if err != nil {
		t.Fatal(err)
	}
	id := o.GetId()
	// Create launches reasoning-<orch8> on the own host: empty setup, the
	// reasoning controller, the reasoning briefing.
	waitFor(t, func() bool { return summaryOf(t, env, id).GetReasoningReady() })
	launches := rec.launchesSeen()
	if len(launches) != 1 {
		t.Fatalf("launches = %d", len(launches))
	}
	l := launches[0]
	if l.DisplayName != "reasoning-"+id[:8] || len(l.Setup.GetSources()) != 0 || !l.Controller.GetReasoning() || l.Controller.GetName() != "review-bot" || l.Controller.GetHostId() != "host-m" || l.Controller.GetOrchestratorId() != id {
		t.Errorf("launch = %+v", l)
	}
	if !strings.Contains(l.Briefing, "**reasoning sandbox** of the switchboard orchestrator **review-bot**") {
		t.Errorf("briefing = %q", l.Briefing)
	}
	rt, _ := env.svc.runtimeFor(id)
	if r := rt.record(); r.GetReasoningSandboxId() != "reasoning-1" || len(r.GetWorkers()) != 1 || !r.GetWorkers()[0].GetReasoning() {
		t.Errorf("record = %+v", r)
	}
	det, _ := env.svc.Get(id)
	if ws := det.GetWorkers(); len(ws) != 1 || !ws[0].GetReasoning() || ws[0].GetHostId() != "host-m" {
		t.Errorf("workers view = %+v", ws)
	}

	// A prompt worker beside it; a reason action and a prompt action.
	w.put(running("wa", "worker-wa"))
	if _, err := env.svc.AttachWorker(bg(), id, "host-m", "wa", nil); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*pb.Action{
		plain(&pb.Action{Name: "think", Type: pb.ActionType_ACTION_TYPE_REASON, Body: "Think about {topic}.", Arguments: []*pb.ActionArgument{{Name: "topic", Required: true}}}),
		plain(reviewPR()),
	} {
		if _, err := putAction(env.svc, id, a); err != nil {
			t.Fatal(err)
		}
	}
	think := func() *pb.Run {
		run, err := env.svc.CallAction(id, "think", map[string]string{"topic": "x"}, pb.EventKind_EVENT_KIND_USER, "", "")
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	// A reason run goes only to the reasoning sandbox; a prompt run never does.
	r1 := think()
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(id, r1.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING && got.GetSandboxId() == "reasoning-1"
	})
	r2 := call(t, env, id, "1")
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(id, r2.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING && got.GetSandboxId() == "wa"
	})
	// Same states and completion report as a worker (SC-017).
	if _, err := rt.runTool("host-m", "reasoning-1", toolReportCompletion, `{"summary":"thought"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := env.svc.GetRun(id, r1.GetId()); got.GetState() != pb.RunState_RUN_STATE_COMPLETED || got.GetSummary() != "thought" {
		t.Errorf("reason run = %+v", got)
	}
	waitFor(t, func() bool { return w.get("reasoning-1").GetController().GetRun() == nil })

	// Not detachable.
	if err := env.svc.DetachWorker(bg(), id, "reasoning-1", nil); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "not detachable") {
		t.Errorf("detach reasoning = %v", err)
	}
	// Rebuild while a run is on it is refused.
	r3 := think()
	waitFor(t, func() bool { return stateOf(env, id, r3.GetId()) == pb.RunState_RUN_STATE_RUNNING })
	if _, err := env.svc.RebuildReasoningSandbox(bg(), id, nil, nil); !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "run "+r3.GetId()+" is on the reasoning sandbox") {
		t.Errorf("rebuild with a run on it = %v", err)
	}
	if _, err := rt.runTool("host-m", "reasoning-1", toolReportCompletion, `{"summary":"ok"}`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return w.get("reasoning-1").GetController().GetRun() == nil })

	// Rebuild with a setup: saved, destroy then create with the folder, log streamed.
	var logged []string
	setup := &pb.ReasoningSetup{Sources: []*pb.SourceRef{{Path: "/srv/proj"}}, Kits: []*pb.KitRef{{Ref: &pb.KitRef_Source{Source: "ghcr.io/acme/kit:1"}}}}
	sb, err := env.svc.RebuildReasoningSandbox(bg(), id, setup, func(line string) { logged = append(logged, line) })
	if err != nil || sb.GetId() != "reasoning-2" {
		t.Fatalf("rebuild = %+v / %v", sb, err)
	}
	if d := rec.destroyedSeen(); len(d) != 1 || d[0] != "reasoning-1" {
		t.Errorf("destroyed = %v", d)
	}
	launches = rec.launchesSeen()
	if len(launches) != 2 || len(launches[1].Setup.GetSources()) != 1 || launches[1].Setup.GetSources()[0].GetPath() != "/srv/proj" || len(launches[1].Setup.GetKits()) != 1 {
		t.Errorf("rebuild launch = %+v", launches[len(launches)-1])
	}
	if r := rt.record(); r.GetReasoningSandboxId() != "reasoning-2" || r.GetReasoningSetup().GetSources()[0].GetPath() != "/srv/proj" {
		t.Errorf("record after rebuild = %+v", r)
	}
	if strings.Join(logged, "\n") != "reasoning: building reasoning-"+id[:8]+"\nsbx: create reasoning-"+id[:8] {
		t.Errorf("log = %q", logged)
	}
	waitFor(t, func() bool {
		r := rt.record()
		return len(r.GetWorkers()) == 2 && summaryOf(t, env, id).GetReasoningReady()
	})

	// Destroyed out of band: not ready, the reason recorded, reason runs fail
	// with it until a rebuild (with the saved setup) brings it back.
	w.mu.Lock()
	delete(w.sbs, "reasoning-2")
	w.mu.Unlock()
	w.events <- &pb.Event{Event: &pb.Event_Removed{Removed: &pb.Event_SandboxRemoved{SandboxId: "reasoning-2"}}}
	waitFor(t, func() bool {
		s := summaryOf(t, env, id)
		return !s.GetReasoningReady() && s.GetReasoningError() == reasoningLostReason
	})
	r4 := think()
	waitFor(t, func() bool { return stateOf(env, id, r4.GetId()) == pb.RunState_RUN_STATE_FAILED })
	if got, _ := env.svc.GetRun(id, r4.GetId()); got.GetReason() != reasoningLostReason {
		t.Errorf("failed reason run = %+v", got)
	}
	if sb, err = env.svc.RebuildReasoningSandbox(bg(), id, nil, nil); err != nil || sb.GetId() != "reasoning-3" {
		t.Fatalf("rebuild after loss = %+v / %v", sb, err)
	}
	if launches = rec.launchesSeen(); launches[2].Setup.GetSources()[0].GetPath() != "/srv/proj" {
		t.Errorf("rebuild should reuse the saved setup: %+v", launches[2].Setup)
	}
	waitFor(t, func() bool {
		return summaryOf(t, env, id).GetReasoningReady() && summaryOf(t, env, id).GetReasoningError() == ""
	})
	r5 := think()
	waitFor(t, func() bool {
		got, _ := env.svc.GetRun(id, r5.GetId())
		return got.GetState() == pb.RunState_RUN_STATE_RUNNING && got.GetSandboxId() == "reasoning-3"
	})
	if _, err := rt.runTool("host-m", "reasoning-3", toolReportCompletion, `{"summary":"ok"}`); err != nil {
		t.Fatal(err)
	}

	// Removing the orchestrator destroys it.
	if _, err := env.svc.Remove(id); err != nil {
		t.Fatal(err)
	}
	if d := rec.destroyedSeen(); len(d) != 2 || d[1] != "reasoning-3" {
		t.Errorf("destroyed after remove = %v", d)
	}

	// A launch failure is reported on the summary, never fatal to Create.
	rec.mu.Lock()
	rec.fail = errors.New("sbx: boom")
	rec.mu.Unlock()
	o2, err := env.svc.Create("other")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return summaryOf(t, env, o2.GetId()).GetReasoningError() == "sbx: boom" })
	if summaryOf(t, env, o2.GetId()).GetReasoningReady() {
		t.Error("a failed launch is not ready")
	}
	// No launcher ⇒ a clear precondition on rebuild and on the summary.
	plain := newEnv(t)
	o3, _ := plain.svc.Create("bare")
	if s := summaryOf(t, plain, o3.GetId()); s.GetReasoningReady() || !strings.Contains(s.GetReasoningError(), "no reasoning sandbox launcher") {
		t.Errorf("no launcher summary = %+v", s)
	}
	if _, err := plain.svc.RebuildReasoningSandbox(bg(), o3.GetId(), nil, nil); !errors.Is(err, ErrPrecondition) {
		t.Errorf("rebuild without launcher = %v", err)
	}
}

func TestReasoningLostDetectedOnReconnect(t *testing.T) {
	env, w, rec := reasoningEnv(t)
	o, _ := env.svc.Create("review-bot")
	waitFor(t, func() bool { return summaryOf(t, env, o.GetId()).GetReasoningReady() })
	// The daemon restarts; meanwhile the sandbox was destroyed on the host.
	env.svc.Stop()
	w.mu.Lock()
	delete(w.sbs, "reasoning-1")
	w.mu.Unlock()
	svc2 := NewService(Deps{Store: env.reg, Hub: env.hub, HostID: "host-m", Dial: env.dials.dial, Clock: env.clock, Logf: t.Logf, DaemonVersion: "test",
		Local: env.svc.local, Sandboxes: rec, Launch: rec.launch})
	if err := svc2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc2.Stop)
	waitFor(t, func() bool {
		det, _ := svc2.Get(o.GetId())
		return !det.GetSummary().GetReasoningReady() && det.GetSummary().GetReasoningError() == reasoningLostReason
	})
	if r, _ := env.reg.GetOrchestrator(o.GetId()); r.GetReasoningSandboxId() != "" || len(r.GetWorkers()) != 0 {
		t.Errorf("record after loss = %+v", r)
	}
}
