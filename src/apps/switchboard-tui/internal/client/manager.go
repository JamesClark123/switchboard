package client

import (
	"context"
	"errors"
	"sort"
	"sync"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// ErrUnknownHost is returned when an id is not registered with the Manager.
var ErrUnknownHost = errors.New("unknown host")

// HostState is the connection state of a known host (FR-021).
type HostState int

const (
	HostDisconnected HostState = iota
	HostConnecting
	HostConnected
)

func (s HostState) String() string {
	switch s {
	case HostConnecting:
		return "connecting"
	case HostConnected:
		return "connected"
	default:
		return "disconnected"
	}
}

// HostEntry describes how to reach one daemon. The TUI maps store.KnownHost into
// this (keeping the client package independent of the store layer).
type HostEntry struct {
	ID          string
	DisplayName string
	Kind        string // "local" | "ssh"
	SocketPath  string
	SSHTarget   string
	SSHOptions  []string
}

// HostConn is a managed host: its entry, current state, live connection (when
// connected), and last error (when failed).
type HostConn struct {
	Entry HostEntry
	State HostState
	Conn  *Conn
	Err   error
}

// DialFunc dials a host entry. Overridable in tests.
type DialFunc func(ctx context.Context, e HostEntry) (*Conn, error)

// defaultDial dispatches on kind: local => Unix socket, ssh => dial-stdio.
func defaultDial(ctx context.Context, e HostEntry) (*Conn, error) {
	if e.Kind == "ssh" {
		return DialSSH(ctx, e.SSHTarget, e.SSHOptions)
	}
	return DialLocal(ctx, e.SocketPath)
}

// Manager holds N hosts and their connections (FR-005). It is safe for
// concurrent use.
type Manager struct {
	mu    sync.Mutex
	hosts map[string]*HostConn
	order []string // insertion order for stable listing
	dial  DialFunc
}

// NewManager constructs an empty Manager using the default dialer.
func NewManager() *Manager {
	return &Manager{hosts: map[string]*HostConn{}, dial: defaultDial}
}

// SetDialFunc overrides the dialer (tests inject an in-process dialer).
func (m *Manager) SetDialFunc(d DialFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dial = d
}

// Upsert adds or updates a host entry, preserving any existing live connection.
func (m *Manager) Upsert(e HostEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hc, ok := m.hosts[e.ID]; ok {
		hc.Entry = e
		return
	}
	m.hosts[e.ID] = &HostConn{Entry: e, State: HostDisconnected}
	m.order = append(m.order, e.ID)
}

// Adopt registers an already-established connection for a host (e.g. the local
// daemon dialed at startup), marking it connected without re-dialing.
func (m *Manager) Adopt(id string, conn *Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hc, ok := m.hosts[id]
	if !ok {
		hc = &HostConn{Entry: HostEntry{ID: id}}
		m.hosts[id] = hc
		m.order = append(m.order, id)
	}
	hc.Conn = conn
	hc.State = HostConnected
	hc.Err = nil
}

// Connect dials a host and records the resulting state. Reconnecting an
// already-known host simply re-dials (resync happens by re-listing afterwards,
// SC-010). Returns the host's error, if any.
func (m *Manager) Connect(ctx context.Context, id string) error {
	m.mu.Lock()
	hc, ok := m.hosts[id]
	dial := m.dial
	if !ok {
		m.mu.Unlock()
		return ErrUnknownHost
	}
	if hc.Conn != nil {
		_ = hc.Conn.Close()
		hc.Conn = nil
	}
	hc.State = HostConnecting
	hc.Err = nil
	entry := hc.Entry
	m.mu.Unlock()

	conn, err := dial(ctx, entry)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		hc.State = HostDisconnected
		hc.Err = err
		return err
	}
	hc.Conn = conn
	hc.State = HostConnected
	return nil
}

// Disconnect closes a host's connection and marks it disconnected. The remote
// sandbox keeps running on its host (spec edge case); only the client link drops.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hc, ok := m.hosts[id]
	if !ok {
		return
	}
	if hc.Conn != nil {
		_ = hc.Conn.Close()
		hc.Conn = nil
	}
	hc.State = HostDisconnected
}

// Get returns a snapshot of a host's managed connection.
func (m *Manager) Get(id string) (*HostConn, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hc, ok := m.hosts[id]
	if !ok {
		return nil, false
	}
	cp := *hc
	return &cp, true
}

// List returns snapshots of all hosts in insertion order.
func (m *Manager) List() []*HostConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*HostConn, 0, len(m.order))
	for _, id := range m.order {
		cp := *m.hosts[id]
		out = append(out, &cp)
	}
	return out
}

// Remove drops a host entirely, closing any connection.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hc, ok := m.hosts[id]; ok {
		if hc.Conn != nil {
			_ = hc.Conn.Close()
		}
		delete(m.hosts, id)
		for i, oid := range m.order {
			if oid == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
}

// HostSandboxes pairs a host with the sandboxes it currently reports.
type HostSandboxes struct {
	Host      HostConn
	Sandboxes []*pb.Sandbox
	Err       error
}

// AggregateSandboxes lists sandboxes across all connected hosts, attributing each
// group to its host (FR-020, SC-006). Disconnected hosts are returned with an
// empty list so the TUI can still show them as disconnected.
func (m *Manager) AggregateSandboxes(ctx context.Context) []HostSandboxes {
	hosts := m.List()
	out := make([]HostSandboxes, 0, len(hosts))
	for _, hc := range hosts {
		hs := HostSandboxes{Host: *hc}
		if hc.State == HostConnected && hc.Conn != nil {
			sbs, err := hc.Conn.List(ctx)
			hs.Sandboxes, hs.Err = sbs, err
		}
		out = append(out, hs)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Host.Entry.DisplayName < out[j].Host.Entry.DisplayName
	})
	return out
}
