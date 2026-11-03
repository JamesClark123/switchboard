package sandbox

import (
	"context"
	"log"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
)

// The runtime lock (SWI-17, research R14; read/write split SWI-26). Each
// sandbox has a runtimeMu, separate from the lifecycle marks and the operation
// latch. It is a read/write lock:
//
//   - Stop, Destroy, Refresh (continuously from its stop through its
//     bring-up), AddKit (the recreate) and Restart (the bring-up) take it for
//     writing, blocking, from just before their runtime call until they have
//     written the resulting state, so they wait out every in-flight reader.
//   - Everything else that runs `sbx exec` (or another runner call that would
//     start a stopped sandbox) into a sandbox the daemon believes RUNNING takes
//     it for reading, with a try-lock: probes and reaps, every spawn (the
//     orphan gate, Admit), the developer's service start and stop, a service's
//     readiness checks and port publish, and MCP load/unload. They never wait,
//     and are dropped or refused while an operation holds it or waits for it
//     (a waiting writer blocks new readers). Readers don't exclude each other.
//     An MCP load or unload can take a minute, so an operation cancels the ones
//     in flight rather than waiting them out (HoldRuntimeRead).
//
// The pairs of readers that must still exclude each other do so on narrower
// locks: a probe, a reap and a spawn of the same sandbox on the orphan gate
// (orphanGate, below); a service's launch and its developer stop on the
// supervisor's per-instance launch lock (portforward.procHandle). Rename, source
// edits and recording an MCP attach don't touch the container's processes and
// don't take it.

// runtimeLocks is the per-sandbox runtimeMu table. Entries are never removed:
// one lock pair per sandbox ever seen is bounded and avoids racing a destroy.
type runtimeLocks struct {
	mu sync.Mutex
	m  map[string]*sandboxLocks
}

// sandboxLocks is one sandbox's runtimeMu and its orphan gate. The orphan
// gate is taken only under a read hold of rw, never waited on: a probe, a
// reap and a spawn each try-lock it, so no two of them overlap (a reap's
// script would stop a spawning agent; a probe would record a spawning agent
// whose session isn't registered yet as an orphan).
//
// holds is the MCP calls in flight under a read hold of rw, which an operation
// cancels before it waits for the write lock. opsWaiting refuses new ones from
// that sweep until the operation's Lock call makes rw refuse them itself. Both
// are guarded by holdsMu, a leaf (the TryRLock under it never waits).
type sandboxLocks struct {
	rw     sync.RWMutex
	orphan sync.Mutex

	holdsMu    sync.Mutex
	holds      map[uint64]context.CancelFunc
	holdSeq    uint64
	opsWaiting int
}

// beginOp cancels the MCP calls in flight and refuses new ones, ahead of an
// operation's rw.Lock; endOp undoes the refusal once that Lock has returned.
func (l *sandboxLocks) beginOp() {
	l.holdsMu.Lock()
	defer l.holdsMu.Unlock()
	l.opsWaiting++
	for _, cancel := range l.holds {
		cancel()
	}
}

func (l *sandboxLocks) endOp() {
	l.holdsMu.Lock()
	defer l.holdsMu.Unlock()
	l.opsWaiting--
}

func (r *runtimeLocks) of(id string) *sandboxLocks {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*sandboxLocks{}
	}
	l := r.m[id]
	if l == nil {
		l = &sandboxLocks{}
		r.m[id] = l
	}
	return l
}

// TryLockRuntime try-locks the sandbox's runtime lock for writing, never
// waiting: the hold a lifecycle operation takes, for a caller that must
// exclude every reader.
func (m *Manager) TryLockRuntime(id string) (unlock func(), ok bool) {
	l := m.runtime.of(id)
	if !l.rw.TryLock() {
		return nil, false
	}
	return l.rw.Unlock, true
}

// TryRLockRuntime try-locks the sandbox's runtime lock for reading, never
// waiting: refused only while a lifecycle operation holds it or waits for it.
// It is the hold of every `sbx exec` outside the orphan gate (the service
// supervisor's); an `sbx mcp` call takes HoldRuntimeRead.
func (m *Manager) TryRLockRuntime(id string) (unlock func(), ok bool) {
	l := m.runtime.of(id)
	if !l.rw.TryRLock() {
		return nil, false
	}
	return l.rw.RUnlock, true
}

// HoldRuntimeRead is TryRLockRuntime for an `sbx mcp` call, which may run for a
// minute and can't be bounded any tighter without breaking slow loads. The
// returned context, derived from ctx, is cancelled when a lifecycle operation
// asks for the lock, so the operation never waits out the call, and neither do
// the readers it blocks meanwhile. The cancel has to end the call promptly, since
// the operation waits for this hold with nothing to give up on: the call kills
// `sbx`'s process group and doesn't wait on what escapes it (SbxRunner.runHeld,
// mcp.CLI.Load). A caller sees the cancel as its context ending while ctx has not.
// unlock releases the hold.
func (m *Manager) HoldRuntimeRead(ctx context.Context, id string) (held context.Context, unlock func(), ok bool) {
	l := m.runtime.of(id)
	l.holdsMu.Lock()
	defer l.holdsMu.Unlock()
	if l.opsWaiting > 0 || !l.rw.TryRLock() {
		return nil, nil, false
	}
	held, cancel := context.WithCancel(ctx)
	if l.holds == nil {
		l.holds = map[uint64]context.CancelFunc{}
	}
	l.holdSeq++
	key := l.holdSeq
	l.holds[key] = cancel
	return held, func() {
		l.holdsMu.Lock()
		delete(l.holds, key)
		l.holdsMu.Unlock()
		cancel()
		l.rw.RUnlock()
	}, true
}

// errRuntimeBusy refuses a runner call into a sandbox while a lifecycle
// operation holds its runtime lock, or reports one that operation cancelled. It
// wraps ErrBusy, so the gRPC layer maps it like an operation-latch refusal.
var errRuntimeBusy = &busyError{label: "a stop, restart, refresh or kit attach"}

// withRuntimeRead runs fn under the sandbox's runtime lock, read (HoldRuntimeRead),
// or refuses with errRuntimeBusy while a lifecycle operation holds it. A failure
// because an operation cancelled fn is errRuntimeBusy too.
func (m *Manager) withRuntimeRead(ctx context.Context, id string, fn func(ctx context.Context) error) error {
	held, unlock, ok := m.HoldRuntimeRead(ctx, id)
	if !ok {
		return errRuntimeBusy
	}
	defer unlock()
	err := fn(held)
	if err != nil && held.Err() != nil && ctx.Err() == nil {
		return errRuntimeBusy
	}
	return err
}

// orphanGate try-locks the runtime lock for reading and then the sandbox's
// orphan gate, the hold of a probe, a reap and a spawn. It refuses while a
// lifecycle operation holds the runtime lock or another of the three holds the
// gate.
func (m *Manager) orphanGate(id string) (unlock func(), ok bool) {
	l := m.runtime.of(id)
	if !l.rw.TryRLock() {
		return nil, false
	}
	if !l.orphan.TryLock() {
		l.rw.RUnlock()
		return nil, false
	}
	return func() {
		l.orphan.Unlock()
		l.rw.RUnlock()
	}, true
}

// runtimeOp takes the sandbox's runtime lock for a lifecycle operation,
// waiting for it, after cancelling the MCP calls holding it (HoldRuntimeRead):
// what's left to wait for has its own bound, seconds for most readers, and up to
// about 50 s for a developer's service stop and 90 s for a port publish. The
// returned func releases it and then applies the outcome rule (research R14) from
// the state the operation wrote: a sandbox RUNNING with an orphan record or
// unknown is marked unknown and probed once. The STOPPED and destroyed branch runs
// when that state is published (emit).
func (m *Manager) runtimeOp(id string) func() {
	l := m.runtime.of(id)
	l.beginOp()
	l.rw.Lock()
	l.endOp()
	return func() {
		// The operation may have started or stopped the container: a listing
		// taken before now must not answer the outcome rule's probe.
		m.lists.invalidate()
		l.rw.Unlock()
		sb, err := m.store.Get(id)
		if err == nil && sb.GetState() == pb.SandboxState_SANDBOX_STATE_RUNNING && m.orphans.pending(id) {
			m.orphans.markUnknown(id)
			go m.orphans.probe(id)
		}
	}
}

// Admit is the orphan gate (agent.OrphanGate) every spawn asks first, under
// the agent registry's lock, so it never waits. Beside an orphan, or while one
// is being checked for, it refuses (agent.ErrDetached / agent.ErrChecking) and
// schedules a re-probe, debounced per sandbox; while a lifecycle operation
// holds the runtime lock, or a probe or reap holds the orphan gate, it refuses
// with agent.ErrBusy. On a pass the caller holds the runtime lock for reading
// and the orphan gate until release.
func (m *Manager) Admit(id string) (release func(), err error) {
	if err := m.orphans.refusal(id); err != nil {
		m.orphans.refused(id)
		return nil, err
	}
	unlock, ok := m.orphanGate(id)
	if !ok {
		return nil, agent.ErrBusy
	}
	return unlock, nil
}

// SetStopDrain registers the best-effort drain a stop runs under the runtime
// lock just before `sbx stop` / `sbx rm` (research R14): it closes the
// sandbox's agent session and terminal, interrupts its starting services and
// signals their exec clients, TERM then KILL within one wait, and reports
// whether it closed a live agent. MAY be nil.
func (m *Manager) SetStopDrain(fn func(sb *pb.Sandbox, wait time.Duration) (closedAgent bool)) {
	m.stopDrain = fn
}

// drain runs the stop drain, if any, reporting whether it closed a live agent.
// That agent may outlive its exec client: if the stop then fails, the caller
// marks the sandbox unknown (stopFailed), so the outcome rule probes it rather
// than letting a second agent spawn beside it.
func (m *Manager) drain(sb *pb.Sandbox) bool {
	if m.stopDrain == nil {
		return false
	}
	return m.stopDrain(sb, orphanKillWait)
}

// stopFailed marks a sandbox unknown after its stop failed with the container
// still up and its agent session already drained.
func (m *Manager) stopFailed(id string, drained bool) {
	if drained {
		m.orphans.markUnknown(id)
	}
}

// Bounds of the shared listing (SWI-26 B3). Variables so tests can change
// them.
var (
	// listSharedMaxAge is how long a successful listing answers later probes.
	listSharedMaxAge = 1500 * time.Millisecond
)

// sharedList is one `sbx ls` shared by the probes that need it at about the
// same time (orphans.running): a call is joined while in flight, and its
// successful result reused, only until listSharedMaxAge after it STARTED, so no
// probe acts on a listing begun longer ago than that however slow the call. At
// start-up up to orphanProbeConcurrency probes would otherwise each list the
// runtime. A lifecycle operation invalidates it as it releases the runtime lock.
// Callers that need a strictly fresh read (a reap, the start-up check,
// listedRunningStrict, Reconcile) call the runner directly.
type sharedList struct {
	mu     sync.Mutex
	gen    uint64      // bumped by invalidate
	flight *listFlight // the newest call in flight, if any
	last   *listFlight // the newest successful call
}

// listFlight is one runner call: done closes once val and err are set.
type listFlight struct {
	gen  uint64
	done chan struct{}
	at   time.Time // when the call started
	val  map[string]string
	err  error
}

// invalidate drops the cached listing; a call in flight is neither joined by
// later callers nor cached.
func (s *sharedList) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.last = nil
}

// listShared returns the runtime's listing, shared (sharedList). The runner
// call runs on its own context, bounded by orphanProbeTimeout, so a caller
// that gives up (ctx) never fails it for the others. The map is shared: read
// it only.
func (m *Manager) listShared(ctx context.Context) (map[string]string, error) {
	s := &m.lists
	s.mu.Lock()
	if l := s.last; l != nil && l.gen == s.gen && time.Since(l.at) < listSharedMaxAge {
		s.mu.Unlock()
		return l.val, nil
	}
	f := s.flight
	if f == nil || f.gen != s.gen || time.Since(f.at) >= listSharedMaxAge {
		f = &listFlight{gen: s.gen, done: make(chan struct{}), at: time.Now()}
		s.flight = f
		go m.runList(f)
	}
	s.mu.Unlock()
	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runList makes flight f's runner call and publishes its result.
func (m *Manager) runList(f *listFlight) {
	ctx, cancel := context.WithTimeout(context.Background(), orphanProbeTimeout)
	val, err := m.runner.ListStatuses(ctx)
	cancel()
	s := &m.lists
	s.mu.Lock()
	f.val, f.err = val, err
	if s.flight == f {
		s.flight = nil
	}
	if err == nil && f.gen == s.gen && (s.last == nil || f.at.After(s.last.at)) {
		s.last = f
	}
	s.mu.Unlock()
	close(f.done)
}

// listedRunningStrict reports whether the runtime lists the sandbox's
// container as running, for a caller that must not read an unreadable runtime
// as stopped (Refresh after a failed stop, research R14): a listing error, or
// a status this daemon doesn't know, counts as still running.
func (m *Manager) listedRunningStrict(ctx context.Context, sb *pb.Sandbox) bool {
	statuses, err := m.runner.ListStatuses(ctx)
	if err != nil {
		log.Printf("[warn] sandbox %s: runtime unreadable after a failed stop; treating it as still running: %v", sb.GetId(), err)
		return true
	}
	state, unknown := listedState(statuses, sb)
	return unknown != "" || state == pb.SandboxState_SANDBOX_STATE_RUNNING
}

// MarkAgentUnknown marks a sandbox unknown (research R14): the change
// observer closed a live agent session on CREATING or ERROR, whose container
// may still be up with the agent in it. The next RUNNING probes it before
// anything can spawn.
func (m *Manager) MarkAgentUnknown(id string) { m.orphans.markUnknown(id) }
