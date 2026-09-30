package ui

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/store"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// --- feature 007 fakes on fakeDaemon ---

func (f *fakeDaemon) AddSources(_ context.Context, id string, sources []*pb.SourceRef, override bool, onUpdate func(client.LaunchUpdate)) (*pb.Sandbox, *pb.ResourceReport, error) {
	if f.addGate != nil {
		<-f.addGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastAddOverride = override
	if f.addSourcesErr != nil {
		return nil, nil, f.addSourcesErr
	}
	if f.addBlocked != nil && !override {
		return nil, f.addBlocked, nil
	}
	if onUpdate != nil && len(sources) > 0 {
		onUpdate(client.LaunchUpdate{Copy: &pb.LaunchProgress_CopyProgress{
			BytesCopied: 50, BytesTotal: 100, CurrentPath: filepath.Join(sources[0].GetPath(), "main.go"),
		}})
	}
	if f.addedSources == nil {
		f.addedSources = map[string][]*pb.SourceRef{}
	}
	f.addedSources[id] = append(f.addedSources[id], sources...)
	for _, sb := range f.sandboxes {
		if sb.GetId() == id {
			sb.Sources = append(sb.Sources, sources...)
			return proto.Clone(sb).(*pb.Sandbox), nil, nil
		}
	}
	return &pb.Sandbox{Id: id, Sources: sources}, nil, nil
}

func (f *fakeDaemon) RemoveSources(_ context.Context, id string, paths []string) (*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeSourcesErr != nil {
		return nil, f.removeSourcesErr
	}
	if f.removedSources == nil {
		f.removedSources = map[string][]string{}
	}
	f.removedSources[id] = append(f.removedSources[id], paths...)
	drop := map[string]bool{}
	for _, p := range paths {
		drop[p] = true
	}
	for _, sb := range f.sandboxes {
		if sb.GetId() == id {
			kept := make([]*pb.SourceRef, 0, len(sb.Sources))
			for _, src := range sb.Sources {
				if !drop[src.GetPath()] {
					kept = append(kept, src)
				}
			}
			sb.Sources = kept
			return proto.Clone(sb).(*pb.Sandbox), nil
		}
	}
	return &pb.Sandbox{Id: id}, nil
}

// sourcesModel is a sized list with one sandbox (api + web) whose launch root
// holds a "lib" directory to add.
func sourcesModel(t *testing.T, d *fakeDaemon) (Model, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	d.sandboxes = []*pb.Sandbox{refreshableSandbox()}
	m := withSandboxes(sized(New(d, root)), d.sandboxes)
	return m, root
}

// drain pumps the add's channel messages through Update until the terminal
// result lands, returning the model and the last command.
func drain(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	for i := 0; i < 10 && cmd != nil; i++ {
		msg := runCmd(cmd)
		if msg == nil {
			return m, nil
		}
		m, cmd = update(m, msg)
		if _, done := msg.(sourcesAddResultMsg); done {
			return m, cmd
		}
	}
	return m, cmd
}

// `S` opens the overlay listing the recorded folders with their paths; esc closes it.
func TestSourcesKeyOpensOverlayListingRecordedFolders(t *testing.T) {
	m, _ := sourcesModel(t, &fakeDaemon{})
	m, _ = update(m, press("S"))
	if m.screen != screenSources {
		t.Fatalf("screen = %v, want screenSources", m.screen)
	}
	v := m.View()
	for _, want := range []string{"Sources · feature-work", "api", "web", "/home/u/code/api", "duplicate"} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay should show %q; got:\n%s", want, v)
		}
	}
	// It floats over the list: the list page stays visible.
	if !strings.Contains(v, "Switchboard") {
		t.Error("the list page should remain visible behind the overlay")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenList {
		t.Error("esc should close the overlay")
	}
}

// The add flow browses, selects, dispatches, streams progress, and lands the
// updated set in both the overlay and the row (US1).
func TestSourcesAddFlowLandsUpdatedSet(t *testing.T) {
	d := &fakeDaemon{}
	m, root := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, press("a"))
	if !m.sourcesView.browsing || !hasEntry(m.launch.entries, "lib") {
		t.Fatalf("a should open the browser on the launch root: browsing=%v entries=%+v", m.sourcesView.browsing, m.launch.entries)
	}
	if !strings.Contains(m.View(), "Add folders from") {
		t.Error("the overlay should render the browser")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // select lib
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should dispatch the add")
	}
	if _, ok := m.sourceAdds["sb-1"]; !ok {
		t.Fatal("the add should be registered in flight")
	}
	if m.sourcesView.browsing {
		t.Error("dispatching should close the browser")
	}
	if m.screen != screenSources {
		t.Errorf("screen = %v, want the overlay to stay open", m.screen)
	}

	// First frame: copy progress badges the row and the overlay.
	msg := runCmd(cmd)
	if _, ok := msg.(sourcesProgressMsg); !ok {
		t.Fatalf("first message = %T, want sourcesProgressMsg", msg)
	}
	m, cmd = update(m, msg)
	if got := m.sourceAdds["sb-1"].progress; got != "adding lib — 50%" {
		t.Errorf("progress = %q", got)
	}
	if !strings.Contains(m.viewList(), "adding lib") || !strings.Contains(m.View(), "adding lib") {
		t.Error("the row and the overlay should badge the in-flight add")
	}
	if strings.Contains(m.viewList(), "creating") {
		t.Error("an add must never render the row as creating (FR-054)")
	}

	// Terminal frame: the record lands everywhere.
	m, _ = drain(t, m, cmd)
	if _, ok := m.sourceAdds["sb-1"]; ok {
		t.Error("the in-flight entry should clear on completion")
	}
	if !strings.Contains(m.status, "added lib to feature-work") {
		t.Errorf("status = %q", m.status)
	}
	if n := len(m.sourcesView.sources); n != 3 {
		t.Errorf("overlay sources = %d, want 3", n)
	}
	if !strings.Contains(sandboxDesc(m.sandboxes[0]), "lib") {
		t.Error("the row's seeded-folder display should include the addition")
	}
	if got := d.addedSources["sb-1"]; len(got) != 1 || got[0].GetPath() != filepath.Join(root, "lib") {
		t.Errorf("daemon received %+v", got)
	}
	if d.lastAddOverride {
		t.Error("a first add must not set the override flag")
	}
}

// While a (slow) add streams, the row badges progress, the sandbox stays in its
// real state, and the rest of the TUI remains usable (FR-055).
func TestSourcesAddProgressBadgeKeepsTUIResponsive(t *testing.T) {
	d := &fakeDaemon{addGate: make(chan struct{})}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, press("a"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})

	if !strings.Contains(m.viewList(), "adding lib") || !strings.Contains(m.viewList(), "RUNNING") {
		t.Errorf("row should show the badge AND its real state; got:\n%s", m.viewList())
	}
	// A second add on the same sandbox is refused client-side while one runs.
	m, _ = update(m, press("a"))
	if m.sourcesView.browsing || !strings.Contains(m.sourcesView.status, "already in progress") {
		t.Errorf("a second add should be refused: browsing=%v status=%q", m.sourcesView.browsing, m.sourcesView.status)
	}
	// The TUI is not blocked: close the overlay and open the launch wizard.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	m, _ = update(m, press("n"))
	if m.screen != screenLaunch {
		t.Error("the TUI should stay usable while an add streams")
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})

	close(d.addGate)
	m, _ = drain(t, m, cmd)
	if !strings.Contains(m.status, "added lib") {
		t.Errorf("status = %q", m.status)
	}
}

// A low-resource block opens the override prompt; declining cancels with nothing
// added, accepting re-sends with the override flag (FR-057).
func TestSourcesAddBlockedOverrideRoundTrip(t *testing.T) {
	d := &fakeDaemon{addBlocked: &pb.ResourceReport{Warnings: []string{"low disk: 1 byte free"}}}
	m, _ := sourcesModel(t, d)
	runFlow := func(m Model) Model {
		m, _ = update(m, press("a"))
		m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
		m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
		m, _ = drain(t, m, cmd)
		return m
	}
	m, _ = update(m, press("S"))
	m = runFlow(m)
	if m.screen != screenConfirm {
		t.Fatalf("screen = %v, want the override prompt", m.screen)
	}
	v := m.View()
	for _, want := range []string{"Low resources", "low disk: 1 byte free", "lib", "feature-work"} {
		if !strings.Contains(v, want) {
			t.Errorf("prompt should show %q; got:\n%s", want, v)
		}
	}
	if strings.Contains(v, "cannot be undone") {
		t.Error("the override prompt must not claim irreversibility")
	}
	// Decline: back on the overlay, nothing added.
	m, _ = update(m, press("n"))
	if m.screen != screenSources {
		t.Errorf("declining should return to the overlay, got %v", m.screen)
	}
	if len(d.addedSources["sb-1"]) != 0 {
		t.Error("declining must add nothing")
	}
	// Accept: the add is re-sent with the override set and completes.
	m = runFlow(m)
	m, cmd := update(m, press("y"))
	msg := runCmd(cmd)
	if _, ok := msg.(sourcesOverrideMsg); !ok {
		t.Fatalf("confirm should yield sourcesOverrideMsg, got %T", msg)
	}
	m, cmd = update(m, msg)
	if _, ok := m.sourceAdds["sb-1"]; !ok {
		t.Fatal("the override should re-dispatch the add")
	}
	m, _ = drain(t, m, cmd)
	if !d.lastAddOverride {
		t.Error("the re-sent add must carry the override flag")
	}
	if got := d.addedSources["sb-1"]; len(got) != 1 {
		t.Errorf("daemon received %+v, want exactly the overridden add", got)
	}
	if !strings.Contains(m.status, "added lib") {
		t.Errorf("status = %q", m.status)
	}
}

func TestSourcesAddErrorSurfaces(t *testing.T) {
	d := &fakeDaemon{addSourcesErr: errors.New("folder name collision: \"lib\" already exists")}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, press("a"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drain(t, m, cmd)
	if !strings.Contains(m.status, "add failed") || !strings.Contains(m.status, "already exists") {
		t.Errorf("status = %q, want the daemon's refusal verbatim", m.status)
	}
	if !strings.Contains(m.View(), "already exists") {
		t.Error("the overlay should show the failure")
	}
	if len(m.sourcesView.sources) != 2 {
		t.Error("a failed add must leave the overlay's set unchanged")
	}
}

// The browser sub-mode navigates like the wizard, refuses an empty selection,
// and esc returns to the folder list.
func TestSourcesBrowserNavigation(t *testing.T) {
	d := &fakeDaemon{}
	m, root := sourcesModel(t, d)
	if err := os.MkdirAll(filepath.Join(root, "lib", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ = update(m, press("S"))
	m, _ = update(m, press("a"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEnter}) // nothing selected
	if !strings.Contains(m.sourcesView.status, "select at least one") {
		t.Errorf("status = %q", m.sourcesView.status)
	}
	m, _ = update(m, press("l")) // descend into lib
	if filepath.Base(m.launch.dir) != "lib" {
		t.Errorf("→ should descend, dir=%q", m.launch.dir)
	}
	m, _ = update(m, press("j"))
	m, _ = update(m, press("k"))
	m, _ = update(m, press("h")) // back up
	if m.launch.dir != root {
		t.Errorf("← should ascend, dir=%q", m.launch.dir)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.sourcesView.browsing || m.screen != screenSources {
		t.Error("esc should leave the browser but keep the overlay open")
	}
	// A second esc closes the overlay; q does too.
	m, _ = update(m, press("q"))
	if m.screen != screenList {
		t.Error("q should close the overlay")
	}
}

// --- US2: remove ---

// The confirm names the sandbox, every marked folder, and the consequence; esc and
// unrecognised keys never confirm (FR-060, SC-006).
func TestSourcesRemoveConfirmNamesEverythingAndCancels(t *testing.T) {
	d := &fakeDaemon{}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // mark api
	if !m.sourcesView.marked["/home/u/code/api"] {
		t.Fatal("space should mark the highlighted folder")
	}
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm {
		t.Fatalf("d should open the confirm, got %v", m.screen)
	}
	v := m.View()
	for _, want := range []string{"Remove seeded folders?", "feature-work", "api", "/home/u/code/api", "uncommitted", "cannot be undone", "untouched"} {
		if !strings.Contains(v, want) {
			t.Errorf("confirm should mention %q; got:\n%s", want, v)
		}
	}
	// A stray key keeps the dialog open; esc cancels back to the overlay.
	m, _ = update(m, press("x"))
	if m.screen != screenConfirm {
		t.Error("an unrecognised key must not close the dialog")
	}
	m, cmd := update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		runCmd(cmd)
	}
	if m.screen != screenSources {
		t.Errorf("esc should return to the overlay, got %v", m.screen)
	}
	if len(d.removedSources) != 0 {
		t.Error("cancelling must remove nothing")
	}
	// Space toggles the mark off again.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	if m.sourcesView.marked["/home/u/code/api"] {
		t.Error("space again should unmark")
	}
}

// A confirmed removal spins the row, then updates the overlay and the row.
func TestSourcesRemoveConfirmedUpdatesOverlayAndRow(t *testing.T) {
	d := &fakeDaemon{}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, press("j"))                     // -> web
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // mark web
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	if m.screen != screenSources {
		t.Errorf("confirming should land back on the overlay, got %v", m.screen)
	}
	if m.busy["sb-1"] != "removing" {
		t.Errorf("busy = %v, want the row marked removing", m.busy)
	}
	msg := runCmd(cmd)
	if _, ok := msg.(sourcesRemovedMsg); !ok {
		t.Fatalf("msg = %T, want sourcesRemovedMsg", msg)
	}
	m, _ = update(m, msg)
	if _, busy := m.busy["sb-1"]; busy {
		t.Error("the row spinner should clear")
	}
	if !strings.Contains(m.status, "removed web from feature-work") {
		t.Errorf("status = %q", m.status)
	}
	if got := d.removedSources["sb-1"]; len(got) != 1 || got[0] != "/home/u/code/web" {
		t.Errorf("daemon received %v", got)
	}
	if n := len(m.sourcesView.sources); n != 1 || m.sourcesView.sources[0].GetPath() != "/home/u/code/api" {
		t.Errorf("overlay sources = %+v, want {api}", m.sourcesView.sources)
	}
	if len(m.sourcesView.marked) != 0 {
		t.Error("marks should clear after a removal")
	}
	if desc := sandboxDesc(m.sandboxes[0]); strings.Contains(desc, "web") {
		t.Errorf("row desc = %q, should no longer list web", desc)
	}
}

// A daemon-side guard refusal surfaces its message verbatim.
func TestSourcesRemoveRefusalSurfacesMessage(t *testing.T) {
	d := &fakeDaemon{removeSourcesErr: errors.New(`service "web" is running with its working directory in "api" — stop it first`)}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, press("d"))
	m, cmd := update(m, press("y"))
	m, _ = update(m, runCmd(cmd))
	if !strings.Contains(m.status, "stop it first") || !strings.Contains(m.sourcesView.status, `service "web"`) {
		t.Errorf("status = %q / overlay %q, want the refusal verbatim", m.status, m.sourcesView.status)
	}
	if len(m.sourcesView.sources) != 2 {
		t.Error("a refused removal must leave the set unchanged")
	}
}

// The at-least-one floor is enforced client-side before any round-trip; `d` with
// nothing marked targets the highlighted folder.
func TestSourcesRemoveFloorAndHighlightedTarget(t *testing.T) {
	d := &fakeDaemon{}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // api
	m, _ = update(m, press("j"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // web — that is everything
	m, _ = update(m, press("d"))
	if m.screen != screenSources || !strings.Contains(m.sourcesView.status, "at least one") {
		t.Errorf("removing every folder should be refused: screen=%v status=%q", m.screen, m.sourcesView.status)
	}
	// Unmark both; `d` on the highlighted (web) row names just web.
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, press("k"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, _ = update(m, press("j"))
	m, _ = update(m, press("d"))
	if m.screen != screenConfirm {
		t.Fatalf("d should confirm the highlighted folder, got %v", m.screen)
	}
	v := m.View()
	if !strings.Contains(v, "/home/u/code/web") || strings.Contains(v, "/home/u/code/api") {
		t.Errorf("confirm should name only web; got:\n%s", v)
	}
	// A single-source sandbox cannot lose its last folder.
	d2 := &fakeDaemon{}
	m2, _ := sourcesModel(t, d2)
	d2.sandboxes[0].Sources = d2.sandboxes[0].Sources[:1]
	m2, _ = update(m2, sandboxesMsg(d2.sandboxes))
	m2, _ = update(m2, press("S"))
	m2, _ = update(m2, press("d"))
	if m2.screen != screenSources || !strings.Contains(m2.sourcesView.status, "at least one") {
		t.Errorf("the last folder must be kept: screen=%v status=%q", m2.screen, m2.sourcesView.status)
	}
}

// --- US3: a live sandbox_changed carrying edited sources re-renders the row and an
// open overlay without any reload (FR-064/FR-067). ---

func TestSandboxChangedEventUpdatesRowAndOverlay(t *testing.T) {
	d := &fakeDaemon{}
	m, _ := sourcesModel(t, d)
	m, _ = update(m, press("S"))
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace}) // mark api, which the event drops

	edited := proto.Clone(d.sandboxes[0]).(*pb.Sandbox)
	edited.Sources = []*pb.SourceRef{{Path: "/home/u/code/web"}, {Path: "/home/u/code/docs"}}
	m, _ = update(m, eventMsg{ev: &pb.Event{Event: &pb.Event_SandboxChanged{SandboxChanged: edited}}})

	if got := m.sourcesView.sources; len(got) != 2 || got[1].GetPath() != "/home/u/code/docs" {
		t.Errorf("overlay sources = %+v, want web + docs", got)
	}
	if m.sourcesView.marked["/home/u/code/api"] {
		t.Error("a mark on a folder that no longer exists should be dropped")
	}
	if !strings.Contains(m.View(), "docs") {
		t.Error("the open overlay should re-render with the added folder")
	}
	if desc := sandboxDesc(m.sandboxes[0]); !strings.Contains(desc, "docs") || strings.Contains(desc, "api") {
		t.Errorf("row desc = %q, want it to follow the edited record", desc)
	}
	if !strings.Contains(m.viewList(), "docs") {
		t.Error("the list should re-render the row's seeded folders")
	}
	// renderFieldsDiffer notices source edits alone.
	if !renderFieldsDiffer(&pb.Sandbox{Sources: []*pb.SourceRef{{Path: "/a"}}}, &pb.Sandbox{Sources: []*pb.SourceRef{{Path: "/b"}}}) {
		t.Error("a source change must count as a rendered-field change")
	}
	if renderFieldsDiffer(&pb.Sandbox{Sources: []*pb.SourceRef{{Path: "/a"}}}, &pb.Sandbox{Sources: []*pb.SourceRef{{Path: "/a"}}}) {
		t.Error("identical sources must not count as a change")
	}
}

// --- US4: the overlay targets the OWNING host (FR-065). ---

// sourcesHostServer is a launchHostServer that also owns one sandbox and records
// the sources added to it, so a test can tell which host an add landed on.
type sourcesHostServer struct {
	launchHostServer
	mu        sync.Mutex
	sandboxes []*pb.Sandbox
	added     []string
}

func (s *sourcesHostServer) ListSandboxes(context.Context, *pb.ListSandboxesRequest) (*pb.ListSandboxesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &pb.ListSandboxesResponse{Sandboxes: s.sandboxes}, nil
}

func (s *sourcesHostServer) AddSandboxSources(req *pb.AddSandboxSourcesRequest, stream pb.Switchboard_AddSandboxSourcesServer) error {
	s.mu.Lock()
	for _, src := range req.GetSources() {
		s.added = append(s.added, src.GetPath())
	}
	var out *pb.Sandbox
	for _, sb := range s.sandboxes {
		if sb.GetId() == req.GetSandboxId() {
			sb.Sources = append(sb.Sources, req.GetSources()...)
			out = proto.Clone(sb).(*pb.Sandbox)
		}
	}
	s.mu.Unlock()
	if out == nil {
		return errors.New("unknown sandbox")
	}
	return stream.Send(&pb.LaunchProgress{Event: &pb.LaunchProgress_Done{Done: out}})
}

func startSourcesHost(t *testing.T, srv *sourcesHostServer) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), srv.id+".sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	g := grpc.NewServer()
	pb.RegisterSwitchboardServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return sock
}

func TestSourcesOverlayTargetsOwningHost(t *testing.T) {
	hostA := &sourcesHostServer{launchHostServer: launchHostServer{id: "hosta", workspace: "/srv/a"}}
	hostB := &sourcesHostServer{
		launchHostServer: launchHostServer{id: "hostb", workspace: "/srv/b"},
		sandboxes: []*pb.Sandbox{{
			Id: "remote-1", DisplayName: "remote-box", HostId: "hostb",
			State:   pb.SandboxState_SANDBOX_STATE_RUNNING,
			Sources: []*pb.SourceRef{{Path: "/srv/b/repo-hostb", IsRepo: true}},
		}},
	}
	socks := map[string]string{"hosta": startSourcesHost(t, hostA), "hostb": startSourcesHost(t, hostB)}

	s, _ := store.New(t.TempDir())
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
	// The active host is hosta; the only sandbox lives on hostb.
	m := sized(New(&fakeDaemon{}, "/work").WithHosts(mgr, s.Hosts(), "hosta"))
	m, _ = update(m, runCmd(m.listDataCmd()))
	if m.current() == nil || m.current().GetId() != "remote-1" {
		t.Fatalf("expected hostb's sandbox to be the selected row, got %+v", m.current())
	}

	m, _ = update(m, press("S"))
	if m.sourcesView.host != "hostb" {
		t.Fatalf("overlay host = %q, want hostb (the owner), not the active host", m.sourcesView.host)
	}
	// `a` browses hostb's filesystem, asynchronously, from ITS workspace root.
	m, cmd := update(m, press("a"))
	if !m.launch.loading || m.launch.dir != "/srv/b" || m.launch.targetHost != "hostb" {
		t.Fatalf("browse should target hostb at /srv/b: loading=%v dir=%q host=%q", m.launch.loading, m.launch.dir, m.launch.targetHost)
	}
	m, _ = update(m, runCmd(cmd)) // browseMsg from hostb
	if !hasEntry(m.launch.entries, "repo-hostb") {
		t.Fatalf("browser should list hostb's candidate, got %+v", m.launch.entries)
	}
	m, _ = update(m, tea.KeyMsg{Type: tea.KeySpace})
	m, cmd = update(m, tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = drain(t, m, cmd)

	hostB.mu.Lock()
	addedB := append([]string(nil), hostB.added...)
	hostB.mu.Unlock()
	hostA.mu.Lock()
	addedA := len(hostA.added)
	hostA.mu.Unlock()
	if len(addedB) != 1 || addedB[0] != "/srv/b/repo-hostb" {
		t.Errorf("hostb received %v, want its own repo", addedB)
	}
	if addedA != 0 {
		t.Error("the add must never reach the active host when another host owns the sandbox")
	}
	if !strings.Contains(m.status, "added repo-hostb to remote-box") {
		t.Errorf("status = %q", m.status)
	}
	if len(m.sourcesView.sources) != 2 {
		t.Errorf("overlay sources = %+v, want 2", m.sourcesView.sources)
	}
}

// Small helpers: name resolution for status lines and cursor bounds.
func TestSourcesHelpers(t *testing.T) {
	d := &fakeDaemon{}
	m, _ := sourcesModel(t, d)
	if got := m.sandboxName("sb-1"); got != "feature-work" {
		t.Errorf("sandboxName from the list = %q", got)
	}
	m.sourcesView = sourcesState{sandboxID: "gone", sandbox: "remembered"}
	if got := m.sandboxName("gone"); got != "remembered" {
		t.Errorf("sandboxName from the overlay = %q", got)
	}
	if got := m.sandboxName("0123456789"); got != "01234567" {
		t.Errorf("sandboxName fallback = %q, want the short id", got)
	}
	m.sourcesView = sourcesState{cursor: 5}
	if _, ok := m.highlightedSource(); ok {
		t.Error("an out-of-range cursor must not yield a source")
	}
	if folderNames(nil) != "" || folderNames([]*pb.SourceRef{{Path: "/a/b"}, {Path: "/c"}}) != "b, c" {
		t.Error("folderNames should join basenames")
	}
	// syncSourcesOverlay ignores nil and other sandboxes.
	m.sourcesView = sourcesState{sandboxID: "sb-1", sources: refreshableSandbox().Sources, marked: map[string]bool{}}
	m.screen = screenSources
	m.syncSourcesOverlay(nil)
	m.syncSourcesOverlay(&pb.Sandbox{Id: "other"})
	if len(m.sourcesView.sources) != 2 {
		t.Error("an unrelated update must not touch the overlay")
	}
}
