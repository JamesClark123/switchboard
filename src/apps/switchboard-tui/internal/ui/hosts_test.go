package ui

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// hostsGRPC is a minimal daemon for the hosts-screen tests.
type hostsGRPC struct {
	pb.UnimplementedSwitchboardServer
	id string
}

func (s *hostsGRPC) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: s.id}, nil
}
func (s *hostsGRPC) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	return &pb.ListSandboxesResponse{Sandboxes: []*pb.Sandbox{{Id: s.id + "-sb", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}}, nil
}

func startHostServer(t *testing.T, id string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), id+".sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, &hostsGRPC{id: id})
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

func TestHostsScreenConnectActivate(t *testing.T) {
	sockA := startHostServer(t, "hosta")
	sockB := startHostServer(t, "hostb")

	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	if _, err := hs.Save(store.KnownHost{ID: "hosta", DisplayName: "alpha", Kind: "ssh", SSHTarget: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Save(store.KnownHost{ID: "hostb", DisplayName: "beta", Kind: "ssh", SSHTarget: "b"}); err != nil {
		t.Fatal(err)
	}
	socks := map[string]string{"hosta": sockA, "hostb": sockB}
	mgr := client.NewManager()
	mgr.SetDialFunc(func(ctx context.Context, e client.HostEntry) (*client.Conn, error) {
		return client.DialLocal(ctx, socks[e.ID])
	})
	m := sized(New(&fakeDaemon{}, "/work").WithHosts(mgr, hs, ""))

	// Open hosts screen; both known hosts appear (disconnected).
	m, cmd := update(m, press("h"))
	if m.screen != screenHosts {
		t.Fatal("h should open hosts screen")
	}
	m, _ = update(m, runCmd(cmd)) // hostsMsg
	if len(m.hosts.rows) != 2 {
		t.Fatalf("expected 2 host rows, got %d", len(m.hosts.rows))
	}
	if !strings.Contains(m.viewHosts(), "DISCONNECTED") {
		t.Error("hosts view should show disconnected state")
	}

	// Navigate j then k.
	m, _ = update(m, press("j"))
	if m.hosts.list.Index() != 1 {
		t.Fatalf("j -> index %d", m.hosts.list.Index())
	}
	m, _ = update(m, press("k"))
	if m.hosts.list.Index() != 0 {
		t.Fatalf("k -> index %d", m.hosts.list.Index())
	}

	// Activating before connecting warns.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.hosts.status, "connect the host first") {
		t.Errorf("status = %q", m.hosts.status)
	}

	// Connect the first host.
	m, cmd = update(m, press("c"))
	m, _ = update(m, runCmd(cmd)) // hostsMsg after connect
	row := m.hostsCurrent()
	if row == nil || row.Host.State != client.HostConnected {
		t.Fatalf("host not connected: %+v", row)
	}
	// Host-grouped view attributes the sandbox to the host (SC-006).
	if len(row.Sandboxes) != 1 {
		t.Errorf("expected 1 sandbox for connected host, got %d", len(row.Sandboxes))
	}

	// Activate it -> becomes the active daemon, returns to list.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList || m.activeHost != "hosta" {
		t.Fatalf("activate failed: screen=%v active=%q", m.screen, m.activeHost)
	}
	if m.daemon.HostID() != "hosta" {
		t.Errorf("active daemon host = %q", m.daemon.HostID())
	}
}

func TestHostsAddDisconnectRemove(t *testing.T) {
	sock := startHostServer(t, "newbox")
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	mgr := client.NewManager()
	mgr.SetDialFunc(func(ctx context.Context, e client.HostEntry) (*client.Conn, error) {
		return client.DialLocal(ctx, sock)
	})
	m := sized(New(&fakeDaemon{}, "/work").WithHosts(mgr, hs, ""))

	m, cmd := update(m, press("h"))
	m, _ = update(m, runCmd(cmd))

	// Add an SSH host inline.
	m, _ = update(m, press("a"))
	if !m.hosts.adding {
		t.Fatal("'a' should start add mode")
	}
	m = typeStr(m, "user@newbox")
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, runCmd(cmd)) // hostsMsg after add
	if len(m.hosts.rows) != 1 {
		t.Fatalf("expected 1 host after add, got %d", len(m.hosts.rows))
	}
	saved, _ := hs.List()
	if len(saved) != 1 || saved[0].SSHTarget != "user@newbox" {
		t.Errorf("host not persisted: %+v", saved)
	}

	// Connect then disconnect.
	m, cmd = update(m, press("c"))
	m, _ = update(m, runCmd(cmd))
	m, _ = update(m, press("x"))
	row := m.hostsCurrent()
	if row != nil {
		if hc, _ := mgr.Get(row.Host.Entry.ID); hc.State != client.HostDisconnected {
			t.Error("x should disconnect the host")
		}
	}

	// Remove it.
	m, cmd = update(m, press("d"))
	m, _ = update(m, runCmd(cmd))
	if len(m.hosts.rows) != 0 {
		t.Errorf("expected 0 hosts after remove, got %d", len(m.hosts.rows))
	}
	if remaining, _ := hs.List(); len(remaining) != 0 {
		t.Errorf("host not deleted from store: %d", len(remaining))
	}
}

func TestHostsAddCancelAndEmptyAndNav(t *testing.T) {
	s, _ := store.New(t.TempDir())
	mgr := client.NewManager()
	m := sized(New(&fakeDaemon{}, "/work").WithHosts(mgr, s.Hosts(), ""))
	m, cmd := update(m, press("h"))
	m, _ = update(m, runCmd(cmd))
	if len(m.hosts.list.Items()) != 0 {
		t.Error("expected an empty hosts list")
	}
	// Add mode: type, backspace, space, then esc-cancel.
	m, _ = update(m, press("a"))
	m = typeStr(m, "ab")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m, _ = update(m, press(" "))
	if m.hosts.input.Value() != "a " {
		t.Errorf("input value = %q", m.hosts.input.Value())
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.hosts.adding {
		t.Error("esc should cancel add mode")
	}
	// esc leaves the screen.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should return to list")
	}
}

func TestHostsViewLocalAndActiveMarker(t *testing.T) {
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	if _, err := hs.Save(store.KnownHost{ID: "local", DisplayName: "localhost", Kind: "local", SocketPath: "/run/s.sock"}); err != nil {
		t.Fatal(err)
	}
	m := sized(New(&fakeDaemon{}, "/work").WithHosts(client.NewManager(), hs, "local"))
	m, cmd := update(m, press("h"))
	m, _ = update(m, runCmd(cmd))
	v := m.viewHosts()
	if !strings.Contains(v, "local /run/s.sock") {
		t.Errorf("local host summary missing: %q", v)
	}
	if !strings.Contains(v, "★") {
		t.Error("active host marker missing")
	}
}

func TestHostsNoOpWithoutManager(t *testing.T) {
	m := New(&fakeDaemon{}, "/work") // no WithHosts
	m, _ = update(m, press("h"))
	if m.screen != screenList || !strings.Contains(m.status, "multi-host") {
		t.Errorf("hosts without manager should warn; status=%q", m.status)
	}
}
