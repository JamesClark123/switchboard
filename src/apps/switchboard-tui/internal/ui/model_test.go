package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

func press(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func update(m Model, msg tea.Msg) (Model, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(Model), cmd
}

// runCmd executes a (synchronous) tea.Cmd and returns its message.
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// sized gives the model a terminal size so bubbles components render.
func sized(m Model) Model {
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

// withSandboxes loads sandboxes into the list (the source of truth for the
// selection) and returns the model with a size applied.
func withSandboxes(m Model, sbs []*pb.Sandbox) Model {
	m = sized(m)
	m, _ = update(m, sandboxesMsg(sbs))
	return m
}

func seeded(d *fakeDaemon, n int) []*pb.Sandbox {
	var out []*pb.Sandbox
	for i := 0; i < n; i++ {
		sb := &pb.Sandbox{Id: string(rune('a'+i)) + "0000000", DisplayName: "sb" + string(rune('a'+i)), State: pb.SandboxState_SANDBOX_STATE_RUNNING}
		d.sandboxes = append(d.sandboxes, sb)
		out = append(out, sb)
	}
	return out
}

func TestListNavigation(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work"), seeded(d, 3))

	m, _ = update(m, press("j"))
	m, _ = update(m, press("j"))
	if m.list.Index() != 2 {
		t.Fatalf("j,j -> index %d, want 2", m.list.Index())
	}
	// Can't move past the end.
	m, _ = update(m, press("j"))
	if m.list.Index() != 2 {
		t.Fatalf("j at end -> index %d, want 2", m.list.Index())
	}
	m, _ = update(m, press("k"))
	m, _ = update(m, press("k"))
	if m.list.Index() != 0 {
		t.Fatalf("k,k -> index %d, want 0", m.list.Index())
	}
	if m.current() == nil {
		t.Fatal("current() should return the selected sandbox")
	}
}

func TestListLifecycleKeys(t *testing.T) {
	d := &fakeDaemon{}
	sbs := seeded(d, 1) // seeded sandboxes start RUNNING
	m := withSandboxes(New(d, "/work"), sbs)

	// 's' on a running sandbox stops it.
	_, cmd := update(m, press("s"))
	if msg := runCmd(cmd); !strings.Contains(string(msg.(statusMsg)), "stopped") {
		t.Errorf("stop status = %v", msg)
	}
	if sbs[0].GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Error("stop did not reach daemon")
	}

	// 's' again — now stopped — starts it (same key toggles by state).
	_, cmd = update(m, press("s"))
	if msg := runCmd(cmd); !strings.Contains(string(msg.(statusMsg)), "restarted") {
		t.Errorf("start status = %v", msg)
	}
	if sbs[0].GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Error("start did not reach daemon")
	}

	// destroy
	_, cmd = update(m, press("d"))
	if msg := runCmd(cmd); !strings.Contains(string(msg.(statusMsg)), "destroyed") {
		t.Errorf("destroy status = %v", msg)
	}
	if list, _ := d.List(context.Background()); len(list) != 0 {
		t.Errorf("destroy did not reach daemon, %d remain", len(list))
	}
}

func TestRefreshKey(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work"), seeded(d, 1))
	if _, cmd := update(m, press("r")); runCmd(cmd) == nil {
		t.Error("r should refresh the list")
	}
}

func TestRenameScreen(t *testing.T) {
	d := &fakeDaemon{}
	sbs := seeded(d, 1)
	sbs[0].State = pb.SandboxState_SANDBOX_STATE_STOPPED // rename requires stopped
	m := withSandboxes(New(d, "/work"), sbs)

	// Enter rename mode (prefilled with current name).
	m, _ = update(m, press("R"))
	if m.screen != screenRename {
		t.Fatal("R should switch to rename screen")
	}
	if !strings.Contains(m.View(), "Rename sandbox") {
		t.Error("rename view not rendered")
	}

	// Clear and type a new name.
	for range m.rename.Value() {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m = typeStr(m, "zed")
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenList {
		t.Error("enter should return to list")
	}
	if msg := runCmd(cmd); !strings.Contains(string(msg.(statusMsg)), "renamed") {
		t.Errorf("rename status = %v", msg)
	}
	if sbs[0].GetDisplayName() != "zed" {
		t.Errorf("rename not applied: %q", sbs[0].GetDisplayName())
	}

	// Esc cancels rename.
	m, _ = update(m, press("R"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should cancel rename")
	}
}

func TestRenameBlockedWhileRunning(t *testing.T) {
	d := &fakeDaemon{}
	m := withSandboxes(New(d, "/work"), seeded(d, 1)) // seeded sandboxes are RUNNING
	m, _ = update(m, press("R"))
	if m.screen == screenRename {
		t.Error("rename should be blocked while the sandbox is running")
	}
	if !strings.Contains(m.status, "stop the sandbox before renaming") {
		t.Errorf("expected a stop-first hint, got status %q", m.status)
	}
}

func TestRenameEmptyIsNoOp(t *testing.T) {
	d := &fakeDaemon{}
	sbs := seeded(d, 1)
	sbs[0].State = pb.SandboxState_SANDBOX_STATE_STOPPED
	m := withSandboxes(New(d, "/work"), sbs)
	m, _ = update(m, press("R"))
	for range m.rename.Value() {
		m, _ = update(m, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	_, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if runCmd(cmd) != nil {
		t.Error("empty rename should issue no command")
	}
}

func TestLaunchOverlayOpensAndCancels(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"proj", "api"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := withSandboxes(New(&fakeDaemon{}, root), []*pb.Sandbox{{Id: "sb000001", DisplayName: "existing", State: pb.SandboxState_SANDBOX_STATE_RUNNING}})

	m, _ = update(m, press("n"))
	if m.screen != screenLaunch || len(m.launch.entries) == 0 {
		t.Fatal("n should open the launch overlay browsing the source root")
	}
	v := m.View()
	if !strings.Contains(v, "Launch sandbox") || !strings.Contains(v, "proj") || !strings.Contains(v, "api") {
		t.Error("overlay should show the modal title and the directory entries")
	}
	// It is a layer over the list: the list page (its title and existing
	// sandbox) remains visible around the modal.
	if !strings.Contains(v, "Switchboard") || !strings.Contains(v, "existing") {
		t.Error("the list page should remain visible behind the launch overlay")
	}

	// Esc closes the overlay back to the list.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should close the overlay")
	}
}

func TestLaunchSelectsMultipleDirectories(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"api", "web", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDaemon{}
	m := sized(New(d, root))
	m, _ = update(m, press("n"))

	// Entries are sorted (api, docs, web). Select "api" and "web".
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // api
	m, _ = update(m, press("j"))                     // -> docs
	m, _ = update(m, press("j"))                     // -> web
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // web
	if len(m.launch.order) != 2 {
		t.Fatalf("expected 2 selected directories, got %v", m.launch.order)
	}

	// Launch fans out to both selected sources.
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	_ = runCmd(cmd) // drain the launch goroutine's first message
	// The fake records the launched sandbox with both sources.
	list, _ := d.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("expected 1 launched sandbox, got %d", len(list))
	}
	bases := map[string]bool{}
	for _, s := range list[0].GetSources() {
		bases[filepath.Base(s.GetPath())] = true
	}
	if !bases["api"] || !bases["web"] {
		t.Errorf("launched sandbox should seed api and web, got sources %+v", list[0].GetSources())
	}
	if bases["docs"] {
		t.Error("docs was not selected and should not be seeded")
	}
}

func TestLaunchBrowserNavigateToggleScroll(t *testing.T) {
	root := t.TempDir()
	// A nested dir to descend into, plus enough siblings to force scrolling.
	if err := os.MkdirAll(filepath.Join(root, "proj", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 15; i++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := sized(New(&fakeDaemon{}, root))
	m, _ = update(m, press("n"))

	// Descend into "proj" (→), confirm the current dir changed, then go back up.
	for {
		if e, ok := m.launch.current(); ok && e.name == "proj" {
			break
		}
		m, _ = update(m, press("j"))
	}
	m, _ = update(m, press("l")) // right/descend
	if filepath.Base(m.launch.dir) != "proj" {
		t.Fatalf("→ should descend into proj, dir=%q", m.launch.dir)
	}
	m, _ = update(m, press("h")) // left/up
	if m.launch.dir != root {
		t.Fatalf("← should return to root, dir=%q", m.launch.dir)
	}

	// Toggle a directory on then off — selection ends empty.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if len(m.launch.order) != 1 {
		t.Fatalf("space should select, got %v", m.launch.order)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if len(m.launch.order) != 0 {
		t.Fatalf("space again should deselect, got %v", m.launch.order)
	}

	// Scrolling: paging down past the visible window advances the offset.
	for i := 0; i < 14; i++ {
		m, _ = update(m, press("j"))
	}
	if m.launch.offset == 0 {
		t.Error("scrolling down should advance the offset")
	}

	// Seeding mode toggle.
	m, _ = update(m, press("m"))
	if !m.launch.cloneMode || !strings.Contains(m.launchModal(), "clone") {
		t.Error("m should toggle clone mode and the modal should reflect it")
	}
}

func TestLaunchWithCustomName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{}
	m := sized(New(d, root))
	m, _ = update(m, press("n"))

	// N enters name-editing; keys type into the field, enter returns to browser.
	m, _ = update(m, press("N"))
	if !m.launch.naming {
		t.Fatal("N should start editing the name")
	}
	m = typeStr(m, "mybox")
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.launch.naming || m.launch.name.Value() != "mybox" {
		t.Fatalf("name editing: naming=%v value=%q", m.launch.naming, m.launch.name.Value())
	}
	if !strings.Contains(m.launchModal(), "mybox") {
		t.Error("the launch modal should show the chosen name")
	}

	// Select the source and launch; the request carries the custom name.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // cursor starts on "proj"
	_, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	_ = runCmd(cmd) // drain the launch goroutine's first message
	d.mu.Lock()
	got := d.lastDisplay
	d.mu.Unlock()
	if got != "mybox" {
		t.Errorf("launch display name = %q, want mybox", got)
	}
}

func TestLaunchRootFallback(t *testing.T) {
	if got := New(&fakeDaemon{}, "/work").launchRoot(); got != "/work" {
		t.Errorf("launchRoot = %q, want /work", got)
	}
	if got := New(&fakeDaemon{}, "").launchRoot(); got == "" {
		t.Error("launchRoot should fall back to the working directory when srcRoot is empty")
	}
}

func TestLaunchIgnoresInputWhileStreaming(t *testing.T) {
	m := sized(New(&fakeDaemon{}, t.TempDir()))
	m, _ = update(m, press("n"))
	m.launch.inProgress = true
	if _, c := update(m, press("x")); c != nil {
		t.Error("keys should be ignored while a launch streams")
	}
}

func TestHandleLaunchProgressAndResults(t *testing.T) {
	d := &fakeDaemon{}
	m := sized(New(d, "/work"))
	m.screen = screenLaunch
	m.launch.bar = newBar()

	// Copy progress.
	m, _ = update(m, launchProgressMsg{Copy: &pb.LaunchProgress_CopyProgress{BytesCopied: 1, BytesTotal: 2, CurrentPath: "p"}})
	if !strings.Contains(m.launch.progress, "copying") {
		t.Errorf("copy progress = %q", m.launch.progress)
	}
	// Log line.
	m, _ = update(m, launchProgressMsg{LogLine: "hello"})
	if !strings.Contains(m.launch.progress, "hello") {
		t.Errorf("log progress = %q", m.launch.progress)
	}
	// Blocked result.
	m, _ = update(m, launchResultMsg{blocked: &pb.ResourceReport{Warnings: []string{"low disk"}}})
	if !strings.Contains(m.launch.progress, "low disk") {
		t.Errorf("blocked progress = %q", m.launch.progress)
	}
	// Error result.
	m, _ = update(m, launchResultMsg{err: context.DeadlineExceeded})
	if !strings.Contains(m.launch.progress, "failed") {
		t.Errorf("error progress = %q", m.launch.progress)
	}
	// Success result returns to list.
	m, cmd := update(m, launchResultMsg{sb: &pb.Sandbox{Id: "id123456"}})
	if m.screen != screenList {
		t.Error("successful launch should return to list")
	}
	if runCmd(cmd) == nil {
		t.Error("successful launch should refresh the list")
	}
}

func TestErrorAndWindowAndQuit(t *testing.T) {
	d := &fakeDaemon{}
	m := New(d, "/work")
	m, _ = update(m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.width != 100 || m.height != 40 {
		t.Error("window size not stored")
	}
	m, _ = update(m, errMsg{err: context.Canceled})
	if !strings.Contains(m.View(), "error:") {
		t.Error("error status not shown")
	}
	mm, cmd := update(m, press("q"))
	if !mm.quitting || cmd == nil {
		t.Error("q should quit")
	}
	if mm.View() != "" {
		t.Error("quitting view should be empty")
	}
}

func TestStatusTriggersRefresh(t *testing.T) {
	d := &fakeDaemon{}
	m := New(d, "/work")
	_, cmd := update(m, statusMsg("did a thing"))
	if runCmd(cmd) == nil {
		t.Error("status should trigger a refresh command")
	}
}

func TestSandboxRowAndHelpers(t *testing.T) {
	sb := &pb.Sandbox{
		DisplayName: "demo",
		State:       pb.SandboxState_SANDBOX_STATE_STOPPED,
		Sources:     []*pb.SourceRef{{Path: "/x/proj"}},
		SeedingMode: pb.SeedingMode_SEEDING_MODE_DUPLICATE,
	}
	if !strings.Contains(sandboxTitle(sb), "demo") {
		t.Errorf("sandboxTitle = %q", sandboxTitle(sb))
	}
	if !strings.Contains(sandboxDesc(sb), "proj") {
		t.Errorf("sandboxDesc = %q", sandboxDesc(sb))
	}
	// No sources renders a hint.
	if !strings.Contains(sandboxDesc(&pb.Sandbox{}), "no sources") {
		t.Error("expected no-sources hint")
	}
	if short("abc") != "abc" {
		t.Error("short of a short id should be unchanged")
	}
	if pad("xy", 5) != "xy   " {
		t.Errorf("pad = %q", pad("xy", 5))
	}
	if pad("toolong", 3) != "toolong" {
		t.Error("pad should not truncate")
	}
	if max(2, 5) != 5 || max(9, 1) != 9 {
		t.Error("max wrong")
	}
	if plural(1, "x", "xs") != "1 x" || plural(2, "x", "xs") != "2 xs" {
		t.Error("plural wrong")
	}
}

func TestInitIssuesRefresh(t *testing.T) {
	m := New(&fakeDaemon{}, "/work")
	if m.Init() == nil {
		t.Error("Init should issue commands")
	}
}
