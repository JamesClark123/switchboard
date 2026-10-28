package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// runtime is the live side of one orchestrator (research R1): its host links,
// the latest record of every sandbox it controls, the dispatch pass, the
// scheduler and the review timers. One goroutine set per orchestrator, started
// at Start for every record and at Create.
type runtime struct {
	svc *Service
	id  string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// recMu orders record writes (SWI-18 research R7): rt.update and
	// rt.updateRuns hold it across the bolt transaction and the store of the
	// result into rec, so the cache and the store agree on order. Lock order:
	// runs.mu → recMu → bolt → mu.
	recMu sync.Mutex

	mu    sync.Mutex
	rec   *pb.Orchestrator       // latest persisted record (never handed out without cloning)
	links map[string]*hostLink   // host id -> link (the own host included)
	sbs   map[string]*pb.Sandbox // sandbox id -> latest record seen (cacheLocked keeps the newest)
	// recGen counts record commits (SWI-18 research R8). Written with rec under
	// recMu and mu; read under mu. A dispatch pass re-validates a claim against
	// rec when it changed since the pass's snapshot.
	recGen uint64
	// detaching holds workers whose detach through the orchestrator is under
	// way; delivering holds workers a run is being delivered to. Both in memory
	// only (cleared by a restart, FR-322), under mu, and mutually exclusive.
	detaching  map[string]struct{}
	delivering map[string]struct{}

	// user story 3+: dispatch, review timers, scheduler (dispatch.go, runs.go,
	// scheduler.go) hang their state here.
	// fireMu makes "is a run of this event open? → queue one" atomic between the
	// scheduler's tick and a manual FireTimedEvent, so dedupe holds for both.
	fireMu sync.Mutex
	// beforeFireLock, when set, runs in FireTimedEvent just before it takes fireMu.
	// A test seam (nil in production) that lets a test act at exactly that point
	// instead of sleeping and hoping.
	beforeFireLock  func()
	dispatchMu      sync.Mutex
	dispatchPending bool
	dispatching     bool
	runs            runState
	reviewTimers    map[string]Timer // run id -> armed review-grace timer
	reported        map[string]bool  // run id -> a completion report arrived
	inputNotified map[string]bool // run id -> RUN_NEEDS_INPUT raised for the current input wait
	// pushMu holds one lock per worker sandbox serializing the pushes of its
	// controller.run (SWI-12): the needs_review flag is computed from the run's
	// live state while holding it, and the clearing push of a run that closed
	// takes it too, so the last push to land always matches the run (review,
	// resume and close race otherwise). Per worker, so a slow host never holds
	// up another's pushes. Guarded by mu.
	pushMu    map[string]*sync.Mutex
	sched     *scheduler
	reasoning reasoningState
	// brief coalesces briefing pushes; lanes coalesces lane display pushes
	// (SWI-18 research R12/R13). Both push in the background, no lock held.
	brief briefer
	lanes lanePusher
}

func newRuntime(ctx context.Context, svc *Service, rec *pb.Orchestrator) *runtime {
	rctx, cancel := context.WithCancel(ctx)
	rt := &runtime{
		svc:           svc,
		id:            rec.GetId(),
		ctx:           rctx,
		cancel:        cancel,
		rec:           proto.Clone(rec).(*pb.Orchestrator),
		links:         map[string]*hostLink{},
		sbs:           map[string]*pb.Sandbox{},
		detaching:     map[string]struct{}{},
		delivering:    map[string]struct{}{},
		reviewTimers:  map[string]Timer{},
		reported:      map[string]bool{},
		inputNotified: map[string]bool{},
	}
	return rt
}

// start opens the host links and the scheduler.
func (rt *runtime) start() {
	rt.syncLinks()
	rt.startScheduler()
}

// stop cancels every link and timer and waits for their goroutines.
func (rt *runtime) stop() {
	rt.cancel()
	rt.mu.Lock()
	for _, t := range rt.reviewTimers {
		t.Stop()
	}
	rt.reviewTimers = map[string]Timer{}
	rt.mu.Unlock()
	rt.stopScheduler()
	rt.wg.Wait()
}

// record returns a clone of the latest record.
func (rt *runtime) record() *pb.Orchestrator {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return proto.Clone(rt.rec).(*pb.Orchestrator)
}

// name returns the orchestrator's current name.
func (rt *runtime) name() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.rec.GetName()
}

// errNoChange, returned by an update callback, skips the write: the update
// returns the current record and no error.
var errNoChange = errors.New("no change")

// update applies mutate to the persisted record and the cached copy.
//
// The callback runs inside the bolt write transaction with recMu held (lock
// order runs.mu → recMu → bolt → mu): it may touch only the record and, under
// mu, in-memory state. It never takes runs.mu, calls failRun/putRun or any
// rt.svc.reg method (bolt's writer lock is not re-entrant), notifies or emits;
// those run after the commit, from what the callback returned (research R7).
func (rt *runtime) update(mutate func(*pb.Orchestrator) error) (*pb.Orchestrator, error) {
	rt.recMu.Lock()
	defer rt.recMu.Unlock()
	rec, err := rt.svc.reg.UpdateOrchestrator(rt.id, mutate)
	return rt.committed(rec, err)
}

// updateRuns is update over the record and its runs in one transaction
// (UpdateOrchestratorRuns), with the same callback rules.
func (rt *runtime) updateRuns(fn func(*pb.Orchestrator, RunTx) error) (*pb.Orchestrator, error) {
	rt.recMu.Lock()
	defer rt.recMu.Unlock()
	rec, err := rt.svc.reg.UpdateOrchestratorRuns(rt.id, fn)
	return rt.committed(rec, err)
}

// committed stores a committed record into rec and bumps recGen (caller holds
// recMu). errNoChange reads as success with the current record.
func (rt *runtime) committed(rec *pb.Orchestrator, err error) (*pb.Orchestrator, error) {
	if errors.Is(err, errNoChange) {
		return rt.record(), nil
	}
	if err != nil {
		return nil, notFound(err)
	}
	rt.mu.Lock()
	rt.rec = proto.Clone(rec).(*pb.Orchestrator)
	rt.recGen++
	rt.mu.Unlock()
	return rec, nil
}

// cacheLocked stores a sandbox record in sbs when it is newer than the cached
// one (caller holds mu). A revision-0 record (its host predates lanes,
// research R16) always replaces.
func (rt *runtime) cacheLocked(sb *pb.Sandbox) {
	if sb == nil || sb.GetId() == "" {
		return
	}
	if cur, ok := rt.sbs[sb.GetId()]; ok && sb.GetRevision() != 0 && sb.GetRevision() <= cur.GetRevision() {
		return
	}
	rt.sbs[sb.GetId()] = proto.Clone(sb).(*pb.Sandbox)
}

// markDetachingLocked marks a worker as detaching (caller holds mu): refused while a
// run is being delivered to it (research R8/R10).
func (rt *runtime) markDetachingLocked(sandboxID, display string) error {
	if _, ok := rt.delivering[sandboxID]; ok {
		return fmt.Errorf("%w: %s is receiving a run — try again in a moment", ErrPrecondition, display)
	}
	rt.detaching[sandboxID] = struct{}{}
	return nil
}

// clearDetaching drops a worker's detaching mark.
func (rt *runtime) clearDetaching(sandboxID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.detaching, sandboxID)
}

// isDetaching reports whether a worker is detaching.
func (rt *runtime) isDetaching(sandboxID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, ok := rt.detaching[sandboxID]
	return ok
}

// endDelivery drops a worker from the delivering set once deliver returned.
func (rt *runtime) endDelivery(sandboxID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.delivering, sandboxID)
}

// sandboxRecord returns the latest record seen for a sandbox (nil when unknown).
func (rt *runtime) sandboxRecord(id string) *pb.Sandbox {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if sb, ok := rt.sbs[id]; ok {
		return proto.Clone(sb).(*pb.Sandbox)
	}
	return nil
}
