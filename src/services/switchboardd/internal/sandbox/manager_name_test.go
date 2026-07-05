package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/registry"
)

// destroyFailRunner behaves like the fake runner but fails Destroy, exercising
// Rename's container-release error path.
type destroyFailRunner struct{ *fakeRunner }

func (destroyFailRunner) Destroy(context.Context, string) error { return errors.New("rm failed") }

func TestRenameContainerReleaseError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	m := NewManager(reg, destroyFailRunner{newFakeRunner()}, filepath.Join(dir, "ws"), "host-1")

	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "orig"}, Sources: []*pb.SourceRef{src}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rename(ctx, sb.GetId(), "renamed"); err == nil {
		t.Error("rename should surface the container-release error")
	}
}

func TestListPrunesMissingWorkspace(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "gone"}, Sources: []*pb.SourceRef{src}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The retained copy vanishes (e.g. deleted out-of-band, or a rename left the
	// record pointing at a non-existent path).
	if err := os.RemoveAll(sb.GetWorkspacePath()); err != nil {
		t.Fatal(err)
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("a sandbox with a missing workspace should be pruned, %d remain", len(list))
	}
	if _, err := m.store.Get(sb.GetId()); err == nil {
		t.Error("the pruned sandbox record should be deleted")
	}
}

// idOnlyDestroyRunner only accepts Destroy by the uuid id, simulating a container
// that sbx still knows by id (out of sync with the display name).
type idOnlyDestroyRunner struct {
	*fakeRunner
	wantID string
}

func (r *idOnlyDestroyRunner) Destroy(_ context.Context, ref string) error {
	if ref != r.wantID {
		return errors.New("no such sandbox: " + ref)
	}
	return nil
}

func TestDestroyFallsBackToID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	runner := &idOnlyDestroyRunner{fakeRunner: newFakeRunner()}
	m := NewManager(reg, runner, filepath.Join(dir, "ws"), "host-1")

	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "myname"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.wantID = sb.GetId() // sbx only answers to the id, not "myname"

	deleted, err := m.Destroy(ctx, sb.GetId())
	if err != nil {
		t.Fatalf("destroy should succeed via the id fallback: %v", err)
	}
	if !deleted {
		t.Error("destroy should remove the retained workspace")
	}
}

// idOnlyStartRunner accepts Start only by the uuid id (name fails), simulating a
// container sbx still knows by id — the restart must fall back from name to id.
type idOnlyStartRunner struct {
	*fakeRunner
	wantID string
}

func (r *idOnlyStartRunner) Start(_ context.Context, ref string) error {
	if ref != r.wantID {
		return errors.New("no such sandbox: " + ref)
	}
	return nil
}

func TestRestartTriesNameThenFallsBackToID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	runner := &idOnlyStartRunner{fakeRunner: newFakeRunner()}
	m := NewManager(reg, runner, filepath.Join(dir, "ws"), "host-1")

	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "myname"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.wantID = sb.GetId() // sbx only answers Start to the id, not "myname"
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}

	// Restart: Start("myname") fails, then Start(<id>) succeeds → RUNNING.
	out, err := m.Restart(ctx, sb.GetId(), nil)
	if err != nil {
		t.Fatalf("restart should succeed via the id fallback: %v", err)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want running", out.GetState())
	}
}

// noStartRunner has no working `start` (like an sbx that only does create/run/rm),
// so restart must relaunch from the retained copy.
type noStartRunner struct{ *fakeRunner }

func (noStartRunner) Start(context.Context, string) error {
	return errors.New("unknown command: start")
}

func TestRestartRecreatesWhenStartFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := registry.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	fr := newFakeRunner()
	m := NewManager(reg, noStartRunner{fr}, filepath.Join(dir, "ws"), "host-1")

	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "myname"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}

	out, err := m.Restart(ctx, sb.GetId(), nil)
	if err != nil {
		t.Fatalf("restart should relaunch from the copy when start fails: %v", err)
	}
	if out.GetState() != pb.SandboxState_SANDBOX_STATE_RUNNING {
		t.Errorf("state = %v, want running", out.GetState())
	}
	if fr.launches != 2 {
		t.Errorf("expected a relaunch (2 launches total), got %d", fr.launches)
	}
}

func TestSbxHandle(t *testing.T) {
	if h := sbxHandle(&pb.Sandbox{Id: "uuid-1", DisplayName: "backend"}); h != "backend" {
		t.Errorf("handle = %q, want the name", h)
	}
	// Falls back to the uuid when a record has no name.
	if h := sbxHandle(&pb.Sandbox{Id: "uuid-1"}); h != "uuid-1" {
		t.Errorf("handle = %q, want the id fallback", h)
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"", "has space", "-leading", ".hidden", "weird?char", "a/b", strings.Repeat("a", 65)} {
		if err := validName(n); err == nil {
			t.Errorf("validName(%q) = nil, want error", n)
		}
	}
	for _, n := range []string{"a", "alpha", "my_box-1.2", "sandbox-abcd1234"} {
		if err := validName(n); err != nil {
			t.Errorf("validName(%q) = %v, want nil", n, err)
		}
	}
}

func TestResolveNameFallbacks(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	const id = "abcd1234-0000-0000-0000-000000000000"

	// No label, no explicit name -> short-id default.
	if n, err := m.resolveName("", "", id); err != nil || n != "sandbox-abcd1234" {
		t.Errorf("empty -> %q, %v; want sandbox-abcd1234", n, err)
	}
	// A label that isn't filesystem-safe falls back to the default.
	if n, err := m.resolveName("", "has spaces", id); err != nil || n != "sandbox-abcd1234" {
		t.Errorf("bad label -> %q, %v; want fallback", n, err)
	}
	// A valid free label is used verbatim.
	if n, err := m.resolveName("", "backend", id); err != nil || n != "backend" {
		t.Errorf("good label -> %q, %v; want backend", n, err)
	}
	// An explicit invalid name is an error.
	if _, err := m.resolveName("bad name", "", id); err == nil {
		t.Error("explicit invalid name should error")
	}
}

func TestRenameToTakenName(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	a, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "alpha"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{}, Sources: []*pb.SourceRef{src}, DisplayName: "beta"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, b.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rename(ctx, b.GetId(), "alpha"); err == nil {
		t.Error("renaming to an existing name on the same host should fail")
	}
	_ = a
}

func TestRenameNoOpAndMissingArtifacts(t *testing.T) {
	ctx := context.Background()
	m, _, _, dir := newTestManager(t)
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(ctx, LaunchRequest{Config: &pb.ConfigSnapshot{Name: "orig"}, Sources: []*pb.SourceRef{src}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop(ctx, sb.GetId()); err != nil {
		t.Fatal(err)
	}

	// Renaming to the same name is a no-op (no error, no work).
	if _, err := m.Rename(ctx, sb.GetId(), "orig"); err != nil {
		t.Fatalf("no-op rename: %v", err)
	}

	// With no live container and a missing workspace dir, rename still succeeds,
	// skipping the container-destroy and the filesystem move.
	gone := filepath.Join(dir, "workspaces", "gone")
	if _, err := m.store.Update(sb.GetId(), func(s *pb.Sandbox) error {
		s.ContainerRef = ""
		s.WorkspacePath = gone
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Rename(ctx, sb.GetId(), "brandnew")
	if err != nil {
		t.Fatalf("rename with missing artifacts: %v", err)
	}
	if out.GetDisplayName() != "brandnew" {
		t.Errorf("display name = %q, want brandnew", out.GetDisplayName())
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("no dir should have been created for a missing source: %v", err)
	}
}
