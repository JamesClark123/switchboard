package terminal

import (
	"errors"
	"sync"
)

// subBufFrames bounds each client's fan-out queue. A client that cannot keep up
// has frames dropped (best-effort) rather than blocking the single PTY reader and
// starving other clients — the same protect-the-daemon policy as the event hub.
const subBufFrames = 512

// Kind distinguishes an in-TUI terminal view from an external `sxb attach`.
type Kind int

const (
	KindInTUI Kind = iota
	KindExternal
)

// ErrExternalBusy is returned when a second EXTERNAL client tries to attach to a
// session that already has one (FR-014/015). The gRPC layer maps this to
// FailedPrecondition so the client can bring the existing window to the front.
var ErrExternalBusy = errors.New("an external terminal is already attached to this sandbox")

// ErrClosed is returned by Attach once the session's PTY has ended.
var ErrClosed = errors.New("terminal session closed")

// PTY is the minimal per-sandbox PTY session a Broadcaster owns. It is satisfied
// by agent.Session (Read/Write/Resize/Close).
type PTY interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(cols, rows uint16) error
	Close() error
}

type attachment struct {
	id         int
	kind       Kind
	ch         chan []byte
	cols, rows uint16
}

// Broadcaster reads a PTY once and fans its output to N attached clients, keeping
// the PTY alive independently of any client. One per running sandbox.
type Broadcaster struct {
	mu       sync.Mutex
	pty      PTY
	ring     *ring
	subs     map[int]*attachment
	nextID   int
	closed   bool
	onChange func() // invoked (without the lock held) when the attachment set changes
}

// New wraps a PTY and starts its read loop. onChange (may be nil) is called after
// an attach/detach so the caller can publish the updated attachment count.
func New(pty PTY, ringBytes int, onChange func()) *Broadcaster {
	b := &Broadcaster{
		pty:      pty,
		ring:     newRing(ringBytes),
		subs:     map[int]*attachment{},
		onChange: onChange,
	}
	go b.readLoop()
	return b
}

func (b *Broadcaster) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := b.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			b.mu.Lock()
			b.ring.write(chunk)
			for _, a := range b.subs {
				select {
				case a.ch <- chunk:
				default: // slow client: drop to protect the others
				}
			}
			b.mu.Unlock()
		}
		if err != nil {
			b.shutdown()
			return
		}
	}
}

// shutdown marks the session closed and releases all clients (their output
// channels close, so their handlers return). Idempotent.
func (b *Broadcaster) shutdown() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	for _, a := range b.subs {
		close(a.ch)
	}
	b.subs = map[int]*attachment{}
	b.mu.Unlock()
	b.notify()
}

// Conn is a single client's live attachment to a session.
type Conn struct {
	b        *Broadcaster
	id       int
	Snapshot []byte        // raw bytes to replay for an immediate redraw
	Out      <-chan []byte // live PTY output; closed on detach or session end
}

// Attach registers a client and returns its snapshot + live output channel. A
// second EXTERNAL attach returns ErrExternalBusy. cols/rows are the client's
// starting window size (0 to leave the PTY size unchanged).
func (b *Broadcaster) Attach(kind Kind, cols, rows uint16) (*Conn, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	if kind == KindExternal {
		for _, a := range b.subs {
			if a.kind == KindExternal {
				b.mu.Unlock()
				return nil, ErrExternalBusy
			}
		}
	}
	id := b.nextID
	b.nextID++
	a := &attachment{id: id, kind: kind, ch: make(chan []byte, subBufFrames), cols: cols, rows: rows}
	b.subs[id] = a
	snap := b.ring.snapshot()
	b.applyResizeLocked()
	b.mu.Unlock()

	b.notify()
	return &Conn{b: b, id: id, Snapshot: snap, Out: a.ch}, nil
}

// Write forwards client keystrokes to the PTY (shared by all clients).
func (c *Conn) Write(p []byte) (int, error) { return c.b.pty.Write(p) }

// Resize records this client's new window size and reconciles the PTY size.
func (c *Conn) Resize(cols, rows uint16) {
	c.b.mu.Lock()
	if a, ok := c.b.subs[c.id]; ok {
		a.cols, a.rows = cols, rows
		c.b.applyResizeLocked()
	}
	c.b.mu.Unlock()
}

// Close detaches this client. The session and PTY keep running (FR-002/004).
func (c *Conn) Close() { c.b.detach(c.id) }

func (b *Broadcaster) detach(id int) {
	b.mu.Lock()
	a, ok := b.subs[id]
	if ok {
		delete(b.subs, id)
		close(a.ch)
		b.applyResizeLocked()
	}
	b.mu.Unlock()
	if ok {
		b.notify()
	}
}

// applyResizeLocked sets the PTY size to the smallest rows/cols across attached
// clients (research.md R3) so no client's viewport ever overflows. Caller holds mu.
func (b *Broadcaster) applyResizeLocked() {
	var cols, rows uint16
	for _, a := range b.subs {
		if a.cols > 0 && (cols == 0 || a.cols < cols) {
			cols = a.cols
		}
		if a.rows > 0 && (rows == 0 || a.rows < rows) {
			rows = a.rows
		}
	}
	if cols > 0 && rows > 0 {
		_ = b.pty.Resize(cols, rows)
	}
}

// Counts returns the number of attached clients and whether an EXTERNAL one exists.
func (b *Broadcaster) Counts() (total int, external bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.subs {
		total++
		if a.kind == KindExternal {
			external = true
		}
	}
	return total, external
}

// IsClosed reports whether the session has ended.
func (b *Broadcaster) IsClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

// Close ends the session and its PTY (called when the sandbox stops, FR-006).
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	for _, a := range b.subs {
		close(a.ch)
	}
	b.subs = map[int]*attachment{}
	b.mu.Unlock()
	b.notify()
	return b.pty.Close()
}

func (b *Broadcaster) notify() {
	if b.onChange != nil {
		b.onChange()
	}
}
