package orchestrator

import (
	"context"
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

	mu    sync.Mutex
	rec   *pb.Orchestrator       // latest persisted record (never handed out without cloning)
	links map[string]*hostLink   // host id -> link (the own host included)
	sbs   map[string]*pb.Sandbox // sandbox id -> latest record seen (workers + reasoning)

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
	// reconcileMu serializes a host reconcile (list + apply) with the roster
	// commit of an attach, a detach and a freshly launched reasoning sandbox,
	// so a listing fetched before one of them can never undo it (drop the new
	// worker, or re-adopt the detached one).
	reconcileMu   sync.Mutex
	inputNotified map[string]bool // run id -> RUN_NEEDS_INPUT raised for the current input wait
	sched         *scheduler
	reasoning     reasoningState
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

// update applies mutate to the persisted record and the cached copy.
func (rt *runtime) update(mutate func(*pb.Orchestrator) error) (*pb.Orchestrator, error) {
	rec, err := rt.svc.reg.UpdateOrchestrator(rt.id, mutate)
	if err != nil {
		return nil, notFound(err)
	}
	rt.mu.Lock()
	rt.rec = proto.Clone(rec).(*pb.Orchestrator)
	rt.mu.Unlock()
	return rec, nil
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
