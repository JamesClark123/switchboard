package mcp

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Refusal is a save-family refusal the gRPC layer maps to FAILED_PRECONDITION
// (NOT_FOUND for GONE) with an McpSaveRefusal status detail (SWI-21 contract).
type Refusal struct {
	Reason pb.McpSaveRefusal_Reason
	Msg    string
}

func (r *Refusal) Error() string { return r.Msg }

// Is matches any Refusal with the same reason, so the sentinels below work with
// errors.Is whatever the message.
func (r *Refusal) Is(target error) bool {
	var t *Refusal
	return errors.As(target, &t) && t.Reason == r.Reason
}

func refuse(reason pb.McpSaveRefusal_Reason, format string, a ...any) error {
	return &Refusal{Reason: reason, Msg: fmt.Sprintf(format, a...)}
}

// Refusal sentinels.
var (
	// ErrBeingReplaced: a live save of the name holds an in-progress record or
	// retries its persist (saving/recording), or Authorize met a save or a
	// pending replace (FR-424).
	ErrBeingReplaced = &Refusal{Reason: pb.McpSaveRefusal_BEING_REPLACED, Msg: "a save of this server is still running; try again later"}
	// ErrRecovering: a leftover in-progress record waits for recovery.
	ErrRecovering = &Refusal{Reason: pb.McpSaveRefusal_UNVERIFIED, Msg: "recovering an interrupted save; try again"}
	// ErrUnavailable: a save ran but its persist failed 3 times (UNAVAILABLE).
	ErrUnavailable = errors.New("unavailable")
)

// authBusyMsg is the FR-424 message for a save or removal during an authorization.
const authBusyMsg = "an authorization of this server is in progress; finish or cancel it first"

// lockOpts says what the request alone shows the operation does (research R3).
type lockOpts struct {
	// authSensitive: touches mcp:<s>:client_secret / :identity or removes the
	// registration, so an in-flight authorization refuses it at take time.
	authSensitive bool
}

// nameLock is the per-server-name lock (research R3): a 1-slot semaphore plus a
// waiter count, all guarded by Manager.mu. refuse is closed (once, via refused)
// when the holder becomes saving/recording, so queued takers are refused instead
// of waiting.
type nameLock struct {
	slot      chan struct{}
	waiters   int
	held      bool
	replacing bool
	refuse    chan struct{}
	refused   bool
}

// nameState is one name's leaf state (research R12), guarded by stateMu. It is
// the only source Authorize, reconcile and ReplaceState read for "pending".
type nameState struct {
	replacing  bool // a held lock whose save re-registers
	pending    bool // a pending replace exists
	saving     bool // a live save's in-progress record landed
	recording  bool // a switchboard-only save retries its persist
	recovering bool // a leftover in-progress record waits for recovery
	lockers    int  // held + waiting
	generation uint64
	// lastEnd is how the most recent replace ended (for a late launch, R12).
	lastEnd replaceEnd
}

type replaceEnd int

const (
	endRegistered replaceEnd = iota
	endRemoved
)

// skipPanicGuard switches the release's re-panic guard off; only the negative
// control of TestSavePanicCrashes sets it.
var skipPanicGuard = false

// st returns name's state, creating it. Caller holds stateMu.
func (m *Manager) st(name string) *nameState {
	s := m.state[name]
	if s == nil {
		s = &nameState{}
		m.state[name] = s
	}
	return s
}

// lockName takes the per-name lock. It refuses, in the same m.mu hold that would
// register the caller as a waiter, an authSensitive caller during an
// authorization (ErrAuthInProgress) and any caller while the name is
// saving/recording (ErrBeingReplaced). A waiter is refused the same way when the
// holder becomes saving/recording. The returned release MUST be deferred
// directly (`defer release()`) by the function that holds the lock.
func (m *Manager) lockName(ctx context.Context, name string, opts lockOpts) (*nameLock, func(), error) {
	m.mu.Lock()
	if _, busy := m.inflight[name]; busy && opts.authSensitive {
		m.mu.Unlock()
		return nil, nil, &Refusal{Reason: pb.McpSaveRefusal_AUTH_IN_PROGRESS, Msg: authBusyMsg}
	}
	m.stateMu.Lock()
	s := m.st(name)
	if s.saving || s.recording {
		m.stateMu.Unlock()
		m.mu.Unlock()
		return nil, nil, ErrBeingReplaced
	}
	s.lockers++
	m.stateMu.Unlock()
	l := m.busy[name]
	if l == nil {
		l = &nameLock{slot: make(chan struct{}, 1), refuse: make(chan struct{})}
		m.busy[name] = l
	}
	l.waiters++
	refuseCh := l.refuse
	m.mu.Unlock()

	select {
	case l.slot <- struct{}{}:
	case <-ctx.Done():
		m.dropWaiter(name, l)
		return nil, nil, ctx.Err()
	case <-refuseCh:
		m.dropWaiter(name, l)
		return nil, nil, ErrBeingReplaced
	}
	m.mu.Lock()
	l.waiters--
	l.held = true
	m.mu.Unlock()
	return l, m.releaseFunc(name, l), nil
}

// dropWaiter undoes a waiter registration that never took the slot.
func (m *Manager) dropWaiter(name string, l *nameLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.waiters--
	m.stateMu.Lock()
	m.st(name).lockers--
	m.stateMu.Unlock()
	if l.waiters == 0 && !l.held {
		delete(m.busy, name)
	}
}

// releaseFunc returns the shared release every lock holder defers directly. Its
// first statement re-panics, so a panicking holder skips the body (it takes
// neither m.mu nor stateMu) and the daemon crashes instead of wedging on a lock
// the panic left held; the restart resolves any record as recovering (R2).
func (m *Manager) releaseFunc(name string, l *nameLock) func() {
	return func() {
		if !skipPanicGuard {
			if r := recover(); r != nil {
				panic(r)
			}
		}
		m.mu.Lock()
		// Backstop for the one exit that skips the post-persist tail (the persist
		// retry ending on shutdown): restore "a closed channel has no live
		// waiters". It never clears saving/recording.
		if l.refused {
			l.refuse, l.refused = make(chan struct{}), false
		}
		l.held, l.replacing = false, false
		m.stateMu.Lock()
		s := m.st(name)
		s.lockers--
		s.replacing = false
		s.generation++
		m.stateMu.Unlock()
		<-l.slot
		if l.waiters == 0 {
			delete(m.busy, name)
		}
		m.mu.Unlock()
	}
}

// markRefusing sets saving (record) or recording (no record) and closes the
// lock's refuse channel in one m.mu hold, so every queued taker is refused.
func (m *Manager) markRefusing(name string, l *nameLock, record bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateMu.Lock()
	if record {
		m.st(name).saving = true
	} else {
		m.st(name).recording = true
	}
	m.stateMu.Unlock()
	if !l.refused {
		l.refused = true
		close(l.refuse)
	}
}

// clearRefusing is the post-persist tail's first step: saving/recording cleared
// (and pending set as the outcome says) with a closed refuse channel swapped for
// a fresh one in the same hold. An unclosed channel is kept so earlier waiters
// still select on the channel the next holder closes.
func (m *Manager) clearRefusing(name string, l *nameLock, pending *bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateMu.Lock()
	s := m.st(name)
	// The replace has ended: a launch from here on records the post-sweep state.
	s.saving, s.recording, s.replacing = false, false, false
	l.replacing = false
	if pending != nil {
		s.pending = *pending
	}
	m.stateMu.Unlock()
	if l.refused {
		l.refuse, l.refused = make(chan struct{}), false
	}
}

// setReplacing records that the held lock's save re-registers (classify).
func (m *Manager) setReplacing(name string, l *nameLock) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.replacing = true
	m.stateMu.Lock()
	m.st(name).replacing = true
	m.stateMu.Unlock()
}

// recheckInflight refuses AUTH_IN_PROGRESS when an authorization of name is in
// flight; Save calls it right after classify once it knows the save replaces or
// touches the client binding (research R3).
func (m *Manager) recheckInflight(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.inflight[name]; busy {
		return &Refusal{Reason: pb.McpSaveRefusal_AUTH_IN_PROGRESS, Msg: authBusyMsg}
	}
	return nil
}

// stateSnapshot copies one name's state.
func (m *Manager) stateSnapshot(name string) nameState {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if s := m.state[name]; s != nil {
		return *s
	}
	return nameState{}
}

// setPending updates the pending flag (and the end of a replace) in the state map.
func (m *Manager) setPending(name string, on bool) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.st(name).pending = on
}

// generations snapshots every name's generation (before a list, R4).
func (m *Manager) generations() map[string]uint64 {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	out := make(map[string]uint64, len(m.state))
	for n, s := range m.state {
		out[n] = s.generation
	}
	return out
}

// staleFor reports whether reconcile must leave name alone: it is locked, has
// an in-progress record, or its generation moved since gens was taken. Caller
// holds stateMu (inside a registry mutate func).
func (m *Manager) staleFor(name string, gens map[string]uint64) bool {
	s := m.state[name]
	if s == nil {
		return false
	}
	return s.lockers > 0 || s.saving || s.recording || s.recovering || s.generation != gens[name]
}

// ReplaceStatus is what a launch's registry write needs to know (R12).
type ReplaceStatus int

const (
	// BeingReplaced: the skip stands (SKIPPED_BEING_REPLACED).
	BeingReplaced ReplaceStatus = iota
	// EndedRegistered: the replace ended with the server registered
	// (NOT_ATTACHED_AT_LAUNCH).
	EndedRegistered
	// EndedRemoved: cancelled or registered by another path (no entry).
	EndedRemoved
)

// ReplaceState reads only the leaf state map, so a launch may call it inside its
// registry mutate func (the late launch race, R12).
func (m *Manager) ReplaceState(name string) ReplaceStatus {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	s := m.state[name]
	if s == nil {
		return EndedRegistered
	}
	if s.replacing || s.pending || s.saving || s.recovering {
		return BeingReplaced
	}
	if s.lastEnd == endRemoved {
		return EndedRemoved
	}
	return EndedRegistered
}

// replaceEnded records how a replace ended and runs the hook. Never called
// under m.mu or inside a registry transaction (R12 Lock order).
func (m *Manager) replaceEnded(name string, registered bool) {
	m.stateMu.Lock()
	if registered {
		m.st(name).lastEnd = endRegistered
	} else {
		m.st(name).lastEnd = endRemoved
	}
	m.stateMu.Unlock()
	if m.onReplaceEnded != nil {
		m.onReplaceEnded(name, registered)
	}
}
