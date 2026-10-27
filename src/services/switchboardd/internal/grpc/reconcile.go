package grpc

import (
	"context"
	"sync"
	"time"
)

// A client lists sandboxes when it connects or reconnects, so ListSandboxes is
// where the registry is checked against the runtime (SWI-13): a container that
// stopped while no client was watching shows STOPPED on the next connect, and
// connected clients get the change events. Package constants (no env vars).
const (
	// reconcileWindow skips a reconcile that finished this recently, so a burst
	// of lists (several clients, a reload) reads the runtime once.
	reconcileWindow = 5 * time.Second
	// reconcileTimeout bounds one reconcile, so a hung `sbx` cannot hold up a
	// list: past it the stored (last-known) states are returned.
	reconcileTimeout = 8 * time.Second
)

// reconcileGate throttles reconciles to one per reconcileWindow. A caller that
// arrives while a reconcile is running does not wait for it: it answers with
// the stored states at once, and the running pass emits whatever it changes.
// A failed reconcile counts as recent too, so an unreadable runtime costs one
// timeout per window rather than one per list.
type reconcileGate struct {
	mu   sync.Mutex
	last time.Time
	now  func() time.Time
}

// run calls fn unless a reconcile is running or finished within reconcileWindow.
func (g *reconcileGate) run(fn func()) {
	if !g.mu.TryLock() {
		return
	}
	defer g.mu.Unlock()
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	if !g.last.IsZero() && now().Sub(g.last) < reconcileWindow {
		return
	}
	fn()
	g.last = now()
}

// reconcileSandboxes checks the registry against the runtime before a list.
// Failure keeps the last-known states (the manager already logged why).
func (s *Server) reconcileSandboxes(ctx context.Context) {
	s.reconcile.run(func() {
		// Detached from the caller: the pass serves every client that lists in
		// this window, so one client hanging up must not cut it short.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileTimeout)
		defer cancel()
		_, _ = s.mgr.Reconcile(rctx)
	})
}
