package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestCreateListRenameRemove(t *testing.T) {
	env := newEnv(t)
	svc := env.svc

	o, err := svc.Create("  review-bot ")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.GetId()) != 36 || o.GetName() != "review-bot" || o.GetHostId() != "host-m" || o.GetCreatedAt() == nil {
		t.Errorf("created = %+v", o)
	}
	stored, err := env.reg.GetOrchestrator(o.GetId())
	if err != nil || stored.GetName() != "review-bot" {
		t.Fatalf("persisted = %v / %v", stored, err)
	}
	if env.hub.changed(o.GetId()) != 1 {
		t.Errorf("create should emit one orchestrator_changed, got %d", env.hub.changed(o.GetId()))
	}

	// Duplicate name on this daemon.
	if _, err := svc.Create("review-bot"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate = %v, want ErrNameTaken", err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 65), "bad\x01name"} {
		if _, err := svc.Create(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%q) = %v, want ErrInvalid", bad, err)
		}
	}

	// List: derived counts.
	list, err := svc.List()
	if err != nil || len(list) != 1 || list[0].GetOrchestrator().GetId() != o.GetId() || list[0].GetWorkerCount() != 0 {
		t.Fatalf("List = %v / %v", list, err)
	}

	// Rename changes only the name.
	before := proto.Clone(stored).(*pb.Orchestrator)
	renamed, err := svc.Rename(o.GetId(), "review-bot-2")
	if err != nil || renamed.GetName() != "review-bot-2" {
		t.Fatalf("Rename = %v / %v", renamed, err)
	}
	before.Name = "review-bot-2"
	after, _ := env.reg.GetOrchestrator(o.GetId())
	if !proto.Equal(before, after) {
		t.Errorf("rename changed more than the name:\n%v\n%v", before, after)
	}
	if _, err := svc.Rename("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename unknown = %v", err)
	}
	o2, _ := svc.Create("other")
	if _, err := svc.Rename(o2.GetId(), "review-bot-2"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename onto a taken name = %v", err)
	}
	if _, err := svc.Rename(o2.GetId(), ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("rename to empty = %v", err)
	}

	// Get: detail with hosts (none) and workers (none).
	det, err := svc.Get(o.GetId())
	if err != nil || det.GetSummary().GetOrchestrator().GetName() != "review-bot-2" || len(det.GetHosts()) != 0 {
		t.Fatalf("Get = %v / %v", det, err)
	}
	if _, err := svc.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown = %v", err)
	}

	// Remove deletes the record and every run key.
	for i := 0; i < 3; i++ {
		seq, _ := env.reg.NextRunSeq(o.GetId())
		_ = env.reg.PutRun(&pb.Run{Id: runID(o.GetId(), seq), OrchestratorId: o.GetId(), State: pb.RunState_RUN_STATE_COMPLETED})
	}
	resp, err := svc.Remove(o.GetId())
	if err != nil || resp == nil {
		t.Fatalf("Remove = %v / %v", resp, err)
	}
	if _, err := env.reg.GetOrchestrator(o.GetId()); err == nil {
		t.Error("record should be deleted")
	}
	if runs, _ := env.reg.ListRuns(o.GetId()); len(runs) != 0 {
		t.Errorf("runs should be deleted, got %d", len(runs))
	}
	if _, err := svc.Remove(o.GetId()); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice = %v", err)
	}
	// The name is free again.
	if _, err := svc.Create("review-bot-2"); err != nil {
		t.Errorf("name should be free after remove: %v", err)
	}
	if n := env.hub.changed(""); n < 5 {
		t.Errorf("every mutation emits orchestrator_changed; got %d", n)
	}
}

func TestSetHostsValidationAndWorkersGuard(t *testing.T) {
	env := newEnv(t)
	svc := env.svc
	o, _ := svc.Create("bot")

	cases := map[string][]*pb.OrchestratorHost{
		"empty id":     {{SshTarget: "x"}},
		"own host":     {{HostId: "host-m", SshTarget: "x"}},
		"dup":          {{HostId: "a", SshTarget: "x"}, {HostId: "a", SshTarget: "y"}},
		"no target":    {{HostId: "a"}},
		"blank target": {{HostId: "a", SshTarget: "  "}},
	}
	for name, hosts := range cases {
		if _, err := svc.SetHosts(o.GetId(), hosts); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	rec, err := svc.SetHosts(o.GetId(), []*pb.OrchestratorHost{{HostId: "box", DisplayName: " build-box ", SshTarget: " me@box ", SshOptions: []string{"-p", "2222"}}})
	if err != nil || len(rec.GetHosts()) != 1 || rec.GetHosts()[0].GetSshTarget() != "me@box" || rec.GetHosts()[0].GetDisplayName() != "build-box" {
		t.Fatalf("SetHosts = %v / %v", rec, err)
	}
	// Reachability of a host whose dial fails shows the dial's words.
	waitFor(t, func() bool { return env.dials.count() >= 1 })
	det, _ := svc.Get(o.GetId())
	if len(det.GetHosts()) != 1 || det.GetHosts()[0].GetReachable() {
		t.Fatalf("host should be unreachable: %+v", det.GetHosts())
	}
	waitFor(t, func() bool {
		d, _ := svc.Get(o.GetId())
		return strings.Contains(d.GetHosts()[0].GetError(), "Connection refused")
	})

	// A worker on the host blocks its removal, naming the worker.
	rt, _ := svc.runtimeFor(o.GetId())
	if _, err := rt.update(func(x *pb.Orchestrator) error {
		x.Workers = append(x.Workers, &pb.Worker{SandboxId: "sb-on-box", HostId: "box"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.SetHosts(o.GetId(), nil)
	if !errors.Is(err, ErrPrecondition) || !strings.Contains(err.Error(), "sb-on-box") {
		t.Errorf("removing a host with workers = %v", err)
	}
	// Workers on the own host never block.
	if _, err := rt.update(func(x *pb.Orchestrator) error {
		x.Workers = []*pb.Worker{{SandboxId: "sb-local", HostId: "host-m"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetHosts(o.GetId(), nil); err != nil {
		t.Errorf("removing an unused host = %v", err)
	}
	if _, err := svc.SetHosts("nope", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetHosts unknown = %v", err)
	}
	// The link for the removed entry is gone.
	if l := rt.link("box"); l != nil {
		t.Error("removed host should have no link")
	}
	if ok, reason := rt.reachability("box"); ok || reason != "no host entry" {
		t.Errorf("reachability of a missing entry = %v %q", ok, reason)
	}
}

func TestStartLoadsRecordsAndListCounts(t *testing.T) {
	reg := newEnv(t).reg // a registry with its own service; we add records directly
	for _, name := range []string{"a-bot", "b-bot"} {
		if err := reg.PutOrchestrator(&pb.Orchestrator{Id: "id-" + name, Name: name, HostId: "host-m",
			Workers: []*pb.Worker{{SandboxId: "w1", HostId: "host-m"}, {SandboxId: "r", HostId: "host-m", Reasoning: true}}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, st := range []pb.RunState{pb.RunState_RUN_STATE_QUEUED, pb.RunState_RUN_STATE_QUEUED, pb.RunState_RUN_STATE_RUNNING, pb.RunState_RUN_STATE_AWAITING_REVIEW, pb.RunState_RUN_STATE_COMPLETED} {
		_ = reg.PutRun(&pb.Run{Id: runID("id-a-bot", uint64(i+1)), OrchestratorId: "id-a-bot", State: st})
	}
	hub := &fakeHub{}
	svc := NewService(Deps{Store: reg, Hub: hub, HostID: "host-m", Clock: newFakeClock(), Logf: t.Logf})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer svc.Stop()
	if err := svc.Start(context.Background()); err != nil {
		t.Errorf("Start twice is a no-op: %v", err)
	}
	svc.mu.Lock()
	n := len(svc.runtimes)
	svc.mu.Unlock()
	if n != 2 {
		t.Fatalf("runtimes = %d, want 2", n)
	}
	list, err := svc.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %v / %v", list, err)
	}
	a := list[0]
	if a.GetOrchestrator().GetName() != "a-bot" || a.GetWorkerCount() != 1 || a.GetQueued() != 2 || a.GetInFlight() != 2 || a.GetAwaitingReview() != 1 {
		t.Errorf("a-bot summary = %+v", a)
	}
	// The own host link reports the worker side as unconfigured (no Local).
	rt, _ := svc.runtimeFor("id-a-bot")
	waitFor(t, func() bool { ok, reason := rt.reachability("host-m"); return !ok && reason != "" })
	views := rt.workerViews()
	if len(views) != 2 || views[0].GetAvailability() != pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE || !views[1].GetReasoning() {
		t.Errorf("workerViews = %+v", views)
	}
	// Stop twice is safe.
	svc.Stop()
}

func TestAvailabilityTable(t *testing.T) {
	running := func(agent pb.AgentStatus, run *pb.WorkerRun) *pb.Sandbox {
		return &pb.Sandbox{State: pb.SandboxState_SANDBOX_STATE_RUNNING, Agent: &pb.AgentSession{Status: agent}, Controller: &pb.SandboxController{Run: run}}
	}
	cases := []struct {
		sb   *pb.Sandbox
		up   bool
		want pb.WorkerAvailability
	}{
		{running(pb.AgentStatus_AGENT_STATUS_IDLE, nil), false, pb.WorkerAvailability_WORKER_AVAILABILITY_UNREACHABLE},
		{&pb.Sandbox{State: pb.SandboxState_SANDBOX_STATE_STOPPED}, true, pb.WorkerAvailability_WORKER_AVAILABILITY_NOT_RUNNING},
		{running(pb.AgentStatus_AGENT_STATUS_IDLE, &pb.WorkerRun{Id: "r", NeedsReview: true}), true, pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_REVIEW},
		{running(pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT, nil), true, pb.WorkerAvailability_WORKER_AVAILABILITY_AWAITING_INPUT},
		{running(pb.AgentStatus_AGENT_STATUS_WORKING, nil), true, pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY},
		{running(pb.AgentStatus_AGENT_STATUS_WAITING, &pb.WorkerRun{Id: "r"}), true, pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY},
		{running(pb.AgentStatus_AGENT_STATUS_WAITING, nil), true, pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY},
		{running(pb.AgentStatus_AGENT_STATUS_IDLE, &pb.WorkerRun{Id: "r"}), true, pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY},
		{running(pb.AgentStatus_AGENT_STATUS_EXITED, nil), true, pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY},
		{running(pb.AgentStatus_AGENT_STATUS_STARTING, nil), true, pb.WorkerAvailability_WORKER_AVAILABILITY_BUSY},
		{running(pb.AgentStatus_AGENT_STATUS_IDLE, nil), true, pb.WorkerAvailability_WORKER_AVAILABILITY_FREE},
	}
	for i, c := range cases {
		if got := availability(c.sb, c.up); got != c.want {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}

func TestTrimHistoryKeepsNewestFinishedOnly(t *testing.T) {
	env := newEnv(t)
	o, _ := env.svc.Create("bot")
	for i := 1; i <= historyKeep+5; i++ {
		st := pb.RunState_RUN_STATE_COMPLETED
		if i%50 == 0 {
			st = pb.RunState_RUN_STATE_QUEUED
		}
		_ = env.reg.PutRun(&pb.Run{Id: runID(o.GetId(), uint64(i)), OrchestratorId: o.GetId(), State: st})
	}
	if err := env.svc.trimHistory(o.GetId()); err != nil {
		t.Fatal(err)
	}
	runs, _ := env.reg.ListRuns(o.GetId())
	finishedN, queuedN := 0, 0
	for _, r := range runs {
		if finished(r) {
			finishedN++
		} else {
			queuedN++
		}
	}
	if finishedN != historyKeep || queuedN != 4 {
		t.Errorf("after trim: finished %d queued %d", finishedN, queuedN)
	}
	// The oldest finished runs went (run 1..5 minus the queued ones).
	if runs[0].GetId() == runID(o.GetId(), 1) {
		t.Error("oldest finished run should be trimmed")
	}
}
