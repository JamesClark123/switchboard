package portforward

import "sync"

// maxCapturedOutput caps a service's captured stdout+stderr (research R9). Same
// 1 MiB budget escape hatch uses, spent differently — see tailBuffer.
const maxCapturedOutput = 1 << 20

// tailBuffer accumulates output up to limit bytes, keeping the LAST bytes written
// and recording that truncation occurred. Safe for concurrent writers (stdout +
// stderr stream into it from separate goroutines).
//
// This is deliberately the opposite retention from escapehatch.boundedBuffer,
// which keeps the head. A run-to-completion command usually fails at the start —
// a bad flag, a missing binary — so its first bytes are the diagnostic ones. A
// long-running service is the reverse: it printed a startup banner an hour ago and
// then died, and the only bytes that explain why are the last ones. Keeping the
// head here would reliably capture the least useful part of the log (FR-051).
type tailBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       []byte
	truncated bool
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.limit <= 0 {
		b.truncated = b.truncated || n > 0
		return n, nil
	}
	// A single write larger than the whole budget: keep only its tail.
	if n >= b.limit {
		b.buf = append(b.buf[:0], p[n-b.limit:]...)
		b.truncated = true
		return n, nil
	}
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.limit; over > 0 {
		// Drop the oldest bytes. copy-then-truncate keeps the backing array, so a
		// noisy service does not reallocate on every write once it is at capacity.
		copy(b.buf, b.buf[over:])
		b.buf = b.buf[:b.limit]
		b.truncated = true
	}
	return n, nil
}

// result returns the retained tail and whether anything was dropped.
func (b *tailBuffer) result() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf), b.truncated
}
