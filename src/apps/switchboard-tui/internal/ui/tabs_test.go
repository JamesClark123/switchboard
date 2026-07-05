package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
)

// connectManager wires a manager to two live host servers and connects both.
func connectManager(t *testing.T, hs *store.HostStore) *client.Manager {
	t.Helper()
	sockA := startHostServer(t, "hosta")
	sockB := startHostServer(t, "hostb")
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
	for _, id := range []string{"hosta", "hostb"} {
		known, _ := hs.Get(id)
		mgr.Upsert(toEntry(*known))
		if err := mgr.Connect(context.Background(), id); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}
	return mgr
}

func TestGroupTabsMultiHost(t *testing.T) {
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	gs := s.Groups()
	mgr := connectManager(t, hs)

	// A user group whose one member lives on hosta (the id the fake server reports).
	g, err := gs.Save(store.Group{Name: "mygroup"})
	if err != nil {
		t.Fatal(err)
	}
	if err := gs.AddMember(g.ID, store.GroupMember{HostID: "hosta", SandboxID: "hosta-sb"}); err != nil {
		t.Fatal(err)
	}

	m := sized(New(&fakeDaemon{}, "/work").WithHosts(mgr, hs, "hosta").WithGroups(gs, nil))

	// Load the tab-bar data (aggregate + groups).
	msg := runCmd(m.listDataCmd())
	m, _ = update(m, msg)

	// Tabs: All Sandboxes + one per connected host (alpha, beta) + mygroup.
	if len(m.tabs) != 4 {
		t.Fatalf("tabs = %d, want 4 (%v)", len(m.tabs), m.tabs)
	}
	if m.tabs[0].kind != tabAll {
		t.Error("first tab should be All Sandboxes")
	}
	v := m.View()
	for _, want := range []string{"All Sandboxes", "alpha", "beta", "mygroup", "switch group"} {
		if !strings.Contains(v, want) {
			t.Errorf("tab bar missing %q", want)
		}
	}

	// All tab: one sandbox per connected host.
	if got := len(m.currentTabRows()); got != 2 {
		t.Errorf("All tab rows = %d, want 2", got)
	}

	// →: alpha (hosta) tab shows just hosta's sandbox.
	mm, _ := m.switchTab(1)
	m = mm.(Model)
	if m.tabs[m.tabIndex].kind != tabHost || m.tabs[m.tabIndex].hostID != "hosta" {
		t.Fatalf("after → expected hosta tab, got %+v", m.tabs[m.tabIndex])
	}
	rows := m.currentTabRows()
	if len(rows) != 1 || rows[0].host != "hosta" {
		t.Errorf("hosta tab rows = %+v", rows)
	}
	// The selected sandbox routes actions to hosta's daemon.
	if m.currentHostID() != "hosta" {
		t.Errorf("currentHostID = %q, want hosta", m.currentHostID())
	}
	if d := m.daemonForHost("hosta"); d == nil || d.HostID() != "hosta" {
		t.Errorf("daemonForHost(hosta) = %v", d)
	}

	// Jump to the group tab and confirm it resolves the member.
	m.tabIndex = 3
	m.refreshListItems()
	rows = m.currentTabRows()
	if len(rows) != 1 || rows[0].sb.GetId() != "hosta-sb" {
		t.Errorf("group tab rows = %+v", rows)
	}

	// ← wraps back around the tab list.
	mm, _ = m.switchTab(-1)
	m = mm.(Model)
	if m.tabIndex != 2 {
		t.Errorf("← from group -> tabIndex %d, want 2", m.tabIndex)
	}

	// Unknown / empty hosts fall back to the active daemon.
	if m.daemonForHost("nope").HostID() != m.daemon.HostID() {
		t.Error("daemonForHost(unknown) should fall back to active daemon")
	}
	if m.daemonForHost("").HostID() != m.daemon.HostID() {
		t.Error("daemonForHost(empty) should fall back to active daemon")
	}
}

func TestNewGroupAppearsInTabsImmediately(t *testing.T) {
	s, _ := store.New(t.TempDir())
	gs := s.Groups()
	m := sized(New(&fakeDaemon{}, "/work").WithGroups(gs, nil))
	// Initial tab data: just "All Sandboxes".
	m, _ = update(m, runCmd(m.listDataCmd()).(listDataMsg))
	if len(m.tabs) != 1 {
		t.Fatalf("expected only the All tab initially, got %d", len(m.tabs))
	}

	// Create a group through the groups screen.
	m, cmd := update(m, press("g"))
	m, _ = update(m, runCmd(cmd)) // groupsMsg (empty)
	m, _ = update(m, press("a"))
	m = typeStr(m, "newgrp")
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // save -> groupsCmd
	m, _ = update(m, runCmd(cmd))                      // groupsMsg -> syncs tabs

	// Back on the list, the tab bar shows the new group with no restart/reload.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.tabs) != 2 || m.tabs[1].kind != tabGroup || m.tabs[1].label != "newgrp" {
		t.Fatalf("new group not reflected in tabs: %v", m.tabs)
	}
	if !strings.Contains(m.View(), "newgrp") {
		t.Error("tab bar should render the newly created group")
	}
}

func TestSingleHostHasNoHostTabs(t *testing.T) {
	// With no manager but a group store, only All + the group tabs appear.
	s, _ := store.New(t.TempDir())
	gs := s.Groups()
	if _, err := gs.Save(store.Group{Name: "solo"}); err != nil {
		t.Fatal(err)
	}
	m := sized(New(&fakeDaemon{}, "/work").WithGroups(gs, nil))
	m, _ = update(m, runCmd(m.listDataCmd()).(listDataMsg))
	if len(m.tabs) != 2 || m.tabs[1].kind != tabGroup {
		t.Fatalf("tabs = %v, want [All, group]", m.tabs)
	}
	// Left/right on the list screen switches tabs.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.tabIndex != 1 {
		t.Errorf("right -> tabIndex %d, want 1", m.tabIndex)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.tabIndex != 0 {
		t.Errorf("left -> tabIndex %d, want 0", m.tabIndex)
	}
}
