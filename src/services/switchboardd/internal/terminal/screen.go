package terminal

import (
	"sync"
	"sync/atomic"
	"time"

	termview "github.com/jamesclark123/switchboard/libs/switchboard-termview"
)

// The daemon's agent screen (SWI-17, research R2). A broadcaster whose sandbox
// has an orchestrator controller keeps a termview.Screen (no scrollback) fed
// from its single PTY reader, so the step check can see the agent's input box
// and confirmation lines with no client attached. Other sandboxes keep none.
const (
	// stepScreenCols × stepScreenRows is the PTY and screen size a screen gets
	// when no client is attached to size the PTY (the size R1 probed).
	stepScreenCols = 140
	stepScreenRows = 45
	// screenQueue bounds the parser goroutine's channel. A full queue drops the
	// chunk and marks the screen stale rather than slowing the reader.
	screenQueue = 256
)

// repaintEvery debounces repaint nudges (at most one per sandbox per period)
// and nudgeGap is how long the PTY stays one row smaller before it is resized
// back, so the agent sees two distinct sizes and redraws. Variables so tests
// can shorten them.
var (
	repaintEvery = time.Second
	nudgeGap     = 50 * time.Millisecond
	// beforeParse, when set, runs before each chunk is parsed (tests use it to
	// stall the parser). Copied into each screen when it is created.
	beforeParse func()
)

// screen is a broadcaster's agent screen: the emulator, its parser goroutine's
// queue and the stale flag. The emulator has its own lock; flagMu guards only
// the flags, so the reader never waits on a parse.
type screen struct {
	view  *termview.Screen
	queue chan []byte

	flagMu  sync.Mutex
	dropped bool   // a chunk was dropped; the parser resets the screen at its next chunk
	stale   bool   // the screen may not match the agent's; set until a repaint is parsed
	resetAt uint64 // nudges at the last reset: a later nudge's repaint clears stale

	nudges atomic.Uint64 // repaint nudges completed (resized back to full size)
	hook   func()        // beforeParse at creation
}

func newScreen(cols, rows uint16) *screen {
	return &screen{
		view:  termview.New(int(cols), int(rows), termview.WithoutScrollback()),
		queue: make(chan []byte, screenQueue),
		// Stale until the first repaint: bytes the agent drew before the screen
		// existed (or before the PTY was read at all) are not on it.
		stale: true,
		hook:  beforeParse,
	}
}

// offer hands a chunk to the parser without blocking. On overflow the chunk is
// dropped and the screen marked stale (research R2).
func (sc *screen) offer(chunk []byte) {
	select {
	case sc.queue <- chunk:
	default:
		sc.flagMu.Lock()
		sc.dropped, sc.stale = true, true
		sc.flagMu.Unlock()
	}
}

// parse is the parser goroutine: it feeds queued chunks to the emulator until
// done closes. After a drop it discards what is queued and resets the screen
// (a dropped chunk may have cut an escape sequence in half); the screen stays
// stale until a chunk is parsed after a later repaint nudge. It never nudges on
// its own: a nudge repaints every attached client too, so recovery waits for a
// reader to ask (RequestRepaint).
func (sc *screen) parse(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case chunk := <-sc.queue:
			if sc.hook != nil {
				sc.hook()
			}
			if sc.takeDrop() {
				sc.drain()
				sc.view.Reset()
				continue
			}
			_, _ = sc.view.Write(chunk)
			sc.flagMu.Lock()
			if sc.stale && !sc.dropped && sc.nudges.Load() > sc.resetAt {
				sc.stale = false
			}
			sc.flagMu.Unlock()
		}
	}
}

// takeDrop clears a pending drop, reporting whether there was one, and records
// the reset point the next repaint must follow.
func (sc *screen) takeDrop() bool {
	sc.flagMu.Lock()
	defer sc.flagMu.Unlock()
	if !sc.dropped {
		return false
	}
	sc.dropped, sc.stale = false, true
	sc.resetAt = sc.nudges.Load()
	return true
}

// drain discards every chunk queued right now.
func (sc *screen) drain() {
	for {
		select {
		case <-sc.queue:
		default:
			return
		}
	}
}

func (sc *screen) isStale() bool {
	sc.flagMu.Lock()
	defer sc.flagMu.Unlock()
	return sc.stale
}

// EnableScreen gives the broadcaster an agent screen if it has none (SWI-17,
// research R2), and nudges a repaint so the screen fills. It is called when a
// sandbox becomes a worker (its broadcaster may predate that) and on its first
// step. With no client attached the PTY and the screen are set to the fixed
// step size; otherwise the screen takes the PTY's current size. A closed
// broadcaster is left alone.
func (b *Broadcaster) EnableScreen() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enableScreenLocked()
}

func (b *Broadcaster) enableScreenLocked() {
	if b.closed || b.scr.Load() != nil {
		return
	}
	if len(b.subs) == 0 || b.cols == 0 || b.rows == 0 {
		b.cols, b.rows = stepScreenCols, stepScreenRows
		_ = b.pty.Resize(b.cols, b.rows)
	}
	sc := newScreen(b.cols, b.rows)
	b.scr.Store(sc)
	go sc.parse(b.done)
	b.nudgeLocked()
}

// Screen returns a copy of the agent screen's live rows and whether the screen
// is stale (a chunk was dropped, or no repaint has been parsed since it was
// enabled). lines is nil when the broadcaster keeps no screen.
func (b *Broadcaster) Screen() (lines []termview.Line, stale bool) {
	sc := b.scr.Load()
	if sc == nil {
		return nil, false
	}
	return sc.view.Lines(), sc.isStale()
}

// HasScreen reports whether the broadcaster keeps an agent screen.
func (b *Broadcaster) HasScreen() bool { return b.scr.Load() != nil }

// RequestRepaint nudges the agent to redraw (a resize one row smaller, then
// back), debounced to one per repaintEvery. A reader that finds the screen
// stale calls it (the lazy recovery, research R2). It reports whether a nudge
// was issued.
func (b *Broadcaster) RequestRepaint() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nudgeLocked()
}

// nudgeLocked issues a debounced repaint nudge. Caller holds mu. The resize
// back runs after nudgeGap on its own goroutine, to the size current then (a
// client may have resized meanwhile), and counts the nudge for the stale flag.
func (b *Broadcaster) nudgeLocked() bool {
	if b.closed || b.cols == 0 || b.rows < 2 {
		return false
	}
	now := time.Now()
	if !b.lastNudge.IsZero() && now.Sub(b.lastNudge) < repaintEvery {
		return false
	}
	b.lastNudge = now
	_ = b.pty.Resize(b.cols, b.rows-1)
	time.AfterFunc(nudgeGap, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.closed {
			return
		}
		_ = b.pty.Resize(b.cols, b.rows)
		if sc := b.scr.Load(); sc != nil {
			sc.nudges.Add(1)
		}
	})
	return true
}
