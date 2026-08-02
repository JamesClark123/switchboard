package portforward

import (
	"fmt"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/protobuf/proto"
)

// instanceKey identifies the at-most-one instance a (sandbox, service) pair may
// have. Unlike escape-hatch runs, services keep no history: a service has one
// current instance, replaced when it is started again. There is nothing to ring-
// trim, so the store stays bounded by the number of declared services.
type instanceKey struct {
	sandboxID string
	name      string
}

// InstanceStore holds service instances for the daemon's lifetime (research R7).
// It is safe for concurrent use, and clones values on the way in and out so a
// caller can never mutate a live record out from under a concurrent reader.
type InstanceStore struct {
	mu     sync.Mutex
	seq    int
	byID   map[string]*pb.ServiceInstance
	byKey  map[instanceKey]string // (sandbox, name) -> current instance id
	orderK []instanceKey          // stable listing order (first-start order)
}

// NewInstanceStore constructs an empty store.
func NewInstanceStore() *InstanceStore {
	return &InstanceStore{
		byID:  map[string]*pb.ServiceInstance{},
		byKey: map[instanceKey]string{},
	}
}

// Create returns the instance for (sandboxID, name), creating a fresh STARTING one
// when there is none or the previous one has reached a terminal state.
//
// created reports whether a new instance was made. When it is false the caller
// MUST NOT launch anything: an instance is already STARTING or RUNNING, and
// starting an already-running service is idempotent — no second process, same
// local address (FR-048). Doing the check inside the store lock is what makes that
// idempotence race-free under concurrent starts.
func (s *InstanceStore) Create(sandboxID, name string) (inst *pb.ServiceInstance, created bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := instanceKey{sandboxID: sandboxID, name: name}
	if id, ok := s.byKey[key]; ok {
		if existing := s.byID[id]; existing != nil && !IsTerminal(existing.GetState()) {
			return proto.Clone(existing).(*pb.ServiceInstance), false
		}
		delete(s.byID, id) // terminal: replaced by the new attempt
	} else {
		s.orderK = append(s.orderK, key)
	}

	s.seq++
	stored := &pb.ServiceInstance{
		Id:          fmt.Sprintf("svc-%d", s.seq),
		SandboxId:   sandboxID,
		ServiceName: name,
		State:       pb.ServiceState_SERVICE_STATE_STARTING,
	}
	s.byID[stored.Id] = stored
	s.byKey[key] = stored.Id
	return proto.Clone(stored).(*pb.ServiceInstance), true
}

// Get returns a clone of the instance with the given id, or (nil, false).
func (s *InstanceStore) Get(id string) (*pb.ServiceInstance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(inst).(*pb.ServiceInstance), true
}

// GetByService returns a clone of the current instance for (sandboxID, name).
func (s *InstanceStore) GetByService(sandboxID, name string) (*pb.ServiceInstance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byKey[instanceKey{sandboxID: sandboxID, name: name}]
	if !ok {
		return nil, false
	}
	inst, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return proto.Clone(inst).(*pb.ServiceInstance), true
}

// Update applies mutate to the stored instance (if present) under the store lock
// and returns a clone of the result. It returns (nil, false) for an unknown id.
func (s *InstanceStore) Update(id string, mutate func(*pb.ServiceInstance)) (*pb.ServiceInstance, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inst, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	mutate(inst)
	return proto.Clone(inst).(*pb.ServiceInstance), true
}

// ListBySandbox returns clones of the current instances for a sandbox, in
// first-start order. An empty sandboxID returns every instance on this host.
func (s *InstanceStore) ListBySandbox(sandboxID string) []*pb.ServiceInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*pb.ServiceInstance, 0, len(s.orderK))
	for _, key := range s.orderK {
		if sandboxID != "" && key.sandboxID != sandboxID {
			continue
		}
		inst, ok := s.byID[s.byKey[key]]
		if !ok {
			continue
		}
		out = append(out, proto.Clone(inst).(*pb.ServiceInstance))
	}
	return out
}

// ActiveBySandbox returns clones of the non-terminal instances for a sandbox —
// what the teardown cascade must stop, and what the sandbox-list indicator counts.
func (s *InstanceStore) ActiveBySandbox(sandboxID string) []*pb.ServiceInstance {
	out := make([]*pb.ServiceInstance, 0)
	for _, inst := range s.ListBySandbox(sandboxID) {
		if !IsTerminal(inst.GetState()) {
			out = append(out, inst)
		}
	}
	return out
}

// IsTerminal reports whether a state is a resting outcome. Both terminal states
// mean the same thing about resources: the process is gone, the local port is
// released, and nothing is published (data-model.md).
func IsTerminal(st pb.ServiceState) bool {
	switch st {
	case pb.ServiceState_SERVICE_STATE_STOPPED, pb.ServiceState_SERVICE_STATE_FAILED:
		return true
	}
	return false
}
