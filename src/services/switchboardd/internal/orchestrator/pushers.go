package orchestrator

import (
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Background pushes to worker hosts (SWI-18 research R12/R13): both coalesce
// their triggers and hold no lock across a call, so a save never waits on a
// host.

// briefer coalesces briefing refreshes: a pending flag and one goroutine.
type briefer struct {
	mu      sync.Mutex
	pending bool
	running bool
}

// requestBriefing asks for every reachable worker's name + briefing to be
// pushed (actions changed, renamed, a worker adopted, a lane used by an
// action renamed or deleted). It returns at once.
func (rt *runtime) requestBriefing() {
	b := &rt.brief
	b.mu.Lock()
	b.pending = true
	if b.running {
		b.mu.Unlock()
		return
	}
	b.running = true
	b.mu.Unlock()
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		for {
			b.mu.Lock()
			if !b.pending || rt.ctx.Err() != nil {
				b.running = false
				b.mu.Unlock()
				return
			}
			b.pending = false
			b.mu.Unlock()
			rt.pushBriefings()
		}
	}()
}

// pushBriefings sends the current name + briefing (and lanes) to every
// reachable worker; hosts that are down catch up on reconnect.
func (rt *runtime) pushBriefings() {
	for _, h := range rt.hostIDs() {
		api, err := rt.apiFor(h)
		if err != nil {
			continue
		}
		rt.pushWorkerStateTo(rt.ctx, h, api)
	}
}

// lanePusher keeps the workers whose lane display must be pushed, per host,
// each host drained by its own goroutine. A push that fails transiently while
// the host is up is retried with the worker's own backoff (retry), driven by
// at most one retry loop per host (retriers), up to lanePushMaxAttempts; a
// worker given up on is corrected by its next lane change, briefing refresh or
// reconnect push.
type lanePusher struct {
	mu       sync.Mutex
	dirty    map[string]map[string]struct{} // host id -> worker ids
	running  map[string]bool
	retry    map[string]*laneRetry   // worker id -> its pending retry
	retriers map[string]*laneRetrier // host id -> its retry loop
}

// laneRetry is one worker's retry state.
type laneRetry struct {
	host    string
	n       int           // consecutive transient failures
	backoff time.Duration // the delay before its latest retry
	due     time.Time
	queued  bool // marked again; waiting for that push's outcome
}

// laneRetrier is a host's retry loop: it sleeps until its earliest due retry,
// woken early when an earlier one joins.
type laneRetrier struct {
	wake  chan struct{} // buffered 1
	timer Timer         // the pending sleep (nil while awake)
}

// nextBackoff doubles cur from lo to hi (cur 0 starts at lo).
func nextBackoff(cur, lo, hi time.Duration) time.Duration {
	if cur == 0 {
		return lo
	}
	return min(2*cur, hi)
}

// markLanes queues lane display pushes for workers (by sandbox id); workers
// no longer on the roster are skipped.
func (rt *runtime) markLanes(sandboxIDs ...string) {
	rec := rt.record()
	p := &rt.lanes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirty == nil {
		p.dirty, p.running = map[string]map[string]struct{}{}, map[string]bool{}
	}
	for _, id := range sandboxIDs {
		host := hostOf(rec, id)
		if host == "" {
			continue
		}
		if p.dirty[host] == nil {
			p.dirty[host] = map[string]struct{}{}
		}
		p.dirty[host][id] = struct{}{}
		if !p.running[host] {
			p.running[host] = true
			rt.wg.Add(1)
			go rt.drainLanes(host)
		}
	}
}

// drainLanes pushes a host's dirty workers until none is left. A push that
// fails transiently while the host is up is queued for the host's retry timer,
// so it never holds up the host's other pushes; a host that is down is left to
// the reconnect push (pushWorkerStateTo).
func (rt *runtime) drainLanes(host string) {
	defer rt.wg.Done()
	p := &rt.lanes
	for {
		p.mu.Lock()
		batch := p.dirty[host]
		if len(batch) == 0 || rt.ctx.Err() != nil {
			p.running[host] = false
			p.mu.Unlock()
			return
		}
		delete(p.dirty, host)
		p.mu.Unlock()
		api, err := rt.apiFor(host)
		if err != nil {
			// Down: the reconnect pushes every worker's lanes.
			p.mu.Lock()
			for id := range batch {
				p.dropRetry(id)
			}
			p.mu.Unlock()
			continue
		}
		for id := range batch {
			err := rt.pushWorker(rt.ctx, api, id, false)
			again := false
			if err != nil && transient(err) {
				again, _ = rt.reachability(host)
			}
			rt.lanePushed(host, id, again)
		}
	}
}

// lanePushed records one push's outcome: a success (or a failure not worth
// retrying) drops the worker's retry; a transient one schedules the next
// after the worker's own backoff, until lanePushMaxAttempts.
func (rt *runtime) lanePushed(host, id string, again bool) {
	p := &rt.lanes
	p.mu.Lock()
	if !again || rt.ctx.Err() != nil {
		p.dropRetry(id)
		p.mu.Unlock()
		return
	}
	if p.retry == nil {
		p.retry, p.retriers = map[string]*laneRetry{}, map[string]*laneRetrier{}
	}
	r := p.retry[id]
	if r == nil {
		r = &laneRetry{}
		p.retry[id] = r
	}
	r.n++
	if r.n > lanePushMaxAttempts {
		p.dropRetry(id)
		p.mu.Unlock()
		rt.svc.logf("[orchestrator %s] gave up pushing lanes to %s on %s; its next lane change, briefing refresh or reconnect pushes them", rt.name(), id, host)
		return
	}
	r.host, r.queued = host, false
	r.backoff = nextBackoff(r.backoff, lanePushRetryMin, linkRetryMax)
	r.due = rt.svc.clock.Now().Add(r.backoff)
	if lr := p.retriers[host]; lr != nil {
		lr.poke()
	} else {
		lr = &laneRetrier{wake: make(chan struct{}, 1)}
		p.retriers[host] = lr
		rt.wg.Add(1)
		go rt.retryLanes(host, lr)
	}
	p.mu.Unlock()
}

// poke wakes the loop (it never blocks).
func (lr *laneRetrier) poke() {
	select {
	case lr.wake <- struct{}{}:
	default:
	}
}

// dropRetry forgets a worker's retry and wakes its host's loop, which ends
// once nothing is left to retry. The caller holds p.mu.
func (p *lanePusher) dropRetry(id string) {
	r := p.retry[id]
	if r == nil {
		return
	}
	delete(p.retry, id)
	if lr := p.retriers[r.host]; lr != nil {
		lr.poke()
	}
}

// retryLanes is a host's retry loop: it marks the workers whose retry is due
// and sleeps until the next one, ending when none is left.
func (rt *runtime) retryLanes(host string, lr *laneRetrier) {
	defer rt.wg.Done()
	p := &rt.lanes
	for {
		p.mu.Lock()
		if lr.timer != nil {
			lr.timer.Stop()
			lr.timer = nil
		}
		now := rt.svc.clock.Now()
		var due []string
		var next time.Time
		for id, r := range p.retry {
			switch {
			case r.host != host || r.queued:
			case !r.due.After(now):
				r.queued = true
				due = append(due, id)
			case next.IsZero() || r.due.Before(next):
				next = r.due
			}
		}
		if rt.ctx.Err() != nil || (len(due) == 0 && next.IsZero()) {
			delete(p.retriers, host)
			p.mu.Unlock()
			return
		}
		if len(due) == 0 {
			lr.timer = rt.svc.clock.AfterFunc(next.Sub(now), lr.poke)
		}
		p.mu.Unlock()
		if len(due) > 0 {
			rt.retryDue(host, due)
			continue
		}
		select {
		case <-lr.wake:
		case <-rt.ctx.Done():
		}
	}
}

// retryDue marks due workers again; a worker that left the roster or the host
// meanwhile drops its retry.
func (rt *runtime) retryDue(host string, ids []string) {
	rec := rt.record()
	var mark []string
	p := &rt.lanes
	p.mu.Lock()
	for _, id := range ids {
		if hostOf(rec, id) == host {
			mark = append(mark, id)
		} else {
			p.dropRetry(id)
		}
	}
	p.mu.Unlock()
	rt.markLanes(mark...)
}

// definiteRefusal reports whether a remote call was answered with a refusal,
// so nothing it would have done happened.
func definiteRefusal(err error) bool {
	switch status.Code(err) {
	case codes.AlreadyExists, codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument, codes.PermissionDenied:
		return true
	}
	return false
}

// transient reports whether a push failure is worth retrying.
func transient(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Aborted, codes.ResourceExhausted:
		return true
	}
	return false
}
