package ui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- feature 009 fakes: the orchestrator surface of fakeDaemon ---
//
// Scripted fields live on fakeDaemon (launch_test.go): orchestrators (the rows
// ListOrchestrators returns), orchDetail (GetOrchestrator; built from the
// matching summary when nil), orchRuns (ListRuns), orchBundle, orchRun (the Run
// CallAction/MarkWorkerFree/CancelRun return when set), orchErr (returned by
// every call when set) and orchCalls, which records "Method args" so a test can
// assert exactly what reached the daemon.

func (f *fakeDaemon) orchCall(format string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orchCalls = append(f.orchCalls, fmt.Sprintf(format, args...))
	return f.orchErr
}

// orchCallsJoined is the recorded calls, one per line (for assertions).
func (f *fakeDaemon) orchCallsJoined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.orchCalls, "\n")
}

func (f *fakeDaemon) findOrch(id string) *pb.OrchestratorSummary {
	for _, s := range f.orchestrators {
		if s.GetOrchestrator().GetId() == id {
			return s
		}
	}
	return nil
}

func (f *fakeDaemon) ListOrchestrators(context.Context) ([]*pb.OrchestratorSummary, error) {
	if err := f.orchCall("ListOrchestrators"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.OrchestratorSummary(nil), f.orchestrators...), nil
}

func (f *fakeDaemon) GetOrchestrator(_ context.Context, id string) (*pb.OrchestratorDetail, error) {
	if err := f.orchCall("GetOrchestrator %s", id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orchDetail != nil && f.orchDetail.GetSummary().GetOrchestrator().GetId() == id {
		return f.orchDetail, nil
	}
	s := f.findOrch(id)
	if s == nil {
		return nil, status.Error(codes.NotFound, "orchestrator not found")
	}
	return &pb.OrchestratorDetail{Summary: s, Hosts: s.GetOrchestrator().GetHosts()}, nil
}

func (f *fakeDaemon) CreateOrchestrator(_ context.Context, name string) (*pb.Orchestrator, error) {
	if err := f.orchCall("CreateOrchestrator %s", name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.orchestrators {
		if s.GetOrchestrator().GetName() == name {
			return nil, status.Errorf(codes.AlreadyExists, "an orchestrator named %q already exists on this host", name)
		}
	}
	o := &pb.Orchestrator{Id: "orch-" + name, Name: name, HostId: f.HostID()}
	f.orchestrators = append(f.orchestrators, &pb.OrchestratorSummary{Orchestrator: o})
	return o, nil
}

func (f *fakeDaemon) RenameOrchestrator(_ context.Context, id, name string) (*pb.Orchestrator, error) {
	if err := f.orchCall("RenameOrchestrator %s %s", id, name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.findOrch(id)
	if s == nil {
		return nil, status.Error(codes.NotFound, "orchestrator not found")
	}
	s.Orchestrator.Name = name
	return s.GetOrchestrator(), nil
}

func (f *fakeDaemon) RemoveOrchestrator(_ context.Context, id string) (*pb.RemoveOrchestratorResponse, error) {
	if err := f.orchCall("RemoveOrchestrator %s", id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.orchestrators[:0]
	for _, s := range f.orchestrators {
		if s.GetOrchestrator().GetId() != id {
			kept = append(kept, s)
		}
	}
	f.orchestrators = kept
	return &pb.RemoveOrchestratorResponse{}, nil
}

func (f *fakeDaemon) SetOrchestratorHosts(_ context.Context, id string, hosts []*pb.OrchestratorHost) (*pb.Orchestrator, error) {
	ids := make([]string, 0, len(hosts))
	for _, h := range hosts {
		ids = append(ids, h.GetHostId())
	}
	if err := f.orchCall("SetOrchestratorHosts %s %s", id, strings.Join(ids, ",")); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.findOrch(id)
	if s == nil {
		return nil, status.Error(codes.NotFound, "orchestrator not found")
	}
	s.Orchestrator.Hosts = hosts
	if f.orchDetail != nil && f.orchDetail.GetSummary().GetOrchestrator().GetId() == id {
		f.orchDetail.Hosts = hosts
	}
	return s.GetOrchestrator(), nil
}

func (f *fakeDaemon) GetWorkerBundle(_ context.Context, id string) (*pb.WorkerBundle, error) {
	if err := f.orchCall("GetWorkerBundle %s", id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orchBundle != nil {
		return f.orchBundle, nil
	}
	s := f.findOrch(id)
	if s == nil {
		return nil, status.Error(codes.NotFound, "orchestrator not found")
	}
	return &pb.WorkerBundle{Controller: &pb.SandboxController{OrchestratorId: id, Name: s.GetOrchestrator().GetName(), HostId: f.HostID()}, Briefing: "briefing"}, nil
}

func (f *fakeDaemon) AttachWorker(_ context.Context, id, hostID, sandboxID string) (*pb.WorkerView, error) {
	if err := f.orchCall("AttachWorker %s %s %s", id, hostID, sandboxID); err != nil {
		return nil, err
	}
	return &pb.WorkerView{SandboxId: sandboxID, HostId: hostID, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_FREE}, nil
}

func (f *fakeDaemon) DetachWorker(_ context.Context, id, sandboxID string) error {
	return f.orchCall("DetachWorker %s %s", id, sandboxID)
}

func (f *fakeDaemon) DetachController(_ context.Context, sandboxID, orchestratorID string, force bool) (*pb.Sandbox, error) {
	if err := f.orchCall("DetachController %s %s %v", sandboxID, orchestratorID, force); err != nil {
		return nil, err
	}
	return &pb.Sandbox{Id: sandboxID}, nil
}

func (f *fakeDaemon) MarkWorkerFree(_ context.Context, id, sandboxID string, failed bool, note string) (*pb.Run, error) {
	if err := f.orchCall("MarkWorkerFree %s %s %v %q", id, sandboxID, failed, note); err != nil {
		return nil, err
	}
	st := pb.RunState_RUN_STATE_COMPLETED
	if failed {
		st = pb.RunState_RUN_STATE_FAILED
	}
	return &pb.Run{Id: "run-reviewed", State: st, Summary: note, ClosedByDeveloper: true}, nil
}

func (f *fakeDaemon) PutAction(_ context.Context, id string, a *pb.Action) (*pb.Action, error) {
	if err := f.orchCall("PutAction %s %s", id, a.GetName()); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putActions = append(f.putActions, a)
	if s := f.findOrch(id); s != nil {
		replaced := false
		for i, ex := range s.Orchestrator.Actions {
			if ex.GetName() == a.GetName() {
				s.Orchestrator.Actions[i] = a
				replaced = true
			}
		}
		if !replaced {
			s.Orchestrator.Actions = append(s.Orchestrator.Actions, a)
		}
	}
	return a, nil
}

func (f *fakeDaemon) DeleteAction(_ context.Context, id, name string) ([]string, error) {
	if err := f.orchCall("DeleteAction %s %s", id, name); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.findOrch(id); s != nil {
		kept := s.Orchestrator.Actions[:0]
		for _, ex := range s.Orchestrator.Actions {
			if ex.GetName() != name {
				kept = append(kept, ex)
			}
		}
		s.Orchestrator.Actions = kept
	}
	return f.disabledEvents, nil
}

func (f *fakeDaemon) PutTimedEvent(_ context.Context, id string, ev *pb.TimedEvent) (*pb.TimedEvent, error) {
	if err := f.orchCall("PutTimedEvent %s %s dedupe_disabled=%v enabled=%v interval=%d", id, ev.GetName(), ev.GetDedupeDisabled(), ev.GetEnabled(), ev.GetIntervalSeconds()); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putEvents = append(f.putEvents, ev)
	if ev.GetId() == "" {
		ev.Id = fmt.Sprintf("tev-%d", len(f.putEvents))
	}
	if s := f.findOrch(id); s != nil {
		replaced := false
		for i, ex := range s.Orchestrator.TimedEvents {
			if ex.GetId() == ev.GetId() {
				s.Orchestrator.TimedEvents[i] = ev
				replaced = true
			}
		}
		if !replaced {
			s.Orchestrator.TimedEvents = append(s.Orchestrator.TimedEvents, ev)
		}
	}
	return ev, nil
}

func (f *fakeDaemon) DeleteTimedEvent(_ context.Context, id, eventID string) error {
	if err := f.orchCall("DeleteTimedEvent %s %s", id, eventID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.findOrch(id); s != nil {
		kept := s.Orchestrator.TimedEvents[:0]
		for _, ex := range s.Orchestrator.TimedEvents {
			if ex.GetId() != eventID {
				kept = append(kept, ex)
			}
		}
		s.Orchestrator.TimedEvents = kept
	}
	return nil
}

func (f *fakeDaemon) FireTimedEvent(_ context.Context, id, eventID string) (*pb.Run, error) {
	if err := f.orchCall("FireTimedEvent %s %s", id, eventID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orchRun != nil {
		return f.orchRun, nil
	}
	return &pb.Run{Id: "run-9", OrchestratorId: id, Origin: eventID, EventKind: pb.EventKind_EVENT_KIND_TIMED, State: pb.RunState_RUN_STATE_QUEUED, Position: 2}, nil
}

func (f *fakeDaemon) CallAction(_ context.Context, id, action string, args map[string]string) (*pb.Run, error) {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var kv []string
	for _, k := range keys {
		kv = append(kv, k+"="+args[k])
	}
	if err := f.orchCall("CallAction %s %s %s", id, action, strings.Join(kv, ",")); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orchRun != nil {
		return f.orchRun, nil
	}
	return &pb.Run{Id: "run-1", OrchestratorId: id, Action: action, Arguments: args, State: pb.RunState_RUN_STATE_QUEUED, Position: 1}, nil
}

func (f *fakeDaemon) ListRuns(_ context.Context, id string) (*pb.ListRunsResponse, error) {
	if err := f.orchCall("ListRuns %s", id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orchRuns != nil {
		return f.orchRuns, nil
	}
	return &pb.ListRunsResponse{}, nil
}

func (f *fakeDaemon) GetRun(_ context.Context, id, runID string) (*pb.Run, error) {
	if err := f.orchCall("GetRun %s %s", id, runID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, group := range [][]*pb.Run{f.orchRuns.GetQueued(), f.orchRuns.GetInFlight(), f.orchRuns.GetHistory()} {
		for _, r := range group {
			if r.GetId() == runID {
				return r, nil
			}
		}
	}
	return &pb.Run{Id: runID, Prompt: "composed prompt for " + runID}, nil
}

func (f *fakeDaemon) CancelRun(_ context.Context, id, runID string) (*pb.Run, error) {
	if err := f.orchCall("CancelRun %s %s", id, runID); err != nil {
		return nil, err
	}
	return &pb.Run{Id: runID, State: pb.RunState_RUN_STATE_CANCELLED}, nil
}

func (f *fakeDaemon) RebuildReasoningSandbox(_ context.Context, id string, setup *pb.ReasoningSetup, onLog func(client.LaunchUpdate)) (*pb.Sandbox, error) {
	var srcs []string
	for _, s := range setup.GetSources() {
		srcs = append(srcs, s.GetPath())
	}
	if err := f.orchCall("RebuildReasoningSandbox %s sources=%s kits=%d", id, strings.Join(srcs, ","), len(setup.GetKits())); err != nil {
		return nil, err
	}
	f.stream(onLog)
	return &pb.Sandbox{Id: "reasoning-sb", DisplayName: "reasoning-" + short(id), State: pb.SandboxState_SANDBOX_STATE_RUNNING}, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// runAll executes a (possibly batched) tea.Cmd synchronously, flattening nested
// batches, and returns every message produced.
func runAll(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, runAll(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// feed runs cmd and feeds every produced message back into the model,
// discarding the commands those updates return.
func feed(m Model, cmd tea.Cmd) Model {
	for _, msg := range runAll(cmd) {
		m, _ = update(m, msg)
	}
	return m
}

// feed2 is feed one level deeper: the commands returned by the first round of
// updates are run too and their messages fed back, while the commands THOSE
// updates return are discarded. It is what opens per-host subscriptions from a
// host snapshot without blocking on the receivers they arm.
func feed2(m Model, cmd tea.Cmd) Model {
	for _, msg := range runAll(cmd) {
		var next tea.Cmd
		m, next = update(m, msg)
		m = feed(m, next)
	}
	return m
}
