package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/editor"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func newGroupStore(t *testing.T) *store.GroupStore {
	t.Helper()
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s.Groups()
}

func TestGroupsCreateAssignNavigate(t *testing.T) {
	gs := newGroupStore(t)
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithGroups(gs), []*pb.Sandbox{{Id: "sb1"}, {Id: "sb2"}})
	m.list.Select(1) // sb2 is the membership target

	// Open groups; none yet.
	m, cmd := update(m, press("g"))
	if m.screen != screenGroups {
		t.Fatal("g should open groups")
	}
	m, _ = update(m, runCmd(cmd))
	if m.groups.targetSbx != "sb2" {
		t.Errorf("target sandbox = %q, want sb2", m.groups.targetSbx)
	}

	// Add a group.
	m, _ = update(m, press("a"))
	m = typeStr(m, "backend")
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = update(m, runCmd(cmd))
	if len(m.groups.rows) != 1 {
		t.Fatalf("expected 1 group, got %d", len(m.groups.rows))
	}

	// Toggle membership of sb2 into the group.
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, runCmd(cmd))
	g, _ := gs.Get("backend")
	if len(g.Members) != 1 || g.Members[0].SandboxID != "sb2" {
		t.Fatalf("membership not added: %+v", g.Members)
	}
	if !strings.Contains(m.viewGroups(), "✓") {
		t.Error("membership checkmark should render")
	}

	// Toggle again removes it.
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, runCmd(cmd))
	g, _ = gs.Get("backend")
	if len(g.Members) != 0 {
		t.Errorf("membership not removed: %+v", g.Members)
	}

	// Re-add then navigate to the member sandbox.
	_ = gs.AddMember("backend", store.GroupMember{SandboxID: "sb1"})
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc}) // back to list
	m, cmd = update(m, press("g"))                 // reopen -> reload groups
	m, _ = update(m, runCmd(cmd))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList || m.list.Index() != 0 {
		t.Errorf("navigate to member failed: screen=%v index=%d", m.screen, m.list.Index())
	}
}

func TestGroupsDeleteAddCancelAndNav(t *testing.T) {
	gs := newGroupStore(t)
	if _, err := gs.Save(store.Group{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.Save(store.Group{Name: "beta"}); err != nil {
		t.Fatal(err)
	}
	m := sized(New(&fakeDaemon{}, "/work").WithGroups(gs))
	m, cmd := update(m, press("g"))
	m, _ = update(m, runCmd(cmd))
	if len(m.groups.rows) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(m.groups.rows))
	}
	// j/k nav.
	m, _ = update(m, press("j"))
	if m.groups.list.Index() != 1 {
		t.Errorf("index = %d", m.groups.list.Index())
	}
	m, _ = update(m, press("k"))

	// Add-cancel via esc, with backspace/space editing.
	m, _ = update(m, press("a"))
	m = typeStr(m, "xy")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	m, _ = update(m, press(" "))
	if m.groups.input.Value() != "x " {
		t.Errorf("input value = %q", m.groups.input.Value())
	}
	m = escDiscard(t, m)
	if m.groups.adding {
		t.Error("esc, discard should cancel add")
	}

	// Delete the cursored group.
	m, cmd = update(m, press("d"))
	m, _ = update(m, runCmd(cmd))
	if len(m.groups.rows) != 1 {
		t.Errorf("expected 1 group after delete, got %d", len(m.groups.rows))
	}

	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should return to list")
	}
}

func TestGroupsNoStore(t *testing.T) {
	m := New(&fakeDaemon{}, "/work")
	m, _ = update(m, press("g"))
	if m.screen != screenList || !strings.Contains(m.status, "groups not available") {
		t.Errorf("groups without store should warn; status=%q", m.status)
	}
}

// TestOpenEditorFromGroupTab (SWI-24 T033): v from a group tab targets the
// sandbox's own host's SSH target, not the active host's.
func TestOpenEditorFromGroupTab(t *testing.T) {
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	for _, h := range []store.KnownHost{
		{ID: "local", DisplayName: "laptop", Kind: "local", SocketPath: "/tmp/s.sock"},
		{ID: "box", DisplayName: "build-box", Kind: "ssh", SSHTarget: "me@box"},
	} {
		if _, err := hs.Save(h); err != nil {
			t.Fatal(err)
		}
	}
	gs := s.Groups()
	g, err := gs.Save(store.Group{Name: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gs.AddMember(g.ID, store.GroupMember{HostID: "box", SandboxID: "remote1"}); err != nil {
		t.Fatal(err)
	}
	local := &fakeDaemon{hostID: "local"}
	mgr := client.NewManager()
	mgr.Upsert(client.HostEntry{ID: "local", DisplayName: "laptop", Kind: "local", SocketPath: "/tmp/s.sock"})
	mgr.Adopt("local", nil)
	mgr.Upsert(client.HostEntry{ID: "box", DisplayName: "build-box", Kind: "ssh", SSHTarget: "me@box"})
	mgr.Adopt("box", nil) // connected; the fake stands in for its connection
	stubLookPath(t)
	l := &fakeLauncher{report: make(chan editor.ExitReport)}
	close(l.report)
	m := sized(New(local, "/work").WithHosts(mgr, hs, "local").WithGroups(gs).WithEditor(l, nil))
	m, _ = update(m, listDataMsg{
		hosts: []client.HostSandboxes{
			{Host: client.HostConn{Entry: client.HostEntry{ID: "local", DisplayName: "laptop"}, State: client.HostConnected}, Sandboxes: []*pb.Sandbox{{Id: "local1", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}},
			{Host: client.HostConn{Entry: client.HostEntry{ID: "box", DisplayName: "build-box"}, State: client.HostConnected}, Sandboxes: []*pb.Sandbox{{Id: "remote1", State: pb.SandboxState_SANDBOX_STATE_RUNNING}}},
		},
		groups: mustGroups(t, gs),
	})
	// Move to the group tab, where remote1 is the only row.
	for i := 0; i < len(m.tabs); i++ {
		if m.tabs[m.tabIndex].kind == tabGroup {
			break
		}
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	}
	if m.tabs[m.tabIndex].kind != tabGroup || m.actionable().GetId() != "remote1" {
		t.Fatalf("group tab: kind %v row %q", m.tabs[m.tabIndex].kind, m.actionable().GetId())
	}
	_, msg, _ := pressV(t, m)
	if msg.failed || strings.Join(l.last(), " ") != "code --remote ssh-remote+me@box /workspace" || local.vscodeCalls != 1 {
		t.Errorf("group-tab v: %+v argv %q calls %d", msg, l.last(), local.vscodeCalls)
	}
}
