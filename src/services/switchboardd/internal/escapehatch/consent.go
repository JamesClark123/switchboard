package escapehatch

import (
	"context"
	"sync"
	"time"
)

// approvalWindow bounds how long a requires-approval run waits for the developer's
// decision before it is treated as denied (clarification Q4). Deny-by-default: only
// an explicit approve within this window runs the command.
const approvalWindow = 5 * time.Minute

// ConsentGate coordinates the approval round-trip for requires-approval runs. A run
// is registered when it enters PENDING_APPROVAL; Await blocks until the developer
// decides (DecideEscapeHatchRun RPC) or the window elapses. Safe for concurrent use.
type ConsentGate struct {
	mu      sync.Mutex
	pending map[string]chan bool // runID -> decision channel (buffered, size 1)
	window  time.Duration
}

// NewConsentGate constructs a ConsentGate with the default approval window.
func NewConsentGate() *ConsentGate {
	return &ConsentGate{pending: map[string]chan bool{}, window: approvalWindow}
}

// SetWindow overrides the approval window (used in tests to avoid a 5-minute wait).
func (g *ConsentGate) SetWindow(d time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.window = d
}

// Register records a run as awaiting a decision. It MUST be called before Await (and
// before the run is announced to clients) so a decision that races in early is not
// lost — the channel is buffered so Decide never blocks.
func (g *ConsentGate) Register(runID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.pending[runID]; !ok {
		g.pending[runID] = make(chan bool, 1)
	}
}

// Await blocks until the run is decided, the window elapses, or ctx is cancelled.
// It returns true only on an explicit approval; every other outcome (denial, window
// elapse, cancellation) returns false (deny-by-default, SC-003). The run is
// deregistered on return, so a later Decide is a no-op.
func (g *ConsentGate) Await(ctx context.Context, runID string) bool {
	g.mu.Lock()
	ch, ok := g.pending[runID]
	window := g.window
	g.mu.Unlock()
	if !ok {
		return false
	}
	defer g.forget(runID)

	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case approved := <-ch:
		return approved
	case <-timer.C:
		return false // window elapsed -> denied
	case <-ctx.Done():
		return false // sandbox stopped / daemon shutdown -> denied
	}
}

// Decide resolves a pending run's approval. It returns true if the run was awaiting
// a decision, false if it was already resolved or never registered (idempotent —
// a duplicate or late decision is a harmless no-op).
func (g *ConsentGate) Decide(runID string, approved bool) bool {
	g.mu.Lock()
	ch, ok := g.pending[runID]
	g.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- approved:
		return true
	default:
		return false // a decision is already buffered
	}
}

func (g *ConsentGate) forget(runID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, runID)
}
