package sandbox

import (
	"errors"
	"sync"
)

// ErrBusy is the sentinel every operation-latch refusal wraps (feature 007,
// FR-066). The gRPC layer maps it to FAILED_PRECONDITION; callers that need the
// in-flight operation's name read it from the error message.
var ErrBusy = errors.New("operation in progress for this sandbox")

// busyError names the operation that already holds a sandbox's latch.
type busyError struct{ label string }

func (e *busyError) Error() string { return e.label + " in progress for this sandbox" }
func (e *busyError) Unwrap() error { return ErrBusy }

// opLock is the per-sandbox operation latch (research R4): at most one
// seed-mutating operation — add sources, remove sources, refresh — may run against
// a workspace at a time. It is try-acquire only and never blocks: a second
// operation is refused immediately with the first one's label, which is the UX
// the spec asks for (a visible "busy", not a silent queue behind a 30-minute copy).
//
// The zero value is ready to use. State is in-memory only; a daemon restart
// clears it, and any operation in flight dies with the daemon (its staging debris
// is collected by Manager.PurgeStaging on the next start).
type opLock struct {
	mu   sync.Mutex
	held map[string]string // sandbox id -> label of the in-flight operation
}

// acquire claims the latch for id on behalf of the operation named label. It
// returns a *busyError (wrapping ErrBusy) when another operation holds it.
func (l *opLock) acquire(id, label string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = map[string]string{}
	}
	if cur, ok := l.held[id]; ok {
		return &busyError{label: cur}
	}
	l.held[id] = label
	return nil
}

// release frees the latch for id. Releasing an unheld latch is a no-op.
func (l *opLock) release(id string) {
	l.mu.Lock()
	delete(l.held, id)
	l.mu.Unlock()
}

// holder reports the label of the operation holding id's latch, if any.
func (l *opLock) holder(id string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	label, ok := l.held[id]
	return label, ok
}
