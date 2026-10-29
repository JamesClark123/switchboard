package sandbox

import (
	"context"
	"log"
	"sync"
	"time"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/agent"
)

// The runtime lock (SWI-17, research R14). Each sandbox has a runtimeMu,
// separate from the lifecycle marks and the operation latch. Stop, Destroy,
// Refresh (continuously from its stop through its bring-up), AddKit (the
// recreate) and Restart (the bring-up) hold it from just before their runtime
// call until they have written the resulting state, blocking on it, so they
// wait out an in-flight probe, reap or spawn. Every probe and reap, every
// spawn (the orphan gate, Admit) and every developer service request
// try-lock it: they never wait, and are dropped or refused while an
// operation holds it. Rename, source edits and MCP attach don't touch the
// container's processes and don't take it.

// runtimeLocks is the per-sandbox runtimeMu table. Entries are never removed:
// one mutex per sandbox ever seen is bounded and avoids racing a destroy.
type runtimeLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (r *runtimeLocks) of(id string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*sync.Mutex{}
	}
	l := r.m[id]
	if l == nil {
		l = &sync.Mutex{}
		r.m[id] = l
	}
	return l
}

// TryLockRuntime try-locks the sandbox's runtime lock, never waiting.
func (m *Manager) TryLockRuntime(id string) (unlock func(), ok bool) {
	l := m.runtime.of(id)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

// runtimeOp takes the sandbox's runtime lock for a lifecycle operation,
// waiting for it. The returned func releases it and then applies the outcome
// rule (research R14) from the state the operation wrote: a sandbox RUNNING
// with an orphan record or unknown is marked unknown and probed once. The
// STOPPED and destroyed branch runs when that state is published (emit).
func (m *Manager) runtimeOp(id string) func() {
	l := m.runtime.of(id)
	l.Lock()
	return func() {
		l.Unlock()
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
// holds the runtime lock it refuses with agent.ErrBusy. On a pass the caller
// holds the runtime lock until release.
func (m *Manager) Admit(id string) (release func(), err error) {
	if err := m.orphans.refusal(id); err != nil {
		m.orphans.refused(id)
		return nil, err
	}
	unlock, ok := m.TryLockRuntime(id)
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
