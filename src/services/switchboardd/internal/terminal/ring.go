// Package terminal turns the daemon's per-sandbox PTY into a persistent,
// multi-client terminal session (feature 003). A Broadcaster reads the PTY once,
// retains recent output in a bounded ring buffer for replay on (re)attach, and
// fans live output out to every attached client. Detaching a client never stops
// the session, so work — including AI/agent prompts — keeps running with no
// terminal attached (FR-001..005). Replay uses raw PTY bytes (the rtach model):
// the client's real terminal performs VT interpretation, so no VT-emulation
// dependency is needed daemon-side (research.md R1/R2; the candidate
// charmbracelet/x/vt was rejected during T001 as an unstable pseudo-version that
// hangs at runtime).
package terminal

// defaultRingBytes bounds the recent-output history replayed to a reconnecting
// client. Large enough to cover a full-screen repaint plus recent scrollback,
// small enough to stay cheap per persistent session on a shared host daemon.
const defaultRingBytes = 256 * 1024

// ring is a bounded byte buffer retaining the most recent bytes written to it.
// Not safe for concurrent use; the Broadcaster guards it with its mutex.
type ring struct {
	buf []byte
	max int
}

func newRing(max int) *ring {
	if max <= 0 {
		max = defaultRingBytes
	}
	return &ring{max: max}
}

// write appends p, evicting the oldest bytes so the buffer never exceeds max.
// When trimming, it copies into a right-sized slice so the backing array does
// not grow without bound.
func (r *ring) write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		trimmed := make([]byte, r.max)
		copy(trimmed, r.buf[len(r.buf)-r.max:])
		r.buf = trimmed
	}
}

// snapshot returns a copy of the current contents (oldest first).
func (r *ring) snapshot() []byte {
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}
