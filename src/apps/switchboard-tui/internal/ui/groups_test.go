package ui

import (
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/vscode"
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

func recordingOpener() (*vscode.Opener, *bool) {
	opened := new(bool)
	o := &vscode.Opener{CodeBin: "code", Run: func(*exec.Cmd) error { *opened = true; return nil }}
	return o, opened
}

func TestGroupsCreateAssignNavigate(t *testing.T) {
	gs := newGroupStore(t)
	opener, _ := recordingOpener()
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithGroups(gs, opener), []*pb.Sandbox{{Id: "sb1"}, {Id: "sb2"}})
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
	m := sized(New(&fakeDaemon{}, "/work").WithGroups(gs, nil))
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
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.groups.adding {
		t.Error("esc should cancel add")
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

func TestOpenVSCode(t *testing.T) {
	opener, opened := recordingOpener()
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithGroups(newGroupStore(t), opener), []*pb.Sandbox{{Id: "sb1"}})

	_, cmd := update(m, press("v"))
	msg := runCmd(cmd)
	if s, ok := msg.(statusMsg); !ok || !strings.Contains(string(s), "opened") {
		t.Fatalf("vscode open status = %v", msg)
	}
	if !*opened {
		t.Error("opener.Open was not called")
	}

	// VSCodeTarget error surfaces.
	d.vscodeErr = exec.ErrNotFound
	_, cmd = update(m, press("v"))
	if _, ok := runCmd(cmd).(errMsg); !ok {
		t.Error("expected errMsg when VSCodeTarget fails")
	}
}

func TestOpenVSCodeRemoteUsesRemoteSSH(t *testing.T) {
	s, _ := store.New(t.TempDir())
	hs := s.Hosts()
	if _, err := hs.Save(store.KnownHost{ID: "remote", DisplayName: "box", Kind: "ssh", SSHTarget: "user@box"}); err != nil {
		t.Fatal(err)
	}
	var args []string
	opener := &vscode.Opener{CodeBin: "code", Run: func(c *exec.Cmd) error { args = c.Args; return nil }}
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work").WithHosts(nil, hs, "remote").WithGroups(s.Groups(), opener), []*pb.Sandbox{{Id: "sb1"}})

	_, cmd := update(m, press("v"))
	_ = runCmd(cmd)
	// A remote sandbox opens its controlled folder over Remote-SSH to that host.
	found := false
	for _, a := range args {
		if a == "ssh-remote+user@box" {
			found = true
		}
	}
	if !found {
		t.Errorf("remote open should target ssh-remote+user@box, args=%v", args)
	}
}

func TestOpenVSCodeNoOpener(t *testing.T) {
	m := withSandboxes(New(&fakeDaemon{}, "/work"), []*pb.Sandbox{{Id: "sb1"}}) // no opener
	_, cmd := update(m, press("v"))
	if s, ok := runCmd(cmd).(statusMsg); !ok || !strings.Contains(string(s), "not configured") {
		t.Errorf("expected not-configured status, got %v", runCmd(cmd))
	}
}
