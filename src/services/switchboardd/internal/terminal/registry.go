package terminal

import (
	"errors"
	"sync"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// ErrNoExisting is returned by BroadcasterExisting when no provider of live
// sessions is configured.
var ErrNoExisting = errors.New("no live-session provider configured")

// Provider lazily supplies the PTY for a sandbox (typically agent.Registry.Session).
type Provider func(sandboxID string) (PTY, error)

// Registry holds one Broadcaster per sandbox, created on first attach and kept
// alive across client detach. Closed explicitly when a sandbox stops.
type Registry struct {
	mu        sync.Mutex
	provider  Provider
	existing  Provider // live sessions only, never spawning (agent.Registry.Existing)
	screenFor func(sandboxID string) bool
	bcs       map[string]*Broadcaster
	ringBytes int
	onChange  func(sandboxID string)
}

// NewRegistry constructs a Registry. ringBytes<=0 uses the default; onChange (may
// be nil) is invoked with the sandbox id whenever its attachment set changes.
func NewRegistry(provider Provider, ringBytes int, onChange func(string)) *Registry {
	return &Registry{
		provider:  provider,
		bcs:       map[string]*Broadcaster{},
		ringBytes: ringBytes,
		onChange:  onChange,
	}
}

// SetExisting registers the provider BroadcasterExisting creates a session
// from: it returns the sandbox's live agent session or an error, atomically,
// and never spawns one (SWI-17: agent.Registry.Existing).
func (r *Registry) SetExisting(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.existing = p
}

// SetScreenFor registers which sandboxes' broadcasters keep an agent screen
// (SWI-17, research R2: those with an orchestrator controller). It is asked
// when a broadcaster is created, outside the registry lock; nil = none.
func (r *Registry) SetScreenFor(fn func(sandboxID string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.screenFor = fn
}

// Broadcaster returns the sandbox's session, creating it (and its PTY) on first
// use. A previously-closed session is recreated.
func (r *Registry) Broadcaster(sandboxID string) (*Broadcaster, error) {
	return r.broadcaster(sandboxID, false)
}

// BroadcasterExisting is Broadcaster for a sandbox whose agent must already be
// live (SWI-17, contracts/worker-step.md §1): a missing broadcaster is created
// only from the existing-session provider, so this never starts an agent. With
// no live agent it returns the provider's error and creates nothing.
func (r *Registry) BroadcasterExisting(sandboxID string) (*Broadcaster, error) {
	return r.broadcaster(sandboxID, true)
}

func (r *Registry) broadcaster(sandboxID string, existingOnly bool) (*Broadcaster, error) {
	r.mu.Lock()
	if b, ok := r.bcs[sandboxID]; ok && !b.IsClosed() {
		r.mu.Unlock()
		return b, nil
	}
	screenFor := r.screenFor
	r.mu.Unlock()
	// Asked outside the lock: it reads the sandbox record.
	withScreen := screenFor != nil && screenFor(sandboxID)

	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.bcs[sandboxID]; ok && !b.IsClosed() {
		return b, nil
	}
	provider := r.provider
	if existingOnly {
		provider = r.existing
		if provider == nil {
			return nil, ErrNoExisting
		}
	}
	pty, err := provider(sandboxID)
	if err != nil {
		return nil, err
	}
	var cb func()
	if r.onChange != nil {
		cb = func() { r.onChange(sandboxID) }
	}
	b := newBroadcaster(pty, r.ringBytes, cb, withScreen)
	r.bcs[sandboxID] = b
	return b, nil
}

// live returns the sandbox's open broadcaster, or nil. It never creates one.
func (r *Registry) live(sandboxID string) *Broadcaster {
	r.mu.Lock()
	b, ok := r.bcs[sandboxID]
	r.mu.Unlock()
	if !ok || b.IsClosed() {
		return nil
	}
	return b
}

// Screen returns a copy of the sandbox's agent screen and whether it is stale.
// ok is false when the sandbox has no open broadcaster or it keeps no screen.
// It never creates a broadcaster (SWI-17, contracts/worker-step.md §1).
func (r *Registry) Screen(sandboxID string) (lines []termview.Line, stale, ok bool) {
	b := r.live(sandboxID)
	if b == nil {
		return nil, false, false
	}
	lines, stale = b.Screen()
	return lines, stale, lines != nil
}

// EnableScreen gives the sandbox's open broadcaster an agent screen (see
// Broadcaster.EnableScreen), reporting whether it has one now. It never
// creates a broadcaster.
func (r *Registry) EnableScreen(sandboxID string) bool {
	b := r.live(sandboxID)
	if b == nil {
		return false
	}
	b.EnableScreen()
	return b.HasScreen()
}

// RequestRepaint asks the sandbox's open broadcaster for a debounced repaint
// nudge (the lazy recovery of a stale screen), reporting whether one was
// issued. It never creates a broadcaster.
func (r *Registry) RequestRepaint(sandboxID string) bool {
	b := r.live(sandboxID)
	if b == nil {
		return false
	}
	return b.RequestRepaint()
}

// Counts returns the sandbox's live attachment count and external flag (0/false
// when it has no active session).
func (r *Registry) Counts(sandboxID string) (int, bool) {
	b := r.live(sandboxID)
	if b == nil {
		return 0, false
	}
	return b.Counts()
}

// Close ends and forgets a sandbox's session (called on sandbox stop/destroy).
func (r *Registry) Close(sandboxID string) {
	r.mu.Lock()
	b, ok := r.bcs[sandboxID]
	delete(r.bcs, sandboxID)
	r.mu.Unlock()
	if ok {
		_ = b.Close()
	}
}
