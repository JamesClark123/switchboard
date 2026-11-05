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
	// repaintSettle is how long the output must stay quiet after a chunk parsed
	// past a nudge, or a restore over a drawn frame, before the screen reads
	// fresh: the first chunk may be only the start of the repaint, over a
	// just-reset blank view.
	repaintSettle = 50 * time.Millisecond
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
	// repaintAt is when the last chunk past such a nudge was parsed, or when a
	// nudge restored over a drawn frame (zero: neither yet); stale clears once
	// it is repaintSettle old (isStale).
	repaintAt time.Time
	// shrunk is set from a nudge's shrink to its restore, unless a reset or a
	// client's resize came between. drawn says the screen holds the agent's
	// frame by then: it read fresh at the shrink, or a chunk was parsed since.
	shrunk, drawn bool

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
// stale until a chunk is parsed during or after a later repaint nudge and the
// output has then been quiet for repaintSettle from the restore at the earliest.
// It never nudges on its own: a nudge repaints every attached client too, so
// recovery waits for a reader to ask (RequestRepaint).
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
			if sc.stale && !sc.dropped {
				sc.drawn = sc.drawn || sc.shrunk
				if sc.nudges.Load() > sc.resetAt {
					sc.repaintAt = time.Now()
				}
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
	sc.dropped = false
	sc.markStaleLocked()
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
	if sc.stale && !sc.dropped && !sc.repaintAt.IsZero() && time.Since(sc.repaintAt) >= repaintSettle {
		sc.stale, sc.repaintAt = false, time.Time{}
	}
	return sc.stale
}

// markStale marks the screen stale for a resize the PTY is about to get. Like a
// reset it waits for a later nudge's repaint; the caller counts the resize as
// that nudge once it is applied, so the repaint it causes clears the flag when
// the output settles. An agent that repaints nothing stays stale until a reader
// asks (RequestRepaint).
func (sc *screen) markStale() {
	sc.flagMu.Lock()
	defer sc.flagMu.Unlock()
	sc.markStaleLocked()
}

func (sc *screen) markStaleLocked() {
	sc.stale = true
	sc.resetAt, sc.repaintAt = sc.nudges.Load(), time.Time{}
	sc.shrunk, sc.drawn = false, false
}

// nudgeShrinks marks the screen stale for a nudge's shrink: what the agent draws
// for the smaller size must not read fresh before the restore. A screen that
// read fresh, or had a repaint settling, already holds the agent's frame.
func (sc *screen) nudgeShrinks() {
	sc.flagMu.Lock()
	defer sc.flagMu.Unlock()
	drawn := !sc.stale || !sc.repaintAt.IsZero()
	sc.markStaleLocked()
	sc.shrunk, sc.drawn = true, drawn
}

// nudgeRestored counts a nudge once the PTY is back at full size. An agent may
// draw on the shrink and nothing on the restore, or nothing on either, so a
// drawn frame starts the settle here rather than waiting for output that may
// never come; output that does come pushes it back (parse). A screen with no
// frame stays stale until a chunk arrives.
func (sc *screen) nudgeRestored() {
	sc.flagMu.Lock()
	defer sc.flagMu.Unlock()
	sc.nudges.Add(1)
	if sc.shrunk && sc.drawn {
		sc.repaintAt = time.Now()
	}
	sc.shrunk, sc.drawn = false, false
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
	b.nudgeLocked(false)
}

// Screen returns a copy of the agent screen's live rows and whether the screen
// is stale (a chunk was dropped, the size changed, or no repaint has been parsed
// since it was enabled). lines is nil when the broadcaster keeps no screen.
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
	return b.nudgeLocked(false)
}

// nudgeLocked issues a repaint nudge, debounced to one per repaintEvery unless
// force is set. It reports whether a nudge was issued or, for force, queued.
// Caller holds mu.
//
// While a nudge is in flight (startNudgeLocked) nothing starts another, so a
// client flooding requests cannot resize the PTY faster than that. A forced
// request is recorded instead and gets one more nudge when the flight ends: its
// client blanked its view after the agent may have drawn its last frame, and
// the restore may draw nothing, so only a later nudge is sure to reach it.
func (b *Broadcaster) nudgeLocked(force bool) bool {
	if !b.nudgeableLocked() {
		return false
	}
	if b.nudging {
		b.nudgeAgain = b.nudgeAgain || force
		return force
	}
	if !force && !b.lastNudge.IsZero() && time.Since(b.lastNudge) < repaintEvery {
		return false
	}
	b.startNudgeLocked(nudgeGap)
	return true
}

func (b *Broadcaster) nudgeableLocked() bool {
	return !b.closed && b.cols != 0 && b.rows >= 2
}

// startNudgeLocked shrinks the PTY by one row. The resize back runs after gap on
// its own goroutine, to the size current then (a client may have resized
// meanwhile), and counts the nudge for the stale flag. The nudge stays in flight
// for a further gap, so the agent sees the full size before any next shrink: a
// signal it handled after both would look like no change at all. The screen
// reads stale from the shrink until the restore's settle (nudgeRestored). gap is
// a parameter so the timers never read nudgeGap, which tests set.
func (b *Broadcaster) startNudgeLocked(gap time.Duration) {
	b.lastNudge, b.nudging = time.Now(), true
	if sc := b.scr.Load(); sc != nil {
		sc.nudgeShrinks()
	}
	_ = b.pty.Resize(b.cols, b.rows-1)
	time.AfterFunc(gap, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.closed {
			return
		}
		_ = b.pty.Resize(b.cols, b.rows)
		if sc := b.scr.Load(); sc != nil {
			sc.nudgeRestored()
		}
		time.AfterFunc(gap, func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			again := b.nudgeAgain && b.nudgeableLocked()
			b.nudging, b.nudgeAgain = false, false
			if again {
				b.startNudgeLocked(gap)
			}
		})
	})
}
