package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	swb "github.com/jamesclark123/switchboard/libs/switchboard-proto"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Detached agents after an owning-daemon restart (SWI-17, research R14). The
// agent outlives the daemon (agent.agentCommand), which then has no handle on
// it: an orphan. The daemon detects and surfaces orphans and never stops one
// on its own. Whether a sandbox has one is decided by a probe (one read-only
// `sbx exec`, agent.ProbeScript) of a RUNNING sandbox, never by state flags:
//
//   - at daemon start, only for a running sandbox that may have a surviving
//     agent (an unretired hook token; on the first start after the upgrade, a
//     set, non-EXITED status), idle workers first, then workers with a run,
//     then the rest (SWI-25 A4), at most orphanProbeConcurrency at a time;
//   - when an agent's exec client exits without the registry closing it;
//   - on a gate refusal (debounced), a main-agent SessionEnd, and every
//     orphanBackstop while an orphan or an unknown sandbox holds a run;
//   - once when a lifecycle operation leaves a sandbox RUNNING with one.
//
// Until a probe answers the sandbox is *unknown* and the gate refuses every
// spawn. Alive with no live session of its generation is an orphan record
// (surfaced once with AGENT_DETACHED, or, for those the start-up check finds,
// once for all of them with AGENTS_DETACHED_AT_START; SWI-25 A2); gone drops the record, retires the
// generation's token and writes EXITED (the gone rule). Each sandbox's state
// is published as AgentSession.agent_check, written only when it changes.
//
// Lock order: the store's write transaction may take o.mu (check); o.mu is
// never held across a store, runner or agent registry call.

// Bounds (data-model.md). Variables so tests can shorten them.
var (
	orphanProbeConcurrency = 8
	orphanProbeTimeout     = 10 * time.Second
	orphanBackstop         = 60 * time.Second
	orphanUnknownLimit     = 60 * time.Second
	orphanKillWait         = 5 * time.Second
	orphanRefusalDebounce  = 10 * time.Second
	orphanRetryBase        = time.Second
	orphanRetryCap         = 60 * time.Second
)

// orphanMaxFailures failed probes in a row surface a sandbox CHECK_FAILED.
const orphanMaxFailures = 3

// replacedNote is the run's step note when the developer replaces the agent
// of an ACTION run (research R14): the orchestrator fails the run with it.
const replacedNote = "agent replaced by the developer after a daemon restart"

// detachedPrefix opens every detached-agent note; the orchestrator's halt
// notes and the TUI's row note prefix-match on it (worker/step.go restartHalt,
// orchestrator/sequence.go, T/run_steps.go), so only a suffix is ever added.
const detachedPrefix = "agent detached by a daemon restart"

// detachedNote is the AGENT_DETACHED notification: the prefix, the agent's
// last state, time and process count (SWI-25 A6, swb.DetachedDetail), and
// what to do.
func detachedNote(check *pb.AgentCheck) string {
	return detachedPrefix + swb.DetachedDetail(check, time.Time{}) + " — t to Replace or Wait"
}

// ReapMode is how ReapOrphan finishes: a Replace writes EXITED (and fails an
// ACTION run with replacedNote); a restart (R on a restart halt) writes none,
// since the fresh agent's STARTING follows directly.
type ReapMode int

const (
	ReapReplace ReapMode = iota
	ReapRestart
)

// OrphanAgents is the agent registry side the tracker reads (agent.Registry).
type OrphanAgents interface {
	LiveGen(sandboxID string) (gen int64, live bool)
}

// OrphanTokens is the hook-token index the tracker reads and retires
// (agent.HookTokens).
type OrphanTokens interface {
	Unretired(sandboxID string) []int64
	Generation(sandboxID string) int64
	Retire(sandboxID string, gen int64)
	BindFromProbe(sandboxID string, p agent.ProbedAgent) error
}

// OrphanNotifier emits the AGENT_DETACHED and AGENT_CHECK_FAILED
// notifications, and the host-level AGENTS_DETACHED_AT_START one (agent.Hub).
type OrphanNotifier interface {
	EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent
	EmitHostNotification(kind pb.NotificationKind, message string, affected []*pb.NotificationEvent_AffectedSandbox, now time.Time) *pb.NotificationEvent
}

// OrphanDeps wires the tracker (SetOrphanDeps). Every field MAY be nil.
type OrphanDeps struct {
	Agents OrphanAgents
	Tokens OrphanTokens
	Notify OrphanNotifier
	// OnClear is called with the record after a probe found a sandbox's agent
	// gone, so a worker's agent is warm-started again (worker.WarmStart).
	OnClear func(sb *pb.Sandbox)
}

// orphanRecord is a detached agent: its generation, the live matched
// processes, and the status and time it was last seen at.
type orphanRecord struct {
	gen        int64
	procs      int
	lastStatus pb.AgentStatus
	lastSeen   *timestamppb.Timestamp
}

// noteOverlay is the check-failed note the daemon put on a worker run's
// step_note (SWI-25 A3) and the note it replaced: shown is restored to saved
// once the check clears or finds the agent, unless something else wrote the
// note meanwhile.
type noteOverlay struct {
	runID, saved, shown string
}

// orphanState is one sandbox's check.
type orphanState struct {
	unknown   bool
	record    *orphanRecord
	failures  int       // failed probes in a row
	firstTry  time.Time // the first probe attempt of this unknown spell
	surfaced  bool      // CHECK_FAILED
	lastErr   string
	overlay   *noteOverlay // the run note while surfaced (kept across drop until restored)
	probing   bool         // single flight
	again     bool         // a probe was asked for while one ran
	retry     *time.Timer
	refusedAt time.Time
	// epoch counts drops (a reap, a confirmed stop, a clear): a probe records
	// its result only if none happened since it took the orphan gate, so a
	// stale "alive" never lands over a reap or stop that slipped in after it.
	epoch uint64
}

type orphans struct {
	m   *Manager
	sem chan struct{}

	mu    sync.Mutex
	st    map[string]*orphanState
	deps  OrphanDeps
	batch *startBatch // the start-up check's probes, while any is pending (SWI-25 A2)
}

// startBatch is the start-up check's set of probes (SWI-25 A2): the orphans
// they find are not notified one by one but once, when the last of them has
// ended, as a host-level AGENTS_DETACHED_AT_START (a lone one keeps its own
// AGENT_DETACHED). A probe that ends without an answer (dropped, or failed
// and retrying) leaves the batch; an orphan it finds later is notified alone.
type startBatch struct {
	pending map[string]bool
	found   []batchFound
}

// batchFound is one orphan the start-up batch found: its notice, had it been
// alone, and its entry in the host-level one.
type batchFound struct {
	id, msg  string
	affected *pb.NotificationEvent_AffectedSandbox
}

func newOrphans(m *Manager) *orphans {
	return &orphans{m: m, sem: make(chan struct{}, orphanProbeConcurrency), st: map[string]*orphanState{}}
}

// SetOrphanDeps wires the detached-agent tracker (SWI-17, research R14).
func (m *Manager) SetOrphanDeps(d OrphanDeps) {
	m.orphans.mu.Lock()
	defer m.orphans.mu.Unlock()
	m.orphans.deps = d
}

func (o *orphans) getDeps() OrphanDeps {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.deps
}

// of returns a sandbox's state, creating it. Callers hold mu.
func (o *orphans) of(id string) *orphanState {
	st := o.st[id]
	if st == nil {
		st = &orphanState{}
		o.st[id] = st
	}
	return st
}

// pending reports whether a sandbox has an orphan record or is unknown.
func (o *orphans) pending(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.st[id]
	return st != nil && (st.unknown || st.record != nil)
}

// refusal is the gate's decision from the orphan state alone.
func (o *orphans) refusal(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.st[id]
	switch {
	case st == nil:
		return nil
	case st.record != nil:
		return agent.ErrDetached
	case st.unknown:
		return agent.ErrChecking
	}
	return nil
}

// refused schedules a re-probe after a gate refusal, at most one per sandbox
// per orphanRefusalDebounce: whatever tries to spawn learns within seconds
// that an orphan is gone.
func (o *orphans) refused(id string) {
	o.mu.Lock()
	st := o.st[id]
	now := time.Now()
	if st == nil || (!st.refusedAt.IsZero() && now.Sub(st.refusedAt) < orphanRefusalDebounce) {
		o.mu.Unlock()
		return
	}
	st.refusedAt = now
	// A probe in flight (e.g. the start-up one) answers this refusal too.
	inFlight := st.probing
	o.mu.Unlock()
	if !inFlight {
		go o.probe(id)
	}
}

// markUnknown marks a sandbox unknown at once (the gate refuses from here on)
// and publishes the check asynchronously: callers run inside the agent
// registry's exit path or a change observer.
func (o *orphans) markUnknown(id string) {
	o.setUnknown(id)
	go o.write(id, nil)
}

func (o *orphans) setUnknown(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.of(id)
	if !st.unknown && st.record == nil {
		st.failures, st.firstTry, st.surfaced, st.lastErr = 0, time.Time{}, false, ""
	}
	st.unknown = true
}

// check is the AgentCheck a sandbox's state publishes (nil = clear). It is
// read inside the store's write transaction (write).
func (o *orphans) check(id string) *pb.AgentCheck {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.st[id]
	switch {
	case st == nil:
		return nil
	case st.record != nil:
		return &pb.AgentCheck{
			State: pb.AgentCheck_STATE_DETACHED, LastStatus: st.record.lastStatus,
			LastSeenAt: st.record.lastSeen, Processes: int32(st.record.procs),
		}
	case st.unknown && st.surfaced:
		return &pb.AgentCheck{State: pb.AgentCheck_STATE_CHECK_FAILED, Error: st.lastErr}
	case st.unknown:
		return &pb.AgentCheck{State: pb.AgentCheck_STATE_UNKNOWN}
	}
	return nil
}

// errNoChange skips a write that would change nothing.
var errNoChange = errors.New("no change")

// write publishes the sandbox's current check, together with extra's changes,
// in one record update, and emits it only when something changed. Writes from
// several goroutines converge: each publishes the state current at its write.
func (o *orphans) write(id string, extra func(s *pb.Sandbox)) *pb.Sandbox {
	out := o.store(id, extra)
	if out != nil {
		o.m.emit(out)
	}
	return out
}

// store is write without the publish: the record is updated, nobody is told.
func (o *orphans) store(id string, extra func(s *pb.Sandbox)) *pb.Sandbox {
	out, err := o.m.store.Update(id, func(s *pb.Sandbox) error {
		before := proto.Clone(s).(*pb.Sandbox)
		want := o.check(id)
		if !proto.Equal(s.GetAgent().GetAgentCheck(), want) {
			if s.Agent == nil {
				s.Agent = &pb.AgentSession{}
			}
			s.Agent.AgentCheck = want
		}
		o.noteOverlay(id, s, want)
		if extra != nil {
			extra(s)
		}
		if proto.Equal(before, s) {
			return errNoChange
		}
		s.UpdatedAt = timestamppb.Now()
		return nil
	})
	if err != nil {
		return nil
	}
	return out
}

// noteOverlay keeps a worker run's step_note in step with the published check
// (SWI-25 A3), inside the store's write transaction: while the check is
// CHECK_FAILED the note is the failure (swb.CheckFailedNote), and the note it
// replaced is kept; once the check is anything else the kept note is restored,
// unless the note was changed under the overlay (then that change stands). A
// note written under the overlay by someone else becomes the one to restore.
// extra runs after it, so a Replace's replacedNote still wins.
func (o *orphans) noteOverlay(id string, s *pb.Sandbox, want *pb.AgentCheck) {
	run := s.GetController().GetRun()
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.st[id]
	if want.GetState() == pb.AgentCheck_STATE_CHECK_FAILED && run != nil {
		if st == nil {
			return // check() read a state that is gone: nothing to overlay
		}
		shown := swb.CheckFailedNote(want.GetError())
		ov := st.overlay
		if ov == nil || ov.runID != run.GetId() {
			ov = &noteOverlay{runID: run.GetId(), saved: run.GetStepNote()}
			st.overlay = ov
		} else if cur := run.GetStepNote(); cur != ov.shown && !swb.IsCheckFailedNote(cur) {
			ov.saved = cur
		}
		ov.shown = shown
		run.StepNote = shown
		return
	}
	if st == nil || st.overlay == nil {
		return
	}
	ov := st.overlay
	st.overlay = nil
	if run != nil && run.GetId() == ov.runID && run.GetStepNote() == ov.shown {
		run.StepNote = ov.saved
	}
}

// exitedOnce is the gone rule's write: EXITED only when the recorded status
// is set and not EXITED already, so an agent's exit is never handled twice.
// It keeps SetAgentStatus's rule for a run with cleanup still to run.
func (o *orphans) exitedOnce(s *pb.Sandbox) {
	if st := s.GetAgent().GetStatus(); st != pb.AgentStatus_AGENT_STATUS_UNSPECIFIED && st != pb.AgentStatus_AGENT_STATUS_EXITED {
		o.exited(s)
	}
}

// exited writes EXITED on the record.
func (o *orphans) exited(s *pb.Sandbox) {
	if s.Agent == nil {
		s.Agent = &pb.AgentSession{}
	}
	s.Agent.Status = pb.AgentStatus_AGENT_STATUS_EXITED
	s.Agent.LastEventAt = timestamppb.Now()
	if run := s.GetController().GetRun(); cleanupPending(run) {
		holdForDeveloper(run, o.m.agentGen(s.GetId()))
	}
}

// published runs the outcome rule's STOPPED/destroyed branch on a confirmed
// stop or destroy (a published STOPPED, or the DESTROYING emitted after the
// record is deleted), including one Announce picked up from outside: the
// container's processes are gone, so the record and unknown go, and EXITED by
// the gone rule. CREATING and ERROR never clear anything: the container may
// still be up. The tokens are retired by the change observer (grpc server).
func (o *orphans) published(sb *pb.Sandbox) {
	switch sb.GetState() {
	case pb.SandboxState_SANDBOX_STATE_STOPPED, pb.SandboxState_SANDBOX_STATE_DESTROYING:
	default:
		return
	}
	if !o.drop(sb.GetId()) || sb.GetState() == pb.SandboxState_SANDBOX_STATE_DESTROYING {
		return
	}
	o.write(sb.GetId(), o.exitedOnce)
}

// drop forgets a sandbox's record and unknown, reporting whether it had
// either. A pending retry is cancelled.
func (o *orphans) drop(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := o.st[id]
	if st == nil {
		return false
	}
	had := st.unknown || st.record != nil
	st.epoch++
	if st.retry != nil {
		st.retry.Stop()
		st.retry = nil
	}
	st.unknown, st.record, st.failures, st.firstTry, st.surfaced, st.lastErr = false, nil, 0, time.Time{}, false, ""
	return had
}

// retireGone retires the sandbox's unretired generations other than a live
// session's: their agents are known gone.
func (o *orphans) retireGone(id string) {
	d := o.getDeps()
	if d.Tokens == nil {
		return
	}
	var liveGen int64
	live := false
	if d.Agents != nil {
		liveGen, live = d.Agents.LiveGen(id)
	}
	for _, g := range d.Tokens.Unretired(id) {
		if !live || g != liveGen {
			d.Tokens.Retire(id, g)
		}
	}
}

// probeOutcome is what one probe attempt found.
type probeOutcome int

const (
	probeDropped probeOutcome = iota // not run: not RUNNING, or an operation holds the lock
	probeFailed
	probeGone
	probeAlive
)

// probe checks one sandbox, single-flight: a probe asked for while one runs
// runs once more after it.
func (o *orphans) probe(id string) { o.probeIn(id, false) }

// probeIn is probe for a caller that may already hold a slot of sem for the
// first attempt (held): the start-up feeder, which takes the slot before it
// starts the probe so that probes start in its order (SWI-25 A4). The slot is
// released here either way.
func (o *orphans) probeIn(id string, held bool) {
	o.mu.Lock()
	st := o.of(id)
	if st.probing {
		st.again = true
		o.mu.Unlock()
		if held {
			<-o.sem
		}
		return
	}
	st.probing = true
	o.mu.Unlock()
	for {
		if !held {
			o.sem <- struct{}{}
		}
		held = false
		out, epoch, res, err := o.probeOnce(id)
		<-o.sem
		o.apply(id, epoch, out, res, err)
		o.mu.Lock()
		if !st.again {
			st.probing = false
			o.mu.Unlock()
			o.batchDone(id)
			return
		}
		st.again = false
		o.mu.Unlock()
	}
}

// running re-reads the record and the runtime's listing: the probe runs only
// against a sandbox that is RUNNING in both, since `sbx exec` would start a
// stopped one. A listing that says stopped under a RUNNING record fails the
// probe (never "gone", which would warm-start into the stopped sandbox): the
// sandbox stays unknown until a reconcile publishes the stop. A probe reads
// the shared listing (listShared, at most listSharedMaxAge old); with fresh
// (a reap: the developer's stop, rare, and the one that signals processes) the
// runtime is listed anew.
func (o *orphans) running(ctx context.Context, id string, fresh bool) (*pb.Sandbox, probeOutcome, error) {
	sb, err := o.m.store.Get(id)
	if err != nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return nil, probeDropped, nil
	}
	list := o.m.listShared
	if fresh {
		list = o.m.runner.ListStatuses
	}
	statuses, err := list(ctx)
	if err != nil {
		return nil, probeFailed, fmt.Errorf("list sandboxes: %w", err)
	}
	state, unknown := listedState(statuses, sb)
	switch {
	case unknown != "":
		return nil, probeFailed, fmt.Errorf("sandbox status %q", unknown)
	case state != pb.SandboxState_SANDBOX_STATE_RUNNING:
		return nil, probeFailed, fmt.Errorf("sandbox is listed %s", stateWord(state))
	}
	return sb, probeAlive, nil
}

// execScript runs a script in the sandbox (one `sbx exec`) under the agent
// launch's login shell (agent.agentCommand) and returns its stdout; a failure
// carries the script's stderr.
func (o *orphans) execScript(ctx context.Context, sb *pb.Sandbox, script string) (string, error) {
	ref := sb.GetContainerRef()
	if ref == "" {
		ref = sb.GetDisplayName()
	}
	cmd := o.m.runner.Exec(ctx, ref, []string{"bash", "-lc", script})
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return string(out), err
}

// probeOnce runs one probe under the orphan gate (try-lock: dropped while an
// operation holds the runtime lock, and retried after orphanRetryBase while
// the sandbox is pending, since a reap or a spawn holding the gate settles
// nothing). It returns the sandbox's epoch as of taking the gate.
func (o *orphans) probeOnce(id string) (probeOutcome, uint64, agent.ProbeResult, error) {
	o.mu.Lock()
	if st := o.st[id]; st != nil && st.firstTry.IsZero() {
		st.firstTry = time.Now() // the clock starts at the first attempt
	}
	o.mu.Unlock()
	if sb, err := o.m.store.Get(id); err != nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return probeDropped, 0, agent.ProbeResult{}, nil
	}
	unlock, err := o.m.orphanGate(id, gateProbing)
	if err != nil {
		o.mu.Lock()
		if st := o.st[id]; st != nil && (st.unknown || st.record != nil) {
			if st.retry != nil {
				st.retry.Stop()
			}
			st.retry = time.AfterFunc(orphanRetryBase, func() { o.probe(id) })
		}
		o.mu.Unlock()
		return probeDropped, 0, agent.ProbeResult{}, nil
	}
	defer unlock()
	o.mu.Lock()
	epoch := o.of(id).epoch
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), orphanProbeTimeout)
	defer cancel()
	sb, out, err := o.running(ctx, id, false)
	if out != probeAlive {
		return out, epoch, agent.ProbeResult{}, err
	}
	text, err := o.execScript(ctx, sb, agent.ProbeScript())
	if err != nil {
		return probeFailed, epoch, agent.ProbeResult{}, fmt.Errorf("probe: %w", err)
	}
	res, err := agent.ParseProbe(text)
	if err != nil {
		return probeFailed, epoch, res, err
	}
	if !res.Alive() {
		return probeGone, epoch, res, nil
	}
	return probeAlive, epoch, res, nil
}

// apply records a probe's outcome, unless the sandbox was dropped (reaped,
// stopped or cleared) since the probe took the orphan gate: the result is
// stale, and whatever dropped it has settled the sandbox.
func (o *orphans) apply(id string, epoch uint64, out probeOutcome, res agent.ProbeResult, perr error) {
	switch out {
	case probeDropped:
	case probeFailed:
		o.failed(id, epoch, perr)
	case probeGone:
		if o.current(id, epoch) {
			o.cleared(id, true)
		}
	case probeAlive:
		o.alive(id, epoch, res)
	}
}

// current reports whether no drop happened since epoch.
func (o *orphans) current(id string, epoch uint64) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.of(id).epoch == epoch
}

// failed leaves the sandbox unknown, retried with backoff (capped at
// orphanRetryCap). After orphanMaxFailures in a row, or orphanUnknownLimit
// since its first attempt, it is surfaced as CHECK_FAILED: the row note, one
// AGENT_CHECK_FAILED notification as it flips, and the failure overlaid on its
// run's step_note for as long as it lasts (noteOverlay; SWI-25 A3).
func (o *orphans) failed(id string, epoch uint64, perr error) {
	o.mu.Lock()
	st := o.of(id)
	if (!st.unknown && st.record == nil) || st.epoch != epoch {
		o.mu.Unlock()
		return // cleared meanwhile
	}
	st.failures++
	st.lastErr = perr.Error()
	surfacing := false
	if st.failures >= orphanMaxFailures || (!st.firstTry.IsZero() && time.Since(st.firstTry) >= orphanUnknownLimit) {
		surfacing = !st.surfaced && st.record == nil
		st.surfaced = true
	}
	if st.record != nil {
		// A detached agent stays detached until a probe finds it gone; the
		// next refusal, SessionEnd or backstop probes again.
		o.mu.Unlock()
		log.Printf("[warn] sandbox %s: re-check of a detached agent failed: %v", id, perr)
		return
	}
	backoff := min(time.Duration(1<<min(st.failures, 6))*orphanRetryBase, orphanRetryCap)
	if st.retry != nil {
		st.retry.Stop()
	}
	st.retry = time.AfterFunc(backoff, func() { o.probe(id) })
	o.mu.Unlock()
	log.Printf("[warn] sandbox %s: check for a detached agent failed: %v (retrying in %s)", id, perr, backoff)
	out := o.write(id, nil)
	if !surfacing {
		return
	}
	if d := o.getDeps(); d.Notify != nil {
		msg := swb.CheckFailedNote(perr.Error()) + " — t to Restart sandbox or Wait"
		if out == nil {
			out, _ = o.m.store.Get(id)
		}
		if run := out.GetController().GetRun(); run != nil {
			msg += " (run " + run.GetId() + ")"
		}
		d.Notify.EmitNotification(id, pb.NotificationKind_NOTIFICATION_KIND_AGENT_CHECK_FAILED, msg, time.Now())
	}
}

// cleared ends a sandbox's check: no agent is left beside a live session.
// With gone (a probe found every agent process gone) the generation's tokens
// are retired and EXITED written by the gone rule, unless a session is live.
func (o *orphans) cleared(id string, gone bool) {
	o.drop(id)
	d := o.getDeps()
	live := false
	if d.Agents != nil {
		_, live = d.Agents.LiveGen(id)
	}
	var extra func(*pb.Sandbox)
	if gone {
		o.retireGone(id)
		if !live {
			extra = o.exitedOnce
		}
	}
	sb := o.write(id, extra)
	if sb == nil {
		sb, _ = o.m.store.Get(id)
	}
	if gone && !live && d.OnClear != nil && sb != nil {
		d.OnClear(sb)
	}
}

// alive records what a probe found alive. With a live session of the same
// generation it is the daemon's own agent; otherwise it is an orphan record,
// surfaced once with AGENT_DETACHED. A run that still has cleanup to run (any
// hold, or a cleanup step) is held NEEDS_DEVELOPER: nothing can be typed into
// an orphan. A probe binds the generation's main PID from agent.pid when none
// is bound (BindFromProbe checks the process first).
func (o *orphans) alive(id string, epoch uint64, res agent.ProbeResult) {
	d := o.getDeps()
	gen := res.FileGen
	var liveGen int64
	live := false
	if d.Agents != nil {
		liveGen, live = d.Agents.LiveGen(id)
	}
	if live && (!res.File || gen == liveGen) {
		o.cleared(id, false)
		return
	}
	if d.Tokens != nil {
		unretired := d.Tokens.Unretired(id)
		if p, ok := res.Main(); ok && slices.Contains(unretired, p.Gen) {
			_ = d.Tokens.BindFromProbe(id, p)
		}
		// agent.pid is writable inside the sandbox: its generation counts only
		// when it is one the daemon issued and has not retired.
		if cur := d.Tokens.Generation(id); !res.File || gen > cur || !slices.Contains(unretired, gen) {
			gen = cur
		}
	}
	sb, err := o.m.store.Get(id)
	if err != nil {
		return
	}
	o.mu.Lock()
	st := o.of(id)
	if st.epoch != epoch {
		o.mu.Unlock()
		return // reaped or stopped since the probe: stale
	}
	fresh := st.record == nil
	rec := &orphanRecord{gen: gen, procs: len(res.Procs), lastStatus: sb.GetAgent().GetStatus(), lastSeen: sb.GetAgent().GetLastEventAt()}
	if !fresh {
		rec.lastStatus, rec.lastSeen = st.record.lastStatus, st.record.lastSeen
	}
	st.record = rec
	if st.retry != nil {
		st.retry.Stop()
		st.retry = nil
	}
	st.unknown, st.failures, st.firstTry, st.surfaced, st.lastErr = false, 0, time.Time{}, false, ""
	o.mu.Unlock()
	out := o.write(id, func(s *pb.Sandbox) {
		if run := s.GetController().GetRun(); cleanupPending(run) {
			holdForDeveloper(run, gen)
		}
	})
	if !fresh {
		return
	}
	log.Printf("[info] sandbox %s: agent of generation %d detached by a daemon restart (%d processes)", id, gen, len(res.Procs))
	if d.Notify != nil {
		msg := detachedNote(o.check(id))
		if out == nil {
			out = sb
		}
		if run := out.GetController().GetRun(); run != nil {
			msg += " (run " + run.GetId() + ")"
		}
		if o.batchTake(id, msg, affectedSandbox(out)) {
			return // notified with the start-up batch
		}
		d.Notify.EmitNotification(id, pb.NotificationKind_NOTIFICATION_KIND_AGENT_DETACHED, msg, time.Now())
	}
}

// affectedSandbox is a sandbox's entry in a host-level notice.
func affectedSandbox(sb *pb.Sandbox) *pb.NotificationEvent_AffectedSandbox {
	c := sb.GetController()
	return &pb.NotificationEvent_AffectedSandbox{
		SandboxId: sb.GetId(), DisplayName: sb.GetDisplayName(),
		OrchestratorId: c.GetOrchestratorId(), OrchestratorName: c.GetName(), OrchestratorHostId: c.GetHostId(),
	}
}

// beginBatch opens the start-up batch over the probes of ids (SWI-25 A2).
func (o *orphans) beginBatch(ids []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	b := &startBatch{pending: map[string]bool{}}
	for _, id := range ids {
		b.pending[id] = true
	}
	o.batch = b
}

// batchTake records an orphan the start-up batch found, reporting whether it
// did (the caller then emits nothing itself).
func (o *orphans) batchTake(id, msg string, affected *pb.NotificationEvent_AffectedSandbox) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	b := o.batch
	if b == nil || !b.pending[id] {
		return false
	}
	b.found = append(b.found, batchFound{id: id, msg: msg, affected: affected})
	return true
}

// batchDone ends id's start-up probe; the last one closes the batch and
// notifies what it found.
func (o *orphans) batchDone(id string) { o.batchEnd(id) }

// batchDrop ends the start-up probes that never started (the daemon is
// shutting down), so what the batch found so far is still notified.
func (o *orphans) batchDrop(ids ...string) { o.batchEnd(ids...) }

func (o *orphans) batchEnd(ids ...string) {
	o.mu.Lock()
	b := o.batch
	if b == nil {
		o.mu.Unlock()
		return
	}
	for _, id := range ids {
		delete(b.pending, id)
	}
	if len(b.pending) > 0 {
		o.mu.Unlock()
		return
	}
	o.batch = nil
	d := o.deps
	o.mu.Unlock()
	if d.Notify == nil {
		return
	}
	switch len(b.found) {
	case 0:
	case 1:
		f := b.found[0]
		d.Notify.EmitNotification(f.id, pb.NotificationKind_NOTIFICATION_KIND_AGENT_DETACHED, f.msg, time.Now())
	default:
		affected := make([]*pb.NotificationEvent_AffectedSandbox, 0, len(b.found))
		for _, f := range b.found {
			affected = append(affected, f.affected)
		}
		msg := fmt.Sprintf("%d agents detached by a daemon restart — enter to review", len(b.found))
		d.Notify.EmitHostNotification(pb.NotificationKind_NOTIFICATION_KIND_AGENTS_DETACHED_AT_START, msg, affected, time.Now())
	}
}

// AgentClientExited is the agent registry's client-exit observer
// (agent.Registry.SetOnClientExit): the exec client of a session the registry
// did not close exited. The agent may outlive it, so the sandbox is unknown
// at once and probed asynchronously; the exit status is not evidence.
func (m *Manager) AgentClientExited(id string, _ int64) {
	m.orphans.markUnknown(id)
	go m.orphans.probe(id)
}

// RecheckOrphan re-probes a sandbox with an orphan record or unknown (a
// main-agent SessionEnd, research R14). Others are left alone.
func (m *Manager) RecheckOrphan(id string) {
	if m.orphans.pending(id) {
		go m.orphans.probe(id)
	}
}

// MarkOrphanCandidates marks every sandbox that may have a surviving agent
// (mayHaveAgent) unknown, in memory only, so the gate refuses a spawn beside
// it from the daemon's first request on (research R14). It runs before the
// hook server serves: an orphan's escape-hatch result delivery must not start
// a second agent before the start-up check (PrepareOrphanCheck, which needs the
// runtime's listing) settles each one. Its deps' Tokens are read, so they are set first.
func (m *Manager) MarkOrphanCandidates() {
	all, err := m.store.List()
	if err != nil {
		return // PrepareOrphanCheck logs it
	}
	d := m.orphans.getDeps()
	for _, sb := range all {
		if m.orphans.mayHaveAgent(sb, d) {
			m.orphans.setUnknown(sb.GetId())
		}
	}
}

// PrepareOrphanCheck snapshots the daemon's sandboxes and token generations and
// returns the start-up check, which classifies each one (research R14): a
// successful listing's stopped or absent sandboxes have no agent (EXITED by the
// gone rule, tokens retired, never probed); a running one that may have a
// surviving agent is marked unknown and probed asynchronously; every other one
// is clear at once. A listing that errs (or a status it doesn't know) retires
// nothing and writes no EXITED: those sandboxes that may have an agent are
// unknown, and their probes retry the listing.
//
// The check runs alongside the gRPC server (SWI-26), but this snapshot must be
// taken before anything can spawn (the hook server serving, a warm start):
// every generation above it is then this daemon's own. MarkOrphanCandidates has
// the gate refuse every candidate meanwhile, and a sandbox with a newer
// generation (e.g. a worker warm-started that was not a candidate) is skipped:
// that agent is never retired, marked EXITED or made unknown from the listing.
// Each record is re-read as it is classified. Only the listing is slow.
//
// The probes start in priority order (SWI-25 A4): idle workers first (a
// controller and no run: the ones a Replace or a warm start waits on), then
// workers with a run, then every other sandbox. One feeder goroutine takes a
// slot of the concurrency semaphore before it starts each probe, so with more
// candidates than slots the start order is the priority order.
func (m *Manager) PrepareOrphanCheck() func(ctx context.Context) {
	all, err := m.store.List()
	if err != nil {
		return func(context.Context) { log.Printf("[warn] detached-agent check skipped: %v", err) }
	}
	snap := map[string]int64{}
	if d := m.orphans.getDeps(); d.Tokens != nil {
		for _, sb := range all {
			snap[sb.GetId()] = d.Tokens.Generation(sb.GetId())
		}
	}
	return func(ctx context.Context) { m.checkOrphansAtStart(ctx, all, snap) }
}

// checkOrphansAtStart is the check PrepareOrphanCheck returns: all and snap are
// its snapshot. The deps are read here: the worker service completes them after
// the snapshot.
func (m *Manager) checkOrphansAtStart(ctx context.Context, all []*pb.Sandbox, snap map[string]int64) {
	o := m.orphans
	d := o.getDeps()
	lctx, cancel := context.WithTimeout(ctx, orphanProbeTimeout)
	statuses, lerr := m.runner.ListStatuses(lctx)
	cancel()
	var probe []probeCandidate
	for _, sb := range all {
		id := sb.GetId()
		if d.Tokens != nil && d.Tokens.Generation(id) > snap[id] {
			continue // an agent spawned since the snapshot: this daemon's own
		}
		if cur, err := m.store.Get(id); err == nil {
			sb = cur // e.g. a Restart since the snapshot
		} else {
			continue // destroyed meanwhile
		}
		var state pb.SandboxState
		unknown := ""
		if lerr == nil {
			state, unknown = listedState(statuses, sb)
		}
		readable := lerr == nil && unknown == ""
		switch {
		case readable && state != pb.SandboxState_SANDBOX_STATE_RUNNING:
			if d.Tokens != nil {
				for _, g := range d.Tokens.Unretired(id) {
					if g <= snap[id] {
						d.Tokens.Retire(id, g)
					}
				}
			}
			o.drop(id)
			o.write(id, o.exitedOnce)
		case o.mayHaveAgent(sb, d):
			o.setUnknown(id)
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
				o.write(id, nil)
				probe = append(probe, probeCandidate{id: id, class: probeClass(sb)})
				break
			}
			// A record that says stopped, under a listing that can't say: its
			// check is stored but not published, since publishing a STOPPED
			// record is a confirmed stop (tokens retired, the check dropped). It
			// is probed once the sandbox runs again (runtimeOp).
			o.store(id, nil)
		default:
			o.drop(id)
			o.write(id, nil)
		}
	}
	slices.SortStableFunc(probe, func(a, b probeCandidate) int { return a.class - b.class })
	ids := make([]string, 0, len(probe))
	for _, c := range probe {
		ids = append(ids, c.id)
	}
	o.beginBatch(ids)
	go o.feed(ctx, probe)
}

// probeCandidate is a sandbox the start-up check probes, with its priority
// class (probeClass): lower first.
type probeCandidate struct {
	id    string
	class int
}

// probeClass orders the start-up probes: idle workers, workers with a run,
// then the rest.
func probeClass(sb *pb.Sandbox) int {
	switch c := sb.GetController(); {
	case c == nil:
		return 2
	case c.GetRun() != nil:
		return 1
	}
	return 0
}

// feed starts the start-up probes in order, taking a slot of sem before each
// so that no later candidate's probe runs ahead of an earlier one's; it stops
// when ctx ends (the daemon is shutting down).
func (o *orphans) feed(ctx context.Context, probe []probeCandidate) {
	for i, c := range probe {
		select {
		case o.sem <- struct{}{}:
		case <-ctx.Done():
			rest := make([]string, 0, len(probe)-i)
			for _, c := range probe[i:] {
				rest = append(rest, c.id)
			}
			o.batchDrop(rest...)
			return
		}
		go o.probeIn(c.id, true)
	}
}

// mayHaveAgent reports whether a sandbox may have an agent that survived the
// previous daemon: an unretired generation token, or, with no token entry at
// all (the first start after the upgrade), a set, non-EXITED status.
func (o *orphans) mayHaveAgent(sb *pb.Sandbox, d OrphanDeps) bool {
	st := sb.GetAgent().GetStatus()
	byStatus := st != pb.AgentStatus_AGENT_STATUS_UNSPECIFIED && st != pb.AgentStatus_AGENT_STATUS_EXITED
	if d.Tokens == nil {
		return byStatus
	}
	if len(d.Tokens.Unretired(sb.GetId())) > 0 {
		return true
	}
	return d.Tokens.Generation(sb.GetId()) == 0 && byStatus
}

// RunOrphanBackstop re-probes, every orphanBackstop until ctx ends, each
// sandbox whose orphan record or unknown check holds a run: a crashed orphan
// fires no hook, and nothing else would notice its run is gone.
func (m *Manager) RunOrphanBackstop(ctx context.Context) {
	t := time.NewTicker(orphanBackstop)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, id := range m.orphans.pendingIDs() {
			if sb, err := m.store.Get(id); err == nil && sb.GetController().GetRun() != nil {
				go m.orphans.probe(id)
			}
		}
	}
}

func (o *orphans) pendingIDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var ids []string
	for id, st := range o.st {
		if st.unknown || st.record != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// ReapOrphan is the developer's stop of a detached agent (research R14),
// called only by AttachAgent with replace_orphan and DeliverWorkerStep's
// restart; the daemon never calls it on its own. A sandbox with no orphan
// record and no unknown check has nothing to stop. Otherwise, on a RUNNING
// sandbox, under the orphan gate (try-lock: a RuntimeBusyError, agent.ErrBusy,
// naming the holder while an operation holds the runtime lock, or a probe or
// spawn holds the gate), one script
// re-probes and stops each live matched process (TERM, then KILL after
// orphanKillWait). A failed exec stops nothing and returns its
// error. After a stop the generation's tokens are retired, the record dropped
// and, for a Replace, EXITED written (with replacedNote on an ACTION run, in
// the same update).
func (m *Manager) ReapOrphan(ctx context.Context, id string, mode ReapMode) error {
	o := m.orphans
	if !o.pending(id) {
		return nil
	}
	sb, err := m.store.Get(id)
	if err != nil {
		return err
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return fmt.Errorf("sandbox is %s", stateWord(sb.GetState()))
	}
	unlock, err := m.orphanGate(id, gateReaping)
	if err != nil {
		return err
	}
	defer unlock()
	// Again under the gate: a probe may have cleared the sandbox since, and
	// the daemon's own fresh agent (a live session) is never stopped.
	if !o.pending(id) {
		return nil
	}
	if d := o.getDeps(); d.Agents != nil {
		if _, live := d.Agents.LiveGen(id); live {
			return ErrLiveSession
		}
	}
	ctx, cancel := context.WithTimeout(ctx, orphanKillWait+orphanProbeTimeout)
	defer cancel()
	sb, out, err := o.running(ctx, id, true)
	n := 0
	switch out {
	case probeDropped:
		return errors.New("sandbox is not running")
	case probeFailed:
		return fmt.Errorf("stop the detached agent: %w", err)
	case probeAlive:
		text, err := o.execScript(ctx, sb, agent.ReapScript(orphanKillWait))
		if err != nil {
			return fmt.Errorf("stop the detached agent: %w", err)
		}
		res, err := agent.ParseProbe(text)
		if err != nil {
			return fmt.Errorf("stop the detached agent: %w", err)
		}
		n = res.Reaped
	}
	o.drop(id)
	o.retireGone(id)
	log.Printf("[info] sandbox %s: detached agent stopped by the developer (%d processes)", id, n)
	o.write(id, func(s *pb.Sandbox) {
		if mode != ReapReplace {
			return
		}
		if run := s.GetController().GetRun(); run != nil && actionRun(run) {
			run.StepNote = replacedNote
		}
		if s.GetAgent().GetStatus() != pb.AgentStatus_AGENT_STATUS_EXITED {
			o.exited(s)
		}
	})
	return nil
}

// ErrLiveSession refuses a reap while the daemon has a live agent session for
// the sandbox: the script would find and stop that agent.
var ErrLiveSession = errors.New("the sandbox's agent is not detached")

// IdleDetached lists the sandboxes with an orphan record (DETACHED) whose
// controller has no run, sorted: the candidates of a bulk Replace (SWI-25 A1,
// ReplaceDetachedAgent with all_idle). Unknown and failing checks are not
// detached agents and are left out.
func (m *Manager) IdleDetached() []string {
	var ids []string
	for _, id := range m.orphans.pendingIDs() {
		if m.orphans.check(id).GetState() != pb.AgentCheck_STATE_DETACHED {
			continue
		}
		if sb, err := m.store.Get(id); err == nil && sb.GetController().GetRun() == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// actionRun reports whether a run is in its action phase (or has no steps):
// replacing its agent fails it.
func actionRun(run *pb.WorkerRun) bool {
	switch run.GetStepRole() {
	case pb.StepRole_STEP_ROLE_SETUP, pb.StepRole_STEP_ROLE_CLEANUP:
		return false
	}
	return true
}

// stateWord is a sandbox state for a message, e.g. "stopped".
func stateWord(s pb.SandboxState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "SANDBOX_STATE_"))
}
