package ui

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
)

// launchHostServer is a minimal remote daemon exposing its host id, workspace
// root, and a per-host source candidate, so the launch host-picker + remote
// source-browse path can be exercised over a real gRPC connection.
type launchHostServer struct {
	pb.UnimplementedSwitchboardServer
	id        string
	workspace string
}

func (s *launchHostServer) GetDaemonInfo(context.Context, *pb.GetDaemonInfoRequest) (*pb.DaemonInfo, error) {
	return &pb.DaemonInfo{HostId: s.id, WorkspaceRoot: s.workspace}, nil
}
func (s *launchHostServer) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	return &pb.ListSandboxesResponse{}, nil
}

// ListSourceCandidates returns a single repo directly under the requested root,
// named after the host so a test can tell whose filesystem it is browsing.
func (s *launchHostServer) ListSourceCandidates(_ context.Context, req *pb.ListSourceCandidatesRequest) (*pb.ListSourceCandidatesResponse, error) {
	return &pb.ListSourceCandidatesResponse{Candidates: []*pb.SourceRef{
		{Path: filepath.Join(req.GetRoot(), "repo-"+s.id), IsRepo: true},
	}}, nil
}

func startLaunchHost(t *testing.T, id, workspace string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), id+".sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, &launchHostServer{id: id, workspace: workspace})
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

func hasEntry(entries []fsEntry, name string) bool {
	for _, e := range entries {
		if e.name == name {
			return true
		}
	}
	return false
}

func indexOfHost(choices []hostChoice, id string) int {
	for i, c := range choices {
		if c.id == id {
			return i
		}
	}
	return -1
}

// TestLaunchHostPickerSwitchesTargetAndBrowse verifies that, with two connected
// remote hosts, the launch wizard (a) browses the active host's filesystem over
// gRPC, (b) opens a host picker on H, and (c) switching hosts re-targets the
// launch daemon, re-roots the browser on the new host, and clears selections.
func TestLaunchHostPickerSwitchesTargetAndBrowse(t *testing.T) {
	sockA := startLaunchHost(t, "hosta", "/srv/a")
	sockB := startLaunchHost(t, "hostb", "/srv/b")

	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	socks := map[string]string{"hosta": sockA, "hostb": sockB}
	mgr := client.NewManager()
	mgr.SetDialFunc(func(ctx context.Context, e client.HostEntry) (*client.Conn, error) {
		return client.DialLocal(ctx, socks[e.ID])
	})
	for _, id := range []string{"hosta", "hostb"} {
		mgr.Upsert(client.HostEntry{ID: id, DisplayName: id, Kind: "ssh"})
		if err := mgr.Connect(context.Background(), id); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}

	m := sized(New(&fakeDaemon{}, "/work").WithHosts(mgr, hs, "hosta"))

	// Open launch: the active host (hosta) is an ssh host, so browsing is remote
	// (asynchronous) and rooted at the daemon's advertised workspace root.
	m, cmd := update(m, press("n"))
	if m.screen != screenLaunch {
		t.Fatal("n should open the launch wizard")
	}
	if !m.launch.loading {
		t.Fatal("a remote target host should browse asynchronously (loading state)")
	}
	if m.launch.dir != "/srv/a" {
		t.Fatalf("hosta browse dir = %q, want /srv/a", m.launch.dir)
	}
	m, _ = update(m, runCmd(cmd)) // browseMsg for hosta
	if m.launch.loading || !hasEntry(m.launch.entries, "repo-hosta") {
		t.Fatalf("hosta browse should list its candidate: loading=%v entries=%+v", m.launch.loading, m.launch.entries)
	}

	// Select the candidate so we can prove the switch clears it.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if len(m.launch.order) != 1 {
		t.Fatalf("expected 1 selected source, got %v", m.launch.order)
	}

	// Open the host picker: both connected hosts appear.
	m, _ = update(m, press("H"))
	if !m.launch.hostPick || len(m.launch.hostChoices) != 2 {
		t.Fatalf("H should open a 2-host picker (pick=%v n=%d)", m.launch.hostPick, len(m.launch.hostChoices))
	}

	// Move the cursor to hostb and confirm.
	idx := indexOfHost(m.launch.hostChoices, "hostb")
	if idx < 0 {
		t.Fatal("hostb missing from picker")
	}
	for m.launch.hostCursor < idx {
		m, _ = update(m, press("j"))
	}
	for m.launch.hostCursor > idx {
		m, _ = update(m, press("k"))
	}
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.launch.hostPick {
		t.Error("enter should close the host picker")
	}
	if m.launch.targetHost != "hostb" {
		t.Fatalf("target host = %q, want hostb", m.launch.targetHost)
	}
	// A launch would now target hostb's daemon, not the active one.
	if got := m.daemonForHost(m.launch.targetHost).HostID(); got != "hostb" {
		t.Fatalf("launch daemon host = %q, want hostb", got)
	}
	// Switching clears the previous host's selections (its paths are meaningless
	// on the new host).
	if len(m.launch.order) != 0 {
		t.Errorf("switching hosts should clear selections, got %v", m.launch.order)
	}
	// The browser re-roots on hostb's workspace root and lists hostb's candidate.
	if m.launch.dir != "/srv/b" {
		t.Fatalf("hostb browse dir = %q, want /srv/b", m.launch.dir)
	}
	m, _ = update(m, runCmd(cmd)) // browseMsg for hostb
	if !hasEntry(m.launch.entries, "repo-hostb") {
		t.Errorf("hostb browse should list its candidate, got %+v", m.launch.entries)
	}
	if m.launchTargetLabel() != "hostb" {
		t.Errorf("target label = %q, want hostb", m.launchTargetLabel())
	}
}

// TestLaunchHostPickerNoopSingleHost verifies H is a no-op (with a hint) when
// there is no other connected host to switch to.
func TestLaunchHostPickerNoopSingleHost(t *testing.T) {
	m := sized(New(&fakeDaemon{}, t.TempDir())) // no manager => single (local) host
	m, _ = update(m, press("n"))
	m, _ = update(m, press("H"))
	if m.launch.hostPick {
		t.Error("H should not open a picker when there is only one host")
	}
	if m.launch.progress == "" {
		t.Error("H with a single host should surface a hint")
	}
}

// TestRemoteLaunchRowAttribution verifies a launch targeting a non-active host
// attributes its optimistic "creating" row and its resulting sandbox to that
// host, not the active one.
func TestRemoteLaunchRowAttribution(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.activeHost = "local"
	m.hostAgg = []client.HostSandboxes{
		{Host: client.HostConn{Entry: client.HostEntry{ID: "local", DisplayName: "localhost"}, State: client.HostConnected}},
		{Host: client.HostConn{Entry: client.HostEntry{ID: "hostb", DisplayName: "beta"}, State: client.HostConnected}},
	}
	m.launching = map[string]*launchInFlight{
		"pending-1": {tempID: "pending-1", host: "hostb", seq: 1,
			sb: &pb.Sandbox{Id: "pending-1", State: pb.SandboxState_SANDBOX_STATE_CREATING}},
	}

	// The still-creating row is attributed to the remote target host.
	var creating *sandboxRow
	for i, r := range m.allRows() {
		if r.sb.GetId() == "pending-1" {
			row := m.allRows()[i]
			creating = &row
		}
	}
	if creating == nil || creating.host != "hostb" {
		t.Fatalf("creating row should be attributed to hostb, got %+v", creating)
	}

	// On success the real sandbox lands only under hostb's aggregate.
	m2, _ := update(m, launchResultMsg{id: "pending-1", sb: &pb.Sandbox{Id: "real-1", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})
	var hostbCount, otherCount int
	for _, hs := range m2.hostAgg {
		for _, sb := range hs.Sandboxes {
			if sb.GetId() != "real-1" {
				continue
			}
			if hs.Host.Entry.ID == "hostb" {
				hostbCount++
			} else {
				otherCount++
			}
		}
	}
	if hostbCount != 1 || otherCount != 0 {
		t.Errorf("real-1 should be attributed only to hostb; hostb=%d other=%d", hostbCount, otherCount)
	}
}

// TestApplyBrowseIgnoresStale verifies a browse response for a since-changed
// host/dir is dropped rather than clobbering the current listing.
func TestApplyBrowseIgnoresStale(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m.screen = screenLaunch
	m.launch = launchState{targetHost: "hostb", dir: "/srv/b", loading: true}

	// A response for a different host is ignored.
	m2, _ := update(m, browseMsg{host: "hosta", dir: "/srv/a", entries: []fsEntry{{name: "x"}}})
	if len(m2.launch.entries) != 0 || !m2.launch.loading {
		t.Error("stale (wrong-host) browse should be ignored")
	}
	// A matching response applies and clears loading.
	m3, _ := update(m, browseMsg{host: "hostb", dir: "/srv/b", entries: []fsEntry{{name: "ok"}}})
	if m3.launch.loading || !hasEntry(m3.launch.entries, "ok") {
		t.Errorf("matching browse should apply: loading=%v entries=%+v", m3.launch.loading, m3.launch.entries)
	}
}
