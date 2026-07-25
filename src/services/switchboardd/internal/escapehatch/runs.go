package escapehatch

import (
	"fmt"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// maxRuns bounds the retained run history per daemon session, mirroring
// agent.Hub's notification buffer. Runs are in-memory only ("session" = daemon
// uptime, clarification Q2); a daemon restart clears them.
const maxRuns = 512

// RunStore holds escape-hatch runs for the daemon's lifetime. It is safe for
// concurrent use. Stored values are cloned on the way in and out so a caller can
// never mutate a live record out from under a concurrent reader.
type RunStore struct {
	mu    sync.Mutex
	seq   int
	order []string // run ids oldest-first, for ring trimming
	runs  map[string]*pb.EscapeHatchRun
}

// NewRunStore constructs an empty RunStore.
func NewRunStore() *RunStore {
	return &RunStore{runs: map[string]*pb.EscapeHatchRun{}}
}

// Create records a new run, assigning it an `ehr-<seq>` id, and returns a clone.
// The oldest run is evicted once the store exceeds maxRuns.
func (s *RunStore) Create(run *pb.EscapeHatchRun) *pb.EscapeHatchRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	stored := proto.Clone(run).(*pb.EscapeHatchRun)
	stored.Id = fmt.Sprintf("ehr-%d", s.seq)
	s.runs[stored.Id] = stored
	s.order = append(s.order, stored.Id)
	if len(s.order) > maxRuns {
		evict := s.order[0]
		s.order = s.order[1:]
		delete(s.runs, evict)
	}
	return proto.Clone(stored).(*pb.EscapeHatchRun)
}

// Get returns a clone of the run with the given id, or (nil, false).
func (s *RunStore) Get(id string) (*pb.EscapeHatchRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(run).(*pb.EscapeHatchRun), true
}

// Update applies mutate to the stored run (if present) under the store lock and
// returns a clone of the result. It returns (nil, false) for an unknown id.
func (s *RunStore) Update(id string, mutate func(*pb.EscapeHatchRun)) (*pb.EscapeHatchRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return nil, false
	}
	mutate(run)
	return proto.Clone(run).(*pb.EscapeHatchRun), true
}

// ListBySandbox returns clones of the runs for a sandbox (oldest-first). An empty
// sandboxID returns every run on this host.
func (s *RunStore) ListBySandbox(sandboxID string) []*pb.EscapeHatchRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pb.EscapeHatchRun, 0, len(s.order))
	for _, id := range s.order {
		run := s.runs[id]
		if sandboxID != "" && run.GetSandboxId() != sandboxID {
			continue
		}
		out = append(out, proto.Clone(run).(*pb.EscapeHatchRun))
	}
	return out
}
