package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runs (data-model "Run"): one call of an action, persisted in receipt order
// (R9), moved through the state machine by the dispatcher, the worker's status
// events, completion reports and the developer (R7).

// runState guards every read-modify-write of the run bucket for one
// orchestrator and the transient per-run state beside it.
type runState struct {
	mu sync.Mutex
}

// newRunID renders run-<orchestrator8>-<seq>.
func newRunID(orchID string, seq uint64) string {
	short := orchID
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("run-%s-%d", short, seq)
}

// CallAction validates and composes a call, queues the run and returns it with
// its position (FR-129/FR-120). kind/origin say who called (FR-128).
func (s *Service) CallAction(id, action string, args map[string]string, kind pb.EventKind, origin, originLabel string) (*pb.Run, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	return rt.callAction(action, args, kind, origin, originLabel)
}

func (rt *runtime) callAction(action string, args map[string]string, kind pb.EventKind, origin, originLabel string) (*pb.Run, error) {
	if args == nil {
		args = map[string]string{}
	}
	// The action is read and the run queued in one transaction, so a run never
	// names an action deleted before it was written (SWI-18 research R7).
	var run *pb.Run
	var pos uint32
	if _, err := rt.updateRuns(func(o *pb.Orchestrator, runs RunTx) error {
		a := findAction(o, action)
		if a == nil {
			return fmt.Errorf("%w: action %s", ErrNotFound, action)
		}
		if err := validateCall(a, args); err != nil {
			return err
		}
		prompt, err := compose(a, args)
		if err != nil {
			return err
		}
		queued, err := runs.Queued()
		if err != nil {
			return err
		}
		seq, err := runs.NextSeq()
		if err != nil {
			return err
		}
		run = &pb.Run{
			Id: newRunID(rt.id, seq), OrchestratorId: rt.id, Action: a.GetName(), Type: a.GetType(),
			Arguments: args, Prompt: prompt, EventKind: kind, Origin: origin, OriginLabel: originLabel,
			ReceivedAt: timestamppb.New(rt.svc.clock.Now()), State: pb.RunState_RUN_STATE_QUEUED,
		}
		// The position is taken before dispatch can pick the run up, so the
		// caller sees where it queued (>= 1) rather than 0 when a free worker
		// takes it at once.
		pos = uint32(len(queued)) + 1
		return runs.Put(run)
	}); err != nil {
		return nil, err
	}
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
	out := proto.Clone(run).(*pb.Run)
	out.Position = pos
	return out, nil
}

// position is a queued run's 1-based position among queued runs.
func (rt *runtime) position(runID string) uint32 {
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return 0
	}
	var pos uint32
	for _, r := range runs {
		if r.GetState() != pb.RunState_RUN_STATE_QUEUED {
			continue
		}
		pos++
		if r.GetId() == runID {
			return pos
		}
	}
	return 0
}

// ListRuns returns the three Queue-view groups (FR-131), queued prompt runs
// with their waiting reasons (FR-315).
func (s *Service) ListRuns(id string) (*pb.ListRunsResponse, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	counts := rt.laneCounts()
	runs, err := s.reg.ListRuns(id)
	if err != nil {
		return nil, err
	}
	out := &pb.ListRunsResponse{}
	var pos uint32
	for _, r := range runs {
		switch {
		case r.GetState() == pb.RunState_RUN_STATE_QUEUED:
			pos++
			r.Position = pos
			r.WaitingReason = waitingReason(r, counts, false)
			out.Queued = append(out.Queued, r)
		case inFlight(r):
			out.InFlight = append(out.InFlight, r)
		default:
			out.History = append(out.History, r)
		}
	}
	// History is newest first.
	for i, j := 0, len(out.History)-1; i < j; i, j = i+1, j-1 {
		out.History[i], out.History[j] = out.History[j], out.History[i]
	}
	return out, nil
}

// GetRun returns one run with its composed prompt.
func (s *Service) GetRun(id, runID string) (*pb.Run, error) {
	if _, err := s.runtimeFor(id); err != nil {
		return nil, err
	}
	run, err := s.reg.GetRun(id, runID)
	if err != nil {
		return nil, notFound(err)
	}
	if run.GetState() == pb.RunState_RUN_STATE_QUEUED {
		rt, _ := s.runtimeFor(id)
		run.Position = rt.position(runID)
		run.WaitingReason = waitingReason(run, rt.laneCounts(), false)
	}
	return run, nil
}

// CancelRun cancels a QUEUED run (FR-126); any other state is refused.
func (s *Service) CancelRun(id, runID string) (*pb.Run, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	run, err := s.reg.GetRun(id, runID)
	if err != nil {
		return nil, notFound(err)
	}
	if run.GetState() != pb.RunState_RUN_STATE_QUEUED {
		return nil, fmt.Errorf("%w: run %s is %s; only a queued run can be cancelled", ErrPrecondition, runID, stateLabel(run.GetState()))
	}
	run.State = pb.RunState_RUN_STATE_CANCELLED
	run.Reason = "cancelled by developer"
	run.EndedAt = timestamppb.New(s.clock.Now())
	if err := rt.putRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

// MarkWorkerFree closes the run AWAITING_REVIEW on a worker as COMPLETED
// (default) or FAILED with the developer's note as summary (FR-156). A worker
// with no run on it whose agent is recorded NEEDS_INPUT is instead set back to
// IDLE on its owning daemon — the developer's override for an agent parked by a
// prompt nobody is going to answer — and an empty run is returned.
func (s *Service) MarkWorkerFree(id, sandboxID string, failed bool, note string) (*pb.Run, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	rt.runs.mu.Lock()
	run := rt.runOnLocked(sandboxID)
	rt.runs.mu.Unlock()
	if run != nil && run.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW {
		return rt.completeRun(run, note, true, failed)
	}
	if run == nil {
		if sb := rt.sandboxRecord(sandboxID); sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT && sb.GetController().GetRun() == nil {
			return &pb.Run{}, rt.clearNeedsInput(hostOf(rt.record(), sandboxID), sandboxID)
		}
	}
	if run != nil && inFlight(run) {
		// Typically acting on a review notification whose run resumed since
		// (SWI-12): say so rather than that there is nothing to review.
		return nil, fmt.Errorf("%w: run %s on sandbox %s resumed and is %s again — no review needed", ErrPrecondition, run.GetId(), sandboxID, stateLabel(run.GetState()))
	}
	return nil, fmt.Errorf("%w: no run awaiting review on sandbox %s", ErrPrecondition, sandboxID)
}

// clearNeedsInput asks a worker's owning daemon to set its parked agent IDLE.
func (rt *runtime) clearNeedsInput(hostID, sandboxID string) error {
	api, err := rt.apiFor(hostID)
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx(unaryTimeout)
	defer cancel()
	sb, err := api.UpdateWorker(ctx, &pb.UpdateWorkerRequest{SandboxId: sandboxID, OrchestratorId: rt.id, ClearNeedsInput: true})
	if err != nil {
		return err
	}
	rt.mu.Lock()
	rt.cacheLocked(sb)
	rt.mu.Unlock()
	rt.svc.emitChanged(rt.id)
	rt.triggerDispatch()
	return nil
}

// stateLabel renders a run state in lower snake case ("awaiting_review").
func stateLabel(st pb.RunState) string {
	return strings.ToLower(strings.TrimPrefix(st.String(), "RUN_STATE_"))
}

// --- transitions (callers hold no lock unless noted) ---

// putRun persists a run and announces the change.
func (rt *runtime) putRun(run *pb.Run) error {
	run.WaitingReason = "" // derived on read, never stored (FR-315)
	if err := rt.svc.reg.PutRun(run); err != nil {
		return err
	}
	rt.svc.emitChanged(rt.id)
	return nil
}

// runOnLocked returns the in-flight run on a sandbox (caller holds runs.mu).
func (rt *runtime) runOnLocked(sandboxID string) *pb.Run {
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return nil
	}
	for _, r := range runs {
		if inFlight(r) && r.GetSandboxId() == sandboxID {
			return r
		}
	}
	return nil
}

// runOn is runOnLocked under the lock.
func (rt *runtime) runOn(sandboxID string) *pb.Run {
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	return rt.runOnLocked(sandboxID)
}

// notify raises a run notification against the worker's sandbox (FR-133).
func (rt *runtime) notify(run *pb.Run, kind pb.NotificationKind, what string) {
	if rt.svc.hub == nil {
		return
	}
	msg := fmt.Sprintf("orchestrator %s: run %s (%s) %s", rt.name(), run.GetId(), run.GetAction(), what)
	rt.svc.hub.EmitNotification(run.GetSandboxId(), kind, msg, rt.svc.clock.Now())
}

// startRun marks a delivered run RUNNING on its sandbox.
func (rt *runtime) startRun(run *pb.Run, sandboxID, hostID string) error {
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	run.State = pb.RunState_RUN_STATE_RUNNING
	run.StartedAt = timestamppb.New(rt.svc.clock.Now())
	run.SandboxId = sandboxID
	run.SandboxHostId = hostID
	return rt.putRun(run)
}

// runNeedsInput folds the agent's NEEDS_INPUT into the run (once per entry).
func (rt *runtime) runNeedsInput(run *pb.Run) {
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	if run.GetState() != pb.RunState_RUN_STATE_RUNNING {
		return
	}
	run.State = pb.RunState_RUN_STATE_AWAITING_INPUT
	if err := rt.putRun(run); err != nil {
		return
	}
	rt.mu.Lock()
	first := !rt.inputNotified[run.GetId()]
	rt.inputNotified[run.GetId()] = true
	rt.mu.Unlock()
	if first {
		rt.notify(run, pb.NotificationKind_NOTIFICATION_KIND_RUN_NEEDS_INPUT, "needs input")
	}
}

// runResumed folds the agent working again (or waiting on its own background
// work) into the run. A run handed to review comes back too (SWI-12): an agent
// that resumes was not finished, it had paused — so the run is RUNNING again
// and the worker's needs_review flag is cleared.
func (rt *runtime) runResumed(run *pb.Run) {
	rt.runs.mu.Lock()
	// The live record, not the caller's copy: a report or a mark free may have
	// closed the run since it was read, and must not be undone.
	live, err := rt.svc.reg.GetRun(rt.id, run.GetId())
	from := live.GetState()
	if err != nil || (from != pb.RunState_RUN_STATE_AWAITING_INPUT && from != pb.RunState_RUN_STATE_AWAITING_REVIEW) {
		rt.runs.mu.Unlock()
		return
	}
	live.State = pb.RunState_RUN_STATE_RUNNING
	err = rt.putRun(live)
	rt.runs.mu.Unlock()
	if err != nil {
		return
	}
	run.State = live.GetState()
	rt.mu.Lock()
	delete(rt.inputNotified, run.GetId())
	rt.mu.Unlock()
	if from == pb.RunState_RUN_STATE_AWAITING_REVIEW {
		rt.syncReviewFlag(run.GetId())
	}
}

// workerPushLock is the worker's lock for controller.run pushes (pushMu).
func (rt *runtime) workerPushLock(sandboxID string) *sync.Mutex {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.pushMu == nil {
		rt.pushMu = map[string]*sync.Mutex{}
	}
	l, ok := rt.pushMu[sandboxID]
	if !ok {
		l = &sync.Mutex{}
		rt.pushMu[sandboxID] = l
	}
	return l
}

// clearWorkerRun pushes the clearing of a closed run's controller.run, under
// the worker's push lock so a review-flag push computed before the close can
// never land after it and leave the closed run on the worker.
func (rt *runtime) clearWorkerRun(run *pb.Run) {
	l := rt.workerPushLock(run.GetSandboxId())
	l.Lock()
	defer l.Unlock()
	rt.setWorkerRun(run.GetSandboxHostId(), run.GetSandboxId(), nil)
}

// syncReviewFlag pushes a worker's controller.run with needs_review set exactly
// when its in-flight run is AWAITING_REVIEW, read live under the worker's push
// lock so that of racing pushes (review, resume, close) the last always matches
// the run.
func (rt *runtime) syncReviewFlag(runID string) {
	first, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil || first.GetSandboxId() == "" {
		return
	}
	l := rt.workerPushLock(first.GetSandboxId())
	l.Lock()
	defer l.Unlock()
	live, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil || !inFlight(live) || live.GetSandboxId() == "" {
		return
	}
	rt.setWorkerRun(live.GetSandboxHostId(), live.GetSandboxId(), &pb.WorkerRun{
		Id: live.GetId(), Action: live.GetAction(), NeedsReview: live.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW,
	})
}

// repairReviewFlag re-pushes the worker's needs_review flag when its record
// disagrees with the run (a push lost while the link was down, or one that
// landed out of order) — the record the link last delivered is the evidence.
func (rt *runtime) repairReviewFlag(run *pb.Run, sb *pb.Sandbox) {
	wr := sb.GetController().GetRun()
	if wr.GetId() != run.GetId() {
		return
	}
	live, err := rt.svc.reg.GetRun(rt.id, run.GetId())
	if err != nil || !inFlight(live) {
		return
	}
	if wr.GetNeedsReview() != (live.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW) {
		rt.syncReviewFlag(run.GetId())
	}
}

// runAwaitingReview hands an idle, unreported run to the developer (FR-156):
// the worker's record is flagged needs_review and a notification raised.
func (rt *runtime) runAwaitingReview(run *pb.Run) {
	rt.runs.mu.Lock()
	// Decided on the live record and the worker's latest status, both under
	// runs.mu like runResumed: an agent that resumed (SWI-12) or a report that
	// closed the run since the caller looked keeps it out of review.
	live, err := rt.svc.reg.GetRun(rt.id, run.GetId())
	if err != nil || (live.GetState() != pb.RunState_RUN_STATE_RUNNING && live.GetState() != pb.RunState_RUN_STATE_AWAITING_INPUT) ||
		rt.sandboxRecord(live.GetSandboxId()).GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		rt.runs.mu.Unlock()
		return
	}
	live.State = pb.RunState_RUN_STATE_AWAITING_REVIEW
	err = rt.putRun(live)
	rt.runs.mu.Unlock()
	if err != nil {
		return
	}
	run.State = live.GetState()
	rt.syncReviewFlag(run.GetId())
	if cur, err := rt.svc.reg.GetRun(rt.id, run.GetId()); err != nil || cur.GetState() != pb.RunState_RUN_STATE_AWAITING_REVIEW {
		return // resumed or closed meanwhile: no stale alert
	}
	rt.notify(live, pb.NotificationKind_NOTIFICATION_KIND_RUN_AWAITING_REVIEW, "finished without a completion report — review the worker and mark it free")
}

// completeRun closes a run with its one summary (a report, or the developer's
// note) and frees the worker. Completions are silent (FR-133).
func (rt *runtime) completeRun(run *pb.Run, summary string, byDeveloper, failed bool) (*pb.Run, error) {
	rt.runs.mu.Lock()
	if !inFlight(run) {
		rt.runs.mu.Unlock()
		return nil, fmt.Errorf("%w: run %s is %s", ErrPrecondition, run.GetId(), stateLabel(run.GetState()))
	}
	run.State = pb.RunState_RUN_STATE_COMPLETED
	if failed {
		run.State = pb.RunState_RUN_STATE_FAILED
		run.Reason = "marked failed by developer"
	}
	run.Summary = summary
	run.ClosedByDeveloper = byDeveloper
	run.EndedAt = timestamppb.New(rt.svc.clock.Now())
	err := rt.putRun(run)
	rt.runs.mu.Unlock()
	if err != nil {
		return nil, err
	}
	rt.closedRun(run)
	rt.clearWorkerRun(run)
	return proto.Clone(run).(*pb.Run), nil
}

// failRun closes a run as FAILED with a reason (FR-124) and raises RUN_FAILED.
func (rt *runtime) failRun(run *pb.Run, reason string) {
	rt.runs.mu.Lock()
	if !inFlight(run) && run.GetState() != pb.RunState_RUN_STATE_QUEUED {
		rt.runs.mu.Unlock()
		return
	}
	run.State = pb.RunState_RUN_STATE_FAILED
	run.Reason = reason
	run.EndedAt = timestamppb.New(rt.svc.clock.Now())
	err := rt.putRun(run)
	rt.runs.mu.Unlock()
	if err != nil {
		return
	}
	rt.closedRun(run)
	if run.GetSandboxId() != "" && rt.sandboxRecord(run.GetSandboxId()) != nil {
		rt.clearWorkerRun(run)
	}
	rt.notify(run, pb.NotificationKind_NOTIFICATION_KIND_RUN_FAILED, "failed: "+reason)
}

// closedRun drops the transient state of a run that just closed, trims the
// history and asks for a dispatch pass (a worker may be free).
func (rt *runtime) closedRun(run *pb.Run) {
	rt.mu.Lock()
	if t, ok := rt.reviewTimers[run.GetId()]; ok {
		t.Stop()
		delete(rt.reviewTimers, run.GetId())
	}
	delete(rt.reported, run.GetId())
	delete(rt.inputNotified, run.GetId())
	rt.mu.Unlock()
	if err := rt.svc.trimHistory(rt.id); err != nil {
		rt.svc.logf("[orchestrator %s] trim history: %v", rt.name(), err)
	}
	rt.triggerDispatch()
}

// setWorkerRun pushes the run flag (or its clearing) onto the worker's record
// through its host link; a down link is retried by the next reconcile.
func (rt *runtime) setWorkerRun(hostID, sandboxID string, run *pb.WorkerRun) {
	if sandboxID == "" {
		return
	}
	api, err := rt.apiFor(hostID)
	if err != nil {
		rt.svc.logf("[orchestrator %s] update worker %s on %s: %v", rt.name(), sandboxID, hostID, err)
		return
	}
	ctx, cancel := rt.callCtx(unaryTimeout)
	defer cancel()
	sb, err := api.UpdateWorker(ctx, &pb.UpdateWorkerRequest{SandboxId: sandboxID, OrchestratorId: rt.id, SetRun: true, Run: run})
	if err != nil {
		rt.svc.logf("[orchestrator %s] update worker %s on %s: %v", rt.name(), sandboxID, hostID, err)
		return
	}
	rt.mu.Lock()
	rt.cacheLocked(sb)
	rt.mu.Unlock()
	if run == nil {
		// The worker may be free now; don't rely on its host's status event.
		rt.triggerDispatch()
	}
}

// failRunsOn fails every in-flight run on a sandbox (worker detached, removed).
func (rt *runtime) failRunsOn(sandboxID, reason string) {
	for {
		run := rt.runOn(sandboxID)
		if run == nil {
			return
		}
		rt.failRun(run, reason)
	}
}

// closeAllRuns fails in-flight and cancels queued runs (orchestrator removal).
func (rt *runtime) closeAllRuns(reason string) (failed, cancelled uint32) {
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return 0, 0
	}
	for _, r := range runs {
		switch {
		case inFlight(r):
			rt.failRun(r, reason)
			failed++
		case r.GetState() == pb.RunState_RUN_STATE_QUEUED:
			rt.runs.mu.Lock()
			r.State = pb.RunState_RUN_STATE_CANCELLED
			r.Reason = reason
			r.EndedAt = timestamppb.New(rt.svc.clock.Now())
			_ = rt.svc.reg.PutRun(r)
			rt.runs.mu.Unlock()
			cancelled++
		}
	}
	return failed, cancelled
}

// --- status folding (research R7) ---

// onSandboxEvent folds a host's sandbox event into the runtime: the latest
// record per relevant sandbox, roster adoption and drops (through
// applyRecords, research R4), and the in-flight run on it.
func (rt *runtime) onSandboxEvent(_ context.Context, hostID string, ev *pb.Event) {
	switch e := ev.GetEvent().(type) {
	case *pb.Event_SandboxChanged:
		sb := e.SandboxChanged
		if sb.GetState() == pb.SandboxState_SANDBOX_STATE_DESTROYING {
			rt.onSandboxRemoved(hostID, sb.GetId())
			return
		}
		if !rt.observe(hostID, sb) {
			return
		}
		if cur := rt.sandboxRecord(sb.GetId()); cur != nil {
			rt.foldStatus(cur)
		}
		rt.triggerDispatch()
	case *pb.Event_Removed:
		rt.onSandboxRemoved(hostID, e.Removed.GetSandboxId())
	}
}

// observe caches an event's record when it concerns this orchestrator and,
// when applying it would change the roster, applies it. Status-only events
// touch only the cache, under mu: they never wait on a record write.
func (rt *runtime) observe(hostID string, sb *pb.Sandbox) bool {
	in := rosterInput{source: srcEvent, hostID: hostID, records: []*pb.Sandbox{sb}}
	rt.mu.Lock()
	if !rt.relevantLocked(rt.rec, sb) || foreignTo(rt.rec, sb.GetId(), hostID) {
		rt.mu.Unlock()
		return false
	}
	rt.cacheLocked(sb)
	in.cache = rt.snapshotCacheLocked(in.rosterIDs())
	dry := applyRecords(proto.Clone(rt.rec).(*pb.Orchestrator), in, rt.svc.clock.Now())
	rt.mu.Unlock()
	if dry.changed {
		if _, err := rt.applyRoster(in); err != nil {
			rt.svc.logf("[orchestrator %s] apply event for %s: %v", rt.name(), sb.GetId(), err)
		}
	}
	return true
}

// onSandboxRemoved applies a removal: terminal for a roster sandbox (runs on
// it fail, the entry and cache go, a removed mark stops re-adoption).
func (rt *runtime) onSandboxRemoved(hostID, sandboxID string) {
	rt.mu.Lock()
	_, cached := rt.sbs[sandboxID]
	tracked := cached || hostOf(rt.rec, sandboxID) != "" || rt.rec.GetHighWaterMarks()[sandboxID] != nil
	rt.mu.Unlock()
	if !tracked {
		return // an unrelated sandbox: nothing to write
	}
	if _, err := rt.applyRoster(rosterInput{source: srcRemoval, hostID: hostID, ids: []string{sandboxID}}); err != nil {
		rt.svc.logf("[orchestrator %s] apply removal of %s: %v", rt.name(), sandboxID, err)
	}
}

// foldStatus maps a worker's record onto the run on it (data-model state machine).
func (rt *runtime) foldStatus(sb *pb.Sandbox) {
	run := rt.runOn(sb.GetId())
	if run == nil {
		return
	}
	switch {
	case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_EXITED:
		rt.failRun(run, "agent exited")
	case sb.GetState() == pb.SandboxState_SANDBOX_STATE_ERROR || sb.GetState() == pb.SandboxState_SANDBOX_STATE_STOPPED:
		if run.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW {
			return // stopped while awaiting review keeps the run (FR-156 edge case)
		}
		rt.failRun(run, "sandbox "+strings.ToLower(strings.TrimPrefix(sb.GetState().String(), "SANDBOX_STATE_")))
	case sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
		// CREATING (a kit attach): the agent session is gone; treat as stopped.
		if run.GetState() != pb.RunState_RUN_STATE_AWAITING_REVIEW {
			rt.failRun(run, "sandbox restarting")
		}
	case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
		rt.disarmReview(run.GetId())
		rt.runNeedsInput(run)
	case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_WORKING,
		sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_WAITING:
		// WAITING: the turn ended to wait on background work the agent started
		// (SWI-12). The run is still going — no review is armed.
		rt.disarmReview(run.GetId())
		rt.runResumed(run)
		rt.repairReviewFlag(run, sb)
	case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_IDLE:
		if run.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW {
			return
		}
		rt.armReview(run)
	}
}

// armReview starts the review grace for an idle, unreported run (once).
func (rt *runtime) armReview(run *pb.Run) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.reported[run.GetId()] {
		return
	}
	if _, armed := rt.reviewTimers[run.GetId()]; armed {
		return
	}
	id := run.GetId()
	rt.reviewTimers[id] = rt.svc.clock.AfterFunc(reviewGrace, func() { rt.reviewExpired(id) })
}

// disarmReview cancels a pending review grace (the agent is busy again).
func (rt *runtime) disarmReview(runID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if t, ok := rt.reviewTimers[runID]; ok {
		t.Stop()
		delete(rt.reviewTimers, runID)
	}
}

// reviewExpired runs when the grace elapsed: still idle and unreported ⇒ review.
func (rt *runtime) reviewExpired(runID string) {
	rt.mu.Lock()
	delete(rt.reviewTimers, runID)
	reported := rt.reported[runID]
	rt.mu.Unlock()
	if reported || rt.ctx.Err() != nil {
		return
	}
	run, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil || !inFlight(run) {
		return
	}
	sb := rt.sandboxRecord(run.GetSandboxId())
	if sb == nil || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		return
	}
	rt.runAwaitingReview(run)
}

// reconcileInFlight re-checks every in-flight run on a host after its link came
// up (FR-134): nothing is ever re-delivered, only the state is re-read.
func (rt *runtime) reconcileInFlight(hostID string) {
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return
	}
	for _, run := range runs {
		if !inFlight(run) || run.GetSandboxHostId() != hostID {
			continue
		}
		sb := rt.sandboxRecord(run.GetSandboxId())
		switch {
		case sb == nil:
			rt.failRun(run, "sandbox gone")
		case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_EXITED:
			rt.failRun(run, "agent exited")
		case run.GetState() == pb.RunState_RUN_STATE_AWAITING_REVIEW:
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING && busy(sb) {
				rt.runResumed(run) // the agent resumed while the link was down
			}
		case sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING:
			rt.failRun(run, "sandbox "+strings.ToLower(strings.TrimPrefix(sb.GetState().String(), "SANDBOX_STATE_")))
		case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_IDLE:
			rt.mu.Lock()
			reported := rt.reported[run.GetId()]
			rt.mu.Unlock()
			if !reported {
				rt.runAwaitingReview(run)
			}
		case sb.GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
			rt.runNeedsInput(run)
		case busy(sb):
			rt.runResumed(run)
		}
		if sb != nil {
			rt.repairReviewFlag(run, sb)
		}
	}
}

// busy reports whether a worker's agent is still on its run: working, or
// waiting on background work it started (SWI-12).
func busy(sb *pb.Sandbox) bool {
	st := sb.GetAgent().GetStatus()
	return st == pb.AgentStatus_AGENT_STATUS_WORKING || st == pb.AgentStatus_AGENT_STATUS_WAITING
}
