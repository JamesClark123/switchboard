package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"time"

	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/worker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A run's step sequence (SWI-17, research R5–R8): the orchestrator owns the
// sequence and its persisted state on the Run; the sandbox's owning daemon
// types each step and classifies it (DeliverWorkerStep). One stepper goroutine
// per sandbox drives it, so at most one step call is ever in flight on a
// sandbox and neither dispatch nor status folding waits on a submit check:
// they, the retry ticker and ResolveRunStep only kick it.
//
// A step moves PENDING → SENDING (persisted before the call) → TURN, finished,
// SENT (typed, not yet classified) or HALTED (not accepted), or back to
// PENDING when the worker refused it (nothing typed). After the action step
// the run waits for its close (AWAIT_CLOSE); the cleanup steps then run while
// the closed run holds the sandbox (hold CLEANING_UP), and the last one leaves
// the sequence DONE and the worker's record cleared.

// Notes the orchestrator writes on a run's step (step_note, mirrored to the
// worker's controller.run.step_note).
const (
	waitingPrefix   = "waiting: "
	orphanNote      = "agent detached by a daemon restart"
	orphanSetupNote = orphanNote + "; R restarts setup with a fresh agent"
	orphanCloseNote = orphanNote + "; t → Replace frees the worker"
	droppedPostNote = "custom post-prompts were dropped"
	restartingNote  = "restarting setup with a fresh agent"
	unconfirmedNote = "typed while the screen was resyncing; it may have run"
	noEvidenceNote  = "typed, but no report says it was accepted"
	unsupportedNote = "the worker's daemon does not support pre/post prompts"
	// unsupportedReason fails a run no attached worker can take (research R6).
	unsupportedReason = "no worker supports pre/post prompts; update their daemons"
)

// replacedNote is the note a daemon writes on the run of an agent the developer
// replaced after a daemon restart (research R14): that run's failure reason.
const replacedNote = "agent replaced by the developer after a daemon restart"

// seqState is a runtime's transient step state (guarded by runtime.mu).
type seqState struct {
	steppers map[string]*stepper   // sandbox id → its stepper
	marks    map[string]*stepMarks // run id → timings of its current step
	noSteps  map[string]bool       // sandbox id → its daemon has no DeliverWorkerStep, until its link reconnects
	retry    Timer                 // the retry ticker, armed while a step waits
}

// stepMarks times the current step of a run (in-memory: after an orchestrator
// restart each wait simply starts again).
type stepMarks struct {
	step      int32
	waitSince time.Time // first refusal of the current wait
	sentSeen  time.Time // first time the stepper saw the step SENT with no evidence
	idleSince time.Time // the input_idle_since being timed (idle-nudge fallback)
	idleSeen  time.Time // when the stepper first saw it
	wake      Timer     // a wake-up for one of the timed waits
}

func newSeqState() seqState {
	return seqState{steppers: map[string]*stepper{}, marks: map[string]*stepMarks{}, noSteps: map[string]bool{}}
}

// stopTimers stops the retry ticker and every wake-up (caller holds mu).
func (s *seqState) stopTimers() {
	if s.retry != nil {
		s.retry.Stop()
		s.retry = nil
	}
	for _, m := range s.marks {
		if m.wake != nil {
			m.wake.Stop()
		}
	}
}

// marksFor returns the marks of a run's current step, reset when the step
// changed (caller holds mu).
func (s *seqState) marksFor(run *pb.Run) *stepMarks {
	m := s.marks[run.GetId()]
	if m == nil || m.step != run.GetStep() {
		if m != nil && m.wake != nil {
			m.wake.Stop()
		}
		m = &stepMarks{step: run.GetStep()}
		s.marks[run.GetId()] = m
	}
	return m
}

// dropMarks forgets a run's timings (caller holds mu).
func (s *seqState) dropMarks(runID string) {
	if m := s.marks[runID]; m != nil && m.wake != nil {
		m.wake.Stop()
	}
	delete(s.marks, runID)
}

// --- predicates over a run's steps ---

// sequenced reports whether a run is delivered step by step (FR-208: a run
// without steps is delivered as before).
func sequenced(r *pb.Run) bool { return len(r.GetSteps()) > 0 }

// curStep is the run's current step (nil when out of range).
func curStep(r *pb.Run) *pb.RunStep {
	if i := int(r.GetStep()); i >= 0 && i < len(r.GetSteps()) {
		return r.GetSteps()[i]
	}
	return nil
}

// stepIndex is the index of the first step with the role and kind given
// (UNSPECIFIED matches any), or -1.
func stepIndex(r *pb.Run, role pb.StepRole, kind pb.StepKind) int {
	for i, s := range r.GetSteps() {
		if (role == pb.StepRole_STEP_ROLE_UNSPECIFIED || s.GetRole() == role) && (kind == pb.StepKind_STEP_KIND_UNSPECIFIED || s.GetKind() == kind) {
			return i
		}
	}
	return -1
}

func actionIndex(r *pb.Run) int {
	return stepIndex(r, pb.StepRole_STEP_ROLE_ACTION, pb.StepKind_STEP_KIND_UNSPECIFIED)
}

func clearIndex(r *pb.Run) int {
	return stepIndex(r, pb.StepRole_STEP_ROLE_CLEANUP, pb.StepKind_STEP_KIND_CLEAR)
}

// actionAccepted reports whether the action prompt was delivered: its step was
// accepted (TURN or AWAIT_CLOSE) or the sequence is past it (FR-209, FR-211).
func actionAccepted(r *pb.Run) bool {
	a := actionIndex(r)
	if a < 0 {
		return false
	}
	switch i := int(r.GetStep()); {
	case i > a:
		return true
	case i == a:
		return r.GetStepStatus() == pb.StepStatus_STEP_STATUS_TURN || r.GetStepStatus() == pb.StepStatus_STEP_STATUS_AWAIT_CLOSE
	}
	return false
}

// stepActive reports whether a run still has step work on its sandbox: a
// claimed or delivered sequence not DONE, or a hold.
func stepActive(r *pb.Run) bool {
	if !sequenced(r) || r.GetSandboxId() == "" {
		return false
	}
	if r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE {
		return true
	}
	st := r.GetStepStatus()
	return st != pb.StepStatus_STEP_STATUS_UNSPECIFIED && st != pb.StepStatus_STEP_STATUS_DONE
}

// holding reports whether a closed run still holds its sandbox: a hold, or a
// halted cleanup step (FR-213). Such a run is listed in flight and is exempt
// from retention.
func holding(r *pb.Run) bool {
	return finished(r) && stepActive(r) &&
		(r.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE || r.GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED)
}

// cleaningUp reports whether a closed run's cleanup steps still run on its
// sandbox: CLEANING_UP, or NEEDS_INPUT_FREEING (only Clear is left).
func cleaningUp(r *pb.Run) bool {
	return r.GetHold() == pb.WorkerHold_WORKER_HOLD_CLEANING_UP || r.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING
}

// restartHalt reports whether a run is halted because its agent was detached
// by an owning-daemon restart (research R14): R restarts setup, S is refused.
func restartHalt(r *pb.Run) bool {
	return r.GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED && strings.HasPrefix(r.GetStepNote(), orphanNote)
}

// haltedOnWorker is controller.run.halted for a run: halted at a step, or
// about to retry (or restart) one, which the worker accepts only while its
// record still reads halted (guard 3a).
func haltedOnWorker(r *pb.Run) bool {
	switch r.GetStepStatus() {
	case pb.StepStatus_STEP_STATUS_HALTED:
		return true
	case pb.StepStatus_STEP_STATUS_PENDING, pb.StepStatus_STEP_STATUS_SENDING:
		return r.GetStepSentAt() != nil || r.GetStepNote() == restartingNote
	}
	return false
}

// moveTo puts a run on step i with the status given, clearing everything the
// previous step left (data-model: the markers clear whenever step changes).
func moveTo(r *pb.Run, i int, st pb.StepStatus) {
	r.Step = int32(i)
	r.StepStatus = st
	r.StepSentAt = nil
	r.StepKindSeen = pb.StepObserved_STEP_OBSERVED_UNKNOWN
	r.StepNote = ""
	r.StepSentBeforeRestart = false
	r.StepUnconfirmed = false
}

// stepLabel names a run's current step for reasons and notifications:
// "setup step 1/2 (/model opus)", "the action prompt", "cleanup step 2/2 (/clear)".
func stepLabel(r *pb.Run) string {
	st := curStep(r)
	if st == nil {
		return "the sequence"
	}
	if st.GetRole() == pb.StepRole_STEP_ROLE_ACTION {
		return "the action prompt"
	}
	n, m := 0, 0
	for i, s := range r.GetSteps() {
		if s.GetRole() == st.GetRole() {
			m++
			if i <= int(r.GetStep()) {
				n++
			}
		}
	}
	what := "setup"
	if st.GetRole() == pb.StepRole_STEP_ROLE_CLEANUP {
		what = "cleanup"
	}
	text := strings.Join(strings.Fields(st.GetText()), " ")
	if r := []rune(text); len(r) > 40 {
		text = string(r[:39]) + "…"
	}
	return fmt.Sprintf("%s step %d/%d (%s)", what, n, m, text)
}

// inSetup reports whether a delivered run has not had its action prompt
// accepted yet (FR-212 names the step for such a run).
func inSetup(r *pb.Run) bool { return sequenced(r) && !actionAccepted(r) }

// duringStep appends the step a run in setup was on to a failure reason.
func duringStep(reason string, r *pb.Run) string {
	if inSetup(r) {
		return reason + " during " + stepLabel(r)
	}
	return reason
}

// agentCheck is the owning daemon's detached-agent check of a sandbox (R14).
func agentCheck(sb *pb.Sandbox) pb.AgentCheck_State {
	return sb.GetAgent().GetAgentCheck().GetState()
}

// canType reports whether a sandbox's agent can be typed into: running, not
// exited and not detached by an owning-daemon restart (an unknown check may
// still clear, so it waits rather than drops).
func canType(sb *pb.Sandbox) bool {
	return sb != nil && sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING &&
		sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED &&
		agentCheck(sb) != pb.AgentCheck_STATE_DETACHED
}

// typedFor reports whether any step of a run reached its agent.
func typedFor(r *pb.Run, sb *pb.Sandbox) bool {
	if r.GetStep() > 0 || r.GetStepSentAt() != nil {
		return true
	}
	wr := sb.GetController().GetRun()
	return wr.GetId() == r.GetId() && wr.GetStepSentAt() != nil
}

// after reports whether a (daemon-clock) timestamp is set and later than ref.
func after(ts, ref *timestamppb.Timestamp) bool {
	return ts != nil && ref != nil && ts.AsTime().After(ref.AsTime())
}

// exitReason is the failure reason of a run whose agent exited: the daemon's
// note on the record when it wrote the Replace one (a developer's Replace after
// a daemon restart, R14), never another note (a check-failed one, or one the
// orchestrator mirrored); otherwise "agent exited", naming the step for a run
// in setup (FR-212).
func exitReason(r *pb.Run, sb *pb.Sandbox) string {
	wr := sb.GetController().GetRun()
	if wr.GetId() == r.GetId() && wr.GetStepNote() == replacedNote {
		return replacedNote
	}
	return duringStep("agent exited", r)
}

// --- effects of a transition, applied once runs.mu is released ---

type stepFx struct {
	sync     bool   // push the run's controller.run (hold, halt, note) to its worker
	clear    bool   // clear the worker's controller.run (the sequence is done)
	halted   string // raise RUN_STEP_NOT_ACCEPTED with this message
	dispatch bool   // a worker may be free (or a run back in the queue)
	kick     bool   // the stepper has work
}

func (rt *runtime) applyFx(run *pb.Run, fx stepFx) {
	switch {
	case fx.clear:
		if rt.sandboxRecord(run.GetSandboxId()) != nil {
			rt.clearWorkerRun(run)
		}
	case fx.sync:
		rt.syncWorkerRun(run.GetId())
	}
	if fx.halted != "" {
		rt.notify(run, pb.NotificationKind_NOTIFICATION_KIND_RUN_STEP_NOT_ACCEPTED, fx.halted)
	}
	if fx.dispatch {
		rt.triggerDispatch()
	}
	if fx.kick {
		rt.kickStepper(run.GetSandboxId())
	}
}

// syncWorkerRun pushes a run's controller.run to its worker when the record
// the link last delivered disagrees (hold, halted, note, review flag), under
// the worker's push lock. A record of another run (not yet recorded, or
// released by the daemon) is never written.
func (rt *runtime) syncWorkerRun(runID string) {
	first, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil || first.GetSandboxId() == "" {
		return
	}
	l := rt.workerPushLock(first.GetSandboxId())
	l.Lock()
	defer l.Unlock()
	live, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil || live.GetSandboxId() == "" {
		return
	}
	cur := rt.sandboxRecord(live.GetSandboxId()).GetController().GetRun()
	if cur.GetId() != live.GetId() {
		return
	}
	if want := workerRunFor(live, cur); !proto.Equal(want, cur) {
		rt.setWorkerRun(live.GetSandboxHostId(), live.GetSandboxId(), want, live.GetId())
	}
}

// --- transactions on the live record ---

// sameStep reports whether the live record is where the caller saw it. The
// in-flight states count as one: status folding moves a run between running,
// awaiting input and awaiting review while a step call is out, and the step's
// answer still applies (lost, it would leave a typed step unclassified).
func sameStep(a, b *pb.Run) bool {
	return stepState(a) == stepState(b) && a.GetStep() == b.GetStep() && a.GetStepSend() == b.GetStepSend() &&
		a.GetStepStatus() == b.GetStepStatus() && a.GetHold() == b.GetHold()
}

func stepState(r *pb.Run) pb.RunState {
	if inFlight(r) {
		return pb.RunState_RUN_STATE_RUNNING
	}
	return r.GetState()
}

// stepTx applies fn to the live record of seen under runs.mu while its step is
// still where the caller saw it, and persists it when fn reports a change. It
// returns the record (false when the run moved on meanwhile).
func (rt *runtime) stepTx(seen *pb.Run, fn func(live *pb.Run) bool) (*pb.Run, bool) {
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	live, err := rt.svc.reg.GetRun(rt.id, seen.GetId())
	if err != nil || !sameStep(live, seen) {
		return nil, false
	}
	accepted := actionAccepted(live)
	if !fn(live) {
		return live, true
	}
	if err := rt.putRun(live); err != nil {
		rt.svc.logf("[orchestrator %s] record step of %s: %v", rt.name(), live.GetId(), err)
		return nil, false
	}
	if !accepted && actionAccepted(live) {
		rt.armIdleReview(live)
	}
	if st := live.GetStepStatus(); st == pb.StepStatus_STEP_STATUS_HALTED || st == pb.StepStatus_STEP_STATUS_DONE {
		rt.dropMarks(live.GetId()) // nothing is timed on a halted or finished step
	}
	return live, true
}

// armIdleReview starts the review grace of a run whose action prompt the
// stepper just accepted while its agent is already idle (a send resolved after
// a restart or reconnect): no further status may come to arm it (FR-156).
func (rt *runtime) armIdleReview(run *pb.Run) {
	if inFlight(run) && run.GetState() != pb.RunState_RUN_STATE_AWAITING_REVIEW &&
		rt.sandboxRecord(run.GetSandboxId()).GetAgent().GetStatus() == pb.AgentStatus_AGENT_STATUS_IDLE {
		rt.armReview(run)
	}
}

// refreshLocked re-reads a caller's copy of a run from the store (caller holds
// runs.mu), so a transition never writes back step state the stepper has
// moved on since the caller read it.
func (rt *runtime) refreshLocked(run *pb.Run) {
	if live, err := rt.svc.reg.GetRun(rt.id, run.GetId()); err == nil {
		proto.Reset(run)
		proto.Merge(run, live)
	}
}

// --- the stepper ---

type stepper struct{ kick chan struct{} }

// kickStepper asks a sandbox's stepper to look at its run, starting one when
// none runs. Kicks are coalesced, never dropped: one arriving during a call is
// consumed when the call returns.
func (rt *runtime) kickStepper(sandboxID string) {
	if sandboxID == "" {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.ctx.Err() != nil {
		return
	}
	if st, ok := rt.seq.steppers[sandboxID]; ok {
		select {
		case st.kick <- struct{}{}:
		default:
		}
		return
	}
	st := &stepper{kick: make(chan struct{}, 1)}
	st.kick <- struct{}{}
	rt.seq.steppers[sandboxID] = st
	rt.wg.Add(1)
	go rt.runStepper(sandboxID, st)
}

// kickSteps kicks a sandbox's stepper when it has step work (a status change).
func (rt *runtime) kickSteps(sandboxID string) {
	rt.mu.Lock()
	_, running := rt.seq.steppers[sandboxID]
	rt.mu.Unlock()
	if running || rt.seqRunOn(sandboxID) != nil {
		rt.kickStepper(sandboxID)
	}
}

// runStepper is a sandbox's stepper: on each kick it advances the run until
// nothing changes, and it ends once the sandbox has no step work left.
func (rt *runtime) runStepper(sandboxID string, st *stepper) {
	defer rt.wg.Done()
	for {
		select {
		case <-rt.ctx.Done():
			rt.mu.Lock()
			delete(rt.seq.steppers, sandboxID)
			rt.mu.Unlock()
			return
		case <-st.kick:
		}
		active := true
		for rt.ctx.Err() == nil {
			var more bool
			more, active = rt.stepOnce(sandboxID)
			if !more {
				break
			}
		}
		rt.mu.Lock()
		if !active && len(st.kick) == 0 {
			delete(rt.seq.steppers, sandboxID)
			rt.mu.Unlock()
			return
		}
		rt.mu.Unlock()
	}
}

// seqRunOn returns the run with step work on a sandbox (nil when none).
func (rt *runtime) seqRunOn(sandboxID string) *pb.Run {
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	runs, err := rt.svc.reg.ListRuns(rt.id)
	if err != nil {
		return nil
	}
	for _, r := range runs {
		if r.GetSandboxId() == sandboxID && stepActive(r) {
			return r
		}
	}
	return nil
}

// stepOnce makes one transition of the sandbox's run against the latest
// sandbox record. more: something changed, look again; active: the sandbox
// still has step work.
func (rt *runtime) stepOnce(sandboxID string) (more, active bool) {
	run := rt.seqRunOn(sandboxID)
	if run == nil {
		return false, false
	}
	sb := rt.sandboxRecord(sandboxID)
	if rt.reconcileStep(run, sb) {
		return true, true
	}
	switch run.GetStepStatus() {
	case pb.StepStatus_STEP_STATUS_SENDING:
		return rt.resolveSending(run), true
	case pb.StepStatus_STEP_STATUS_PENDING:
		return rt.sendStep(run, sb), true
	case pb.StepStatus_STEP_STATUS_SENT:
		return rt.resolveSent(run, sb), true
	case pb.StepStatus_STEP_STATUS_TURN:
		return rt.finishTurn(run, sb), true
	}
	return false, true
}

// reconcileStep folds the daemon's record into the run (research R7, R14,
// T022): a claim the worker can no longer take, a hold the daemon ended or
// raised, cleanup whose agent is gone, a setup halted by a detached agent, and
// the worker's copy of the run's flags. It reports whether it moved the run.
func (rt *runtime) reconcileStep(run *pb.Run, sb *pb.Sandbox) bool {
	pending := run.GetStepStatus() == pb.StepStatus_STEP_STATUS_PENDING
	if run.GetState() == pb.RunState_RUN_STATE_QUEUED {
		// A claim the worker can't take (gone, stopped, detached agent): back
		// to the queue, nothing having been typed.
		if pending && (sb == nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING ||
			agentCheck(sb) != pb.AgentCheck_STATE_CLEAR || hostOf(rt.record(), run.GetSandboxId()) == "") {
			return rt.unclaim(run)
		}
		return false
	}
	if sb == nil {
		return false // removal ends it (onSandboxRemoved)
	}
	wr := sb.GetController().GetRun()
	if finished(run) && run.GetHold() != pb.WorkerHold_WORKER_HOLD_NONE {
		switch {
		case wr.GetId() != run.GetId():
			// The daemon released the hold (a fresh session, T008), or the
			// record is gone: the cleanup is over, and a retried Clear still
			// waiting is dropped (FR-216).
			_, ok := rt.stepTx(run, func(l *pb.Run) bool {
				l.Hold = pb.WorkerHold_WORKER_HOLD_NONE
				moveTo(l, int(l.GetStep()), pb.StepStatus_STEP_STATUS_DONE)
				return true
			})
			if ok {
				rt.dropMarks(run.GetId())
				rt.triggerDispatch()
			}
			return true
		case wr.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER && run.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER,
			cleaningUp(run) && run.GetStepStatus() != pb.StepStatus_STEP_STATUS_SENDING && !canType(sb):
			// The agent exited or the sandbox stopped during cleanup (the
			// daemon may have raised the hold already): the remaining steps
			// are dropped and the developer frees it (FR-216).
			var fx stepFx
			live, ok := rt.stepTx(run, func(l *pb.Run) bool {
				fx = rt.toDeveloperLocked(l, sb)
				return true
			})
			if ok {
				rt.applyFx(live, fx)
			}
			return true
		}
	}
	if rt.orphanHalt(run, sb) {
		return true
	}
	if wr.GetId() == run.GetId() && run.GetStepStatus() != pb.StepStatus_STEP_STATUS_SENDING {
		// A halt, hold or note push lost on a down link (T022 halted repair).
		if want := workerRunFor(run, wr); !proto.Equal(want, wr) {
			rt.syncWorkerRun(run.GetId())
		}
	}
	return false
}

// orphanHalt halts a run in setup whose agent a daemon restart detached
// (research R14): the setup done so far lives in a session nobody can type
// into, so R restarts it from step 0 with a fresh agent. A run whose action
// prompt was accepted is left in flight.
func (rt *runtime) orphanHalt(run *pb.Run, sb *pb.Sandbox) bool {
	if agentCheck(sb) != pb.AgentCheck_STATE_DETACHED || finished(run) || !inFlight(run) || actionAccepted(run) {
		return false
	}
	switch run.GetStepStatus() {
	case pb.StepStatus_STEP_STATUS_HALTED:
		if restartHalt(run) {
			return false
		}
	case pb.StepStatus_STEP_STATUS_PENDING:
		if run.GetStepNote() == restartingNote {
			return false // R was pressed: the next send restarts the agent
		}
	case pb.StepStatus_STEP_STATUS_SENDING:
		return false // resolved from the daemon record first
	case pb.StepStatus_STEP_STATUS_SENT:
		if curStep(run).GetRole() == pb.StepRole_STEP_ROLE_ACTION && turnEvidence(run, sb) != pb.StepObserved_STEP_OBSERVED_UNKNOWN {
			return false // the action prompt started its turn: the run is in ACTION
		}
	}
	var fx stepFx
	live, ok := rt.stepTx(run, func(l *pb.Run) bool {
		sent := l.GetStepSentAt() != nil
		l.StepStatus = pb.StepStatus_STEP_STATUS_HALTED
		l.StepNote = orphanSetupNote
		l.StepSentBeforeRestart = sent
		l.StepUnconfirmed = false
		fx = stepFx{sync: true, halted: "halted at " + stepLabel(l) + ": " + orphanSetupNote}
		return true
	})
	if ok {
		rt.applyFx(live, fx)
	}
	return true
}

// unclaim puts a claimed, untyped run back in the queue.
func (rt *runtime) unclaim(run *pb.Run) bool {
	_, ok := rt.stepTx(run, func(l *pb.Run) bool {
		unclaimLocked(l)
		return true
	})
	if ok {
		rt.dropMarks(run.GetId())
		rt.triggerDispatch()
	}
	return true
}

func unclaimLocked(l *pb.Run) {
	l.SandboxId, l.SandboxHostId = "", ""
	moveTo(l, 0, pb.StepStatus_STEP_STATUS_UNSPECIFIED)
}

// unclaimOn puts every claimed, untyped run on a sandbox back in the queue
// (its worker was dropped): pending, or halted on a refused first step.
func (rt *runtime) unclaimOn(sandboxID string) {
	rt.runs.mu.Lock()
	runs, _ := rt.svc.reg.ListRuns(rt.id)
	n := 0
	for _, r := range runs {
		st := r.GetStepStatus()
		if r.GetState() == pb.RunState_RUN_STATE_QUEUED && r.GetSandboxId() == sandboxID && (st == pb.StepStatus_STEP_STATUS_PENDING || st == pb.StepStatus_STEP_STATUS_HALTED) {
			unclaimLocked(r)
			if rt.putRun(r) == nil {
				n++
			}
		}
	}
	rt.runs.mu.Unlock()
	if n > 0 {
		rt.triggerDispatch()
	}
}

// endHoldingOn ends the step work of the closed runs on a sandbox that left
// the orchestrator (removed, detached, a rebuilt reasoning sandbox): their
// cleanup is over (FR-222, T021).
func (rt *runtime) endHoldingOn(sandboxID string) {
	rt.runs.mu.Lock()
	defer rt.runs.mu.Unlock()
	runs, _ := rt.svc.reg.ListRuns(rt.id)
	for _, r := range runs {
		if finished(r) && r.GetSandboxId() == sandboxID && stepActive(r) {
			r.Hold = pb.WorkerHold_WORKER_HOLD_NONE
			moveTo(r, int(r.GetStep()), pb.StepStatus_STEP_STATUS_DONE)
			_ = rt.putRun(r)
			rt.dropMarks(r.GetId())
		}
	}
}

// --- sending a step ---

// notReady is why a step can't be typed now, from the record alone (no call
// is made for an agent visibly busy), or "".
func notReady(sb *pb.Sandbox) string {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return "sandbox is " + swb.SandboxStateLabel(sb.GetState())
	}
	switch st := sb.GetAgent().GetStatus(); st {
	case pb.AgentStatus_AGENT_STATUS_WORKING, pb.AgentStatus_AGENT_STATUS_WAITING, pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT:
		return "agent is " + swb.AgentStatusLabel(st)
	}
	return ""
}

// sendStep delivers the run's PENDING step (research R5): the send is
// persisted as SENDING first, then the worker types it and classifies it.
func (rt *runtime) sendStep(run *pb.Run, sb *pb.Sandbox) bool {
	st := curStep(run)
	if sb == nil || st == nil {
		return false
	}
	api, err := rt.apiFor(run.GetSandboxHostId())
	if err != nil {
		return false // the link's reconnect kicks it again
	}
	restart := run.GetStepNote() == restartingNote && agentCheck(sb) == pb.AgentCheck_STATE_DETACHED
	if reason := notReady(sb); reason != "" && !restart {
		rt.stepRefused(run, sb, reason)
		return false
	}
	// A step typed before (a retry) keeps its send number, which the worker
	// accepts once while the run is halted; any other send takes a new one.
	reuse := run.GetStepSentAt() != nil
	retry := reuse || (st.GetKind() == pb.StepKind_STEP_KIND_CLEAR && run.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER)
	sending, ok := rt.stepTx(run, func(l *pb.Run) bool {
		if !reuse {
			l.StepSend++
		}
		l.StepStatus = pb.StepStatus_STEP_STATUS_SENDING
		return true
	})
	if !ok {
		return true
	}
	ctx, cancel := rt.callCtx(deliverTimeout)
	resp, err := api.DeliverWorkerStep(ctx, &pb.DeliverWorkerStepRequest{
		SandboxId: sending.GetSandboxId(), OrchestratorId: rt.id, RunId: sending.GetId(), Action: sending.GetAction(),
		Step: st, First: sending.GetState() == pb.RunState_RUN_STATE_QUEUED, StepIndex: sending.GetStepSend(),
		Retry: retry, Restart: restart,
	})
	cancel()
	return rt.applyOutcome(sending, resp, err)
}

// stepResult classifies a DeliverWorkerStep answer.
type stepResult int

const (
	resTyped       stepResult = iota // an outcome: the step was typed
	resSent                          // ALREADY_EXISTS: typed before (a lost response)
	resRefused                       // FAILED_PRECONDITION: nothing typed, try again
	resInvalid                       // INVALID_ARGUMENT: never typed, never retried
	resUnsupported                   // an older daemon
	resTransport                     // unknown: resolved from the daemon record later
)

func classifyStep(err error) stepResult {
	code := status.Code(err)
	switch {
	case err == nil:
		return resTyped
	case errors.Is(err, worker.ErrStepSent), code == codes.AlreadyExists:
		return resSent
	case errors.Is(err, worker.ErrUnsupported), code == codes.Unimplemented:
		return resUnsupported
	case errors.Is(err, worker.ErrInvalid), code == codes.InvalidArgument:
		return resInvalid
	case errors.Is(err, worker.ErrPrecondition), errors.Is(err, worker.ErrNotFound), errors.Is(err, worker.ErrForbidden),
		code == codes.FailedPrecondition, code == codes.NotFound, code == codes.PermissionDenied:
		return resRefused
	}
	return resTransport
}

// refusalReason is a refusal's reason as the worker gave it.
func refusalReason(err error) string {
	msg := err.Error()
	if st, ok := status.FromError(err); ok {
		msg = st.Message()
	}
	return strings.TrimPrefix(msg, worker.ErrPrecondition.Error()+": ")
}

// applyOutcome records a step call's answer on the run (research R5, R6).
func (rt *runtime) applyOutcome(sent *pb.Run, resp *pb.DeliverWorkerStepResponse, err error) bool {
	res := classifyStep(err)
	if res == resTransport {
		// Typed or not is unknown: SENDING stays until the daemon's record
		// says (resolveSending), and nothing is re-typed blindly.
		rt.svc.logf("[orchestrator %s] step %d of %s on %s: %v", rt.name(), sent.GetStep(), sent.GetId(), sent.GetSandboxId(), err)
		rt.armStepRetry()
		return false
	}
	sb := rt.sandboxRecord(sent.GetSandboxId())
	now := rt.svc.clock.Now()
	var fx stepFx
	answered := false
	live, ok := rt.stepTx(sent, func(l *pb.Run) bool {
		closedEarly := finished(l) && l.GetHold() == pb.WorkerHold_WORKER_HOLD_NONE
		switch res {
		case resRefused:
			l.StepStatus = pb.StepStatus_STEP_STATUS_PENDING
			if closedEarly {
				fx = rt.failCleanupLocked(l, sb, typedFor(l, sb))
			}
			return true
		case resUnsupported:
			if l.GetState() == pb.RunState_RUN_STATE_QUEUED {
				unclaimLocked(l)
				fx = stepFx{dispatch: true}
				return true
			}
			fx = haltLocked(l, unsupportedNote)
		case resInvalid:
			fx = haltLocked(l, refusalReason(err))
		default:
			switch l.GetState() {
			case pb.RunState_RUN_STATE_QUEUED:
				l.State = pb.RunState_RUN_STATE_RUNNING
				l.StartedAt = timestamppb.New(now)
			case pb.RunState_RUN_STATE_AWAITING_INPUT:
				// Typed at an empty, ready box: the agent's question (asked by
				// an earlier step) is gone, and the sequence goes on (US2 #4).
				l.State = pb.RunState_RUN_STATE_RUNNING
				answered = true
			}
			fx = typedLocked(l, res, resp, sb)
		}
		if closedEarly && l.GetStepStatus() != pb.StepStatus_STEP_STATUS_DONE {
			// Cancelled or failed while this step was in flight: a typed step
			// reached the agent, so Clear (only) follows (FR-214, amended).
			fx = rt.failCleanupLocked(l, sb, res != resInvalid || typedFor(l, sb))
		}
		return true
	})
	if !ok {
		return true // the run moved on meanwhile (a report started cleanup): only its step was answered
	}
	switch res {
	case resRefused:
		rt.applyFx(live, fx)
		if live.GetStepStatus() == pb.StepStatus_STEP_STATUS_PENDING {
			rt.stepRefused(live, sb, refusalReason(err))
		}
		return false
	case resUnsupported:
		if live.GetState() == pb.RunState_RUN_STATE_QUEUED {
			rt.mu.Lock()
			rt.seq.noSteps[sent.GetSandboxId()] = true
			rt.mu.Unlock()
			rt.svc.logf("[orchestrator %s] %s on %s predates pre/post prompts; its run goes to another worker", rt.name(), sent.GetSandboxId(), sent.GetSandboxHostId())
		}
	}
	rt.mu.Lock()
	rt.seq.dropMarks(live.GetId())
	if answered {
		delete(rt.inputNotified, live.GetId())
	}
	rt.mu.Unlock()
	rt.applyFx(live, fx)
	return true
}

// typedLocked applies an outcome of a typed step (caller holds runs.mu).
func typedLocked(l *pb.Run, res stepResult, resp *pb.DeliverWorkerStepResponse, sb *pb.Sandbox) stepFx {
	sync := stepFx{sync: true}
	if res == resSent {
		// ALREADY_EXISTS: typed by an earlier call whose answer was lost. The
		// time is the daemon's record (a gRPC status carries none).
		l.StepStatus = pb.StepStatus_STEP_STATUS_SENT
		if wr := sb.GetController().GetRun(); wr.GetId() == l.GetId() {
			l.StepSentAt = wr.GetStepSentAt()
		}
		return sync
	}
	l.StepSentAt = resp.GetSentAt()
	l.StepNote = ""
	l.StepUnconfirmed, l.StepSentBeforeRestart = false, false
	switch resp.GetOutcome() {
	case pb.StepOutcome_STEP_OUTCOME_MODEL_TURN:
		l.StepStatus, l.StepKindSeen = pb.StepStatus_STEP_STATUS_TURN, pb.StepObserved_STEP_OBSERVED_MODEL_TURN
	case pb.StepOutcome_STEP_OUTCOME_MODEL_TURN_INPUT_ONLY:
		l.StepStatus, l.StepKindSeen = pb.StepStatus_STEP_STATUS_TURN, pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY
	case pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL:
		l.StepKindSeen = pb.StepObserved_STEP_OBSERVED_AGENT_LOCAL
		return advanceLocked(l)
	case pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED:
		// The screen stayed stale: typed, not classified. Hook evidence or a
		// read-back resolves it (resolveSent).
		l.StepStatus = pb.StepStatus_STEP_STATUS_SENT
		l.StepUnconfirmed = true
	default:
		note := resp.GetNote()
		if note == "" {
			note = "not accepted"
		}
		return haltLocked(l, note)
	}
	return sync
}

// haltLocked halts the run at its current step, not accepted (FR-206c).
func haltLocked(l *pb.Run, note string) stepFx {
	l.StepStatus = pb.StepStatus_STEP_STATUS_HALTED
	l.StepNote = note
	return stepFx{sync: true, halted: "halted: " + stepLabel(l) + " not accepted — " + note}
}

// advanceLocked finishes the current step (caller holds runs.mu): after the
// action step the run waits for its close; after the last step the sequence
// is DONE and the worker's record cleared; otherwise the next step is due.
func advanceLocked(l *pb.Run) stepFx {
	i := int(l.GetStep())
	if curStep(l).GetRole() == pb.StepRole_STEP_ROLE_ACTION && !finished(l) {
		l.StepStatus = pb.StepStatus_STEP_STATUS_AWAIT_CLOSE
		l.StepNote = ""
		return stepFx{sync: true}
	}
	if i+1 >= len(l.GetSteps()) || l.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING {
		// Freeing: Clear was the one step left (FR-216).
		l.Hold = pb.WorkerHold_WORKER_HOLD_NONE
		moveTo(l, i, pb.StepStatus_STEP_STATUS_DONE)
		return stepFx{clear: true}
	}
	moveTo(l, i+1, pb.StepStatus_STEP_STATUS_PENDING)
	return stepFx{sync: true, kick: true}
}

// stepRefused records a refusal: once refusals have lasted stepWaitNote the
// step's note says why (research R6), and the retry ticker runs.
func (rt *runtime) stepRefused(run *pb.Run, sb *pb.Sandbox, reason string) {
	now := rt.svc.clock.Now()
	rt.mu.Lock()
	m := rt.seq.marksFor(run)
	if m.waitSince.IsZero() {
		m.waitSince = now
	}
	since := m.waitSince
	rt.mu.Unlock()
	rt.armStepRetry()
	note := waitingPrefix + reason
	if now.Sub(since) < stepWaitNote || run.GetStepNote() == note ||
		(run.GetStepNote() == restartingNote && agentCheck(sb) == pb.AgentCheck_STATE_DETACHED) {
		return
	}
	if live, ok := rt.stepTx(run, func(l *pb.Run) bool {
		l.StepNote = note
		return true
	}); ok {
		rt.syncWorkerRun(live.GetId())
	}
}

// armStepRetry arms the retry ticker (research R6) unless it is armed.
func (rt *runtime) armStepRetry() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.seq.retry != nil || rt.ctx.Err() != nil {
		return
	}
	rt.seq.retry = rt.svc.clock.AfterFunc(stepRetryEvery, rt.stepRetryTick)
}

// stepRetryTick kicks every sandbox whose step waits to be sent (or whose send
// is unresolved) on a reachable host, and re-arms while there is one.
func (rt *runtime) stepRetryTick() {
	rt.mu.Lock()
	rt.seq.retry = nil
	rt.mu.Unlock()
	if rt.ctx.Err() != nil {
		return
	}
	rt.runs.mu.Lock()
	runs, _ := rt.svc.reg.ListRuns(rt.id)
	rt.runs.mu.Unlock()
	waiting := false
	for _, r := range runs {
		st := r.GetStepStatus()
		if !stepActive(r) || (st != pb.StepStatus_STEP_STATUS_PENDING && st != pb.StepStatus_STEP_STATUS_SENDING) {
			continue
		}
		if up, _ := rt.reachability(r.GetSandboxHostId()); up {
			waiting = true
			rt.kickStepper(r.GetSandboxId())
		}
	}
	if waiting {
		rt.armStepRetry()
	}
}

// wakeAfter kicks a run's stepper once d has elapsed (a timed wait).
func (rt *runtime) wakeAfter(run *pb.Run, d time.Duration) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.ctx.Err() != nil {
		return
	}
	m := rt.seq.marksFor(run)
	if m.wake != nil {
		m.wake.Stop()
	}
	id := run.GetSandboxId()
	m.wake = rt.svc.clock.AfterFunc(d, func() { rt.kickStepper(id) })
}

func (rt *runtime) dropMarks(runID string) {
	rt.mu.Lock()
	rt.seq.dropMarks(runID)
	rt.mu.Unlock()
}

// --- resolving a step whose answer is unknown ---

// resolveSending resolves a step left SENDING (a lost answer, a link blip, an
// orchestrator restart) from a fresh read of the daemon's record, never a
// cached one (research R5, T022): the send number recorded → typed (SENT with
// the daemon's time); otherwise → PENDING, it was never typed.
func (rt *runtime) resolveSending(run *pb.Run) bool {
	api, err := rt.apiFor(run.GetSandboxHostId())
	if err != nil {
		return false
	}
	ctx, cancel := rt.callCtx(unaryTimeout)
	sbs, err := api.ListSandboxes(ctx)
	cancel()
	if err != nil {
		return false
	}
	var sb *pb.Sandbox
	for _, x := range sbs {
		if x.GetId() == run.GetSandboxId() {
			sb = x
		}
	}
	if sb != nil {
		rt.mu.Lock()
		if _, known := rt.sbs[sb.GetId()]; known {
			rt.cacheLocked(sb) // the cache keeps the newest record (SWI-18)
		}
		rt.mu.Unlock()
	}
	wr := sb.GetController().GetRun()
	typed := wr.GetId() == run.GetId() && wr.GetStepSentAt() != nil && wr.GetStepIndex() == run.GetStepSend() &&
		(run.GetStepSentAt() == nil || !proto.Equal(wr.GetStepSentAt(), run.GetStepSentAt()))
	now := rt.svc.clock.Now()
	var fx stepFx
	live, ok := rt.stepTx(run, func(l *pb.Run) bool {
		closedEarly := finished(l) && l.GetHold() == pb.WorkerHold_WORKER_HOLD_NONE
		if !typed {
			l.StepStatus = pb.StepStatus_STEP_STATUS_PENDING
			if closedEarly {
				fx = rt.failCleanupLocked(l, sb, typedFor(l, sb))
			}
			return true
		}
		if l.GetState() == pb.RunState_RUN_STATE_QUEUED {
			l.State = pb.RunState_RUN_STATE_RUNNING
			l.StartedAt = timestamppb.New(now)
		}
		l.StepStatus = pb.StepStatus_STEP_STATUS_SENT
		l.StepSentAt = wr.GetStepSentAt()
		fx = stepFx{sync: true}
		if closedEarly {
			fx = rt.failCleanupLocked(l, sb, true)
		}
		return true
	})
	if ok {
		rt.applyFx(live, fx)
	}
	return true
}

// turnEvidence classifies a typed step from the daemon's main-session
// evidence after its send (research R8 rule 2): a prompt submit, or a
// needs-input report.
func turnEvidence(run *pb.Run, sb *pb.Sandbox) pb.StepObserved {
	sent, a := run.GetStepSentAt(), sb.GetAgent()
	switch {
	case after(a.GetLastSubmitAt(), sent):
		return pb.StepObserved_STEP_OBSERVED_MODEL_TURN
	case after(a.GetLastNeedsInputAt(), sent):
		return pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY
	}
	return pb.StepObserved_STEP_OBSERVED_UNKNOWN
}

func builtin(k pb.StepKind) bool {
	return k == pb.StepKind_STEP_KIND_MODEL || k == pb.StepKind_STEP_KIND_EFFORT || k == pb.StepKind_STEP_KIND_CLEAR
}

// resolveSent resolves a step typed but not classified (an UNCONFIRMED
// outcome, a lost answer, an orchestrator restart) by evidence after its send,
// never by an idle state that was already there (FR-206a, FR-217, research
// R8). Rule 2: a turn start makes it a TURN (finishTurn then waits for its
// end). Rule 3: a built-in whose setting reads back is finished; the daemon
// runs the read-back (check_only), and for Clear it requires the clear
// session start after the send, corroborated by its own screen (R13), so the
// orchestrator never finishes Clear on the hook evidence alone. An agent
// detached by an owning-daemon restart has no screen to read: only hook
// evidence counts for it (R14). Rule 4: otherwise, once sentGrace has passed,
// it halts for the developer, marked as possibly run.
func (rt *runtime) resolveSent(run *pb.Run, sb *pb.Sandbox) bool {
	st := curStep(run)
	if sb == nil || st == nil {
		return false
	}
	if kind := turnEvidence(run, sb); kind != pb.StepObserved_STEP_OBSERVED_UNKNOWN {
		live, ok := rt.stepTx(run, func(l *pb.Run) bool {
			l.StepStatus, l.StepKindSeen = pb.StepStatus_STEP_STATUS_TURN, kind
			l.StepUnconfirmed = false
			return true
		})
		if ok {
			rt.syncWorkerRun(live.GetId())
		}
		return true
	}
	if builtin(st.GetKind()) && canType(sb) {
		if agentCheck(sb) == pb.AgentCheck_STATE_UNKNOWN {
			return false // a probe is pending: its answer (a status change) kicks again
		}
		api, err := rt.apiFor(run.GetSandboxHostId())
		if err != nil {
			return false
		}
		ctx, cancel := rt.callCtx(deliverTimeout)
		resp, err := api.DeliverWorkerStep(ctx, &pb.DeliverWorkerStepRequest{
			SandboxId: run.GetSandboxId(), OrchestratorId: rt.id, RunId: run.GetId(), Action: run.GetAction(),
			Step: st, CheckOnly: true, StepIndex: run.GetStepSend(),
		})
		cancel()
		switch {
		case err != nil, resp.GetOutcome() == pb.StepOutcome_STEP_OUTCOME_UNCONFIRMED:
			// The screen is resyncing, the agent is busy, or the link blipped:
			// read again (the retry ticker only kicks steps waiting to be sent).
			rt.wakeAfter(run, stepRetryEvery)
			return false
		case resp.GetOutcome() == pb.StepOutcome_STEP_OUTCOME_AGENT_LOCAL:
			return rt.finishStep(run)
		}
		return rt.haltSent(run, resp.GetNote())
	}
	now := rt.svc.clock.Now()
	rt.mu.Lock()
	m := rt.seq.marksFor(run)
	if m.sentSeen.IsZero() {
		m.sentSeen = now
	}
	waited := now.Sub(m.sentSeen)
	rt.mu.Unlock()
	if waited < sentGrace {
		rt.wakeAfter(run, sentGrace-waited)
		return false
	}
	return rt.haltSent(run, "")
}

// haltSent halts a SENT step for the developer: may have run.
func (rt *runtime) haltSent(run *pb.Run, note string) bool {
	var fx stepFx
	live, ok := rt.stepTx(run, func(l *pb.Run) bool {
		if note == "" {
			note = noEvidenceNote
			if l.GetStepUnconfirmed() {
				note = unconfirmedNote
			}
		}
		l.StepSentBeforeRestart = !l.GetStepUnconfirmed()
		fx = haltLocked(l, note)
		return true
	})
	if ok {
		rt.applyFx(live, fx)
	}
	return true
}

// finishStep finishes the current step and advances.
func (rt *runtime) finishStep(run *pb.Run) bool {
	var fx stepFx
	live, ok := rt.stepTx(run, func(l *pb.Run) bool {
		fx = advanceLocked(l)
		return true
	})
	if ok {
		rt.dropMarks(live.GetId())
		rt.applyFx(live, fx)
	}
	return true
}

// stepFinished reports whether a TURN step's turn has ended, by evidence after
// its send (FR-206a/b): IDLE with a turn end after it; for a step classified
// only by a needs-input report, IDLE with any report after it.
func stepFinished(run *pb.Run, sb *pb.Sandbox) bool {
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING || sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE {
		return false
	}
	sent, a := run.GetStepSentAt(), sb.GetAgent()
	if after(a.GetLastTurnEndAt(), sent) {
		return true
	}
	return run.GetStepKindSeen() == pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY && after(a.GetLastEventAt(), sent)
}

// finishTurn finishes a TURN step once its turn ended. A step is also
// finished once the input box has stayed empty and ready for inputOnlyIdle
// after its send (the idle-nudge fallback, research R1), timed on this clock
// from when the period was first seen: for a step classified only by a
// needs-input report, and for any step whose agent is IDLE (a turn interrupted
// with Esc, or ended by an error, may report no turn end).
func (rt *runtime) finishTurn(run *pb.Run, sb *pb.Sandbox) bool {
	if sb == nil {
		return false
	}
	if stepFinished(run, sb) {
		return rt.finishStep(run)
	}
	idle := sb.GetAgent().GetInputIdleSince()
	inputOnly := run.GetStepKindSeen() == pb.StepObserved_STEP_OBSERVED_MODEL_TURN_INPUT_ONLY
	if (!inputOnly && sb.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_IDLE) || !after(idle, run.GetStepSentAt()) {
		return false
	}
	now := rt.svc.clock.Now()
	rt.mu.Lock()
	m := rt.seq.marksFor(run)
	if !m.idleSince.Equal(idle.AsTime()) {
		m.idleSince, m.idleSeen = idle.AsTime(), now
	}
	waited := now.Sub(m.idleSeen)
	rt.mu.Unlock()
	if waited >= inputOnlyIdle {
		return rt.finishStep(run)
	}
	rt.wakeAfter(run, inputOnlyIdle-waited)
	return false
}

// --- closing a run with steps ---

// closeStepsLocked decides a closing run's step work (caller holds runs.mu,
// the run already closed): reported reports whether it closed by a completion
// report or a mark free (cleanup follows) rather than failing or being
// cancelled. It returns the effects, and false for a run with no steps on a
// sandbox (closed as before).
func (rt *runtime) closeStepsLocked(l *pb.Run, reported bool) (stepFx, bool) {
	if !sequenced(l) || l.GetSandboxId() == "" {
		if sequenced(l) {
			l.StepStatus = pb.StepStatus_STEP_STATUS_DONE
		}
		return stepFx{}, false
	}
	sb := rt.sandboxRecord(l.GetSandboxId())
	if reported {
		return rt.beginCleanupLocked(l, sb), true
	}
	if l.GetStepStatus() == pb.StepStatus_STEP_STATUS_SENDING {
		return stepFx{kick: true}, true // the stepper resolves the send first
	}
	return rt.failCleanupLocked(l, sb, typedFor(l, sb)), true
}

// beginCleanupLocked starts the cleanup of a run closed by its report or a
// mark free (FR-214, FR-215): the sandbox stays held (CLEANING_UP) until the
// cleanup steps finished. With none, the sequence is DONE. When its agent
// can't be typed into, the steps are dropped and the developer frees it.
func (rt *runtime) beginCleanupLocked(l *pb.Run, sb *pb.Sandbox) stepFx {
	first := stepIndex(l, pb.StepRole_STEP_ROLE_CLEANUP, pb.StepKind_STEP_KIND_UNSPECIFIED)
	if first < 0 {
		moveTo(l, int(l.GetStep()), pb.StepStatus_STEP_STATUS_DONE)
		return stepFx{clear: true}
	}
	if !canType(sb) {
		moveTo(l, first, pb.StepStatus_STEP_STATUS_PENDING) // what was left, for the dropped note
		return rt.toDeveloperLocked(l, sb)
	}
	l.Hold = pb.WorkerHold_WORKER_HOLD_CLEANING_UP
	l.HoldSince = timestamppb.New(rt.svc.clock.Now())
	moveTo(l, first, pb.StepStatus_STEP_STATUS_PENDING)
	return stepFx{sync: true, kick: true}
}

// failCleanupLocked decides what follows a run that failed or was cancelled
// (FR-214 as amended): after its action prompt was delivered, or when nothing
// was typed, nothing; otherwise Clear only, if enabled and the agent is live.
// A detached agent can't be cleared, so the developer frees it.
func (rt *runtime) failCleanupLocked(l *pb.Run, sb *pb.Sandbox, typed bool) stepFx {
	done := func() stepFx {
		l.Hold = pb.WorkerHold_WORKER_HOLD_NONE
		moveTo(l, int(l.GetStep()), pb.StepStatus_STEP_STATUS_DONE)
		return stepFx{clear: true}
	}
	if actionAccepted(l) || !typed {
		return done()
	}
	if sb != nil && agentCheck(sb) == pb.AgentCheck_STATE_DETACHED {
		return rt.toDeveloperLocked(l, sb)
	}
	c := clearIndex(l)
	if c < 0 || !canType(sb) {
		return done()
	}
	l.Hold = pb.WorkerHold_WORKER_HOLD_CLEANING_UP
	l.HoldSince = timestamppb.New(rt.svc.clock.Now())
	moveTo(l, c, pb.StepStatus_STEP_STATUS_PENDING)
	return stepFx{sync: true, kick: true}
}

// toDeveloperLocked drops a run's remaining cleanup and holds the sandbox for
// the developer (FR-216): only a fresh session (startup or clear), seen by its
// daemon, frees it. The run stays on its Clear step, which R may retry.
func (rt *runtime) toDeveloperLocked(l *pb.Run, sb *pb.Sandbox) stepFx {
	note := ""
	if agentCheck(sb) == pb.AgentCheck_STATE_DETACHED {
		note = orphanCloseNote
		from := int(l.GetStep())
		for i, s := range l.GetSteps() {
			if i >= from && s.GetRole() == pb.StepRole_STEP_ROLE_CLEANUP && s.GetKind() == pb.StepKind_STEP_KIND_CUSTOM {
				note += "; " + droppedPostNote
				break
			}
		}
	}
	if l.GetHold() != pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER {
		l.HoldSince = timestamppb.New(rt.svc.clock.Now())
	}
	l.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER
	at := clearIndex(l)
	if at < 0 {
		at = int(l.GetStep())
	}
	moveTo(l, at, pb.StepStatus_STEP_STATUS_DONE)
	l.StepNote = note
	return stepFx{sync: true}
}

// freeCleanup is a mark free of a worker whose closed run is cleaning up while
// its agent asks for input (FR-216): the remaining custom post-prompts are
// dropped and the hold becomes NEEDS_INPUT_FREEING; Clear (if enabled) is then
// sent under the normal guard, once the question is gone and the box is empty
// and ready, and the worker is free once it finished. With no Clear it is free
// at once. The run's outcome is never changed. A sandbox held otherwise is
// refused with what frees it. held is false when the sandbox has no held run
// (MarkWorkerFree goes on as before).
func (rt *runtime) freeCleanup(sandboxID string) (out *pb.Run, held bool, err error) {
	run := rt.seqRunOn(sandboxID)
	if run == nil || !finished(run) {
		return nil, false, nil
	}
	switch run.GetHold() {
	case pb.WorkerHold_WORKER_HOLD_CLEANING_UP:
	case pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER:
		return nil, true, fmt.Errorf("%w: sandbox %s is held after run %s until a fresh session starts: run /clear in its terminal (t → Replace for a detached agent)", ErrPrecondition, sandboxID, run.GetId())
	case pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING:
		return nil, true, fmt.Errorf("%w: sandbox %s is already being freed after run %s", ErrPrecondition, sandboxID, run.GetId())
	default:
		return nil, false, nil
	}
	if rt.sandboxRecord(sandboxID).GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_NEEDS_INPUT {
		return nil, true, fmt.Errorf("%w: sandbox %s is cleaning up after run %s; it can be freed only while its agent asks for input", ErrPrecondition, sandboxID, run.GetId())
	}
	var fx stepFx
	live, ok := rt.stepTx(run, func(l *pb.Run) bool {
		switch st := curStep(l); {
		case l.GetStepStatus() == pb.StepStatus_STEP_STATUS_SENDING:
			err = fmt.Errorf("%w: a cleanup step of run %s is being sent; try again", ErrPrecondition, l.GetId())
			return false
		case st.GetKind() == pb.StepKind_STEP_KIND_CLEAR && l.GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED:
			err = fmt.Errorf("%w: Clear of run %s is halted; retry or fail it from the run", ErrPrecondition, l.GetId())
			return false
		}
		c := clearIndex(l)
		if c < 0 {
			l.Hold = pb.WorkerHold_WORKER_HOLD_NONE
			moveTo(l, int(l.GetStep()), pb.StepStatus_STEP_STATUS_DONE)
			fx = stepFx{clear: true}
			return true
		}
		l.Hold = pb.WorkerHold_WORKER_HOLD_NEEDS_INPUT_FREEING
		l.HoldSince = timestamppb.New(rt.svc.clock.Now())
		if int(l.GetStep()) != c {
			moveTo(l, c, pb.StepStatus_STEP_STATUS_PENDING)
		}
		fx = stepFx{sync: true, kick: true}
		return true
	})
	switch {
	case err != nil:
		return nil, true, err
	case !ok:
		return nil, true, fmt.Errorf("%w: run %s moved on; reload", ErrPrecondition, run.GetId())
	}
	rt.dropMarks(live.GetId())
	rt.applyFx(live, fx)
	return proto.Clone(live).(*pb.Run), true, nil
}

// --- developer resolution (FR-222) ---

// ResolveRunStep retries, skips or fails a run's halted step (FR-222,
// contracts/proto-delta.md), or retries Clear under a NEEDS_DEVELOPER hold.
// step must be the run's current step (a stale view is refused).
func (s *Service) ResolveRunStep(id, runID string, step int32, res pb.StepResolution) (*pb.Run, error) {
	rt, err := s.runtimeFor(id)
	if err != nil {
		return nil, err
	}
	return rt.resolveRunStep(runID, step, res)
}

func (rt *runtime) resolveRunStep(runID string, step int32, res pb.StepResolution) (*pb.Run, error) {
	switch res {
	case pb.StepResolution_STEP_RESOLUTION_RETRY, pb.StepResolution_STEP_RESOLUTION_SKIP, pb.StepResolution_STEP_RESOLUTION_FAIL:
	default:
		return nil, fmt.Errorf("%w: resolution must be retry, skip or fail", ErrInvalid)
	}
	moved := fmt.Errorf("%w: the run moved on; reload", ErrPrecondition)
	rt.runs.mu.Lock()
	run, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil {
		rt.runs.mu.Unlock()
		return nil, notFound(err)
	}
	st := curStep(run)
	halted := run.GetStepStatus() == pb.StepStatus_STEP_STATUS_HALTED
	clearHeld := st.GetKind() == pb.StepKind_STEP_KIND_CLEAR && run.GetHold() == pb.WorkerHold_WORKER_HOLD_NEEDS_DEVELOPER &&
		run.GetStepStatus() == pb.StepStatus_STEP_STATUS_DONE
	switch {
	case st == nil || step != run.GetStep() || (!halted && !clearHeld):
		rt.runs.mu.Unlock()
		return nil, moved
	case res == pb.StepResolution_STEP_RESOLUTION_SKIP && st.GetKind() == pb.StepKind_STEP_KIND_CLEAR:
		rt.runs.mu.Unlock()
		return nil, fmt.Errorf("%w: Clear cannot be skipped", ErrPrecondition)
	case !halted && res != pb.StepResolution_STEP_RESOLUTION_RETRY:
		rt.runs.mu.Unlock()
		return nil, moved
	case res == pb.StepResolution_STEP_RESOLUTION_SKIP && st.GetKind() == pb.StepKind_STEP_KIND_ACTION_PROMPT:
		rt.runs.mu.Unlock()
		return nil, fmt.Errorf("%w: the action prompt cannot be skipped; retry or fail", ErrPrecondition)
	case res == pb.StepResolution_STEP_RESOLUTION_SKIP && restartHalt(run):
		rt.runs.mu.Unlock()
		return nil, fmt.Errorf("%w: the agent was replaced; restart setup with R", ErrPrecondition)
	}
	var fx stepFx
	failReason := ""
	switch res {
	case pb.StepResolution_STEP_RESOLUTION_RETRY:
		if restartHalt(run) {
			// The setup done so far died with the detached agent: start over
			// from step 0, every step a new send, the first one restarting the
			// agent (research R14), so the action prompt never reaches an
			// agent without its model and effort (SC-001).
			moveTo(run, 0, pb.StepStatus_STEP_STATUS_PENDING)
			run.StepNote = restartingNote
		} else {
			// The same send again: the worker accepts it once while halted.
			run.StepStatus = pb.StepStatus_STEP_STATUS_PENDING
			run.StepNote = ""
			run.StepUnconfirmed, run.StepSentBeforeRestart = false, false
		}
		fx = stepFx{kick: true}
	case pb.StepResolution_STEP_RESOLUTION_SKIP:
		fx = advanceLocked(run)
		fx.sync, fx.kick = !fx.clear, true
	case pb.StepResolution_STEP_RESOLUTION_FAIL:
		switch {
		case st.GetRole() != pb.StepRole_STEP_ROLE_CLEANUP:
			// FR-212: the run fails naming the step; Clear only follows.
			failReason = "failed by the developer at " + stepLabel(run)
		case st.GetKind() == pb.StepKind_STEP_KIND_CLEAR:
			// The sandbox needs the developer; R stays on the row (FR-222).
			fx = rt.toDeveloperLocked(run, rt.sandboxRecord(run.GetSandboxId()))
		default:
			// A custom post-prompt: the rest of them are dropped, Clear still
			// follows (FR-216, FR-222).
			if c := clearIndex(run); c >= 0 {
				moveTo(run, c, pb.StepStatus_STEP_STATUS_PENDING)
				fx = stepFx{sync: true, kick: true}
			} else {
				run.Hold = pb.WorkerHold_WORKER_HOLD_NONE
				moveTo(run, int(run.GetStep()), pb.StepStatus_STEP_STATUS_DONE)
				fx = stepFx{clear: true}
			}
		}
	}
	if failReason == "" {
		if err := rt.putRun(run); err != nil {
			rt.runs.mu.Unlock()
			return nil, err
		}
	}
	rt.runs.mu.Unlock()
	rt.dropMarks(runID)
	if failReason != "" {
		rt.failRun(run, failReason)
		rt.kickStepper(run.GetSandboxId())
	} else {
		rt.applyFx(run, fx)
	}
	out, err := rt.svc.reg.GetRun(rt.id, runID)
	if err != nil {
		return nil, notFound(err)
	}
	return out, nil
}

// --- reconcile against the daemon's records (T022) ---

// clearStaleRecord clears a worker record naming a run of this orchestrator
// that is finished and has no step work left (or is unknown): a cancel/claim
// race, or a clearing push lost on a down link, never keeps a worker taken.
func (rt *runtime) clearStaleRecord(sb *pb.Sandbox) {
	wr := sb.GetController().GetRun()
	if wr == nil || sb.GetController().GetOrchestratorId() != rt.id {
		return
	}
	run, err := rt.svc.reg.GetRun(rt.id, wr.GetId())
	switch {
	case err != nil && !errors.Is(notFound(err), ErrNotFound):
		return
	case err == nil && (!finished(run) || stepActive(run)):
		return
	}
	hostID := hostOf(rt.record(), sb.GetId())
	if hostID == "" {
		return
	}
	l := rt.workerPushLock(sb.GetId())
	l.Lock()
	defer l.Unlock()
	if cur := rt.sandboxRecord(sb.GetId()).GetController().GetRun(); cur.GetId() != wr.GetId() {
		return
	}
	rt.setWorkerRun(hostID, sb.GetId(), nil, wr.GetId())
}

// stepsUnsupported reports whether a sandbox's daemon refused step delivery
// since its link last connected (research R6).
func (rt *runtime) stepsUnsupported(sandboxID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.seq.noSteps[sandboxID]
}

// forgetUnsupported clears the marks of a host's workers (its link
// reconnected: its daemon may have been updated).
func (rt *runtime) forgetUnsupported(hostID string) {
	rec := rt.record()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, w := range rec.GetWorkers() {
		if w.GetHostId() == hostID {
			delete(rt.seq.noSteps, w.GetSandboxId())
		}
	}
}

// noStepWorker reports whether a run with steps has workers its lanes allow
// and every one of them predates step delivery: it can never be delivered (R6).
func (rt *runtime) noStepWorker(rec *pb.Orchestrator, ids []string, mode pb.LaneMode) bool {
	seen := false
	for _, w := range rec.GetWorkers() {
		if w.GetReasoning() || w.GetLanesPending() || !allowed(w.GetLaneIds(), ids, mode) {
			continue
		}
		if !rt.stepsUnsupported(w.GetSandboxId()) {
			return false
		}
		seen = true
	}
	return seen
}

// claim records a worker on a still-queued run with steps (research R5): the
// run stays QUEUED until its first step is typed, and its stepper takes over.
func (rt *runtime) claim(run *pb.Run, w *pb.Worker) bool {
	rt.runs.mu.Lock()
	live, err := rt.svc.reg.GetRun(rt.id, run.GetId())
	if err != nil || live.GetState() != pb.RunState_RUN_STATE_QUEUED || live.GetSandboxId() != "" {
		rt.runs.mu.Unlock()
		return false
	}
	live.SandboxId, live.SandboxHostId = w.GetSandboxId(), w.GetHostId()
	moveTo(live, 0, pb.StepStatus_STEP_STATUS_PENDING)
	err = rt.putRun(live)
	rt.runs.mu.Unlock()
	if err != nil {
		return false
	}
	rt.kickStepper(w.GetSandboxId())
	return true
}

// actionTyped reports whether the daemon recorded the action step's send while
// its answer is still unknown (SENDING, SENT, or halted as "may have run"):
// only the answer was lost, so the agent's completion report is accepted and
// closes the run as usual (FR-211, research R5).
func actionTyped(run *pb.Run, sb *pb.Sandbox) bool {
	if int(run.GetStep()) != actionIndex(run) {
		return false
	}
	switch run.GetStepStatus() {
	case pb.StepStatus_STEP_STATUS_SENT:
		return true
	case pb.StepStatus_STEP_STATUS_HALTED:
		return run.GetStepSentAt() != nil && (run.GetStepUnconfirmed() || run.GetStepSentBeforeRestart())
	case pb.StepStatus_STEP_STATUS_SENDING:
		wr := sb.GetController().GetRun()
		return wr.GetId() == run.GetId() && wr.GetStepIndex() == run.GetStepSend() && wr.GetStepSentAt() != nil &&
			(run.GetStepSentAt() == nil || !proto.Equal(wr.GetStepSentAt(), run.GetStepSentAt()))
	}
	return false
}
