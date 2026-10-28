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
// the host is up is retried with a per-worker backoff (failures), at most one
// timer per host (armed), up to lanePushMaxAttempts; anything given up on is
// corrected by the next reconnect push.
type lanePusher struct {
	mu       sync.Mutex
	dirty    map[string]map[string]struct{} // host id -> worker ids
	running  map[string]bool
	failures map[string]int                 // worker id -> consecutive transient failures
	retry    map[string]map[string]struct{} // host id -> worker ids waiting for the timer
	armed    map[string]bool                // host id -> its retry timer is pending
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
				delete(p.failures, id)
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
// retrying) clears the worker's failures; a transient one queues it for the
// host's retry timer, arming it unless it already is, until
// lanePushMaxAttempts.
func (rt *runtime) lanePushed(host, id string, again bool) {
	p := &rt.lanes
	p.mu.Lock()
	defer p.mu.Unlock()
	if !again || rt.ctx.Err() != nil {
		delete(p.failures, id)
		return
	}
	if p.failures == nil {
		p.failures, p.retry, p.armed = map[string]int{}, map[string]map[string]struct{}{}, map[string]bool{}
	}
	p.failures[id]++
	if p.failures[id] > lanePushMaxAttempts {
		delete(p.failures, id)
		rt.svc.logf("[orchestrator %s] gave up pushing lanes to %s on %s; the next reconnect pushes them", rt.record().GetName(), id, host)
		return
	}
	if p.retry[host] == nil {
		p.retry[host] = map[string]struct{}{}
	}
	p.retry[host][id] = struct{}{}
	if p.armed[host] {
		return
	}
	var d time.Duration
	for range p.failures[id] {
		d = nextBackoff(d, lanePushRetryMin, linkRetryMax)
	}
	p.armed[host] = true
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		ok := wait(rt.ctx, rt.svc.clock, d)
		p.mu.Lock()
		var ids []string
		for id := range p.retry[host] {
			// A worker pushed fine meanwhile has no failures left.
			if p.failures[id] > 0 {
				ids = append(ids, id)
			}
		}
		delete(p.retry, host)
		p.armed[host] = false
		p.mu.Unlock()
		if ok {
			rt.markLanes(ids...)
		}
	}()
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
