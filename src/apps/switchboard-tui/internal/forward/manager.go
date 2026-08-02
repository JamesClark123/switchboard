package forward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// Opener opens one ForwardPort relay stream to the daemon. It is the only thing
// this package needs from the client connection, which keeps the forward manager
// testable without a live daemon.
type Opener interface {
	ForwardPort(ctx context.Context) (pb.Switchboard_ForwardPortClient, error)
}

// ErrNoLocalPort is returned when no port could be bound on the developer's
// machine. Surfaced as the NO_LOCAL_PORT failure so the developer is told, rather
// than left with a service that is running but has no address (FR-049).
var ErrNoLocalPort = errors.New("no free port available on this machine")

// forward is one live local listener and the connections it has accepted.
type forward struct {
	instanceID string
	sandboxID  string
	localPort  uint32
	listener   net.Listener
	cancel     context.CancelFunc
}

// Manager owns every listener on the developer's machine.
//
// It is the SOLE allocator of those ports (research R1), which is what makes
// FR-049's uniqueness structural: two live listeners can never be handed the same
// port by the OS, whether they belong to different services, different sandboxes,
// or sandboxes on entirely different hosts.
type Manager struct {
	mu       sync.Mutex
	forwards map[string]*forward // instance id -> forward
}

// NewManager constructs an empty Manager.
func NewManager() *Manager {
	return &Manager{forwards: map[string]*forward{}}
}

// Open binds a free port on 127.0.0.1 for a running instance and starts relaying
// accepted connections to the daemon. It returns the allocated local port.
//
// Opening a forward that already exists is idempotent: the existing port comes back
// unchanged, so a duplicate RUNNING event cannot move a service's address out from
// under a browser tab.
//
// Binding loopback rather than 0.0.0.0 is deliberate — this feature exposes a
// service to the developer, not to their network.
func (m *Manager) Open(opener Opener, sandboxID, instanceID string) (uint32, error) {
	m.mu.Lock()
	if existing, ok := m.forwards[instanceID]; ok {
		port := existing.localPort
		m.mu.Unlock()
		return port, nil
	}
	m.mu.Unlock()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNoLocalPort, err)
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return 0, ErrNoLocalPort
	}
	port := uint32(addr.Port)

	ctx, cancel := context.WithCancel(context.Background())
	f := &forward{
		instanceID: instanceID,
		sandboxID:  sandboxID,
		localPort:  port,
		listener:   ln,
		cancel:     cancel,
	}

	m.mu.Lock()
	// Re-check under the lock: two RUNNING events can race here.
	if existing, ok := m.forwards[instanceID]; ok {
		m.mu.Unlock()
		cancel()
		_ = ln.Close()
		return existing.localPort, nil
	}
	m.forwards[instanceID] = f
	m.mu.Unlock()

	go f.accept(ctx, opener)
	return port, nil
}

// Close tears down an instance's forward. Closing the listener also ends every
// connection relayed through it. Closing an unknown instance is a no-op.
func (m *Manager) Close(instanceID string) {
	m.mu.Lock()
	f, ok := m.forwards[instanceID]
	delete(m.forwards, instanceID)
	m.mu.Unlock()
	if !ok {
		return
	}
	f.cancel()
	_ = f.listener.Close()
}

// CloseSandbox tears down every forward belonging to one sandbox.
func (m *Manager) CloseSandbox(sandboxID string) {
	for _, id := range m.instancesFor(sandboxID) {
		m.Close(id)
	}
}

// CloseAll tears down every forward — used when a host disconnects or the client
// exits. The SERVICES are unaffected: they are daemon-owned and keep running, which
// is why a later reconnect re-opens forwards (possibly on different local ports)
// rather than restarting anything.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.forwards))
	for id := range m.forwards {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Close(id)
	}
}

// Port returns the local port bound for an instance, or 0 when it has no forward.
func (m *Manager) Port(instanceID string) uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.forwards[instanceID]; ok {
		return f.localPort
	}
	return 0
}

// Count returns how many forwards are live (test/observability).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.forwards)
}

func (m *Manager) instancesFor(sandboxID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, f := range m.forwards {
		if f.sandboxID == sandboxID {
			out = append(out, id)
		}
	}
	return out
}
