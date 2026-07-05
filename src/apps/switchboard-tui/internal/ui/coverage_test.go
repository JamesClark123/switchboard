package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func TestErrMsgError(t *testing.T) {
	e := errMsg{err: context.Canceled}
	if e.Error() != context.Canceled.Error() {
		t.Errorf("errMsg.Error() = %q", e.Error())
	}
}

func TestListKeysNoOpOnEmpty(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work"))
	m, _ = update(m, sandboxesMsg(nil))
	// current() is nil; per-sandbox keys must be safe no-ops.
	for _, k := range []string{"s", "d", "R", "t", "T", "v"} {
		mm, cmd := update(m, press(k))
		if cmd != nil {
			t.Errorf("key %q on empty list should be a no-op", k)
		}
		m = mm
	}
	if m.current() != nil {
		t.Error("current() should be nil on empty list")
	}
}

func TestLaunchFromConfigPresetsClone(t *testing.T) {
	cfg := &store.Configuration{Name: "c1", SeedingMode: "clone", KitOptions: map[string]string{"network": `"host"`}}
	m := sized(New(&fakeDaemon{}, t.TempDir()).WithConfigs(newConfigStore(t)))
	mm, _ := m.enterLaunchWithConfig(cfg)
	m = mm.(Model)
	if m.launch.config == nil || !m.launch.cloneMode {
		t.Fatal("launch from a clone config should bind the config and preset clone mode")
	}
	// The clone config surfaces "clone" as the seeding mode in the overlay.
	if !strings.Contains(m.View(), "clone") {
		t.Error("clone config should surface the clone seeding mode")
	}
}

func TestPickerEnterOnEmptyIsNoOp(t *testing.T) {
	m := sized(New(&fakeDaemon{}, "/work").WithConfigs(newConfigStore(t)))
	m, cmd := update(m, press("C"))
	m, _ = update(m, runCmd(cmd)) // empty configsMsg
	if m.pickerCurrent() != nil {
		t.Error("pickerCurrent should be nil when empty")
	}
	mm, c := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if c != nil || mm.screen != screenConfigPicker {
		t.Error("enter on empty picker should be a no-op")
	}
}

func TestToggleMembershipNoTarget(t *testing.T) {
	gs := newGroupStore(t)
	if _, err := gs.Save(store.Group{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	// No sandbox selected (empty list) -> targetSbx stays empty.
	m := sized(New(&fakeDaemon{}, "/work").WithGroups(gs, nil))
	m, cmd := update(m, press("g"))
	m, _ = update(m, runCmd(cmd))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if m.groups.status != "no sandbox selected to assign" {
		t.Errorf("status = %q", m.groups.status)
	}
}

func TestSelectByIDMissAndHeaderNoUnread(t *testing.T) {
	m := withSandboxes(New(&fakeDaemon{}, "/work"), []*pb.Sandbox{{Id: "sb1"}})
	// Missing id leaves the selection unchanged.
	before := m.list.Index()
	selectByID(&m.list, "nope")
	if m.list.Index() != before {
		t.Error("selectByID with a missing id should not move the cursor")
	}
	// Header renders without an unread badge.
	if m.header() == "" {
		t.Error("header should render")
	}
}

func TestHelpFooterWrapsInsteadOfTruncating(t *testing.T) {
	m := withSandboxes(New(&fakeDaemon{}, "/work"), []*pb.Sandbox{
		{Id: "a", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
	})
	lines := func(width int) int {
		mm, _ := update(m, tea.WindowSizeMsg{Width: width, Height: 30})
		return strings.Count(mm.renderHelp(mm.listHelp()), "\n") + 1
	}
	wide := lines(200)  // everything on one line
	narrow := lines(40) // must wrap onto more lines
	if wide != 1 {
		t.Errorf("wide help should be one line, got %d", wide)
	}
	if narrow <= wide {
		t.Errorf("narrow help should wrap to more lines than wide (%d), got %d", wide, narrow)
	}
	// Nothing is dropped: the last binding is still present at a narrow width.
	mm, _ := update(m, tea.WindowSizeMsg{Width: 40, Height: 30})
	if !strings.Contains(mm.renderHelp(mm.listHelp()), "quit") {
		t.Error("wrapping must not drop bindings (expected 'quit' to remain)")
	}
}

func TestListViewRenders(t *testing.T) {
	m := withSandboxes(New(&fakeDaemon{}, "/work"), []*pb.Sandbox{
		{Id: "abc12345", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING},
	})
	if v := m.View(); v == "" {
		t.Error("list view should render content")
	}
	// Small terminal exercises the minimum-width clamp.
	m2, _ := update(m, tea.WindowSizeMsg{Width: 5, Height: 4})
	if m2.bodyWidth() != 20 {
		t.Errorf("bodyWidth clamp = %d, want 20", m2.bodyWidth())
	}
}

// TestPerScreenViewAndForward renders each screen through the top-level View
// (exercising the help bars + chrome) and forwards a non-key message so each
// screen's component-forwarding branch runs.
func TestPerScreenViewAndForward(t *testing.T) {
	d := &fakeDaemon{manifest: testManifest(), candidates: []*pb.SourceRef{{Path: "/a"}}}
	base := func() Model {
		s, _ := store.New(t.TempDir())
		m := New(d, "/work").
			WithConfigs(s.Configs()).
			WithGroups(s.Groups(), nil).
			WithHosts(client.NewManager(), s.Hosts(), "local")
		return withSandboxes(m, []*pb.Sandbox{{Id: "sb1", DisplayName: "demo", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})
	}
	open := func(m Model, k string) Model {
		m, cmd := update(m, press(k))
		if msg := runCmd(cmd); msg != nil {
			m, _ = update(m, msg)
		}
		return m
	}
	render := func(m Model) {
		if m.View() == "" {
			t.Errorf("empty view on screen %v", m.screen)
		}
		update(m, tea.MouseMsg{}) // exercises forward for this screen
	}

	render(open(base(), "n")) // launch
	render(open(base(), "c")) // config editor
	render(open(base(), "C")) // config picker
	hosts := open(base(), "h")
	render(hosts)
	hosts, _ = update(hosts, press("a")) // hosts add-mode
	render(hosts)
	groups := open(base(), "g")
	render(groups)
	groups, _ = update(groups, press("a")) // groups add-mode
	render(groups)
	nm := base()
	nm.inbox = []*pb.NotificationEvent{{Id: "n1", SandboxId: "sb1", Kind: pb.NotificationKind_NOTIFICATION_KIND_TASK_COMPLETE}}
	render(open(nm, "i"))     // notifications
	render(open(base(), "p")) // prompt
	render(open(base(), "R")) // rename
	render(base())            // list + forward default branch

	// listItem.FilterValue and helpBindings.FullHelp.
	if (listItem{title: "t"}).FilterValue() != "t" {
		t.Error("FilterValue should fall back to title")
	}
	if (listItem{title: "t", filter: "f"}).FilterValue() != "f" {
		t.Error("FilterValue should prefer filter")
	}
	if len(helpBindings{}.FullHelp()) != 1 {
		t.Error("FullHelp should wrap bindings")
	}
}
