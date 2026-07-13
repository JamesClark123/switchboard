package terminal

import (
	"io"
	"sync"
	"testing"
	"time"
)

// fakePTY is an in-memory PTY: bytes fed via feed() surface on Read; writes and
// resizes are recorded. Close makes Read return io.EOF.
type fakePTY struct {
	mu      sync.Mutex
	out     chan []byte
	written []byte
	resizes [][2]uint16
	closed  bool
}

func newFakePTY() *fakePTY { return &fakePTY{out: make(chan []byte, 64)} }

func (f *fakePTY) feed(b []byte) { f.out <- b }

func (f *fakePTY) Read(p []byte) (int, error) {
	chunk, ok := <-f.out
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func (f *fakePTY) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written = append(f.written, p...)
	return len(p), nil
}

func (f *fakePTY) Resize(cols, rows uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]uint16{cols, rows})
	return nil
}

func (f *fakePTY) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.out)
	}
	return nil
}

func (f *fakePTY) lastResize() ([2]uint16, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.resizes) == 0 {
		return [2]uint16{}, false
	}
	return f.resizes[len(f.resizes)-1], true
}

func recv(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		return b
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for output")
		return nil
	}
}

func TestFanOutToMultipleClients(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()

	c1, err := b.Attach(KindInTUI, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := b.Attach(KindInTUI, 80, 24)
	if err != nil {
		t.Fatal(err)
	}

	pty.feed([]byte("hello"))
	if got := string(recv(t, c1.Out)); got != "hello" {
		t.Fatalf("c1 got %q", got)
	}
	if got := string(recv(t, c2.Out)); got != "hello" {
		t.Fatalf("c2 got %q", got)
	}
}

func TestSnapshotReplaysPriorOutput(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()

	// Output produced before anyone attaches must be in the reconnect snapshot.
	pty.feed([]byte("line1\r\n"))
	pty.feed([]byte("line2\r\n"))
	// Give the read loop time to buffer both chunks.
	waitFor(t, func() bool { return len(b.ring.snapshot()) == len("line1\r\nline2\r\n") })

	c, err := b.Attach(KindInTUI, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(c.Snapshot); got != "line1\r\nline2\r\n" {
		t.Fatalf("snapshot = %q, want prior output", got)
	}
}

func TestDetachKeepsSessionAlive(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()

	c1, _ := b.Attach(KindInTUI, 80, 24)
	c1.Close() // detach

	if b.IsClosed() {
		t.Fatal("session must survive client detach (FR-002/004)")
	}
	if pty.closed {
		t.Fatal("PTY must not be closed on detach")
	}

	// A later reattach still works and sees earlier output.
	pty.feed([]byte("after-detach"))
	waitFor(t, func() bool { return len(b.ring.snapshot()) > 0 })
	c2, err := b.Attach(KindInTUI, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if string(c2.Snapshot) != "after-detach" {
		t.Fatalf("reattach snapshot = %q", c2.Snapshot)
	}
}

func TestRingBoundsMemory(t *testing.T) {
	r := newRing(8)
	r.write([]byte("abcdef"))
	r.write([]byte("ghij"))
	if got := string(r.snapshot()); got != "cdefghij" {
		t.Fatalf("ring = %q, want last 8 bytes", got)
	}
	if len(r.buf) > r.max {
		t.Fatalf("ring exceeded bound: %d > %d", len(r.buf), r.max)
	}
}

func TestSmallestOfAttachedResize(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()

	if _, err := b.Attach(KindInTUI, 100, 40); err != nil {
		t.Fatal(err)
	}
	// A smaller second client must shrink the PTY to the minimum of both.
	if _, err := b.Attach(KindExternal, 80, 24); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { r, ok := pty.lastResize(); return ok && r == [2]uint16{80, 24} })
}

func TestSecondExternalRejected(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()

	if _, err := b.Attach(KindExternal, 80, 24); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Attach(KindExternal, 80, 24); err != ErrExternalBusy {
		t.Fatalf("second external attach err = %v, want ErrExternalBusy", err)
	}
	// An IN_TUI attach alongside the external is allowed (FR-016).
	if _, err := b.Attach(KindInTUI, 80, 24); err != nil {
		t.Fatalf("in-tui alongside external should be allowed: %v", err)
	}
	total, external := b.Counts()
	if total != 2 || !external {
		t.Fatalf("counts = (%d, %v), want (2, true)", total, external)
	}
}

func TestWriteForwardsToPTY(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()
	c, _ := b.Attach(KindInTUI, 80, 24)
	if _, err := c.Write([]byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	pty.mu.Lock()
	got := string(pty.written)
	pty.mu.Unlock()
	if got != "ls\n" {
		t.Fatalf("pty got %q", got)
	}
}

func TestCloseReleasesClients(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	c, _ := b.Attach(KindInTUI, 80, 24)
	_ = b.Close()
	// The client's output channel must close so its handler returns.
	select {
	case _, ok := <-c.Out:
		if ok {
			// drain any buffered frame, then expect close
			select {
			case _, ok2 := <-c.Out:
				if ok2 {
					t.Fatal("expected Out to close after Close()")
				}
			case <-time.After(time.Second):
				t.Fatal("Out did not close after Close()")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Out did not close after Close()")
	}
	if _, err := b.Attach(KindInTUI, 80, 24); err != ErrClosed {
		t.Fatalf("attach after close err = %v, want ErrClosed", err)
	}
}

func TestRegistryReuseAndClose(t *testing.T) {
	made := 0
	reg := NewRegistry(func(string) (PTY, error) {
		made++
		return newFakePTY(), nil
	}, 0, nil)

	b1, err := reg.Broadcaster("sb1")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := reg.Broadcaster("sb1")
	if b1 != b2 || made != 1 {
		t.Fatalf("expected reuse of one broadcaster, made=%d", made)
	}
	reg.Close("sb1")
	if !b1.IsClosed() {
		t.Fatal("Close should end the session")
	}
	// After close, a new attach recreates it.
	if _, err := reg.Broadcaster("sb1"); err != nil || made != 2 {
		t.Fatalf("expected recreate after close, made=%d err=%v", made, err)
	}
}

func TestRegistryCountsPublished(t *testing.T) {
	var mu sync.Mutex
	changes := 0
	reg := NewRegistry(func(string) (PTY, error) { return newFakePTY(), nil }, 0, func(string) {
		mu.Lock()
		changes++
		mu.Unlock()
	})
	b, _ := reg.Broadcaster("sb1")
	c, _ := b.Attach(KindInTUI, 80, 24)
	c.Close()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return changes >= 2 }) // attach + detach
	if n, _ := reg.Counts("sb1"); n != 0 {
		t.Fatalf("counts after detach = %d, want 0", n)
	}
}

func TestConnResizeReconcilesPTY(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	defer b.Close()
	c, _ := b.Attach(KindInTUI, 100, 40)
	c.Resize(70, 20)
	waitFor(t, func() bool { r, ok := pty.lastResize(); return ok && r == [2]uint16{70, 20} })
}

func TestPTYEOFEndsSessionAndReleasesClients(t *testing.T) {
	pty := newFakePTY()
	b := New(pty, 0, nil)
	c, err := b.Attach(KindInTUI, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	// PTY ends on its own (process exit) WITHOUT an explicit Broadcaster.Close.
	_ = pty.Close()
	// The client's channel closes and the session reports closed.
	select {
	case _, ok := <-c.Out:
		_ = ok
	case <-time.After(2 * time.Second):
		t.Fatal("Out did not close after PTY EOF")
	}
	waitFor(t, func() bool { return b.IsClosed() })
	if _, err := b.Attach(KindInTUI, 80, 24); err != ErrClosed {
		t.Fatalf("attach after PTY EOF err = %v, want ErrClosed", err)
	}
}

// waitFor polls cond up to 2s.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
