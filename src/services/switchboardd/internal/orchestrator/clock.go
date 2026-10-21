package orchestrator

import (
	"context"
	"time"
)

// Clock is the time source the runtime uses for every wait: backoff, the link
// ping, the review grace and the timed-event scheduler. Tests inject a fake
// (research R16); production uses the wall clock.
type Clock interface {
	Now() time.Time
	// AfterFunc runs f in its own goroutine once d has elapsed.
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer is what AfterFunc returns.
type Timer interface {
	Stop() bool
}

// realClock is the wall clock.
type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// wait blocks for d on the clock, or until ctx is done. It returns false when
// the context ended first.
func wait(ctx context.Context, clock Clock, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	ch := make(chan struct{})
	t := clock.AfterFunc(d, func() { close(ch) })
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		t.Stop()
		return false
	}
}
