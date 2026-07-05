//go:build e2e

package e2e

import (
	"io"
	"sync"
)

// syncBuffer is a goroutine-safe growing byte buffer for capturing PTY output.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func newSyncBuffer() *syncBuffer { return &syncBuffer{} }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// copyInto streams r into w until EOF (the PTY closes when the child exits).
func copyInto(w io.Writer, r io.Reader) (int64, error) {
	return io.Copy(w, r)
}
