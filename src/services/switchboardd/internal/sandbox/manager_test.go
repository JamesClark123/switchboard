package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// fakeRunner records calls and simulates container state without a real sbx.
type fakeRunner struct {
	mu       sync.Mutex
	running  map[string]bool
	launches int
	failNext error
	// kitAdds records "<ref> <kitSource>" per AddKit call; lastKits records the
	// KitSources of the most recent Launch, so tests can assert kits survive a
	// container recreate.
	kitAdds  []string
	lastKits []string
	// failStart makes Start fail, forcing bringUp down its relaunch branch (the
	// common real-world case: sbx often can't resume a container across a stop).
	failStart bool
}

func newFakeRunner() *fakeRunner { return &fakeRunner{running: map[string]bool{}} }

func (f *fakeRunner) Launch(_ context.Context, spec LaunchSpec, _ func(string)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "", err
	}
	f.launches++
	f.lastKits = spec.KitSources
	// Mirror the real SbxRunner: the handle is the assigned --name (the human
	// name), falling back to the id.
	ref := spec.Name
	if ref == "" {
		ref = spec.SandboxID
	}
	f.running[ref] = true
	return ref, nil
}
func (f *fakeRunner) Stop(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[ref] = false
	return nil
}
func (f *fakeRunner) Start(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart {
		return errors.New("start unsupported")
	}
	f.running[ref] = true
	return nil
}
func (f *fakeRunner) Destroy(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, ref)
	return nil
}
func (f *fakeRunner) IsRunning(_ context.Context, ref string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[ref], nil
}
func (f *fakeRunner) CloneRepo(_ context.Context, _, dest string, _ func(string)) error {
	return os.MkdirAll(dest, 0o755)
}
func (f *fakeRunner) AddKit(_ context.Context, ref, kitSource string, _ func(string)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.kitAdds = append(f.kitAdds, ref+" "+kitSource)
	return nil
}
func (f *fakeRunner) ValidateKit(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return "spec.yaml: invalid kind", err
	}
	return "", nil
}

func newTestManager(t *testing.T) (*Manager, *registry.Registry, *fakeRunner, string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	ws := filepath.Join(dir, "workspaces")
	runner := newFakeRunner()
	return NewManager(reg, runner, ws, "host-1"), reg, runner, dir
}

func makeSource(t *testing.T, dir, name string) *pb.SourceRef {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &pb.SourceRef{Path: p, IsRepo: false}
}

func TestLaunchStopRestartDestroy(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	var progressSeen bool
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "feature-work"},
		Sources: []*pb.SourceRef{src},
	}, func(duplicate.Progress) { progressSeen = true }, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("state = %v, want running", sb.GetState())
	}
	if sb.GetDisplayName() != "feature-work" {
		t.Errorf("display name = %q, want config label", sb.GetDisplayName())
	}
	if !progressSeen {
		t.Error("expected copy progress during launch")
	}
	// Verbatim copy present in controlled folder.
	if _, err := os.Stat(filepath.Join(sb.GetWorkspacePath(), "proj", "file.txt")); err != nil {
		t.Errorf("expected duplicated file: %v", err)
	}
	// Original unchanged.
	if _, err := os.Stat(filepath.Join(src.GetPath(), "file.txt")); err != nil {
		t.Errorf("source file missing: %v", err)
	}

	// Stop retains the copy (FR-012a).
	if _, err := m.Stop(context.Background(), sb.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sb.GetWorkspacePath()); err != nil {
		t.Errorf("stop must retain workspace copy: %v", err)
	}

	// Restart returns to running (FR-012b).
	r, err := m.Restart(context.Background(), sb.GetId(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("restart state = %v", r.GetState())
	}

	// Destroy deletes the copy (FR-012c).
	deleted, err := m.Destroy(context.Background(), sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Error("expected workspace deletion on destroy")
	}
	if _, err := os.Stat(sb.GetWorkspacePath()); !os.IsNotExist(err) {
		t.Errorf("destroy must delete workspace copy, stat err=%v", err)
	}
	_ = runner
}

func TestLaunchFailureMarksError(t *testing.T) {
	m, _, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	runner.failNext = errContext()

	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "x"},
		Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected launch error")
	}
	if sb.GetState() != pb.SandboxState_SANDBOX_STATE_ERROR {
		t.Errorf("state = %v, want error", sb.GetState())
	}
	if sb.GetError() == "" {
		t.Error("expected error message recorded")
	}
}

func TestReadoptReconcilesState(t *testing.T) {
	m, reg, runner, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "x"},
		Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate the container dying while the daemon was down.
	runner.mu.Lock()
	runner.running[sb.GetContainerRef()] = false
	runner.mu.Unlock()

	if err := m.Readopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if got.GetState() != pb.SandboxState_SANDBOX_STATE_STOPPED {
		t.Errorf("readopt: dead container should be stopped, got %v", got.GetState())
	}

	// Now mark it running again and re-adopt -> running.
	runner.mu.Lock()
	runner.running[sb.GetContainerRef()] = true
	runner.mu.Unlock()
	if err := m.Readopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ = reg.Get(sb.GetId())
	if got.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("readopt: live container should be running, got %v", got.GetState())
	}
}

func TestRename(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "x"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := sb.GetWorkspacePath()

	// A running sandbox cannot be renamed.
	if _, err := m.Rename(ctx, sb.GetId(), "my-custom-name"); err == nil {
		t.Fatal("expected error renaming a running sandbox")
	}

	// Stop, then rename: the display name and the retained workspace dir both move.
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}
	out, err := m.Rename(ctx, sb.GetId(), "my-custom-name")
	if err != nil {
		t.Fatal(err)
	}
	if out.GetDisplayName() != "my-custom-name" {
		t.Errorf("rename failed: %q", out.GetDisplayName())
	}
	wantPath := filepath.Join(filepath.Dir(oldPath), "my-custom-name")
	if out.GetWorkspacePath() != wantPath {
		t.Errorf("workspace path = %q, want %q", out.GetWorkspacePath(), wantPath)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old workspace dir should be gone, stat err = %v", err)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("renamed workspace dir should exist: %v", err)
	}

	// Invalid and duplicate names are rejected.
	if _, err := m.Rename(ctx, sb.GetId(), ""); err == nil {
		t.Error("expected error renaming to empty")
	}
	if _, err := m.Rename(ctx, sb.GetId(), "bad name"); err == nil {
		t.Error("expected error for a name with spaces")
	}
}

func TestLaunchNameUniquenessAndPath(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")

	// Explicit name is used as the workspace directory basename.
	a, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "alpha",
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.GetDisplayName() != "alpha" || filepath.Base(a.GetWorkspacePath()) != "alpha" {
		t.Errorf("name/path = %q / %q, want alpha", a.GetDisplayName(), a.GetWorkspacePath())
	}

	// A second explicit "alpha" on the same host is rejected.
	if _, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "alpha",
	}, nil, nil); err == nil {
		t.Error("expected a duplicate-name error")
	}

	// A colliding config label is auto-uniquified rather than rejected.
	b, err := m.Launch(ctx, LaunchRequest{
		Config: &pb.ConfigSnapshot{Name: "alpha"}, Sources: []*pb.SourceRef{src},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.GetDisplayName() == "alpha" || !strings.HasPrefix(b.GetDisplayName(), "alpha-") {
		t.Errorf("label collision should uniquify, got %q", b.GetDisplayName())
	}
}

func errContext() error { return context.DeadlineExceeded }
