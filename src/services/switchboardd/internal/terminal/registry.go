package terminal

import "sync"

// Provider lazily supplies the PTY for a sandbox (typically agent.Registry.Session).
type Provider func(sandboxID string) (PTY, error)

// Registry holds one Broadcaster per sandbox, created on first attach and kept
// alive across client detach. Closed explicitly when a sandbox stops.
type Registry struct {
	mu        sync.Mutex
	provider  Provider
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

// Broadcaster returns the sandbox's session, creating it (and its PTY) on first
// use. A previously-closed session is recreated.
func (r *Registry) Broadcaster(sandboxID string) (*Broadcaster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.bcs[sandboxID]; ok && !b.IsClosed() {
		return b, nil
	}
	pty, err := r.provider(sandboxID)
	if err != nil {
		return nil, err
	}
	var cb func()
	if r.onChange != nil {
		cb = func() { r.onChange(sandboxID) }
	}
	b := New(pty, r.ringBytes, cb)
	r.bcs[sandboxID] = b
	return b, nil
}

// Counts returns the sandbox's live attachment count and external flag (0/false
// when it has no active session).
func (r *Registry) Counts(sandboxID string) (int, bool) {
	r.mu.Lock()
	b, ok := r.bcs[sandboxID]
	r.mu.Unlock()
	if !ok || b.IsClosed() {
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
