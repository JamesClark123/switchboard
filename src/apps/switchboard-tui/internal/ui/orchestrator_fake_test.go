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
// matching summary when nil), orchRuns (ListRuns; orchRunsByID per orchestrator
// when set), orchBundle, orchRun (the Run
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

func (f *fakeDaemon) GetWorkerBundle(_ context.Context, id, hostID string, laneIDs []string) (*pb.WorkerBundle, error) {
	if err := f.orchCall("GetWorkerBundle %s", id+laneSuffix(hostID, laneIDs)); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("GetWorkerBundle"); err != nil {
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

func (f *fakeDaemon) AttachWorker(_ context.Context, id, hostID, sandboxID string, laneIDs []string) (*pb.WorkerView, error) {
	if err := f.orchCall("AttachWorker %s %s %s%s", id, hostID, sandboxID, laneSuffix("", laneIDs)); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("AttachWorker"); err != nil {
		return nil, err
	}
	return &pb.WorkerView{SandboxId: sandboxID, HostId: hostID, Availability: pb.WorkerAvailability_WORKER_AVAILABILITY_FREE}, nil
}

func (f *fakeDaemon) DetachWorker(_ context.Context, id, sandboxID string, guard *pb.LaneGuardConfirm) error {
	if err := f.orchCall("DetachWorker %s %s%s", id, sandboxID, guardSuffix(guard)); err != nil {
		return err
	}
	return f.popLaneErr("DetachWorker")
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
	f.mu.Lock()
	closed := "run-reviewed"
	for _, sb := range f.sandboxes {
		if sb.GetId() == sandboxID && sb.GetController().GetRun() != nil {
			closed = sb.GetController().GetRun().GetId()
		}
	}
	if f.freedRunID != "" {
		closed = f.freedRunID
	}
	f.mu.Unlock()
	return &pb.Run{Id: closed, State: st, Summary: note, ClosedByDeveloper: true}, nil
}

func (f *fakeDaemon) PutAction(_ context.Context, id string, a *pb.Action, guard *pb.LaneGuardConfirm) (*pb.Action, error) {
	if err := f.orchCall("PutAction %s %s%s", id, a.GetName(), guardSuffix(guard)); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("PutAction"); err != nil {
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
	return f.runsOf(id), nil
}

// runsOf is the scripted runs of one orchestrator (caller holds mu).
func (f *fakeDaemon) runsOf(id string) *pb.ListRunsResponse {
	if r, ok := f.orchRunsByID[id]; ok {
		return r
	}
	if f.orchRuns != nil {
		return f.orchRuns
	}
	return &pb.ListRunsResponse{}
}

func (f *fakeDaemon) GetRun(_ context.Context, id, runID string) (*pb.Run, error) {
	if err := f.orchCall("GetRun %s %s", id, runID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	runs := f.runsOf(id)
	for _, group := range [][]*pb.Run{runs.GetQueued(), runs.GetInFlight(), runs.GetHistory()} {
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

// ResolveRunStep records the call (SWI-17); orchErr fails it.
func (f *fakeDaemon) ResolveRunStep(_ context.Context, id, runID string, step int32, res pb.StepResolution) (*pb.Run, error) {
	if err := f.orchCall("ResolveRunStep %s %s %d %s", id, runID, step, res); err != nil {
		return nil, err
	}
	return &pb.Run{Id: runID}, nil
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

// --- SWI-18: lanes ---

// laneSuffix renders the lane arguments a call recorded (empty without lanes).
func laneSuffix(hostID string, laneIDs []string) string {
	if len(laneIDs) == 0 {
		return ""
	}
	return " host=" + hostID + " lanes=" + strings.Join(laneIDs, ",")
}

// guardSuffix renders a guard confirmation (empty when nil).
func guardSuffix(g *pb.LaneGuardConfirm) string {
	if g == nil {
		return ""
	}
	return " guard=" + strings.Join(append(append([]string(nil), g.GetUnlanedActions()...), g.GetUnlanedRuns()...), ",")
}

// popLaneErr returns the next scripted error for a method, if any.
func (f *fakeDaemon) popLaneErr(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.laneErrs[method]
	if len(q) == 0 {
		return nil
	}
	f.laneErrs[method] = q[1:]
	return q[0]
}

// queueLaneErr scripts errors for a method's next calls.
func (f *fakeDaemon) queueLaneErr(method string, errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.laneErrs == nil {
		f.laneErrs = map[string][]error{}
	}
	f.laneErrs[method] = append(f.laneErrs[method], errs...)
}

// guardRefusal is the daemon's open-lane guard refusal with its impact.
func guardRefusal(actions ...string) error {
	st, _ := status.New(codes.FailedPrecondition, "no open-lane worker would be left for actions "+strings.Join(actions, ", ")+" — confirm to continue").
		WithDetails(&pb.LaneChangeImpact{UnlanedActions: actions})
	return st.Err()
}

// orchRecord is the record the lane calls change: the detail's when scripted,
// else the summary's (callers hold f.mu).
func (f *fakeDaemon) orchRecord(id string) *pb.Orchestrator {
	if f.orchDetail != nil && f.orchDetail.GetSummary().GetOrchestrator().GetId() == id {
		return f.orchDetail.GetSummary().GetOrchestrator()
	}
	return f.findOrch(id).GetOrchestrator()
}

// workerViewOf is a detail worker view (callers hold f.mu).
func (f *fakeDaemon) workerViewOf(id, sandboxID string) *pb.WorkerView {
	if f.orchDetail == nil || f.orchDetail.GetSummary().GetOrchestrator().GetId() != id {
		return nil
	}
	for _, w := range f.orchDetail.GetWorkers() {
		if w.GetSandboxId() == sandboxID {
			return w
		}
	}
	return nil
}

func (f *fakeDaemon) PutLane(_ context.Context, id, laneID, name, description string) (*pb.Lane, error) {
	if err := f.orchCall("PutLane %s %s %s %q", id, laneID, name, description); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("PutLane"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.orchRecord(id)
	if rec == nil {
		return nil, status.Error(codes.NotFound, "orchestrator not found")
	}
	for _, l := range rec.GetLanes() {
		if l.GetName() == name && l.GetId() != laneID {
			return nil, status.Errorf(codes.AlreadyExists, "a lane named %q already exists", name)
		}
	}
	if laneID == "" {
		f.laneSeq++
		l := &pb.Lane{Id: fmt.Sprintf("ln-%d", f.laneSeq), Name: name, Description: description}
		rec.Lanes = append(rec.Lanes, l)
		return l, nil
	}
	for _, l := range rec.GetLanes() {
		if l.GetId() == laneID {
			l.Name, l.Description = name, description
			return l, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "lane %q no longer exists", laneID)
}

func (f *fakeDaemon) DeleteLane(_ context.Context, id, laneID string, guard *pb.LaneGuardConfirm) (*pb.LaneChangeImpact, error) {
	if err := f.orchCall("DeleteLane %s %s%s", id, laneID, guardSuffix(guard)); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("DeleteLane"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	strip := func(ids []string) []string {
		var out []string
		for _, x := range ids {
			if x != laneID {
				out = append(out, x)
			}
		}
		return out
	}
	if rec := f.orchRecord(id); rec != nil {
		var kept []*pb.Lane
		for _, l := range rec.GetLanes() {
			if l.GetId() != laneID {
				kept = append(kept, l)
			}
		}
		rec.Lanes = kept
		for _, a := range rec.GetActions() {
			a.LaneIds = strip(a.GetLaneIds())
		}
		for _, w := range rec.GetWorkers() {
			w.LaneIds = strip(w.GetLaneIds())
		}
	}
	if f.orchDetail != nil {
		for _, w := range f.orchDetail.GetWorkers() {
			w.LaneIds = strip(w.GetLaneIds())
		}
	}
	if f.laneApplied != nil {
		return f.laneApplied, nil
	}
	return &pb.LaneChangeImpact{}, nil
}

func (f *fakeDaemon) SetWorkerLanes(_ context.Context, req *pb.SetWorkerLanesRequest) (*pb.WorkerView, error) {
	if err := f.orchCall("SetWorkerLanes %s %s lanes=%s expected=%s%s", req.GetOrchestratorId(), req.GetSandboxId(),
		strings.Join(req.GetLaneIds(), ","), strings.Join(req.GetExpectedLaneIds(), ","), guardSuffix(req.GetGuard())); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("SetWorkerLanes"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if v := f.workerViewOf(req.GetOrchestratorId(), req.GetSandboxId()); v != nil {
		v.LaneIds, v.LanesPending = append([]string(nil), req.GetLaneIds()...), false
		return v, nil
	}
	return &pb.WorkerView{SandboxId: req.GetSandboxId(), LaneIds: req.GetLaneIds()}, nil
}

func (f *fakeDaemon) SetLaneMembers(_ context.Context, req *pb.SetLaneMembersRequest) (*pb.Lane, error) {
	if err := f.orchCall("SetLaneMembers %s %s members=%s expected=%s%s", req.GetOrchestratorId(), req.GetLaneId(),
		strings.Join(req.GetSandboxIds(), ","), strings.Join(req.GetExpectedSandboxIds(), ","), guardSuffix(req.GetGuard())); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("SetLaneMembers"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orchDetail != nil {
		for _, w := range f.orchDetail.GetWorkers() {
			in := false
			for _, id := range req.GetSandboxIds() {
				in = in || id == w.GetSandboxId()
			}
			var lanes []string
			for _, l := range w.GetLaneIds() {
				if l != req.GetLaneId() {
					lanes = append(lanes, l)
				}
			}
			if in {
				lanes = append(lanes, req.GetLaneId())
				w.LanesPending = false
			}
			w.LaneIds = lanes
		}
	}
	return &pb.Lane{Id: req.GetLaneId()}, nil
}

func (f *fakeDaemon) CheckLaneChange(_ context.Context, req *pb.CheckLaneChangeRequest) (*pb.LaneChangeImpact, error) {
	kind := strings.TrimPrefix(fmt.Sprintf("%T", req.GetChange()), "*switchboardv1.CheckLaneChangeRequest_")
	if err := f.orchCall("CheckLaneChange %s %s", req.GetOrchestratorId(), kind); err != nil {
		return nil, err
	}
	if err := f.popLaneErr("CheckLaneChange"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.laneCheck != nil {
		return f.laneCheck, nil
	}
	return &pb.LaneChangeImpact{}, nil
}

func (f *fakeDaemon) DismissDroppedWorkerTags(_ context.Context, id string) (*pb.Orchestrator, error) {
	if err := f.orchCall("DismissDroppedWorkerTags %s", id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.orchRecord(id)
	if rec == nil {
		return nil, status.Error(codes.NotFound, "orchestrator not found")
	}
	rec.DroppedWorkerTags = nil
	return rec, nil
}
