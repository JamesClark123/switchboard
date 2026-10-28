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
// each host drained by its own goroutine.
type lanePusher struct {
	mu      sync.Mutex
	dirty   map[string]map[string]struct{} // host id -> worker ids
	running map[string]bool
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
// fails while the host is up for a transient reason is queued again; a host
// that is down is left to the reconnect push (pushWorkerStateTo).
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
			continue // down: the reconnect pushes every worker's lanes
		}
		var retry []string
		for id := range batch {
			if err := rt.pushWorker(rt.ctx, api, id, false); err != nil && transient(err) {
				if up, _ := rt.reachability(host); up {
					retry = append(retry, id)
				}
			}
		}
		if len(retry) > 0 && rt.ctx.Err() == nil {
			// A host answering with transient errors is not retried in a tight
			// loop: wait before the next round.
			select {
			case <-rt.ctx.Done():
			case <-time.After(lanePushRetryDelay):
			}
			p.mu.Lock()
			if p.dirty[host] == nil {
				p.dirty[host] = map[string]struct{}{}
			}
			for _, id := range retry {
				p.dirty[host][id] = struct{}{}
			}
			p.mu.Unlock()
		}
	}
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
