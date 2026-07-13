package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/jamesclark123/switchboard/libs/switchboard-proto/gen"
	"github.com/jamesclark123/switchboard/services/switchboardd/internal/duplicate"
)

func launchForTag(t *testing.T, m *Manager, dir string) *pb.Sandbox {
	t.Helper()
	src := makeSource(t, dir, "proj")
	sb, err := m.Launch(context.Background(), LaunchRequest{
		Config:  &pb.ConfigSnapshot{Name: "feature-work"},
		Sources: []*pb.SourceRef{src},
	}, func(duplicate.Progress) {}, nil)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	return sb
}

func TestSetTagSetsTrimsClearsAndPersists(t *testing.T) {
	m, reg, _, dir := newTestManager(t)
	sb := launchForTag(t, m, dir)

	var emitted *pb.Sandbox
	m.SetOnChange(func(s *pb.Sandbox) { emitted = s })

	// Set (with surrounding whitespace that must be trimmed).
	out, err := m.SetTag(sb.GetId(), "  auth-refactor  ")
	if err != nil {
		t.Fatal(err)
	}
	if out.GetTag() != "auth-refactor" {
		t.Fatalf("tag = %q, want trimmed", out.GetTag())
	}
	if emitted == nil || emitted.GetTag() != "auth-refactor" {
		t.Fatal("SetTag must emit the change")
	}
	// No other field changed.
	if out.GetId() != sb.GetId() || out.GetDisplayName() != sb.GetDisplayName() || out.GetState() != sb.GetState() {
		t.Fatal("SetTag must not change id/name/state")
	}

	// Persistence via the registry.
	reloaded, err := reg.Get(sb.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.GetTag() != "auth-refactor" {
		t.Fatalf("persisted tag = %q", reloaded.GetTag())
	}

	// Over-long tags are capped at 64 chars.
	long := strings.Repeat("x", 100)
	capped, err := m.SetTag(sb.GetId(), long)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.GetTag()) != maxTagLen {
		t.Fatalf("tag length = %d, want %d", len(capped.GetTag()), maxTagLen)
	}

	// Empty clears.
	cleared, err := m.SetTag(sb.GetId(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.GetTag() != "" {
		t.Fatalf("tag after clear = %q", cleared.GetTag())
	}
}

func TestWorkspaceMarkerWritten(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	sb := launchForTag(t, m, dir)
	if sb.GetWorkspacePath() == "" {
		t.Skip("no workspace path produced")
	}
	marker := filepath.Join(sb.GetWorkspacePath(), workspaceMarkerDir, workspaceMarkerFile)
	blob, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if got["sandbox_id"] != sb.GetId() || got["host_id"] != "host-1" {
		t.Fatalf("marker = %v, want this sandbox+host", got)
	}
}

func TestManagerGet(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	sb := launchForTag(t, m, dir)
	got, err := m.Get(sb.GetId())
	if err != nil || got.GetId() != sb.GetId() {
		t.Fatalf("Get = (%v, %v), want the launched sandbox", got.GetId(), err)
	}
}

func TestWriteWorkspaceMarkerEmptyPath(t *testing.T) {
	// An empty workspace path is a no-op, not an error.
	if err := writeWorkspaceMarker("", "h", "id"); err != nil {
		t.Fatalf("empty path should be a no-op: %v", err)
	}
}

func TestResolveWorkspaceSkipsEmptyWorkspace(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	// A record with no workspace path must be skipped, not matched.
	if err := m.store.Put(&pb.Sandbox{Id: "nowp", State: pb.SandboxState_SANDBOX_STATE_RUNNING}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := m.ResolveWorkspace(t.TempDir()); err != nil || ok {
		t.Fatalf("resolve = (%v,%v), want not found", ok, err)
	}
}

func TestSetTagUnknownSandbox(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	if _, err := m.SetTag("does-not-exist", "x"); err == nil {
		t.Fatal("expected error for unknown sandbox")
	}
}

func TestResolveWorkspaceExactAndNested(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	sb := launchForTag(t, m, dir)
	wp := sb.GetWorkspacePath()
	if wp == "" {
		t.Skip("no workspace path produced")
	}

	// Exact path.
	got, ok, err := m.ResolveWorkspace(wp)
	if err != nil || !ok || got.GetId() != sb.GetId() {
		t.Fatalf("resolve exact = (%v,%v,%v)", got.GetId(), ok, err)
	}

	// Nested subdirectory resolves to the same sandbox (FR-018).
	nested := filepath.Join(wp, "proj", "deeper")
	got2, ok2, err := m.ResolveWorkspace(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !ok2 || got2.GetId() != sb.GetId() {
		t.Fatalf("resolve nested = (%v,%v)", got2.GetId(), ok2)
	}

	// A path outside any workspace does not resolve.
	if _, ok3, err := m.ResolveWorkspace(t.TempDir()); err != nil || ok3 {
		t.Fatalf("resolve outside = (%v,%v), want not found", ok3, err)
	}
}

func TestResolveWorkspaceDeepestWins(t *testing.T) {
	m, _, _, dir := newTestManager(t)
	sb := launchForTag(t, m, dir)
	if sb.GetWorkspacePath() == "" {
		t.Skip("no workspace path produced")
	}
	// Resolving the workspace root's parent (the shared ws root) must not match
	// any single sandbox's copy.
	parent := filepath.Dir(sb.GetWorkspacePath())
	if _, ok, err := m.ResolveWorkspace(parent); err != nil || ok {
		t.Fatalf("resolve ws-root parent = (%v,%v), want not found", ok, err)
	}
}
