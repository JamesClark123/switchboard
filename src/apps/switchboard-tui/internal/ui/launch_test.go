package ui

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/jamesclark123/switchboard/apps/switchboard-tui/internal/client"
	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
)

// fakeDaemon implements the ui.Daemon interface for teatest, simulating a local
// daemon without any gRPC/Docker/sbx involvement.
type fakeDaemon struct {
	mu           sync.Mutex
	sandboxes    []*pb.Sandbox
	candidates   []*pb.SourceRef
	manifest     *pb.OptionManifest
	lastConfig   *pb.ConfigSnapshot
	lastDisplay  string
	events       chan *pb.Event
	promptedID   string
	promptText   string
	ackedIDs     []string
	subscribeErr error
	vscodeErr    error
	daemonVer    string
	updateErr    error
	updateTarget string
}

func (f *fakeDaemon) PromptAgent(_ context.Context, id, prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promptedID, f.promptText = id, prompt
	return nil
}

func (f *fakeDaemon) AckNotifications(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ackedIDs = append(f.ackedIDs, ids...)
	return nil
}

func (f *fakeDaemon) Subscribe(ctx context.Context, _ bool) (client.EventStream, error) {
	if f.subscribeErr != nil {
		return nil, f.subscribeErr
	}
	return &fakeStream{ctx: ctx, ch: f.events}, nil
}

func (f *fakeDaemon) VSCodeTarget(_ context.Context, id string) (*pb.VSCodeTarget, error) {
	if f.vscodeErr != nil {
		return nil, f.vscodeErr
	}
	return &pb.VSCodeTarget{ContainerName: "/" + id, WorkspacePath: "/workspace"}, nil
}

// fakeStream feeds queued events (or blocks until ctx is done when ch is nil).
type fakeStream struct {
	ctx context.Context
	ch  chan *pb.Event
}

func (s *fakeStream) Recv() (*pb.Event, error) {
	select {
	case <-s.ctx.Done():
		return nil, io.EOF
	case ev, ok := <-s.ch:
		if !ok {
			return nil, io.EOF
		}
		return ev, nil
	}
}

func (f *fakeDaemon) HostID() string { return "test-host" }

func (f *fakeDaemon) DaemonVersion() string { return f.daemonVer }

func (f *fakeDaemon) UpdateDaemon(_ context.Context, target string, onProgress func(stage, message string)) error {
	f.mu.Lock()
	f.updateTarget = target
	f.mu.Unlock()
	if onProgress != nil {
		onProgress("applying", "installing "+target)
	}
	return f.updateErr
}

func (f *fakeDaemon) List(context.Context) ([]*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*pb.Sandbox, len(f.sandboxes))
	copy(out, f.sandboxes)
	return out, nil
}

func (f *fakeDaemon) ListSources(context.Context, string, bool) ([]*pb.SourceRef, error) {
	return f.candidates, nil
}

func (f *fakeDaemon) OptionManifest(context.Context) (*pb.OptionManifest, error) {
	return f.manifest, nil
}

func (f *fakeDaemon) Launch(_ context.Context, req *pb.LaunchSandboxRequest, onUpdate func(client.LaunchUpdate)) (*pb.Sandbox, *pb.ResourceReport, error) {
	if onUpdate != nil {
		onUpdate(client.LaunchUpdate{Copy: &pb.LaunchProgress_CopyProgress{BytesCopied: 50, BytesTotal: 100, CurrentPath: "proj/main.go"}})
	}
	name := req.GetDisplayName()
	if name == "" {
		name = "sandbox-abcd1234"
	}
	sb := &pb.Sandbox{
		Id:          "abcd1234ef",
		DisplayName: name,
		State:       pb.SandboxState_SANDBOX_STATE_RUNNING,
		Sources:     req.GetSources(),
		SeedingMode: req.GetConfig().GetSeedingMode(),
	}
	f.mu.Lock()
	f.sandboxes = append(f.sandboxes, sb)
	f.lastConfig = req.GetConfig()
	f.lastDisplay = req.GetDisplayName()
	f.mu.Unlock()
	return sb, nil, nil
}

func (f *fakeDaemon) Stop(_ context.Context, id string) (*pb.Sandbox, error) {
	return f.setState(id, pb.SandboxState_SANDBOX_STATE_STOPPED)
}
func (f *fakeDaemon) Restart(_ context.Context, id string) (*pb.Sandbox, error) {
	return f.setState(id, pb.SandboxState_SANDBOX_STATE_RUNNING)
}
func (f *fakeDaemon) Destroy(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.sandboxes {
		if s.GetId() == id {
			f.sandboxes = append(f.sandboxes[:i], f.sandboxes[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeDaemon) Rename(_ context.Context, id, name string) (*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sandboxes {
		if s.GetId() == id {
			s.DisplayName = name
			return s, nil
		}
	}
	return nil, nil
}
func (f *fakeDaemon) setState(id string, st pb.SandboxState) (*pb.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sandboxes {
		if s.GetId() == id {
			s.State = st
			return s, nil
		}
	}
	return nil, nil
}

// step gives async tea.Cmds (gRPC/launch goroutines) time to land before the
// next keystroke, keeping the interaction deterministic.
func step() { time.Sleep(80 * time.Millisecond) }

// fullOutput reads the entire cumulative program output after it has finished.
func fullOutput(t *testing.T, tm *teatest.TestModel) string {
	t.Helper()
	// Esc returns to the list from any sub-screen (no-op on the list itself),
	// where 'q' quits.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
	b, err := io.ReadAll(tm.Output())
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	return string(b)
}

func assertContains(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q", s)
		}
	}
}

func TestLaunchWizardFanOut(t *testing.T) {
	// Real directories the browser lists; "proj" and "lib" are the sources to seed.
	root := t.TempDir()
	for _, d := range []string{"lib", "proj"} { // sorted order in the browser
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := &fakeDaemon{}
	tm := teatest.NewTestModel(t, New(d, root), teatest.WithInitialTermSize(100, 32))
	step()

	// Open the launch overlay, select BOTH directories (space), then launch (enter).
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeySpace}) // select "lib"
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // -> proj
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeySpace}) // select "proj"
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // launch
	step()
	step()

	out := fullOutput(t, tm)
	assertContains(t, out, "Switchboard", "Launch sandbox", "proj", "lib", "sandbox-abcd1234", "RUNNING")

	// The fake daemon recorded one launched sandbox seeded from BOTH directories.
	list, _ := d.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("expected 1 sandbox, got %d", len(list))
	}
	bases := map[string]bool{}
	for _, s := range list[0].GetSources() {
		bases[filepath.Base(s.GetPath())] = true
	}
	if !bases["lib"] || !bases["proj"] {
		t.Errorf("launched sandbox should seed both lib and proj, got %+v", list[0].GetSources())
	}
}

func TestLaunchRequiresSelection(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{}
	tm := teatest.NewTestModel(t, New(d, root), teatest.WithInitialTermSize(100, 32))
	step()

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	step()
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // enter with nothing selected -> prompt to select
	step()

	out := fullOutput(t, tm)
	assertContains(t, out, "Launch sandbox", "select at least one directory")
	if list, _ := d.List(context.Background()); len(list) != 0 {
		t.Fatalf("expected no sandboxes, got %d", len(list))
	}
}
