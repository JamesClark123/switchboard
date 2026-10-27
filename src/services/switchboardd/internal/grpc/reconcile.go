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

// reconcileGate throttles reconciles and collapses concurrent callers onto one:
// a caller arriving while a reconcile runs waits for it and then finds it
// recent. A failed reconcile counts as recent too, so an unreadable runtime
// costs one timeout per window rather than one per list.
type reconcileGate struct {
	mu   sync.Mutex
	last time.Time
	now  func() time.Time
}

// run calls fn unless a reconcile finished within reconcileWindow.
func (g *reconcileGate) run(fn func()) {
	g.mu.Lock()
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
	if s.mgr == nil {
		return
	}
	s.reconcile.run(func() {
		// Detached from the caller: every caller waiting on the gate shares this
		// pass, so one client hanging up must not cut it short for the rest.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reconcileTimeout)
		defer cancel()
		_, _ = s.mgr.Reconcile(rctx)
	})
}
