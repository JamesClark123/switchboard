package orchestrator

import (
	"math/bits"
	"slices"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// The dispatch planner (SWI-18 research R8, contracts/dispatch.md): a pure
// index over one snapshot of the record and its free workers. Exclusive runs
// reserve a worker first, so an earlier run that could also go elsewhere does
// not take the only worker a later exclusive run can use; then each run, in
// queue order, walks its candidates (L6):
//
//	exclusive: own reservation → unreserved lane worker → later run's reservation
//	inclusive: unreserved lane worker → open-lane worker → later run's reservation
//	un-laned:  open-lane worker
//
// The planner makes no calls and takes no lock; the pass re-checks every
// candidate against the live state before delivering to it.

// planWorker is one free worker in a pass.
type planWorker struct {
	w        *pb.Worker
	order    int    // roster order
	mask     uint32 // lanes, one bit per lane index
	lanes    int    // lane count
	open     bool   // an open-lane worker
	reserved int    // index+1 of the run holding it; 0 when none
	claimed  bool   // took a run this pass
	out      bool   // left the pass (refused, or failed its re-check)
}

func (pw *planWorker) gone() bool { return pw.claimed || pw.out }

// planRun is one queued run with its effective setting.
type planRun struct {
	run  *pb.Run
	ids  []string
	mode pb.LaneMode
	ok   bool // false: no setting (its action was deleted with no kept copy)
	mask uint32
}

func (r planRun) exclusive() bool { return r.ok && len(r.ids) > 0 && r.mode != pb.LaneMode_LANE_MODE_INCLUSIVE }
func (r planRun) inclusive() bool { return r.ok && len(r.ids) > 0 && r.mode == pb.LaneMode_LANE_MODE_INCLUSIVE }

// lanePlan indexes the free workers of one pass.
type lanePlan struct {
	bit    map[string]uint32        // lane id → its bit
	byLane map[string][]*planWorker // per lane: (lane count, roster order)
	head   map[string]int           // per lane: first entry not gone
	open   []*planWorker            // open-lane workers, roster order
	openAt int                      // first open entry not gone
	resv   map[int]*planWorker      // run index → its reservation
	// touches counts list entries visited (the cost test reads it).
	touches int
}

// buildPlan indexes the roster's free workers. isFree says whether a worker
// is free for this pass (sandbox, host and detaching checks); reasoning and
// lanes-pending workers never are. Lists are ordered by bucketing on lane
// count in roster order, with no comparison sort.
func buildPlan(rec *pb.Orchestrator, isFree func(*pb.Worker) bool) *lanePlan {
	p := &lanePlan{bit: map[string]uint32{}, byLane: map[string][]*planWorker{}, head: map[string]int{}, resv: map[int]*planWorker{}}
	for i, l := range rec.GetLanes() {
		if i < maxLanes {
			p.bit[l.GetId()] = 1 << i
		}
	}
	var buckets [maxLanes + 1][]*planWorker
	for i, w := range rec.GetWorkers() {
		if w.GetReasoning() || w.GetLanesPending() || !isFree(w) {
			continue
		}
		pw := &planWorker{w: w, order: i, open: len(w.GetLaneIds()) == 0, mask: p.maskOf(w.GetLaneIds())}
		pw.lanes = bits.OnesCount32(pw.mask)
		if pw.open {
			p.open = append(p.open, pw)
			continue
		}
		buckets[pw.lanes] = append(buckets[pw.lanes], pw)
	}
	for _, b := range buckets {
		for _, pw := range b {
			for _, id := range pw.w.GetLaneIds() {
				if _, ok := p.bit[id]; ok {
					p.byLane[id] = append(p.byLane[id], pw)
				}
			}
		}
	}
	return p
}

// maskOf is the mask of the lane ids that exist.
func (p *lanePlan) maskOf(ids []string) uint32 {
	var m uint32
	for _, id := range ids {
		m |= p.bit[id]
	}
	return m
}

// better reports whether a is preferred over b for run r: fewest lanes
// outside r's, then roster order (FR-314, amended).
func better(a, b *planWorker, r planRun) bool {
	if b == nil {
		return true
	}
	oa, ob := bits.OnesCount32(a.mask&^r.mask), bits.OnesCount32(b.mask&^r.mask)
	if oa != ob {
		return oa < ob
	}
	return a.order < b.order
}

// firstIn returns the first worker of a lane's list that pick accepts. Gone
// entries at the head are passed for good; others are only stepped over.
func (p *lanePlan) firstIn(lane string, pick func(*planWorker) bool) *planWorker {
	list := p.byLane[lane]
	i := p.head[lane]
	for i < len(list) && list[i].gone() {
		p.touches++
		i++
	}
	p.head[lane] = i
	for ; i < len(list); i++ {
		p.touches++
		if pw := list[i]; !pw.gone() && pick(pw) {
			return pw
		}
	}
	return nil
}

// bestOf compares the first worker pick accepts in each of r's lanes.
func (p *lanePlan) bestOf(r planRun, pick func(*planWorker) bool) *planWorker {
	var best *planWorker
	for _, id := range r.ids {
		if pw := p.firstIn(id, pick); pw != nil && better(pw, best, r) {
			best = pw
		}
	}
	return best
}

// firstOpen returns the first open-lane worker pick accepts.
func (p *lanePlan) firstOpen(pick func(*planWorker) bool) *planWorker {
	for p.openAt < len(p.open) && p.open[p.openAt].gone() {
		p.touches++
		p.openAt++
	}
	for _, pw := range p.open[p.openAt:] {
		p.touches++
		if !pw.gone() && pick(pw) {
			return pw
		}
	}
	return nil
}

// reserve gives each exclusive run, in queue order, one worker of its lanes.
func (p *lanePlan) reserve(runs []planRun) {
	for i, r := range runs {
		if r.run.GetType() != pb.ActionType_ACTION_TYPE_PROMPT || !r.exclusive() {
			continue
		}
		if pw := p.bestOf(r, func(pw *planWorker) bool { return pw.reserved == 0 }); pw != nil {
			pw.reserved = i + 1
			p.resv[i] = pw
		}
	}
}

// next returns run i's next candidate, skipping the workers in skip (ones
// that failed a lane re-check for this run only); nil when none is left.
func (p *lanePlan) next(i int, r planRun, skip map[*planWorker]bool) *planWorker {
	unreserved := func(pw *planWorker) bool { return pw.reserved == 0 && !skip[pw] }
	// Any reservation still held belongs to a later run: earlier runs have
	// used or released theirs.
	later := func(pw *planWorker) bool { return pw.reserved > i+1 && !skip[pw] }
	switch {
	case r.exclusive():
		if pw := p.resv[i]; pw != nil && !pw.gone() && !skip[pw] {
			return pw
		}
		if pw := p.bestOf(r, unreserved); pw != nil {
			return pw
		}
		return p.bestOf(r, later)
	case r.inclusive():
		if pw := p.bestOf(r, unreserved); pw != nil {
			return pw
		}
		if pw := p.firstOpen(func(pw *planWorker) bool { return !skip[pw] }); pw != nil {
			return pw
		}
		return p.bestOf(r, later)
	default:
		return p.firstOpen(func(pw *planWorker) bool { return !skip[pw] })
	}
}

// claim records that run i was delivered to pw: a reservation pw held for a
// later run is taken from it, and run i's own reservation elsewhere is
// released.
func (p *lanePlan) claim(i int, pw *planWorker) {
	if pw.reserved != 0 && pw.reserved != i+1 {
		delete(p.resv, pw.reserved-1)
	}
	pw.reserved = 0
	pw.claimed = true
	p.release(i)
}

// drop takes pw out of the pass, with any reservation on it.
func (p *lanePlan) drop(pw *planWorker) {
	if pw.reserved != 0 {
		delete(p.resv, pw.reserved-1)
		pw.reserved = 0
	}
	pw.out = true
}

// release frees run i's reservation, if it still holds one.
func (p *lanePlan) release(i int) {
	if pw := p.resv[i]; pw != nil {
		pw.reserved = 0
		delete(p.resv, i)
	}
}

// laneIDsExist reports whether every id names a lane of rec.
func laneIDsExist(rec *pb.Orchestrator, ids []string) bool {
	return !slices.ContainsFunc(ids, func(id string) bool { return laneByID(rec, id) == nil })
}
