package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

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
//     set, non-EXITED status), in list order (tasks.md Deferred items), at most
//     orphanProbeConcurrency at a time;
//   - when an agent's exec client exits without the registry closing it;
//   - on a gate refusal (debounced), a main-agent SessionEnd, and every
//     orphanBackstop while an orphan or an unknown sandbox holds a run;
//   - once when a lifecycle operation leaves a sandbox RUNNING with one.
//
// Until a probe answers the sandbox is *unknown* and the gate refuses every
// spawn. Alive with no live session of its generation is an orphan record
// (surfaced once with AGENT_DETACHED); gone drops the record, retires the
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

// detachedMessage is the AGENT_DETACHED notification and the plain row note
// (tasks.md Deferred items).
const detachedMessage = "agent detached by a daemon restart — t to Replace or Wait"

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

// OrphanNotifier emits the AGENT_DETACHED notification (agent.Hub).
type OrphanNotifier interface {
	EmitNotification(sandboxID string, kind pb.NotificationKind, message string, now time.Time) *pb.NotificationEvent
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

// orphanState is one sandbox's check.
type orphanState struct {
	unknown   bool
	record    *orphanRecord
	failures  int       // failed probes in a row
	firstTry  time.Time // the first probe attempt of this unknown spell
	surfaced  bool      // CHECK_FAILED
	lastErr   string
	probing   bool // single flight
	again     bool // a probe was asked for while one ran
	retry     *time.Timer
	refusedAt time.Time
	// epoch counts drops (a reap, a confirmed stop, a clear): a probe records
	// its result only if none happened since it took the runtime lock, so a
	// stale "alive" never lands over a reap or stop that slipped in after it.
	epoch uint64
}

type orphans struct {
	m   *Manager
	sem chan struct{}

	mu   sync.Mutex
	st   map[string]*orphanState
	deps OrphanDeps
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
	o.mu.Unlock()
	go o.probe(id)
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
		if want := o.check(id); !proto.Equal(s.GetAgent().GetAgentCheck(), want) {
			if s.Agent == nil {
				s.Agent = &pb.AgentSession{}
			}
			s.Agent.AgentCheck = want
		}
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
func (o *orphans) probe(id string) {
	o.mu.Lock()
	st := o.of(id)
	if st.probing {
		st.again = true
		o.mu.Unlock()
		return
	}
	st.probing = true
	o.mu.Unlock()
	for {
		o.sem <- struct{}{}
		out, epoch, res, err := o.probeOnce(id)
		<-o.sem
		o.apply(id, epoch, out, res, err)
		o.mu.Lock()
		if !st.again {
			st.probing = false
			o.mu.Unlock()
			return
		}
		st.again = false
		o.mu.Unlock()
	}
}

// running re-reads the record and the runtime's listing: the probe runs only
// against a sandbox that is RUNNING in both, since `sbx exec` would start a
// stopped one. A listing that says stopped means its agent is gone.
func (o *orphans) running(ctx context.Context, id string) (*pb.Sandbox, probeOutcome, error) {
	sb, err := o.m.store.Get(id)
	if err != nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return nil, probeDropped, nil
	}
	statuses, err := o.m.runner.ListStatuses(ctx)
	if err != nil {
		return nil, probeFailed, fmt.Errorf("list sandboxes: %w", err)
	}
	state, unknown := listedState(statuses, sb)
	switch {
	case unknown != "":
		return nil, probeFailed, fmt.Errorf("sandbox status %q", unknown)
	case state != pb.SandboxState_SANDBOX_STATE_RUNNING:
		return nil, probeGone, nil
	}
	return sb, probeAlive, nil
}

// execScript runs a script in the sandbox (one `sbx exec`) and returns its
// stdout.
func (o *orphans) execScript(ctx context.Context, sb *pb.Sandbox, script string) (string, error) {
	ref := sb.GetContainerRef()
	if ref == "" {
		ref = sb.GetDisplayName()
	}
	cmd := o.m.runner.Exec(ctx, ref, []string{"/bin/sh", "-c", script})
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	return string(out), err
}

// probeOnce runs one probe under the runtime lock (try-lock: dropped while an
// operation holds it, whose outcome rule settles the sandbox instead). It
// returns the sandbox's epoch as of taking the lock.
func (o *orphans) probeOnce(id string) (probeOutcome, uint64, agent.ProbeResult, error) {
	o.mu.Lock()
	if st := o.st[id]; st != nil && st.firstTry.IsZero() {
		st.firstTry = time.Now() // the clock starts at the first attempt
	}
	o.mu.Unlock()
	if sb, err := o.m.store.Get(id); err != nil || sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		return probeDropped, 0, agent.ProbeResult{}, nil
	}
	unlock, ok := o.m.TryLockRuntime(id)
	if !ok {
		return probeDropped, 0, agent.ProbeResult{}, nil
	}
	defer unlock()
	o.mu.Lock()
	epoch := o.of(id).epoch
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), orphanProbeTimeout)
	defer cancel()
	sb, out, err := o.running(ctx, id)
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
// stopped or cleared) since the probe took the runtime lock: the result is
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
// since its first attempt, it is surfaced once as CHECK_FAILED (the row note;
// tasks.md Deferred items: no notification, no run note).
func (o *orphans) failed(id string, epoch uint64, perr error) {
	o.mu.Lock()
	st := o.of(id)
	if (!st.unknown && st.record == nil) || st.epoch != epoch {
		o.mu.Unlock()
		return // cleared meanwhile
	}
	st.failures++
	st.lastErr = perr.Error()
	if st.failures >= orphanMaxFailures || (!st.firstTry.IsZero() && time.Since(st.firstTry) >= orphanUnknownLimit) {
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
	o.write(id, nil)
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
		if p, ok := res.Main(); ok && slices.Contains(d.Tokens.Unretired(id), p.Gen) {
			_ = d.Tokens.BindFromProbe(id, p)
		}
		if !res.File {
			gen = d.Tokens.Generation(id)
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
		msg := detachedMessage
		if out == nil {
			out = sb
		}
		if run := out.GetController().GetRun(); run != nil {
			msg += " (run " + run.GetId() + ")"
		}
		d.Notify.EmitNotification(id, pb.NotificationKind_NOTIFICATION_KIND_AGENT_DETACHED, msg, time.Now())
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
// a second agent before CheckOrphansAtStart (which needs the runtime's
// listing) settles each one. Its deps' Tokens are read, so they are set first.
func (m *Manager) MarkOrphanCandidates() {
	all, err := m.store.List()
	if err != nil {
		return // CheckOrphansAtStart logs it
	}
	d := m.orphans.getDeps()
	for _, sb := range all {
		if m.orphans.mayHaveAgent(sb, d) {
			m.orphans.setUnknown(sb.GetId())
		}
	}
}

// CheckOrphansAtStart classifies every sandbox at daemon start (research
// R14), before any agent can spawn: a successful listing's stopped or absent
// sandboxes have no agent (EXITED by the gone rule, tokens retired, never
// probed); a running one that may have a surviving agent is marked unknown
// synchronously and probed asynchronously; every other one is clear at once.
// A listing that errs (or a status it doesn't know) retires nothing and writes
// no EXITED: those sandboxes that may have an agent are unknown, and their
// probes retry the listing.
func (m *Manager) CheckOrphansAtStart(ctx context.Context) {
	all, err := m.store.List()
	if err != nil {
		log.Printf("[warn] detached-agent check skipped: %v", err)
		return
	}
	lctx, cancel := context.WithTimeout(ctx, orphanProbeTimeout)
	statuses, lerr := m.runner.ListStatuses(lctx)
	cancel()
	o := m.orphans
	d := o.getDeps()
	var probe []string
	for _, sb := range all {
		id := sb.GetId()
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
					d.Tokens.Retire(id, g)
				}
			}
			o.drop(id)
			o.write(id, o.exitedOnce)
		case o.mayHaveAgent(sb, d):
			o.setUnknown(id)
			if sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING {
				o.write(id, nil)
				probe = append(probe, id)
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
	for _, id := range probe {
		go o.probe(id)
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
// sandbox, under the runtime lock (try-lock: agent.ErrBusy while an operation
// holds it), one script re-probes and stops each live matched process (TERM,
// then KILL after orphanKillWait). A failed exec stops nothing and returns its
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
	unlock, ok := m.TryLockRuntime(id)
	if !ok {
		return agent.ErrBusy
	}
	defer unlock()
	// Again under the lock: a probe may have cleared the sandbox since, and
	// the daemon's own fresh agent (a live session) is never stopped.
	if !o.pending(id) {
		return nil
	}
	if d := o.getDeps(); d.Agents != nil {
		if _, live := d.Agents.LiveGen(id); live {
			return errLiveSession
		}
	}
	ctx, cancel := context.WithTimeout(ctx, orphanKillWait+orphanProbeTimeout)
	defer cancel()
	sb, out, err := o.running(ctx, id)
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

// errLiveSession refuses a reap while the daemon has a live agent session for
// the sandbox: the script would find and stop that agent.
var errLiveSession = errors.New("the sandbox's agent is not detached")

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
